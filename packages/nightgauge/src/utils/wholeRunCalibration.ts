/**
 * Whole-run cohort cost calibration for the pre-flight budget gate (#3364).
 *
 * The pre-flight estimate used to publish the SUM of per-(stage, model) p75s.
 * p75s do not add: each stage's tail comes from a different run, so the sum
 * (~$26.47 for every S and M run on 2026-11-16) sat ~4x above the real
 * whole-run cost, and it was size-blind because the cells are keyed by
 * (stage, model). This module takes the p75 of WHOLE completed runs of the
 * same size instead, narrowed to the same routing when enough such runs exist.
 *
 * Its only import is the band authority (`TIER_BANDS`), so
 * `scripts/backtest-cost-estimate.ts` can load it under `tsx` against the
 * built SDK without the rest of the extension's dependency graph.
 */

import { TIER_BANDS } from "@nightgauge/sdk";

/** One completed pipeline run, priced as a whole. */
export interface CompletedRunCost {
  /** Total `tokens.estimated_cost_usd` of this one run. */
  totalCostUsd: number;
  /** `size:*` label at run time, or null when unlabelled. */
  sizeLabel: string | null;
  /** stage → model that served it; deterministic stages are absent. */
  stageModels: Record<string, string>;
  /** ISO timestamp the run record was written. */
  recordedAt: string;
}

/** Which whole-run cohort produced the figure; `none` means none qualified. */
export type WholeRunSource = "same-routing" | "same-size" | "none";

export interface WholeRunCalibration {
  /** p75 whole-run cost of the cohort, or null when no cohort qualifies. */
  costUsd: number | null;
  /** Runs behind `costUsd` (or the size cohort's size when `none`). */
  sampleCount: number;
  source: WholeRunSource;
}

/** Minimum cohort size for any whole-run figure. */
export const MIN_WHOLE_RUN_SAMPLES = 3;
/** Minimum same-routing subset before the cohort narrows to it. */
export const MIN_ROUTING_SAMPLES = 5;
/** Most recent runs kept in the cohort, so old routing/pricing ages out. */
export const WHOLE_RUN_WINDOW = 30;

/**
 * Reduce a model identifier to its family band, so the selector's band names
 * (`opus`) and the recorded concrete ids (`claude-opus-5-5`) compare equal.
 */
export function modelBand(model: string): string {
  const m = model.toLowerCase();
  for (const band of TIER_BANDS) {
    if (m.includes(band)) return band;
  }
  return m;
}

/** Stable `stage=band` signature over the stages that dispatched a model. */
export function routingSignature(stageModels: Record<string, string | null | undefined>): string {
  return Object.keys(stageModels)
    .filter((s) => typeof stageModels[s] === "string" && stageModels[s] !== "")
    .sort()
    .map((s) => `${s}=${modelBand(stageModels[s] as string)}`)
    .join(",");
}

/**
 * A run shares the planned routing when every stage it dispatched a model for
 * ran on the band the plan selects for that stage. Stages the run handled
 * deterministically carry no model and are not compared.
 */
function sameRouting(run: CompletedRunCost, planned: Record<string, string>): boolean {
  const stages = Object.keys(run.stageModels);
  if (stages.length === 0) return false;
  return stages.every(
    (s) => planned[s] !== undefined && modelBand(planned[s]) === modelBand(run.stageModels[s])
  );
}

/** Nearest-rank p75 over an ascending array (matches adaptiveBudgetLoader). */
function p75Sorted(sorted: number[]): number {
  const idx = Math.max(0, Math.ceil(0.75 * sorted.length) - 1);
  return sorted[idx];
}

/**
 * p75 whole-run cost of the most recent completed runs of `sizeLabel`,
 * narrowed to `plannedStageModels` routing when ≥ {@link MIN_ROUTING_SAMPLES}
 * such runs exist. Returns `none` below {@link MIN_WHOLE_RUN_SAMPLES}.
 */
export function computeWholeRunCalibration(
  runs: CompletedRunCost[],
  sizeLabel: string,
  plannedStageModels: Record<string, string>,
  opts: { window?: number; minSamples?: number; minRoutingSamples?: number } = {}
): WholeRunCalibration {
  const window = opts.window ?? WHOLE_RUN_WINDOW;
  const minSamples = opts.minSamples ?? MIN_WHOLE_RUN_SAMPLES;
  const minRouting = opts.minRoutingSamples ?? MIN_ROUTING_SAMPLES;

  const sameSize = runs.filter((r) => r.totalCostUsd > 0 && r.sizeLabel === sizeLabel);
  if (sameSize.length < minSamples) {
    return { costUsd: null, sampleCount: sameSize.length, source: "none" };
  }
  const routed = sameSize.filter((r) => sameRouting(r, plannedStageModels));
  const useRouting = routed.length >= minRouting;
  const cohort = (useRouting ? routed : sameSize)
    .slice()
    .sort((a, b) => b.recordedAt.localeCompare(a.recordedAt))
    .slice(0, window);
  const sorted = cohort.map((r) => r.totalCostUsd).sort((a, b) => a - b);
  return {
    costUsd: p75Sorted(sorted),
    sampleCount: cohort.length,
    source: useRouting ? "same-routing" : "same-size",
  };
}

/** An estimate counts as accurate when within this factor of the actual. */
export const BACKTEST_RATIO_BAND = 2;

export interface WholeRunBacktest {
  /** Runs that had a qualifying prior cohort. */
  paired: number;
  /** Share of paired runs whose estimate was within 2x of the actual. */
  withinTwoX: number;
  /** Median estimate / actual over paired runs (null when none paired). */
  medianRatio: number | null;
}

/**
 * Replay each completed run since `since` against its PRIOR runs only (no
 * look-ahead), using the run's own recorded routing as the planned routing.
 */
export function backtestWholeRunCalibration(
  runs: CompletedRunCost[],
  opts: { since?: string } = {}
): WholeRunBacktest {
  const ordered = runs
    .filter((r) => r.totalCostUsd > 0)
    .slice()
    .sort((a, b) => a.recordedAt.localeCompare(b.recordedAt));
  const ratios: number[] = [];
  for (let i = 0; i < ordered.length; i++) {
    const run = ordered[i];
    if (run.sizeLabel === null) continue;
    if (opts.since && run.recordedAt < opts.since) continue;
    const est = computeWholeRunCalibration(ordered.slice(0, i), run.sizeLabel, run.stageModels);
    if (est.costUsd === null) continue;
    ratios.push(est.costUsd / run.totalCostUsd);
  }
  if (ratios.length === 0) return { paired: 0, withinTwoX: 0, medianRatio: null };
  const within = ratios.filter(
    (r) => r >= 1 / BACKTEST_RATIO_BAND && r <= BACKTEST_RATIO_BAND
  ).length;
  const sorted = ratios.slice().sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  const median = sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
  return { paired: ratios.length, withinTwoX: within / ratios.length, medianRatio: median };
}
