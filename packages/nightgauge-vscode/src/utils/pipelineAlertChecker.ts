/**
 * Pipeline Alert Checker - Post-run threshold checking for cost and duration
 *
 * Pure utility that compares pipeline run metrics against configurable
 * thresholds and returns alert decisions. Non-critical — never blocks
 * pipeline completion.
 *
 * Cost anomaly detection uses a ratio-based formula:
 *   actual_cost > estimated_cost × cost_anomaly_ratio  AND  actual_cost > cost_anomaly_min_usd
 *
 * When estimated cost is unavailable (estimatedCostUsd === 0), falls back to a
 * conservative flat threshold: actual_cost > cost_anomaly_min_usd × 10.
 *
 * @see Issue #1048 - Automated cost/duration alerting
 * @see Issue #1335 - Replace flat cost threshold with ratio-based anomaly detection
 */

export interface PipelineAlert {
  type: "cost" | "duration";
  actual: number;
  /** For cost alerts: estimated_cost × ratio. For duration alerts: threshold in minutes. */
  threshold: number;
  /** Pre-run estimated cost (0 if unavailable) */
  estimatedCost: number;
  issueNumber: number;
  message: string;
  stageBreakdown?: Array<{
    stage: string;
    actualCost: number;
    estimatedCost: number;
  }>;
}

export interface AlertCheckResult {
  costExceeded: boolean;
  durationExceeded: boolean;
  alerts: PipelineAlert[];
}

export interface AlertThresholds {
  enabled: boolean;
  /** @deprecated Maps to cost_anomaly_min_usd for backward compat */
  cost_threshold_usd?: number;
  /** Multiplier on estimated cost — alert when actual > estimated × ratio (default: 2.0) */
  cost_anomaly_ratio: number;
  /** Minimum cost floor — alert only when actual exceeds this USD amount (default: 3.0) */
  cost_anomaly_min_usd: number;
  duration_threshold_minutes: number;
}

/**
 * Build per-stage cost breakdown for alert messages.
 */
function buildStageBreakdown(
  perStageCosts?: Record<string, number>,
  estimatedPerStageCosts?: Record<string, number>
): Array<{ stage: string; actualCost: number; estimatedCost: number }> | undefined {
  if (!perStageCosts || Object.keys(perStageCosts).length === 0) {
    return undefined;
  }
  return Object.entries(perStageCosts)
    .filter(([, cost]) => cost > 0)
    .map(([stage, actualCost]) => ({
      stage,
      actualCost,
      estimatedCost: estimatedPerStageCosts?.[stage] ?? 0,
    }))
    .sort((a, b) => b.actualCost - a.actualCost);
}

/**
 * Check pipeline run metrics against alerting thresholds.
 *
 * Returns an AlertCheckResult with any exceeded thresholds.
 * When alerting is disabled, returns an empty result.
 */
export function checkPipelineAlerts(params: {
  issueNumber: number;
  costUsd: number;
  /** Model/complexity-aware estimated cost from AutoModelSelector (0 if unavailable) */
  estimatedCostUsd: number;
  durationMinutes: number;
  thresholds: AlertThresholds;
  /** Actual per-stage costs for detailed breakdown */
  perStageCosts?: Record<string, number>;
  /** Estimated per-stage costs for detailed breakdown */
  estimatedPerStageCosts?: Record<string, number>;
}): AlertCheckResult {
  const {
    issueNumber,
    costUsd,
    estimatedCostUsd,
    durationMinutes,
    thresholds,
    perStageCosts,
    estimatedPerStageCosts,
  } = params;

  const result: AlertCheckResult = {
    costExceeded: false,
    durationExceeded: false,
    alerts: [],
  };

  if (!thresholds.enabled) {
    return result;
  }

  const effectiveRatio = thresholds.cost_anomaly_ratio;
  const effectiveMinUsd = thresholds.cost_anomaly_min_usd ?? thresholds.cost_threshold_usd ?? 3.0;

  let anomalous: boolean;
  let thresholdUsd: number;

  if (estimatedCostUsd <= 0) {
    // No estimate available — fall back to conservative flat threshold
    // (cost_anomaly_min_usd × 10 approximates legacy cost_threshold_usd behavior)
    thresholdUsd = effectiveMinUsd * 10;
    anomalous = costUsd > thresholdUsd;
  } else {
    thresholdUsd = estimatedCostUsd * effectiveRatio;
    anomalous = costUsd > thresholdUsd && costUsd > effectiveMinUsd;
  }

  if (anomalous) {
    const stageBreakdown = buildStageBreakdown(perStageCosts, estimatedPerStageCosts);
    result.costExceeded = true;
    result.alerts.push({
      type: "cost",
      actual: costUsd,
      threshold: thresholdUsd,
      estimatedCost: estimatedCostUsd,
      issueNumber,
      message:
        estimatedCostUsd > 0
          ? `Pipeline run #${issueNumber} cost $${costUsd.toFixed(2)} exceeded ` +
            `${effectiveRatio}× estimated cost ($${estimatedCostUsd.toFixed(2)})`
          : `Pipeline run #${issueNumber} cost $${costUsd.toFixed(2)} exceeded ` +
            `flat threshold of $${thresholdUsd.toFixed(2)} (no estimate available)`,
      stageBreakdown,
    });
  }

  if (durationMinutes > thresholds.duration_threshold_minutes) {
    result.durationExceeded = true;
    result.alerts.push({
      type: "duration",
      actual: durationMinutes,
      threshold: thresholds.duration_threshold_minutes,
      estimatedCost: 0,
      issueNumber,
      message: `Pipeline run #${issueNumber} duration ${durationMinutes.toFixed(1)}min exceeded threshold of ${thresholds.duration_threshold_minutes}min`,
    });
  }

  return result;
}
