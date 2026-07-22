/**
 * SkillRunner — Thin executor service for pipeline stages.
 *
 * Spawns the Claude CLI for a single stage, streams output, and reports
 * the result (exit code, tokens, feedback signals). All orchestration
 * decisions (retry, escalation, budget, RALPH) live in Go — this class
 * only executes what Go tells it to.
 *
 * Called by:
 * - IpcStageRunner bridge (when Go sends pipeline.runStage event)
 * - HeadlessOrchestrator shim (transition period)
 *
 * @see Issue #1901 — Decompose HeadlessOrchestrator
 */

import * as fs from "fs";
import * as path from "path";
import type { PipelineStage } from "@nightgauge/sdk";
import {
  runStageSkillHeadless,
  killAllActiveProcesses,
  hasActiveProcess,
  type SkillRunResult,
  type SkillProcessHandle,
  type ErrorCategory,
  type RateLimitEventData,
} from "../utils/skillRunner";
import type { StallEvent } from "../schemas/stallEvents";
import type { StallEscalationLevel, PauseForStallPayload } from "../schemas/pipelineState";
import { precomputeCalibratedStallThresholds } from "../utils/incrediConfig";
import type { Logger } from "../utils/logger";
import type { IpcClient } from "./IpcClient";

/**
 * Parameters for executing a pipeline stage.
 * Matches the Go RunStageParams from pipeline_messages.go.
 */
export interface RunStageParams {
  stage: PipelineStage;
  issueNumber: number;
  model: string;
  maxTokens?: number;
  timeout: number; // ms
  skillContent?: string; // Resolved SKILL.md body from platform (paid tiers); empty = use local file
  contextFile?: string;
  outputFile?: string;
  worktreeDir: string;
  repo?: string;
  allowedTools?: string[];
  prompt?: string;
  /** When true, stall handling uses escalation+pause instead of silent kill (Issue #2656) */
  autonomousMode?: boolean;
  /**
   * The run's UUID, threaded from the Go scheduler (#228). Passed to
   * runStageSkillHeadless so the SDK TraceRecorder writes to the run's
   * <run_id>.jsonl instead of disabling itself when run-state.json lacks one.
   */
  runId?: string;
}

/**
 * Phase event detected during stage execution.
 */
export interface PhaseEvent {
  stage: string;
  name: string;
  index: number;
  total: number;
}

/**
 * Result of a stage execution.
 * Sent back to Go via pipeline.stageResult IPC request.
 */
export interface StageResult {
  success: boolean;
  exitCode: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  feedbackFile?: string;
  durationMs: number;
  promptDetected?: boolean;
  /** Error classification from local stderr detection (Issue #2573) */
  errorCategory?: ErrorCategory;
  /** For rate limits, exact wait duration in ms until reset (Issue #2573) */
  retryAfterMs?: number;
  /**
   * Human-readable error reason for non-zero exit. Carries canonical markers
   * (`[stall-killed]`, `[cost-cap-exceeded]`) that the Go ClassifyTerminalKind
   * heuristic uses to populate `terminal_failure_kind` on the V3 RunRecord
   * written to the daily JSONL. Empty when success=true. (Issue #3207)
   */
  errorText?: string;
  /**
   * Tail of subagent stdout/stderr captured at terminal failure (≤200 lines /
   * ≤200KB). Carried into the V3 record's per-stage `last_output_lines` field
   * so retros see the final output before the kill. (Issue #3207)
   */
  lastOutputLines?: string;
  /** Whether the process was killed by the stall detector (idle or hard cap). */
  stallKilled?: boolean;
  /** Whether the process was killed by the per-stage cost cap circuit breaker. */
  costCapExceeded?: boolean;
  /**
   * #3666 follow-up: in-memory shipped-partially signal forwarded via IPC.
   *
   * True when costCapExceeded fired AND the stage's work product (e.g. a
   * pr-create PR) actually shipped. The Go scheduler reads this directly
   * from the IPC stage result and advances to the next stage rather than
   * counting the run as a hard failure. Replaces the budget-overrun-{N}.json
   * disk contract, which broke for multi-repo workspaces because TS wrote
   * to the per-issue worktree but Go read from workspaceRoot.
   */
  shippedPartially?: boolean;
  /**
   * PR number produced by the killed stage (set when shippedPartially is
   * true; zero otherwise). Surfaced in operator-visible logs.
   */
  shippedPRNumber?: number;

  // ── Issue #3605 stage-exit diagnostic fields ─────────────────────────────
  // Captured by the underlying skillRunner subprocess wrapper and forwarded
  // to PipelineBridge so the daily exit-record JSONL has enough forensic
  // detail to debug failures without re-running. All optional — empty values
  // are dropped at the IPC boundary so healthy runs stay terse.

