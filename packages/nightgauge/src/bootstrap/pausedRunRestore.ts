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
 *
 * Every repository of the window is scanned, not only the primary one: a
 * platform run of a linked repository pauses in that repository's clone, and
 * a reload ends it just the same. A run of another repository is resumed
 * through the queue, routed to its repository, since the single-run path only
 * runs the primary repository.
 */

import { randomUUID } from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";
import type { Logger } from "../utils/logger";
import {
  type ReloadInterruptedRunHolds,
  reloadInterruptedRemoteRun,
} from "../utils/reloadInterruptedRun";
import {
  ANY_RUNTIME_FILE,
  classifyRuntimeStub,
  runtimeSweepVerdict,
} from "../utils/runtimeStubSweep";

/** A repository as the scan routes a Resume to it. */
export interface PausedRunRepo {
  owner: string;
  repo: string;
}

/** A paused snapshot the activation scan found. */
export interface PausedSnapshot {
  filePath: string;
  issueNumber: number;
  /** The platform run a reload ended, when the snapshot is one (reloadInterruptedRemoteRun). */
  interrupted: { remoteRunId: string; issueNumber: number } | null;
  /**
   * The repository the run belongs to when it is not the window's primary
   * one; absent for the primary repository. `null`: another repository whose
   * identity is unknown, so no Resume can be routed to it, and only the hold
   * applies.
   */
  repo?: PausedRunRepo | null;
  /**
   * "private" when the paused run was private on the hosted service (#2400):
   * the run a Resume starts keeps it.
   */
  visibility?: "private";
}

/** One clone's pipeline directory to scan. */
export interface PausedScanTarget {
  pipelineDir: string;
  /** "owner/repo" (or a short name) for the stub sweep's repo-mismatch check. */
  containingRepoSlug?: string;
  /** Absent for the primary repository; see PausedSnapshot.repo. */
  repo?: PausedRunRepo | null;
}

/**
 * The paused snapshots in one clone's pipeline directory. Stale or
 * cross-contaminated stubs are swept on the way (#307, utils/runtimeStubSweep),
 * and a malformed file is skipped.
 */
export async function scanPausedSnapshots(
  target: PausedScanTarget,
  logger: Pick<Logger, "info" | "warn">,
  isAlive?: (pid: number) => boolean
): Promise<PausedSnapshot[]> {
  const files = await fs.promises.readdir(target.pipelineDir).catch(() => [] as string[]);
  const paused: PausedSnapshot[] = [];
  // TWO name patterns, deliberately, and they are NOT interchangeable
  // (ADR-017 #370 step 1): the sweep may only DELETE legacy names, while the
  // pause-restore prompt READS both. Both patterns and the gating between
  // them live in utils/runtimeStubSweep, where `runtimeSweepVerdict` is
  // unit-tested — this branch guards an `fs.unlink`, and an inline regex here
  // could be widened back to one with the whole suite green.
  for (const file of files.filter((f) => ANY_RUNTIME_FILE.test(f))) {
    const filePath = path.join(target.pipelineDir, file);
    try {
      const runtime = JSON.parse(await fs.promises.readFile(filePath, "utf-8")) as {
        paused?: boolean;
        issueNumber?: number;
        repo?: string | null;
        stage?: string | null;
        remoteRunId?: unknown;
        ownerPid?: unknown;
        visibility?: unknown;
      };
      // The sweep fails SAFE on the new scheme: a run-identity-keyed snapshot
      // is never classified and never deleted here.
      const verdict = runtimeSweepVerdict(file, () =>
        classifyRuntimeStub(runtime, target.containingRepoSlug)
      );
      if (verdict.action === "delete") {
        logger.warn("Sweeping stale/cross-contaminated runtime stub (#307)", {
          file,
          reason: verdict.reason,
          repo: runtime.repo ?? null,
          stage: runtime.stage ?? null,
          issueNumber: runtime.issueNumber,
          containingRepoSlug: target.containingRepoSlug,
        });
        await fs.promises.unlink(filePath).catch(() => {});
        continue;
      }
      if (runtime.paused && typeof runtime.issueNumber === "number") {
        logger.info("Paused pipeline detected on activation", {
          issueNumber: runtime.issueNumber,
          file,
          containingRepoSlug: target.containingRepoSlug,
        });
        paused.push({
          filePath,
          issueNumber: runtime.issueNumber,
          // A paused run that a platform trigger started, whose owning daemon
          // is gone: a reload ended it, and this window holds it for the
          // platform's verbs until its Resume runs (#2339). A live owner is
          // another window's daemon, which answers itself.
          interrupted: isAlive
            ? reloadInterruptedRemoteRun(runtime, isAlive)
            : reloadInterruptedRemoteRun(runtime),
          ...(target.repo === undefined ? {} : { repo: target.repo }),
          ...(runtime.visibility === "private" ? { visibility: "private" as const } : {}),
        });
      }
    } catch {
      // Ignore malformed runtime files
    }
  }
  return paused;
}

export interface PausedRunRestoreDeps {
  holds: Pick<ReloadInterruptedRunHolds, "found" | "resumed">;
  /** Ask whether to resume the issue's paused run; "Resume" resumes it. */
  ask(issueNumber: number): PromiseLike<string | undefined>;
  /**
   * Start the new run a Resume asks for: in the primary repository when
   * `repo` is undefined, else in that repository.
   */
  resume(issueNumber: number, repo?: PausedRunRepo, visibility?: "private"): Promise<void>;
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
  if (snapshot.repo === null) {
    // Another repository whose identity is unknown: a Resume could only start
    // the run in the primary repository, so none is offered; the hold above
    // still answers the platform's verbs.
    deps.logger.info("Not offering Resume for a paused run of an unidentified repository", {
      issueNumber: snapshot.issueNumber,
      file: snapshot.filePath,
    });
    return;
  }
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
  // A private run resumes private (#2400).
  await (snapshot.repo
    ? deps.resume(snapshot.issueNumber, snapshot.repo, snapshot.visibility)
    : deps.resume(snapshot.issueNumber, undefined, snapshot.visibility));
}
