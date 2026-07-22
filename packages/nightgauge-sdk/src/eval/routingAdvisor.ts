/**
 * Eval → routing feedback (Issue #4175).
 *
 * Consumes model-eval records (the JSONL the S7 CLI emits, or the S9 analytics
 * API) and produces, per job class and per performance mode, a recommended model
 * with confidence and a cost/quality rationale. This is the loop that turns eval
 * results into routing decisions.
 *
 * Deliberately **advisory and opt-in**: the advisor is a self-contained input the
 * selector consults only when `model_routing.use_eval_recommendations` is enabled
 * — it never silently overrides `AutoModelSelector`'s existing complexity /
 * cost-per-success routing. `advise()` returns the eval-recommended model for a
 * (jobClass, mode) when confidence is sufficient, else leaves the base pick.
 *
 * @see docs/decisions/011-model-eval-system.md
 */

import {
  BASELINE_PROMPT_VARIANT,
  type JobClass,
  type ModelEvalRecord,
} from "./modelEvalSchemas.js";

/**
 * Performance modes eval recommendations are computed for, mapping to the
 * pipeline's mode profiles:
 *   - efficiency : cheapest model meeting the quality floor
 *   - balanced   : best quality-per-dollar
 *   - maximum    : highest quality among non-frontier models
 *   - frontier   : highest quality, frontier tier allowed
 */
export const ROUTING_MODES = ["efficiency", "balanced", "maximum", "frontier"] as const;
export type RoutingMode = (typeof ROUTING_MODES)[number];

export type Confidence = "low" | "medium" | "high";

/** Aggregated performance of one model on one job class. */
export interface ModelJobStats {
  modelId: string;
  jobClass: JobClass;
  samples: number;
  passRate: number;
  meanQuality: number;
  meanCostUsd: number;
  /** quality per dollar (composite / cost), guarded against zero cost. */
  qualityPerDollar: number;
}

export interface Recommendation {
  jobClass: JobClass;
  mode: RoutingMode;
  modelId: string;
  meanQuality: number;
  meanCostUsd: number;
  passRate: number;
  samples: number;
  confidence: Confidence;
  rationale: string;
}

export interface AdvisorOptions {
  /** Minimum composite quality a model must reach to be eligible (default 70). */
  qualityFloor?: number;
  /** Minimum samples before a recommendation is anything but low confidence (default 3). */
  minSamples?: number;
  /** Confidence required before advise() overrides the base pick (default "medium"). */
  minConfidenceToApply?: Confidence;
}

const CONFIDENCE_RANK: Record<Confidence, number> = { low: 0, medium: 1, high: 2 };

/**
 * Builds recommendations from eval records and advises the router. Pure: all
 * inputs are records; no clock, filesystem, or network.
 */
export class EvalRoutingAdvisor {
  private readonly stats: Map<JobClass, ModelJobStats[]>;
  private readonly qualityFloor: number;
  private readonly minSamples: number;
  private readonly minConfidenceToApply: Confidence;

  constructor(records: ModelEvalRecord[], options: AdvisorOptions = {}) {
    this.qualityFloor = options.qualityFloor ?? 70;
    this.minSamples = options.minSamples ?? 3;
    this.minConfidenceToApply = options.minConfidenceToApply ?? "medium";
    // Routing advice must reflect the prompts production actually runs — the
    // baseline variant. Experimental prompt-variant cells (#72) measure TEXT,
    // not models, and would skew per-model stats if aggregated here. The ??
    // covers pre-v2 records handed in without a schema parse (the parse fills
    // the baseline default) so old data never silently mutes the advisor.
    this.stats = aggregate(
      records.filter(
        (r) => (r.cell.prompt_variant ?? BASELINE_PROMPT_VARIANT) === BASELINE_PROMPT_VARIANT
      )
    );
  }

  /** All aggregated (jobClass, model) stats — useful for the dashboard/API too. */
  statsFor(jobClass: JobClass): ModelJobStats[] {
    return this.stats.get(jobClass) ?? [];
  }

  /** The recommended model for a job class under a mode, or undefined if no data. */
  recommend(jobClass: JobClass, mode: RoutingMode): Recommendation | undefined {
    const candidates = this.stats.get(jobClass);
    if (!candidates || candidates.length === 0) return undefined;

    // Frontier/maximum consider all; efficiency/balanced require the quality floor.
    const eligible =
      mode === "maximum" || mode === "frontier"
        ? candidates
        : candidates.filter((c) => c.meanQuality >= this.qualityFloor);
    const pool = eligible.length > 0 ? eligible : candidates;

    const winner = pickForMode(pool, mode);
    return {
      jobClass,
      mode,
      modelId: winner.modelId,
      meanQuality: round1(winner.meanQuality),
      meanCostUsd: round4(winner.meanCostUsd),
      passRate: round2(winner.passRate),
      samples: winner.samples,
      confidence: confidenceFor(winner.samples, this.minSamples),
      rationale: rationaleFor(mode, winner),
    };
  }

