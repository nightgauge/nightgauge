/**
 * Whole-pipeline cell executor for the effort sweep (Issue #2118).
 *
 * Where `LiveCellExecutor` spawns one model on one prompt, this executor runs
 * the control issue through the real pipeline (`nightgauge run <issue>`) inside
 * a clone-isolated cell, with every stage pinned to the cell's model and effort.
 *
 * Steps per cell:
 *   1. Write `.nightgauge/config.local.yaml` in the cell (uncommitted, so the
 *      base SHA and control hash are untouched): `model_routing.mode: manual`,
 *      `pipeline.stage_models.<stage>` and `model_routing.stage_efforts.<stage>`
 *      for every stage.
 *   2. Run the pipeline through the injected {@link PipelineRunFn}.
 *   3. Read the run's history record — the `record_type: "run"` line for the
 *      issue in `<pipeline state dir>/history/*.jsonl` (written by
 *      `internal/state/history.go`). Cost is the sum of
 *      `tokens.per_stage[*].cost_usd`; attempts the sum of each stage's
 *      `attempts_until_success`; the dispatched effort is each stage's
 *      `model_selection.effort` (falling back to `model_effort`).
 *   4. Fail the cell when any model-dispatching stage ran at an effort other
 *      than `cell.effort`, or recorded none — an unverifiable pin is a
 *      mislabeled measurement.
 *   5. Evaluate the control's `pass_criteria` on the run's branch.
 *
 * A timeout or failed run throws a {@link CellExecutionError} carrying the cost
 * read so far, so the runner records it rather than pricing the cell at zero.
 *
 * @see docs/MODEL_EVALUATION.md - "Whole-pipeline effort sweep"
 */

import { mkdir, readdir, readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  CellExecutionError,
  type CellExecution,
  type CellSweepTelemetry,
  type EvalCellExecutor,
  type EvalWorkspace,
} from "./modelEvalRunner.js";
import type { EvalMatrixCell, EvalTask, GateResult } from "./modelEvalSchemas.js";
import { PIPELINE_SKILLS } from "./schemas.js";
import { defaultExec, type ExecFn } from "./worktreeWorkspace.js";

/** Outcome of one `nightgauge run` in a cell. */
export interface PipelineRunResult {
  code: number;
  timedOut: boolean;
  stdout: string;
  stderr: string;
}

/** Runs the pipeline for `issueNumber` in `cwd`; injected so tests need no pipeline. */
export type PipelineRunFn = (args: {
  cwd: string;
  issueNumber: number;
  repo: string;
  timeoutMs: number;
}) => Promise<PipelineRunResult>;

/** The subset of a history run record (internal/state/history.go V2RunRecord) the sweep reads. */
export interface PipelineRunRecord {
  issue_number: number;
  branch?: string;
  outcome?: string;
  stages?: Record<
    string,
    {
      status?: string;
      model_selection?: { model?: string; effort?: string };
      model_effort?: string;
      attempts_until_success?: number;
    }
  >;
  tokens?: {
    total_input?: number;
    total_output?: number;
    total_cache_read?: number;
    total_cache_creation?: number;
    per_stage?: Record<string, { cost_usd?: number }>;
  };
}

/** Reads the latest run record for `issueNumber` from the cell, or undefined. */
export type RunRecordReader = (
  cwd: string,
  issueNumber: number
) => Promise<PipelineRunRecord | undefined>;

export interface PipelineCellExecutorOptions {
  /** Runs the pipeline. There is deliberately no default; see {@link refusePipelineRun}. */
  run: PipelineRunFn;
  /** Per-cell wall-clock timeout. */
  timeoutMs: number;
  /** Reads the run record (default: the cell's pipeline history directory). */
  readRunRecord?: RunRecordReader;
  /** Shell boundary for pass-criteria checks (default: node:child_process). */
  exec?: ExecFn;
  /** Monotonic clock in ms (injectable for tests). */
  now?: () => number;
}

/**
 * The live run function the sweep cannot yet supply. `nightgauge run <issue>`
 * runs pr-merge after pr-create and has no option to stop before it, so a live
 * cell would merge into the fixture repo. Until that option exists, live cells
 * refuse to start rather than merge.
 */
export const refusePipelineRun: PipelineRunFn = async () => {
  throw new Error(
    "live sweep cells are unavailable: `nightgauge run` has no way to stop before pr-merge, " +
      "so a cell would merge its PR into the fixture repo"
  );
};

