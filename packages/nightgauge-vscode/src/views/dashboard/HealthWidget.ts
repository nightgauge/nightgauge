/**
 * HealthWidget - Health widget data aggregation service
 *
 * Computes pipeline health scores from weighted metrics, generates sparkline
 * trend data, collects active alerts from SDK drift detection, and surfaces
 * top recommendations from token efficiency analysis.
 *
 * All SDK analysis calls are wrapped in try/catch — if SDK dist is unavailable
 * or data is insufficient, the widget degrades gracefully with empty arrays.
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */

import type { DashboardState, PipelineRunSummary } from "./DashboardState";
import type { DependabotPRData } from "../../services/DependabotPRService";
import type {
  HealthWidgetData,
  HealthScoreWeights,
  HealthComponent,
  TrendSparkline,
  ActiveAlert,
  Recommendation,
  PredictionAccuracy,
  TrendChartDay,
  TrendAnalysis,
  TrendRange,
  DimensionSparkline,
} from "./HealthWidgetTypes";
import { DEFAULT_HEALTH_WEIGHTS, DEFAULT_TREND_RANGE, getHealthStatus } from "./HealthWidgetTypes";
import { HealthScoreHistoryWriter, HealthScoreHistoryReader } from "../../utils/healthScoreHistory";
import type { HealthScoreSnapshot } from "../../schemas/healthScoreHistory";

/**
 * HealthWidgetService aggregates data from DashboardState, SDK analysis
 * modules, and work-time feedback into a single HealthWidgetData object
 * for rendering by HealthWidgetHtml.
 */
export class HealthWidgetService {
  constructor(
    private readonly dashboardState: DashboardState,
    private readonly workspacePath: string | undefined,
    private readonly weights: HealthScoreWeights = DEFAULT_HEALTH_WEIGHTS,
    private readonly cacheAlertThreshold: number = 40
  ) {}

