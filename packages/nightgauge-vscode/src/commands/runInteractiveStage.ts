/**
 * Run Interactive Stage command
 *
 * Runs a single pipeline stage in interactive (conversational) mode.
 * This is a shortcut command that bypasses the mode selection QuickPick
 * and directly runs the stage in interactive mode.
 *
 * Interactive mode:
 * - Does NOT use the -p flag (stdin stays open)
 * - Does NOT produce stream-json output (no token tracking)
 * - Supports mid-execution user input via the message input field
 *
 * **IMPORTANT**: Interactive mode is NOT supported for:
 * - Multi-stage pipelines (use headless mode via Run Stage)
 * - Batch processing (automatically uses headless)
 *
 * @see docs/INTERACTIVE_MODE.md
 * @see Issue #499 - Mode selection UX
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import type { StageTreeItem, PipelineTreeProvider, OutputWindow } from "../views";
import type { PipelineStateService } from "../services/PipelineStateService";
import {
  runStageSkillInteractive,
  getStageLabel,
  getNextStage,
  type SkillRunCallbacks,
} from "../utils/skillRunner";
import { createToolCallData, type ToolCallData } from "../views/outputWindow/ToolCallIndicator";
import { getExecutionAdapter } from "../utils/incrediConfig";

/**
 * Stage options for quick pick
 */
interface StageOption extends vscode.QuickPickItem {
  stage: PipelineStage;
}

/**
 * Available stages with descriptions (same as runStage.ts)
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
 * Parse raw text output from interactive mode
 * Unlike headless mode, interactive mode produces raw text, not JSON
 */
function parseInteractiveOutput(data: string): string[] {
  return data.split("\n").filter((line) => line.trim());
}

/**
 * Register the Run Interactive Stage command
 *
 * This command runs a stage directly in interactive mode, bypassing
 * the mode selection QuickPick. Use this when you want to have a
 * conversation with the AI during stage execution.
 *
 * @see Issue #499 - Mode selection UX
 */
