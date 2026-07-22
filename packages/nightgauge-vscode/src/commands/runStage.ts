/**
 * Run Stage command
 *
 * Runs a single pipeline stage selected by the user.
 * When invoked from a tree item inline button, runs that specific stage.
 * When invoked from command palette, shows stage selector.
 *
 * Each stage runs in a fresh Claude Code session (context isolation)
 * to prevent token accumulation and maintain focused contexts.
 *
 * Uses headless CLI mode (`claude -p`) with SKILL.md instructions passed
 * as the prompt. Output streams to the OutputWindow in real-time.
 *
 * Sets execution mode to 'manual' if not already set, ensuring proper
 * notification behavior for individual stage runs.
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import type { StageTreeItem, PipelineTreeProvider, OutputWindow } from "../views";
import type { PipelineStateService } from "../services/PipelineStateService";
import {
  runStageSkillHeadless,
  runStageSkillInteractive,
  getStageLabel,
  sendInputToActiveProcess,
  type SkillRunCallbacks,
  type SkillExecutionMode,
} from "../utils/skillRunner";
import {
  getDefaultStageExecutionMode,
  getExecutionAdapter,
  type StageExecutionMode,
} from "../utils/incrediConfig";
import { isStreamJsonEnvelope, isEnvelopeFragment } from "../utils/streamJsonFilter";
import { parsePhaseMarker } from "@nightgauge/sdk";
import { createPhaseTracker } from "../utils/phaseTracker";
import { createToolCallData, type ToolCallData } from "../views/outputWindow/ToolCallIndicator";
import { validateAskUserQuestionPayload, formatResponseForStdin } from "../types/askUserQuestion";

/**
 * Stage options for quick pick
 */
interface StageOption extends vscode.QuickPickItem {
  stage: PipelineStage;
}

/**
 * Confirmation dialog option value
 */
type ConfirmationChoice = "confirm" | "cancel";

/**
 * Execution mode option for QuickPick
 *
 * @see Issue #499 - Mode selection UX
 */
interface ExecutionModeOption extends vscode.QuickPickItem {
  mode: StageExecutionMode;
}

/**
 * Mode selection options for the QuickPick
 */
const EXECUTION_MODE_OPTIONS: ExecutionModeOption[] = [
  {
    label: "$(play) Headless (Recommended)",
    description: "Automated execution with token tracking",
    mode: "headless",
  },
  {
    label: "$(terminal) Interactive",
    description: "Conversational mode - send follow-up messages",
    mode: "interactive",
  },
];

/**
 * Show mode selection QuickPick if needed
 *
 * Returns the selected mode, or undefined if user cancelled.
 * Skips the dialog and returns 'headless' if:
 * - Batch mode is active (interactive not supported)
 * - Config has a default mode set
 *
 * @param pipelineStateService - To check if batch mode is active
 * @param logger - For debug logging
 * @returns The selected execution mode, or undefined if cancelled
 *
 * @see Issue #499 - Mode selection UX
 */
async function selectExecutionMode(
  pipelineStateService: PipelineStateService | null,
  logger: Logger
): Promise<StageExecutionMode | undefined> {
  const adapter = getExecutionAdapter();
  // Claude and Codex support interactive mode; every other adapter is
  // headless-only (Gemini, Copilot, LM Studio, …). Codex runs its TUI in a
  // terminal (#4024); Claude streams over piped stdio.
  if (adapter !== "claude" && adapter !== "codex") {
    logger.debug("Adapter is headless-only - forcing headless execution", { adapter });
    return "headless";
  }

  // Check config for default mode
  const defaultMode = getDefaultStageExecutionMode();

  // If there's a config default, use it without prompting
  // Users who set a default prefer to not be prompted
  if (defaultMode) {
    logger.debug("Using configured default execution mode", { defaultMode });
    return defaultMode;
  }

  // Show mode selection QuickPick
  const selected = await vscode.window.showQuickPick(EXECUTION_MODE_OPTIONS, {
    placeHolder: "Select execution mode",
    title: "Nightgauge: Run Stage - Execution Mode",
  });

  if (!selected) {
    return undefined; // User cancelled
  }

  return selected.mode;
}

