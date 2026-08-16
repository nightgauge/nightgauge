/**
 * Tests for the model-eval core data contracts (Issue #4168,
 * docs/decisions/011-model-eval-system.md). Pure schema tests — no model calls.
 *
 * Covers the S1 acceptance criteria: round-trip parse, reject-unknown-keys
 * (strict), tier/effort parity with AutoModelSelector, and field defaults.
 */

import { describe, it, expect } from "vitest";
import {
  MODEL_EVAL_SCHEMA_VERSION,
  MIN_HONEST_SCHEMA_VERSION,
  EFFORT_LEVELS,
  REASONING_LEVELS,
  TRANSPORTS,
  RATE_PROVENANCES,
  TransportFactsSchema,
  ModelDescriptorSchema,
  EvalTaskSchema,
  EvalScoreSchema,
  EvalMatrixCellSchema,
  ModelEvalCellResultSchema,
  ModelEvalVerdictSchema,
  EvalRunSchema,
  ModelEvalRecordSchema,
  CheckCommandSchema,
  TokenUsageSchema,
  type ModelDescriptor,
  type EvalTask,
  type EvalRun,
} from "../../src/eval/modelEvalSchemas.js";
import { getModelDescriptor } from "../../src/eval/modelRegistry.js";
import { EvalVerdictSchema, MODEL_TIERS } from "../../src/eval/schemas.js";

const TS = "2026-06-30T12:00:00.000Z";

const OPUS: ModelDescriptor = {
  id: "claude-opus-4-8",
  provider: "anthropic",
  tiers: ["opus"],
  display_name: "Opus 4.8",
  concrete_version: "claude-opus-4-8",
  rates: { input: 5, output: 25, cache_read: 0.5, cache_creation_5m: 6.25, cache_creation_1h: 10 },
  supported_efforts: ["low", "medium", "high"],
  context_window: 200000,
};

const TASK: EvalTask = {
  id: "pricing-card-component",
  title: "Build a responsive pricing card component with tests",
  job_class: "ui-creation",
  target_stages: ["feature-dev", "feature-validate"],
  difficulty: "medium",
  instruction: "Create a responsive pricing card with three tiers and unit tests.",
  fixture: { kind: "base-commit", ref: "abc1234" },
  checks: [
    { name: "build", command: "npm run build", expect_exit_code: 0 },
    { name: "test", command: "npm test", expect_exit_code: 0 },
  ],
  rubric: {
    criteria: [
      { dimension: "ux_quality", weight: 0.5, guidance: "Is it visually polished and responsive?" },
      { dimension: "correctness", weight: 0.5, guidance: "Do the tests pass and cover the tiers?" },
    ],
  },
};

describe("model-eval schemas — parity guards", () => {
  it("EFFORT_LEVELS pins the five-level ladder, in ascending order", () => {
    // Not a union-parity check any more: since #394 every other surface
    // (`ClaudeEffort`, the Zod validators, the resolver, the Go `EffortOrder`
    // mirror) derives from this array, so nothing can disagree with it by
    // construction. What still needs pinning is the array's own content and
    // ORDER — the ladder is ascending reasoning depth and the clamps index
    // against it. `max` is the top of the ladder from Opus 5 (#75).
    expect([...EFFORT_LEVELS]).toEqual(["low", "medium", "high", "xhigh", "max"]);
  });

  it("REASONING_LEVELS is the provider-neutral budget axis", () => {
    expect([...REASONING_LEVELS]).toEqual(["none", "low", "medium", "high"]);
  });

  it("ModelDescriptor.tiers only accepts AutoModelSelector tiers", () => {
    for (const tier of MODEL_TIERS) {
      expect(ModelDescriptorSchema.parse({ ...OPUS, tiers: [tier] }).tiers).toEqual([tier]);
    }
    // Multi-band entries (one model serving several tiers, #56) parse too.
    expect(ModelDescriptorSchema.parse({ ...OPUS, tiers: ["opus", "fable"] }).tiers).toEqual([
      "opus",
      "fable",
    ]);
    expect(() => ModelDescriptorSchema.parse({ ...OPUS, tiers: ["gpt5"] })).toThrow();
    expect(() => ModelDescriptorSchema.parse({ ...OPUS, tiers: [] })).toThrow();
  });
});

