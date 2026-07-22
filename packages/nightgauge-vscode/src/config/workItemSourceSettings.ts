/**
 * Work-Item Source Settings for Nightgauge
 *
 * Provides typed access to work-item source configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #2571 - Add work item source configuration and provider selection wiring
 */

import { ConfigBridge } from "../services/ConfigBridge";
import { type WorkItemSourceConfig, DEFAULT_CONFIG } from "./schema";

export type { WorkItemSourceConfig };

const DEFAULTS = DEFAULT_CONFIG.work_item_source!;

/**
 * Get current work-item source configuration from ConfigBridge.
 *
 * Reads from the 6-tier merged configuration. If ConfigBridge is not
 * initialized, returns defaults and logs a debug message.
 *
 * Default mode is "github" — preserves current ProjectBoardService behavior
 * for any existing config file that does not specify work_item_source.
 */
export function getWorkItemSourceConfig(): WorkItemSourceConfig {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for work_item_source");
    return { ...DEFAULTS };
  }

  const workItemSource = configBridge.getValue<WorkItemSourceConfig>("work_item_source");
  return {
    mode: workItemSource?.mode ?? DEFAULTS.mode,
    provider_options: workItemSource?.provider_options,
  };
}