/**
 * Show confirmation dialog for backward stage transitions
 *
 * @param stage - The stage being transitioned to
 * @param message - The message explaining the backward transition
 * @returns True if user confirmed, false if cancelled
 */
async function confirmBackwardTransition(stage: PipelineStage, message: string): Promise<boolean> {
  const result = await vscode.window.showQuickPick(
    [
      {
        label: "$(warning) Confirm",
        description: `Go back to ${stage}`,
        value: "confirm" as ConfirmationChoice,
      },
      {
        label: "$(close) Cancel",
        description: "Stay at current stage",
        value: "cancel" as ConfirmationChoice,
      },
    ],
    {
      title: "Confirm Backward Transition",
      placeHolder: message,
    }
  );

  return result?.value === "confirm";
}

/**
 * Available stages with descriptions
 */
const STAGE_OPTIONS: StageOption[] = [
  {
    label: "$(git-pull-request) Issue Pickup",
    description: "Claim a GitHub issue and create feature branch",
    stage: "issue-pickup",
  },
  {
    label: "$(file-text) Feature Planning",
    description: "Create implementation plan from requirements",
    stage: "feature-planning",
  },
  {
    label: "$(code) Feature Development",
    description: "Implement the feature following the plan",
    stage: "feature-dev",
  },
  {
    label: "$(check) Feature Validation",
    description: "Run tests and validate implementation",
    stage: "feature-validate",
  },
  {
    label: "$(git-pull-request) PR Create",
    description: "Create pull request with proper format",
    stage: "pr-create",
  },
  {
    label: "$(git-merge) PR Merge",
    description: "Merge PR after reviews pass",
    stage: "pr-merge",
  },
];

/**
 * Parsed output item - can be text or a tool call
 *
 * @internal Exported for testing — not part of the public API
 */
export interface ParsedOutputItem {
  type: "text" | "text_delta" | "tool" | "content_block_stop";
  text?: string;
  toolCall?: ToolCallData;
}

/**
 * Parse stream-json output from Claude CLI
 * Returns parsed items: text lines or tool call data
 *
 * @internal Exported for testing — not part of the public API
 */
export function parseStreamOutput(data: string): ParsedOutputItem[] {
  const items: ParsedOutputItem[] = [];
  for (const line of data.split("\n")) {
    if (!line.trim()) continue;

    // Filter stream-json envelopes before JSON parsing (Issue #873)
    // Allow content_block_stop through — it signals the end of a content
    // block so the caller can flush accumulated text_delta fragments.
    if (isStreamJsonEnvelope(line)) {
      // Check if this is a content_block_stop we need to pass through
      if (line.trim().startsWith('{"type":"content_block_stop"')) {
        items.push({ type: "content_block_stop" });
      }
      continue;
    }

    try {
      const parsed = JSON.parse(line);

      // Filter raw tool_result JSON from Claude API (Issue #846)
      // These are internal API messages with no user value — tool indicators
      // already provide visibility into tool activity
      if (
        parsed.type === "user" &&
        parsed.message?.role === "user" &&
        Array.isArray(parsed.message?.content) &&
        parsed.message.content.some(
          (c: Record<string, unknown>) => c.type === "tool_result" && c.tool_use_id
        )
      ) {
        continue;
      }

      // Handle different message types from Claude CLI stream-json
      if (parsed.type === "assistant" && parsed.message?.content) {
        // Extract text from content blocks
        for (const block of parsed.message.content) {
          if (block.type === "text" && block.text) {
            items.push({ type: "text", text: block.text });
          } else if (block.type === "tool_use") {
            // Create structured tool call data
            const toolCall = createToolCallData(
              block.name,
              block.input as Record<string, unknown> | undefined,
              true // isActive
            );
            items.push({ type: "tool", toolCall });
          }
        }
      } else if (parsed.type === "content_block_delta" && parsed.delta?.text) {
        // Emit as text_delta so the caller can accumulate fragments and
        // render them as a single block when content_block_stop arrives.
        items.push({ type: "text_delta", text: parsed.delta.text });
      } else if (parsed.type === "content_block_stop") {
        items.push({ type: "content_block_stop" });
      } else if (parsed.type === "token:usage") {
        const input = parsed.inputTokens ?? 0;
        const output = parsed.outputTokens ?? 0;
        const cost = parsed.costUsd ?? 0;
        items.push({
          type: "text",
          text: `Tokens: ${input.toLocaleString()} in, ${output.toLocaleString()} out, $${cost.toFixed(4)}`,
        });
      } else if (parsed.level && parsed.message) {
        items.push({
          type: "text",
          text: String(parsed.message),
        });
      } else if (parsed.type === "stage:start" || parsed.type === "stage:complete") {
        // Suppressed: already reported by onStageStart/onStageComplete callbacks (Issue #770)
        continue;
      } else if (parsed.type === "stage:error" && parsed.stage) {
        const errorMessage =
          parsed.error?.message ?? parsed.error?.toString?.() ?? "Unknown stage error";
        items.push({
          type: "text",
          text: `✗ ${parsed.stage} failed: ${errorMessage}`,
        });
      } else if (parsed.type === "result") {
        // Final result - could include token usage
        if (parsed.result) {
          items.push({ type: "text", text: parsed.result });
        }
      }
    } catch {
      // Not JSON, use as plain text — filter envelope fragments (Issue #873)
      if (line.trim() && !isEnvelopeFragment(line)) {
        items.push({ type: "text", text: line });
      }
    }
  }
  return items;
}

