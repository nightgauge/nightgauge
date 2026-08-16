/**
 * Eval → routing feedback (Issue #4175; re-keyed to the dispatch envelope by
 * the selection-query cutover, #581 / spike #568 §4.2–4.3).
 *
 * Consumes model-eval records (the JSONL the S7 CLI emits, or the S9 analytics
 * API) and produces, per job class and per performance mode, a recommended
 * dispatch ENVELOPE `(model_id, effort, thinking)` with confidence and a
 * cost/quality rationale. Aggregation keys on
 * `(job_class, model_id, effort, thinking)` — the axes #571 made honest —
 * with job class as the outer dimension; collapsing effort/thinking (the
 * pre-#581 keying) averaged cells the pipeline can now dispatch distinctly.
 *
 * Sparsity and confidence (spike §4.3):
 * - the registry interlock prunes combinations the registry declares illegal
 *   (an effort off the model's declared ladder; thinking off where the
 *   disable interlock forbids it) — models without a registry entry pass
 *   through, no fact is invented;
 * - a combination is ADVISABLE only at n ≥ `minSamples` (default 5, the
 *   `CostPerSuccessContext` floor — the old per-model minSamples=3 was too
 *   loose per combination);
 * - hierarchical backoff when every exact combination is sparse: exact
 *   `(model, effort, thinking)` → `(model, *, *)` aggregate → declared
 *   ladder (the advisor returns nothing and the axis query alone decides).
 *   Every recommendation and advice entry carries its backoff level;
 * - judge-reliability `low_confidence` rows are excluded from aggregation
 *   (ADR 011 §4); sparse combinations are emitted with `advisable: false`
 *   rather than omitted, so sparsity is visible instead of silent.
 *
 * Deliberately **advisory and opt-in**: consumed only when
 * `model_routing.use_eval_recommendations` is enabled (default false), via
 * the materialized advice file (routingAdvice.ts) both resolvers read — it
 * never silently overrides the axis query's declared-ladder routing.
 *
 * @see docs/decisions/011-model-eval-system.md
 */

import {
  BASELINE_PROMPT_VARIANT,
  MIN_HONEST_SCHEMA_VERSION,
  type EffortLevel,
  type JobClass,
  type ModelEvalRecord,
} from "./modelEvalSchemas.js";
import { getModelDescriptor, thinkingDisableConflict } from "./modelRegistry.js";

/**
 * Performance modes eval recommendations are computed for, mapping to the
 * pipeline's mode profiles:
 *   - efficiency : cheapest envelope meeting the quality floor
 *   - balanced   : best quality-per-dollar
 *   - maximum    : highest quality among non-frontier models
 *   - frontier   : highest quality, frontier tier allowed
 */
export const ROUTING_MODES = ["efficiency", "balanced", "maximum", "frontier"] as const;
export type RoutingMode = (typeof ROUTING_MODES)[number];

export type Confidence = "low" | "medium" | "high";

/** The binary thinking axis (spike #568 §3): eval `reasoning: none` → off, else on. */
export type ThinkingState = "on" | "off";

/**
 * Backoff level of a recommendation / advice entry (spike §4.3): `exact` is a
 * measured `(model, effort, thinking)` combination; `model` is the
 * `(model, *, *)` aggregate consulted when every exact combination is sparse.
 * The third level — declared ladder — is spelled as the ABSENCE of advice.
 */
export type AdviceBackoff = "exact" | "model";

/** Aggregated performance of one dispatch envelope on one job class. */
export interface EnvelopeStats {
  jobClass: JobClass;
  modelId: string;
  effort: EffortLevel;
  thinking: ThinkingState;
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
  /** The recommended envelope. effort/thinking absent at `model` backoff. */
  modelId: string;
  effort?: EffortLevel;
  thinking?: ThinkingState;
  backoff: AdviceBackoff;
  /** Whether the combination met the per-combination sample floor. */
  advisable: boolean;
  meanQuality: number;
  meanCostUsd: number;
  passRate: number;
  samples: number;
  confidence: Confidence;
  rationale: string;
}

export interface AdvisorOptions {
  /** Minimum composite quality a combination must reach to be eligible (default 70). */
  qualityFloor?: number;
  /**
   * Minimum samples per (model, effort, thinking) combination before it is
   * advisable (default 5 — spike §4.3's per-combination floor).
   */
  minSamples?: number;
  /** Confidence required before advise() overrides the base pick (default "medium"). */
  minConfidenceToApply?: Confidence;
}