describe("model-eval schemas — round-trip parse", () => {
  it("parses a valid ModelDescriptor", () => {
    expect(ModelDescriptorSchema.parse(OPUS)).toEqual(OPUS);
  });

  it("parses a provider-neutral (non-Anthropic, no tier) descriptor", () => {
    const other: ModelDescriptor = {
      id: "vendor-x-pro",
      provider: "other",
      display_name: "Vendor X Pro",
      concrete_version: "vendor-x-pro-2026",
      rates: { input: 2, output: 8 },
      supported_efforts: ["medium", "high"],
      context_window: 128000,
    };
    expect(ModelDescriptorSchema.parse(other)).toEqual(other);
  });

  it("round-trips an EMPTY effort axis, and REQUIRES the key (#336)", () => {
    // `[]` is a positive declaration — "this model has no effort axis" (Haiku
    // has no extended thinking). `.min(1)` used to make it inexpressible,
    // which forced the fact into a hardcoded band set in the VSCode extension
    // while the registry declared the opposite.
    const noAxis = ModelDescriptorSchema.parse({ ...OPUS, supported_efforts: [] });
    expect(noAxis.supported_efforts).toEqual([]);

    // Omitting the key is REJECTED, not read as "unknown". A descriptor that
    // exists has been characterized by definition; an omittable key would let
    // an entry look complete while saying nothing, which is the same silent
    // rot `.min(1)` produced from the other direction.
    const { supported_efforts: _omitted, ...withoutField } = OPUS;
    expect(() => ModelDescriptorSchema.parse(withoutField)).toThrow();
  });

  it("spells 'unknown effort axis' as descriptor-ABSENCE, not an absent key (#336)", () => {
    // The third state still exists — it just lives one level up. A local
    // ollama/lm-studio model or an unregistered id has no descriptor at all,
    // and that `undefined` is what consumers branch on.
    expect(getModelDescriptor("qwen3-coder:32b")).toBeUndefined();
    expect(getModelDescriptor("claude-haiku-4-5-20251001")?.supported_efforts).toEqual([]);
  });

  it("parses a valid EvalTask", () => {
    expect(EvalTaskSchema.parse(TASK)).toEqual(TASK);
  });

  it("parses a full EvalRun with one cell", () => {
    const run: EvalRun = {
      schema_version: MODEL_EVAL_SCHEMA_VERSION,
      run_id: "run-1",
      timestamp: TS,
      mode: "mock",
      suite: "smoke",
      tasks: [TASK.id],
      matrix: [
        { model_id: OPUS.id, effort: "high", reasoning: "high", prompt_variant: "baseline" },
      ],
      models: [OPUS],
      cells: [
        {
          task_id: TASK.id,
          job_class: "ui-creation",
          stage: "feature-dev",
          cell: {
            model_id: OPUS.id,
            effort: "high",
            reasoning: "high",
            prompt_variant: "baseline",
          },
          model_id: OPUS.id,
          model_version_label: "Opus 4.8",
          verdict: "pass",
          tokens: { input: 1000, output: 500, cache_read: 0, cache_creation: 0 },
          cost_usd: 0.0175,
          latency_ms: 42000,
          attempts_to_green: 1,
          gate_results: [{ name: "build", passed: true }],
          score: {
            composite: 88,
            correctness: 100,
            dimensions: [{ dimension: "ux_quality", score: 80, weight: 0.5 }],
            judge_used: true,
          },
        },
      ],
      summary: { total: 1, passed: 1, failed: 0, errored: 0, skipped: 0, total_cost_usd: 0.0175 },
    };
    expect(EvalRunSchema.parse(run)).toEqual(run);
  });
});

