/**
 * Epic group expand/collapse commands for Project Board
 *
 * Provides commands to expand or collapse all epic groups in the project board view.
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { ProjectBoardTreeProvider } from "../views/ProjectBoardTreeProvider";
import type { TabId } from "../types/TabConfig";

/**
 * Map of tab IDs to their tree providers
 */
export type ProjectBoardProviders = Map<TabId, ProjectBoardTreeProvider>;

/**
 * Register the expand all epic groups command
 */
export function registerExpandAllEpicGroupsCommand(
  projectBoardProviders: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.projectBoard.expandAll", async () => {
    logger.debug("Expand all epic groups command invoked");

    // Update the setting to expand by default (not collapsed)
    await vscode.workspace
      .getConfiguration("nightgauge.projectBoard")
      .update("defaultEpicCollapsed", false, vscode.ConfigurationTarget.Global);

    // Refresh all project board providers
    for (const provider of projectBoardProviders.values()) {
      provider.refresh();
    }

    logger.info("All epic groups expanded");
  });
}

/**
 * Register the collapse all epic groups command
 */
export function registerCollapseAllEpicGroupsCommand(
  projectBoardProviders: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.projectBoard.collapseAll", async () => {
    logger.debug("Collapse all epic groups command invoked");

    // Update the setting to collapse by default
    await vscode.workspace
      .getConfiguration("nightgauge.projectBoard")
      .update("defaultEpicCollapsed", true, vscode.ConfigurationTarget.Global);

    // Refresh all project board providers
    for (const provider of projectBoardProviders.values()) {
      provider.refresh();
    }

    logger.info("All epic groups collapsed");
  });
}

/**
 * Register all epic group commands
 */
export function registerEpicGroupCommands(
  projectBoardProviders: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable[] {
  return [
    registerExpandAllEpicGroupsCommand(projectBoardProviders, logger),
    registerCollapseAllEpicGroupsCommand(projectBoardProviders, logger),
  ];
}
