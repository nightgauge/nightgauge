/**
 * View Context command
 *
 * Opens the context file for a pipeline stage in a read-only editor.
 * Supports both single-pipeline and concurrent slot modes.
 */

import * as vscode from "vscode";
import * as path from "node:path";
import type { PipelineStage } from "@nightgauge/sdk";
import type { ContextFileViewer, PipelineTreeProvider, StageTreeItem } from "../views";
import type { Logger } from "../utils/logger";
import { openContextFile } from "../views";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";

/**
 * Map stage to context file type
 * Bookend stages (pipeline-start, pipeline-finish) map to state file
 */
const STAGE_TO_CONTEXT: Record<PipelineStage, string> = {
  "pipeline-start": "state",
  "issue-pickup": "issue",
  "feature-planning": "planning",
  "feature-dev": "dev",
  "feature-validate": "validate",
  "pr-create": "pr",
  "pr-merge": "pr",
  "pipeline-finish": "state",
};

/**
 * Register the View Context command
 */
export function registerViewContextCommand(
  contextViewer: ContextFileViewer,
  treeProvider: PipelineTreeProvider,
  logger: Logger,
  concurrentPipelineManager: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.viewContext", async (item?: StageTreeItem) => {
    // Resolve issue number — check concurrent slot parent first, then legacy single-pipeline
    let issueNumber: number | undefined;
    let worktreePath: string | undefined;

    if (item && "stage" in item) {
      // Stage clicked inside a ConcurrentSlotTreeItem — get issue from slot
      const parentSlot = treeProvider.findParentSlot(item);
      if (parentSlot) {
        issueNumber = parentSlot.issueNumber;
        // Resolve worktree path for this slot's context files
        if (concurrentPipelineManager) {
          const activeSlots = concurrentPipelineManager.getActiveSlots();
          const slot = activeSlots.find((s) => s.issueNumber === issueNumber);
          if (slot) {
            worktreePath = slot.worktreePath;
          }
        }
      }
    }

    // Fall back to single-pipeline mode
    if (!issueNumber) {
      issueNumber = treeProvider.getCurrentIssueNumber();
    }

    if (!issueNumber) {
      vscode.window.showWarningMessage("No active issue. Run a pipeline first.");
      return;
    }

    // Determine which context file to open
    let contextType: string;

    if (item && "stage" in item) {
      // Called from tree item context menu
      contextType = STAGE_TO_CONTEXT[item.stage];
    } else {
      // Called from command palette - show quick pick
      const selection = await vscode.window.showQuickPick(
        [
          { label: "Issue Context", value: "issue" },
          { label: "Planning Context", value: "planning" },
          { label: "Dev Context", value: "dev" },
          { label: "PR Context", value: "pr" },
        ],
        { placeHolder: "Select context file to view" }
      );

      if (!selection) {
        return;
      }

      contextType = selection.value;
    }

    const filename = `${contextType}-${issueNumber}.json`;
    logger.debug("Opening context file", { filename, worktreePath });

    try {
      // For concurrent slots, temporarily point the viewer at the worktree's context path
      if (worktreePath) {
        const settings = await import("../config/settings");
        const slotContextPath = path.join(worktreePath, settings.getSettings().contextPath);
        const originalPath = contextViewer.getContextPath();
        contextViewer.setContextPath(slotContextPath);
        try {
          await openContextFile(contextViewer, filename);
        } finally {
          // Restore original path so other callers aren't affected
          contextViewer.setContextPath(originalPath);
        }
      } else {
        await openContextFile(contextViewer, filename);
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to open context file";
      vscode.window.showErrorMessage(message);
      logger.error("Failed to open context file", {
        filename,
        error: message,
      });
    }
  });
}
