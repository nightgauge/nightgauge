/**
 * Tests for the whole-pipeline cell executor (Issue #2118). The pipeline run,
 * run-record reader and shell are injected; the cell dir is a real temp dir.
 */

import { describe, it, expect, afterEach } from "vitest";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  PipelineCellExecutor,
  latestRunRecord,
  refusePipelineRun,
  type PipelineRunFn,
  type PipelineRunRecord,
} from "../../src/eval/pipelineCellExecutor.js";
import { CellExecutionError } from "../../src/eval/modelEvalRunner.js";
import type { EvalMatrixCell, EvalTask } from "../../src/eval/modelEvalSchemas.js";
import type { ExecFn } from "../../src/eval/worktreeWorkspace.js";

const CELL: EvalMatrixCell = {
  model_id: "claude-opus-4-8",
  effort: "high",
  reasoning: "none",
  prompt_variant: "baseline",
};

const TASK: EvalTask = {
  id: "control",
  title: "control",
  job_class: "backend-logic",
  target_stages: ["feature-dev"],
  difficulty: "medium",
  instruction: "x",
  fixture: { kind: "base-commit", ref: "abc123", repo: "nightgauge/eval-control" },
  checks: [{ name: "test", command: "npm test" }],
  rubric: { criteria: [{ dimension: "correctness", weight: 1, guidance: "?" }] },
  control: true,
  source_issue: {
    url: "https://github.com/nightgauge/eval-control/issues/1",
    repo: "nightgauge/eval-control",
    number: 1,
    base_sha: "abc123",
    content_sha256: "f".repeat(64),
  },
  pass_criteria: { tests_must_pass: ["test/a.test.ts"], files_must_change: ["src/a.ts"] },
} as EvalTask;

/** Scripted run record: every stage dispatched at `effort`, $0.5 each. */
function record(effort: (stage: string) => string | undefined): PipelineRunRecord {
  const stages = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
  ];
  return {
    issue_number: 1,
    branch: "feat/1-x",
    stages: Object.fromEntries(
      stages.map((s) => [
        s,
        {
          status: "completed",
          model_selection: { model: CELL.model_id, effort: effort(s) },
          attempts_until_success: 1,
        },
      ])
    ),
    tokens: {
      total_input: 10,
      total_output: 20,
      per_stage: Object.fromEntries(stages.map((s) => [s, { cost_usd: 0.5 }])),
    },
  };
}

const okExec: ExecFn = async (cmd, args) =>
  cmd === "git" && args[0] === "diff"
    ? { code: 0, stdout: "src/a.ts\n", stderr: "" }
    : { code: 0, stdout: "", stderr: "" };

const okRun: PipelineRunFn = async () => ({ code: 0, timedOut: false, stdout: "", stderr: "" });

