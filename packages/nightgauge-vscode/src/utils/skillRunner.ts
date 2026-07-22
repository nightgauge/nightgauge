/**
 * Skill Runner utility
 *
 * Runs Nightgauge pipeline skills via Claude Code CLI in headless or interactive mode.
 *
 * ## Execution Modes
 *
 * **Headless Mode** (`runStageSkillHeadless`):
 * - Uses `-p --output-format stream-json` flags
 * - stdin closed immediately after prompt (`stdin.end()`)
 * - Token tracking via stream-json parsing
 * - For automated pipelines and batch processing
 *
 * **Interactive Mode** (`runStageSkillInteractive`):
 * - No `-p` flag, stdin stays open for user messages
 * - Raw text output (no token tracking)
 * - For conversational single-stage execution
 * - Supports mid-execution user input via `writeToInteractiveProcess()`
 *
 * Each skill invocation starts a fresh Claude session (context isolation)
 * to prevent token accumulation across pipeline stages.
 *
 * Status updates come via ContextWatcherService which detects:
 * - running-*.json files → Stage started
 * - issue-*.json, planning-*.json, etc. → Stage completed
 *
 * Token usage is extracted from stream-json output and emitted via callbacks
 * (headless mode only).
 *
 * @see docs/ARCHITECTURE_DIAGRAMS.md - Token Counting Data Flow
 * @see docs/INTERACTIVE_MODE.md - Interactive vs Headless architecture
 * @see Issue #433 - config.yaml (formerly nightgauge.yaml)
 * @see Issue #495 - Interactive process spawning
 */

import * as vscode from "vscode";
import { spawn, execFileSync, type ChildProcess } from "child_process";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { randomUUID } from "crypto";
import type { PipelineStage } from "@nightgauge/sdk";
import type { StallEvent } from "../schemas/stallEvents";
import type { StallEscalationLevel, PauseForStallPayload } from "../schemas/pipelineState";
import {
  AutoModelSelector,
  ExperimentManager,
  GeminiContextGenerator,
  CodexContextGenerator,
  CodexMcpProvisioner,
  parsePhaseMarker,
  parsePhaseMarkers,
  createPhaseInference,
  TraceRecorder,
  validateModelForAdapter,
  isAgenticAdapter,
  AdapterError,
  clampTier,
  type ModelSelectionResult,
  type IssueMetadata,
  type ExperimentAssignment,
  type ModelEnvelope,
  type ModelTier,
} from "@nightgauge/sdk";
import {
  parseStreamJsonLine,
  extractTokenUsage,
  TokenAccumulator,
  LiveStageEstimator,
  resolveStageBookedUsage,
  calculateRateLimitWait,
  isHardRateLimit,
  isQuotaPressureSignal,
  isAnthropicSessionLimit,
  parseSessionLimitResetsAt,
  type ParsedTokenUsage,
  type RateLimitEventData,
} from "./tokenParser";
import { resolveConfigPathSync, logDeprecationWarning } from "./configPathResolver";
import { readEffectiveConfigTextSync } from "./mergedConfigReader";
import { shouldEmitSnapshot } from "./budgetStreamEnforcement";
import { resolveExtensionBundleRoot } from "./extensionBundle";
import { BinaryResolver } from "../services/BinaryResolver";
import { killProcessTree, DescendantTracker } from "./processTree";
import { RepositoryContextLoader } from "../services/RepositoryContextLoader";
import { ConnectivityStateBus } from "../platform/ConnectivityStateBus";
import {
  getAuthProvider,
  getExecutionAdapter,
  getDefaultModel,
  getFallbackModel,
  getMaxTurns,
  getCostBudget,
  getStageModel,
  getStageOverrideModel,
  getStallThresholds,
  getStallKillMultiplier,
  getStallIdleMs,
  getQuotaSignalIdleMs,
  shouldQuotaFastFail,
  getStageHardCapMs,
  getStageCostCapUsd,
  getEffectiveStageCostCap,
  getStageCostWarnMultiplier,
  getRunwayCeilingUsd,
  getBudgetEvalCadenceMs,
  getStageTimeCapMs,
  getCalibratedStallData,
  getAutonomousStallConfig,
  getLargeDiffThreshold,
  getConfidenceThreshold,
  getMinimumModel,
  getModelRoutingMode,
  getStageEffort,
  getExplicitStageEffort,
  conformEffortForFable,
  getExperimentConfig,
  getGeminiModel,
  getGeminiAuthMethod,
  resolveCodexPipelineModel,
  getCodexModel,
  getCodexCliCommand,
  getCodexCliArgs,
  getCodexResumeEnabled,
  getCopilotModel,
  getLmStudioModel,
  getLmStudioBaseUrl,
  getLmStudioApiKey,
  getLmStudioTimeoutMs,
  getStageModelsMatrix,
  getTypeOverrides,
  modelSupportsEffort,
  getStageMcpTools,
  getMcpToolsConfig,
  getSuperchargeCodexModel,
  getPerformanceMode,
  getModeEnvelope,
  type ModeEnvelope,
  getModeStageProfile,
  getModeStageAdapterModel,
  getGitHubUser,
  getGitHubAuthToken,
  getGitHubAuthTokens,
  getProgressRunawayConfig,
  type AuthProvider,
  type ClaudeEffort,
  type ExecutionAdapter,
  type DefaultModel,
  type PipelineModelOverride,
} from "./incrediConfig";
import { isCostAwareRoutingEnabled, getCostPerSuccessContext } from "./costAwareRouting";
import { withBehavioralPreamble } from "./behavioralPreamble";
import { ProgressMonitor, recordToolCallProgress, isBlindMonitorKill } from "./progressMonitor";
import {
  resolveStageAdapter,
  walkAdapterFallback,
  enumerateAvailableAdapters,
  type AdapterDecision,
  type AdapterSource,
  type AutoRouterOptions,
} from "./resolvers/adapterResolver";

// Re-export for consumers
export type { ParsedTokenUsage, RateLimitEventData } from "./tokenParser";
export { formatRateLimitCountdown } from "./tokenParser";
export type { AdapterDecision, AdapterSource } from "./resolvers/adapterResolver";

/**
 * Default timeout for interactive processes (30 minutes).
 * After this period of inactivity, the process will be terminated.
 *
 * Can be overridden via .nightgauge/config.yaml:
 * ```yaml
 * execution:
 *   interactive:
 *     timeout_minutes: 60
 * ```
 *
 * @see docs/INTERACTIVE_MODE.md
 * @see Issue #495
 */
export const INTERACTIVE_TIMEOUT_MS = 30 * 60 * 1000;

/**
 * Maps pipeline stages to their skill directory names
 * Bookend stages (pipeline-start, pipeline-finish) have no skill files
 */
const STAGE_TO_SKILL_DIR: Record<PipelineStage, string> = {
  "pipeline-start": "", // Bookend stage - executed synchronously
  "issue-pickup": "nightgauge-issue-pickup",
  "feature-planning": "nightgauge-feature-planning",
  "feature-dev": "nightgauge-feature-dev",
  "feature-validate": "nightgauge-feature-validate",
  "pr-create": "nightgauge-pr-create",
  "pr-merge": "nightgauge-pr-merge",
  "pipeline-finish": "", // Bookend stage - executed synchronously
};

/**
 * Stage order for pipeline progression
 * Includes bookend stages at start and end
 */
const STAGE_ORDER: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/**
 * Result from running a skill
 */
export interface SkillRunResult {
  success: boolean;
  exitCode: number | null;
  error?: Error;
  /** Total token usage for this skill run */
  tokenUsage?: ParsedTokenUsage;
  /**
   * True when {@link tokenUsage} is the live in-stage estimate booked as a
   * kill-path fallback (#296) rather than the authoritative terminal-`result`
   * total. Set only when a stage was killed mid-flight before the CLI emitted
   * its terminal envelope, so the accumulator was empty and the
   * `LiveStageEstimator` snapshot was booked instead. Undefined on the normal
   * (reconciled) path. Lets downstream consumers weight an estimated datapoint
   * differently from an authoritative one.
   */
  costEstimated?: boolean;
  /** Session ID for conversation resumption (Issue #118) */
  sessionId?: string;
  /**
   * Whether the subagent attempted an interactive prompt (e.g., AskUserQuestion)
   * during headless execution. When true, the exit was likely premature — the
   * agent asked a question no one could answer. @see Issue #697
   */
  promptDetected?: boolean;
  /** Model decision for this stage (Issue #732) */
  modelDecision?: ModelDecision;
  /**
   * Adapter decision for this stage (Issue #3223).
   *
   * Records the resolved adapter and the precedence step that produced it
   * (env / stage-config / global-config / fallback / default), so the
   * orchestrator can persist per-stage adapter provenance into history records
   * (`HistoryStageTokenUsageSchema.adapter_source`). Populated by
   * `runStageSkillHeadless` from `resolveStageAdapter` and any fallback hop.
   */
  adapterDecision?: AdapterDecision;
  /**
   * Whether the stage was terminated due to budget exceeded (Issue #835).
   * When true, the process was killed because cost exceeded the hard budget limit.
   */
  budgetExceeded?: boolean;
  /**
   * Whether the process was killed due to stall detection (Issue #1620).
   * When true, the process exceeded stall_kill_multiplier × stall_threshold
   * and was forcibly terminated.
   */
  stallKilled?: boolean;
  /**
   * Whether the process was aborted via autonomous stall pause dialog (Issue #2656).
   * When true, the user (or auto-abort timer) chose to abort during an
   * autonomous mode stall pause. Distinct from stallKilled (which is the
   * silent kill path used in interactive mode).
   */
  stallAborted?: boolean;
  /**
   * Whether the process was killed because the stage's accumulated cost
   * exceeded the runaway ceiling (max($75, effectiveCap × runaway_ceiling_multiplier)).
   * Treated as a transient stall-kill — no queue halt, no autonomous pause.
   * Backward-compatible: also true on old [cost-cap-exceeded] paths for consumers
   * that only check this flag. (Issue #3002, revised #3508)
   */
  costCapExceeded?: boolean;
  /**
   * Whether the cost warn threshold was crossed during this stage execution.
   * When true, a non-blocking toast was emitted but the stage was not killed.
   * (Issue #3508)
   */
  costWarnFired?: boolean;
  /**
   * The dollar ceiling that was actually crossed to trigger the kill (USD).
   * Only populated when a real dollar ceiling fired. It is `undefined` for the
   * progress-based runaway kill (the sole current setter of `costCapExceeded`)
   * because that path fires on stalled *productive progress*, not on crossing a
   * dollar amount — reporting the $75 runaway-ceiling floor here previously
   * produced the nonsensical "Cost $4.51 exceeded ceiling ($75.00)" telemetry
   * for a run that never approached the ceiling. (Issue #3002, corrected #266)
   */
  costCapUsd?: number;
  /**
   * Cost observed at the polling tick that triggered the kill (USD). Only
   * populated when `costCapExceeded` is true. (Issue #3002)
   */
  costAtTerminationUsd?: number;
  /**
   * Error classification from local stderr detection or Go taxonomy (Issue #2573).
   * Set when the process exits with a non-zero code.
   */
  errorCategory?: ErrorCategory;
  /**
   * For rate limit errors, the exact wait duration in milliseconds until the
   * limit resets. Derived from the last rate_limit_event's resetsAt field.
   */
  retryAfterMs?: number;
  /**
   * Stall detection events accumulated during this skill execution (Issue #2652).
   * Contains one entry per state change: warn threshold reached, user response,
   * or forcible kill. Empty array means the stage ran without stalls.
   * Absent when no stall events were recorded.
   */
  stallEvents?: StallEvent[];
  /**
   * Last lines of stdout/stderr captured at terminal failure (≤200KB).
   * Forwarded to the Go scheduler via pipeline.stageResult so the V3 RunRecord
   * in the daily JSONL carries the trailing output snippet. Only populated on
   * non-zero exit. (Issue #3207)
   */
  lastOutputLines?: string;

  // ─── Issue #3605 stage-exit diagnostic fields ───────────────────────────
  // Forwarded verbatim to Go via pipeline.stageResult so the daily exit-record
  // (.nightgauge/pipeline/exit-records/<UTC-day>.jsonl) carries enough
  // forensic detail to debug failures without re-running. All optional —
  // empty fields are dropped at the IPC boundary so healthy runs stay terse.

  /**
   * POSIX signal name delivered to the subprocess at kill time (SIGTERM /
   * SIGKILL). Empty when the process exited naturally. (#3605)
   */
  signal?: string;
  /**
   * Source code path that delivered `signal`: "stall-kill" | "hard-cap" |
   * "quota-fast-fail" | "processTree-reaper" | "external" | "". (#3605)
   */
  signalSource?: string;
  /**
   * Total wall time from spawn to exit in milliseconds. Captured by the
   * SkillRunner because Go's stageStartedAt brackets the deterministic-merge
   * fast path and over-reports for that fast path. (#3605)
   */
  elapsedMs?: number;
  /**
   * Milliseconds since the last subprocess output chunk at the moment of
   * exit. Distinguishes wedged-then-killed (large) from killed-mid-activity
   * (small). (#3605)
   */
  idleMsAtExit?: number;
  /**
   * Cache-creation tokens for the stage. The CLI emits this as a distinct
   * usage field from cache-read tokens; the exit-record stores both so
   * cost analysis can attribute the cache-priming cost separately. (#3605)
   */
  cacheCreationTokens?: number;
  /**
   * The most recent Bash tool_use command observed in the stream, truncated
   * to 500 chars. Common forensic anchor — many silent kills happen mid-Bash.
   * (#3605)
   */
  lastBashCommand?: string;
  /**
   * Exit code of the Bash tool_result matching `lastBashCommand`, when it
   * landed before the stage exited. Number-or-undefined so Go can preserve
   * the "never observed" distinction via pointer nullability. (#3605)
   */
  lastBashExit?: number;
  /**
   * True when the stream included a stop-hook error notification before
   * the stage exited. (#3605)
   */
  stopHookErrored?: boolean;
  /**
   * The last 4 KB of stderr from a ring buffer that survives line splits
   * (subprocess stderr lands as raw bytes; the ring keeps only the trailing
   * 4 KB so the on-disk record stays compact). (#3605)
   */
  stderrTail?: string;
  /**
   * The model that actually served the stage per the CLI stream — the LAST
   * model observed on system/init, assistant `message.model`, or a refusal
   * fallback event. Undefined when the stream carried no model info. The
   * claude CLI silently retries safety-refused turns on a fallback model
   * (`model_refusal_fallback`) and still exits 0, so the requested model is
   * not guaranteed to be the serving one. Forwarded to Go via
   * pipeline.stageResult for cost/telemetry/history attribution. (#91)
   * See docs/spikes/fable-5-behavior-porting.md §8.3.
   */
  servedModel?: string;
  /**
   * The CLI's silent model swap after a safety refusal, when one was
   * observed (#91). Attribution + notification only — never used to retry.
   */
  modelRefusalFallback?: {
    originalModel: string;
    fallbackModel: string;
    category?: string;
  };
}

/**
 * Execution mode for skill execution (Issue #496)
 * - 'headless': Automated execution with stream-json and token tracking
 * - 'interactive': Conversational execution with raw text output
 */
export type SkillExecutionMode = "headless" | "interactive";

/**
 * Error category from local stderr detection or Go taxonomy classifier.
 * @see Issue #2573 — CLI error classification
 */
export type ErrorCategory = "rate_limit" | "auth" | "network" | "token_limit" | "unknown";

/**
 * Callbacks for streaming skill output
 */
export interface SkillRunCallbacks {
  onStdout?: (data: string) => void;
  onStderr?: (data: string) => void;
  onComplete?: (result: SkillRunResult) => void;
  onError?: (error: Error) => void;
  /**
   * Fired once the stage's model is resolved, BEFORE the CLI is spawned, so
   * the resolved model is recorded up-front and ANY termination path — a
   * budget/stall/cost-cap/retry-exhaustion kill, or any other early exit that
   * never reaches completeStage — still attributes the correct model instead
   * of the platform bucketing the stage's cost as `unknown` (#367). This
   * generalizes the four kill-callsite fixes from #365: attribution no longer
   * depends on which termination path runs. The Go notify handler records the
   * model on every transition (latest-wins, empties ignored), so a concrete
   * `servedModel` at completion still overrides this up-front value.
   */
  onModelResolved?: (stage: PipelineStage, model: string, adapter: string) => void;
  /** Called when token usage is detected from stream-json output */
  onTokenUsage?: (usage: ParsedTokenUsage) => void;
  /**
   * Called with a LIVE in-stage token/cost estimate while a stage is still
   * running (#233), throttled to at most once per 5s. The estimate is derived
   * from per-turn `assistant` message usage via a SEPARATE `LiveStageEstimator`
   * (latest-wins input/cache_read, summed output, pricing-table cost) — NOT the
   * authoritative `TokenAccumulator`, which is reconciled from the terminal
   * `result` envelope and continues to drive `onTokenUsage`/`onComplete`.
   * Consumers forward this to the platform via `pipeline.notifyStageProgress`.
   */
  onStageProgress?: (usage: ParsedTokenUsage) => void;
  /**
   * Called with a LIVE in-stage cost snapshot for BUDGET ENFORCEMENT while a
   * stage is still running (Issue #254), throttled on a configurable cadence
   * (`pipeline.budget_eval_cadence_ms`, default 5s). Carries the same
   * `LiveStageEstimator` estimate as {@link onStageProgress} but is a SEPARATE
   * hook so enforcement cadence is decoupled from platform-telemetry cadence.
   *
   * The consumer (HeadlessOrchestrator) feeds `usage.costUsd` into the same
   * `BudgetEnforcer`/`PipelineBudgetCeiling` decision logic used at stage end,
   * so wind-down → warn → terminate fire MID-stage. This is an estimate and
   * must NOT be booked: the authoritative terminal `result` envelope (via
   * `onTokenUsage`) remains the single source of recorded cost.
   */
  onCostSnapshot?: (usage: ParsedTokenUsage) => void;
  /**
   * Called when a tool_use block is detected in stream-json output
   * Used for handling interactive tools like AskUserQuestion
   * @param toolName - The name of the tool being invoked
   * @param toolInput - The parsed input/arguments for the tool
   * @param toolUseId - The unique ID for this tool use (for matching tool_result)
   */
  onToolUse?: (toolName: string, toolInput: unknown, toolUseId?: string) => void;
  /**
   * Called when session ID is detected in result message (Issue #118)
   * Used for resuming conversation with AskUserQuestion responses
   */
  onSessionId?: (sessionId: string) => void;
  /**
   * Called when execution mode is determined (Issue #496)
   * Used by OutputWindow to update its rendering mode
   * @param mode - The execution mode ('headless' or 'interactive')
   */
  onMode?: (mode: SkillExecutionMode) => void;
  /**
   * Called when a phase marker is detected in streaming text output.
   * Phase markers are HTML comments emitted by skills:
   * `<!-- phase:start name="..." index=N total=N stage="..." -->`
   * Used by PipelineBridge to notify Go via pipeline.notifyPhaseTransition IPC.
   */
  onPhaseStart?: (stage: PipelineStage, name: string, index: number, total: number) => void;
  /**
   * Called when a tool_use block is detected in stream-json output (Issue #639)
   * Used for recording tool calls to PipelineStateService → Dashboard
   * @param toolName - The name of the tool being invoked
   * @param toolInput - The parsed input/arguments for the tool
   * @param toolUseId - The unique ID for this tool use (for matching tool_result) (Issue #1031)
   */
  onToolCall?: (toolName: string, toolInput: unknown, toolUseId?: string) => void;
  /**
   * Called when a tool_result is detected in stream-json output (Issue #1031)
   * Used for backfilling duration_ms, result, and error on tool call records
   * @param toolUseId - The tool_use_id that this result corresponds to
   * @param result - The tool result content (truncated)
   * @param isError - Whether the tool call resulted in an error
   */
  onToolResult?: (toolUseId: string, result: string, isError: boolean) => void;
  /**
   * Called when a stage completes after a stall warning was shown (Issue #797)
   * Used to auto-remove stall warning entries from the output window
   */
  onStallWarningClear?: () => void;
  /**
   * Called when a stall warning is shown at 1×, 2×, or 3× threshold (Issue #2655).
   * Used to update persistent UI indicators (status bar + output panel entries).
   * Distinct from onStallEvent which fires for all stall actions (kill, stop, etc.).
   * @param event - The stall event with elapsed_ms and threshold_ms
   * @param multiplier - Escalation level: 1 = first warning, 2 = 2× threshold, etc.
   */
  onStallWarning?: (event: StallEvent, multiplier: number) => void;
  /**
   * Called when a rate_limit_event is detected in stream-json output (Issue #2573).
   * The SkillRunner does NOT pause — it notifies via callback and the CLI manages
   * its own retry/wait behavior. This callback is for UI messaging only.
   * @param event - Rate limit event data with computed waitMs
   */
  onRateLimitEvent?: (event: RateLimitEventData & { waitMs: number }) => void;
  /**
   * Called when a stall event occurs during skill execution (Issue #2652).
   * Emitted at warn threshold, user prompt response, and stall auto-kill.
   * @param event - The stall event with timestamp, elapsed_ms, threshold_ms, action
   */
  onStallEvent?: (event: StallEvent) => void;

  /**
   * Called at each escalation level in autonomous mode (Issue #2656).
   * Fires for status_bar, output_panel, notification, discord, pause.
   * @param level - The escalation level reached
   * @param event - The stall event with elapsed/threshold
   */
  onStallEscalation?: (level: StallEscalationLevel, event: StallEvent) => void;

  /**
   * Called when autonomous stall pause is triggered (Issue #2656).
   * Returns the user's decision: "resume" continues execution, "abort" kills the process.
   * If this callback is not set, autonomous escalation falls back to normal stall kill.
   * @param payload - Pause context including issue number, stage, elapsed time, timeout
   * @returns Promise resolving to "resume" or "abort"
   */
  onStallPause?: (payload: PauseForStallPayload) => Promise<"resume" | "abort">;
}

/**
 * Active skill process handle
 */
export interface SkillProcessHandle {
  process: ChildProcess;
  stage: PipelineStage;
  issueNumber?: number;
  kill: () => void;
  /**
   * Whether the process is currently waiting for user input
   * (e.g., AskUserQuestion tool)
   */
  waitingForInput?: boolean;
  /**
   * Session ID for resuming conversation (Issue #118)
   * Set when result message is received, used with --resume flag
   */
  sessionId?: string;
  /**
   * Whether this is an interactive process (Issue #495)
   * Interactive processes have open stdin and support mid-execution input.
   */
  isInteractive?: boolean;
  /**
   * Write a message to the process stdin (Issue #495)
   * Only available for interactive processes. Returns false if stdin unavailable.
   */
  writeToStdin?: (message: string) => boolean;
  /**
   * Productive-progress accessor for the orchestrator's escalation gates
   * (Issue #3851). Returns the cumulative count of PRODUCTIVE progress signals
   * (commits / new-file writes / phase markers / CI progress) observed by the
   * stage's ProgressMonitor. The HeadlessOrchestrator snapshots this before a
   * budget/ceiling escalation and compares against a later snapshot: if the
   * delta is flat the stage is churning (do NOT escalate — auto-commit + stop);
   * if the delta is positive the stage is making real progress (escalate).
   * Undefined for execution paths without a ProgressMonitor (e.g. interactive).
   */
  getProductiveProgressDelta?: () => number;
}

// Track active processes for cleanup
const activeProcesses: Map<string, SkillProcessHandle> = new Map();
const OUTPUT_ERROR_TAIL_MAX_CHARS = 8192;
/** Check interval for stall detection (decoupled from warning threshold) */
const HEADLESS_STALL_CHECK_INTERVAL_MS = 30_000;

/**
 * Stall-threshold multiple at which the previously no-op Nx escalation warning
 * (Issue #3851) escalates to a runaway kill — but ONLY when there has been no
 * productive progress over the no-progress window. 8× is far above any healthy
 * long-but-progressing run (which keeps resetting the productive window) yet
 * bounds a churning stage that warns forever without stopping.
 */
const NX_RUNAWAY_KILL_MULTIPLE = 8;

/**
 * Fast-fail idle threshold once a quota-exhausted `rate_limit_event` has
 * been observed (Issue #3425, follow-up to #3386).
 *
 * When the agent's 5-hour Anthropic bucket is depleted with overage rejected
 * for `out_of_credits`, every subsequent API turn silently waits for the
 * bucket to reset. The agent emits zero stdout for the duration. To the
 * idle watchdog this is indistinguishable from a true wedge — until the
 * `rate_limit_event` arrives. After that, treating idle silence as anything
 * other than quota-bounded waste is wrong: the default `stallKillMs` for
 * feature-dev is 80 minutes, so the runner sits burning $13–$23 per attempt
 * waiting for an upstream wall to fall.
 *
 * Once a quota-exhausted signal is in hand, fast-fail at this short idle
 * threshold (default 120s = 2 minutes) instead. The 120s number matches the
 * acceptance criteria in #3386 and gives the CLI enough wall-clock to ship
 * any in-flight chunks (assistant message close, tool_result echo) before
 * the watchdog declares quota exhaustion.
 */
const QUOTA_EXHAUSTED_FAST_FAIL_IDLE_MS = 120_000;

/**
 * Idle-kill floor applied when stall calibration is cold (killSec 0), #252.
 * Cold start used to disable the idle-kill outright, which made a wedged or
 * silent session unkillable: cost-gated detectors never activate at $0, and
 * the elapsed hard-cap is progress-gated. 30 minutes of total output silence
 * is far beyond any healthy session's quietest span (calibrated repos run
 * with ~20 min idle kills on the slowest stages) while still bounding the
 * 9-hour zombie class observed 2026-07-18.
 */
const COLD_START_IDLE_KILL_FLOOR_MS = 30 * 60 * 1000;

/**
 * Model decision source annotation for observability.
 *
 * Indicates where the model selection came from in the resolution chain:
 * - `env`: Environment variable override
 * - `config`: Explicit per-stage config in `.nightgauge/config.yaml`
 * - `stage-default`: Built-in default for lightweight stages (haiku)
 * - `auto`: AutoModelSelector chose based on issue complexity
 * - `default`: Global default or hardcoded fallback
 *
 * @since Issue #732
 */
export type ModelSource =
  | "env"
  | "config"
  | "stage-default"
  | "auto"
  /** AutoProviderRouter chose the (adapter, model) pair (Issue #3230) */
  | "auto-router"
  | "experiment"
  | "default"
  | "feedback-escalation"
  | "user-override"
  /** Performance mode override (efficiency / maximum) supplied a stage profile (Issue #3009) */
  | "performance-mode"
  /** Legacy supercharge override — retained for back-compat with telemetry/history (Issue #2433) */
  | "supercharge";

/**
 * Model decision result with source annotation.
 *
 * Attached to `SkillRunResult.modelDecision` after each stage execution,
 * enabling downstream consumers (Dashboard, execution history) to observe
 * which model was selected and why.
 *
 * @since Issue #732
 */
export interface ModelDecision {
  model: string;
  source: ModelSource;
  /** AutoModelSelector result when source is 'auto' */
  selectionResult?: ModelSelectionResult;
  /** Model routing mode from config (Issue #734) */
  mode?: "manual" | "automatic" | "hybrid";
  /** Claude effort level passed via --effort when resolved (Issue #934) */
  effort?: ClaudeEffort;
  /** Experiment assignment when source is 'experiment' (Issue #949) */
  experimentAssignment?: ExperimentAssignment;
  /** The model that was active before escalation, when source is 'feedback-escalation' (Issue #1343) */
  escalatedFrom?: string;
}

/**
 * Enforce a minimum model floor.
 *
 * If the selected model is lighter than the minimum, returns the minimum.
 * Model tier ordering: haiku (0) < sonnet (1) < opus (2).
 *
 * @param selected - The model selected by AutoModelSelector
 * @param minimum - The minimum model floor (from config)
 * @returns The selected model or the minimum, whichever is more capable
 *
 * @see Issue #732 - AutoModelSelector integration
 */
function enforceMinimumModel(
  selected: DefaultModel,
  minimum: DefaultModel | undefined
): DefaultModel {
  if (!minimum) return selected;
  const tiers: Record<DefaultModel, number> = { haiku: 0, sonnet: 1, opus: 2, fable: 3 };
  return tiers[selected] >= tiers[minimum] ? selected : minimum;
}

/**
 * Built-in per-stage model defaults for lightweight stages.
 *
 * These stages either perform simple, well-defined tasks OR use
 * deterministic context generation first — in either case, Haiku
 * is sufficient for the LLM's reduced (or fallback-only) role.
 *
 * issue-pickup: re-added in Issue #2614. The LLM now only runs as a fallback
 * when generateDeterministicIssueContext() fails, so Haiku is appropriate.
 * pr-create/pr-merge: create a PR, wait for merge — no complex reasoning needed.
 *
 * @see Issue #972 - Add per-stage model defaults for lightweight stages
 * @see Issue #1593 - Issue-pickup uses Sonnet for classification
 * @see Issue #2614 - Deterministic-first execution (issue-pickup back to haiku)
 */
const LIGHTWEIGHT_STAGE_DEFAULTS: Partial<Record<PipelineStage, DefaultModel>> = {
  "issue-pickup": "haiku",
  "pr-create": "haiku",
  // pr-merge removed (#197): since the two-path design landed, the LLM never
  // sees an easy merge — the deterministic runner merges every clean case
  // and punts to the LLM only when something is WRONG (blocked merge state,
  // failing checks, dirty state). Issue size is irrelevant to that
  // difficulty; routing the punt path to the cheapest tier assigned the
  // weakest model to the hardest instances (bowlsheet#233: haiku improvised
  // an admin bypass). pr-merge resolves to sonnet via DEFAULT_STAGE_MODELS.
};

/**
 * Count total lines changed (insertions + deletions) in the current branch vs main.
 * Returns 0 on any error so callers fall through to the default model.
 */
