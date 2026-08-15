/**
 * Tests for the per-adapter eval spawn profiles (Issue #107, honest axes #571).
 * Pure functions — no CLI, no network. Covers the pieces the executor tests
 * don't assert directly: command-resolution precedence, provider→profile
 * resolution, and the real effort/thinking knob wiring.
 */

import { describe, it, expect, afterEach } from "vitest";
import {
  CLAUDE_DISABLE_THINKING_ENV,
  CLAUDE_MAX_THINKING_TOKENS_ENV,
  CLAUDE_THINKING_BUDGETS,
  UnsupportedCellError,
  claudeEvalProfile,
  codexEvalProfile,
  parseClaudeResult,
  resolveEvalAdapterProfile,
  resolveEvalAdapterProfileForAdapter,
} from "../../src/eval/evalAdapters.js";
import { EFFORT_LEVELS } from "../../src/eval/modelEvalSchemas.js";

const CODEX_ENV = "NIGHTGAUGE_CODEX_CLI_COMMAND";
const CLAUDE_ENV = "NIGHTGAUGE_CLAUDE_CLI_COMMAND";

afterEach(() => {
  delete process.env[CODEX_ENV];
  delete process.env[CLAUDE_ENV];
});

describe("resolveEvalAdapterProfile", () => {
  it("maps anthropic → claude and openai → codex", () => {
    expect(resolveEvalAdapterProfile("anthropic")).toBe(claudeEvalProfile);
    expect(resolveEvalAdapterProfile("openai")).toBe(codexEvalProfile);
  });

  it("throws an actionable error for an unwired provider", () => {
    expect(() => resolveEvalAdapterProfile("google")).toThrow(
      /not implemented for provider 'google'/
    );
    // The error names the adapters that ARE wired, so it is self-remediating.
    expect(() => resolveEvalAdapterProfile("ollama")).toThrow(/claude, codex/);
  });

  it("resolves by adapter name through the registry's adapter→provider map", () => {
    expect(resolveEvalAdapterProfileForAdapter("codex")).toBe(codexEvalProfile);
    expect(resolveEvalAdapterProfileForAdapter("claude-headless")).toBe(claudeEvalProfile);
    expect(resolveEvalAdapterProfileForAdapter("claude")).toBe(claudeEvalProfile);
  });
});

describe("command resolution precedence", () => {
  it("prefers an explicit override over the env var and the default", () => {
    process.env[CODEX_ENV] = "codex-from-env";
    expect(codexEvalProfile.resolveCommand("codex-override")).toBe("codex-override");
  });

  it("falls back to the profile env var when no override is given", () => {
    process.env[CODEX_ENV] = "codex-from-env";
    expect(codexEvalProfile.resolveCommand()).toBe("codex-from-env");
    process.env[CLAUDE_ENV] = "claude-from-env";
    expect(claudeEvalProfile.resolveCommand()).toBe("claude-from-env");
  });

  it("falls back to the built-in default when neither override nor env is set", () => {
    expect(codexEvalProfile.resolveCommand()).toBe("codex");
    expect(claudeEvalProfile.resolveCommand()).toBe("claude");
  });
});

describe("claude spawn plan — real effort/thinking knobs (#571)", () => {
  it("emits the pipeline's --effort flag for every ladder level", () => {
    for (const effort of EFFORT_LEVELS) {
      const plan = claudeEvalProfile.buildSpawnPlan("claude-opus-5", effort, "low");
      const i = plan.args.indexOf("--effort");
      expect(i).toBeGreaterThanOrEqual(0);
      expect(plan.args[i + 1]).toBe(effort);
    }
  });

  it("expresses reasoning 'none' as the thinking-disable env, not a prompt keyword", () => {
    const plan = claudeEvalProfile.buildSpawnPlan("claude-sonnet-5", "high", "none");
    expect(plan.env).toEqual({ [CLAUDE_DISABLE_THINKING_ENV]: "1" });
    expect(plan.env[CLAUDE_MAX_THINKING_TOKENS_ENV]).toBeUndefined();
  });

  it("expresses reasoning levels as MAX_THINKING_TOKENS budgets (keyword ladder retired)", () => {
    for (const reasoning of ["low", "medium", "high"] as const) {
      const plan = claudeEvalProfile.buildSpawnPlan("claude-sonnet-5", "high", reasoning);
      expect(plan.env).toEqual({
        [CLAUDE_MAX_THINKING_TOKENS_ENV]: String(CLAUDE_THINKING_BUDGETS[reasoning]),
      });
      expect(plan.env[CLAUDE_DISABLE_THINKING_ENV]).toBeUndefined();
    }
    // The budget ladder ascends with the axis.
    expect(CLAUDE_THINKING_BUDGETS.low).toBeLessThan(CLAUDE_THINKING_BUDGETS.medium);
    expect(CLAUDE_THINKING_BUDGETS.medium).toBeLessThan(CLAUDE_THINKING_BUDGETS.high);
  });

  it("keeps the headless, permissionless flag shape", () => {
    const plan = claudeEvalProfile.buildSpawnPlan("claude-sonnet-5", "medium", "none");
    expect(plan.args).toEqual(
      expect.arrayContaining([
        "--print",
        "--output-format",
        "json",
        "--model",
        "claude-sonnet-5",
        "--dangerously-skip-permissions",
      ])
    );
  });
});