  /**
   * Get complete health widget data
   *
   * Orchestrates all data sources and handles empty state.
   *
   * @param trendRange - Time range for the health trend chart (default: 7d)
   */
  async getData(
    trendRange: TrendRange = DEFAULT_TREND_RANGE,
    dependabotData?: DependabotPRData | null
  ): Promise<HealthWidgetData> {
    const history = this.dashboardState.getHistory();

    if (history.length === 0) {
      return {
        summary: { score: 0, status: "critical", components: [] },
        sparklines: [],
        alerts: [],
        recommendations: [],
        predictionAccuracy: null,
        lastUpdated: new Date().toISOString(),
        isEmpty: true,
      };
    }

    const components = this.computeHealthComponents();
    const score = this.computeWeightedScore(components);
    const status = getHealthStatus(score);

    const [sparklines, baseAlerts, recommendations, predictionAccuracy] = await Promise.all([
      Promise.resolve(this.getSparklines()),
      this.getActiveAlerts(),
      this.getRecommendations(),
      this.getPredictionAccuracy(),
    ]);

    // Inject dependabot stale-PR alert (Issue #3116)
    const alerts = [...baseAlerts];
    if (dependabotData && dependabotData.staleCount > 0) {
      const level: ActiveAlert["level"] = dependabotData.securityCount > 0 ? "critical" : "warning";
      const label =
        `${dependabotData.staleCount} stale dependabot PR${dependabotData.staleCount > 1 ? "s" : ""} > 7 days` +
        (dependabotData.securityCount > 0 ? ` (${dependabotData.securityCount} security)` : "");
      alerts.push({
        level,
        stage: "dependency-health",
        metric: "stale-dependabot-prs",
        message: label,
        timestamp: dependabotData.fetchedAt || new Date().toISOString(),
      });
    }

    const finalScore = score;
    const finalStatus = status;

    // Load persisted health history for configurable trend range (Issue #789)
    let trendChart: TrendChartDay[] = [];
    let trendAnalysis: TrendAnalysis | null = null;
    if (this.workspacePath) {
      try {
        const rangeStart = new Date();
        let comparisonBuckets: number;

        if (trendRange === "24h") {
          rangeStart.setHours(rangeStart.getHours() - 24);
          comparisonBuckets = 12; // 12h vs 12h
        } else {
          const days = trendRange === "7d" ? 7 : trendRange === "30d" ? 30 : 90;
          rangeStart.setDate(rangeStart.getDate() - days);
          comparisonBuckets = trendRange === "7d" ? 3 : trendRange === "30d" ? 7 : 14;
        }

        const snapshots = await HealthScoreHistoryReader.readDateRange(
          this.workspacePath,
          rangeStart,
          new Date()
        );

        trendChart =
          trendRange === "24h"
            ? HealthScoreHistoryReader.aggregateByHour(snapshots)
            : HealthScoreHistoryReader.aggregateByDay(snapshots);

        trendAnalysis = HealthScoreHistoryReader.analyzeTrend(trendChart, comparisonBuckets);
      } catch {
        // Non-critical — degrade gracefully
      }
    }

    // Load per-dimension sparklines from trends.jsonl (Issue #1411)
    let dimensionSparklines: DimensionSparkline[] | undefined;
    if (this.workspacePath) {
      try {
        const { HealthTrendsWriter } =
          await import("@nightgauge/sdk/dist/analysis/health/HealthTrendsWriter");
        const recentEntries = await HealthTrendsWriter.read(this.workspacePath, { limit: 20 });
        if (recentEntries.length > 0) {
          dimensionSparklines = buildDimensionSparklines(recentEntries);
        }
      } catch {
        // Non-critical — sparklines simply won't render
      }
    }

    return {
      summary: { score: finalScore, status: finalStatus, components },
      sparklines,
      alerts,
      recommendations,
      predictionAccuracy,
      lastUpdated: new Date().toISOString(),
      isEmpty: false,
      trendChart,
      trendAnalysis,
      trendRange,
      dimensionSparklines,
    };
  }

  /**
   * Compute individual health components from DashboardState trend data
   *
   * Each component produces a 0-100 score based on the underlying metric.
   */
  computeHealthComponents(): HealthComponent[] {
    const components: HealthComponent[] = [];
    const normalizedWeights = this.normalizeWeights(this.weights);
    const history = this.dashboardState.getHistory();
    const runsNeeded = Math.max(1, 6 - history.length);

    // 1. Success Rate (weight 0.30)
    const aggregates = this.dashboardState.getAggregates("all");
    const successScore = Math.round(aggregates.successRate * 100);
    components.push({
      name: "successRate",
      score: successScore,
      weight: normalizedWeights.successRate,
      trend: successScore >= 80 ? "improving" : successScore >= 50 ? "stable" : "degrading",
      label: "Success Rate",
    });

    // 2. Cost Trend (weight 0.30 — stable or decreasing = healthy)
    const costTrend = this.dashboardState.getCostTrend();
    const costScore = costTrend.hasEnoughData
      ? computeCostTrendScore(costTrend.percentChange)
      : 100; // No trend data yet — assume healthy until proven otherwise
    components.push({
      name: "costTrend",
      score: costScore,
      weight: normalizedWeights.costTrend,
      trend: !costTrend.hasEnoughData
        ? "stable"
        : costTrend.percentChange < -5
          ? "improving"
          : costTrend.percentChange > 15
            ? "degrading"
            : "stable",
      label: "Cost Trend",
      insufficientData: !costTrend.hasEnoughData,
      insufficientDataMessage: !costTrend.hasEnoughData
        ? `Need ${runsNeeded} more run${runsNeeded !== 1 ? "s" : ""} for trend data`
        : undefined,
    });

    // 3. Reliability (weight 0.25 — inverse of failure rate)
    const failedRuns = history.filter((r) => r.status === "failed").length;
    const failureRate = history.length > 0 ? failedRuns / history.length : 0;
    const reliabilityScore = Math.round((1 - failureRate) * 100);
    components.push({
      name: "failureRate",
      score: reliabilityScore,
      weight: normalizedWeights.failureRate,
      trend: reliabilityScore >= 80 ? "improving" : reliabilityScore >= 50 ? "stable" : "degrading",
      label: "Reliability",
    });

    // 4. Cache Hit Rate (weight 0.15)
    // Filter to runs that actually used tokens FIRST, then take the recent
    // window. Slicing first and filtering after reported 0% whenever the five
    // most recent runs were zero-token records (early-failed / no-op / partial
    // runs), even though real runs cache at ~99%. Filter-then-slice makes the
    // metric reflect the most recent runs that genuinely exercised the cache.
    const runsWithCacheData = history.filter(hasValidCacheData).slice(0, 5);
    const avgCacheHitRate =
      runsWithCacheData.length > 0
        ? runsWithCacheData.reduce((sum, r) => sum + (r.efficiency?.cacheHitRate ?? 0), 0) /
          runsWithCacheData.length
        : 0;
    const cacheScore = Math.round(avgCacheHitRate * 100);
    const cacheThreshold = this.cacheAlertThreshold;
    components.push({
      name: "cacheHitRate",
      score: cacheScore,
      weight: normalizedWeights.cacheHitRate,
      trend:
        cacheScore >= cacheThreshold + 10
          ? "improving"
          : cacheScore >= cacheThreshold
            ? "stable"
            : "degrading",
      label: "Cache Hit Rate",
    });

    return components;
  }

