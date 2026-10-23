/**
 * Warning Settings for Nightgauge
 *
 * Provides typed access to drag & drop warning configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import { type UIWarningsConfig, DEFAULT_CONFIG } from "./schema";

/**
 * Warning configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UIWarningsConfig).
 */
export interface WarningSettings {
  /** Master enable/disable for all warnings */
  enabled: boolean;

  /** Warn when dragging In Progress issues */
  warnOnInProgress: boolean;

  /** Warn when dragging In Review issues */
  warnOnInReview: boolean;
}

/**
 * Default warning settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.warnings from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_WARNING_SETTINGS: WarningSettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.warnings
);

/**
 * Map ConfigBridge UIWarningsConfig to legacy WarningSettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UIWarningsConfig): WarningSettings {
  const defaults = DEFAULT_CONFIG.ui!.warnings!;

  return {
    enabled: config?.enabled ?? defaults.enabled!,
    warnOnInProgress: config?.warn_on_in_progress ?? defaults.warn_on_in_progress!,
    warnOnInReview: config?.warn_on_in_review ?? defaults.warn_on_in_review!,
  };
}

/**
 * Get current warning settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getWarningSettings(): WarningSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for warnings");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.warnings);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.warnings);
}

/**
 * Re-export UIWarningsConfig for consumers that need the raw type
 */
export type { UIWarningsConfig };
