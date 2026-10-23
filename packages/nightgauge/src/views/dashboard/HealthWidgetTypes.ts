/**
 * HealthWidgetTypes - TypeScript interfaces for pipeline health dashboard data
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */

/**
 * Configurable weights for health score computation.
 * Weights are auto-normalized (divided by their sum) so users
 * can adjust individual values without recalculating the total.
 */
export interface HealthScoreWeights {
  successRate: number; // default 0.30
  costTrend: number; // default 0.30
  failureRate: number; // default 0.25
  cacheHitRate: number; // default 0.15
}

export const DEFAULT_HEALTH_WEIGHTS: HealthScoreWeights = {
  successRate: 0.3,
  costTrend: 0.3,
  failureRate: 0.25,
  cacheHitRate: 0.15,
};

/**
 * Status thresholds for mapping health scores to labels
 */
interface HealthStatusThresholds {
  excellent: number; // >= 90
  good: number; // >= 70
  fair: number; // >= 50
  poor: number; // >= 30
  // Below poor threshold = critical
}

const DEFAULT_HEALTH_THRESHOLDS: HealthStatusThresholds = {
  excellent: 90,
  good: 70,
  fair: 50,
  poor: 30,
};

export type HealthStatus = "excellent" | "good" | "fair" | "poor" | "critical";

/**
 * Overall health summary with composite score
 */
interface HealthSummary {
  score: number; // 0-100
  status: HealthStatus;
  components: HealthComponent[];
}

/**
 * Individual health component (one per metric)
 */
export interface HealthComponent {
  name: string;
  score: number; // 0-100
  weight: number; // 0-1 (normalized)
  trend: "improving" | "stable" | "degrading";
  label: string;
  insufficientData?: boolean;
  insufficientDataMessage?: string;
}

/**
 * Whether higher values are "better" for the metric.
 * Drives the color coding of the sparkline arrow: a falling cost is good
 * (green ↓) while a falling success rate is bad (red ↓).
 *
 * - "higher-is-better": success rate, cache hit rate
 * - "lower-is-better":  cost per run, token usage
 */
export type MetricPolarity = "higher-is-better" | "lower-is-better";

/**
 * Data for a sparkline trend chart
 *
 * `trend` is the literal direction of the recent values (up = numbers rose,
 * down = numbers fell). `polarity` is required so the renderer can pick the
 * correct color — green when the direction is good for this metric, red when
 * the direction is bad. Without polarity, lower-is-better metrics (cost,
 * tokens) get inverted color coding.
 *
 * Setting `treatZeroAsMissing` to false tells the renderer to count `0`
 * values when computing the "avg of N runs" footer label — needed for
 * Success Rate where 0 (all failed) is a valid observation, not missing data.
 */
export interface TrendSparkline {
  metric: string;
  label: string;
  data: number[]; // last N data points
  trend: "up" | "down" | "stable";
  polarity: MetricPolarity;
  unit: string;
  treatZeroAsMissing?: boolean;
}

/**
 * Active alert from drift detection or threshold violations
 */
export interface ActiveAlert {
  level: "info" | "warning" | "critical";
  stage: string;
  metric: string;
  message: string;
  timestamp: string;
}

/**
 * Actionable config change for a recommendation (mirrors SDK RecommendationAction)
 */
interface RecommendationAction {
  type: "config-patch" | "info-only";
  configPath: string;
  suggestedValue: unknown;
  label: string;
}

/**
 * Recommendation from token efficiency analysis
 */
export interface Recommendation {
  title: string;
  description: string;
  estimatedSavingsUsd: number;
  category: string;
  severity: string;
  action?: RecommendationAction;
}

/**
 * Prediction accuracy summary from work-time feedback
 */
export interface PredictionAccuracy {
  totalObservations: number;
  avgEstimated: number;
  avgActual: number;
  accuracyPercent: number;
  trend: "improving" | "stable" | "degrading";
}

/**
 * Available time ranges for the health trend chart.
 * Default: '7d' (weekly view for fast AI pipeline feedback).
 */
export type TrendRange = "24h" | "7d" | "30d" | "90d";

/** Human-readable labels for trend range options */
export const TREND_RANGE_LABELS: Record<TrendRange, string> = {
  "24h": "Last 24 Hours",
  "7d": "Last 7 Days",
  "30d": "Last 30 Days",
  "90d": "Last 90 Days",
};

/** Default trend range — 7 days gives a weekly picture */
export const DEFAULT_TREND_RANGE: TrendRange = "7d";

/**
 * Single bucket in the trend chart (a day or an hour depending on range)
 * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
 */
export interface TrendChartDay {
  date: string; // YYYY-MM-DD for daily, YYYY-MM-DDTHH for hourly
  avgScore: number;
  count: number; // runs in this bucket
}

/**
 * Trend analysis comparing recent vs prior period
 * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
 */
export interface TrendAnalysis {
  direction: "improving" | "stable" | "declining";
  message: string;
  periodDays: number;
  percentChange: number;
}

/**
 * An active health policy applied to the current or most recent pipeline run
 * @see Issue #1395 - Health-gated pipeline policies
 */
export interface ActiveHealthPolicy {
  tier: "warning" | "critical" | "emergency";
  policy: string;
  reason: string;
}

/**
 * Per-dimension sparkline from time-series trends.jsonl
 * @see Issue #1411 - Health trend persistence and dashboard sparklines
 */
export interface DimensionSparkline {
  dimension: string; // HealthDimension value
  label: string; // Human-readable: 'Token Economics', 'Cost Health', etc.
  data: number[]; // Last N scores (0-100), oldest first, most recent last
  trend: "improving" | "stable" | "declining";
}

/**
 * Complete data model for the health widget
 */
export interface HealthWidgetData {
  summary: HealthSummary;
  sparklines: TrendSparkline[];
  alerts: ActiveAlert[];
  recommendations: Recommendation[];
  predictionAccuracy: PredictionAccuracy | null;
  lastUpdated: string;
  isEmpty: boolean;
  /** Trend chart data for the selected range (Issue #789) */
  trendChart?: TrendChartDay[];
  /** Trend analysis summary (Issue #789) */
  trendAnalysis?: TrendAnalysis | null;
  /** Currently selected trend range */
  trendRange?: TrendRange;
  /** Per-dimension sparklines from time-series trends.jsonl (Issue #1411) */
  dimensionSparklines?: DimensionSparkline[];
}

/**
 * Map health status to display color CSS class
 */
export function getHealthStatusColor(status: HealthStatus): string {
  switch (status) {
    case "excellent":
      return "health-excellent";
    case "good":
      return "health-good";
    case "fair":
      return "health-fair";
    case "poor":
      return "health-poor";
    case "critical":
      return "health-critical";
  }
}

/**
 * Derive health status from a numeric score using thresholds
 */
export function getHealthStatus(
  score: number,
  thresholds: HealthStatusThresholds = DEFAULT_HEALTH_THRESHOLDS
): HealthStatus {
  if (score >= thresholds.excellent) return "excellent";
  if (score >= thresholds.good) return "good";
  if (score >= thresholds.fair) return "fair";
  if (score >= thresholds.poor) return "poor";
  return "critical";
}