  /**
   * Compute weighted composite score from health components
   *
   * Uses auto-normalized weights so the sum doesn't need to equal 1.0.
   */
  computeWeightedScore(components: HealthComponent[]): number {
    if (components.length === 0) return 0;

    let weightedSum = 0;
    let totalWeight = 0;

    for (const c of components) {
      weightedSum += c.score * c.weight;
      totalWeight += c.weight;
    }

    return totalWeight > 0 ? Math.round(weightedSum / totalWeight) : 0;
  }

  /**
   * Get sparkline trend data for the "Recent Activity" section.
   *
   * Returns raw per-run metrics — cost in dollars, total tokens — that the
   * health-component cards above don't already surface. Success rate and
   * cache hit rate were previously here as a fourth and third sparkline, but
   * those values are already shown as composite scores in the Pipeline
   * Health summary directly above this section. Duplicating them with a
   * different time window (last-5-runs) next to the all-time score caused
   * predictable user confusion ("why is Success Rate 91 here and 0% there?").
   *
   * The renderer labels this section "Recent Activity (Last N Runs)" so the
   * timeframe is unambiguous.
   */
  getSparklines(limit: number = 10): TrendSparkline[] {
    const sparklines: TrendSparkline[] = [];

    // Cost trend sparkline.
    // The arrow direction tracks the literal data direction (up = cost rose).
    // Color polarity is set so a rising cost reads red and a falling cost reads green.
    const costData = this.dashboardState.getHistoricalData("cost", limit);
    if (costData.length > 0) {
      const costTrend = this.dashboardState.getCostTrend();
      let costDirection: "up" | "down" | "stable" = "stable";
      if (costTrend.hasEnoughData) {
        if (costTrend.percentChange > 5) costDirection = "up";
        else if (costTrend.percentChange < -5) costDirection = "down";
      }
      sparklines.push({
        metric: "cost",
        label: "Cost per Run",
        data: costData,
        trend: costDirection,
        polarity: "lower-is-better",
        unit: "USD",
      });
    }

    // Token usage sparkline — same polarity story as cost.
    const tokenData = this.dashboardState.getHistoricalData("tokens", limit);
    if (tokenData.length > 0) {
      const tokenTrend = this.dashboardState.getTokenTrend();
      sparklines.push({
        metric: "tokens",
        label: "Tokens per Run",
        data: tokenData,
        trend: tokenTrend.direction,
        polarity: "lower-is-better",
        unit: "tokens",
      });
    }

    return sparklines;
  }