function getDiffLineCount(workspaceRoot: string): number {
  try {
    const result = execFileSync("git", ["diff", "main", "--shortstat"], {
      cwd: workspaceRoot,
      encoding: "utf-8",
      timeout: 5000,
    });
    const insMatch = result.match(/(\d+) insertion/);
    const delMatch = result.match(/(\d+) deletion/);
    return (insMatch ? parseInt(insMatch[1], 10) : 0) + (delMatch ? parseInt(delMatch[1], 10) : 0);
  } catch {
    return 0;
  }
}

/**
 * Resolve the effective model for a pipeline stage using the full resolution chain.
 *
 * Resolution order:
 * 0. Performance mode override (Issue #3009 — replaces supercharge from #2433).
 *    `efficiency` / `maximum` look up the per-stage profile in MODE_PROFILES
 *    and return a ModelDecision with `source: "performance-mode"`. `elevated`
 *    supplies no overrides and falls through to the existing routing chain.
 * 1. getStageModel() — env var > config override > defaults (mode-aware)
 * 1.5. LIGHTWEIGHT_STAGE_DEFAULTS — built-in per-stage defaults (mode-agnostic)
 * 1.6. getStageOverrideModel() — adaptive policy routing override (automatic/hybrid only)
 * 2. AutoModelSelector — when getStageModel returns undefined (automatic/hybrid)
 * 3. getDefaultModel() — global default
 * 4. 'sonnet' — hardcoded final fallback
 *
 * @param stage - The pipeline stage
 * @param workspaceRoot - Workspace root path
 * @param issueMetadata - Optional issue metadata for AutoModelSelector
 * @returns ModelDecision with the resolved model and its source
 *
 * @see Issue #732 - AutoModelSelector integration
 * @internal Exported for testing only
 */
/**
 * Build the AutoRouterOptions bag for `resolveStageAdapter`. Returns
 * `undefined` when essentials (workspaceRoot, issueMetadata) are missing so
 * the resolver bypasses Step 2.5 cleanly.
 *
 * The router runs only when:
 * - The workspace root is known (auth probes need a config root).
 * - Issue metadata is present (router needs complexity to pick a model tier).
 *
 * Auth-validated adapters are enumerated lazily via `validateAdapterPrerequisites`.
 *
 * @see Issue #3230 — AutoProviderRouter wiring
 * @internal
 */
function buildAutoRouterOptions(
  _stage: PipelineStage,
  workspaceRoot: string | undefined,
  issueMetadata: IssueMetadata | undefined
): AutoRouterOptions | undefined {
  if (!workspaceRoot || !issueMetadata) {
    return undefined;
  }
  const selector = new AutoModelSelector();
  // Use a probe stage to pull complexity through the same path as the router
  // model pick. Stage value doesn't matter for extractComplexity-equivalent
  // behavior — the result's `complexity` field is stage-agnostic.
  const probeResult = selector.selectModel(_stage, issueMetadata);
  const issueType = selector.extractIssueType(issueMetadata);
  const routingMode = getModelRoutingMode(workspaceRoot);
  return {
    enumerateAvailableAdapters: () =>
      enumerateAvailableAdapters((candidate) =>
        validateAdapterPrerequisites(candidate, workspaceRoot, "headless")
      ),
    complexity: probeResult.complexity,
    mode: routingMode,
    issueType,
    recentHistory: [],
  };
}

/** Effort levels in ascending reasoning depth, for envelope clamping (Issue #19). */
const EFFORT_ORDER: ClaudeEffort[] = ["low", "medium", "high", "xhigh"];

/**
 * Clamp a complexity-derived effort into the mode envelope's `[effortFloor,
 * effortCeiling]` (Issue #19). Efficiency caps at medium; Maximum raises to high.
 * When no effort is set, only a floor can introduce one.
 */
function clampEffortToEnvelope(
  effort: ClaudeEffort | undefined,
  envelope: ModeEnvelope
): ClaudeEffort | undefined {
  if (effort === undefined) return envelope.effortFloor;
  let i = EFFORT_ORDER.indexOf(effort);
  if (i < 0) return effort;
  if (envelope.effortFloor) i = Math.max(i, EFFORT_ORDER.indexOf(envelope.effortFloor));
  if (envelope.effortCeiling) i = Math.min(i, EFFORT_ORDER.indexOf(envelope.effortCeiling));
  return EFFORT_ORDER[i];
}

/**
 * Clamp a model tier into the mode envelope's `[floor, ceiling]` (Issue #19).
 * `DefaultModel` and the SDK `ModelTier` are the same union, so the cast is safe.
 */
function clampModelToEnvelope(model: DefaultModel, envelope: ModeEnvelope): DefaultModel {
  return clampTier(model as ModelTier, envelope as ModelEnvelope) as DefaultModel;
}

/**
 * Resolve the `--fallback-model` for a run, accounting for Fable's separate
 * Max-plan usage bucket. When a stage runs on **Fable**, default the CLI
 * fallback to **Opus** so a Fable-only usage limit or capacity overload degrades
 * gracefully mid-session (Claude Code retries the turn on the fallback model)
 * instead of hard-failing — Opus/Sonnet draw from a different quota bucket. A
 * user-configured non-Fable fallback still wins; a non-Fable run keeps whatever
 * was configured (possibly none).
 */
export function resolveFallbackModel(
  runModel: string | undefined,
  configuredFallback: DefaultModel | undefined
): DefaultModel | undefined {
  if (runModel === "fable") {
    return configuredFallback && configuredFallback !== "fable" ? configuredFallback : "opus";
  }
  return configuredFallback;
}

export function resolveModel(
  stage: PipelineStage,
  workspaceRoot: string,
  issueMetadata?: IssueMetadata,
  issueNumber?: number
): ModelDecision {
  const stageEffort = getStageEffort(stage, workspaceRoot, issueMetadata);

  // Step 0: Performance mode (Issue #3009, Issue #19)
  // Reads `NIGHTGAUGE_PERFORMANCE_MODE` env var or `.nightgauge/performance-mode.yaml`.
  //
  // Modes are policy ENVELOPES (Issue #19): a `[floor, ceiling]` band the
  // adaptive router selects within. A mode that still pins a stage explicitly
  // (Maximum) short-circuits here and returns the pin; every other mode
  // (efficiency/elevated/frontier) has no pins and flows through the routing
  // chain, where its envelope clamps the auto/lightweight/default result.
  // The canonical alias (haiku|sonnet|opus|fable) is consumed by Claude verbatim;
  // non-Claude adapters translate it downstream.
  // See docs/PERFORMANCE_MODES.md — Cross-adapter behavior.
  const performanceMode = getPerformanceMode(workspaceRoot);
  const envelope = getModeEnvelope(performanceMode);
  if (performanceMode !== "elevated") {
    const profile = getModeStageProfile(performanceMode, stage);
    if (profile?.model) {
      const routingModeForPerformance = getModelRoutingMode(workspaceRoot);
      return {
        model: profile.model,
        source: "performance-mode",
        mode: routingModeForPerformance,
        effort: profile.effort ?? clampEffortToEnvelope(stageEffort, envelope),
      };
    }
  }
  // Effort is clamped to the mode envelope (Efficiency caps at medium; Maximum
  // raises to high). Explicit config/env/experiment paths below keep their own
  // effort — only the automatic/lightweight/default paths use this value.
  const effort = clampEffortToEnvelope(stageEffort, envelope);

  // Step 1: Check stage-level config (env var > config > defaults based on mode)
  const routingMode = getModelRoutingMode(workspaceRoot);
  const stageModel = getStageModel(stage, workspaceRoot);
  if (stageModel !== undefined) {
    // getStageModel already handles env var priority internally
    // Determine if it came from env var or config
    const envKey = `NIGHTGAUGE_PIPELINE_STAGE_MODEL_${stage.toUpperCase().replace(/-/g, "_")}`;
    const source: ModelSource = process.env[envKey] ? "env" : "config";
    return { model: stageModel, source, mode: routingMode, effort };
  }

  // Step 1.5: Per-stage defaults for lightweight stages (mode-agnostic)
  // Fires when no explicit config/env override is set. Ensures lightweight
  // stages always route to Haiku without needing AutoModelSelector metadata.
  // Exception: pr-create escalates to sonnet when the diff is large, to avoid
  // stalls where haiku cannot produce a complete PR for big changesets.
  const lightweightDefault = LIGHTWEIGHT_STAGE_DEFAULTS[stage];
  if (lightweightDefault !== undefined) {
    if (stage === "pr-create") {
      const threshold = getLargeDiffThreshold(workspaceRoot);
      if (threshold > 0) {
        const diffLines = getDiffLineCount(workspaceRoot);
        if (diffLines > threshold) {
          // Clamp to the mode envelope (e.g. Efficiency caps at sonnet anyway;
          // Maximum's floor would raise it — though Maximum returns pins earlier).
          return {
            model: clampModelToEnvelope("sonnet", envelope),
            source: "stage-default",
            mode: routingMode,
            effort,
          };
        }
      }
    }
    return {
      model: clampModelToEnvelope(lightweightDefault, envelope),
      source: "stage-default",
      mode: routingMode,
      effort,
    };
  }

  // Step 1.6: Adaptive policy routing override (Issue #1571)
  // Long-horizon override from health-gated policies — applied after env var
  // (env always wins) and lightweight defaults, but before experiment and
  // AutoModelSelector. Only active in automatic or hybrid modes (manual mode
  // uses explicit stage_models which are handled in Step 1).
  if (routingMode === "automatic" || routingMode === "hybrid") {
    const policyOverride = getStageOverrideModel(stage, workspaceRoot);
    if (policyOverride !== undefined) {
      return {
        model: policyOverride,
        source: "config",
        mode: routingMode,
        effort,
      };
    }
  }

  // Step 1.7: Active A/B experiment override (Issue #949)
  // Experiments override auto-selection but not explicit env/config overrides.
  if (issueNumber !== undefined && issueNumber > 0) {
    const experimentConfig = getExperimentConfig(workspaceRoot);
    if (experimentConfig) {
      const assignment = ExperimentManager.assign(issueNumber, stage, {
        name: experimentConfig.name,
        active: experimentConfig.active,
        control: experimentConfig.control,
        treatment: experimentConfig.treatment,
        split_percent: experimentConfig.split_percent,
        target_stages: experimentConfig.target_stages,
        min_runs: experimentConfig.min_runs,
        observation_window: experimentConfig.observation_window,
        min_effect_size: experimentConfig.min_effect_size,
      });
      if (assignment) {
        return {
          model: assignment.model as DefaultModel,
          source: "experiment",
          mode: routingMode,
          effort: assignment.effort as ClaudeEffort | undefined,
          experimentAssignment: assignment,
        };
      }
    }
  }

  // Step 2: getStageModel returned undefined → automatic or hybrid mode
  // Try AutoModelSelector if issue metadata is available
  if (issueMetadata) {
    try {
      const stageMatrix = getStageModelsMatrix(workspaceRoot);
      const typeOverrides = getTypeOverrides(workspaceRoot);
      const selectorConfig: Record<string, unknown> = {};
      if (stageMatrix) selectorConfig.stageMatrix = stageMatrix;
      if (typeOverrides) selectorConfig.typeOverrides = typeOverrides;
      const selector = new AutoModelSelector(
        Object.keys(selectorConfig).length > 0
          ? (selectorConfig as Record<string, unknown>)
          : undefined
      );
      // Cost-aware auto-tune (Issue #21): feed historical cost-per-success so
      // the selector can prefer a cheaper model at comparable success. The pick
      // is still clamped to the envelope below, so this operates strictly within
      // the mode band. Warm cache only — never blocks; fail-open to undefined.
      const costPerSuccess = isCostAwareRoutingEnabled(workspaceRoot)
        ? getCostPerSuccessContext(workspaceRoot)
        : undefined;

      // Pass the mode envelope (Issue #19): the selector clamps to [floor,
      // ceiling], caps upgrades at the ceiling, and — only under a fable
      // ceiling — may escalate L/XL planning/dev to Fable.
      const result = selector.selectModel(
        stage,
        issueMetadata,
        undefined,
        costPerSuccess,
        envelope
      );
      const threshold = getConfidenceThreshold(workspaceRoot);
      const minModel = getMinimumModel(stage, workspaceRoot);

      if (result.confidence >= threshold) {
        // enforceMinimumModel may raise above the selector's pick; re-clamp to
        // the envelope ceiling so a config minimum can't exceed the mode band.
        const model = clampModelToEnvelope(
          enforceMinimumModel(result.model as DefaultModel, minModel),
          envelope
        );

        return {
          model,
          source: "auto",
          selectionResult: result,
          mode: routingMode,
          effort,
        };
      }
      // Low confidence → fall through to default
    } catch (error) {
      // AutoModelSelector failure → fall through to default (graceful degradation)
      console.error(`[skillRunner] AutoModelSelector failed for stage ${stage}:`, error);
    }
  }

  // Step 3: Global default (clamped to the mode envelope)
  const defaultModel = getDefaultModel(workspaceRoot);
  if (defaultModel) {
    return {
      model: clampModelToEnvelope(defaultModel, envelope),
      source: "default",
      mode: routingMode,
      effort,
    };
  }

  // Step 4: Hardcoded fallback (clamped to the mode envelope)
  return {
    model: clampModelToEnvelope("sonnet", envelope),
    source: "default",
    mode: routingMode,
    effort,
  };
}

function appendTail(buffer: string, chunk: string, maxChars: number): string {
  const combined = buffer + chunk;
  if (combined.length <= maxChars) {
    return combined;
  }
  return combined.slice(-maxChars);
}

function formatElapsed(ms: number): string {
  const totalSeconds = Math.max(1, Math.floor(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes > 0) {
    return `${minutes}m ${seconds}s`;
  }
  return `${seconds}s`;
}

/**
 * If `workspaceRoot` looks like a per-issue worktree
 * (`<canonical>/[.]worktrees/issue-<N>`), return the canonical repo root.
 * Otherwise return null.
 *
 * Used by the stall and cost-cap diagnostic writers (#3204) to mirror their
 * output to the canonical workspace, where AutoRetroService looks. Without
 * the mirror, concurrent-mode runs write the diagnostic into the worktree
 * which is cleaned up after the failure — leaving the retro with no
 * actionable evidence and forcing it to the unknown / pattern-fallback path.
 */
function deriveCanonicalFromWorktree(workspaceRoot: string): string | null {
  const norm = path.resolve(workspaceRoot);
  const segments = norm.split(path.sep);
  for (let i = segments.length - 1; i > 0; i--) {
    if (/^issue-\d+$/.test(segments[i])) {
      const parent = segments[i - 1];
      if (parent === "worktrees" || parent === ".worktrees") {
        // Slice off both the `issue-N` segment AND the worktree base.
        const canonical = segments.slice(0, i - 1).join(path.sep);
        if (canonical && canonical !== norm) return canonical;
        return null;
      }
    }
  }
  return null;
}

/**
 * Write a diagnostic file to the issue's history directory under
 * `workspaceRoot`, and ALSO mirror it under the canonical workspace when
 * `workspaceRoot` is a worktree. Logs the path(s) via the provided onStderr
 * callback for observability. Best-effort: failures are surfaced as warnings
 * and never thrown.
 */
function writeDiagnosticWithMirror(
  workspaceRoot: string,
  issueNumber: number,
  filename: string,
  content: string,
  onStderr?: (s: string) => void
): void {
  const targets = [workspaceRoot];
  const canonical = deriveCanonicalFromWorktree(workspaceRoot);
  if (canonical) targets.push(canonical);

  for (const target of targets) {
    try {
      const histDir = path.join(target, ".nightgauge", "pipeline", "history", String(issueNumber));
      fs.mkdirSync(histDir, { recursive: true });
      const diagFile = path.join(histDir, filename);
      fs.writeFileSync(diagFile, content, "utf-8");
      onStderr?.(`[skillRunner] Diagnostic written to: ${diagFile}\n`);
    } catch (err) {
      onStderr?.(`[skillRunner] WARNING: Failed to write diagnostic to ${target}: ${err}\n`);
    }
  }
}

// @internal Exported for testing only
export function inferProcessError(
  success: boolean,
  stderrText: string,
  stdoutText: string,
  exitCode: number | null
): Error | undefined {
  if (success) {
    return undefined;
  }

  if (stderrText.length > 0) {
    return new Error(stderrText.split("\n").slice(-3).join("\n"));
  }

  if (stdoutText.length > 0) {
    const streamJsonOutcome = extractStreamJsonError(stdoutText);
    if (streamJsonOutcome.kind === "error") {
      return streamJsonOutcome.error;
    }
    if (streamJsonOutcome.kind === "success") {
      // Protocol-level success — do not invent an error message. The non-zero
      // exit code is still surfaced by the exit-code fallback below.
      return undefined;
    }

    const tailError = extractTailError(stdoutText);
    if (tailError) {
      return tailError;
    }
  }

  return new Error(`Process exited with code ${exitCode ?? "unknown"}`);
}

type StreamJsonOutcome = { kind: "error"; error: Error } | { kind: "success" } | { kind: "absent" };

// Claude Code stream-json emits a terminal envelope `{type:"result", is_error, result, subtype}`
// when the CLI exits. When is_error is true, `result` carries the real cause. When it is false
// the CLI succeeded at the protocol level even if the process returned non-zero (e.g. wrapper
// script issues), so we do not invent an error message from stream bodies.
export function extractStreamJsonError(stdoutText: string): StreamJsonOutcome {
  const lines = stdoutText.split("\n");
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = lines[i]?.trim();
    if (!line || line[0] !== "{") continue;
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(line) as Record<string, unknown>;
    } catch {
      continue;
    }
    if (parsed["type"] !== "result") continue;
    if (parsed["is_error"] === true) {
      const result = parsed["result"];
      const subtype = parsed["subtype"];
      const base =
        typeof result === "string" && result.trim().length > 0
          ? result.trim()
          : typeof subtype === "string" && subtype.trim().length > 0
            ? subtype.trim()
            : "Stream-json result envelope reported is_error without a message";
      const message =
        typeof subtype === "string" && subtype !== base ? `${subtype}: ${base}` : base;
      // Anthropic session/usage limits arrive here as a normal is_error result
      // (e.g. "success: You've hit your session limit · resets 10:30am
      // (America/Denver)"), NOT as a structured rate_limit_event. Normalize
      // them to the canonical environmental-quota marker so the existing
      // recovery path engages — global quota cooldown until resetsAt, no
      // terminal halt, no lifetime-failure-cap increment, auto-resume on reset
      // — instead of failing the run and pausing for manual triage. #3792.
      if (isAnthropicSessionLimit(message)) {
        const resetsAt = parseSessionLimitResetsAt(message);
        const resetSuffix = resetsAt !== undefined ? ` resetsAt=${resetsAt}` : "";
        return {
          kind: "error",
          error: new Error(`[rate-limit-quota-exhausted] ${message};${resetSuffix}`),
        };
      }
      return { kind: "error", error: new Error(message) };
    }
    return { kind: "success" };
  }
  return { kind: "absent" };
}

// Fallback for non-stream-json output (Codex structured JSON lines, plain text).
// Walks backward through non-empty lines, skipping SDK tool_result / tool_use / assistant /
// user wrappers so we do not present file contents as an error message.
function extractTailError(stdoutText: string): Error | undefined {
  const candidates: string[] = [];
  const lines = stdoutText.split("\n");
  for (let i = lines.length - 1; i >= 0 && candidates.length < 3; i--) {
    const line = lines[i]?.trim();
    if (!line) continue;
    if (line[0] === "{") {
      let parsed: Record<string, unknown> | null;
      try {
        parsed = JSON.parse(line) as Record<string, unknown>;
      } catch {
        parsed = null;
      }
      if (parsed) {
        // Codex adapter: { level, message } or { error }
        if (typeof parsed["message"] === "string" && (parsed["message"] as string).trim()) {
          return new Error((parsed["message"] as string).trim());
        }
        if (typeof parsed["error"] === "string" && (parsed["error"] as string).trim()) {
          return new Error((parsed["error"] as string).trim());
        }
        // Stream-json wrapper we do not want to echo back as an "error"
        const type = parsed["type"];
        if (
          type === "user" ||
          type === "assistant" ||
          type === "system" ||
          type === "tool_result" ||
          type === "tool_use" ||
          "tool_use_result" in parsed
        ) {
          continue;
        }
      }
    }
    candidates.unshift(line);
  }
  if (candidates.length === 0) return undefined;
  return new Error(candidates.join("\n"));
}

/**
 * Classify an error from stderr content using local keyword detection.
 *
 * Matches rate limit, auth, network, and token limit indicators.
 * Returns "unknown" if no pattern matches.
 *
 * @see Issue #2573 — CLI error classification
 * @internal Exported for testing only
 */
export function classifyError(stderrText: string): ErrorCategory {
  const lower = stderrText.toLowerCase();
  if (
    lower.includes("rate limit") ||
    lower.includes("429") ||
    lower.includes("too many requests") ||
    lower.includes("quota exceeded") ||
    // Anthropic session/usage limit — transient, same recovery class. #3792
    lower.includes("session limit") ||
    lower.includes("usage limit")
  ) {
    return "rate_limit";
  }
  if (
    lower.includes("401") ||
    lower.includes("403") ||
    lower.includes("unauthorized") ||
    lower.includes("forbidden") ||
    lower.includes("permission denied")
  ) {
    return "auth";
  }
  if (
    lower.includes("connection") ||
    lower.includes("timeout") ||
    lower.includes("econnreset") ||
    lower.includes("network") ||
    lower.includes("dns")
  ) {
    return "network";
  }
  if (
    lower.includes("token limit") ||
    lower.includes("context length") ||
    lower.includes("max_tokens")
  ) {
    return "token_limit";
  }
  return "unknown";
}

/**
 * Get the next stage after the given stage
 */
export function getNextStage(currentStage: PipelineStage): PipelineStage | null {
  const currentIndex = STAGE_ORDER.indexOf(currentStage);
  if (currentIndex === -1 || currentIndex === STAGE_ORDER.length - 1) {
    return null;
  }
  return STAGE_ORDER[currentIndex + 1];
}

/**
 * Get human-readable stage label
 */
export function getStageLabel(stage: PipelineStage): string {
  const labels: Record<PipelineStage, string> = {
    "pipeline-start": "Initialize",
    "issue-pickup": "Issue Pickup",
    "feature-planning": "Feature Planning",
    "feature-dev": "Feature Development",
    "feature-validate": "Feature Validation",
    "pr-create": "PR Creation",
    "pr-merge": "PR Merge",
    "pipeline-finish": "Completion",
  };
  return labels[stage];
}

/**
 * Find the SKILL.md file for a stage.
 *
 * Exported for use by context schema repair (Issue #2552).
 */
export function findSkillFile(stage: PipelineStage): string | null {
  const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!workspaceRoot) return null;

  const skillDir = STAGE_TO_SKILL_DIR[stage];

  // Try different possible locations (first match wins)
  const possiblePaths = [
    // In skills/ directory (nightgauge source repo — dev mode)
    path.join(workspaceRoot, "skills", skillDir, "SKILL.md"),
    // In claude-plugins/nightgauge/commands/ (legacy plugin location)
    path.join(workspaceRoot, "claude-plugins", "nightgauge", "commands", `${skillDir}.md`),
  ];

  // Fallback: skills bundled inside the extension's dist/ directory.
  // This is how standalone repos (customers) find pipeline skills without
  // needing the nightgauge source repo as a workspace sibling.
  const ext = vscode.extensions.getExtension("nightgauge.nightgauge-vscode");
  // Self-heal past a garbage-collected extension dir (auto-update GC): resolve
  // to the newest surviving sibling version so bundled skills still load (#3883).
  const bundleRoot = resolveExtensionBundleRoot(ext?.extensionPath);
  if (bundleRoot) {
    possiblePaths.push(path.join(bundleRoot, "dist", "skills", skillDir, "SKILL.md"));
  }

  for (const skillPath of possiblePaths) {
    if (fs.existsSync(skillPath)) {
      return skillPath;
    }
  }

  return null;
}

/**
 * Expand <!-- include: relative/path.md --> directives in skill content.
 * Resolves paths relative to the SKILL.md file's directory.
 * Non-existent includes are left as-is (graceful degradation for portability).
 *
 * @see Issue #862 - Extract shared SKILL.md content to common includes
 */
function expandIncludes(content: string, skillDir: string): string {
  return content.replace(/<!-- include: (.+?) -->/g, (_match, relativePath: string) => {
    const absPath = path.resolve(skillDir, relativePath.trim());
    try {
      return fs.readFileSync(absPath, "utf-8");
    } catch {
      // Leave directive as-is if file not found (portability)
      return _match;
    }
  });
}

/**
 * Read MCP server names from .claude/settings.json (project-level MCP config)
 * and return wildcard tool name patterns for each server.
 *
 * Returns [] with a warning log if the config file is not found or unreadable.
 *
 * @see Issue #1725
 */
function readAllMcpToolPatterns(workspaceRoot: string): string[] {
  const settingsPath = path.join(workspaceRoot, ".claude", "settings.json");
  try {
    if (!fs.existsSync(settingsPath)) return [];
    const raw = fs.readFileSync(settingsPath, "utf-8");
    const settings = JSON.parse(raw) as {
      mcpServers?: Record<string, unknown>;
    };
    const servers = Object.keys(settings.mcpServers ?? {});
    // Return wildcard patterns: mcp__{serverName}__*
    return servers.map((name) => `mcp__${name}__*`);
  } catch {
    // Non-fatal: if config is unreadable, skip MCP tools
    return [];
  }
}

/**
 * Resolve mcp-tools patterns to concrete tool names.
 *
 * - Empty array → returns [] (no-op, preserves current behavior)
 * - ['all'] → reads .claude/settings.json to enumerate server names,
 *   returns ['mcp__{server}__*', ...] patterns for each server
 * - Any other patterns → passed through as-is (specific names or patterns
 *   the caller passes directly to --allowedTools)
 *
 * @see Issue #1725
 */
function resolveMcpTools(mcpTools: string[], workspaceRoot: string): string[] {
  if (mcpTools.length === 0) return [];

  if (mcpTools[0] === "all") {
    return readAllMcpToolPatterns(workspaceRoot);
  }

  // Pass through specific names / patterns as-is
  return mcpTools;
}

/**
 * Read and parse SKILL.md content
 * Extracts the allowed-tools and programmatic-tools from frontmatter and the instructions.
 * Expands <!-- include: --> directives before returning content.
 *
 * @see Issue #1066 - programmatic-tools frontmatter support
 */
function readSkillFile(skillPath: string): {
  content: string;
  allowedTools: string[];
  mcpTools: string[];
  programmaticTools?: string[];
} | null {
  try {
    const raw = fs.readFileSync(skillPath, "utf-8");
    const content = expandIncludes(raw, path.dirname(skillPath));

    // Extract allowed-tools from frontmatter
    const frontmatterMatch = content.match(/^---\n([\s\S]*?)\n---/);
    let allowedTools: string[] = ["Read", "Write", "Edit", "Glob", "Grep", "Bash", "Task"];
    let mcpTools: string[] = [];
    let programmaticTools: string[] | undefined;

    if (frontmatterMatch) {
      const frontmatter = frontmatterMatch[1];
      const toolsMatch = frontmatter.match(/allowed-tools:\s*(.+)/);
      if (toolsMatch) {
        allowedTools = toolsMatch[1].trim().split(/\s+/);
      }

      // Parse mcp-tools: space-separated MCP tool names or 'all' (Issue #1725, #1726)
      const mcpMatch = frontmatter.match(/mcp-tools:\s*(.+)/);
      if (mcpMatch) {
        const raw = mcpMatch[1].trim();
        mcpTools = raw === "all" ? ["all"] : raw.split(/\s+/);
      }

      // Parse programmatic-tools: space-separated tool names (Issue #1066)
      const ptcMatch = frontmatter.match(/programmatic-tools:\s*(.+)/);
      if (ptcMatch) {
        programmaticTools = ptcMatch[1].trim().split(/\s+/);
      }
    }

    // Strip frontmatter from the content passed to the executing agent.
    // Frontmatter keys like `agent:` and `context: fork` are metadata for the
    // skill-invocation layer — they've already been extracted above. Leaving
    // them in the prompt causes Claude to interpret them as subagent-routing
    // directives and wrap the entire stage in a Task subagent whose synthesized
    // prompt omits the phase markers, breaking phase tracking in the sidebar.
    const contentBody = frontmatterMatch
      ? content.slice(frontmatterMatch[0].length).trimStart()
      : content;

    return { content: contentBody, allowedTools, mcpTools, programmaticTools };
  } catch {
    return null;
  }
}

/**
 * Parse skill content from an injected string (platform-resolved).
 * Same frontmatter parsing logic as readSkillFile() but operates on a string
 * instead of reading from disk. Skips expandIncludes() since platform content
 * is already fully rendered.
 *
 * @see Issue #1473 - Platform skill resolution
 */
function parseSkillContent(raw: string): {
  content: string;
  allowedTools: string[];
  mcpTools: string[];
  programmaticTools?: string[];
} | null {
  try {
    const frontmatterMatch = raw.match(/^---\n([\s\S]*?)\n---/);
    let allowedTools: string[] = ["Read", "Write", "Edit", "Glob", "Grep", "Bash", "Task"];
    let mcpTools: string[] = [];
    let programmaticTools: string[] | undefined;

    if (frontmatterMatch) {
      const frontmatter = frontmatterMatch[1];
      const toolsMatch = frontmatter.match(/allowed-tools:\s*(.+)/);
      if (toolsMatch) {
        allowedTools = toolsMatch[1].trim().split(/\s+/);
      }

      const mcpMatch = frontmatter.match(/mcp-tools:\s*(.+)/);
      if (mcpMatch) {
        const rawMcp = mcpMatch[1].trim();
        mcpTools = rawMcp === "all" ? ["all"] : rawMcp.split(/\s+/);
      }

      const ptcMatch = frontmatter.match(/programmatic-tools:\s*(.+)/);
      if (ptcMatch) {
        programmaticTools = ptcMatch[1].trim().split(/\s+/);
      }
    }

    // Strip frontmatter — same rationale as readSkillFile above.
    const contentBody = frontmatterMatch ? raw.slice(frontmatterMatch[0].length).trimStart() : raw;

    return { content: contentBody, allowedTools, mcpTools, programmaticTools };
  } catch {
    return null;
  }
}

