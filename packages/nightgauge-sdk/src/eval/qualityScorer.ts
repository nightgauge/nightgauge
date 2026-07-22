/**
 * Model-eval grading & scoring engine (Issue #4173).
 *
 * Turns a cell's raw outcome into a normalized composite `EvalScore` (0–100)
 * from three components, weighted per job class:
 *   1. correctness  — deterministic gate results (build/test/lint/typecheck)
 *   2. automated    — penalties vs a baseline (attempts-to-green, latency, cost)
 *   3. judge        — an LLM judge scores subjective dimensions against the rubric
 *
 * The scoring math is a pure function; the LLM judge is injected (mock in tests).
 * The **judge reliability guard** (per docs/decisions/011) runs the judge N times
 * and flags dimensions whose score variance exceeds a threshold as
 * `low_confidence`, so unstable subjective scores are never trusted silently.
 *
 * A deterministic-only mode (no judge) still yields a correctness-based score so
 * CI can grade without LLM cost.
 *
 * @see docs/decisions/011-model-eval-system.md
 */

import type {
  EvalScore,
  EvalRubric,
  GateResult,
  JobClass,
  QualityDimensionName,
  QualityDimensionScore,
} from "./modelEvalSchemas.js";

/** Component weights for the composite; must sum to 1 per job class. */
export interface ScoreWeights {
  correctness: number;
  automated: number;
  judge: number;
}

/**
 * Per-job-class weighting. UI/UX weight the judge (subjective quality) higher;
 * bugfix/backend/testing weight deterministic correctness higher; refactor
 * balances correctness with code-quality judgment. Values sum to 1.
 */
export const JOB_CLASS_WEIGHTS: Record<JobClass, ScoreWeights> = {
  "ui-creation": { correctness: 0.35, automated: 0.15, judge: 0.5 },
  "ux-styling": { correctness: 0.25, automated: 0.15, judge: 0.6 },
  "backend-logic": { correctness: 0.5, automated: 0.2, judge: 0.3 },
  testing: { correctness: 0.5, automated: 0.15, judge: 0.35 },
  bugfix: { correctness: 0.6, automated: 0.2, judge: 0.2 },
  refactor: { correctness: 0.45, automated: 0.15, judge: 0.4 },
  docs: { correctness: 0.25, automated: 0.15, judge: 0.6 },
};

/** Baseline a cell's efficiency is penalized against (per task/stage). */
export interface AutomatedBaseline {
  /** Expected attempts to green (1 = first try). */
  attempts: number;
  /** Expected wall-clock latency (ms). */
  latencyMs: number;
  /** Expected cost (USD). */
  costUsd: number;
}

/** A cell's measured efficiency metrics (from the runner). */
export interface AutomatedMetrics {
  attemptsToGreen: number;
  latencyMs: number;
  costUsd: number;
}

/** One judge-scored dimension (before aggregation). */
export interface JudgeDimensionScore {
  dimension: QualityDimensionName;
  /** 0–100. */
  score: number;
  rationale?: string;
}

/** A single judge verdict over the rubric dimensions. */
export interface EvalJudgeVerdict {
  dimensions: JudgeDimensionScore[];
}

/**
 * The subjective judge. Scores a cell's produced work against the task rubric.
 * Injected so the scorer stays pure and testable; the real binding is an LLM
 * call reusing the workflow EvalJudgeVerdict shape.
 */
export interface EvalJudge {
  judge(rubric: EvalRubric): Promise<EvalJudgeVerdict>;
}

export interface ReliabilityGuardOptions {
  /** How many times to sample the judge (default 3). */
  samples?: number;
  /** Std-dev (points) above which a dimension is flagged low-confidence (default 10). */
  varianceThreshold?: number;
}

// ---------------------------------------------------------------------------
// Pure components
// ---------------------------------------------------------------------------

/** Correctness (0–100): fraction of deterministic gates that passed. */
export function computeCorrectness(
  gates: GateResult[],
  verdict: "pass" | "fail" | "error"
): number {
  if (gates.length === 0) return verdict === "pass" ? 100 : 0;
  return (gates.filter((g) => g.passed).length / gates.length) * 100;
}

/**
 * Automated efficiency score (0–100): starts at 100 and applies penalties for
 * exceeding the baseline on attempts, latency, and cost. Beating the baseline
 * neither adds nor removes points (capped at 100).
 */
export function computeAutomatedScore(
  metrics: AutomatedMetrics,
  baseline: AutomatedBaseline
): number {
  // Each extra attempt beyond the first baseline attempt costs 20 points.
  const attemptPenalty = Math.max(0, metrics.attemptsToGreen - baseline.attempts) * 20;
  // Latency/cost penalties scale with the overage ratio (up to 30 points each).
  const latencyPenalty = ratioPenalty(metrics.latencyMs, baseline.latencyMs, 30);
  const costPenalty = ratioPenalty(metrics.costUsd, baseline.costUsd, 30);
  return clamp0to100(100 - attemptPenalty - latencyPenalty - costPenalty);
}

/** Penalty in [0, max] proportional to how far `actual` exceeds `baseline`. */
function ratioPenalty(actual: number, baseline: number, max: number): number {
  if (baseline <= 0 || actual <= baseline) return 0;
  const overage = actual / baseline - 1; // 0 at baseline, 1 at 2× baseline
  return Math.min(max, overage * max);
}