const CONFIDENCE_RANK: Record<Confidence, number> = { low: 0, medium: 1, high: 2 };

/** Collapse the eval reasoning axis onto the binary thinking axis (spike §3). */
export function thinkingStateOf(record: ModelEvalRecord): ThinkingState {
  return record.cell.reasoning === "none" ? "off" : "on";
}

/**
 * A record the advisor may aggregate: an honest (v3+) row, from an executed
 * cell (skipped cells carry no measurement), under the baseline prompt
 * variant (experimental variant cells, #72, measure TEXT, not models), not
 * flagged low-confidence by the judge-reliability guard (ADR 011 §4).
 * Defensive on purpose — callers hand in JSONL rows that may predate the
 * current schema and skip the Zod parse.
 */
function isAggregatable(r: ModelEvalRecord): boolean {
  // `>=` (not a negated `<`) so a missing/malformed version (Number → NaN)
  // fails CLOSED: a row of unknown provenance is not an honest measurement.
  if (!(Number(r.schema_version) >= MIN_HONEST_SCHEMA_VERSION)) return false;
  if (r.verdict === "skipped") return false;
  if (r.score?.low_confidence === true) return false;
  return (r.cell.prompt_variant ?? BASELINE_PROMPT_VARIANT) === BASELINE_PROMPT_VARIANT;
}

/**
 * Registry-interlock prune (spike §4.3): drop rows whose envelope the
 * registry declares illegal — an effort off the model's declared
 * `supported_efforts` ladder (or any effort against a model that declares no
 * effort axis), and thinking `off` where the thinking-disable interlock
 * forbids the pairing. A model WITHOUT a registry entry passes through: the
 * query never invents a fact about a model the registry does not describe.
 */
function isRegistryLegal(r: ModelEvalRecord): boolean {
  const descriptor = getModelDescriptor(r.model_id);
  if (!descriptor) return true;
  const ladder = descriptor.supported_efforts as readonly string[];
  if (!ladder.includes(r.cell.effort)) return false;
  if (thinkingStateOf(r) === "off") {
    const { conflict } = thinkingDisableConflict(r.model_id, r.cell.effort);
    if (conflict) return false;
  }
  return true;
}

interface Accumulator {
  jobClass: JobClass;
  modelId: string;
  effort: EffortLevel;
  thinking: ThinkingState;
  n: number;
  passes: number;
  quality: number;
  cost: number;
}

/**
 * Builds envelope recommendations from eval records and advises the router.
 * Pure: all inputs are records; no clock, filesystem, or network.
 */
export class EvalRoutingAdvisor {
  private readonly stats: Map<JobClass, EnvelopeStats[]>;
  private readonly qualityFloor: number;
  private readonly minSamplesPerCombination: number;
  private readonly minConfidenceToApply: Confidence;

  constructor(records: ModelEvalRecord[], options: AdvisorOptions = {}) {
    this.qualityFloor = options.qualityFloor ?? 70;
    this.minSamplesPerCombination = options.minSamples ?? 5;
    this.minConfidenceToApply = options.minConfidenceToApply ?? "medium";
    // Aggregate only honest, executed, baseline-variant, judge-confident,
    // registry-legal rows (#571, #72, spike §4.3) — see isAggregatable and
    // isRegistryLegal for why each exclusion exists.
    this.stats = aggregate(records.filter((r) => isAggregatable(r) && isRegistryLegal(r)));
  }

  /** The per-combination sample floor in force. */
  get minSamples(): number {
    return this.minSamplesPerCombination;
  }

  /**
   * The quality floor efficiency/balanced picks must clear. Stamped into the
   * advice file (`quality_floor`) so consumption-side pickers — TS
   * `pickAdvice` and Go `AdviseBand` — apply the floor the file was built
   * with instead of re-hardcoding the default and silently diverging when a
   * materialization run customizes it.
   */
  get advisoryQualityFloor(): number {
    return this.qualityFloor;
  }

  /** All aggregated envelope stats for a job class — dashboard/API surface too. */
  statsFor(jobClass: JobClass): EnvelopeStats[] {
    return this.stats.get(jobClass) ?? [];
  }

  /** Every job class with at least one aggregated combination. */
  jobClasses(): JobClass[] {
    return [...this.stats.keys()];
  }

