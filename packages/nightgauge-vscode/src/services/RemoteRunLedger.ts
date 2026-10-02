/**
 * RemoteRunLedger — the editor windows of one machine agree on who answers a
 * platform run verb (#2357).
 *
 * Every window of a machine shares one platform agent, so the platform sends
 * a run verb (cancel, pause, resume, approve, reject) to all of them and keeps
 * the first acknowledgement. Only the window that holds the run answers it
 * (#2340): a refusal from another window would reach the platform first,
 * because it needs no work while the holder applies the verb. But a verb for
 * a run no window holds then went unanswered until it expired, five minutes
 * later, where it used to be refused at once.
 *
 * This ledger lets the windows tell the two apart, through files in the
 * machine-state directory (`STATE/agent-commands/`):
 *
 *   - `holders/<pid>.json`: the platform run ids each live window holds (its
 *     slots, the dispatches preparing a slot, and the triggers it accepted),
 *     rewritten whenever that set changes and removed when the window closes.
 *     A file whose process is gone is ignored and removed.
 *   - `answers/<command id>`: created exclusively by the first window that
 *     answers a command. The holder creates it before it applies the verb; a
 *     window that does not hold the run refuses the verb only when no live
 *     window lists the run and it creates this file first, so the platform
 *     gets one acknowledgement per command, and never a refusal ahead of the
 *     holder's answer. Files older than a day are swept.
 *
 * Every failure here fails safe: a ledger that cannot be read counts the run
 * as held elsewhere, and an answer that cannot be claimed is not sent, so the
 * worst outcome is the command expiring, as it did before.
 */

import * as fs from "node:fs";
import * as path from "node:path";
import type * as vscode from "vscode";
import { isProcessAlive } from "../utils/processAlive";

/** The ledger's directory under the machine-state root. */
export const REMOTE_RUN_LEDGER_DIR = "agent-commands";

/** Answer markers older than this are swept; a command expires after five minutes. */
const ANSWER_RETENTION_MS = 24 * 60 * 60 * 1000;

/** A command id as a file name: the platform's ids are UUIDs, anything else is hashed out. */
function answerFileName(commandId: string): string {
  return /^[A-Za-z0-9._-]{1,128}$/.test(commandId) && !/^\.+$/.test(commandId)
    ? commandId
    : `cmd-${Buffer.from(commandId).toString("base64url").slice(0, 120)}`;
}

export interface RemoteRunLedgerOptions {
  /** This window's key: its extension host's process id. */
  windowId?: number;
  /** Whether another window's process is alive. */
  isAlive?: (pid: number) => boolean;
  now?: () => number;
}

export class RemoteRunLedger implements vscode.Disposable {
  private readonly windowId: number;
  private readonly isAlive: (pid: number) => boolean;
  private readonly now: () => number;
  private readonly holdersDir: string;
  private readonly answersDir: string;
  /** The set to write next, and whether a write is in flight. */
  private pending: string[] | null = null;
  private writing: Promise<void> | null = null;
  private disposed = false;

  constructor(dir: string, options: RemoteRunLedgerOptions = {}) {
    this.windowId = options.windowId ?? process.pid;
    this.isAlive = options.isAlive ?? isProcessAlive;
    this.now = options.now ?? Date.now;
    this.holdersDir = path.join(dir, "holders");
    this.answersDir = path.join(dir, "answers");
  }

  private get ownFile(): string {
    return path.join(this.holdersDir, `${this.windowId}.json`);
  }

  /**
   * Record the platform run ids this window holds now. Writes are queued one
   * at a time, and only the latest set is written, so an older set never
   * replaces a newer one. Resolves once the set is on disk (or failed).
   */
  publish(runIds: Iterable<string>): Promise<void> {
    if (this.disposed) return Promise.resolve();
    this.pending = [...new Set(runIds)].sort();
    if (!this.writing) {
      this.writing = this.drain().finally(() => {
        this.writing = null;
      });
    }
    return this.writing;
  }

  private async drain(): Promise<void> {
    while (this.pending !== null && !this.disposed) {
      const runIds = this.pending;
      this.pending = null;
      try {
        if (runIds.length === 0) {
          await fs.promises.rm(this.ownFile, { force: true });
          continue;
        }
        await fs.promises.mkdir(this.holdersDir, { recursive: true });
        const tmp = `${this.ownFile}.${process.pid}.tmp`;
        await fs.promises.writeFile(tmp, JSON.stringify({ pid: this.windowId, runIds }));
        await fs.promises.rename(tmp, this.ownFile);
      } catch {
        // Best effort: an unwritten set only makes other windows answer later.
      }
    }
  }

  /**
   * Whether another live window lists the run. A ledger that cannot be read
   * answers true, so a window never refuses a verb it cannot rule out.
   */
  async heldElsewhere(runId: string): Promise<boolean> {
    let names: string[];
    try {
      names = await fs.promises.readdir(this.holdersDir);
    } catch (err) {
      return (err as NodeJS.ErrnoException).code !== "ENOENT";
    }
    for (const name of names) {
      const match = /^(\d+)\.json$/.exec(name);
      if (!match) continue;
      const pid = Number(match[1]);
      if (pid === this.windowId) continue;
      const file = path.join(this.holdersDir, name);
      if (!this.isAlive(pid)) {
        await fs.promises.rm(file, { force: true }).catch(() => {});
        continue;
      }
      try {
        const held = JSON.parse(await fs.promises.readFile(file, "utf8")) as { runIds?: unknown };
        if (Array.isArray(held.runIds) && held.runIds.includes(runId)) return true;
      } catch (err) {
        // Gone between the listing and the read: that window holds nothing.
        if ((err as NodeJS.ErrnoException).code === "ENOENT") continue;
        return true;
      }
    }
    return false;
  }

  /**
   * Claim the one answer this machine sends for a command. True for the
   * first caller; false when another window claimed it, or when the claim
   * cannot be recorded.
   */
  async claimAnswer(commandId: string): Promise<boolean> {
    try {
      await fs.promises.mkdir(this.answersDir, { recursive: true });
      const handle = await fs.promises.open(
        path.join(this.answersDir, answerFileName(commandId)),
        "wx"
      );
      await handle.close();
    } catch {
      return false;
    }
    void this.sweepAnswers();
    return true;
  }

  private async sweepAnswers(): Promise<void> {
    try {
      const cutoff = this.now() - ANSWER_RETENTION_MS;
      for (const name of await fs.promises.readdir(this.answersDir)) {
        const file = path.join(this.answersDir, name);
        const stat = await fs.promises.stat(file).catch(() => null);
        if (stat && stat.mtimeMs < cutoff) await fs.promises.rm(file, { force: true });
      }
    } catch {
      // Best effort.
    }
  }

  /** The window closes: it holds nothing any more. */
  dispose(): void {
    this.disposed = true;
    this.pending = null;
    try {
      fs.rmSync(this.ownFile, { force: true });
    } catch {
      // Best effort: a dead window's file is ignored and removed by the others.
    }
  }
}
