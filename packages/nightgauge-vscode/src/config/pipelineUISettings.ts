/**
 * Pipeline UI Settings for Nightgauge
 *
 * Provides typed access to pipeline UI configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #476 - Refactor tree providers, extension.ts, and settings.ts to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import { type UIPipelineUIConfig, DEFAULT_CONFIG } from "./schema";

/**
 * Pipeline UI configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UIPipelineUIConfig).
 */
export interface PipelineUISettings {
  /** When true, auto-run next stage on completion */
  autoContinue: boolean;

  /** Delay in ms before auto-continuing (0-10000) */
  autoContinueDelay: number;
}

/**
 * Default pipeline UI settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.pipeline from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_PIPELINE_UI_SETTINGS: PipelineUISettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.pipeline
);

/**
 * Map ConfigBridge UIPipelineUIConfig to legacy PipelineUISettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UIPipelineUIConfig): PipelineUISettings {
  const defaults = DEFAULT_CONFIG.ui!.pipeline!;

  return {
    autoContinue: config?.auto_continue ?? defaults.auto_continue!,
    autoContinueDelay: config?.auto_continue_delay ?? defaults.auto_continue_delay!,
  };
}

/**
 * Get current pipeline UI settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getPipelineUISettings(): PipelineUISettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for pipeline UI");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.pipeline);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.pipeline);
}

/**
 * Re-export UIPipelineUIConfig for consumers that need the raw type
 */
export type { UIPipelineUIConfig };
