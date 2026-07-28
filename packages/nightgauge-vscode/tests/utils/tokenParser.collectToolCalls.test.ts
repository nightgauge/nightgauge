/**
 * tokenParser.collectToolCalls.test.ts
 *
 * The shape-normalisation boundary (#169).
 *
 * Tool calls reach the pipeline in two shapes and consumers kept picking one.
 * `collectToolCalls` exists so there is no shape left to pick: everything
 * downstream — `promptDetected`, the AskUserQuestion abort, the dashboard feed,
 * `onToolUse`, the runaway monitor's signal feed — reads its output instead of
 * the raw fields. These tests pin the normalisation itself, so a future parser
 * change that drops a shape fails here rather than silently switching off four
 * features somewhere else.
 */

import { describe, expect, it } from "vitest";

import { collectToolCalls, parseStreamJsonLine } from "../../src/utils/tokenParser";

describe("parseStreamJsonLine — singular tool_use id (#169)", () => {
  const streamingLine = (block: Record<string, unknown>) =>
    JSON.stringify({ type: "content_block_start", index: 0, content_block: block });

  it("exposes the content_block id so the singular shape can be deduped", () => {
    // Without this the two shapes have no common key, and "the same call
    // reported twice" is indistinguishable from "two calls".
    const parsed = parseStreamJsonLine(
      streamingLine({
        type: "tool_use",
        id: "toolu_01ABC",
        name: "Bash",
        input: { command: "go build ./..." },
      })
    );

    expect(parsed?.toolName).toBe("Bash");
    expect(parsed?.toolUseId).toBe("toolu_01ABC");
  });

  it("omits the id rather than coercing a non-string one", () => {
    const parsed = parseStreamJsonLine(
      streamingLine({ type: "tool_use", id: 42, name: "Bash", input: { command: "echo hi" } })
    );

    expect(parsed?.toolUseId).toBeUndefined();
  });

  it("omits the id when the block carries none", () => {
    const parsed = parseStreamJsonLine(
      streamingLine({ type: "tool_use", name: "Bash", input: { command: "echo hi" } })
    );

    expect(parsed?.toolUseId).toBeUndefined();
  });
});

describe("collectToolCalls", () => {
  it("flattens the plural assistant-message shape — the one the CLI emits", () => {
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [
            { type: "text", text: "Running the suite." },
            { type: "tool_use", id: "toolu_a", name: "Bash", input: { command: "npm test" } },
            { type: "tool_use", id: "toolu_b", name: "Read", input: { file_path: "/x.ts" } },
          ],
        },
      })
    );

    expect(collectToolCalls(parsed)).toEqual([
      { name: "Bash", input: { command: "npm test" }, id: "toolu_a" },
      { name: "Read", input: { file_path: "/x.ts" }, id: "toolu_b" },
    ]);
  });

  it("flattens the singular streaming shape, id included", () => {
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "content_block_start",
        index: 0,
        content_block: {
          type: "tool_use",
          id: "toolu_c",
          name: "Edit",
          input: { file_path: "/y.ts" },
        },
      })
    );

    expect(collectToolCalls(parsed)).toEqual([
      { name: "Edit", input: { file_path: "/y.ts" }, id: "toolu_c" },
    ]);
  });

  it("returns an empty list for messages with no tool calls", () => {
    // Result/usage envelopes vastly outnumber tool calls in a stream; they must
    // not manufacture a phantom observation.
    expect(collectToolCalls(parseStreamJsonLine(JSON.stringify({ type: "result" })))).toEqual([]);
    expect(collectToolCalls(null)).toEqual([]);
  });

  it("omits the id key entirely when a call has none", () => {
    // `{id: undefined}` and no `id` key must not diverge: the dedupe set keys
    // on `id !== undefined`, so a present-but-undefined key would still be
    // correct here, but asserting the exact shape keeps consumers honest.
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [{ type: "tool_use", name: "Read", input: { file_path: "/z" } }],
        },
      })
    );

    expect(collectToolCalls(parsed)).toEqual([{ name: "Read", input: { file_path: "/z" } }]);
  });
});
