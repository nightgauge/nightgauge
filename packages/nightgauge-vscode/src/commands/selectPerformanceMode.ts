/**
 * Select Performance Mode command (Issue #3009, Issue #20)
 *
 * Opens a QuickPick that lets the user pick a performance mode
 * (Efficiency / Elevated / Maximum / Frontier) or, via the "Custom…" entry, pin
 * an explicit model per pipeline stage. Preset selections persist to
 * `.nightgauge/performance-mode.yaml`; the Custom flow persists to the
 * existing `model_routing.mode` + `pipeline.stage_models` config surface. The
 * status bar refreshes immediately.
 *
 * Selecting a preset clears any custom per-stage pins so presets behave as
 * advertised rather than being silently overridden by stale overrides.
 *
 * UI patterns (separator + `$(check)` prefix) match
 * `commands/filterRepositoriesView.ts`.
 *
 * @see docs/decisions/012-performance-mode-envelopes.md — Proposal B
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import {
  getPerformanceMode,
  writePerformanceModeStateFile,
  PERFORMANCE_MODES,
  MODE_PROFILES,
  type PerformanceMode,
} from "../utils/incrediConfig";
import {
  CUSTOM_SELECTABLE_STAGES,
  STAGE_MODEL_CHOICES,
  readStageModelSelections,
  writeStageModelSelections,
  clearStageModelSelections,
  hasCustomStageOverrides,
  type StageModelChoice,
} from "../utils/customStageModels";
import type { PipelineStage } from "@nightgauge/sdk";
import { resolveModelForAdapter } from "@nightgauge/sdk";
import { getExecutionAdapter, type ExecutionAdapter } from "../utils/resolvers/modelResolver";

interface ModeQuickPickItem extends vscode.QuickPickItem {
  /** A preset mode, or the "custom" sentinel that opens the per-stage picker. */
  action: PerformanceMode | "custom";
}

function getWorkspaceRoot(): string | undefined {
  return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
}

/** Human-facing label for a stage-model choice. */
function providerModelLabel(
  choice: Exclude<StageModelChoice, "auto">,
  adapter: ExecutionAdapter
): string {
  return (
    resolveModelForAdapter(adapter, choice)?.display_name ??
    resolveModelForAdapter(adapter, choice)?.id ??
    choice.charAt(0).toUpperCase() + choice.slice(1)
  );
}

/** Human-facing provider-aware presentation for one performance mode. */
export function getModePresentation(
  mode: PerformanceMode,
  adapter: ExecutionAdapter
): { description: string; costHint: string } {
  const profile = MODE_PROFILES[mode];
  const floor = providerModelLabel(profile.envelope?.floor ?? "haiku", adapter);
  const ceiling = providerModelLabel(profile.envelope?.ceiling ?? "opus", adapter);
  const provider = adapter === "claude" ? "Claude" : adapter === "codex" ? "Codex" : adapter;
  const descriptions: Record<PerformanceMode, string> = {
    efficiency: `Cheap and fast — ${provider} routing constrained to ${floor}…${ceiling}.`,
    elevated: `Balanced default — adaptive ${provider} routing, ${floor}…${ceiling}.`,
    maximum: `Best-effort quality — ${ceiling} + high effort everywhere, no budget ceiling.`,
    frontier: `Premium opt-in — ${provider} may reach ${ceiling} on eligible hard reasoning stages.`,
  };
  const costHints: Record<PerformanceMode, string> = {
    efficiency: "lower cost",
    elevated: "baseline",
    maximum: "higher cost",
    frontier: "highest-capability opt-in",
  };
  return { description: descriptions[mode], costHint: costHints[mode] };
}

function choiceLabel(choice: StageModelChoice, adapter: ExecutionAdapter): string {
  if (choice === "auto") return "Auto";
  return `${providerModelLabel(choice, adapter)} (${choice} tier)`;
}

/** Short capability/cost hint per stage-model choice. */
function choiceDetail(choice: StageModelChoice, adapter: ExecutionAdapter): string {
  switch (choice) {
    case "auto":
      return "Defer to the adaptive router (recommended)";
    case "haiku":
      return "Cheapest — plumbing / lightweight stages";
    case "sonnet":
      return `Balanced — ${providerModelLabel(choice, adapter)}`;
    case "opus":
      return "Strongest general — deep reasoning";
    case "fable":
      return `Frontier tier — ${providerModelLabel(choice, adapter)}. Deliberate opt-in.`;
  }
}

/**
 * Multi-step "hub" picker: shows every stage with its current model, lets the
 * user drill into a stage to change it, and saves on confirm. Returns true when
 * the user saved, false when they cancelled.
 */
