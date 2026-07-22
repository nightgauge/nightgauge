/**
 * TabConfig - Configuration for Project Board tabs
 *
 * Defines the structure for tab navigation in the Project Board View.
 * Each tab corresponds to a GitHub Project board status column.
 */

/**
 * Valid tab/status identifiers for the project board view.
 * These map to GitHub Project board Status field values.
 */
export type TabId = "ready" | "in-progress" | "in-review" | "backlog";

/**
 * Configuration for a single Project Board tab
 */
export interface TabConfig {
  /** Unique identifier for the tab (kebab-case) */
  id: TabId;
  /** Display label for the tab */
  label: string;
  /** GitHub Project board Status field value */
  status: string;
  /** Icon to display (VS Code codicon) */
  icon: string;
}

/**
 * All available tabs for the Project Board View.
 * Order determines display order in the view container.
 */
export const PROJECT_BOARD_TABS: readonly TabConfig[] = [
  {
    id: "ready",
    label: "Ready",
    status: "Ready",
    icon: "checklist",
  },
  {
    id: "in-progress",
    label: "In Progress",
    status: "In progress",
    icon: "sync~spin",
  },
  {
    id: "in-review",
    label: "In Review",
    status: "In review",
    icon: "git-pull-request",
  },
  {
    id: "backlog",
    label: "Backlog",
    status: "Backlog",
    icon: "list-unordered",
  },
] as const;

/**
 * Get TabConfig by ID
 */
export function getTabConfig(id: TabId): TabConfig | undefined {
  return PROJECT_BOARD_TABS.find((tab) => tab.id === id);
}

/**
 * Get TabConfig by status value
 */
export function getTabByStatus(status: string): TabConfig | undefined {
  return PROJECT_BOARD_TABS.find((tab) => tab.status === status);
}
