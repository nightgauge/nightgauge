/**
 * Focus Pipeline View Command
 *
 * Moves keyboard focus to the pipeline tree view.
 * Provides keyboard shortcut for navigation.
 *
 * @see Issue #304 - Add Accessibility Alternatives for Drag & Drop
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";

/**
 * Register the Focus Pipeline View command
 *
 * Provides keyboard shortcut (Ctrl+Alt+P / Cmd+Alt+P) to focus the
 * pipeline tree view for keyboard navigation.
 */
export function registerFocusPipelineViewCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.focusPipelineView", async () => {
    try {
      await vscode.commands.executeCommand("nightgauge.pipelineView.focus");
      vscode.window.showInformationMessage("Pipeline view focused");
      logger.info("Focus moved to pipeline view");
    } catch (error) {
      logger.error("Failed to focus pipeline view", { error });
      vscode.window.showErrorMessage("Failed to focus pipeline view");
    }
  });
}