/** The local-tier config a cell runs under: every stage pinned to one model and effort. */
export function cellConfigYaml(cell: EvalMatrixCell): string {
  const lines = [
    "# Written by the eval sweep (#2118); uncommitted, local tier.",
    "pipeline:",
    "  stage_models:",
  ];
  for (const s of PIPELINE_SKILLS) lines.push(`    ${s}: ${cell.model_id}`);
  lines.push("model_routing:", "  mode: manual", "  stage_efforts:");
  for (const s of PIPELINE_SKILLS) lines.push(`    ${s}: ${cell.effort}`);
  lines.push("human_in_the_loop:", "  auto_accept_stages: true", "");
  return lines.join("\n");
}

/** Telemetry derived from a run record. */
export interface RunTelemetry {
  cost_usd: number;
  attempts_to_green: number;
  stage_efforts: CellSweepTelemetry["stage_efforts"];
  /** Stages that dispatched a model at the wrong effort, or recorded none. */
  effort_mismatches: string[];
  tokens: CellExecution["tokens"];
}

/** Derive cost, attempts and dispatched efforts from a run record. */
export function telemetryFromRecord(record: PipelineRunRecord, effort: string): RunTelemetry {
  const stages = record.stages ?? {};
  const perStage = record.tokens?.per_stage ?? {};
  const cost = Object.values(perStage).reduce((s, t) => s + (t.cost_usd ?? 0), 0);
  const stage_efforts: Record<string, string> = {};
  const effort_mismatches: string[] = [];
  let attempts = 0;
  for (const [name, st] of Object.entries(stages)) {
    attempts += st.attempts_until_success ?? 0;
    // A stage with no model selection dispatched no model (deterministic).
    if (!st.model_selection) continue;
    const got = st.model_selection.effort || st.model_effort;
    if (got) stage_efforts[name] = got;
    if (got !== effort) effort_mismatches.push(`${name}: ${got ?? "no effort recorded"}`);
  }
  return {
    cost_usd: cost,
    attempts_to_green: attempts,
    stage_efforts: stage_efforts as RunTelemetry["stage_efforts"],
    effort_mismatches,
    tokens: {
      input: record.tokens?.total_input ?? 0,
      output: record.tokens?.total_output ?? 0,
      cache_read: record.tokens?.total_cache_read ?? 0,
      cache_creation: record.tokens?.total_cache_creation ?? 0,
    },
  };
}

export class PipelineCellExecutor implements EvalCellExecutor {
  private readonly exec: ExecFn;
  private readonly readRunRecord: RunRecordReader;
  private readonly now: () => number;

  constructor(private readonly options: PipelineCellExecutorOptions) {
    this.exec = options.exec ?? defaultExec;
    this.readRunRecord = options.readRunRecord ?? historyRunRecordReader(this.exec);
    this.now = options.now ?? (() => Date.now());
  }

  async execute(
    task: EvalTask,
    cell: EvalMatrixCell,
    workspace: EvalWorkspace
  ): Promise<CellExecution> {
    const src = task.source_issue;
    if (!src || !task.pass_criteria) {
      throw new Error(
        `task "${task.id}" is not a control task (needs source_issue and pass_criteria)`
      );
    }
    const { number: issueNumber, repo } = src;
    const dir = workspace.dir;

    await mkdir(join(dir, ".nightgauge"), { recursive: true });
    await writeFile(join(dir, ".nightgauge", "config.local.yaml"), cellConfigYaml(cell));

    const t0 = this.now();
    const res = await this.options.run({
      cwd: dir,
      issueNumber,
      repo,
      timeoutMs: this.options.timeoutMs,
    });
    const latency_ms = Math.max(0, Math.round(this.now() - t0));
    const wall_clock_seconds = latency_ms / 1000;

    const record = await this.readRunRecord(dir, issueNumber).catch(() => undefined);
    const tel = record ? telemetryFromRecord(record, cell.effort) : undefined;
    const sweep: CellSweepTelemetry = {
      wall_clock_seconds,
      timed_out: res.timedOut,
      ...(tel ? { stage_efforts: tel.stage_efforts } : {}),
    };

    if (res.timedOut || res.code !== 0) {
      const why = res.timedOut
        ? `pipeline run timed out after ${this.options.timeoutMs}ms`
        : `pipeline run exited ${res.code}: ${tailLine(res.stderr || res.stdout)}`;
      throw new CellExecutionError(why, {
        tokens: tel?.tokens,
        cost_usd: tel?.cost_usd ?? 0,
        latency_ms,
        attempts_to_green: tel?.attempts_to_green,
        sweep: { ...sweep, objective_passed: false },
      });
    }
    if (!tel) {
      throw new CellExecutionError(
        `no run record for issue #${issueNumber} in the cell's pipeline history`,
        {
          latency_ms,
          sweep: { ...sweep, objective_passed: false },
        }
      );
    }
    if (tel.effort_mismatches.length > 0) {
      throw new CellExecutionError(
        `effort pin '${cell.effort}' did not reach every stage: ${tel.effort_mismatches.join("; ")}`,
        {
          tokens: tel.tokens,
          cost_usd: tel.cost_usd,
          latency_ms,
          attempts_to_green: tel.attempts_to_green,
          sweep: { ...sweep, objective_passed: false },
        }
      );
    }

    const gates = await this.checkPassCriteria(dir, src.base_sha, record?.branch, task);
    const objective_passed = gates.every((g) => g.passed);
    return {
      verdict: objective_passed ? "pass" : "fail",
      tokens: tel.tokens,
      cost_usd: tel.cost_usd,
      latency_ms,
      attempts_to_green: tel.attempts_to_green,
      gate_results: gates,
      model_version_label: cell.model_id,
      sweep: { ...sweep, objective_passed },
    };
  }

