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
 *   - `holders/<pid>.json`: the platform run ids each live window holds
 *     (every run it answers a verb for: its slots, the dispatches on their
 *     way to a slot, the triggers it is queueing, the runs its queue
 *     carries, and the paused runs a reload ended there), rewritten whenever
 *     that set changes. It counts while its process lives.
 *   - `holders/<pid>.closed.json`: the runs a window held when it closed or
 *     reloaded, which the others still honour for CLOSED_LISTING_GRACE_MS: a
 *     reloading window holds its queued runs again once it is back, and must
 *     not have them refused meanwhile. A file of its own, so a listing write
 *     still in flight when the window closes cannot replace it. Any file
 *     that stopped counting is removed.
 *   - `answers/<command id>`: created exclusively by the first window that
 *     answers a command, naming that window, and marked answered once its
 *     acknowledgement reached the platform. The holder creates it before it
 *     applies the verb; a window that does not hold the run refuses the verb
 *     only when no window lists the run and it creates this file first, so
 *     the platform gets one acknowledgement per command, and never a refusal
 *     ahead of the holder's answer. A claim whose window is gone before it
 *     answered is taken over by the first window to create
 *     `answers/<command id>.taken-from-<pid>`; an answered claim never is.
 *     Files older than a day are swept.
 *   - `claims/<run id>.json`: the window that holds a paused run a reload
 *     ended (#2339). Every window of one clone reads the same paused
 *     snapshots, so the first live window to claim the run holds it, and the
 *     others leave it; a claim whose window is gone is taken over.
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

/**
 * How long a closed window's listing still counts (#2357): long enough for a
 * reloading window to activate and list its runs again.
 */
export const CLOSED_LISTING_GRACE_MS = 60_000;

/** A claim lock older than this was left by a window that died holding it. */
const CLAIM_LOCK_STALE_MS = 10_000;

/** A command or run id as a file name: the platform's ids are UUIDs, anything else is hashed out. */
function answerFileName(commandId: string): string {
  return /^[A-Za-z0-9._-]{1,128}$/.test(commandId) && !/^\.+$/.test(commandId)
    ? commandId
    : `cmd-${Buffer.from(commandId).toString("base64url").slice(0, 120)}`;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
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
  private readonly claimsDir: string;
  /** The set to write next, and whether a write is in flight. */
  private pending: string[] | null = null;
  private writing: Promise<void> | null = null;
  /** The latest set asked for, which a closing window marks closed. */
  private latest: string[] = [];
  private disposed = false;

  constructor(dir: string, options: RemoteRunLedgerOptions = {}) {
    this.windowId = options.windowId ?? process.pid;
    this.isAlive = options.isAlive ?? isProcessAlive;
    this.now = options.now ?? Date.now;
    this.holdersDir = path.join(dir, "holders");
    this.answersDir = path.join(dir, "answers");
    this.claimsDir = path.join(dir, "claims");
  }

  private get ownFile(): string {
    return path.join(this.holdersDir, `${this.windowId}.json`);
  }

  private get closedFile(): string {
    return path.join(this.holdersDir, `${this.windowId}.closed.json`);
  }

  /**
   * Record the platform run ids this window holds now. Writes are queued one
   * at a time, and only the latest set is written, so an older set never
   * replaces a newer one. Resolves once the set is on disk (or failed).
   */
  publish(runIds: Iterable<string>): Promise<void> {
    if (this.disposed) return Promise.resolve();
    this.pending = [...new Set(runIds)].sort();
    this.latest = this.pending;
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
    const listed = await this.listingsOf(runId);
    return listed.live || listed.closedUntil !== null;
  }

  /**
   * How long, in milliseconds, the run stays held elsewhere only by the
   * listings of windows that closed or are reloading (#2357); null when a
   * live window lists it, or none does. A window back from a reload lists
   * the runs it still holds within that time; a run no window lists again
   * by then is held by nobody, and a verb for it can be refused.
   */
  async closedHoldLeftMs(runId: string): Promise<number | null> {
    const listed = await this.listingsOf(runId);
    if (listed.live || listed.closedUntil === null) return null;
    return Math.max(0, listed.closedUntil - this.now());
  }

  /**
   * The other windows' listings of the run: whether a live one lists it (or
   * one cannot be read), and else when the last closed listing of it stops
   * counting. Expired and dead listings are removed on the way.
   */
  private async listingsOf(runId: string): Promise<{ live: boolean; closedUntil: number | null }> {
    let names: string[];
    try {
      names = await fs.promises.readdir(this.holdersDir);
    } catch (err) {
      return { live: (err as NodeJS.ErrnoException).code !== "ENOENT", closedUntil: null };
    }
    let closedUntil: number | null = null;
    for (const name of names) {
      const match = /^(\d+)(?:\.closed)?\.json$/.exec(name);
      if (!match) continue;
      const pid = Number(match[1]);
      if (pid === this.windowId) continue;
      const file = path.join(this.holdersDir, name);
      let held: { runIds?: unknown; closedAt?: unknown };
      try {
        const parsed: unknown = JSON.parse(await fs.promises.readFile(file, "utf8"));
        if (typeof parsed !== "object" || parsed === null) throw new Error("not a listing");
        held = parsed as typeof held;
      } catch (err) {
        // Gone between the listing and the read: that window holds nothing.
        if ((err as NodeJS.ErrnoException).code === "ENOENT") continue;
        if (!this.isAlive(pid)) {
          await fs.promises.rm(file, { force: true }).catch(() => {});
          continue;
        }
        return { live: true, closedUntil: null };
      }
      // A window that closed or reloaded: its listing counts for a while,
      // whether or not its process has exited yet. Any other listing counts
      // while its process lives.
      const closedAt = typeof held.closedAt === "number" ? held.closedAt : null;
      const expired =
        closedAt !== null ? this.now() - closedAt >= CLOSED_LISTING_GRACE_MS : !this.isAlive(pid);
      if (expired) {
        await fs.promises.rm(file, { force: true }).catch(() => {});
        continue;
      }
      if (!Array.isArray(held.runIds) || !held.runIds.includes(runId)) continue;
      if (closedAt === null) return { live: true, closedUntil: null };
      closedUntil = Math.max(closedUntil ?? 0, closedAt + CLOSED_LISTING_GRACE_MS);
    }
    return { live: false, closedUntil };
  }

  /**
   * Claim the hold of a paused run a window reload ended (#2339), for this
   * window alone. True when this window holds the claim now: it was free,
   * already this window's, or left by a window that is gone. False when
   * another live window holds it, or when the claim cannot be recorded.
   */
  async claimRun(runId: string): Promise<boolean> {
    const file = path.join(this.claimsDir, `${answerFileName(runId)}.json`);
    const claimed = await this.underClaimLock(file, async () => {
      const owner = await this.claimOwner(file);
      if (owner !== null && owner !== this.windowId && this.isAlive(owner)) return false;
      const tmp = `${file}.${process.pid}.tmp`;
      await fs.promises.writeFile(tmp, JSON.stringify({ pid: this.windowId }));
      await fs.promises.rename(tmp, file);
      return true;
    });
    return claimed ?? false;
  }

  /** Give up this window's claim on a run (#2339); another window's claim is left alone. */
  async releaseRun(runId: string): Promise<void> {
    const file = path.join(this.claimsDir, `${answerFileName(runId)}.json`);
    await this.underClaimLock(file, async () => {
      if ((await this.claimOwner(file)) === this.windowId) {
        await fs.promises.rm(file, { force: true });
      }
      return true;
    });
  }

  /** The window a claim file names, or null when there is none or it is unreadable. */
  private async claimOwner(file: string): Promise<number | null> {
    return (await this.readClaim(file))?.pid ?? null;
  }

  /** A claim file's window and whether its answer was delivered; null when unreadable. */
  private async readClaim(file: string): Promise<{ pid: number; answered: boolean } | null> {
    try {
      const claim = JSON.parse(await fs.promises.readFile(file, "utf8")) as {
        pid?: unknown;
        answered?: unknown;
      };
      return typeof claim.pid === "number"
        ? { pid: claim.pid, answered: claim.answered === true }
        : null;
    } catch {
      return null;
    }
  }

  /**
   * Run `fn` holding the claim's lock, a directory only one window can
   * create, so reading a claim and taking it over is one step. Undefined
   * when the lock or `fn` fails: nothing is claimed then.
   */
  private async underClaimLock<T>(file: string, fn: () => Promise<T>): Promise<T | undefined> {
    const lock = `${file}.lock`;
    try {
      await fs.promises.mkdir(this.claimsDir, { recursive: true });
    } catch {
      return undefined;
    }
    for (let attempt = 0; attempt < 100; attempt++) {
      try {
        await fs.promises.mkdir(lock);
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code !== "EEXIST") return undefined;
        const stat = await fs.promises.stat(lock).catch(() => null);
        if (stat && this.now() - stat.mtimeMs > CLAIM_LOCK_STALE_MS) {
          await fs.promises.rm(lock, { recursive: true, force: true }).catch(() => {});
        } else {
          await sleep(10);
        }
        continue;
      }
      try {
        return await fn();
      } catch {
        return undefined;
      } finally {
        await fs.promises.rm(lock, { recursive: true, force: true }).catch(() => {});
      }
    }
    return undefined;
  }

  /**
   * Claim the one answer this machine sends for a command. True for the
   * first caller, and for the first window to take over the claim of a
   * window that is gone: it claimed the answer, then closed or reloaded
   * before it answered, and nobody else would answer then. False when a
   * live window claimed it, or when the claim cannot be recorded.
   */
  async claimAnswer(commandId: string): Promise<boolean> {
    const file = path.join(this.answersDir, answerFileName(commandId));
    let handle: fs.promises.FileHandle;
    try {
      await fs.promises.mkdir(this.answersDir, { recursive: true });
      handle = await fs.promises.open(file, "wx");
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code !== "EEXIST") return false;
      if (!(await this.takeOverAnswer(file))) return false;
      void this.sweepAnswers();
      return true;
    }
    try {
      // The claimer, so another window can tell when it is gone.
      await handle.writeFile(JSON.stringify({ pid: this.windowId }));
    } catch {
      // The claim stands; only a takeover of it is ruled out.
    } finally {
      await handle.close().catch(() => {});
    }
    void this.sweepAnswers();
    return true;
  }

  /**
   * Record that this window's answer to a command reached the platform, so
   * the claim is never taken over once this window is gone: a copy of the
   * command delivered again later gets no second, contrary acknowledgement.
   * Only this window's own claim is marked. Best effort: an unmarked claim is
   * taken over only once its window is gone.
   */
  async markAnswered(commandId: string): Promise<void> {
    const file = path.join(this.answersDir, answerFileName(commandId));
    try {
      if ((await this.claimOwner(file)) !== this.windowId) return;
      const tmp = `${file}.${process.pid}.answered.tmp`;
      await fs.promises.writeFile(tmp, JSON.stringify({ pid: this.windowId, answered: true }));
      await fs.promises.rename(tmp, file);
    } catch {
      // Best effort, as above.
    }
  }

  /**
   * Take over an answer whose claimer is gone before it answered (#2357).
   * The first window to create the takeover marker for that claimer wins
   * it. A claim that names no claimer (one being written, or an unreadable
   * one) is kept, as is a live window's and one already answered.
   */
  private async takeOverAnswer(file: string): Promise<boolean> {
    const claim = await this.readClaim(file);
    if (claim === null || claim.answered) return false;
    const owner = claim.pid;
    if (owner === this.windowId || this.isAlive(owner)) return false;
    try {
      const marker = await fs.promises.open(`${file}.taken-from-${owner}`, "wx");
      await marker.close();
      const tmp = `${file}.${process.pid}.tmp`;
      await fs.promises.writeFile(tmp, JSON.stringify({ pid: this.windowId }));
      await fs.promises.rename(tmp, file);
      return true;
    } catch {
      return false;
    }
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

  /**
   * The window closes or reloads: the runs it holds are listed as closed, so
   * the other windows honour them for CLOSED_LISTING_GRACE_MS more, and a
   * reloading window's queued runs are not refused before it lists them
   * again. The closed listing is a file of its own: a write of the live
   * listing still in flight lands on the live file, which stops counting
   * once this process is gone.
   */
  dispose(): void {
    this.disposed = true;
    this.pending = null;
    try {
      if (this.latest.length > 0) {
        fs.mkdirSync(this.holdersDir, { recursive: true });
        const tmp = `${this.closedFile}.${process.pid}.tmp`;
        fs.writeFileSync(
          tmp,
          JSON.stringify({ pid: this.windowId, runIds: this.latest, closedAt: this.now() })
        );
        fs.renameSync(tmp, this.closedFile);
      }
      fs.rmSync(this.ownFile, { force: true });
    } catch {
      // Best effort: a dead window's file is ignored and removed by the others.
    }
  }
}
