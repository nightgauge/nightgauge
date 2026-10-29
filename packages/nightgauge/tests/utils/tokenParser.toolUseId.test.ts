import { describe, expect, it } from "vitest";

import { collectToolCalls, parseStreamJsonLine } from "../../src/utils/tokenParser";

/**
 * `last_bash_exit` is the companion anchor to `last_bash_command`: it answers
 * "did the thing it died during actually fail?". Correlating it requires the
 * `tool_use` id, because the matching `tool_result` arrives later, in a
 * separate user message.
 *
 * The parser collected `{ name, input }` and dropped `block.id`, so the plural
 * assistant-message shape — the runtime shape — had no key to correlate on.
 * The capture therefore recorded WHAT ran but never whether it succeeded, and
 * `last_bash_exit` was unpopulated in the shape that produces ~all traffic.
 *
 * This is the same defect class as #147 (shape blindness) one layer down:
 * the field is present on the wire and discarded at a parse boundary. Real
 * CLI transcripts carry it — e.g. `toolu_016inVa5TYCHMyWHqf97bBqu`.
 */
describe("parseStreamJsonLine — tool_use id propagation", () => {
  const line = (blocks: unknown[]) =>
    JSON.stringify({
      type: "assistant",
      message: { role: "assistant", content: blocks },
    });

  it("propagates the tool_use id so a later tool_result can be correlated", () => {
    const parsed = parseStreamJsonLine(
      line([
        {
          type: "tool_use",
          id: "toolu_016inVa5TYCHMyWHqf97bBqu",
          name: "Bash",
          input: { command: "flutter test integration_test/" },
        },
      ])
    );

    expect(parsed?.toolUses).toHaveLength(1);
    // The correlation key. Without it, last_bash_exit can never populate.
    expect(parsed?.toolUses?.[0].id).toBe("toolu_016inVa5TYCHMyWHqf97bBqu");
  });

  it("keeps each id with its own call when one message carries several", () => {
    // Misattribution is the failure mode that matters here: binding a result to
    // the wrong command would report a passing exit for a command that failed.
    const parsed = parseStreamJsonLine(
      line([
        { type: "tool_use", id: "toolu_first", name: "Bash", input: { command: "go build ./..." } },
        { type: "tool_use", id: "toolu_second", name: "Read", input: { file_path: "/tmp/x" } },
        { type: "tool_use", id: "toolu_third", name: "Bash", input: { command: "go test ./..." } },
      ])
    );

    expect(parsed?.toolUses?.map((t) => [t.name, t.id])).toEqual([
      ["Bash", "toolu_first"],
      ["Read", "toolu_second"],
      ["Bash", "toolu_third"],
    ]);
  });

  it("omits id rather than inventing one when the block has none", () => {
    // A synthesised or malformed block must leave the exit code unknown. An
    // empty-string id would compare equal to another absent id and bind a
    // result to an unrelated call — worse than reporting nothing.
    const parsed = parseStreamJsonLine(
      line([{ type: "tool_use", name: "Bash", input: { command: "echo hi" } }])
    );

    expect(parsed?.toolUses).toHaveLength(1);
    expect(parsed?.toolUses?.[0].id).toBeUndefined();
  });

  it("ignores a non-string id instead of passing it through", () => {
    const parsed = parseStreamJsonLine(
      line([{ type: "tool_use", id: 42, name: "Bash", input: { command: "echo hi" } }])
    );

    expect(parsed?.toolUses?.[0].id).toBeUndefined();
  });
});

describe("parseStreamJsonLine — parent_tool_use_id propagation (#2660)", () => {
  it("puts a sub-agent message's parent_tool_use_id on each of its tool calls", () => {
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "assistant",
        parent_tool_use_id: "toolu_task",
        message: {
          role: "assistant",
          content: [
            { type: "tool_use", id: "a", name: "Write", input: { file_path: "x" } },
            { type: "tool_use", id: "b", name: "Bash", input: { command: "ls" } },
          ],
        },
      })
    );
    expect(parsed?.toolUses?.map((t) => t.parentToolUseId)).toEqual(["toolu_task", "toolu_task"]);
  });

  it("carries parent_tool_use_id on a sub-agent message with no tool_use", () => {
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "assistant",
        parent_tool_use_id: "toolu_task",
        message: { role: "assistant", content: [{ type: "text", text: "done" }] },
      })
    );
    expect(parsed?.parentToolUseId).toBe("toolu_task");
  });

  it("leaves top-level calls without a parent", () => {
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "assistant",
        parent_tool_use_id: null,
        message: {
          role: "assistant",
          content: [{ type: "tool_use", id: "a", name: "Task", input: {} }],
        },
      })
    );
    expect(parsed?.toolUses?.[0]).not.toHaveProperty("parentToolUseId");
  });

  it("carries parent_tool_use_id on the singular content_block_start shape", () => {
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "content_block_start",
        parent_tool_use_id: "toolu_task",
        content_block: { type: "tool_use", id: "a", name: "Write", input: {} },
      })
    );
    expect(collectToolCalls(parsed)).toEqual([
      { name: "Write", input: {}, id: "a", parentToolUseId: "toolu_task" },
    ]);
  });
});