function commandExists(command: string): boolean {
  if (process.env.VITEST === "true") {
    return true;
  }

  const pathEnv = process.env.PATH ?? "";
  const paths = pathEnv.split(path.delimiter).filter(Boolean);
  for (const dir of paths) {
    const fullPath = path.join(dir, command);
    if (fs.existsSync(fullPath)) {
      return true;
    }
  }
  return false;
}

export function validateAdapterPrerequisites(
  adapter: ExecutionAdapter,
  workspaceRoot: string,
  mode: SkillExecutionMode
): string | null {
  // Agentic truth-gate (#57): chat-completion-only adapters (gemini-sdk,
  // ollama, lm-studio) have no tool loop — a pipeline stage dispatched to
  // them emits prose instead of commits. This gate covers primary dispatch,
  // the fallback walker, and auto-router enumeration (all funnel through
  // here). Eval/judge surfaces don't run this check and keep chat-only
  // adapters.
  if (!isAgenticAdapter(adapter)) {
    return (
      `The ${adapter} adapter is chat-completion-only (no agentic tool loop): ` +
      `pipeline stages cannot edit files, run shell commands, or call gh through it. ` +
      'Switch to an agentic adapter (claude, codex, gemini, copilot) via "Nightgauge: Switch Execution Adapter".'
    );
  }

  if (adapter === "claude") {
    if (!commandExists("claude")) {
      return (
        "Claude adapter selected, but `claude` CLI is not available in PATH. " +
        'Install Claude Code CLI or switch to Codex adapter via "Nightgauge: Switch Execution Adapter".'
      );
    }
    return null;
  }

  if (adapter === "gemini") {
    if (mode === "interactive") {
      return (
        "Gemini adapter supports headless execution only. " +
        'Use "Nightgauge: Run Stage" (headless) or switch adapter back to Claude for interactive mode.'
      );
    }
    if (!commandExists("gemini")) {
      return (
        "Gemini adapter selected, but `gemini` CLI is not available in PATH. " +
        "Install Gemini CLI or switch to gemini-sdk adapter."
      );
    }
    return null;
  }

  if (adapter === "gemini-sdk") {
    if (mode === "interactive") {
      return (
        "Gemini SDK adapter supports headless execution only. " +
        'Use "Nightgauge: Run Stage" (headless) or switch adapter back to Claude for interactive mode.'
      );
    }
    // gemini-sdk doesn't need CLI — it uses @google/genai directly.
    // API key validation happens at runtime in GeminiSdkAdapter.validateAuth().
    return null;
  }

  if (adapter === "lm-studio") {
    if (mode === "interactive") {
      return (
        "LM Studio adapter supports headless execution only. " +
        'Use "Nightgauge: Run Stage" (headless) or switch adapter back to Claude for interactive mode.'
      );
    }
    // LM Studio uses direct HTTP — no CLI executable needed.
    // Model must be configured (empty string = runtime error in LmStudioAdapter).
    const lmModel = getLmStudioModel(workspaceRoot);
    if (!lmModel) {
      return (
        "LM Studio adapter requires a model name. " +
        "Set NIGHTGAUGE_LM_STUDIO_MODEL or add lm_studio.model to .nightgauge/config.yaml. " +
        'Example: lm_studio:\n  model: "lmstudio-community/Meta-Llama-3.1-8B-Instruct-GGUF"'
      );
    }
    // Still need node + SDK CLI for run-stage.sh execution
    if (!commandExists("node")) {
      return "LM Studio adapter requires `node` in PATH.";
    }
    const lmCliEntry = path.join(
      workspaceRoot,
      "packages",
      "nightgauge-sdk",
      "dist",
      "cli",
      "index.js"
    );
    if (!fs.existsSync(lmCliEntry)) {
      return (
        `LM Studio adapter requires built Nightgauge SDK CLI at ${lmCliEntry}. ` +
        "Run: npm run -w @nightgauge/sdk build"
      );
    }
    return null;
  }

  if (adapter === "copilot") {
    if (mode === "interactive") {
      return (
        "Copilot adapter supports headless execution only. " +
        'Use "Nightgauge: Run Stage" (headless) or switch adapter back to Claude for interactive mode.'
      );
    }

    const stageRunnerPath = path.join(workspaceRoot, "scripts", "run-stage.sh");
    if (!fs.existsSync(stageRunnerPath)) {
      return `Unified stage runner not found at ${stageRunnerPath}.`;
    }

    for (const requiredTool of ["node", "git", "gh"]) {
      if (!commandExists(requiredTool)) {
        return (
          `Copilot adapter requires \`${requiredTool}\` in PATH. ` +
          `Install it or switch adapter to Claude.`
        );
      }
    }

    if (!commandExists("copilot")) {
      return (
        "Copilot adapter selected, but `copilot` CLI is not available in PATH. " +
        "Install the GitHub Copilot Coding Agent CLI or switch to a different adapter."
      );
    }

    const cliEntry = path.join(
      workspaceRoot,
      "packages",
      "nightgauge-sdk",
      "dist",
      "cli",
      "index.js"
    );
    if (!fs.existsSync(cliEntry)) {
      return (
        `Copilot adapter requires built Nightgauge SDK CLI at ${cliEntry}. ` +
        "Run: npm run -w @nightgauge/sdk build"
      );
    }

    return null;
  }

  // Remaining: codex adapter.
  // Interactive Codex launches the Codex TUI directly in a terminal (#4024), so
  // it only needs the `codex` CLI — not the headless run-stage.sh / SDK-CLI /
  // node/git/gh chain that the JSON pipeline path requires.
  if (mode === "interactive") {
    const codexCmd = getCodexCliCommand(workspaceRoot);
    if (!commandExists(codexCmd)) {
      return (
        `Codex adapter selected, but the \`${codexCmd}\` CLI is not available in PATH. ` +
        "Install the Codex CLI or switch to a different adapter."
      );
    }
    return null;
  }

  const stageRunnerPath = path.join(workspaceRoot, "scripts", "run-stage.sh");
  if (!fs.existsSync(stageRunnerPath)) {
    return `Unified stage runner not found at ${stageRunnerPath}.`;
  }

  for (const requiredTool of ["node", "git", "gh"]) {
    if (!commandExists(requiredTool)) {
      return (
        `Codex adapter requires \`${requiredTool}\` in PATH. ` +
        `Install it or switch adapter to Claude.`
      );
    }
  }

  const cliEntry = path.join(
    workspaceRoot,
    "packages",
    "nightgauge-sdk",
    "dist",
    "cli",
    "index.js"
  );
  if (!fs.existsSync(cliEntry)) {
    return (
      `Codex adapter requires built Nightgauge SDK CLI at ${cliEntry}. ` +
      "Run: npm run -w @nightgauge/sdk build"
    );
  }

  return null;
}

/**
 * Resolve a GitHub token for injection into skill subprocess environments.
 *
 * Uses the same priority chain as IpcClientBase.resolveGitHubToken():
 *   1. github_auth.token in config.yaml (supports env:VAR_NAME)
 *   2. github_auth.tokens[owner] in config.yaml (supports env:VAR_NAME)
 *   3. GITHUB_TOKEN environment variable
 *   4. Per-repo github_user → gh auth token --user X (synchronous)
 *   5. gh auth token (default user, synchronous)
 *
 * Returns { token, source } where source describes the resolution method
 * for logging and debugging. Returns null if no token could be resolved.
 *
 * @param workspaceRoot - Workspace root path for config reading
 * @returns Resolved token and source, or null if no token available
 *
 * @see Issue #2670 - Config-based token resolution
 */
export function resolveTokenForSubprocess(
  workspaceRoot?: string
): { token: string; source: string } | null {
  // 1. Project-level token from config.yaml github_auth.token
  const directToken = getGitHubAuthToken(workspaceRoot);
  if (directToken) {
    return { token: directToken, source: "config (github_auth.token)" };
  }

  // 2. Per-org token from config.yaml github_auth.tokens[owner]
  // Prefer the token matching the configured owner; fall back to first available entry.
  const tokensMap = getGitHubAuthTokens(workspaceRoot);
  if (Object.keys(tokensMap).length > 0) {
    const configuredUser = getGitHubUser(workspaceRoot);
    if (configuredUser && tokensMap[configuredUser]) {
      return {
        token: tokensMap[configuredUser],
        source: `config (github_auth.tokens.${configuredUser})`,
      };
    }
    const [firstOwner, firstToken] = Object.entries(tokensMap)[0];
    return { token: firstToken, source: `config (github_auth.tokens.${firstOwner})` };
  }

  // 3. GITHUB_TOKEN environment variable
  if (process.env.GITHUB_TOKEN) {
    return { token: process.env.GITHUB_TOKEN, source: "env (GITHUB_TOKEN)" };
  }

  // 4. Per-repo github_user → gh auth token --user X (synchronous)
  const githubUser = getGitHubUser(workspaceRoot);
  if (githubUser) {
    try {
      const { execFileSync } = require("child_process") as typeof import("child_process");
      const token = execFileSync("gh", ["auth", "token", "--user", githubUser], {
        timeout: 5000,
        encoding: "utf-8",
      }).trim();
      if (token) {
        return { token, source: `gh (--user ${githubUser})` };
      }
    } catch {
      // gh auth failed for this user — continue to fallback
    }
  }

  // 5. Default gh auth token
  try {
    const { execFileSync } = require("child_process") as typeof import("child_process");
    const token = execFileSync("gh", ["auth", "token"], {
      timeout: 5000,
      encoding: "utf-8",
    }).trim();
    if (token) {
      return { token, source: "gh (default user)" };
    }
  } catch {
    // gh not installed or not authenticated
  }

  return null;
}

/*
 * Thinking is deliberately NOT disabled on Claude spawns.
 *
 * The forced CLAUDE_CODE_DISABLE_THINKING=1 workaround for #3801 (thinking-
 * block replay 400 on claude CLI 2.1.154) was removed after the bug stopped
 * reproducing on CLI 2.1.186 — three multi-turn replay runs with thinking
 * enabled (up to 26 turns / 9 replayed blocks) completed without a 400; see
 * docs/spikes/fable-5-behavior-porting.md §8.2. Every Claude spawn spreads
 * `...process.env` first, so an operator on an older CLI can restore the
 * workaround without a rebuild: export CLAUDE_CODE_DISABLE_THINKING=1.
 * Mirrors the Go adapters (claude.go / claude_sdk.go).
 */

/**
 * Load auto-accept configuration from nightgauge config file
 * Returns environment variables to pass to Claude CLI
 */
