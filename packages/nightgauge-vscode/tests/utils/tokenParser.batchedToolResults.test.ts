/**
 * tokenParser.batchedToolResults.test.ts
 *
 * Parallel tool calls come back BATCHED (#455).
 *
 * When the assistant issues several tool calls in one turn, the CLI executes
 * them together and returns every outcome in a SINGLE `user` envelope carrying
 * N `tool_result` blocks. `parseStreamJsonLine` used to `return` on the first
 * block, so results 2..N were never surfaced: their `ToolCallLog` entries kept
 * good ids (`indexed`) but never joined, and therefore carried no result, no
 * error and no `duration_ms` — which the Dashboard renders exactly like calls
 * that quietly succeeded. That is the #402 symptom on a different wire shape,
 * and `describeToolCallCorrelationGap` is structurally blind to it: arm 1 needs
 * an empty log and arm 2 needs `retainedIndexedCalls === 0`, while these calls
 * are both retained and indexed.
 *
 * Fixture provenance: complete `assistant` / `user` envelopes of the shape the
 * Claude CLI actually emits on stdout — `message.content[]` arrays, real
 * `toolu_…` ids, `parent_tool_use_id`/`session_id` siblings and all. NOT
 * `content_block_start` events: those need `--include-partial-messages`, which
 * the pipeline never passes, and building fixtures from them is precisely how a
 * shape-blind parser passes its own suite (docs/TROUBLESHOOTING.md
 * "Tool-call shape blindness", #166).
 */

import { describe, expect, it } from "vitest";

import { describeToolCallCorrelationGap, ToolCallLog } from "../../src/utils/skillRunner";
import { collectToolCalls, parseStreamJsonLine } from "../../src/utils/tokenParser";

const SESSION_ID = "7f2b0c14-9a3d-4f61-b0c2-5a8e1d33c907";

/**
 * One assistant turn that fired three tool calls in parallel — the shape that
 * produces a batched result envelope. Ids are distinct literals: `tool_${Date.now()}`
 * collides within a millisecond, which the CLI never does (#156).
 */
const ASSISTANT_PARALLEL_CALLS = JSON.stringify({
  type: "assistant",
  message: {
    id: "msg_01QhV8sJ4pWn2kLd6RtYb3Ff",
    type: "message",
    role: "assistant",
    model: "claude-opus-4-1-20250805",
    content: [
      { type: "text", text: "Checking all three at once." },
      {
        type: "tool_use",
        id: "toolu_01AqR7mZ9xKp2VbN4sTgHc1D",
        name: "Read",
        input: { file_path: "packages/nightgauge-vscode/src/utils/tokenParser.ts" },
      },
      {
        type: "tool_use",
        id: "toolu_01BwS8nY0yLq3WcM5tUhJd2E",
        name: "Bash",
        input: { command: "printf '<!-- phase:start name=\"validate\" index=2 total=5 -->\\n'" },
      },
      {
        type: "tool_use",
        id: "toolu_01CxT9oZ1zMr4XdN6uViKe3F",
        name: "Grep",
        input: { pattern: "describeToolCallCorrelationGap" },
      },
    ],
    stop_reason: "tool_use",
    stop_sequence: null,
    usage: {
      input_tokens: 6,
      cache_creation_input_tokens: 0,
      cache_read_input_tokens: 31_204,
      output_tokens: 214,
      service_tier: "standard",
    },
  },
  parent_tool_use_id: null,
  session_id: SESSION_ID,
});

/**
 * The single `user` envelope the CLI returns for those three calls: a success
 * with string content, a success with the array-of-text-blocks content shape,
 * and a failure carrying `is_error`.
 */
const USER_BATCHED_RESULTS = JSON.stringify({
  type: "user",
  message: {
    role: "user",
    content: [
      {
        type: "tool_result",
        tool_use_id: "toolu_01AqR7mZ9xKp2VbN4sTgHc1D",
        content: '     1\timport { z } from "zod";\n     2\t',
      },
      {
        type: "tool_result",
        tool_use_id: "toolu_01BwS8nY0yLq3WcM5tUhJd2E",
        content: [{ type: "text", text: '<!-- phase:start name="validate" index=2 total=5 -->\n' }],
      },
      {
        type: "tool_result",
        tool_use_id: "toolu_01CxT9oZ1zMr4XdN6uViKe3F",
        content: "No files found",
        is_error: true,
      },
    ],
  },
  parent_tool_use_id: null,
  session_id: SESSION_ID,
});

/**
 * The `runStageSkillHeadless` correlation path, reduced to the two statements
 * that matter: every tool call observed once, then every tool_result bound.
 * `resultLimit` reproduces the pre-#455 parser, which surfaced only the first
 * block — the counterfactual that keeps the assertions below non-vacuous.
 */
