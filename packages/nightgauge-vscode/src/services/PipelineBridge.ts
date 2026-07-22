/**
 * PipelineBridge — Wires Go pipeline orchestration events to TypeScript SkillRunner.
 *
 * When Go sends "pipeline.runStage" event via IPC, this bridge:
 * 1. Receives the RunStageParams
 * 2. Delegates to SkillRunner.runStage()
 * 3. Sends "pipeline.stageResult" request back to Go with the result
 *
 * When Go sends "pipeline.abort" event, this bridge:
 * 1. Calls SkillRunner.abort() to kill the active process
 *
 * This bridge replaces the orchestration logic previously in HeadlessOrchestrator.ts.
 * Go handles all retry/backtrack/budget/RALPH decisions; TypeScript only executes.
 *
 * @see Issue #1901 — Decompose HeadlessOrchestrator
 * @see internal/ipc/ipc_stage_runner.go — Go side of this bridge
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { SkillRunner, type RunStageParams, type SkillRunnerCallbacks } from "./SkillRunner";
import type { IpcClient } from "./IpcClient";
import type { Logger } from "../utils/logger";
import type { PipelineStateService } from "./PipelineStateService";
import type { LicensePreflight } from "../platform/LicensePreflight";
import type { OfflineManager } from "../platform/OfflineManager";
import type { OutputWindow } from "../views/outputWindow/OutputWindow";
import type { StatusBarManager } from "../utils/statusBar";
import type { PipelineTreeProvider } from "../views/PipelineTreeProvider";
import { createStreamOutputHandler } from "../utils/streamOutputHandler";
import { createPhaseTracker, type PhaseTracker } from "../utils/phaseTracker";
import { getStageLabel, formatRateLimitCountdown } from "../utils/skillRunner";
import {
  isRetryableApiError,
  calculateBackoffDelay,
  isRateLimitError,
} from "../utils/retryHelpers";
import { getRetryConfig } from "../utils/incrediConfig";
import { StallStatusBarItem } from "./StallStatusBarItem";
import type { StallEscalationLevel, PauseForStallPayload } from "../schemas/pipelineState";

/**
 * IPC RunStageParams as received from Go.
 * Matches ipc.RunStageParams in pipeline_messages.go.
 */
interface IpcRunStageParams {
  stage: string;
  issueNumber: number;
  model: string;
  maxTokens?: number;
  timeoutMs: number;
  skillContent?: string;
  contextFile?: string;
  outputFile?: string;
  worktreeDir: string;
  repo?: string;
  allowedTools?: string[];
  prompt?: string;
  skillFallbackUsed?: boolean;
  /** When true, stall handling uses escalation+pause instead of silent kill (Issue #2656) */
  autonomousMode?: boolean;
  /** UUID v7 run ID threaded from runstate for correlation (#3557) */
  runId?: string;
}

/**
 * IPC AbortParams as received from Go.
 * Matches ipc.AbortParams in pipeline_messages.go.
 */
interface IpcAbortParams {
  issueNumber: number;
  reason: string;
}

/**
 * PipelineBridge — connects Go orchestration events to TS SkillRunner execution.
 *
 * Lifecycle: created once per extension activation, disposed on deactivation.
 */
export class PipelineBridge {
  private readonly skillRunner: SkillRunner;
  private readonly disposables: Array<{ dispose: () => void }> = [];
  private readonly streamHandler: ReturnType<typeof createStreamOutputHandler> | null;
  private readonly phaseTracker: PhaseTracker | null;
  private readonly stallStatusBarItem: StallStatusBarItem;