function loadAutoAcceptConfigSync(
  workspaceRoot: string,
  stage: PipelineStage
): Record<string, string> {
  const env: Record<string, string> = {
    // Always set CI=true to ensure non-interactive mode
    CI: "true",
  };

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(workspaceRoot);
    if (!pathResult.exists) {
      return env; // No config file, use defaults
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read config file synchronously
    const configContent = readEffectiveConfigTextSync(pathResult);
    // Parse YAML - using simple approach for now
    const lines = configContent.split("\n");
    let inHumanInTheLoop = false;
    let autoAcceptStages = false;
    let autoAcceptPermissions = false;
    const trustedStages: string[] = [];

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed.startsWith("human_in_the_loop:")) {
        inHumanInTheLoop = true;
        continue;
      }

      if (inHumanInTheLoop) {
        // Check for next top-level key
        if (
          trimmed &&
          !trimmed.startsWith("#") &&
          /^[a-z_]+:/.test(trimmed) &&
          !trimmed.startsWith("  ")
        ) {
          inHumanInTheLoop = false;
          continue;
        }

        if (trimmed.includes("auto_accept_stages:")) {
          autoAcceptStages = trimmed.includes("true");
        } else if (trimmed.includes("auto_accept_permissions:")) {
          autoAcceptPermissions = trimmed.includes("true");
        } else if (trimmed.startsWith("- ") && trustedStages.length > 0) {
          // Part of trusted_stages array
          const stageName = trimmed.substring(2).trim();
          trustedStages.push(stageName);
        } else if (trimmed.includes("trusted_stages:")) {
          // Start of array - check if inline or multiline
          const afterColon = trimmed.split("trusted_stages:")[1];
          if (afterColon && afterColon.trim().startsWith("[")) {
            // Inline array: [stage1, stage2]
            const match = afterColon.match(/\[(.*)\]/);
            if (match) {
              const items = match[1].split(",").map((s) => s.trim().replace(/['"]/g, ""));
              trustedStages.push(...items);
            }
          } else {
            // Multiline array - items on next lines
            trustedStages.push("__marker__"); // Marker to continue parsing
          }
        }
      }
    }

    // Remove marker if present
    const markerIndex = trustedStages.indexOf("__marker__");
    if (markerIndex >= 0) {
      trustedStages.splice(markerIndex, 1);
    }

    // Check if permissions should be auto-accepted
    // Respect environment variable overrides first
    if (process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS === "true") {
      env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS = "true";
    } else if (autoAcceptPermissions) {
      env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS = "true";
    }

    // Check if this specific stage should be auto-accepted
    // Respect environment variable overrides first
    if (process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES === "true") {
      env.NIGHTGAUGE_AUTO_ACCEPT_STAGES = "true";
    } else if (autoAcceptStages) {
      env.NIGHTGAUGE_AUTO_ACCEPT_STAGES = "true";
    } else if (trustedStages.includes(stage)) {
      // Stage is in trusted list
      env.NIGHTGAUGE_AUTO_ACCEPT_STAGES = "true";
    }
  } catch (error) {
    // Fail-safe: if config loading fails, don't enable auto-accept
    console.error("Failed to load auto-accept config:", error);
  }

  return env;
}

/**
 * Rewrite skill-relative read-directive paths to absolute host paths (#196).
 *
 * ADR-010's progressive-disclosure directives ("Read
 * `skills/<name>/_includes/foo.md` now…") assumed CWD is the nightgauge
 * repo root — only true when dogfooding nightgauge itself. Cross-repo
 * pipeline runs spawn with cwd = the TARGET repo's worktree, which has no
 * skills/ directory, so agents ran `find / -maxdepth 6` (~40s whole-fs
 * scans) and converged on ~/.codex/skills — a stale-version /
 * cross-adapter contamination hazard. The runner knows the absolute skill
 * directory at prompt-build time; rewrite the skill's OWN references (its
 * resolved dir basename, the canonical `nightgauge-<stage>` name, and the
 * prefix-stripped `<stage>` variant used by the plugin cache) plus the
 * sibling `skills/_shared/` — never cross-skill references, which must
 * keep naming the other skill.
 */
export function rewriteSkillRelativePaths(
  content: string,
  stage: PipelineStage,
  skillDir: string
): string {
  const dir = skillDir.replace(/[/\\]+$/, "");
  const sharedDir = path.join(path.dirname(dir), "_shared");
  let out = content.split("skills/_shared/").join(sharedDir + path.sep);
  const ownNames = new Set([path.basename(dir), `nightgauge-${stage}`, stage]);
  for (const name of ownNames) {
    out = out.split(`skills/${name}/`).join(dir + path.sep);
  }
  return out;
}

/**
 * Build the prompt for headless execution
 */
function buildSkillPrompt(
  stage: PipelineStage,
  skillContent: string,
  issueNumber?: number,
  skillDir?: string
): string {
  // Build context-specific intro
  let intro = `Execute the following pipeline skill instructions for the Nightgauge pipeline.
Stage: ${getStageLabel(stage)}\n\n`;

  if (issueNumber) {
    intro += `**Issue Number**: #${issueNumber}\n\n`;
  }

  if (skillDir) {
    intro += `**Skill directory**: \`${skillDir}\` — supporting files (\`_includes/\`, \`_shared/\`) live here, NOT under the current working directory. Never scan the filesystem for them (#196).\n\n`;
  }

  // Phase tracking override removed (#3466): skills now natively emit markers via
  // `printf '<!-- phase:start ... -->\n'` Bash commands. The runtime detects these
  // from Bash tool_use inputs in tokenParser.ts (lines 285-298), making phase tracking
  // model-agnostic without a runtime shim.
  intro += `---\n\n`;
  intro += skillDir ? rewriteSkillRelativePaths(skillContent, stage, skillDir) : skillContent;

  return intro;
}

/**
 * Resolve the directory the pipeline's plugin hooks live in, to be exported as
 * CLAUDE_PLUGIN_ROOT for the spawned Claude subprocess. The plugin's hooks.json
 * references its scripts as `$CLAUDE_PLUGIN_ROOT/hooks/<name>.sh`, so this value
 * MUST point at a directory that actually contains `hooks/` — otherwise every
 * PreToolUse/PostToolUse hook (including the deterministic "block push to main"
 * guard in gate.go) silently fails to resolve and never runs.
 *
 * Historically this was hardcoded to `workspaceFolders[0]/claude-plugins/
 * nightgauge`, which only exists when the primary workspace folder IS the
 * nightgauge source repo. For any other primary repo (platform, dashboard,
 * a standalone customer repo, or — as in the acmeapp incident — a separate
 * product workspace) that path does not exist, so the safety hooks were silently
 * absent and a pr-merge agent was able to push straight to main unguarded.
 *
 * Resolution order:
 *   1. Any workspace folder that has a live `claude-plugins/nightgauge`
 *      (dogfooding the nightgauge repo — live hooks win so edits apply
 *      without a republish).
 *   2. The extension's bundled `dist/claude-plugins/nightgauge` (ships in
 *      every VSIX — the universal fallback that guarantees customer repos and
 *      non-nightgauge workspaces still get the guards).
 *   3. undefined — only when neither is found (dev shell with no extension).
 */
export function resolvePluginRoot(): string | undefined {
  const hasHooks = (root: string): boolean => fs.existsSync(path.join(root, "hooks", "hooks.json"));

  // 1. Prefer a live workspace copy (any folder, not just [0]).
  for (const folder of vscode.workspace.workspaceFolders ?? []) {
    const candidate = path.join(folder.uri.fsPath, "claude-plugins", "nightgauge");
    if (hasHooks(candidate)) {
      return candidate;
    }
  }

  // 2. Fall back to the hooks bundled inside the extension's dist/ — always
  //    present, so non-nightgauge workspaces still get the safety guards.
  const ext = vscode.extensions.getExtension("nightgauge.nightgauge-vscode");
  const bundleRoot = resolveExtensionBundleRoot(ext?.extensionPath);
  if (bundleRoot) {
    const bundled = path.join(bundleRoot, "dist", "claude-plugins", "nightgauge");
    if (hasHooks(bundled)) {
      return bundled;
    }
  }

  return undefined;
}

/**
 * Resolve the nightgauge Go binary and return env vars that make it
 * discoverable to skill subprocesses under ANY adapter (Claude, Codex, Gemini,
 * …) — Issue #4029.
 *
 * The extension bundles the binary at `<extensionPath>/dist/bin/…`, which is
 * NOT on the subprocess PATH. Skills historically located it by globbing the
 * `~/.vscode/extensions/nightgauge.<version>/dist/bin/` directory — a
 * VSCode-extension-specific path that breaks portability (and missed the
 * published, platform-suffixed binary). Instead the host now resolves the
 * binary authoritatively (BinaryResolver) and exports:
 *   - `NIGHTGAUGE_BIN` — absolute path, honored first by the skill cascade
 *     and `claude-plugins/.../guard.sh`.
 *   - `PATH` — with the binary's dir prepended so bare `nightgauge …` and
 *     `command -v nightgauge` resolve in every provider's shell.
 *
 * Returns `{}` when the binary is already on PATH (BinaryResolver's filesystem
 * tiers miss) — the skill's own `command -v` cascade covers that case — or when
 * resolution throws (defensive: never block a stage spawn over discovery).
 */
function resolveBinaryDiscoveryEnv(): Record<string, string> {
  try {
    const binaryPath = BinaryResolver.fromVSCode().resolveSync();
    if (!binaryPath) return {};
    // Only export a path we can actually execute. If it exists but lacks the
    // execute bit, fall through to the skill's own cascade (which guards on
    // `[ -x ]`) rather than exporting a misleading NIGHTGAUGE_BIN.
    try {
      fs.accessSync(binaryPath, fs.constants.X_OK);
    } catch {
      return {};
    }
    const binDir = path.dirname(binaryPath);
    const sep = process.platform === "win32" ? ";" : ":";
    const currentPath = process.env.PATH ?? "";
    return {
      NIGHTGAUGE_BIN: binaryPath,
      PATH: currentPath ? `${binDir}${sep}${currentPath}` : binDir,
    };
  } catch {
    return {};
  }
}

const loadedUnderVitest = process.env.VITEST === "true";

/**
 * Emit a per-stage-run observability line to the extension-host console.
 *
 * Stage-start diagnostics (stall thresholds, cost caps, runaway ceilings,
 * time caps, autonomous escalation, phase markers) are operator-facing logs
 * printed straight to `console.log` — distinct from the pipeline output
 * channel wired through `callbacks.onStderr`. They carry no assertion value in
 * unit tests, yet `runStageSkillHeadless` is driven ~400 times across the
 * ~20 vitest suites that exercise it, producing hundreds of `console.log`
 * calls per file. Under vitest's default console interception every call
 * forwards an `onUserConsoleLog` RPC from the worker to the main process; that
 * volume races the worker's environment teardown, surfacing intermittently as
 * `EnvironmentTeardownError: Closing rpc while "onUserConsoleLog" was pending`
 * (Issue #263). Suppressing these lines under the vitest worker removes the
 * RPC pressure at its source for every current and future caller. Production
 * (extension host, where `VITEST` is unset) logs exactly as before.
 */
function logStageDiagnostic(message: string): void {
  if (loadedUnderVitest || process.env.VITEST === "true") {
    return;
  }
  console.log(message);
}

/**
 * Run a pipeline stage skill via Claude Code CLI in headless mode
 *
 * Reads the SKILL.md instructions and passes them as a prompt to `claude -p`.
 * Uses `--output-format stream-json` for real-time streaming output.
 *
 * Loads auto-accept configuration from .nightgauge/config.yaml and passes environment
 * variables to Claude CLI to control permission prompts and stage gates.
 *
 * @param stage - The pipeline stage to run
 * @param issueNumber - The issue number (required for most stages)
 * @param callbacks - Callbacks for streaming output
 * @param issueMetadata - Optional issue metadata for AutoModelSelector (Issue #732)
 * @param skipToPhase - Optional phase name to skip to via SKIP_TO_PHASE env var (Issue #1187)
 * @param injectedSkillContent - Optional platform-resolved SKILL.md body (Issue #1473)
 * @returns Handle to control the process
 */
export function runStageSkillHeadless(
  stage: PipelineStage,
  issueNumber?: number,
  callbacks?: SkillRunCallbacks,
  issueMetadata?: IssueMetadata,
  _batchContext?: unknown,
  skipToPhase?: string,
  modelOverride?: PipelineModelOverride,
  pauseAutoRouting?: boolean,
  pinnedWorkspaceRoot?: string,
  modelOverrideSource?: ModelSource,
  injectedSkillContent?: string,
  /** When true, stall handling uses escalation+pause instead of silent kill (Issue #2656) */
  autonomousMode?: boolean,
  /**
   * Cost warn threshold in USD (Issue #3508).
   * When stage cost exceeds this value, a non-blocking toast fires but the stage
   * continues. Computed by HeadlessOrchestrator as historicalMedian × warnMultiplier.
   * 0 or undefined = disabled (no history yet, or warn disabled by config).
   */
  warnThresholdUsd?: number,
  /**
   * The issue's intended owning repo as `owner/repo` (Issue #3867).
   *
   * In multi-repo (N:1) workspaces the worktree CWD belongs to the issue's
   * repo, but `getCurrentRepository()` returns the workspace PRIMARY repo —
   * so deriving the repo-identity assertion from it leaks the wrong repo into
   * `NIGHTGAUGE_TARGET_REPO` and every stage's repo-mismatch gate fails
   * (AcmeApp #42: worktree=acmeapp-platform, env=acmeapp-infra).
   *
   * Callers that know the per-issue repo (HeadlessOrchestrator via
   * `repoOverride`, SkillRunner via `params.repo` from the Go scheduler) MUST
   * pass it here. When unset (single-repo / manual single-stage invocation),
   * we fall back to `getCurrentRepository()`.
   */
  targetRepoOverride?: string,
  /**
   * The run's UUID, threaded from the Go scheduler (SkillRunner) or the
   * interactive orchestrator (#228). When present it is handed to the SDK
   * TraceRecorder so phase-transition events land in the run's
   * `<run_id>.jsonl`; when absent the recorder falls back to reading `run_id`
   * from `run-state.json`, and only disables itself (silent no-op) when
   * neither resolves — a per-stage manual invocation must never invent one.
   */
  runId?: string
): SkillProcessHandle {
  // When a pinned workspace root is provided (from HeadlessOrchestrator),
  // use it directly to prevent repo-switch mid-pipeline from changing CWD.
  // @see Issue #1592 - Pin workspace root for duration of pipeline run
  let workspaceRoot: string | undefined = pinnedWorkspaceRoot;

  // Fall back to dynamic resolution only when no pinned root is provided
  // (e.g., manual single-stage invocations outside the pipeline).
  if (!workspaceRoot) {
    try {
      const contextLoader = RepositoryContextLoader.getInstance();
      if (contextLoader.getCurrentRepository()) {
        workspaceRoot = contextLoader.getWorkingDirectory();
      }
    } catch {
      // Context loader unavailable (e.g., test environment without full VSCode API)
    }
    workspaceRoot ??= vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  }

  // Resolve target repo identity for skill-side verification (Issue #1306).
  //
  // The orchestrator's per-issue repo (targetRepoOverride) is the authoritative
  // INDEPENDENT source of truth — it must NOT be derived from the worktree CWD,
  // or the repo-mismatch assertion (CWD git remote vs expected) becomes
  // tautological. In multi-repo (N:1) workspaces `getCurrentRepository()`
  // returns the workspace PRIMARY repo, not the issue's repo, so it leaks the
  // wrong identity (Issue #3867). Prefer the override; fall back to the current
  // repo only for single-repo / manual single-stage invocations.
  let targetRepo: string | undefined = targetRepoOverride?.trim() || undefined;
  if (!targetRepo) {
    try {
      const repoContextLoader = RepositoryContextLoader.getInstance();
      const currentRepo = repoContextLoader.getCurrentRepository();
      if (currentRepo?.github) {
        targetRepo = `${currentRepo.github.owner}/${currentRepo.github.repo}`;
      }
    } catch {
      // Context loader unavailable — targetRepo stays undefined
    }
  }

  if (!workspaceRoot) {
    const error = new Error("No workspace folder open");
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });

    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
    };
  }

  // Find and read the SKILL.md file — use injected content if provided (Issue #1473)
  // Resolve the absolute skill directory regardless of content source: the
  // spawn env exports it (NIGHTGAUGE_SKILL_DIR) and buildSkillPrompt rewrites
  // skill-relative read directives against it (#196).
  let skillDir: string | null;
  let skillData: ReturnType<typeof readSkillFile>;
  if (injectedSkillContent) {
    // Parse frontmatter from platform-resolved content (in-memory, no disk I/O)
    skillData = parseSkillContent(injectedSkillContent);
    const diskPath = findSkillFile(stage);
    skillDir = diskPath ? path.dirname(diskPath) : null;
    if (!skillData) {
      // Fallback to disk on parse failure
      skillData = diskPath ? readSkillFile(diskPath) : null;
    }
  } else {
    const skillPath = findSkillFile(stage);
    if (!skillPath) {
      const error = new Error(`SKILL.md not found for stage: ${stage}`);
      callbacks?.onError?.(error);
      callbacks?.onComplete?.({ success: false, exitCode: null, error });

      return {
        process: null as unknown as ChildProcess,
        stage,
        issueNumber,
        kill: () => {},
      };
    }
    skillDir = path.dirname(skillPath);
    skillData = readSkillFile(skillPath);
  }
  if (!skillData) {
    const error = new Error(`Failed to load skill for stage: ${stage}`);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });

    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
    };
  }

  // Build the prompt. Mutable: the Haiku behavioral preamble (#77 → #106)
  // is prepended below once the model decision is final.
  let prompt = buildSkillPrompt(stage, skillData.content, issueNumber, skillDir ?? undefined);

  // Per-stage adapter resolution (Issue #3223 — Wave 2 of Epic #3212; Issue
  // #3230 added Step 2.5 — AutoProviderRouter).
  //
  // Replaces the previous `getExecutionAdapter(workspaceRoot)` global lookup
  // with `resolveStageAdapter(stage, workspaceRoot, env, autoRouterOptions)`,
  // which honors the precedence chain
  //   env → pipeline.stage_adapters.<stage> → auto-router → ui.core.adapter
  //   → "claude"
  // and returns an `{adapter, source, rationale?, routerModel?}` decision.
  // This makes mixed-adapter pipelines (e.g. Claude planning → Gemini dev)
  // work and lets the SDK router pick adapters when no explicit override is
  // configured. The interactive dispatcher (`runStageSkillInteractive`,
  // ~line 3609) still uses the global lookup intentionally — see comment.
  const autoRouterOptions = buildAutoRouterOptions(stage, workspaceRoot, issueMetadata);
  const initialDecision = resolveStageAdapter(stage, workspaceRoot, process.env, autoRouterOptions);
  let adapter: ExecutionAdapter = initialDecision.adapter;
  let adapterSource: AdapterSource = initialDecision.source;
  let routerRationale: string | undefined = initialDecision.rationale;
  let prereqError = validateAdapterPrerequisites(adapter, workspaceRoot, "headless");

  // Issue #3231 — track every adapter the dispatcher considers at stage start,
  // in order. Element 0 is always the primary; subsequent elements are
  // fallback candidates from `walkAdapterFallback`. Persisted on
  // `AdapterDecision.adapterFallbackChainUsed` so per-stage execution-history
  // records can attribute the routing audit trail (AC #5). The walker is
  // strictly stage-start — no mid-stream fallback after token spend.
  const chainUsed: ExecutionAdapter[] = [adapter];
  // Snapshot the primary's prereq error so the [stage:adapter-unavailable]
  // envelope (when emitted with no fallback walked) carries the original
  // reason, not a stale value from the walker's last hop.
  const primaryPrereqError = prereqError;
  let chainExhausted = false;

  if (prereqError) {
    // AC #3 — when the resolved adapter's prereq validation fails, walk the
    // effective fallback chain (per-stage override → global chain → built-in
    // default — AC #1, #2). Strict mode (`pipeline.disable_fallback: true` —
    // AC #7) returns an empty effective chain so `walkAdapterFallback` is
    // a no-op and the dispatcher falls through to the
    // [stage:adapter-unavailable] envelope below.
    const walk = walkAdapterFallback(
      adapter,
      prereqError,
      (candidate) => validateAdapterPrerequisites(candidate, workspaceRoot, "headless"),
      workspaceRoot,
      stage
    );
    // Append every fallback candidate the walker tried (skip element 0 — it
    // is the failed primary, already in chainUsed).
    for (let i = 1; i < walk.hopsAttempted.length; i++) {
      chainUsed.push(walk.hopsAttempted[i]);
      // AC #4 — info-level log per hop using the AC-specified format.
      const hop = walk.hopsAttempted[i];
      callbacks?.onStderr?.(
        `[skillRunner] primary=${adapter} unavailable: ${prereqError}; ` +
          `falling back to ${hop} per pipeline.adapter_fallback_chain\n`
      );
    }
    if (walk.winner) {
      adapter = walk.winner.adapter;
      adapterSource = walk.winner.source;
      routerRationale = undefined;
      prereqError = null;
    } else if (walk.hopsAttempted.length > 1) {
      // The walker tried at least one fallback candidate but every one
      // failed. Distinguish this from "primary-only failed" by marking the
      // chain as exhausted — the dispatcher emits [stage:no-adapter-available].
      chainExhausted = true;
      prereqError = walk.lastError || prereqError;
    }
  }

  if (prereqError) {
    // AC #3 / AC #6 — emit a structured envelope that AutoRetroService
    // recognizes via its `[stage:` prefix routing. Two distinct shapes:
    //
    // - `[stage:no-adapter-available]` — the walker tried the full effective
    //   chain and every candidate failed. `adapters_tried` lists every hop.
    // - `[stage:adapter-unavailable]`   — primary failed and no fallback was
    //   walked (chain disabled by `disable_fallback`, empty effective chain,
    //   or the primary's failure already exhausted the only candidate).
    const error = chainExhausted
      ? new Error(
          `[stage:no-adapter-available] adapters_tried=[${chainUsed.join(",")}] ` +
            `reason=${prereqError}`
        )
      : new Error(
          `[stage:adapter-unavailable] adapter=${adapter} source=${adapterSource} ` +
            `reason=${primaryPrereqError ?? prereqError}`
        );
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({
      success: false,
      exitCode: null,
      error,
      adapterDecision: {
        adapter,
        source: adapterSource,
        // Only emit the audit trail when fallback was actually walked
        // (length ≥ 2). Length-1 keeps records terse for the common
        // primary-only-failed-with-disable_fallback path.
        ...(chainUsed.length >= 2 ? { adapterFallbackChainUsed: chainUsed } : {}),
      },
    });
    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
    };
  }

  // Surface the resolved adapter when it differs from the hardcoded default
  // OR was selected via a non-default precedence step (env / stage-config /
  // auto-router / fallback). Keeps log noise minimal for default-config users
  // (AC #6). Issue #3230 — when the auto-router picked, include the rationale
  // string so operators can audit cost/capability/context trade-offs.
  if (adapterSource !== "default") {
    if (adapterSource === "auto-router" && routerRationale) {
      callbacks?.onStderr?.(
        `[skillRunner] Auto-router: adapter=${adapter} rationale="${routerRationale}"\n`
      );
    } else {
      callbacks?.onStderr?.(`[skillRunner] Stage adapter: ${adapter} (source=${adapterSource})\n`);
    }
  }

  // Estimate token count (~4 chars per token for English text)
  const estimatedTokens = Math.ceil(prompt.length / 4);
  const tokenLabel =
    estimatedTokens > 1000 ? `${(estimatedTokens / 1000).toFixed(1)}K` : String(estimatedTokens);

  // Emit headless mode (Issue #496)
  callbacks?.onMode?.("headless");

  // Merge MCP tools: config.yaml stage overrides frontmatter when present.
  // Global config tools are always merged in. (Issue #1725, #1726)
  const mcpToolsFromFrontmatter = skillData.mcpTools;
  const mcpToolsFromConfig = getMcpToolsConfig(workspaceRoot, stage);
  const configStageMcpTools = getStageMcpTools(workspaceRoot, stage);
  // Stage-level config overrides frontmatter; global config always merges in
  const baseMcpTools =
    configStageMcpTools.length > 0 ? configStageMcpTools : mcpToolsFromFrontmatter;
  const mergedMcpTools = Array.from(new Set([...baseMcpTools, ...mcpToolsFromConfig]));
  const resolvedMcpTools = resolveMcpTools(mergedMcpTools, workspaceRoot);

  if (resolvedMcpTools.length > 0) {
    callbacks?.onStderr?.(`[skillRunner] MCP tools: ${resolvedMcpTools.join(", ")}\n`);
  }

  // Filter out AskUserQuestion - it doesn't work in headless mode (-p)
  // Claude CLI treats it as a permission denial, causing the agent to retry
  // repeatedly and flood the output. See Issue #118, #171, #205.
  const filteredTools = skillData.allowedTools.filter((tool) => tool !== "AskUserQuestion");

  const allAllowedTools = [...filteredTools, ...resolvedMcpTools];

  let cmd = "claude";
  let args: string[];

  // Resolve model using full resolution chain (Issue #732 - AutoModelSelector)
  // Hoisted so the decision is available in the onComplete callback.
  // When modelOverride is provided (Issue #1343 - escalation engine), bypass
  // resolveModel() and use the escalated model directly.
  // When pauseAutoRouting is true (Issue #1395 - health emergency policy), skip
  // issueMetadata so AutoModelSelector is not consulted — resolution falls through
  // to config/default model while still respecting env var and stage-default overrides.
  const effectiveMetadata = pauseAutoRouting ? undefined : issueMetadata;
  const baselineModelDecision = resolveModel(stage, workspaceRoot, effectiveMetadata, issueNumber);
  const priorModel = modelOverride
    ? adapter === "claude"
      ? baselineModelDecision.model
      : adapter === "codex"
        ? resolveCodexPipelineModel(baselineModelDecision.model, workspaceRoot)
        : undefined
    : undefined;
  const baseDecision: ModelDecision = modelOverride
    ? {
        model:
          adapter === "codex"
            ? resolveCodexPipelineModel(modelOverride, workspaceRoot)
            : modelOverride,
        source: modelOverrideSource ?? "user-override",
        escalatedFrom: priorModel,
      }
    : adapter === "codex"
      ? {
          ...baselineModelDecision,
          model: resolveCodexPipelineModel(baselineModelDecision.model, workspaceRoot),
        }
      : baselineModelDecision;
  // Issue #3230: when the AutoProviderRouter picked the adapter, attribute the
  // model selection to "auto-router" so per-stage history records the routing
  // step accurately. Skip when the user explicitly overrode the model
  // (override beats router for source attribution).
  const modelDecision: ModelDecision =
    adapterSource === "auto-router" && !modelOverride
      ? { ...baseDecision, source: "auto-router" }
      : baseDecision;

  // Record the resolved model up-front, before the CLI spawns (#367), so a
  // stage killed before completion still attributes its true model rather than
  // 'unknown'. Fires once here for every adapter; for codex/copilot the model
  // may be perf-mode-remapped just before spawn, but that later value is
  // reconciled by the Go handler's latest-wins recording at completion.
  callbacks?.onModelResolved?.(stage, modelDecision.model, adapter);

  // Behavioral preamble for the Haiku tier (#77 → #106): prepended
  // prompt-proximally now that the model decision is final; measured skip
  // on Sonnet/Opus. Mirrors the Go scheduler and SDK StageExecutor
  // injections.
  prompt = withBehavioralPreamble(prompt, modelDecision.model);

  if (adapter === "claude") {
    const authProvider = getAuthProvider(workspaceRoot);
    args = [
      "-p",
      "--no-session-persistence",
      "--output-format",
      "stream-json",
      "--verbose",
      "--allowedTools",
      allAllowedTools.join(","),
    ];

    if (authProvider === "bedrock") {
      args.push("--bedrock");
      callbacks?.onStderr?.("[skillRunner] Using AWS Bedrock backend\n");
    } else if (authProvider === "vertex") {
      args.push("--vertex");
      callbacks?.onStderr?.("[skillRunner] Using Google Vertex AI backend\n");
    }

    // Pass --model from resolution chain (Issue #732)
    // Priority: env var > config > AutoModelSelector > default > 'sonnet'
    // Always pass --model explicitly — CLI default may differ from expected 'sonnet'
    args.push("--model", modelDecision.model);
    // Silently skip --effort for models that do not support extended thinking (e.g. Haiku).
    // Issue #1235 - Per-model effort level configuration
    // Fable (#73): the resolved effort is conformed to Fable's published
    // guidance before it reaches the CLI — explicit config floors at `high`
    // (Fable's own default), router-selected fable (L/XL only) gets `xhigh`,
    // and a deliberate pin with no explicit effort omits the flag so the
    // server default applies. Sonnet/Opus values pass through untouched.
    let effort = modelDecision.effort;
    if (modelDecision.model.toLowerCase().includes("fable")) {
      const conformed = conformEffortForFable(
        effort,
        getExplicitStageEffort(stage, workspaceRoot),
        modelDecision.source
      );
      if (conformed.coerced) {
        callbacks?.onStderr?.(
          `[skillRunner] Effort ${effort} conformed to ${conformed.effort} for Fable (high is Fable's documented default; xhigh on router escalation)\n`
        );
      }
      effort = conformed.effort;
    }
    const finalEffort = effort;
    const supportsEffort =
      adapter === "claude" &&
      !!finalEffort &&
      modelSupportsEffort(modelDecision.model as DefaultModel);
    if (supportsEffort && finalEffort) {
      args.push("--effort", finalEffort);
    }

    const effortLabel = supportsEffort ? finalEffort : "default";
    // Consolidated metadata line (Issue #795): single compact INFO line per stage
    callbacks?.onStderr?.(
      `[skillRunner] Stage: ${stage} | Model: ${modelDecision.model} (${modelDecision.source}) | Effort: ${effortLabel} | Prompt: ~${tokenLabel} tokens\n`
    );
    // Repo identity observability (Issue #1306): log CWD and target repo for debugging
    callbacks?.onStderr?.(
      `[skillRunner] CWD: ${workspaceRoot}${targetRepo ? ` | Repo: ${targetRepo}` : ""}\n`
    );

    // AutoModelSelector reasoning (Issue #946): surface selection logic for observability
    if (modelDecision.source === "auto" && modelDecision.selectionResult?.reasoning) {
      callbacks?.onStderr?.(
        `[skillRunner]   → Reasoning: ${modelDecision.selectionResult.reasoning}\n`
      );
    }

    // Pass --fallback-model from config (Issue #626). A Fable run defaults its
    // fallback to Opus (separate Max-plan bucket) so a Fable usage-limit/overload
    // degrades gracefully mid-session instead of hard-failing.
    const fallbackModel = resolveFallbackModel(
      modelDecision.model,
      getFallbackModel(workspaceRoot)
    );
    if (fallbackModel) {
      args.push("--fallback-model", fallbackModel);
      const fableNote = modelDecision.model === "fable" ? " (Fable → Opus)" : "";
      callbacks?.onStderr?.(`[skillRunner] Fallback model: ${fallbackModel}${fableNote}\n`);
    }

    // Pass --max-turns from config (Issue #626)
    const maxTurns = getMaxTurns(workspaceRoot);
    if (maxTurns) {
      args.push("--max-turns", String(maxTurns));
      callbacks?.onStderr?.(`[skillRunner] Max turns: ${maxTurns}\n`);
    }

    // Pass --max-budget-usd from batch resource limits (Issue #626)
    const costBudget = getCostBudget(workspaceRoot);
    if (costBudget && costBudget > 0) {
      args.push("--max-budget-usd", String(costBudget));
      callbacks?.onStderr?.(`[skillRunner] Budget cap: $${costBudget}\n`);
    }
  } else {
    if (!issueNumber) {
      const error = new Error(`${adapter} adapter requires an issue number to execute a stage.`);
      callbacks?.onError?.(error);
      callbacks?.onComplete?.({ success: false, exitCode: null, error });
      return {
        process: null as unknown as ChildProcess,
        stage,
        issueNumber,
        kill: () => {},
      };
    }
    cmd = path.join(workspaceRoot, "scripts", "run-stage.sh");
    args = [adapter, stage, String(issueNumber)];
  }
  if (adapter === "codex" && modelOverride) {
    callbacks?.onStderr?.(
      `[skillRunner] Codex run-level override: ${modelDecision.model} (${modelOverrideSource ?? "user-override"})\n`
    );
  }

  // Load auto-accept configuration from .nightgauge/config.yaml
  const autoAcceptEnv = loadAutoAcceptConfigSync(workspaceRoot, stage);

  // Log auto-accept mode if enabled
  if (autoAcceptEnv.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS === "true") {
    callbacks?.onStderr?.("[skillRunner] Auto-accept permissions: ENABLED\n");
    // Pass --permission-mode for explicit CLI-level permission bypass (Issue #626)
    if (adapter === "claude") {
      args.push("--permission-mode", "bypassPermissions");
      callbacks?.onStderr?.("[skillRunner] Permission mode: bypassPermissions\n");
    }
  }
  if (autoAcceptEnv.NIGHTGAUGE_AUTO_ACCEPT_STAGES === "true") {
    callbacks?.onStderr?.("[skillRunner] Auto-accept stages: ENABLED\n");
  }

  // Pass programmatic tool definitions via env var for PTC executor (Issue #1066, #1069)
  const ptcEnv: Record<string, string> = {};
  if (skillData.programmaticTools && skillData.programmaticTools.length > 0) {
    ptcEnv.NIGHTGAUGE_TOOL_DEFINITIONS = JSON.stringify(skillData.programmaticTools);
    callbacks?.onStderr?.(
      `[skillRunner] Programmatic tools: ${skillData.programmaticTools.join(", ")}\n`
    );

    // Pass ANTHROPIC_API_KEY to skill environment for PTC execution (Issue #1069)
    const apiKey = process.env.ANTHROPIC_API_KEY;
    if (apiKey) {
      ptcEnv.ANTHROPIC_API_KEY = apiKey;
      callbacks?.onStderr?.("[skillRunner] PTC API key: available\n");
    }
  }

  // Gemini configuration env vars (Issue #1056)
  // API key flows via process.env.GEMINI_API_KEY (set at extension activation from SecretStorage).
  // Model is resolved from config and passed explicitly.
  // Performance-mode wiring (Issue #3214): when the active mode supplies a
  // tier override for this stage, translate haiku|sonnet|opus to the
  // adapter-specific id and stamp modelDecision.model so run history reports
  // the actual model that ran (mirrors the Codex precedent at line 1755).
  const geminiEnv: Record<string, string> = {};
  if (adapter === "gemini" || adapter === "gemini-sdk") {
    const perfMapping =
      modelDecision.source === "performance-mode"
        ? getModeStageAdapterModel(getPerformanceMode(workspaceRoot), stage, adapter)
        : undefined;
    let geminiModel: string;
    let modelSourceLabel = "";
    if (perfMapping && !perfMapping.mismatch) {
      geminiModel = perfMapping.model;
      modelDecision.model = perfMapping.model;
      modelSourceLabel = " (performance-mode)";
    } else {
      geminiModel = getGeminiModel(workspaceRoot);
    }
    geminiEnv.NIGHTGAUGE_GEMINI_MODEL = geminiModel;
    callbacks?.onStderr?.(`[skillRunner] Gemini model: ${geminiModel}${modelSourceLabel}\n`);

    const geminiAuthMethod = getGeminiAuthMethod(workspaceRoot);
    geminiEnv.NIGHTGAUGE_GEMINI_AUTH_METHOD = geminiAuthMethod;
    callbacks?.onStderr?.(`[skillRunner] Gemini auth method: ${geminiAuthMethod}\n`);
  }

  // Codex model configuration (Issue #1656)
  const codexEnv: Record<string, string> = {};
  if (adapter === "codex") {
    // When the active performance mode is `maximum` (or legacy supercharge),
    // prefer the user-configurable Codex override
    // (`pipeline.performance_mode.maximum.codex_model` or env var) so users
    // can point the heavy tier at a new model without a code change. Falls
    // through to modelDecision.model, which for `maximum` already resolves
    // to the registry's opus tier (CODEX_TIER_MODEL_MAP.opus) via
    // resolveCodexPipelineModel("opus").
    const heavyCodexOverride =
      modelDecision.source === "performance-mode" || modelDecision.source === "supercharge"
        ? getSuperchargeCodexModel(workspaceRoot)
        : undefined;
    const codexModel = heavyCodexOverride ?? modelDecision.model;
    codexEnv.NIGHTGAUGE_CODEX_MODEL = codexModel;
    const sourceSuffix = modelOverride
      ? " (run override)"
      : heavyCodexOverride
        ? " (performance-mode override)"
        : "";
    callbacks?.onStderr?.(`[skillRunner] Codex model: ${codexModel}${sourceSuffix}\n`);

    const codexCliCommand = getCodexCliCommand(workspaceRoot);
    codexEnv.NIGHTGAUGE_CODEX_CLI_COMMAND = codexCliCommand;
    callbacks?.onStderr?.(`[skillRunner] Codex CLI command: ${codexCliCommand}\n`);

    const codexCliArgs = getCodexCliArgs(workspaceRoot);
    if (codexCliArgs) {
      codexEnv.NIGHTGAUGE_CODEX_CLI_ARGS = codexCliArgs;
      callbacks?.onStderr?.(`[skillRunner] Codex CLI args: ${codexCliArgs}\n`);
    } else {
      codexEnv.NIGHTGAUGE_CODEX_CLI_ARGS = "";
      callbacks?.onStderr?.("[skillRunner] Codex CLI args: (adapter defaults)\n");
    }

    const codexResumeEnabled = getCodexResumeEnabled(workspaceRoot);
    codexEnv.NIGHTGAUGE_CODEX_RESUME_ENABLED = codexResumeEnabled ? "true" : "";
    callbacks?.onStderr?.(
      `[skillRunner] Codex session resume: ${codexResumeEnabled ? "enabled" : "disabled"}\n`
    );
  }

  // Copilot model and GitHub auth configuration (Issue #1946)
  // Performance-mode wiring (Issue #3214): mode profile takes precedence over
  // the configured copilot model when active; falls back to getCopilotModel
  // otherwise. Mismatch is impossible for copilot — every tier maps.
  const copilotEnv: Record<string, string> = {};
  if (adapter === "copilot") {
    const perfMapping =
      modelDecision.source === "performance-mode"
        ? getModeStageAdapterModel(getPerformanceMode(workspaceRoot), stage, adapter)
        : undefined;
    let copilotModel: string | undefined;
    let modelSourceLabel = "";
    if (perfMapping && !perfMapping.mismatch) {
      copilotModel = perfMapping.model;
      modelDecision.model = perfMapping.model;
      modelSourceLabel = " (performance-mode)";
    } else {
      copilotModel = getCopilotModel(workspaceRoot);
    }
    if (copilotModel) {
      copilotEnv.NIGHTGAUGE_COPILOT_MODEL = copilotModel;
      callbacks?.onStderr?.(`[skillRunner] Copilot model: ${copilotModel}${modelSourceLabel}\n`);
    } else {
      callbacks?.onStderr?.("[skillRunner] Copilot model: (CLI default)\n");
    }

    // Pass GitHub auth tokens for copilot binary authentication
    // Cascade: GH_TOKEN → GITHUB_TOKEN → COPILOT_GITHUB_TOKEN
    // CopilotCliAdapter.validateAuth() will fall back to `copilot auth status`
    // if none are present.
    if (process.env.GH_TOKEN) copilotEnv.GH_TOKEN = process.env.GH_TOKEN;
    if (process.env.GITHUB_TOKEN) copilotEnv.GITHUB_TOKEN = process.env.GITHUB_TOKEN;
    if (process.env.COPILOT_GITHUB_TOKEN)
      copilotEnv.COPILOT_GITHUB_TOKEN = process.env.COPILOT_GITHUB_TOKEN;

    const hasToken =
      process.env.GH_TOKEN || process.env.GITHUB_TOKEN || process.env.COPILOT_GITHUB_TOKEN;
    callbacks?.onStderr?.(
      hasToken
        ? "[skillRunner] Copilot auth: token available\n"
        : "[skillRunner] Copilot auth: no token in env — will attempt `copilot auth status`\n"
    );
  }

  // LM Studio model and server configuration (Issue #2057)
  // Performance-mode wiring (Issue #3214): LM Studio cannot honor canonical
  // tier aliases — the served model is whatever is loaded locally. When the
  // mode profile names a tier, log an info-level mismatch and demote
  // modelDecision.source from "performance-mode" to "config" so the run
  // history (executionHistoryWriter.ts:482) stays honest. AC #3.
  const lmStudioEnv: Record<string, string> = {};
  if (adapter === "lm-studio") {
    if (modelDecision.source === "performance-mode") {
      const perfMapping = getModeStageAdapterModel(
        getPerformanceMode(workspaceRoot),
        stage,
        adapter
      );
      if (perfMapping?.mismatch) {
        callbacks?.onStderr?.(
          `[skillRunner] LM Studio cannot honor performance-mode tier "${perfMapping.model}" — using configured local model.\n`
        );
        modelDecision.source = "config";
      }
    }
    const lmStudioModel = getLmStudioModel(workspaceRoot);
    lmStudioEnv.NIGHTGAUGE_LM_STUDIO_MODEL = lmStudioModel;
    callbacks?.onStderr?.(`[skillRunner] LM Studio model: ${lmStudioModel || "(unconfigured)"}\n`);

    const lmStudioBaseUrl = getLmStudioBaseUrl(workspaceRoot);
    lmStudioEnv.NIGHTGAUGE_LM_STUDIO_BASE_URL = lmStudioBaseUrl;
    callbacks?.onStderr?.(`[skillRunner] LM Studio base URL: ${lmStudioBaseUrl}\n`);

    const lmStudioApiKey = getLmStudioApiKey(workspaceRoot);
    lmStudioEnv.NIGHTGAUGE_LM_STUDIO_API_KEY = lmStudioApiKey;

    const lmStudioTimeoutMs = getLmStudioTimeoutMs(workspaceRoot);
    lmStudioEnv.NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS = String(lmStudioTimeoutMs);
    callbacks?.onStderr?.(`[skillRunner] LM Studio timeout: ${lmStudioTimeoutMs}ms\n`);
  }

  // Generate GEMINI.md for Gemini-based adapters before spawn (Issue #1055)
  if (adapter === "gemini" || adapter === "gemini-sdk") {
    try {
      const generator = new GeminiContextGenerator();
      const geminiPath = generator.generateSync({
        projectRoot: workspaceRoot,
        stage,
        issueNumber: issueNumber ?? 0,
        adapter,
      });
      if (geminiPath) {
        callbacks?.onStderr?.(`[skillRunner] Generated GEMINI.md for ${adapter} adapter\n`);
      }
    } catch (error) {
      callbacks?.onStderr?.(
        `[skillRunner] Warning: Failed to generate GEMINI.md: ${error instanceof Error ? error.message : String(error)}\n`
      );
    }
  }

  // Provision AGENTS.md steering for the Codex adapter before spawn (Issue #4028).
  // Non-destructive: writes a managed block, preserving any user-authored AGENTS.md.
  if (adapter === "codex") {
    try {
      const codexPath = new CodexContextGenerator().generateSync({
        projectRoot: workspaceRoot,
        stage,
        issueNumber: issueNumber ?? 0,
        adapter,
      });
      if (codexPath) {
        callbacks?.onStderr?.(`[skillRunner] Provisioned AGENTS.md steering for ${adapter}\n`);
      }
    } catch (error) {
      callbacks?.onStderr?.(
        `[skillRunner] Warning: Failed to provision AGENTS.md: ${error instanceof Error ? error.message : String(error)}\n`
      );
    }

    // Make the pipeline's MCP servers reachable from Codex stages by translating
    // .mcp.json → ~/.codex/config.toml [mcp_servers.*] (Issue #4025). Idempotent
    // and intentionally PERSISTED (no cleanup), preserving any user-defined
    // [mcp_servers.*] tables outside the managed block.
    try {
      const mcpResult = new CodexMcpProvisioner().provisionSync({
        workspaceRoot,
        adapter,
      });
      if (mcpResult && mcpResult.provisioned.length > 0) {
        callbacks?.onStderr?.(
          `[skillRunner] Provisioned Codex MCP servers [${mcpResult.provisioned.join(", ")}] → ${mcpResult.configPath}\n`
        );
      }
      if (mcpResult && mcpResult.skippedCollisions.length > 0) {
        callbacks?.onStderr?.(
          `[skillRunner] Skipped Codex MCP servers (user-defined) [${mcpResult.skippedCollisions.join(", ")}]\n`
        );
      }
    } catch (error) {
      callbacks?.onStderr?.(
        `[skillRunner] Warning: Failed to provision Codex MCP config: ${error instanceof Error ? error.message : String(error)}\n`
      );
    }
  }

  // Compute absolute CLAUDE_PLUGIN_ROOT so hook scripts (wait-for-ci-checks.sh,
  // fetch-ci-failure-logs.sh, the push-to-main guard, etc.) are always found
  // regardless of which repo is the active working directory. Resolves to a
  // live workspace copy when present, else the extension's bundled hooks — so
  // non-nightgauge workspaces (platform, dashboard, customer repos, the
  // acmeapp incident) still load the deterministic safety guards instead of
  // pointing CLAUDE_PLUGIN_ROOT at a non-existent path. @see resolvePluginRoot
  const computedPluginRoot = resolvePluginRoot();
  if (!computedPluginRoot) {
    callbacks?.onStderr?.(
      `[skillRunner] WARNING: no claude-plugins/nightgauge hooks found (live or bundled) — ` +
        `pipeline safety guards (push-to-main block, workflow gate) will NOT run for this stage\n`
    );
  }

  // GitHub token injection (Issues #2487, #2670)
  // Resolve the best available GitHub token using priority chain:
  // config.yaml (github_auth.token/tokens) → GITHUB_TOKEN env → github_user → gh auth token
  // Both GH_TOKEN and GITHUB_TOKEN are set so `gh` CLI and direct API consumers work.
  const perRepoTokenEnv: Record<string, string> = {};
  const tokenResult = resolveTokenForSubprocess(workspaceRoot);
  if (tokenResult) {
    perRepoTokenEnv.GH_TOKEN = tokenResult.token;
    perRepoTokenEnv.GITHUB_TOKEN = tokenResult.token;
    callbacks?.onStderr?.(`[skillRunner] GitHub token source: ${tokenResult.source}\n`);
  } else {
    callbacks?.onStderr?.(
      `[skillRunner] WARNING: No GitHub token resolved — subprocess may fail API calls\n`
    );
  }

  // Provider-aware model preflight (#4021): the LAST gate before spawn. All
  // adapter-specific model env vars are populated above (including performance-
  // mode / supercharge overrides), so this is the single choke point. For a
  // CLOSED adapter (codex/gemini/gemini-sdk) an invalid model is rejected here
  // with an actionable message — never at CLI spawn time. The resolved concrete
  // model is written back into the env so a routing tier (haiku|sonnet|opus|
  // fable) can never leak to the CLI as --model.
  //
  // NOT validated here: `claude` (passes --model as a tier arg natively, sets no
  // model env), and `ollama` (an OPEN adapter not wired into skillRunner's
  // spawn branches — no ollamaEnv exists; codexPreflight covers it generically).
  //
  // On failure we DO NOT throw: runStageSkillHeadless returns a handle and
  // reports failures via callbacks (mirroring the prereq / skill-not-found paths
  // above). A synchronous throw here would escape past the caller's handle-based
  // error handling. Emit a `[stage:model-invalid]` envelope and return.
  try {
    if (adapter === "codex" && codexEnv.NIGHTGAUGE_CODEX_MODEL) {
      codexEnv.NIGHTGAUGE_CODEX_MODEL = validateModelForAdapter(
        "codex",
        codexEnv.NIGHTGAUGE_CODEX_MODEL
      ).model;
    } else if (
      (adapter === "gemini" || adapter === "gemini-sdk") &&
      geminiEnv.NIGHTGAUGE_GEMINI_MODEL
    ) {
      geminiEnv.NIGHTGAUGE_GEMINI_MODEL = validateModelForAdapter(
        adapter,
        geminiEnv.NIGHTGAUGE_GEMINI_MODEL
      ).model;
    } else if (adapter === "copilot" && copilotEnv.NIGHTGAUGE_COPILOT_MODEL) {
      copilotEnv.NIGHTGAUGE_COPILOT_MODEL = validateModelForAdapter(
        "copilot",
        copilotEnv.NIGHTGAUGE_COPILOT_MODEL
      ).model;
    } else if (adapter === "lm-studio" && lmStudioEnv.NIGHTGAUGE_LM_STUDIO_MODEL) {
      lmStudioEnv.NIGHTGAUGE_LM_STUDIO_MODEL = validateModelForAdapter(
        "lm-studio",
        lmStudioEnv.NIGHTGAUGE_LM_STUDIO_MODEL
      ).model;
    }
  } catch (validationError) {
    const detail =
      validationError instanceof AdapterError
        ? validationError.format()
        : validationError instanceof Error
          ? validationError.message
          : String(validationError);
    const error = new Error(`[stage:model-invalid] adapter=${adapter} reason=${detail}`);
    callbacks?.onStderr?.(`[skillRunner] Model preflight failed: ${detail}\n`);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });
    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
    };
  }

  const proc = spawn(cmd, args, {
    cwd: workspaceRoot,
    shell: false,
    stdio: ["pipe", "pipe", "pipe"], // Enable stdin pipe
    env: {
      ...process.env,
      // Export the resolved Go binary (NIGHTGAUGE_BIN + PATH prepend) so
      // skill subprocesses discover it under any adapter without a
      // VSCode-extension-specific glob (Issue #4029). Spread before the other
      // env maps; none of them set PATH/NIGHTGAUGE_BIN, so this wins over
      // the inherited process.env PATH.
      ...resolveBinaryDiscoveryEnv(),
      ...autoAcceptEnv, // Merge auto-accept config
      ...ptcEnv, // Merge PTC tool definitions (Issue #1066)
      ...geminiEnv, // Merge Gemini config env vars (Issue #1056)
      ...codexEnv, // Merge Codex model config env vars (Issue #1656)
      ...copilotEnv, // Merge Copilot model + auth env vars (Issue #1946)
      ...lmStudioEnv, // Merge LM Studio config env vars (Issue #2057)
      ...perRepoTokenEnv, // Merge per-repo GitHub token (Issue #2487)
      // Always inject absolute CLAUDE_PLUGIN_ROOT so hook scripts resolve correctly
      // when the active repo (e.g. acme-platform) has no claude-plugins/.
      ...(computedPluginRoot ? { CLAUDE_PLUGIN_ROOT: computedPluginRoot } : {}),
      // Non-Claude adapters: run-stage.sh sets NIGHTGAUGE_ADAPTER and
      // NIGHTGAUGE_OUTPUT_FORMAT internally. Pass them here too so the
      // spawn env is consistent before exec replaces the process.
      ...(adapter !== "claude"
        ? {
            NIGHTGAUGE_ADAPTER: adapter,
            NIGHTGAUGE_OUTPUT_FORMAT: "json",
          }
        : {}),
      // Phase-level retry: pass SKIP_TO_PHASE so the skill can skip
      // completed phases and resume from the failed one (Issue #1187)
      ...(skipToPhase ? { SKIP_TO_PHASE: skipToPhase } : {}),
      // Repo identity assertion: pass expected repo so skills can verify CWD (Issue #1306)
      ...(targetRepo ? { NIGHTGAUGE_TARGET_REPO: targetRepo } : {}),
      // Absolute skill directory so agents resolve _includes/_shared without
      // CWD assumptions or filesystem scans in cross-repo worktrees (#196)
      ...(skillDir ? { NIGHTGAUGE_SKILL_DIR: skillDir } : {}),
    },
  });

  if (adapter === "claude") {
    if (proc.stdin) {
      proc.stdin.write(prompt);
      proc.stdin.end(); // Signal EOF to start processing
    } else {
      const error = new Error("Failed to get stdin pipe for Claude CLI");
      callbacks?.onError?.(error);
    }
  }

  // Track descendant pids while the CLI is alive so that on exit (clean or
  // killed) we can reap orphaned test runners reparented to init. See #781:
  // a hung vitest survived its dead Claude parent for 53 minutes.
  const descendantTracker = new DescendantTracker();
  if (proc.pid) {
    descendantTracker.start(proc.pid);
  }

  const processKey = `${stage}-${issueNumber ?? "no-issue"}`;

  // Token accumulator for this skill run.
  // Issue #3228: pass (adapter, model) so getTotal() resolves cost via the
  // unified resolver chain (native -> table-computed -> unknown). Non-Claude
  // adapters never emit a native total_cost_usd, so without this they would
  // continue reporting $0 per stage.
  const tokenAccumulator = new TokenAccumulator(adapter, modelDecision.model);

  // Live in-stage token/cost estimator (#233), SEPARATE from tokenAccumulator.
  // Fed only by per-turn `assistant` message usage (incrementalUsage), which is
  // a growing-context snapshot — latest-wins for input/cache_read, summed for
  // output. NEVER fed into tokenAccumulator (that would over-count); the
  // terminal `result` envelope stays authoritative and reconciles at stage end.
  // See LiveStageEstimator's class doc for the full accumulation-semantics
  // rationale. onStageProgress is throttled to >=5s to bound IPC/platform load
  // (a single dogfood stage produced ~1,571 usage payloads).
  const liveEstimator = new LiveStageEstimator(adapter, modelDecision.model);
  const PROGRESS_EMIT_CADENCE_MS = 5000;
  // Seed one cadence in the past so the FIRST snapshot emits promptly rather
  // than waiting a full window (also makes fake-timer tests deterministic at t=0).
  let lastProgressEmitMs = -PROGRESS_EMIT_CADENCE_MS;
  // Live budget-enforcement cadence (#254), separate from the platform-telemetry
  // cadence above so enforcement responsiveness is tunable on its own. Feeds the
  // same live estimate into onCostSnapshot → BudgetEnforcer so wind-down → warn →
  // terminate fire mid-stage instead of all at once at stage end.
  const budgetEvalCadenceMs = getBudgetEvalCadenceMs(workspaceRoot);
  let lastBudgetEvalMs = -Math.max(budgetEvalCadenceMs, 1);

  // Served-model attribution (#91): the claude CLI can silently swap to a
  // fallback model on a safety refusal (model_refusal_fallback) and still
  // exit 0. Track the last model the stream reports so the result attributes
  // what actually served — never used to retry or re-route.
  // See docs/spikes/fable-5-behavior-porting.md §8.3.
  let servedModel: string | undefined;
  let modelRefusalFallback: SkillRunResult["modelRefusalFallback"];
  const observeServedModel = (parsed: ReturnType<typeof parseStreamJsonLine>): void => {
    if (!parsed) return;
    if (parsed.modelRefusalFallback && !modelRefusalFallback) {
      modelRefusalFallback = parsed.modelRefusalFallback;
      callbacks?.onStderr?.(
        `[skillRunner] claude CLI model_refusal_fallback: ${parsed.modelRefusalFallback.originalModel} → ` +
          `${parsed.modelRefusalFallback.fallbackModel} (category ${parsed.modelRefusalFallback.category ?? "unknown"}); ` +
          `attributing the served model (#91)\n`
      );
    }
    if (parsed.model && parsed.model !== servedModel) {
      servedModel = parsed.model;
      // Point the computed-cost fallback at the serving model's pricing;
      // the native total_cost_usd path stays authoritative when present.
      tokenAccumulator.setModel(parsed.model);
      // Keep the live estimator's computed cost aligned to the served model too.
      liveEstimator.setModel(parsed.model);
    }
  };

  // Buffer for incomplete JSON lines
  let stdoutBuffer = "";
  let stderrBuffer = "";
  let stdoutRawTail = "";

  // Buffer for phase marker detection in streaming text content.
  // Phase markers are HTML comments emitted by skills in content_block_delta
  // text. They may span multiple deltas, so we buffer text and check on newlines.
  let phaseContentBuffer = "";

  // Deterministic phase inference (Issue #3760). Some stages — notably the
  // edit-heavy feature-dev — do not reliably emit `printf` phase markers, so the
  // tree shows no phase progress for them. This infers phases from the tool
  // calls the agent actually makes and feeds them through the SAME onPhaseStart
  // channel as real markers. It is a no-op for stages that self-report reliably.
  // Real markers always win via observeRealMarker(); inference only advances.
  const phaseInference = createPhaseInference(stage);
  let phaseStartEmitted = false;

  // Lifecycle trace (#180 / ADR 013): persist phase-marker transitions to the
  // per-run decision trace as the "sdk" producer. An explicit run_id threaded
  // from the orchestrator (#228) is authoritative; otherwise it resolves from
  // run-state.json. A manual single-stage invocation with neither gets a
  // silent (but now debug-logged) no-op recorder — a per-stage caller must
  // never invent a run id or one run's trace would split across files.
  // Fail-open by contract.
  const traceRecorder = TraceRecorder.open({
    pipelineDir: path.join(workspaceRoot, ".nightgauge", "pipeline"),
    ...(targetRepo ? { repo: targetRepo } : {}),
    ...(issueNumber && issueNumber > 0 ? { issue: issueNumber } : {}),
    ...(runId ? { runId } : {}),
  });

  // Session ID for conversation resumption (Issue #118)
  let capturedSessionId: string | undefined;
  // Track last rate limit event for error classification (Issue #2573)
  let lastRateLimitEvent: RateLimitEventData | undefined;
  // Wall-clock time the most recent rate_limit_event was observed (Issue #3425).
  // Used by the quota-exhausted fast-fail watchdog to bound idle-after-quota
  // independently of the full `stallKillMs` budget.
  let lastRateLimitEventAtMs: number | undefined;
  const startedAtMs = Date.now();
  let stallWarningShown = false;
  let stallPromptShown = false;
  let stallTicker: NodeJS.Timeout | null = null;
  let stageCompleted = false;
  // Stall event accumulator — flushed into SkillRunResult at stage completion (Issue #2652)
  const stallEvents: StallEvent[] = [];

  // Subprocess I/O capture for stall diagnostics (Issue #2871).
  // Accumulates stdout/stderr output capped at 50KB each to prevent memory bloat.
  // Written to the history directory when the process is killed for stall.
  const STALL_DIAG_BUFFER_MAX = 50 * 1024; // 50KB
  let diagStdoutBuffer = "";
  let diagStderrBuffer = "";

  // CI progress tracking for CI-aware stall detection (Issue #902)
  let lastCIProgressMs = 0;
  let ciProgressDetected = false;
  let lastCIStatus = "";

  const clearStallTicker = () => {
    if (stallTicker) {
      clearInterval(stallTicker);
      stallTicker = null;
    }
  };

  // Per-stage stall thresholds with escalating follow-up intervals (Issue #769)
  // Uses history-calibrated values when available (Issue #2654), falls back to
  // env/config/static otherwise.
  const stallThresholds = getStallThresholds(workspaceRoot);
  const staticStallThresholdSec = stallThresholds[stage] ?? 120; // fallback 2 min
  const stallKillMultiplier = getStallKillMultiplier(workspaceRoot, stage);

  // Check for pre-computed calibrated stall data (Issue #2654; Issue #3216
  // mode-aware bucketing). Thread the active performance mode so the
  // (stage, mode) bucket is consulted; size is reserved for future per-size
  // keying and is currently passed through but unused by the lookup.
  const calibratedData = workspaceRoot
    ? getCalibratedStallData(workspaceRoot, stage, getPerformanceMode(workspaceRoot))
    : undefined;

  let stallThresholdSec: number;
  let stallThresholdMs: number;
  let stallKillMs: number;
  let thresholdSource: string;

  if (calibratedData) {
    stallThresholdSec = calibratedData.warnSec;
    stallThresholdMs = stallThresholdSec * 1000;
    // #252: cold start (killSec 0) used to DISABLE the idle-kill entirely,
    // which left a wedged/silent session unkillable by any gate (every other
    // detector is cost/event-driven and never activates at $0). Floor it at a
    // conservative 30 minutes of TOTAL SILENCE instead — no healthy adapter
    // session goes 30 minutes without a single stdout/stderr byte (legitimate
    // quiet Bash spans are minutes, and calibrated thresholds replace the
    // floor as soon as history exists). An explicit stall_kill_multiplier=0 /
    // stall_idle_ms config remains a real operator opt-out below.
    stallKillMs =
      calibratedData.killSec > 0 ? calibratedData.killSec * 1000 : COLD_START_IDLE_KILL_FLOOR_MS;
    thresholdSource = calibratedData.source;
  } else {
    stallThresholdSec = staticStallThresholdSec;
    stallThresholdMs = stallThresholdSec * 1000;
    stallKillMs = stallKillMultiplier > 0 ? stallThresholdMs * stallKillMultiplier : 0;
    thresholdSource = "static";
  }

  // stall_idle_ms config override (Issue #3484): when set, replaces the
  // multiplier-derived stallKillMs with an absolute idle budget. Preserves
  // existing multiplier logic as-is when unset (returns undefined).
  const configuredStallIdleMs = getStallIdleMs(workspaceRoot);
  if (configuredStallIdleMs !== undefined) {
    stallKillMs = configuredStallIdleMs;
    logStageDiagnostic(
      `[skillRunner] stall_idle_ms override for ${stage}: ${stallKillMs / 1000}s (was ${
        calibratedData ? calibratedData.killSec : (stallThresholdMs * stallKillMultiplier) / 1000
      }s) (Issue #3484)`
    );
  }

  // Idle budget after ANY rate-limit signal before the quota fast-fail fires
  // (Issue #3702). Distinct from the aggressive 120s `status: "limited"` gate:
  // a soft `allowed_warning` (e.g. seven_day bucket) can precede the CLI hanging
  // on a later hard-limited request that never streams. The ticker caps this
  // below `stallKillMs` so a quota signal only makes a stage fail faster.
  const quotaSignalIdleMs = getQuotaSignalIdleMs(workspaceRoot);

  // Per-stage hard cap (Issue #2871, behaviour split in #3155): an absolute
  // ceiling on TOTAL elapsed time, fired independently of the idle-stall kill.
  // Before #3155 this value was folded into `stallKillMs`, which meant the
  // "stall kill" path could fire on a productive stage that simply ran long —
  // see issue #338 incident, where feature-validate was killed at 1201s while
  // still emitting tool_use chunks every few seconds. The two checks now live
  // in the ticker as separate gates: idle-stall via `stallKillMs`, absolute
  // ceiling via `hardCapMs`.
  const hardCapMs = getStageHardCapMs(stage, workspaceRoot);

  // Per-stage cost cap (Issue #3002, model-scaled in #3180, mode-scaled in
  // #3217): hard USD ceiling enforced both inside the stall ticker AND on
  // every streaming token-usage update. 0 = uncapped. Distinct from
  // BudgetEnforcer (which uses estimate-vs-actual with grace) — this cap is
  // a deterministic hard ceiling that fires the moment accumulated cost
  // crosses the threshold.
  //
  // Mode-aware multipliers compose multiplicatively:
  //   effectiveCap = baseCap × modelScale × modeMultiplier
  // - modelScale (#3180) widens the cap for heavier model/effort
  //   (e.g. opus:high = 5.0×). See `COST_CAP_MODEL_SCALE`.
  // - modeMultiplier (#3217) widens or narrows the cap based on the active
  //   performance mode (efficiency=0.5×, elevated=1.0×, maximum=2.0×).
  //   See `DEFAULT_COST_CAP_MODE_MULTIPLIER`.
  // At elevated mode (default), the multiplier is 1.0× — math is identical
  // to pre-#3217.
  const performanceModeForCostCap = getPerformanceMode(workspaceRoot);
  const {
    baseCap: costCapBaseUsd,
    scale: costCapScale,
    modeMultiplier: costCapModeMultiplier,
    providerScale: costCapProviderScale,
    effectiveCap: costCapUsd,
  } = getEffectiveStageCostCap(
    stage,
    adapter === "claude" ? { model: modelDecision.model, effort: modelDecision.effort } : undefined,
    workspaceRoot,
    performanceModeForCostCap,
    adapter
  );
  let costCapExceeded = false;
  let costAtTerminationUsd = 0;
  if (costCapUsd > 0) {
    if (costCapScale !== 1.0 || costCapModeMultiplier !== 1.0 || costCapProviderScale !== 1.0) {
      logStageDiagnostic(
        `[skillRunner] Stage cost cap for ${stage}: $${costCapUsd.toFixed(2)} ` +
          `(base $${costCapBaseUsd.toFixed(2)} × ${costCapScale.toFixed(2)} for ` +
          `${modelDecision.model}/${modelDecision.effort ?? "default"} × ` +
          `${costCapModeMultiplier.toFixed(2)} for mode=${performanceModeForCostCap} × ` +
          `${costCapProviderScale.toFixed(2)} for provider=${adapter}) ` +
          `(Issue #3002, scaled #3180/#3217/#3229)`
      );
    } else {
      logStageDiagnostic(
        `[skillRunner] Stage cost cap for ${stage}: $${costCapUsd.toFixed(2)} ` +
          `(mode=${performanceModeForCostCap}, provider=${adapter}) (Issue #3002)`
      );
    }
  }

  // Runaway ceiling (Issue #3508): max($75, effectiveCap × runaway_ceiling_multiplier).
  // Demoted from kill to warn-only by Issue #3783 — progress-based detection takes over.
  // 0 when effectiveCap is 0 (uncapped stage).
  const runwayCeilingUsd = getRunwayCeilingUsd(costCapUsd, workspaceRoot);

  // Progress-based runaway detection (Issue #3783): replaces the dollar-ceiling kill.
  // Only activates after minCostToActivateUsd so short cheap stages are never false-killed.
  const progressRunawayConfig = getProgressRunawayConfig(
    workspaceRoot,
    stage,
    performanceModeForCostCap,
    autonomousMode
  );
  const progressMonitor = new ProgressMonitor(progressRunawayConfig);
  // No-progress window the elapsed hard-cap gate consults (Issue #3851).
  const progressRunawayWindowMs = progressRunawayConfig.noProgressWindowMs;
  // One-shot log when the elapsed hard-cap was reached but the progress gate
  // spared the stage (healthy long run).
  let hardCapProgressGateLogged = false;

  // Fail-open instrumentation for the runaway monitor (Issue #295). Every
  // tool_use event surfaced by the stream parser increments this counter,
  // regardless of whether its signature was novel. If a runaway kill is ever
  // about to fire while the ProgressMonitor recorded ZERO signals yet this
  // counter is > 0, the parser→monitor feed is disconnected (not the agent
  // stalled) and the kill is suppressed — a blind monitor must never shoot.
  let parsedToolEventCount = 0;
  let runawayFeedDisconnectWarned = false;

  // Warn threshold (Issue #3508): historicalMedian × cost_warn_multiplier.
  // Populated by the caller (HeadlessOrchestrator) and passed in as a parameter.
  // Defaults to 0 (disabled) when no history exists.
  const resolvedWarnThresholdUsd = warnThresholdUsd ?? 0;
  let costWarnFired = false;

  if (runwayCeilingUsd > 0) {
    logStageDiagnostic(
      `[skillRunner] Runaway ceiling for ${stage}: $${runwayCeilingUsd.toFixed(2)} ` +
        `(effectiveCap $${costCapUsd.toFixed(2)} × multiplier, $75 floor) (Issue #3508)`
    );
  }
  if (resolvedWarnThresholdUsd > 0) {
    logStageDiagnostic(
      `[skillRunner] Cost warn threshold for ${stage}: $${resolvedWarnThresholdUsd.toFixed(2)} (Issue #3508)`
    );
  }

  // Per-stage time cap (Issue #3229): the time-based fallback for
  // adapters where token cost is structurally meaningless
  // (`provider_scale=0`, e.g. lm-studio, ollama). When the provider
  // scale fires the "switch to time-cap" sentinel we OR this value with
  // the existing `hardCapMs` ticker — whichever is smaller and `> 0`
  // wins, leaving the absolute hard-cap escape hatch intact.
  const stageTimeCapMs = getStageTimeCapMs(stage, workspaceRoot);
  const timeCapActive = costCapUsd === 0 && costCapProviderScale === 0 && stageTimeCapMs > 0;
  const effectiveHardCapMs = timeCapActive
    ? hardCapMs > 0
      ? Math.min(hardCapMs, stageTimeCapMs)
      : stageTimeCapMs
    : hardCapMs;
  if (timeCapActive) {
    logStageDiagnostic(
      `[skillRunner] Stage time cap for ${stage}: ${stageTimeCapMs / 1000}s ` +
        `(provider=${adapter}, provider_scale=0 → time-cap mode; ` +
        `effective hard-cap=${effectiveHardCapMs / 1000}s) (Issue #3229)`
    );
  }

  // Log threshold source at stage start for observability (Issue #2654)
  const killInfo =
    stallKillMs > 0
      ? calibratedData?.isColdStart
        ? `idle_kill=${stallKillMs / 1000}s(cold-start floor, #252)`
        : `idle_kill=${stallKillMs / 1000}s`
      : "idle_kill=disabled";
  const hardCapInfo = effectiveHardCapMs > 0 ? `, hard_cap=${effectiveHardCapMs / 1000}s` : "";
  logStageDiagnostic(
    `[skillRunner] Stall thresholds for ${stage}: warn=${stallThresholdSec}s (source: ${thresholdSource}), ${killInfo}${hardCapInfo}`
  );

  // Autonomous mode escalation config (Issue #2656)
  const autonomousStallCfg = autonomousMode ? getAutonomousStallConfig(workspaceRoot) : undefined;
  const escalationEnabled = autonomousMode && autonomousStallCfg?.escalationEnabled !== false;
  // Extreme stall threshold: max_observed × 3 or stall_threshold × 10 (whichever is available)
  const extremeThresholdMs = escalationEnabled
    ? calibratedData && calibratedData.warnSec > 0 && !calibratedData.isColdStart
      ? calibratedData.warnSec * 3 * 1000 // max_observed-based: calibrated warn × 3
      : stallThresholdMs * 10 // fallback: static threshold × 10
    : 0;
  // Escalation levels mapped to multipliers of the stall threshold
  const ESCALATION_LEVELS: StallEscalationLevel[] = [
    "status_bar",
    "output_panel",
    "notification",
    "discord",
    "pause",
  ];
  // Track which escalation levels have been triggered
  let escalationIndex = 0; // 0 = status_bar already fires via normal stall warning
  let stallPauseActive = false;
  let stallPauseResolved = false;

  if (escalationEnabled) {
    logStageDiagnostic(
      `[skillRunner] Autonomous escalation enabled for ${stage}: ` +
        `extreme=${extremeThresholdMs / 1000}s, pause_timeout=${autonomousStallCfg!.pauseTimeoutMs / 1000}s`
    );
  }

  let stallMultiplier = 1;
  let stallKilled = false;
  let stallKillDisabled = false; // Set true when user clicks "Keep Waiting" (Issue #2653)

  // Stage-exit diagnostic state (Issue #3605). Captured at signal-delivery
  // time so the post-exit completion handler can forward authoritative values
  // to the Go scheduler via pipeline.stageResult. Empty values flow through
  // omitempty at the IPC boundary so healthy runs stay terse.
  let exitSignal: string = "";
  let exitSignalSource: string = "";
  let lastBashCommand: string = "";
  let lastBashExit: number | undefined;
  let pendingBashToolUseId: string | undefined;
  let stopHookErrored = false;
  // 4 KB stderr ring buffer (Issue #3605). Existing OUTPUT_ERROR_TAIL_MAX_CHARS
  // is 50 KB and serves the V3 record. The narrower 4 KB ring is purpose-built
  // for the daily exit-record stderr_tail field — kept in lock-step with the
  // Go-side stageExitRecordMaxStderrTailBytes constant.
  const STAGE_EXIT_STDERR_TAIL_MAX = 4 * 1024;
  let exitStderrTail: string = "";

  // Nudge grace period (Issue #3484): deferred kill gives Claude one last chance
  // to resume output before SIGTERM fires. Hard-cap and quota-fast-fail paths
  // skip this grace period to avoid prolonging already-overbudget runs.
  const NUDGE_GRACE_MS = 60_000;
  let nudgeAttempted = false;
  let nudgeAtMs: number | undefined;

  // Stall diagnostic enrichment (Issue #3484): track last tool result ID and
  // last phase name seen in the output stream for post-hoc diagnosis.
  let lastToolResultId: string | undefined;
  let lastPhaseName: string | undefined;

  // Activity heartbeat (Issue #338 → #3155): tracks the most recent stdout or
  // stderr chunk from the subprocess. The idle-stall kill compares this to
  // `stallKillMs` so a stage that's actively producing output (tool_use blocks,
  // assistant chunks, build progress) is not killed for simply running long.
  // Initialised to startedAtMs so the first idle window is measured from
  // process start, matching pre-split behaviour for genuinely silent stages.
  let lastChunkAtMs = startedAtMs;

  // Connectivity-aware stall gating (Issue #3203). When ConnectivityStateBus
  // reports the network is offline, the subagent is almost certainly blocked
  // on Anthropic API or tool-call HTTP and emits no chunks. The idle-kill and
  // hard-cap gates would terminate it incorrectly. We suspend both gates while
  // offline; on reconnect we reset `lastChunkAtMs` (fresh idle budget) and
  // accumulate the offline duration so it does not count toward the hard-cap.
  let connectivityOfflineSinceMs: number | null = null;
  let connectivityAccumulatedOfflineMs = 0;

  // Cost warn closure (Issue #3508). Non-blocking — fires a single toast when
  // cost crosses warnThresholdUsd, then sets costWarnFired to prevent re-trigger.
  // Never kills the process. Safe to call from both the 30s stall ticker and
  // from streaming token-usage updates.
  const checkCostWarn = (): void => {
    if (resolvedWarnThresholdUsd <= 0 || costWarnFired || costCapExceeded || stallKilled) return;
    const costNow = tokenAccumulator.getTotal().costUsd;
    if (costNow <= 0 || costNow <= resolvedWarnThresholdUsd) return;
    costWarnFired = true;
    const msg =
      `[cost-warn] Issue #${issueNumber ?? "?"}: ${stage} is tracking above warn threshold ` +
      `($${costNow.toFixed(2)} > $${resolvedWarnThresholdUsd.toFixed(2)} warn threshold). Pipeline continues.\n`;
    callbacks?.onStderr?.(msg);
  };

  // Runaway-ceiling warn closure (Issue #3783 demotes from kill to warn-only).
  // Fires a single warning toast when cost crosses runwayCeilingUsd; never kills.
  // The progress-based kill path (checkRunaway below) is the primary enforcement.
  let runwayCeilingWarnFired = false;
  const checkRunawayCeilingWarn = (): void => {
    if (runwayCeilingUsd <= 0 || runwayCeilingWarnFired || costCapExceeded || stallKilled) return;
    const costNow = tokenAccumulator.getTotal().costUsd;
    if (costNow <= 0 || costNow <= runwayCeilingUsd) return;
    runwayCeilingWarnFired = true;
    callbacks?.onStderr?.(
      `[runaway-ceiling-warn] Stage ${stage}: cost $${costNow.toFixed(4)} crossed ` +
        `runaway ceiling $${runwayCeilingUsd.toFixed(2)} (warn-only — progress monitor ` +
        `is active). Pipeline continues. (Issue #3783)\n`
    );
  };

  // Progress-based kill closure (Issue #3783). Returns true when the stage
  // should be killed. Emits [runaway-progress-exceeded] — classified as
  // TerminalKindRunawayProgress by Go (stall-kill recovery path, 30m backoff).
  // costCapExceeded is set true for backward compat with the Go scheduler's
  // result evaluation (it checks costCapExceeded to classify terminal outcomes).
  // `force` (Issue #3851): bypass the progress-window/churn gate inside
  // ProgressMonitor.check because the caller (the Nx-threshold escalation) has
  // ALREADY confirmed no productive progress over the window AND a high stall
  // multiple. Reuses the same kill machinery + [runaway-progress-exceeded]
  // marker so the Go scheduler classifies it identically (transient stall-kill
  // path, 30m backoff).
  const checkRunaway = (force = false): boolean => {
    if (costCapExceeded || stallKilled || stallKillDisabled) return false;
    const costNow = tokenAccumulator.getTotal().costUsd;
    const result = progressMonitor.check(costNow);

    if (result.shouldWarn && !costWarnFired) {
      callbacks?.onStderr?.(
        `[runaway-progress-warn] Stage ${stage}: ${result.reason} (Issue #3783)\n`
      );
    }

    if (!force && !result.shouldKill) return false;

    // Fail-open guard (Issue #295): a blind monitor must never shoot. The stage
    // parsed real tool events (`parsedToolEventCount > 0`) but the monitor
    // recorded ZERO signals — the parser→monitor feed is disconnected, the
    // agent is NOT stalled. Killing here would be a false kill (the bowlsheet
    // #262 class). Log the discrepancy prominently and refuse to kill. Applies
    // to the forced (Nx-escalation) path too — the blindness is what matters,
    // not which path proposed the kill.
    if (isBlindMonitorKill(result.signalsSeen, parsedToolEventCount)) {
      if (!runawayFeedDisconnectWarned) {
        runawayFeedDisconnectWarned = true;
        callbacks?.onStderr?.(
          `[runaway-progress-feed-disconnect] Stage ${stage}: runaway kill SUPPRESSED (fail-open). ` +
            `Progress monitor recorded 0 signals but ${parsedToolEventCount} tool events were parsed ` +
            `from the stream — the parser→monitor feed is disconnected, the agent is NOT stalled. ` +
            `Refusing to kill a stage the monitor cannot see. (Issue #295)\n`
        );
      }
      return false;
    }

    costCapExceeded = true; // backward compat — scheduler checks this flag
    exitSignalSource = "runaway-progress";
    // Report the REAL burn (#296). `costNow` is the authoritative accumulator,
    // which mid-stage is usually empty (no terminal `result` envelope has
    // arrived), so on its own it under-reports the kill cost — the live
    // estimator holds the true in-stage spend from per-turn `assistant`
    // messages. Take whichever is higher so the kill log, `costAtTerminationUsd`,
    // and the cost booked at close (also the live estimate — see the close
    // handler) all report the same number. The progress DECISION above stays on
    // `costNow` so runaway detection semantics are unchanged.
    const reportedCostUsd = Math.max(costNow, liveEstimator.estimate().costUsd);
    costAtTerminationUsd = reportedCostUsd;
    const elapsed = Date.now() - startedAtMs;

    const killEvent: StallEvent = {
      timestamp: new Date().toISOString(),
      elapsed_ms: elapsed,
      threshold_ms: stallKillMs,
      action: "kill",
    };
    stallEvents.push(killEvent);
    callbacks?.onStallEvent?.(killEvent);
    callbacks?.onStderr?.(
      `[runaway-progress-exceeded] Stage ${stage} terminated: ${result.reason}. ` +
        `Cost $${reportedCostUsd.toFixed(4)}, signals seen: ${result.signalsSeen}, ` +
        `ms since last progress: ${result.msSinceLastProgress}. ` +
        `Treated as transient (stall-kill path). (Issue #3783)\n`
    );

    clearStallTicker();
    proc.kill("SIGTERM");
    const killTimer = setTimeout(() => {
      try {
        proc.kill("SIGKILL");
      } catch {
        /* already dead */
      }
      if (proc.pid) {
        void killProcessTree(proc.pid, "SIGKILL", callbacks?.onStderr);
      }
    }, 5000);
    proc.on("exit", () => clearTimeout(killTimer));
    return true;
  };

  stallTicker = setInterval(() => {
    const elapsed = Date.now() - startedAtMs;
    const idleMs = Date.now() - lastChunkAtMs;

    // Connectivity-aware stall gating (Issue #3203). Suspend kill checks when
    // the network is offline; reset the idle window when connectivity returns.
    const connState = ConnectivityStateBus.state;
    if (connState === "offline") {
      if (connectivityOfflineSinceMs === null) {
        connectivityOfflineSinceMs = Date.now();
        const pauseEvent: StallEvent = {
          timestamp: new Date().toISOString(),
          elapsed_ms: elapsed,
          threshold_ms: effectiveHardCapMs > 0 ? effectiveHardCapMs : stallKillMs,
          action: "connectivity_paused",
        };
        stallEvents.push(pauseEvent);
        callbacks?.onStallEvent?.(pauseEvent);
        callbacks?.onStderr?.(
          `[skillRunner] Connectivity offline — stall-kill suspended for ${stage}.\n`
        );
      }
      // Skip every kill / warn check while offline. Cost-cap is also gated
      // because token accounting cannot advance during a network outage.
      return;
    }
    if (connectivityOfflineSinceMs !== null) {
      // Just transitioned back to online or degraded — record the outage,
      // reset the idle budget, emit the resume event, and RETURN. The kill /
      // warn / cost-cap checks below intentionally do not run on the resume
      // tick because `idleMs` was already computed at the top of the tick
      // (before the reset), and would still reflect the stale offline-window
      // value — firing the kill on the same tick we declared "we're back"
      // (#3247). The next ticker fire (HEADLESS_STALL_CHECK_INTERVAL_MS later)
      // recomputes `idleMs` against the freshly-reset `lastChunkAtMs` and
      // resumes the normal flow.
      const offlineDuration = Date.now() - connectivityOfflineSinceMs;
      connectivityAccumulatedOfflineMs += offlineDuration;
      connectivityOfflineSinceMs = null;
      lastChunkAtMs = Date.now();
      const resumeEvent: StallEvent = {
        timestamp: new Date().toISOString(),
        elapsed_ms: Date.now() - startedAtMs,
        threshold_ms: effectiveHardCapMs > 0 ? effectiveHardCapMs : stallKillMs,
        action: "connectivity_resumed",
      };
      stallEvents.push(resumeEvent);
      callbacks?.onStallEvent?.(resumeEvent);
      callbacks?.onStderr?.(
        `[skillRunner] Connectivity restored — resuming stall checks ` +
          `(was offline for ${formatElapsed(offlineDuration)}).\n`
      );
      return;
    }

    // Effective elapsed time excludes offline outages so the hard-cap and
    // escalation thresholds reflect productive runtime, not wall-clock that
    // included a network outage. Raw `elapsed` is still used in event
    // payloads and log strings so observability matches wall-clock.
    const effectiveElapsed = elapsed - connectivityAccumulatedOfflineMs;

    // Cost polling fallback. Push-based check fires on every token-usage
    // update (see `tokenAccumulator.add` sites below); this ticker call
    // catches the rare path where a process burns cost without emitting
    // parseable stream-json usage chunks.
    // Issue #3783: checkCostWarn (historical median warn), then
    // checkRunawayCeilingWarn (ceiling warn-only), then checkRunaway (progress kill).
    checkCostWarn();
    checkRunawayCeilingWarn();
    if (checkRunaway()) {
      return;
    }

    // ── Quota-exhausted fast-fail (Issue #3425, follow-up to #3386) ──────
    // PR #3405 closed #3386 by relabeling the kill marker when the LAST
    // observed `rate_limit_event` indicated quota exhaustion. It did NOT
    // shorten the time-to-kill: the stall ticker still waited for the full
    // `stallKillMs` (80 minutes for feature-dev), burning $13–$23 per
    // attempt. The acceptance criteria explicitly required "escalate idle
    // thresholds when a hard-quota signal is present" / "fast-fail" — that
    // half was missed.
    //
    // This gate restores the missing behaviour: when a recent
    // `rate_limit_event` carries a quota-exhausted indicator AND the
    // subprocess has been idle for ≥ QUOTA_EXHAUSTED_FAST_FAIL_IDLE_MS
    // (default 120s), kill immediately. Idle is measured from the latest
    // of `lastChunkAtMs` and `lastRateLimitEventAtMs` — the latter handles
    // the case where the rate_limit_event itself was the last emitted
    // chunk (so `lastChunkAtMs` would otherwise reset the budget).
    //
    // Issue #3448: the trigger previously also OR'd in
    // `overageStatus === "rejected" && overageDisabledReason ===
    // "out_of_credits"` even when `status === "allowed"`. That branch was
    // a false-positive: Anthropic emits exactly that payload as the
    // steady-state on plans without overage enabled — `status: "allowed"`
    // means the current request IS served, and `overageStatus: "rejected"`
    // only means "we would not bill overage if you exceeded base." Killing
    // on that signal terminated dozens of healthy pipelines (#371, #381,
    // #889, #893, #894, #3375, ...). Only `status === "limited"` reliably
    // indicates the base bucket is actually exhausted.
    const quotaExhaustedSignalActive =
      !!lastRateLimitEvent && lastRateLimitEvent.status === "limited";
    const idleSinceQuotaSignalMs =
      lastRateLimitEventAtMs !== undefined
        ? Math.min(idleMs, Date.now() - lastRateLimitEventAtMs)
        : Number.POSITIVE_INFINITY;

    // Soft-quota-signal idle fast-fail (Issue #3702). The aggressive gate above
    // only fires on `status: "limited"` — but a soft `allowed_warning` (e.g. the
    // seven_day bucket) can precede the CLI hanging on a LATER hard-limited
    // request that never streams. #977 idled 81 minutes after exactly this and
    // burned the full 80-min idle budget (~$18) because no `limited` event ever
    // arrived. We don't kill on the warning itself — a healthy stage keeps
    // streaming and resets the idle clock — but prolonged silence after a
    // quota-PRESSURE signal is a reliable quota-block tell. The budget is capped
    // below `stallKillMs` so a quota signal can only make a stage fail faster,
    // never slower, and the kill is classified as quota-exhausted (below) so the
    // scheduler re-arms at the bucket's resetsAt instead of penalizing the
    // issue's failure cap.
    //
    // Issue #3825: only a DEGRADED status (`allowed_warning` or `limited`) arms
    // this gate — NOT a plain `status: "allowed"`. The CLI emits `allowed`
    // events as steady-state telemetry on nearly every run, so keying on "any
    // event seen" mis-routed ordinary idle stalls into the quota-exhausted path
    // and set a GLOBAL cooldown derived from the bucket's NORMAL reset (#3804's
    // feature-validate idled 15m during self-assessment after a healthy
    // `allowed` five_hour event and halted ALL autonomous dispatch for ~1h38m).
    // A stall after a healthy `allowed` now falls through to the normal
    // stall-kill path: transient backoff, no lifetime-cap penalty, no cooldown.
    const degradedQuotaSignalSeen =
      !!lastRateLimitEvent && isQuotaPressureSignal(lastRateLimitEvent.status);
    const quotaSignalIdleBudgetMs =
      stallKillMs > 0 ? Math.min(stallKillMs, quotaSignalIdleMs) : quotaSignalIdleMs;
    const quotaFastFailThresholdMs = quotaExhaustedSignalActive
      ? QUOTA_EXHAUSTED_FAST_FAIL_IDLE_MS
      : quotaSignalIdleBudgetMs;
    const quotaFastFailReached =
      !stallKilled &&
      !stallKillDisabled &&
      shouldQuotaFastFail({
        quotaExhaustedSignalActive,
        anyQuotaSignalSeen: degradedQuotaSignalSeen,
        idleSinceQuotaSignalMs,
        exhaustedFastFailIdleMs: QUOTA_EXHAUSTED_FAST_FAIL_IDLE_MS,
        quotaSignalIdleBudgetMs,
      });

    // ── Autonomous escalation path (Issue #2656) ────────────────────────
    // In autonomous mode with escalation enabled, replace the silent kill
    // with a 5-level escalation sequence that ends with a pause dialog.
    //
    // EXEMPTION (Issue #3425): when the quota-exhausted fast-fail gate has
    // fired, skip the multi-level escalation entirely and fall through to
    // the kill path. Walking the operator through 4 stall warnings + a
    // pause dialog is exactly the friction we're trying to eliminate when
    // the upstream API quota is the diagnosed cause — there is nothing for
    // the operator to do besides wait for the bucket to reset.
    if (
      escalationEnabled &&
      !stallPauseActive &&
      !stallPauseResolved &&
      !stallKilled &&
      !stallKillDisabled &&
      !quotaFastFailReached
    ) {
      // Determine which escalation level we should be at based on elapsed time.
      // Levels are spaced at multipliers of stall threshold:
      //   1× = status_bar, 2× = output_panel, 3× = notification, 4× = discord
      //   extreme threshold = pause
      if (effectiveElapsed >= extremeThresholdMs && escalationIndex < ESCALATION_LEVELS.length) {
        // Jump to pause level (last level)
        escalationIndex = ESCALATION_LEVELS.length - 1;
        const pauseLevel = ESCALATION_LEVELS[escalationIndex];
        const pauseEvent: StallEvent = {
          timestamp: new Date().toISOString(),
          elapsed_ms: elapsed,
          threshold_ms: extremeThresholdMs,
          action: "escalation_pause",
        };
        stallEvents.push(pauseEvent);
        callbacks?.onStallEvent?.(pauseEvent);
        callbacks?.onStallEscalation?.(pauseLevel, pauseEvent);
        callbacks?.onStderr?.(
          `[skillRunner] Autonomous stall escalation: PAUSE triggered for ${stage} ` +
            `after ${formatElapsed(elapsed)} (extreme threshold: ${formatElapsed(extremeThresholdMs)}).\n`
        );

        // If pause callback is available, enter pause state
        if (callbacks?.onStallPause) {
          stallPauseActive = true;
          clearStallTicker(); // Stop checking while paused

          const pausePayload: PauseForStallPayload = {
            reason: "stall_extreme",
            issue_number: issueNumber ?? 0,
            stage,
            elapsed_ms: elapsed,
            threshold_ms: extremeThresholdMs,
            timeout_ms: autonomousStallCfg!.pauseTimeoutMs,
          };

          // Auto-abort timer
          const autoAbortTimer = setTimeout(() => {
            if (stallPauseActive && !stallPauseResolved) {
              stallPauseResolved = true;
              stallPauseActive = false;
              stallKilled = true;
              const autoAbortEvent: StallEvent = {
                timestamp: new Date().toISOString(),
                elapsed_ms: Date.now() - startedAtMs,
                threshold_ms: extremeThresholdMs,
                action: "auto_abort",
              };
              stallEvents.push(autoAbortEvent);
              callbacks?.onStallEvent?.(autoAbortEvent);
              callbacks?.onStderr?.(
                `[skillRunner] Auto-abort: no user response within ${formatElapsed(autonomousStallCfg!.pauseTimeoutMs)}. Killing process.\n`
              );
              proc.kill("SIGTERM");
              setTimeout(() => {
                try {
                  proc.kill("SIGKILL");
                } catch {
                  /* already dead */
                }
                if (proc.pid) {
                  void killProcessTree(proc.pid, "SIGKILL", callbacks?.onStderr);
                }
              }, 5000);
            }
          }, autonomousStallCfg!.pauseTimeoutMs);

          // Ask user for resolution (non-blocking — the interval is already cleared)
          void callbacks
            .onStallPause(pausePayload)
            .then((resolution) => {
              clearTimeout(autoAbortTimer);
              if (stallPauseResolved) return; // auto-abort already fired
              stallPauseResolved = true;
              stallPauseActive = false;

              if (resolution === "resume") {
                const resumeEvent: StallEvent = {
                  timestamp: new Date().toISOString(),
                  elapsed_ms: Date.now() - startedAtMs,
                  threshold_ms: extremeThresholdMs,
                  action: "resume",
                };
                stallEvents.push(resumeEvent);
                callbacks?.onStallEvent?.(resumeEvent);
                callbacks?.onStderr?.(
                  `[skillRunner] User chose Resume — continuing stage execution.\n`
                );
                stallKillDisabled = true; // Don't kill after resume
              } else {
                stallKilled = true;
                const abortEvent: StallEvent = {
                  timestamp: new Date().toISOString(),
                  elapsed_ms: Date.now() - startedAtMs,
                  threshold_ms: extremeThresholdMs,
                  action: "abort",
                };
                stallEvents.push(abortEvent);
                callbacks?.onStallEvent?.(abortEvent);
                callbacks?.onStderr?.(`[skillRunner] User chose Abort — killing process.\n`);
                proc.kill("SIGTERM");
                setTimeout(() => {
                  try {
                    proc.kill("SIGKILL");
                  } catch {
                    /* already dead */
                  }
                  if (proc.pid) {
                    void killProcessTree(proc.pid, "SIGKILL", callbacks?.onStderr);
                  }
                }, 5000);
              }
            })
            .catch(() => {
              // Pause callback failed — fall back to kill
              clearTimeout(autoAbortTimer);
              if (!stallPauseResolved) {
                stallPauseResolved = true;
                stallPauseActive = false;
                stallKilled = true;
                proc.kill("SIGTERM");
              }
            });

          return;
        }
        // No pause callback — fall through to normal kill below
      } else {
        // Check intermediate escalation levels (spaced at threshold multipliers)
        const levelMultipliers = [1, 2, 3, 4]; // status_bar=1×, output_panel=2×, notification=3×, discord=4×
        for (let i = escalationIndex; i < levelMultipliers.length; i++) {
          const levelThresholdMs = stallThresholdMs * levelMultipliers[i];
          if (effectiveElapsed >= levelThresholdMs && escalationIndex <= i) {
            escalationIndex = i + 1;
            const level = ESCALATION_LEVELS[i];
            const escalationEvent: StallEvent = {
              timestamp: new Date().toISOString(),
              elapsed_ms: elapsed,
              threshold_ms: levelThresholdMs,
              action: "warn",
            };
            // Don't push duplicate stall events for status_bar (already handled below)
            if (i > 0) {
              stallEvents.push(escalationEvent);
              callbacks?.onStallEvent?.(escalationEvent);
            }
            callbacks?.onStallEscalation?.(level, escalationEvent);
            callbacks?.onStderr?.(
              `[skillRunner] Autonomous escalation: ${level} (${i + 1}× threshold) for ${stage} ` +
                `after ${formatElapsed(elapsed)}.\n`
            );
          }
        }
        return; // Don't execute normal kill logic while escalation is active
      }
    }

    // ── Normal stall kill path (interactive mode or escalation disabled) ─
    // True stall auto-kill: forcibly terminate after `stallKillMs` of IDLE
    // time — i.e. no stdout/stderr from the subprocess for that duration.
    // Issue #338 incident (#3155): the previous check used `elapsed` (total
    // runtime since start), which killed productive long stages that were
    // still emitting tool_use blocks every few seconds. Idle-time semantics
    // match what "stall" actually means and stop punishing slow but
    // progressing work. The absolute ceiling is enforced separately below
    // via `stage_hard_caps`.
    //
    // The pr-create-specific early "heartbeat" kill (Issue #2871) was
    // removed in #2973 because tool_use silences during `gh pr create`
    // could legitimately reach 60–120s. Idle thresholds are still tuned
    // higher than that (default feature-validate is 1200s = 20 min idle),
    // so those legitimate Bash silences continue to be tolerated.
    const idleKillThresholdReached = stallKillMs > 0 && idleMs >= stallKillMs;
    // Absolute hard-cap reached on wall-clock.
    const hardCapElapsedReached = effectiveHardCapMs > 0 && effectiveElapsed >= effectiveHardCapMs;
    // Issue #3851: the elapsed hard-cap is PROGRESS-GATED — it only kills when
    // the cap is reached AND the stage is making no productive progress (no
    // commits / new-file writes / phase markers / CI progress within the
    // no-progress window). A stage that is steadily committing at 91 minutes is
    // NEVER killed by the cap — that would re-introduce the #2982/#3840 blunt
    // elapsed-kill class. The time-cap mode (provider_scale=0, no meaningful
    // cost signal) keeps the original unconditional behaviour because there is
    // no cost-driven progress monitor to consult for those adapters.
    const noProductiveProgress =
      progressMonitor.msSinceLastProductiveProgress > progressRunawayWindowMs;
    const hardCapReached = timeCapActive
      ? hardCapElapsedReached
      : hardCapElapsedReached && noProductiveProgress;
    if (hardCapElapsedReached && !hardCapReached && !hardCapProgressGateLogged) {
      hardCapProgressGateLogged = true;
      callbacks?.onStderr?.(
        `[hard-cap-progress-gate] Stage ${stage} reached the ${formatElapsed(effectiveHardCapMs)} ` +
          `elapsed hard-cap but is still making productive progress ` +
          `(${progressMonitor.getProductiveProgressDelta()} productive signals; ` +
          `${Math.round(progressMonitor.msSinceLastProductiveProgress / 1000)}s since last). ` +
          `Not killing — healthy long run. (Issue #3851)\n`
      );
    }

    // Nudge grace period (Issue #3484): when the idle threshold is first reached,
    // log a [stall-nudge] warning and defer SIGTERM by NUDGE_GRACE_MS (60s).
    // Hard-cap and quota-fast-fail paths skip this grace to avoid cost overruns.
    // Reset nudge state when new output arrives (idleKillThresholdReached becomes false).
    if (!idleKillThresholdReached && nudgeAttempted) {
      nudgeAttempted = false;
      nudgeAtMs = undefined;
    }
    if (
      idleKillThresholdReached &&
      !hardCapReached &&
      !quotaFastFailReached &&
      !nudgeAttempted &&
      !stallKilled &&
      !stallKillDisabled
    ) {
      nudgeAttempted = true;
      nudgeAtMs = Date.now();
      const nudgeMsg = `[stall-nudge] Stage ${stage} idle for ${formatElapsed(idleMs)}. Waiting ${NUDGE_GRACE_MS / 1000}s before kill.\n`;
      stderrBuffer = appendTail(stderrBuffer, nudgeMsg, OUTPUT_ERROR_TAIL_MAX_CHARS);
      callbacks?.onStderr?.(nudgeMsg);
    }
    const pastNudgeGrace =
      nudgeAttempted && nudgeAtMs !== undefined && Date.now() - nudgeAtMs >= NUDGE_GRACE_MS;
    const shouldKill =
      ((idleKillThresholdReached && (!nudgeAttempted || pastNudgeGrace)) ||
        hardCapReached ||
        quotaFastFailReached) &&
      !stallKilled &&
      !stallKillDisabled;

    if (shouldKill) {
      stallKilled = true;
      // Issue #3386: distinguish a true stall (the agent is genuinely
      // wedged) from a quota-exhausted silence (the agent's next API turn
      // is silently waiting for the Anthropic 5-hour bucket to reset
      // because overage is rejected with `out_of_credits`). The two look
      // identical to the idle watchdog — zero stdout chunks for N minutes —
      // but they need very different retry policies. Penalizing an issue's
      // lifetime failure cap for an upstream API quota wall is wrong (every
      // issue eventually trips it the same way) and the default exponential
      // backoff re-fires under the same conditions and burns more tokens.
      //
      // Detection: only `status === "limited"` reliably indicates the
      // base bucket is exhausted — see the matching commentary on the
      // fast-fail gate above. Issue #3448 removed the overage-only branch
      // that previously fired on every healthy `status: "allowed"`
      // payload, mis-routing dozens of healthy idle-stalls as quota
      // failures. The Go classifier sees `[rate-limit-quota-exhausted]`
      // only when the bucket truly IS limited.
      // Issue #3702: classify ANY quota-fast-fail kill as quota-exhausted, not
      // just the `status: "limited"` case. Both the aggressive 120s gate and the
      // soft-signal idle gate indicate the stage is wedged behind a quota wall,
      // so both should route to the Go scheduler's quota-backoff/resume-at-reset
      // path rather than the generic stall path (which penalizes the issue's
      // lifetime failure cap and re-fires under the same conditions).
      const quotaExhausted = !hardCapReached && quotaFastFailReached;
      // Issue #3425/#3702: when a fast-fail gate fires, the marker's reported
      // idle window is the gate's threshold (120s for `limited`, the capped
      // quota-signal budget otherwise), not the full `stallKillMs` (which would
      // say 80m even when we killed far sooner). Use whichever idle measurement
      // actually triggered the kill.
      const reportedIdleMs = quotaFastFailReached
        ? Math.max(quotaFastFailThresholdMs, idleSinceQuotaSignalMs)
        : stallKillMs;
      const killReason = hardCapReached
        ? timeCapActive
          ? `exceeded stage_time_cap (${formatElapsed(effectiveHardCapMs)} total runtime)`
          : `exceeded stage_hard_cap (${formatElapsed(effectiveHardCapMs)} total runtime)`
        : quotaExhausted
          ? `[rate-limit-quota-exhausted] idle ${formatElapsed(reportedIdleMs)} after rate_limit_event (status=${lastRateLimitEvent?.status ?? "unknown"}; ${lastRateLimitEvent?.rateLimitType ?? "unknown"} bucket; resetsAt=${lastRateLimitEvent?.resetsAt ?? "unknown"})`
          : `exceeded stall idle threshold (${formatElapsed(stallKillMs)} without output)`;
      const killEvent: StallEvent = {
        timestamp: new Date().toISOString(),
        elapsed_ms: elapsed,
        threshold_ms: hardCapReached
          ? effectiveHardCapMs
          : quotaFastFailReached
            ? quotaFastFailThresholdMs
            : stallKillMs,
        action: "kill",
      };
      stallEvents.push(killEvent);
      callbacks?.onStallEvent?.(killEvent);
      // The kill marker MUST reach `stderrBuffer` so it ends up in
      // `result.error.message` via `inferProcessError`. Pre-fix, the
      // synthetic onStderr call below only flowed to the UI logger and
      // diagnostic log file; `stderrBuffer` is fed exclusively by
      // `proc.stderr.on('data')` (line 3272). Subprocess stderr is empty
      // for an idle stall (the agent never wrote anything), so
      // `inferProcessError` returned undefined and CPM fell back to the
      // generic `Pipeline failed at <stage>` Error. The result: the
      // `[rate-limit-quota-exhausted]` marker (#3386), `[stall-killed]`
      // marker, and any other diagnostic context the autonomous Go
      // scheduler classifies on were all lost between TS and Go (#3406).
      const killMarker =
        `[skillRunner] Stage ${killReason} — ` +
        `forcibly terminating process after ${formatElapsed(elapsed)} ` +
        `(idle for ${formatElapsed(idleMs)}).\n`;
      stderrBuffer = appendTail(stderrBuffer, killMarker, OUTPUT_ERROR_TAIL_MAX_CHARS);
      callbacks?.onStderr?.(killMarker);

      // Write diagnostic log (Issue #2871). #3204 added the canonical-workspace
      // mirror so concurrent-mode worktrees don't bury the diagnostic in a
      // directory that gets cleaned up before AutoRetroService runs.
      if (workspaceRoot && issueNumber != null) {
        const diagContent = [
          `=== Stall Kill Diagnostic Log ===`,
          `timestamp: ${new Date().toISOString()}`,
          `stage: ${stage}`,
          `issue: ${issueNumber}`,
          `elapsed: ${formatElapsed(elapsed)}`,
          `idle: ${formatElapsed(idleMs)}`,
          `kill_reason: ${killReason}`,
          `kill_path: ${
            hardCapReached
              ? timeCapActive
                ? "time_cap"
                : "hard_cap"
              : quotaFastFailReached
                ? "quota_fast_fail"
                : "idle"
          }`,
          `stall_kill_ms: ${stallKillMs}`,
          `last_tool_result_id: ${lastToolResultId ?? "(none)"}`,
          `last_phase: ${lastPhaseName ?? "(unknown)"}`,
          `quota_fast_fail_idle_ms: ${quotaFastFailReached ? quotaFastFailThresholdMs : 0}`,
          `idle_since_quota_signal_ms: ${
            lastRateLimitEventAtMs !== undefined ? Date.now() - lastRateLimitEventAtMs : 0
          }`,
          `hard_cap_ms: ${hardCapMs}`,
          `stage_time_cap_ms: ${stageTimeCapMs}`,
          `effective_hard_cap_ms: ${effectiveHardCapMs}`,
          ``,
          `=== STDOUT ===`,
          diagStdoutBuffer || "(empty)",
          ``,
          `=== STDERR ===`,
          diagStderrBuffer || "(empty)",
        ].join("\n");
        writeDiagnosticWithMirror(
          workspaceRoot,
          issueNumber,
          `${stage}-stalled.log`,
          diagContent,
          callbacks?.onStderr
        );
      }

      clearStallTicker();
      proc.kill("SIGTERM");
      // Stage-exit diagnostic capture (Issue #3605): record which kill path
      // delivered the signal so retros can answer "who killed it?" without
      // grepping logs. Idempotent — only the first delivery wins.
      if (exitSignal === "") {
        exitSignal = "SIGTERM";
        exitSignalSource = hardCapReached
          ? "hard-cap"
          : quotaFastFailReached
            ? "quota-fast-fail"
            : "stall-kill";
      }
      // SIGKILL fallback after 5 seconds — also reap orphaned children
      // (vitest, jest, build commands) so a hung subprocess can't survive
      // the parent and re-trigger the same hang on retry.
      const killTimer = setTimeout(() => {
        try {
          proc.kill("SIGKILL");
        } catch {
          /* already dead */
        }
        // Promote signal record to SIGKILL once the harder signal is delivered;
        // SignalSource stays the same (the originating kill path).
        exitSignal = "SIGKILL";
        if (proc.pid) {
          void killProcessTree(proc.pid, "SIGKILL", callbacks?.onStderr);
        }
      }, 5000);
      proc.on("exit", () => clearTimeout(killTimer));
      return;
    }

    // CI-aware stall detection (Issue #902): if we received CI progress
    // recently during pr-merge, suppress generic stall warnings and show
    // CI-specific status instead.
    if (ciProgressDetected && stage === "pr-merge") {
      const sinceLastProgress = Date.now() - lastCIProgressMs;
      // Only warn if no CI progress for 60s (indicates script stall, not CI wait)
      if (sinceLastProgress < 60_000) {
        // CI is actively reporting — no stall warning needed
        return;
      }
      // CI progress stale — use CI-specific messaging for stall warning
      if (!stallWarningShown && effectiveElapsed >= stallThresholdMs) {
        stallWarningShown = true;
        stallMultiplier = 2;
        callbacks?.onStderr?.(
          `[skillRunner] CI checks still in progress after ${formatElapsed(elapsed)}. ` +
            `Last status: ${lastCIStatus || "unknown"}. This is normal for long CI runs.\n`
        );
        return;
      }
    }

    if (!stallWarningShown && effectiveElapsed >= stallThresholdMs) {
      stallWarningShown = true;
      stallMultiplier = 2; // Next warning at 2x threshold
      const warnEvent: StallEvent = {
        timestamp: new Date().toISOString(),
        elapsed_ms: elapsed,
        threshold_ms: stallThresholdMs,
        action: "warn",
      };
      stallEvents.push(warnEvent);
      callbacks?.onStallEvent?.(warnEvent);
      callbacks?.onStallWarning?.(warnEvent, 1);
      callbacks?.onStderr?.(
        `[skillRunner] Stage still running after ${formatElapsed(elapsed)}. ` +
          "This may be normal for network/GitHub operations, but if no progress appears, stop and retry.\n"
      );

      // One-time actionable prompt for the most common confusing case.
      if (stage === "issue-pickup" && !stallPromptShown) {
        stallPromptShown = true;
        void vscode.window
          .showWarningMessage(
            `Nightgauge: Issue Pickup is still running after ${formatElapsed(elapsed)}.`,
            "Stop Stage",
            "Keep Waiting"
          )
          .then((choice) => {
            if (choice === "Stop Stage" && !stageCompleted) {
              const stopEvent: StallEvent = {
                timestamp: new Date().toISOString(),
                elapsed_ms: Date.now() - startedAtMs,
                threshold_ms: stallThresholdMs,
                action: "stop_stage",
              };
              stallEvents.push(stopEvent);
              callbacks?.onStallEvent?.(stopEvent);
              callbacks?.onStderr?.("[skillRunner] Stop requested from stalled-stage prompt\n");
              proc.kill("SIGTERM");
            } else if (choice === "Keep Waiting") {
              stallKillDisabled = true;
              const keepWaitingEvent: StallEvent = {
                timestamp: new Date().toISOString(),
                elapsed_ms: Date.now() - startedAtMs,
                threshold_ms: stallThresholdMs,
                action: "keep_waiting",
              };
              stallEvents.push(keepWaitingEvent);
              callbacks?.onStallEvent?.(keepWaitingEvent);
              callbacks?.onStderr?.(
                "[skillRunner] User chose Keep Waiting — stall kill timer disabled for this execution\n"
              );
            }
          });
      }
      return;
    }

    // Escalating follow-up warnings at 2x, 3x, 4x... of original threshold
    if (stallWarningShown) {
      const nextWarningMs = stallThresholdMs * stallMultiplier;
      if (effectiveElapsed >= nextWarningMs) {
        const currentMultiplier = stallMultiplier;
        stallMultiplier++;
        const escalationEvent: StallEvent = {
          timestamp: new Date().toISOString(),
          elapsed_ms: elapsed,
          threshold_ms: stallThresholdMs,
          action: "warn",
        };
        stallEvents.push(escalationEvent);
        callbacks?.onStallEvent?.(escalationEvent);
        callbacks?.onStallWarning?.(escalationEvent, currentMultiplier);
        callbacks?.onStderr?.(
          `[skillRunner] Stage still running after ${formatElapsed(elapsed)} (${currentMultiplier}x threshold). ` +
            "Consider stopping if no progress.\n"
        );

        // Issue #3851: the Nx warning used to be a pure no-op — it incremented
        // the multiplier forever and only printed "consider stopping," so a
        // churning stage warned every threshold-interval but was never stopped.
        // At a high multiple AND no productive progress, escalate to the
        // existing runaway kill machinery. Progress-gated: a stage steadily
        // committing / writing new files keeps resetting the productive window,
        // so a healthy long run is never killed here (#2982/#3840 guard).
        if (
          currentMultiplier >= NX_RUNAWAY_KILL_MULTIPLE &&
          !progressRunawayConfig.observeOnly &&
          !stallKilled &&
          !stallKillDisabled &&
          !costCapExceeded &&
          progressMonitor.msSinceLastProductiveProgress > progressRunawayWindowMs
        ) {
          callbacks?.onStderr?.(
            `[runaway-nx-threshold] Stage ${stage} hit ${currentMultiplier}× stall threshold ` +
              `with no productive progress for ` +
              `${Math.round(progressMonitor.msSinceLastProductiveProgress / 1000)}s ` +
              `(productive signals: ${progressMonitor.getProductiveProgressDelta()}). ` +
              `Escalating to runaway kill. (Issue #3851)\n`
          );
          checkRunaway(true);
          return;
        }
      }
    }
  }, HEADLESS_STALL_CHECK_INTERVAL_MS);

  // Track consecutive identical tool calls for loop detection (Issue #218)
  // This detects when Claude repeatedly tries AskUserQuestion despite it being
  // filtered from allowed tools. The CLI treats it as permission denial, causing
  // Claude to retry indefinitely.
  let lastToolCall: { name: string; inputHash: string } | null = null;
  let consecutiveAttempts = 0;

  // Track whether the subagent attempted an interactive prompt (Issue #697)
  // Any AskUserQuestion attempt in headless mode indicates the agent wanted
  // user input that can never be answered — the exit is likely premature.
  let promptDetected = false;
  const MAX_CONSECUTIVE_ATTEMPTS = 3;

  // Handle stdout - parse stream-json for token usage and tool_use blocks
  proc.stdout?.on("data", (data: Buffer) => {
    const text = data.toString();
    callbacks?.onStdout?.(text);
    stdoutRawTail = appendTail(stdoutRawTail, text, OUTPUT_ERROR_TAIL_MAX_CHARS);
    lastChunkAtMs = Date.now();

    // Deterministic phase inference (Issue #3760): emit the stage's first phase
    // as soon as output starts, so stages that don't self-report (feature-dev)
    // still show a live phase immediately. No-op when inference is disabled.
    if (!phaseStartEmitted) {
      phaseStartEmitted = true;
      const startMarker = phaseInference.start();
      if (startMarker) {
        lastPhaseName = startMarker.name;
        traceRecorder.phaseTransition(stage, startMarker);
        callbacks?.onPhaseStart?.(stage, startMarker.name, startMarker.index, startMarker.total);
      }
    }

    // Accumulate for stall diagnostic (Issue #2871), capped at 50KB
    if (diagStdoutBuffer.length < STALL_DIAG_BUFFER_MAX) {
      diagStdoutBuffer += text.slice(0, STALL_DIAG_BUFFER_MAX - diagStdoutBuffer.length);
    }

    // Accumulate output and process complete lines
    stdoutBuffer += text;
    const lines = stdoutBuffer.split("\n");

    // Keep the last incomplete line in the buffer
    stdoutBuffer = lines.pop() ?? "";

    // Process complete lines
    for (const line of lines) {
      // Detect CI progress from wait-for-ci-checks.sh (Issue #902)
      if (line.startsWith("CI_PROGRESS:")) {
        try {
          const ciStatus = JSON.parse(line.substring("CI_PROGRESS:".length));
          ciProgressDetected = true;
          lastCIProgressMs = Date.now();
          progressMonitor.recordSignal("ci_progress");
          if (ciStatus.passed !== undefined && ciStatus.total !== undefined) {
            lastCIStatus = `${ciStatus.passed}/${ciStatus.total} passed, ${ciStatus.pending ?? 0} pending`;
            callbacks?.onStderr?.(
              `[skillRunner] CI: ${lastCIStatus} (${ciStatus.elapsed ?? "?"}s elapsed)\n`
            );
          } else if (ciStatus.method === "gh_run_watch") {
            lastCIStatus = `watching run #${ciStatus.run_id}`;
            callbacks?.onStderr?.(
              `[skillRunner] CI: using gh run watch for run #${ciStatus.run_id}\n`
            );
          }
        } catch {
          /* ignore malformed CI progress */
        }
        continue;
      }

      const parsed = parseStreamJsonLine(line);

      // Served-model attribution (#91): last observed model wins; a refusal
      // fallback event gets one observable log line the moment it fires.
      observeServedModel(parsed);

      // Detect phase markers in streaming text content.
      // Skills emit HTML comments like: <!-- phase:start name="..." index=N total=N stage="..." -->
      //
      // Claude CLI stream-json emits complete `assistant` messages (with text in
      // message.content[].text blocks), NOT incremental `content_block_delta` events.
      // We check both message types for backward compatibility and future-proofing.
      const phaseText =
        parsed?.type === "assistant" && parsed.content
          ? parsed.content
          : parsed?.type === "content_block_delta" && parsed.content
            ? parsed.content
            : null;

      if (phaseText) {
        phaseContentBuffer += phaseText;
        // Check each complete line in the buffer for phase markers
        const phaseLines = phaseContentBuffer.split("\n");
        phaseContentBuffer = phaseLines.pop() ?? "";
        for (const phaseLine of phaseLines) {
          const marker = parsePhaseMarker(phaseLine);
          if (marker) {
            logStageDiagnostic(
              `[skillRunner] PHASE MARKER DETECTED: stage=${stage} name=${marker.name} index=${marker.index} total=${marker.total} hasCallback=${!!callbacks?.onPhaseStart}`
            );
            lastPhaseName = marker.name; // track for stall diagnostic (#3484)
            phaseInference.observeRealMarker(marker.index); // real marker wins (#3760)
            progressMonitor.recordSignal("phase_marker");
            traceRecorder.phaseTransition(stage, marker);
            callbacks?.onPhaseStart?.(stage, marker.name, marker.index, marker.total);
          }
        }
      }
      // Flush remaining phase buffer on content_block_stop
      if (parsed?.type === "content_block_stop" && phaseContentBuffer) {
        for (const marker of parsePhaseMarkers(phaseContentBuffer)) {
          lastPhaseName = marker.name; // track for stall diagnostic (#3484)
          phaseInference.observeRealMarker(marker.index); // real marker wins (#3760)
          traceRecorder.phaseTransition(stage, marker);
          callbacks?.onPhaseStart?.(stage, marker.name, marker.index, marker.total);
        }
        phaseContentBuffer = "";
      }

      // Detect phase markers from printf tool output (tool_result path).
      // This is the SOLE detection channel for printf-emitted markers: the
      // tool_result in the subsequent user message carries the actual printf
      // stdout — always complete with a real newline — and is the reliable
      // signal for all stages, especially those where extended thinking
      // suppresses assistant text blocks (#3465 follow-up). The command echo
      // in the assistant's tool_use input is deliberately not scanned
      // (tokenParser no longer surfaces it) — counting both sightings of the
      // same marker double-recorded every phase in phaseHistory (#217).
      if (parsed?.type === "user" && parsed.toolResult?.content) {
        for (const marker of parsePhaseMarkers(parsed.toolResult.content)) {
          lastPhaseName = marker.name;
          phaseInference.observeRealMarker(marker.index); // real marker wins (#3760)
          traceRecorder.phaseTransition(stage, marker);
          callbacks?.onPhaseStart?.(stage, marker.name, marker.index, marker.total);
        }
      }

      // Deterministic phase inference from assistant-message tool calls (#3760).
      // The CLI delivers tool_use inside complete `assistant` messages, so this
      // is the primary signal for edit-heavy stages (feature-dev) that don't
      // reliably emit phase markers. No-op for self-reporting stages; monotonic.
      if (parsed?.toolUses) {
        for (const { name, input } of parsed.toolUses) {
          const inferred = phaseInference.observeToolUse(name, input);
          if (inferred) {
            lastPhaseName = inferred.name;
            traceRecorder.phaseTransition(stage, inferred);
            callbacks?.onPhaseStart?.(stage, inferred.name, inferred.index, inferred.total);
          }
        }
      }

      // Feed the runaway progress monitor from EVERY parsed tool call (Issue
      // #295). This runs off both the plural `toolUses[]` shape (the runtime
      // shape — complete `assistant` messages) and the singular
      // `content_block_start` shape, so the productive/activity signal feed can
      // never again go silent for the whole stage. The returned count powers
      // the fail-open guard in checkRunaway. Kept separate from the phase
      // inference above (different concern: window progress vs phase tracking).
      parsedToolEventCount += recordToolCallProgress(progressMonitor, parsed);

      if (parsed?.usage) {
        // Accumulate token usage
        tokenAccumulator.add(parsed.usage);

        // Notify callback with updated usage
        callbacks?.onTokenUsage?.(tokenAccumulator.getTotal());

        // Push-based cost enforcement (Issue #3180, #3783): bound overshoot to a
        // single tool-use's incremental cost rather than the 30s ticker poll.
        // checkCostWarn (median warn), checkRunawayCeilingWarn (ceiling warn-only).
        // checkRunaway (progress kill) intentionally not called here — the 30s
        // ticker is the right cadence for the progress-window evaluation.
        checkCostWarn();
        checkRunawayCeilingWarn();
      }

      // Live in-stage token/cost estimate (#233). `assistant` messages carry a
      // growing-context usage snapshot exposed as `incrementalUsage` (NOT
      // `usage`), so it feeds the SEPARATE liveEstimator and never
      // tokenAccumulator — the authoritative terminal `result` total above is
      // untouched. Throttled to >=5s so a chatty stage can't spam the platform
      // ingest with progress events.
      if (parsed?.incrementalUsage) {
        liveEstimator.observe(parsed.incrementalUsage);
        const nowMs = Date.now();
        if (nowMs - lastProgressEmitMs >= PROGRESS_EMIT_CADENCE_MS) {
          lastProgressEmitMs = nowMs;
          callbacks?.onStageProgress?.(liveEstimator.estimate());
        }
        // Live budget/ceiling enforcement (#254): feed the same estimate into
        // the enforcement path on its own throttled cadence. The terminal
        // `result` envelope (onTokenUsage above) stays authoritative for booked
        // cost — this snapshot only drives mid-stage threshold evaluation.
        if (shouldEmitSnapshot(nowMs, lastBudgetEvalMs, budgetEvalCadenceMs)) {
          lastBudgetEvalMs = nowMs;
          callbacks?.onCostSnapshot?.(liveEstimator.estimate());
        }
      }

      // Capture session_id from result messages for resumption (Issue #118)
      if (parsed?.sessionId) {
        capturedSessionId = parsed.sessionId;
        // Update the handle with session_id for external access
        handle.sessionId = capturedSessionId;
        callbacks?.onSessionId?.(capturedSessionId);
      }

      // Loop detection for AskUserQuestion in headless mode (Issue #218)
      // Detects when Claude repeatedly attempts the same blocked tool call
      if (parsed?.toolName) {
        const inputHash = JSON.stringify(parsed.toolInput ?? {}).slice(0, 100);

        if (parsed.toolName === "AskUserQuestion") {
          // Flag any AskUserQuestion attempt in headless mode (Issue #697)
          promptDetected = true;

          if (lastToolCall?.name === "AskUserQuestion" && lastToolCall?.inputHash === inputHash) {
            consecutiveAttempts++;

            if (consecutiveAttempts >= MAX_CONSECUTIVE_ATTEMPTS) {
              const error = new Error(
                `Stage aborted: Claude attempted AskUserQuestion ${consecutiveAttempts} times. ` +
                  `AskUserQuestion is not supported in headless pipeline mode.`
              );

              callbacks?.onStderr?.(`[skillRunner] Loop detected: ${error.message}\n`);
              callbacks?.onError?.(error);
              proc.kill("SIGTERM");
              activeProcesses.delete(processKey);
              return;
            }
          } else {
            consecutiveAttempts = 1;
          }

          lastToolCall = { name: "AskUserQuestion", inputHash };
        } else {
          // Reset on other tool calls
          lastToolCall = null;
          consecutiveAttempts = 0;
        }
      }

      // Detect tool_use blocks for interactive tools like AskUserQuestion
      // The toolName and toolInput are extracted by parseStreamJsonLine
      if (parsed?.toolName) {
        // Extract tool_use ID from the raw JSON for matching tool_result
        let toolUseId: string | undefined;
        try {
          const rawParsed = JSON.parse(line.trim());
          if (rawParsed.content_block?.id) {
            toolUseId = rawParsed.content_block.id;
          }
        } catch {
          // Ignore parse errors for ID extraction
        }

        callbacks?.onToolUse?.(parsed.toolName, parsed.toolInput, toolUseId);

        // Fire onToolCall for Dashboard tool call recording (Issue #639, #1031)
        callbacks?.onToolCall?.(parsed.toolName, parsed.toolInput, toolUseId);

        // Progress-based runaway classification (file_change / commit /
        // distinct_tool) is now centralized in recordToolCallProgress and
        // driven above for BOTH the `content_block_start` (singular toolName)
        // and complete-`assistant`-message (plural toolUses[]) shapes — see
        // Issue #295. It is NOT duplicated here: this block runs only for the
        // singular `content_block_start` shape, which recordToolCallProgress
        // already classifies, so recording again would double-count the churn
        // gauge for that shape.

        // Deterministic phase inference (Issue #3760): for stages that don't
        // reliably emit phase markers (feature-dev), advance phase progress from
        // the tool calls the agent actually makes. No-op for self-reporting
        // stages; monotonic; real markers always take precedence.
        const inferred = phaseInference.observeToolUse(parsed.toolName, parsed.toolInput);
        if (inferred) {
          lastPhaseName = inferred.name;
          callbacks?.onPhaseStart?.(stage, inferred.name, inferred.index, inferred.total);
        }

        // Stage-exit diagnostic capture (Issue #3605): record the most recent
        // Bash command (truncated to 500 chars) so retros can answer "what was
        // it doing when it died?" without grepping the full session log. The
        // matching tool_result's `is_error` flag flips lastBashExit so we know
        // whether the command actually failed before the stage ended.
        if (parsed.toolName === "Bash") {
          const cmd = (parsed.toolInput as { command?: unknown } | undefined)?.command;
          if (typeof cmd === "string") {
            lastBashCommand = cmd.length > 500 ? cmd.slice(0, 500) + "…" : cmd;
          }
          pendingBashToolUseId = toolUseId;
          // Reset the matching exit code until the tool_result lands.
          lastBashExit = undefined;
        }
      }

      // Detect tool_result in user messages for telemetry backfill (Issue #1031)
      if (parsed?.toolResult) {
        lastToolResultId = parsed.toolResult.toolUseId; // track for stall diagnostic (#3484)
        callbacks?.onToolResult?.(
          parsed.toolResult.toolUseId,
          parsed.toolResult.content,
          parsed.toolResult.isError
        );

        // Stage-exit diagnostic capture (Issue #3605): when the tool_result
        // belongs to the most recent Bash tool_use we tracked, flip
        // lastBashExit. We don't have the literal exit code from the CLI's
        // tool_result envelope, but we can distinguish success (0) from
        // failure (1) via the isError flag — sufficient resolution to
        // diagnose "stage exited mid-Bash, last bash command failed."
        if (pendingBashToolUseId && parsed.toolResult.toolUseId === pendingBashToolUseId) {
          lastBashExit = parsed.toolResult.isError ? 1 : 0;
          pendingBashToolUseId = undefined;
        }
      }

      // Stage-exit diagnostic capture (Issue #3605): detect stop-hook errors
      // in the stream so the daily exit-record carries the boolean. The
      // notification format ({"type":"notification","notification":{"key":
      // "stop-hook-error"}}) is not part of StreamJsonMessageType — we parse
      // the raw line directly because that union is a hot-path constraint
      // that shouldn't grow for every diagnostic add.
      if (!stopHookErrored && line.includes("stop-hook-error")) {
        try {
          const raw = JSON.parse(line.trim()) as {
            type?: string;
            notification?: { key?: string };
          };
          if (raw.type === "notification" && raw.notification?.key === "stop-hook-error") {
            stopHookErrored = true;
          }
        } catch {
          // best-effort — malformed lines never block the record
        }
      }

      // Detect rate_limit_event and notify callback (Issue #2573)
      if (parsed?.type === "rate_limit_event" && parsed.rateLimitEvent) {
        lastRateLimitEvent = parsed.rateLimitEvent;
        // Record arrival timestamp so the stall ticker (Issue #3425) can
        // measure idle time accumulated AFTER the quota-exhausted signal,
        // independently of `lastChunkAtMs` (which we just refreshed by
        // virtue of receiving this stdout chunk a few lines up at 3104).
        lastRateLimitEventAtMs = Date.now();
        const waitMs = calculateRateLimitWait(parsed.rateLimitEvent.resetsAt);
        callbacks?.onRateLimitEvent?.({
          ...parsed.rateLimitEvent,
          waitMs,
        });
      }
    }
  });

  // Handle stderr — tail-cap to prevent unbounded growth when Claude CLI
  // runs with --debug/--debug-to-stderr. stderrBuffer is only consumed post-close
  // for error display and keyword classification, both of which only need the tail.
  proc.stderr?.on("data", (data: Buffer) => {
    const text = data.toString();
    stderrBuffer = appendTail(stderrBuffer, text, OUTPUT_ERROR_TAIL_MAX_CHARS);
    // Issue #3605 — separate 4 KB ring purpose-built for the daily exit-record
    // stderr_tail field. The wider OUTPUT_ERROR_TAIL_MAX_CHARS (50 KB) ring
    // already serves the V3 record's last_output_lines; using the narrower
    // ring here keeps the on-disk exit-record compact.
    exitStderrTail = appendTail(exitStderrTail, text, STAGE_EXIT_STDERR_TAIL_MAX);
    callbacks?.onStderr?.(text);
    lastChunkAtMs = Date.now();

    // Accumulate for stall diagnostic (Issue #2871), capped at 50KB
    if (diagStderrBuffer.length < STALL_DIAG_BUFFER_MAX) {
      diagStderrBuffer += text.slice(0, STALL_DIAG_BUFFER_MAX - diagStderrBuffer.length);
    }
  });

  // Handle process completion
  proc.on("close", (exitCode) => {
    stageCompleted = true;
    clearStallTicker();
    // Drain the lifecycle trace recorder's append chain (fail-open, #180).
    void traceRecorder.flush();
    if (stallWarningShown) {
      callbacks?.onStallWarningClear?.();
    }
    // Strip the Codex AGENTS.md managed block now the stage is done, so the
    // ephemeral steering never lands in a commit (AGENTS.md is a committed file;
    // user content outside the markers is preserved). Mirrors GEMINI.md's
    // generate→use→cleanup lifecycle. Issue #4028.
    if (adapter === "codex") {
      try {
        new CodexContextGenerator().cleanupSync(workspaceRoot);
      } catch {
        /* best-effort */
      }
    }
    // Orphan reaper: kill any descendants that outlived the parent. Tracked
    // pids were captured periodically while the parent was alive, so we can
    // still reach them after they've been reparented to init (#781 retro).
    try {
      descendantTracker.killSurvivors(callbacks?.onStderr);
    } catch {
      /* best-effort */
    }
    // Process any remaining buffered output
    if (stdoutBuffer) {
      const parsed = parseStreamJsonLine(stdoutBuffer);
      observeServedModel(parsed);
      if (parsed?.usage) {
        tokenAccumulator.add(parsed.usage);
        callbacks?.onTokenUsage?.(tokenAccumulator.getTotal());
      }
      // Capture session_id from final buffered output
      if (parsed?.sessionId && !capturedSessionId) {
        capturedSessionId = parsed.sessionId;
        handle.sessionId = capturedSessionId;
        callbacks?.onSessionId?.(capturedSessionId);
      }
    }

    const success = exitCode === 0;
    const stderrText = stderrBuffer.trim();
    const stdoutText = appendTail(stdoutRawTail, stdoutBuffer, OUTPUT_ERROR_TAIL_MAX_CHARS).trim();

    // Safety-net fallback (Issue #2919): if the streaming line parser missed the
    // CLI's terminal `type:"result"` envelope but it did make it into the raw
    // stdout tail, rescue the usage block here. Prevents slot badges from
    // showing $0.00 / 0 tokens on successful runs where stream-json framing
    // raced the close event or the envelope was wrapped in unexpected output.
    if (success && !tokenAccumulator.hasTokens() && stdoutText.length > 0) {
      const rescued = extractTokenUsage(stdoutText);
      if (rescued) {
        tokenAccumulator.add(rescued);
        callbacks?.onTokenUsage?.(tokenAccumulator.getTotal());
        callbacks?.onStderr?.(
          `[skillRunner] Recovered token usage from stdout tail (fallback path). ` +
            `Stage ${stage} would have reported zero tokens otherwise.\n`
        );
      } else {
        // Exit was clean but no usage found anywhere — surface diagnostic for
        // future debugging rather than silently reporting zeros.
        const tailSnippet = stdoutText.slice(-500).replace(/\s+/g, " ");
        callbacks?.onStderr?.(
          `[skillRunner] WARNING: stage ${stage} exited 0 but no token usage captured. ` +
            `Last 500 chars of stdout: ${tailSnippet}\n`
        );
      }
    }

    const inferredError = inferProcessError(success, stderrText, stdoutText, exitCode);

    // Cleanup GEMINI.md after stage completion (Issue #1055)
    if (adapter === "gemini" || adapter === "gemini-sdk") {
      try {
        fs.unlinkSync(path.join(workspaceRoot, "GEMINI.md"));
      } catch {
        // File may already be gone — ignore
      }
    }

    // Classify error from stderr when process failed (Issue #2573)
    let errorCategory: ErrorCategory | undefined;
    let retryAfterMs: number | undefined;
    if (!success) {
      // If a rate_limit_event was detected during execution, use that directly
      if (lastRateLimitEvent && isHardRateLimit(lastRateLimitEvent.status)) {
        errorCategory = "rate_limit";
        retryAfterMs = calculateRateLimitWait(lastRateLimitEvent.resetsAt);
      } else {
        // Fall back to stderr keyword detection
        errorCategory = classifyError(stderrBuffer);
      }
    }

    // Snapshot trailing output for the Go scheduler's V3 RunRecord on
    // terminal failure (Issue #3207). Prefers stderr (where the stall-kill
    // marker was just appended); falls back to stdout when stderr is empty.
    let lastOutputLines: string | undefined;
    if (!success) {
      const tail = stderrText.length > 0 ? stderrText : stdoutText;
      if (tail.length > 0) {
        // Cap at ≤200 lines (matches the Go runtime ring buffer convention).
        const lines = tail.split("\n");
        lastOutputLines = lines.slice(-200).join("\n");
      }
    }

    // Book the stage's cost/tokens (#296). Normal path: the authoritative
    // TokenAccumulator, reconciled from the terminal `result` envelope. Kill
    // path: a SIGTERM'd CLI (runaway / stall / budget / user cancel) never
    // emits that envelope, so fall back to the LiveStageEstimator's last
    // in-stage snapshot — otherwise the killed stage books $0 while the kill
    // log reported the real burn (bowlsheet #262). One shared decision
    // (`resolveStageBookedUsage`) serves every kill site because they all
    // funnel the subprocess to this single close handler; `estimated`
    // distinguishes the fallback so downstream can weight it accordingly.
    const bookedUsage = resolveStageBookedUsage(tokenAccumulator, liveEstimator);
    if (bookedUsage?.estimated) {
      callbacks?.onStderr?.(
        `[skillRunner] Stage ${stage}: no terminal result envelope (process ended mid-stage); ` +
          `booking the live cost estimate $${bookedUsage.usage.costUsd.toFixed(4)} ` +
          `as the stage cost so the killed stage's real burn is recorded (#296).\n`
      );
    }
    activeProcesses.delete(processKey);
    callbacks?.onComplete?.({
      success,
      exitCode,
      error: inferredError,
      tokenUsage: bookedUsage?.usage,
      costEstimated: bookedUsage?.estimated || undefined,
      sessionId: capturedSessionId,
      promptDetected,
      modelDecision,
      adapterDecision: {
        adapter,
        source: adapterSource,
        // Issue #3231 — propagate the fallback audit trail when the walker
        // ran (length ≥ 2). Omitted on the common path where the primary
        // succeeded so per-stage history records stay terse.
        ...(chainUsed.length >= 2 ? { adapterFallbackChainUsed: chainUsed } : {}),
      },
      stallKilled: stallKilled || undefined,
      stallAborted: (stallPauseResolved && stallKilled) || undefined,
      costCapExceeded: costCapExceeded || undefined,
      // #266: `costCapExceeded` is now set ONLY by the progress-based runaway
      // kill (`checkRunaway`) — the $75 dollar runaway ceiling was demoted to
      // warn-only in #3783 and never sets this flag. A progress kill crosses
      // no dollar ceiling, so `costCapUsd` MUST stay undefined here; forwarding
      // `runwayCeilingUsd` misattributed the kill as "cost exceeded ceiling
      // ($75)" even when the cost was a fraction of it. The authoritative
      // figure is the cost captured at termination.
      costCapUsd: undefined,
      costAtTerminationUsd: costCapExceeded ? costAtTerminationUsd : undefined,
      costWarnFired: costWarnFired || undefined,
      errorCategory,
      retryAfterMs,
      stallEvents: stallEvents.length > 0 ? stallEvents : undefined,
      lastOutputLines,
      // ── Issue #3605 stage-exit diagnostic fields ───────────────────────
      // Always-populated where we know the value; otherwise undefined so
      // the JSON-RPC layer drops it. The TS layer captures signal/source
      // at kill delivery time, idle/elapsed at the close handler from the
      // shared activity heartbeat, and last-Bash + stop-hook flag from
      // the stream parser above. The Go scheduler treats every field as
      // optional — empty values yield a (still valid) terser daily record.
      signal: exitSignal || undefined,
      signalSource: exitSignalSource || undefined,
      elapsedMs: Date.now() - startedAtMs,
      idleMsAtExit: Date.now() - lastChunkAtMs,
      cacheCreationTokens: bookedUsage?.usage.cacheCreationTokens,
      lastBashCommand: lastBashCommand || undefined,
      lastBashExit,
      stopHookErrored: stopHookErrored || undefined,
      stderrTail: exitStderrTail || undefined,
      // ── #91 served-model attribution ───────────────────────────────────
      // Only forwarded when it diverges from the requested model, so alias
      // canonicalization (e.g. "opus" → "claude-opus-4-8") still flows but
      // healthy records stay terse when the stream reported nothing new.
      servedModel: servedModel && servedModel !== modelDecision.model ? servedModel : undefined,
      modelRefusalFallback,
    });
  });

  // Handle process error
  proc.on("error", (error) => {
    stageCompleted = true;
    clearStallTicker();
    activeProcesses.delete(processKey);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({
      success: false,
      exitCode: null,
      error,
    });
  });

  const handle: SkillProcessHandle = {
    process: proc,
    stage,
    issueNumber,
    kill: () => {
      clearStallTicker();
      proc.kill("SIGTERM");
      // SIGKILL fallback after 5 seconds (Issue #835) — also tree-kill so
      // a hung child (e.g. vitest with a leaked Redis subscriber) can't
      // outlive the parent and re-trigger the same hang on retry (#781 retro).
      const killTimer = setTimeout(() => {
        try {
          proc.kill("SIGKILL");
        } catch {
          /* already dead */
        }
        if (proc.pid) {
          void killProcessTree(proc.pid, "SIGKILL", callbacks?.onStderr);
        }
      }, 5000);
      proc.on("exit", () => clearTimeout(killTimer));
      activeProcesses.delete(processKey);
    },
    // Issue #3851: expose the productive-progress signal so the orchestrator's
    // unattended budget/ceiling escalation can gate on real progress.
    getProductiveProgressDelta: () => progressMonitor.getProductiveProgressDelta(),
  };

  activeProcesses.set(processKey, handle);
  return handle;
}

