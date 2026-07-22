/**
 * Tests for the model-eval matrix runner (Issue #4171). Pure orchestration with
 * mocked workspace + executor boundaries — no worktrees or model calls.
 */

import { describe, it, expect } from "vitest";
import {
  ModelEvalRunner,
  mapWithConcurrency,
  type EvalCellExecutor,
  type WorkspaceProvider,
  type CellExecution,
} from "../../src/eval/modelEvalRunner.js";
import { computeCostUsd } from "../../src/eval/modelRegistry.js";
import type { EvalMatrixCell, EvalTask, ModelDescriptor } from "../../src/eval/modelEvalSchemas.js";

const TASK = (id: string): EvalTask => ({
  id,
  title: id,
  job_class: "backend-logic",
  target_stages: ["feature-dev"],
  difficulty: "medium",
  instruction: "do it",
  fixture: { kind: "scaffold-script", ref: `evals/fixtures/${id}/setup.sh` },
  checks: [{ name: "test", command: "npm test", expect_exit_code: 0 }],
  rubric: { criteria: [{ dimension: "correctness", weight: 1, guidance: "?" }] },
});

const CELL = (model_id: string, effort: "low" | "medium" | "high"): EvalMatrixCell => ({
  model_id,
  effort,
  reasoning: "none",
  prompt_variant: "baseline",
});

const MODELS: ModelDescriptor[] = [];

/** Mock provider that records acquire/dispose per cell. */
function trackingProvider() {
  const acquired: string[] = [];
  let disposed = 0;
  const provider: WorkspaceProvider = {
    async acquire(task, cell) {
      acquired.push(`${task.id}:${cell.model_id}:${cell.effort}`);
      return { dir: `/tmp/ws/${task.id}-${cell.model_id}`, dispose: async () => void disposed++ };
    },
  };
  return { provider, acquired, disposedCount: () => disposed };
}

/** Mock executor: deterministic telemetry; passes effort through into tokens. */
const okExecutor: EvalCellExecutor = {
  async execute(_task, cell): Promise<CellExecution> {
    const outFactor = cell.effort === "high" ? 3 : cell.effort === "medium" ? 2 : 1;
    return {
      verdict: "pass",
      tokens: { input: 1_000_000, output: 1_000_000 * outFactor, cache_read: 0, cache_creation: 0 },
      latency_ms: 1000 * outFactor,
      attempts_to_green: 1,
      gate_results: [{ name: "test", passed: true }],
      model_version_label: cell.model_id,
      stage: "feature-dev",
    };
  },
};

