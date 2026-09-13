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
  getOpenCodeModel,
  type PipelineModelOverride,
} from "../utils/nightgaugeConfig";
import { CODEX_RECOMMENDED_DEFAULT_MODEL } from "@nightgauge/sdk";
import { CodexModelCatalogService } from "../services/CodexModelCatalogService";
import { OpenCodeModelCatalogService } from "../services/OpenCodeModelCatalogService";

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

/** The result of resolving model options for the current adapter. */
interface ModelOptionsResult {
  /** `null` when the adapter has no model picker at all (unsupported). */
  options: ModelOption[] | null;
  /**
   * Set when `options` is an empty array — i.e. the adapter has a picker but
   * nothing selectable came back — so the caller can explain why instead of
   * opening a blank QuickPick.
   */
  emptyMessage?: string;
}

/**
 * OpenCode model options for "Run Pipeline with Model" (Issue #1628).
 *
 * Only `selectable` catalog entries become QuickPick items: the "could not
 * list models" notice the catalog service returns on failure is informational
 * and must never be offered or stored as a run override. When filtering
 * leaves nothing selectable, surface that notice's own label as the reason
 * instead of silently opening an empty picker.
 */
async function getOpenCodeModelOptions(workspaceRoot?: string): Promise<ModelOptionsResult> {
  const configuredModel = getOpenCodeModel(workspaceRoot);
  const catalog = await new OpenCodeModelCatalogService().listModels(configuredModel);

  const options = catalog
    .filter((entry) => entry.selectable)
    .map((entry) => ({
      label: entry.label,
      model: entry.id,
      displayLabel: entry.id,
    }));

  if (options.length > 0) {
    return { options };
  }

  const notice = catalog.find((entry) => !entry.selectable);
  return {
    options,
    emptyMessage:
      notice?.label ??
      "OpenCode has no models to select. Set opencode.model or check the opencode " +
        "CLI, then try again.",
  };
}

async function getModelOptionsForAdapter(
  adapter: ReturnType<typeof getExecutionAdapter>,
  workspaceRoot?: string
): Promise<ModelOptionsResult> {
  if (adapter === "claude") {
    return { options: CLAUDE_MODEL_OPTIONS };
  }

  if (adapter === "codex") {
    return { options: getCodexModelOptions(getCodexModel(workspaceRoot)) };
  }

  if (adapter === "opencode") {
    return getOpenCodeModelOptions(workspaceRoot);
  }

  return { options: null };
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
      const { options: modelOptions, emptyMessage } = await getModelOptionsForAdapter(
        adapter,
        workspaceRoot
      );

      if (!modelOptions) {
        vscode.window.showWarningMessage(
          `Run Pipeline with Model currently supports Claude, Codex, and OpenCode. ` +
            `Current adapter: ${adapter}.`
        );
        return;
      }

      if (modelOptions.length === 0) {
        vscode.window.showWarningMessage(
          emptyMessage ?? `No models are available to select for the ${adapter} adapter.`
        );
        return;
      }

      // Show model selection QuickPick
      const selected = await vscode.window.showQuickPick(modelOptions, {
        placeHolder:
          adapter === "codex"
            ? "Select Codex model for this pipeline run"
            : adapter === "opencode"
              ? "Select OpenCode model for this pipeline run"
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