/**
 * Fallback: Run a pipeline stage skill via terminal
 *
 * Uses interactive terminal mode as a fallback when headless doesn't work.
 *
 * @param stage - The pipeline stage to run
 * @param issueNumber - The issue number
 * @param _callbacks - Not used in terminal mode
 * @returns The terminal instance
 */
export function runStageSkill(
  stage: PipelineStage,
  issueNumber?: number,
  _callbacks?: SkillRunCallbacks
): vscode.Terminal {
  const skillDir = STAGE_TO_SKILL_DIR[stage];

  // Build the command
  let command = `claude /nightgauge:${skillDir.replace("nightgauge-", "")}`;
  if (issueNumber && stage === "issue-pickup") {
    command += ` ${issueNumber}`;
  }

  const terminalName = issueNumber ? `Nightgauge: Issue #${issueNumber}` : "Nightgauge Pipeline";

  // Try to find an existing Nightgauge terminal
  let terminal = vscode.window.terminals.find((t) => t.name.startsWith("Nightgauge:"));

  if (!terminal) {
    terminal = vscode.window.createTerminal({
      name: terminalName,
      cwd: vscode.workspace.workspaceFolders?.[0]?.uri.fsPath,
    });
  }

  terminal.show(true);
  terminal.sendText(command);

  return terminal;
}

