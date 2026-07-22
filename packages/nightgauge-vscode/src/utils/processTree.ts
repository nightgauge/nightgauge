/**
 * Recursive process-tree termination.
 *
 * When the pipeline kills a stalled stage, it sends SIGTERM/SIGKILL to the
 * Claude CLI process. Claude's own children — the Bash subprocesses it spawned
 * (vitest, npm, build commands) — are NOT in the same process group and
 * survive as orphans. A hung test runner (e.g. a vitest with a leaked Redis
 * subscriber) keeps consuming resources and can re-trigger the same hang on
 * retry. See issue #781 retro for the failure mode.
 *
 * This helper walks the process tree via `pgrep -P` (POSIX) and SIGKILLs every
 * descendant before the parent dies, guaranteeing orphan-free shutdown.
 */

import { execFile } from "child_process";
import { promisify } from "util";

const execFileAsync = promisify(execFile);

type LogFn = (line: string) => void;

async function getChildPids(parentPid: number): Promise<number[]> {
  try {
    const { stdout } = await execFileAsync("pgrep", ["-P", String(parentPid)], {
      timeout: 2000,
    });
    return stdout
      .split("\n")
      .map((s) => Number(s.trim()))
      .filter((n) => Number.isFinite(n) && n > 0);
  } catch {
    // Non-zero exit = no matches; missing pgrep on Windows handled by caller.
    return [];
  }
}

async function collectDescendants(rootPid: number, acc: Set<number>): Promise<void> {
  if (acc.has(rootPid)) return;
  acc.add(rootPid);
  const children = await getChildPids(rootPid);
  for (const child of children) {
    await collectDescendants(child, acc);
  }
}

function killOne(pid: number, signal: NodeJS.Signals): boolean {
  try {
    process.kill(pid, signal);
    return true;
  } catch {
    return false;
  }
}

/**
 * Recursively SIGKILLs all descendants of `rootPid`, then the root itself.
 * Returns the list of pids that received the signal so callers can log.
 *
 * Best-effort: missing pgrep, races, and already-dead processes are silently
 * tolerated — the goal is "no orphans survive" not "atomic kill."
 */
export async function killProcessTree(
  rootPid: number,
  signal: NodeJS.Signals = "SIGKILL",
  log?: LogFn
): Promise<number[]> {
  const all = new Set<number>();
  await collectDescendants(rootPid, all);

  // Kill leaves first so a freshly-orphaned parent can't fork another child.
  const ordered = Array.from(all).sort((a, b) => b - a);
  const killed: number[] = [];
  for (const pid of ordered) {
    if (pid === process.pid) continue; // never kill self
    if (killOne(pid, signal)) {
      killed.push(pid);
    }
  }
  if (killed.length > 0) {
    log?.(`[processTree] ${signal} sent to ${killed.length} pid(s): ${killed.join(",")}`);
  }
  return killed;
}

/**
 * Tracks descendants of a long-running parent process by periodically walking
 * `pgrep -P`. Lets us reap children that get reparented to init when the
 * parent exits — by then `pgrep -P parent` returns nothing, so we have to
 * remember the pids while the parent is still alive.
 *
 * Used by skillRunner: a hung vitest spawned by Claude becomes an orphan
 * the moment Claude dies, and its parent re-points to PID 1. Tracking pids
 * during the run and killing the survivors on close closes that gap.
 */
export class DescendantTracker {
  private readonly tracked = new Set<number>();
  private timer: ReturnType<typeof setInterval> | null = null;

  start(rootPid: number, intervalMs = 15_000): void {
    const collect = (): void => {
      const all = new Set<number>();
      collectDescendants(rootPid, all)
        .then(() => {
          for (const pid of all) {
            if (pid !== rootPid) this.tracked.add(pid);
          }
        })
        .catch(() => {
          /* best-effort */
        });
    };
    collect();
    this.timer = setInterval(collect, intervalMs);
    if (typeof this.timer.unref === "function") this.timer.unref();
  }

  stop(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  /**
   * SIGKILLs any tracked pid still alive. Returns the killed list. The probe
   * uses signal 0 — throws if dead, letting us silently skip already-departed
   * pids instead of spamming logs with every npm/node intermediate.
   */
  killSurvivors(log?: LogFn): number[] {
    this.stop();
    const killed: number[] = [];
    for (const pid of this.tracked) {
      try {
        process.kill(pid, 0);
      } catch {
        continue;
      }
      if (killOne(pid, "SIGKILL")) killed.push(pid);
    }
    if (killed.length > 0) {
      log?.(
        `[processTree] Reaped ${killed.length} orphaned child(ren) after parent exit: ${killed.join(",")}`
      );
    }
    this.tracked.clear();
    return killed;
  }
}
