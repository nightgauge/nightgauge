/**
 * Output Window Settings for Nightgauge
 *
 * Provides typed access to output window configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #476 - Refactor tree providers, extension.ts, and settings.ts to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import { type UIOutputWindowConfig, type VerboseLevel, DEFAULT_CONFIG } from "./schema";

/**
 * Re-export VerboseLevel type for backward compatibility.
 */
export type { VerboseLevel };

/**
 * Output window configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UIOutputWindowConfig).
 */
export interface OutputWindowSettings {
  /** When true, output window opens automatically on pipeline start */
  autoOpen: boolean;

  /** When true, output scrolls to latest content */
  autoScroll: boolean;

  /** Controls amount of detail shown */
  verboseLevel: VerboseLevel;

  /** When true, shows real-time token/cost tracking */
  showTokenUsage: boolean;

  /** When true, wraps long lines */
  wordWrap: boolean;

  /**
   * When true, rebuilds archived tabs from on-disk session logs on the first
   * panel open after a VSCode reload (Issue #2818).
   */
  rehydrateFromLogs: boolean;
}

/**
 * Default output window settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.output_window from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_OUTPUT_WINDOW_SETTINGS: OutputWindowSettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.output_window
);

/**
 * Map ConfigBridge UIOutputWindowConfig to legacy OutputWindowSettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UIOutputWindowConfig): OutputWindowSettings {
  const defaults = DEFAULT_CONFIG.ui!.output_window!;

  return {
    autoOpen: config?.auto_open ?? defaults.auto_open!,
    autoScroll: config?.auto_scroll ?? defaults.auto_scroll!,
    verboseLevel: config?.verbose_level ?? defaults.verbose_level!,
    showTokenUsage: config?.show_token_usage ?? defaults.show_token_usage!,
    wordWrap: config?.word_wrap ?? defaults.word_wrap!,
    rehydrateFromLogs: config?.rehydrate_from_logs ?? defaults.rehydrate_from_logs!,
  };
}

/**
 * Get current output window settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getOutputWindowSettings(): OutputWindowSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for output window");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.output_window);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.output_window);
}

/**
 * Re-export UIOutputWindowConfig for consumers that need the raw type
 */
export type { UIOutputWindowConfig };
