/**
 * The live skill-eval runner must not hand a scenario model tools or the
 * operator's checkout: a live cell once pushed the branch it was run from.
 * Mock spawn only — no live model calls.
 */

import { describe, it, expect } from "vitest";
import { existsSync, readdirSync } from "fs";
import { LiveClaudeModelRunner, type SpawnFn } from "../../src/eval/modelRunner.js";
import type { EvalScenario } from "../../src/eval/schemas.js";

const scenario = {
  id: "s",
  skill: "issue-pickup",
  prompt: "say ok",
  assertions: [],
} as unknown as EvalScenario;

function capture() {
  const calls: { args: string[]; cwd: string; entries: string[] }[] = [];
  const spawnFn: SpawnFn = async (_cmd, args, _prompt, cwd) => {
    calls.push({ args, cwd, entries: readdirSync(cwd) });
    return { stdout: "ok", stderr: "", code: 0 };
  };
  return { calls, spawnFn };
}

describe("LiveClaudeModelRunner isolation", () => {
  it("disables every tool", async () => {
    const { calls, spawnFn } = capture();
    await new LiveClaudeModelRunner({ spawnFn }).run(scenario, "sonnet");
    const i = calls[0].args.indexOf("--tools");
    expect(i).toBeGreaterThan(-1);
    expect(calls[0].args[i + 1]).toBe("");
  });

  it("runs in an empty scratch directory, not the current checkout, and removes it", async () => {
    const { calls, spawnFn } = capture();
    await new LiveClaudeModelRunner({ spawnFn }).run(scenario, "sonnet");
    expect(calls[0].cwd).not.toBe(process.cwd());
    expect(calls[0].entries).toEqual([]);
    expect(existsSync(calls[0].cwd)).toBe(false);
  });

  it("honours an explicit cwd", async () => {
    const { calls, spawnFn } = capture();
    await new LiveClaudeModelRunner({ spawnFn, cwd: process.cwd() }).run(scenario, "sonnet");
    expect(calls[0].cwd).toBe(process.cwd());
  });
});
