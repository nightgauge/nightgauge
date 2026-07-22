/**
 * Tests for the per-adapter eval spawn profiles (Issue #107). Pure functions —
 * no CLI, no network. Covers the pieces the executor tests don't assert
 * directly: command-resolution precedence and provider→profile resolution.
 */

import { describe, it, expect, afterEach } from "vitest";
import {
  claudeEvalProfile,
  codexEvalProfile,
  parseClaudeResult,
  resolveEvalAdapterProfile,
  resolveEvalAdapterProfileForAdapter,
} from "../../src/eval/evalAdapters.js";

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

describe("reasoning wiring is adapter-specific", () => {
  it("claude carries reasoning in the prompt, codex carries it in a flag", () => {
    // Claude: keyword directive in the prompt; no reasoning in args.
    expect(claudeEvalProfile.reasoningPromptDirective("high")).toContain("Ultrathink");
    expect(claudeEvalProfile.buildArgs("claude-opus-4-8", "high")).not.toContain("-c");
    // Codex: flag in args; empty prompt directive.
    expect(codexEvalProfile.reasoningPromptDirective("high")).toBe("");
    const codexArgs = codexEvalProfile.buildArgs("gpt-5.5", "high");
    expect(codexArgs).toContain("-c");
    expect(codexArgs[codexArgs.indexOf("-c") + 1]).toBe("model_reasoning_effort=high");
  });

  it("codex omits the reasoning flag for none", () => {
    expect(codexEvalProfile.buildArgs("gpt-5.5", "none")).not.toContain("-c");
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
