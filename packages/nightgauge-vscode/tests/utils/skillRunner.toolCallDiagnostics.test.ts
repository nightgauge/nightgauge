/**
 * skillRunner.toolCallDiagnostics.test.ts
 *
 * #1118 — a task polled by a series of short calls, driven through the real
 * stream handler in the bytes the CLI actually emits.
 *
 * The measured live run: one task showing 0.0% CPU, observed by 15 distinct
 * heartbeating tool calls, the longest ~4.5 minutes, because the polling tool
 * is invoked with a 5-minute block timeout. Every call returned cleanly,
 * `elapsedSeconds` reset to zero, and `WEDGED_TOOL_CALL_CEILING_S` (20
 * minutes, keyed to ONE call) never came within four times of firing.
 *
 * That task was NOT wedged: it completed successfully at ~24 minutes, because
 * its output was piped through `tail` and buffered. So this file asserts BOTH
 * halves of the contract, and the second half is the regression guard:
 *
 *   1. The silence is reported, so the operator has a signal at all.
 *   2. Heartbeat activity is STILL BOOKED after the silence is reported. If
 *      it were withheld, the existing stall machinery would have reclaimed
 *      that stage at minute 20 and destroyed the worktree four minutes before
 *      the build succeeded.
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

/**
 * The observed mid-transformation tree: a codegen step whose delete phase
 * finished and then stopped, leaving deletions of tracked generated files
 * alongside hand-edited sources.
 */
const OBSERVED_PORCELAIN = [
  ...Array.from({ length: 89 }, (_, i) => ` D lib/generated/file_${i}.g.dart`),
  ...Array.from({ length: 15 }, (_, i) => ` M lib/src/feature_${i}.dart`),
].join("\n");

