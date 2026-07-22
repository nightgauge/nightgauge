/**
 * Filter Repositories View command
 *
 * Opens a QuickPick menu to filter issues within a specific repo + status node
 * in the Repositories tree view. Filter state is scoped per-repo per-status.
 */

import * as vscode from "vscode";
import type { RepositoriesTreeProvider } from "../views/RepositoriesTreeProvider";
import type { Logger } from "../utils/logger";
import { IssueSummaryTreeItem } from "../views/items/IssueSummaryTreeItem";
import {
  PRIORITY_OPTIONS,
  SIZE_OPTIONS,
  COMPONENT_OPTIONS,
  hasActiveFilters,
  type FilterPriority,
  type FilterSize,
  type FilterComponent,
} from "../types/FilterConfig";

interface SeparatorItem extends vscode.QuickPickItem {
  kind: vscode.QuickPickItemKind.Separator;
}

interface FilterQuickPickItem extends vscode.QuickPickItem {
  kind?: vscode.QuickPickItemKind;
  filterType: "priority" | "size" | "component" | "hideBlocked" | "clear";
  filterValue: string;
}

type QuickPickItem = FilterQuickPickItem | SeparatorItem;

/**
 * Register the Filter Repositories View command
 */
export function registerFilterRepositoriesViewCommand(
  provider: RepositoriesTreeProvider,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.filterRepositoriesView",
    async (item?: IssueSummaryTreeItem) => {
      logger.debug("Opening filter repositories view QuickPick", {
        repoName: item?.repoName,
        statusType: item?.statusType,
      });

      if (!item || !(item instanceof IssueSummaryTreeItem)) {
        void vscode.window.showWarningMessage(
          "Right-click a status group (Ready, In Progress, Backlog) to filter it."
        );
        return;
      }

      const currentFilters = provider.getFilterForStatus(item.repoName, item.statusType);

      const quickPickItems: QuickPickItem[] = [];

      // Clear filters option
      const hasActive =
        hasActiveFilters(currentFilters) ||
        currentFilters.hideBlocked ||
        !!currentFilters.searchText;
      if (hasActive) {
        quickPickItems.push({
          label: "$(clear-all) Clear All Filters",
          description: "Reset all filters to show all issues",
          filterType: "clear",
          filterValue: "all",
        });
      }

      // Priority
      quickPickItems.push({
        label: "Priority",
        kind: vscode.QuickPickItemKind.Separator,
      } as SeparatorItem);
      for (const option of PRIORITY_OPTIONS) {
        const isCurrent = currentFilters.priority === option.value;
        quickPickItems.push({
          label: `${isCurrent ? "$(check) " : "      "}${option.label}`,
          description:
            option.value === "all"
              ? "Show all priorities"
              : `Filter to ${option.label} issues only`,
          filterType: "priority",
          filterValue: option.value,
        });
      }

      // Size
      quickPickItems.push({
        label: "Size",
        kind: vscode.QuickPickItemKind.Separator,
      } as SeparatorItem);
      for (const option of SIZE_OPTIONS) {
        const isCurrent = currentFilters.size === option.value;
        quickPickItems.push({
          label: `${isCurrent ? "$(check) " : "      "}${option.label}`,
          description:
            option.value === "all" ? "Show all sizes" : `Filter to ${option.label} issues only`,
          filterType: "size",
          filterValue: option.value,
        });
      }

      // Component
      quickPickItems.push({
        label: "Component",
        kind: vscode.QuickPickItemKind.Separator,
      } as SeparatorItem);
      const componentAllCurrent = currentFilters.component === "all";
      quickPickItems.push({
        label: `${componentAllCurrent ? "$(check) " : "      "}All Components`,
        description: "Show all components",
        filterType: "component",
        filterValue: "all",
      });
      for (const component of COMPONENT_OPTIONS) {
        const isCurrent = currentFilters.component === component;
        quickPickItems.push({
          label: `${isCurrent ? "$(check) " : "      "}${component}`,
          description: `Filter to component:${component} issues only`,
          filterType: "component",
          filterValue: component,
        });
      }

      // Hide blocked
      quickPickItems.push({
        label: "Blocked",
        kind: vscode.QuickPickItemKind.Separator,
      } as SeparatorItem);
      quickPickItems.push({
        label: `${currentFilters.hideBlocked ? "$(check) " : "      "}Hide Blocked Issues`,
        description: currentFilters.hideBlocked
          ? "Blocked issues are hidden"
          : "Show all issues including blocked",
        filterType: "hideBlocked",
        filterValue: currentFilters.hideBlocked ? "false" : "true",
      });

      const selected = await vscode.window.showQuickPick(quickPickItems, {
        placeHolder: hasActive ? "Active filters applied" : "No active filters",
        title: `Filter issues in ${item.repoName} / ${item.statusType}`,
        matchOnDescription: true,
      });

      if (!selected || !("filterType" in selected)) {
        logger.debug("Filter selection cancelled");
        return;
      }

      const filterItem = selected as FilterQuickPickItem;

      if (filterItem.filterType === "clear") {
        logger.info("Clearing all filters for status group", {
          repoName: item.repoName,
          statusType: item.statusType,
        });
        provider.setFilterForStatus(item.repoName, item.statusType, {
          priority: "all" as FilterPriority,
          size: "all" as FilterSize,
          component: "all" as FilterComponent,
          searchText: "",
          hideBlocked: false,
        });
        void vscode.window.showInformationMessage("All filters cleared");
      } else if (filterItem.filterType === "hideBlocked") {
        const newValue = filterItem.filterValue === "true";
        provider.setFilterForStatus(item.repoName, item.statusType, {
          hideBlocked: newValue,
        });
        void vscode.window.showInformationMessage(
          newValue ? "Blocked issues hidden" : "Showing all issues"
        );
      } else {
        provider.setFilterForStatus(item.repoName, item.statusType, {
          [filterItem.filterType]: filterItem.filterValue,
        });
        if (filterItem.filterValue === "all") {
          void vscode.window.showInformationMessage(
            `${filterItem.filterType.charAt(0).toUpperCase() + filterItem.filterType.slice(1)} filter cleared`
          );
        } else {
          void vscode.window.showInformationMessage(
            `Filtering by ${filterItem.filterType}: ${filterItem.filterValue}`
          );
        }
      }
    }
  );
}
