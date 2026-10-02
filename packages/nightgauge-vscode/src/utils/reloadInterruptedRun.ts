/**
 * Which paused snapshot is a platform run a window reload ended (#2339).
 *
 * A platform pause holds a run in the window that executes it: the slot's
 * runPipeline() call stays in flight at the stage boundary. A window reload
 * ends that call with the extension host, and the daemon that owned the run's
 * runtime with it; the paused snapshot is all that is left. When the snapshot
 * names the platform run id (`remoteRunId`) and its owning process is gone,
 * the window that finds it holds the run for the platform's verbs: it refuses
 * a resume with the reason, since only its Resume prompt can continue the run
 * (as a new run), until ADR-017's consume-on-claim step lets a platform resume
 * continue it. A snapshot whose owner is alive belongs to another window's
 * live run, which answers for itself.
 */

import { isProcessAlive } from "./processAlive";

/** The runtime snapshot fields the decision reads. */
export interface PausedSnapshotFields {
  paused?: boolean;
  issueNumber?: number;
  remoteRunId?: unknown;
  ownerPid?: unknown;
}

/** The platform run a reload ended, or null when the snapshot is not one. */
export function reloadInterruptedRemoteRun(
  runtime: PausedSnapshotFields,
  isAlive: (pid: number) => boolean = isProcessAlive
): { remoteRunId: string; issueNumber: number } | null {
  if (runtime.paused !== true) return null;
  if (typeof runtime.remoteRunId !== "string" || runtime.remoteRunId === "") return null;
  if (typeof runtime.issueNumber !== "number") return null;
  // 0 records no owner, which refuses nothing.
  const owner = typeof runtime.ownerPid === "number" ? runtime.ownerPid : 0;
  if (owner > 0 && isAlive(owner)) return null;
  return { remoteRunId: runtime.remoteRunId, issueNumber: runtime.issueNumber };
}

/** What a hold needs from the pipeline manager. */
export interface ReloadInterruptedRunTarget {
  holdReloadInterruptedRun(
    remoteRunId: string,
    issueNumber: number,
    end?: () => Promise<void>
  ): void;
  dropReloadInterruptedRun(remoteRunId: string): void;
}

/** The machine's exclusive claims on such runs (RemoteRunLedger). */
export interface ReloadInterruptedRunClaims {
  claimRun(runId: string): Promise<boolean>;
  releaseRun(runId: string): Promise<void>;
}

/**
 * The paused runs a window reload ended that this window holds for the
 * platform's verbs (#2339).
 *
 * Every window on a worktree of one clone scans the same paused snapshots, so
 * a run is held only by the first live window to claim it in the machine's
 * ledger; otherwise each would answer every verb for it. The activation scan
 * can find a run before the pipeline manager exists, so its hold waits here
 * until the manager is attached.
 */
export class ReloadInterruptedRunHolds {
  private target: ReloadInterruptedRunTarget | null = null;
  private readonly waiting = new Map<string, { issueNumber: number; end: () => Promise<void> }>();

  /** `claims` is absent without a machine-state directory: every finder holds then. */
  constructor(private readonly claims?: ReloadInterruptedRunClaims) {}

  /**
   * The scan found a paused run a reload ended. This window holds it when it
   * claims the run; `consume` removes the run's paused snapshot when the
   * platform cancels it, which ends the run and gives the claim up. Resolves
   * whether this window holds it.
   */
  async found(
    run: { remoteRunId: string; issueNumber: number },
    consume: () => Promise<void>
  ): Promise<boolean> {
    if (this.claims && !(await this.claims.claimRun(run.remoteRunId))) return false;
    const end = async (): Promise<void> => {
      await consume();
      await this.claims?.releaseRun(run.remoteRunId);
    };
    if (this.target) {
      this.target.holdReloadInterruptedRun(run.remoteRunId, run.issueNumber, end);
    } else {
      this.waiting.set(run.remoteRunId, { issueNumber: run.issueNumber, end });
    }
    return true;
  }

  /** The pipeline manager exists now: it holds the runs from here on. */
  attach(target: ReloadInterruptedRunTarget): void {
    this.target = target;
    for (const [remoteRunId, { issueNumber, end }] of this.waiting) {
      target.holdReloadInterruptedRun(remoteRunId, issueNumber, end);
    }
    this.waiting.clear();
  }

  /**
   * The window's Resume started a new run from the snapshot, which does not
   * serve the platform run: this window no longer holds it.
   */
  async resumed(remoteRunId: string): Promise<void> {
    this.waiting.delete(remoteRunId);
    this.target?.dropReloadInterruptedRun(remoteRunId);
    await this.claims?.releaseRun(remoteRunId);
  }
}
