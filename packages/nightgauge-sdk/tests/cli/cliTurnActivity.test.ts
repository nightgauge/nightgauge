/**
 * Turn boundaries the SDK stage CLI forwards for codex, gemini and grok
 * (#1668), so the extension's stage turn budget counts them as the Go
 * executor does (internal/execution/stage_budget.go countTurn).
 */
import { describe, it, expect } from "vitest";
import {
  applyStageTurnBudget,
  forwardCliTurnActivity,
  stageBudgetMaxTurns,
} from "../../src/cli/adapters/cliQueryHelper.js";
import type { AdapterActivity, NightgaugeAdapter } from "../../src/cli/adapters/ICliAdapter.js";

function forward(adapter: NightgaugeAdapter, lines: string[]): AdapterActivity[] {
  const got: AdapterActivity[] = [];
  for (const line of lines) forwardCliTurnActivity(adapter, line, (a) => got.push(a));
  return got;
}

describe("forwardCliTurnActivity (#1668)", () => {
  it("codex: a completed non-message item is a turn; the agent's message and reasoning are not", () => {
    const got = forward("codex", [
      '{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}',
      '{"type":"item.completed","item":{"type":"agent_message","text":"secret"}}',
      '{"type":"item.completed","item":{"type":"reasoning","text":"secret"}}',
      '{"type":"item.completed","item":{"type":"file_change"}}',
      '{"type":"turn.completed","usage":{"input_tokens":1}}',
    ]);
    expect(got).toEqual([
      { adapter: "codex", event: "item.completed", turn: true, asksAnother: true },
      { adapter: "codex", event: "item.completed", turn: true, asksAnother: true },
    ]);
    expect(JSON.stringify(got)).not.toContain("secret");
  });

  it("gemini: each tool_use is a turn", () => {
    expect(
      forward("gemini", [
        '{"type":"tool_use","tool_name":"read_file"}',
        '{"type":"message","content":"hi"}',
      ])
    ).toEqual([{ adapter: "gemini", event: "tool_use", turn: true, asksAnother: true }]);
  });

  it("grok: a usage event is a turn and a tool_call asks for another", () => {
    expect(
      forward("grok", ['{"type":"tool_call","name":"bash"}', '{"type":"usage","input_tokens":5}'])
    ).toEqual([
      { adapter: "grok", event: "tool_call", asksAnother: true },
      { adapter: "grok", event: "usage", turn: true, asksAnother: false },
    ]);
  });

  it("copilot prints no turn boundary, so nothing is forwarded; bad lines are ignored", () => {
    expect(forward("copilot", ['{"type":"tool_use"}', "plain text"])).toEqual([]);
    expect(forward("codex", ['{"type":"item.completed"', "not json"])).toEqual([]);
    expect(() =>
      forwardCliTurnActivity("gemini", '{"type":"tool_use"}', () => {
        throw new Error("boom");
      })
    ).not.toThrow();
  });
});

describe("stageBudgetMaxTurns (#1668)", () => {
  it("reads a positive integer only", () => {
    expect(stageBudgetMaxTurns({ NIGHTGAUGE_STAGE_MAX_TURNS: "17" })).toBe(17);
    expect(stageBudgetMaxTurns({ NIGHTGAUGE_STAGE_MAX_TURNS: "0" })).toBeUndefined();
    expect(stageBudgetMaxTurns({ NIGHTGAUGE_STAGE_MAX_TURNS: "-1" })).toBeUndefined();
    expect(stageBudgetMaxTurns({})).toBeUndefined();
  });
});

describe("applyStageTurnBudget (#1668)", () => {
  it("lowers grok's --max-turns to the budget, never raises it, and adds it when absent", () => {
    const args = ["--output-format", "streaming-json", "--max-turns", "200"];
    applyStageTurnBudget(args, { NIGHTGAUGE_STAGE_MAX_TURNS: "40" });
    expect(args).toEqual(["--output-format", "streaming-json", "--max-turns", "40"]);
    applyStageTurnBudget(args, { NIGHTGAUGE_STAGE_MAX_TURNS: "400" });
    expect(args[3]).toBe("40");
    const bare = ["--json"];
    applyStageTurnBudget(bare, { NIGHTGAUGE_STAGE_MAX_TURNS: "9" });
    expect(bare).toEqual(["--json", "--max-turns", "9"]);
    const none = ["--max-turns", "200"];
    applyStageTurnBudget(none, {});
    expect(none).toEqual(["--max-turns", "200"]);
  });
});