describe("ModelEvalRunner", () => {
  it("expands the task × matrix and prices cost from the registry", async () => {
    const { provider, acquired, disposedCount } = trackingProvider();
    const runner = new ModelEvalRunner(okExecutor, provider);
    const matrix = [CELL("claude-opus-4-8", "low"), CELL("claude-haiku-4-5-20251001", "high")];
    const run = await runner.run({
      suite: "smoke",
      runId: "r1",
      timestamp: "2026-06-30T00:00:00.000Z",
      mode: "mock",
      tasks: [TASK("a"), TASK("b")],
      matrix,
      models: MODELS,
    });

    expect(run.cells).toHaveLength(4); // 2 tasks × 2 cells
    expect(run.summary).toMatchObject({ total: 4, passed: 4, failed: 0, errored: 0 });
    expect(acquired).toHaveLength(4);
    expect(disposedCount()).toBe(4); // one workspace acquired AND disposed per cell

    // Opus low effort: 1M in + 1M out → 5 + 25 = $30.
    const opus = run.cells.find((c) => c.cell.model_id === "claude-opus-4-8")!;
    expect(opus.cost_usd).toBeCloseTo(computeCostUsd("claude-opus-4-8", opus.tokens), 6);
    expect(opus.cost_usd).toBeCloseTo(30, 6);

    // total_cost_usd is the sum of all cell costs.
    const manual = run.cells.reduce((s, c) => s + c.cost_usd, 0);
    expect(run.summary.total_cost_usd).toBeCloseTo(manual, 6);
  });

  it("bills cache tokens in cost (snake_case TokenUsage is normalized for pricing)", async () => {
    const { provider } = trackingProvider();
    // A cell with heavy cache_read — the shape a real agentic run produces.
    const cacheHeavy: EvalCellExecutor = {
      async execute(_task, cell) {
        return {
          verdict: "pass",
          tokens: { input: 1000, output: 1000, cache_read: 1_000_000, cache_creation: 1_000_000 },
          latency_ms: 1000,
          attempts_to_green: 1,
          gate_results: [{ name: "test", passed: true }],
          model_version_label: cell.model_id,
          stage: "feature-dev",
        };
      },
    };
    const runner = new ModelEvalRunner(cacheHeavy, provider);
    const run = await runner.run({
      suite: "s",
      runId: "r",
      timestamp: "t",
      mode: "mock",
      tasks: [TASK("a")],
      matrix: [CELL("claude-opus-4-8", "low")],
      models: MODELS,
    });
    const cell = run.cells[0];
    // opus: input 5, output 25, cache_read 0.5, cache_creation 6.25 (per MTok).
    // (1000·5 + 1000·25 + 1e6·0.5 + 1e6·6.25) / 1e6 = 0.03 + 0.5 + 6.25 = 6.78.
    expect(cell.cost_usd).toBeCloseTo(6.78, 6);
    // Guard against regression to the cache-dropped price (0.03).
    expect(cell.cost_usd).toBeGreaterThan(1);
  });

  it("isolates a failing cell without failing the run", async () => {
    const { provider } = trackingProvider();
    const flaky: EvalCellExecutor = {
      async execute(_task, cell) {
        if (cell.model_id === "boom") throw new Error("executor exploded");
        return okExecutor.execute(_task, cell, { dir: "x", dispose: async () => {} });
      },
    };
    const runner = new ModelEvalRunner(flaky, provider);
    const run = await runner.run({
      suite: "s",
      runId: "r",
      timestamp: "t",
      mode: "mock",
      tasks: [TASK("a")],
      matrix: [CELL("claude-opus-4-8", "low"), CELL("boom", "low")],
      models: MODELS,
    });
    expect(run.summary).toMatchObject({ total: 2, passed: 1, errored: 1 });
    const errored = run.cells.find((c) => c.cell.model_id === "boom")!;
    expect(errored.verdict).toBe("error");
    expect(errored.error).toMatch(/exploded/);
    expect(errored.cost_usd).toBe(0);
  });

  it("passes effort/reasoning through to the executor (distinct outputs per cell)", async () => {
    const { provider } = trackingProvider();
    const runner = new ModelEvalRunner(okExecutor, provider);
    const run = await runner.run({
      suite: "s",
      runId: "r",
      timestamp: "t",
      mode: "mock",
      tasks: [TASK("a")],
      matrix: [CELL("claude-sonnet-5", "low"), CELL("claude-sonnet-5", "high")],
      models: MODELS,
    });
    const low = run.cells.find((c) => c.cell.effort === "low")!;
    const high = run.cells.find((c) => c.cell.effort === "high")!;
    expect(high.tokens.output).toBeGreaterThan(low.tokens.output);
    expect(high.latency_ms).toBeGreaterThan(low.latency_ms);
  });

  it("scores each cell (deterministic-only) when a scoringBaseline is given", async () => {
    const { provider } = trackingProvider();
    const runner = new ModelEvalRunner(okExecutor, provider);
    const run = await runner.run({
      suite: "s",
      runId: "r",
      timestamp: "t",
      mode: "mock",
      tasks: [TASK("a")],
      matrix: [CELL("claude-opus-4-8", "low")],
      models: MODELS,
      scoringBaseline: { attempts: 1, latencyMs: 60_000, costUsd: 100 },
    });
    const cell = run.cells[0];
    expect(cell.score).toBeDefined();
    expect(cell.score!.judge_used).toBe(false);
    expect(cell.score!.correctness).toBe(100); // the one gate passed
  });

  it("folds the executor's judge verdict into the composite (judge_used=true)", async () => {
    const { provider } = trackingProvider();
    // A ui-creation task (judge-weighted) + an executor that surfaces a judge verdict.
    const uiTask: EvalTask = {
      ...TASK("ui"),
      job_class: "ui-creation",
      rubric: { criteria: [{ dimension: "ux_quality", weight: 1, guidance: "polished?" }] },
    };
    const judged: EvalCellExecutor = {
      async execute(_t, cell) {
        return {
          verdict: "pass",
          tokens: { input: 1000, output: 1000, cache_read: 0, cache_creation: 0 },
          latency_ms: 1000,
          attempts_to_green: 1,
          gate_results: [{ name: "test", passed: true }],
          model_version_label: cell.model_id,
          stage: "feature-dev",
          judge: {
            verdict: { dimensions: [{ dimension: "ux_quality", score: 40 }] },
            lowConfidence: ["ux_quality"],
          },
        };
      },
    };
    const runner = new ModelEvalRunner(judged, provider);
    const run = await runner.run({
      suite: "s",
      runId: "r",
      timestamp: "t",
      mode: "mock",
      tasks: [uiTask],
      matrix: [CELL("claude-opus-4-8", "low")],
      models: MODELS,
      scoringBaseline: { attempts: 1, latencyMs: 60_000, costUsd: 100 },
    });
    const score = run.cells[0].score!;
    expect(score.judge_used).toBe(true);
    expect(score.low_confidence).toBe(true); // propagated from the guard
    expect(score.dimensions.map((d) => d.dimension)).toContain("ux_quality");
    // A weak judge (40) must drag the composite below the gate-only correctness (100).
    expect(score.composite).toBeLessThan(100);
  });
});

describe("mapWithConcurrency", () => {
  it("preserves order and respects the concurrency limit", async () => {
    let inFlight = 0;
    let maxInFlight = 0;
    const out = await mapWithConcurrency([1, 2, 3, 4, 5, 6], 2, async (n) => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      await Promise.resolve();
      inFlight--;
      return n * 10;
    });
    expect(out).toEqual([10, 20, 30, 40, 50, 60]);
    expect(maxInFlight).toBeLessThanOrEqual(2);
  });
});
