/**
 * Ready Items Settings for Nightgauge
 *
 * Provides typed access to ready items / project board issue list configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #476 - Refactor tree providers, extension.ts, and settings.ts to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import {
  type UIReadyItemsConfig,
  type UIReadyItemsFiltersConfig,
  DEFAULT_CONFIG,
  type SortBy as SchemaSortBy,
  type SortDirectionEnum as SchemaSortDirection,
  type PriorityFilter as SchemaPriorityFilter,
  type SizeFilter as SchemaSizeFilter,
} from "./schema";

/**
 * Re-export sort types from schema for backward compatibility.
 */
export type SortBy = SchemaSortBy;
export type SortDirection = SchemaSortDirection;
export type PriorityFilter = SchemaPriorityFilter;
export type SizeFilter = SchemaSizeFilter;

/**
 * Ready items filter settings interface
 */
export interface ReadyItemsFilterSettings {
  /** Priority filter */
  priority: PriorityFilter;
  /** Size filter */
  size: SizeFilter;
  /** Component filter (label prefix) */
  component: string;
  /** Hide blocked issues from view */
  hideBlocked: boolean;
}

/**
 * Ready items configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UIReadyItemsConfig).
 */
export interface ReadyItemsSettings {
  /** Enable auto-refresh of issue list */
  autoRefresh: boolean;

  /** Refresh interval in seconds (min 60) */
  refreshInterval: number;

  /** Field to sort issues by */
  sortBy: SortBy;

  /** Sort direction */
  sortDirection: SortDirection;

  /** Filter criteria */
  filters: ReadyItemsFilterSettings;

  /** Text to filter issues by title/number */
  searchText: string;

  /** Show dependency indicators on issues */
  showDependencies: boolean;
}

/**
 * Default ready items settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.ready_items from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_READY_ITEMS_SETTINGS: ReadyItemsSettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.ready_items
);

/**
 * Map ConfigBridge UIReadyItemsConfig to legacy ReadyItemsSettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UIReadyItemsConfig): ReadyItemsSettings {
  const defaults = DEFAULT_CONFIG.ui!.ready_items!;
  const defaultFilters = defaults.filters!;

  return {
    autoRefresh: config?.auto_refresh ?? defaults.auto_refresh!,
    refreshInterval: config?.refresh_interval ?? defaults.refresh_interval!,
    sortBy: config?.sort_by ?? defaults.sort_by!,
    sortDirection: config?.sort_direction ?? defaults.sort_direction!,
    filters: {
      priority: config?.filters?.priority ?? defaultFilters.priority!,
      size: config?.filters?.size ?? defaultFilters.size!,
      component: config?.filters?.component ?? defaultFilters.component!,
      hideBlocked: config?.filters?.hide_blocked ?? defaultFilters.hide_blocked ?? false,
    },
    searchText: config?.search_text ?? defaults.search_text!,
    showDependencies: config?.show_dependencies ?? defaults.show_dependencies!,
  };
}

/**
 * Get current ready items settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getReadyItemsSettings(): ReadyItemsSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for ready items");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.ready_items);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.ready_items);
}

/**
 * Re-export types for consumers that need the raw types
 */
export type { UIReadyItemsConfig, UIReadyItemsFiltersConfig };