/**
 * Kill all active skill processes
 */
export function killAllActiveProcesses(): void {
  for (const handle of activeProcesses.values()) {
    handle.kill();
  }
  activeProcesses.clear();
}

/**
 * Check if a stage is currently running
 */
export function isStageRunning(stage: PipelineStage, issueNumber?: number): boolean {
  const processKey = `${stage}-${issueNumber ?? "no-issue"}`;
  return activeProcesses.has(processKey);
}

/**
 * Get the active process for a stage
 */
export function getActiveProcess(
  stage: PipelineStage,
  issueNumber?: number
): SkillProcessHandle | undefined {
  const processKey = `${stage}-${issueNumber ?? "no-issue"}`;
  return activeProcesses.get(processKey);
}

/**
 * Resume a conversation to send AskUserQuestion response (Issue #118)
 *
 * Uses `claude -p --resume <session_id>` to continue the conversation
 * after AskUserQuestion tool was invoked. This spawns a NEW process
 * rather than trying to write to the original process's stdin.
 *
 * @param sessionId - Session ID from the original process
 * @param response - User's response to send
 * @param callbacks - Callbacks for streaming output
 * @returns Handle to control the resume process
 */
export function resumeSessionWithResponse(
  sessionId: string,
  response: string,
  callbacks?: SkillRunCallbacks
): SkillProcessHandle {
  const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;

  if (!workspaceRoot) {
    const error = new Error("No workspace folder open");
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });

    return {
      process: null as unknown as ChildProcess,
      stage: "issue-pickup", // Placeholder - will be updated by caller
      kill: () => {},
    };
  }

  callbacks?.onStderr?.(`[skillRunner] Resuming session: ${sessionId}\n`);
  callbacks?.onStderr?.(`[skillRunner] Response: ${response}\n`);

  // Build CLI arguments for resuming
  const args = [
    "-p",
    "--no-session-persistence",
    "--output-format",
    "stream-json",
    "--verbose",
    "--resume",
    sessionId,
  ];

  // Add backend-specific flag based on auth_provider config
  const authProvider = getAuthProvider(workspaceRoot);
  if (authProvider === "bedrock") {
    args.push("--bedrock");
    callbacks?.onStderr?.("[skillRunner] Using AWS Bedrock backend\n");
  } else if (authProvider === "vertex") {
    args.push("--vertex");
    callbacks?.onStderr?.("[skillRunner] Using Google Vertex AI backend\n");
  }

  // Pass --model from config for resume sessions (Issue #626)
  // Always pass --model explicitly — CLI default may differ from expected 'sonnet'
  const defaultModel = getDefaultModel(workspaceRoot);
  if (defaultModel) {
    args.push("--model", defaultModel);
  }

  // Pass --fallback-model from config for resume sessions (Issue #626). When the
  // resumed run is on Fable, default the fallback to Opus (separate Max-plan
  // bucket) so a Fable usage-limit degrades gracefully.
  const fallbackModel = resolveFallbackModel(defaultModel, getFallbackModel(workspaceRoot));
  if (fallbackModel) {
    args.push("--fallback-model", fallbackModel);
  }

  // Spawn Claude CLI with stdin pipe
  const proc = spawn("claude", args, {
    cwd: workspaceRoot,
    shell: false,
    stdio: ["pipe", "pipe", "pipe"],
    env: {
      ...process.env,
      CI: "true",
    },
  });

  // Send the response and close stdin
  if (proc.stdin) {
    proc.stdin.write(response);
    proc.stdin.end();
  }

  const processKey = `resume-${sessionId}`;

  // Token accumulator for this resume.
  // Issue #3228: resume sessions are Claude-only; pass adapter='claude' and
  // the resolved default model so getTotal() emits a costSource label even
  // though the native path is the only branch that fires here.
  const tokenAccumulator = new TokenAccumulator("claude", defaultModel ?? "sonnet");
  // Live in-stage estimator for the resume path (#233), mirroring the main
  // stage path — fed only by `assistant` incrementalUsage, reconciled by the
  // terminal `result` total. See LiveStageEstimator's class doc.
  const liveEstimator = new LiveStageEstimator("claude", defaultModel ?? "sonnet");
  const PROGRESS_EMIT_CADENCE_MS = 5000;
  let lastProgressEmitMs = -PROGRESS_EMIT_CADENCE_MS;
  let stdoutBuffer = "";
  let capturedSessionId: string | undefined;

  // Served-model attribution (#91) — a resumed session can hit the CLI's
  // refusal fallback too; track it the same way as the main stage path.
  let servedModel: string | undefined;
  let modelRefusalFallback: SkillRunResult["modelRefusalFallback"];
  const observeServedModel = (parsed: ReturnType<typeof parseStreamJsonLine>): void => {
    if (!parsed) return;
    if (parsed.modelRefusalFallback && !modelRefusalFallback) {
      modelRefusalFallback = parsed.modelRefusalFallback;
      callbacks?.onStderr?.(
        `[skillRunner] claude CLI model_refusal_fallback (resume): ${parsed.modelRefusalFallback.originalModel} → ` +
          `${parsed.modelRefusalFallback.fallbackModel} (category ${parsed.modelRefusalFallback.category ?? "unknown"}) (#91)\n`
      );
    }
    if (parsed.model && parsed.model !== servedModel) {
      servedModel = parsed.model;
      tokenAccumulator.setModel(parsed.model);
      liveEstimator.setModel(parsed.model);
    }
  };

  // Handle stdout
  proc.stdout?.on("data", (data: Buffer) => {
    const text = data.toString();
    callbacks?.onStdout?.(text);

    stdoutBuffer += text;
    const lines = stdoutBuffer.split("\n");
    stdoutBuffer = lines.pop() ?? "";

    for (const line of lines) {
      const parsed = parseStreamJsonLine(line);
      observeServedModel(parsed);
      if (parsed?.usage) {
        tokenAccumulator.add(parsed.usage);
        callbacks?.onTokenUsage?.(tokenAccumulator.getTotal());
      }
      // Live in-stage estimate (#233) — same separation as the main path:
      // assistant incrementalUsage feeds the estimator only, throttled >=5s.
      if (parsed?.incrementalUsage) {
        liveEstimator.observe(parsed.incrementalUsage);
        const nowMs = Date.now();
        if (nowMs - lastProgressEmitMs >= PROGRESS_EMIT_CADENCE_MS) {
          lastProgressEmitMs = nowMs;
          callbacks?.onStageProgress?.(liveEstimator.estimate());
        }
      }
      if (parsed?.sessionId) {
        capturedSessionId = parsed.sessionId;
        handle.sessionId = capturedSessionId;
        callbacks?.onSessionId?.(capturedSessionId);
      }
      if (parsed?.toolName && callbacks?.onToolUse) {
        let toolUseId: string | undefined;
        try {
          const rawParsed = JSON.parse(line.trim());
          if (rawParsed.content_block?.id) {
            toolUseId = rawParsed.content_block.id;
          }
        } catch {
          // Ignore parse errors
        }
        callbacks.onToolUse(parsed.toolName, parsed.toolInput, toolUseId);
      }
    }
  });

  // Handle stderr
  proc.stderr?.on("data", (data: Buffer) => {
    callbacks?.onStderr?.(data.toString());
  });

  // Handle completion
  proc.on("close", (exitCode) => {
    if (stdoutBuffer) {
      const parsed = parseStreamJsonLine(stdoutBuffer);
      observeServedModel(parsed);
      if (parsed?.usage) {
        tokenAccumulator.add(parsed.usage);
        callbacks?.onTokenUsage?.(tokenAccumulator.getTotal());
      }
      if (parsed?.sessionId && !capturedSessionId) {
        capturedSessionId = parsed.sessionId;
        handle.sessionId = capturedSessionId;
        callbacks?.onSessionId?.(capturedSessionId);
      }
    }

    // Same booking decision as the main stage path (#296): fall back to the
    // live estimate when a mid-flight kill left the accumulator empty, so a
    // resumed session that is cancelled still books its real burn.
    const bookedUsage = resolveStageBookedUsage(tokenAccumulator, liveEstimator);
    activeProcesses.delete(processKey);
    callbacks?.onComplete?.({
      success: exitCode === 0,
      exitCode,
      tokenUsage: bookedUsage?.usage,
      costEstimated: bookedUsage?.estimated || undefined,
      sessionId: capturedSessionId,
      // #91 served-model attribution (diverged-only, like the stage path).
      servedModel: servedModel && servedModel !== defaultModel ? servedModel : undefined,
      modelRefusalFallback,
    });
  });

  // Handle error
  proc.on("error", (error) => {
    activeProcesses.delete(processKey);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({
      success: false,
      exitCode: null,
      error,
    });
  });

  const handle: SkillProcessHandle = {
    process: proc,
    stage: "issue-pickup", // Placeholder - caller should update
    kill: () => {
      proc.kill("SIGTERM");
      activeProcesses.delete(processKey);
    },
  };

  activeProcesses.set(processKey, handle);
  return handle;
}