  /**
   * Get active alerts from SDK drift detection
   *
   * Gracefully degrades if SDK dist is unavailable.
   */
  async getActiveAlerts(): Promise<ActiveAlert[]> {
    const alerts: ActiveAlert[] = [];

    try {
      // Check if we can import FailurePatternDetector from SDK dist
      const { FailurePatternDetector } =
        await import("@nightgauge/sdk/dist/analysis/FailurePatternDetector");

      const history = this.dashboardState.getHistory();
      if (history.length < 3) return alerts;

      // Convert DashboardState history to ExecutionHistoryRecord[] format.
      // Each run is flattened into per-stage records matching the SDK type.
      // Filter out stages without tokenUsage to avoid zero-value records (#986).
      const records = history.slice(0, 20).flatMap((run) =>
        run.stages
          .filter((s) => s.tokenUsage != null)
          .map((s) => ({
            issueNumber: run.issueNumber,
            stage: s.stage,
            success: s.status === "complete",
            retries: 0,
            inputTokens: s.tokenUsage?.inputTokens ?? 0,
            outputTokens: s.tokenUsage?.outputTokens ?? 0,
            cacheReadTokens: s.tokenUsage?.cacheReadTokens,
            cacheCreationTokens: s.tokenUsage?.cacheCreationTokens,
            costUsd: s.tokenUsage?.costUsd ?? 0,
            durationMs: s.durationMs ?? 0,
            timestamp: s.completedAt?.toISOString() ?? run.startedAt.toISOString(),
          }))
      );

      const detector = new FailurePatternDetector();
      const result = detector.analyze(records);

      for (const finding of result.findings.slice(0, 10)) {
        alerts.push({
          level:
            finding.severity === "infrastructure"
              ? "critical"
              : finding.severity === "manual-fix"
                ? "warning"
                : "info",
          stage: finding.affectedStages?.[0] ?? "pipeline",
          metric: finding.category,
          message: finding.description ?? finding.category,
          timestamp: new Date().toISOString(),
        });
      }
    } catch {
      // SDK analysis not available — graceful degradation
    }

    // Append cost anomaly alerts from recent history (Issue #1335)
    const costAnomalyAlerts = await this.detectCostAnomalyAlerts();
    alerts.push(...costAnomalyAlerts);

    return alerts;
  }

