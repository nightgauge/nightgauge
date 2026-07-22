/**
 * Filter Project Board command
 *
 * Opens a QuickPick menu to select filter criteria for project board views.
 * Supports filtering by priority, size, and component labels.
 * Updates VS Code configuration and refreshes all providers.
 */

import * as vscode from "vscode";
import type { ProjectBoardTreeProvider } from "../views/ProjectBoardTreeProvider";
import type { Logger } from "../utils/logger";
import type { TabId } from "../types/TabConfig";
import {
  type FilterPriority,
  type FilterSize,
  type FilterComponent,
  PRIORITY_OPTIONS,
  SIZE_OPTIONS,
  COMPONENT_OPTIONS,
  hasActiveFilters,
} from "../types/FilterConfig";

/**
 * Map of tab IDs to their providers
 */
export type ProjectBoardProviders = Map<TabId, ProjectBoardTreeProvider>;

/**
 * QuickPick separator item
 */
interface SeparatorItem extends vscode.QuickPickItem {
  kind: vscode.QuickPickItemKind.Separator;
}

/**
 * Filter QuickPick item
 */
interface FilterQuickPickItem extends vscode.QuickPickItem {
  kind?: vscode.QuickPickItemKind;
  filterType: "priority" | "size" | "component" | "hideBlocked" | "clear";
  filterValue: string;
}

type QuickPickItem = FilterQuickPickItem | SeparatorItem;

/**
 * Get the current filter state from configuration
 */
function getCurrentFilters(): {
  priority: FilterPriority;
  size: FilterSize;
  component: FilterComponent;
  hideBlocked: boolean;
} {
  const config = vscode.workspace.getConfiguration("nightgauge.readyItems.filters");
  return {
    priority: config.get<FilterPriority>("priority", "all"),
    size: config.get<FilterSize>("size", "all"),
    component: config.get<FilterComponent>("component", "all"),
    hideBlocked: config.get<boolean>("hideBlocked", false),
  };
}

/**
 * Build QuickPick items for filter selection
 */
function buildQuickPickItems(currentFilters: {
  priority: FilterPriority;
  size: FilterSize;
  component: FilterComponent;
  hideBlocked: boolean;
}): QuickPickItem[] {
  const items: QuickPickItem[] = [];

  // Clear filters option (only show if filters are active)
  if (hasActiveFilters(currentFilters) || currentFilters.hideBlocked) {
    items.push({
      label: "$(clear-all) Clear All Filters",
      description: "Reset all filters to show all issues",
      filterType: "clear",
      filterValue: "all",
    });
    items.push({
      label: "Priority",
      kind: vscode.QuickPickItemKind.Separator,
    } as SeparatorItem);
  } else {
    items.push({
      label: "Priority",
      kind: vscode.QuickPickItemKind.Separator,
    } as SeparatorItem);
  }

  // Priority options
  for (const option of PRIORITY_OPTIONS) {
    const isCurrent = currentFilters.priority === option.value;
    items.push({
      label: `${isCurrent ? "$(check) " : "      "}${option.label}`,
      description:
        option.value === "all" ? "Show all priorities" : `Filter to ${option.label} issues only`,
      filterType: "priority",
      filterValue: option.value,
    });
  }

  // Size separator
  items.push({
    label: "Size",
    kind: vscode.QuickPickItemKind.Separator,
  } as SeparatorItem);

  // Size options
  for (const option of SIZE_OPTIONS) {
    const isCurrent = currentFilters.size === option.value;
    items.push({
      label: `${isCurrent ? "$(check) " : "      "}${option.label}`,
      description:
        option.value === "all" ? "Show all sizes" : `Filter to ${option.label} issues only`,
      filterType: "size",
      filterValue: option.value,
    });
  }

  // Component separator
  items.push({
    label: "Component",
    kind: vscode.QuickPickItemKind.Separator,
  } as SeparatorItem);

  // Component options
  const componentAllCurrent = currentFilters.component === "all";
  items.push({
    label: `${componentAllCurrent ? "$(check) " : "      "}All Components`,
    description: "Show all components",
    filterType: "component",
    filterValue: "all",
  });

  for (const component of COMPONENT_OPTIONS) {
    const isCurrent = currentFilters.component === component;
    items.push({
      label: `${isCurrent ? "$(check) " : "      "}${component}`,
      description: `Filter to component:${component} issues only`,
      filterType: "component",
      filterValue: component,
    });
  }

  // Hide blocked separator (Issue #822)
  items.push({
    label: "Blocked",
    kind: vscode.QuickPickItemKind.Separator,
  } as SeparatorItem);

  items.push({
    label: `${currentFilters.hideBlocked ? "$(check) " : "      "}Hide Blocked Issues`,
    description: currentFilters.hideBlocked
      ? "Blocked issues are hidden"
      : "Show all issues including blocked",
    filterType: "hideBlocked",
    filterValue: currentFilters.hideBlocked ? "false" : "true",
  });

  return items;
}