function correlate(lines: string[], resultLimit = Number.POSITIVE_INFINITY): ToolCallLog {
  const log = new ToolCallLog();
  for (const line of lines) {
    const parsed = parseStreamJsonLine(line);
    for (const call of collectToolCalls(parsed)) {
      log.observeToolUse(call.name, call.input, call.id);
    }
    for (const toolResult of (parsed?.toolResults ?? []).slice(0, resultLimit)) {
      log.observeToolResult(
        toolResult.toolUseId,
        toolResult.isError,
        toolResult.content.substring(0, 200)
      );
    }
  }
  return log;
}

describe("parseStreamJsonLine — batched tool_result envelopes (#455)", () => {
  it("surfaces every tool_result block in the envelope, in document order", () => {
    const parsed = parseStreamJsonLine(USER_BATCHED_RESULTS);

    expect(parsed?.type).toBe("user");
    expect(parsed?.toolResults).toEqual([
      {
        toolUseId: "toolu_01AqR7mZ9xKp2VbN4sTgHc1D",
        content: '     1\timport { z } from "zod";\n     2\t',
        isError: false,
      },
      {
        toolUseId: "toolu_01BwS8nY0yLq3WcM5tUhJd2E",
        content: '<!-- phase:start name="validate" index=2 total=5 -->\n',
        isError: false,
      },
      {
        toolUseId: "toolu_01CxT9oZ1zMr4XdN6uViKe3F",
        content: "No files found",
        isError: true,
      },
    ]);
  });

  it("keeps each block's own is_error — one failure does not colour the batch", () => {
    const results = parseStreamJsonLine(USER_BATCHED_RESULTS)?.toolResults ?? [];

    expect(results.map((r) => r.isError)).toEqual([false, false, true]);
  });

  it("omits the field entirely when no block carries a string tool_use_id", () => {
    // Absent, not `[]`: an id-less block must not manufacture a result that
    // could bind to an unrelated call (#155/#169).
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "user",
        message: { role: "user", content: [{ type: "tool_result", content: "no id here" }] },
        parent_tool_use_id: null,
        session_id: SESSION_ID,
      })
    );

    expect(parsed?.type).toBe("user");
    expect(parsed?.toolResults).toBeUndefined();
  });
});

describe("batched results reach the tool-call log (#455)", () => {
  it("joins all three parallel calls, each with its own outcome and duration", () => {
    const log = correlate([ASSISTANT_PARALLEL_CALLS, USER_BATCHED_RESULTS]);

    expect(log.size).toBe(3);
    expect(log.retainedIndexedCount).toBe(3);
    // The join count is the direct measure: 3 results bound 3 entries. Before
    // #455 this was 1.
    expect(log.correlatedResults).toBe(3);

    const entries = log.snapshot();
    expect(entries.map((e) => e.tool)).toEqual(["Read", "Bash", "Grep"]);
    // Not one entry left rendering as a quiet success: every row carries a
    // duration and exactly one of result/error.
    for (const entry of entries) {
      expect(entry.duration_ms).toBeDefined();
      expect(entry.result === undefined).not.toBe(entry.error === undefined);
    }
    expect(entries[0].result).toContain('import { z } from "zod"');
    expect(entries[1].result).toContain('phase:start name="validate"');
    expect(entries[2].error).toBe("No files found");
    expect(entries[2].result).toBeUndefined();
  });

  it("the pre-#455 first-result-only behaviour leaves two silent rows", () => {
    // The counterfactual. Without it the assertions above could pass on a log
    // that simply never observed anything.
    const log = correlate([ASSISTANT_PARALLEL_CALLS, USER_BATCHED_RESULTS], 1);

    expect(log.correlatedResults).toBe(1);
    const [, second, third] = log.snapshot();
    for (const entry of [second, third]) {
      expect(entry.duration_ms).toBeUndefined();
      expect(entry.result).toBeUndefined();
      expect(entry.error).toBeUndefined();
    }
  });

  it("no correlation-gap arm fires on the dropped-results shape — why it hid", () => {
    // Both arms of the #402 detector are blind here: the log is non-empty and
    // every retained call is indexed, so nothing reported the two rows that
    // silently lost their outcome. Fixing the parser is the only cure; a third
    // arm would have to guess at a ratio, and after #455 the remaining
    // unjoined entries are genuine kill races rather than a capture defect.
    const preFix = correlate([ASSISTANT_PARALLEL_CALLS, USER_BATCHED_RESULTS], 1);
    const gapArgs = (log: ToolCallLog) => ({
      stage: "feature-validate",
      parsedToolEventCount: 3,
      retainedCalls: log.size,
      retainedIndexedCalls: log.retainedIndexedCount,
      capturedTotal: log.capturedTotal,
      correlatedResults: log.correlatedResults,
    });

    expect(describeToolCallCorrelationGap(gapArgs(preFix))).toBeUndefined();
    expect(
      describeToolCallCorrelationGap(
        gapArgs(correlate([ASSISTANT_PARALLEL_CALLS, USER_BATCHED_RESULTS]))
      )
    ).toBeUndefined();
  });
});
