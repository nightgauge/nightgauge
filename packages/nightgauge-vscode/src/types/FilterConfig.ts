/**
 * FilterConfig - Filter configuration types for Project Board View
 *
 * Defines the types and constants for filtering issues in the Project Board View.
 * Follows the same pattern as SortBy/SortDirection in ProjectBoardService.
 */

import type { Priority, Size } from "../services/ProjectBoardService";

/**
 * Filter priority values - matches Priority type with 'all' option
 */
export type FilterPriority = "all" | "P0" | "P1" | "P2";

/**
 * Filter size values - matches Size type with 'all' option
 */
export type FilterSize = "all" | "XS" | "S" | "M" | "L" | "XL";

/**
 * Filter component values - 'all' or any component label string
 */
export type FilterComponent = "all" | string;

/**
 * Complete filter state for Project Board View
 */
export interface FilterState {
  priority: FilterPriority;
  size: FilterSize;
  component: FilterComponent;
}

/**
 * Component options available for filtering
 * These are the common component labels used in the project
 */
export const COMPONENT_OPTIONS = [
  "pattern-mining",
  "configs",
  "platform",
  "smart-setup",
  "standards",
] as const;

/**
 * Default filter state - shows all issues
 */
export const DEFAULT_FILTER_STATE: FilterState = {
  priority: "all",
  size: "all",
  component: "all",
};

/**
 * Priority filter options with display labels
 */
export const PRIORITY_OPTIONS: { value: FilterPriority; label: string }[] = [
  { value: "all", label: "All Priorities" },
  { value: "P0", label: "P0 (Critical)" },
  { value: "P1", label: "P1 (High)" },
  { value: "P2", label: "P2 (Medium/Low)" },
];

/**
 * Size filter options with display labels
 */
export const SIZE_OPTIONS: { value: FilterSize; label: string }[] = [
  { value: "all", label: "All Sizes" },
  { value: "XS", label: "XS (Extra Small)" },
  { value: "S", label: "S (Small)" },
  { value: "M", label: "M (Medium)" },
  { value: "L", label: "L (Large)" },
  { value: "XL", label: "XL (Extra Large)" },
];

/**
 * Check if any filters are active (not set to 'all')
 */
export function hasActiveFilters(state: FilterState): boolean {
  return state.priority !== "all" || state.size !== "all" || state.component !== "all";
}

/**
 * Get a human-readable summary of active filters
 */
export function getFilterSummary(state: FilterState): string {
  const parts: string[] = [];

  if (state.priority !== "all") {
    parts.push(`Priority: ${state.priority}`);
  }
  if (state.size !== "all") {
    parts.push(`Size: ${state.size}`);
  }
  if (state.component !== "all") {
    parts.push(`Component: ${state.component}`);
  }

  return parts.length > 0 ? parts.join(", ") : "No filters";
}

/**
 * Check if a priority value matches the filter
 */
export function matchesPriorityFilter(
  issuePriority: Priority,
  filterPriority: FilterPriority
): boolean {
  if (filterPriority === "all") return true;
  return issuePriority === filterPriority;
}

/**
 * Check if a size value matches the filter
 */
export function matchesSizeFilter(issueSize: Size, filterSize: FilterSize): boolean {
  if (filterSize === "all") return true;
  return issueSize === filterSize;
}

/**
 * Check if labels contain the filtered component
 */
export function matchesComponentFilter(
  labels: string[],
  filterComponent: FilterComponent
): boolean {
  if (filterComponent === "all") return true;
  return labels.some((label) => label === `component:${filterComponent}`);
}

/**
 * Check if an issue matches the search text
 * Matches against issue title (case-insensitive substring) or issue number
 */
export function matchesSearchText(
  issueTitle: string,
  issueNumber: number,
  searchText: string
): boolean {
  if (!searchText || searchText.trim() === "") return true;

  const normalizedSearch = searchText.trim().toLowerCase();

  // Check issue number (with or without #)
  const numberSearch = normalizedSearch.replace(/^#/, "");
  if (issueNumber.toString() === numberSearch) {
    return true;
  }

  // Check title (case-insensitive substring)
  return issueTitle.toLowerCase().includes(normalizedSearch);
}
