/**
 * Tests for the suite orchestration (Issue #4174): matrix build, end-to-end run
 * with mock executor/workspace, deterministic scoring, JSONL serialization, and
 * the comparison matrix.
 */

import { describe, it, expect } from "vitest";
import {
  buildMatrix,
  runEvalSuite,
  serializeEvalRun,
  evalRunToRecords,
  formatComparisonMatrix,
  computeVariantDeltas,
  formatVariantDeltas,
} from "../../src/eval/evalSuite.js";
import { ModelEvalRecordSchema, type EvalTask } from "../../src/eval/modelEvalSchemas.js";
import type { EvalCellExecutor, WorkspaceProvider } from "../../src/eval/modelEvalRunner.js";

const TASK: EvalTask = {
  id: "sample",
  title: "sample",
  job_class: "ui-creation",
  target_stages: ["feature-dev"],
  difficulty: "medium",
  instruction: "build",
  fixture: { kind: "scaffold-script", ref: "evals/fixtures/sample/setup.sh" },
  checks: [{ name: "test", command: "npm test", expect_exit_code: 0 }],
  rubric: { criteria: [{ dimension: "ux_quality", weight: 1, guidance: "?" }] },
};

const noopWorkspaces: WorkspaceProvider = {
  acquire: async () => ({ dir: "/tmp/x", dispose: async () => {} }),
};

const passExecutor: EvalCellExecutor = {
  async execute(_task, cell) {
    return {
      verdict: "pass",
      tokens: { input: 1_000_000, output: 1_000_000, cache_read: 0, cache_creation: 0 },
      latency_ms: 30_000,
      attempts_to_green: 1,
      gate_results: [{ name: "test", passed: true }],
      model_version_label: cell.model_id,
      stage: "feature-dev",
    };
  },
};

describe("buildMatrix", () => {
  it("is the cartesian product of models × efforts × reasoning (baseline variant implicit)", () => {
    const m = buildMatrix(["a", "b"], ["low", "high"], ["none"]);
    expect(m).toHaveLength(4);
    expect(m).toContainEqual({
      model_id: "b",
      effort: "high",
      reasoning: "none",
      prompt_variant: "baseline",
    });
  });

  it("expands the prompt-variant axis into the cartesian product (#72)", () => {
    const m = buildMatrix(["a", "b"], ["high"], ["none"], ["baseline", "concise"]);
    expect(m).toHaveLength(4);
    expect(m).toContainEqual({
      model_id: "a",
      effort: "high",
      reasoning: "none",
      prompt_variant: "concise",
    });
    expect(m).toContainEqual({
      model_id: "b",
      effort: "high",
      reasoning: "none",
      prompt_variant: "baseline",
    });
  });
});

describe("runEvalSuite", () => {
  it("runs the matrix and assembles an EvalRun", async () => {
    const run = await runEvalSuite({
      suite: "smoke",
      runId: "r1",
      timestamp: "2026-06-30T00:00:00.000Z",
      mode: "mock",
      tasks: [TASK],
      matrix: buildMatrix(["claude-opus-4-8", "claude-haiku-4-5-20251001"], ["low"], ["none"]),
      models: [],
      executor: passExecutor,
      workspaces: noopWorkspaces,
    });
    expect(run.cells).toHaveLength(2);
    expect(run.summary.passed).toBe(2);
    expect(run.summary.total_cost_usd).toBeGreaterThan(0);
  });

  it("applies deterministic scoring when a baseline is given", async () => {
    const run = await runEvalSuite({
      suite: "smoke",
      runId: "r1",
      timestamp: "t",
      mode: "mock",
      tasks: [TASK],
      matrix: buildMatrix(["claude-opus-4-8"], ["low"], ["none"]),
      models: [],
      executor: passExecutor,
      workspaces: noopWorkspaces,
      scoringBaseline: { attempts: 1, latencyMs: 60_000, costUsd: 1 },
    });
    const cell = run.cells[0];
    expect(cell.score).toBeDefined();
    expect(cell.score!.judge_used).toBe(false);
    expect(cell.score!.correctness).toBe(100); // the one gate passed
  });
});

describe("serialization", () => {
  it("produces one valid JSONL record per cell", async () => {
    const run = await runEvalSuite({
      suite: "smoke",
      runId: "r1",
      timestamp: "t",
      mode: "mock",
      tasks: [TASK],
      matrix: buildMatrix(["claude-opus-4-8", "claude-sonnet-5"], ["low"], ["none"]),
      models: [],
      executor: passExecutor,
      workspaces: noopWorkspaces,
    });
    const lines = serializeEvalRun(run).split("\n");
    expect(lines).toHaveLength(2);
    for (const line of lines) {
      expect(() => ModelEvalRecordSchema.parse(JSON.parse(line))).not.toThrow();
    }
    expect(evalRunToRecords(run)[0].run_id).toBe("r1");
  });

  it("comparison matrix has a header row and one row per model", async () => {
    const run = await runEvalSuite({
      suite: "smoke",
      runId: "r1",
      timestamp: "t",
      mode: "mock",
      tasks: [TASK],
      matrix: buildMatrix(["claude-opus-4-8", "claude-sonnet-5"], ["low"], ["none"]),
      models: [],
      executor: passExecutor,
      workspaces: noopWorkspaces,
    });
    const table = formatComparisonMatrix(run).split("\n");
    expect(table[0]).toContain("model");
    expect(table).toHaveLength(3); // header + 2 models
  });
});

