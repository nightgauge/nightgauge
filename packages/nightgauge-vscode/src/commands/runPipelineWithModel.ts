/**
 * Run Pipeline with Model command (Issue #1610)
 *
 * Allows users to select a specific model for an entire pipeline run,
 * overriding automatic model routing. The override is per-run only and does
 * not persist to config.
 *
 * Flow: user selects model → override is stored on the orchestrator →
 * pickupIssue command is invoked → runPipeline() consumes the override.
 */

import * as vscode from "vscode";
import { ReadyIssueTreeItem } from "../views/items/ReadyIssueTreeItem";
import type { Logger } from "../utils/logger";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { StatusBarManager } from "../utils/statusBar";
import {
  getExecutionAdapter,
  getCodexModel,
  type PipelineModelOverride,
} from "../utils/incrediConfig";
import { CODEX_RECOMMENDED_DEFAULT_MODEL } from "@nightgauge/sdk";
import { CodexModelCatalogService } from "../services/CodexModelCatalogService";

interface ModelOption extends vscode.QuickPickItem {
  model: PipelineModelOverride;
  displayLabel: string;
}

const CLAUDE_MODEL_OPTIONS: ModelOption[] = [
  {
    label: "$(rocket) Opus",
    description: "Most capable — recommended default for hard work",
    model: "opus",
    displayLabel: "Opus",
  },
  {
    label: "$(zap) Sonnet",
    description: "Balanced — speed and capability",
    model: "sonnet",
    displayLabel: "Sonnet",
  },
  {
    label: "$(dashboard) Haiku",
    description: "Fastest — lightweight tasks",
    model: "haiku",
    displayLabel: "Haiku",
  },
  {
    label: "$(star-full) Fable 5",
    description: "Premium frontier tier — ~2× Opus cost. Use deliberately.",
    detail:
      "Most powerful model. Reserve for the hardest reasoning; Opus 4.8 is already state-of-the-art for most coding.",
    model: "fable",
    displayLabel: "Fable 5",
  },
];

function getCodexModelOptions(currentModel: string): ModelOption[] {
  const recommendedModels = new CodexModelCatalogService().listModels();

  const models =
    currentModel && !recommendedModels.includes(currentModel)
      ? [currentModel, ...recommendedModels]
      : recommendedModels;

  return models.map((model) => ({
    label: model === currentModel ? `${model} (Configured)` : model,
    description: model === CODEX_RECOMMENDED_DEFAULT_MODEL ? "Recommended default" : undefined,
    model,
    displayLabel: model,
  }));
}

function getModelOptionsForAdapter(
  adapter: ReturnType<typeof getExecutionAdapter>,
  workspaceRoot?: string
): ModelOption[] | null {
  if (adapter === "claude") {
    return CLAUDE_MODEL_OPTIONS;
  }

  if (adapter === "codex") {
    return getCodexModelOptions(getCodexModel(workspaceRoot));
  }

  return null;
}

export function registerRunPipelineWithModelCommand(
  logger: Logger,
  headlessOrchestrator: HeadlessOrchestrator | null,
  statusBar?: StatusBarManager
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.runPipelineWithModel",
    async (item?: ReadyIssueTreeItem) => {
      if (!headlessOrchestrator) {
        vscode.window.showErrorMessage("Pipeline orchestrator is not available.");
        return;
      }

      const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
      const adapter = getExecutionAdapter(workspaceRoot);
      const modelOptions = getModelOptionsForAdapter(adapter, workspaceRoot);

      if (!modelOptions) {
        vscode.window.showWarningMessage(
          `Run Pipeline with Model currently supports Claude and Codex. Current adapter: ${adapter}.`
        );
        return;
      }

      // Show model selection QuickPick
      const selected = await vscode.window.showQuickPick(modelOptions, {
        placeHolder:
          adapter === "codex"
            ? "Select Codex model for this pipeline run"
            : "Select Claude model for this pipeline run",
        title: "Nightgauge: Run Pipeline with Model",
      });

      if (!selected) {
        return; // User cancelled
      }

      logger.info("User selected model override for pipeline run", {
        model: selected.model,
        issueNumber: item instanceof ReadyIssueTreeItem ? item.issueNumber : undefined,
      });

      // Store the override — runPipeline() will consume it
      headlessOrchestrator.setNextRunModelOverride(selected.model);

      // Show model override in status bar during the run
      statusBar?.setModelOverrideLabel(selected.displayLabel);

      // Delegate to the existing pickupIssue command which routes through
      // HeadlessOrchestrator.runPipeline() for unified execution.
      await vscode.commands.executeCommand("nightgauge.pickupIssue", item);
    }
  );
}
