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

describe("applyBackgroundTaskEvent (#2517)", () => {
  it("replaces the set with the list in background_tasks_changed", () => {
    const ids = new Set<string>(["stale"]);
    expect(applyBackgroundTaskEvent(CHANGED_TWO, ids)).toBe(true);
    expect([...ids].sort()).toEqual(["bewmujgwv", "bv61n5dmb"]);
    expect(applyBackgroundTaskEvent(CHANGED_NONE, ids)).toBe(true);
    expect(ids.size).toBe(0);
  });

  it("adds a task started in the background, not a foreground one", () => {
    const ids = new Set<string>();
    applyBackgroundTaskEvent(STARTED_FOREGROUND, ids);
    expect(ids.size).toBe(0);
    applyBackgroundTaskEvent(STARTED_BACKGROUND, ids);
    expect([...ids]).toEqual(["bv61n5dmb"]);
  });

  it("removes a task on task_notification", () => {
    const ids = new Set<string>();
    applyBackgroundTaskEvent(CHANGED_ONE, ids);
    expect(applyBackgroundTaskEvent(NOTIFIED, ids)).toBe(true);
    expect(ids.size).toBe(0);
  });

  it("ignores lines that are not background-task events", () => {
    const ids = new Set<string>(["bewmujgwv"]);
    expect(applyBackgroundTaskEvent('{"type":"system","subtype":"init"}', ids)).toBe(false);
    expect(applyBackgroundTaskEvent("not json, mentions task", ids)).toBe(false);
    expect(
      applyBackgroundTaskEvent(
        '{"type":"user","subtype":"task_notification","task_id":"bewmujgwv"}',
        ids
      )
    ).toBe(false);
    expect([...ids]).toEqual(["bewmujgwv"]);
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

  it("stops deferring once the external ceiling is passed", () => {
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
