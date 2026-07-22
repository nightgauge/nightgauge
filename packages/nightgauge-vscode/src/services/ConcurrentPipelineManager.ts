/**
 * ConcurrentPipelineManager - Manage multiple concurrent pipeline executions
 *
 * Coordinates multiple HeadlessOrchestrator instances, each running in its own
 * git worktree. When a slot completes, automatically picks up the next eligible
 * queued issue (one that has no blocking dependencies on running issues).
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { exec, execFile } from "child_process";
import { promisify } from "util";
import type { PipelineStage } from "@nightgauge/sdk";
import { WorktreeManager, type WorktreeInfo } from "../utils/WorktreeManager";
import { killAllActiveProcesses } from "../utils/skillRunner";
import { getPRForIssue } from "../utils/prDetection";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

/**
 * Hard deadline for {@link ConcurrentPipelineManager.abortAll} to wait for
 * in-flight slots to finish. If `waitForIdle()` does not complete within this
 * window, abortAll force-clears the slot map and resets `isShuttingDown` so
 * the queue does not stay permanently frozen. See Issue #3111.
 */
const ABORT_ALL_TIMEOUT_MS = 30_000;

/**
 * Transient network-blip detector (#4002): true when the failure text carries
 * one of the two network terminal-kind signatures — an Anthropic transport
 * drop (`api_connection_lost`: "API Error: The socket connection was closed
 * unexpectedly" / "socket hang up") or the pipeline-start GitHub outage marker
 * (`github_network_outage`). Both auto-recover via the Go scheduler's
 * environmental routing (short backoff / global cooldown, board→Ready, no
 * lifetime-cap increment), so they must neither halt the queue nor post a
 * failure comment. Match strings mirror Go's ClassifyTerminalKind and
 * bootstrap/services.ts — keep aligned.
 */
function isTransientNetworkFailureText(errMsg: string): boolean {
  return (
    /socket connection was closed/i.test(errMsg) ||
    /socket hang up/i.test(errMsg) ||
    /api_connection_lost/i.test(errMsg) ||
    /github-network-outage/i.test(errMsg) ||
    /github_network_outage/i.test(errMsg)
  );
}

import type { IssueQueueService } from "./IssueQueueService";
import type { HeadlessOrchestrator } from "./HeadlessOrchestrator";
import type { PipelineRunResult } from "./HeadlessOrchestrator";
import type { PipelineStateService } from "./PipelineStateService";
import type { Logger } from "../utils/logger";
import type { ActiveSlot, QueueItem } from "../types/queue";
import { updateProjectItemStatus } from "../utils/projectFieldWriter";
import { ARCHITECTURE_APPROVAL_REQUIRED_MARKER, postFailureComment } from "../utils/failureComment";
import { getConcurrentPipelineConfig } from "../utils/incrediConfig";
import type { WorkspaceManager } from "./WorkspaceManager";
import { IpcClient } from "./IpcClient";

/**
 * Factory function to create a HeadlessOrchestrator for a worktree.
 * Returns both the orchestrator and the per-slot PipelineStateService
 * so the UI layer can create tree providers for each concurrent slot.
 *
 * @see Issue #1631 - Concurrent Pipeline Visibility
 */
export type OrchestratorFactory = (
  workingDirectory: string,
  issueNumber: number
) => {
  orchestrator: HeadlessOrchestrator;
  stateService: PipelineStateService;
};

/**
 * Slot state for a single concurrent pipeline execution
 */
interface PipelineSlot {
  /** Slot index (0-based) */
  index: number;
  /** Platform run ID from ack — used to route cancel commands to the right slot */
  runId?: string;
  /** Issue number being processed */
  issueNumber: number;
  /** Issue title for display */
  title: string;
  /** Parent epic number (if this is a sub-issue of an epic) */
  epicNumber?: number;
  /** Full repo identity "owner/repo" for cross-repo pipelines */
  repo?: string;
  /** Worktree info */
  worktree: WorktreeInfo;
  /** WorktreeManager that created this slot's worktree (for correct cleanup on repo switch) */
  worktreeManager: WorktreeManager;
  /** HeadlessOrchestrator instance for this slot */
  orchestrator: HeadlessOrchestrator;
  /** Per-slot PipelineStateService for UI tree provider binding (#1631) */
  stateService: PipelineStateService;
  /** When this slot started */
  startedAt: string;
  /** Current pipeline stage */
  currentStage?: PipelineStage;
  /** Promise that resolves when the pipeline completes */
  runPromise?: Promise<PipelineRunResult>;
  /** Epic ordering position (0-based) — used to drain successors on failure */
  epicOrder?: number;
  /**
   * True when the user explicitly stopped this slot (per-slot stop button or
   * Stop All). Distinguishes a deliberate cancellation from a real pipeline
   * failure so the UI doesn't surface a misleading "Pipeline failed at X"
   * modal and the failure-as-such bookkeeping (board status flip, GitHub
   * comment, queue halt) is suppressed. Set in `abortSlot`/`abortAll`,
   * read in the slot completion handler.
   */
  userCancelled?: boolean;
}

/**
 * Callbacks for ConcurrentPipelineManager events
 */
export interface ConcurrentPipelineCallbacks {
  /** Called immediately when an issue is dequeued, before worktree creation */
  onSlotPreparing?: (issueNumber: number, title: string, epicNumber?: number) => void;
  /** Called when a slot starts processing an issue (after worktree is ready) */
  onSlotStarted?: (
    slotIndex: number,
    issueNumber: number,
    title: string,
    stateService: PipelineStateService,
    epicNumber?: number,
    repoSlug?: string
  ) => void;
  /** Called when a slot's pipeline stage changes */
  onSlotStageChanged?: (slotIndex: number, issueNumber: number, stage: PipelineStage) => void;
  /** Called when a slot completes successfully */
  onSlotCompleted?: (
    slotIndex: number,
    issueNumber: number,
    result: PipelineRunResult,
    tokens: {
      input: number;
      output: number;
      cacheRead: number;
      cacheCreation: number;
      estimated_cost_usd: number;
    },
    repoSlug?: string
  ) => void;
  /** Called when a slot fails */
  onSlotFailed?: (
    slotIndex: number,
    issueNumber: number,
    error: Error,
    costUsd: number,
    repoSlug?: string
  ) => void;
  /**
   * Called when a slot DEFERRED — pickup found the issue's native blockedBy
   * dependencies still open (#189/#305). This is NOT a failure: no user-facing
   * failure notification, no autonomous pause, no failure telemetry. The
   * handler frees the Go slot with a non-failure `blocked_dependency` signal so
   * the scheduler keeps the issue eligible for a later tick.
   */
  onSlotDeferred?: (
    slotIndex: number,
    issueNumber: number,
    result: PipelineRunResult,
    costUsd: number,
    repoSlug?: string
  ) => void;
  /** Called when a slot is cleaned up (worktree removed) */
  onSlotCleaned?: (slotIndex: number, issueNumber: number) => void;
  /**
   * Called when a re-enqueue attempt after a slot-start failure itself
   * throws — e.g., because the queue's stop-control guard is active or the
   * IPC transport failed. Without this callback the error would be lost
   * silently and the user would lose the item with no feedback.
   *
   * @see Issue #2992 — broken failure recovery
   */
  onReEnqueueFailed?: (issueNumber: number, error: Error) => void;
  /** Called when all slots are idle and queue is empty */
  onAllComplete?: () => void;
  /** Called when stdout output arrives for a slot */
  onSlotOutput?: (
    slotIndex: number,
    issueNumber: number,
    data: string,
    stage?: PipelineStage
  ) => void;
  /** Called when stderr output arrives for a slot */
  onSlotError?: (slotIndex: number, issueNumber: number, data: string) => void;
  /** Called when a phase starts within a slot's stage (for live phase progress) */
  onSlotPhaseStart?: (
    slotIndex: number,
    issueNumber: number,
    stage: PipelineStage,
    phaseName: string,
    phaseIndex: number,
    totalPhases: number
  ) => void;
}

/**
 * Typed error surfaced when `git worktree add` fails because the branch
 * already exists. Carries the branch name and (if present) the open PR URL
 * so the toast/tree-view consumer can deep-link to remediation.
 *
 * @see Issue #2992
 */
export class BranchCollisionError extends Error {
  constructor(
    message: string,
    public readonly branchName: string,
    public readonly prUrl?: string
  ) {
    super(message);
    this.name = "BranchCollisionError";
  }
}

/**
 * ConcurrentPipelineManager manages N pipeline "slots" for parallel execution
 */
export class ConcurrentPipelineManager implements vscode.Disposable {
  private slots: Map<number, PipelineSlot> = new Map(); // keyed by issueNumber
  /**
   * In-flight slot reservations, keyed by issueNumber. A reservation is taken
   * synchronously in `startSlot` BEFORE the async `worktreeManager.create()`
   * and released either when the real {@link PipelineSlot} lands in `slots`
   * (success) or when the start fails. Without it, a slot's repo is invisible
   * to `availableSlotCount` and the `runningItems` set until line ~719 — so a
   * second `fillSlots()` pass that begins while a worktree is still being
   * created under-counts same-repo concurrency, and the Go scheduler re-seeds
   * `repoInFlight` without the in-flight item → the per-repo cap can be
   * exceeded across passes. Reserving here makes both the workspace ceiling
   * and the per-repo running set reflect intent-to-run immediately. #3874.
   */
  private reservedSlots: Map<number, { index: number; repo: string }> = new Map();
  private pendingRunIds: Map<number, string> = new Map(); // runId from ack, applied when slot opens

  /**
   * In-flight slot lifecycle promises, keyed by issue number. Unlike
   * `this.slots` — which `cleanupSlot` empties partway through the lifecycle,
   * BEFORE the halt/pause decision runs — an entry here survives until the
   * ENTIRE `runSlotPipeline` promise (its finally-block cleanup AND
   * `haltQueueOnSlotFailure`) has settled. This is the real completion signal
   * `settleForTest` awaits, letting tests synchronize on the actual event
   * instead of a fixed `setTimeout` that races the async chain under CPU load
   * (the #100 / #243 flake class).
   */
  private readonly lifecyclePromises = new Map<number, Promise<PipelineRunResult>>();
  private worktreeManager: WorktreeManager;
  private maxConcurrent: number;
  private callbacks: ConcurrentPipelineCallbacks = {};
  private isShuttingDown = false;
  private isAbortAllInProgress = false;
  private isFilling = false;
  private fillAgain = false;
  private authCircuitOpen = false;
  private disposables: vscode.Disposable[] = [];

  private _onSlotsChanged = new vscode.EventEmitter<ActiveSlot[]>();
  readonly onSlotsChanged = this._onSlotsChanged.event;

  /** Optional WorkspaceManager for resolving cross-repo local paths */
  private workspaceManager: WorkspaceManager | undefined;

  /**
   * Optional pre-dispatch gate. When set and returning a non-null reason,
   * `fillSlots` refuses to start new slots and logs the reason. Used by
   * Issue #3300 to refuse dispatch when the running extension build is stale
   * on critical pipeline paths. Returns null to allow dispatch.
   */
  private dispatchGate: (() => string | null) | null = null;

  setDispatchGate(gate: (() => string | null) | null): void {
    this.dispatchGate = gate;
  }

