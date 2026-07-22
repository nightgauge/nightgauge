/**
 * Sort Repositories View command
 *
 * Opens a QuickPick menu to select sort field and direction for a specific
 * repo + status node in the Repositories tree view. Sort state is scoped
 * per-repo per-status (not global), so each status group can sort independently.
 */

import * as vscode from "vscode";
import type { RepositoriesTreeProvider } from "../views/RepositoriesTreeProvider";
import type { Logger } from "../utils/logger";
import { IssueSummaryTreeItem } from "../views/items/IssueSummaryTreeItem";
import type { SortBy, SortDirection } from "../services/ProjectBoardService";

interface SortQuickPickItem extends vscode.QuickPickItem {
  sortBy: SortBy;
  sortDirection: SortDirection;
}

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

function getDirectionLabel(direction: SortDirection): string {
  return direction === "asc" ? "Ascending" : "Descending";
}

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
 * Register the Sort Repositories View command
 */
export function registerSortRepositoriesViewCommand(
  provider: RepositoriesTreeProvider,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.sortRepositoriesView",
    async (item?: IssueSummaryTreeItem) => {
      logger.debug("Opening sort repositories view QuickPick", {
        repoName: item?.repoName,
        statusType: item?.statusType,
      });

      if (!item || !(item instanceof IssueSummaryTreeItem)) {
        void vscode.window.showWarningMessage(
          "Right-click a status group (Ready, In Progress, Backlog) to sort it."
        );
        return;
      }

      const { sortBy: currentSortBy, sortDirection: currentDirection } = provider.getSortForStatus(
        item.repoName,
        item.statusType
      );

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

      const selected = await vscode.window.showQuickPick(items, {
        placeHolder: `Current: ${getSortByLabel(currentSortBy)} - ${getDirectionLabel(currentDirection)}`,
        title: `Sort ${item.statusType.charAt(0).toUpperCase() + item.statusType.slice(1)} issues in ${item.repoName}`,
      });

      if (!selected) {
        logger.debug("Sort selection cancelled");
        return;
      }

      logger.info("Setting sort for status group", {
        repoName: item.repoName,
        statusType: item.statusType,
        sortBy: selected.sortBy,
        sortDirection: selected.sortDirection,
      });

      provider.setSortForStatus(
        item.repoName,
        item.statusType,
        selected.sortBy,
        selected.sortDirection
      );

      void vscode.window.showInformationMessage(
        `Sorted by ${getSortByLabel(selected.sortBy)} (${getDirectionLabel(selected.sortDirection)})`
      );
    }
  );
}
