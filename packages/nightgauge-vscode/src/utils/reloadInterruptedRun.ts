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
