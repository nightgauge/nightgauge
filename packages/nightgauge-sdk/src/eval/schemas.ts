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
import { TIER_BANDS } from "./tierBands.js";

/** Harness record schema version. Bump on breaking JSONL shape changes. */
export const EVAL_SCHEMA_VERSION = "1";

/**
 * Model tiers the harness can evaluate, in ascending capability order.
 *
 * Derived from `TIER_BANDS` — the one TS band declaration (#581) — rather
 * than kept as a structurally-checked copy. The compile-time parity guard the
 * copy needed is gone with the copy: `ModelTier` derives from the same
 * authority.
 */
export const MODEL_TIERS = TIER_BANDS;

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

/**
 * Every skill the scenario loader walks — the six stages plus the skills that
 * are evaluated but are not pipeline stages.
 *
 * Kept separate from `PIPELINE_SKILLS` on purpose. That list also types
 * `target_stages` and `stage` in the model-eval schemas, where a non-stage
 * member would be meaningless; widening it in place would have quietly made
 * "check-triage" a legal answer to "which stage does this model routing apply
 * to". The loader's question is different — which directories hold scenarios —
 * so it gets its own list.
 *
 * A skill missing from here is not an error: its scenario directory is simply
 * never read, and its scenarios never run. That silence is why
 * `nightgauge-check-triage` is registered in the same change that adds its
 * scenarios (#1262) — three scenario files that no harness loads are the exact
 * shape of decoration shipped as coverage.
 */
export const EVAL_SKILLS = [...PIPELINE_SKILLS, "check-triage"] as const;

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

/**
 * Where a text/regex/JSON assertion looks. Absent → the whole output.
 *
 * `last_fenced_block` → only the body of the LAST fenced code block in the
 * output (optionally the last one whose info-string language is in `lang`).
 * This is how a scenario checks the model's actual decision rather than its
 * prose: the prompt asks it to end with the command(s) it would run in a
 * ```bash block, or with a small decision object in a ```json block, and the
 * forbidden-behaviour assertion reads only that block. Prose may then name the
 * forbidden thing freely to reject it. A missing block (or, for JSON
 * assertions, an unparseable one) FAILS the assertion — fail closed — for
 * negative assertions too.
 */
const ScopeFields = {
  scope: z.literal("last_fenced_block").optional(),
  /** Info-string language(s) the block must carry, e.g. "bash" or ["bash","sh"]. Case-insensitive. */
  lang: z.union([z.string().min(1), z.array(z.string().min(1)).min(1)]).optional(),
  /**
   * Strip unquoted shell `#` comments from the scoped block before matching,
   * so `gh pr merge --squash  # never --admin` checks only what would run.
   */
  strip_comments: z.boolean().optional(),
};

/**
 * JavaScript regex flags. `y` (sticky) is rejected: it anchors at lastIndex 0,
 * silently turning a "matches anywhere" assertion into "matches at the start".
 */
const RegexFlags = z
  .string()
  .regex(/^[dgimsuv]*$/, 'regex flags may only use "dgimsuv" (sticky "y" is rejected)');

/**
 * Deterministic checks against a model's output. Each assertion is a
 * discriminated-union member keyed by `type`. Assertions are intentionally
 * coarse (contract-shape checks) so they tolerate phrasing variation while
 * still catching documented failure modes.
 */
export const EvalAssertionSchema = z
  .discriminatedUnion("type", [
    z.object({
      type: z.literal("contains"),
      /** Substring that MUST appear in the output. */
      value: z.string().min(1),
      /** Case-insensitive match (default false). */
      ignore_case: z.boolean().optional(),
      /** Human-readable description of what this guards. */
      description: z.string().optional(),
      ...ScopeFields,
    }),
    z.object({
      type: z.literal("not_contains"),
      /** Substring that MUST NOT appear in the output. */
      value: z.string().min(1),
      ignore_case: z.boolean().optional(),
      description: z.string().optional(),
      ...ScopeFields,
    }),
    z.object({
      type: z.literal("matches_regex"),
      /** JavaScript regex source the output MUST match. */
      pattern: z.string().min(1),
      /** Regex flags (e.g. "i", "m", "s"). */
      flags: RegexFlags.optional(),
      description: z.string().optional(),
      ...ScopeFields,
    }),
    z.object({
      type: z.literal("not_matches_regex"),
      /**
       * JavaScript regex source the output MUST NOT match. To forbid a
       * behaviour, scope it to the structured part of the answer
       * (`scope: "last_fenced_block"`) so a correct answer whose prose names
       * the forbidden thing to reject it still passes.
       */
      pattern: z.string().min(1),
      flags: RegexFlags.optional(),
      description: z.string().optional(),
      ...ScopeFields,
    }),
    z.object({
      type: z.literal("json_path_exists"),
      /**
       * Dot/bracket path into a JSON object parsed from the output. Supports
       * `a.b.c` and `a.b[0].c`. The assertion passes if the path resolves to a
       * value that is not `undefined`. Unscoped, the first balanced JSON in
       * the output is used; scoped, the whole block body must parse.
       */
      path: z.string().min(1),
      description: z.string().optional(),
      ...ScopeFields,
    }),
    z.object({
      type: z.literal("json_path_equals"),
      /** Dot/bracket path, as for `json_path_exists`. */
      path: z.string().min(1),
      /** The JSON scalar the path MUST resolve to (strict equality, no coercion). */
      value: z.union([z.string(), z.number(), z.boolean(), z.null()]),
      description: z.string().optional(),
      ...ScopeFields,
    }),
    z.object({
      type: z.literal("exit_code"),
      /** Expected process exit code (live mode only; mock fixtures supply it). */
      value: z.number().int(),
      description: z.string().optional(),
    }),
  ])
  .superRefine((a, ctx) => {
    if (a.type === "exit_code" || a.scope !== undefined) return;
    if (a.lang !== undefined || a.strip_comments !== undefined) {
      ctx.addIssue({
        code: "custom",
        message: '"lang" and "strip_comments" require scope: "last_fenced_block"',
      });
    }
  });

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
  skill: z.enum(EVAL_SKILLS),
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
  skill: z.enum(EVAL_SKILLS),
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
  skills: z.array(z.enum(EVAL_SKILLS)),
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