describe("PipelineCellExecutor", () => {
  let dir: string;
  afterEach(async () => {
    if (dir) await rm(dir, { recursive: true, force: true });
  });

  async function exec(opts: {
    run?: PipelineRunFn;
    rec?: PipelineRunRecord;
    shell?: ExecFn;
    noTestFile?: boolean;
  }) {
    dir = await mkdtemp(join(tmpdir(), "ng-pce-"));
    if (!opts.noTestFile) {
      await mkdir(join(dir, "test"), { recursive: true });
      await writeFile(join(dir, "test", "a.test.ts"), "");
    }
    let t = 0;
    const executor = new PipelineCellExecutor({
      run: opts.run ?? okRun,
      timeoutMs: 1000,
      readRunRecord: async () => opts.rec,
      exec: opts.shell ?? okExec,
      now: () => (t += 4000),
    });
    return executor.execute(TASK, CELL, { dir, dispose: async () => {} });
  }

  it("pins the cell's model and effort for every stage in an uncommitted local config", async () => {
    await exec({ rec: record(() => "high") });
    const yaml = await readFile(join(dir, ".nightgauge", "config.local.yaml"), "utf8");
    expect(yaml).toContain("mode: manual");
    for (const s of [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]) {
      expect(yaml).toContain(`    ${s}: claude-opus-4-8`);
      expect(yaml).toContain(`    ${s}: high`);
    }
  });

  it("records cost, attempts, wall time and each stage's dispatched effort", async () => {
    const r = await exec({ rec: record(() => "high") });
    expect(r.verdict).toBe("pass");
    expect(r.cost_usd).toBe(2.5);
    expect(r.attempts_to_green).toBe(5);
    expect(r.sweep).toMatchObject({
      wall_clock_seconds: 4,
      objective_passed: true,
      timed_out: false,
    });
    expect(Object.values(r.sweep!.stage_efforts!)).toEqual([
      "high",
      "high",
      "high",
      "high",
      "high",
    ]);
  });

  it("fails the cell, keeping its cost, when a stage ran at another effort", async () => {
    const err = await exec({ rec: record((s) => (s === "feature-dev" ? "medium" : "high")) }).catch(
      (e) => e
    );
    expect(err).toBeInstanceOf(CellExecutionError);
    expect(err.message).toContain("feature-dev: medium");
    expect(err.partial.cost_usd).toBe(2.5);
  });

  it("fails the cell when a model-dispatching stage recorded no effort", async () => {
    const err = await exec({ rec: record((s) => (s === "pr-create" ? undefined : "high")) }).catch(
      (e) => e
    );
    expect(err.message).toContain("pr-create: no effort recorded");
  });

  it("a timeout yields a CellExecutionError with partial cost and timed_out", async () => {
    const rec = record(() => "high");
    rec.tokens!.per_stage = { "issue-pickup": { cost_usd: 0.3 } };
    const err = await exec({
      run: async () => ({ code: 1, timedOut: true, stdout: "", stderr: "" }),
      rec,
    }).catch((e) => e);
    expect(err).toBeInstanceOf(CellExecutionError);
    expect(err.partial.cost_usd).toBe(0.3);
    expect(err.partial.sweep).toMatchObject({ timed_out: true, objective_passed: false });
  });

  it("a run that completes but misses pass_criteria is a fail, not a pass", async () => {
    const shell: ExecFn = async (cmd, args) =>
      cmd === "git" && args[0] === "diff"
        ? { code: 0, stdout: "src/other.ts\n", stderr: "" }
        : { code: cmd === "bash" ? 1 : 0, stdout: "", stderr: "1 failing" };
    const r = await exec({ rec: record(() => "high"), shell, noTestFile: true });
    expect(r.verdict).toBe("fail");
    expect(r.sweep?.objective_passed).toBe(false);
    expect(r.gate_results.map((g) => g.name)).toEqual([
      "files_must_change:src/a.ts",
      "tests_must_pass:test/a.test.ts",
      "check:test",
    ]);
    expect(r.gate_results.every((g) => !g.passed)).toBe(true);
  });

  it("live runs refuse to start while nightgauge run cannot stop before pr-merge", async () => {
    await expect(
      refusePipelineRun({ cwd: "/", issueNumber: 1, repo: "a/b", timeoutMs: 1 })
    ).rejects.toThrow(/stop before pr-merge/);
  });
});

describe("latestRunRecord", () => {
  let hist: string;
  afterEach(async () => {
    if (hist) await rm(hist, { recursive: true, force: true });
  });

  it("returns the last run record for the issue across daily files", async () => {
    hist = await mkdtemp(join(tmpdir(), "ng-hist-"));
    await mkdir(hist, { recursive: true });
    const line = (o: object) => JSON.stringify(o) + "\n";
    await writeFile(
      join(hist, "2026-10-26.jsonl"),
      line({ record_type: "run", issue_number: 1, branch: "old" }) + "not json\n"
    );
    await writeFile(
      join(hist, "2026-10-27.jsonl"),
      line({ record_type: "run", issue_number: 1, branch: "new" }) +
        line({ record_type: "run", issue_number: 2, branch: "other" }) +
        line({ record_type: "exit", issue_number: 1, branch: "exit" })
    );
    expect((await latestRunRecord(hist, 1))?.branch).toBe("new");
    expect(await latestRunRecord(hist, 3)).toBeUndefined();
  });
});
