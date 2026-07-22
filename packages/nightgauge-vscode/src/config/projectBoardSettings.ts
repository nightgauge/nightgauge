/**
 * Project Board Settings for Nightgauge
 *
 * Provides typed access to project board UI configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #476 - Refactor tree providers, extension.ts, and settings.ts to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import { type UIProjectBoardConfig, DEFAULT_CONFIG } from "./schema";

/**
 * Project board configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UIProjectBoardConfig).
 */
export interface ProjectBoardSettings {
  /** Group issues under their parent epic */
  groupByEpic: boolean;

  /** Default collapse state for epic groups */
  defaultEpicCollapsed: boolean;
}

/**
 * Default project board settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.project_board from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_PROJECT_BOARD_SETTINGS: ProjectBoardSettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.project_board
);

/**
 * Map ConfigBridge UIProjectBoardConfig to legacy ProjectBoardSettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UIProjectBoardConfig): ProjectBoardSettings {
  const defaults = DEFAULT_CONFIG.ui!.project_board!;

  return {
    groupByEpic: config?.group_by_epic ?? defaults.group_by_epic!,
    defaultEpicCollapsed: config?.default_epic_collapsed ?? defaults.default_epic_collapsed!,
  };
}

/**
 * Get current project board settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getProjectBoardSettings(): ProjectBoardSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for project board");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.project_board);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.project_board);
}

/**
 * Re-export UIProjectBoardConfig for consumers that need the raw type
 */
export type { UIProjectBoardConfig };
