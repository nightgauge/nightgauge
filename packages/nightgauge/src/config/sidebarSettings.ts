/**
 * Sidebar Settings for Nightgauge
 *
 * Provides typed access to sidebar UI configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #476 - Refactor tree providers, extension.ts, and settings.ts to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import { type UISidebarConfig, DEFAULT_CONFIG } from "./schema";

/**
 * Sidebar configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UISidebarConfig).
 */
export interface SidebarSettings {
  /** Hide empty sections in sidebar (no items to display) */
  hideEmptySections: boolean;
}

/**
 * Default sidebar settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.sidebar from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_SIDEBAR_SETTINGS: SidebarSettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.sidebar
);

/**
 * Map ConfigBridge UISidebarConfig to legacy SidebarSettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UISidebarConfig): SidebarSettings {
  const defaults = DEFAULT_CONFIG.ui!.sidebar!;

  return {
    hideEmptySections: config?.hide_empty_sections ?? defaults.hide_empty_sections!,
  };
}

/**
 * Get current sidebar settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getSidebarSettings(): SidebarSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for sidebar");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.sidebar);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.sidebar);
}

/**
 * Re-export UISidebarConfig for consumers that need the raw type
 */
export type { UISidebarConfig };
