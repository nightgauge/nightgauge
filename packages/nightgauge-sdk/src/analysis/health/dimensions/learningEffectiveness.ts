/**
 * Learning Effectiveness Dimension Analyzer
 *
 * Evaluates whether the pipeline's learning systems (calibration, tuning,
 * experiments) are producing measurable improvements.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
  RecommendationHistoryEntry,
} from "../types.js";
import { getHealthStatus } from "../types.js";
import { computeTrend, mean, clamp, hasEnoughData, buildPeriodComparison } from "../statistics.js";

// ── Internal helpers ────────────────────────────────────────────────

/**
 * Resolve an ISO timestamp string to a numeric epoch value for sorting.
 * Invalid timestamps fall back to 0 so they sort first rather than throwing.
 */
function toEpoch(ts: string): number {
  const ms = Date.parse(ts);
  return Number.isNaN(ms) ? 0 : ms;
}

/**
 * Determine whether health scores improved after a given tuning action.
 *
 * Strategy: collect health scores within the `windowMs` window *before* the
 * tuning entry and the same window *after* it, then compare their means.
 * Returns null when there are insufficient data points on either side.
 */
function scoreImprovedAfterTuning(
  tuningTimestamp: string,
  sortedScores: Array<{ epoch: number; score: number }>,
  windowMs: number = 7 * 24 * 60 * 60 * 1000 // 7-day window
): boolean | null {
  const tuningEpoch = toEpoch(tuningTimestamp);

  const before = sortedScores
    .filter((s) => s.epoch >= tuningEpoch - windowMs && s.epoch < tuningEpoch)
    .map((s) => s.score);

  const after = sortedScores
    .filter((s) => s.epoch > tuningEpoch && s.epoch <= tuningEpoch + windowMs)
    .map((s) => s.score);

  if (before.length === 0 || after.length === 0) return null;

  return mean(after) > mean(before);
}

/**
 * Compute the span of time covered by the dataset in weeks.
 * Returns 0 when there are fewer than 2 data points.
 */
function spanInWeeks(epochs: number[]): number {
  if (epochs.length < 2) return 0;
  const min = Math.min(...epochs);
  const max = Math.max(...epochs);
  return (max - min) / (7 * 24 * 60 * 60 * 1000);
}

// ── Main Analyzer ───────────────────────────────────────────────────

