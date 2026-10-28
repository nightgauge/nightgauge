#!/usr/bin/env npx tsx
/**
 * Whole-pipeline effort sweep over the pinned control issue (Issue #2118).
 *
 * Runs the control task once per supported effort of one model, sequentially,
 * and writes one JSONL record per cell (cost, wall time, attempts, objective
 * pass/fail, score) to the model-eval records store. Usually invoked through
 * `nightgauge eval sweep`.
 *
 * Usage:
 *   npx tsx scripts/eval-sweep.ts --model claude-opus-4-8 --control [--efforts low,high]
 *     [--max-usd 20] [--estimate-usd 5] [--cell-timeout-min 90] [--mode stub|live] [--out path]
 *
 * Exit codes: 0 every requested cell ran; 1 spend cap reached or a setup
 * error; 2 invalid arguments.
 *
 * @see docs/MODEL_EVALUATION.md - "Whole-pipeline effort sweep"
 */

import { fileURLToPath } from "node:url";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import {
  DEFAULT_MODEL_EVAL_RECORDS_DIR,
  ModelEvalRunner,
  PipelineCellExecutor,
  WorktreeWorkspaceProvider,
  getModelDescriptor,
  loadEvalTasks,
  refusePipelineRun,
  resolveSweepEfforts,
  serializeEvalRun,
  verifyControlIssue,
  type CellExecution,
  type EvalCellExecutor,
  type EvalRun,
  type WorkspaceProvider,
} from "../packages/nightgauge-sdk/src/eval/index.js";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

interface SweepArgs {
  model: string;
  control: boolean;
  efforts?: string[];
  maxUsd?: number;
  estimateUsd: number;
  cellTimeoutMin: number;
  mode: "stub" | "live";
  tasksDir: string;
  out: string;
}

class UsageError extends Error {}

function parseArgs(argv: string[]): SweepArgs {
  const get = (flag: string): string | undefined => {
    const i = argv.indexOf(flag);
    return i >= 0 ? argv[i + 1] : undefined;
  };
  const num = (flag: string): number | undefined => {
    const v = get(flag);
    if (v === undefined) return undefined;
    const n = Number(v);
    if (!Number.isFinite(n) || n < 0) throw new UsageError(`${flag} must be a non-negative number`);
    return n;
  };
  const model = get("--model");
  if (!model) throw new UsageError("--model is required");
  const mode = get("--mode") ?? "stub";
  if (mode !== "stub" && mode !== "live") throw new UsageError("--mode must be stub or live");
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  return {
    model,
    control: argv.includes("--control"),
    efforts: get("--efforts")
      ?.split(",")
      .map((s) => s.trim())
      .filter(Boolean),
    maxUsd: num("--max-usd"),
    estimateUsd: num("--estimate-usd") ?? 5,
    cellTimeoutMin: num("--cell-timeout-min") ?? 90,
    mode,
    tasksDir: get("--tasks-dir") ?? "evals/tasks",
    out: get("--out") ?? path.join(DEFAULT_MODEL_EVAL_RECORDS_DIR, `sweep-${stamp}.jsonl`),
  };
}

/**
 * Stub mode: no model, no pipeline. Each cell reports the estimate as its cost
 * so the sweep loop, spend cap and record format can be exercised for free.
 */
function stubExecutor(costUsd: number): EvalCellExecutor {
  return {
    async execute(_task, cell): Promise<CellExecution> {
      return {
        verdict: "pass",
        tokens: { input: 0, output: 0, cache_read: 0, cache_creation: 0 },
        cost_usd: costUsd,
        latency_ms: 0,
        attempts_to_green: 1,
        gate_results: [],
        model_version_label: cell.model_id,
        sweep: { wall_clock_seconds: 0, objective_passed: true, timed_out: false },
      };
    },
  };
}

const tempWorkspaces: WorkspaceProvider = {
  async acquire() {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "nightgauge-sweep-stub-"));
    return { dir, dispose: () => fs.rm(dir, { recursive: true, force: true }) };
  },
};