  constructor(
    private repoRoot: string,
    private queueService: IssueQueueService,
    private orchestratorFactory: OrchestratorFactory,
    private logger: Logger,
    config?: { maxConcurrent?: number; worktreeBase?: string },
    workspaceManager?: WorkspaceManager
  ) {
    const pipelineConfig = getConcurrentPipelineConfig(repoRoot);
    this.maxConcurrent = config?.maxConcurrent ?? pipelineConfig.maxConcurrent;
    const worktreeBase = config?.worktreeBase ?? pipelineConfig.worktreeBase;
    this.worktreeManager = new WorktreeManager(repoRoot, worktreeBase);
    this.workspaceManager = workspaceManager;

    this.disposables.push(this._onSlotsChanged);
  }

  /**
   * Whether the worktree-based pipeline path is enabled (max_concurrent >= 1).
   *
   * With Issue #1831, all pipeline executions route through
   * ConcurrentPipelineManager — including single-issue runs (maxConcurrent=1).
   * This getter returns true whenever the manager can process slots.
   */
  get isConcurrentEnabled(): boolean {
    return this.maxConcurrent >= 1;
  }

  /**
   * The configured maximum number of concurrent slots.
   * Exposed for callers that need to distinguish single-slot (1) from multi-slot (>1)
   * behavior, e.g. status bar display.
   *
   * @see Issue #1831 - Unify pipeline worktree path
   */
  get maxConcurrentSlots(): number {
    return this.maxConcurrent;
  }

  /**
   * Update the maximum concurrent slot ceiling at runtime.
   * Takes effect on the next fillSlots cycle — running pipelines are never interrupted.
   * Value is clamped to 1–10.
   */
  setMaxConcurrentSlots(n: number): void {
    const clamped = Math.max(1, Math.min(10, Math.round(n)));
    this.logger.info("Updating max concurrent slots", {
      previous: this.maxConcurrent,
      new: clamped,
    });
    this.maxConcurrent = clamped;
  }

  /**
   * Number of currently active slots
   */
  get activeSlotCount(): number {
    return this.slots.size;
  }

  /**
   * Number of available slots.
   *
   * Subtracts in-flight reservations (slots whose worktree is still being
   * created and are not yet in `this.slots`) in addition to active slots, so
   * the workspace ceiling is never briefly over-subscribed during worktree
   * creation and a concurrent `fillSlots()` pass cannot dispatch into a slot
   * that is already being claimed. #3874.
   */
  get availableSlotCount(): number {
    return this.maxConcurrent - this.slots.size - this.reservedSlots.size;
  }

  /**
   * Get all active slots as external-facing data
   */
  getActiveSlots(): ActiveSlot[] {
    return Array.from(this.slots.values()).map((slot) => ({
      slotIndex: slot.index,
      issueNumber: slot.issueNumber,
      worktreePath: slot.worktree.path,
      branch: slot.worktree.branch,
      startedAt: slot.startedAt,
      currentStage: slot.currentStage,
      epicNumber: slot.epicNumber,
    }));
  }

  /**
   * Get the PipelineStateService for a specific slot by slot index.
   * Returns undefined if no slot is active at the given index.
   * Complements the onSlotStarted callback for pull-based access (#1634).
   */
  getSlotStateService(slotIndex: number): PipelineStateService | undefined {
    for (const slot of this.slots.values()) {
      if (slot.index === slotIndex) {
        return slot.stateService;
      }
    }
    return undefined;
  }

  /**
   * Check if an issue is currently in an active pipeline slot
   */
  isIssueInSlots(issueNumber: number): boolean {
    return this.slots.has(issueNumber);
  }

  /**
   * Set event callbacks
   */
  setCallbacks(callbacks: ConcurrentPipelineCallbacks): void {
    this.callbacks = callbacks;
  }

  /**
   * Update the repository root when the active workspace changes.
   * Re-creates the WorktreeManager so new slots use the correct repo.
   * Running slots retain their original WorktreeManager for safe cleanup.
   * Also re-reads config from the new repo for maxConcurrent.
   */
  updateRepoRoot(newRepoRoot: string): void {
    if (this.repoRoot === newRepoRoot) return;
    this.logger.info("Updating concurrent pipeline repo root", {
      from: this.repoRoot,
      to: newRepoRoot,
      activeSlots: this.slots.size,
    });
    this.repoRoot = newRepoRoot;
    const pipelineConfig = getConcurrentPipelineConfig(newRepoRoot);
    this.maxConcurrent = pipelineConfig.maxConcurrent;
    this.worktreeManager = new WorktreeManager(newRepoRoot, pipelineConfig.worktreeBase);
  }

  /**
   * Fill available slots with independent issues from the queue
   *
   * This is the main entry point for starting concurrent pipelines.
   * It dequeues up to N independent issues and starts a pipeline for each.
   */
  async fillSlots(): Promise<number> {
    if (this.isShuttingDown) return 0;
    if (this.authCircuitOpen) {
      this.logger.warn(
        "fillSlots skipped — auth circuit breaker is open (Claude Code session expired)"
      );
      return 0;
    }
    // Pre-dispatch gate (#3300). Refuses to start new slots when the running
    // extension is stale on critical pipeline paths. In-flight slots continue
    // to completion — this only stops NEW dispatches. The reason string is
    // logged at warn level and shown to the user via the staleness status bar
    // item; the gate is overridable via `pipeline.allow_stale_dispatch: true`
    // (the bootstrap wires the gate to honor that flag).
    if (this.dispatchGate) {
      const refusal = this.dispatchGate();
      if (refusal) {
        this.logger.warn("fillSlots refused by dispatch gate", { reason: refusal });
        return 0;
      }
    }

    // Guard against concurrent fillSlots calls. The debounced onItemAdded
    // callback can fire while a previous fillSlots is still creating worktrees
    // (async gap during fetchIssueTitle between enqueued items), causing two
    // concurrent `git worktree add` calls that fight over .git/config lock.
    // If a second call arrives, mark fillAgain so we re-check after.
    if (this.isFilling) {
      this.fillAgain = true;
      this.logger.debug("fillSlots already running, will re-check after");
      return 0;
    }

    this.isFilling = true;
    let totalStarted = 0;

    try {
      do {
        this.fillAgain = false;

        const available = this.availableSlotCount;
        if (available <= 0) break;

        if (this.isShuttingDown) break;

        // Pass each in-flight slot's repo so the scheduler can enforce per-repo
        // concurrency caps (concurrency.per_repo_max / repository_overrides).
        // `available` (the global ceiling) alone does not stop two same-repo
        // issues from dispatching into separate slots.
        //
        // Include reservations (slots whose worktree is still being created and
        // are not yet in `this.slots`) so a second pass cannot under-count
        // same-repo concurrency and let the Go scheduler re-seed `repoInFlight`
        // without the in-flight item — the cross-pass race this issue closes
        // (#3874).
        const runningItems = [
          ...Array.from(this.slots.values()).map((s) => ({
            repo: s.repo ?? "",
            number: s.issueNumber,
          })),
          ...Array.from(this.reservedSlots.entries()).map(([number, r]) => ({
            repo: r.repo,
            number,
          })),
        ];
        this.logger.debug("fillSlots: dequeuing", {
          available,
          runningItems,
        });
        const items = await this.queueService.dequeueIndependent(available, runningItems);

        if (items.length === 0) {
          this.logger.info(
            "fillSlots: dequeueIndependent returned 0 items — queue may be empty or all items blocked"
          );
          break;
        }

        this.logger.info("Filling concurrent pipeline slots", {
          available,
          dequeued: items.length,
          issues: items.map((i) => i.issueNumber),
        });

        // Create worktrees sequentially to avoid git .git/config lock contention.
        // Each `git worktree add` writes to .git/config, and concurrent writes
        // cause "could not lock config file" errors. Pipelines still run in
        // parallel after worktree creation (startSlot fires runPromise async).
        for (const item of items) {
          // Re-check after each async worktree creation — Stop may have been
          // pressed while we were awaiting the previous startSlot.
          if (this.isShuttingDown) break;
          // #188: per-issue in-flight guard at the dispatch boundary. An
          // issue with a live slot (or a reservation whose worktree is still
          // being created) must be skipped by subsequent fills regardless of
          // how many onItemAdded events fired — bowlsheet#233 double-ran
          // runPipeline within 3s (two pre-flights, overlapping stage
          // starts, races on the same context files and worktree). Skip
          // WITHOUT re-enqueueing: the issue is already being worked.
          if (this.slots.has(item.issueNumber) || this.reservedSlots.has(item.issueNumber)) {
            this.logger.warn("Skipping duplicate dispatch — issue already in flight (#188)", {
              issueNumber: item.issueNumber,
              hasLiveSlot: this.slots.has(item.issueNumber),
              hasReservation: this.reservedSlots.has(item.issueNumber),
            });
            continue;
          }
          const ok = await this.startSlot(item);
          if (ok) {
            totalStarted++;
          } else {
            // Re-enqueue failed items so they aren't lost.
            // Set fillAgain so the do-while loop re-dequeues after this batch
            // completes — without this, re-enqueued items sit in the queue
            // until the next external event (slot completion, etc.) and can
            // be silently lost if no further events fire. See Issue #2359.
            try {
              await this.queueService.enqueue(item.issueNumber, item.title, item.labels);
              this.fillAgain = true;
              this.logger.info("Re-enqueued item after slot start failure", {
                issueNumber: item.issueNumber,
                fillAgain: true,
              });
            } catch (err) {
              const reEnqueueError = err instanceof Error ? err : new Error(String(err));
              this.logger.error("Failed to re-enqueue item after slot failure", {
                issueNumber: item.issueNumber,
                error: reEnqueueError.message,
                stack: reEnqueueError.stack,
              });
              try {
                this.callbacks.onReEnqueueFailed?.(item.issueNumber, reEnqueueError);
              } catch {
                // Never let a callback error break the fill loop.
              }
            }
          }
        }
      } while (this.fillAgain && !this.isShuttingDown);
    } finally {
      this.isFilling = false;
    }

    return totalStarted;
  }

  /**
   * Start a pipeline in a new slot for the given queue item.
   * Returns true if the slot was created successfully, false if it failed.
   */
  /**
   * Resolve the WorktreeManager for a queue item. For cross-repo items,
   * looks up the target repo's local path via WorkspaceManager and returns
   * a WorktreeManager rooted there. Falls back to the default (single-root)
   * manager this instance was constructed with.
   *
   * This is the PER-COMMAND target-root resolution path: it runs at dispatch
   * time (fillSlots → startSlot), independent of the fixed `repoRoot` passed
   * to the constructor, so a multi-root `.code-workspace` with no single
   * resolvable `incrediRoot` still dispatches each item to the correct repo
   * as long as `workspaceManager` was provided. Returns null (item rejected,
   * see caller) when the item targets a repo not present in this workspace —
   * this is the "unmatched repo" graceful-failure path.
   *
   * @see Issue #2245 - Cross-repo worktree creation
   * @see Issue #4117 - Agent runner gated on a single incrediRoot
   */
  private resolveWorktreeManager(item: QueueItem): WorktreeManager | null {
    if (!item.repoName || !this.workspaceManager) {
      return this.worktreeManager;
    }

    const targetRepo = this.workspaceManager.findRepositoryByGitHub(item.repoName);
    if (!targetRepo) {
      this.logger.error("Cross-repo item rejected — target repo not found in workspace", {
        issueNumber: item.issueNumber,
        repoName: item.repoName,
      });
      return null;
    }

    // If it resolves to the same root, use the existing manager
    if (targetRepo.path === this.worktreeManager.getRepoRoot()) {
      return this.worktreeManager;
    }

    this.logger.info("Using cross-repo worktree manager", {
      issueNumber: item.issueNumber,
      repoName: item.repoName,
      targetPath: targetRepo.path,
    });
    return new WorktreeManager(targetRepo.path);
  }

