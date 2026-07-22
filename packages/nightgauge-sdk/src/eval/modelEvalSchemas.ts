/**
 * Model Evaluation & Benchmarking System — Zod schemas (core data contracts).
 *
 * Distinct from the **skill**-eval harness in `./schemas.ts` (binary pass/fail
 * on synthetic prompts, a live CI gate). This lane runs **realistic tasks**
 * through the real pipeline across a matrix of `model × effort × reasoning` and
 * measures cost / latency / attempts-to-green / correctness / subjective
 * quality. Shared primitives (`ModelTierSchema`, `PIPELINE_SKILLS`,
 * `EvalVerdictSchema`, `EvalModeSchema`) are reused from `./schemas.ts` — two
 * lanes, one set of primitives.
 *
 * The wire-facing subset of these shapes is mirrored into
 * `@nightgauge/shared-types` by S8 (#1158); these schemas are the source.
 *
 * @see docs/decisions/011-model-eval-system.md - the design decisions
 * @see Issue #4168 - Eval system ADR + core data contracts
 * @see Issue #4169 - Provider-agnostic model & pricing registry (populates ModelDescriptor)
 */

import { z } from "zod";
import type { ClaudeEffort } from "../analysis/AutoModelSelector.js";
import { EvalModeSchema, EvalVerdictSchema, ModelTierSchema, PIPELINE_SKILLS } from "./schemas.js";

/**
 * Model-eval record schema version. Bump on breaking JSONL/wire shape changes.
 * v2 (#72): matrix cells gained the `prompt_variant` axis — old readers with
 * strict cell schemas reject v2 records, so the version signals the change
 * (old v1 records still parse here via the field's `baseline` default).
 */
export const MODEL_EVAL_SCHEMA_VERSION = "2";

/**
 * The implicit prompt variant: the unmodified on-disk task instruction (#72).
 * Every cell carries a variant name; `baseline` means "no overlay applied".
 */
export const BASELINE_PROMPT_VARIANT = "baseline";

// ---------------------------------------------------------------------------
// Providers, effort, reasoning
// ---------------------------------------------------------------------------

/**
 * Model providers. Anthropic is seeded first, but the system is provider-neutral
 * by design — a new provider is a registry data entry, not a code change.
 */
export const PROVIDERS = [
  "anthropic",
  "openai",
  "google",
  "copilot",
  "ollama",
  "lm-studio",
  "other",
] as const;
export const ProviderSchema = z.enum(PROVIDERS);
export type Provider = z.infer<typeof ProviderSchema>;

/**
 * Effort levels. Kept identical to `ClaudeEffort` from AutoModelSelector — the
 * `satisfies`-style compile guard below fails if the two ever drift, so the
 * source of truth stays single even though Zod needs a runtime enum.
 */
export const EFFORT_LEVELS = ["low", "medium", "high", "xhigh"] as const;

// Compile-time guard: the enum members must be exactly the ClaudeEffort union.
type _EffortParity = (typeof EFFORT_LEVELS)[number] extends ClaudeEffort
  ? ClaudeEffort extends (typeof EFFORT_LEVELS)[number]
    ? true
    : never
  : never;
const _effortParity: _EffortParity = true;
void _effortParity;

export const EffortLevelSchema = z.enum(EFFORT_LEVELS);
export type EffortLevel = z.infer<typeof EffortLevelSchema>;

/**
 * Provider-neutral reasoning-budget axis. Today effort is derived but reasoning
 * is not a typed axis; S4 (#4171) wires this into the adapter spawn (Claude
 * extended thinking, OpenAI reasoning effort, etc.). `none` means no extended
 * reasoning budget.
 */
export const REASONING_LEVELS = ["none", "low", "medium", "high"] as const;
export const ReasoningLevelSchema = z.enum(REASONING_LEVELS);
export type ReasoningLevel = z.infer<typeof ReasoningLevelSchema>;

// ---------------------------------------------------------------------------
// Model descriptor + pricing (S2 populates the registry; this is the shape)
// ---------------------------------------------------------------------------

