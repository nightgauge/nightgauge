/**
 * Focus Project Board View Command
 *
 * Moves keyboard focus to the project board tree view.
 * Provides keyboard shortcut for navigation.
 *
 * @see Issue #304 - Add Accessibility Alternatives for Drag & Drop
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";

/**
 * Register the Focus Project Board View command
 *
 * Provides keyboard shortcut (Ctrl+Alt+B / Cmd+Alt+B) to focus the
 * project board tree view for keyboard navigation.
 */
export function registerFocusProjectBoardViewCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.focusProjectBoardView", async () => {
    try {
      await vscode.commands.executeCommand("nightgauge-project-board.focus");
      vscode.window.showInformationMessage("Project board view focused");
      logger.info("Focus moved to project board view");
    } catch (error) {
      logger.error("Failed to focus project board view", { error });
      vscode.window.showErrorMessage("Failed to focus project board view");
    }
  });
}