/**
 * Aggregate judge dimension scores into a single 0–100 using the rubric weights,
 * and produce the per-dimension breakdown carrying those weights.
 */
export function aggregateJudge(
  verdict: EvalJudgeVerdict,
  rubric: EvalRubric,
  lowConfidence?: Set<QualityDimensionName>
): { score: number; dimensions: QualityDimensionScore[] } {
  const weightOf = new Map(rubric.criteria.map((c) => [c.dimension, c.weight]));
  let weighted = 0;
  let totalWeight = 0;
  const dimensions: QualityDimensionScore[] = [];
  for (const d of verdict.dimensions) {
    const weight = weightOf.get(d.dimension) ?? 0;
    weighted += d.score * weight;
    totalWeight += weight;
    dimensions.push({
      dimension: d.dimension,
      score: d.score,
      weight,
      rationale: d.rationale,
      low_confidence: lowConfidence?.has(d.dimension) || undefined,
    });
  }
  const score = totalWeight > 0 ? weighted / totalWeight : 0;
  return { score, dimensions };
}

// ---------------------------------------------------------------------------
// Judge reliability guard
// ---------------------------------------------------------------------------

/**
 * Run the judge `samples` times and aggregate per dimension by mean, flagging any
 * dimension whose score std-dev exceeds the threshold as low-confidence. Returns
 * a single mean verdict plus the set of low-confidence dimensions.
 */
export async function runJudgeWithReliabilityGuard(
  judge: EvalJudge,
  rubric: EvalRubric,
  options: ReliabilityGuardOptions = {}
): Promise<{ verdict: EvalJudgeVerdict; lowConfidence: Set<QualityDimensionName> }> {
  const samples = Math.max(1, options.samples ?? 3);
  const threshold = options.varianceThreshold ?? 10;

  const byDimension = new Map<QualityDimensionName, { scores: number[]; rationale?: string }>();
  for (let i = 0; i < samples; i++) {
    const v = await judge.judge(rubric);
    for (const d of v.dimensions) {
      const entry = byDimension.get(d.dimension) ?? { scores: [] };
      entry.scores.push(d.score);
      entry.rationale ??= d.rationale;
      byDimension.set(d.dimension, entry);
    }
  }

  const dimensions: JudgeDimensionScore[] = [];
  const lowConfidence = new Set<QualityDimensionName>();
  for (const [dimension, { scores, rationale }] of byDimension) {
    const mean = scores.reduce((s, n) => s + n, 0) / scores.length;
    if (stddev(scores) > threshold) lowConfidence.add(dimension);
    dimensions.push({ dimension, score: mean, rationale });
  }
  return { verdict: { dimensions }, lowConfidence };
}

function stddev(xs: number[]): number {
  if (xs.length < 2) return 0;
  const mean = xs.reduce((s, n) => s + n, 0) / xs.length;
  const variance = xs.reduce((s, n) => s + (n - mean) ** 2, 0) / xs.length;
  return Math.sqrt(variance);
}

// ---------------------------------------------------------------------------
// Composite
// ---------------------------------------------------------------------------

export interface ScoreCellInput {
  jobClass: JobClass;
  verdict: "pass" | "fail" | "error";
  gates: GateResult[];
  metrics: AutomatedMetrics;
  baseline: AutomatedBaseline;
  /** Judge output + low-confidence set (omit for deterministic-only scoring). */
  judge?: {
    verdict: EvalJudgeVerdict;
    rubric: EvalRubric;
    lowConfidence?: Set<QualityDimensionName>;
  };
  /** Weight override; defaults to the job-class profile. */
  weights?: ScoreWeights;
}

/**
 * Compute a cell's composite `EvalScore`. With a judge, blends all three
 * components per the job-class weights; without one, blends correctness +
 * automated (renormalized) and marks `judge_used: false`.
 */
export function scoreCell(input: ScoreCellInput): EvalScore {
  const weights = input.weights ?? JOB_CLASS_WEIGHTS[input.jobClass];
  const correctness = computeCorrectness(input.gates, input.verdict);
  const automated = computeAutomatedScore(input.metrics, input.baseline);

  if (!input.judge) {
    const denom = weights.correctness + weights.automated || 1;
    const composite = (correctness * weights.correctness + automated * weights.automated) / denom;
    return {
      composite: round2(composite),
      correctness: round2(correctness),
      dimensions: [],
      judge_used: false,
    };
  }

  const { score: judgeScore, dimensions } = aggregateJudge(
    input.judge.verdict,
    input.judge.rubric,
    input.judge.lowConfidence
  );
  const composite =
    correctness * weights.correctness + automated * weights.automated + judgeScore * weights.judge;
  const lowConfidence = (input.judge.lowConfidence?.size ?? 0) > 0;
  return {
    composite: round2(composite),
    correctness: round2(correctness),
    dimensions,
    judge_used: true,
    low_confidence: lowConfidence || undefined,
  };
}

function clamp0to100(n: number): number {
  return Math.max(0, Math.min(100, n));
}
function round2(n: number): number {
  return Math.round(n * 100) / 100;
}