/**
 * Get session ID from the most recently completed or active process
 * Used to find the session to resume for AskUserQuestion
 */
export function getLastSessionId(): string | undefined {
  // Check active processes first
  for (const handle of activeProcesses.values()) {
    if (handle.sessionId) {
      return handle.sessionId;
    }
  }
  return undefined;
}

/**
 * Check if any process is currently running
 */
export function hasActiveProcess(): boolean {
  return activeProcesses.size > 0;
}

/**
 * Get the active interactive process handle (Issue #497)
 *
 * Returns the currently active interactive process handle if one exists.
 * Interactive processes have open stdin and support mid-execution user input.
 *
 * @returns The active interactive process handle, or null if no interactive process is running
 *
 * @example
 * ```typescript
 * const handle = getActiveInteractiveProcess();
 * if (handle) {
 *   handle.writeToStdin?.("Hello from user");
 * }
 * ```
 *
 * @see runStageSkillInteractive
 */
export function getActiveInteractiveProcess(): SkillProcessHandle | null {
  for (const handle of activeProcesses.values()) {
    if (handle.isInteractive) {
      return handle;
    }
  }
  return null;
}

/**
 * Send input to active process (DEPRECATED - use resumeSessionWithResponse)
 *
 * NOTE: With Claude CLI `-p` mode, stdin must be closed (stdin.end()) for
 * the process to start. This means mid-execution messaging is not supported
 * via stdin. Use `resumeSessionWithResponse()` for AskUserQuestion flows
 * which spawns a new process with `--resume`.
 *
 * This function exists for backward compatibility and will always return
 * false since stdin is closed immediately after the initial prompt.
 *
 * @deprecated Use resumeSessionWithResponse() for AskUserQuestion responses
 */