describe("codex spawn plan — effort drives the single reasoning-effort knob (#571)", () => {
  it("maps cell.effort onto -c model_reasoning_effort=…, mirroring the pipeline adapter", () => {
    for (const effort of EFFORT_LEVELS) {
      const plan = codexEvalProfile.buildSpawnPlan("gpt-5.5", effort, "none");
      const i = plan.args.indexOf("-c");
      expect(i).toBeGreaterThanOrEqual(0);
      expect(plan.args[i + 1]).toBe(`model_reasoning_effort=${effort}`);
      expect(plan.env).toEqual({});
    }
  });

  it("fails as unsupported for a non-none reasoning — codex has no separate thinking knob", () => {
    expect(() => codexEvalProfile.buildSpawnPlan("gpt-5.5", "high", "high")).toThrow(
      UnsupportedCellError
    );
    expect(() => codexEvalProfile.buildSpawnPlan("gpt-5.5", "high", "low")).toThrow(
      /no thinking knob separate from 'model_reasoning_effort'/
    );
  });
});

describe("parseResult normalization", () => {
  it("claude: maps the result envelope to normalized telemetry", () => {
    const stdout = JSON.stringify({
      type: "result",
      is_error: false,
      duration_ms: 1234,
      usage: {
        input_tokens: 100,
        output_tokens: 40,
        cache_read_input_tokens: 10,
        cache_creation_input_tokens: 5,
      },
    });
    expect(claudeEvalProfile.parseResult(stdout)).toEqual({
      usage: { input: 100, output: 40, cache_read: 10, cache_creation: 5 },
      durationMs: 1234,
      isError: false,
    });
  });

  it("claude: returns null on unparseable output", () => {
    expect(claudeEvalProfile.parseResult("not json")).toBeNull();
    expect(claudeEvalProfile.parseResult("")).toBeNull();
  });

  it("codex: sums turn usage, subtracts the cached subset, reports no duration", () => {
    const jsonl = [
      JSON.stringify({
        type: "turn.completed",
        usage: { input_tokens: 500, cached_input_tokens: 120, output_tokens: 80 },
      }),
      JSON.stringify({ type: "item.completed", item: { type: "agent_message", text: "done" } }),
    ].join("\n");
    expect(codexEvalProfile.parseResult(jsonl)).toEqual({
      usage: { input: 380, output: 80, cache_read: 120, cache_creation: 0 },
      durationMs: 0,
      isError: false,
    });
  });

  it("codex: flags an explicit failure signal via isError", () => {
    const jsonl = JSON.stringify({
      type: "item.completed",
      item: { type: "agent_message", text: "execution halted" },
    });
    expect(codexEvalProfile.parseResult(jsonl)?.isError).toBe(true);
  });

  it("codex: returns null on empty output", () => {
    expect(codexEvalProfile.parseResult("   ")).toBeNull();
  });
});

describe("parseClaudeResult", () => {
  it("recovers the result object from trailing noise", () => {
    const out = `diagnostic\n${JSON.stringify({ type: "result", usage: { output_tokens: 9 } })}`;
    expect(parseClaudeResult(out)?.usage?.output_tokens).toBe(9);
  });
});
