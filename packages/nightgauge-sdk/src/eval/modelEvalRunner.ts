/**
 * Model-eval matrix runner (Issue #4171).
 *
 * Runs each realistic task (S3) once per `{model × effort × reasoning}` cell,
 * each in an **isolated, seeded workspace**, and assembles an `EvalRun` (S1).
 * Cost is computed from the single-source registry (S2). Pure orchestration:
 * the two side-effecting boundaries — workspace isolation and cell execution —
 * are injected, so the matrix/concurrency/assembly logic is fully unit-testable
 * (mirrors the SkillEvalHarness injected-runner pattern).
 *
 * The real bindings (git-worktree provider + adapter-spawning executor) are thin
 * adapters over the existing pipeline worktree isolation and StageRunner; the
 * CLI (S7, #4174) wires them. Attempts-to-green and gate results are surfaced by
 * the executor and formalized by the telemetry work in S6 (#4172).
 *
 * @see docs/decisions/011-model-eval-system.md
 */

import { computeCostUsd } from "./modelRegistry.js";
import {
  MODEL_EVAL_SCHEMA_VERSION,
  type EvalMatrixCell,
  type EvalRun,
  type EvalTask,
  type GateResult,
  type ModelDescriptor,
  type ModelEvalCellResult,
  type QualityDimensionName,
  type TokenUsage,
} from "./modelEvalSchemas.js";
import { scoreCell, type AutomatedBaseline, type EvalJudgeVerdict } from "./qualityScorer.js";
import type { EvalVerdict } from "./schemas.js";

/** An isolated, seeded working copy for one cell. Disposed after the cell runs. */
export interface EvalWorkspace {
  /** Absolute path to the isolated working directory (a git worktree in prod). */
  readonly dir: string;
  /** Tear down the workspace (remove the worktree). Must be idempotent. */
  dispose(): Promise<void>;
}

/**
 * Creates an isolated workspace seeded to a task's fixture state. In production
 * this creates a git worktree and runs the task's `fixture` scaffold; in tests
 * it is mocked. The runner acquires one per cell so every model starts identical.
 */
export interface WorkspaceProvider {
  acquire(task: EvalTask, cell: EvalMatrixCell): Promise<EvalWorkspace>;
}

/** Raw outcome of executing one task under one cell (before cost is priced in). */
export interface CellExecution {
  verdict: EvalVerdict;
  tokens: TokenUsage;
  latency_ms: number;
  /** Ralph iterations + retries + escalations until green (S6 formalizes capture). */
  attempts_to_green: number;
  gate_results: GateResult[];
  /** Concrete version label recorded for interpretation. */
  model_version_label: string;
  /** The stage exercised, when a single stage was targeted. */
  stage?: ModelEvalCellResult["stage"];
  error?: string;
  /**
   * Subjective judge verdict, produced by the executor while the workspace is
   * still alive (an LLM judge must inspect the produced work). Transient: the
   * runner folds it into `score` and it is not persisted raw. Absent when no
   * judge ran (mock, or live without `--judge`).
   */
  judge?: {
    verdict: EvalJudgeVerdict;
    /** Dimensions the reliability guard flagged as unstable. */
    lowConfidence: QualityDimensionName[];
  };
}

/**
 * Executes one task under one cell inside a prepared workspace and returns raw
 * telemetry. This is the boundary to the real pipeline (adapter spawn + gates);
 * it must honor `cell.effort` and `cell.reasoning`. Mocked in tests.
 */
export interface EvalCellExecutor {
  execute(task: EvalTask, cell: EvalMatrixCell, workspace: EvalWorkspace): Promise<CellExecution>;
}

export interface ModelEvalRunOptions {
  /** Suite name (a named set of tasks + matrix). */
  suite: string;
  /** Stable run id (injected; never generated in a pure fn). */
  runId: string;
  /** ISO-8601 timestamp injected by the caller. */
  timestamp: string;
  /** `mock` (deterministic executor) or `live`. */
  mode: "mock" | "live";
  /** Tasks to evaluate (already loaded + validated). */
  tasks: EvalTask[];
  /** The `{model × effort × reasoning}` matrix to run every task under. */
  matrix: EvalMatrixCell[];
  /** Snapshot of the descriptors used — persisted so historical cost stays interpretable. */
  models: ModelDescriptor[];
  /** Max concurrent cells (default 4). Cells are isolated so this is safe. */
  concurrency?: number;
  /**
   * When set, each cell is scored (S5) against this baseline, folding in the
   * executor's judge verdict when present. Omit to leave `cell.score` unset.
   */
  scoringBaseline?: AutomatedBaseline;
}

/**
 * Orchestrates a model-eval run: (task × cell) fan-out, isolated workspace per
 * cell, cost pricing via the registry, per-cell error isolation, and EvalRun
 * assembly. No clock/process/filesystem dependency of its own.
 */
