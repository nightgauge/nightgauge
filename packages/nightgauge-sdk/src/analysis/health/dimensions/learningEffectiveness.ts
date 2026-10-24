/**
 * Learning Effectiveness Dimension Analyzer (re-based, #1900)
 *
 * Asks one question: are the pipeline's predictions about a change right?
 * Routing and sizing are the two predictions the pipeline makes before a run
 * and measures after it, so their accuracy is the learning signal.
 *
 * Inputs — a change to any of these must change this analyzer, or it starves:
 *
 * | Input | Path | Fields |
 * | --- | --- | --- |
 * | Outcome corpus | `<clone>/pipeline/history/outcomes.jsonl` | `completedAt`, `predictedModel`, `actualModel`, `predictedSize`, `actualSize` |
 * | Calibration | `<clone>/pipeline/calibration.json` | `updated_at` |
 *
 * Rules:
 * - A pair counts only when BOTH halves are non-empty. An empty half is an
 *   unknown (the Go writer records "" for an unregistered model id or a run
 *   that never measured), excluded from the denominator (#340) rather than
 *   booked as a miss.
 * - A row with no prediction at all is a coverage gap (#304), reported as
 *   `predictionCoverage` and a finding, never silently dropped.
 * - Each accuracy (routing, size) contributes only when it has at least
 *   `minimumSampleSizes.basic` pairs. When neither does, the dimension reports
 *   `hasEnoughData: false` with the reason; no weaker proxy is substituted.
 * - Calibration that lags the newest outcome by more than
 *   {@link CALIBRATION_STALE_DAYS} days (or is absent) costs 10 points: the
 *   loop is recording outcomes it is not learning from.
 *
 * @see Issue #1900 - re-base learning-effectiveness
 */

import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
  LearningOutcomeEntry,
} from "../types.js";
import { getHealthStatus } from "../types.js";
import { clamp, buildPeriodComparison } from "../statistics.js";

/** Calibration older than the newest outcome by more than this is stale. */
export const CALIBRATION_STALE_DAYS = 14;

const DAY_MS = 86_400_000;

interface PairStats {
  pairs: number;
  hits: number;
}

function pairStats(
  rows: LearningOutcomeEntry[],
  predicted: (r: LearningOutcomeEntry) => string,
  actual: (r: LearningOutcomeEntry) => string
): PairStats {
  let pairs = 0;
  let hits = 0;
  for (const r of rows) {
    const p = predicted(r);
    const a = actual(r);
    if (!p || !a) continue;
    pairs++;
    if (p === a) hits++;
  }
  return { pairs, hits };
}

/** Rows carrying at least one complete predicted/actual pair. */
function pairedRows(rows: LearningOutcomeEntry[]): number {
  return rows.filter(
    (r) => (r.predictedModel && r.actualModel) || (r.predictedSize && r.actualSize)
  ).length;
}

/** Mean of the accuracies that met the minimum; null when none did. */
function accuracyScore(stats: PairStats[], minimum: number): number | null {
  const usable = stats.filter((s) => s.pairs >= minimum);
  if (usable.length === 0) return null;
  return usable.reduce((sum, s) => sum + s.hits / s.pairs, 0) / usable.length;
}