export function registerRunInteractiveStageCommand(
  logger: Logger,
  statusBar: StatusBarManager,
  treeProvider: PipelineTreeProvider,
  outputWindow: OutputWindow,
  pipelineStateService?: PipelineStateService | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.runInteractiveStage",
    async (item?: StageTreeItem | PipelineStage) => {
      const adapter = getExecutionAdapter();
      // Claude (piped-stdio) and Codex (TUI in a terminal, #4024) support
      // interactive mode; all other adapters are headless-only.
      if (adapter !== "claude" && adapter !== "codex") {
        logger.warn("Interactive mode requires the Claude or Codex adapter", { adapter });
        vscode.window.showWarningMessage(
          'Interactive mode requires the Claude or Codex adapter. Switch via "Nightgauge: Switch Execution Adapter".'
        );
        return;
      }

      // Determine which stage to run
      let stage: PipelineStage;

      if (item && typeof item === "object" && "stage" in item) {
        stage = item.stage;
      } else if (typeof item === "string") {
        stage = item as PipelineStage;
      } else {
        // Show stage selector
        const selected = await vscode.window.showQuickPick(STAGE_OPTIONS, {
          placeHolder: "Select a pipeline stage to run in interactive mode",
          title: "Nightgauge: Run Interactive Stage",
        });

        if (!selected) {
          return;
        }
        stage = selected.stage;
      }

      // Get issue number
      let issueNumber = treeProvider.getCurrentIssueNumber();

      if (!issueNumber) {
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
          return;
        }

        issueNumber = parseInt(input, 10);
      }

      // Guard: bookend stages cannot be run
      if (stage === "pipeline-start" || stage === "pipeline-finish") {
        logger.warn("nightgauge.runInteractiveStage called with bookend stage - skipping", {
          stage,
        });
        return;
      }

      logger.info("Starting stage in INTERACTIVE mode via Claude Code CLI", {
        stage,
        issueNumber,
      });

      // Validate stage transition
      if (pipelineStateService) {
        try {
          const validation = await pipelineStateService.validateStageTransition(stage, issueNumber);

          if (!validation.allowed && !validation.requiresConfirmation) {
            logger.error("Stage transition blocked", {
              stage,
              reason: validation.error,
            });
            vscode.window.showErrorMessage(validation.error || "Stage transition blocked");
            return;
          }
        } catch (error) {
          logger.warn("Failed to validate stage transition", { error });
        }
      }

      // Set execution mode to 'manual' for interactive runs
      if (pipelineStateService) {
        try {
          await pipelineStateService.setExecutionMode("manual");
          const isPaused = await pipelineStateService.isPaused();
          if (isPaused) {
            await pipelineStateService.resumePipeline();
          }
        } catch (error) {
          logger.warn("Failed to set execution mode", { error });
        }
      }

      // Start stage in pipeline state
      if (pipelineStateService) {
        try {
          await pipelineStateService.startStage(stage);
          // Record that this stage is running in interactive mode
          await pipelineStateService.setStageExecutionMode(stage, "interactive");
        } catch (error) {
          logger.warn("Failed to update pipeline state", { stage, error });
        }
      }

      // Update UI — this command is invoked by an explicit user action
      // (Run Stage in Interactive Mode), so reveal the panel to the
      // foreground.
      outputWindow.reveal();
      outputWindow.setIssueNumber(issueNumber);
      outputWindow.updateStageStatus(stage, "running");
      outputWindow.appendLine(
        `Starting ${getStageLabel(stage)} for issue #${issueNumber} in INTERACTIVE mode...`,
        "info",
        stage
      );
      outputWindow.appendLine(
        "Interactive mode: Token tracking unavailable. Use the message input to send follow-up messages.",
        "warning",
        stage
      );

      statusBar.showRunning(stage, "interactive");
      treeProvider.updateStageStatus(stage, "running");

      // Set up callbacks
      const callbacks: SkillRunCallbacks = {
        onStdout: (data) => {
          const lines = parseInteractiveOutput(data);
          for (const line of lines) {
            outputWindow.appendLine(line, "info", stage);
          }
        },
        onStderr: (data) => {
          const lines = parseInteractiveOutput(data);
          for (const line of lines) {
            const isError =
              line.toLowerCase().includes("error") || line.toLowerCase().includes("failed");
            outputWindow.appendLine(line, isError ? "error" : "warning", stage);
          }
        },
        // Note: onTokenUsage will NOT fire in interactive mode
        // because there's no stream-json output to parse
        onComplete: async (result) => {
          if (result.success) {
            logger.info("Interactive stage completed", { stage, issueNumber });

            if (pipelineStateService) {
              try {
                // Attribute the stage to its served/requested model + adapter
                // (#268) so the Go notify handler records them for BuildV2Record.
                // In interactive mode there is no stream-json to parse, so
                // servedModel is usually absent — modelDecision.model (the
                // requested model, which equals what served) is the fallback.
                await pipelineStateService.completeStage(stage, {
                  model: result.servedModel ?? result.modelDecision?.model,
                  adapter: result.adapterDecision?.adapter,
                });
              } catch (err) {
                logger.warn("Failed to mark stage complete", { stage, err });
              }
            }

            outputWindow.updateStageStatus(stage, "complete");
            outputWindow.appendLine(
              `✓ ${getStageLabel(stage)} completed (interactive mode)`,
              "info",
              stage
            );
            statusBar.showComplete(stage);

            // Show next step suggestion (no auto-continue in interactive mode)
            const nextStage = getNextStage(stage);
            if (nextStage && nextStage !== "pipeline-finish") {
              vscode.window
                .showInformationMessage(
                  `${getStageLabel(stage)} complete. Next: ${getStageLabel(nextStage)}`,
                  "Run Next Stage"
                )
                .then((selection) => {
                  if (selection === "Run Next Stage") {
                    vscode.commands.executeCommand("nightgauge.runStage", nextStage);
                  }
                });
            }
          } else {
            logger.error("Interactive stage failed", {
              stage,
              issueNumber,
              error: result.error,
            });

            if (pipelineStateService) {
              try {
                await pipelineStateService.failStage(
                  stage,
                  result.error?.message || "Unknown error"
                );
              } catch (err) {
                logger.warn("Failed to mark stage failed", { stage, err });
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
          logger.error("Interactive stage error", {
            stage,
            issueNumber,
            error,
          });
          outputWindow.appendLine(`Error: ${error.message}`, "error", stage);
        },
        onMode: (mode) => {
          logger.debug("Execution mode confirmed", { stage, mode });
          outputWindow.setMode(mode);
        },
      };

      // Run in interactive mode
      try {
        const handle = runStageSkillInteractive(stage, issueNumber, callbacks);

        // A failed launch returns an error stub (no child process, no stdin
        // writer) after firing onError/onComplete — bail then. The Codex TUI
        // path (#4024) legitimately has no child process (it runs in a terminal)
        // but exposes writeToStdin, so it is NOT a bail.
        if (!handle.process && !handle.writeToStdin) {
          return;
        }

        outputWindow.appendLine(
          `Running ${getStageLabel(stage)} in interactive mode...`,
          "info",
          stage
        );
        outputWindow.appendLine(
          "Tip: Use the message input field below to send follow-up messages.",
          "info",
          stage
        );
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to start interactive stage", { error });
        statusBar.showError(message);
        outputWindow.updateStageStatus(stage, "error");
        outputWindow.appendLine(`Failed to start: ${message}`, "error", stage);
        vscode.window.showErrorMessage(`Failed to start stage: ${message}`);
      }
    }
  );
}
