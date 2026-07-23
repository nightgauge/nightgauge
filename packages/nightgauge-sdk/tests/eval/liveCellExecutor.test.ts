/**
 * Tests for the live cell executor (Issue #4174 follow-up). Both side-effecting
 * boundaries — the model spawn and the shell (`npm install` + checks) — are
 * injected, so no real CLI, network, or toolchain runs here.
 */

import { describe, it, expect } from "vitest";
import {
  LiveCellExecutor,
  type CliSpawnFn,
  type CliSpawnResult,
} from "../../src/eval/liveCellExecutor.js";
import { parseClaudeResult } from "../../src/eval/evalAdapters.js";
import type { ExecFn, ExecResult } from "../../src/eval/worktreeWorkspace.js";
import type { EvalMatrixCell, EvalTask } from "../../src/eval/modelEvalSchemas.js";
import type { EvalWorkspace } from "../../src/eval/modelEvalRunner.js";
import type { EvalJudge, EvalJudgeVerdict } from "../../src/eval/qualityScorer.js";

const WORKSPACE: EvalWorkspace = { dir: "/tmp/ws/cell", dispose: async () => {} };

function task(overrides: Partial<EvalTask> = {}): EvalTask {
  return {
    id: "bugfix-date",
    title: "Fix a date bug",
    job_class: "bugfix",
    target_stages: ["feature-dev", "feature-validate"],
    difficulty: "easy",
    instruction: "Fix daysBetween so month-boundary ranges are correct.",
    fixture: { kind: "scaffold-script", ref: "evals/fixtures/bugfix-date/setup.sh" },
    checks: [{ name: "test", command: "npm test", expect_exit_code: 0 }],
    rubric: { criteria: [{ dimension: "correctness", weight: 1, guidance: "?" }] },
    ...overrides,
  };
}

const CELL = (over: Partial<EvalMatrixCell> = {}): EvalMatrixCell => ({
  model_id: "claude-sonnet-5",
  effort: "high",
  reasoning: "none",
  prompt_variant: "baseline",
  ...over,
});

/** A claude result JSON blob with the given usage/flags. */
function claudeJson(
  usage: Partial<{
    input_tokens: number;
    output_tokens: number;
    cache_read_input_tokens: number;
    cache_creation_input_tokens: number;
  }>,
  extra: Record<string, unknown> = {}
): string {
  return JSON.stringify({
    type: "result",
    subtype: "success",
    is_error: false,
    duration_ms: 4200,
    result: "done",
    usage: {
      input_tokens: 0,
      output_tokens: 0,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
      ...usage,
    },
    ...extra,
  });
}

/** Records spawn calls and returns a scripted queue of results. */
function scriptedSpawn(results: CliSpawnResult[]): {
  spawn: CliSpawnFn;
  calls: Array<{ command: string; args: string[]; prompt: string; cwd: string }>;
} {
  const calls: Array<{ command: string; args: string[]; prompt: string; cwd: string }> = [];
  let i = 0;
  const spawn: CliSpawnFn = async (command, args, prompt, cwd) => {
    calls.push({ command, args, prompt, cwd });
    const r = results[Math.min(i, results.length - 1)];
    i++;
    return r;
  };
  return { spawn, calls };
}

/**
 * Records exec calls. `checkCodes` is a queue of exit codes for `bash -c`
 * invocations (the checks); `installCode` is used for the package-manager call.
 */
function scriptedExec(opts: { installCode?: number; checkCodes?: number[] } = {}): {
  exec: ExecFn;
  calls: Array<{ cmd: string; args: string[] }>;
} {
  const calls: Array<{ cmd: string; args: string[] }> = [];
  const checkCodes = [...(opts.checkCodes ?? [0])];
  const exec: ExecFn = async (cmd, args): Promise<ExecResult> => {
    calls.push({ cmd, args });
    if (cmd === "bash") {
      const code = checkCodes.length > 1 ? checkCodes.shift()! : (checkCodes[0] ?? 0);
      return { code, stdout: "", stderr: code === 0 ? "" : "1 test failed" };
    }
    // package manager (install)
    const code = opts.installCode ?? 0;
    return { code, stdout: "", stderr: code === 0 ? "" : "install boom" };
  };
  return { exec, calls };
}