export function analyzeLearningEffectiveness(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const minimum = config.minimumSampleSizes.basic;
  const signals = dataset.learningSignals;
  const rows = signals?.outcomes ?? [];

  const routing = pairStats(
    rows,
    (r) => r.predictedModel,
    (r) => r.actualModel
  );
  const size = pairStats(
    rows,
    (r) => r.predictedSize,
    (r) => r.actualSize
  );
  const sampleSize = pairedRows(rows);
  const predicted = rows.filter((r) => r.predictedModel || r.predictedSize).length;
  const predictionCoverage = rows.length > 0 ? predicted / rows.length : 0;

  const metrics: Record<string, number> = {
    outcomeRows: rows.length,
    routingPairs: routing.pairs,
    sizePairs: size.pairs,
    predictionCoverage,
    sampleSize,
  };

  const accuracy = accuracyScore([routing, size], minimum);
  if (!signals || accuracy === null) {
    return {
      dimension: "learning-effectiveness",
      score: 50,
      status: getHealthStatus(50),
      findings: [],
      metrics,
      hasEnoughData: false,
      sampleSize,
      insufficientDataReason: !signals
        ? "no outcome corpus (pipeline/history/outcomes.jsonl) was supplied"
        : `outcomes.jsonl has ${routing.pairs} routing and ${size.pairs} size predicted/actual ` +
          `pair(s) across ${rows.length} row(s); either needs ${minimum}`,
    };
  }

  if (routing.pairs > 0) metrics.routingAccuracy = routing.hits / routing.pairs;
  if (size.pairs > 0) metrics.sizeAccuracy = size.hits / size.pairs;

  const findings: Finding[] = [];
  let score = accuracy * 100;

  // ── Calibration freshness ─────────────────────────────────────────
  const newestOutcome = Math.max(
    ...rows.map((r) => Date.parse(r.completedAt)).filter((t) => !Number.isNaN(t))
  );
  const calibratedAt = signals.calibrationUpdatedAt
    ? Date.parse(signals.calibrationUpdatedAt)
    : Number.NaN;
  const lagDays = Number.isNaN(calibratedAt)
    ? null
    : Math.max(0, Math.round((newestOutcome - calibratedAt) / DAY_MS));
  if (lagDays !== null) metrics.calibrationLagDays = lagDays;
  if (lagDays === null || lagDays > CALIBRATION_STALE_DAYS) {
    score -= 10;
    findings.push({
      id: "le-calibration-stale",
      dimension: "learning-effectiveness",
      severity: "medium",
      title: lagDays === null ? "No calibration recorded" : "Calibration lags the outcome corpus",
      description:
        lagDays === null
          ? "outcomes.jsonl has scored runs but pipeline/calibration.json is absent or has no updated_at."
          : `pipeline/calibration.json was last updated ${lagDays} day(s) before the newest outcome.`,
      impact:
        "Outcomes are being recorded that the calibration never learns from, so routing and sizing cannot improve.",
      recommendation:
        "Check that calibration runs after each completed pipeline and that it can read outcomes.jsonl.",
      evidence: { calibrationUpdatedAt: signals.calibrationUpdatedAt, lagDays },
      confidence: "high",
    });
  }

  // ── Prediction coverage (#304) ─────────────────────────────────────
  if (rows.length >= minimum && predictionCoverage < 0.5) {
    findings.push({
      id: "le-prediction-coverage",
      dimension: "learning-effectiveness",
      severity: "medium",
      title: "Low prediction coverage",
      description: `Only ${predicted} of ${rows.length} outcome row(s) carry a routing or size prediction; the accuracy above describes that slice only.`,
      impact: "Runs without a prediction cannot teach the router anything.",
      recommendation:
        "Find which dispatch paths record an empty predictedModel/predictedSize (#304 warnings) and record the prediction.",
      evidence: { predicted, outcomeRows: rows.length, predictionCoverage },
      confidence: "high",
    });
  }

  // ── Accuracy findings ─────────────────────────────────────────────
  for (const [name, stats] of [
    ["Routing", routing],
    ["Size", size],
  ] as const) {
    if (stats.pairs >= minimum && stats.hits / stats.pairs < 0.6) {
      findings.push({
        id: `le-${name.toLowerCase()}-accuracy`,
        dimension: "learning-effectiveness",
        severity: "medium",
        title: `${name} prediction accuracy is low`,
        description: `${stats.hits} of ${stats.pairs} ${name.toLowerCase()} prediction(s) matched what the run produced.`,
        impact: "Mispredicted runs are dispatched to the wrong model or planned at the wrong size.",
        recommendation: `Review the ${name.toLowerCase()} predictor's inputs against recent misses.`,
        evidence: { pairs: stats.pairs, hits: stats.hits },
        confidence: stats.pairs >= config.minimumSampleSizes.significance ? "high" : "medium",
      });
    }
  }

  score = clamp(Math.round(score), 0, 100);

  let periodComparison = undefined;
  const baselineRows = baseline?.learningSignals?.outcomes;
  if (baselineRows) {
    const baselineAccuracy = accuracyScore(
      [
        pairStats(
          baselineRows,
          (r) => r.predictedModel,
          (r) => r.actualModel
        ),
        pairStats(
          baselineRows,
          (r) => r.predictedSize,
          (r) => r.actualSize
        ),
      ],
      minimum
    );
    if (baselineAccuracy !== null) {
      periodComparison = buildPeriodComparison(
        accuracy,
        baselineAccuracy,
        sampleSize,
        /* lowerIsBetter */ false,
        config.confidenceThreshold
      );
    }
  }

  // A falling accuracy against the baseline is the "worsening" signal the
  // cross-referencer correlates with degrading dimensions (rule 3).
  if (periodComparison?.direction === "degrading") {
    findings.push({
      id: "le-accuracy-worsening",
      dimension: "learning-effectiveness",
      severity: periodComparison.isSignificant ? "high" : "medium",
      title: "Prediction accuracy worsening against the baseline period",
      description: `Prediction accuracy fell from ${Math.round(periodComparison.baselineValue * 100)}% to ${Math.round(periodComparison.currentValue * 100)}%.`,
      impact: "The pipeline is getting worse at predicting its own runs, not better.",
      recommendation:
        "Compare the recent misses with the baseline period's hits for a changed input.",
      evidence: { ...periodComparison },
      confidence: periodComparison.isSignificant ? "high" : "medium",
    });
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