  /**
   * The recommended envelope for a job class under a mode, or undefined when
   * no data exists at all (the declared ladder rules — spike §4.3's final
   * backoff level).
   */
  recommend(jobClass: JobClass, mode: RoutingMode): Recommendation | undefined {
    const combos = this.stats.get(jobClass);
    if (!combos || combos.length === 0) return undefined;

    const floor = this.minSamplesPerCombination;

    // Level 1 — exact combinations with enough samples.
    const advisable = combos.filter((c) => c.samples >= floor);
    if (advisable.length > 0) {
      const winner = pickForMode(this.eligiblePool(advisable, mode), mode);
      return this.toRecommendation(jobClass, mode, winner, "exact", true);
    }

    // Level 2 — (model, *, *) aggregates whose pooled samples pass the floor.
    const modelAggregates = aggregateByModel(combos).filter((c) => c.samples >= floor);
    if (modelAggregates.length > 0) {
      const winner = pickForMode(this.eligiblePool(modelAggregates, mode), mode);
      const rec = this.toRecommendation(jobClass, mode, winner, "model", true);
      // The aggregate is not a dispatchable envelope: effort/thinking are
      // averaged-over, so they are absent rather than picked arbitrarily.
      delete rec.effort;
      delete rec.thinking;
      return rec;
    }

    // Sparse everywhere: surface the best exact combination with advisable
    // false and (necessarily) low confidence, so consumers SEE the sparsity
    // instead of a silent void. advise() will never apply it.
    const winner = pickForMode(this.eligiblePool(combos, mode), mode);
    return this.toRecommendation(jobClass, mode, winner, "exact", false);
  }

  /**
   * Advisory override: given the base model the selector chose, return the
   * eval-recommended envelope for (jobClass, mode) when it names a different
   * model AND confidence meets the threshold; otherwise return the base pick
   * unchanged. `source` records which path decided.
   */
  advise(
    baseModelId: string,
    jobClass: JobClass,
    mode: RoutingMode
  ): {
    modelId: string;
    effort?: EffortLevel;
    thinking?: ThinkingState;
    source: "eval-advisory" | "base";
    rationale: string;
  } {
    const rec = this.recommend(jobClass, mode);
    if (!rec || rec.modelId === baseModelId) {
      return {
        modelId: baseModelId,
        source: "base",
        rationale: rec ? "eval agrees with base pick" : "no eval data",
      };
    }
    if (
      !rec.advisable ||
      CONFIDENCE_RANK[rec.confidence] < CONFIDENCE_RANK[this.minConfidenceToApply]
    ) {
      return {
        modelId: baseModelId,
        source: "base",
        rationale: `eval confidence ${rec.confidence} below threshold`,
      };
    }
    return {
      modelId: rec.modelId,
      effort: rec.effort,
      thinking: rec.thinking,
      source: "eval-advisory",
      rationale: rec.rationale,
    };
  }

  /**
   * Every (job class, envelope) combination as a flat advice list — the
   * materialization surface for the routing-advice file (spike §4.2). Exact
   * combinations are always emitted, sparse ones with `advisable: false`;
   * `(model, *, *)` aggregates are added (backoff `model`) only when NO
   * exact combination in the WHOLE job class is advisable — a deliberately
   * conservative, class-global gate: once any exact evidence exists for the
   * class, pooled aggregates stay out of the file entirely, so a second
   * model's pooled evidence never competes with another model's measured
   * combination. (Spike §4.3's backoff hierarchy is per-combination;
   * narrowing this gate to per-model is deliberately deferred until sparse-
   * but-mixed classes show up in practice — less advice is the safe error.)
   * The recorded backoff level tells measured routing from declared routing.
   */
  adviceEntries(): Array<EnvelopeStats & { advisable: boolean; backoff: AdviceBackoff }> {
    const out: Array<EnvelopeStats & { advisable: boolean; backoff: AdviceBackoff }> = [];
    const floor = this.minSamplesPerCombination;
    for (const combos of this.stats.values()) {
      for (const c of combos) {
        out.push({ ...c, advisable: c.samples >= floor, backoff: "exact" });
      }
      const anyAdvisable = combos.some((c) => c.samples >= floor);
      if (!anyAdvisable) {
        for (const agg of aggregateByModel(combos)) {
          if (agg.samples >= floor) {
            out.push({ ...agg, advisable: true, backoff: "model" });
          }
        }
      }
    }
    return out;
  }

  private eligiblePool(pool: EnvelopeStats[], mode: RoutingMode): EnvelopeStats[] {
    // Frontier/maximum consider all; efficiency/balanced require the quality floor.
    const eligible =
      mode === "maximum" || mode === "frontier"
        ? pool
        : pool.filter((c) => c.meanQuality >= this.qualityFloor);
    return eligible.length > 0 ? eligible : pool;
  }

