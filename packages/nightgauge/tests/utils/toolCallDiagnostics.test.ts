/**
 * #1118 — a long tool call, and the task it polls, must be legible without
 * `ps`; and NOTHING here may make a stage killable that was not killable
 * before.
 *
 * Observed live: a codegen task showed 0.0% CPU and no filesystem writes,
 * polled by 15 distinct heartbeating calls, the longest ~4.5 minutes, because
 * the polling tool is invoked with a 5-minute block timeout. Every call
 * returned cleanly and `elapsedSeconds` reset to zero, so the 20-minute
 * per-call ceiling never came close to firing.
 *
 * That task was NOT wedged. It completed successfully after ~24 minutes — its
 * output was piped through `tail`, so it buffered and produced no incremental
 * output by construction. A predicate that turned "no new output for 20
 * minutes across 2+ polls" into a kill would have reclaimed the stage at
 * minute 20 and destroyed the worktree four minutes before it succeeded.
 *
 * So the tracker below measures silence and REPORTS it. The tests assert both
 * halves: the observation is emitted, and the activity feed is untouched.
 *
 * The tree at stake in that run held 89 deletions of tracked generated files
 * alongside 15 hand-edited ones, and learning that required `git status` by
 * hand.
 */
import { describe, it, expect } from "vitest";
import {
  LONG_TOOL_CALL_REPORT_INTERVAL_S,
  longToolCallReportBucket,
  declaredTimeoutMs,
  formatLongSilentTask,
  formatLongToolCall,
  formatTimeoutOverrun,
  identifyPolledTask,
  parseWorktreeStatus,
  summarizeWorktreeChanges,
  formatWorktreeSummary,
  PolledTaskSilenceTracker,
} from "../../src/utils/toolCallDiagnostics";

const CEILING_S = 20 * 60;

describe("#1118 — reporting cadence for an in-flight tool call", () => {
  // Not every heartbeat: at ~30s apart that is ~40 lines before the ceiling.
  // Not once: a single line cannot show a call approaching the boundary.
  it("stays silent for the first interval", () => {
    expect(longToolCallReportBucket(0)).toBe(0);
    expect(longToolCallReportBucket(LONG_TOOL_CALL_REPORT_INTERVAL_S - 1)).toBe(0);
  });

  it("advances one window per interval", () => {
    expect(longToolCallReportBucket(LONG_TOOL_CALL_REPORT_INTERVAL_S)).toBe(1);
    expect(longToolCallReportBucket(LONG_TOOL_CALL_REPORT_INTERVAL_S * 2 + 5)).toBe(2);
  });

  // The whole design constraint in one assertion: at most a handful of lines
  // before the ceiling, so the ceiling line is not buried.
  it("yields at most three windows before the 20-minute ceiling", () => {
    expect(longToolCallReportBucket(CEILING_S - 1)).toBeLessThanOrEqual(3);
  });

  it("treats a nonsense elapsed time as window zero rather than throwing", () => {
    expect(longToolCallReportBucket(Number.NaN)).toBe(0);
    expect(longToolCallReportBucket(-90)).toBe(0);
  });
});

describe("#1118 — the periodic long-call line", () => {
  it("names the tool, its elapsed time and its distance from the ceiling", () => {
    const line = formatLongToolCall({
      stage: "feature-validate",
      toolName: "TaskOutput",
      elapsedSeconds: 900,
      ceilingSeconds: CEILING_S,
    });
    expect(line).toContain("[long-tool-call]");
    expect(line).toContain("feature-validate");
    expect(line).toContain("TaskOutput");
    expect(line).toContain("900s");
    // Both halves of "where am I": the fraction consumed and what is left.
    expect(line).toContain("75%");
    expect(line).toContain("300s remaining");
    expect(line.endsWith("\n")).toBe(true);
  });

  it("carries the declared timeout and the overrun factor when one was declared", () => {
    const line = formatLongToolCall({
      stage: "feature-validate",
      toolName: "TaskOutput",
      elapsedSeconds: 1020,
      ceilingSeconds: CEILING_S,
      declaredTimeoutMs: 300_000,
    });
    expect(line).toContain("300000ms timeout");
    expect(line).toContain("3.4× past it");
  });

  it("says nothing about a timeout when none was declared", () => {
    const line = formatLongToolCall({
      stage: "feature-dev",
      toolName: "Bash",
      elapsedSeconds: 600,
      ceilingSeconds: CEILING_S,
    });
    expect(line).not.toContain("timeout");
  });

  it("still identifies the stage when the heartbeat carried no tool name", () => {
    const line = formatLongToolCall({
      stage: "feature-dev",
      elapsedSeconds: 600,
      ceilingSeconds: CEILING_S,
    });
    expect(line).toContain("a tool call");
    expect(line).toContain("feature-dev");
  });
});

