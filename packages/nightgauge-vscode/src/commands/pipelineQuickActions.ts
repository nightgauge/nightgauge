/**
 * Pipeline Quick Actions command
 *
 * Shows a QuickPick with granular pipeline control options when the
 * status bar is clicked during concurrent execution. Offers per-slot,
 * per-epic, and global controls.
 *
 * @see Issue #2261 - Per-slot / per-epic pipeline controls
 */

import * as vscode from "vscode";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";

interface QuickActionItem extends vscode.QuickPickItem {
  action: () => Promise<void> | void;
}

/**
 * Register the Pipeline Quick Actions command
 *
 * Shows a QuickPick with per-slot stop, per-epic stop, and global
 * pipeline controls. Invoked from the status bar during concurrent runs.
 */
export function registerPipelineQuickActionsCommand(
  logger: Logger,
  concurrentPipelineManager: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.showPipelineQuickActions", async () => {
    if (!concurrentPipelineManager || concurrentPipelineManager.activeSlotCount === 0) {
      // Fall back to dashboard if nothing is running
      await vscode.commands.executeCommand("nightgauge.showDashboard");
      return;
    }

    const activeSlots = concurrentPipelineManager.getActiveSlots();
    const items: QuickActionItem[] = [];

    // Section: Per-slot stop options
    for (const slot of activeSlots) {
      const epicSuffix = slot.epicNumber ? ` (Epic #${slot.epicNumber})` : "";
      items.push({
        label: `$(debug-stop) Stop #${slot.issueNumber}${epicSuffix}`,
        description: slot.currentStage ? `Currently: ${slot.currentStage}` : "Running",
        action: async () => {
          await vscode.commands.executeCommand("nightgauge.stopSlot", {
            issueNumber: slot.issueNumber,
          });
        },
      });
    }

    // Section: Per-epic stop (if any epics are running)
    const epicNumbers = new Set<number>();
    for (const slot of activeSlots) {
      if (slot.epicNumber) {
        epicNumbers.add(slot.epicNumber);
      }
    }
    if (epicNumbers.size > 0) {
      items.push({
        label: "",
        kind: vscode.QuickPickItemKind.Separator,
        action: async () => {},
      });
      for (const epicNumber of epicNumbers) {
        const epicSlots = activeSlots.filter((s) => s.epicNumber === epicNumber);
        items.push({
          label: `$(close-all) Stop Epic #${epicNumber}`,
          description: `${epicSlots.length} running issue(s)`,
          action: async () => {
            await vscode.commands.executeCommand("nightgauge.stopEpic", {
              epicNumber,
            });
          },
        });
      }
    }

    // Section: Global controls
    items.push({
      label: "",
      kind: vscode.QuickPickItemKind.Separator,
      action: async () => {},
    });
    items.push({
      label: "$(debug-step-out) Stop After Current Issues",
      description: "Let running issues finish, stop dequeuing",
      action: async () => {
        await vscode.commands.executeCommand("nightgauge.stopBatchAfterCurrent");
      },
    });
    items.push({
      label: "$(debug-stop) Stop All Pipelines",
      description: "Stop all running slots and clear queue",
      action: async () => {
        await vscode.commands.executeCommand("nightgauge.stopPipeline");
      },
    });
    items.push({
      label: "$(dashboard) Open Dashboard",
      description: "View pipeline metrics and status",
      action: async () => {
        await vscode.commands.executeCommand("nightgauge.showDashboard");
      },
    });

    const selected = await vscode.window.showQuickPick(items, {
      placeHolder: "Pipeline Controls",
      title: `${activeSlots.length} Pipeline(s) Running`,
    });

    if (selected) {
      logger.info("Pipeline quick action selected", {
        label: selected.label,
      });
      await selected.action();
    }
  });
}