vi.mock("child_process", async () => {
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    spawn: vi.fn(),
    execFileSync: vi.fn((_cmd: string, args: string[]) => {
      if (isSkillRenderCall(args)) return skillRenderStdout(args);
      if (args?.[0] === "status" && args?.[1] === "--porcelain") return OBSERVED_PORCELAIN;
      return "";
    }),
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

import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { ProgressMonitor } from "../../src/utils/progressMonitor";
import { createMockChildProcess } from "../mocks/child-process";

/** The 20-minute per-call ceiling, restated so the test is self-contained. */
const CEILING_S = 20 * 60;
/** The longest single call actually observed in the live run. */
const CALL_MS = 4.5 * 60 * 1000;
const TASK_ID = "task_7";
/** The polling tool's own declared bound — 5 minutes, in milliseconds. */
const DECLARED_TIMEOUT_MS = 300_000;

describe("skillRunner — a task polled by a series of short calls (#1118)", () => {
  let mockProcess: ChildProcess;
  let recordSignal: ReturnType<typeof vi.spyOn>;
  let clockMs: number;

  beforeEach(() => {
    vi.clearAllMocks();
    // setSystemTime moves Date.now WITHOUT running the 30s stall ticker, so
    // 20 minutes of wall-clock passes without dragging in every other timer.
    vi.useFakeTimers();
    clockMs = 1_700_000_000_000;
    vi.setSystemTime(clockMs);
    recordSignal = vi.spyOn(ProgressMonitor.prototype, "recordSignal");
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  function advance(ms: number) {
    clockMs += ms;
    vi.setSystemTime(clockMs);
  }

  function emit(payload: unknown) {
    mockProcess.stdout!.emit("data", Buffer.from(JSON.stringify(payload) + "\n"));
  }

  /** A poll call, in the assistant-message shape the Claude CLI emits. */
  function emitPollCall(id: string) {
    emit({
      type: "assistant",
      message: {
        id: `msg_${id}`,
        type: "message",
        role: "assistant",
        model: "claude-opus-4-20250514",
        content: [
          {
            type: "tool_use",
            id,
            name: "TaskOutput",
            input: { task_id: TASK_ID, block: true, timeout: DECLARED_TIMEOUT_MS },
          },
        ],
        stop_reason: null,
        stop_sequence: null,
        usage: {
          input_tokens: 4,
          cache_creation_input_tokens: 0,
          cache_read_input_tokens: 10,
          output_tokens: 1,
        },
      },
      parent_tool_use_id: null,
      session_id: "sess_test",
    });
  }

  function emitHeartbeat(id: string, elapsedSeconds: number) {
    emit({
      type: "tool_progress",
      tool_use_id: id,
      tool_name: "TaskOutput",
      elapsed_time_seconds: elapsedSeconds,
      heartbeat: true,
    });
  }

  function emitPollResult(id: string, content: string) {
    emit({
      type: "user",
      message: {
        role: "user",
        content: [{ type: "tool_result", tool_use_id: id, content, is_error: false }],
      },
      session_id: "sess_test",
    });
  }

  function heartbeatCount(): number {
    return recordSignal.mock.calls.filter((c: unknown[]) => c[0] === "tool_heartbeat").length;
  }

  /**
   * One poll of the task: the call, its heartbeats every 30s, and the
   * result that reports nothing new. Returns nothing; time advances by one
   * full call.
   */
  function pollOnce(index: number, resultBody: string) {
    const id = `toolu_poll_${index}`;
    emitPollCall(id);
    for (let elapsed = 30; elapsed <= CALL_MS / 1000; elapsed += 30) {
      emitHeartbeat(id, elapsed);
      advance(30_000);
    }
    emitPollResult(id, resultBody);
  }

  /** Drive the measured live run: 15 short polls of one task, no new output. */
  function driveObservedSilence(resultBody = "(no new output)") {
    for (let i = 0; i < 15; i++) {
      pollOnce(i, resultBody);
    }
  }

  it("reports the silence, naming the task, the poll count and the tree at stake", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-validate", 1118, { onStderr });

    // 15 calls, none anywhere near the 1200s per-call ceiling, all polling one
    // task that returns the same body. This is the measured live run.
    driveObservedSilence();

    // Premise: the per-call ceiling genuinely cannot see this. If it ever did,
    // the assertion below would pass for the wrong reason.
    expect(onStderr).not.toHaveBeenCalledWith(expect.stringContaining("[wedged-tool-call]"));

    emitPollCall("toolu_poll_15");
    emitHeartbeat("toolu_poll_15", 30);

    const lines = onStderr.mock.calls
      .map((c) => String(c[0]))
      .filter((line) => line.includes("[long-silent-task]"));
    expect(lines).toHaveLength(1);
    expect(lines[0]).toContain("feature-validate");
    expect(lines[0]).toContain(`TaskOutput:${TASK_ID}`);
    // The operator learns what is at stake without running git by hand.
    expect(lines[0]).toContain("89 deleted");
    expect(lines[0]).toContain("15 modified");
    expect(lines[0]).toContain("/test/workspace");
  });

  /**
   * THE REGRESSION GUARD.
   *
   * The task in the run this issue came from looked exactly like the one
   * driven above — 15 polls, no incremental output, zero CPU — and it
   * COMPLETED SUCCESSFULLY at ~24 minutes, because its output was piped
   * through `tail` and buffered. A predicate that withheld `tool_heartbeat`
   * on the strength of that silence would have let the existing stall
   * machinery reclaim the stage at minute 20, killing a healthy build four
   * minutes short and destroying the worktree with it.
   *
   * So: reporting the silence must change NOTHING about liveness. Every
   * heartbeat after the report still books activity, exactly as before.
   */
  it("still books heartbeat activity after reporting the silence", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-validate", 1118, { onStderr });

    driveObservedSilence();
    const bookedBeforeReport = heartbeatCount();
    expect(bookedBeforeReport).toBeGreaterThan(0);

    emitPollCall("toolu_poll_15");
    emitHeartbeat("toolu_poll_15", 30);

    // The report fired…
    expect(onStderr).toHaveBeenCalledWith(expect.stringContaining("[long-silent-task]"));
    // …and the heartbeat that carried it still counted as activity.
    expect(heartbeatCount()).toBe(bookedBeforeReport + 1);

    // Still true many heartbeats later — the report is not a latch that
    // quietly stops the clock after the first line.
    for (let elapsed = 60; elapsed <= 300; elapsed += 30) {
      emitHeartbeat("toolu_poll_15", elapsed);
    }
    expect(heartbeatCount()).toBe(bookedBeforeReport + 10);
  });

  /**
   * Same guard, one layer down: the progress feed. Withholding a repeat poll
   * from `recordToolCallProgress` would starve the runaway monitor's activity
   * signal for exactly the buffered-output build described above.
   */
  it("still feeds a repeat poll of a silent task to the progress monitor", () => {
    runStageSkillHeadless("feature-validate", 1118, {});

    const distinctToolCalls = () =>
      recordSignal.mock.calls.filter((c: unknown[]) => c[0] === "distinct_tool").length;

    driveObservedSilence();
    const before = distinctToolCalls();

    emitPollCall("toolu_poll_15");
    expect(distinctToolCalls()).toBe(before + 1);
  });

  it("reports the silence once, not once per 30-second heartbeat", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-validate", 1118, { onStderr });

    driveObservedSilence();
    emitPollCall("toolu_poll_15");
    for (let elapsed = 30; elapsed <= 300; elapsed += 30) {
      emitHeartbeat("toolu_poll_15", elapsed);
    }

    const lines = onStderr.mock.calls
      .map((c) => String(c[0]))
      .filter((line) => line.includes("[long-silent-task]"));
    expect(lines).toHaveLength(1);
  });

  it("says nothing while the polled task is still producing output", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-validate", 1118, { onStderr });

    // Same 15 calls over the same wall-clock — but the task is talking.
    for (let i = 0; i < 15; i++) {
      pollOnce(i, `Built ${i * 40} outputs`);
    }

    const booked = heartbeatCount();
    emitPollCall("toolu_poll_15");
    emitHeartbeat("toolu_poll_15", 30);

    expect(heartbeatCount()).toBe(booked + 1);
    expect(onStderr).not.toHaveBeenCalledWith(expect.stringContaining("[long-silent-task]"));
  });
});