/**
 * Get current filter summary for placeholder
 */
function getFilterPlaceholder(currentFilters: {
  priority: FilterPriority;
  size: FilterSize;
  component: FilterComponent;
  hideBlocked: boolean;
}): string {
  const parts: string[] = [];

  if (currentFilters.priority !== "all") {
    parts.push(`Priority: ${currentFilters.priority}`);
  }
  if (currentFilters.size !== "all") {
    parts.push(`Size: ${currentFilters.size}`);
  }
  if (currentFilters.component !== "all") {
    parts.push(`Component: ${currentFilters.component}`);
  }
  if (currentFilters.hideBlocked) {
    parts.push("Blocked: hidden");
  }

  return parts.length > 0 ? `Current: ${parts.join(", ")}` : "No active filters";
}

/**
 * Register the Filter Project Board command
 */
export function registerFilterProjectBoardCommand(
  providers: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.filterProjectBoard", async () => {
    logger.debug("Opening filter project board QuickPick");

    // Get current filter settings
    const currentFilters = getCurrentFilters();

    // Build QuickPick items
    const items = buildQuickPickItems(currentFilters);

    // Show QuickPick
    const selected = await vscode.window.showQuickPick(items, {
      placeHolder: getFilterPlaceholder(currentFilters),
      title: "Filter Project Board Issues",
      matchOnDescription: true,
    });

    if (!selected || !("filterType" in selected)) {
      logger.debug("Filter selection cancelled");
      return;
    }

    const filterItem = selected as FilterQuickPickItem;

    // Get configuration target
    const config = vscode.workspace.getConfiguration("nightgauge.readyItems");

    // Handle selection
    if (filterItem.filterType === "clear") {
      // Clear all filters
      logger.info("Clearing all filters");
      await config.update("filters.priority", "all", vscode.ConfigurationTarget.Global);
      await config.update("filters.size", "all", vscode.ConfigurationTarget.Global);
      await config.update("filters.component", "all", vscode.ConfigurationTarget.Global);
      await config.update("filters.hideBlocked", false, vscode.ConfigurationTarget.Global);
      vscode.window.showInformationMessage("All filters cleared");
    } else if (filterItem.filterType === "hideBlocked") {
      // Toggle hide blocked filter (Issue #822)
      const newValue = filterItem.filterValue === "true";
      logger.info("Toggling hide blocked filter", { hideBlocked: newValue });
      await config.update("filters.hideBlocked", newValue, vscode.ConfigurationTarget.Global);
      vscode.window.showInformationMessage(
        newValue ? "Blocked issues hidden" : "Showing all issues"
      );
    } else {
      // Update specific filter
      logger.info("Updating filter", {
        filterType: filterItem.filterType,
        filterValue: filterItem.filterValue,
      });

      await config.update(
        `filters.${filterItem.filterType}`,
        filterItem.filterValue,
        vscode.ConfigurationTarget.Global
      );

      // Show confirmation
      if (filterItem.filterValue === "all") {
        vscode.window.showInformationMessage(
          `${filterItem.filterType.charAt(0).toUpperCase() + filterItem.filterType.slice(1)} filter cleared`
        );
      } else {
        vscode.window.showInformationMessage(
          `Filtering by ${filterItem.filterType}: ${filterItem.filterValue}`
        );
      }
    }

    // Note: Providers automatically refresh via onDidChangeConfiguration listener.
    // No explicit refresh needed here - filters are applied client-side to cached data.
  });
}
