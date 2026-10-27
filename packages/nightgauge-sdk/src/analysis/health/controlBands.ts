/**
 * Tiered control bands over one stable metric (#1286).
 *
 * Per-run cloud cost is banded upward against a rolling mean and standard
 * deviation. The tiers live in the version-controlled `control-bands.json`;
 * each excursion escalates record → diagnose → finding. A band never fires
 * unless its baseline passes the stability gate, and the whole band is dormant
 * while the file says `enabled: false`.
 *
 * Every function here is pure: the definition is a parameter so tests inject
 * their own, and the shipped file is the default.
 *
 * @see docs/HEALTH_MONITORING.md § Control bands
 */

import { z } from "zod";
import type { ExecutionHistoryRecord } from "../types.js";
import type { Finding } from "./types.js";
import { mean, standardDeviation } from "./statistics.js";
import shippedDefinition from "./control-bands.json";

// ── Definition ────────────────────────────────────────────────────

const TIER_NAMES = ["innermost", "middle", "outermost"] as const;
const TIER_ACTIONS = ["record", "diagnose", "finding"] as const;

export const ControlBandDefinitionSchema = z
  .object({
    schema_version: z.literal(1),
    metric: z.literal("cost_per_run_usd"),
    enabled: z.boolean(),
    rolling_window: z.number().int().min(2),
    baseline: z.object({
      min_samples: z.number().int().min(2),
      max_cv: z.number().positive(),
      max_drift_sigma: z.number().positive(),
    }),
    tiers: z
      .array(
        z.object({
          name: z.enum(TIER_NAMES),
          min_sigma: z.number().positive(),
          action: z.enum(TIER_ACTIONS),
        })
      )
      .length(3),
  })
  .refine(
    (d) => d.tiers.every((t, i) => t.name === TIER_NAMES[i] && t.action === TIER_ACTIONS[i]),
    {
      message: "tiers must be innermost/record, middle/diagnose, outermost/finding in order",
    }
  )
  .refine((d) => d.tiers.every((t, i) => i === 0 || t.min_sigma > d.tiers[i - 1].min_sigma), {
    message: "tier min_sigma must strictly increase outward",
  })
  .refine((d) => d.baseline.min_samples <= d.rolling_window, {
    message: "baseline.min_samples cannot exceed rolling_window",
  });

export type ControlBandDefinition = z.infer<typeof ControlBandDefinitionSchema>;
export type ControlBandTierName = (typeof TIER_NAMES)[number];
export type ControlBandTier = ControlBandTierName | "none";

/** The shipped tier file, validated at load. A malformed file throws here. */
export const CONTROL_BAND_DEFINITION: ControlBandDefinition =
  ControlBandDefinitionSchema.parse(shippedDefinition);

// ── Stability gate ────────────────────────────────────────────────

export interface BaselineStability {
  stable: boolean;
  reason: string;
  n: number;
  mean: number;
  sd: number;
  cv: number;
  /** Second-half mean minus first-half mean, in σ of the whole window. */
  driftSigma: number;
}

/** Whether `window` is a baseline a band may be drawn against. */
export function assessBaselineStability(
  window: number[],
  def: ControlBandDefinition = CONTROL_BAND_DEFINITION
): BaselineStability {
  const n = window.length;
  const avg = mean(window);
  const sd = standardDeviation(window);
  const cv = avg > 0 ? sd / avg : 0;
  const half = Math.floor(n / 2);
  const driftSigma =
    sd > 0 && half > 0 ? (mean(window.slice(n - half)) - mean(window.slice(0, half))) / sd : 0;
  const stats = { n, mean: avg, sd, cv, driftSigma };

  if (n < def.baseline.min_samples) {
    return { stable: false, reason: `${n} samples < ${def.baseline.min_samples}`, ...stats };
  }
  if (sd === 0) return { stable: false, reason: "zero variance", ...stats };
  if (cv > def.baseline.max_cv) {
    return { stable: false, reason: `cv ${cv.toFixed(2)} > ${def.baseline.max_cv}`, ...stats };
  }
  if (Math.abs(driftSigma) > def.baseline.max_drift_sigma) {
    return {
      stable: false,
      reason: `drift ${driftSigma.toFixed(2)}σ exceeds ${def.baseline.max_drift_sigma}σ`,
      ...stats,
    };
  }
  return { stable: true, reason: "stable", ...stats };
}

// ── Evaluator ─────────────────────────────────────────────────────

export interface ControlBandEvaluation {
  metric: ControlBandDefinition["metric"];
  tier: ControlBandTier;
  action: (typeof TIER_ACTIONS)[number] | null;
  /** Why the tier is `none`, or the tier name when it fired. */
  reason: string;
  value: number | null;
  z: number | null;
  /** Upper bound of each band: mean + min_sigma·σ. Empty when no baseline. */
  bounds: Partial<Record<ControlBandTierName, number>>;
  baseline: BaselineStability | null;
}

const ORDINAL: Record<ControlBandTier, number> = { none: 0, innermost: 1, middle: 2, outermost: 3 };

/** The tier as an ordinal 0–3, for numeric metrics. */
export function tierOrdinal(tier: ControlBandTier): number {
  return ORDINAL[tier];
}