  /**
   * Advisory override: given the base model the selector chose, return the
   * eval-recommended model for (jobClass, mode) when it differs AND confidence
   * meets the threshold; otherwise return the base pick unchanged. `source`
   * records which path decided.
   */
  advise(
    baseModelId: string,
    jobClass: JobClass,
    mode: RoutingMode
  ): { modelId: string; source: "eval-advisory" | "base"; rationale: string } {
    const rec = this.recommend(jobClass, mode);
    if (!rec || rec.modelId === baseModelId) {
      return {
        modelId: baseModelId,
        source: "base",
        rationale: rec ? "eval agrees with base pick" : "no eval data",
      };
    }
    if (CONFIDENCE_RANK[rec.confidence] < CONFIDENCE_RANK[this.minConfidenceToApply]) {
      return {
        modelId: baseModelId,
        source: "base",
        rationale: `eval confidence ${rec.confidence} below threshold`,
      };
    }
    return { modelId: rec.modelId, source: "eval-advisory", rationale: rec.rationale };
  }
}

// ---------------------------------------------------------------------------
// Aggregation + selection
// ---------------------------------------------------------------------------

function aggregate(records: ModelEvalRecord[]): Map<JobClass, ModelJobStats[]> {
  // (jobClass|model) → accumulator
  const acc = new Map<
    string,
    {
      jobClass: JobClass;
      modelId: string;
      n: number;
      passes: number;
      quality: number;
      cost: number;
    }
  >();
  for (const r of records) {
    const key = `${r.job_class}|${r.model_id}`;
    const a = acc.get(key) ?? {
      jobClass: r.job_class,
      modelId: r.model_id,
      n: 0,
      passes: 0,
      quality: 0,
      cost: 0,
    };
    a.n += 1;
    if (r.verdict === "pass") a.passes += 1;
    a.quality += r.score?.composite ?? 0;
    a.cost += r.cost_usd;
    acc.set(key, a);
  }

  const byJob = new Map<JobClass, ModelJobStats[]>();
  for (const a of acc.values()) {
    const meanQuality = a.quality / a.n;
    const meanCostUsd = a.cost / a.n;
    const stats: ModelJobStats = {
      modelId: a.modelId,
      jobClass: a.jobClass,
      samples: a.n,
      passRate: a.passes / a.n,
      meanQuality,
      meanCostUsd,
      qualityPerDollar: meanCostUsd > 0 ? meanQuality / meanCostUsd : meanQuality,
    };
    const list = byJob.get(a.jobClass) ?? [];
    list.push(stats);
    byJob.set(a.jobClass, list);
  }
  return byJob;
}

function pickForMode(pool: ModelJobStats[], mode: RoutingMode): ModelJobStats {
  switch (mode) {
    case "efficiency":
      // cheapest (ties → higher quality)
      return [...pool].sort(
        (a, b) => a.meanCostUsd - b.meanCostUsd || b.meanQuality - a.meanQuality
      )[0];
    case "balanced":
      return [...pool].sort((a, b) => b.qualityPerDollar - a.qualityPerDollar)[0];
    case "maximum":
    case "frontier":
      // highest quality (ties → cheaper)
      return [...pool].sort(
        (a, b) => b.meanQuality - a.meanQuality || a.meanCostUsd - b.meanCostUsd
      )[0];
  }
}

function confidenceFor(samples: number, minSamples: number): Confidence {
  if (samples < minSamples) return "low";
  if (samples < minSamples * 3) return "medium";
  return "high";
}

function rationaleFor(mode: RoutingMode, s: ModelJobStats): string {
  const q = s.meanQuality.toFixed(1);
  const c = s.meanCostUsd.toFixed(4);
  switch (mode) {
    case "efficiency":
      return `cheapest above quality floor: ${q} quality at $${c}/task`;
    case "balanced":
      return `best quality-per-dollar: ${q} quality at $${c}/task`;
    case "maximum":
    case "frontier":
      return `highest quality: ${q} at $${c}/task`;
  }
}

function round1(n: number): number {
  return Math.round(n * 10) / 10;
}
function round2(n: number): number {
  return Math.round(n * 100) / 100;
}
function round4(n: number): number {
  return Math.round(n * 1e4) / 1e4;
}