/** USD per 1,000,000 tokens. Cache rates optional (not all providers bill them). */
export const TokenRatesSchema = z
  .object({
    input: z.number().nonnegative(),
    output: z.number().nonnegative(),
    cache_read: z.number().nonnegative().optional(),
    cache_creation: z.number().nonnegative().optional(),
  })
  .strict();
export type TokenRates = z.infer<typeof TokenRatesSchema>;

/**
 * Provider-neutral description of an evaluable model — the single source of
 * truth for cost computation and capability metadata. The S2 registry is a list
 * of these; adding a model (e.g. a new release) is one entry.
 */
export const ModelDescriptorSchema = z
  .object({
    /** Stable registry key, e.g. "claude-opus-4-8". */
    id: z.string().min(1),
    provider: ProviderSchema,
    /**
     * Cross-provider capability BANDS this model serves. Band names reuse the
     * canonical routing tiers (haiku/sonnet/opus/fable) but are provider-neutral:
     * a provider without a fable-equivalent maps `fable` to its strongest model
     * (e.g. gpt-5.5 serves both opus and fable). At most one non-deprecated
     * model per (provider, band) — enforced by the registry loaders.
     */
    tiers: z.array(ModelTierSchema).min(1).optional(),
    /** Human-readable label, e.g. "Opus 4.8". */
    display_name: z.string().min(1),
    /** Concrete version id used for invocation/record, e.g. "claude-opus-4-8". */
    concrete_version: z.string().min(1),
    /** USD/MTok rates — the basis for all eval cost computation. */
    rates: TokenRatesSchema,
    supported_efforts: z.array(EffortLevelSchema).min(1),
    supported_reasoning: z.array(ReasoningLevelSchema).min(1),
    context_window: z.number().int().positive(),
    deprecated: z.boolean().optional(),
    /** For deprecated models, the current id callers should migrate to. */
    replacement: z.string().min(1).optional(),
    /** Provider-recommended default for its strongest band (UI ordering hint). */
    recommended: z.boolean().optional(),
    /** Research-preview model — excluded from default catalog/UI listings. */
    research_preview: z.boolean().optional(),
  })
  .strict();
export type ModelDescriptor = z.infer<typeof ModelDescriptorSchema>;

// ---------------------------------------------------------------------------
// Quality dimensions, rubric, scoring
// ---------------------------------------------------------------------------

/** Named scoring dimensions a rubric / judge can score. */
export const QUALITY_DIMENSIONS = [
  "correctness",
  "completeness",
  "code_quality",
  "ux_quality",
  "clarity",
  "performance",
] as const;
export const QualityDimensionNameSchema = z.enum(QUALITY_DIMENSIONS);
export type QualityDimensionName = z.infer<typeof QualityDimensionNameSchema>;

/** One scored dimension of a cell's quality (judge-emitted or derived). */
export const QualityDimensionScoreSchema = z
  .object({
    dimension: QualityDimensionNameSchema,
    /** 0–100 for this dimension. */
    score: z.number().min(0).max(100),
    /** Contribution weight within the composite (0–1). */
    weight: z.number().min(0).max(1),
    rationale: z.string().optional(),
    /** Set by the S5 judge-reliability guard when repeat-judgment variance is high. */
    low_confidence: z.boolean().optional(),
  })
  .strict();
export type QualityDimensionScore = z.infer<typeof QualityDimensionScoreSchema>;

/** One rubric criterion: which dimension, how heavily weighted, and judge guidance. */
export const RubricCriterionSchema = z
  .object({
    dimension: QualityDimensionNameSchema,
    weight: z.number().min(0).max(1),
    guidance: z.string().min(1),
  })
  .strict();
export type RubricCriterion = z.infer<typeof RubricCriterionSchema>;

/** A task's grading rubric — the criteria the S5 judge scores against. */
export const EvalRubricSchema = z
  .object({
    criteria: z.array(RubricCriterionSchema).min(1),
  })
  .strict();
export type EvalRubric = z.infer<typeof EvalRubricSchema>;