/**
 * Register the Run Stage command
 */
export function registerRunStageCommand(
  _orchestrator: unknown,
  logger: Logger,
  statusBar: StatusBarManager,
  treeProvider: PipelineTreeProvider,
  outputWindow: OutputWindow,
  pipelineStateService?: PipelineStateService | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.runStage",
    async (item?: StageTreeItem | PipelineStage) => {
      // Determine which stage to run
      let stage: PipelineStage;

      // Handle different input types:
      // 1. StageTreeItem object (from tree view click)
      // 2. Raw PipelineStage string (from programmatic calls like handleAutoContinue)
      // 3. undefined (from command palette)
      if (item && typeof item === "object" && "stage" in item) {
        // Tree item with stage property
        stage = item.stage;
      } else if (typeof item === "string") {
        // Raw stage string passed programmatically
        stage = item as PipelineStage;
      } else {
        // No valid input - show stage selector
        const selected = await vscode.window.showQuickPick(STAGE_OPTIONS, {
          placeHolder: "Select a pipeline stage to run",
          title: "Nightgauge: Run Stage",
        });

        if (!selected) {
          return; // User cancelled
        }
        stage = selected.stage;
      }

      // Try to get issue number from tree provider first
      let issueNumber = treeProvider.getCurrentIssueNumber();

      if (!issueNumber) {
        // No issue in context - prompt for issue number
        const input = await vscode.window.showInputBox({
          prompt: "Enter issue number",
          placeHolder: "42",
          validateInput: (value) => {
            const num = parseInt(value, 10);
            if (isNaN(num) || num <= 0) {
              return "Please enter a valid positive issue number";
            }
            return null;
          },
        });

        if (!input) {
          return; // User cancelled
        }

        issueNumber = parseInt(input, 10);
      }

      // Guard: pipeline-start bookend stage must not run manually.
      // It is handled synchronously when issue pickup initializes pipeline state.
      if (stage === "pipeline-start") {
        logger.warn("nightgauge.runStage called with pipeline-start - skipping", {
          stage,
        });
        return;
      }

      // Handle pipeline-finish bookend stage synchronously (no SKILL.md, zero AI tokens).
      if (stage === "pipeline-finish") {
        logger.info("Running pipeline-finish bookend stage via manual run", {
          issueNumber,
        });

        if (!pipelineStateService) {
          logger.warn("Cannot run pipeline-finish: PipelineStateService unavailable");
          vscode.window.showWarningMessage(
            "Cannot complete pipeline: state service is unavailable."
          );
          return;
        }

        try {
          await pipelineStateService.startStage("pipeline-finish", {
            forceBackward: true,
          });
          treeProvider.updateStageStatus("pipeline-finish", "running");
          outputWindow.updateStageStatus("pipeline-finish", "running");
          statusBar.showRunning("pipeline-finish");

          // Brief pause so users see the running transition before completion.
          await new Promise((resolve) => setTimeout(resolve, 500));

          await pipelineStateService.completeStage("pipeline-finish");
          treeProvider.updateStageStatus("pipeline-finish", "complete");
          outputWindow.updateStageStatus("pipeline-finish", "complete");
          outputWindow.appendLine("✓ Completion stage completed", "info", "pipeline-finish");
          statusBar.showComplete("pipeline-finish");
          logger.info("Pipeline-finish stage completed from manual run", {
            issueNumber,
          });
        } catch (error) {
          const message = error instanceof Error ? error.message : "Unknown error";
          logger.error("Pipeline-finish stage failed", {
            issueNumber,
            error: message,
          });
          try {
            await pipelineStateService.failStage("pipeline-finish", message);
          } catch (stateErr) {
            logger.warn("Failed to mark pipeline-finish as failed", {
              stateErr,
            });
          }
          treeProvider.updateStageStatus("pipeline-finish", "failed");
          outputWindow.updateStageStatus("pipeline-finish", "error");
          outputWindow.appendLine(
            `✗ Completion stage failed: ${message}`,
            "error",
            "pipeline-finish"
          );
          statusBar.showError(message);
        }
        return;
      }

      // Guard against overlapping runs triggered from different UI paths
      // (e.g., manual run while retry/auto-continue is still active).
      if (pipelineStateService) {
        try {
          const state = await pipelineStateService.getState();
          const runningStage = state
            ? (Object.entries(state.stages).find(([, s]) => s.status === "running")?.[0] as
                PipelineStage | undefined)
            : undefined;

          if (runningStage) {
            logger.warn("Run stage blocked: another stage is currently running", {
              requestedStage: stage,
              runningStage,
              issueNumber,
            });
            vscode.window.showWarningMessage(
              `Cannot start ${getStageLabel(stage)} while ${getStageLabel(runningStage)} is running.`
            );
            return;
          }
        } catch (error) {
          logger.warn("Failed to check for running stage before manual start", {
            error,
          });
        }
      }

      // Select execution mode (Issue #499)
      // This checks batch mode (forces headless) and config default
      const executionMode = await selectExecutionMode(pipelineStateService ?? null, logger);

      if (!executionMode) {
        // User cancelled mode selection
        return;
      }

      logger.info("Starting stage via execution adapter", {
        stage,
        issueNumber,
        executionMode,
        adapter: getExecutionAdapter(),
      });

      // Validate stage transition before proceeding
      if (pipelineStateService) {
        try {
          const validation = await pipelineStateService.validateStageTransition(stage, issueNumber);

          if (!validation.allowed) {
            if (validation.requiresConfirmation) {
              // Show confirmation dialog for backward transition
              const confirmed = await confirmBackwardTransition(
                stage,
                validation.confirmationMessage || "Proceed with backward transition?"
              );

              if (!confirmed) {
                logger.info("User cancelled backward transition", { stage });
                vscode.window.showInformationMessage(`Stage transition to ${stage} cancelled.`);
                return;
              }
              logger.info("User confirmed backward transition", { stage });
            } else {
              // Hard block - show error
              logger.error("Stage transition blocked", {
                stage,
                reason: validation.error,
              });
              vscode.window.showErrorMessage(validation.error || "Stage transition blocked");
              return;
            }
          }
        } catch (error) {
          logger.warn("Failed to validate stage transition", { error });
          // Continue anyway - validation is a safety net, not a blocker
        }
      }

      // Set execution mode to 'manual' if not already set
      // This ensures proper notification behavior for individual stage runs
      if (pipelineStateService) {
        try {
          const currentMode = await pipelineStateService.getExecutionMode();
          if (!currentMode) {
            // First stage run manually - set to manual mode
            await pipelineStateService.setExecutionMode("manual");
            logger.debug("Set execution mode to manual for individual stage run");
          }
          // Also resume pipeline if it was paused (user explicitly ran a stage)
          const isPaused = await pipelineStateService.isPaused();
          if (isPaused) {
            await pipelineStateService.resumePipeline();
            logger.debug("Resumed pipeline (user ran stage while paused)");
          }
        } catch (error) {
          logger.warn("Failed to check/set execution mode", { error });
        }
      }

      // Mark stage as running in state service (Issue #246 fix)
      // This was missing — stages went from 'pending' directly to 'complete'
      if (pipelineStateService) {
        try {
          await pipelineStateService.startStage(stage);
        } catch (error) {
          logger.warn("Failed to update pipeline state on stage start", {
            stage,
            error,
          });
        }
      }

      // Update OutputWindow with starting status — this command is
      // invoked by an explicit user action (Run Stage), so reveal the
      // panel to the foreground.
      outputWindow.reveal();
      outputWindow.setIssueNumber(issueNumber);
      outputWindow.updateStageStatus(stage, "running");
      outputWindow.appendLine(
        `Starting ${getStageLabel(stage)} for issue #${issueNumber}...`,
        "info",
        stage
      );
      outputWindow.appendLine(`Execution adapter: ${getExecutionAdapter()}`, "info", stage);

      statusBar.showRunning(stage, executionMode);

      // Update tree provider to show running state (Issue #246 fix)
      treeProvider.updateStageStatus(stage, "running");

      // Record execution mode in stage state (Issue #499)
      if (pipelineStateService) {
        try {
          await pipelineStateService.setStageExecutionMode(stage, executionMode);
        } catch (error) {
          logger.warn("Failed to record stage execution mode", {
            stage,
            executionMode,
            error,
          });
        }
      }

      // Track previous cumulative totals to compute deltas for updateTokens().
      // onTokenUsage receives cumulative running totals from TokenAccumulator,
      // but updateTokens() is additive. @see Issue #843
      let prevUsage = {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      };

      // Phase tracker for pipeline tree view progress (@see Issue #1115)
      const phaseTracker = pipelineStateService ? createPhaseTracker(pipelineStateService) : null;

      /**
       * Emit text to the output window, suppressing phase markers.
       * Phase markers are detected and forwarded to the phase tracker.
       */
      function emitText(text: string, level: "info" | "error" | "warning" = "info") {
        if (phaseTracker) {
          const marker = parsePhaseMarker(text);
          if (marker) {
            phaseTracker.onPhaseDetected(stage, marker);
            return; // Suppress marker from output
          }
        }
        outputWindow.appendLine(text, level, stage);
      }

      // Accumulate streaming text_delta fragments into complete content
      // blocks before passing to appendLine().  Individual deltas are too
      // small for reliable content-type detection (code vs text), causing
      // fragmented rendering where code breaks in and out of code blocks.
      let stdoutDeltaBuffer = "";
      let stderrDeltaBuffer = "";

      function flushStdoutDelta() {
        if (stdoutDeltaBuffer) {
          emitText(stdoutDeltaBuffer);
          stdoutDeltaBuffer = "";
        }
      }

      function flushStderrDelta() {
        if (stderrDeltaBuffer) {
          const isError =
            stderrDeltaBuffer.toLowerCase().includes("error") ||
            stderrDeltaBuffer.toLowerCase().includes("failed");
          outputWindow.appendLine(stderrDeltaBuffer, isError ? "error" : "warning", stage);
          stderrDeltaBuffer = "";
        }
      }

      // Set up callbacks to stream output to OutputWindow
      const callbacks: SkillRunCallbacks = {
        onStdout: (data) => {
          const items = parseStreamOutput(data);
          for (const item of items) {
            if (item.type === "text_delta" && item.text) {
              // Accumulate streaming fragments
              stdoutDeltaBuffer += item.text;
            } else if (item.type === "content_block_stop") {
              // Content block complete — flush accumulated text as one entry
              flushStdoutDelta();
            } else if (item.type === "text" && item.text) {
              // Non-streaming text (assistant messages, results) — flush
              // any pending delta first, then append directly
              flushStdoutDelta();
              emitText(item.text);
            } else if (item.type === "tool" && item.toolCall) {
              flushStdoutDelta();
              outputWindow.logToolIndicator(item.toolCall, stage);
            }
          }
        },
        onStderr: (data) => {
          // Stderr often contains progress info, not just errors
          const items = parseStreamOutput(data);
          for (const item of items) {
            if (item.type === "text_delta" && item.text) {
              stderrDeltaBuffer += item.text;
            } else if (item.type === "content_block_stop") {
              flushStderrDelta();
            } else if (item.type === "text" && item.text) {
              flushStderrDelta();
              const isError =
                item.text.toLowerCase().includes("error") ||
                item.text.toLowerCase().includes("failed");
              outputWindow.appendLine(item.text, isError ? "error" : "warning", stage);
            } else if (item.type === "tool" && item.toolCall) {
              flushStderrDelta();
              outputWindow.logToolIndicator(item.toolCall, stage);
            }
          }
        },
        // Token usage callback - updates PipelineStateService (Issue #39 fix)
        // Convert cumulative totals to deltas before passing to updateTokens(). @see Issue #843
        onTokenUsage: (usage) => {
          const delta = {
            inputTokens: usage.inputTokens - prevUsage.inputTokens,
            outputTokens: usage.outputTokens - prevUsage.outputTokens,
            cacheReadTokens: usage.cacheReadTokens - prevUsage.cacheReadTokens,
            cacheCreationTokens: usage.cacheCreationTokens - prevUsage.cacheCreationTokens,
            costUsd: usage.costUsd - prevUsage.costUsd,
          };
          prevUsage = { ...usage };

          if (pipelineStateService) {
            pipelineStateService
              .updateTokens({
                inputTokens: delta.inputTokens,
                outputTokens: delta.outputTokens,
                cacheReadTokens: delta.cacheReadTokens,
                cacheCreationTokens: delta.cacheCreationTokens,
                costUsd: delta.costUsd,
                stage,
              })
              .catch((err) => {
                logger.warn("Failed to update pipeline state tokens", { err });
              });
          }

          logger.debug("Token usage from runStage", {
            stage,
            inputTokens: usage.inputTokens,
            outputTokens: usage.outputTokens,
          });
        },
        // Tool use callback - handles interactive tools like AskUserQuestion (Issue #118)
        onToolUse: async (toolName, toolInput, toolUseId) => {
          // Check if this is an AskUserQuestion tool call
          if (toolName === "AskUserQuestion") {
            logger.info("AskUserQuestion tool detected", {
              stage,
              toolUseId,
            });

            // Validate the payload
            const payload = validateAskUserQuestionPayload(toolInput);
            if (!payload) {
              logger.warn("Invalid AskUserQuestion payload", { toolInput });
              return;
            }

            // Show the question prompt in OutputWindow and wait for response
            const response = await outputWindow.showQuestionPrompt(payload, toolUseId);

            // Send the response to the active process stdin
            if (response) {
              const formattedResponse = formatResponseForStdin(response);
              logger.debug("Sending question response to stdin", {
                response: formattedResponse,
              });

              const sent = sendInputToActiveProcess(formattedResponse);

              if (!sent) {
                logger.warn("Failed to send question response to process", {
                  stage,
                  toolUseId,
                });
              }
            } else {
              // User cancelled/skipped - send empty response to continue
              logger.info("User skipped question", { stage, toolUseId });
              sendInputToActiveProcess(JSON.stringify({ answers: {} }));
            }
          }
        },
        onComplete: async (result) => {
          // Flush any remaining accumulated text_delta content
          // (phase markers in the buffer are detected during flush)
          flushStdoutDelta();
          flushStderrDelta();

          // Complete the last running phase before marking stage done
          phaseTracker?.completeStagePhases(stage);

          if (result.success) {
            logger.info("Stage completed successfully", { stage, issueNumber });

            // Mark stage complete in state service FIRST (before UI updates)
            // This ensures state.json always reflects completion status
            // Issue #164 fix: Was only called for final stage (pr-merge)
            if (pipelineStateService) {
              try {
                // Attribute the stage to the served model (#91) + executing
                // adapter so the Go notify handler records them for BuildV2Record
                // (#268: by-model cost breakdown + Adapter Mix donut).
                await pipelineStateService.completeStage(stage, {
                  model: result.servedModel ?? result.modelDecision?.model,
                  adapter: result.adapterDecision?.adapter,
                });
              } catch (err) {
                logger.warn("Failed to mark stage complete in state service", {
                  stage,
                  err,
                });
              }
            }

            // Show tool usage summary before status update
            outputWindow.showToolSummary();

            outputWindow.updateStageStatus(stage, "complete");
            outputWindow.appendLine(`✓ ${getStageLabel(stage)} completed`, "info", stage);
            statusBar.showComplete(stage);

            // ===================================================================
            // NOTE: Auto-continue logic was REMOVED in Issue #531.
            //
            // The nightgauge.runStage command is now for manual single-stage execution ONLY.
            // Full pipeline runs should use nightgauge.pickupIssue, which routes through
            // HeadlessOrchestrator.runPipeline() for unified execution.
            //
            // This prevents duplicate execution paths (command chain vs batch loop)
            // and ensures consistent behavior for epic pre-checks, routing, stop button.
            // ===================================================================
            logger.info("Manual stage run complete - no auto-continue", {
              stage,
              issueNumber,
            });
          } else {
            logger.error("Stage failed", {
              stage,
              issueNumber,
              error: result.error?.message ?? "Unknown error",
            });

            // Mark stage failed in state service
            // Issue #164 fix: Was never called, stages stayed 'running' forever on failure
            if (pipelineStateService) {
              try {
                await pipelineStateService.failStage(
                  stage,
                  result.error?.message || "Unknown error"
                );
              } catch (err) {
                logger.warn("Failed to mark stage failed in state service", {
                  stage,
                  err,
                });
              }
            }

            outputWindow.updateStageStatus(stage, "error");
            outputWindow.appendLine(
              `✗ ${getStageLabel(stage)} failed: ${result.error?.message || "Unknown error"}`,
              "error",
              stage
            );
            statusBar.showError(result.error?.message || "Stage failed");
          }
        },
        onError: (error) => {
          logger.error("Stage execution error", { stage, issueNumber, error });
          outputWindow.appendLine(`Error: ${error.message}`, "error", stage);
        },
        // Execution mode callback - updates OutputWindow mode indicator (Issue #496)
        onMode: (mode) => {
          logger.debug("Execution mode set", { stage, mode });
          outputWindow.setMode(mode);
        },
      };

      // Run stage via selected execution adapter
      // SKILL.md instructions are passed as prompt
      // Each command starts a NEW conversation (context isolation)
      // Use appropriate runner based on execution mode (Issue #499)
      try {
        const handle =
          executionMode === "interactive"
            ? runStageSkillInteractive(stage, issueNumber, callbacks)
            : runStageSkillHeadless(stage, issueNumber, callbacks);

        // A failed launch returns an error stub (no child process, no stdin
        // writer) after reporting via callbacks — bail then. The Codex
        // interactive TUI path (#4024) has no child process (it runs in a
        // terminal) but exposes writeToStdin, so it is NOT a bail. Mirrors the
        // guard in runInteractiveStage.ts.
        if (!handle.process && !handle.writeToStdin) {
          // Error already reported via callbacks
          return;
        }

        const modeLabel = executionMode === "interactive" ? "interactive" : "headless";
        outputWindow.appendLine(
          `Running ${getStageLabel(stage)} in ${modeLabel} mode...`,
          "info",
          stage
        );

        // For interactive mode, remind user how to send messages
        if (executionMode === "interactive") {
          outputWindow.appendLine(
            "Tip: Use the message input field below to send follow-up messages.",
            "info",
            stage
          );
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to start stage", error instanceof Error ? error : undefined);
        statusBar.showError(message);
        outputWindow.updateStageStatus(stage, "error");
        outputWindow.appendLine(`Failed to start: ${message}`, "error", stage);
        vscode.window.showErrorMessage(`Failed to start stage: ${message}`);
      }
    }
  );
}
