/**
 * skillRunner.stageExitSelfChecks.test.ts
 *
 * The stage-exit forensic self-checks, end to end (#456).
 *
 * The two detectors (`describeForensicCaptureGap`, #147/#302, and
 * `describeToolCallCorrelationGap`, #402) were already unit-tested. The code
 * that CALLED them was not: it lived inline in `runStageSkillHeadless`'s close
 * handler, which spawns a subprocess. Mutation testing during #402's review
 * showed the consequence — deleting either emission block wholesale passed the
 * FULL package suite, and so did swapping `retainedIndexedCalls` for the log's
 * `size`, which suppresses the #402 warning in exactly the shape it exists to
 * report. A detector nothing calls is as silent as one that does not exist.
 *
 * So this file pins the three things the detectors' own tests structurally
 * cannot see:
 *
 *   1. the ARGUMENT MAPPING, through the real exported
 *      `composeStageExitSelfChecks` — including that the #402 arm is fed the
 *      log's `retainedIndexedCount` and never its `size`;
 *   2. the DELIVERY — that a stage which ends in the id-less shape actually
 *      ships both warnings in the exit record's `stderr_tail`, driven through
 *      the real runner in the bytes the Claude CLI emits, and that they
 *      survive the `STAGE_EXIT_STDERR_TAIL_MAX` cap on a stage whose tail is
 *      already full (appendTail truncates from the FRONT);
 *   3. the byId EVICTION SWEEP in both correlating classes — a result
 *      delivered for an already-evicted id must be DROPPED, not joined. It was
 *      unpinned (mutant M5 survived) and the delta is real: unbounded `byId`
 *      growth plus a late join incrementing the lifetime correlation tally for
 *      an entry the record does not carry, which is the one number the #302 /
 *      #402 reports quote as evidence that correlation ever worked.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";

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

import {
  runStageSkillHeadless,
  composeStageExitSelfChecks,
  appendTail,
  RecentBashRing,
  RECENT_BASH_MAX_ENTRIES,
  ToolCallLog,
  TOOL_CALL_LOG_MAX_ENTRIES,
  STAGE_EXIT_STDERR_TAIL_MAX,
} from "../../src/utils/skillRunner";
import { createMockChildProcess } from "../mocks/child-process";

/** Marker every self-check line carries, so log greps keep working. */
const GAP_PREFIX = "[forensic-capture-gap]";

/** A ring in the #302 shape: commands retained, NONE of them indexed. */
function idLessRing(count: number): RecentBashRing {
  const ring = new RecentBashRing();
  for (let i = 0; i < count; i++) ring.observeToolUse("Bash", { command: `echo ${i}` });
  return ring;
}

/** A log in the #402 shape: calls retained, NONE of them indexed. */
function idLessLog(count: number): ToolCallLog {
  const log = new ToolCallLog();
  for (let i = 0; i < count; i++) log.observeToolUse("Read", { file_path: `/f/${i}` });
  return log;
}

/**
 * A ring/log with one properly indexed entry — the healthy shape.
 *
 * Used as the OTHER side of a single-arm case. An EMPTY ring or log is not
 * neutral: with tool events parsed, empty is itself arm 1 of its own detector
 * (#147 / #402), so pairing an id-less log with an empty ring reports two
 * gaps and says nothing about which detector produced which line.
 */
function healthyRing(): RecentBashRing {
  const ring = new RecentBashRing();
  ring.observeToolUse("Bash", { command: "npm test" }, "toolu_ok");
  return ring;
}

function healthyLog(): ToolCallLog {
  const log = new ToolCallLog();
  log.observeToolUse("Bash", { command: "npm test" }, "toolu_ok");
  return log;
}