export class ModelEvalRunner {
  constructor(
    private readonly executor: EvalCellExecutor,
    private readonly workspaces: WorkspaceProvider
  ) {}

  async run(options: ModelEvalRunOptions): Promise<EvalRun> {
    const { tasks, matrix } = options;
    const pairs: Array<{ task: EvalTask; cell: EvalMatrixCell }> = [];
    for (const task of tasks) {
      for (const cell of matrix) pairs.push({ task, cell });
    }

    const cells = await mapWithConcurrency(pairs, options.concurrency ?? 4, ({ task, cell }) =>
      this.runCell(task, cell, options.scoringBaseline)
    );

    const total_cost_usd = round6(cells.reduce((s, c) => s + c.cost_usd, 0));
    return {
      schema_version: MODEL_EVAL_SCHEMA_VERSION,
      run_id: options.runId,
      timestamp: options.timestamp,
      mode: options.mode,
      suite: options.suite,
      tasks: tasks.map((t) => t.id),
      matrix,
      models: options.models,
      cells,
      summary: {
        total: cells.length,
        passed: cells.filter((c) => c.verdict === "pass").length,
        failed: cells.filter((c) => c.verdict === "fail").length,
        errored: cells.filter((c) => c.verdict === "error").length,
        total_cost_usd,
      },
    };
  }

  private async runCell(
    task: EvalTask,
    cell: EvalMatrixCell,
    baseline?: AutomatedBaseline
  ): Promise<ModelEvalCellResult> {
    const base = {
      task_id: task.id,
      job_class: task.job_class,
      cell,
      model_id: cell.model_id,
    } as const;

    let workspace: EvalWorkspace | undefined;
    try {
      workspace = await this.workspaces.acquire(task, cell);
      const exec = await this.executor.execute(task, cell, workspace);
      // computeCostUsd expects camelCase cache fields; `exec.tokens` is the
      // snake_case TokenUsage shape. Normalize so cache_read/cache_creation are
      // actually billed — otherwise cache (which dominates real agentic cost) is
      // silently priced at 0. @see modelRegistry "bills cache tokens" test.
      const cost_usd = round6(
        computeCostUsd(cell.model_id, {
          input: exec.tokens.input,
          output: exec.tokens.output,
          cacheRead: exec.tokens.cache_read,
          cacheCreation: exec.tokens.cache_creation,
        })
      );
      return {
        ...base,
        stage: exec.stage,
        model_version_label: exec.model_version_label,
        verdict: exec.verdict,
        tokens: exec.tokens,
        cost_usd,
        latency_ms: exec.latency_ms,
        attempts_to_green: exec.attempts_to_green,
        gate_results: exec.gate_results,
        error: exec.error,
        // Score inline (S5) while the executor's judge verdict is in hand — a live
        // LLM judge must inspect the workspace, which is disposed after this cell.
        ...(baseline
          ? {
              score: scoreCell({
                jobClass: task.job_class,
                verdict: exec.verdict,
                gates: exec.gate_results,
                metrics: {
                  attemptsToGreen: exec.attempts_to_green,
                  latencyMs: exec.latency_ms,
                  costUsd: cost_usd,
                },
                baseline,
                judge: exec.judge
                  ? {
                      verdict: exec.judge.verdict,
                      rubric: task.rubric,
                      lowConfidence: new Set(exec.judge.lowConfidence),
                    }
                  : undefined,
              }),
            }
          : {}),
      };
    } catch (err) {
      // A failed cell is recorded, never fatal to the run.
      return {
        ...base,
        model_version_label: cell.model_id,
        verdict: "error",
        tokens: { input: 0, output: 0, cache_read: 0, cache_creation: 0 },
        cost_usd: 0,
        latency_ms: 0,
        attempts_to_green: 0,
        gate_results: [],
        error: err instanceof Error ? err.message : String(err),
      };
    } finally {
      // Always tear down the isolated workspace.
      if (workspace) await workspace.dispose().catch(() => {});
    }
  }
}

/** Round to 6 decimals to keep cost sums stable across floating-point noise. */
function round6(n: number): number {
  return Math.round(n * 1e6) / 1e6;
}

/**
 * Run `fn` over `items` with at most `limit` in flight, preserving input order in
 * the result. A rejecting `fn` rejects the whole map — callers that need per-item
 * isolation (like the runner) catch inside `fn`.
 */
export async function mapWithConcurrency<T, R>(
  items: T[],
  limit: number,
  fn: (item: T, index: number) => Promise<R>
): Promise<R[]> {
  const results = new Array<R>(items.length);
  let next = 0;
  const workers = Array.from({ length: Math.max(1, Math.min(limit, items.length)) }, async () => {
    while (true) {
      const i = next++;
      if (i >= items.length) return;
      results[i] = await fn(items[i], i);
    }
  });
  await Promise.all(workers);
  return results;
}
