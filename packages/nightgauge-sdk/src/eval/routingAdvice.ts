/**
 * Routing-advice data file (selection-query cutover, #581 / spike #568 §4.2).
 *
 * The eval lane materializes the advisor's aggregates to
 * `.nightgauge/model-evals/routing-advice.json`, and BOTH resolvers read that
 * file — the TS `resolveModel` chain and the Go dispatch path — exactly as
 * both read the model registry today. A data-file handoff keeps the extension
 * out of the routing business: threading TS advice over the wire would
 * reintroduce the dual-path drift #340 removed.
 *
 * Consumption is opt-in (`model_routing.use_eval_recommendations`, default
 * false) and clamp-bounded: the advisor slots between the stage default and
 * the ladder ordering — it may re-pick WITHIN the candidate set and envelope
 * clamps, never outside them. Only honest (schema_version ≥ 3) eval records
 * ever reach this file (the advisor excludes the rest structurally), and
 * every entry carries its backoff level and an `advisable` flag so sparse
 * evidence is visible instead of silently authoritative.
 */

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { z } from "zod";
import {
  EffortLevelSchema,
  JobClassSchema,
  MIN_HONEST_SCHEMA_VERSION,
  type JobClass,
  type ModelEvalRecord,
} from "./modelEvalSchemas.js";
import {
  EvalRoutingAdvisor,
  pickForMode,
  type AdvisorOptions,
  type EnvelopeStats,
  type RoutingMode,
} from "./routingAdvisor.js";

/** Bump on breaking shape changes of the advice file. */
export const ROUTING_ADVICE_SCHEMA_VERSION = 1;

/** Where the advice file lives, relative to the workspace root. */
export const ROUTING_ADVICE_RELATIVE_PATH = join(
  ".nightgauge",
  "model-evals",
  "routing-advice.json"
);

export const ThinkingStateSchema = z.enum(["on", "off"]);

export const RoutingAdviceEntrySchema = z
  .object({
    job_class: JobClassSchema,
    model_id: z.string().min(1),
    /** Absent on `model`-backoff aggregates — averaged-over, not dispatchable. */
    effort: EffortLevelSchema.optional(),
    thinking: ThinkingStateSchema.optional(),
    /** Which backoff level produced the entry (spike §4.3). */
    backoff: z.enum(["exact", "model"]),
    samples: z.number().int().nonnegative(),
    pass_rate: z.number().min(0).max(1),
    mean_quality: z.number().min(0).max(100),
    mean_cost_usd: z.number().nonnegative(),
    quality_per_dollar: z.number().nonnegative(),
    /** False = below the per-combination sample floor; visible, never applied. */
    advisable: z.boolean(),
  })
  .strict();
export type RoutingAdviceEntry = z.infer<typeof RoutingAdviceEntrySchema>;

export const RoutingAdviceFileSchema = z
  .object({
    schema_version: z.literal(ROUTING_ADVICE_SCHEMA_VERSION),
    /** ISO-8601, injected by the caller — building the file stays pure. */
    generated_at: z.string(),
    /** The per-combination sample floor the advisable flags were computed at. */
    min_samples: z.number().int().positive(),
    /** The honest-record floor the source records were filtered by (#571). */
    min_honest_schema_version: z.number().int().positive(),
    entries: z.array(RoutingAdviceEntrySchema),
  })
  .strict();
export type RoutingAdviceFile = z.infer<typeof RoutingAdviceFileSchema>;

/**
 * Build the advice file content from eval records. Pure: the timestamp is
 * injected. Honesty gate, low-confidence exclusion, registry-interlock
 * pruning and the sample floor all come from {@link EvalRoutingAdvisor}.
 */
export function buildRoutingAdvice(
  records: ModelEvalRecord[],
  generatedAt: string,
  options: AdvisorOptions = {}
): RoutingAdviceFile {
  const advisor = new EvalRoutingAdvisor(records, options);
  return {
    schema_version: ROUTING_ADVICE_SCHEMA_VERSION,
    generated_at: generatedAt,
    min_samples: advisor.minSamples,
    min_honest_schema_version: MIN_HONEST_SCHEMA_VERSION,
    entries: advisor.adviceEntries().map((e) => ({
      job_class: e.jobClass,
      model_id: e.modelId,
      // `model`-backoff aggregates carry no dispatchable effort/thinking.
      ...(e.backoff === "exact" && e.effort !== undefined ? { effort: e.effort } : {}),
      ...(e.backoff === "exact" ? { thinking: e.thinking } : {}),
      backoff: e.backoff,
      samples: e.samples,
      pass_rate: round(e.passRate, 4),
      mean_quality: round(e.meanQuality, 2),
      mean_cost_usd: round(e.meanCostUsd, 6),
      quality_per_dollar: round(e.qualityPerDollar, 4),
      advisable: e.advisable,
    })),
  };
}

/** Write the advice file under the workspace root, creating directories. */
export function writeRoutingAdvice(workspaceRoot: string, advice: RoutingAdviceFile): string {
  const path = join(workspaceRoot, ROUTING_ADVICE_RELATIVE_PATH);
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `${JSON.stringify(advice, null, 2)}\n`);
  return path;
}

/**
 * Read and validate the advice file, or undefined when absent or invalid —
 * ALWAYS fail-open: routing must never be blocked by a missing or stale
 * advice file (its absence just means the declared ladder rules).
 */
export function readRoutingAdvice(workspaceRoot: string): RoutingAdviceFile | undefined {
  try {
    const raw = readFileSync(join(workspaceRoot, ROUTING_ADVICE_RELATIVE_PATH), "utf-8");
    return RoutingAdviceFileSchema.parse(JSON.parse(raw));
  } catch {
    return undefined;
  }
}

/**
 * Pick the best ADVISABLE advice entry for (jobClass, mode) from a file —
 * the consumption-side selection both resolvers share. Exact-backoff entries
 * are preferred over `model` aggregates; the mode ordering and quality floor
 * mirror the advisor's own `recommend`. Returns undefined when nothing
 * advisable exists (the axis query alone decides — today's behavior).
 */
export function pickAdvice(
  advice: RoutingAdviceFile,
  jobClass: JobClass,
  mode: RoutingMode,
  qualityFloor = 70
): RoutingAdviceEntry | undefined {
  const forClass = advice.entries.filter((e) => e.job_class === jobClass && e.advisable);
  if (forClass.length === 0) return undefined;
  const exact = forClass.filter((e) => e.backoff === "exact");
  const pool = exact.length > 0 ? exact : forClass;
  const stats = pool.map(toStats);
  const eligible =
    mode === "maximum" || mode === "frontier"
      ? stats
      : stats.filter((s) => s.meanQuality >= qualityFloor);
  const winner = pickForMode(eligible.length > 0 ? eligible : stats, mode);
  return pool[stats.indexOf(winner)];
}

function toStats(e: RoutingAdviceEntry): EnvelopeStats {
  return {
    jobClass: e.job_class,
    modelId: e.model_id,
    effort: e.effort ?? "low",
    thinking: e.thinking ?? "on",
    samples: e.samples,
    passRate: e.pass_rate,
    meanQuality: e.mean_quality,
    meanCostUsd: e.mean_cost_usd,
    qualityPerDollar: e.quality_per_dollar,
  };
}

function round(n: number, places: number): number {
  const f = 10 ** places;
  return Math.round(n * f) / f;
}