  /**
   * Detect cost anomaly alerts from recent pipeline history.
   *
   * Compares each completed run's actual cost against a model/complexity-aware
   * estimate using AutoModelSelector. Fires an alert when:
   *   actual_cost > estimated_cost × ratio  AND  actual_cost > minUsd
   *
   * Gracefully degrades if AutoModelSelector import fails.
   *
   * @see Issue #1335 - Cost anomaly alerting
   */
  private async detectCostAnomalyAlerts(
    thresholdRatio: number = 2.0,
    minUsd: number = 3.0
  ): Promise<ActiveAlert[]> {
    const alerts: ActiveAlert[] = [];
    try {
      const { AutoModelSelector, CalibrationService } = await import("@nightgauge/sdk");
      const selector = new AutoModelSelector();
      const calibrationPath = this.workspacePath
        ? CalibrationService.getDefaultPath(this.workspacePath)
        : null;
      const calibration = calibrationPath ? await CalibrationService.load(calibrationPath) : null;
      const history = this.dashboardState.getHistory().slice(0, 10);

      for (const run of history) {
        if (run.status !== "complete") continue;

        const actualCost = run.usage.costUsd;
        if (actualCost <= minUsd) continue;

        // Derive approximate size label from routing path
        let sizeLabel = "M";
        if (run.routing?.route === "trivial") {
          sizeLabel = "S";
        } else if (run.routing?.route === "extensive") {
          sizeLabel = "L";
        }

        // Issue #3216: HealthWidget shows aggregate cost-anomaly history, so
        // we explicitly read the `elevated` calibration bucket (the natural
        // baseline) rather than the operator's currently-active mode. This
        // matches today's behavior and keeps the alert calibration stable as
        // operators toggle between modes.
        const estimate = selector.estimatePipelineCost(
          { labels: [`size:${sizeLabel}`], title: run.title },
          run.routing?.skippedStages ?? [],
          calibration,
          "elevated"
        );
        const expectedCost = estimate.totalEstimatedCost;

        if (expectedCost > 0 && actualCost > expectedCost * thresholdRatio && actualCost > minUsd) {
          // Find top stage by actual cost for the stage field
          const topStage = run.stages
            .filter((s) => s.tokenUsage?.costUsd)
            .sort((a, b) => (b.tokenUsage?.costUsd ?? 0) - (a.tokenUsage?.costUsd ?? 0))[0];

          alerts.push({
            level: "warning",
            stage: topStage?.stage ?? "pipeline",
            metric: "cost_anomaly",
            message: `Run #${run.issueNumber} cost $${actualCost.toFixed(2)} (${(actualCost / expectedCost).toFixed(1)}× estimated $${expectedCost.toFixed(2)})`,
            timestamp: run.completedAt?.toISOString() ?? run.startedAt.toISOString(),
          });
        }
      }
    } catch {
      // Graceful degradation — AutoModelSelector unavailable
    }
    return alerts;
  }

  /**
   * Get top recommendations from token efficiency analysis
   *
   * Gracefully degrades if SDK dist is unavailable.
   */
  async getRecommendations(): Promise<Recommendation[]> {
    const recommendations: Recommendation[] = [];

    try {
      const { TokenEfficiencyAnalyzer } =
        await import("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer");

      const history = this.dashboardState.getHistory();
      if (history.length < 3) return recommendations;

      // Convert to ExecutionHistoryRecord[] format (flat per-stage records).
      // Filter out stages without tokenUsage to avoid zero-value records (#986).
      const records = history.slice(0, 20).flatMap((run) =>
        run.stages
          .filter((s) => s.tokenUsage != null)
          .map((s) => ({
            issueNumber: run.issueNumber,
            stage: s.stage,
            success: s.status === "complete",
            retries: 0,
            inputTokens: s.tokenUsage?.inputTokens ?? 0,
            outputTokens: s.tokenUsage?.outputTokens ?? 0,
            cacheReadTokens: s.tokenUsage?.cacheReadTokens,
            cacheCreationTokens: s.tokenUsage?.cacheCreationTokens,
            costUsd: s.tokenUsage?.costUsd ?? 0,
            durationMs: s.durationMs ?? 0,
            timestamp: s.completedAt?.toISOString() ?? run.startedAt.toISOString(),
          }))
      );

      const analyzer = new TokenEfficiencyAnalyzer();
      const result = analyzer.analyze(records);

      // Sort findings by estimated savings (descending) and take top 5.
      // Minimum threshold of $0.005 per occurrence suppresses sub-cent noise.
      const sortedFindings = result.wastePatterns
        .filter((f: { estimatedSavingsUsd?: number }) => (f.estimatedSavingsUsd ?? 0) >= 0.005)
        .sort(
          (a: { estimatedSavingsUsd?: number }, b: { estimatedSavingsUsd?: number }) =>
            (b.estimatedSavingsUsd ?? 0) - (a.estimatedSavingsUsd ?? 0)
        )
        .slice(0, 5);

      for (const finding of sortedFindings) {
        recommendations.push({
          title:
            finding.category?.replace(/-/g, " ").replace(/^\w/, (c: string) => c.toUpperCase()) ??
            "Optimization",
          description: finding.description ?? `Reduce ${finding.category} to save tokens`,
          estimatedSavingsUsd: finding.estimatedSavingsUsd ?? 0,
          category: finding.category ?? "efficiency",
          severity: finding.severity ?? "info",
          action: finding.action ?? undefined,
        });
      }
    } catch {
      // SDK analysis not available — graceful degradation
    }

    return recommendations;
  }