describe("skillRunner — a call that outlives its own declared timeout (#1118)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  function emit(payload: unknown) {
    mockProcess.stdout!.emit("data", Buffer.from(JSON.stringify(payload) + "\n"));
  }

  function emitBashCall(id: string, timeout: number) {
    emit({
      type: "assistant",
      message: {
        id: `msg_${id}`,
        type: "message",
        role: "assistant",
        model: "claude-opus-4-20250514",
        content: [
          {
            type: "tool_use",
            id,
            name: "Bash",
            input: { command: "run the project codegen", timeout },
          },
        ],
        stop_reason: null,
        stop_sequence: null,
        usage: {
          input_tokens: 4,
          cache_creation_input_tokens: 0,
          cache_read_input_tokens: 10,
          output_tokens: 1,
        },
      },
      parent_tool_use_id: null,
      session_id: "sess_test",
    });
  }

  function emitHeartbeat(id: string, elapsedSeconds: number) {
    emit({
      type: "tool_progress",
      tool_use_id: id,
      tool_name: "Bash",
      elapsed_time_seconds: elapsedSeconds,
      heartbeat: true,
    });
  }

  it("says so, naming the declared limit and the overrun", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-validate", 1118, { onStderr });

    emitBashCall("toolu_bash", DECLARED_TIMEOUT_MS);
    // 17 minutes against a declared 5 — the observed threefold overrun.
    emitHeartbeat("toolu_bash", 1020);

    const lines = onStderr.mock.calls
      .map((c) => String(c[0]))
      .filter((line) => line.includes("[tool-call-timeout-exceeded]"));
    expect(lines).toHaveLength(1);
    expect(lines[0]).toContain("300000ms timeout");
    expect(lines[0]).toContain("1020s");
  });

  it("says nothing for a call still inside its declared timeout", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-validate", 1118, { onStderr });

    emitBashCall("toolu_bash", DECLARED_TIMEOUT_MS);
    emitHeartbeat("toolu_bash", 120);

    expect(onStderr).not.toHaveBeenCalledWith(
      expect.stringContaining("[tool-call-timeout-exceeded]")
    );
  });

  it("makes a long call legible on a fixed cadence, not once per heartbeat", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-validate", 1118, { onStderr });

    emitBashCall("toolu_bash", DECLARED_TIMEOUT_MS);
    // 15 minutes of heartbeats at the adapter's ~30s cadence: 30 of them.
    for (let elapsed = 30; elapsed <= 900; elapsed += 30) {
      emitHeartbeat("toolu_bash", elapsed);
    }

    const lines = onStderr.mock.calls
      .map((c) => String(c[0]))
      .filter((line) => line.includes("[long-tool-call]"));
    // One per five-minute window: 5, 10 and 15 minutes — not 30 lines.
    expect(lines).toHaveLength(3);
    expect(lines[0]).toContain("300s");
    expect(lines[2]).toContain("900s");
    expect(lines[2]).toContain(`${CEILING_S}s wedge ceiling`);
  });
});
