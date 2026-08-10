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
 * Effort levels — **the single effort vocabulary authority** (#394).
 *
 * Every other surface derives from this array rather than re-listing it:
 * `EffortLevel` / `ClaudeEffort` (the type), `EffortLevelSchema` and the
 * extension's `ClaudeEffortSchema` (the runtime validators), the experiment
 * schema, and the resolver's validator array and config regexes. Independent
 * copies drifted three ways and silently rejected `max` after #75; derivation
 * removes the failure mode rather than guarding against it.
 */
export const EFFORT_LEVELS = ["low", "medium", "high", "xhigh", "max"] as const;

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

/**
 * USD per 1,000,000 tokens. Cache rates optional (not all providers bill them).
 *
 * Cache CREATION has two rates, not one (#358). Anthropic bills a cache write
 * by the TTL it buys: 5-minute at 1.25x base input, 1-hour at 2.0x. The CLI
 * reports which pool a write landed in via
 * `usage.cache_creation.{ephemeral_5m,ephemeral_1h}_input_tokens`, so a single
 * blended rate mis-prices every run.
 */
export const TokenRatesSchema = z
  .object({
    input: z.number().nonnegative(),
    output: z.number().nonnegative(),
    cache_read: z.number().nonnegative().optional(),
    /** 5-minute TTL cache write (1.25x base input). */
    cache_creation_5m: z.number().nonnegative().optional(),
    /** 1-hour TTL cache write (2.0x base input). */
    cache_creation_1h: z.number().nonnegative().optional(),
  })
  .strict();
export type TokenRates = z.infer<typeof TokenRatesSchema>;

/**
 * Sentinel for {@link BehaviorSchema.shape.thinking_disable_max_effort}: this
 * model rejects disabled thinking at EVERY effort level, so there is no
 * effort to name as the ceiling.
 *
 * Needed because "omitted" already means the opposite — unconstrained. Fable 5
 * returns a 400 for `thinking: {"type": "disabled"}` at any effort, and
 * without this value the only way to describe it is to leave the field off,
 * which tells the interlock the pairing is always legal.
 */
export const THINKING_DISABLE_NEVER = "never";

/**
 * Ceiling on disabling thinking: an effort level, or `never`. Deliberately not
 * a member of {@link EffortLevelSchema} — `never` is not a requestable effort,
 * and widening the effort enum would let it leak into `supported_efforts`.
 */
export const ThinkingDisableLimitSchema = z.union([
  EffortLevelSchema,
  z.literal(THINKING_DISABLE_NEVER),
]);
export type ThinkingDisableLimit = z.infer<typeof ThinkingDisableLimitSchema>;

/**
 * How readily a model does something unbidden. Coarse on purpose (#77): these
 * are revisable claims sourced from vendor documentation, and a three-way
 * enum keeps them honest — a numeric score would imply a precision the
 * evidence does not support (ADR 016 §6).
 *
 * `normal` is the neutral reading, and it is also what an undeclared
 * propensity means, so a model with no entry behaves exactly as before.
 */
export const PROPENSITY_LEVELS = ["low", "normal", "high"] as const;
export const PropensityLevelSchema = z.enum(PROPENSITY_LEVELS);
export type PropensityLevel = z.infer<typeof PropensityLevelSchema>;

/**
 * The propensities overlays key off. Each is a documented disposition, not an
 * instruction: an overlay reads `verification: "high"` and decides to drop the
 * skill's own verification scaffolding, rather than restating the fact.
 */
export const PropensitySchema = z
  .object({
    /** How readily the model checks its own work without being asked. */
    verification: PropensityLevelSchema.optional(),
    /** How readily the model hands work to subagents. */
    delegation: PropensityLevelSchema.optional(),
    /** How much the model narrates progress between tool calls. */
    narration: PropensityLevelSchema.optional(),
  })
  .strict();
export type Propensity = z.infer<typeof PropensitySchema>;

/**
 * Factual runtime properties of a model (#77).
 *
 * Strictly things the provider documents and a reader could verify — no
 * judgment, no instructions. Behavioral GUIDANCE lives in skill overlays
 * (ADR 016); this is the data those overlays reason about, so a fact is stated
 * once here rather than restated in every overlay that depends on it.
 */
export const BehaviorSchema = z
  .object({
    /** Whether the model reasons by default with no thinking parameter set. */
    thinking_default: z.enum(["on", "off"]).optional(),
    /**
     * Highest effort at which thinking may be disabled, or `never`. Omitted =
     * unconstrained (pre-Opus-5, where the two settings were independent).
     * Opus 5 caps this at `high`; disabling thinking at `xhigh`/`max` is a 400.
     * Fable 5 refuses at every level — see {@link THINKING_DISABLE_NEVER}.
     */
    thinking_disable_max_effort: ThinkingDisableLimitSchema.optional(),
    /** Provider default effort when none is requested. */
    effort_default: EffortLevelSchema.optional(),
    /** Bounds thinking AND response text together — headroom matters at high effort. */
    max_output_tokens: z.number().int().positive().optional(),
    /** Coarse dispositions overlays act on. Absent = read every axis as `normal`. */
    propensity: PropensitySchema.optional(),
  })
  .strict();
export type Behavior = z.infer<typeof BehaviorSchema>;

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
    /**
     * The effort levels this model accepts. REQUIRED, and emptiable — two
     * registry states, both positive declarations (#336):
     *
     * - a **non-empty** array — the model takes an effort parameter, at
     *   exactly these levels;
     * - **`[]`** — the model has NO effort axis. Haiku has no extended
     *   thinking, so there is no level to request. This is a declaration, not
     *   missing data, and it is what suppresses `--effort` at the dispatch
     *   boundary.
     *
     * There is deliberately no third, in-descriptor "unknown" state.
     * **Unknown is descriptor-ABSENCE** — `getModelDescriptor` returning
     * `undefined` for a local ollama/lm-studio model or an unregistered id —
     * and that is the only spelling of it. An omittable key would let a
     * registry entry be silently uncharacterized while looking complete, which
     * is the failure mode this field already had once: `.min(1)` made `[]`
     * inexpressible, so "haiku has no effort axis" had to live in a hardcoded
     * band set in the VSCode extension while the registry declared the
     * opposite.
     */
    supported_efforts: z.array(EffortLevelSchema),
    supported_reasoning: z.array(ReasoningLevelSchema).min(1),
    context_window: z.number().int().positive(),
    deprecated: z.boolean().optional(),
    /** For deprecated models, the current id callers should migrate to. */
    replacement: z.string().min(1).optional(),
    /** Provider-recommended default for its strongest band (UI ordering hint). */
    recommended: z.boolean().optional(),
    /** Research-preview model — excluded from default catalog/UI listings. */
    research_preview: z.boolean().optional(),
    /**
     * Factual, vendor-documented runtime properties (#77). Optional — a model
     * without it behaves exactly as before, and local models have no registry
     * entry at all. Facts only: never prose, never instructions.
     */
    behavior: BehaviorSchema.optional(),
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