  /**
   * Get prediction accuracy from work-time feedback, falling back to the
   * complexity model's prediction_accuracy section when no work-time
   * observations are available yet.
   *
   * Primary source: work-time feedback (estimated vs actual minutes)
   * Fallback source: complexity model size-bucket prediction accuracy
   */
  async getPredictionAccuracy(): Promise<PredictionAccuracy | null> {
    if (!this.workspacePath) return null;

    try {
      const insights = await this.dashboardState.getVelocityInsights(this.workspacePath);

      if (insights) {
        const accuracyTrend = await this.dashboardState.getAccuracyTrend(this.workspacePath);
        let trend: "improving" | "stable" | "degrading" = "stable";
        if (accuracyTrend?.hasEnoughData) {
          trend = accuracyTrend.improving ? "improving" : "degrading";
        }
        return {
          totalObservations: insights.totalObservations,
          avgEstimated: insights.avgEstimated,
          avgActual: insights.avgActual,
          accuracyPercent: insights.accuracyPercent,
          trend,
        };
      }

      // Fallback: use complexity model's size-bucket prediction accuracy
      const modelAccuracy = await this.dashboardState.getPredictionAccuracyFromModel(
        this.workspacePath
      );
      if (modelAccuracy) {
        return {
          totalObservations: modelAccuracy.totalPredictions,
          avgEstimated: 0,
          avgActual: 0,
          accuracyPercent: modelAccuracy.accuracyPercent,
          trend: "stable",
        };
      }

      return null;
    } catch {
      return null;
    }
  }

  /**
   * Record a health score snapshot to disk after pipeline completion.
   *
   * Computes current health components, builds a snapshot, and persists it
   * to health-history.jsonl. Non-critical: logs warnings on failure, never throws.
   *
   * @param issueNumber - Issue number from the completed pipeline run
   * @param costUsd - Total cost of the completed run
   * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
   */
  async recordSnapshot(issueNumber: number, costUsd: number): Promise<void> {
    if (!this.workspacePath) return;

    try {
      const components = this.computeHealthComponents();
      const score = this.computeWeightedScore(components);
      const status = getHealthStatus(score);

      // Extract cache hit rate from the most recent runs that actually used
      // tokens (exclude zero-token runs #989). Filter BEFORE slicing — slicing
      // first reported 0% when the recent window was all zero-token records.
      const history = this.dashboardState.getHistory();
      const runsWithCacheData = history.filter(hasValidCacheData).slice(0, 5);
      const avgCacheHitRate =
        runsWithCacheData.length > 0
          ? runsWithCacheData.reduce((sum, r) => sum + (r.efficiency?.cacheHitRate ?? 0), 0) /
            runsWithCacheData.length
          : 0;

      // Build component scores map
      const componentScores: Record<string, number> = {};
      for (const c of components) {
        componentScores[c.name] = c.score;
      }

      const snapshot: HealthScoreSnapshot = {
        schema_version: "1",
        timestamp: new Date().toISOString(),
        score,
        status,
        components: componentScores,
        cacheHitRate: avgCacheHitRate,
        costUsd,
        issueNumber,
      };

      await HealthScoreHistoryWriter.appendSnapshot(this.workspacePath, snapshot);
    } catch (error) {
      console.warn(`[Nightgauge] Failed to record health snapshot: ${error}`);
    }
  }

  /**
   * Get total cost from pipeline run history
   */
  getTotalCost(runs: PipelineRunSummary[]): number {
    return runs.reduce((sum, r) => sum + r.usage.costUsd, 0);
  }

