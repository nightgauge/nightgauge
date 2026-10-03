/**
 * pausedRunRestore — what a window does with the paused runs it finds when it
 * activates (#2008, #2339).
 *
 * Each paused snapshot (`runtime-*.json` with `paused: true`) is offered to
 * the operator. Resume does not continue the paused run, which ended with the
 * window that ran it: it starts a NEW run of the issue, and consumes the
 * snapshot the prompt was built from, so the prompt does not come back on the
 * next activation with another full run behind it.
 *
 * A snapshot that names a platform run a window reload ended is held for the
 * platform's verbs first (ReloadInterruptedRunHolds), and every such run is
 * held before any prompt is shown: a prompt waits for the operator, and a run
 * not held meanwhile would have a platform resume or cancel refused
 * `no-active-run`.
 *
 * A Resume consumes the snapshot before it starts the new run, and starts
 * nothing when the snapshot is gone already: a platform cancel ended the run
 * while the prompt was on screen (the cancel consumes the snapshot), or
 * another window of the clone resumed it first. Consuming renames the
 * snapshot away before removing it, because a rename succeeds for one caller
 * only, and an unlink does not: two concurrent unlinks of one file can both
 * succeed (APFS does this). So at most one new run starts from one snapshot,
 * and a platform cancel and a Resume never both take it. ADR-017 step 8 (the
 * consume-on-claim rename of the snapshot) is meant to build on this with a
 * claim that also lets a platform resume continue the run.
 */

import { randomUUID } from "node:crypto";
import * as fs from "node:fs";
import type { Logger } from "../utils/logger";
import type { ReloadInterruptedRunHolds } from "../utils/reloadInterruptedRun";

/** A paused snapshot the activation scan found. */
export interface PausedSnapshot {
  filePath: string;
  issueNumber: number;
  /** The platform run a reload ended, when the snapshot is one (reloadInterruptedRemoteRun). */
  interrupted: { remoteRunId: string; issueNumber: number } | null;
}

export interface PausedRunRestoreDeps {
  holds: Pick<ReloadInterruptedRunHolds, "found" | "resumed">;
  /** Ask whether to resume the issue's paused run; "Resume" resumes it. */
  ask(issueNumber: number): PromiseLike<string | undefined>;
  /** Start the new run a Resume asks for. */
  resume(issueNumber: number): Promise<void>;
  /** Tell the operator a Resume found the paused run ended or resumed elsewhere. */
  gone(issueNumber: number): void;
  logger: Pick<Logger, "info" | "warn">;
  /** Consumes a snapshot: consumePausedSnapshot, but in tests. */
  consume?: (file: string) => Promise<boolean>;
}

/**
 * Take a paused snapshot away for good: true when this caller took it, false
 * when it was gone already. It is renamed to a name only this caller uses,
 * which one caller wins, then removed. Any other failure is thrown, and the
 * snapshot stays.
 */
export async function consumePausedSnapshot(file: string): Promise<boolean> {
  const taken = `${file}.${randomUUID()}.consumed`;
  try {
    await fs.promises.rename(file, taken);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return false;
    throw err;
  }
  await fs.promises.unlink(taken).catch(() => {});
  return true;
}

/** Hold every run a reload ended, then offer each paused run to the operator in turn. */
export async function restorePausedRuns(
  paused: PausedSnapshot[],
  deps: PausedRunRestoreDeps
): Promise<void> {
  const consume = deps.consume ?? consumePausedSnapshot;
  for (const snapshot of paused) {
    if (!snapshot.interrupted) continue;
    try {
      // A platform cancel ends the run by consuming its snapshot; one a
      // Resume consumed first is ended already.
      await deps.holds.found(snapshot.interrupted, async () => {
        await consume(snapshot.filePath);
      });
    } catch (err) {
      deps.logger.warn("Could not hold a paused run a window reload ended", {
        issueNumber: snapshot.issueNumber,
        remoteRunId: snapshot.interrupted.remoteRunId,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
  for (const snapshot of paused) {
    try {
      await offer(snapshot, deps, consume);
    } catch (err) {
      deps.logger.warn("Could not resume a paused run", {
        issueNumber: snapshot.issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
}

async function offer(
  snapshot: PausedSnapshot,
  deps: PausedRunRestoreDeps,
  consume: (file: string) => Promise<boolean>
): Promise<void> {
  if ((await deps.ask(snapshot.issueNumber)) !== "Resume") return;
  // The new run does not serve the platform run (#2339). The hold is given
  // up first, so no platform cancel consumes the snapshot after this point;
  // one that came earlier did, and the consumption below finds it gone.
  if (snapshot.interrupted) await deps.holds.resumed(snapshot.interrupted.remoteRunId);
  try {
    if (!(await consume(snapshot.filePath))) {
      deps.logger.info("The paused run was ended or resumed elsewhere — not resuming it", {
        issueNumber: snapshot.issueNumber,
        file: snapshot.filePath,
      });
      deps.gone(snapshot.issueNumber);
      return;
    }
  } catch (err) {
    // Best effort: a snapshot that cannot be removed must not stop the
    // resume the operator just asked for.
    deps.logger.warn("Could not remove the paused snapshot after Resume", {
      file: snapshot.filePath,
      issueNumber: snapshot.issueNumber,
      err: err instanceof Error ? err.message : String(err),
    });
  }
  await deps.resume(snapshot.issueNumber);
}
