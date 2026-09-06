/**
 * progressMonitor.externalProgress.test.ts
 *
 * Regression tests for Issue #1488: the progress-runaway monitor killed a
 * `feature-validate` stage that was doing exactly the right thing — waiting on
 * a long external process.
 *
 * Two observed kills, both in a downstream workspace repository:
 *
 *   - one issue killed twice inside 90 minutes with a Playwright suite mid-run:
 *     once by the churn detector (40 `tail`/`ps` polls, no productive signal)
 *     and once by the no-progress window (`sleep 1; echo waiting_for_monitor…`).
 *   - another at 810s and $1.80, with its own `git commit` STILL RUNNING. The
 *     commit registered as productive when the call was issued; the window
 *     then expired underneath the command that proved the stage productive.
 *
 * Every clock the monitor had was fed from the agent's message stream, so all
 * of them go cold precisely when the stage is waiting correctly. The fix gives
 * it three sources that do not: a declared child pid, byte growth of a declared
 * log, and a tool call currently in flight.
 *
 * These tests pin both directions of the issue's acceptance criteria — the
 * waiting stage survives to its process's exit, and a stage that loops with no
 * child and no log growth is still killed at the window.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  ProgressMonitor,
  defaultExternalProgressProbe,
  type ExternalProgressProbe,
  type ProgressMonitorConfig,
} from "../../src/utils/progressMonitor";

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

/** A probe whose answers the test controls outright. */
function makeProbe(state: { alivePids: Set<number>; sizes: Map<string, number> }) {
  const probe: ExternalProgressProbe = {
    isProcessAlive: (pid) => state.alivePids.has(pid),
    fileSize: (path) => state.sizes.get(path) ?? null,
  };
  return probe;
}

/** Advance the fake clock and evaluate, as the 30-second ticker would. */
function tickTo(monitor: ProgressMonitor, seconds: number, costUsd = 1.5) {
  vi.advanceTimersByTime(seconds * 1000);
  return monitor.check(costUsd);
}