  /**
   * Normalize weights so they sum to 1.0
   */
  private normalizeWeights(weights: HealthScoreWeights): HealthScoreWeights {
    const sum =
      weights.successRate + weights.costTrend + weights.failureRate + weights.cacheHitRate;

    if (sum === 0) return DEFAULT_HEALTH_WEIGHTS;

    return {
      successRate: weights.successRate / sum,
      costTrend: weights.costTrend / sum,
      failureRate: weights.failureRate / sum,
      cacheHitRate: weights.cacheHitRate / sum,
    };
  }
}

/**
 * Score cost trend using a one-sided Gaussian decay with a dead band.
 *
 * Stable or decreasing costs score 100 (healthy). Only sustained increases
 * beyond a 15% dead band reduce the score. The 15% band accounts for natural
 * cost variance from issue-size differences (S=$2-3, M=$5-7, L=$10-15).
 *
 * Curve: +25%→96, +35%→85, +50%→61, +65%→37, +85%→14
 */
export function computeCostTrendScore(percentChange: number): number {
  if (percentChange <= 15) return 100;
  const excess = percentChange - 15;
  return Math.max(0, Math.round(100 * Math.exp(-Math.pow(excess / 50, 2))));
}

/**
 * A run has valid cache data when its total token count is non-zero (#989).
 * Zero-token runs produce cacheHitRate 0 which would drag down averages.
 */
function hasValidCacheData(r: PipelineRunSummary): boolean {
  const u = r.usage;
  return (
    u.inputTokens + u.outputTokens + (u.cacheReadTokens ?? 0) + (u.cacheCreationTokens ?? 0) > 0
  );
}

/** Human-readable labels for each health dimension */
const DIMENSION_LABELS: Record<string, string> = {
  "token-economics": "Token Economics",
  "cost-health": "Cost Health",
  "stage-effectiveness": "Stage Effectiveness",
  "model-routing": "Model Routing",
  reliability: "Reliability",
  "learning-effectiveness": "Learning Effectiveness",
  "pipeline-velocity": "Pipeline Velocity",
  "skill-drift": "Skill Drift",
};

/**
 * Build DimensionSparkline[] from recent HealthTrendEntry records.
 * Only returns sparklines for dimensions with ≥2 data points.
 * Trend: compares avg of last 3 vs avg of prior 3 entries.
 *
 * @see Issue #1411 - Health trend persistence and dashboard sparklines
 */
function buildDimensionSparklines(
  entries: Array<{ dimensions: Partial<Record<string, number>> }>
): DimensionSparkline[] {
  const ALL_DIMS = [
    "token-economics",
    "cost-health",
    "stage-effectiveness",
    "model-routing",
    "reliability",
    "learning-effectiveness",
    "pipeline-velocity",
    "skill-drift",
  ];

  const result: DimensionSparkline[] = [];

  for (const dim of ALL_DIMS) {
    const scores: number[] = [];
    for (const entry of entries) {
      const score = entry.dimensions[dim];
      if (typeof score === "number") {
        scores.push(score);
      }
    }

    if (scores.length < 2) continue;

    // Compute trend: last 3 vs prior 3
    const recent = scores.slice(-3);
    const prior = scores.slice(-6, -3);

    let trend: "improving" | "stable" | "declining" = "stable";
    if (prior.length > 0) {
      const recentAvg = recent.reduce((a, b) => a + b, 0) / recent.length;
      const priorAvg = prior.reduce((a, b) => a + b, 0) / prior.length;
      const delta = recentAvg - priorAvg;
      if (delta > 2) {
        trend = "improving";
      } else if (delta < -2) {
        trend = "declining";
      }
    }

    result.push({
      dimension: dim,
      label: DIMENSION_LABELS[dim] ?? dim,
      data: scores,
      trend,
    });
  }

  return result;
}