  private async startSlot(item: QueueItem): Promise<boolean> {
    const slotIndex = this.findAvailableSlotIndex();
    const branchName = `feat/${item.issueNumber}-${this.slugify(item.title)}`;

    // Reserve this slot's identity (index + repo) synchronously, before any
    // async work below (worktree creation). This makes `availableSlotCount`
    // and the `runningItems` set in `fillSlots` reflect intent-to-run
    // immediately, closing the cross-pass per-repo cap race (#3874). The
    // reservation is released on every exit path: it is superseded by the real
    // slot entry on success (see `finally` below) and removed on failure.
    this.reservedSlots.set(item.issueNumber, {
      index: slotIndex,
      repo: item.repoName ?? "",
    });
    let reservationReleased = false;
    const releaseReservation = () => {
      if (!reservationReleased) {
        this.reservedSlots.delete(item.issueNumber);
        reservationReleased = true;
      }
    };
    try {
      return await this.startSlotInner(item, slotIndex, branchName);
    } finally {
      // Always drop the reservation. On success the slot is already in
      // `this.slots` (set inside startSlotInner), so accounting is unchanged;
      // on failure this frees the reserved capacity for re-fill.
      releaseReservation();
    }
  }

  private async startSlotInner(
    item: QueueItem,
    slotIndex: number,
    branchName: string
  ): Promise<boolean> {
    // Resolve the correct WorktreeManager for this item (cross-repo aware).
    // Returns null if the item targets a repo not present in this workspace.
    const slotWorktreeManager = this.resolveWorktreeManager(item);
    if (!slotWorktreeManager) {
      this.logger.warn("Skipping cross-repo item — target repo not in workspace", {
        issueNumber: item.issueNumber,
        repoName: item.repoName,
      });
      this.callbacks.onSlotFailed?.(
        slotIndex,
        item.issueNumber,
        new Error(
          `Cannot run issue #${item.issueNumber} — repo ${item.repoName} is not open in this workspace. ` +
            `Open the target repo in a multi-root workspace or run the pipeline from that repo's workspace.`
        ),
        0,
        item.repoName
      );
      return false;
    }

    // Detect epic branch for sub-issues so the worktree branches from the
    // epic branch (with main merged in) instead of bare main.
    let baseBranch: string | undefined;
    if (item.epicNumber) {
      try {
        const { stdout } = await execAsync(
          `git ls-remote --heads origin "epic/${item.epicNumber}-*" | head -1 | awk '{print $2}' | sed 's|refs/heads/||'`,
          { cwd: slotWorktreeManager.getRepoRoot(), timeout: 15_000 }
        );
        const epicBranch = stdout.trim();
        if (epicBranch) {
          baseBranch = epicBranch;
          this.logger.info("Epic branch detected for sub-issue worktree", {
            issueNumber: item.issueNumber,
            epicNumber: item.epicNumber,
            epicBranch,
          });
        }
      } catch {
        // Non-critical — fall back to main
      }
    }

    this.logger.info("Starting concurrent pipeline slot", {
      slotIndex,
      issueNumber: item.issueNumber,
      branch: branchName,
      repoRoot: slotWorktreeManager.getRepoRoot(),
      ...(baseBranch ? { baseBranch } : {}),
    });

    // Notify UI immediately so the user sees feedback before worktree creation
    this.callbacks.onSlotPreparing?.(item.issueNumber, item.title, item.epicNumber);

    // Check for a conflict-restart signal left by pr-merge when it failed due
    // to unresolvable merge conflicts. If present, we force-delete the remote
    // branch before creating the fresh worktree — GitHub auto-closes the stale
    // conflicting PR, and the new push won't be rejected as non-fast-forward.
    let deleteRemoteBranch = false;
    const conflictSignalPath = path.join(
      slotWorktreeManager.getRepoRoot(),
      ".nightgauge",
      "pipeline",
      `conflict-restart-${item.issueNumber}.json`
    );
    try {
      await fs.access(conflictSignalPath);
      deleteRemoteBranch = true;
      this.logger.info(
        "Conflict-restart signal detected — will force-delete remote branch before dispatch",
        { issueNumber: item.issueNumber, branchName }
      );
      // Consume the signal immediately so a second concurrent dispatch can't
      // also read it (belt-and-suspenders against re-entry).
      await fs.unlink(conflictSignalPath).catch(() => {});
    } catch {
      // No signal file — normal dispatch
    }

    let worktree: WorktreeInfo | undefined;
    const maxWorktreeRetries = 2;
    for (let attempt = 1; attempt <= maxWorktreeRetries; attempt++) {
      try {
        worktree = await slotWorktreeManager.create(item.issueNumber, branchName, {
          npmInstall: false,
          ...(baseBranch ? { baseBranch } : {}),
          ...(deleteRemoteBranch ? { deleteRemoteBranch: true } : {}),
        });
        break; // success
      } catch (error) {
        const errMsg = error instanceof Error ? error.message : "Unknown error";
        const isConfigLock = errMsg.includes("could not lock config file");

        if (isConfigLock && attempt < maxWorktreeRetries) {
          // .git/config lock contention — another worktree operation may be
          // finishing. Wait briefly and retry. See Issue #2359.
          this.logger.warn("Worktree creation hit .git/config lock — retrying after delay", {
            issueNumber: item.issueNumber,
            attempt,
            maxRetries: maxWorktreeRetries,
          });
          await new Promise((r) => setTimeout(r, 3000));
          continue;
        }

        this.logger.error("Failed to create worktree for concurrent pipeline", {
          issueNumber: item.issueNumber,
          repoRoot: slotWorktreeManager.getRepoRoot(),
          error: errMsg,
          attempt,
        });

        // Branch-collision detection: if the worktree failed because the branch
        // already exists (locally or as a leftover from a previous pipeline run
        // that left an open PR), the raw git error is not actionable. Look up
        // an open PR and replace the message with a remediation hint. Never
        // auto-delete the branch — the user may have unpushed work on it.
        // @see Issue #2992
        const surfacedError = await this.enrichBranchCollisionError(
          error,
          errMsg,
          branchName,
          item.issueNumber,
          slotWorktreeManager.getRepoRoot()
        );

        this.callbacks.onSlotFailed?.(
          slotIndex,
          item.issueNumber,
          surfacedError,
          0, // no cost — worktree creation failed before pipeline ran
          item.repoName
        );
        return false;
      }
    }
    // All retry failures return false above, so worktree is always assigned here.
    if (!worktree) return false;

    // Check if stop was pressed during the async worktree creation.
    // Without this, a slot can start running after the user already hit stop.
    if (this.isShuttingDown) {
      this.logger.info("Stop pressed during worktree creation — aborting slot", {
        issueNumber: item.issueNumber,
      });
      try {
        await slotWorktreeManager.cleanup(item.issueNumber, true);
      } catch {
        // Best effort cleanup
      }
      return false;
    }

    const { orchestrator, stateService } = this.orchestratorFactory(
      worktree.path,
      item.issueNumber
    );
    // Issue #3704: seed _lastState so updateTokens() does not no-op before
    // any IPC pipeline.notifyStageTransition fires for this worktree slot.
    stateService.initEmpty();

    // Cross-repo override: if the queued item belongs to a different repo,
    // set the repo override so all gh CLI calls target the correct repo.
    if (item.repoName) {
      orchestrator.setRepoOverride(item.repoName);
    }

    // Concurrent slots are inherently unattended — they run from the
    // autonomous scheduler / queue with no human watching the modal. Mark the
    // orchestrator so budget/ceiling escalations auto-resolve (up to the cap)
    // instead of blocking on an interactive prompt that never gets clicked.
    orchestrator.setUnattended(true);

    // Capture the slot's worktreeManager so cleanup uses the correct repo
    // even if updateRepoRoot() is called while this slot is running.
    // For cross-repo items, this is the target repo's manager (not this.worktreeManager).
    const pendingRunId = this.pendingRunIds.get(item.issueNumber);
    if (pendingRunId !== undefined) {
      this.pendingRunIds.delete(item.issueNumber);
    }

    const slot: PipelineSlot = {
      index: slotIndex,
      issueNumber: item.issueNumber,
      title: item.title,
      epicNumber: item.epicNumber,
      repo: item.repoName,
      worktree,
      worktreeManager: slotWorktreeManager,
      orchestrator,
      stateService,
      startedAt: new Date().toISOString(),
      epicOrder: item.epicOrder,
      runId: pendingRunId,
    };

    this.slots.set(item.issueNumber, slot);
    this.emitSlotsChanged();

    // Enrich pipeline state with epic context for Discord/UI
    if (item.epicNumber != null) {
      stateService.setMeta({
        epic_number: item.epicNumber,
        epic_position: (item.epicOrder ?? 0) + 1, // 1-indexed
      });

      // Best-effort: fetch queue to count total epic sub-issues
      const epicNum = item.epicNumber;
      this.queueService
        .getQueue()
        .then((queueState) => {
          if (!queueState) return;
          const queuedCount = queueState.items.filter((q) => q.epicNumber === epicNum).length;
          const runningCount = this.getSlotsByEpic(epicNum).length;
          const total = queuedCount + runningCount;
          if (total > 0) {
            stateService.setMeta({ epic_total: total });
          }
        })
        .catch(() => {
          /* non-critical */
        });
    }

    this.callbacks.onSlotStarted?.(
      slotIndex,
      item.issueNumber,
      item.title,
      stateService,
      item.epicNumber,
      item.repoName
    );

    // Run pipeline asynchronously — don't await, let it complete in background
    const runPromise = this.runSlotPipeline(slot);
    slot.runPromise = runPromise;
    // Track the full lifecycle by issue so `settleForTest` (tests) can await the
    // real completion signal. The slot is deleted from `this.slots` mid-flight
    // (cleanupSlot), so it can no longer be observed there once cleanup begins.
    this.lifecyclePromises.set(item.issueNumber, runPromise);
    void runPromise.finally(() => {
      if (this.lifecyclePromises.get(item.issueNumber) === runPromise) {
        this.lifecyclePromises.delete(item.issueNumber);
      }
    });
    return true;
  }

