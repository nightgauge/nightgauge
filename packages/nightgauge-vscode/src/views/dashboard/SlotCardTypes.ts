/**
 * SlotCardTypes - Data shapes for the Overview tab "Pipeline Slots" cards.
 *
 * The cards replace the prior single-run activity widget and the
 * Project Board "Top Ready Issues" list with one unified view of
 * "what is the pipeline working on right now, and what's queued next".
 *
 * The shapes are intentionally serialisable so a future platform-send
 * path can forward them as-is.
 */

import type { PipelineStage } from "@nightgauge/sdk";

export type SlotStageStatus =
  "pending" | "running" | "complete" | "failed" | "skipped" | "deferred";

export type SlotCardStatus = "preparing" | "running" | "paused" | "completed" | "failed";

export interface SlotStageSummary {
  stage: PipelineStage;
  status: SlotStageStatus;
  durationMs?: number;
  inputTokens?: number;
  outputTokens?: number;
  costUsd?: number;
}

export interface SlotPhaseSummary {
  /** Display name (e.g. "implementation") */
  name: string;
  /** 1-based current index across the stage's phases */
  index: number;
  /** Total phases for the stage */
  total: number;
}

/**
 * Data backing a single slot card.
 */
export interface SlotCardData {
  /** 0-based slot index from Go's `activeSlots` */
  slotIndex: number;
  issueNumber: number;
  title: string;
  branch?: string;
  worktreePath?: string;
  repoName?: string;
  epicNumber?: number;
  status: SlotCardStatus;
  /** ISO timestamp the slot started */
  startedAt?: string;

  currentStage?: PipelineStage;
  currentPhase?: SlotPhaseSummary;
  stages: SlotStageSummary[];
  completedStageCount: number;
  totalStageCount: number;
  /** True when any open backtrack/RALPH retry has fired */
  hasIssues?: boolean;

  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  costUsd: number;
}

/**
 * Data backing a queued (not-yet-running) issue card.
 *
 * `paused` (Issue #3001) reflects items waiting behind a pipeline that hit a
 * terminal failure; `pausedReason` carries the structured cause used by the
 * card UI to render the paused-clock indicator and tooltip.
 */
export interface QueuedCardData {
  issueNumber: number;
  title: string;
  position: number;
  status: "pending" | "ready" | "processing" | "failed" | "paused";
  isBlocked: boolean;
  blockerCount: number;
  blockerNumbers: number[];
  labels: string[];
  /** Derived from labels (P0/P1/P2) */
  priority?: "P0" | "P1" | "P2";
  repoName?: string;
  epicNumber?: number;
  /** ISO timestamp when added to queue */
  addedAt?: string;
  /** Reason this card is paused — populated only when status === "paused". */
  pausedReason?:
    | {
        kind: "upstream_failure";
        failed_run_id: string;
        summary?: string;
      }
    | {
        kind: "baseline_ci_red";
        summary?: string;
        workflow: string;
        job?: string;
        failed_runs?: number;
        lookback_runs?: number;
      }
    | {
        // Issue #231 — native blockedBy deferral; blockers auto-close to resume.
        kind: "blocked_dependency";
        summary?: string;
        blockingIssues: { number: number; title?: string; repo?: string }[];
      };
}

export interface PipelineSlotsViewData {
  maxConcurrent: number;
  queueStatus: "idle" | "waiting" | "processing" | "paused";
  slots: SlotCardData[];
  queued: QueuedCardData[];
}