function formatTable(run: EvalRun): string {
  const rows = ["effort   verdict  objective  cost_usd   wall_s  attempts"];
  for (const c of run.cells) {
    rows.push(
      [
        c.cell.effort.padEnd(8),
        c.verdict.padEnd(8),
        String(c.objective_passed ?? "-").padEnd(10),
        c.cost_usd.toFixed(4).padStart(9),
        String(Math.round(c.wall_clock_seconds ?? 0)).padStart(7),
        String(c.attempts_to_green).padStart(9),
      ].join(" ")
    );
  }
  return rows.join("\n");
}

async function main(): Promise<number> {
  let args: SweepArgs;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (err) {
    console.error(`eval-sweep: ${(err as Error).message}`);
    return 2;
  }
  if (!args.control) {
    console.error(
      "eval-sweep: --control is required (the sweep only runs the pinned control task)"
    );
    return 2;
  }
  let efforts: string[];
  try {
    efforts = resolveSweepEfforts(args.model, args.efforts);
  } catch (err) {
    console.error(`eval-sweep: ${(err as Error).message}`);
    return 2;
  }

  const tasks = await loadEvalTasks(path.resolve(REPO_ROOT, args.tasksDir));
  const control = tasks.find((t) => t.control);
  if (!control?.source_issue) {
    console.error(
      `eval-sweep: no control task registered under ${args.tasksDir} (evals/tasks/README.md § Control task)`
    );
    return 1;
  }

  const runId = `sweep-${new Date().toISOString().replace(/[:.]/g, "-")}`;
  let executor: EvalCellExecutor;
  let workspaces: WorkspaceProvider;
  let controlHash = control.source_issue.content_sha256;
  if (args.mode === "live") {
    controlHash = await verifyControlIssue(control);
    // The executor refuses to start a live cell until `nightgauge run` can stop
    // before pr-merge (see refusePipelineRun). Fail before any clone is made.
    try {
      await refusePipelineRun({ cwd: REPO_ROOT, issueNumber: 0, repo: "", timeoutMs: 0 });
    } catch (err) {
      console.error(`eval-sweep: ${(err as Error).message}`);
      return 1;
    }
    executor = new PipelineCellExecutor({
      run: refusePipelineRun,
      timeoutMs: args.cellTimeoutMin * 60_000,
    });
    workspaces = new WorktreeWorkspaceProvider({ repoRoot: REPO_ROOT, isolation: "clone", runId });
  } else {
    executor = stubExecutor(args.estimateUsd);
    workspaces = tempWorkspaces;
  }

  const run = await new ModelEvalRunner(executor, workspaces).sweep({
    suite: "effort-sweep",
    runId,
    timestamp: new Date().toISOString(),
    mode: args.mode === "live" ? "live" : "mock",
    task: control,
    modelId: args.model,
    efforts,
    maxUsd: args.maxUsd,
    estimateCellUsd: () => args.estimateUsd,
    controlHash,
    models: [getModelDescriptor(args.model)!],
    scoringBaseline: { attempts: 1, latencyMs: 3_600_000, costUsd: args.estimateUsd },
  });

  const outPath = path.resolve(REPO_ROOT, args.out);
  await fs.mkdir(path.dirname(outPath), { recursive: true });
  if (run.cells.length > 0) await fs.writeFile(outPath, serializeEvalRun(run) + "\n", "utf-8");

  console.log(formatTable(run));
  const written = run.cells.length > 0 ? `records → ${args.out}` : "no records written";
  console.log(`\nTotal cost: $${run.summary.total_cost_usd.toFixed(4)} | ${written}`);
  const cap = run.summary.cap_reached;
  if (cap) {
    console.error(
      `spend cap reached: $${cap.projected_usd.toFixed(2)} projected > $${cap.max_usd.toFixed(2)} cap; ` +
        `ran ${run.cells.length} of ${efforts.length} cells`
    );
    return 1;
  }
  return 0;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error(err instanceof Error ? (err.stack ?? err.message) : String(err));
    process.exit(1);
  });