  constructor(
    private readonly ipcClient: IpcClient,
    private readonly logger: Logger,
    private readonly stateService: PipelineStateService | null = null,
    private readonly licensePreflight: LicensePreflight | null = null,
    private readonly offlineManager: OfflineManager | null = null,
    private readonly outputWindow: OutputWindow | null = null,
    private readonly statusBar: StatusBarManager | null = null,
    private readonly treeProvider: PipelineTreeProvider | null = null
  ) {
    this.skillRunner = new SkillRunner(ipcClient, logger);
    this.stallStatusBarItem = new StallStatusBarItem();
    this.disposables.push(this.stallStatusBarItem);

    // Create stream output handler for routing stdout/stderr to OutputWindow.
    // Mirrors the wiring in services.ts for the interactive (HeadlessOrchestrator) path.
    if (this.outputWindow && this.stateService) {
      this.phaseTracker = createPhaseTracker(this.stateService);
      this.streamHandler = createStreamOutputHandler(this.outputWindow, {
        onPhaseDetected: this.phaseTracker.onPhaseDetected,
      });
    } else {
      this.phaseTracker = null;
      this.streamHandler = null;
    }

    this.registerEventHandlers();
  }

  /**
   * Register IPC event handlers for Go → TypeScript communication.
   */
  private registerEventHandlers(): void {
    // Handle pipeline.runStage: Go asks TS to execute a stage
    this.disposables.push(
      this.ipcClient.on("pipeline.runStage", (data: unknown) => {
        const params = data as IpcRunStageParams;
        this.handleRunStage(params).catch((err) => {
          this.logger.error("PipelineBridge: runStage handler error", {
            error: String(err),
          });
        });
      })
    );

    // Handle pipeline.abort: Go asks TS to kill the active process
    this.disposables.push(
      this.ipcClient.on("pipeline.abort", (data: unknown) => {
        const params = data as IpcAbortParams;
        this.logger.info("PipelineBridge: abort received", {
          issueNumber: params.issueNumber,
          reason: params.reason,
        });
        this.skillRunner.abort();
      })
    );

    // Handle pipeline.validateLicense: Go asks TS to validate license before stages
    this.disposables.push(
      this.ipcClient.on("pipeline.validateLicense", (data: unknown) => {
        const { issueNumber } = data as { issueNumber: number };
        this.handleValidateLicense(issueNumber).catch((err) => {
          this.logger.error("PipelineBridge: validateLicense handler error", {
            error: String(err),
          });
        });
      })
    );

    // Handle pipeline.licenseExpired: Go notifies TS that license expired mid-run
    this.disposables.push(
      this.ipcClient.on("pipeline.licenseExpired", (data: unknown) => {
        const { issueNumber } = data as { issueNumber: number };
        void vscode.window
          .showWarningMessage(
            `Your license expired during pipeline run #${issueNumber} — please renew for continued access.`,
            "Renew"
          )
          .then((action) => {
            if (action === "Renew") {
              void vscode.env.openExternal(
                vscode.Uri.parse("https://nightgauge.dev/account/renew")
              );
            }
          });
      })
    );
  }