  private toRecommendation(
    jobClass: JobClass,
    mode: RoutingMode,
    winner: EnvelopeStats,
    backoff: AdviceBackoff,
    advisable: boolean
  ): Recommendation {
    return {
      jobClass,
      mode,
      modelId: winner.modelId,
      effort: winner.effort,
      thinking: winner.thinking,
      backoff,
      advisable,
      meanQuality: round1(winner.meanQuality),
      meanCostUsd: round4(winner.meanCostUsd),
      passRate: round2(winner.passRate),
      samples: winner.samples,
      confidence: confidenceFor(winner.samples, this.minSamplesPerCombination),
      rationale: rationaleFor(mode, winner),
    };
  }
}

// ---------------------------------------------------------------------------
// Aggregation + selection
// ---------------------------------------------------------------------------

function aggregate(records: ModelEvalRecord[]): Map<JobClass, EnvelopeStats[]> {
  // (jobClass|model|effort|thinking) → accumulator — the envelope re-key
  // (#581): the axes #571 made honest are aggregation keys, never averaged
  // away.
  const acc = new Map<string, Accumulator>();
  for (const r of records) {
    const thinking = thinkingStateOf(r);
    const key = `${r.job_class}|${r.model_id}|${r.cell.effort}|${thinking}`;
    const a = acc.get(key) ?? {
      jobClass: r.job_class,
      modelId: r.model_id,
      effort: r.cell.effort,
      thinking,
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

  const byJob = new Map<JobClass, EnvelopeStats[]>();
  for (const a of acc.values()) {
    const list = byJob.get(a.jobClass) ?? [];
    list.push(toStats(a));
    byJob.set(a.jobClass, list);
  }
  return byJob;
}

function toStats(a: Accumulator): EnvelopeStats {
  const meanQuality = a.quality / a.n;
  const meanCostUsd = a.cost / a.n;
  return {
    jobClass: a.jobClass,
    modelId: a.modelId,
    effort: a.effort,
    thinking: a.thinking,
    samples: a.n,
    passRate: a.passes / a.n,
    meanQuality,
    meanCostUsd,
    qualityPerDollar: meanCostUsd > 0 ? meanQuality / meanCostUsd : meanQuality,
  };
}

/**
 * The `(model, *, *)` backoff aggregate (spike §4.3): pool every combination
 * of one model, sample-weighted. The effort/thinking carried on the result
 * are those of the largest contributing combination — REPORTING context only;
 * recommend() strips them before returning a `model`-backoff recommendation.
 */
function aggregateByModel(combos: EnvelopeStats[]): EnvelopeStats[] {
  const byModel = new Map<string, EnvelopeStats[]>();
  for (const c of combos) {
    const list = byModel.get(c.modelId) ?? [];
    list.push(c);
    byModel.set(c.modelId, list);
  }
  const out: EnvelopeStats[] = [];
  for (const list of byModel.values()) {
    const samples = list.reduce((n, c) => n + c.samples, 0);
    const weighted = (f: (c: EnvelopeStats) => number) =>
      list.reduce((sum, c) => sum + f(c) * c.samples, 0) / samples;
    const largest = [...list].sort((a, b) => b.samples - a.samples)[0];
    const meanQuality = weighted((c) => c.meanQuality);
    const meanCostUsd = weighted((c) => c.meanCostUsd);
    out.push({
      jobClass: largest.jobClass,
      modelId: largest.modelId,
      effort: largest.effort,
      thinking: largest.thinking,
      samples,
      passRate: weighted((c) => c.passRate),
      meanQuality,
      meanCostUsd,
      qualityPerDollar: meanCostUsd > 0 ? meanQuality / meanCostUsd : meanQuality,
    });
  }
  return out;
}

export function pickForMode(pool: EnvelopeStats[], mode: RoutingMode): EnvelopeStats {
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

function rationaleFor(mode: RoutingMode, s: EnvelopeStats): string {
  const q = s.meanQuality.toFixed(1);
  const c = s.meanCostUsd.toFixed(4);
  const envelope = `${s.modelId}@${s.effort}/thinking-${s.thinking}`;
  switch (mode) {
    case "efficiency":
      return `cheapest above quality floor: ${envelope} at ${q} quality, $${c}/task`;
    case "balanced":
      return `best quality-per-dollar: ${envelope} at ${q} quality, $${c}/task`;
    case "maximum":
    case "frontier":
      return `highest quality: ${envelope} at ${q}, $${c}/task`;
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