describe("composeStageExitSelfChecks — the runner's argument mapping (#456)", () => {
  it("emits the #302 ring warning with the runner's argument mapping", () => {
    // 12 id-less Bash commands: 10 retained (the ring's window), 12 captured
    // over the stage's life, 0 indexed, 0 correlated. Every one of those four
    // numbers is a DIFFERENT property, and the message quotes three of them —
    // so a mapping that conflated the window with the workload is visible here.
    const ring = idLessRing(RECENT_BASH_MAX_ENTRIES + 2);
    expect(ring.size).toBe(RECENT_BASH_MAX_ENTRIES);
    expect(ring.capturedTotal).toBe(RECENT_BASH_MAX_ENTRIES + 2);

    const checks = composeStageExitSelfChecks({
      stage: "feature-validate",
      parsedToolEventCount: 37,
      ring,
      log: healthyLog(),
    });

    expect(checks).toHaveLength(1);
    const warning = checks[0];
    expect(warning).toContain(GAP_PREFIX);
    expect(warning).toContain("Stage feature-validate");
    expect(warning).toContain("parsed 37 tool event(s)");
    // capturedTotal (12), NOT the retained window (10).
    expect(warning).toContain(`captured ${RECENT_BASH_MAX_ENTRIES + 2} Bash command(s)`);
    expect(warning).toContain("(0 exit(s) correlated over the stage)");
    // the retained window (10), NOT the workload.
    expect(warning).toContain(`NONE of the ${RECENT_BASH_MAX_ENTRIES} command(s) retained`);
    expect(warning).toContain("(Issue #302)");
  });

  it("emits the #402 log warning with retainedIndexedCalls from the log, not its size", () => {
    // The mutation this pins: `retainedIndexedCalls: toolCallLog.size`. The
    // log below is built so the two disagree — size is 5, retainedIndexedCount
    // is 0 — which is precisely the shape the #402 arm reports on. Fed `size`,
    // the arm's `=== 0` predicate is false and the warning vanishes: the
    // record ships id-less rows that render as calls which quietly succeeded,
    // and nothing says so.
    const log = idLessLog(5);
    expect(log.size).toBe(5);
    expect(log.retainedIndexedCount).toBe(0);
    expect(log.size).not.toBe(log.retainedIndexedCount);

    const checks = composeStageExitSelfChecks({
      stage: "feature-dev",
      parsedToolEventCount: 9,
      ring: healthyRing(),
      log,
    });

    expect(checks).toHaveLength(1);
    const warning = checks[0];
    expect(warning).toContain(GAP_PREFIX);
    expect(warning).toContain("Stage feature-dev");
    expect(warning).toContain("parsed 9 tool event(s)");
    expect(warning).toContain("captured 5 tool call(s)");
    expect(warning).toContain("(0 result(s) correlated over the stage)");
    expect(warning).toContain("NONE of the 5 tool call(s) retained");
    expect(warning).toContain("(Issue #402)");
  });

  it("emits both warnings from one exit", () => {
    // An id-less stage breaks BOTH correlators at once — that is the realistic
    // shape, not two independent defects — so the exit must carry both lines.
    const checks = composeStageExitSelfChecks({
      stage: "feature-dev",
      parsedToolEventCount: 4,
      ring: idLessRing(2),
      log: idLessLog(3),
    });

    expect(checks).toHaveLength(2);
    expect(checks[0]).toContain("(Issue #302)");
    expect(checks[1]).toContain("(Issue #402)");
    for (const line of checks) {
      expect(line.startsWith(GAP_PREFIX)).toBe(true);
      expect(line.endsWith("\n")).toBe(true);
    }
  });

  it("stays silent on a healthy stage, and on a stage that parsed no tool events", () => {
    expect(
      composeStageExitSelfChecks({
        stage: "feature-validate",
        parsedToolEventCount: 1,
        ring: healthyRing(),
        log: healthyLog(),
      })
    ).toEqual([]);

    // parsedToolEventCount === 0 is a different (already-reported) condition:
    // a stage that genuinely did nothing, not a capture gap.
    expect(
      composeStageExitSelfChecks({
        stage: "feature-validate",
        parsedToolEventCount: 0,
        ring: idLessRing(3),
        log: idLessLog(3),
      })
    ).toEqual([]);
  });
});

