/**
 * nxRunawayActivityGate.test.ts
 *
 * Regression tests for Issue #161: the Nx stall-multiple escalation killed
 * `feature-validate` stages that were actively producing tool results.
 *
 * Observed kills (`.nightgauge/pipeline/exit-records/`):
 *
 *   feature-dev       elapsed_ms=1800401  idle_ms_at_exit=59   exit=143
 *   feature-validate  elapsed_ms=2400980  idle_ms_at_exit=621  exit=143
 *   feature-validate  elapsed_ms=2400654  idle_ms_at_exit=376  exit=143
 *
 * The two `feature-validate` kills landed 326ms apart at exactly 2400s, which
 * is `DEFAULT_STALL_THRESHOLDS["feature-validate"] (300s) ×
 * NX_RUNAWAY_KILL_MULTIPLE (8)` sampled on the 30s stall ticker. The session
 * log confirms it verbatim:
 *
 *   [runaway-nx-threshold] Stage feature-validate hit 8× stall threshold with
 *   no productive progress for 250s (productive signals: 4). Escalating to
 *   runaway kill. (Issue #3851)
 *
 * `idle_ms_at_exit` in the hundreds of milliseconds is proof the stage was
 * mid-tool-call, not stalled — the last one had run its full validation,
 * committed, and was on its final `git push`, all exit 0.
 *
 * Root cause: the escalation called the runaway kill in FORCE mode, which
 * bypassed `ProgressMonitor.check` wholesale and with it the #128 activity
 * gate. The plain no-progress kill had been gated since #128; this path had
 * not, which is the entire difference between the stages #128 saved and the
 * ones #161 lost.
 */

import { describe, it, expect } from "vitest";
import { shouldNxRunawayKill } from "../../src/utils/resolvers/monitoringResolver";

const WINDOW_MS = 120_000; // no_progress_window_ms default
const KILL_MULTIPLE = 8; // NX_RUNAWAY_KILL_MULTIPLE

function decide(overrides: Partial<Parameters<typeof shouldNxRunawayKill>[0]> = {}) {
  return shouldNxRunawayKill({
    currentMultiplier: KILL_MULTIPLE,
    killMultiple: KILL_MULTIPLE,
    observeOnly: false,
    stallKilled: false,
    stallKillDisabled: false,
    costCapExceeded: false,
    // Past the productive window — the condition #3851 keyed on.
    msSinceLastProductiveProgress: 250_000,
    // Cold by default; individual tests warm it.
    msSinceLastActivity: 250_000,
    windowMs: WINDOW_MS,
    ...overrides,
  });
}

describe("Nx stall-multiple escalation — activity gate (#161)", () => {
  it("does NOT kill the dogfood infra-repo runaway stage: mid-tool-call at 8× threshold", () => {
    // The exact shape of the lost stage: 8× threshold reached, no PRODUCTIVE
    // signal for 250s (its phases were printf markers), but a novel tool call
    // 0.4s ago — it was inside `git push`.
    expect(
      decide({
        msSinceLastProductiveProgress: 250_000,
        msSinceLastActivity: 376,
      })
    ).toBe("activity-gated");
  });

  it("does NOT kill while activity is anywhere inside the window", () => {
    // The boundary is the same single window the #128 gate uses — one knob,
    // two clocks, so they can never drift apart.
    expect(decide({ msSinceLastActivity: WINDOW_MS })).toBe("activity-gated");
    expect(decide({ msSinceLastActivity: WINDOW_MS - 1 })).toBe("activity-gated");
  });

  it("STILL kills a genuinely wedged stage: both clocks cold at 8×", () => {
    // The gate defers; it cannot disable. A wedged stage issues no tool calls
    // at all, so it dies on exactly the schedule it did before #161.
    expect(
      decide({
        msSinceLastProductiveProgress: 900_000,
        msSinceLastActivity: WINDOW_MS + 1,
      })
    ).toBe("kill");
  });

  it("is a no-op below the kill multiple, however cold both clocks are", () => {
    // 2×…7× stay warnings. Only the Nth escalates.
    for (const multiplier of [2, 3, 4, 5, 6, 7]) {
      expect(
        decide({
          currentMultiplier: multiplier,
          msSinceLastProductiveProgress: 900_000,
          msSinceLastActivity: 900_000,
        })
      ).toBe("no-op");
    }
    expect(decide({ currentMultiplier: 8 })).toBe("kill");
    // Past the multiple keeps killing — the multiplier only ever climbs.
    expect(decide({ currentMultiplier: 9 })).toBe("kill");
  });

  it("is a no-op while productive progress is still inside the window", () => {
    // A stage steadily committing / writing new files keeps resetting the
    // productive clock, so it never reaches this path (#2982 / #3840 guard).
    expect(decide({ msSinceLastProductiveProgress: WINDOW_MS })).toBe("no-op");
    expect(decide({ msSinceLastProductiveProgress: WINDOW_MS - 1 })).toBe("no-op");
  });

  it("never kills in observe-only (maximum performance) mode", () => {
    expect(decide({ observeOnly: true })).toBe("no-op");
  });

  it("never double-fires once another kill path has claimed the stage", () => {
    expect(decide({ stallKilled: true })).toBe("no-op");
    expect(decide({ costCapExceeded: true })).toBe("no-op");
  });

  it("respects the operator's Keep Waiting choice", () => {
    expect(decide({ stallKillDisabled: true })).toBe("no-op");
  });
});

describe("Nx stall-multiple ceiling arithmetic (#161)", () => {
  // Not a behavioural assertion on the gate — a pin on the derived ceiling
  // itself, so the next reader of an exit record that says
  // `kill_ceiling_value: 2400000ms` can confirm where 2400s came from without
  // re-deriving it. These are the two stages #161 lost.
  it("matches the observed feature-validate kill at exactly 2400s", () => {
    const featureValidateWarnSec = 300; // DEFAULT_STALL_THRESHOLDS
    expect(featureValidateWarnSec * 1000 * KILL_MULTIPLE).toBe(2_400_000);
  });

  it("matches the feature-dev ceiling at 4800s (NOT the observed 1800s kill)", () => {
    // feature-dev's 1800401ms kill was the plain no-progress path, not this
    // one — 1800401 is simply tick 60 of the 30s ticker. Pinning the real
    // feature-dev Nx ceiling here keeps that distinction from being lost
    // again: any future report of a feature-dev kill at 4800s IS this path.
    const featureDevWarnSec = 600; // DEFAULT_STALL_THRESHOLDS
    expect(featureDevWarnSec * 1000 * KILL_MULTIPLE).toBe(4_800_000);
  });
});