describe("model-eval schemas — honest-cell versioning (#571)", () => {
  const record = {
    task_id: TASK.id,
    job_class: "ui-creation",
    cell: { model_id: OPUS.id, effort: "high", reasoning: "high", prompt_variant: "baseline" },
    model_id: OPUS.id,
    model_version_label: "Opus 4.8",
    verdict: "pass",
    tokens: { input: 1, output: 1 },
    cost_usd: 0.01,
    latency_ms: 10,
    attempts_to_green: 1,
    gate_results: [],
    schema_version: MODEL_EVAL_SCHEMA_VERSION,
    run_id: "run-1",
    suite: "smoke",
    timestamp: TS,
    mode: "live",
  };

  it("the current schema version is 3 — effort/thinking are actually applied from v3 on", () => {
    expect(MODEL_EVAL_SCHEMA_VERSION).toBe("3");
    // The honest-aggregation floor every pooling path filters by: the current
    // version must never fall below it, or the writer would emit rows its own
    // aggregators refuse.
    expect(Number(MODEL_EVAL_SCHEMA_VERSION)).toBeGreaterThanOrEqual(MIN_HONEST_SCHEMA_VERSION);
  });

  it("rejects pre-fix (v2) records — their effort labels were never applied", () => {
    expect(() => ModelEvalRecordSchema.parse({ ...record, schema_version: "2" })).toThrow();
  });

  it("parses a skipped cell with its skip_reason", () => {
    const skipped = ModelEvalCellResultSchema.parse({
      task_id: "t",
      job_class: "bugfix",
      cell: {
        model_id: "claude-haiku-4-5-20251001",
        effort: "high",
        reasoning: "none",
        prompt_variant: "baseline",
      },
      model_id: "claude-haiku-4-5-20251001",
      model_version_label: "claude-haiku-4-5-20251001",
      verdict: "skipped",
      skip_reason: "model declares no effort axis (supported_efforts: [])",
      tokens: { input: 0, output: 0 },
      cost_usd: 0,
      latency_ms: 0,
      attempts_to_green: 0,
      gate_results: [],
    });
    expect(skipped.verdict).toBe("skipped");
    expect(skipped.skip_reason).toMatch(/no effort axis/);
  });

  it("widens the verdict vocabulary ONLY in this lane — the skill-eval lane stays untouched", () => {
    expect([...ModelEvalVerdictSchema.options]).toEqual(["pass", "fail", "error", "skipped"]);
    // The shared skill-eval verdict must not learn "skipped".
    expect(() => EvalVerdictSchema.parse("skipped")).toThrow();
  });
});

describe("model-eval schemas — strict (reject unknown keys)", () => {
  it("rejects unknown keys on ModelDescriptor", () => {
    expect(() => ModelDescriptorSchema.parse({ ...OPUS, sneaky: true })).toThrow();
  });

  it("rejects unknown keys on EvalTask", () => {
    expect(() => EvalTaskSchema.parse({ ...TASK, extra: 1 })).toThrow();
  });

  it("rejects unknown keys on EvalScore", () => {
    expect(() =>
      EvalScoreSchema.parse({
        composite: 50,
        correctness: 50,
        dimensions: [],
        judge_used: false,
        x: 1,
      })
    ).toThrow();
  });
});

describe("model-eval schemas — orthogonal axis fields (#578)", () => {
  it("pins the closed transport and provenance vocabularies", () => {
    expect([...TRANSPORTS]).toEqual(["cli", "api"]);
    expect([...RATE_PROVENANCES]).toEqual(["measured", "list", "subscription", "placeholder"]);
  });

  it("accepts the minimal transport fact — served alone", () => {
    expect(TransportFactsSchema.parse({ served: false })).toEqual({ served: false });
  });

  it("accepts the full measured shape and rejects a malformed verified date", () => {
    const full = {
      served: true,
      verified: "2026-08-15",
      evidence: "grok models catalog listing, grok CLI 1.0.4",
      rates: { input: 0.34, output: 1.02 },
      rate_provenance: "measured",
    };
    expect(TransportFactsSchema.parse(full)).toEqual(full);
    expect(() => TransportFactsSchema.parse({ served: true, verified: "Aug 15" })).toThrow();
  });

  it("is strict: unknown transport-fact keys are rejected", () => {
    expect(() => TransportFactsSchema.parse({ served: true, latency_ms: 40 })).toThrow();
  });

  it("rejects a provenance outside the closed set", () => {
    expect(() => TransportFactsSchema.parse({ served: true, rate_provenance: "vendor" })).toThrow();
  });

  it("accepts descriptor transports keyed cli/api and rejects other keys", () => {
    const withTransports = {
      ...OPUS,
      transports: { cli: { served: true }, api: { served: true } },
    };
    expect(() => ModelDescriptorSchema.parse(withTransports)).not.toThrow();
    // `sdk` folds into `api` (spike #568 §2.1) — it must not be a key of its own.
    expect(() =>
      ModelDescriptorSchema.parse({ ...OPUS, transports: { sdk: { served: true } } })
    ).toThrow();
    // Partial by design: a cli-only map is a model whose api fact is unexpressed.
    expect(() =>
      ModelDescriptorSchema.parse({ ...OPUS, transports: { cli: { served: true } } })
    ).not.toThrow();
  });

  it("accepts descriptor rate_provenance from the closed set only", () => {
    expect(() => ModelDescriptorSchema.parse({ ...OPUS, rate_provenance: "list" })).not.toThrow();
    expect(() => ModelDescriptorSchema.parse({ ...OPUS, rate_provenance: "fixture" })).toThrow();
  });

  it("keeps both fields optional — a pre-#578 descriptor still parses", () => {
    // Additive phase: nothing forces the axes yet (#579 does), so historical
    // eval-run snapshots with axis-less descriptors must keep parsing.
    expect(() => ModelDescriptorSchema.parse(OPUS)).not.toThrow();
  });
});

