/**
 * StaleSlotRecoveryService - Detect and recover orphaned concurrent pipeline slots
 *
 * When VSCode reloads during concurrent pipeline execution, spawned Claude
 * subprocesses become orphaned. The extension host restarts, killing stall
 * detection, process close handlers, and ConcurrentPipelineManager tracking.
 * State files remain stuck with stages at "status": "running" forever.
 *
 * This service scans worktree state files on activation and recovers them.
 *
 * @see Issue #1643 - Stale concurrent slot recovery on extension reload
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { PipelineStage } from "@nightgauge/sdk";
import { WorktreeManager } from "../utils/WorktreeManager";
import { PipelineStateService } from "./PipelineStateService";
import type { PipelineState } from "./PipelineStateService";
import type { Logger } from "../utils/logger";

/**
 * Information about a recovered stale slot
 */
export interface StaleSlotInfo {
  issueNumber: number;
  title: string;
  branch: string;
  worktreePath: string;
  staleStage: PipelineStage;
  staleSinceMs: number;
  processAlive: boolean;
  action: "marked-failed" | "killed-and-failed";
}

/** Default stale threshold: 10 minutes without update */
const DEFAULT_STALE_THRESHOLD_MS = 10 * 60 * 1000;

/**
 * Check whether a process with the given PID is alive.
 * Uses kill(pid, 0) which checks for existence without sending a signal.
 */
function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

export class StaleSlotRecoveryService {
  constructor(
    private repoRoot: string,
    private worktreeBase: string,
    private logger: Logger,
    private staleThresholdMs: number = DEFAULT_STALE_THRESHOLD_MS
  ) {}

  /**
   * Scan all worktree state files and recover any stale slots.
   *
   * A slot is recovered (marked failed) ONLY when its process is genuinely gone
   * — i.e. it was orphaned by an extension reload and its close handler never
   * ran to update state. A slot is recovered when:
   * 1. A worktree directory exists with a state.json file
   * 2. A stage has status "running"
   * 3. AND the recorded `process_pid` is dead (or no PID was recorded) AND the
   *    stage started more than `staleThresholdMs` ago.
   *
   * Critically, a stage whose process is still **alive** is NEVER recovered,
   * regardless of how long it has been running. An alive process is actively
   * doing work — not a stuck orphan — and feature-dev / feature-validate /
   * pr-merge legitimately run far longer than the threshold. Killing a live,
   * producing process here was the #3840 root cause: a healthy 105-turn
   * feature-dev was SIGTERM'd at the 10-minute mark on an extension reload and
   * falsely recorded as failed, even though it completed successfully ~110s
   * later. A genuinely-wedged live orphan is rare and far less harmful than
   * killing healthy work on every reload.
   *
   * @returns Array of recovered stale slots
   */
  async recoverStaleSlots(): Promise<StaleSlotInfo[]> {
    const recovered: StaleSlotInfo[] = [];

    const worktreeDir = path.join(this.repoRoot, this.worktreeBase);
    let entries: string[];
    try {
      const dirEntries = await fs.readdir(worktreeDir, {
        withFileTypes: true,
      });
      entries = dirEntries
        .filter((e) => e.isDirectory() && e.name.startsWith("issue-"))
        .map((e) => e.name);
    } catch {
      // Worktree directory doesn't exist — nothing to recover
      return recovered;
    }

    const now = Date.now();

    for (const entry of entries) {
      const match = entry.match(/^issue-(\d+)$/);
      if (!match) continue;
      const issueNumber = parseInt(match[1], 10);
      const worktreePath = path.join(worktreeDir, entry);
      const statePath = path.join(worktreePath, ".nightgauge", "pipeline", "state.json");

      let stateData: PipelineState;
      try {
        const raw = await fs.readFile(statePath, "utf-8");
        stateData = JSON.parse(raw) as PipelineState;
      } catch {
        // No state file or invalid JSON — skip
        continue;
      }

      // Find any stage stuck in "running"
      for (const [stageName, stageState] of Object.entries(stateData.stages)) {
        if (stageState.status !== "running") continue;
        if (!stageState.started_at) continue;

        const startedAt = new Date(stageState.started_at).getTime();
        const elapsed = now - startedAt;
        const pid = stageState.process_pid;
        const alive = pid ? isProcessAlive(pid) : false;

        // #3840: an ALIVE process is actively working — never a stuck orphan.
        // The elapsed-since-start clock is irrelevant here (stages legitimately
        // run far longer than the threshold). NEVER kill or fail a live
        // process; only recover slots whose process is actually gone.
        if (alive) {
          continue;
        }

        // Process is gone (dead PID) or was never recorded. Only treat as a
        // stuck orphan once past the threshold, so a slot that just started and
        // hasn't recorded its PID yet isn't clobbered mid-write.
        if (elapsed < this.staleThresholdMs) {
          continue;
        }

        // Dead/absent process with a stage still marked "running" past the
        // threshold — a genuine orphan whose close handler never ran. Mark it
        // failed. No process to kill (it's already gone).
        const action: StaleSlotInfo["action"] = "marked-failed";

        // Update state file to mark stage as failed. Use a classifiable marker
        // so the failure never lands as an empty terminal_kind (#3840).
        const stateService = PipelineStateService.createForWorktree(worktreePath);
        try {
          await stateService.failStage(
            stageName as PipelineStage,
            `[stale-slot-orphan] process not running after extension reload; stage was stuck in "running"${
              pid ? ` (PID ${pid} exited)` : " (no PID recorded)"
            }`
          );
        } catch (err) {
          this.logger.warn("Failed to mark stale stage as failed", {
            issueNumber,
            stage: stageName,
            error: err instanceof Error ? err.message : String(err),
          });
          continue;
        }

        const info: StaleSlotInfo = {
          issueNumber,
          title: stateData.title || `Issue #${issueNumber}`,
          branch: stateData.branch || "unknown",
          worktreePath,
          staleStage: stageName as PipelineStage,
          staleSinceMs: elapsed,
          processAlive: alive,
          action,
        };

        recovered.push(info);
        this.logger.info("Recovered stale concurrent slot", {
          issueNumber,
          stage: stageName,
          elapsed: `${Math.round(elapsed / 1000)}s`,
          pid: pid ?? "none",
          processAlive: alive,
          action,
        });

        // Only recover the first stale stage per worktree
        break;
      }
    }

    return recovered;
  }
}