  /** claude CLI session id, when captured. */
  sessionId?: string;
  /** POSIX signal name (SIGTERM / SIGKILL) delivered to the subprocess. */
  signal?: string;
  /**
   * Source code path that delivered `signal`:
   * "stall-kill" | "hard-cap" | "quota-fast-fail" | "processTree-reaper" | "external".
   */
  signalSource?: string;
  /** Wall time from spawn to exit in milliseconds. */
  elapsedMs?: number;
  /** Milliseconds since the last subprocess output chunk at exit. */
  idleMsAtExit?: number;
  /** The most recent Bash tool_use command (truncated to ≤500 chars). */
  lastBashCommand?: string;
  /** Exit code of the matching Bash tool_result, when observed. */
  lastBashExit?: number;
  /** True when the stream included a stop-hook error notification. */
  stopHookErrored?: boolean;
  /** Last 4 KB of stderr from the SkillRunner ring buffer. */
  stderrTail?: string;

  // ── #91 served-model attribution ─────────────────────────────────────────

  /**
   * The model that actually served the stage per the CLI stream (last
   * observed), when it diverged from the requested model. The claude CLI
   * silently retries safety-refused turns on a fallback model
   * (`model_refusal_fallback`) and still exits 0 — Go attributes
   * cost/telemetry/history to this value when set.
   * See docs/spikes/fable-5-behavior-porting.md §8.3.
   */
  servedModel?: string;
  /** Original model of the CLI's refusal fallback, when one was observed. */
  refusalFallbackFrom?: string;
  /** Fallback model of the CLI's refusal fallback, when one was observed. */
  refusalFallbackTo?: string;
  /** `api_refusal_category` from the refusal fallback event. */
  refusalFallbackCategory?: string;
}

/**
 * Callbacks for streaming output during stage execution.
 */
export interface SkillRunnerCallbacks {
  onStdout?: (stage: PipelineStage, data: string) => void;
  onStderr?: (stage: PipelineStage, data: string) => void;
  onPhaseStart?: (stage: PipelineStage, name: string, index: number, total: number) => void;
  onTokenUsage?: (usage: {
    inputTokens: number;
    outputTokens: number;
    cacheReadTokens: number;
    cacheCreationTokens: number;
    costUsd: number;
  }) => void;
  /**
   * Live in-stage token/cost estimate (#233), throttled to >=5s by the
   * underlying runner. Distinct from onTokenUsage: this is the growing-context
   * live preview (latest-wins input, summed output), forwarded to the platform
   * via pipeline.notifyStageProgress. Never drives the authoritative per-stage
   * totals — the terminal `result` envelope reconciles those.
   */
  onStageProgress?: (
    stage: PipelineStage,
    usage: {
      inputTokens: number;
      outputTokens: number;
      cacheReadTokens: number;
      cacheCreationTokens: number;
      costUsd: number;
    }
  ) => void;
  onToolCall?: (stage: PipelineStage, data: { tool: string; target: string }) => void;
  /** Called when a rate_limit_event is detected in stream-json output (Issue #2573) */
  onRateLimitEvent?: (event: RateLimitEventData & { waitMs: number }) => void;
  /**
   * Called when a stall warning is shown at 1×, 2×, or 3× threshold (Issue #2655).
   * Used to update persistent UI indicators (status bar + output panel entries).
   * @param event - The stall event with elapsed_ms and threshold_ms
   * @param multiplier - Escalation level: 1 = first warning, 2 = 2×, etc.
   */
  onStallWarning?: (event: StallEvent, multiplier: number) => void;
  /**
   * Called when a stage completes after a stall warning was shown (Issue #797, #2655).
   * Used to clear persistent stall UI indicators (status bar + output panel entries).
   */
  onStallWarningClear?: () => void;
  /**
   * Called at each escalation level in autonomous mode (Issue #2656).
   * @param stage - The pipeline stage
   * @param level - The escalation level reached
   * @param event - The stall event
   */
  onStallEscalation?: (
    stage: PipelineStage,
    level: StallEscalationLevel,
    event: StallEvent
  ) => void;
  /**
   * Called when autonomous stall pause is triggered (Issue #2656).
   * @param stage - The pipeline stage
   * @param payload - Pause context including issue number, elapsed time, timeout
   * @returns Promise resolving to "resume" or "abort"
   */
  onStallPause?: (
    stage: PipelineStage,
    payload: PauseForStallPayload
  ) => Promise<"resume" | "abort">;
}

/**
 * SkillRunner — spawns Claude CLI, streams output, reports result.
 *
 * This service is stateless per invocation. Each call to runStage()
 * spawns a fresh subprocess with full context isolation.
 */
export class SkillRunner {
  private activeHandle: SkillProcessHandle | null = null;

  constructor(
    private readonly ipcClient: IpcClient | null,
    private readonly logger: Logger
  ) {}