describe("model-eval schemas — defaults & bounds", () => {
  it("CheckCommand.expect_exit_code defaults to 0", () => {
    expect(
      CheckCommandSchema.parse({ name: "build", command: "npm run build" }).expect_exit_code
    ).toBe(0);
  });

  it("TokenUsage cache fields default to 0", () => {
    const t = TokenUsageSchema.parse({ input: 10, output: 5 });
    expect(t.cache_read).toBe(0);
    expect(t.cache_creation).toBe(0);
  });

  it("EvalScore.composite must be within 0–100", () => {
    expect(() =>
      EvalScoreSchema.parse({ composite: 101, correctness: 50, dimensions: [], judge_used: false })
    ).toThrow();
  });

  it("ModelEvalCellResult rejects negative cost", () => {
    expect(() =>
      ModelEvalCellResultSchema.parse({
        task_id: "t",
        job_class: "bugfix",
        cell: { model_id: "m", effort: "low", reasoning: "none", prompt_variant: "baseline" },
        model_id: "m",
        model_version_label: "M",
        verdict: "fail",
        tokens: { input: 1, output: 1 },
        cost_usd: -1,
        latency_ms: 100,
        attempts_to_green: 0,
        gate_results: [],
      })
    ).toThrow();
  });

  it("rejects a task id that is not kebab-case", () => {
    expect(() => EvalTaskSchema.parse({ ...TASK, id: "Not Kebab" })).toThrow();
  });

  it("ModelEvalRecord extends a cell with run-level stamps", () => {
    const rec = ModelEvalRecordSchema.parse({
      task_id: TASK.id,
      job_class: "ui-creation",
      cell: { model_id: OPUS.id, effort: "high", reasoning: "high", prompt_variant: "baseline" },
      model_id: OPUS.id,
      model_version_label: "Opus 4.8",
      verdict: "pass",
      tokens: { input: 1, output: 1 },
      cost_usd: 0.01,
      latency_ms: 10,
      attempts_to_green: 1,
      gate_results: [],
      schema_version: MODEL_EVAL_SCHEMA_VERSION,
      run_id: "run-1",
      suite: "smoke",
      timestamp: TS,
      mode: "live",
    });
    expect(rec.run_id).toBe("run-1");
    expect(rec.suite).toBe("smoke");
  });

  it("cell.prompt_variant defaults to baseline so pre-v2 records still parse (#72)", () => {
    // A v1-era cell without the field — the default must fill it, not reject.
    const cell = EvalMatrixCellSchema.parse({
      model_id: "claude-sonnet-5",
      effort: "high",
      reasoning: "none",
    });
    expect(cell.prompt_variant).toBe("baseline");
  });

  it("cell carries an explicit prompt_variant and stays strict (#72)", () => {
    const cell = EvalMatrixCellSchema.parse({
      model_id: "claude-sonnet-5",
      effort: "high",
      reasoning: "none",
      prompt_variant: "concise-preamble",
    });
    expect(cell.prompt_variant).toBe("concise-preamble");
    expect(() =>
      EvalMatrixCellSchema.parse({
        model_id: "m",
        effort: "high",
        reasoning: "none",
        prompt_variant: "x",
        unknown_axis: "nope",
      })
    ).toThrow();
  });
});
