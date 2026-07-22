/**
 * Limits Settings for Nightgauge
 *
 * Provides typed access to usage limits configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #1333 - Show Claude Code usage limits and alert users
 */

import { ConfigBridge } from "../services/ConfigBridge";
import { type UILimitsConfig, DEFAULT_CONFIG } from "./schema";

/**
 * Typed limits settings interface for consumers
 */
export interface LimitsSettings {
  /** Monthly API cost budget in USD. 0 = disabled. */
  monthlyBudgetUsd: number;
  /** Percentage of monthly budget at which a warning notification fires */
  warningThresholdPct: number;
  /** Percentage of monthly budget at which a critical alert fires */
  criticalThresholdPct: number;
  /** How often (in seconds) to check usage against the budget threshold */
  pollingIntervalSeconds: number;
  /** Percentage of platform quota at which a warning notification fires (default: 80) */
  quotaWarningThresholdPct: number;
  /** Percentage of platform quota at which a critical alert fires (default: 90) */
  quotaCriticalThresholdPct: number;
  /** Percentage of platform quota at which a block notification fires (default: 100) */
  quotaBlockThresholdPct: number;
}

const DEFAULTS = DEFAULT_CONFIG.ui!.limits!;

/**
 * Map UILimitsConfig (snake_case) to LimitsSettings (camelCase)
 */
function mapToLimitsSettings(config?: UILimitsConfig): LimitsSettings {
  return {
    monthlyBudgetUsd: config?.monthly_budget_usd ?? DEFAULTS.monthly_budget_usd!,
    warningThresholdPct: config?.warning_threshold_pct ?? DEFAULTS.warning_threshold_pct!,
    criticalThresholdPct: config?.critical_threshold_pct ?? DEFAULTS.critical_threshold_pct!,
    pollingIntervalSeconds: config?.polling_interval_seconds ?? DEFAULTS.polling_interval_seconds!,
    quotaWarningThresholdPct:
      config?.quota_warning_threshold_pct ?? DEFAULTS.quota_warning_threshold_pct!,
    quotaCriticalThresholdPct:
      config?.quota_critical_threshold_pct ?? DEFAULTS.quota_critical_threshold_pct!,
    quotaBlockThresholdPct:
      config?.quota_block_threshold_pct ?? DEFAULTS.quota_block_threshold_pct!,
  };
}

/**
 * Get current usage limits settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration. If ConfigBridge is not
 * initialized, returns defaults and logs a warning.
 */
export function getLimitsSettings(): LimitsSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for limits");
    return mapToLimitsSettings();
  }

  const ui = configBridge.getUI();
  return mapToLimitsSettings(ui?.limits);
}

export type { UILimitsConfig };