  /**
   * Run a pipeline in a slot and handle completion/cleanup
   */
  private async runSlotPipeline(slot: PipelineSlot): Promise<PipelineRunResult> {
    let pipelineSucceeded = false;
    let pipelineBudgetExceeded = false;
    let pipelineFailedAtPrMerge = false;
    let pipelineFailedAtStreamIdleTimeout = false;
    let isAlreadyResolved = false;
    // #305: a blockedBy deferral is a non-failure. When set, the finally block
    // below skips every failure side-effect (In-review board move, failure
    // comment, successor drain, queue halt/autonomous pause).
    let pipelineDeferred = false;
    let pipelineResult: PipelineRunResult | undefined;
    const startMs = Date.now();
    this.logger.info("[SlotLifecycle] runSlotPipeline STARTED", {
      slotIndex: slot.index,
      issueNumber: slot.issueNumber,
      slotsBeforeRun: this.slots.size,
    });

    // Pre-initialize pipeline state with the real issue title from the queue
    // item so that HeadlessOrchestrator.runPipeline() finds existing state and
    // skips its placeholder initialization (`Issue #NNN`). This ensures the
    // Discord embed and other consumers see the actual GitHub issue title from
    // the very first event.
    if (slot.stateService && slot.title) {
      try {
        await slot.stateService.initializePipeline(
          slot.issueNumber,
          slot.title,
          `feat/${slot.issueNumber}-${this.slugify(slot.title)}`
        );
      } catch {
        // Non-critical — runPipeline will initialize with placeholder
      }
    }

    try {
      const result = await slot.orchestrator.runPipeline(slot.issueNumber, {
        onStageStart: (stage) => {
          slot.currentStage = stage;
          this.emitSlotsChanged();
          this.callbacks.onSlotStageChanged?.(slot.index, slot.issueNumber, stage);
        },
        onStdout: (stage, data) => {
          this.callbacks.onSlotOutput?.(slot.index, slot.issueNumber, data, stage);
        },
        onPhaseStart: (stage, name, index, total) => {
          this.callbacks.onSlotPhaseStart?.(
            slot.index,
            slot.issueNumber,
            stage,
            name,
            index,
            total
          );
        },
        onStderr: (stage, data) => {
          // Apply the same keyword-based classification as streamOutputHandler
          // so informational stderr lines (e.g. "[skillRunner] Stage: ...",
          // "[PRE-FLIGHT] cost estimate") don't appear as [ERROR] in the output.
          for (const line of data.split("\n")) {
            if (!line.trim()) continue;
            const lower = line.toLowerCase();
            const isError = lower.includes("error") || lower.includes("failed");
            if (isError) {
              this.callbacks.onSlotError?.(slot.index, slot.issueNumber, line);
            } else {
              this.callbacks.onSlotOutput?.(slot.index, slot.issueNumber, line, stage);
            }
          }
        },
      });

      pipelineResult = result;
      pipelineSucceeded = result.success;
      pipelineDeferred = result.deferred ?? false;
      pipelineBudgetExceeded = result.budgetExceeded ?? false;
      pipelineFailedAtPrMerge = !result.success && result.failedStage === "pr-merge";
      // Detect Anthropic stream-idle-timeout (#3398) and rate-limit quota
      // exhaustion (#3386) so the worktree is preserved instead of cleaned
      // up. Pre-fix, $14–24 of in-progress edits were wiped on every
      // occurrence, including substantial work already written to disk.
      // Preserving the worktree keeps the work available for inspection
      // (and a future "resume from worktree" path) and matches the pattern
      // used for budget-exceeded / pr-merge failures.
      const errMsg = result.error?.message ?? "";
      pipelineFailedAtStreamIdleTimeout =
        !result.success &&
        (/stream idle timeout/i.test(errMsg) ||
          /rate-limit-quota-exhausted/i.test(errMsg) ||
          // Anthropic transport drop (#4002) — same mid-stage death class as
          // stream-idle-timeout: work already on disk must survive the blip.
          /socket connection was closed/i.test(errMsg) ||
          /socket hang up/i.test(errMsg));
      isAlreadyResolved = result.outcomeType === "already-resolved";

      this.logger.info("[SlotLifecycle] runPipeline() RESOLVED", {
        slotIndex: slot.index,
        issueNumber: slot.issueNumber,
        success: result.success,
        failedStage: result.failedStage,
        durationMs: Date.now() - startMs,
      });

      // Extract cost from per-slot state for health snapshot recording
      const slotState = await slot.stateService.getState();
      const slotCostUsd = slotState?.tokens?.estimated_cost_usd ?? 0;
      // Issue #3704: pass full token breakdown so onSlotCompleted can write a
      // complete execution-history record (not just the bare cost scalar).
      const slotTokens = {
        input: slotState?.tokens?.input ?? 0,
        output: slotState?.tokens?.output ?? 0,
        cacheRead: slotState?.tokens?.cacheRead ?? 0,
        cacheCreation: slotState?.tokens?.cacheCreation ?? 0,
        estimated_cost_usd: slotCostUsd,
      };

      if (result.success) {
        this.logger.info("Concurrent pipeline slot completed successfully", {
          slotIndex: slot.index,
          issueNumber: slot.issueNumber,
          durationMs: result.totalDurationMs,
          costUsd: slotCostUsd,
        });
        this.callbacks.onSlotCompleted?.(
          slot.index,
          slot.issueNumber,
          result,
          slotTokens,
          slot.repo
        );
      } else if (result.deferred) {
        // #305: pickup deferred on open blockedBy dependencies. NOT a failure —
        // route to the deferral callback (frees the Go slot with a non-failure
        // `blocked_dependency` signal, keeps the issue eligible) and skip the
        // failure UI/notification/pause entirely. The `finally` block's failure
        // side-effects are gated on `!pipelineDeferred`.
        this.logger.info("Concurrent pipeline slot deferred — open blockedBy dependencies", {
          slotIndex: slot.index,
          issueNumber: slot.issueNumber,
          costUsd: slotCostUsd,
        });
        this.callbacks.onSlotDeferred?.(
          slot.index,
          slot.issueNumber,
          result,
          slotCostUsd,
          slot.repo
        );
      } else if (slot.userCancelled) {
        // User-initiated cancellation (per-slot Stop button or Stop All). This
        // is NOT a pipeline failure — surface it as a clean cancellation so
        // the user doesn't see a misleading "Pipeline failed at X" modal for
        // their own deliberate action.
        this.logger.info("Concurrent pipeline slot cancelled by user", {
          slotIndex: slot.index,
          issueNumber: slot.issueNumber,
          stageWhenCancelled: result.failedStage,
          costUsd: slotCostUsd,
        });
        this.callbacks.onSlotFailed?.(
          slot.index,
          slot.issueNumber,
          new Error(`Cancelled by user`),
          slotCostUsd,
          slot.repo
        );
      } else {
        this.logger.warn("Concurrent pipeline slot failed", {
          slotIndex: slot.index,
          issueNumber: slot.issueNumber,
          failedStage: result.failedStage,
          costUsd: slotCostUsd,
        });

        // Circuit breaker: if the slot failed at issue-pickup with an auth
        // error (Claude Code session expired), trip the breaker to prevent
        // burning remaining slots on the same auth failure.
        if (result.failedStage === "issue-pickup" && result.totalDurationMs < 10_000) {
          const errorStr = result.error?.message ?? result.error?.toString() ?? "";
          if (
            errorStr.includes("authentication_failed") ||
            errorStr.includes("Not logged in") ||
            errorStr.includes("apiKeySource") ||
            errorStr.includes("Please run /login")
          ) {
            this.tripAuthCircuitBreaker(errorStr);
          }
        }

        // Blocked terminal state (#190): a run that did not deliver its PR
        // must be impossible to mistake for success OR for a generic crash —
        // surface the blocker classification and the remediation the stage
        // already computed.
        const slotError = result.blocked
          ? new Error(
              `BLOCKED — PR ${result.blocked.prNumber ? `#${result.blocked.prNumber} ` : ""}unmerged: ` +
                `${result.blocked.blocker}.` +
                (result.blocked.remediation ? ` Remediation: ${result.blocked.remediation}` : "")
            )
          : (result.error ?? new Error(`Pipeline failed at ${result.failedStage}`));

        this.callbacks.onSlotFailed?.(
          slot.index,
          slot.issueNumber,
          slotError,
          slotCostUsd,
          slot.repo
        );
      }

      return result;
    } catch (error) {
      // Extract cost even on throw — may have partial data
      const throwState = await slot.stateService.getState().catch(() => null);
      const throwCostUsd = throwState?.tokens?.estimated_cost_usd ?? 0;

      this.logger.error("[SlotLifecycle] runPipeline() THREW", {
        slotIndex: slot.index,
        issueNumber: slot.issueNumber,
        error: error instanceof Error ? error.message : "Unknown error",
        durationMs: Date.now() - startMs,
      });
      this.callbacks.onSlotFailed?.(
        slot.index,
        slot.issueNumber,
        error instanceof Error ? error : new Error("Pipeline execution failed"),
        throwCostUsd,
        slot.repo
      );
      throw error;
    } finally {
      this.logger.info("[SlotLifecycle] FINALLY block entered", {
        slotIndex: slot.index,
        issueNumber: slot.issueNumber,
        pipelineSucceeded,
        slotsBeforeCleanup: this.slots.size,
        isShuttingDown: this.isShuttingDown,
      });

      // Safety net: move board status to "In review" on pipeline failure.
      // HeadlessOrchestrator.markStatusInReviewOnFailure() handles most cases,
      // but it can be skipped by early returns (epic detection, closed issue,
      // auth failure) or fail silently for cross-repo items that resolve the
      // wrong workspace root. This catch-all ensures no failed issue gets
      // permanently stuck at "In progress" AND no failed issue silently
      // re-enters the Ready tree for re-dequeue.
      // @see Issue #563 post-mortem, Issue #2967
      // User-cancelled slots skip this — the issue should stay at its current
      // status (typically "In progress") so the user can resume cleanly.
      // Deferred slots skip this too (#305): a blockedBy deferral is not a
      // failure — the issue stays Ready/eligible, not moved to In review.
      if (!pipelineSucceeded && !pipelineDeferred && !this.isShuttingDown && !slot.userCancelled) {
        try {
          // Use the slot's worktreeManager repo root — this is already resolved
          // to the correct repo for cross-repo items (not the workspace default).
          const cwd =
            slot.worktreeManager.getRepoRoot() ||
            vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ||
            "";
          if (cwd) {
            const revertResult = await updateProjectItemStatus(
              slot.issueNumber,
              "In review",
              cwd,
              this.logger
            );
            if (revertResult.success) {
              this.logger.info(
                "Safety net: moved board status to In review after pipeline failure",
                { issueNumber: slot.issueNumber, repo: slot.repo }
              );
            }
            // Silent on failure — HeadlessOrchestrator may have already moved it
          }
        } catch {
          // Best effort — never block cleanup
        }
      }

      // Post a diagnostic comment on the GitHub issue so failures are visible
      // and actionable without digging through local logs. Skip when the user
      // cancelled — they don't need a public comment narrating their own stop.
      // Also skip transient network blips (#4002): they auto-retry without
      // operator action, the comment's recommendations don't apply, and during
      // a GitHub outage the post can't succeed anyway (the original incident
      // tried to post "run `gh auth login`" over the dead network).
      if (
        !pipelineSucceeded &&
        !pipelineDeferred &&
        !this.isShuttingDown &&
        !slot.userCancelled &&
        pipelineResult &&
        !isTransientNetworkFailureText(pipelineResult.error?.message ?? "")
      ) {
        try {
          const slotState = await slot.stateService.getState().catch(() => null);
          const commentCwd = slot.worktreeManager.getRepoRoot();
          await postFailureComment({
            issueNumber: slot.issueNumber,
            result: pipelineResult,
            state: slotState,
            repoOverride: slot.repo,
            cwd: commentCwd,
            logger: this.logger,
          });
        } catch {
          // Best effort — never block cleanup
        }
      }

      // Clean up the slot — preserve worktree if budget was exceeded (Issue #1935)
      // or if pr-merge failed (Issue #500). Preserving the worktree on pr-merge
      // failure keeps pipeline context files intact so that re-queuing the issue
      // can resume from pr-merge instead of re-running all stages from scratch.
      const preserveWorktree =
        pipelineBudgetExceeded || pipelineFailedAtPrMerge || pipelineFailedAtStreamIdleTimeout;
      // #3969: delete the local feature branch only on a clean success (PR
      // merged). On failure/cancel the branch is preserved for resume/recovery.
      const deleteMergedBranch = pipelineSucceeded && !slot.userCancelled;
      await this.cleanupSlot(slot, preserveWorktree, deleteMergedBranch);

      // If the pipeline failed and was part of a sequential epic, drain all
      // successor items from the queue. Without this, fillSlots() would dequeue
      // the next epic issue even though the predecessor failed — leading to
      // merge conflicts and wasted compute.
      // Skip drain when the issue was already closed (already-resolved) — that
      // means the work is done and successors should proceed, not be drained.
      // Also skip when the user cancelled — the user can manually drain or
      // resume; auto-draining is the wrong default for deliberate stops.
      // @see Issue #1819 - sequential epic ordering
      // Skip on a deferral (#305): the predecessor didn't fail, so successors
      // stay queued — draining them would strand work behind a transient block.
      if (
        !pipelineSucceeded &&
        !pipelineDeferred &&
        !isAlreadyResolved &&
        !this.isShuttingDown &&
        !slot.userCancelled
      ) {
        try {
          const drained = await this.queueService.drainBlockedSuccessors(
            slot.issueNumber,
            slot.epicOrder
          );
          if (drained.length > 0) {
            this.logger.info("Drained blocked successor issues after slot failure", {
              failedIssue: slot.issueNumber,
              drainedIssues: drained,
              drainedCount: drained.length,
            });
          }
        } catch (drainError) {
          this.logger.warn("Failed to drain blocked successors", {
            failedIssue: slot.issueNumber,
            error: drainError instanceof Error ? drainError.message : "Unknown error",
          });
        }
      }

      // Route post-cleanup behavior based on slot outcome.
      //
      // On SUCCESS: fill the now-available slot with the next queued issue.
      //
      // On FAILURE (stall-killed, stage error, network drop, etc.): do NOT
      // auto-continue. Clear the pending queue and surface a modal so the
      // user is aware and can triage. Without this, a single failure would
      // silently auto-start the next epic sibling, producing cross-issue
      // merge conflicts — see Issue #2967.
      //
      // Running slots that are still alive are NOT aborted here; they drain
      // naturally. Only future fills are suppressed.
      //
      // A DEFERRAL (#305) is not a failure: fall through to fillSlots() so the
      // scheduler continues to the next candidate instead of halting the queue
      // / pausing autonomous. The onSlotDeferred handler already freed the Go
      // slot with a non-failure `blocked_dependency` signal.
      if (!pipelineSucceeded && !pipelineDeferred && !this.isShuttingDown && !slot.userCancelled) {
        await this.haltQueueOnSlotFailure(slot, pipelineResult);
      } else if (!this.isShuttingDown) {
        await this.fillSlots();
      }

      // If this was a "stop after current" drain (pauseFilling, not abortAll),
      // reset isShuttingDown once the last slot finishes so new issues can be
      // enqueued immediately without requiring a window reload.
      if (
        this.slots.size === 0 &&
        !this.isFilling &&
        this.isShuttingDown &&
        !this.isAbortAllInProgress
      ) {
        this.logger.info("[SlotLifecycle] Drain complete — resetting shutdown guard", {
          issueNumber: slot.issueNumber,
        });
        this.isShuttingDown = false;
        void vscode.commands.executeCommand(
          "setContext",
          "nightgauge.stopAfterCurrentQueue",
          false
        );
      }

      // Check if all done — only fire when no slots remain AND no fillSlots
      // is in progress (which may be about to create new slots from the queue).
      // Skip during shutdown: user pressed stop, don't trigger epic sweep.
      if (this.slots.size === 0 && !this.isFilling && !this.isShuttingDown) {
        this.logger.info("[SlotLifecycle] ALL SLOTS DONE → onAllComplete", {
          issueNumber: slot.issueNumber,
          pipelineSucceeded,
          durationMs: Date.now() - startMs,
        });
        this.callbacks.onAllComplete?.();
      }
    }
  }