describe("ProgressMonitor — external progress (#1488)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-06T00:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe("acceptance 1: a stage that launches a long process and polls it survives", () => {
    it("survives a 6-minute child process and is killable again once it exits", () => {
      const state = { alivePids: new Set<number>([69700]), sizes: new Map<string, number>() };
      const monitor = new ProgressMonitor(makeConfig(), makeProbe(state));

      // The stage declares the suite it just backgrounded (the observed shape).
      monitor.declareExternalProgress({
        pid: 69700,
        log: "/tmp/integration-suite.log",
        label: "playwright browser-integration",
      });
      state.sizes.set("/tmp/integration-suite.log", 0);

      // Six minutes of polling, well past the 120s window, with only the kind
      // of tool calls the monitor cannot read as progress.
      for (let minute = 1; minute <= 6; minute++) {
        monitor.recordSignal("distinct_tool", `Bash:tail -60 log #${minute}`);
        const result = tickTo(monitor, 60);
        expect(result.shouldKill).toBe(false);
        // The first two minutes are inside the window; from minute 3 on it is
        // the live child, and nothing else, holding the kill off.
        if (minute > 2) {
          expect(result.reason).toContain("child pid 69700");
        }
      }

      // The suite finishes and the pid is reaped: the deferral ends with it.
      state.alivePids.delete(69700);
      const afterExit = tickTo(monitor, 121);
      expect(afterExit.shouldKill).toBe(true);
      expect(afterExit.ceiling?.name).toBe("progress-no-progress-window");
    });

    it("counts byte growth of a declared log as activity even with no live pid", () => {
      const state = { alivePids: new Set<number>(), sizes: new Map([["/tmp/suite.log", 4_096]]) };
      const monitor = new ProgressMonitor(makeConfig(), makeProbe(state));

      monitor.declareExternalProgress({ log: "/tmp/suite.log", label: "vitest" });

      // Growth defers.
      state.sizes.set("/tmp/suite.log", 8_192);
      const growing = tickTo(monitor, 200);
      expect(growing.shouldKill).toBe(false);
      expect(growing.reason).toContain("grew by 4096B");

      // The log stops growing: nothing defers any more.
      const flat = tickTo(monitor, 121);
      expect(flat.shouldKill).toBe(true);
    });

    it("seeds a declared log at its current size, so a pre-existing file is not free deferral", () => {
      const state = { alivePids: new Set<number>(), sizes: new Map([["/tmp/old.log", 1_000_000]]) };
      const monitor = new ProgressMonitor(makeConfig(), makeProbe(state));

      monitor.declareExternalProgress({ log: "/tmp/old.log" });

      // Same size at poll time — declaring a large existing file proves nothing.
      const result = tickTo(monitor, 200);
      expect(result.shouldKill).toBe(true);
    });

    it("keeps a stage alive while its own git commit is still running", () => {
      const monitor = new ProgressMonitor(
        makeConfig(),
        makeProbe({
          alivePids: new Set<number>(),
          sizes: new Map<string, number>(),
        })
      );

      // The commit registers as productive at the moment the call is ISSUED…
      monitor.recordSignal("commit");
      monitor.setInFlightToolCalls(1);

      // …and then takes four minutes (pre-commit hooks). Under the old rule
      // the window expired underneath it and the stage was killed at 810s.
      const midCommit = tickTo(monitor, 240);
      expect(midCommit.shouldKill).toBe(false);
      expect(midCommit.reason).toContain("1 tool call(s) in flight");

      // The tool_result arrives; the call stops proving liveness immediately.
      monitor.setInFlightToolCalls(0);
      expect(tickTo(monitor, 121).shouldKill).toBe(true);
    });

    it("suppresses the churn detector while a declared child is alive", () => {
      const state = { alivePids: new Set<number>([69700]), sizes: new Map<string, number>() };
      const monitor = new ProgressMonitor(makeConfig(), makeProbe(state));
      monitor.declareExternalProgress({ pid: 69700, label: "playwright" });

      // 40 distinct polls — the exact profile that fired progress-churn-tools.
      for (let i = 0; i < 40; i++) {
        monitor.recordSignal("distinct_tool", `Bash:poll ${i}`);
      }
      const alive = tickTo(monitor, 200);
      expect(alive.shouldKill).toBe(false);
      expect(alive.churnSinceProgress).toBeGreaterThanOrEqual(40);

      // With the child gone, the same churn is a kill again.
      state.alivePids.delete(69700);
      const dead = tickTo(monitor, 60);
      expect(dead.shouldKill).toBe(true);
      expect(dead.ceiling?.name).toBe("progress-churn-tools");
    });
  });

  describe("acceptance 2: a loop with no child and no log growth is still killed", () => {
    it("kills an ls/cat loop at the window exactly as before", () => {
      const monitor = new ProgressMonitor(
        makeConfig(),
        makeProbe({
          alivePids: new Set<number>(),
          sizes: new Map<string, number>(),
        })
      );

      // Novel-looking but declaring nothing: no pid, no log, no call in flight.
      monitor.recordSignal("distinct_tool", "Bash:ls");
      monitor.recordSignal("distinct_tool", "Bash:cat foo");

      const result = tickTo(monitor, 300);
      expect(result.shouldKill).toBe(true);
      expect(result.ceiling?.name).toBe("progress-no-progress-window");
    });

    it("kills a wedged declared child once the external ceiling is passed", () => {
      // The immortality guard: a child that is alive but hung defers only up to
      // externalProgressCeilingMs, matching WEDGED_TOOL_CALL_CEILING_S (#1083).
      const state = { alivePids: new Set<number>([4242]), sizes: new Map<string, number>() };
      const monitor = new ProgressMonitor(
        makeConfig({ externalProgressCeilingMs: 600_000 }),
        makeProbe(state)
      );
      monitor.declareExternalProgress({ pid: 4242, label: "build_runner" });

      expect(tickTo(monitor, 300).shouldKill).toBe(false);

      // Past the ceiling the probe is not consulted at all, so the pid stays
      // alive and the stage still dies.
      const past = tickTo(monitor, 400);
      expect(state.alivePids.has(4242)).toBe(true);
      expect(past.shouldKill).toBe(true);
    });

    it("never lets external progress satisfy a kill — only defer one", () => {
      const state = { alivePids: new Set<number>([4242]), sizes: new Map<string, number>() };
      const monitor = new ProgressMonitor(makeConfig(), makeProbe(state));
      monitor.declareExternalProgress({ pid: 4242 });

      tickTo(monitor, 200);
      // Deferral must not be mistaken for forward motion by the cost-ceiling
      // escalation, which reads getProductiveProgressDelta().
      expect(monitor.getProductiveProgressDelta()).toBe(0);
      expect(monitor.hasObservedProductiveProgress).toBe(false);
    });

    it("ignores an implausible pid rather than deferring on it", () => {
      const monitor = new ProgressMonitor(
        makeConfig(),
        makeProbe({
          alivePids: new Set<number>([0, 1]),
          sizes: new Map<string, number>(),
        })
      );
      monitor.declareExternalProgress({ pid: 1 });
      monitor.declareExternalProgress({ pid: -5 });

      expect(tickTo(monitor, 200).shouldKill).toBe(true);
    });

    it("respects externalProgressCeilingMs = 0 as 'disabled'", () => {
      const state = { alivePids: new Set<number>([4242]), sizes: new Map<string, number>() };
      const monitor = new ProgressMonitor(
        makeConfig({ externalProgressCeilingMs: 0 }),
        makeProbe(state)
      );
      monitor.declareExternalProgress({ pid: 4242 });

      expect(tickTo(monitor, 200).shouldKill).toBe(true);
    });
  });

  describe("defaultExternalProgressProbe", () => {
    it("reports this process as alive and an impossible pid as gone", () => {
      expect(defaultExternalProgressProbe.isProcessAlive(process.pid)).toBe(true);
      // Guarded above by the pid > 1 rule; 0x7fffffff is a plausible-looking
      // pid that will not exist.
      expect(defaultExternalProgressProbe.isProcessAlive(0x7fffffff)).toBe(false);
      expect(defaultExternalProgressProbe.isProcessAlive(1)).toBe(false);
    });

    it("returns null for a file it cannot read", () => {
      expect(
        defaultExternalProgressProbe.fileSize("/nonexistent/nightgauge-1488/does-not-exist.log")
      ).toBeNull();
    });
  });
});