async function runCustomStageModelPicker(
  root: string,
  logger: Logger,
  statusBar?: StatusBarManager
): Promise<boolean> {
  const selections = readStageModelSelections(root);
  const adapter = getExecutionAdapter(root);

  // Loop the hub until the user saves or cancels.
  for (;;) {
    const stageItems: Array<vscode.QuickPickItem & { stage?: PipelineStage; save?: boolean }> =
      CUSTOM_SELECTABLE_STAGES.map((stage) => ({
        label: stage,
        description: choiceLabel(selections[stage], adapter),
        stage,
      }));

    const saveItem: vscode.QuickPickItem & { save?: boolean } = {
      label: "$(check) Save custom models",
      detail: "Persist selections and switch routing to hybrid",
      save: true,
    };
    const separator: vscode.QuickPickItem = {
      label: "",
      kind: vscode.QuickPickItemKind.Separator,
    };

    const picked = await vscode.window.showQuickPick([...stageItems, separator, saveItem], {
      title: "Custom Models — pick a stage to change, then Save",
      placeHolder: "Auto = defer to the adaptive router",
      matchOnDescription: true,
    });

    if (!picked) return false; // cancelled
    if ("save" in picked && picked.save) {
      writeStageModelSelections(root, selections);
      logger.info("Custom per-stage models saved", { selections });
      statusBar?.setCustomOverridesActive(hasCustomStageOverrides(root));
      const pinned = CUSTOM_SELECTABLE_STAGES.filter((s) => selections[s] !== "auto").length;
      vscode.window.showInformationMessage(
        pinned === 0
          ? "Custom models cleared — all stages on Auto (adaptive router)."
          : `Custom models saved — ${pinned} stage${pinned === 1 ? "" : "s"} pinned, routing set to hybrid.`
      );
      return true;
    }

    const stage = (picked as { stage?: PipelineStage }).stage;
    if (!stage) continue;

    // Drill-in: choose the model for this stage.
    const modelItems: Array<vscode.QuickPickItem & { choice: StageModelChoice }> =
      STAGE_MODEL_CHOICES.map((choice) => ({
        label: `${selections[stage] === choice ? "$(check) " : "      "}${choiceLabel(choice, adapter)}`,
        detail: choiceDetail(choice, adapter),
        choice,
      }));

    const chosen = await vscode.window.showQuickPick(modelItems, {
      title: `Model for ${stage}`,
      placeHolder: `Current: ${choiceLabel(selections[stage], adapter)}`,
      matchOnDetail: true,
    });
    if (chosen) {
      selections[stage] = chosen.choice;
    }
    // Loop back to the hub regardless.
  }
}

/**
 * Register the `nightgauge.selectPerformanceMode` command.
 *
 * No-op when no workspace is open. The status-bar item is updated
 * immediately on selection so the badge reflects the new mode without
 * waiting for a pipeline run.
 */
export function registerSelectPerformanceModeCommand(
  logger: Logger,
  statusBar?: StatusBarManager
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.selectPerformanceMode", async () => {
    const root = getWorkspaceRoot();
    if (!root) {
      vscode.window.showErrorMessage("No workspace open — cannot change performance mode.");
      return;
    }

    const currentMode = getPerformanceMode(root);
    const effectiveAdapter = getExecutionAdapter(root);
    const customActive = hasCustomStageOverrides(root);
    const items: ModeQuickPickItem[] = PERFORMANCE_MODES.map((mode) => {
      const profile = MODE_PROFILES[mode];
      const presentation = getModePresentation(mode, effectiveAdapter);
      // A preset is "current" only when no custom pins are shadowing it.
      const isCurrent = !customActive && mode === currentMode;
      return {
        label: `${isCurrent ? "$(check) " : "      "}${profile.label}`,
        description: presentation.costHint,
        detail: presentation.description,
        action: mode,
      };
    });

    const separator: ModeQuickPickItem = {
      label: "",
      kind: vscode.QuickPickItemKind.Separator,
      action: "custom",
    };
    const customItem: ModeQuickPickItem = {
      label: `${customActive ? "$(check) " : "      "}Custom…`,
      description: "per-stage models",
      detail: "Pin an explicit model per pipeline stage (Auto = adaptive router)",
      action: "custom",
    };

    const selected = await vscode.window.showQuickPick([...items, separator, customItem], {
      placeHolder: customActive
        ? "Current: Custom (per-stage overrides active)"
        : `Performance mode (current: ${MODE_PROFILES[currentMode].label})`,
      title: "Nightgauge: Performance Mode",
      matchOnDescription: true,
      matchOnDetail: true,
    });

    if (!selected) {
      logger.debug("Performance mode selection cancelled");
      return;
    }

    // ---- Custom per-stage flow ----
    if (selected.action === "custom") {
      await runCustomStageModelPicker(root, logger, statusBar);
      return;
    }

    // ---- Preset mode flow ----
    const mode = selected.action;

    // Selecting a preset clears any custom pins so the preset actually governs
    // routing (stale stage_models would otherwise silently override it).
    if (customActive) {
      try {
        clearStageModelSelections(root);
        statusBar?.setCustomOverridesActive(false);
        logger.info("Cleared custom per-stage models on preset selection", { mode });
      } catch (error) {
        logger.warn("Failed to clear custom per-stage models", { error });
      }
    }

    if (mode === currentMode && !customActive) {
      vscode.window.showInformationMessage(
        `Performance mode is already set to ${MODE_PROFILES[currentMode].label}.`
      );
      return;
    }

    try {
      writePerformanceModeStateFile(root, mode);
      statusBar?.setPerformanceMode(mode);
      logger.info("Performance mode changed", { from: currentMode, to: mode });
      vscode.window.showInformationMessage(
        `Performance mode set to ${MODE_PROFILES[mode].label} — ${MODE_PROFILES[mode].costHint}.`
      );
    } catch (error) {
      logger.error("Failed to set performance mode", { error });
      vscode.window.showErrorMessage(
        `Failed to set performance mode: ${error instanceof Error ? error.message : String(error)}`
      );
    }
  });
}