export function analyzeLearningEffectiveness(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const { selfTuningLog, healthScores, experimentResults, healthReports } = dataset;

  // ── Data availability check ──────────────────────────────────────

  // Self-improvement needs at least a minimal signal to be assessed.
  const dataPresent = healthScores.length >= 2 || selfTuningLog.length >= 1;

  const sampleSize = healthScores.length + selfTuningLog.length;

  if (!dataPresent) {
    return {
      dimension: "learning-effectiveness",
      score: 50,
      status: getHealthStatus(50),
      findings: [],
      metrics: {
        healthScoreCount: healthScores.length,
        tuningActionCount: selfTuningLog.length,
        experimentCount: experimentResults.length,
        healthReportCount: healthReports.length,
        sampleSize,
      },
      hasEnoughData: false,
      sampleSize,
    };
  }

  const findings: Finding[] = [];
  let findingIndex = 0;

  // ── Pre-compute sorted health scores ─────────────────────────────

  const sortedScoreEntries = [...healthScores]
    .map((e) => ({ epoch: toEpoch(e.timestamp), score: e.score }))
    .sort((a, b) => a.epoch - b.epoch);

  const scoreTimeSeries = sortedScoreEntries.map((e) => e.score);

  // ── 1. Health score trajectory ───────────────────────────────────
  // computeTrend treats an increasing slope as 'degrading'; for health scores
  // a positive slope is good, so we invert the direction semantics here.

  const { slope: scoreSlope, direction: rawTrendDirection } = computeTrend(scoreTimeSeries);

  // Invert: scores going up (positive slope → 'degrading' from computeTrend)
  // means the health is actually improving and vice-versa.
  const scoreTrendImproving = rawTrendDirection === "degrading"; // slope > 0 → scores rising
  const scoreTrendWorsening = rawTrendDirection === "improving"; // slope < 0 → scores falling

  // ── 2. Tuning action effectiveness ───────────────────────────────

  let tuningImprovedCount = 0;
  let tuningAssessableCount = 0;

  for (const entry of selfTuningLog) {
    const result = scoreImprovedAfterTuning(entry.timestamp, sortedScoreEntries);
    if (result !== null) {
      tuningAssessableCount++;
      if (result) tuningImprovedCount++;
    }
  }

  const tuningEffective =
    tuningAssessableCount > 0 && tuningImprovedCount / tuningAssessableCount >= 0.5;

  // ── 3. A/B experiment activity and outcomes ───────────────────────

  const experimentNames = new Set(experimentResults.map((e) => e.experimentName));
  const experimentsRunning = experimentNames.size > 0;

  // For each experiment, compare treatment vs control success rates
  let positiveExperimentCount = 0;

  for (const name of experimentNames) {
    const entries = experimentResults.filter((e) => e.experimentName === name);
    const treatment = entries.filter((e) => e.group === "treatment");
    const control = entries.filter((e) => e.group === "control");

    if (treatment.length === 0 || control.length === 0) continue;

    const treatmentSuccessRate = treatment.filter((e) => e.success).length / treatment.length;
    const controlSuccessRate = control.filter((e) => e.success).length / control.length;

    if (treatmentSuccessRate > controlSuccessRate) {
      positiveExperimentCount++;
    }
  }

  const experimentsPositive =
    experimentNames.size > 0 && positiveExperimentCount / experimentNames.size >= 0.5;

  // ── 4. Recommendation follow-through (decreasing counts) ─────────

  const sortedReports = [...healthReports].sort(
    (a, b) => toEpoch(a.createdAt) - toEpoch(b.createdAt)
  );

  const recommendationCounts = sortedReports.map((r) => r.recommendationCount);

  // A decreasing recommendation count implies findings are being acted upon.
  // computeTrend: slope < 0 → direction = 'improving' (values falling).
  const { direction: recTrend } = computeTrend(recommendationCounts);
  const recommendationsDecreasing = recTrend === "improving"; // slope < 0

  // ── 5. Tuning frequency ───────────────────────────────────────────

  const tuningEpochs = selfTuningLog.map((e) => toEpoch(e.timestamp));
  const tuningWeeksSpan = spanInWeeks(tuningEpochs);
  const tuningActionCount = selfTuningLog.length;

  // Require at least 1 action per week on average over the observed span.
  // When there is only 1 action and no span, treat it as insufficient frequency.
  const regularTuning =
    tuningActionCount >= 1 &&
    (tuningWeeksSpan === 0 ? false : tuningActionCount / tuningWeeksSpan >= 1);

  // ── Scoring ───────────────────────────────────────────────────────

  let score = 50; // neutral baseline — learning system must prove itself

  if (scoreTrendImproving) score += 15;
  if (scoreTrendWorsening) score -= 10;

  if (tuningActionCount > 0 && tuningEffective) score += 10;

  if (experimentsRunning && experimentsPositive) score += 10;

  if (recommendationsDecreasing) score += 10;

  if (regularTuning) score += 5;

  // ── 6. Recommendation history integration ────────────────────────

  let followThroughRate = 0;
  let effectivenessRate = 0;
  let recurringCount = 0;

  if (dataset.recommendationHistory && dataset.recommendationHistory.length > 0) {
    const recHistory: RecommendationHistoryEntry[] = dataset.recommendationHistory;

    // Follow-through rate: how many entries with linked issues are closed
    const entriesWithIssues = recHistory.filter((e) => e.issue_number !== undefined);
    const closedIssues = entriesWithIssues.filter((e) => e.issue_state === "closed");
    followThroughRate =
      entriesWithIssues.length > 0 ? closedIssues.length / entriesWithIssues.length : 0;

    // Effectiveness rate: among closed entries with before/after metrics, how many improved
    const closedWithMetrics = closedIssues.filter(
      (e) => e.metric_before !== undefined && e.metric_after !== undefined
    );
    const improvedEntries = closedWithMetrics.filter(
      (e) => (e.metric_after as number) > (e.metric_before as number)
    );
    effectivenessRate =
      closedWithMetrics.length > 0 ? improvedEntries.length / closedWithMetrics.length : 0;

    // Recurring findings: group by normalized title, count groups with repeated entries
    // where at least one is closed but the finding keeps reappearing
    const titleGroups = new Map<string, RecommendationHistoryEntry[]>();
    for (const entry of recHistory) {
      const normalized = entry.title
        .toLowerCase()
        .trim()
        .replace(/^\[health\]\s*/i, "");
      const group = titleGroups.get(normalized) ?? [];
      group.push(entry);
      titleGroups.set(normalized, group);
    }
    recurringCount = 0;
    for (const group of titleGroups.values()) {
      if (
        group.length >= 2 &&
        group.some((e) => e.issue_state === "closed") &&
        group.some((e) => e.issue_state !== "closed")
      ) {
        recurringCount++;
      }
    }

    // Score adjustments
    if (followThroughRate > 0.7) score += 5;
    if (effectivenessRate > 0.5) score += 10;
    if (recurringCount > 2) score -= 5;

    // Findings
    if (effectivenessRate < 0.3 && closedWithMetrics.length >= 2) {
      findings.push({
        id: `si-${++findingIndex}`,
        dimension: "learning-effectiveness",
        severity: "medium",
        title: "Low recommendation effectiveness",
        description: `Only ${(effectivenessRate * 100).toFixed(0)}% of closed recommendations with measurable outcomes showed improvement (${improvedEntries.length} of ${closedWithMetrics.length}).`,
        impact:
          "Recommendations that do not produce measurable improvements waste engineering effort and erode confidence in the health analysis system.",
        recommendation:
          "Review the quality and specificity of generated recommendations. Ensure recommendations include concrete, actionable steps and that metric baselines are captured accurately before and after changes.",
        evidence: {
          closedWithMetricsCount: closedWithMetrics.length,
          improvedCount: improvedEntries.length,
          effectivenessRate,
        },
        confidence: "high",
      });
    }

    if (followThroughRate < 0.4 && entriesWithIssues.length >= 3) {
      findings.push({
        id: `si-${++findingIndex}`,
        dimension: "learning-effectiveness",
        severity: "medium",
        title: "Low recommendation follow-through rate",
        description: `Only ${(followThroughRate * 100).toFixed(0)}% of recommendations with linked issues have been resolved (${closedIssues.length} of ${entriesWithIssues.length}).`,
        impact:
          "A low follow-through rate means identified health issues are not being addressed, allowing problems to compound over time.",
        recommendation:
          "Prioritise closing open recommendation issues. Consider adding recommendation tracking to sprint planning to ensure health findings receive dedicated attention.",
        evidence: {
          entriesWithIssuesCount: entriesWithIssues.length,
          closedIssuesCount: closedIssues.length,
          followThroughRate,
        },
        confidence: "high",
      });
    }

    if (recurringCount > 2) {
      findings.push({
        id: `si-${++findingIndex}`,
        dimension: "learning-effectiveness",
        severity: "medium",
        title: "Recurring findings not addressed at root cause",
        description: `${recurringCount} finding categories have been closed and reopened, suggesting surface-level fixes rather than root-cause resolution.`,
        impact:
          "Recurring findings indicate systemic issues that are not being properly resolved, leading to repeated remediation cycles and ongoing instability.",
        recommendation:
          "Conduct a root-cause analysis for the recurring finding categories. Look for shared underlying factors and address the system design or process gaps driving recurrence.",
        evidence: {
          recurringCount,
          totalHistoryEntries: recHistory.length,
        },
        confidence: "medium",
      });
    }
  }

  // ── 7. V4 workflow-orchestration calibration (Issue #3915) ────────
  // Fold-derived signal from the canonical schemaVersion-4 WorkflowEvent tree:
  // a high adversarial judge-rejection rate means agents keep claiming "done"
  // on work the judges reject — a learning-loop signal that the fan-out is
  // producing low-quality output rather than improving.

  const workflow = dataset.workflowCalibration;
  let workflowJudgeRejectionRate: number | null = null;
  let workflowFanoutEfficiency: number | null = null;
  if (workflow && workflow.runCount > 0) {
    workflowJudgeRejectionRate = workflow.meanJudgeRejectionRate;
    workflowFanoutEfficiency = workflow.meanFanoutEfficiency;

    // A judge-rejection rate above 40% across the observed runs is a strong
    // negative learning signal — the system is not converging on accepted work.
    if (workflowJudgeRejectionRate !== null && workflowJudgeRejectionRate > 0.4) {
      score -= 10;
      findings.push({
        id: `si-${++findingIndex}`,
        dimension: "learning-effectiveness",
        severity: workflowJudgeRejectionRate > 0.6 ? "high" : "medium",
        title: "High adversarial judge-rejection rate in workflow fan-out",
        description: `Adversarial judges rejected ${(workflowJudgeRejectionRate * 100).toFixed(0)}% of claims across ${workflow.runCount} workflow run(s).`,
        impact:
          "A high judge-rejection rate means fanned-out agents repeatedly claim completion on work the judges reject, indicating the orchestration is not learning to produce acceptable output and is burning budget on rework.",
        recommendation:
          "Review the agent prompts and the judge criteria for the affected phases. A persistently high rejection rate suggests either over-strict judges or under-specified agent tasks — calibrate one or the other.",
        evidence: {
          judgeRejectionRate: workflowJudgeRejectionRate,
          totalJudges: workflow.totalJudges,
          runCount: workflow.runCount,
        },
        confidence: workflow.runCount >= 3 ? "high" : "medium",
      });
    }

    // Low fan-out efficiency (many agents failing) wastes spawn budget.
    if (workflowFanoutEfficiency !== null && workflowFanoutEfficiency < 0.6) {
      score -= 5;
      findings.push({
        id: `si-${++findingIndex}`,
        dimension: "learning-effectiveness",
        severity: "medium",
        title: "Low workflow fan-out efficiency",
        description: `Only ${(workflowFanoutEfficiency * 100).toFixed(0)}% of fanned-out agents succeeded across ${workflow.runCount} workflow run(s) (${workflow.totalAgents} agents).`,
        impact:
          "A low fan-out efficiency means a large fraction of spawned agents fail, wasting concurrency budget and slowing the run without contributing accepted output.",
        recommendation:
          "Investigate the failing agents' terminal kinds — budget-exceeded or timeout failures point at ceilings set too low, while error failures point at the agent task itself.",
        evidence: {
          fanoutEfficiency: workflowFanoutEfficiency,
          totalAgents: workflow.totalAgents,
          runCount: workflow.runCount,
        },
        confidence: "medium",
      });
    }
  }

  score = clamp(score, 0, 100);

  // ── Findings ──────────────────────────────────────────────────────

  // No self-tuning activity at all
  if (tuningActionCount === 0) {
    findings.push({
      id: `si-${++findingIndex}`,
      dimension: "learning-effectiveness",
      severity: "medium",
      title: "No self-tuning activity recorded",
      description:
        "The self-tuning log is empty. The pipeline has not made any autonomous configuration adjustments.",
      impact:
        "Without active tuning the pipeline cannot adapt to changing workload patterns, leading to suboptimal performance over time.",
      recommendation:
        "Ensure the self-tuning mechanism is enabled and that tuning triggers (e.g. cost thresholds, error rate spikes) are correctly configured.",
      evidence: {
        tuningActionCount,
        healthScoreCount: healthScores.length,
      },
      confidence: "high",
    });
  }

  // Worsening health trajectory
  if (scoreTrendWorsening && scoreTimeSeries.length >= 2) {
    findings.push({
      id: `si-${++findingIndex}`,
      dimension: "learning-effectiveness",
      severity: Math.abs(scoreSlope) > 1 ? "high" : "medium",
      title: "Health score trajectory is declining",
      description: `Overall health scores are trending downward (slope: ${scoreSlope.toFixed(3)} per period) across ${scoreTimeSeries.length} recorded data points.`,
      impact:
        "A worsening health trajectory indicates the learning system is not compensating for degradation, risking pipeline reliability.",
      recommendation:
        "Review recent self-tuning entries to determine whether adjustments are counterproductive. Examine stage-level findings for root causes driving the decline.",
      evidence: {
        scoreSlope,
        scoreCount: scoreTimeSeries.length,
        latestScore:
          scoreTimeSeries.length > 0 ? scoreTimeSeries[scoreTimeSeries.length - 1] : null,
        earliestScore: scoreTimeSeries.length > 0 ? scoreTimeSeries[0] : null,
      },
      confidence: hasEnoughData(scoreTimeSeries.length, config.minimumSampleSizes.trend)
        ? "high"
        : "medium",
    });
  }

  // No experiments running
  if (!experimentsRunning) {
    findings.push({
      id: `si-${findingIndex + 1}`,
      dimension: "learning-effectiveness",
      severity: "low",
      title: "No A/B experiments recorded",
      description:
        "The experiment log contains no entries. The pipeline is not validating configuration changes through controlled experiments.",
      impact:
        "Without experiments, optimisation changes cannot be validated before full rollout, increasing the risk of regressions.",
      recommendation:
        "Introduce A/B experiments for significant configuration changes (model routing thresholds, prompt templates, retry budgets) to measure impact before committing.",
      evidence: {
        experimentCount: experimentResults.length,
        tuningActionCount,
      },
      confidence: "high",
    });
  }

  // ── Metrics ───────────────────────────────────────────────────────

  const avgHealthScore = mean(scoreTimeSeries);

  const metrics: Record<string, number> = {
    avgHealthScore,
    scoreSlope,
    tuningActionCount,
    tuningEffectiveCount: tuningImprovedCount,
    tuningAssessableCount,
    experimentsCount: experimentNames.size,
    positiveExperimentCount,
    recommendationCountSlope: computeTrend(recommendationCounts).slope,
    healthReportCount: sortedReports.length,
    regularTuningActionsPerWeek: tuningWeeksSpan > 0 ? tuningActionCount / tuningWeeksSpan : 0,
    sampleSize,
    recommendationCount: dataset.recommendationHistory?.length ?? 0,
    recommendationFollowThroughRate: followThroughRate,
    recommendationEffectivenessRate: effectivenessRate,
    recurringFindingCount: recurringCount,
    workflowRunCount: workflow?.runCount ?? 0,
    workflowJudgeRejectionRate: workflowJudgeRejectionRate ?? -1,
    workflowFanoutEfficiency: workflowFanoutEfficiency ?? -1,
    workflowNativeVsFanoutCostDeltaUsd: workflow?.nativeVsFanoutCostDeltaUsd ?? 0,
  };

  // ── Period comparison (baseline) ──────────────────────────────────

  let periodComparison = undefined;
  if (baseline !== undefined && baseline.healthScores.length > 0 && healthScores.length > 0) {
    const baselineAvgScore = mean(baseline.healthScores.map((e) => e.score));
    periodComparison = buildPeriodComparison(
      avgHealthScore,
      baselineAvgScore,
      sampleSize,
      /* lowerIsBetter */ false,
      config.confidenceThreshold
    );
  }

  return {
    dimension: "learning-effectiveness",
    score,
    status: getHealthStatus(score),
    findings,
    metrics,
    hasEnoughData: true,
    sampleSize,
    periodComparison,
  };
}
