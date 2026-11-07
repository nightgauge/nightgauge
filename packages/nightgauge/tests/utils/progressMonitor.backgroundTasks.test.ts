/**
 * progressMonitor.backgroundTasks.test.ts
 *
 * Regression tests for Issue #2517: the runaway gate killed #2505's
 * feature-validate 127s into waiting on its own `scripts/ci-local.sh`.
 *
 * The Bash call outlived its 600s timeout, Claude Code moved it to the
 * background, and its tool_result came back at once, so the in-flight count of
 * #1488 dropped to zero while the gate was still running. The agent then waited
 * for the task's notification, as it should, and every clock went cold. The
 * stream announced the running task the whole time in `background_tasks_changed`.
 *
 * The event lines below are the shapes that run emitted, trimmed.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  ProgressMonitor,
  applyBackgroundTaskEvent,
  applyConnectivityResume,
  shiftBackgroundTaskStarts,
  syncBackgroundTasks,
  type ExternalProgressProbe,
  type ProgressMonitorConfig,
} from "../../src/utils/progressMonitor";

const CHANGED_ONE =
  '{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"bewmujgwv","run_id":"0mvnduisi-79f09b96","task_type":"local_bash"}]}';
const CHANGED_TWO =
  '{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"bewmujgwv","task_type":"local_bash"},{"task_id":"bv61n5dmb","task_type":"local_bash"}]}';
const CHANGED_NONE = '{"type":"system","subtype":"background_tasks_changed","tasks":[]}';
const STARTED_FOREGROUND =
  '{"type":"system","subtype":"task_started","task_id":"bewmujgwv","tool_use_id":"toolu_012","is_backgrounded":false,"task_type":"local_bash"}';
const STARTED_BACKGROUND =
  '{"type":"system","subtype":"task_started","task_id":"bv61n5dmb","tool_use_id":"toolu_01H","is_backgrounded":true,"task_type":"local_bash"}';
const NOTIFIED =
  '{"type":"system","subtype":"task_notification","task_id":"bewmujgwv","tool_use_id":"toolu_012","status":"completed","output_file":""}';

function makeConfig(overrides: Partial<ProgressMonitorConfig> = {}): ProgressMonitorConfig {
  return {
    enabled: true,
    noProgressWindowMs: 120_000,
    minCostToActivateUsd: 0.5,
    catastrophicLimitUsd: 200,
    observeOnly: false,
    churnToolThreshold: 40,
    catastrophicKill: false,
    externalProgressCeilingMs: 1_200_000,
    ...overrides,
  };
}

const noProbe: ExternalProgressProbe = {
  isProcessAlive: () => false,
  fileSize: () => null,
};

function tickTo(monitor: ProgressMonitor, seconds: number, costUsd = 1.5) {
  vi.advanceTimersByTime(seconds * 1000);
  return monitor.check(costUsd);
}

describe("isBackgroundWorkLive (#2711)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("holds until the task ceiling, then lets the idle clocks run", () => {
    const monitor = new ProgressMonitor(makeConfig({ backgroundTaskCeilingMs: 60_000 }), noProbe);
    expect(monitor.isBackgroundWorkLive()).toBe(false);
    monitor.setBackgroundTasks(1, Date.now());
    vi.advanceTimersByTime(60_000);
    expect(monitor.isBackgroundWorkLive()).toBe(true);
    vi.advanceTimersByTime(1);
    expect(monitor.isBackgroundWorkLive()).toBe(false);
  });

  it("holds for a declared log only while it grows, and a declared pid while alive", () => {
    let size = 100;
    let alive = false;
    const probe: ExternalProgressProbe = {
      isProcessAlive: () => alive,
      fileSize: () => size,
    };
    const monitor = new ProgressMonitor(makeConfig(), probe);
    monitor.declareExternalProgress({ pid: 4242, log: "/tmp/gate.log", label: "gate" });
    expect(monitor.isBackgroundWorkLive()).toBe(false);
    size = 200;
    expect(monitor.isBackgroundWorkLive()).toBe(true);
    expect(monitor.isBackgroundWorkLive()).toBe(false);
    alive = true;
    expect(monitor.isBackgroundWorkLive()).toBe(true);
    vi.advanceTimersByTime(1_200_001);
    expect(monitor.isBackgroundWorkLive()).toBe(false);
  });

  it("holds for a foreground tool call until it returns or passes the external ceiling", () => {
    const monitor = new ProgressMonitor(makeConfig(), noProbe);
    monitor.setInFlightToolCalls(1);
    expect(monitor.isBackgroundWorkLive()).toBe(true);
    monitor.setInFlightToolCalls(0);
    expect(monitor.isBackgroundWorkLive()).toBe(false);
    monitor.setInFlightToolCalls(1);
    vi.advanceTimersByTime(1_200_001);
    expect(monitor.isBackgroundWorkLive()).toBe(false);
  });

  it("stops when the last task ends", () => {
    const monitor = new ProgressMonitor(makeConfig(), noProbe);
    monitor.setBackgroundTasks(1, Date.now());
    monitor.setBackgroundTasks(0);
    expect(monitor.isBackgroundWorkLive()).toBe(false);
  });
});

describe("applyBackgroundTaskEvent (#2517)", () => {
  it("replaces the set with the list in background_tasks_changed", () => {
    const ids = new Map<string, number>([["stale", 0]]);
    expect(applyBackgroundTaskEvent(CHANGED_TWO, ids)).toBe(true);
    expect([...ids.keys()].sort()).toEqual(["bewmujgwv", "bv61n5dmb"]);
    expect(applyBackgroundTaskEvent(CHANGED_NONE, ids)).toBe(true);
    expect(ids.size).toBe(0);
  });

  it("adds a task started in the background, not a foreground one", () => {
    const ids = new Map<string, number>();
    applyBackgroundTaskEvent(STARTED_FOREGROUND, ids);
    expect(ids.size).toBe(0);
    applyBackgroundTaskEvent(STARTED_BACKGROUND, ids);
    expect([...ids.keys()]).toEqual(["bv61n5dmb"]);
  });

  it("removes a task on task_notification", () => {
    const ids = new Map<string, number>();
    applyBackgroundTaskEvent(CHANGED_ONE, ids);
    expect(applyBackgroundTaskEvent(NOTIFIED, ids)).toBe(true);
    expect(ids.size).toBe(0);
  });

  it("ignores lines that are not background-task events", () => {
    const ids = new Map<string, number>([["bewmujgwv", 0]]);
    expect(applyBackgroundTaskEvent('{"type":"system","subtype":"init"}', ids)).toBe(false);
    expect(applyBackgroundTaskEvent("not json, mentions task", ids)).toBe(false);
    expect(
      applyBackgroundTaskEvent(
        '{"type":"user","subtype":"task_notification","task_id":"bewmujgwv"}',
        ids
      )
    ).toBe(false);
    expect([...ids.keys()]).toEqual(["bewmujgwv"]);
  });
});

describe("ProgressMonitor — background tasks (#2517)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-24T23:20:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("keeps a stage alive while a background task runs, and kills once it finishes", () => {
    const monitor = new ProgressMonitor(makeConfig(), noProbe);
    monitor.setBackgroundTasks(1);

    // Twelve minutes of silence, as #2505 sat waiting on ci-local.sh.
    for (let minute = 1; minute <= 12; minute++) {
      const result = tickTo(monitor, 60);
      expect(result.shouldKill).toBe(false);
      if (minute > 2) expect(result.reason).toContain("1 background task(s) running");
    }

    monitor.setBackgroundTasks(0);
    const done = tickTo(monitor, 121);
    expect(done.shouldKill).toBe(true);
    expect(done.ceiling?.name).toBe("progress-no-progress-window");
  });

  it("bounds a task with no known start by the external ceiling", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ externalProgressCeilingMs: 600_000 }),
      noProbe
    );
    monitor.setBackgroundTasks(1);

    expect(tickTo(monitor, 300).shouldKill).toBe(false);
    // A wedged background job must not make the stage immortal.
    expect(tickTo(monitor, 400).shouldKill).toBe(true);
  });
});

describe("ProgressMonitor — background task ceiling (#2554)", () => {
  const MIN = 60_000;
  const MONITOR_STARTED =
    '{"type":"system","subtype":"task_started","task_id":"mon1","is_backgrounded":true,"task_type":"monitor"}';
  const MONITOR_DONE = '{"type":"system","subtype":"task_notification","task_id":"mon1"}';
  const GATE_STARTED =
    '{"type":"system","subtype":"task_started","task_id":"gate1","is_backgrounded":true,"task_type":"local_bash"}';

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-25T00:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function sync(monitor: ProgressMonitor, tasks: Map<string, number>) {
    monitor.setBackgroundTasks(tasks.size, tasks.size > 0 ? Math.min(...tasks.values()) : null);
  }

  it("keeps a gate started 4 min after the last progress alive past the external ceiling (#1501)", () => {
    const monitor = new ProgressMonitor(makeConfig({ backgroundTaskCeilingMs: 45 * MIN }), noProbe);
    const tasks = new Map<string, number>();

    // Four minutes of rebasing, then the gate goes to the background.
    vi.advanceTimersByTime(4 * MIN);
    monitor.check(1.5);
    applyBackgroundTaskEvent(GATE_STARTED, tasks, Date.now());
    sync(monitor, tasks);

    // Tick every 30s to t=48 min, re-arming a Monitor every 10 min.
    for (let t = 4 * MIN + 30_000; t <= 48 * MIN; t += 30_000) {
      vi.setSystemTime(new Date(Date.parse("2026-10-25T00:00:00Z") + t));
      if (t % (10 * MIN) === 0) {
        applyBackgroundTaskEvent(MONITOR_DONE, tasks, Date.now());
        applyBackgroundTaskEvent(MONITOR_STARTED, tasks, Date.now());
        sync(monitor, tasks);
      }
      const result = monitor.check(1.5);
      expect(result.shouldKill, `t=${t / 1000}s: ${result.reason}`).toBe(false);
    }
  });

  it("kills once the oldest task outlives the background ceiling", () => {
    const monitor = new ProgressMonitor(makeConfig({ backgroundTaskCeilingMs: 30 * MIN }), noProbe);
    const tasks = new Map<string, number>();
    applyBackgroundTaskEvent(GATE_STARTED, tasks, Date.now());
    sync(monitor, tasks);

    let killed = false;
    for (let minute = 1; minute <= 40 && !killed; minute++) {
      const result = tickTo(monitor, 60);
      if (minute <= 30) expect(result.shouldKill).toBe(false);
      killed = result.shouldKill;
    }
    expect(killed).toBe(true);
  });

  it("re-syncs from background_tasks_changed keep the oldest start time", () => {
    const tasks = new Map<string, number>();
    applyBackgroundTaskEvent(GATE_STARTED, tasks, 1_000);
    applyBackgroundTaskEvent(
      '{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"gate1"},{"task_id":"mon1"}]}',
      tasks,
      9_000
    );
    expect(tasks.get("gate1")).toBe(1_000);
    expect(tasks.get("mon1")).toBe(9_000);
    applyBackgroundTaskEvent(GATE_STARTED, tasks, 20_000);
    expect(tasks.get("gate1")).toBe(1_000);
  });
});

describe("ProgressMonitor gate deferrals (#2694)", () => {
  const MIN = 60_000;
  let size = 0;
  const logProbe: ExternalProgressProbe = {
    isProcessAlive: () => false,
    fileSize: () => size,
  };

  beforeEach(() => {
    vi.useFakeTimers();
    size = 100;
  });
  afterEach(() => vi.useRealTimers());

  function monitorWithTask(ageMs: number): ProgressMonitor {
    const monitor = new ProgressMonitor(makeConfig(), logProbe);
    monitor.declareExternalProgress({ log: "/tmp/gate.log", label: "ci-local.sh gate" });
    monitor.setBackgroundTasks(1, Date.now() - ageMs);
    return monitor;
  }

  /** Advance in 30s ticks, optionally growing the log; returns the first kill. */
  function tick(monitor: ProgressMonitor, ticks: number, grow: boolean) {
    for (let i = 0; i < ticks; i++) {
      vi.advanceTimersByTime(30_000);
      if (grow) size += 10;
      const result = monitor.check(1);
      if (result.shouldKill) return result;
    }
    return undefined;
  }

  it("a connectivity resume keeps a task alive for the next two ticks", () => {
    const tasks = new Map([["t", Date.now()]]);
    const monitor = new ProgressMonitor(makeConfig({ backgroundTaskCeilingMs: 5 * MIN }), noProbe);
    syncBackgroundTasks(monitor, tasks);
    vi.advanceTimersByTime(10 * MIN);
    applyConnectivityResume(monitor, tasks, 10 * MIN);
    expect(tick(monitor, 2, false)).toBeUndefined();
  });

  it("shifts only tasks that started before the outage", () => {
    const now = Date.now();
    const tasks = new Map([
      ["old", now - 20 * MIN],
      ["new", now - MIN],
    ]);
    shiftBackgroundTaskStarts(tasks, 10 * MIN, now);
    expect(tasks.get("old")).toBe(now - 10 * MIN);
    expect(tasks.get("new")).toBe(now - MIN);
  });

  it("the restart gives a clock already stale before the outage a whole window", () => {
    const monitor = new ProgressMonitor(makeConfig(), noProbe);
    vi.advanceTimersByTime(100_000);
    vi.advanceTimersByTime(10 * MIN);
    monitor.resumeAfterOffline(10 * MIN);
    expect(monitor.msSinceLastActivity).toBe(0);
    expect(monitor.msSinceLastProductiveProgress).toBe(100_000);
  });

  it("keeps a 100-minute task with a growing log alive", () => {
    const monitor = monitorWithTask(100 * MIN);
    vi.advanceTimersByTime(5 * MIN);
    expect(tick(monitor, 6, true)).toBeUndefined();
    size += 10;
    expect(monitor.isBackgroundWorkLive()).toBe(true);
  });

  // The two real kills: a 50-minute gate. #2740 restarted its gate in the
  // background and declared nothing; #2758 declared a log that went quiet.
  it("keeps a 50-minute gate alive whether or not it declared a log", () => {
    const undeclared = new ProgressMonitor(makeConfig(), noProbe);
    undeclared.setBackgroundTasks(1, Date.now() - 50 * MIN);
    const quiet = monitorWithTask(50 * MIN);
    vi.advanceTimersByTime(5 * MIN);
    expect(tick(undeclared, 6, false)).toBeUndefined();
    expect(tick(quiet, 6, false)).toBeUndefined();
  });

  it("kills one whose log stopped, naming the ceiling and the age", () => {
    const monitor = monitorWithTask(100 * MIN);
    vi.advanceTimersByTime(5 * MIN);
    const kill = tick(monitor, 6, false);
    expect(kill?.ceiling?.name).toBe("progress-background-task-ceiling");
    expect(kill?.reason).toMatch(
      /ci-local\.sh gate background task ran \d+m\d*s?, past the 90m background-task ceiling/
    );
  });

  it("an undeclared task dies at the ceiling, so no wait is unbounded", () => {
    const monitor = new ProgressMonitor(makeConfig(), noProbe);
    monitor.setBackgroundTasks(1, Date.now() - 88 * MIN);
    vi.advanceTimersByTime(5 * MIN);
    const kill = tick(monitor, 6, false);
    expect(kill?.ceiling?.name).toBe("progress-background-task-ceiling");
  });

  it("a growing log dies at the hard bound", () => {
    const monitor = monitorWithTask(119 * MIN);
    vi.advanceTimersByTime(25 * MIN);
    const kill = tick(monitor, 8, true);
    expect(kill?.ceiling?.name).toBe("progress-background-task-hard-ceiling");
  });

  it("names the external ceiling when that deferral expired", () => {
    const monitor = new ProgressMonitor(makeConfig(), logProbe);
    monitor.declareExternalProgress({ log: "/tmp/gate.log" });
    vi.advanceTimersByTime(21 * MIN);
    const result = monitor.check(1);
    expect(result.shouldKill).toBe(true);
    expect(result.ceiling?.name).toBe("progress-external-progress-ceiling");
    expect(result.reason).toContain("past the 20m external-progress ceiling");
  });

  it("keeps today's ceiling when nothing was deferring", () => {
    const monitor = new ProgressMonitor(makeConfig(), noProbe);
    vi.advanceTimersByTime(21 * MIN);
    expect(monitor.check(1).ceiling?.name).toBe("progress-no-progress-window");
  });
});