  /**
   * The control's objective criteria on the run's branch: every
   * `files_must_change` path is in the diff from `base_sha`, every
   * `tests_must_pass` file exists, and every task `check` command exits as
   * expected (the checks are what run those tests).
   */
  private async checkPassCriteria(
    dir: string,
    baseSha: string,
    branch: string | undefined,
    task: EvalTask
  ): Promise<GateResult[]> {
    const criteria = task.pass_criteria!;
    const ref = branch && branch.length > 0 ? branch : "HEAD";
    if (ref !== "HEAD") await this.exec("git", ["checkout", "-q", ref], { cwd: dir });
    const diff = await this.exec("git", ["diff", "--name-only", baseSha, ref], { cwd: dir });
    const changed = new Set(
      diff.stdout
        .split("\n")
        .map((l) => l.trim())
        .filter(Boolean)
    );
    const gates: GateResult[] = criteria.files_must_change.map((f) => ({
      name: `files_must_change:${f}`,
      passed: diff.code === 0 && changed.has(f),
    }));
    for (const f of criteria.tests_must_pass) {
      const exists = await stat(join(dir, f)).then(
        () => true,
        () => false
      );
      gates.push({
        name: `tests_must_pass:${f}`,
        passed: exists,
        ...(exists ? {} : { detail: "missing" }),
      });
    }
    for (const check of task.checks) {
      const r = await this.exec("bash", ["-c", check.command], { cwd: dir });
      const passed = r.code === (check.expect_exit_code ?? 0);
      gates.push({
        name: `check:${check.name}`,
        passed,
        ...(passed ? {} : { detail: tailLine(r.stderr || r.stdout) }),
      });
    }
    return gates;
  }
}

/**
 * Default {@link RunRecordReader}: resolves the cell's history directory with
 * `nightgauge layout path pipeline history` and returns the last
 * `record_type: "run"` line for the issue across the daily JSONL files.
 */
export function historyRunRecordReader(exec: ExecFn, binary = "nightgauge"): RunRecordReader {
  return async (cwd, issueNumber) => {
    const r = await exec(binary, ["layout", "path", "pipeline", "history"], { cwd });
    if (r.code !== 0) return undefined;
    return latestRunRecord(r.stdout.trim(), issueNumber);
  };
}

/** The last `record_type: "run"` record for `issueNumber` under `historyDir`. */
export async function latestRunRecord(
  historyDir: string,
  issueNumber: number
): Promise<PipelineRunRecord | undefined> {
  const files = (await readdir(historyDir).catch(() => [] as string[]))
    .filter((f) => /^\d{4}-\d{2}-\d{2}\.jsonl$/.test(f))
    .sort();
  let latest: PipelineRunRecord | undefined;
  for (const f of files) {
    const text = await readFile(join(historyDir, f), "utf8");
    for (const line of text.split("\n")) {
      if (!line.trim()) continue;
      let rec: { record_type?: string; issue_number?: number };
      try {
        rec = JSON.parse(line);
      } catch {
        continue;
      }
      if (rec.record_type === "run" && rec.issue_number === issueNumber) {
        latest = rec as PipelineRunRecord;
      }
    }
  }
  return latest;
}

function tailLine(s: string): string {
  const lines = s.trim().split("\n");
  return (lines[lines.length - 1] ?? "").slice(0, 300);
}
