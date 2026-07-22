/**
 * Unit tests for ProgressMonitor (Issue #3783)
 *
 * Covers: window mechanics, distinct_tool deduplication, observe-only mode,
 * catastrophic-limit warn, min-cost activation guard, and the two AC regression
 * tests (legitimate long stage not killed; cheap infinite loop stopped).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ProgressMonitor, type ProgressMonitorConfig } from "../../src/utils/progressMonitor";

function makeConfig(overrides: Partial<ProgressMonitorConfig> = {}): ProgressMonitorConfig {
  return {
    enabled: true,
    noProgressWindowMs: 5_000,
    minCostToActivateUsd: 0.1,
    catastrophicLimitUsd: 100,
    observeOnly: false,
    churnToolThreshold: 40,
    catastrophicKill: false,
    ...overrides,
  };
}

describe("ProgressMonitor", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  // ── AC Regression Test A (HEALTHY LONG STAGE — must NOT be killed) ──────
  // #3851: a healthy long stage makes steady PRODUCTIVE progress (commits /
  // new-file writes). It interleaves plenty of reads/searches (novel tool
  // signatures), but because it keeps committing and writing new files the
  // productive window keeps resetting and it is NEVER killed — even at high
  // cost and high tool-call volume. This is the #2982/#3840 false-kill guard.
  it("does NOT kill a healthy long stage making steady productive progress", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 5_000, minCostToActivateUsd: 0.1, churnToolThreshold: 40 })
    );

    // 30 minutes of work: every ~3s a productive signal (alternating commits
    // and new-file writes), with a flurry of novel reads/searches between them.
    for (let i = 0; i < 600; i++) {
      vi.advanceTimersByTime(3_000);
      // Lots of activity (reads/searches) — novel signatures, but NOT progress.
      monitor.recordSignal("distinct_tool", `Read:file-${i}`);
      monitor.recordSignal("distinct_tool", `Grep:pattern-${i}`);
      // ...but real forward motion every iteration.
      if (i % 2 === 0) {
        monitor.recordSignal("commit");
      } else {
        monitor.recordSignal("file_change", `src/new-file-${i}.ts`);
      }
      // Check on each iteration the way the 30s ticker would.
      expect(monitor.check(50.0).shouldKill).toBe(false);
    }

    const result = monitor.check(50.0);
    expect(result.shouldKill).toBe(false);
    expect(result.productiveSignals).toBe(600);
  });

  // ── AC Regression Test B (CHURN — must be killed) ───────────────────────
  // #3851: the #3811 profile — hundreds of novel tool signatures (reads /
  // searches / re-edits) but ZERO productive progress. The churn detector
  // fires once the distinct-tool count passes the threshold AND the productive
  // window has elapsed, even when each individual signature is unique.
  it("kills a churning stage: many novel tool calls, no productive progress", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 5_000, minCostToActivateUsd: 0.1, churnToolThreshold: 40 })
    );

    // 60 distinct novel signatures (reads of different files) over 60s — well
    // past the churn threshold, no commits or new files.
    for (let i = 0; i < 60; i++) {
      vi.advanceTimersByTime(1_000);
      monitor.recordSignal("distinct_tool", `Read:file-${i}`);
    }

    const result = monitor.check(20.0);
    expect(result.shouldKill).toBe(true);
    expect(result.reason).toMatch(/churn detected/i);
    expect(result.productiveSignals).toBe(0);
  });

  it("does NOT count novel tool calls as window-advancing progress", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 5_000, minCostToActivateUsd: 0.1, churnToolThreshold: 1000 })
    );

    // Distinct tool calls every 2s for 12s — under the (huge) churn threshold,
    // so churn does not fire — but they do NOT advance the no-progress window,
    // so the no-progress kill DOES fire. (Old behaviour: this would NOT kill.)
    for (let i = 0; i < 6; i++) {
      vi.advanceTimersByTime(2_000);
      monitor.recordSignal("distinct_tool", `cmd-${i}`);
    }

    const result = monitor.check(15.0);
    expect(result.shouldKill).toBe(true);
    expect(result.productiveSignals).toBe(0);
  });

  // ── AC Regression Test B' (cheap identical loop) ─────────────────────────
  it("kills a cheap stage repeating identical tool calls with no progress", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 5_000, minCostToActivateUsd: 0.1, churnToolThreshold: 1000 })
    );

    for (let i = 0; i < 20; i++) {
      vi.advanceTimersByTime(500);
      monitor.recordSignal("distinct_tool", "same-tool-hash");
    }

    vi.advanceTimersByTime(5_001);

    const result = monitor.check(0.3);
    expect(result.shouldKill).toBe(true);
    expect(result.reason).toMatch(/no productive progress/i);
  });

  // ── Window mechanics ────────────────────────────────────────────────────
  it("does not kill before the window expires", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 10_000 }));

    vi.advanceTimersByTime(9_999);
    const result = monitor.check(1.0);
    expect(result.shouldKill).toBe(false);
  });

  it("kills exactly at window expiry", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 10_000 }));

    vi.advanceTimersByTime(10_001);
    const result = monitor.check(1.0);
    expect(result.shouldKill).toBe(true);
  });

  it("resets the window on a new PRODUCTIVE signal (commit)", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 5_000 }));

    vi.advanceTimersByTime(4_500);
    monitor.recordSignal("commit");
    vi.advanceTimersByTime(4_500); // 4.5s since last productive signal — within window

    const result = monitor.check(1.0);
    expect(result.shouldKill).toBe(false);
  });

  it("does NOT reset the window for a distinct_tool signal (#3851)", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 5_000, churnToolThreshold: 1000 })
    );

    // A novel tool call at 4.5s does NOT reset the window, so by 5.5s the
    // no-progress window has elapsed and the stage is killed. (This is the
    // opposite of the pre-#3851 behaviour.)
    vi.advanceTimersByTime(4_500);
    monitor.recordSignal("distinct_tool", "cmd-a");
    vi.advanceTimersByTime(1_000); // total 5.5s since construction, no productive signal

    const result = monitor.check(1.0);
    expect(result.shouldKill).toBe(true);
  });

  // ── distinct_tool deduplication ─────────────────────────────────────────
  it("deduplicates repeated identical distinct_tool sigs (churn gauge)", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 5_000, churnToolThreshold: 1000 })
    );

    monitor.recordSignal("distinct_tool", "same-sig");
    monitor.recordSignal("distinct_tool", "same-sig");
    monitor.recordSignal("distinct_tool", "same-sig");

    // Only the first unique sig counts toward churn / signal totals.
    const result = monitor.check(1.0);
    expect(result.signalsSeen).toBe(1);
    expect(result.churnSinceProgress).toBe(1);
  });

  it("counts distinct_tool sigs independently", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 5_000 }));

    monitor.recordSignal("distinct_tool", "sig-a");
    monitor.recordSignal("distinct_tool", "sig-b");
    monitor.recordSignal("distinct_tool", "sig-a"); // duplicate — ignored

    expect(monitor.hasObservedAnyProgress).toBe(true);

    const result = monitor.check(1.0);
    expect(result.signalsSeen).toBe(2); // only 2 unique
    expect(result.productiveSignals).toBe(0); // none are productive
  });

  // ── Non-distinct_tool signals always advance window ─────────────────────
  it("always resets window for phase_marker signals", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 5_000 }));

    vi.advanceTimersByTime(4_000);
    monitor.recordSignal("phase_marker");
    vi.advanceTimersByTime(4_900); // 4.9s since last reset

    expect(monitor.check(1.0).shouldKill).toBe(false);
  });

  it("always resets window for ci_progress signals", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 5_000 }));

    vi.advanceTimersByTime(4_000);
    monitor.recordSignal("ci_progress");
    vi.advanceTimersByTime(4_900);

    expect(monitor.check(1.0).shouldKill).toBe(false);
  });

  it("always resets window for file_change signals", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 5_000 }));

    vi.advanceTimersByTime(4_000);
    monitor.recordSignal("file_change");
    vi.advanceTimersByTime(4_900);

    expect(monitor.check(1.0).shouldKill).toBe(false);
  });

  // ── min-cost activation guard ───────────────────────────────────────────
  it("does not fire when cost is below min activation threshold", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 1_000, minCostToActivateUsd: 0.5 })
    );

    vi.advanceTimersByTime(5_000);
    const result = monitor.check(0.49);
    expect(result.shouldKill).toBe(false);
    expect(result.shouldWarn).toBe(false);
  });

  it("activates once cost meets the threshold", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 1_000, minCostToActivateUsd: 0.5 })
    );

    vi.advanceTimersByTime(2_000);
    const result = monitor.check(0.5);
    expect(result.shouldKill).toBe(true);
  });

  // ── observe-only mode (maximum performance mode) ────────────────────────
  it("never kills in observe-only mode", () => {
    const monitor = new ProgressMonitor(makeConfig({ observeOnly: true, noProgressWindowMs: 100 }));

    vi.advanceTimersByTime(5_000);
    const result = monitor.check(1.0);
    expect(result.shouldKill).toBe(false);
    expect(result.shouldWarn).toBe(true);
  });

  // ── disabled mode ────────────────────────────────────────────────────────
  it("returns no-op when disabled", () => {
    const monitor = new ProgressMonitor(makeConfig({ enabled: false, noProgressWindowMs: 100 }));

    vi.advanceTimersByTime(10_000);
    const result = monitor.check(99.0);
    expect(result.shouldKill).toBe(false);
    expect(result.shouldWarn).toBe(false);
  });

  // ── catastrophic limit backstop ──────────────────────────────────────────
  it("warns at catastrophic limit without killing", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ catastrophicLimitUsd: 50, noProgressWindowMs: 5_000 })
    );

    // Record a productive signal so the no-progress window is not exceeded.
    monitor.recordSignal("commit");

    const result = monitor.check(50.0);
    expect(result.shouldKill).toBe(false);
    expect(result.shouldWarn).toBe(true);
    expect(result.reason).toMatch(/catastrophic limit/i);
  });

  // ── catastrophic KILL (unattended, progress-gated — #3851) ───────────────
  it("kills at catastrophic limit when catastrophicKill and window exceeded", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ catastrophicLimitUsd: 50, noProgressWindowMs: 5_000, catastrophicKill: true })
    );

    vi.advanceTimersByTime(6_000); // window exceeded, no productive progress

    const result = monitor.check(60.0);
    expect(result.shouldKill).toBe(true);
    expect(result.reason).toMatch(/catastrophic kill/i);
  });

  it("does NOT catastrophic-kill while still making productive progress", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ catastrophicLimitUsd: 50, noProgressWindowMs: 5_000, catastrophicKill: true })
    );

    // Productive progress within the window — even at $200, only warn.
    vi.advanceTimersByTime(2_000);
    monitor.recordSignal("commit");
    vi.advanceTimersByTime(2_000);

    const result = monitor.check(200.0);
    expect(result.shouldKill).toBe(false);
    expect(result.shouldWarn).toBe(true);
  });

  it("does NOT catastrophic-kill in observe-only mode even when set", () => {
    const monitor = new ProgressMonitor(
      makeConfig({
        catastrophicLimitUsd: 50,
        noProgressWindowMs: 5_000,
        catastrophicKill: true,
        observeOnly: true,
      })
    );

    vi.advanceTimersByTime(6_000);
    const result = monitor.check(60.0);
    expect(result.shouldKill).toBe(false);
  });

  // ── file_change: new path productive, re-write is churn (#3851) ──────────
  it("treats a new file_change path as productive but a re-write as churn", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ noProgressWindowMs: 5_000, churnToolThreshold: 1000 })
    );

    monitor.recordSignal("file_change", "src/a.ts"); // new → productive
    monitor.recordSignal("file_change", "src/a.ts"); // re-write → churn, not progress
    monitor.recordSignal("file_change", "src/b.ts"); // new → productive

    const result = monitor.check(1.0);
    expect(result.productiveSignals).toBe(2);
    expect(result.churnSinceProgress).toBe(0); // a productive signal was last → reset
  });

  // ── getProductiveProgressDelta accessor (orchestrator gate — #3851) ──────
  it("exposes a monotonic productive-progress count via getProductiveProgressDelta", () => {
    const monitor = new ProgressMonitor(makeConfig());

    expect(monitor.getProductiveProgressDelta()).toBe(0);

    monitor.recordSignal("distinct_tool", "Read:x"); // activity — not productive
    monitor.recordSignal("distinct_tool", "Grep:y"); // activity — not productive
    expect(monitor.getProductiveProgressDelta()).toBe(0);

    monitor.recordSignal("commit");
    monitor.recordSignal("file_change", "src/new.ts");
    monitor.recordSignal("phase_marker");
    monitor.recordSignal("ci_progress");
    expect(monitor.getProductiveProgressDelta()).toBe(4);

    // A snapshot/delta pattern the orchestrator uses: no new productive signal
    // between snapshots → delta is zero (churn → orchestrator refuses to escalate).
    const snapshot = monitor.getProductiveProgressDelta();
    monitor.recordSignal("distinct_tool", "Read:z");
    expect(monitor.getProductiveProgressDelta() - snapshot).toBe(0);
  });

  it("catastrophic warn fires before no-progress kill when both conditions met", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ catastrophicLimitUsd: 50, noProgressWindowMs: 5_000 })
    );

    vi.advanceTimersByTime(10_000); // window exceeded

    const result = monitor.check(50.0); // also at catastrophic limit
    // Catastrophic check comes first — warn-only
    expect(result.shouldKill).toBe(false);
    expect(result.shouldWarn).toBe(true);
  });

  // ── hasObservedAnyProgress ───────────────────────────────────────────────
  it("reports false before any signal is recorded", () => {
    const monitor = new ProgressMonitor(makeConfig());
    expect(monitor.hasObservedAnyProgress).toBe(false);
  });

  it("reports true after any signal is recorded", () => {
    const monitor = new ProgressMonitor(makeConfig());
    monitor.recordSignal("phase_marker");
    expect(monitor.hasObservedAnyProgress).toBe(true);
  });

  it("msSinceLastProgress is included in kill result", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 1_000 }));

    vi.advanceTimersByTime(3_000);
    const result = monitor.check(1.0);
    expect(result.shouldKill).toBe(true);
    expect(result.msSinceLastProgress).toBeGreaterThanOrEqual(3_000);
  });
});