/**
 * Evaluate the LAST value of `chronological` against the `rolling_window`
 * values before it. Upward excursions only.
 */
export function evaluateControlBand(
  chronological: number[],
  def: ControlBandDefinition = CONTROL_BAND_DEFINITION
): ControlBandEvaluation {
  const none = (reason: string, rest: Partial<ControlBandEvaluation> = {}) => ({
    metric: def.metric,
    tier: "none" as const,
    action: null,
    reason,
    value: chronological.length > 0 ? chronological[chronological.length - 1] : null,
    z: null,
    bounds: {},
    baseline: null,
    ...rest,
  });

  if (!def.enabled) return none("disabled");
  if (chronological.length === 0) return none("no observations");

  const value = chronological[chronological.length - 1];
  const window = chronological.slice(0, -1).slice(-def.rolling_window);
  const baseline = assessBaselineStability(window, def);
  if (!baseline.stable) return none(`unstable baseline: ${baseline.reason}`, { baseline });

  const z = (value - baseline.mean) / baseline.sd;
  const bounds = Object.fromEntries(
    def.tiers.map((t) => [t.name, baseline.mean + t.min_sigma * baseline.sd])
  ) as ControlBandEvaluation["bounds"];
  const fired = [...def.tiers].reverse().find((t) => z >= t.min_sigma);
  if (!fired) return none("within band", { z, bounds, baseline });

  return {
    metric: def.metric,
    tier: fired.name,
    action: fired.action,
    reason: fired.name,
    value,
    z,
    bounds,
    baseline,
  };
}

// ── Middle tier: read-only diagnosis ──────────────────────────────

export interface StageCostDiagnosis {
  stage: string;
  costUsd: number;
  baselineMeanUsd: number;
  deltaUsd: number;
  retries: number;
  models: string[];
}

export interface CostExcursionDiagnosis {
  /** Stages of the excursion run, largest cost delta first. */
  stages: StageCostDiagnosis[];
  totalRetries: number;
}

/**
 * Explain an excursion from the run's own records: per-stage cost against the
 * baseline runs' per-stage mean, retries and models. Reads only — never
 * mutates its inputs and has no side effects.
 */
export function diagnoseCostExcursion(
  runRecords: readonly ExecutionHistoryRecord[],
  baselineRecords: readonly ExecutionHistoryRecord[]
): CostExcursionDiagnosis {
  const baselineRuns = new Map<string, Map<number, number>>();
  for (const r of baselineRecords) {
    const perRun = baselineRuns.get(r.stage) ?? new Map<number, number>();
    perRun.set(r.issueNumber, (perRun.get(r.issueNumber) ?? 0) + r.costUsd);
    baselineRuns.set(r.stage, perRun);
  }

  const byStage = new Map<string, { cost: number; retries: number; models: Set<string> }>();
  for (const r of runRecords) {
    const s = byStage.get(r.stage) ?? { cost: 0, retries: 0, models: new Set<string>() };
    s.cost += r.costUsd;
    s.retries += r.retries;
    if (r.model) s.models.add(r.model);
    byStage.set(r.stage, s);
  }

  const stages = Array.from(byStage, ([stage, s]) => {
    const baselineMeanUsd = mean(Array.from(baselineRuns.get(stage)?.values() ?? []));
    return {
      stage,
      costUsd: s.cost,
      baselineMeanUsd,
      deltaUsd: s.cost - baselineMeanUsd,
      retries: s.retries,
      models: [...s.models].sort(),
    };
  }).sort((a, b) => b.deltaUsd - a.deltaUsd);

  return { stages, totalRetries: stages.reduce((sum, s) => sum + s.retries, 0) };
}

/** The band outcome a cost-health result carries: the evaluation, plus the
 * diagnosis from the middle tier outward. */
export interface ControlBandResult {
  evaluation: ControlBandEvaluation;
  diagnosis?: CostExcursionDiagnosis;
}

// ── Outermost tier: finding ───────────────────────────────────────

export function buildControlBandFinding(
  evaluation: ControlBandEvaluation,
  diagnosis: CostExcursionDiagnosis | undefined,
  id: string
): Finding {
  const sigma = evaluation.z?.toFixed(2) ?? "?";
  const top = diagnosis?.stages[0];
  return {
    id,
    dimension: "cost-health",
    severity: "high",
    title: "Run Cost Beyond Outermost Control Band",
    description: `The latest run cost $${evaluation.value?.toFixed(4)}, ${sigma}σ above the rolling baseline mean of $${evaluation.baseline?.mean.toFixed(4)}.`,
    impact: "A cost excursion this far outside a stable baseline is unlikely to be noise.",
    recommendation: top
      ? `Start with stage "${top.stage}" (+$${top.deltaUsd.toFixed(4)} over its baseline, ${top.retries} retries).`
      : "Inspect the run's stages for retries, oversized context, or model mis-routing.",
    evidence: {
      z: evaluation.z,
      value: evaluation.value,
      bounds: evaluation.bounds,
      baseline: evaluation.baseline,
      ...(diagnosis ? { diagnosis } : {}),
    },
    confidence: "high",
  };
}
