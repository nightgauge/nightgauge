/**
 * tokenParser.batchedToolResults.test.ts
 *
 * `ParsedStreamMessage.toolResults` is an ARRAY, and every consumer of it has
 * to walk the whole array (#455).
 *
 * ── What the Claude CLI actually emits ───────────────────────────────────────
 *
 * The CLI splits every content block into its own stdout event. A turn with
 * three genuinely parallel tool calls arrives as three `assistant` events (one
 * `tool_use` block each, all sharing one `message.id`) followed by three `user`
 * events (one `tool_result` block each, NOT necessarily in call order). It does
 * not batch. Verified three ways:
 *
 *   1. Live capture, Claude Code 2.1.233 — `CAPTURE_PARALLEL` /
 *      `CAPTURE_PHASE` below are those bytes, redacted and nothing else.
 *   2. This repo's own real capture,
 *      `internal/execution/testdata/claude_stream_real_capture.jsonl`: two
 *      parallel Bash calls, lines 7-8 one `tool_use` each, lines 10-11 one
 *      `tool_result` each. Its README states the rule outright — "the CLI emits
 *      one per content block".
 *   3. 580 recorded pipeline session logs under `.nightgauge/logs/`: of 92,432
 *      lines carrying a `tool_result`, zero carry two. (Those logs elide long
 *      lines at ~250 chars, so this corroborates rather than proves; the two
 *      complete captures are the dispositive evidence.)
 *
 * So the ORIGINAL framing of #455 — "the CLI batches parallel results into one
 * envelope, and results 2..N are silently dropped in production" — is FALSE.
 * Every result reaches the consumer today, joins, and gets a `duration_ms`.
 * An earlier revision of this file asserted the batched envelope was "the shape
 * the Claude CLI actually emits"; that claim was fiction of exactly the #166 /
 * #300 kind the fixture rule exists to stop, and it is retracted here.
 *
 * ── What the change is actually for ──────────────────────────────────────────
 *
 * `parseStreamJsonLine` reads an Anthropic Messages API `user` envelope, whose
 * `content` is a LIST and whose canonical representation of parallel results is
 * several `tool_result` blocks in one turn. A parser that `return`s inside the
 * loop over that list is lossy by construction: it is correct only for as long
 * as the CLI keeps splitting, which is a CLI implementation detail this repo
 * already treats as unstable ("Recapture it if the CLI's shape changes" —
 * testdata/README.md). Making the read total costs nothing and removes a silent
 * truncation; the plural name now matches `toolUses`, which is plural for
 * exactly the same reason.
 *
 * That is a robustness guarantee, not a bug fix, and the tests are split to say
 * so. `CAPTURE_*` tests pin the shape production actually runs on.
 * `BATCHED_*_ENVELOPE` tests pin the array contract — and they are the only
 * thing that can, since no real envelope has a second block to lose. Both
 * batched envelopes are built by folding the VERBATIM captured blocks into one
 * `message.content`; only the grouping is synthetic, and it is labelled
 * synthetic wherever it is used.
 *
 * Every test below drives the real `runStageSkillHeadless` stdout handler
 * except the four that name `parseStreamJsonLine` directly. Simulating the
 * consumer with a local `correlate()` helper — which this file used to do —
 * pins nothing: the defect can be reintroduced verbatim at
 * `skillRunner.ts` and the whole suite stays green.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";

import { parseStreamJsonLine } from "../../src/utils/tokenParser";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(
    () => `---
name: test-skill
allowed-tools: Read Write Edit Bash
---
# Test Skill
`
  ),
}));

vi.mock("child_process", async () => {
  // Since #79 the extension composes no skill text of its own: it shells out
  // to `nightgauge skill render`. Answer that one call with the shared
  // envelope stub; every other execFileSync caller keeps an empty result.
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    spawn: vi.fn(),
    execFileSync: vi.fn((_cmd: string, args: string[]) =>
      isSkillRenderCall(args) ? skillRenderStdout(args) : ""
    ),
    execFile: vi.fn(
      (
        _cmd: string,
        _args: string[],
        _opts: unknown,
        cb: (e: Error | null, s: string, t: string) => void
      ) => {
        cb(new Error("no children"), "", "");
      }
    ),
  };
});

import { runStageSkillHeadless, type ToolCallLogEntry } from "../../src/utils/skillRunner";

// ─────────────────────────── captured stdout ────────────────────────────────
//
// Live capture, Claude Code 2.1.233, `claude-haiku-4-5-20251001`:
//
//   claude -p "In a SINGLE message, make three parallel Bash tool calls at the
//   same time: one running 'echo one', one running 'echo two', and one running
//   'cat /nonexistent-file-xyz'. Do not run them sequentially. Then reply
//   done." --output-format stream-json --verbose \
//     --model claude-haiku-4-5-20251001 --allowedTools Bash
//
// The six event lines below are that run's stdout byte-for-byte, in emission
// order, with only `session_id` / `uuid` / `request_id` rewritten to stable
// placeholders (the shape-preserving redaction `internal/execution/testdata`
// already uses). Nothing else is touched: `stop_reason` really is `null` on a
// tool_use event, `is_error` really is present on successes, key order really
// does differ between the two success blocks and the error block, and the
// `caller` / `stop_details` / `tool_use_result` siblings are all the CLI's.
const CAPTURE_PARALLEL = String.raw`
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_011Ce6oAcaRkx1C8x1QMmTJc","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01RtzAqnPMXnG5YwXNJb4WhM","name":"Bash","input":{"command":"echo one","description":"Echo one"},"caller":{"type":"direct"}}],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":3239,"cache_read_input_tokens":13851,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3239},"output_tokens":4,"service_tier":"standard","inference_geo":"not_available"},"diagnostics":null,"context_management":null},"parent_tool_use_id":null,"session_id":"sess-parallel-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:09:21.877Z","request_id":"req_REDACTED"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_011Ce6oAcaRkx1C8x1QMmTJc","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01QLNdJUL6FhhtcnvTvTzxLd","name":"Bash","input":{"command":"echo two","description":"Echo two"},"caller":{"type":"direct"}}],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":3239,"cache_read_input_tokens":13851,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3239},"output_tokens":4,"service_tier":"standard","inference_geo":"not_available"},"diagnostics":null,"context_management":null},"parent_tool_use_id":null,"session_id":"sess-parallel-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:09:22.194Z","request_id":"req_REDACTED"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_011Ce6oAcaRkx1C8x1QMmTJc","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01U4HbBvy74bTRBeNW2XBddw","name":"Bash","input":{"command":"cat /nonexistent-file-xyz","description":"Cat nonexistent file"},"caller":{"type":"direct"}}],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":3239,"cache_read_input_tokens":13851,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3239},"output_tokens":4,"service_tier":"standard","inference_geo":"not_available"},"diagnostics":null,"context_management":null},"parent_tool_use_id":null,"session_id":"sess-parallel-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:09:22.543Z","request_id":"req_REDACTED"}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01RtzAqnPMXnG5YwXNJb4WhM","type":"tool_result","content":"one","is_error":false}]},"parent_tool_use_id":null,"session_id":"sess-parallel-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:09:23.616Z","tool_use_result":{"stdout":"one","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01QLNdJUL6FhhtcnvTvTzxLd","type":"tool_result","content":"two","is_error":false}]},"parent_tool_use_id":null,"session_id":"sess-parallel-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:09:23.619Z","tool_use_result":{"stdout":"two","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"Exit code 1\ncat: /nonexistent-file-xyz: No such file or directory","is_error":true,"tool_use_id":"toolu_01U4HbBvy74bTRBeNW2XBddw"}]},"parent_tool_use_id":null,"session_id":"sess-parallel-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:09:23.623Z","tool_use_result":"Error: Exit code 1\ncat: /nonexistent-file-xyz: No such file or directory"}
`
  .trim()
  .split("\n");

// Same CLI and flags, two parallel Bash calls that each `printf` a phase
// marker. Kept separate because it carries a fact the first capture does not:
// the results come back in the OPPOSITE order to the calls (`verify` before
// `build`), which is why correlation is by id and never by recency.
const CAPTURE_PHASE = String.raw`
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_011Ce6ocx1SrxwaqFf1eUnjj","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_012do5Z799oyEA7e8v41vWMB","name":"Bash","input":{"command":"printf '<!-- phase:start name=\"build\" index=1 total=3 stage=\"feature-validate\" -->\\n'","description":"Print phase start marker for build"},"caller":{"type":"direct"}}],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":3269,"cache_read_input_tokens":13851,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3269},"output_tokens":4,"service_tier":"standard","inference_geo":"not_available"},"diagnostics":null,"context_management":null},"parent_tool_use_id":null,"session_id":"sess-phase-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:15:19.892Z","request_id":"req_REDACTED"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_011Ce6ocx1SrxwaqFf1eUnjj","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_0155EMVoQQQgsgmpqXhe46Fy","name":"Bash","input":{"command":"printf '<!-- phase:start name=\"verify\" index=2 total=3 stage=\"feature-validate\" -->\\n'","description":"Print phase start marker for verify"},"caller":{"type":"direct"}}],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":3269,"cache_read_input_tokens":13851,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3269},"output_tokens":4,"service_tier":"standard","inference_geo":"not_available"},"diagnostics":null,"context_management":null},"parent_tool_use_id":null,"session_id":"sess-phase-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:15:20.360Z","request_id":"req_REDACTED"}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_0155EMVoQQQgsgmpqXhe46Fy","type":"tool_result","content":"<!-- phase:start name=\"verify\" index=2 total=3 stage=\"feature-validate\" -->","is_error":false}]},"parent_tool_use_id":null,"session_id":"sess-phase-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:15:21.828Z","tool_use_result":{"stdout":"<!-- phase:start name=\"verify\" index=2 total=3 stage=\"feature-validate\" -->","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_012do5Z799oyEA7e8v41vWMB","type":"tool_result","content":"<!-- phase:start name=\"build\" index=1 total=3 stage=\"feature-validate\" -->","is_error":false}]},"parent_tool_use_id":null,"session_id":"sess-phase-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:15:21.829Z","tool_use_result":{"stdout":"<!-- phase:start name=\"build\" index=1 total=3 stage=\"feature-validate\" -->","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}}
`
  .trim()
  .split("\n");

// SYNTHETIC GROUPING — not a captured line. The three `tool_result` blocks are
// lifted verbatim out of CAPTURE_PARALLEL's three `user` events and folded into
// one `message.content`, i.e. the Messages API's own representation of a
// parallel-tool turn. The CLI does not currently emit this (see the header);
// it exists solely to give the array contract a second element to lose.
const BATCHED_RESULTS_ENVELOPE = String.raw`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01RtzAqnPMXnG5YwXNJb4WhM","type":"tool_result","content":"one","is_error":false},{"tool_use_id":"toolu_01QLNdJUL6FhhtcnvTvTzxLd","type":"tool_result","content":"two","is_error":false},{"type":"tool_result","content":"Exit code 1\ncat: /nonexistent-file-xyz: No such file or directory","is_error":true,"tool_use_id":"toolu_01U4HbBvy74bTRBeNW2XBddw"}]},"parent_tool_use_id":null,"session_id":"sess-parallel-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:09:23.623Z"}`;

// SYNTHETIC GROUPING, same construction, over CAPTURE_PHASE's two blocks.
const BATCHED_PHASE_ENVELOPE = String.raw`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_0155EMVoQQQgsgmpqXhe46Fy","type":"tool_result","content":"<!-- phase:start name=\"verify\" index=2 total=3 stage=\"feature-validate\" -->","is_error":false},{"tool_use_id":"toolu_012do5Z799oyEA7e8v41vWMB","type":"tool_result","content":"<!-- phase:start name=\"build\" index=1 total=3 stage=\"feature-validate\" -->","is_error":false}]},"parent_tool_use_id":null,"session_id":"sess-phase-0000","uuid":"00000000-0000-0000-0000-000000000000","timestamp":"2026-08-16T17:15:21.829Z"}`;

// ───────────────────────────── harness ──────────────────────────────────────

function createMockChildProcess(): ChildProcess {
  const proc = new EventEmitter() as ChildProcess;
  proc.stdout = new EventEmitter() as NodeJS.ReadableStream;
  proc.stderr = new EventEmitter() as NodeJS.ReadableStream;
  proc.stdin = {
    write: vi.fn(),
    end: vi.fn(),
    destroyed: false,
  } as unknown as NodeJS.WritableStream;
  proc.kill = vi.fn();
  proc.killed = false;
  return proc;
}

interface StageObservation {
  /** The wire-shape `tool_calls` array this stage would forward to Go. */
  toolCalls: ToolCallLogEntry[];
  phases: { name: string; index: number; total: number }[];
}