describe("stage-exit self-checks reach the exit record (#456)", () => {
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
   * A complete `assistant` message — the shape the Claude CLI actually emits.
   * The tool_use carries NO id, which is the whole point: that is the shape
   * both correlators are blind to and both self-checks exist to report.
   */
  function emitIdLessBashCall(command: string): void {
    const message = JSON.stringify({
      type: "assistant",
      message: {
        id: `msg_${command.replace(/\W/g, "")}`,
        type: "message",
        role: "assistant",
        model: "claude-opus-4-20250514",
        content: [{ type: "tool_use", name: "Bash", input: { command } }],
        stop_reason: null,
        stop_sequence: null,
        usage: {
          input_tokens: 4,
          cache_creation_input_tokens: 0,
          cache_read_input_tokens: 14203,
          output_tokens: 1,
        },
      },
      parent_tool_use_id: null,
      session_id: "sess_test",
    });
    mockProcess.stdout!.emit("data", Buffer.from(message + "\n"));
  }

  async function runIdLessStage(prefillStderr?: string): Promise<string> {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-validate", 456, { onComplete });

    emitIdLessBashCall("npm run build");
    emitIdLessBashCall("npm test");

    if (prefillStderr !== undefined) {
      mockProcess.stderr!.emit("data", Buffer.from(prefillStderr));
    }

    mockProcess.emit("close", 0);
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(onComplete).toHaveBeenCalled();
    const result = onComplete.mock.calls[0][0] as { stderrTail?: string };
    return result.stderrTail ?? "";
  }

  it("ships both warnings in the exit record's stderr tail", async () => {
    const tail = await runIdLessStage();

    // Not "onStderr was called" — the callback notifies the CALLER and does
    // not write the ring, which is the exact hole the append closes. Assert on
    // the field the Go scheduler persists.
    expect(tail).toContain("(Issue #302)");
    expect(tail).toContain("(Issue #402)");
    expect(tail.split(GAP_PREFIX)).toHaveLength(3);
  });

  it("keeps both warnings when the tail is already full (they survive the cap)", async () => {
    // A debug-logging stage fills the 4 KB ring long before it exits. The
    // warnings are appended LAST, and appendTail truncates from the FRONT, so
    // the newest bytes — the evidence of the record's own incompleteness —
    // are the ones that survive. Truncating from the back would drop exactly
    // the lines this feature exists to ship, and the record would once again
    // look healthy and terse.
    const filler = "F".repeat(STAGE_EXIT_STDERR_TAIL_MAX);
    const tail = await runIdLessStage(filler);

    expect(tail.length).toBeLessThanOrEqual(STAGE_EXIT_STDERR_TAIL_MAX);
    expect(tail).toContain("(Issue #302)");
    expect(tail).toContain("(Issue #402)");
    // The front is what got cut: the tail can no longer be all filler.
    expect(tail.startsWith(filler)).toBe(false);
    expect(tail.endsWith("F")).toBe(false);
  });

  it("appendTail cuts the front, so a full ring still ends with what was just appended", () => {
    const full = "F".repeat(STAGE_EXIT_STDERR_TAIL_MAX);
    const appended = composeStageExitSelfChecks({
      stage: "feature-validate",
      parsedToolEventCount: 2,
      ring: idLessRing(2),
      log: idLessLog(2),
    }).join("");

    const result = appendTail(full, appended, STAGE_EXIT_STDERR_TAIL_MAX);

    expect(result.length).toBe(STAGE_EXIT_STDERR_TAIL_MAX);
    expect(result.endsWith(appended)).toBe(true);
    expect(result).toContain("(Issue #302)");
    expect(result).toContain("(Issue #402)");
  });
});

describe("byId eviction — a result for an evicted id is dropped (#456)", () => {
  /** The private correlation index, reached the only way a test can. */
  function byIdSize(subject: object): number {
    return (subject as unknown as { byId: Map<string, unknown> }).byId.size;
  }

  it("ToolCallLog: an evicted id's late result does not increment correlatedResults", () => {
    const log = new ToolCallLog();
    for (let i = 0; i < TOOL_CALL_LOG_MAX_ENTRIES + 1; i++) {
      log.observeToolUse("Read", { file_path: `/f/${i}` }, `toolu_${i}`);
    }

    // `toolu_0` was pushed out of the window by the last call. Its result
    // arrives anyway — the ordinary shape, since a long-running first call's
    // result can land after 200 later ones.
    log.observeToolResult("toolu_0", false, "ok");

    // Dropped: the entry is not in the record, so joining it would inflate the
    // lifetime tally the #402 report quotes as evidence correlation worked —
    // for a row no reader can see.
    expect(log.correlatedResults).toBe(0);
    // And the index stays bounded alongside the window it describes.
    expect(byIdSize(log)).toBeLessThanOrEqual(TOOL_CALL_LOG_MAX_ENTRIES);
    expect(log.size).toBe(TOOL_CALL_LOG_MAX_ENTRIES);

    // Control: a result for an id still IN the window does join, so the
    // assertion above is about eviction and not about correlation being broken.
    log.observeToolResult(`toolu_${TOOL_CALL_LOG_MAX_ENTRIES}`, false, "ok");
    expect(log.correlatedResults).toBe(1);
  });

  it("RecentBashRing: an evicted id's late result does not increment correlatedExits", () => {
    const ring = new RecentBashRing();
    for (let i = 0; i < RECENT_BASH_MAX_ENTRIES + 1; i++) {
      ring.observeToolUse("Bash", { command: `echo ${i}` }, `toolu_${i}`);
    }

    ring.observeToolResult("toolu_0", true);

    expect(ring.correlatedExits).toBe(0);
    expect(byIdSize(ring)).toBeLessThanOrEqual(RECENT_BASH_MAX_ENTRIES);
    expect(ring.size).toBe(RECENT_BASH_MAX_ENTRIES);
    // The evicted command's exit must not reappear in the snapshot either.
    expect(ring.snapshot().some((e) => e.cmd === "echo 0")).toBe(false);

    ring.observeToolResult(`toolu_${RECENT_BASH_MAX_ENTRIES}`, true);
    expect(ring.correlatedExits).toBe(1);
    expect(ring.last()?.exit).toBe(1);
  });
});