export function sendInputToActiveProcess(input: string): boolean {
  console.warn(
    "[skillRunner] sendInputToActiveProcess is deprecated. " +
      "Use resumeSessionWithResponse() with session_id for follow-up messages. " +
      `Attempted input: "${input.substring(0, 50)}..."`
  );

  // Attempt to write anyway - this will fail if stdin is closed
  for (const handle of activeProcesses.values()) {
    if (handle.process && handle.process.stdin && !handle.process.stdin.destroyed) {
      try {
        handle.process.stdin.write(input + "\n");
        return true;
      } catch {
        return false;
      }
    }
  }
  return false;
}

/**
 * Run a pipeline stage skill via Claude Code CLI in interactive mode.
 *
 * Unlike headless mode, interactive mode:
 * - Does NOT use the -p flag
 * - Does NOT close stdin after writing the prompt
 * - Does NOT produce stream-json output (no token tracking)
 * - Supports mid-execution user input via writeToInteractiveProcess()
 *
 * Interactive mode is intended for single-stage exploratory execution where
 * the user may want to ask questions or provide clarifications mid-execution.
 *
 * **IMPORTANT**: Interactive mode is NOT supported for:
 * - Multi-stage pipelines (use headless mode)
 * - Batch processing (use headless mode)
 *
 * @param stage - The pipeline stage to run
 * @param issueNumber - The issue number (required for most stages)
 * @param callbacks - Callbacks for streaming output (note: onTokenUsage will not fire)
 * @returns Handle to control the process, with writeToStdin for follow-up messages
 *
 * @see runStageSkillHeadless for headless mode (automated pipelines)
 * @see docs/INTERACTIVE_MODE.md for architecture
 * @see Issue #495
 */
/**
 * Build the shell command that launches the Codex TUI seeded with a stage
 * prompt (#4024). The prompt is base64 in `promptB64File`; it is decoded
 * in-shell into a single positional argument. Because base64 output and the
 * UUID temp-file path are both shell-safe, a markdown prompt containing
 * backticks, `$`, or quotes cannot break out of the argument. `openssl base64
 * -d -A` is used as the decoder (present on macOS/BSD and Linux/GNU; `-A`
 * treats the whole file as a single base64 stream).
 *
 * Exported for unit testing the quote-safe seeding.
 */
/**
 * A single safe shell token: command names, absolute paths, and model ids only.
 * Anything with whitespace or shell metacharacters (`;`, `|`, `$`, backtick,
 * `&`, quotes, …) is rejected so a malicious `.nightgauge/config.yaml`
 * (this tool runs on cloned repos) cannot inject commands into the launch
 * string. (#4024 — commit security review)
 */
const CODEX_SAFE_TOKEN = /^[A-Za-z0-9._/-]+$/;

export function buildCodexInteractiveLaunchCommand(
  codexCmd: string,
  model: string | undefined,
  promptB64File: string
): string {
  // codexCmd is interpolated into a shell command — refuse anything that isn't a
  // single safe token (command name or absolute path).
  if (!CODEX_SAFE_TOKEN.test(codexCmd)) {
    throw new Error(
      `Unsafe Codex CLI command "${codexCmd}" — only a single command name or ` +
        "absolute path (no spaces or shell metacharacters) is allowed."
    );
  }
  // model is already validated against the closed Codex model set upstream; this
  // is defense-in-depth — drop a malformed value rather than interpolate it.
  const modelFlag = model && CODEX_SAFE_TOKEN.test(model) ? `--model ${model} ` : "";
  // 1. Decode the base64 prompt into a shell variable, then `rm` the temp file
  //    immediately — the file's lifetime is tied to the decode, with no
  //    host-side timer that could race a slow shell (#4024 review #2).
  // 2. Launch the Codex TUI with the prompt as a single quoted argument (`"$P"`
  //    is quote-safe regardless of prompt content).
  // 3. `; exit` so the terminal closes when Codex exits, giving the host a
  //    completion signal via onDidCloseTerminal rather than waiting for the user
  //    to close the pane manually (#4024 review #1).
  return (
    `P="$(openssl base64 -d -A -in '${promptB64File}')"; ` +
    `rm -f '${promptB64File}'; ` +
    `${codexCmd} ${modelFlag}"$P"; exit`
  );
}

/**
 * Launch the Codex interactive TUI for a stage in a VSCode terminal (#4024).
 *
 * Codex's interactive mode is a full-screen terminal UI, so — unlike Claude's
 * piped-stdio interactive path — it must run in a real terminal where the user
 * drives the conversation directly. The assembled stage prompt seeds the first
 * turn. AGENTS.md steering (#4028) and MCP servers (#4025) are provisioned first
 * so the interactive session has the same context a headless Codex stage gets.
 *
 * The prompt is delivered quote-safely: a markdown prompt contains backticks,
 * `$`, and quotes that would break a raw shell argument, so it is base64-encoded
 * to a temp file and decoded in-shell (`openssl base64 -d` — present on macOS
 * and Linux). The temp file path is a UUID, so it is itself shell-safe.
 */
function launchCodexInteractiveTerminal(
  stage: PipelineStage,
  issueNumber: number | undefined,
  prompt: string,
  workspaceRoot: string,
  callbacks?: SkillRunCallbacks
): SkillProcessHandle {
  // Namespace the key like the Claude interactive path so an interactive Codex
  // run and a headless run of the same stage+issue can't clobber each other's
  // activeProcesses registration (#4024 review #4).
  const processKey = `interactive-${stage}-${issueNumber ?? "no-issue"}`;
  const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

  // Validate the configured Codex CLI command BEFORE any side effects: it is
  // interpolated into the shell launch string, and `.nightgauge/config.yaml`
  // is untrusted on a cloned repo, so an unsafe value must abort the launch
  // rather than inject commands. (#4024 — commit security review)
  const codexCmd = getCodexCliCommand(workspaceRoot);
  if (!CODEX_SAFE_TOKEN.test(codexCmd)) {
    const error = new Error(
      `Refusing to launch Codex: configured codex CLI command "${codexCmd}" is not a ` +
        "single safe token (command name or absolute path). Fix codex.cli_command in config."
    );
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });
    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
      isInteractive: true,
    };
  }

  // Resolve + validate the Codex model (#4021 parity). A bad configured model
  // throws (closed policy) — fall back to Codex's own default rather than
  // blocking the interactive launch.
  let model: string | undefined;
  try {
    const validated = validateModelForAdapter("codex", getCodexModel(workspaceRoot)).model;
    if (validated) {
      model = validated;
      callbacks?.onStderr?.(`[skillRunner] Codex model: ${validated}\n`);
    }
  } catch (e) {
    callbacks?.onStderr?.(
      `[skillRunner] Codex model validation failed (${errText(e)}); using Codex default\n`
    );
  }

  // Provision provider-aware steering + MCP so the interactive session matches a
  // headless Codex stage. Both self-guard by adapter and are best-effort.
  try {
    new CodexContextGenerator().generateSync({
      projectRoot: workspaceRoot,
      stage,
      issueNumber: issueNumber ?? 0,
      adapter: "codex",
    });
  } catch (e) {
    callbacks?.onStderr?.(`[skillRunner] Warning: AGENTS.md provisioning failed: ${errText(e)}\n`);
  }
  try {
    new CodexMcpProvisioner().provisionSync({ workspaceRoot, adapter: "codex" });
  } catch (e) {
    callbacks?.onStderr?.(`[skillRunner] Warning: Codex MCP provisioning failed: ${errText(e)}\n`);
  }

  // Seed the prompt quote-safely via a base64 temp file decoded in-shell.
  // (codexCmd was resolved + validated above, before any side effects.)
  const promptFile = path.join(os.tmpdir(), `codex-interactive-${randomUUID()}.b64`);
  try {
    fs.writeFileSync(promptFile, Buffer.from(prompt, "utf-8").toString("base64"), "utf-8");
  } catch (e) {
    const error = new Error(`Failed to write Codex interactive prompt file: ${errText(e)}`);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });
    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
      isInteractive: true,
    };
  }

  const terminalName = issueNumber ? `Codex: Issue #${issueNumber} (${stage})` : `Codex: ${stage}`;
  const terminal = vscode.window.createTerminal({ name: terminalName, cwd: workspaceRoot });
  const launchCmd = buildCodexInteractiveLaunchCommand(codexCmd, model, promptFile);

  callbacks?.onStderr?.(
    `[skillRunner] Stage: ${stage} (interactive) | Adapter: codex | Launching TUI in "${terminalName}"\n`
  );
  callbacks?.onStderr?.(`[skillRunner] (seed prompt file: ${promptFile})\n`);
  callbacks?.onMode?.("interactive");

  terminal.show(true);
  terminal.sendText(launchCmd);

  let completed = false;
  let closeListener: vscode.Disposable | undefined;
  // `aborted` distinguishes an explicit kill()/abort from a natural terminal
  // close: an aborted stage must report success:false, never completeStage
  // (#4024 review #7).
  const finish = (exitCode: number | null, aborted = false): void => {
    if (completed) return;
    completed = true;
    closeListener?.dispose();
    activeProcesses.delete(processKey);
    // Strip the AGENTS.md managed block now that the interactive session ended.
    try {
      new CodexContextGenerator().cleanupSync(workspaceRoot);
    } catch {
      /* best-effort */
    }
    // Backstop the in-shell `rm` (the prompt file is normally deleted by the
    // launch command itself) — covers a pane closed before the command ran.
    try {
      fs.unlinkSync(promptFile);
    } catch {
      /* already removed in-shell, or never written */
    }
    // Interactive mode is user-driven: a clean (0 / unknown) exit is success; a
    // non-zero exit, or an explicit abort, is a failure.
    callbacks?.onComplete?.({
      success: !aborted && (exitCode === 0 || exitCode === null),
      exitCode,
      error: aborted ? new Error("Codex interactive session aborted") : undefined,
    });
  };

  // The launch command ends in `; exit`, so the terminal closes when Codex
  // exits and this fires with Codex's exit code (#4024 review #1).
  closeListener = vscode.window.onDidCloseTerminal((closed) => {
    if (closed === terminal) {
      finish(closed.exitStatus?.code ?? null);
    }
  });

  const handle: SkillProcessHandle = {
    process: null as unknown as ChildProcess,
    stage,
    issueNumber,
    isInteractive: true,
    kill: () => {
      closeListener?.dispose();
      try {
        terminal.dispose();
      } catch {
        /* already disposed */
      }
      finish(null, true);
    },
    // The user types directly in the TUI; expose sendText so the OutputWindow
    // relay can still forward a line into the terminal when invoked.
    writeToStdin: (message: string): boolean => {
      try {
        terminal.sendText(message);
        return true;
      } catch {
        return false;
      }
    },
  };

  activeProcesses.set(processKey, handle);
  return handle;
}

export function runStageSkillInteractive(
  stage: PipelineStage,
  issueNumber?: number,
  callbacks?: SkillRunCallbacks
): SkillProcessHandle {
  let workspaceRoot: string | undefined;
  try {
    const contextLoader = RepositoryContextLoader.getInstance();
    if (contextLoader.getCurrentRepository()) {
      workspaceRoot = contextLoader.getWorkingDirectory();
    }
  } catch {
    // Context loader unavailable (e.g., test environment without full VSCode API)
  }
  workspaceRoot ??= vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;

  if (!workspaceRoot) {
    const error = new Error("No workspace folder open");
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });

    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
      isInteractive: true,
    };
  }

  // Find and read the SKILL.md file (reuse existing infrastructure)
  const skillPath = findSkillFile(stage);
  if (!skillPath) {
    const error = new Error(`SKILL.md not found for stage: ${stage}`);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });

    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
      isInteractive: true,
    };
  }

  const skillData = readSkillFile(skillPath);
  if (!skillData) {
    const error = new Error(`Failed to read SKILL.md at: ${skillPath}`);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });

    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
      isInteractive: true,
    };
  }

  // Build the prompt (reuse existing infrastructure)
  const prompt = buildSkillPrompt(stage, skillData.content, issueNumber, path.dirname(skillPath));

  // Interactive mode intentionally uses the global adapter lookup, not
  // `resolveStageAdapter`. Per-stage adapter routing (Issue #3223) is
  // headless-only because the user steers a single stage at a time
  // interactively and picks the adapter implicitly via "Switch Execution
  // Adapter". Lifting per-stage routing into interactive mode is a separate
  // issue if ever wanted; this asymmetry is by design.
  const adapter = getExecutionAdapter(workspaceRoot);
  const prereqError = validateAdapterPrerequisites(adapter, workspaceRoot, "interactive");
  if (prereqError) {
    const error = new Error(prereqError);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({ success: false, exitCode: null, error });
    return {
      process: null as unknown as ChildProcess,
      stage,
      issueNumber,
      kill: () => {},
      isInteractive: true,
    };
  }

  // Codex interactive mode launches the Codex TUI in a VSCode terminal seeded
  // with the stage prompt (#4024). Codex's interactive mode is a full-screen TUI
  // — unlike Claude's piped-stdio interactive — so it needs a real terminal and
  // the user drives the conversation directly there. Returns early on this path.
  if (adapter === "codex") {
    return launchCodexInteractiveTerminal(stage, issueNumber, prompt, workspaceRoot, callbacks);
  }

  // Estimate token count (~4 chars per token for English text)
  const interactiveTokens = Math.ceil(prompt.length / 4);
  const interactiveTokenLabel =
    interactiveTokens > 1000
      ? `${(interactiveTokens / 1000).toFixed(1)}K`
      : String(interactiveTokens);

  // Consolidated metadata line (Issue #795)
  callbacks?.onStderr?.(
    `[skillRunner] Stage: ${stage} (interactive) | Adapter: ${adapter} | Prompt: ~${interactiveTokenLabel} tokens\n`
  );

  // Emit interactive mode (Issue #496)
  callbacks?.onMode?.("interactive");

  // In interactive mode, AskUserQuestion IS allowed since stdin stays open
  // and the user can respond via writeToStdin()
  // Merge MCP tools: stage config overrides frontmatter; global merges in (Issue #1725, #1726)
  const interactiveConfigMcpTools = getMcpToolsConfig(workspaceRoot, stage);
  const interactiveStageMcpTools = getStageMcpTools(workspaceRoot, stage);
  const interactiveBaseMcpTools =
    interactiveStageMcpTools.length > 0 ? interactiveStageMcpTools : skillData.mcpTools;
  const interactiveMergedMcpTools = Array.from(
    new Set([...interactiveBaseMcpTools, ...interactiveConfigMcpTools])
  );
  const interactiveResolvedMcpTools = resolveMcpTools(interactiveMergedMcpTools, workspaceRoot);

  if (interactiveResolvedMcpTools.length > 0) {
    callbacks?.onStderr?.(`[skillRunner] MCP tools: ${interactiveResolvedMcpTools.join(", ")}\n`);
  }

  const allInteractiveAllowedTools = [...skillData.allowedTools, ...interactiveResolvedMcpTools];

  // Get auth provider from config (Issue #511)
  const authProvider = getAuthProvider(workspaceRoot);

  // Build CLI arguments for interactive mode
  // NO -p flag (keeps stdin open for conversation)
  // NO --output-format stream-json (raw text output)
  // NO --no-session-persistence (session can persist for multi-turn)
  const args = ["--verbose", "--allowedTools", allInteractiveAllowedTools.join(",")];

  // Add backend-specific flag based on auth_provider config (Issue #511)
  // Default behavior ('max') requires no additional flag
  if (authProvider === "bedrock") {
    args.push("--bedrock");
  } else if (authProvider === "vertex") {
    args.push("--vertex");
  }

  // Load auto-accept configuration (same as headless)
  const autoAcceptEnv = loadAutoAcceptConfigSync(workspaceRoot, stage);

  // Log optional config details
  if (authProvider !== "max") {
    callbacks?.onStderr?.(
      `[skillRunner] Using ${authProvider === "bedrock" ? "AWS Bedrock" : "Google Vertex AI"} backend\n`
    );
  }
  if (autoAcceptEnv.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS === "true") {
    callbacks?.onStderr?.("[skillRunner] Auto-accept permissions: ENABLED\n");
  }

  // Spawn Claude CLI WITHOUT -p flag for interactive mode
  const proc = spawn("claude", args, {
    cwd: workspaceRoot,
    shell: false,
    stdio: ["pipe", "pipe", "pipe"],
    env: {
      ...process.env,
      ...autoAcceptEnv,
    },
  });

  // Write the initial prompt to stdin
  // IMPORTANT: Do NOT call stdin.end() - keep it open for user input
  if (proc.stdin) {
    proc.stdin.write(prompt);
    // Note: NO stdin.end() - this is the key difference from headless mode
  } else {
    const error = new Error("Failed to get stdin pipe for Claude CLI");
    callbacks?.onError?.(error);
  }

  const processKey = `interactive-${stage}-${issueNumber ?? "no-issue"}`;

  // Set up inactivity timeout
  let inactivityTimeout: NodeJS.Timeout | null = null;

  const resetInactivityTimeout = () => {
    if (inactivityTimeout) {
      clearTimeout(inactivityTimeout);
    }
    inactivityTimeout = setTimeout(() => {
      callbacks?.onStderr?.(
        `[skillRunner] Interactive process timed out after ${INTERACTIVE_TIMEOUT_MS / 60000} minutes of inactivity\n`
      );
      handle.kill();
    }, INTERACTIVE_TIMEOUT_MS);
  };

  // Start the inactivity timer
  resetInactivityTimeout();

  // Handle stdout - raw text output (no stream-json parsing)
  proc.stdout?.on("data", (data: Buffer) => {
    const text = data.toString();
    callbacks?.onStdout?.(text);
    // Reset inactivity timeout on output
    resetInactivityTimeout();
  });

  // Handle stderr
  proc.stderr?.on("data", (data: Buffer) => {
    const text = data.toString();
    callbacks?.onStderr?.(text);
    // Reset inactivity timeout on output
    resetInactivityTimeout();
  });

  // Handle process completion
  proc.on("close", (exitCode) => {
    if (inactivityTimeout) {
      clearTimeout(inactivityTimeout);
    }
    activeProcesses.delete(processKey);
    callbacks?.onComplete?.({
      success: exitCode === 0,
      exitCode,
      // No token usage in interactive mode
      tokenUsage: undefined,
    });
  });

  // Handle process error
  proc.on("error", (error) => {
    if (inactivityTimeout) {
      clearTimeout(inactivityTimeout);
    }
    activeProcesses.delete(processKey);
    callbacks?.onError?.(error);
    callbacks?.onComplete?.({
      success: false,
      exitCode: null,
      error,
    });
  });

  // Create the handle with interactive-specific methods
  const handle: SkillProcessHandle = {
    process: proc,
    stage,
    issueNumber,
    isInteractive: true,
    kill: () => {
      if (inactivityTimeout) {
        clearTimeout(inactivityTimeout);
      }
      proc.kill("SIGTERM");
      activeProcesses.delete(processKey);
    },
    writeToStdin: (message: string): boolean => {
      if (!proc.stdin || proc.stdin.destroyed) {
        return false;
      }
      try {
        proc.stdin.write(message + "\n");
        // Reset inactivity timeout on user input
        resetInactivityTimeout();
        return true;
      } catch {
        return false;
      }
    },
  };

  activeProcesses.set(processKey, handle);
  return handle;
}

/**
 * Write a follow-up message to an interactive process's stdin.
 *
 * Only works for interactive processes (created via runStageSkillInteractive).
 * Headless processes have closed stdin and will reject writes.
 *
 * @param handle - The process handle from runStageSkillInteractive
 * @param message - The message to send
 * @returns true if write succeeded, false if stdin unavailable
 *
 * @see runStageSkillInteractive
 * @see Issue #495
 */
export function writeToInteractiveProcess(handle: SkillProcessHandle, message: string): boolean {
  // Validate this is an interactive process
  if (!handle.isInteractive) {
    console.warn(
      "[skillRunner] writeToInteractiveProcess called on non-interactive process. " +
        "Use runStageSkillInteractive() to create an interactive process."
    );
    return false;
  }

  // Use the handle's writeToStdin method
  if (handle.writeToStdin) {
    return handle.writeToStdin(message);
  }

  // Fallback: try direct write
  if (handle.process && handle.process.stdin && !handle.process.stdin.destroyed) {
    try {
      handle.process.stdin.write(message + "\n");
      return true;
    } catch {
      return false;
    }
  }

  return false;
}

/**
 * Check if a process is interactive
 *
 * @param handle - The process handle to check
 * @returns true if the process is interactive
 */
export function isInteractiveProcess(handle: SkillProcessHandle): boolean {
  return handle.isInteractive === true;
}