  /**
   * Execute a single pipeline stage.
   *
   * Spawns a Claude CLI subprocess via runStageSkillHeadless(),
   * collects token usage and exit code, and returns the result.
   */
  async runStage(params: RunStageParams, callbacks?: SkillRunnerCallbacks): Promise<StageResult> {
    const { stage, issueNumber } = params;
    const startTime = Date.now();

    this.logger.info("SkillRunner: starting stage", {
      stage,
      issueNumber,
      model: params.model,
      worktreeDir: params.worktreeDir,
    });

    // Pre-compute history-calibrated stall thresholds before spawning the process.
    // The cache is read synchronously by runStageSkillHeadless() when setting up
    // the stall ticker. Fire-and-forget errors are safe — falls back to static defaults.
    // @see Issue #2654 - History-calibrated stall thresholds
    try {
      await precomputeCalibratedStallThresholds(params.worktreeDir);
    } catch (err) {
      this.logger.warn("SkillRunner: calibrated stall threshold pre-compute failed", { err });
    }

    return new Promise<StageResult>((resolve) => {
      const handle = runStageSkillHeadless(
        stage,
        issueNumber,
        {
          onStdout: (data) => {
            callbacks?.onStdout?.(stage, data);
          },
          onStderr: (data) => {
            callbacks?.onStderr?.(stage, data);
          },
          onTokenUsage: async (usage) => {
            callbacks?.onTokenUsage?.(usage);
          },
          onStageProgress: (usage) => {
            callbacks?.onStageProgress?.(stage, usage);
          },
          onPhaseStart: (_detectedStage, name, index, total) => {
            callbacks?.onPhaseStart?.(stage, name, index, total);
          },
          onToolCall: (toolName, toolInput) => {
            const input = toolInput as Record<string, unknown> | undefined;
            const target =
              typeof input?.file_path === "string"
                ? input.file_path
                : typeof input?.command === "string"
                  ? (input.command as string).substring(0, 100)
                  : typeof input?.pattern === "string"
                    ? (input.pattern as string)
                    : "";
            callbacks?.onToolCall?.(stage, { tool: toolName, target });
          },
          onRateLimitEvent: (event) => {
            callbacks?.onRateLimitEvent?.(event);
          },
          onStallWarning: (event, multiplier) => {
            callbacks?.onStallWarning?.(event, multiplier);
          },
          onStallWarningClear: () => {
            callbacks?.onStallWarningClear?.();
          },
          onStallEscalation: callbacks?.onStallEscalation
            ? (level, event) => {
                callbacks.onStallEscalation!(stage, level, event);
              }
            : undefined,
          onStallPause: callbacks?.onStallPause
            ? (payload) => callbacks.onStallPause!(stage, payload)
            : undefined,
          onComplete: async (result: SkillRunResult) => {
            const durationMs = Date.now() - startTime;
            this.activeHandle = null;

            this.logger.info("SkillRunner: stage complete", {
              stage,
              issueNumber,
              success: result.success,
              exitCode: result.exitCode,
              durationMs,
              inputTokens: result.tokenUsage?.inputTokens ?? 0,
              outputTokens: result.tokenUsage?.outputTokens ?? 0,
            });

            // #3666 follow-up: detect shipped-partially BEFORE we build the
            // errorText so the IPC result carries an authoritative signal to
            // Go. SkillRunner has the worktree path; the pr-{N}.json file
            // there is written by pr-create's Phase 4 the moment the PR is
            // opened. If it exists with a valid pr_number, the work product
            // shipped — the cost-cap kill only caught the tail-end CI-check
            // monitoring, not the PR creation itself.
            let shippedPartially = false;
            let shippedPRNumber: number | undefined;
            if (result.costCapExceeded && stage === "pr-create") {
              try {
                const prContextPath = path.join(
                  params.worktreeDir,
                  ".nightgauge",
                  "pipeline",
                  `pr-${issueNumber}.json`
                );
                if (fs.existsSync(prContextPath)) {
                  const parsed = JSON.parse(fs.readFileSync(prContextPath, "utf-8")) as {
                    pr_number?: unknown;
                  };
                  if (typeof parsed.pr_number === "number" && parsed.pr_number > 0) {
                    shippedPartially = true;
                    shippedPRNumber = parsed.pr_number;
                    this.logger.warn(
                      "SkillRunner: pr-create cost-cap kill reclassified as shipped-partially — PR exists, will advance to pr-merge",
                      {
                        stage,
                        issueNumber,
                        prNumber: shippedPRNumber,
                        worktreeDir: params.worktreeDir,
                      }
                    );
                  }
                }
              } catch (err) {
                // Fail-closed: any uncertainty leaves shippedPartially=false
                // and the run is treated as a real failure. Better to err on
                // the side of escalating to the user than to swallow a real
                // problem with a false reclassification.
                this.logger.debug("SkillRunner: shipped-partially detection failed (non-fatal)", {
                  stage,
                  issueNumber,
                  err: err instanceof Error ? err.message : String(err),
                });
              }
            }

            // Synthesize a Go-friendly errorText so the scheduler's
            // ClassifyTerminalKind heuristic can populate
            // terminal_failure_kind on the V3 RunRecord. The canonical
            // markers (`[stall-killed]` / `[cost-cap-exceeded]`) drive
            // classification — the trailing detail is for human readability.
            // (Issue #3207)
            const success = result.success && !result.promptDetected;
            let errorText: string | undefined;
            if (!success) {
              if (result.costCapExceeded) {
                errorText = `[cost-cap-exceeded] ${stage} terminated`;
                if (
                  typeof result.costCapUsd === "number" &&
                  typeof result.costAtTerminationUsd === "number"
                ) {
                  errorText += ` ($${result.costAtTerminationUsd.toFixed(
                    4
                  )} ≥ $${result.costCapUsd.toFixed(2)} cap)`;
                }
                if (shippedPartially) {
                  errorText += ` — PR #${shippedPRNumber} shipped, advancing to pr-merge`;
                }
              } else if (result.stallKilled) {
                errorText = `[stall-killed] ${stage} terminated`;
                if (result.error?.message) {
                  errorText += `: ${result.error.message}`;
                }
              } else if (result.error?.message) {
                errorText = result.error.message;
              } else {
                errorText = `stage ${stage} exited ${result.exitCode ?? 1}`;
              }
            }

            resolve({
              success,
              exitCode: result.exitCode ?? 1,
              inputTokens: result.tokenUsage?.inputTokens ?? 0,
              outputTokens: result.tokenUsage?.outputTokens ?? 0,
              cacheReadTokens: result.tokenUsage?.cacheReadTokens ?? 0,
              cacheCreationTokens: result.tokenUsage?.cacheCreationTokens ?? 0,
              costUsd: result.tokenUsage?.costUsd ?? 0,
              durationMs,
              promptDetected: result.promptDetected,
              errorCategory: result.errorCategory,
              retryAfterMs: result.retryAfterMs,
              errorText,
              lastOutputLines: result.lastOutputLines,
              stallKilled: result.stallKilled,
              costCapExceeded: result.costCapExceeded,
              // #3666 follow-up: shipped-partially signal forwarded via IPC.
              shippedPartially,
              shippedPRNumber,
              // ── #3605 stage-exit diagnostic forwarding ─────────────
              sessionId: result.sessionId,
              signal: result.signal,
              signalSource: result.signalSource,
              elapsedMs: result.elapsedMs ?? durationMs,
              idleMsAtExit: result.idleMsAtExit,
              lastBashCommand: result.lastBashCommand,
              lastBashExit: result.lastBashExit,
              stopHookErrored: result.stopHookErrored,
              stderrTail: result.stderrTail,
              // ── #91 served-model attribution forwarding ────────────
              servedModel: result.servedModel,
              refusalFallbackFrom: result.modelRefusalFallback?.originalModel,
              refusalFallbackTo: result.modelRefusalFallback?.fallbackModel,
              refusalFallbackCategory: result.modelRefusalFallback?.category,
            });
          },
        },
        undefined, // issueMetadata
        undefined, // batchContext
        undefined, // skipToPhase
        undefined, // modelOverride
        undefined, // pauseAutoRouting
        params.worktreeDir, // pinnedWorkspaceRoot
        undefined, // modelOverrideSource
        params.skillContent ?? undefined, // injectedSkillContent — platform-resolved skill body
        params.autonomousMode, // autonomousMode (Issue #2656)
        undefined, // warnThresholdUsd (Go scheduler enforces budget on this path)
        // Issue #3867: the Go scheduler's per-issue repo (owner/repo). Drives
        // NIGHTGAUGE_TARGET_REPO so the repo-mismatch gate checks the
        // issue's intended repo, not the workspace primary.
        params.repo,
        // #228: the run's UUID, so the SDK TraceRecorder writes to the run's
        // <run_id>.jsonl instead of disabling when run-state.json lacks one.
        params.runId
      );

      this.activeHandle = handle;
    });
  }

  /**
   * Kill the currently running stage process (if any).
   */
  abort(): void {
    if (this.activeHandle) {
      this.logger.info("SkillRunner: aborting active process");
      this.activeHandle.kill();
      this.activeHandle = null;
    }
  }

  /**
   * Whether a stage process is currently running.
   */
  get isRunning(): boolean {
    return this.activeHandle !== null && hasActiveProcess();
  }

  /**
   * Kill all active skill processes globally.
   */
  static killAll(): void {
    killAllActiveProcesses();
  }
}
