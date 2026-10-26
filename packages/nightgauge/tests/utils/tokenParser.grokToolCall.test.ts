/**
 * Grok `tool_call` lines reach the tool-call consumers (#559).
 *
 * A Grok `streaming-json` run reports tool activity as `tool_call` (at start,
 * carrying `toolName`) followed by `tool_call_update` lines (no `toolName`).
 * Before #559 neither produced a tool call, so phase inference, `onToolUse`
 * and the tool-call log were silent for Grok.
 */
import { describe, expect, it } from "vitest";

import { collectToolCalls, parseStreamJsonLine } from "../../src/utils/tokenParser";

describe("parseStreamJsonLine — Grok tool_call (#559)", () => {
  it("surfaces a mapped tool_call in the singular shape", () => {
    const line = JSON.stringify({
      type: "tool_call",
      toolCallId: "call-1",
      status: "pending",
      toolName: "run_terminal_command",
      rawInput: { command: "echo one" },
    });
    expect(collectToolCalls(parseStreamJsonLine(line))).toEqual([
      { name: "Bash", input: { command: "echo one" }, id: "call-1" },
    ]);
  });

  it("yields no tool call for tool_call_update or an unmapped tool", () => {
    const update = JSON.stringify({ type: "tool_call_update", toolCallId: "call-1" });
    const unmapped = JSON.stringify({ type: "tool_call", toolName: "web_search", rawInput: {} });
    expect(collectToolCalls(parseStreamJsonLine(update))).toEqual([]);
    expect(collectToolCalls(parseStreamJsonLine(unmapped))).toEqual([]);
  });
});