describe("LiveCellExecutor", () => {
  it("passes when checks are green: sums tokens, resolves concrete version, records stage", async () => {
    const { spawn, calls } = scriptedSpawn([
      {
        stdout: claudeJson({
          input_tokens: 12000,
          output_tokens: 8000,
          cache_read_input_tokens: 2000,
        }),
        stderr: "",
        code: 0,
      },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec });

    const result = await exe.execute(task(), CELL(), WORKSPACE);

    expect(result.verdict).toBe("pass");
    expect(result.attempts_to_green).toBe(1);
    expect(result.tokens).toEqual({
      input: 12000,
      output: 8000,
      cache_read: 2000,
      cache_creation: 0,
    });
    expect(result.latency_ms).toBe(4200);
    expect(result.gate_results).toEqual([{ name: "test", passed: true, detail: undefined }]);
    expect(result.model_version_label).toBe("Sonnet 5");
    expect(result.stage).toBe("feature-dev");
    // Invoked by concrete version, headless + permissionless.
    expect(calls[0].args).toEqual(
      expect.arrayContaining([
        "--print",
        "--output-format",
        "json",
        "--model",
        "claude-sonnet-5",
        "--dangerously-skip-permissions",
      ])
    );
    // Task instruction reaches the model over stdin.
    expect(calls[0].prompt).toContain("Fix daysBetween");
    expect(calls[0].cwd).toBe("/tmp/ws/cell");
  });

  it("fails (not errors) when checks stay red with maxAttempts=1", async () => {
    const { spawn } = scriptedSpawn([
      { stdout: claudeJson({ input_tokens: 100, output_tokens: 50 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [1] });
    const exe = new LiveCellExecutor({ spawn, exec });

    const result = await exe.execute(task(), CELL(), WORKSPACE);

    expect(result.verdict).toBe("fail");
    expect(result.attempts_to_green).toBe(1);
    expect(result.gate_results[0]).toMatchObject({ name: "test", passed: false });
    expect(result.gate_results[0].detail).toContain("test failed");
  });

  it("retries with failing-check feedback and greens on attempt 2 (tokens summed)", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: claudeJson({ input_tokens: 1000, output_tokens: 500 }), stderr: "", code: 0 },
      { stdout: claudeJson({ input_tokens: 2000, output_tokens: 700 }), stderr: "", code: 0 },
    ]);
    // First check run fails, second passes.
    const { exec } = scriptedExec({ checkCodes: [1, 0] });
    const exe = new LiveCellExecutor({ spawn, exec, maxAttempts: 3 });

    const result = await exe.execute(task(), CELL(), WORKSPACE);

    expect(result.verdict).toBe("pass");
    expect(result.attempts_to_green).toBe(2);
    expect(result.tokens.input).toBe(3000);
    expect(result.tokens.output).toBe(1200);
    expect(result.latency_ms).toBe(8400); // 4200 × 2
    // The retry prompt carries the failing-check detail forward.
    expect(calls).toHaveLength(2);
    expect(calls[1].prompt).toContain("did not pass all checks");
    expect(calls[1].prompt).toContain("test");
  });

  it("stops at maxAttempts and reports fail when never green", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: claudeJson({ input_tokens: 10, output_tokens: 5 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [1] });
    const exe = new LiveCellExecutor({ spawn, exec, maxAttempts: 2 });

    const result = await exe.execute(task(), CELL(), WORKSPACE);

    expect(result.verdict).toBe("fail");
    expect(result.attempts_to_green).toBe(2);
    expect(calls).toHaveLength(2);
  });

  it("maps reasoning=high to the ultrathink keyword in the prompt", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: claudeJson({ input_tokens: 1, output_tokens: 1 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec });

    await exe.execute(task(), CELL({ reasoning: "high" }), WORKSPACE);
    expect(calls[0].prompt).toContain("Ultrathink");
  });

  it("applies the prompt-variant overlay to the executed prompt (#72)", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: claudeJson({ input_tokens: 1, output_tokens: 1 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({
      spawn,
      exec,
      variants: new Map([
        ["concise-preamble", { name: "concise-preamble", prepend: "BE-CONCISE-MARKER" }],
      ]),
    });

    await exe.execute(task(), CELL({ prompt_variant: "concise-preamble" }), WORKSPACE);
    const prompt = calls[0].prompt;
    expect(prompt).toContain("BE-CONCISE-MARKER");
    // Overlay wraps the instruction, before the task text.
    expect(prompt.indexOf("BE-CONCISE-MARKER")).toBeLessThan(prompt.indexOf(task().instruction));
  });

  it("runs baseline cells with the untouched instruction even when variants are loaded (#72)", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: claudeJson({ input_tokens: 1, output_tokens: 1 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({
      spawn,
      exec,
      variants: new Map([
        ["concise-preamble", { name: "concise-preamble", prepend: "BE-CONCISE-MARKER" }],
      ]),
    });

    await exe.execute(task(), CELL(), WORKSPACE);
    expect(calls[0].prompt).not.toContain("BE-CONCISE-MARKER");
  });

  it("throws when a cell references a variant that was never loaded (#72)", async () => {
    const { spawn } = scriptedSpawn([
      { stdout: claudeJson({ input_tokens: 1, output_tokens: 1 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec });

    await expect(
      exe.execute(task(), CELL({ prompt_variant: "never-loaded" }), WORKSPACE)
    ).rejects.toThrow(/not loaded/);
  });

  it("throws (→ error verdict via runner) when dependency install fails", async () => {
    const { spawn } = scriptedSpawn([{ stdout: claudeJson({}), stderr: "", code: 0 }]);
    const { exec } = scriptedExec({ installCode: 1 });
    const exe = new LiveCellExecutor({ spawn, exec });

    await expect(exe.execute(task(), CELL(), WORKSPACE)).rejects.toThrow(
      /dependency install failed/
    );
  });

  it("skips install when skipInstall is set", async () => {
    const { spawn } = scriptedSpawn([{ stdout: claudeJson({}), stderr: "", code: 0 }]);
    const { exec, calls } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec, skipInstall: true });

    await exe.execute(task(), CELL(), WORKSPACE);
    // Only the check ran; no package-manager install call.
    expect(calls.every((c) => c.cmd === "bash")).toBe(true);
  });

  it("throws when the claude CLI exits non-zero", async () => {
    const { spawn } = scriptedSpawn([{ stdout: "", stderr: "auth required", code: 1 }]);
    const { exec } = scriptedExec();
    const exe = new LiveCellExecutor({ spawn, exec, skipInstall: true });

    await expect(exe.execute(task(), CELL(), WORKSPACE)).rejects.toThrow(
      /claude invocation failed/
    );
  });

  it("throws when the claude output is not parseable JSON", async () => {
    const { spawn } = scriptedSpawn([{ stdout: "not json at all", stderr: "", code: 0 }]);
    const { exec } = scriptedExec();
    const exe = new LiveCellExecutor({ spawn, exec, skipInstall: true });

    await expect(exe.execute(task(), CELL(), WORKSPACE)).rejects.toThrow(/could not parse/);
  });

  it("falls back to the model success signal for a task with no checks", async () => {
    const { spawn } = scriptedSpawn([
      { stdout: claudeJson({}, { is_error: false }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec();
    const exe = new LiveCellExecutor({ spawn, exec, skipInstall: true });

    const result = await exe.execute(task({ checks: [] }), CELL(), WORKSPACE);
    expect(result.verdict).toBe("pass");
    expect(result.gate_results).toEqual([{ name: "completed", passed: true }]);
  });

  it("honors expect_exit_code !== 0 for a check", async () => {
    const { spawn } = scriptedSpawn([{ stdout: claudeJson({}), stderr: "", code: 0 }]);
    // Check expects exit 1; bash returns 1 → passes.
    const { exec } = scriptedExec({ checkCodes: [1] });
    const exe = new LiveCellExecutor({ spawn, exec, skipInstall: true });

    const result = await exe.execute(
      task({ checks: [{ name: "must-fail", command: "false", expect_exit_code: 1 }] }),
      CELL(),
      WORKSPACE
    );
    expect(result.verdict).toBe("pass");
    expect(result.gate_results[0]).toMatchObject({ name: "must-fail", passed: true });
  });
});

describe("LiveCellExecutor — adapter parameterization (#107)", () => {
  /**
   * A Codex `--json` JSONL stream: thread.started, one turn.completed carrying
   * usage (OpenAI convention — `input_tokens` is cache-INCLUSIVE, `cached_input_tokens`
   * the cached subset), and a final agent_message. `message` drives the failure signal.
   */
  function codexJsonl(
    opts: { input?: number; cached?: number; output?: number; message?: string } = {}
  ): string {
    const { input = 0, cached = 0, output = 0, message = "Implemented the fix." } = opts;
    return [
      JSON.stringify({ type: "thread.started", thread_id: "t-1" }),
      JSON.stringify({
        type: "turn.completed",
        usage: { input_tokens: input, cached_input_tokens: cached, output_tokens: output },
      }),
      JSON.stringify({ type: "item.completed", item: { type: "agent_message", text: message } }),
    ].join("\n");
  }

  it("spawns the codex CLI flag shape (not claude) for an openai-provider model", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: codexJsonl({ input: 10, output: 5 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec });

    const result = await exe.execute(task(), CELL({ model_id: "gpt-5.5" }), WORKSPACE);

    expect(result.verdict).toBe("pass");
    expect(result.model_version_label).toBe("GPT-5.5");
    expect(calls[0].command).toBe("codex");
    expect(calls[0].args).toEqual(
      expect.arrayContaining([
        "exec",
        "--dangerously-bypass-approvals-and-sandbox",
        "--json",
        "--model",
        "gpt-5.5",
      ])
    );
    // The Claude flag shape must NOT leak onto a codex run.
    expect(calls[0].args).not.toContain("--print");
    expect(calls[0].args).not.toContain("--dangerously-skip-permissions");
  });

  it("parses codex turn.completed usage, normalizing the cached subset out of input", async () => {
    const { spawn } = scriptedSpawn([
      { stdout: codexJsonl({ input: 1000, cached: 200, output: 300 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec });

    const result = await exe.execute(task(), CELL({ model_id: "gpt-5.5" }), WORKSPACE);

    // input_tokens (1000, cache-inclusive) − cached (200) = 800 non-cached; cached → cache_read.
    expect(result.tokens).toEqual({
      input: 800,
      output: 300,
      cache_read: 200,
      cache_creation: 0,
    });
    // Codex reports no duration; the executor falls back to measured wall time (≥ 0).
    expect(result.latency_ms).toBeGreaterThanOrEqual(0);
  });

  it("expresses codex reasoning via the model_reasoning_effort flag, not a prompt keyword", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: codexJsonl({ input: 1, output: 1 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec });

    await exe.execute(task(), CELL({ model_id: "gpt-5.5", reasoning: "high" }), WORKSPACE);

    // Reasoning is a CLI flag for codex …
    const args = calls[0].args;
    expect(args).toContain("-c");
    expect(args[args.indexOf("-c") + 1]).toBe("model_reasoning_effort=high");
    // … and must NOT appear as Claude's prompt keyword.
    expect(calls[0].prompt).not.toContain("Ultrathink");
  });

  it("omits the codex reasoning flag when reasoning is none (codex default)", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: codexJsonl({ input: 1, output: 1 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({ spawn, exec });

    await exe.execute(task(), CELL({ model_id: "gpt-5.5", reasoning: "none" }), WORKSPACE);
    expect(calls[0].args).not.toContain("-c");
  });

  it("honors an explicit adapter override, resolving the model within that provider", async () => {
    const { spawn, calls } = scriptedSpawn([
      { stdout: codexJsonl({ input: 5, output: 2 }), stderr: "", code: 0 },
    ]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    // adapter:"codex" forces the openai provider, so the bare "sonnet" tier
    // resolves to the OpenAI model serving that band, not a Claude model.
    const exe = new LiveCellExecutor({ spawn, exec, adapter: "codex" });

    const result = await exe.execute(task(), CELL({ model_id: "sonnet" }), WORKSPACE);

    expect(calls[0].command).toBe("codex");
    expect(calls[0].args).toEqual(expect.arrayContaining(["--model", "gpt-5.6-terra"]));
    expect(result.model_version_label).toBe("GPT-5.6 Terra");
  });

  it("uses codex's own failure signal for a task with no checks", async () => {
    const { spawn } = scriptedSpawn([
      {
        stdout: codexJsonl({ input: 1, output: 1, message: "execution halted" }),
        stderr: "",
        code: 0,
      },
    ]);
    const { exec } = scriptedExec();
    const exe = new LiveCellExecutor({ spawn, exec, skipInstall: true });

    const result = await exe.execute(
      task({ checks: [] }),
      CELL({ model_id: "gpt-5.5" }),
      WORKSPACE
    );
    expect(result.verdict).toBe("fail");
    expect(result.gate_results).toEqual([{ name: "completed", passed: false }]);
  });

  it("throws an actionable error for a provider whose live CLI is not wired", async () => {
    const { spawn } = scriptedSpawn([{ stdout: "", stderr: "", code: 0 }]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    // adapter:"gemini" → google provider, which has no eval spawn profile yet.
    const exe = new LiveCellExecutor({ spawn, exec, adapter: "gemini" });

    await expect(exe.execute(task(), CELL(), WORKSPACE)).rejects.toThrow(
      /not implemented for provider 'google'/
    );
  });

  it("reports 'codex invocation failed' when the codex CLI exits non-zero", async () => {
    const { spawn } = scriptedSpawn([{ stdout: "", stderr: "codex login required", code: 1 }]);
    const { exec } = scriptedExec();
    const exe = new LiveCellExecutor({ spawn, exec, skipInstall: true });

    await expect(exe.execute(task(), CELL({ model_id: "gpt-5.5" }), WORKSPACE)).rejects.toThrow(
      /codex invocation failed/
    );
  });
});

describe("LiveCellExecutor — judge wiring", () => {
  /** A fake judge that returns a fixed verdict and counts how many times it ran. */
  function countingJudge(uxScore: number) {
    let calls = 0;
    const judge: EvalJudge = {
      async judge(): Promise<EvalJudgeVerdict> {
        calls++;
        return { dimensions: [{ dimension: "ux_quality", score: uxScore }] };
      },
    };
    return { judge, calls: () => calls };
  }

  it("runs the judge factory with (task, cell, workspace) and attaches the verdict", async () => {
    const { spawn } = scriptedSpawn([{ stdout: claudeJson({}), stderr: "", code: 0 }]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const seen: Array<{ id: string; model: string; dir: string }> = [];
    const { judge } = countingJudge(88);
    const exe = new LiveCellExecutor({
      spawn,
      exec,
      skipInstall: true,
      judgeSamples: 1,
      judgeFactory: (t, c, ws) => {
        seen.push({ id: t.id, model: c.model_id, dir: ws.dir });
        return judge;
      },
    });

    const result = await exe.execute(task(), CELL(), WORKSPACE);
    expect(seen).toEqual([{ id: "bugfix-date", model: "claude-sonnet-5", dir: "/tmp/ws/cell" }]);
    expect(result.judge?.verdict.dimensions[0]).toMatchObject({
      dimension: "ux_quality",
      score: 88,
    });
    expect(result.judge?.lowConfidence).toEqual([]);
  });

  it("does not attach a judge when the factory returns null", async () => {
    const { spawn } = scriptedSpawn([{ stdout: claudeJson({}), stderr: "", code: 0 }]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const exe = new LiveCellExecutor({
      spawn,
      exec,
      skipInstall: true,
      judgeFactory: () => null,
    });
    const result = await exe.execute(task(), CELL(), WORKSPACE);
    expect(result.judge).toBeUndefined();
  });

  it("samples the judge judgeSamples times through the reliability guard", async () => {
    const { spawn } = scriptedSpawn([{ stdout: claudeJson({}), stderr: "", code: 0 }]);
    const { exec } = scriptedExec({ checkCodes: [0] });
    const { judge, calls } = countingJudge(70);
    const exe = new LiveCellExecutor({
      spawn,
      exec,
      skipInstall: true,
      judgeSamples: 3,
      judgeFactory: () => judge,
    });
    await exe.execute(task(), CELL(), WORKSPACE);
    expect(calls()).toBe(3);
  });
});

describe("parseClaudeResult", () => {
  it("parses a single JSON object", () => {
    expect(parseClaudeResult(claudeJson({ input_tokens: 5 }))?.usage?.input_tokens).toBe(5);
  });

  it("recovers the result object from trailing lines of noise", () => {
    const out = `some diagnostic line\n${claudeJson({ output_tokens: 9 })}`;
    expect(parseClaudeResult(out)?.usage?.output_tokens).toBe(9);
  });

  it("returns null on empty or unparseable output", () => {
    expect(parseClaudeResult("")).toBeNull();
    expect(parseClaudeResult("nope")).toBeNull();
  });
});
