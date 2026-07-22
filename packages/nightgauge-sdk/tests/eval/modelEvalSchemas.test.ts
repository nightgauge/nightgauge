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
  EFFORT_LEVELS,
  REASONING_LEVELS,
  ModelDescriptorSchema,
  EvalTaskSchema,
  EvalScoreSchema,
  EvalMatrixCellSchema,
  ModelEvalCellResultSchema,
  EvalRunSchema,
  ModelEvalRecordSchema,
  CheckCommandSchema,
  TokenUsageSchema,
  type ModelDescriptor,
  type EvalTask,
  type EvalRun,
} from "../../src/eval/modelEvalSchemas.js";
import { MODEL_TIERS } from "../../src/eval/schemas.js";

const TS = "2026-06-30T12:00:00.000Z";

const OPUS: ModelDescriptor = {
  id: "claude-opus-4-8",
  provider: "anthropic",
  tiers: ["opus"],
  display_name: "Opus 4.8",
  concrete_version: "claude-opus-4-8",
  rates: { input: 5, output: 25, cache_read: 0.5, cache_creation: 6.25 },
  supported_efforts: ["low", "medium", "high"],
  supported_reasoning: ["none", "low", "medium", "high"],
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
  it("EFFORT_LEVELS matches the ClaudeEffort union exactly", () => {
    expect([...EFFORT_LEVELS]).toEqual(["low", "medium", "high", "xhigh"]);
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
      supported_reasoning: ["none", "high"],
      context_window: 128000,
    };
    expect(ModelDescriptorSchema.parse(other)).toEqual(other);
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
      summary: { total: 1, passed: 1, failed: 0, errored: 0, total_cost_usd: 0.0175 },
    };
    expect(EvalRunSchema.parse(run)).toEqual(run);
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