/** Composite per-cell score (0–100) + its components. */
export const EvalScoreSchema = z
  .object({
    /** Weighted blend of correctness + automated-metric penalties + judge quality. */
    composite: z.number().min(0).max(100),
    /** Deterministic-gates correctness component (0–100). */
    correctness: z.number().min(0).max(100),
    /** Per-dimension judge breakdown (empty in deterministic-only mode). */
    dimensions: z.array(QualityDimensionScoreSchema),
    /** True when an LLM judge contributed subjective scores. */
    judge_used: z.boolean(),
    /** Overall judge-reliability flag (set by the S5 variance guard). */
    low_confidence: z.boolean().optional(),
  })
  .strict();
export type EvalScore = z.infer<typeof EvalScoreSchema>;

// ---------------------------------------------------------------------------
// Tasks, fixtures, checks, job classes
// ---------------------------------------------------------------------------

/**
 * Job classes — the realistic categories of work people build with the pipeline.
 * Used to group results and to weight scoring per class.
 */
export const JOB_CLASSES = [
  "ui-creation",
  "ux-styling",
  "backend-logic",
  "testing",
  "bugfix",
  "refactor",
  "docs",
] as const;
export const JobClassSchema = z.enum(JOB_CLASSES);
export type JobClass = z.infer<typeof JobClassSchema>;

export const DifficultySchema = z.enum(["easy", "medium", "hard"]);
export type Difficulty = z.infer<typeof DifficultySchema>;

/** How to materialize a task's seed repo state into a worktree. */
export const EvalFixtureRefSchema = z
  .object({
    kind: z.enum(["base-commit", "scaffold-script", "snapshot-dir"]),
    /** Commit SHA / script path / directory path, per `kind`. */
    ref: z.string().min(1),
    /** Optional `owner/repo` when the fixture lives outside the primary repo. */
    repo: z.string().optional(),
  })
  .strict();
export type EvalFixtureRef = z.infer<typeof EvalFixtureRefSchema>;

/** A deterministic check run in the worktree after a stage/pipeline completes. */
export const CheckCommandSchema = z
  .object({
    /** "build" | "test" | "lint" | "typecheck" | custom name. */
    name: z.string().min(1),
    /** Shell command executed in the worktree. */
    command: z.string().min(1),
    /** Exit code that means "passed" (default 0). */
    expect_exit_code: z.number().int().default(0),
  })
  .strict();
export type CheckCommand = z.infer<typeof CheckCommandSchema>;

/**
 * A realistic evaluation task. One task × one matrix cell = one
 * `ModelEvalCellResult`.
 */
export const EvalTaskSchema = z
  .object({
    /** Stable kebab-case task id. */
    id: z
      .string()
      .min(1)
      .regex(/^[a-z0-9-]+$/, "task id must be kebab-case ([a-z0-9-])"),
    title: z.string().min(1),
    job_class: JobClassSchema,
    /** Pipeline stage(s) this task exercises. */
    target_stages: z.array(z.enum(PIPELINE_SKILLS)).min(1),
    difficulty: DifficultySchema,
    /** The instruction / issue text handed to the pipeline. */
    instruction: z.string().min(1),
    fixture: EvalFixtureRefSchema,
    /** Deterministic checks scored by the correctness component. */
    checks: z.array(CheckCommandSchema),
    rubric: EvalRubricSchema,
  })
  .strict();
export type EvalTask = z.infer<typeof EvalTaskSchema>;

// ---------------------------------------------------------------------------
// Matrix cells + results + run
// ---------------------------------------------------------------------------

/** One `{model, effort, reasoning, prompt_variant}` combination to evaluate a task under. */
export const EvalMatrixCellSchema = z
  .object({
    model_id: z.string().min(1),
    effort: EffortLevelSchema,
    reasoning: ReasoningLevelSchema,
    /**
     * Named prompt-variant overlay the cell executes under (#72).
     * `baseline` = the unmodified on-disk instruction. The default keeps
     * pre-v2 JSONL records parseable.
     */
    prompt_variant: z.string().min(1).default(BASELINE_PROMPT_VARIANT),
  })
  .strict();