  /**
   * Handle a pipeline.runStage event from Go.
   * Executes the stage via SkillRunner and sends the result back.
   */
  private async handleRunStage(ipcParams: IpcRunStageParams): Promise<void> {
    const stage = ipcParams.stage as PipelineStage;

    this.logger.info("PipelineBridge: received runStage", {
      stage,
      issueNumber: ipcParams.issueNumber,
      model: ipcParams.model,
    });

    // Show warning notification when paid-tier platform resolution failed
    if (ipcParams.skillFallbackUsed) {
      void vscode.window
        .showWarningMessage("Using community skill — platform unavailable", "Reconnect")
        .then((action) => {
          if (action === "Reconnect") {
            this.offlineManager?.start(); // Re-trigger health check cycle
          }
        });
    }

    const runParams: RunStageParams = {
      stage,
      issueNumber: ipcParams.issueNumber,
      model: ipcParams.model,
      maxTokens: ipcParams.maxTokens,
      timeout: ipcParams.timeoutMs,
      skillContent: ipcParams.skillContent,
      contextFile: ipcParams.contextFile,
      outputFile: ipcParams.outputFile,
      worktreeDir: ipcParams.worktreeDir,
      repo: ipcParams.repo,
      allowedTools: ipcParams.allowedTools,
      prompt: ipcParams.prompt,
      autonomousMode: ipcParams.autonomousMode,
      // Forward the run's UUID so SkillRunner can open the SDK TraceRecorder
      // against the run's <run_id>.jsonl instead of silently disabling (#228).
      runId: ipcParams.runId,
    };

    // Notify OutputWindow that a stage is starting — mirrors the
    // onStageStart callback in the interactive (HeadlessOrchestrator) path.
    // Automated per-stage update — ensure the panel exists without stealing
    // the user's active tab (no reveal).
    if (this.outputWindow) {
      this.outputWindow.show();
      this.outputWindow.setIssueNumber(ipcParams.issueNumber);
      this.outputWindow.updateStageStatus(stage, "running");
      this.outputWindow.appendLine(`Starting ${getStageLabel(stage)}...`, "info", stage);
    }
    this.statusBar?.showRunning(stage);
    this.treeProvider?.updateStageStatus(stage, "running");

    // Track previous cumulative totals so we can convert SkillRunner's
    // running-total onTokenUsage into the additive deltas that
    // PipelineStateService.updateTokens expects. Matches the delta-tracking
    // pattern used for the legacy chat-initiated path in
    // bootstrap/services.ts (Issue #843, #2919).
    let prevTokenUsage = {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };

    // Set up callbacks for streaming output and phase tracking.
    // stdout/stderr are routed through the stream handler → OutputWindow
    // so automated-mode output is visible in the Output view.
    const callbacks: SkillRunnerCallbacks = {
      onStdout: (stg, data) => {
        this.streamHandler?.onStdout(stg, data);
      },
      onStderr: (stg, data) => {
        this.streamHandler?.onStderr(stg, data);
      },
      onPhaseStart: (_stage, name, index, total) => {
        this.logger.info("PipelineBridge: phase started", {
          stage,
          name,
          index,
          total,
        });

        // Track the phase locally so PipelineStateService fires _onPhaseStart
        // and the tree view updates in real time. This is the authoritative
        // path for Go-driven IPC mode: notifyPhaseTransition below fails
        // because activeRuntimes is only populated by the HeadlessOrchestrator
        // path, not by IpcStageRunner.
        this.phaseTracker?.onPhaseDetected(stage as PipelineStage, {
          name,
          index,
          total,
          stage,
        });

        // Best-effort: also notify Go so it can record the phase in
        // RuntimeState for analytics. This will fail in IPC mode (no
        // activeRuntime) but the error is swallowed — local tracking above
        // is what drives the tree view.
        this.ipcClient
          .call("pipeline.notifyPhaseTransition", {
            repo: ipcParams.repo ?? "",
            issueNumber: ipcParams.issueNumber,
            stage,
            name,
            index,
            total,
            eventType: "start",
          })
          .catch((err) => {
            this.logger.error("PipelineBridge: notifyPhaseTransition failed", {
              error: String(err),
            });
          });
      },
      onTokenUsage: (usage) => {
        this.logger.info("PipelineBridge: token usage", {
          stage,
          inputTokens: usage.inputTokens,
          outputTokens: usage.outputTokens,
          costUsd: usage.costUsd,
        });

        // Forward live deltas to PipelineStateService so slot badges update
        // mid-stage instead of only at stage.complete (Issue #2919). The
        // legacy chat-initiated path (bootstrap/services.ts) already does this;
        // the Go-driven path previously only logged, so per-stage tokens were
        // invisible until Go emitted stage.complete — and were lost entirely
        // whenever the terminal CLI result envelope was missed.
        if (this.stateService) {
          const delta = {
            inputTokens: usage.inputTokens - prevTokenUsage.inputTokens,
            outputTokens: usage.outputTokens - prevTokenUsage.outputTokens,
            cacheReadTokens: usage.cacheReadTokens - prevTokenUsage.cacheReadTokens,
            cacheCreationTokens: usage.cacheCreationTokens - prevTokenUsage.cacheCreationTokens,
            costUsd: usage.costUsd - prevTokenUsage.costUsd,
          };
          prevTokenUsage = { ...usage };

          // Only forward when there's an actual delta — avoids noise when
          // SkillRunner's fallback path re-emits an unchanged total.
          const hasDelta =
            delta.inputTokens > 0 ||
            delta.outputTokens > 0 ||
            delta.cacheReadTokens > 0 ||
            delta.cacheCreationTokens > 0 ||
            delta.costUsd > 0;

          if (hasDelta) {
            this.stateService
              .updateTokens({
                inputTokens: delta.inputTokens,
                outputTokens: delta.outputTokens,
                cacheReadTokens: delta.cacheReadTokens,
                cacheCreationTokens: delta.cacheCreationTokens,
                costUsd: delta.costUsd,
                stage,
                issueNumber: ipcParams.issueNumber,
              })
              .catch((err) => {
                this.logger.warn("PipelineBridge: updateTokens failed", {
                  stage,
                  error: String(err),
                });
              });
          }
        }
      },
      onStageProgress: (_stage, usage) => {
        // Live in-stage token/cost estimate (#233), already throttled to >=5s in
        // SkillRunner. Fire-and-forget a stage_progress event to the platform via
        // Go so the run-detail view can show tokens/cost accruing mid-stage; the
        // authoritative per-stage totals still flow through the "complete"
        // transition. Best-effort — swallow errors like notifyPhaseTransition.
        this.ipcClient
          .call("pipeline.notifyStageProgress", {
            repo: ipcParams.repo ?? "",
            issueNumber: ipcParams.issueNumber,
            stage,
            inputTokens: usage.inputTokens,
            outputTokens: usage.outputTokens,
            cacheReadTokens: usage.cacheReadTokens,
            costUsd: usage.costUsd,
          })
          .catch((err) => {
            this.logger.warn("PipelineBridge: notifyStageProgress failed", {
              stage,
              error: String(err),
            });
          });
      },
      onRateLimitEvent: (event) => {
        this.logger.info("PipelineBridge: rate limit event", {
          stage,
          rateLimitType: event.rateLimitType,
          utilization: event.utilization,
          status: event.status,
          waitMs: event.waitMs,
        });

        if (event.status === "limited") {
          // Hard rate limit — show pause notification
          this.outputWindow?.appendLine(
            `\u23F8 Pipeline paused \u2014 rate limited (${event.rateLimitType})\n` +
              `   Utilization: ${event.utilization}%\n` +
              `   Resuming in ${formatRateLimitCountdown(event.waitMs)}`,
            "warning",
            stage
          );
          this.statusBar?.showError(
            `Rate limited \u2014 resuming in ${formatRateLimitCountdown(event.waitMs)}`
          );
        } else {
          // Warning only — approaching limit
          this.outputWindow?.appendLine(
            `\u26A0 Approaching rate limit (${event.rateLimitType}). Utilization: ${event.utilization}%`,
            "warning",
            stage
          );
        }
      },
      // Persistent stall indicators (Issue #2655)
      onStallWarning: (event, multiplier) => {
        this.stallStatusBarItem.showStalled(stage, event.elapsed_ms);
        this.outputWindow?.addStallWarning(stage, event.elapsed_ms, event.threshold_ms, multiplier);
      },
      onStallWarningClear: () => {
        this.stallStatusBarItem.clear();
        this.outputWindow?.removeStallWarnings(stage);
      },
      // Autonomous stall escalation callbacks (Issue #2656)
      onStallEscalation: ipcParams.autonomousMode
        ? (
            _stg: PipelineStage,
            level: StallEscalationLevel,
            event: import("../schemas/stallEvents").StallEvent
          ) => {
            this.handleStallEscalation(stage, level, event, ipcParams.issueNumber);
          }
        : undefined,
      onStallPause: ipcParams.autonomousMode
        ? (_stg: PipelineStage, payload: PauseForStallPayload) =>
            this.handleStallPause(stage, payload)
        : undefined,
    };

    // 5xx backoff state for the Go-driven IPC path (#3619). Keeps retry
    // logic inside the TS execution layer so Go receives one final result.
    let apiRetryCount = 0;
    const retryConfig = getRetryConfig(ipcParams.worktreeDir);

    let result: Awaited<ReturnType<typeof this.skillRunner.runStage>> | undefined;
    try {
      // Inner retry loop for transient Anthropic 5xx errors. Mirrors the
      // HeadlessOrchestrator path so the Go scheduler never sees intermediate
      // failures — it gets one final result (success or capped-failure).
      while (true) {
        result = await this.skillRunner.runStage(runParams, callbacks);

        if (!result.success && result.errorText) {
          const shouldRetry =
            isRetryableApiError(result.errorText, retryConfig) &&
            apiRetryCount < retryConfig.max_auto_attempts;
          if (shouldRetry) {
            const delay = isRateLimitError(result.errorText)
              ? retryConfig.rate_limit_delay_ms
              : calculateBackoffDelay(apiRetryCount, retryConfig);
            this.logger.warn("PipelineBridge: 5xx from SkillRunner — backing off", {
              stage,
              apiRetryCount,
              delayMs: delay,
              errorText: result.errorText,
            });
            apiRetryCount++;
            await new Promise((r) => setTimeout(r, delay));
            continue;
          }
        }
        break;
      }

      // Flush any remaining buffered stream content before reporting completion
      this.streamHandler?.flushStage(stage);
      // Finalize the last running phase so the spinner doesn't outlive the stage.
      // The phase tracker only completes phase N when N+1 starts; the terminal
      // phase of a stage needs an explicit close at stage end. Mirrors the
      // legacy onStageComplete wiring in bootstrap/services.ts.
      this.phaseTracker?.completeStagePhases(stage);

      // Update OutputWindow and UI with stage result
      if (result.success) {
        this.outputWindow?.updateStageStatus(stage, "complete");
        this.treeProvider?.updateStageStatus(stage, "complete");
        this.outputWindow?.appendLine(`\u2713 ${getStageLabel(stage)} completed`, "info", stage);
        this.statusBar?.showComplete(stage);
      } else {
        this.outputWindow?.updateStageStatus(stage, "error");
        this.treeProvider?.updateStageStatus(stage, "failed");
        this.outputWindow?.appendLine(
          `\u2717 ${getStageLabel(stage)} failed (exit code ${result.exitCode})`,
          "error",
          stage
        );
        this.statusBar?.showError(`${getStageLabel(stage)} failed`);
      }

      // Send result back to Go via pipeline.stageResult request.
      // errorText / lastOutputLines drive ClassifyTerminalKind on the Go side
      // so the V3 RunRecord written to .nightgauge/pipeline/history/<day>.jsonl
      // carries terminal_failure_kind=stall_kill (or budget_exceeded) instead of
      // dropping the record or mis-classifying it as subagent_crash. (Issue #3207)
      await this.ipcClient.call("pipeline.stageResult", {
        stage: ipcParams.stage,
        issueNumber: ipcParams.issueNumber,
        success: result.success,
        exitCode: result.exitCode,
        inputTokens: result.inputTokens,
        outputTokens: result.outputTokens,
        cacheReadTokens: result.cacheReadTokens,
        costUsd: result.costUsd,
        feedbackFile: result.feedbackFile,
        errorCategory: result.errorCategory,
        retryAfterMs: result.retryAfterMs,
        errorText: result.errorText,
        lastOutputLines: result.lastOutputLines,
        // ── #3605 stage-exit diagnostic record fields ──────────────────
        // Forwarded verbatim from the SkillRunner result; the Go scheduler
        // persists them in the daily JSONL alongside its own observations
        // (rate-limit reading, concurrent siblings). Empty fields are
        // dropped at the IPC boundary so healthy runs stay compact.
        sessionId: result.sessionId,
        signal: result.signal,
        signalSource: result.signalSource,
        elapsedMs: result.elapsedMs,
        idleMsAtExit: result.idleMsAtExit,
        cacheCreationTokens: result.cacheCreationTokens,
        lastBashCommand: result.lastBashCommand,
        lastBashExit: result.lastBashExit,
        stopHookErrored: result.stopHookErrored,
        stderrTail: result.stderrTail,
        // #3666 follow-up: budget-kill + shipped-partially signal forwarded
        // via IPC. Replaces the budget-overrun-{N}.json disk contract that
        // silently broke for multi-repo workspaces (Go couldn't locate the
        // per-issue worktree from workspaceRoot alone).
        budgetExceeded: result.costCapExceeded,
        shippedPartially: result.shippedPartially,
        shippedPRNumber: result.shippedPRNumber,
        // #91 served-model attribution: the claude CLI can silently swap to
        // a fallback model on a safety refusal and still exit 0. Go uses the
        // served model for cost/telemetry/history sinks when set.
        servedModel: result.servedModel,
        refusalFallbackFrom: result.refusalFallbackFrom,
        refusalFallbackTo: result.refusalFallbackTo,
        refusalFallbackCategory: result.refusalFallbackCategory,
      });
    } catch (err) {
      this.logger.error("PipelineBridge: stage execution failed", {
        stage,
        error: String(err),
      });

      // Flush any buffered content before reporting failure
      this.streamHandler?.flushStage(stage);
      // Finalize any in-flight phase so the spinner doesn't persist past failure.
      this.phaseTracker?.completeStagePhases(stage);

      // Update UI with failure
      this.outputWindow?.updateStageStatus(stage, "error");
      this.treeProvider?.updateStageStatus(stage, "failed");
      this.outputWindow?.appendLine(
        `\u2717 ${getStageLabel(stage)} failed: ${String(err)}`,
        "error",
        stage
      );
      this.statusBar?.showError(`${getStageLabel(stage)} failed`);

      // Send failure result back to Go, forwarding any tokens/cost SkillRunner
      // captured before the throw so the Output Window shows real usage.
      // errorText carries the thrown reason so the Go scheduler classifies
      // this as a real failure rather than dropping the V3 RunRecord. (Issue #3207)
      await this.ipcClient.call("pipeline.stageResult", {
        stage: ipcParams.stage,
        issueNumber: ipcParams.issueNumber,
        success: false,
        exitCode: 1,
        inputTokens: result?.inputTokens ?? 0,
        outputTokens: result?.outputTokens ?? 0,
        cacheReadTokens: result?.cacheReadTokens ?? 0,
        cacheCreationTokens: result?.cacheCreationTokens ?? 0,
        costUsd: result?.costUsd ?? 0,
        errorText: `pipeline-bridge: ${String(err)}`,
        lastOutputLines: result?.lastOutputLines,
      });
    }
  }

