/**
 * Issue Queue Types
 *
 * Types and interfaces for issue queue management.
 * Enables queuing issues for sequential processing when a pipeline is active.
 *
 * @see Issue #236 - Queue Issues When Pipeline Active
 * @see docs/ARCHITECTURE.md - Context-Isolated Pipeline Architecture
 */

import type { BlockingIssue } from "../services/ProjectBoardService";

/**
 * Status of a single issue in the queue.
 *
 * `paused` (Issue #3001): item was waiting behind a pipeline that hit a terminal
 * failure. The reason is in `QueueItem.pausedReason`. Resumes only via explicit
 * operator action — never auto-resumes when `pipeline.failure_mode = "halt"`.
 */
export type QueueItemStatus =
  "pending" | "ready" | "processing" | "completed" | "failed" | "paused";

/**
 * Why a queue item was paused. Discriminated `kind` so future paused reasons
 * (manual hold, license check, etc.) can be added without re-shaping callers.
 *
 * @see Issue #3001 — Preserve pipeline + queue state on terminal failure
 * @see Issue #3004 — Baseline-CI dependency gate (`baseline_ci_red` variant)
 * @see Issue #231 — Native blockedBy deferral (`blocked_dependency` variant)
 */
export type QueueItemPausedReason =
  | {
      kind: "upstream_failure";
      /**
       * Identifier of the failed run that caused this item to pause. Composed as
       * `${issue_number}-${started_at}` in the Go scheduler so callers can
       * correlate with the failed RunRecord in the daily JSONL.
       */
      failed_run_id: string;
      /** Human-readable summary (e.g., "stage feature-dev: stall_kill"). Optional. */
      summary?: string;
    }
  | {
      /**
       * Issue acceptance criteria require a CI baseline that is currently red on
       * `main`. The baseline-CI gate (Issue #3004) emits this variant during
       * issue-pickup Phase 2.8. The daily `baseline-defer-sweep.yml` cron resumes
       * the item when the last `green_threshold` runs are all `success`.
       */
      kind: "baseline_ci_red";
      /** Human-readable summary surfaced in the dashboard's paused-items panel. */
      summary?: string;
      /** The workflow file the AC referenced (e.g. `ci.yml`). */
      workflow: string;
      /** Optional named job inside the workflow (e.g. `Integration & E2E Tests`). */
      job?: string;
      /** Number of failed runs in the lookback window at defer time. */
      failed_runs?: number;
      /** Lookback window size used by the gate at defer time. */
      lookback_runs?: number;
    }
  | {
      /**
       * Issue has an OPEN native `blockedBy` dependency (the blocker's PR is not
       * merged). The deps-gate (Issue #231) emits this variant during
       * issue-pickup Phase 2.9. `deps-gate promote` (and the autonomous cascade)
       * resumes the item once all `blockingIssues` have closed. A controlled
       * hold, not a failure.
       */
      kind: "blocked_dependency";
      /** Human-readable summary surfaced in the dashboard's paused-items panel. */
      summary?: string;
      /** The open blockers naming why dispatch is deferred. */
      blockingIssues: { number: number; title?: string; repo?: string }[];
    };

/**
 * A single item in the issue queue
 */
export interface QueueItem {
  /** Issue number */
  issueNumber: number;
  /** Issue title for display */
  title: string;
  /** Position in queue (1-indexed for display) */
  position: number;
  /** Current status of this queue item */
  status: QueueItemStatus;
  /** Timestamp when added to queue */
  addedAt: string;
  /** Labels from the issue (for priority/context) */
  labels?: string[];
  /** Blocking dependencies (for blocked indicator display) */
  blockedBy?: BlockingIssue[];
  /** Position in epic body's sub-issue list (0-indexed) */
  epicOrder?: number;
  /** Parent epic issue number (set for sub-issues of an epic) */
  epicNumber?: number;
  /** Repository name (for cross-repo queue display, Issue #2188) */
  repoName?: string;
  /**
   * Reason this item is paused. Only present when `status === "paused"`.
   *
   * @see Issue #3001 — terminal failure preservation
   */
  pausedReason?: QueueItemPausedReason;
}

/**
 * Status of the overall queue
 */
export type QueueStatus =
  | "idle" // No items in queue
  | "waiting" // Items queued, waiting for pipeline to complete
  | "processing" // Auto-starting next item
  | "paused"; // Queue paused due to pipeline failure

/**
 * Queue configuration options
 */
export interface QueueConfig {
  /** Maximum number of issues that can be queued (default: 20) */
  maxQueueSize?: number;
  /** Whether to auto-start next issue when pipeline completes (default: true) */
  autoStart?: boolean;
  /** Delay between pipeline completion and next issue start in ms (default: 2000) */
  autoStartDelay?: number;
}

/**
 * Default queue configuration
 */
export const DEFAULT_QUEUE_CONFIG: Required<QueueConfig> = {
  maxQueueSize: 20,
  autoStart: true,
  autoStartDelay: 2000,
};

