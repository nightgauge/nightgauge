/**
 * PipelinePolicyOverrides - Health-gated per-run policy overrides
 *
 * Defines the interface for temporary pipeline adjustments applied
 * when health scores fall below configured thresholds.
 *
 * All overrides are per-run only — they never persist to config.yaml.
 *
 * @see Issue #1395 - Health-gated pipeline policies
 */

export type HealthPolicyTier = "none" | "warning" | "critical" | "emergency";

export interface PipelinePolicyOverrides {
  tier: HealthPolicyTier;
  /** Additional retry attempts: 0 (none), 1 (warning), or 2 (critical/emergency) */
  retryBudgetIncrease: number;
  /** Escalate all non-bookend stages by one model tier (critical/emergency) */
  escalateAllStages: boolean;
  /** Pause auto-routing and use default model selection (emergency only) */
  pauseAutoRouting: boolean;
  /** Human-readable reasons for each active policy */
  reasons: string[];
  /** Health score that triggered the policies */
  score: number;
  /** ISO 8601 timestamp of policy evaluation */
  timestamp: string;
}
