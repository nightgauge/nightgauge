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
import { classifyTerminalKind, uuidV7, type PipelineStage } from "@nightgauge/sdk";
import { WorktreeManager, type WorktreeInfo } from "../utils/WorktreeManager";
import { killAllActiveProcesses } from "../utils/skillRunner";
import { getPRForIssue } from "../utils/prDetection";
import { describePreservedWip, listPreservedWip } from "../utils/preservedWip";

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
 * Terminal kinds that must NOT halt the queue (#3444/#3835/#3508/#4002/#4222).
 *
 * haltQueueOnSlotFailure exists to surface REAL bugs — validation errors,
 * subagent crashes, gate failures — where a human should triage before the
 * queue auto-continues. For everything here the Go scheduler already
 * auto-recovers (per-issue backoff, global quota cooldown, board→Ready, no
 * lifetime-cap increment, explicitly no pause), so halting overrides that
 * decision and forces a manual Resume after a blip.
 *
 * WHY THIS IS A SET AND NOT A LADDER (#306). Every entry used to be its own
 * pile of regexes carrying a "Match strings mirror Go's ClassifyTerminalKind —
 * keep aligned" comment, with nothing checking the claim; the manifest's own
 * note said they could still drift. They now resolve the kind through the
 * canonical table and test membership. What stays local is only the POLICY —
 * which kinds skip the halt — which is genuinely this layer's decision and is
 * pinned by tests/services/concurrentPipelineManager.haltPolicy.test.ts against
 * the kinds the table can actually produce.
 */
const HALT_SKIP_ENVIRONMENTAL: ReadonlySet<string> = new Set([
  "stream_idle_timeout",
  "rate_limit_quota_exhausted",
  "network_unavailable",
]);
const HALT_SKIP_TRANSIENT_STALL: ReadonlySet<string> = new Set(["stall_kill"]);

/**
 * Transient network-blip detector (#4002): true when the failure text resolves
 * to one of the two network terminal kinds — an Anthropic transport drop
 * (`api_connection_lost`) or the pipeline-start GitHub outage
 * (`github_network_outage`). Both auto-recover via the Go scheduler's
 * environmental routing (short backoff / global cooldown, board→Ready, no
 * lifetime-cap increment), so they must neither halt the queue nor post a
 * failure comment.
 *
 * PINNED BY THE #306 TABLE. This used to be a private ladder of six regexes
 * carrying a "keep aligned with Go" comment and nothing that checked it. It now
 * asks the canonical classifier for the kind and tests membership, so the only
 * thing local to this file is the SET of kinds that skip the halt — a routing
 * policy, which is genuinely this layer's decision. The vocabulary it names is
 * pinned by TERMINAL_KINDS_SKIPPING_HALT below.
 */
function isTransientNetworkFailureText(errMsg: string): boolean {
  const kind = classifyTerminalKind(errMsg);
  return kind === "api_connection_lost" || kind === "github_network_outage";
}

import type { IssueQueueService } from "./IssueQueueService";
import type { HeadlessOrchestrator } from "./HeadlessOrchestrator";
import type { PipelineRunResult } from "./HeadlessOrchestrator";
import type { PipelineStateService } from "./PipelineStateService";
import type { Logger } from "../utils/logger";
import type { ActiveSlot, QueueItem } from "../types/queue";
import { updateProjectItemStatus } from "../utils/projectFieldWriter";
import { postFailureComment } from "../utils/failureComment";
import { getConcurrentPipelineConfig } from "../utils/nightgaugeConfig";
import type { WorkspaceManager } from "./WorkspaceManager";
import { IpcClient } from "./IpcClient";
import type { AbandonedDispatchSituation } from "./IpcClientBase";

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
 * Result of one `startSlot` dispatch.
 *
 * Three states, not a boolean, because "failed" and "abandoned" need opposite
 * handling and a boolean forces them together (#307). `failed` means the
 * dispatch could not start and the item should go back on the queue;
 * `abandoned` means the abort deadline force-cleared this dispatch while it was
 * still inside worktree creation — its queue mark is already released and the
 * operator asked for it to stop, so re-enqueueing would silently undo the stop.
 */
type StartSlotOutcome = "started" | "failed" | "abandoned";

/**
 * A dispatch that has taken a slot's identity but has not yet become a
 * {@link PipelineSlot} — it is inside `startSlot`'s worktree creation. See
 * {@link ConcurrentPipelineManager.reservedSlots}.
 */
interface SlotReservation {
  /** Slot index this dispatch reserved. */
  index: number;
  /** "owner/repo" for cross-repo dispatches; "" when unknown. */
  repo: string;
  /** Per-dispatch run identity — see {@link PipelineSlot.runId}. */
  runId: string;
  /**
   * THE CLAIM on this dispatch's single terminal outcome, with exactly the
   * semantics of {@link PipelineSlot.terminalOutcomeDispatched} (#307). A
   * reservation has two possible claimants — `startSlotInner`'s own failure
   * exits and the abort deadline's `bookForceClearedReservation` — and firing
   * `onSlotFailed` twice charges the Go scheduler's lifetime cap twice.
   */
  terminalOutcomeDispatched?: boolean;
  /**
   * Set once an outcome callback has ACTUALLY FIRED for this dispatch — see
   * {@link PipelineSlot.ownTerminalOutcomeBooked} for why the claim is not the
   * same fact.
   */
  ownTerminalOutcomeBooked?: boolean;
}

/**
 * Slot state for a single concurrent pipeline execution
 */
interface PipelineSlot {
  /** Slot index (0-based) */
  index: number;
  /**
   * THE RUN IDENTITY — a UUIDv7 minted in `startSlot` before the reservation
   * is taken, never reused, and now sent to Go on every call this run makes
   * (ADR-017 step 3, #370). The issue number is NOT an identity: the same
   * issue can be force-cleared and re-queued within one extension-host
   * session, so a late event from the dead run and a live event from its
   * successor are indistinguishable when keyed by issue. Everything that must
   * tell "this run" from "a later run of the same issue" keys off it:
   * `forceClearedRunIds` (the abort tombstone), `stillOwnsIssue` (the
   * supersede check in `cleanupSlot`), the `reservedSlots` record — and, from
   * this step, the slot's `PipelineStateService`, which sends it on every
   * `pipeline.*` call.
   *
   * WHAT #307 CALLED THE "GENERATION" IS THIS FIELD. It was deliberately
   * extension-internal then, because Go's runtime registry was keyed by bare
   * issue number and re-keying it was ADR-scale work. That work is ADR-017:
   * the id now goes on the wire, where the server ACCEPTS AND IGNORES it
   * until the step-4 re-key. Until that lands, the KNOWN EXPOSURE paragraph
   * in docs/GO_BINARY.md § "Force-Clear Terminal Bookkeeping (Issue #307)"
   * still describes the Go side's behaviour: a late call from a dead run
   * still resolves to whatever runtime holds its issue number.
   */
  runId: string;
  /**
   * Platform run ID from a dashboard-trigger ack — routes cancel/approve/
   * reject commands back to this slot (#3552). NOT this run's identity: it is
   * minted by the platform, may be absent entirely (every non-triggered
   * dispatch), and naming it `runId` next to the real one is how the two get
   * conflated. See ADR-017 Decision 2.
   */
  remoteRunId?: string;
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
  /**
   * THE CLAIM on this run's single terminal outcome (#307). Exactly one of
   * `runSlotPipeline` and the abort deadline's `bookForceClearedSlot` may fire
   * onSlotCompleted / onSlotDeferred / onSlotFailed for a given slot, because
   * the callbacks are not idempotent: bootstrap turns each into
   * `autonomousComplete`, which feeds the cascade breaker and the per-issue
   * lifetime cap and frees the Go scheduler's running-slot entry.
   *
   * THE ATOMICITY ARGUMENT. The extension host is single-threaded, so a
   * check-then-set pair with NO `await` between the two statements cannot be
   * interleaved: no other task can observe the intermediate state. Every
   * claimant therefore reads and writes this flag in one synchronous step and
   * only then awaits. Round 3 checked the tombstone at the top of the terminal
   * boundary but claimed AFTER `await getState()`; the deadline could land in
   * that window, see an unclaimed slot, book its own outcome, and the run then
   * booked a second one on resume.
   *
   * Deliberately NOT the same thing as the force-clear tombstone. Conflating
   * the two is what made round 1 regress: the catch block treated "the latch
   * is set" as "a force-clear booked this run", but the latch had been set two
   * lines earlier by the very invocation now in the catch, so an ordinary
   * failing run whose `getState()` rejected skipped `onSlotFailed` entirely.
   * "This invocation already dispatched" and "a force-clear booked this
   * generation" are different questions and have separate answers — which is
   * why `runSlotPipeline` tracks its own claim in an invocation-local
   * variable rather than re-reading this shared flag.
   *
   * ("a force-clear booked this generation" above is now "…this run id" — the
   * concept did not change, only its name and its reach onto the wire.)
   */
  terminalOutcomeDispatched?: boolean;
  /**
   * Set the moment `runSlotPipeline` has actually FIRED one of its own outcome
   * callbacks (#305 review). Distinct from {@link terminalOutcomeDispatched},
   * and the distinction is load-bearing rather than pedantic.
   *
   * The claim above is taken at terminal boundary 1, BEFORE
   * `await slot.stateService.getState()`; the callback fires after that await.
   * A dispatch that wedges in that window has `terminalOutcomeDispatched=true`
   * and has reported NOTHING — no `autonomousComplete`, so the Go scheduler's
   * running-slot seat stays held, and the queue mark is the only bookkeeping
   * that happened. Treating the claim as "it reported an outcome" made the
   * abandoned-dispatch card suppress itself in exactly that case, leaving the
   * condition silent end to end, which is the thing #305 exists to stop.
   *
   * ON THIS TYPE, only the dispatch's own callbacks set it:
   * `bookForceClearedSlot` booking an outcome on a wedged run's behalf
   * deliberately does not — that IS the abandoned case, and the card says so.
   *
   * The RESERVATION twin is weaker, and the difference is worth stating rather
   * than glossing (#305 review). `claimReservationOutcome` sets
   * `SlotReservation.ownTerminalOutcomeBooked` after the callback it fired
   * returns, and `bookForceClearedReservation` calls it — so on that arm the
   * force-clear CAN set the flag. What holds on both arms is the property the
   * card depends on: the flag is READ into a local before any claim of ours
   * fires, so a force-clear never suppresses its own card. Do not restate this
   * as "the force-clear never sets it"; that is the invariant the reservation
   * arm does not have.
   */
  ownTerminalOutcomeBooked?: boolean;
  /**
   * Set by `cleanupSlot` on entry so the teardown runs at most once per slot
   * (#307). Both terminal funnels can reach it for the same slot: a run that
   * had already passed its `finally`'s tombstone check and parked in
   * `completeQueueItem` resumes into `cleanupSlot` after the force-clear has
   * torn the slot down. Without the latch `onSlotCleaned` fires twice and the
   * `finally`'s `preserveWorktree=false` deletes the tree the force-clear
   * deliberately preserved for a process that may still hold it.
   */
  cleanupDone?: boolean;
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
  /**
   * Called when a slot FINISHES a stage (#1055).
   *
   * Distinct from onSlotStageChanged, which fires when a stage STARTS. Phases
   * must be closed on completion: closing them on the next stage's start means
   * the last stage of a run is never closed at all, and the terminal close is
   * looked up by a stage that has no active phase yet, so it no-ops.
   */
  onSlotStageCompleted?: (slotIndex: number, issueNumber: number, stage: PipelineStage) => void;
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
  /** Called when stderr output arrives for a slot. `stage` is the emitting
   * stage when known (#283) — consumers must prefer it over the slot's
   * current-stage pointer, which advances early. */
  onSlotError?: (
    slotIndex: number,
    issueNumber: number,
    data: string,
    stage?: PipelineStage
  ) => void;
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
 * ConcurrentPipelineManager manages N pipeline "slots" for parallel execution.
 *
 * PATH WARNING (#257): this class — not the Go scheduler — is the execution
 * path the product is primarily operated in. Extension-mode runs go
 * queue.dequeueIndependent over IPC → fillSlots → HeadlessOrchestrator and
 * NEVER enter the Go Scheduler.runPipeline loop; their terminal bookkeeping
 * lives in runSlotPipeline's finally block plus the IPC
 * pipeline.notifyComplete handler. A behavior wired only into the Go loop
 * does not exist here, and vice versa, with no error and no failed test
 * (#210, #254). Before adding a terminal-path behavior, answer: which of the
 * two paths reaches this, and is the other intentionally excluded? Then
 * record it in internal/orchestrator/testdata/terminal_behaviors.json — the
 * parity tests (tests/services/terminalParity.test.ts and the Go twin) fail
 * until you do.
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
  private reservedSlots: Map<number, SlotReservation> = new Map();
  /** Platform ack run id, applied to {@link PipelineSlot.remoteRunId} when the slot opens. */
  private pendingRemoteRunIds: Map<number, string> = new Map();

  /**
   * TOMBSTONES: run identities the abort deadline force-cleared (#307).
   *
   * `abortAll`'s deadline branch books a force-cleared slot's terminal state on
   * the run's behalf (queue mark, terminal outcome callback, slot teardown) and
   * then records the run id here. Every terminal boundary in
   * `runSlotPipeline` — the try-block outcome dispatch, the catch, and the
   * fenced finally — checks this FIRST and exits silently, so the wedged run
   * settling minutes later cannot fire a second `onSlotFailed`
   * (→ `autonomousComplete`, which charges the lifetime cap and feeds the
   * cascade breaker), release a successor's queue mark, or delete a
   * successor's worktree.
   *
   * PERMANENT for the run id, by design. There is no release path and no
   * budget that un-claims an entry: a tombstone that can be revoked is a
   * tombstone that expires exactly when the wedge is worst, and round 1 proved
   * that shape re-opens the original defect (the release ran on the LIKELY
   * path — the first bookkeeping IPC call outliving a 5s budget — and handed
   * the dead run back the un-guarded ordinary exits). Growth is one short
   * string per force-cleared slot, and a force-clear costs a 30s abort
   * deadline, so this is operator-rate-bounded, not request-rate-bounded.
   *
   * Keyed by RUN ID, never by issue number: the whole point is to stay
   * correct while a live successor for the same issue sits in `slots` or in
   * `reservedSlots`.
   *
   * Note for ADR-017: this set is a local tombstone, not the run's terminal
   * state on the Go side. `pipeline.abandonRun` — the verb that tells the
   * server a force-cleared dispatch is over — lands in step 6, and the funnel
   * starts calling it there.
   */
  private readonly forceClearedRunIds = new Set<string>();

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
          // These items were marked "processing" by the dequeue and will never
          // reach a terminal run, so release the mark before abandoning them
          // (#254) — otherwise a stop leaves them undispatchable forever.
          if (this.isShuttingDown) {
            await this.completeQueueItem(item, "shutdown before dispatch");
            continue;
          }
          // #188: per-issue in-flight guard at the dispatch boundary. An
          // issue with a live slot (or a reservation whose worktree is still
          // being created) must be skipped by subsequent fills regardless of
          // how many onItemAdded events fired — the dogfood pr-merge deadlock
          // double-ran runPipeline within 3s (two pre-flights, overlapping stage
          // starts, races on the same context files and worktree). Skip
          // WITHOUT re-enqueueing: the issue is already being worked.
          if (this.slots.has(item.issueNumber) || this.reservedSlots.has(item.issueNumber)) {
            this.logger.warn("Skipping duplicate dispatch — issue already in flight (#188)", {
              issueNumber: item.issueNumber,
              hasLiveSlot: this.slots.has(item.issueNumber),
              hasReservation: this.reservedSlots.has(item.issueNumber),
            });
            // The live slot's own completion clears ITS mark; this duplicate
            // dequeue put a second one on and no run will ever clear it (#254).
            await this.completeQueueItem(item, "duplicate dispatch skipped");
            continue;
          }
          const outcome = await this.startSlot(item);
          if (outcome === "started") {
            totalStarted++;
          } else if (outcome === "abandoned") {
            // #307: the abort deadline force-cleared this dispatch while it was
            // inside worktree creation. It already released this dispatch's
            // queue mark and told the operator. Re-enqueueing here would undo
            // the stop they asked for, and `completeQueueItem` would clear a
            // mark that may now belong to a successor.
            this.logger.info("Dispatch abandoned by the abort deadline — not re-enqueued (#307)", {
              issueNumber: item.issueNumber,
            });
          } else {
            // Re-enqueue failed items so they aren't lost.
            // Set fillAgain so the do-while loop re-dequeues after this batch
            // completes — without this, re-enqueued items sit in the queue
            // until the next external event (slot completion, etc.) and can
            // be silently lost if no further events fire. See Issue #2359.
            try {
              // Clear the dispatch's "processing" mark FIRST (#254). The
              // re-enqueue below adds a fresh pending item; leaving the old
              // mark in place would keep both, and the processing one makes
              // the issue undispatchable — so the re-enqueue would be inert.
              await this.completeQueueItem(item, "slot start failed");
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
   * Release the "processing" mark that `dequeueIndependent` put on an item.
   *
   * `DequeueIndependent` no longer splices items out of the Go queue (#232 /
   * #246) — it marks them `processing` so in-flight work is visible to
   * `queueStatusLocked()` and cloud sync, and relies on a terminal
   * `CompleteQueueItem` to remove them. That call was wired only into Go's
   * `Scheduler.runPipeline()`, which this class never enters: it dequeues over
   * IPC and runs the stages itself. So every dispatch leaked a permanent
   * `processing` item, the re-dispatch guard made that issue undispatchable,
   * and the queue reported "processing" with nothing running (#254).
   *
   * The invariant this restores: **an item marked `processing` is completed on
   * every path where dispatch does not lead to a terminal run.** There are four
   * — run finished (any outcome), duplicate skip, slot-start failure, and
   * shutdown before dispatch.
   *
   * Best-effort by design: this runs on cleanup paths, several of which are
   * already handling a failure. A dead IPC socket must not mask the real error
   * or abort cleanup — the reconcile sweep catches a missed mark later, whereas
   * a thrown error here loses the run's actual outcome.
   */
  private async completeQueueItem(
    ref: { issueNumber: number; repoName?: string },
    reason: string
  ): Promise<void> {
    try {
      await this.queueService.complete(ref.repoName ?? "", ref.issueNumber);
      this.logger.debug("Cleared queue processing mark", {
        issueNumber: ref.issueNumber,
        repo: ref.repoName ?? "",
        reason,
      });
    } catch (err) {
      this.logger.warn("Failed to clear queue processing mark — item may linger as processing", {
        issueNumber: ref.issueNumber,
        repo: ref.repoName ?? "",
        reason,
        error: err instanceof Error ? err.message : String(err),
      });
    }
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
   * resolvable `nightgaugeRoot` still dispatches each item to the correct repo
   * as long as `workspaceManager` was provided. Returns null (item rejected,
   * see caller) when the item targets a repo not present in this workspace —
   * this is the "unmatched repo" graceful-failure path.
   *
   * @see Issue #2245 - Cross-repo worktree creation
   * @see Issue #4117 - Agent runner gated on a single nightgaugeRoot
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

  private async startSlot(item: QueueItem): Promise<StartSlotOutcome> {
    const slotIndex = this.findAvailableSlotIndex();
    // THE run identity (#307 / ADR-017). Minted BEFORE the reservation so the
    // reservation record, the eventual PipelineSlot and the slot's state
    // service all carry the same id — that is what lets a late event prove
    // whether the thing currently holding this issue number is still itself
    // or a later dispatch, on both sides of the wire.
    const runId = uuidV7();

    // Reserve this slot's identity (index + repo) synchronously, before any
    // async work below (worktree creation). This makes `availableSlotCount`
    // and the `runningItems` set in `fillSlots` reflect intent-to-run
    // immediately, closing the cross-pass per-repo cap race (#3874). The
    // reservation is released on every exit path: it is superseded by the real
    // slot entry on success (see `finally` below) and removed on failure.
    const reservation: SlotReservation = {
      index: slotIndex,
      repo: item.repoName ?? "",
      runId,
    };
    this.reservedSlots.set(item.issueNumber, reservation);
    let reservationReleased = false;
    const releaseReservation = () => {
      if (reservationReleased) return;
      reservationReleased = true;
      // Release only OUR reservation (#307). A dispatch that wedged inside
      // worktree creation can unwind long after the abort deadline force-cleared
      // it and the operator re-queued the issue; deleting by issue number alone
      // would strip the successor's reservation, which is what makes the #188
      // duplicate-dispatch guard and the per-repo concurrency cap correct.
      if (this.reservedSlots.get(item.issueNumber)?.runId === runId) {
        this.reservedSlots.delete(item.issueNumber);
      }
    };
    try {
      // THE branch name comes from the Go composer, never from here (#889).
      // `issue-pickup`'s own skill already names the binary as the authority
      // for prefix derivation and slug generation; a second implementation
      // here hardcoded `feat/`, so every `type:bug` issue was branded a
      // feature, and it doubled an issue number the queue item's display title
      // already carried. The call is repo-independent, so it does not depend
      // on the worktree `startSlotInner` is about to create.
      //
      // INSIDE the try, and BELOW the reservation, on purpose. The reservation
      // must be taken synchronously — `availableSlotCount` and `fillSlots`'
      // running set have to reflect intent-to-run before this function yields,
      // or a second pass beginning during the await under-counts same-repo
      // concurrency and the per-repo cap is exceeded across passes — the
      // cross-pass race the reservation comment above names.
      // Composing above it reopens exactly that window, and widens the #307
      // force-clear race by a whole IPC round-trip.
      //
      // Fails closed. There is deliberately no local fallback: a fallback IS
      // the second composer, and it would come back the moment IPC hiccuped.
      // The `finally` below releases the reservation if this throws.
      const branchName = (
        await IpcClient.getInstance().gitComposeBranchName(
          item.issueNumber,
          item.title,
          item.labels
        )
      ).name;
      const outcome = await this.startSlotInner(item, slotIndex, branchName, reservation);
      // The dispatch may have been force-cleared while it was awaiting worktree
      // creation. Report `abandoned` so fillSlots neither re-enqueues the item
      // (the operator stopped it — silently putting it back is not a stop) nor
      // clears a queue mark that now belongs to a successor: the force-clear
      // already released this dispatch's own mark.
      if (this.forceClearedRunIds.has(runId)) {
        this.logger.debug("startSlot unwound after its dispatch was force-cleared (#307)", {
          issueNumber: item.issueNumber,
          runId,
          innerOutcome: outcome,
        });
        return "abandoned";
      }
      return outcome;
    } finally {
      // Always drop the reservation. On success the slot is already in
      // `this.slots` (set inside startSlotInner), so accounting is unchanged;
      // on failure this frees the reserved capacity for re-fill.
      releaseReservation();
    }
  }

  /**
   * Resolve the "owner/repo" this dispatch belongs to (ADR-017 Decision 10).
   *
   * The identity is globally unique on its own; the repo is an ATTRIBUTE of
   * the run that the Go layer needs to materialise the platform's run row and
   * to key the issue index (`repo#issue`). `item.repoName` is the field the
   * queue already stamps for a cross-repo dispatch; for a dispatch into the
   * workspace's own repo it is absent, so fall back to the workspace
   * manifest's entry for the root this slot's worktree manager resolved.
   *
   * THE MANIFEST LOOKUP IS NOT A DEPENDABLE FALLBACK ON ITS OWN.
   * `Repository.github` is a getter over a per-repo config that is null until
   * somebody has awaited `loadConfig()`, and no startup path guarantees that
   * before a slot dispatch — so for the COMMON same-repo case its answer
   * depends on whether an unrelated consumer happened to warm the cache
   * first. That is a race, not a fallback, and losing it is not cosmetic: with
   * `repo: ""` the Go server keeps the runtime snapshot off disk entirely
   * ("with no repo there is no correct home", #307) and ADR-017 Decision 6
   * keys the issue index on `repo#issue`, so the run persists nothing and is
   * invisible to the step-5 reconciler. On main this could not happen because
   * `runPipelineInner` unconditionally resolved the repo through `gh repo
   * view`; the last resort below restores exactly that resolution, run in the
   * slot's own worktree, before the identity is installed.
   *
   * Never invents one — a slot with no resolvable owner/name reports "" and
   * says so, exactly as `raiseAbandonedDispatchCard` already does.
   */
  private async resolveSlotRepoSlug(
    item: QueueItem,
    worktreeManager: WorktreeManager,
    orchestrator: HeadlessOrchestrator
  ): Promise<string> {
    if (item.repoName?.includes("/")) return item.repoName;
    const root = worktreeManager.getRepoRoot();
    for (const repository of this.workspaceManager?.getAllRepositories() ?? []) {
      if (repository.path !== root) continue;
      const gh = repository.github;
      if (gh?.owner && gh.repo) return `${gh.owner}/${gh.repo}`;
    }
    const resolved = await orchestrator.resolveRunRepoSlug();
    if (resolved.includes("/")) return resolved;
    this.logger.warn("Dispatch has no resolvable owner/repo — run telemetry will be unattributed", {
      issueNumber: item.issueNumber,
      repoRoot: root,
    });
    return "";
  }

  /**
   * True when `slot` is still the dispatch that owns its issue number.
   *
   * False as soon as ANOTHER dispatch holds the issue — in `this.slots` OR in
   * `this.reservedSlots`. Both halves matter: the manager's own in-flight
   * predicate (the #188 duplicate-dispatch guard) is
   * `slots.has(n) || reservedSlots.has(n)`, so a re-dispatch that is still
   * inside `startSlot`'s worktree creation owns the issue while being absent
   * from `slots`. A supersede check that reads `slots` alone treats that window
   * as "nobody else is here" and lets a dead run delete the tree the successor
   * is being created in.
   */
  private stillOwnsIssue(slot: PipelineSlot): boolean {
    const liveSlot = this.slots.get(slot.issueNumber);
    if (liveSlot) return liveSlot.runId === slot.runId;
    const reservation = this.reservedSlots.get(slot.issueNumber);
    if (reservation) return reservation.runId === slot.runId;
    // Nobody holds the issue: this slot was already torn out of the map by its
    // own cleanup (or by the abort deadline). Nothing to protect.
    return true;
  }

  /**
   * Fire a reservation's terminal outcome at most once (#307).
   *
   * ATOMIC BY SINGLE-THREADEDNESS: the read of the claim and the write that
   * takes it are adjacent statements with no `await` between them, so no other
   * task — in particular `bookForceClearedReservation`, which runs from the
   * abort deadline's timer — can observe the intermediate state. Whoever wins
   * the claim owns the one `onSlotFailed` this dispatch is allowed, and
   * `onSlotFailed` is what bootstrap turns into `autonomousComplete`: firing it
   * twice charges the Go scheduler's per-issue lifetime cap twice and frees the
   * running seat twice.
   */
  private claimReservationOutcome(reservation: SlotReservation, fire: () => void): boolean {
    if (reservation.terminalOutcomeDispatched === true) return false;
    reservation.terminalOutcomeDispatched = true;
    // --- claim taken; awaits are safe from here on
    try {
      fire();
    } catch (err) {
      this.logger.error("Reservation terminal outcome threw — Go scheduler seat may stay held", {
        runId: reservation.runId,
        error: err instanceof Error ? err.message : String(err),
      });
    }
    // The callback has now ACTUALLY fired (or thrown from inside bootstrap's
    // handler, which still means it ran). Distinct from the claim above — see
    // SlotReservation.ownTerminalOutcomeBooked.
    reservation.ownTerminalOutcomeBooked = true;
    return true;
  }

  private async startSlotInner(
    item: QueueItem,
    slotIndex: number,
    branchName: string,
    reservation: SlotReservation
  ): Promise<StartSlotOutcome> {
    const runId = reservation.runId;
    // Resolve the correct WorktreeManager for this item (cross-repo aware).
    // Returns null if the item targets a repo not present in this workspace.
    const slotWorktreeManager = this.resolveWorktreeManager(item);
    if (!slotWorktreeManager) {
      this.logger.warn("Skipping cross-repo item — target repo not in workspace", {
        issueNumber: item.issueNumber,
        repoName: item.repoName,
      });
      this.claimReservationOutcome(reservation, () =>
        this.callbacks.onSlotFailed?.(
          slotIndex,
          item.issueNumber,
          new Error(
            `Cannot run issue #${item.issueNumber} — repo ${item.repoName} is not open in this workspace. ` +
              `Open the target repo in a multi-root workspace or run the pipeline from that repo's workspace.`
          ),
          0,
          item.repoName
        )
      );
      return "failed";
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

    // Preserved work from a previous kill (#1105). `worktreeManager.create`
    // below force-removes the old worktree and `branch -D`s its branch before
    // building a fresh one from origin/<base>, so from here on the ONLY path
    // back to a killed stage's work is its refs/nightgauge/wip/ anchor. Say so
    // BEFORE the teardown: the observed failure was a re-dispatch that planned
    // from scratch over 13 preserved paths and never mentioned them.
    //
    // Advisory, never blocking — listPreservedWip swallows its own failures,
    // and this must not be able to fail a dispatch.
    try {
      const preserved = await listPreservedWip(slotWorktreeManager.getRepoRoot(), item.issueNumber);
      if (preserved.length > 0) {
        this.logger.warn(describePreservedWip(item.issueNumber, preserved), {
          issueNumber: item.issueNumber,
          refs: preserved.map((p) => p.ref),
        });
      }
    } catch {
      // Never block a dispatch on a diagnostic.
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

        this.claimReservationOutcome(reservation, () =>
          this.callbacks.onSlotFailed?.(
            slotIndex,
            item.issueNumber,
            surfacedError,
            0, // no cost — worktree creation failed before pipeline ran
            item.repoName
          )
        );
        return "failed";
      }
    }
    // All retry failures return false above, so worktree is always assigned here.
    if (!worktree) return "failed";

    // Check if stop was pressed during the async worktree creation.
    // Without this, a slot can start running after the user already hit stop.
    //
    // The tombstone half (#307) covers the case `isShuttingDown` no longer
    // does: the abort deadline force-cleared THIS dispatch and then reset
    // `isShuttingDown` on its way out, so a worktree creation that unwedges
    // afterwards would otherwise open a live slot for a run the operator
    // stopped — and, if the issue was re-queued in between, a SECOND live slot
    // for an issue that already has one.
    if (this.isShuttingDown || this.forceClearedRunIds.has(runId)) {
      this.logger.info("Stop pressed during worktree creation — aborting slot", {
        issueNumber: item.issueNumber,
        forceCleared: this.forceClearedRunIds.has(runId),
      });
      try {
        await slotWorktreeManager.cleanup(item.issueNumber, true);
      } catch {
        // Best effort cleanup
      }
      return "failed";
    }

    const { orchestrator, stateService } = this.orchestratorFactory(
      worktree.path,
      item.issueNumber
    );
    // Install THIS dispatch's identity on the slot's own state service before
    // anything it owns can emit (ADR-017 Decision 10, #370). The factory
    // hands back a fresh service per slot, so the not-ambient refusal cannot
    // fire here — and if it ever does, the dispatch must fail loudly rather
    // than run under whatever identity was already installed.
    //
    // The repo is resolved FIRST and `beginRun` is adjacent to the resolved
    // value. The await is safe before this claim precisely because the service
    // is fresh and privately held: no other producer can reach it, so there is
    // no claim race to lose (unlike the singleton mint sites, which resolve
    // before their check for exactly that reason).
    const slotRepoSlug = await this.resolveSlotRepoSlug(item, slotWorktreeManager, orchestrator);
    stateService.beginRun(runId, slotRepoSlug, item.issueNumber);
    // Issue #3704: seed _lastState so updateTokens() does not no-op before
    // any IPC pipeline.notifyStageTransition fires for this worktree slot.
    stateService.initEmpty();

    // Cross-repo override: if the queued item belongs to a different repo,
    // set the repo override so all gh CLI calls target the correct repo.
    if (item.repoName) {
      orchestrator.setRepoOverride(item.repoName);
    }

    // Pin the root of the repo THIS RUN targets (#305 review). The factory
    // seeded `mainRepoRoot` from the RUNNER root, which is one fixed path for
    // every slot; `slotWorktreeManager` is the one already resolved per item,
    // so for a cross-repo dispatch this is the sibling repo the run belongs to.
    // It is the root the daemon writes `budget-override.json` under when a
    // budget-ceiling card is resolved (`Server.repoRoot(repo)`), so pinning it
    // here is what makes the raised ceiling reach the run that needed it
    // instead of a sibling repo's next dispatch.
    orchestrator.setRunRepoRoot(slotWorktreeManager.getRepoRoot());

    // Concurrent slots are inherently unattended — they run from the
    // autonomous scheduler / queue with no human watching the modal. Mark the
    // orchestrator so budget/ceiling escalations auto-resolve (up to the cap)
    // instead of blocking on an interactive prompt that never gets clicked.
    orchestrator.setUnattended(true);

    // Capture the slot's worktreeManager so cleanup uses the correct repo
    // even if updateRepoRoot() is called while this slot is running.
    // For cross-repo items, this is the target repo's manager (not this.worktreeManager).
    const pendingRemoteRunId = this.pendingRemoteRunIds.get(item.issueNumber);
    if (pendingRemoteRunId !== undefined) {
      this.pendingRemoteRunIds.delete(item.issueNumber);
    }

    const slot: PipelineSlot = {
      index: slotIndex,
      runId,
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
      remoteRunId: pendingRemoteRunId,
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
    // The rejection (if any) is already fully handled inside runSlotPipeline
    // (logged, onSlotFailed fired, cleanup run) and rethrown so slot.runPromise
    // consumers still observe it. .finally() adopts that rejection into a new
    // promise; this .catch() only exists to stop that derived promise from
    // surfacing as an unhandled rejection.
    void runPromise
      .finally(() => {
        if (this.lifecyclePromises.get(item.issueNumber) === runPromise) {
          this.lifecyclePromises.delete(item.issueNumber);
        }
      })
      .catch(() => undefined);
    return "started";
  }

  /**
   * Run a pipeline in a slot and handle completion/cleanup
   */
  private async runSlotPipeline(slot: PipelineSlot): Promise<PipelineRunResult> {
    let pipelineSucceeded = false;
    let isAlreadyResolved = false;
    // #305: a blockedBy deferral is a non-failure. When set, the finally block
    // below skips every failure side-effect (In-review board move, failure
    // comment, successor drain, queue halt/autonomous pause).
    let pipelineDeferred = false;
    let pipelineResult: PipelineRunResult | undefined;
    /**
     * True once THIS invocation has taken {@link PipelineSlot.terminalOutcomeDispatched}
     * (#307). Invocation-local on purpose: the shared flag answers "somebody
     * booked this slot's outcome", which is the wrong question in the catch —
     * the somebody may be the abort deadline, and re-reading the shared flag
     * there is the round-1 conflation that swallowed ordinary failures.
     */
    let outcomeClaimedHere = false;
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
        // The branch the worktree is ACTUALLY on, not a recomposition (#889).
        // Recomposing here was a third composer, and it disagreed with the
        // real branch the moment labels or truncation differed — so pipeline
        // state opened naming a branch that did not exist.
        await slot.stateService.initializePipeline(
          slot.issueNumber,
          slot.title,
          slot.worktree.branch
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
        // #1055: the slot never wired onStageComplete, so nothing closed a
        // stage's phases on the concurrent path. onStageComplete is already
        // declared on PipelineCallbacks and already fires on every success and
        // failure path, so no orchestrator change is needed.
        onStageComplete: (stage) => {
          this.callbacks.onSlotStageCompleted?.(slot.index, slot.issueNumber, stage);
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
              this.callbacks.onSlotError?.(slot.index, slot.issueNumber, line, stage);
            } else {
              this.callbacks.onSlotOutput?.(slot.index, slot.issueNumber, line, stage);
            }
          }
        },
      });

      pipelineResult = result;
      pipelineSucceeded = result.success;
      pipelineDeferred = result.deferred ?? false;
      isAlreadyResolved = result.outcomeType === "already-resolved";

      this.logger.info("[SlotLifecycle] runPipeline() RESOLVED", {
        slotIndex: slot.index,
        issueNumber: slot.issueNumber,
        success: result.success,
        failedStage: result.failedStage,
        durationMs: Date.now() - startMs,
      });

      // ── TERMINAL BOUNDARY 1 of 3 (#307): CHECK-AND-CLAIM ─────────────────
      // The two statements below are ONE ATOMIC STEP. The extension host is
      // single-threaded and there is no `await` between them, so no other task
      // — in particular the abort deadline's `forceClearStuckSlots`, which runs
      // from a timer — can run in between and observe "not tombstoned, not
      // claimed". Whoever gets here first owns this slot's single terminal
      // outcome; the loser stands down.
      //
      // Do NOT move an await into this pair. Round 3 checked the tombstone here
      // and claimed only after the `getState()` await below: the deadline
      // landed in that window, `bookForceClearedSlot` saw an unclaimed slot and
      // fired its own `onSlotFailed`, and the run then fired a second outcome
      // on resume — two `autonomousComplete` calls for one run, which
      // double-charges the Go scheduler's per-issue lifetime cap and cascade
      // breaker and, once the operator has re-queued the issue, charges them
      // against the SUCCESSOR's run.
      if (this.isForceCleared(slot)) {
        this.logger.debug("Force-cleared run settled — outcome dropped (#307)", {
          issueNumber: slot.issueNumber,
          runId: slot.runId,
          success: result.success,
        });
        return result;
      }
      slot.terminalOutcomeDispatched = true;
      outcomeClaimedHere = true;
      // ── claim taken; awaits are safe from here on ─────────────────────────

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

      // Exactly one of the four branches below fires exactly one outcome
      // callback; the claim for it was taken at terminal boundary 1 above.
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
      // The dispatch reported its OWN outcome. Set only here and in the catch
      // below — never where the claim is taken, and never by the force-clear
      // (#305 review). See PipelineSlot.ownTerminalOutcomeBooked.
      slot.ownTerminalOutcomeBooked = true;

      return result;
    } catch (error) {
      // ── TERMINAL BOUNDARY 2 of 3 (#307): CHECK-AND-CLAIM ─────────────────
      // Same atomic pair as boundary 1: tombstone check and claim are adjacent
      // statements with no `await` between them.
      //
      // Skipped entirely when THIS invocation already holds the claim, which
      // happens when boundary 1 claimed and something after it threw (a
      // rejecting `getState()`, a throwing onSlotCompleted callback). The
      // outcome is already ours: `bookForceClearedSlot` read the claim and
      // stood down, so re-checking the tombstone here would drop an outcome
      // nobody else will book.
      //
      // Note what is deliberately NOT read: the SHARED
      // `slot.terminalOutcomeDispatched`. That flag also reads true when the
      // abort deadline booked the outcome, and treating those two cases alike
      // is the round-1 conflation that made an ordinary failing run with a
      // rejecting `getState()` skip `onSlotFailed` entirely — #307's own
      // symptom (no autonomousComplete, Go's running-slot entry never freed)
      // manufactured on a path with no relationship to abortAll.
      if (!outcomeClaimedHere) {
        if (this.isForceCleared(slot)) {
          this.logger.debug("Force-cleared run threw — failure dropped (#307)", {
            issueNumber: slot.issueNumber,
            runId: slot.runId,
            error: error instanceof Error ? error.message : "Unknown error",
          });
          throw error;
        }
        // The SHARED claim only — `outcomeClaimedHere` is not re-assigned here
        // because nothing after this block reads it (the finally is guarded by
        // the tombstone alone, and this path always rethrows).
        slot.terminalOutcomeDispatched = true;
      }
      // ── claim taken; awaits are safe from here on ─────────────────────────

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
      slot.ownTerminalOutcomeBooked = true;
      throw error;
    } finally {
      // terminal-parity:begin runSlotPipeline-finally (#257 — this region is
      // content-pinned by internal/orchestrator/testdata/terminal_behaviors.json;
      // any edit fails tests/services/terminalParity.test.ts until the manifest
      // is updated, which is the moment to check the Go path for the same
      // behavior)
      //
      // TERMINAL BOUNDARY 3 of 3 (#307). FIRST act of the funnel: a run id
      // the abort deadline force-cleared has already had this whole block run
      // on its behalf by `forceClearStuckSlots`, so running it again is not
      // idempotent repair — it is corruption once the operator has re-queued
      // the issue. `completeQueueItem` would strip the SUCCESSOR's `processing`
      // mark (Scheduler.completeQueueItemLocked matches on repo+number), and
      // `cleanupSlot` would delete the successor's worktree (one worktree path
      // per issue number). See the `force-clear-terminal-bookkeeping` row in
      // terminal_behaviors.json for the full accounting of what the force-clear
      // performs, omits, and delegates.
      if (this.isForceCleared(slot)) {
        this.logger.debug("Force-cleared run reached its finally — skipped (#307)", {
          issueNumber: slot.issueNumber,
          runId: slot.runId,
        });
      } else {
        this.logger.info("[SlotLifecycle] FINALLY block entered", {
          slotIndex: slot.index,
          issueNumber: slot.issueNumber,
          pipelineSucceeded,
          slotsBeforeCleanup: this.slots.size,
          isShuttingDown: this.isShuttingDown,
        });

        // Clear the dequeue's "processing" mark — the terminal counterpart to
        // fillSlots' dequeueIndependent, covering success, failure, throw and
        // cancellation alike (#254). Done FIRST: everything below is best-effort
        // recovery work, and `cleanupSlot` is not itself wrapped in a try, so a
        // throw down there would skip this and strand the item.
        await this.completeQueueItem(
          { issueNumber: slot.issueNumber, repoName: slot.repo },
          "run reached terminal state"
        );

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
        if (
          !pipelineSucceeded &&
          !pipelineDeferred &&
          !this.isShuttingDown &&
          !slot.userCancelled
        ) {
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
        // Also skip when the run ended on a recorded out-of-scope blocked
        // finding (#1147): the orchestrator has already posted a comment that
        // names the signal, the rationale and the verbatim evidence, and it
        // posts on BOTH surfaces rather than only this one. A generic failure
        // report underneath it would narrate the same stop a second time as a
        // failure, which is the shape #1147 exists to stop it being.
        if (
          !pipelineSucceeded &&
          !pipelineDeferred &&
          !this.isShuttingDown &&
          !slot.userCancelled &&
          pipelineResult &&
          !pipelineResult.blocked?.outOfScopeFinding &&
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

        // A failed pipeline may leave valuable uncommitted agent work in the
        // worktree. Never force-remove that work before an operator can inspect
        // or resume it. Deferred runs are also resumable by definition. See #66.
        const preserveWorktree = !pipelineSucceeded || pipelineDeferred;
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
        if (
          !pipelineSucceeded &&
          !pipelineDeferred &&
          !this.isShuttingDown &&
          !slot.userCancelled
        ) {
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
      // terminal-parity:end runSlotPipeline-finally
    }
  }

  /**
   * Clean up a completed/failed slot.
   *
   * @param slot - The pipeline slot to clean up
   * @param preserveWorktree - If true, skip worktree removal. Failed and deferred
   *   runs may contain uncommitted work or context required for inspection/resume;
   *   only a clean successful merge is safe to remove automatically.
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
    // SUPERSEDE GUARD (#307). Every destructive step below is keyed by ISSUE
    // NUMBER — `slots.delete(n)`, `worktreeManager.cleanup(n)` (one worktree
    // path per issue), the tree item, the queue's active-slot list. If a later
    // dispatch of the same issue holds it, this slot is a ghost and every one
    // of those steps hits the successor instead. Release only what is
    // unambiguously ours (this slot's own state service) and stand down.
    //
    // Asked FIRST, before the at-most-once latch below: "is this teardown even
    // addressed to me?" outranks "have I already run?", and the stand-down
    // branch touches nothing keyed by issue number.
    if (!this.stillOwnsIssue(slot)) {
      this.logger.warn("cleanupSlot stood down — a newer dispatch owns this issue (#307)", {
        issueNumber: slot.issueNumber,
        runId: slot.runId,
        successorRunId:
          this.slots.get(slot.issueNumber)?.runId ??
          this.reservedSlots.get(slot.issueNumber)?.runId,
      });
      try {
        slot.stateService.dispose();
      } catch {
        // Best effort
      }
      return;
    }

    // AT-MOST-ONCE (#307). Both terminal funnels can reach this for the same
    // slot with nobody else holding the issue: a run that passed its finally's
    // tombstone check and then parked in `completeQueueItem` resumes here after
    // the force-clear already tore the slot down. The second pass would fire
    // `onSlotCleaned` again and — with the finally's `preserveWorktree=false`
    // on a successful run — delete the tree the force-clear deliberately
    // preserved for a process that may still hold it. Check-and-set with no
    // await between the two statements, so the pair is atomic on the
    // single-threaded host.
    if (slot.cleanupDone === true) {
      this.logger.debug("cleanupSlot skipped — this slot was already torn down (#307)", {
        issueNumber: slot.issueNumber,
        runId: slot.runId,
      });
      return;
    }
    slot.cleanupDone = true;

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
        // Logging must never interrupt failure recovery. Some injected/test
        // managers implement only the cleanup surface.
        worktreePath: slot.worktreeManager.getWorktreePath?.(slot.issueNumber),
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
      // timeouts mid-token-output, extended GitHub connectivity loss) that the
      // autonomous scheduler already auto-recovers from via per-issue backoff +
      // the global quota cooldown set in onPipelineComplete. Tripping the halt
      // on them forces the user to manually click Resume after the cooldown
      // expires (~4h for a quota miss), which defeats the purpose of the
      // environmental classification path.
      //
      // The kind comes from the canonical #306 table, so this branch and the
      // run record can no longer describe the same failure differently. The one
      // raw-text condition that survives is NOT a duplicated matcher: a bare
      // Anthropic "session/usage limit" with no model named is a shape the
      // RECORD does not classify at all (Go returns "" for it), and skipping the
      // halt for it is a local policy call the operator has relied on since
      // #3792.
      //
      // The REACTION side does classify it, via the table's declared
      // `signal_extensions` — but a halt decision is not a kind, and reaching
      // for signalTerminalKind here would make a queue-halt policy depend on
      // which rules happen to be in the signal subset. So this stays raw, and
      // the whole method body is fenced by
      // tests/services/concurrentPipelineManager.haltPolicy.test.ts: exactly one
      // regex, and no string method on haltErrMsg other than slice().
      const haltErrMsg = pipelineResult?.error?.message ?? "";
      const haltKind = classifyTerminalKind(haltErrMsg);
      const isEnvironmentalFailure =
        (haltKind !== undefined && HALT_SKIP_ENVIRONMENTAL.has(haltKind)) ||
        /\b(?:session|usage)\s+limit\b/i.test(haltErrMsg);
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
      // scheduler already classifies it as api_overloaded and auto-recovers:
      // per-issue 5-minute backoff, board→Ready, NO lifetime-cap increment, NO
      // global cooldown, and — per its own log — explicitly NO queue pause.
      // Without this branch the 529 fell through to the halt path below, which
      // cleared the queue and called autonomousPause(), OVERRIDING the Go
      // layer's "no pause" decision and forcing a manual Resume after a
      // momentary overload (the original incident: acmeapp #100 paused the
      // whole queue while #34/#85 — same 529 window — correctly retried). Skip
      // the halt and surface a non-blocking toast so the operator sees the
      // retry without the queue grinding to a stop; the issue is already
      // surfaced in the Autonomous panel's retry list by Go's recordFailure. It
      // is NOT folded into isEnvironmentalFailure because that path returns
      // silently; an overload deserves the same visible-but-non-blocking
      // treatment as a stall-kill.
      const isApiOverloaded = haltKind === "api_overloaded";
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
      // Runaway-ceiling kills (#3508) resolve to stall_kill in the table and are
      // covered by the same set: no queue halt, no autonomous pause, 30m backoff
      // via the Go layer.
      const isStallKill = haltKind !== undefined && HALT_SKIP_TRANSIENT_STALL.has(haltKind);
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
      if (haltKind === "architecture_approval_required") {
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
          // #148: pass structured fields through so the Go-side pause handler
          // can raise a proper terminal-failure Action Center card instead of
          // leaving the halt's cause undiscoverable until a misleading
          // "Fleet idle" card fires one scan cycle later. None of the
          // classified suppression branches above matched by this point, so
          // this is a genuine, unclassified terminal failure. costUsd is
          // best-effort (#146's terminating-stage cost plumbing hasn't
          // landed) and degrades to omitted/zero on the card.
          const pauseSlotState = await slot.stateService.getState().catch(() => null);
          const pauseCostUsd = pauseSlotState?.tokens?.estimated_cost_usd ?? 0;
          // #283: prefer the gate's structured verdict over the generic
          // label — a validation_error (harness/bookkeeping fault) must not
          // present as an unclassifiable organic failure on the halt card.
          const pauseTerminalKind = pipelineResult?.terminalKind || "unclassified";
          await ipc.autonomousPause(
            `haltQueueOnSlotFailure: issue #${slot.issueNumber} failed at ${failedStage}`,
            "haltQueueOnSlotFailure",
            slot.repo ?? "",
            slot.issueNumber,
            failedStage,
            pauseTerminalKind,
            pauseCostUsd
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
        this.logger.warn("abortAll exceeded deadline — force-clearing slots", {
          timeoutMs: ABORT_ALL_TIMEOUT_MS,
          stuckIssues: Array.from(this.slots.keys()),
          strandedReservations: Array.from(this.reservedSlots.keys()),
          isFilling: this.isFilling,
        });
        // Best-effort second sweep before giving up — covers processes spawned
        // between the first kill and the timeout.
        try {
          killAllActiveProcesses();
        } catch {
          // Best effort
        }
        // Book the dead runs' terminal state (#307). Before this, `slots.clear()`
        // was the ONLY mutation on this branch: no queue-mark release, no
        // terminal outcome, no slot teardown — so the issue stayed `processing`
        // forever (undispatchable, #254's outcome through a second door), Go's
        // scheduler never freed its running-slot entry, and the state
        // service/tree teardown chain never ran. forceClearStuckSlots tombstones
        // and empties the slot map itself, synchronously, before its first await.
        const forceCleared = await this.forceClearStuckSlots();
        // Count what was ACTUALLY force-cleared, reservations included. The
        // deadline can fire with `isFilling: true` and zero slots — a dispatch
        // wedged inside worktree creation — and a toast reading "0 stuck
        // slot(s)" after releasing a queue mark and a Go scheduler seat tells
        // the operator nothing happened when something did.
        void vscode.window.showWarningMessage(
          `Stop took longer than ${Math.round(ABORT_ALL_TIMEOUT_MS / 1000)}s — force-cleared ${forceCleared} stuck dispatch(es). Pipeline ready for new work.`
        );
      }
    } finally {
      if (timeoutHandle) clearTimeout(timeoutHandle);
      this.isShuttingDown = false;
      this.isAbortAllInProgress = false;
    }
  }

  /**
   * True when the abort deadline force-cleared THIS dispatch (#307).
   *
   * Keyed by RUN ID, never by issue number: by the time a wedged run
   * settles, the operator may have re-queued the issue, so a live successor can
   * be sitting in `slots` or in `reservedSlots` under the same number.
   */
  private isForceCleared(slot: PipelineSlot): boolean {
    return this.forceClearedRunIds.has(slot.runId);
  }

  /**
   * Book the terminal state of every slot the abort deadline gave up on, then
   * tombstone it (#307).
   *
   * The order is load-bearing. Tombstones and the slot-map clear happen
   * SYNCHRONOUSLY, before the first await, so a wedged run that settles during
   * the bookkeeping cannot re-enter its own terminal funnel behind us. The
   * bookkeeping then runs, per dispatch, each step guarded independently: a
   * step that throws is logged and the next step still runs, because a
   * partially-booked dead run beats an unbooked one and the tombstone stands
   * either way. TWO ARMS — {@link bookForceClearedSlot} for dispatches that
   * became slots, {@link bookForceClearedReservation} for those still inside
   * worktree creation. Returns how many dispatches were force-cleared across
   * both, which is what the operator toast reports.
   *
   * NOT bounded by a per-slot timeout. Round 1 had one, and it was the defect:
   * the budget released the settle-once claim on the LIKELY path (the first
   * step is an IPC round-trip against a manager that is wedged by definition),
   * handing the dead run back the un-guarded ordinary exits and re-opening both
   * #307 and the successor corruption. The bound here is the IPC client's own
   * request timeout, paid once across all slots rather than once per slot:
   * slots are booked concurrently precisely so `isShuttingDown` — which makes
   * IssueQueueService refuse every enqueue — is not held for N × that timeout
   * (the #3111 condition this whole deadline exists to bound).
   *
   * THAT BOUND SURVIVES THE #305 CARD RAISE, and only because the raise is
   * issued CONCURRENTLY with `completeQueueItem` rather than after it. Two IPC
   * calls in series per slot would have paid `IpcClientBase.getTimeoutMs()`
   * (30s by default) twice, growing the worst-case `isShuttingDown` hold from
   * ~30s to ~60s on top of the 30s deadline already spent — the #3111 condition
   * arriving through a change made inside the fix for it, against a daemon that
   * is unresponsive by definition when this deadline fires. In flight together,
   * both calls share one timeout window, so the statement above stays true.
   */
  private async forceClearStuckSlots(): Promise<number> {
    // terminal-parity:begin force-clear-funnel (#257/#307 — this whole region,
    // through bookForceClearedReservation, is the SECOND terminal funnel on the
    // extension path. It is content-pinned by
    // internal/orchestrator/testdata/terminal_behaviors.json exactly like the
    // runSlotPipeline finally, so a behavior added to one funnel and not the
    // other cannot land silently. The RESERVATION arm is inside the fence on
    // purpose: it books a terminal outcome too, and round 3 left it outside,
    // where a change to it broke no hash. See the
    // force-clear-terminal-bookkeeping row for the full
    // performed/omitted/delegated accounting.)
    const stuckSlots = Array.from(this.slots.values());
    const strandedReservations = Array.from(this.reservedSlots.entries());

    // --- synchronous prologue: no await until every run id is tombstoned
    for (const slot of stuckSlots) {
      this.forceClearedRunIds.add(slot.runId);
    }
    for (const [, reservation] of strandedReservations) {
      this.forceClearedRunIds.add(reservation.runId);
    }
    this.slots.clear();
    this.emitSlotsChanged();
    // --- end synchronous prologue

    // allSettled, not all: every step below already swallows its own failure,
    // but the force-clear must never be the thing that makes `abortAll` reject
    // — that is the #3111 condition (a Stop that does not return) arriving
    // through the fix for it.
    await Promise.allSettled([
      ...stuckSlots.map((slot) => this.bookForceClearedSlot(slot)),
      ...strandedReservations.map(([issueNumber, reservation]) =>
        this.bookForceClearedReservation(issueNumber, reservation)
      ),
    ]);
    return stuckSlots.length + strandedReservations.length;
  }

  /**
   * Run the settled path's terminal bookkeeping for one force-cleared slot.
   *
   * Three steps, each independently guarded, plus one notification:
   *   1. `completeQueueItem` — release the dequeue's `processing` mark, the
   *      terminal counterpart to `dequeueIndependent` (#254). Without it the
   *      issue is undispatchable for good.
   *   2. the terminal outcome callback — `onSlotFailed`, which is what
   *      bootstrap turns into `autonomousComplete` (frees the Go scheduler's
   *      running-slot entry), plus the phase-tracker / state-subscription /
   *      tree / notifier teardown. Skipped when the run already CLAIMED its own
   *      outcome and was merely mid-teardown when the deadline fired —
   *      `autonomousComplete` is not idempotent (it feeds the cascade breaker
   *      and the per-issue lifetime cap), so booking twice double-charges.
   *   3. `cleanupSlot` with the worktree PRESERVED — a killed process may still
   *      hold the tree, and the settled Stop All path preserves it too (#66).
   *
   * The `abandoned-dispatch` card (#305) rides ALONGSIDE step 1 rather than
   * after it, and is gated on a different flag from step 2: step 2 asks "may I
   * book an outcome?" (the claim), the card asks "did this dispatch report
   * one?" (`ownTerminalOutcomeBooked`). Those diverge for a run that took the
   * claim and then wedged before its callback fired — a case where nothing was
   * booked and the card is the only signal left.
   *
   * The error handed to `onSlotFailed` is byte-identical to the one the SETTLED
   * Stop All path emits (`runSlotPipeline`'s `slot.userCancelled` branch). Both
   * Stop paths must book the same thing; giving only the wedged variant a
   * distinguishing marker would make the two disagree about what an operator
   * Stop costs. Neither is classified as an operator abort today — see
   * `force-clear-terminal-bookkeeping` in terminal_behaviors.json.
   */
  private async bookForceClearedSlot(slot: PipelineSlot): Promise<void> {
    // ── CHECK-AND-CLAIM, before the first await ──────────────────────────
    // Mirror of the terminal boundaries in runSlotPipeline. The tombstone is
    // already in place (forceClearStuckSlots' synchronous prologue set it, and
    // the redundant add here keeps this method correct on its own terms if it
    // is ever called from elsewhere), and the claim is read and taken with no
    // await in between — so a run parked inside its own terminal boundary
    // cannot slip between the read and the write. `alreadyClaimed === true`
    // means the run took the claim first and WILL fire its own outcome when it
    // resumes; booking a second one here is the double-charge this flag exists
    // to prevent.
    this.forceClearedRunIds.add(slot.runId);
    const alreadyClaimed = slot.terminalOutcomeDispatched === true;
    if (!alreadyClaimed) slot.terminalOutcomeDispatched = true;
    // Read in the SAME synchronous step, and read separately from the claim:
    // "the run took the claim" and "the run reported an outcome" are different
    // facts, and only the second one may silence the card (#305 review).
    const runReportedItsOwnOutcome = slot.ownTerminalOutcomeBooked === true;
    // ── claim decision made; awaits are safe from here on ────────────────

    this.logger.warn("Force-clearing stuck slot — booking its terminal state (#307)", {
      slotIndex: slot.index,
      issueNumber: slot.issueNumber,
      runId: slot.runId,
      runAlreadyClaimedItsOwnOutcome: alreadyClaimed,
      runReportedItsOwnOutcome,
    });

    // #305: tell the operator what their Stop left behind. Started HERE, in
    // flight alongside `completeQueueItem`, and awaited below — NOT chained
    // after it. Both are IPC round-trips bounded by the client's 30s request
    // timeout, and the daemon is unresponsive by definition when this deadline
    // fires; serialising them would double the window in which `isShuttingDown`
    // makes IssueQueueService refuse every enqueue, which is the #3111
    // condition `forceClearStuckSlots`' own doc comment bounds. Concurrency
    // costs nothing here: the raise deliberately needs neither the queue result
    // nor the state service.
    //
    // SUPPRESSED ONLY BY A CALLBACK THAT ACTUALLY FIRED, never by the claim
    // (fixed in review). `terminalOutcomeDispatched` is taken at terminal
    // boundary 1 BEFORE `await slot.stateService.getState()`, and the outcome
    // callback fires after it — so a run that wedged in that window holds the
    // claim while having reported nothing at all: no `autonomousComplete`, the
    // Go scheduler's seat still held, the queue mark the only bookkeeping done.
    // Gating on the claim silenced the card there, which made the condition
    // silent end to end — the thing this producer exists to stop. A run that
    // genuinely booked its own outcome and merely wedged in teardown sets
    // `ownTerminalOutcomeBooked`, and only that suppresses.
    //
    // THE SITUATION IS THE CALL SITE'S TO NAME (#305 review). `alreadyClaimed`
    // decides which of two different things happened to this dispatch, and only
    // this frame knows it: with the claim taken, step 2 below stands down and
    // NOBODY books the terminal outcome, so the Go scheduler's seat stays held
    // and the card must say so. Without it, the force-clear books the outcome
    // and the only residue is the preserved worktree. One body for both told
    // the operator "nothing is blocked" in the one case where something was.
    const raisePromise = runReportedItsOwnOutcome
      ? undefined
      : this.raiseAbandonedDispatchCard(
          slot.repo,
          slot.issueNumber,
          slot.currentStage,
          alreadyClaimed ? "claim-taken-then-wedged" : "slot-worktree-preserved"
        );

    try {
      await this.completeQueueItem(
        { issueNumber: slot.issueNumber, repoName: slot.repo },
        "abort deadline force-cleared an unsettled slot"
      );
    } catch (err) {
      // Unreachable today — completeQueueItem catches internally and only
      // warns. Kept as a fence against that changing: if the mark is not
      // released the issue is undispatchable for good, and the tombstone means
      // the run's own finally will not retry it.
      this.logger.error("Force-clear: queue mark NOT released — issue stays undispatchable", {
        issueNumber: slot.issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
    }

    if (!alreadyClaimed) {
      // Read the run's real spend the same way the SETTLED path does. The state
      // service is still alive here (cleanupSlot disposes it below), and the
      // fifth argument is not cosmetic: bootstrap gates
      // `dashboard.recordHealthSnapshotForRun` behind `costUsd > 0`, so a
      // hard-coded 0 silently drops the reliability snapshot for every
      // force-cleared run and paints $0 on the Output Window badge.
      const costUsd =
        (await slot.stateService
          .getState()
          .then((s) => s?.tokens?.estimated_cost_usd)
          .catch(() => undefined)) ?? 0;
      try {
        this.callbacks.onSlotFailed?.(
          slot.index,
          slot.issueNumber,
          new Error(`Cancelled by user`),
          costUsd,
          slot.repo
        );
      } catch (err) {
        this.logger.error(
          "Force-clear: terminal outcome NOT booked — Go scheduler slot stays held",
          {
            issueNumber: slot.issueNumber,
            error: err instanceof Error ? err.message : String(err),
          }
        );
      }
    }

    // Settle the concurrent raise before teardown. Never throws (the method
    // swallows its own failure), so this cannot abort the exactly-once
    // bookkeeping below.
    if (raisePromise) await raisePromise;

    try {
      await this.cleanupSlot(slot, /* preserveWorktree */ true, /* deleteBranch */ false);
    } catch (err) {
      this.logger.error("Force-clear: slot teardown failed — state service/tree item may leak", {
        issueNumber: slot.issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Raise the `abandoned-dispatch` Action Center card for a force-cleared
   * dispatch (#305).
   *
   * AN INFORMATIONAL CARD, NOT A REMEDY. Everything that reaches here came from
   * an operator Stop: `forceClearStuckSlots` has one call site (the abort
   * deadline in `abortAll`), and `abortAll` is reached only from
   * `nightgauge.stopPipeline`, `nightgauge.abortPipeline`, and `deactivate()`.
   * The daemon-side builder therefore emits an `fyi` card with two noop options
   * and no Retry — re-dispatching work the operator just cancelled is not a fix,
   * and offering it as the primary action told them to undo their own decision.
   *
   * `situation` IS NOT OPTIONAL AND NOT INFERRABLE DAEMON-SIDE. Three different
   * things reach this method, and the honest body differs on every fact an
   * operator acts on — whether a stage ran, whether there is a worktree that
   * may hold uncommitted work, and whether the dispatch's terminal outcome was
   * booked by anyone. Only the calling frame knows: the arm is structural, and
   * the booking status is a flag read synchronously before the first await.
   * Round 3 shipped one fixed body for all three and it was false for two of
   * them — it promised a preserved worktree to a dispatch that never created
   * one, and "NOTHING IS BLOCKED" to one still holding a scheduler seat.
   *
   * NO RUN ID, and that is correct rather than a shortcut. At force-clear time
   * Go mints the RunID and the extension has no verb to ask for one; the wedged
   * run's `run-state.json` may also already be archived. The card therefore
   * carries no trace back-reference — an explicitly handled case daemon-side.
   * Synthesising an id to fill the field would put a fabricated identity into
   * the audit trail, which is worse than an honest gap. (#370, which threads
   * run identity through the IPC surface, is what improves this later.)
   *
   * FAIL-OPEN AND NEVER-THROWING, twice over: every raise path is fail-open by
   * contract, and this one sits inside the force-clear funnel, where a throw
   * would abort the exactly-once terminal bookkeeping #307 exists to guarantee
   * — turning a missing notification into a leaked queue mark and a held
   * scheduler seat.
   */
  private async raiseAbandonedDispatchCard(
    repo: string | undefined,
    issueNumber: number,
    stage: string | undefined,
    situation: AbandonedDispatchSituation
  ): Promise<void> {
    if (!repo || !repo.includes("/")) {
      // A slot with no resolvable owner/name has no card identity. A legitimate
      // local state, not a failure to report.
      return;
    }
    try {
      const result = await IpcClient.getInstance().attentionRaise(
        "abandoned-dispatch",
        repo,
        issueNumber,
        undefined, // runId — see the doc comment above
        undefined, // pr
        undefined, // prState
        undefined, // mergeable
        undefined, // mergeStateStatus
        undefined, // reviewDecision
        undefined, // checks
        stage,
        situation
      );
      this.logger.info("Force-clear: abandoned-dispatch card raised (#305)", {
        issueNumber,
        situation,
        outcome: result.outcome,
        requestId: result.id,
      });
    } catch (err) {
      this.logger.warn("Force-clear: attention.raise failed (fail-open) — no card for this wedge", {
        issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Book the terminal state of one STRANDED RESERVATION — a dispatch the
   * dequeue already marked `processing` whose `startSlot` never became a slot
   * because it wedged inside worktree creation (#307).
   *
   * TWO steps, not one. Round 3 released only the queue mark, on the claim that
   * "there is no slot, no orchestrator and no state service to tear down". That
   * is true and irrelevant: `AutonomousScheduler.enqueueItem` appends the item
   * to `state.Running` at DISPATCH time, before the extension builds anything,
   * and the only in-session release is `OnPipelineComplete` — reachable solely
   * through `IpcClient.autonomousComplete`, which bootstrap calls from
   * onSlotCompleted / onSlotFailed / onSlotDeferred. A reservation that fires
   * none of them holds a global MaxConcurrent seat until the scheduler
   * restarts, and `isRunning()` makes the issue permanently ineligible for
   * re-dispatch — the leaked-scheduler-seat defect this issue is named after,
   * reproduced by its own fix. `onSlotFailed` is the seam: it carries the
   * reservation's own index and repo, it is already how `startSlotInner`
   * reports a worktree-creation failure, and bootstrap's handler is
   * reservation-safe (`removePreparingSlot` is exactly the placeholder a
   * reservation owns; the tracker/subscription lookups are misses).
   */
  private async bookForceClearedReservation(
    issueNumber: number,
    reservation: SlotReservation
  ): Promise<void> {
    // ── CHECK-AND-CLAIM, before the first await ──────────────────────────
    this.forceClearedRunIds.add(reservation.runId);
    // Read BEFORE our own claim fires anything, for the same reason the slot
    // arm reads its flag up front: the question is whether the DISPATCH
    // reported an outcome, not whether anybody did (#305 review).
    const dispatchReportedItsOwnOutcome = reservation.ownTerminalOutcomeBooked === true;
    const booked = this.claimReservationOutcome(reservation, () =>
      this.callbacks.onSlotFailed?.(
        reservation.index,
        issueNumber,
        new Error(`Cancelled by user`),
        0, // no pipeline ran — there is no spend to report
        reservation.repo
      )
    );
    // ── claim decision made; awaits are safe from here on ────────────────

    this.logger.warn("Force-clearing stranded reservation — booking its terminal state (#307)", {
      slotIndex: reservation.index,
      issueNumber,
      runId: reservation.runId,
      dispatchAlreadyClaimedItsOwnOutcome: !booked,
      dispatchReportedItsOwnOutcome,
    });

    // #305: same card as the slot arm, gated the same way and issued the same
    // way — CONCURRENTLY with `completeQueueItem`, not chained after it, so the
    // funnel still pays one IPC timeout rather than two (see
    // `forceClearStuckSlots`' bound). A dispatch that wedged inside worktree
    // creation is as abandoned as one that wedged mid-stage; giving only one
    // arm the notification is the asymmetry this fence exists to prevent. No
    // stage: the pipeline never started one.
    //
    // `ownTerminalOutcomeBooked` — set inside `claimReservationOutcome` AFTER
    // the callback returns — is the suppressing signal, not the claim. On
    // today's code neither is ever set here: both `claimReservationOutcome`
    // call sites in `startSlotInner` are followed immediately by
    // `return "failed"`, whose `finally` releases the reservation, so a claimed
    // reservation is gone before the deadline can see it. The guard is the
    // structural half of the slot arm's, not dead weight — the invariant it
    // encodes ("only card a dispatch that reported nothing") must not depend on
    // which unwind path a future edit adds an await to.
    //
    // TWO SITUATIONS HERE TOO, and neither is the slot arm's. `booked === true`
    // is `reservation-never-started`: this dispatch wedged inside worktree
    // setup, so no stage ran, no agent wrote anything and the daemon was never
    // told about the run — the slot arm's "the worktree may hold uncommitted
    // work" and "the Go-side state may be stale" are both impossible, and round
    // 3 printed them anyway. `booked === false` means the dispatch had claimed
    // its outcome and this funnel stood down, which is the same
    // claim-taken-then-wedged hold the slot arm can hit.
    const raisePromise = dispatchReportedItsOwnOutcome
      ? undefined
      : this.raiseAbandonedDispatchCard(
          reservation.repo,
          issueNumber,
          undefined,
          booked ? "reservation-never-started" : "claim-taken-then-wedged"
        );

    await this.completeQueueItem(
      { issueNumber, repoName: reservation.repo },
      "abort deadline force-cleared a stranded reservation"
    );

    if (raisePromise) await raisePromise;

    // The `reservedSlots` entry is deliberately LEFT IN PLACE. It is what stops
    // a re-dispatch from colliding with the still-running `startSlot`, and that
    // dispatch removes its own entry (by run id) when it unwinds. A wedge
    // that never unwinds therefore holds the reserved capacity for the life of
    // the extension host — pre-existing, unchanged here, and recorded in the
    // force-clear-terminal-bookkeeping row.
    // terminal-parity:end force-clear-funnel
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
   * Store a pending PLATFORM run id for an issue before fillSlots() creates
   * the slot. Applied to {@link PipelineSlot.remoteRunId} when startSlot()
   * creates the PipelineSlot for that issueNumber.
   *
   * NOT this run's identity (ADR-017 Decision 2). The dispatch mints its own
   * UUIDv7 in `startSlot`; this value comes from the dashboard trigger's ack
   * and exists only so the platform's cancel/approve/reject commands — which
   * address a run by the id THEY minted — reach the right slot.
   * @see Issue #3552 — cancel command handler
   */
  setPendingRemoteRunId(issueNumber: number, remoteRunId: string): void {
    this.pendingRemoteRunIds.set(issueNumber, remoteRunId);
  }

  /**
   * Drop a pending platform run id that will never be consumed because the
   * dispatch was abandoned before a slot opened (e.g. an enqueue refused by
   * the stop guard after the ack already returned one). Leaving it set would
   * let a future, unrelated dispatch of the same issueNumber wrongly adopt it.
   * @see Issue #4118 — dashboard trigger enqueue path
   */
  clearPendingRemoteRunId(issueNumber: number): void {
    this.pendingRemoteRunIds.delete(issueNumber);
  }

  /**
   * Find the issueNumber whose active slot carries the given PLATFORM run id.
   * Returns null if no active slot matches (e.g., pipeline already completed).
   *
   * Deliberately matches on `remoteRunId`, never on the slot's own identity:
   * the caller is a platform command quoting the id the platform assigned, so
   * comparing it against a locally-minted UUIDv7 would never match anything.
   * @see Issue #3552 — cancel command handler
   */
  findSlotByRemoteRunId(remoteRunId: string): number | null {
    for (const [issueNumber, slot] of this.slots) {
      if (slot.remoteRunId === remoteRunId) return issueNumber;
    }
    return null;
  }

  /**
   * Cancel the pipeline slot identified by the platform's run id.
   * Sets userCancelled=true so the slot completion handler suppresses failure
   * bookkeeping, then calls gracefulStop(SIGTERM → 10s → SIGKILL).
   * Returns true if a slot was found and stop initiated, false if no match.
   * @see Issue #3552 — cancel command handler
   */
  async cancelByRemoteRunId(remoteRunId: string): Promise<boolean> {
    const issueNumber = this.findSlotByRemoteRunId(remoteRunId);
    if (issueNumber === null) return false;
    const slot = this.slots.get(issueNumber);
    if (!slot) return false;
    slot.userCancelled = true;
    await slot.orchestrator.gracefulStop(10_000);
    return true;
  }

  /**
   * Forward an approval decision to the slot identified by the platform's run id.
   * Returns true if a slot was found and approve() called, false if no match.
   * @see Issue #3553 — approve command handler
   */
  approveByRemoteRunId(remoteRunId: string): boolean {
    const issueNumber = this.findSlotByRemoteRunId(remoteRunId);
    if (issueNumber === null) return false;
    const slot = this.slots.get(issueNumber);
    if (!slot) return false;
    slot.orchestrator.approve();
    return true;
  }

  /**
   * Reject the approval gate for the slot identified by the platform's run id.
   * Returns true if a slot was found and reject() called, false if no match.
   * @see Issue #3553 — reject command handler
   */
  rejectByRemoteRunId(remoteRunId: string): boolean {
    const issueNumber = this.findSlotByRemoteRunId(remoteRunId);
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