/**
 * Active pipeline slot information for concurrent execution
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */
export interface ActiveSlot {
  /** Slot index (0-based) */
  slotIndex: number;
  /** Issue number being processed in this slot */
  issueNumber: number;
  /** Absolute path to the worktree directory */
  worktreePath: string;
  /** Git branch name */
  branch: string;
  /** Timestamp when this slot started */
  startedAt: string;
  /** Current pipeline stage */
  currentStage?: string;
  /** Parent epic number (if this is a sub-issue of an epic) */
  epicNumber?: number;
}

/**
 * Queue state — Go is the authoritative source (schema 2.0+).
 *
 * Kept separate from state.json and batch-state.json to allow
 * independent lifecycle and clean separation of concerns.
 *
 * @see Issue #1898 - Consolidate Queue into Go
 */
export interface QueueState {
  /** Schema version for future migrations */
  schema_version: string;
  /** Current queue status */
  status: QueueStatus;
  /** Queued items in order */
  items: QueueItem[];
  /** Configuration used for this queue (local — not persisted by Go) */
  config?: Required<QueueConfig>;
  /** Timestamp when queue was created/last modified */
  updated_at: string;
  /** Reason for pause (if status is 'paused') */
  pauseReason?: string;
  /**
   * Currently active pipeline slots for concurrent execution.
   * Empty when max_concurrent is 1 (sequential mode).
   *
   * @see Issue #1621 - Git worktree-based concurrent pipeline execution
   */
  activeSlots?: ActiveSlot[];
}

/**
 * Queue schema version — Go is the authoritative owner.
 *
 * 2.0 → 2.1 (Issue #3001): added per-item `paused` status and structured
 * `pausedReason`. Additive; readers default missing `pausedReason` to undefined
 * and treat unknown statuses as `pending`.
 *
 * 2.1 → 2.2 (Issue #3004): added `baseline_ci_red` discriminated variant to
 * `QueueItemPausedReason`. Additive — 2.1 readers ignore the unknown `kind`
 * value (it parses as a generic paused item) and 2.2 readers gain the
 * baseline-CI fields. The daily `baseline-defer-sweep` cron resumes 2.2 items
 * when the baseline goes green.
 *
 * 2.2 → 2.3 (Issue #231): added `blocked_dependency` discriminated variant to
 * `QueueItemPausedReason` (with `blockingIssues`). Additive — 2.2 readers ignore
 * the unknown `kind` (it parses as a generic paused item) and 2.3 readers gain
 * the blocker list. `deps-gate promote` resumes 2.3 items when their blockers
 * all close.
 *
 * @see Issue #1898 - Consolidate Queue into Go
 * @see Issue #3001 — Preserve pipeline + queue state on terminal failure
 * @see Issue #3004 — Baseline-CI dependency gate
 * @see Issue #231 — Native blockedBy dependency deferral
 */
export const QUEUE_SCHEMA_VERSION = "2.3";

/**
 * Callbacks for queue events
 */
export interface QueueCallbacks {
  /** Called when an item is added to the queue */
  onItemAdded?: (item: QueueItem) => void;
  /** Called when an item is removed from the queue */
  onItemRemoved?: (issueNumber: number) => void;
  /** Called when the queue is reordered */
  onQueueReordered?: (items: QueueItem[]) => void;
  /** Called when the queue is cleared */
  onQueueCleared?: () => void;
  /** Called when next item auto-starts */
  onAutoStart?: (item: QueueItem) => void;
  /** Called when queue is paused due to failure */
  onQueuePaused?: (reason: string) => void;
  /** Called when queue status changes */
  onStatusChanged?: (status: QueueStatus) => void;
  /**
   * Called when pipeline fails with queued items remaining
   * Should return true to continue with next item, false to stop queue
   */
  onPipelineFailure?: (failedIssueNumber: number, queueLength: number) => Promise<boolean>;
  /**
   * Called when a blocked issue is being queued; returns true to add anyway, false to cancel
   *
   * @see Issue #820 - Warn Before Queuing Blocked Issues
   */
  onBlockedWarning?: (
    issueNumber: number,
    issueTitle: string,
    blockerTitles: string[]
  ) => Promise<boolean>;
}

/**
 * Create an initial queue state
 */
export function createInitialQueueState(config: Required<QueueConfig>): QueueState {
  return {
    schema_version: "1.1",
    status: "idle",
    items: [],
    config,
    updated_at: new Date().toISOString(),
  };
}

/**
 * Create a queue item from issue info
 */
export function createQueueItem(
  issueNumber: number,
  title: string,
  position: number,
  labels?: string[],
  blockedBy?: BlockingIssue[],
  epicOrder?: number
): QueueItem {
  return {
    issueNumber,
    title,
    position,
    status: "pending",
    addedAt: new Date().toISOString(),
    labels,
    blockedBy: blockedBy && blockedBy.length > 0 ? blockedBy : undefined,
    epicOrder,
  };
}

/**
 * Recalculate positions after queue modification
 */
export function recalculatePositions(items: QueueItem[]): QueueItem[] {
  return items.map((item, index) => ({
    ...item,
    position: index + 1,
  }));
}