  /**
   * Clean up a completed/failed slot.
   *
   * @param slot - The pipeline slot to clean up
   * @param preserveWorktree - If true, skip worktree removal (e.g., budget-exceeded
   *   failures where WIP was auto-committed and should be inspectable). The branch
   *   and worktree remain for manual inspection or pipeline retry.
   *   @see Issue #1935 - Budget-pause instead of budget-kill
   */
  /**
   * Trip the auth circuit breaker when Claude Code session has expired.
   * Prevents burning remaining slots on the same auth failure and shows
   * a clear notification to the user with remediation steps.
   * @see Issue #2350 - Surface Claude Code auth expiry to user
   */
  private tripAuthCircuitBreaker(errorStr: string): void {
    if (this.authCircuitOpen) return; // already tripped
    this.authCircuitOpen = true;

    this.logger.error("[AuthCircuitBreaker] Claude Code session expired — halting all slots", {
      errorSnippet: errorStr.slice(0, 200),
    });

    // Show prominent notification with action button
    const loginAction = "Open Terminal";
    void vscode.window
      .showErrorMessage(
        "Claude Code session expired — pipeline slots halted. " +
          'Please run "claude" in a terminal and log in, then retry.',
        loginAction
      )
      .then((choice) => {
        if (choice === loginAction) {
          const terminal = vscode.window.createTerminal("Claude Login");
          terminal.show();
          terminal.sendText("claude");
        }
        // Reset the circuit breaker after user acknowledges
        this.authCircuitOpen = false;
      });
  }

  private async cleanupSlot(
    slot: PipelineSlot,
    preserveWorktree = false,
    deleteBranch = false
  ): Promise<void> {
    this.slots.delete(slot.issueNumber);
    this.emitSlotsChanged();

    // Dispose the per-slot state service to release its EventEmitter resources
    try {
      slot.stateService.dispose();
    } catch {
      // Best effort
    }

    if (preserveWorktree) {
      this.logger.info("Preserving worktree — context files kept for resume on re-queue", {
        issueNumber: slot.issueNumber,
        worktreePath: slot.worktreeManager.getWorktreePath(slot.issueNumber),
      });
      this.callbacks.onSlotCleaned?.(slot.index, slot.issueNumber);
    } else {
      try {
        // #3969: on a SUCCESSFUL pipeline (PR merged) tear down the local branch
        // too — WorktreeManager.cleanup removes the worktree first, then runs
        // `git branch -D` (a live worktree blocks the branch delete, so order
        // matters). The remote head is already deleted by pr-merge's
        // `--delete-branch`. On FAILURE deleteBranch is false so the branch is
        // preserved for resume/recovery. Without this, merged feature branches
        // accumulated indefinitely (142 stale locals across AcmeApp).
        // Use the slot's own worktreeManager (not this.worktreeManager) so
        // cleanup targets the correct repo even after updateRepoRoot().
        await slot.worktreeManager.cleanup(slot.issueNumber, deleteBranch);
        this.callbacks.onSlotCleaned?.(slot.index, slot.issueNumber);
      } catch (error) {
        this.logger.warn("Failed to clean up worktree after pipeline", {
          issueNumber: slot.issueNumber,
          error: error instanceof Error ? error.message : "Unknown error",
        });
      }
    }

    // Update queue state with current active slots
    try {
      await this.queueService.updateActiveSlots(this.getActiveSlots());
    } catch {
      // Non-critical
    }
  }

