/**
 * killCeiling.test.ts
 *
 * Issue #161: a complete stage-exit record was NOT sufficient to identify
 * which ceiling killed a stage. `signal_source=runaway-progress` names the
 * closure that delivered SIGTERM, and four unrelated limits funnel into it —
 * the no-progress window, the churn detector, the catastrophic cost backstop,
 * and the derived Nx stall multiple. Tracing the whole resolver chain ruled
 * every documented candidate out and left no explanation, because the ceiling
 * that actually fired is computed at runtime and appears in no config file.
 *
 * These tests pin the two halves that make the record self-describing: the
 * ProgressMonitor attributes every kill and warn to a named ceiling, and the
 * flattened value carries the derivation, not just the number.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { formatKillCeilingValue, msLimit, usdLimit } from "../../src/utils/killCeiling";
import { ProgressMonitor, type ProgressMonitorConfig } from "../../src/utils/progressMonitor";

function makeConfig(overrides: Partial<ProgressMonitorConfig> = {}): ProgressMonitorConfig {
  return {
    enabled: true,
    noProgressWindowMs: 120_000,
    minCostToActivateUsd: 0.5,
    catastrophicLimitUsd: 200,
    observeOnly: false,
    churnToolThreshold: 40,
    catastrophicKill: false,
    ...overrides,
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("formatKillCeilingValue", () => {
  it("carries the derivation, which is the load-bearing half for a derived ceiling", () => {
    // "2400000ms" alone sends the reader back into the resolver chain — the
    // exact dead end #161 documented. The derivation ends the investigation.
    expect(
      formatKillCeilingValue({
        name: "nx-stall-multiple",
        limit: msLimit(2_400_000),
        derivation: "stall warn threshold 300s (source: static) × NX_RUNAWAY_KILL_MULTIPLE=8",
      })
    ).toBe("2400000ms (stall warn threshold 300s (source: static) × NX_RUNAWAY_KILL_MULTIPLE=8)");
  });

  it("falls back to the bare limit when there is nothing to derive", () => {
    expect(
      formatKillCeilingValue({ name: "stall-idle", limit: msLimit(1_200_000), derivation: "" })
    ).toBe("1200000ms");
  });

  it("renders ms and USD limits in their own units", () => {
    expect(msLimit(120_000)).toBe("120000ms");
    expect(msLimit(1234.6)).toBe("1235ms");
    expect(usdLimit(200)).toBe("$200.00");
    expect(usdLimit(0.5)).toBe("$0.50");
  });
});

describe("ProgressMonitor attributes every kill to a named ceiling (#161)", () => {
  it("names the no-progress window when both clocks go cold", () => {
    vi.useFakeTimers();
    const monitor = new ProgressMonitor(makeConfig());
    vi.advanceTimersByTime(200_000);

    const result = monitor.check(5);
    expect(result.shouldKill).toBe(true);
    expect(result.ceiling?.name).toBe("progress-no-progress-window");
    expect(result.ceiling?.limit).toBe("120000ms");
    expect(result.ceiling?.derivation).toContain("no_progress_window_ms");
  });

  it("names the churn threshold, distinguishing it from the plain window kill", () => {
    // Both kills previously reported `signal_source=runaway-progress` and
    // nothing else, so a retro could not tell a spin loop from a quiet stage.
    vi.useFakeTimers();
    const monitor = new ProgressMonitor(makeConfig({ churnToolThreshold: 3 }));
    monitor.recordSignal("distinct_tool", "Read:a");
    monitor.recordSignal("distinct_tool", "Read:b");
    monitor.recordSignal("distinct_tool", "Read:c");
    vi.advanceTimersByTime(200_000);

    const result = monitor.check(5);
    expect(result.shouldKill).toBe(true);
    expect(result.ceiling?.name).toBe("progress-churn-tools");
    expect(result.ceiling?.limit).toBe("3 tools");
    expect(result.ceiling?.derivation).toContain("churn_tool_threshold");
  });

  it("names the catastrophic cost backstop on both its kill and its warn", () => {
    vi.useFakeTimers();
    const killer = new ProgressMonitor(makeConfig({ catastrophicKill: true }));
    vi.advanceTimersByTime(200_000);
    const killed = killer.check(250);
    expect(killed.shouldKill).toBe(true);
    expect(killed.ceiling?.name).toBe("progress-catastrophic-cost");
    expect(killed.ceiling?.limit).toBe("$200.00");

    const warner = new ProgressMonitor(makeConfig({ catastrophicKill: false }));
    const warned = warner.check(250);
    expect(warned.shouldKill).toBe(false);
    expect(warned.shouldWarn).toBe(true);
    expect(warned.ceiling?.name).toBe("progress-catastrophic-cost");
  });

  it("attributes no ceiling to outcomes that crossed nothing", () => {
    // A ceiling on a no-op outcome would be a lie in the record: an
    // activity-gated deferral or a below-floor stage tripped no limit.
    vi.useFakeTimers();
    const monitor = new ProgressMonitor(makeConfig());

    // Below the cost activation floor.
    expect(monitor.check(0.1).ceiling).toBeUndefined();
    // Progress healthy.
    expect(monitor.check(5).ceiling).toBeUndefined();

    // Activity-gated: productive window elapsed, novel tool call just landed.
    vi.advanceTimersByTime(200_000);
    monitor.recordSignal("distinct_tool", "Bash:git push");
    const gated = monitor.check(5);
    expect(gated.shouldKill).toBe(false);
    expect(gated.ceiling).toBeUndefined();

    // Disabled.
    const off = new ProgressMonitor(makeConfig({ enabled: false }));
    expect(off.check(5).ceiling).toBeUndefined();
  });
});