export type EvalMatrixCell = z.infer<typeof EvalMatrixCellSchema>;

/** Token usage for a cell (raw counts). */
export const TokenUsageSchema = z
  .object({
    input: z.number().int().nonnegative(),
    output: z.number().int().nonnegative(),
    cache_read: z.number().int().nonnegative().default(0),
    cache_creation: z.number().int().nonnegative().default(0),
  })
  .strict();
export type TokenUsage = z.infer<typeof TokenUsageSchema>;

/** One deterministic gate's outcome for a cell. */
export const GateResultSchema = z
  .object({
    name: z.string().min(1),
    passed: z.boolean(),
    detail: z.string().optional(),
  })
  .strict();
export type GateResult = z.infer<typeof GateResultSchema>;

/** One task × matrix-cell outcome: telemetry + (optional) score. */
export const ModelEvalCellResultSchema = z
  .object({
    task_id: z.string().min(1),
    job_class: JobClassSchema,
    /** Stage exercised, when the run targeted a single stage. */
    stage: z.enum(PIPELINE_SKILLS).optional(),
    cell: EvalMatrixCellSchema,
    /** Concrete model id evaluated (denormalized from cell for query convenience). */
    model_id: z.string().min(1),
    /** Concrete version label recorded for interpretation. */
    model_version_label: z.string(),
    verdict: EvalVerdictSchema,
    tokens: TokenUsageSchema,
    /** Computed from the S2 registry rates. */
    cost_usd: z.number().nonnegative(),
    latency_ms: z.number().int().nonnegative(),
    /** Canonical attempts-until-success (Ralph iterations + retries + escalations). */
    attempts_to_green: z.number().int().nonnegative(),
    gate_results: z.array(GateResultSchema),
    /** Composite score from S5; absent until scored. */
    score: EvalScoreSchema.optional(),
    /** Free-form error when verdict === "error". */
    error: z.string().optional(),
  })
  .strict();
export type ModelEvalCellResult = z.infer<typeof ModelEvalCellResultSchema>;

/** Run-level rollup. */
export const EvalRunSummarySchema = z
  .object({
    total: z.number().int().nonnegative(),
    passed: z.number().int().nonnegative(),
    failed: z.number().int().nonnegative(),
    errored: z.number().int().nonnegative(),
    total_cost_usd: z.number().nonnegative(),
  })
  .strict();
export type EvalRunSummary = z.infer<typeof EvalRunSummarySchema>;

/**
 * A complete model-eval run across the (task × matrix) space. Carries a snapshot
 * of the `ModelDescriptor`s used so historical cost stays interpretable when the
 * registry later changes.
 */
export const EvalRunSchema = z
  .object({
    schema_version: z.literal(MODEL_EVAL_SCHEMA_VERSION),
    run_id: z.string().min(1),
    /** ISO-8601 timestamp, injected by the runner (never generated in a pure fn). */
    timestamp: z.string(),
    mode: EvalModeSchema,
    /** Suite name (a named set of tasks + matrix). */
    suite: z.string().min(1),
    /** Task ids covered. */
    tasks: z.array(z.string().min(1)),
    matrix: z.array(EvalMatrixCellSchema),
    /** Snapshot of descriptors (incl. pricing) used for this run. */
    models: z.array(ModelDescriptorSchema),
    cells: z.array(ModelEvalCellResultSchema),
    summary: EvalRunSummarySchema,
  })
  .strict();
export type EvalRun = z.infer<typeof EvalRunSchema>;

/** One persisted JSONL line: a single cell stamped with run-level fields. */
export const ModelEvalRecordSchema = ModelEvalCellResultSchema.extend({
  schema_version: z.literal(MODEL_EVAL_SCHEMA_VERSION),
  run_id: z.string().min(1),
  suite: z.string().min(1),
  timestamp: z.string(),
  mode: EvalModeSchema,
}).strict();
export type ModelEvalRecord = z.infer<typeof ModelEvalRecordSchema>;
