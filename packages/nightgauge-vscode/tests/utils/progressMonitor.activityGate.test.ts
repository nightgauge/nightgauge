/**
 * progressMonitor.activityGate.test.ts
 *
 * Regression tests for Issue #128: the progress-based runaway monitor killed a
 * `feature-dev` stage that had already produced its deliverable and was in its
 * terminal verification / scope-tidying / self-assessment phase.
 *
 * Observed kill:
 *   [runaway-progress-exceeded] Stage feature-dev terminated: No productive
 *   progress (commit / new file / phase / CI) for 153s (window: 120s,
 *   productive signals: 3, activity...)
 *
 * The stage was reverting an out-of-scope generated-file change, re-running
 * verification, and writing its self-assessment — all of which are tool calls
 * and none of which are "productive" under the artifact-only signal set. The
 * monitor was therefore most likely to fire exactly when the stage was closest
 * to succeeding, and the kill was maximally destructive because `feature-dev`
 * never commits (#1608) — the work existed only in the worktree.
 *
 * The fix adds a second clock: novel tool invocations. The plain no-progress
 * kill now requires BOTH clocks cold. These tests pin both directions —
 * a working-but-quiet stage survives, a wedged / spinning / churning stage
 * still dies.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ProgressMonitor, type ProgressMonitorConfig } from "../../src/utils/progressMonitor";

function makeConfig(overrides: Partial<ProgressMonitorConfig> = {}): ProgressMonitorConfig {
  return {
    enabled: true,
    // Production defaults for the incident: 2-minute window, $0.50 floor.
    noProgressWindowMs: 120_000,
    minCostToActivateUsd: 0.5,
    catastrophicLimitUsd: 200,
    observeOnly: false,
    churnToolThreshold: 40,
    catastrophicKill: false,
    ...overrides,
  };
}

describe("ProgressMonitor activity gate (#128)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  // ── Direction 1: the misfire must not happen ────────────────────────────
  it("does NOT kill a stage in its terminal verification phase (the #128 incident)", () => {
    const monitor = new ProgressMonitor(makeConfig());

    // Implementation phase: three productive signals, exactly as the incident
    // recorded ("productive signals: 3").
    monitor.recordSignal("phase_marker");
    vi.advanceTimersByTime(30_000);
    monitor.recordSignal("file_change", "test/harness_entrypoint_test.dart");
    vi.advanceTimersByTime(30_000);
    monitor.recordSignal("file_change", "test/harness.dart");

    // Terminal phase: 153s of verification / scope-tidying / epilogue. Real
    // tool calls the whole way, but nothing the artifact-only signal set counts.
    const verification = [
      'Bash:{"command":"git status --porcelain"}',
      'Bash:{"command":"git diff --stat"}',
      'Bash:{"command":"git checkout -- lib/generated/api.g.dart"}',
      'Bash:{"command":"flutter analyze"}',
      'Read:{"file_path":"test/harness.dart"}',
      'Bash:{"command":"dart format --output=none --set-exit-if-changed ."}',
      'Bash:{"command":"flutter test test/harness_entrypoint_test.dart"}',
    ];
    for (const sig of verification) {
      vi.advanceTimersByTime(22_000); // 7 × 22s = 154s of quiet-but-busy work
      monitor.recordSignal("distinct_tool", sig);
      // The 30s ticker would evaluate here on every pass.
      expect(monitor.check(5.65).shouldKill).toBe(false);
    }

    const result = monitor.check(5.65);
    expect(result.shouldKill).toBe(false);
    expect(result.productiveSignals).toBe(3);
    // The productive window IS exceeded — that part of the diagnosis stands.
    expect(result.msSinceLastProgress).toBeGreaterThan(120_000);
    // ...but the stage is demonstrably alive, so the kill is deferred.
    expect(result.msSinceLastActivity).toBeLessThanOrEqual(120_000);
    expect(result.reason).toMatch(/working, not stalled/i);
  });

  // ── Direction 2: a genuinely wedged process is still killed ─────────────
  it("still kills a wedged stage: productive work, then total tool silence", () => {
    const monitor = new ProgressMonitor(makeConfig());

    monitor.recordSignal("phase_marker");
    vi.advanceTimersByTime(10_000);
    monitor.recordSignal("file_change", "src/impl.ts");
    monitor.recordSignal("distinct_tool", 'Bash:{"command":"npm test"}');

    // Then nothing at all — no tool calls, no signals of any kind.
    vi.advanceTimersByTime(121_000);

    const result = monitor.check(5.0);
    expect(result.shouldKill).toBe(true);
    expect(result.reason).toMatch(/no tool activity/i);
    expect(result.msSinceLastActivity).toBeGreaterThan(120_000);
  });

  it("kills a stage whose activity dies partway through the quiet phase", () => {
    const monitor = new ProgressMonitor(makeConfig());

    monitor.recordSignal("file_change", "src/impl.ts");
    // 100s of verification activity...
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(20_000);
      monitor.recordSignal("distinct_tool", `Bash:cmd-${i}`);
      expect(monitor.check(5.0).shouldKill).toBe(false);
    }
    // ...then the process wedges.
    vi.advanceTimersByTime(60_000);
    expect(monitor.check(5.0).shouldKill).toBe(false); // activity window not yet cold
    vi.advanceTimersByTime(61_000);

    const result = monitor.check(5.0);
    expect(result.shouldKill).toBe(true);
  });

  // ── Direction 2b: a spin loop is not "activity" ─────────────────────────
  it("still kills an identical-call spin loop (repeats are deduplicated)", () => {
    const monitor = new ProgressMonitor(makeConfig({ churnToolThreshold: 1000 }));

    // The same tool signature, over and over, forever. Repeats never reach the
    // activity clock, so this is a stall no matter how fast it spins.
    for (let i = 0; i < 200; i++) {
      vi.advanceTimersByTime(1_000);
      monitor.recordSignal("distinct_tool", 'Bash:{"command":"gh pr checks"}');
    }

    const result = monitor.check(5.0);
    expect(result.shouldKill).toBe(true);
    expect(result.signalsSeen).toBe(1); // 200 calls, 1 novel signature
  });

  // ── Direction 2c: #3851 churn protection is untouched ───────────────────
  it("still kills a churning stage even though it is busy (#3811 guard)", () => {
    const monitor = new ProgressMonitor(makeConfig({ churnToolThreshold: 40 }));

    // The #3811 profile: a flood of novel tool signatures, zero productive
    // progress. The activity clock is warm on every tick — the churn detector
    // must fire anyway, or the activity gate would have re-opened #3811.
    for (let i = 0; i < 60; i++) {
      vi.advanceTimersByTime(3_000);
      monitor.recordSignal("distinct_tool", `Read:file-${i}`);
    }

    const result = monitor.check(112.0);
    expect(result.shouldKill).toBe(true);
    expect(result.reason).toMatch(/churn detected/i);
    expect(result.msSinceLastActivity).toBeLessThan(120_000); // busy, and killed anyway
  });

  // ── The gate never rescues a stage below the other backstops ────────────
  it("does not let activity defer the catastrophic-cost kill", () => {
    const monitor = new ProgressMonitor(
      makeConfig({ catastrophicLimitUsd: 200, catastrophicKill: true })
    );

    for (let i = 0; i < 10; i++) {
      vi.advanceTimersByTime(20_000);
      monitor.recordSignal("distinct_tool", `Read:file-${i}`);
    }

    const result = monitor.check(250.0);
    expect(result.shouldKill).toBe(true);
    expect(result.reason).toMatch(/catastrophic kill/i);
  });

  it("reports the activity clock on every check result", () => {
    const monitor = new ProgressMonitor(makeConfig({ noProgressWindowMs: 30_000 }));

    vi.advanceTimersByTime(5_000);
    monitor.recordSignal("distinct_tool", "Read:a");
    vi.advanceTimersByTime(7_000);

    expect(monitor.check(1.0).msSinceLastActivity).toBe(7_000);
    expect(monitor.msSinceLastActivity).toBe(7_000);
  });
});