function expectDefined<T>(value: T | undefined, what: string): T {
  if (value === undefined) throw new Error(`expected ${what} to be defined`);
  return value;
}

describe("toolResults — the array contract, end to end (#455)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  /**
   * Drive the REAL stdout handler inside `runStageSkillHeadless` with the given
   * stream lines and return what its consumers recorded.
   *
   * This is the point of the file. `toolCalls` is the tool-call log the stage
   * forwards to Go over `pipeline.stageResult`, and `phases` is the
   * `onPhaseStart` channel the runaway monitor feeds from — both populated by
   * the two loops over `parsed.toolResults` in `skillRunner.ts`, and neither
   * reachable from a test that re-implements correlation locally.
   */
  async function runStage(lines: string[]): Promise<StageObservation> {
    const phases: StageObservation["phases"] = [];
    let result: { toolCalls?: ToolCallLogEntry[] } | undefined;

    runStageSkillHeadless("feature-validate", 455, {
      onPhaseStart: (_stage, name, index, total) => phases.push({ name, index, total }),
      onComplete: (r) => {
        result = r;
      },
    });

    for (const line of lines) {
      mockProcess.stdout!.emit("data", Buffer.from(line + "\n"));
    }
    mockProcess.emit("close", 0);
    await new Promise((resolve) => setTimeout(resolve, 10));

    const completed = expectDefined(result, "onComplete result");
    return { toolCalls: expectDefined(completed.toolCalls, "toolCalls"), phases };
  }

  // ─────────── the shape production actually runs on ───────────

  describe("one tool_result per user event — the captured CLI shape", () => {
    it("captures a turn whose parallel results arrive one per envelope", () => {
      // Guards the provenance claim itself. If a future CLI starts batching,
      // this is the test that fails and sends someone back to recapture,
      // rather than the fiction surviving in a comment.
      const userEvents = CAPTURE_PARALLEL.filter((l) => JSON.parse(l).type === "user");
      expect(userEvents).toHaveLength(3);
      for (const line of userEvents) {
        expect(parseStreamJsonLine(line)?.toolResults).toHaveLength(1);
      }
      // ... from ONE assistant turn, split across three events.
      const assistantEvents = CAPTURE_PARALLEL.filter((l) => JSON.parse(l).type === "assistant");
      expect(assistantEvents).toHaveLength(3);
      expect(new Set(assistantEvents.map((l) => JSON.parse(l).message.id)).size).toBe(1);
      // `stop_reason` is null on a tool_use event, not "tool_use".
      expect(JSON.parse(assistantEvents[0]).message.stop_reason).toBeNull();
    });

    it("joins all three parallel calls, each with its own outcome and duration", async () => {
      const { toolCalls } = await runStage(CAPTURE_PARALLEL);

      expect(toolCalls).toHaveLength(3);
      expect(toolCalls.map((c) => c.target)).toEqual([
        "echo one",
        "echo two",
        "cat /nonexistent-file-xyz",
      ]);
      // Not one row left rendering as a quiet success: every row carries a
      // duration and exactly one of result/error.
      for (const call of toolCalls) {
        expect(call.duration_ms).toBeDefined();
        expect(call.result === undefined).not.toBe(call.error === undefined);
      }
      expect(toolCalls[0].result).toBe("one");
      expect(toolCalls[1].result).toBe("two");
      expect(toolCalls[2].error).toContain("No such file or directory");
      expect(toolCalls[2].result).toBeUndefined();
    });

    it("binds out-of-order results to their own call, and surfaces both markers", async () => {
      // CAPTURE_PHASE's results arrive reversed. Correlation is by id, so both
      // land correctly — and both markers reach the phase channel, which is
      // what stops the runaway monitor starving (#161).
      const { toolCalls, phases } = await runStage(CAPTURE_PHASE);

      expect(toolCalls).toHaveLength(2);
      expect(toolCalls[0].result).toContain('name="build"');
      expect(toolCalls[1].result).toContain('name="verify"');
      expect(phases).toEqual(
        expect.arrayContaining([
          { name: "verify", index: 2, total: 3 },
          { name: "build", index: 1, total: 3 },
        ])
      );
    });
  });

  // ─────────── the array contract, on the synthetic grouping ───────────

  describe("a batched envelope must not be truncated (transport robustness)", () => {
    it("parses every block in document order, each keeping its own is_error", () => {
      const results = parseStreamJsonLine(BATCHED_RESULTS_ENVELOPE)?.toolResults;

      expect(results).toEqual([
        {
          toolUseId: "toolu_01RtzAqnPMXnG5YwXNJb4WhM",
          content: "one",
          isError: false,
        },
        {
          toolUseId: "toolu_01QLNdJUL6FhhtcnvTvTzxLd",
          content: "two",
          isError: false,
        },
        {
          toolUseId: "toolu_01U4HbBvy74bTRBeNW2XBddw",
          content: "Exit code 1\ncat: /nonexistent-file-xyz: No such file or directory",
          isError: true,
        },
      ]);
    });

    it("omits the field entirely when no block carries a string tool_use_id", () => {
      // Absent, not `[]`: an id-less block must not manufacture a result that
      // could bind to an unrelated call (#155/#169).
      const parsed = parseStreamJsonLine(
        JSON.stringify({
          type: "user",
          message: { role: "user", content: [{ type: "tool_result", content: "no id here" }] },
          parent_tool_use_id: null,
          session_id: "sess-parallel-0000",
        })
      );

      expect(parsed?.type).toBe("user");
      expect(parsed?.toolResults).toBeUndefined();
    });

    it("the tool-call log joins every block, not just the first", async () => {
      // THE consumer-loop pin. Reintroducing #455 at the tool-call channel —
      // `for (const toolResult of (parsed?.toolResults ?? []).slice(0, 1))` —
      // leaves calls 2 and 3 indexed but unjoined, which is precisely the
      // "quiet success" row the Dashboard cannot distinguish from a real one.
      // Nothing else in the suite fails on that mutation.
      const { toolCalls } = await runStage([
        ...CAPTURE_PARALLEL.filter((l) => JSON.parse(l).type === "assistant"),
        BATCHED_RESULTS_ENVELOPE,
      ]);

      expect(toolCalls).toHaveLength(3);
      for (const call of toolCalls) {
        expect(call.duration_ms).toBeDefined();
        expect(call.result === undefined).not.toBe(call.error === undefined);
      }
      expect(toolCalls.map((c) => c.result)).toEqual(["one", "two", undefined]);
      expect(toolCalls[2].error).toContain("No such file or directory");
    });

    it("the phase channel reads every block, not just the first", async () => {
      // The second consumer loop, and the same mutation applies to it
      // independently: markers printed by blocks 2..N are a PRODUCTIVE signal
      // for the runaway monitor, and losing them is how a stage that is walking
      // its phases gets killed for "no productive progress" (#161).
      const { phases } = await runStage([
        ...CAPTURE_PHASE.filter((l) => JSON.parse(l).type === "assistant"),
        BATCHED_PHASE_ENVELOPE,
      ]);

      expect(phases).toEqual(
        expect.arrayContaining([
          { name: "verify", index: 2, total: 3 },
          { name: "build", index: 1, total: 3 },
        ])
      );
    });
  });
});
