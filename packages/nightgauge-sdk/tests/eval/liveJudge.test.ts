/**
 * Tests for the live LLM judge (Issue #4173/#4174 follow-up). The spawn boundary
 * is injected, so no real CLI runs; extraction is tested directly.
 */

import { describe, it, expect } from "vitest";
import { LiveClaudeJudge, extractJudgeVerdict, type CliSpawnFn } from "../../src/eval/index.js";
import type { EvalRubric, EvalTask } from "../../src/eval/modelEvalSchemas.js";

const RUBRIC: EvalRubric = {
  criteria: [
    { dimension: "ux_quality", weight: 0.6, guidance: "polished + responsive?" },
    { dimension: "correctness", weight: 0.4, guidance: "renders + callback fires?" },
  ],
};

const TASK: EvalTask = {
  id: "ui-thing",
  title: "UI thing",
  job_class: "ui-creation",
  target_stages: ["feature-dev"],
  difficulty: "medium",
  instruction: "Build a polished pricing card.",
  fixture: { kind: "scaffold-script", ref: "evals/fixtures/ui-thing/setup.sh" },
  checks: [],
  rubric: RUBRIC,
};

/** Wrap judge answer text in the CLI `--output-format json` result envelope. */
function cliResult(resultText: string, code = 0): { stdout: string; stderr: string; code: number } {
  return {
    stdout: JSON.stringify({
      type: "result",
      is_error: false,
      duration_ms: 100,
      result: resultText,
    }),
    stderr: "",
    code,
  };
}

function spawnReturning(result: { stdout: string; stderr: string; code: number }): {
  spawn: CliSpawnFn;
  calls: Array<{ command: string; args: string[]; prompt: string; cwd: string }>;
} {
  const calls: Array<{ command: string; args: string[]; prompt: string; cwd: string }> = [];
  const spawn: CliSpawnFn = async (command, args, prompt, cwd) => {
    calls.push({ command, args, prompt, cwd });
    return result;
  };
  return { spawn, calls };
}

describe("extractJudgeVerdict", () => {
  it("keeps only rubric dimensions, clamps scores, ignores unknown dimensions", () => {
    const text =
      'noise {"dimensions":[{"dimension":"ux_quality","score":150,"rationale":"great"},' +
      '{"dimension":"correctness","score":-5},{"dimension":"performance","score":80}]} trailing';
    const v = extractJudgeVerdict(text, RUBRIC);
    const byDim = Object.fromEntries(v.dimensions.map((d) => [d.dimension, d.score]));
    expect(byDim).toEqual({ ux_quality: 100, correctness: 0 }); // performance dropped (not in rubric)
    expect(v.dimensions.find((d) => d.dimension === "ux_quality")?.rationale).toBe("great");
  });

  it("throws when no scorable rubric dimension is present", () => {
    expect(() =>
      extractJudgeVerdict('{"dimensions":[{"dimension":"performance","score":50}]}', RUBRIC)
    ).toThrow(/no scorable/);
    expect(() => extractJudgeVerdict("not json", RUBRIC)).toThrow(/no scorable/);
  });
});

describe("LiveClaudeJudge", () => {
  it("grades from embedded source: parses dimensions, resolves grader version, tool-free", async () => {
    const answer =
      '{"dimensions":[{"dimension":"ux_quality","score":82},{"dimension":"correctness","score":91}]}';
    const { spawn, calls } = spawnReturning(cliResult(answer));
    const judge = new LiveClaudeJudge({
      workspaceDir: "/tmp/ws/cell",
      task: TASK,
      model: "claude-sonnet-5",
      spawnClaude: spawn,
      collectSources: async () => [
        { path: "src/PricingCards.tsx", content: "export const x = 1;" },
      ],
    });

    const verdict = await judge.judge(RUBRIC);
    const byDim = Object.fromEntries(verdict.dimensions.map((d) => [d.dimension, d.score]));
    expect(byDim).toEqual({ ux_quality: 82, correctness: 91 });

    // Invoked by concrete version with all file/exec tools disallowed.
    expect(calls[0].args).toEqual(expect.arrayContaining(["--model", "claude-sonnet-5"]));
    expect(calls[0].args).toEqual(
      expect.arrayContaining([
        "--disallowed-tools",
        "Write",
        "Edit",
        "Bash",
        "Read",
        "Glob",
        "Grep",
      ])
    );
    // The prompt frames the task and embeds the engineer's source.
    expect(calls[0].prompt).toContain("Build a polished pricing card");
    expect(calls[0].prompt).toContain("src/PricingCards.tsx");
    expect(calls[0].prompt).toContain("export const x = 1;");
  });

  it("handles a task that produced no source files", async () => {
    const { spawn, calls } = spawnReturning(
      cliResult('{"dimensions":[{"dimension":"correctness","score":0}]}')
    );
    const judge = new LiveClaudeJudge({
      workspaceDir: "/tmp/x",
      task: TASK,
      spawnClaude: spawn,
      collectSources: async () => [],
    });
    await judge.judge(RUBRIC);
    expect(calls[0].prompt).toContain("no source files were produced");
  });

  it("throws when the grader CLI exits non-zero", async () => {
    const { spawn } = spawnReturning({ stdout: "", stderr: "auth", code: 1 });
    const judge = new LiveClaudeJudge({
      workspaceDir: "/tmp/x",
      task: TASK,
      spawnClaude: spawn,
      collectSources: async () => [],
    });
    await expect(judge.judge(RUBRIC)).rejects.toThrow(/judge invocation failed/);
  });

  it("throws when the grader output is unparseable", async () => {
    const { spawn } = spawnReturning({ stdout: "totally not json", stderr: "", code: 0 });
    const judge = new LiveClaudeJudge({
      workspaceDir: "/tmp/x",
      task: TASK,
      spawnClaude: spawn,
      collectSources: async () => [],
    });
    await expect(judge.judge(RUBRIC)).rejects.toThrow(/could not parse judge/);
  });
});