describe("#1118 — a call that outlives the timeout it declared", () => {
  it("reports the declared limit and the multiple by which it was exceeded", () => {
    const line = formatTimeoutOverrun({
      stage: "feature-validate",
      toolName: "TaskOutput",
      elapsedSeconds: 1020,
      declaredTimeoutMs: 300_000,
    });
    expect(line).toContain("[tool-call-timeout-exceeded]");
    expect(line).toContain("300000ms timeout");
    expect(line).toContain("1020s");
    expect(line).toContain("3.4×");
  });
});

describe("#1118 — reading a declared timeout out of a tool_use input", () => {
  it("reads the millisecond timeout a call declared for itself", () => {
    expect(declaredTimeoutMs({ command: "dart run build_runner build", timeout: 300_000 })).toBe(
      300_000
    );
  });

  it("accepts the snake_case and camelCase spellings", () => {
    expect(declaredTimeoutMs({ timeout_ms: 120_000 })).toBe(120_000);
    expect(declaredTimeoutMs({ timeoutMs: 60_000 })).toBe(60_000);
  });

  // A fabricated limit is worse than none: it would produce an overrun line
  // for a call that never declared anything.
  it("declares nothing for absent, non-numeric or non-positive values", () => {
    expect(declaredTimeoutMs({ command: "ls" })).toBeUndefined();
    expect(declaredTimeoutMs({ timeout: "300000" })).toBeUndefined();
    expect(declaredTimeoutMs({ timeout: 0 })).toBeUndefined();
    expect(declaredTimeoutMs({ timeout: Number.NaN })).toBeUndefined();
    expect(declaredTimeoutMs(null)).toBeUndefined();
    expect(declaredTimeoutMs("timeout: 5")).toBeUndefined();
  });
});

describe("#1118 — the tree a wedge strands", () => {
  // The observed shape: `--delete-conflicting-outputs` finished its delete
  // phase and stopped, leaving deletions of tracked generated files mixed
  // with hand-edited sources.
  const observedPorcelain = [
    ...Array.from({ length: 89 }, (_, i) => ` D lib/generated/file_${i}.g.dart`),
    ...Array.from({ length: 15 }, (_, i) => ` M lib/src/feature_${i}.dart`),
  ].join("\n");

  it("counts deletions and modifications separately", () => {
    const summary = parseWorktreeStatus(observedPorcelain);
    expect(summary.deleted).toBe(89);
    expect(summary.modified).toBe(15);
    expect(summary.total).toBe(104);
  });

  // Both porcelain columns are load-bearing. A summary that read only the
  // worktree column would report zero deletions for a staged-delete tree —
  // exactly the tree this issue is about.
  it("counts a deletion whichever column reports it", () => {
    expect(parseWorktreeStatus("D  a.dart\n D b.dart\nMD c.dart").deleted).toBe(3);
  });

  it("classifies additions, renames and untracked entries", () => {
    const summary = parseWorktreeStatus("A  new.dart\nR  old.dart -> new2.dart\n?? scratch.txt");
    expect(summary.added).toBe(1);
    expect(summary.renamed).toBe(1);
    expect(summary.untracked).toBe(1);
  });

  it("renders counts by change kind and the worktree path", () => {
    const line = formatWorktreeSummary("/w/issue-1118", parseWorktreeStatus(observedPorcelain));
    expect(line).toContain("/w/issue-1118");
    expect(line).toContain("89 deleted");
    expect(line).toContain("15 modified");
    expect(line).toContain("104 changed entries");
  });

  it("says so when the tree is clean", () => {
    expect(formatWorktreeSummary("/w/issue-1118", parseWorktreeStatus(""))).toContain("is clean");
  });

  // This runs on a stage that is already in trouble. A diagnostic that throws
  // into the stage it is describing is worse than no diagnostic at all.
  it("degrades to nothing when git cannot be read", () => {
    const summary = summarizeWorktreeChanges("/w/no-repo", () => {
      throw new Error("fatal: not a git repository");
    });
    expect(summary).toBeUndefined();
    expect(formatWorktreeSummary("/w/no-repo", summary)).toBe("");
  });

  it("names the worktree path in the long-silent-task line too", () => {
    const line = formatLongSilentTask({
      stage: "feature-validate",
      toolName: "TaskOutput",
      silence: {
        taskId: "TaskOutput:task_7",
        polls: 15,
        silentForSeconds: 1440,
        unchangedLooks: 14,
      },
      worktreeSummary: formatWorktreeSummary(
        "/w/issue-1118",
        parseWorktreeStatus(observedPorcelain)
      ),
    });
    expect(line).toContain("[long-silent-task]");
    expect(line).toContain("polled TaskOutput:task_7 15 times");
    expect(line).toContain("24m");
    expect(line).toContain("89 deleted");
    expect(line).toContain("/w/issue-1118");
  });

  // The line must read as an observation the operator judges, never as a
  // verdict. The run this came from looked exactly like this and finished
  // successfully at ~24 minutes.
  it("states the observation without asserting the task is wedged", () => {
    const line = formatLongSilentTask({
      stage: "feature-validate",
      toolName: "TaskOutput",
      silence: {
        taskId: "TaskOutput:task_7",
        polls: 15,
        silentForSeconds: 1440,
        unchangedLooks: 14,
      },
      worktreeSummary: "",
    });
    expect(line).toContain("may be wedged or simply quiet");
    expect(line).toContain("Check CPU and recent writes before killing it");
    expect(line).toContain("Nothing has been killed or deferred");
  });

  it("reads the working tree through the injected git call", () => {
    const seen: string[] = [];
    const summary = summarizeWorktreeChanges("/w/issue-1118", (p) => {
      seen.push(p);
      return " D a.g.dart\n M b.dart\n";
    });
    expect(seen).toEqual(["/w/issue-1118"]);
    expect(summary).toEqual({
      modified: 1,
      added: 0,
      deleted: 1,
      renamed: 0,
      untracked: 0,
      total: 2,
    });
  });
});

