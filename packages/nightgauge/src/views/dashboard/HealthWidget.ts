/**
 * HealthWidget - Health widget data aggregation service
 *
 * The headline score is the latest entry of the one health series (#1898):
 * the checkout's `health/trends.jsonl`, appended once per completed run by
 * `recordHealthForRun` with the SDK's `HealthAnalysisEngine`. The widget never
 * computes a score of its own, so the number shown is the number persisted.
 * No entry, or an entry whose every dimension was starved, reads as N/A —
 * never 0, never "critical".
 *
 * Sparklines, alerts and recommendations come from DashboardState and SDK
 * analysis; all SDK calls degrade gracefully to empty arrays.
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */

import type { DashboardState, PipelineRunSummary } from "./DashboardState";
import type { DependabotPRData } from "../../services/DependabotPRService";
import type {
  HealthWidgetData,
  HealthSummary,
  TrendSparkline,
  ActiveAlert,
  Recommendation,
  PredictionAccuracy,
  TrendChartDay,
  TrendAnalysis,
  TrendRange,
  DimensionSparkline,
} from "./HealthWidgetTypes";
import { DEFAULT_TREND_RANGE } from "./HealthWidgetTypes";
import {
  aggregateHealthByDay,
  aggregateHealthByHour,
  analyzeHealthTrend,
  readHealthSeries,
} from "../../utils/healthSeries";
import { isUsableWorkspaceRoot } from "../../utils/cloneLayout";
import type { HealthTrendEntry } from "@nightgauge/sdk";

/**
 * HealthWidgetService aggregates data from DashboardState, SDK analysis
 * modules, and work-time feedback into a single HealthWidgetData object
 * for rendering by HealthWidgetHtml.
 */
export class HealthWidgetService {
  constructor(
    private readonly dashboardState: DashboardState,
    private readonly workspacePath: string | undefined
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
        // No runs is no data — not a score of 0 and not "critical" (#1898).
        summary: summarizeHealthEntry(null),
        sparklines: [],
        alerts: [],
        recommendations: [],
        predictionAccuracy: null,
        lastUpdated: new Date().toISOString(),
        isEmpty: true,
      };
    }

    // A series that cannot be read is no data, not a blank widget.
    const series = this.workspacePath
      ? await readHealthSeries(this.workspacePath).catch(() => [])
      : [];
    const summary = summarizeHealthEntry(series.at(-1) ?? null);

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

        const inRange = series.filter((e) => new Date(e.timestamp) >= rangeStart);
        trendChart =
          trendRange === "24h" ? aggregateHealthByHour(inRange) : aggregateHealthByDay(inRange);

        trendAnalysis = analyzeHealthTrend(trendChart, comparisonBuckets);
      } catch {
        // Non-critical — degrade gracefully
      }
    }

    // Per-dimension sparklines from the same series (Issue #1411)
    const recentEntries = series.slice(-20);
    const dimensionSparklines: DimensionSparkline[] | undefined =
      recentEntries.length > 0 ? buildDimensionSparklines(recentEntries) : undefined;

    return {
      summary,
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
      const { AutoModelSelector, StageModelCalibrationService } = await import("@nightgauge/sdk");
      const { toModelEnvelope } = await import("../../utils/modeProfiles");
      const selector = new AutoModelSelector();
      const calibrationPath = isUsableWorkspaceRoot(this.workspacePath)
        ? StageModelCalibrationService.getDefaultPath(this.workspacePath)
        : null;
      const stageModelCalibration = calibrationPath
        ? await StageModelCalibrationService.load(calibrationPath)
        : null;
      const history = this.dashboardState.getHistory().slice(0, 10);

      const { providerForAdapter } = await import("@nightgauge/sdk");
      const { resolveStageAdapter } = await import("../../utils/resolvers/adapterResolver");
      const widgetProvider = providerForAdapter(
        resolveStageAdapter("feature-dev", this.workspacePath).adapter
      );

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

        // Issue #142 (was #3216): HealthWidget shows aggregate cost-anomaly
        // history, so we explicitly use the `elevated` routing envelope (the
        // natural baseline) rather than the operator's currently-active mode.
        // This matches today's behavior and keeps the alert calibration
        // stable as operators toggle between modes. Per-(stage, model)
        // calibration is still consulted per stage.
        const estimate = selector.estimatePipelineCost(
          { labels: [`size:${sizeLabel}`], title: run.title },
          {
            skipStages: run.routing?.skippedStages ?? [],
            stageModelCalibration,
            envelope: toModelEnvelope("elevated"),
            // PipelineRunSummary carries no per-run adapter, so this prices
            // against the configured provider. That is right for the common
            // single-provider workspace and wrong for a run that used another
            // one — which the `unpriced` guard below at least keeps from being
            // reported as a confident overrun (#1213).
            provider: widgetProvider,
          }
        );
        const expectedCost = estimate.totalEstimatedCost;

        // An unpriced estimate is a floor, not a budget: comparing an actual
        // against it would report an overrun for every unpriceable provider.
        if (
          !estimate.unpriced &&
          expectedCost > 0 &&
          actualCost > expectedCost * thresholdRatio &&
          actualCost > minUsd
        ) {
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
   * Get total cost from pipeline run history
   */
  getTotalCost(runs: PipelineRunSummary[]): number {
    return runs.reduce((sum, r) => sum + r.usage.costUsd, 0);
  }
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
 * The headline for one series entry. `null` (no entry yet) and an entry
 * recorded as null both read as no-data; every dimension is listed, an
 * excluded one with the reason it did not contribute (#1900).
 */
export function summarizeHealthEntry(entry: HealthTrendEntry | null): HealthSummary {
  const dimensions = ALL_DIMS.map((dimension) => {
    const score = entry?.dimensions[dimension as keyof HealthTrendEntry["dimensions"]];
    const reason =
      entry?.excluded_dimensions[dimension as keyof HealthTrendEntry["excluded_dimensions"]];
    return {
      dimension,
      label: DIMENSION_LABELS[dimension] ?? dimension,
      score: typeof score === "number" ? score : null,
      excludedReason: typeof score === "number" ? undefined : (reason ?? "no health entry yet"),
    };
  });
  return {
    score: entry?.overall_score ?? null,
    status: entry?.status ?? "no-data",
    dimensions,
    contributing: dimensions.filter((d) => d.score !== null).length,
    recordedAt: entry?.timestamp,
    issueNumber: entry?.issue_number,
  };
}

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