  /**
   * Halt the queue in response to a slot failure and notify the user.
   *
   * After a pipeline failure the queue was previously auto-refilled via
   * `fillSlots()`, which meant a stall-kill on one epic child would silently
   * start the next epic child — ending in cross-issue merge conflicts because
   * some siblings had landed and others had failed halfway. See Issue #2967.
   *
   * Behavior:
   * - Clear the pending queue so no further `fillSlots()` dequeues the same
   *   batch. Currently-running slots are NOT cancelled; they finish on their
   *   own so in-flight work is not thrown away.
   * - Show a modal with action buttons (View Issue / Show Output) so the
   *   user is forced to acknowledge the failure before further work begins.
   *
   * Never throws — every step is best-effort.
   *
   * @see Issue #2967 - Pipeline failures silently reset issues to Ready and auto-continue the queue
   */
  /**
   * Reconcile a slot failure against the forge before halting the queue.
   *
   * Returns true when the issue is already CLOSED — i.e. the pipeline's work
   * landed (it closes the issue on merge) and the reported failure is a phantom
   * (the stage exited non-zero after the work shipped, or a spurious/duplicate
   * failure signal fired). In that case the queue must NOT be halted.
   *
   * Fail-safe by construction: returns false when the repo is unknown or any
   * forge query errors, so an uncertain check falls through to the normal halt
   * and a genuine failure is never masked. Only a positive, verified CLOSED
   * state suppresses the pause. #3835 / #3840.
   */
  private async isIssueResolvedOnForge(slot: PipelineSlot): Promise<boolean> {
    const repo = slot.repo;
    // Validate before shelling out. `execFile` (argv, no shell) already prevents
    // metacharacter injection, but reject anything that isn't a well-formed
    // owner/repo + integer issue number as defense-in-depth — and so a malformed
    // value fails closed (false → normal halt) rather than producing a bogus gh
    // call. repo comes from workspace config; issueNumber is typed `number` but
    // the runtime guard makes that guarantee explicit.
    if (!repo || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repo)) return false;
    if (!Number.isInteger(slot.issueNumber)) return false;
    try {
      const { stdout } = await execFileAsync(
        "gh",
        ["issue", "view", String(slot.issueNumber), "--repo", repo, "--json", "state"],
        { timeout: 15_000 }
      );
      const state = String((JSON.parse(stdout) as { state?: string }).state ?? "").toUpperCase();
      return state === "CLOSED";
    } catch {
      return false;
    }
  }

  /**
   * Reconcile a slot failure against the branch's PR on the forge.
   *
   * Returns true when an OPEN or MERGED PR exists for the slot's feature branch
   * — i.e. the work has progressed into review (OPEN) or already landed
   * (MERGED), so a reported failure on this issue is a phantom even though the
   * issue itself is still OPEN. This is the Case 2 gap: issue #35 was OPEN with
   * an open PR, so the issue-CLOSED-only check missed it and the page fired
   * despite a `success:true` pr-create (#3873).
   *
   * A CLOSED-but-not-merged PR (abandoned branch) does NOT count — that is a
   * genuinely-incomplete issue.
   *
   * Fail-safe by construction: returns false on a malformed repo/branch or any
   * forge query error, so an uncertain check falls through to the normal halt
   * and a genuine failure is never masked.
   */
  private async isBranchPrLandedOnForge(slot: PipelineSlot): Promise<boolean> {
    const repo = slot.repo;
    const branch = slot.worktree?.branch;
    // Defense-in-depth argv validation (execFile is shell-free, but reject
    // malformed values so they fail closed rather than producing a bogus call).
    if (!repo || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repo)) return false;
    if (!branch || !/^[A-Za-z0-9_.\-/]+$/.test(branch)) return false;
    try {
      const { stdout } = await execFileAsync(
        "gh",
        [
          "pr",
          "list",
          "--repo",
          repo,
          "--head",
          branch,
          "--state",
          "all",
          "--json",
          "state",
          "--limit",
          "10",
        ],
        { timeout: 15_000 }
      );
      const prs = JSON.parse(stdout) as Array<{ state?: string }>;
      if (!Array.isArray(prs)) return false;
      return prs.some((pr) => {
        const state = String(pr.state ?? "").toUpperCase();
        return state === "MERGED" || state === "OPEN";
      });
    } catch {
      return false;
    }
  }

  /**
   * Reconcile a slot failure against the failed stage's own exit-record.
   *
   * The Go scheduler writes one `StageExitRecord` per stage to
   * `.nightgauge/pipeline/exit-records/<UTC-day>.jsonl` carrying a
   * `success` flag (`scheduler_exit_record.go`). The notifier is a SEPARATE
   * paging surface from that writer — Case 2 paged "failed at pr-create" while
   * the pr-create exit-record said `success:true`. Reading the record directly
   * is the most authoritative local signal: if the stage's latest record proves
   * success, the page is a phantom and must be suppressed (#3873).
   *
   * Matches the LATEST record for `{issueNumber, stage}` across the current and
   * previous UTC day (records are append-only, newest-last) so a same-stage
   * success from this run is found even just after UTC midnight.
   *
   * Fail-safe: returns false when the stage is unknown, no record matches, or
   * any read/parse error occurs — uncertainty never suppresses a page.
   */
  private async exitRecordSaysSuccess(slot: PipelineSlot, failedStage: string): Promise<boolean> {
    if (!failedStage || failedStage === "unknown") return false;
    // Exit-records are written by Go to the WORKSPACE/REPO ROOT (Go's
    // srv.workspaceRoot / scheduler workspaceRoot — see
    // internal/ipc/diagnostics_stage_exit.go:34 and scheduler_exit_record.go),
    // NOT the per-issue worktree checkout. Use the slot's repo root (the same
    // value the failure-comment + cwd paths use at lines ~978/1007) so the read
    // lands on the file Go actually wrote. Fall back to this.repoRoot.
    const root = slot.worktreeManager?.getRepoRoot() || this.repoRoot;
    if (!root) return false;

    const dir = path.join(root, ".nightgauge", "pipeline", "exit-records");
    const dayFiles = this.recentExitRecordDayFiles();

    let latestSuccess: boolean | undefined;
    for (const day of dayFiles) {
      let content: string;
      try {
        content = await fs.readFile(path.join(dir, `${day}.jsonl`), "utf8");
      } catch {
        continue; // missing day file is normal — try the next
      }
      for (const line of content.split("\n")) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        let rec: { issue?: number; stage?: string; success?: boolean };
        try {
          rec = JSON.parse(trimmed) as typeof rec;
        } catch {
          continue; // skip a malformed line rather than failing the whole read
        }
        if (rec.issue === slot.issueNumber && rec.stage === failedStage) {
          // Append-only file → later lines are newer. Keep overwriting so the
          // final assignment is the latest record for this {issue, stage}.
          latestSuccess = rec.success === true;
        }
      }
    }
    return latestSuccess === true;
  }

  /**
   * UTC day stamps (YYYY-MM-DD) for yesterday then today, matching the Go
   * `DailyFilePath` format (`time.Now().UTC().Format("2006-01-02")`). Two days
   * covers the just-after-midnight case where a success record landed on the
   * previous UTC day.
   *
   * Order is [yesterday, today] ON PURPOSE: the caller overwrites its
   * latest-success accumulator while iterating files in order, so today's
   * records must be processed LAST to win over a same-{issue,stage} record from
   * yesterday (e.g. yesterday's failed first attempt vs today's successful
   * retry). `toISOString()` is always UTC, so this stays aligned with the Go
   * writer — do NOT switch to local-time date math.
   */
  private recentExitRecordDayFiles(): string[] {
    const today = new Date();
    const yesterday = new Date(today.getTime() - 24 * 60 * 60 * 1000);
    const stamp = (d: Date) => d.toISOString().slice(0, 10);
    return [stamp(yesterday), stamp(today)];
  }

  /**
   * Combined reconciliation gate for the operator page (#3873). Suppress the
   * page when ANY of these prove the work is not a genuine failure:
   *
   *   - the issue is CLOSED on the forge (existing #3835/#3840 check), OR
   *   - the branch's PR is open/merged on the forge (Case 2 — issue still open
   *     but work in review/landed), OR
   *   - the failed stage's latest exit-record says `success:true` (Case 2 — a
   *     `success:true` pr-create can never page "failed at pr-create").
   *
   * Every component check fails closed, so the combined gate fails closed: it
   * suppresses ONLY on a positive, verified signal.
   */
  private async shouldSuppressFailurePage(
    slot: PipelineSlot,
    failedStage: string
  ): Promise<boolean> {
    if (await this.exitRecordSaysSuccess(slot, failedStage)) return true;
    if (await this.isIssueResolvedOnForge(slot)) return true;
    if (await this.isBranchPrLandedOnForge(slot)) return true;
    return false;
  }

  private async haltQueueOnSlotFailure(
    slot: PipelineSlot,
    pipelineResult: PipelineRunResult | undefined
  ): Promise<void> {
    try {
      // #3444: Skip the halt for environmental terminal kinds — failures
      // caused by upstream conditions (Anthropic API quota, idle stream
      // timeouts mid-token-output, extended GitHub connectivity loss) that
      // the autonomous scheduler already auto-recovers from via per-issue
      // backoff + the global quota cooldown set in onPipelineComplete. The
      // halt is meant to surface REAL bugs (validation errors, subagent
      // crashes, stall kills, gate failures) where the user must triage
      // before the queue auto-continues. Tripping it on environmental
      // kinds forces the user to manually click Resume after the cooldown
      // expires (~4h for a quota miss), which defeats the purpose of the
      // environmental classification path.
      //
      // Match strings are the same patterns used in bootstrap/services.ts
      // (terminalFailureKind classification) and runSlotPipeline()
      // (pipelineFailedAtStreamIdleTimeout). Keep aligned.
      const haltErrMsg = pipelineResult?.error?.message ?? "";
      const isEnvironmentalFailure =
        /stream idle timeout/i.test(haltErrMsg) ||
        /rate-limit-quota-exhausted/i.test(haltErrMsg) ||
        /rate_limit_quota_exhausted/i.test(haltErrMsg) ||
        // Anthropic session/usage limit — transient, recovers at reset. #3792.
        // (Normalized to the quota-exhausted marker in skillRunner; matched
        // raw here as defense-in-depth for non-stream-json error paths.)
        /\b(?:session|usage)\s+limit\b/i.test(haltErrMsg) ||
        /network unavailable: extended github connectivity loss/i.test(haltErrMsg);
      if (isEnvironmentalFailure) {
        this.logger.info(
          "Skipping haltQueueOnSlotFailure — environmental failure auto-retries via cooldown",
          {
            failedIssue: slot.issueNumber,
            errSnippet: haltErrMsg.slice(0, 200),
          }
        );
        return;
      }

      // Anthropic API 529 "Overloaded" is a transient capacity blip — nothing
      // is wrong in our code or the issue, and it clears within minutes. The Go
      // scheduler already classifies it as TerminalKindApiOverloaded and
      // auto-recovers: per-issue 5-minute backoff, board→Ready, NO lifetime-cap
      // increment, NO global cooldown, and — per its own log — explicitly NO
      // queue pause. Without this branch the 529 fell through to the halt path
      // below, which cleared the queue and called autonomousPause(), OVERRIDING
      // the Go layer's "no pause" decision and forcing a manual Resume after a
      // momentary overload (the original incident: acmeapp #100 paused the
      // whole queue while #34/#85 — same 529 window — correctly retried). Skip
      // the halt and surface a non-blocking toast so the operator sees the
      // retry without the queue grinding to a stop; the issue is already
      // surfaced in the Autonomous panel's retry list by Go's recordFailure.
      // Match string mirrors Go's ClassifyTerminalKind (strings.Contains
      // "overloaded") against the 529 result envelope ("API Error: 529
      // Overloaded" / "API Error: Overloaded"). It is NOT folded into
      // isEnvironmentalFailure because that path returns silently; an overload
      // deserves the same visible-but-non-blocking treatment as a stall-kill.
      const isApiOverloaded = /overloaded/i.test(haltErrMsg);
      if (isApiOverloaded) {
        const failedStage = pipelineResult?.failedStage ?? "unknown";
        this.logger.info(
          "Skipping haltQueueOnSlotFailure — Anthropic API 529 overload is transient, Go layer retries with backoff",
          {
            failedIssue: slot.issueNumber,
            failedStage,
            errSnippet: haltErrMsg.slice(0, 200),
          }
        );
        void vscode.window.showWarningMessage(
          `Nightgauge: Issue #${slot.issueNumber} hit an Anthropic API overload (529) at ${failedStage} — will retry automatically in ~5 min.`
        );
        return;
      }

      // Transient network blip (#4002): an Anthropic transport drop
      // (api_connection_lost) or GitHub unreachable at pipeline-start
      // (github_network_outage). The Go scheduler auto-recovers both —
      // short per-issue backoff / global cooldown, board→Ready, no
      // lifetime-cap increment, explicitly no pause. Same
      // visible-but-non-blocking treatment as the 529 branch above: the
      // original incident paused the whole queue and paged the operator
      // over a 4-second DNS blip.
      if (isTransientNetworkFailureText(haltErrMsg)) {
        const failedStage = pipelineResult?.failedStage ?? "unknown";
        this.logger.info(
          "Skipping haltQueueOnSlotFailure — transient network blip, Go layer retries with backoff/cooldown",
          {
            failedIssue: slot.issueNumber,
            failedStage,
            errSnippet: haltErrMsg.slice(0, 200),
          }
        );
        void vscode.window.showWarningMessage(
          `Nightgauge: Issue #${slot.issueNumber} hit a network blip at ${failedStage} — will retry automatically once connectivity recovers.`
        );
        return;
      }

      // Stall-kills are transient — the agent exceeded its idle or hard-cap
      // threshold, not a code defect. The Go layer already reverts the issue
      // to Ready and applies a 30-minute backoff; halting the queue, clearing
      // pending items, and showing a blocking modal on top of that forces
      // manual intervention for what is essentially an infrastructure hiccup.
      // Show a non-blocking warning toast instead so the user is aware, then
      // let autonomous continue working on other ready issues uninterrupted.
      const isStallKill =
        /exceeded stall idle threshold/i.test(haltErrMsg) ||
        /\[stall-killed\]/i.test(haltErrMsg) ||
        /stall-killed/i.test(haltErrMsg) ||
        /stall kill threshold/i.test(haltErrMsg) ||
        /stalled and killed/i.test(haltErrMsg) ||
        /heartbeat stall/i.test(haltErrMsg) ||
        /exceeded stage_hard_cap/i.test(haltErrMsg) ||
        // Issue #3508: runaway ceiling kills are treated as stall-kills —
        // no queue halt, no autonomous pause, 30m backoff via Go layer.
        /\[runaway-ceiling-exceeded\]/i.test(haltErrMsg) ||
        /runaway-ceiling-exceeded/i.test(haltErrMsg) ||
        /runaway cost ceiling exceeded/i.test(haltErrMsg);
      if (isStallKill) {
        const failedStage = pipelineResult?.failedStage ?? "unknown";
        this.logger.info(
          "Skipping haltQueueOnSlotFailure — stall-kill is transient, Go layer will retry with backoff",
          {
            failedIssue: slot.issueNumber,
            failedStage,
            errSnippet: haltErrMsg.slice(0, 200),
          }
        );
        void vscode.window.showWarningMessage(
          `Nightgauge: Issue #${slot.issueNumber} stalled at ${failedStage} — will retry automatically in 30 min.`
        );
        return;
      }

      // Architecture-approval pause (#4222): a deliberate, per-issue,
      // human-owned decision point — NOT a failure. The orchestrator halted
      // BEFORE feature-dev (zero dev/validate spend), the outcome classifier
      // records it as productive, the board is moved to "In review", and the
      // approval-aware failure comment (failureComment.ts keys off this
      // marker) tells the human exactly how to approve. Halting the whole
      // queue and pausing autonomous here turned one issue's "waiting for a
      // human" into a full stop for every other ready issue (observed in a
      // production autonomous run).
      // Surface a visible-but-non-blocking toast and keep the queue flowing;
      // the issue re-enters when a human adds `approved:architecture` (or the
      // approval file) and re-queues it.
      if (haltErrMsg.includes(ARCHITECTURE_APPROVAL_REQUIRED_MARKER)) {
        this.logger.info(
          "Skipping haltQueueOnSlotFailure — architecture-approval pause is an actionable human decision, not a failure",
          {
            failedIssue: slot.issueNumber,
            errSnippet: haltErrMsg.slice(0, 200),
          }
        );
        // One-click GUI approval — the whole point of the gate is a HUMAN
        // decision, so the human gets a real affordance, not a cryptic error.
        void this.offerArchitectureApproval(slot);
        return;
      }

      // #3835/#3840/#3873: reconcile against the forge AND the exit-record
      // before halting. The dominant false-alarm class is a stage that exits
      // non-zero (or fires a spurious / duplicate failure signal) AFTER its work
      // already landed — e.g. pr-merge recorded failed 12s after the PR merged
      // (#3806), or a phantom failure with an empty terminal_kind. A separate
      // #3873 regression: this notifier paged "failed at pr-create" while the
      // pr-create exit-record said success:true and an OPEN PR existed — the
      // old issue-CLOSED-only check missed both signals. Pausing the whole queue
      // and paging the operator on completed work is the core pain (#3835).
      // shouldSuppressFailurePage now suppresses when ANY of: issue CLOSED, the
      // branch PR is open/merged, or the failed stage's exit-record says
      // success:true. Fail-safe: every component check returns false on error →
      // normal halt, so a genuine failure is never masked on uncertainty.
      const reconcileStage = pipelineResult?.failedStage ?? "unknown";
      if (await this.shouldSuppressFailurePage(slot, reconcileStage)) {
        this.logger.info(
          "Skipping haltQueueOnSlotFailure — work landed / stage succeeded (issue closed, branch PR open/merged, or exit-record success:true); phantom failure",
          {
            failedIssue: slot.issueNumber,
            failedStage: reconcileStage,
            errSnippet: haltErrMsg.slice(0, 200),
          }
        );
        return;
      }

      const drainedBefore = await this.queueService.getQueue().catch(() => null);
      const pendingCount =
        drainedBefore?.items.filter((i) => i.status === "pending" || i.status === "ready").length ??
        0;

      if (pendingCount > 0) {
        try {
          await this.queueService.clear();
          this.logger.info(
            "Queue cleared after slot failure — pending items require user acknowledgement before auto-continuing",
            {
              failedIssue: slot.issueNumber,
              pendingCleared: pendingCount,
            }
          );
        } catch (clearError) {
          this.logger.warn("Failed to clear queue after slot failure", {
            failedIssue: slot.issueNumber,
            error: clearError instanceof Error ? clearError.message : String(clearError),
          });
        }
      }

      // #3020: Clearing the local TypeScript queue is not enough — Go's
      // autonomous scheduler runs independently and will keep dispatching new
      // candidates from the project board, ignoring the local queue clear.
      // Without this pause, the user sees "no further issues will start
      // automatically" while autonomous keeps burning runs (~$92 in the
      // original incident). Pause is best-effort: a transient IPC failure
      // shouldn't block the user-facing notification, and Go's safety rails
      // remain a backstop.
      let autonomousPaused = false;
      try {
        const ipc = IpcClient.getInstance();
        const status = await ipc.autonomousStatus();
        if (status.status === "running") {
          const failedStage = pipelineResult?.failedStage ?? "unknown";
          await ipc.autonomousPause(
            `haltQueueOnSlotFailure: issue #${slot.issueNumber} failed at ${failedStage}`,
            "haltQueueOnSlotFailure"
          );
          autonomousPaused = true;
          this.logger.info("Autonomous mode paused after slot failure", {
            failedIssue: slot.issueNumber,
          });
        }
      } catch (pauseError) {
        this.logger.warn("Failed to pause autonomous mode after slot failure", {
          failedIssue: slot.issueNumber,
          error: pauseError instanceof Error ? pauseError.message : String(pauseError),
        });
      }

      const failedStage = pipelineResult?.failedStage ?? "unknown";
      const slotState = await slot.stateService.getState().catch(() => null);
      const costUsd = slotState?.tokens?.estimated_cost_usd ?? 0;
      const repo = slot.repo ?? "";
      const issueUrl = repo ? `https://github.com/${repo}/issues/${slot.issueNumber}` : undefined;

      const queuePart =
        pendingCount > 0
          ? `Queue cleared (${pendingCount} pending item${pendingCount === 1 ? "" : "s"} removed). `
          : "";
      const autonomousPart = autonomousPaused
        ? "Autonomous mode paused. Resume from the Autonomous panel after triage."
        : "Triage this failure, then re-queue or resume autonomous to continue.";
      const detail =
        `Issue #${slot.issueNumber} failed at ${failedStage}` +
        (costUsd > 0 ? ` — $${costUsd.toFixed(2)} spent.` : ".") +
        `\n\n${queuePart}${autonomousPart}`;

      const viewIssueAction = issueUrl ? "View Issue" : undefined;
      const showOutputAction = "Show Output";
      const actions = [viewIssueAction, showOutputAction].filter(
        (s): s is string => typeof s === "string"
      );

      // Fire-and-forget — the modal blocks the user but not the finally block.
      void vscode.window
        .showErrorMessage(
          `Nightgauge pipeline halted — failure on #${slot.issueNumber}`,
          { modal: true, detail },
          ...actions
        )
        .then((choice) => {
          if (choice === viewIssueAction && issueUrl) {
            void vscode.env.openExternal(vscode.Uri.parse(issueUrl));
          } else if (choice === showOutputAction) {
            void vscode.commands.executeCommand("nightgauge.showOutputWindow");
          }
        });
    } catch (haltError) {
      this.logger.warn("haltQueueOnSlotFailure encountered an unexpected error", {
        failedIssue: slot.issueNumber,
        error: haltError instanceof Error ? haltError.message : String(haltError),
      });
    }
  }

  /**
   * One-click GUI affordance for the architecture-approval gate (#4222).
   *
   * The gate exists so a HUMAN reviews high-impact decisions before
   * feature-dev spends anything — so the human must get a real approval
   * control, not a cryptic "failed at feature-dev". Shows an actionable
   * notification:
   *
   *   - "Approve & Re-queue" — adds the `approved:architecture` label (the
   *     deterministic gate's approval evidence; label created if the repo
   *     lacks it), moves the board item back to Ready, and re-enqueues the
   *     issue so the pipeline re-runs and passes the gate.
   *   - "Open Issue" — opens the GitHub issue (which carries the
   *     approval-aware comment with full context) for review first.
   *
   * Every step is best-effort with a precise fallback message — a failed
   * `gh` call degrades to the manual instructions, never a silent no-op.
   */
  private async offerArchitectureApproval(slot: PipelineSlot): Promise<void> {
    const approveAction = "Approve & Re-queue";
    const openAction = "Open Issue";
    const choice = await vscode.window.showWarningMessage(
      `Nightgauge: Issue #${slot.issueNumber} needs architecture approval before ` +
        `implementation (high-impact change). The queue continues with other issues.`,
      approveAction,
      openAction
    );

    if (choice === openAction && slot.repo) {
      void vscode.env.openExternal(
        vscode.Uri.parse(`https://github.com/${slot.repo}/issues/${slot.issueNumber}`)
      );
      return;
    }
    if (choice !== approveAction) return;

    const cwd =
      slot.worktreeManager.getRepoRoot() ||
      vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ||
      "";
    const repoArgs = slot.repo ? ["--repo", slot.repo] : [];
    try {
      // 1. Approval evidence: the `approved:architecture` label. Create it
      //    first in case the repo has never used the gate (create fails
      //    silently when it already exists).
      await execFileAsync(
        "gh",
        [
          "label",
          "create",
          "approved:architecture",
          ...repoArgs,
          "--color",
          "0e8a16",
          "--description",
          "Human-approved architectural decision — architecture gate passes",
        ],
        { cwd, timeout: 15_000 }
      ).catch(() => undefined);
      await execFileAsync(
        "gh",
        [
          "issue",
          "edit",
          String(slot.issueNumber),
          ...repoArgs,
          "--add-label",
          "approved:architecture",
        ],
        { cwd, timeout: 15_000 }
      );

      // 2. Board back to Ready so the autonomous scheduler can redispatch
      //    (the failure path parked it at "In review"). Best-effort.
      try {
        await updateProjectItemStatus(slot.issueNumber, "Ready", cwd, this.logger);
      } catch {
        // Non-fatal — the local re-enqueue below still runs the issue.
      }

      // 3. Local queue re-entry (cross-repo aware).
      const [owner, repo] = (slot.repo ?? "").split("/");
      await this.queueService.enqueue(
        slot.issueNumber,
        slot.title,
        undefined,
        undefined,
        owner && repo ? { repoOverride: { owner, repo } } : undefined
      );

      this.logger.info("Architecture approved from GUI — issue re-queued", {
        issueNumber: slot.issueNumber,
        repo: slot.repo,
      });
      void vscode.window.showInformationMessage(
        `Nightgauge: Issue #${slot.issueNumber} approved (label added) and re-queued.`
      );
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.logger.warn("GUI architecture approval failed", {
        issueNumber: slot.issueNumber,
        error: msg,
      });
      void vscode.window.showErrorMessage(
        `Nightgauge: Could not approve #${slot.issueNumber} automatically (${msg.slice(0, 120)}). ` +
          `Add the "approved:architecture" label on GitHub and re-queue the issue manually.`
      );
    }
  }

  /**
   * Whether the manager is currently in a shutdown/stop window.
   *
   * Consumed by IssueQueueService to reject enqueue attempts that arrive
   * after Stop All / Stop Queue After Current has been pressed (e.g.
   * autonomous.dispatch events emitted by Go between pauseFilling() and
   * the eventual resumeFilling() / end of abortAll()). Without this
   * guard, delayed dispatch events could re-populate the queue after
   * the user cleared it, defeating the Stop control.
   */
  get isShutdownInProgress(): boolean {
    return this.isShuttingDown;
  }

  /**
   * Temporarily prevent fillSlots() from dequeuing new items.
   * Used by the stop command to freeze the queue while the confirmation
   * dialog is visible — without this, dying slots refill before the user
   * can confirm, making "Stop" ineffective.
   */
  pauseFilling(): void {
    this.isShuttingDown = true;
  }

  /**
   * Resume filling after a cancelled stop confirmation.
   */
  resumeFilling(): void {
    this.isShuttingDown = false;
  }

  /**
   * Abort all running pipelines
   */
  async abortAll(): Promise<void> {
    this.isAbortAllInProgress = true;
    this.isShuttingDown = true;
    this.logger.info("Aborting all concurrent pipeline slots", {
      activeSlots: this.slots.size,
    });

    // Clear the queue first so no new items get dequeued by fillSlots
    try {
      await this.queueService.clear();
    } catch {
      // Best effort — queue clear is non-critical
    }

    // Stop all running orchestrators. Mark each slot as user-cancelled BEFORE
    // issuing the stop so the slot's runSlot completion handler treats the
    // cancellation as a deliberate user action, not a pipeline failure.
    for (const slot of this.slots.values()) {
      try {
        slot.userCancelled = true;
        slot.orchestrator.stop();
      } catch {
        // Best effort
      }
    }

    // Defense-in-depth: per-slot stop() already kills its tracked process, but
    // the global skillRunner registry can hold stale handles if a close event
    // was missed or a stage spawned auxiliary processes. Clear it as a backstop.
    try {
      killAllActiveProcesses();
    } catch {
      // Best effort
    }

    // Wait for fillSlots to finish if it's mid-worktree-creation, then wait
    // for all running slots. The isShuttingDown check in startSlot will prevent
    // the pending worktree from actually starting a pipeline.
    const waitForIdle = async () => {
      await this.waitForAll();
      // If fillSlots was in progress, it may have added new slots after
      // waitForAll returned — wait again until truly idle.
      while (this.isFilling || this.slots.size > 0) {
        await new Promise((r) => setTimeout(r, 100));
        await this.waitForAll();
      }
    };

    // Bound the wait — without a deadline, a stuck slot (e.g. mid-worktree
    // creation when the user disconnects) leaves isShuttingDown=true forever
    // and the shutdownGuard silently refuses every subsequent enqueue.
    // See Issue #3111.
    const TIMEOUT_SENTINEL = Symbol("abort-all-timeout");
    let timeoutHandle: NodeJS.Timeout | undefined;
    const timeoutPromise = new Promise<typeof TIMEOUT_SENTINEL>((resolve) => {
      timeoutHandle = setTimeout(() => resolve(TIMEOUT_SENTINEL), ABORT_ALL_TIMEOUT_MS);
    });
    try {
      const result = await Promise.race([waitForIdle(), timeoutPromise]);
      if (result === TIMEOUT_SENTINEL) {
        const stuckIssues = Array.from(this.slots.keys());
        this.logger.warn("abortAll exceeded deadline — force-clearing slots", {
          timeoutMs: ABORT_ALL_TIMEOUT_MS,
          stuckIssues,
          isFilling: this.isFilling,
        });
        // Best-effort second sweep before giving up — covers processes spawned
        // between the first kill and the timeout.
        try {
          killAllActiveProcesses();
        } catch {
          // Best effort
        }
        this.slots.clear();
        this.emitSlotsChanged();
        void vscode.window.showWarningMessage(
          `Stop took longer than ${Math.round(ABORT_ALL_TIMEOUT_MS / 1000)}s — force-cleared ${stuckIssues.length} stuck slot(s). Pipeline ready for new work.`
        );
      }
    } finally {
      if (timeoutHandle) clearTimeout(timeoutHandle);
      this.isShuttingDown = false;
      this.isAbortAllInProgress = false;
    }
  }

  /**
   * Abort a specific slot by issue number
   */
  abortSlot(issueNumber: number): boolean {
    const slot = this.slots.get(issueNumber);
    if (!slot) return false;

    this.logger.info("Aborting concurrent pipeline slot", {
      slotIndex: slot.index,
      issueNumber,
    });
    // Mark BEFORE issuing the stop so the slot's runSlot completion handler
    // (which fires asynchronously when orchestrator.stop() unwinds) can route
    // through the cancellation path instead of treating the cancel as a
    // real pipeline failure.
    slot.userCancelled = true;
    slot.orchestrator.stop();
    return true;
  }

  /**
   * Get all running slots that belong to a specific epic.
   *
   * @param epicNumber - The parent epic issue number
   * @returns Array of { issueNumber, title } for running slots in this epic
   *
   * @see Issue #2261 - Per-slot / per-epic pipeline controls
   */
  getSlotsByEpic(epicNumber: number): { issueNumber: number; title: string }[] {
    const result: { issueNumber: number; title: string }[] = [];
    for (const slot of this.slots.values()) {
      if (slot.epicNumber === epicNumber) {
        result.push({ issueNumber: slot.issueNumber, title: slot.title });
      }
    }
    return result;
  }

  /**
   * Abort all running slots that belong to a specific epic and drain
   * queued successor issues from that epic.
   *
   * Other running slots and non-epic queue items are unaffected.
   *
   * @param epicNumber - The parent epic issue number
   * @returns Number of slots that were stopped
   *
   * @see Issue #2261 - Per-slot / per-epic pipeline controls
   */
  async abortEpic(epicNumber: number): Promise<number> {
    const epicSlots = this.getSlotsByEpic(epicNumber);
    if (epicSlots.length === 0) {
      this.logger.info("No running slots found for epic", { epicNumber });
      return 0;
    }

    this.logger.info("Aborting all slots for epic", {
      epicNumber,
      slotCount: epicSlots.length,
      issues: epicSlots.map((s) => s.issueNumber),
    });

    // Stop each slot's orchestrator
    let stoppedCount = 0;
    for (const { issueNumber } of epicSlots) {
      if (this.abortSlot(issueNumber)) {
        stoppedCount++;
      }
    }

    // Drain queued items that belong to this epic so they don't
    // get dequeued by fillSlots() after the running slots die.
    try {
      const drained = await this.queueService.drainEpicItems(epicNumber);
      if (drained.length > 0) {
        this.logger.info("Drained queued epic items after abortEpic", {
          epicNumber,
          drainedIssues: drained,
          drainedCount: drained.length,
        });
      }
    } catch (error) {
      this.logger.warn("Failed to drain queued epic items", {
        epicNumber,
        error: error instanceof Error ? error.message : "Unknown error",
      });
    }

    return stoppedCount;
  }

  /**
   * Check if an issue is currently running in a slot
   */
  isRunning(issueNumber: number): boolean {
    return this.slots.has(issueNumber);
  }

  /**
   * Store a pending runId for an issue before fillSlots() creates the slot.
   * Applied when startSlot() creates the PipelineSlot for that issueNumber.
   * @see Issue #3552 — cancel command handler
   */
  setPendingRunId(issueNumber: number, runId: string): void {
    this.pendingRunIds.set(issueNumber, runId);
  }

  /**
   * Drop a pending runId that will never be consumed because the dispatch was
   * abandoned before a slot opened (e.g. an enqueue refused by the stop guard
   * after the ack already returned a runId). Leaving it set would let a future,
   * unrelated dispatch of the same issueNumber wrongly adopt this stale runId.
   * @see Issue #4118 — dashboard trigger enqueue path
   */
  clearPendingRunId(issueNumber: number): void {
    this.pendingRunIds.delete(issueNumber);
  }

  /**
   * Find the issueNumber whose active slot has the given runId.
   * Returns null if no active slot matches (e.g., pipeline already completed).
   * @see Issue #3552 — cancel command handler
   */
  findSlotByRunId(runId: string): number | null {
    for (const [issueNumber, slot] of this.slots) {
      if (slot.runId === runId) return issueNumber;
    }
    return null;
  }

  /**
   * Cancel the pipeline slot identified by runId.
   * Sets userCancelled=true so the slot completion handler suppresses failure
   * bookkeeping, then calls gracefulStop(SIGTERM → 10s → SIGKILL).
   * Returns true if a slot was found and stop initiated, false if no match.
   * @see Issue #3552 — cancel command handler
   */
  async cancelByRunId(runId: string): Promise<boolean> {
    const issueNumber = this.findSlotByRunId(runId);
    if (issueNumber === null) return false;
    const slot = this.slots.get(issueNumber);
    if (!slot) return false;
    slot.userCancelled = true;
    await slot.orchestrator.gracefulStop(10_000);
    return true;
  }

  /**
   * Forward an approval decision to the pipeline slot identified by runId.
   * Returns true if a slot was found and approve() called, false if no match.
   * @see Issue #3553 — approve command handler
   */
  approveByRunId(runId: string): boolean {
    const issueNumber = this.findSlotByRunId(runId);
    if (issueNumber === null) return false;
    const slot = this.slots.get(issueNumber);
    if (!slot) return false;
    slot.orchestrator.approve();
    return true;
  }

  /**
   * Reject the approval gate for the pipeline slot identified by runId.
   * Returns true if a slot was found and reject() called, false if no match.
   * @see Issue #3553 — reject command handler
   */
  rejectByRunId(runId: string): boolean {
    const issueNumber = this.findSlotByRunId(runId);
    if (issueNumber === null) return false;
    const slot = this.slots.get(issueNumber);
    if (!slot) return false;
    slot.orchestrator.reject();
    return true;
  }

  /**
   * Wait for all running slots to complete
   */
  async waitForAll(): Promise<void> {
    const promises = Array.from(this.slots.values())
      .map((slot) => slot.runPromise)
      .filter(Boolean);
    await Promise.allSettled(promises);
  }

  /**
   * Test-only synchronization hook. Resolves once the FULL slot lifecycle —
   * `runSlotPipeline` plus its finally-block cleanup and the
   * `haltQueueOnSlotFailure` pause decision — has settled for the given
   * issue(s), or for every in-flight slot when called with no arguments.
   *
   * Tests await this instead of a fixed `setTimeout`, so scheduler latency
   * under CPU contention can never race the assertion (the #100 / #243 flake
   * class). Unlike `waitForAll`, it reads `lifecyclePromises` (which outlives
   * the mid-lifecycle `cleanupSlot` that empties `this.slots`), and awaiting a
   * specific issue never blocks on unrelated slots that are still running.
   */
  async settleForTest(...issueNumbers: number[]): Promise<void> {
    const promises =
      issueNumbers.length > 0
        ? issueNumbers
            .map((n) => this.lifecyclePromises.get(n))
            .filter((p): p is Promise<PipelineRunResult> => p !== undefined)
        : [...this.lifecyclePromises.values()];
    await Promise.allSettled(promises);
  }

  /**
   * Clean up orphaned worktrees from previous sessions
   */
  async cleanupOrphans(): Promise<number> {
    return this.worktreeManager.cleanupOrphans();
  }

  /**
   * Remove all managed worktrees
   */
  async cleanupAllWorktrees(): Promise<void> {
    return this.worktreeManager.cleanupAll();
  }

  /**
   * Get the WorktreeManager instance for direct access
   */
  getWorktreeManager(): WorktreeManager {
    return this.worktreeManager;
  }

  /**
   * If a worktree-creation error looks like a branch collision, look up an
   * open PR for the issue and return a richer error with actionable
   * remediation. Non-collision errors pass through unchanged.
   *
   * Detection is intentionally broad — `already exists` covers both
   * `fatal: a branch named 'feat/...' already exists` and
   * `fatal: '<path>' already exists` from `git worktree add`.
   *
   * @see Issue #2992 — branch-collision actionable error
   */
  private async enrichBranchCollisionError(
    original: unknown,
    errMsg: string,
    branchName: string,
    issueNumber: number,
    repoRoot: string
  ): Promise<Error> {
    const fallback = original instanceof Error ? original : new Error("Worktree creation failed");

    if (!errMsg.includes("already exists")) {
      return fallback;
    }

    const pr = await getPRForIssue(issueNumber, repoRoot).catch(() => null);
    const message = pr
      ? `Branch '${branchName}' already exists and PR #${pr.number} is open for issue #${issueNumber}. ` +
        `Use 'pr-merge' to finish it (${pr.url}) or abort the issue to reset.`
      : `Branch '${branchName}' already exists but no open PR was found. ` +
        `Run 'git branch -D ${branchName}' in ${repoRoot} and retry.`;

    return new BranchCollisionError(message, branchName, pr?.url);
  }

  private findAvailableSlotIndex(): number {
    // Exclude both active slot indices and in-flight reservations (#3874) so a
    // second startSlot beginning while a prior worktree is still being created
    // does not pick the same index.
    const usedIndices = new Set([
      ...Array.from(this.slots.values()).map((s) => s.index),
      ...Array.from(this.reservedSlots.values()).map((r) => r.index),
    ]);
    for (let i = 0; i < this.maxConcurrent; i++) {
      if (!usedIndices.has(i)) return i;
    }
    return this.maxConcurrent; // Shouldn't happen if called when slots available
  }

  private slugify(text: string): string {
    return text
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
      .substring(0, 40);
  }

  private emitSlotsChanged(): void {
    this._onSlotsChanged.fire(this.getActiveSlots());
  }

  dispose(): void {
    this.isShuttingDown = true;
    // Kill all active orchestrators
    for (const slot of this.slots.values()) {
      try {
        slot.orchestrator.stop();
      } catch {
        // Best effort
      }
    }
    for (const d of this.disposables) {
      d.dispose();
    }
  }
}