  /**
   * Handle a pipeline.validateLicense event from Go.
   * Validates the license via LicensePreflight and sends the result back.
   */
  private async handleValidateLicense(issueNumber: number): Promise<void> {
    this.logger.info("PipelineBridge: received validateLicense", {
      issueNumber,
    });

    let result: {
      allowed: boolean;
      tier: string;
      cacheUntil: string;
      reason?: string;
      actionUrl?: string;
      status?: string;
      offline?: boolean;
    };

    if (this.licensePreflight) {
      this.licensePreflight.clearCache(); // Fresh check at pipeline start
      const preflightResult = await this.licensePreflight.validate();
      result = preflightResult;
    } else {
      result = {
        allowed: true,
        tier: "community",
        cacheUntil: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
        status: "community",
        offline: false,
      };
    }

    // #4156: only forward `status` when it's a platform-authoritative answer
    // (offline=false). An offline/degraded result reports status:"community"
    // for UI purposes (see LicensePreflight.communityResult) but that is NOT
    // a confirmed answer — forwarding it would let a transient connectivity
    // blip overwrite Go's cached last-confirmed-revoked/suspended status
    // (IpcLicenseChecker.lastConfirmedStatus) with a spurious "clean" one,
    // silently re-opening a known-bad license the next time re-validation
    // itself times out.
    await this.ipcClient.call("pipeline.licenseResult", {
      issueNumber,
      allowed: result.allowed,
      tier: result.tier,
      reason: result.reason ?? "",
      actionUrl: result.actionUrl ?? "",
      cacheUntil: result.cacheUntil,
      status: result.offline ? "" : (result.status ?? ""),
    });
  }

