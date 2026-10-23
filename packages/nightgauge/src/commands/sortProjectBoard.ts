/**
 * Sort Project Board command
 *
 * Opens a QuickPick menu to select sort field and direction for project board views.
 * Updates VS Code configuration and refreshes all providers.
 */

import * as vscode from "vscode";
import type { ProjectBoardTreeProvider } from "../views/ProjectBoardTreeProvider";
import type { Logger } from "../utils/logger";
import type { TabId } from "../types/TabConfig";
import type { SortBy, SortDirection } from "../services/ProjectBoardService";

/**
 * Map of tab IDs to their providers
 */
export type ProjectBoardProviders = Map<TabId, ProjectBoardTreeProvider>;

interface SortQuickPickItem extends vscode.QuickPickItem {
  sortBy: SortBy;
  sortDirection: SortDirection;
}

/**
 * Get display label for sort field
 */
function getSortByLabel(sortBy: SortBy): string {
  switch (sortBy) {
    case "smart":
      return "Smart";
    case "board":
      return "Board Order";
    case "priority":
      return "Priority";
    case "number":
      return "Issue Number";
    case "size":
      return "Size";
    case "dependencies":
      return "Dependencies";
    default: {
      const _exhaustive: never = sortBy;
      return _exhaustive;
    }
  }
}

/**
 * Get display label for sort direction
 */
function getDirectionLabel(direction: SortDirection): string {
  return direction === "asc" ? "Ascending" : "Descending";
}

/**
 * Get description for sort option
 */
function getSortDescription(sortBy: SortBy, direction: SortDirection): string {
  switch (sortBy) {
    case "smart":
      return direction === "asc"
        ? "Priority → Unblocked → Small → Oldest (most actionable first)"
        : "Low priority → Blocked → Large → Newest";
    case "board":
      return direction === "asc"
        ? "Preserve GitHub Project board order"
        : "Reverse GitHub Project board order";
    case "priority":
      return direction === "asc"
        ? "P0 (Critical) first, then P1, P2, unset"
        : "Unset first, then P2, P1, P0";
    case "number":
      return direction === "asc" ? "Lowest issue number first" : "Highest issue number first";
    case "size":
      return direction === "asc"
        ? "Smallest (XS) first, then S, M, L, XL"
        : "Largest (XL) first, then L, M, S, XS";
    case "dependencies":
      return direction === "asc" ? "Fewest dependencies first" : "Most dependencies first";
    default: {
      const _exhaustive: never = sortBy;
      return _exhaustive;
    }
  }
}

/**
 * Register the Sort Project Board command
 */
export function registerSortProjectBoardCommand(
  providers: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.sortProjectBoard", async () => {
    logger.debug("Opening sort project board QuickPick");

    // Get current settings
    const config = vscode.workspace.getConfiguration("nightgauge.readyItems");
    const currentSortBy = config.get<SortBy>("sortBy", "board");
    const currentDirection = config.get<SortDirection>("sortDirection", "asc");

    // Build QuickPick items
    const sortFields: SortBy[] = ["smart", "board", "priority", "number", "size", "dependencies"];
    const directions: SortDirection[] = ["asc", "desc"];
    const items: SortQuickPickItem[] = [];

    for (const sortBy of sortFields) {
      for (const direction of directions) {
        const isCurrent = sortBy === currentSortBy && direction === currentDirection;
        items.push({
          label: `${isCurrent ? "$(check) " : ""}${getSortByLabel(sortBy)} - ${getDirectionLabel(direction)}`,
          description: getSortDescription(sortBy, direction),
          sortBy,
          sortDirection: direction,
        });
      }
    }

    // Show QuickPick
    const selected = await vscode.window.showQuickPick(items, {
      placeHolder: `Current: ${getSortByLabel(currentSortBy)} - ${getDirectionLabel(currentDirection)}`,
      title: "Sort Project Board Issues",
    });

    if (!selected) {
      logger.debug("Sort selection cancelled");
      return;
    }

    // Update configuration (persists setting for next session)
    logger.info("Updating sort settings", {
      sortBy: selected.sortBy,
      sortDirection: selected.sortDirection,
    });

    await config.update("sortBy", selected.sortBy, vscode.ConfigurationTarget.Global);
    await config.update("sortDirection", selected.sortDirection, vscode.ConfigurationTarget.Global);

    // Directly update all providers — config watchers are unreliable here
    // because ProjectBoardTreeProvider reads sort via ConfigBridge (which
    // ignores VSCode settings changes), and ReadyItemsTreeProvider's
    // onDidChangeConfiguration may not fire synchronously.
    for (const provider of providers.values()) {
      provider.setSort(selected.sortBy, selected.sortDirection);
    }
    vscode.window.showInformationMessage(
      `Sorted by ${getSortByLabel(selected.sortBy)} (${getDirectionLabel(selected.sortDirection)})`
    );
  });
}
