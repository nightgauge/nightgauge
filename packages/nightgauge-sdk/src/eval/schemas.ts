/**
 * Cross-Model Skill Evaluation Harness — Zod schemas.
 *
 * Declarative scenario format + run-report shapes for evaluating pipeline-stage
 * skills against multiple model tiers (haiku/sonnet/opus) and detecting
 * regressions across skill refactors and model bumps.
 *
 * The harness invokes models by **tier alias** (`--model haiku|sonnet|opus`),
 * matching how the live pipeline runs skills — never by pinned concrete model
 * ID. Concrete version labels are recorded for reporting only.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 * @see packages/nightgauge-sdk/src/analysis/AutoModelSelector.ts - ModelTier (reused)
 * @see docs/SKILL_EVALUATION.md - scenario format and assertion reference
 */

import { z } from "zod";
import type { ModelTier } from "../analysis/AutoModelSelector.js";

/** Harness record schema version. Bump on breaking JSONL shape changes. */
export const EVAL_SCHEMA_VERSION = "1";

/**
 * Model tiers the harness can evaluate, in ascending capability order.
 *
 * Kept structurally identical to `ModelTier` from AutoModelSelector. The
 * `satisfies` check below fails to compile if the two ever drift, so the tier
 * source of truth stays single even though Zod needs a runtime enum.
 */
export const MODEL_TIERS = ["haiku", "sonnet", "opus", "fable"] as const;

// Compile-time guard: the enum members must be exactly the ModelTier union.
type _TierParity = (typeof MODEL_TIERS)[number] extends ModelTier
  ? ModelTier extends (typeof MODEL_TIERS)[number]
    ? true
    : never
  : never;
const _tierParity: _TierParity = true;
void _tierParity;

export const ModelTierSchema = z.enum(MODEL_TIERS);

/**
 * Concrete model version labels per tier — for reporting/record only, NOT for
 * invocation. The live path passes the tier alias to the `claude` CLI, which
 * resolves the concrete version itself (see memory `model_version_resolution`).
 * Update these labels when the CLI's resolved versions change so historical
 * records remain interpretable; a bump here does not change invocation.
 */
export const MODEL_TIER_VERSION_LABELS: Record<ModelTier, string> = {
  haiku: "Haiku 4.5",
  sonnet: "Sonnet 4.6",
  opus: "Opus 4.8",
  fable: "Fable 5",
};

/** The six pipeline-stage skills the harness ships scenarios for. */
export const PIPELINE_SKILLS = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

/**
 * Deterministic checks against a model's output. Each assertion is a
 * discriminated-union member keyed by `type`. Assertions are intentionally
 * coarse (contract-shape checks) so they tolerate phrasing variation while
 * still catching documented failure modes.
 */
export const EvalAssertionSchema = z.discriminatedUnion("type", [
  z.object({
    type: z.literal("contains"),
    /** Substring that MUST appear in the output. */
    value: z.string().min(1),
    /** Case-insensitive match (default false). */
    ignore_case: z.boolean().optional(),
    /** Human-readable description of what this guards. */
    description: z.string().optional(),
  }),
  z.object({
    type: z.literal("not_contains"),
    /** Substring that MUST NOT appear in the output. */
    value: z.string().min(1),
    ignore_case: z.boolean().optional(),
    description: z.string().optional(),
  }),
  z.object({
    type: z.literal("matches_regex"),
    /** JavaScript regex source the output MUST match. */
    pattern: z.string().min(1),
    /** Regex flags (e.g. "i", "m", "s"). */
    flags: z.string().optional(),
    description: z.string().optional(),
  }),
  z.object({
    type: z.literal("json_path_exists"),
    /**
     * Dot/bracket path into a JSON object parsed from the output. Supports
     * `a.b.c` and `a.b[0].c`. The assertion passes if the path resolves to a
     * value that is not `undefined`.
     */
    path: z.string().min(1),
    description: z.string().optional(),
  }),
  z.object({
    type: z.literal("exit_code"),
    /** Expected process exit code (live mode only; mock fixtures supply it). */
    value: z.number().int(),
    description: z.string().optional(),
  }),
]);