  /**
   * Whether a stage is currently running.
   */
  get isRunning(): boolean {
    return this.skillRunner.isRunning;
  }

  /**
   * Abort the currently running stage.
   */
  abort(): void {
    this.skillRunner.abort();
  }

  // ── Autonomous stall escalation handlers (Issue #2656) ─────────────

  /**
   * Handle a stall escalation event at a specific level.
   * Updates UI indicators and sends notifications at appropriate levels.
   */
  private handleStallEscalation(
    stage: PipelineStage,
    level: StallEscalationLevel,
    _event: import("../schemas/stallEvents").StallEvent,
    issueNumber: number
  ): void {
    const elapsedMin = Math.floor(_event.elapsed_ms / 60000);

    switch (level) {
      case "status_bar":
        // Already handled by onStallWarning → stallStatusBarItem.showStalled
        break;
      case "output_panel":
        this.outputWindow?.appendLine(
          `\u26A0 Autonomous escalation: stage ${stage} stalled for ${elapsedMin}m (2\u00D7 threshold)`,
          "warning",
          stage
        );
        break;
      case "notification":
        void vscode.window
          .showWarningMessage(
            `Nightgauge: #${issueNumber} ${stage} stalled for ${elapsedMin}m`,
            "View Output"
          )
          .then((choice) => {
            if (choice === "View Output") {
              void vscode.commands.executeCommand("nightgauge.showOutputWindow");
            }
          });
        break;
      case "discord":
        // Discord notification is sent via the DiscordService from bootstrap/services.ts
        // We emit an IPC event that the service layer can listen to
        this.logger.warn("Autonomous stall escalation: discord level", {
          stage,
          issueNumber,
          elapsedMin,
        });
        this.outputWindow?.appendLine(
          `\u26A0 Autonomous escalation: Discord notification sent for ${stage} stall (${elapsedMin}m)`,
          "warning",
          stage
        );
        break;
      case "pause":
        // Pause is handled separately via onStallPause callback
        break;
    }

    // Update status bar escalation indicator
    this.stallStatusBarItem.showStalled(stage, _event.elapsed_ms);
  }

