/**
 * #2770 carries #2773's review follow-ups for the progress monitor:
 *
 *   - `hadSource` reads the sources live now: a declared log that vanished no
 *     longer makes a kill read as an expired external-progress deferral, just
 *     as a reaped pid does not;
 *   - a churn kill is recorded as `progress-churn-tools` with its tool-count
 *     limit even when a deferral expired behind it.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ProgressMonitor,
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

function makeProbe(sizes: Map<string, number>): ExternalProgressProbe {
  return { isProcessAlive: () => false, fileSize: (path) => sizes.get(path) ?? null };
}

function tickTo(monitor: ProgressMonitor, seconds: number) {
  vi.advanceTimersByTime(seconds * 1000);
  return monitor.check(1.5);
}

describe("ProgressMonitor — #2773 review follow-ups", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-04T00:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("a declared log that vanished is not a source behind the kill", () => {
    const sizes = new Map([["/tmp/gate.log", 100]]);
    const monitor = new ProgressMonitor(makeConfig(), makeProbe(sizes));
    monitor.declareExternalProgress({ log: "/tmp/gate.log", label: "gate" });
    sizes.delete("/tmp/gate.log");

    const killed = tickTo(monitor, 1_300);
    expect(killed.shouldKill).toBe(true);
    expect(killed.ceiling?.name).toBe("progress-no-progress-window");
    expect(killed.reason).not.toContain("external-progress ceiling");
  });

  it("a declared log still present names the external-progress ceiling", () => {
    const sizes = new Map([["/tmp/gate.log", 100]]);
    const monitor = new ProgressMonitor(makeConfig(), makeProbe(sizes));
    monitor.declareExternalProgress({ log: "/tmp/gate.log", label: "gate" });

    const killed = tickTo(monitor, 1_300);
    expect(killed.shouldKill).toBe(true);
    expect(killed.ceiling?.name).toBe("progress-external-progress-ceiling");
  });

  it("a churn kill keeps its own ceiling when a background-task deferral expired behind it", () => {
    const monitor = new ProgressMonitor(makeConfig(), makeProbe(new Map()));
    monitor.setBackgroundTasks(1, Date.now());
    vi.advanceTimersByTime(91 * 60_000); // past the 90-minute background-task ceiling
    for (let i = 0; i < 40; i++) monitor.recordSignal("distinct_tool", `Bash:poll ${i}`);

    const killed = monitor.check(1.5);
    expect(killed.shouldKill).toBe(true);
    expect(killed.ceiling).toMatchObject({ name: "progress-churn-tools", limit: "40 tools" });
    expect(killed.reason).toContain("background task ran");
  });
});