export type EvalAssertion = z.infer<typeof EvalAssertionSchema>;
export type EvalAssertionType = EvalAssertion["type"];

// ---------------------------------------------------------------------------
// Scenario
// ---------------------------------------------------------------------------

/**
 * A single declarative evaluation scenario. One scenario × one model tier =
 * one matrix cell.
 */
export const EvalScenarioSchema = z.object({
  /** Stable kebab-case scenario id. Unique within a skill. */
  id: z
    .string()
    .min(1)
    .regex(/^[a-z0-9-]+$/, "scenario id must be kebab-case ([a-z0-9-])"),
  /** Which pipeline-stage skill this scenario exercises. */
  skill: z.enum(PIPELINE_SKILLS),
  /** One-line human description of the scenario. */
  description: z.string().min(1),
  /** The known failure mode this scenario guards against. */
  failure_mode: z.string().min(1),
  /** The scenario input handed to the skill (the user-side prompt). */
  prompt: z.string().min(1),
  /** Deterministic checks; ALL must pass for the cell to pass. */
  assertions: z.array(EvalAssertionSchema).min(1),
  /**
   * Optional tier subset. When set, the harness only evaluates these tiers for
   * this scenario; otherwise it evaluates whatever tiers the run requests.
   */
  models: z.array(ModelTierSchema).optional(),
});

export type EvalScenario = z.infer<typeof EvalScenarioSchema>;

// ---------------------------------------------------------------------------
// Results
// ---------------------------------------------------------------------------

export const EvalVerdictSchema = z.enum(["pass", "fail", "error"]);
export type EvalVerdict = z.infer<typeof EvalVerdictSchema>;

export const EvalModeSchema = z.enum(["mock", "live"]);
export type EvalMode = z.infer<typeof EvalModeSchema>;

/** Evidence for a single failed assertion. */
export const AssertionFailureSchema = z.object({
  type: z.string(),
  /** Human-readable reason the assertion failed. */
  reason: z.string(),
  /** What the assertion was looking for (value/pattern/path/code). */
  expected: z.string().optional(),
});

export type AssertionFailure = z.infer<typeof AssertionFailureSchema>;

/** One (scenario, model) matrix cell result. */
export const EvalCellResultSchema = z.object({
  scenario_id: z.string(),
  skill: z.enum(PIPELINE_SKILLS),
  model: ModelTierSchema,
  /** Concrete version label recorded for interpretation (reporting only). */
  model_version_label: z.string(),
  verdict: EvalVerdictSchema,
  /** Failed-assertion evidence; empty on pass. */
  failures: z.array(AssertionFailureSchema),
  /** Exit code returned by the runner (mock-supplied or live). */
  exit_code: z.number().int().optional(),
  /** Free-form error message when verdict === "error". */
  error: z.string().optional(),
});

export type EvalCellResult = z.infer<typeof EvalCellResultSchema>;

/** A complete run report across the (scenario × model) matrix. */
export const EvalRunReportSchema = z.object({
  schema_version: z.literal(EVAL_SCHEMA_VERSION),
  /** ISO-8601 timestamp, injected by the runner (never generated in a pure fn). */
  timestamp: z.string(),
  mode: EvalModeSchema,
  /** Skills covered by this run. */
  skills: z.array(z.enum(PIPELINE_SKILLS)),
  /** Model tiers covered by this run. */
  models: z.array(ModelTierSchema),
  cells: z.array(EvalCellResultSchema),
  summary: z.object({
    total: z.number().int(),
    passed: z.number().int(),
    failed: z.number().int(),
    errored: z.number().int(),
  }),
});

export type EvalRunReport = z.infer<typeof EvalRunReportSchema>;

/** One persisted JSONL line: a single cell stamped with run-level fields. */
export const EvalRecordSchema = EvalCellResultSchema.extend({
  schema_version: z.literal(EVAL_SCHEMA_VERSION),
  timestamp: z.string(),
  mode: EvalModeSchema,
});

export type EvalRecord = z.infer<typeof EvalRecordSchema>;