describe("#1118 — identifying the task a call is polling", () => {
  it("recognises the polling tools and the id they carry", () => {
    expect(identifyPolledTask("TaskOutput", { task_id: "task_7", block: true })).toBe(
      "TaskOutput:task_7"
    );
    expect(identifyPolledTask("BashOutput", { bash_id: "bash_2" })).toBe("BashOutput:bash_2");
  });

  // A bash_id and a task_id that happen to collide are different tasks; one
  // shared clock would let either mask the other's wedge.
  it("namespaces the id by tool", () => {
    expect(identifyPolledTask("TaskOutput", { task_id: "1" })).not.toBe(
      identifyPolledTask("BashOutput", { bash_id: "1" })
    );
  });

  it("is not a poll when the tool does work of its own", () => {
    expect(identifyPolledTask("Bash", { command: "dart run build_runner build" })).toBeUndefined();
    expect(identifyPolledTask("Read", { file_path: "/x" })).toBeUndefined();
  });

  it("is not a poll when no task is named", () => {
    expect(identifyPolledTask("TaskOutput", { block: true })).toBeUndefined();
    expect(identifyPolledTask("TaskOutput", null)).toBeUndefined();
  });
});

describe("#1118 — measuring how long a polled task has said nothing", () => {
  const BUDGET_MS = CEILING_S * 1000;
  const POLL_INPUT = { task_id: "task_7", block: true, timeout: 300_000 };
  /** What a blocking poll returns when its task has produced nothing. */
  const NO_NEW_OUTPUT = "(no new output)";

  /** ~4.5 minutes per call is the longest single call actually observed. */
  const CALL_MS = 4.5 * 60 * 1000;
  const POLLS = 15;

  /**
   * The measured live pattern: 15 distinct calls, each well under the per-call
   * ceiling, all polling one task that returns the same body every time.
   * Returns the wall-clock at which the last poll returned.
   */
  function driveObservedSilence(tracker: PolledTaskSilenceTracker, base: number): number {
    for (let i = 0; i < POLLS; i++) {
      const at = base + i * CALL_MS;
      const id = `toolu_poll_${i}_${base}`;
      tracker.observePoll("TaskOutput", POLL_INPUT, id, at);
      tracker.observeResult(id, NO_NEW_OUTPUT, at + CALL_MS);
    }
    return base + POLLS * CALL_MS;
  }

  it("notices the silence even though no single call approaches the ceiling", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    const endedAt = driveObservedSilence(tracker, 1_000_000);

    // The per-call ceiling is 1200s and the longest call was 270s: nothing in
    // `elapsedSeconds` can ever reach it. The task-keyed clock does — and it
    // is used to REPORT, never to kill.
    expect(tracker.hasBeenSilent("TaskOutput:task_7", endedAt)).toBe(true);
  });

  it("says nothing while the task is still producing new output", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    const base = 1_000_000;
    for (let i = 0; i < POLLS; i++) {
      const at = base + i * CALL_MS;
      const id = `toolu_poll_${i}`;
      tracker.observePoll("TaskOutput", POLL_INPUT, id, at);
      tracker.observeResult(id, `compiled unit ${i}`, at + CALL_MS);
    }
    expect(tracker.hasBeenSilent("TaskOutput:task_7", base + POLLS * CALL_MS)).toBe(false);
  });

  // A single call sitting on one task is what the per-call ceiling already
  // reports; this covers the cross-call case, not that one.
  it("stays quiet about a single long poll", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_only", 0);
    expect(tracker.hasBeenSilent("TaskOutput:task_7", BUDGET_MS * 3)).toBe(false);
  });

  it("restarts the clock when the task finally speaks", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    const silentAt = driveObservedSilence(tracker, 1_000_000);
    expect(tracker.hasBeenSilent("TaskOutput:task_7", silentAt)).toBe(true);

    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_revived", silentAt);
    tracker.observeResult("toolu_revived", "Built 412 outputs", silentAt + 1000);
    expect(tracker.hasBeenSilent("TaskOutput:task_7", silentAt + 2000)).toBe(false);
  });

  it("binds a heartbeat's tool_use id back to the task it is polling", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_9", 0);
    expect(tracker.taskForToolUse("toolu_9")).toBe("TaskOutput:task_7");
    expect(tracker.taskForToolUse("toolu_unknown")).toBeUndefined();
  });

  it("reports a silence once, not once per 30-second heartbeat", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    const at = driveObservedSilence(tracker, 1_000_000);

    const first = tracker.silenceToReport("TaskOutput:task_7", at);
    expect(first).toBeDefined();
    expect(first!.polls).toBe(POLLS);
    expect(first!.silentForSeconds).toBeGreaterThanOrEqual(CEILING_S);
    expect(first!.unchangedLooks).toBe(POLLS - 1);
    expect(tracker.silenceToReport("TaskOutput:task_7", at + 30_000)).toBeUndefined();
  });

  it("has nothing to report before the budget is up", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    const base = 1_000_000;
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_0", base);
    tracker.observeResult("toolu_0", NO_NEW_OUTPUT, base);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_1", base + CALL_MS);
    expect(tracker.silenceToReport("TaskOutput:task_7", base + CALL_MS)).toBeUndefined();
  });

  it("can report a fresh silence after the task spoke and went quiet again", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    const revivedAt = driveObservedSilence(tracker, 1_000_000);
    expect(tracker.silenceToReport("TaskOutput:task_7", revivedAt)).toBeDefined();

    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_revived", revivedAt);
    tracker.observeResult("toolu_revived", "Built 412 outputs", revivedAt);
    const silentAgainAt = driveObservedSilence(tracker, revivedAt + 1);
    expect(tracker.silenceToReport("TaskOutput:task_7", silentAgainAt)).toBeDefined();
  });

  it("counts consecutive identical looks", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_1", 0);
    tracker.observeResult("toolu_1", NO_NEW_OUTPUT, 0);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_2", 1000);
    tracker.observeResult("toolu_2", NO_NEW_OUTPUT, 1000);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_3", 2000);
    tracker.observeResult("toolu_3", NO_NEW_OUTPUT, 2000);
    expect(tracker.silenceToReport("TaskOutput:task_7", BUDGET_MS * 2)!.unchangedLooks).toBe(2);
  });

  // A poll that re-echoes a long unchanged buffer is identical in its first
  // 200 characters whether or not the task moved; a truncated comparison
  // would call the moving case unchanged and start a silence that is not one.
  it("compares the whole body, not a prefix", () => {
    const tracker = new PolledTaskSilenceTracker(BUDGET_MS);
    const prefix = "x".repeat(4096);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_1", 0);
    tracker.observeResult("toolu_1", prefix, 0);
    tracker.observePoll("TaskOutput", POLL_INPUT, "toolu_2", 1000);
    tracker.observeResult("toolu_2", prefix + "\nBuilt 412 outputs", 1000);
    // The clock restarted at t=1000 because the body DID change. Had the
    // comparison stopped at 200 characters it would still read t=0, and this
    // instant would already be over budget.
    expect(tracker.hasBeenSilent("TaskOutput:task_7", BUDGET_MS)).toBe(false);
    expect(tracker.hasBeenSilent("TaskOutput:task_7", 1000 + BUDGET_MS)).toBe(true);
  });
});