// ---------------------------------------------------------------------------
// Prompt-variant deltas (#72)
// ---------------------------------------------------------------------------

/**
 * Executor whose outcome depends on the prompt variant: baseline cells pass,
 * cells under the named variant fail their check. This is the shape of a
 * REGRESSION — the overlaid prompt text makes the model do worse.
 */
const variantRegressionExecutor: EvalCellExecutor = {
  async execute(_task, cell) {
    const pass = cell.prompt_variant === "baseline";
    return {
      verdict: pass ? "pass" : "fail",
      tokens: { input: 1_000_000, output: 1_000_000, cache_read: 0, cache_creation: 0 },
      latency_ms: 30_000,
      attempts_to_green: pass ? 1 : 2,
      gate_results: [{ name: "test", passed: pass }],
      model_version_label: cell.model_id,
      stage: "feature-dev",
    };
  },
};

describe("computeVariantDeltas (#72)", () => {
  const suiteOpts = {
    suite: "smoke",
    runId: "r1",
    timestamp: "t",
    mode: "mock" as const,
    tasks: [TASK],
    models: [],
    workspaces: noopWorkspaces,
    scoringBaseline: { attempts: 1, latencyMs: 60_000, costUsd: 1 },
  };

  it("detects a variant scoring WORSE than baseline as a negative delta", async () => {
    const run = await runEvalSuite({
      ...suiteOpts,
      matrix: buildMatrix(
        ["claude-sonnet-5"],
        ["high"],
        ["none"],
        ["baseline", "verbose-preamble"]
      ),
      executor: variantRegressionExecutor,
    });

    const deltas = computeVariantDeltas(run.cells);
    expect(deltas).toHaveLength(1);
    const d = deltas[0];
    expect(d.model_id).toBe("claude-sonnet-5");
    expect(d.prompt_variant).toBe("verbose-preamble");
    // The regression is a MEASUREMENT, not just "not green": composite drops
    // and both deltas go negative.
    expect(d.delta).toBeLessThan(0);
    expect(d.pass_rate_delta).toBeLessThan(0);
    expect(d.mean_composite).toBeLessThan(d.baseline_composite);

    const table = formatVariantDeltas(deltas);
    expect(table).toContain("verbose-preamble");
    expect(table).toContain("-"); // signed negative delta rendered
  });

  it("detects an improvement symmetrically (positive delta)", async () => {
    const variantImprovesExecutor: EvalCellExecutor = {
      async execute(task, cell) {
        // Inverse of the regression executor: only the variant passes.
        const flipped = await variantRegressionExecutor.execute(
          task,
          { ...cell, prompt_variant: cell.prompt_variant === "baseline" ? "x" : "baseline" },
          { dir: "/tmp/x", dispose: async () => {} }
        );
        return flipped;
      },
    };
    const run = await runEvalSuite({
      ...suiteOpts,
      matrix: buildMatrix(["claude-sonnet-5"], ["high"], ["none"], ["baseline", "better"]),
      executor: variantImprovesExecutor,
    });

    const deltas = computeVariantDeltas(run.cells);
    expect(deltas).toHaveLength(1);
    expect(deltas[0].delta).toBeGreaterThan(0);
  });

  it("compares a variant against a baseline captured on a LATER run via concatenated records", async () => {
    // Run 1: baseline only. Run 2: variant only. The delta is computable from
    // the union of persisted records — no same-run requirement.
    const baselineRun = await runEvalSuite({
      ...suiteOpts,
      runId: "r-baseline",
      matrix: buildMatrix(["claude-sonnet-5"], ["high"], ["none"], ["baseline"]),
      executor: variantRegressionExecutor,
    });
    const variantRun = await runEvalSuite({
      ...suiteOpts,
      runId: "r-variant",
      matrix: buildMatrix(["claude-sonnet-5"], ["high"], ["none"], ["verbose-preamble"]),
      executor: variantRegressionExecutor,
    });

    const combined = [...evalRunToRecords(baselineRun), ...evalRunToRecords(variantRun)];
    const deltas = computeVariantDeltas(combined);
    expect(deltas).toHaveLength(1);
    expect(deltas[0].prompt_variant).toBe("verbose-preamble");
    expect(deltas[0].delta).toBeLessThan(0);
  });

  it("skips models with no scored baseline (a delta without a baseline is not a measurement)", async () => {
    const variantOnly = await runEvalSuite({
      ...suiteOpts,
      matrix: buildMatrix(["claude-sonnet-5"], ["high"], ["none"], ["verbose-preamble"]),
      executor: variantRegressionExecutor,
    });
    expect(computeVariantDeltas(variantOnly.cells)).toHaveLength(0);
    expect(formatVariantDeltas([])).toContain("no variant deltas");
  });
});