  /**
   * Handle the pause level of stall escalation.
   * Shows a modal dialog with Resume/Abort options and auto-abort timer.
   * Returns the user's decision.
   */
  private async handleStallPause(
    stage: PipelineStage,
    payload: PauseForStallPayload
  ): Promise<"resume" | "abort"> {
    const elapsedMin = Math.floor(payload.elapsed_ms / 60000);
    const timeoutMin = Math.floor(payload.timeout_ms / 60000);

    this.outputWindow?.appendLine(
      `\u23F8 Pipeline PAUSED: ${stage} stalled for ${elapsedMin}m ` +
        `(auto-abort in ${timeoutMin}m if no response)`,
      "warning",
      stage
    );
    this.statusBar?.showError(`Paused: ${stage} stalled — action needed`);

    const choice = await vscode.window.showWarningMessage(
      `Pipeline paused: #${payload.issue_number} ${stage} has been stalled for ${elapsedMin} minutes.\n` +
        `Auto-abort in ${timeoutMin} minutes if no action is taken.`,
      { modal: true },
      "Resume",
      "Abort"
    );

    const action: "resume" | "abort" = choice === "Resume" ? "resume" : "abort";

    this.outputWindow?.appendLine(
      action === "resume"
        ? `\u25B6 User chose Resume — continuing ${stage}`
        : `\u23F9 User chose Abort — terminating ${stage}`,
      "info",
      stage
    );

    return action;
  }

  /**
   * Clean up event handlers.
   */
  dispose(): void {
    for (const d of this.disposables) {
      d.dispose();
    }
    this.disposables.length = 0;
    this.skillRunner.abort();
  }
}
