/**
 * skillRunner.idleStallKill.test.ts
 *
 * Contract test for the idle-vs-elapsed stall-kill split (Issue #3155 / #338).
 *
 * Before #3155, the stall-kill path used `Date.now() - startedAtMs` (total
 * elapsed) as its kill threshold, which meant a productive long stage that
 * was still emitting tool_use/assistant chunks every few seconds got killed
 * the moment it crossed `stall_kill_multiplier × stall_threshold`. The
 * #338 incident is the canonical case: feature-validate ran 1201s of
 * productive work, was killed at the 20-min cap, $1.93 burnt, queue drained.
 *
 * The fix splits the gate into two:
 *   - `stall_kill_multiplier × stall_threshold` is the IDLE threshold —
 *     time since last stdout/stderr chunk from the subprocess.
 *   - `stage_hard_caps.<stage>` is the absolute ceiling — total runtime
 *     since stage start, fired regardless of activity.
 *
 * The two checks live in the same ticker (skillRunner.ts) and either can
 * fire independently. This file documents the decision matrix so future
 * changes don't quietly merge the two thresholds back together.
 */

import { describe, it, expect } from "vitest";

const NUDGE_GRACE_MS = 60_000;

/**
 * Mirrors the kill-decision logic in skillRunner.ts. Keep in sync —
 * search the file for `idleKillThresholdReached` to find the source.
 */
function shouldKill(args: {
  idleMs: number;
  elapsedMs: number;
  stallKillMs: number;
  hardCapMs: number;
  stallKilled: boolean;
  stallKillDisabled: boolean;
  nudgeAttempted?: boolean;
  nudgeAtMs?: number;
  nowMs?: number;
}): { kill: boolean; path: "idle" | "hard_cap" | "none"; nudge?: boolean } {
  const idleKillThresholdReached = args.stallKillMs > 0 && args.idleMs >= args.stallKillMs;
  const hardCapReached = args.hardCapMs > 0 && args.elapsedMs >= args.hardCapMs;
  if (args.stallKilled || args.stallKillDisabled) return { kill: false, path: "none" };
  if (hardCapReached) return { kill: true, path: "hard_cap" };

  // Nudge grace period logic
  const nudgeAttempted = args.nudgeAttempted ?? false;
  const nudgeAtMs = args.nudgeAtMs;
  const nowMs = args.nowMs ?? Date.now();
  const pastNudgeGrace =
    nudgeAttempted && nudgeAtMs !== undefined && nowMs - nudgeAtMs >= NUDGE_GRACE_MS;

  if (idleKillThresholdReached && !nudgeAttempted) {
    return { kill: false, path: "none", nudge: true };
  }
  if (idleKillThresholdReached && nudgeAttempted && pastNudgeGrace) {
    return { kill: true, path: "idle" };
  }
  if (idleKillThresholdReached && nudgeAttempted && !pastNudgeGrace) {
    return { kill: false, path: "none" };
  }
  return { kill: false, path: "none" };
}

describe("idle-vs-elapsed stall-kill split (#3155)", () => {
  const stallKillMs = 1_200_000; // feature-validate default: 20 min idle
  const hardCapMs = 0; // no hard cap by default

  it("does NOT kill a productive long stage when idle time is small (the #338 regression)", () => {
    // Feature-validate at the 20-min mark, but every chunk arrived <30s ago.
    // Pre-#3155 this killed the stage; post-#3155 it must not.
    const result = shouldKill({
      idleMs: 30_000,
      elapsedMs: 1_201_000,
      stallKillMs,
      hardCapMs,
      stallKilled: false,
      stallKillDisabled: false,
    });
    expect(result.kill).toBe(false);
  });

  it("emits nudge (not kill) when idle threshold is first reached", () => {
    // First tick past the idle threshold: nudge fires, kill deferred.
    const result = shouldKill({
      idleMs: 1_201_000,
      elapsedMs: 1_201_000,
      stallKillMs,
      hardCapMs,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: false,
    });
    expect(result.kill).toBe(false);
    expect(result.nudge).toBe(true);
  });

  it("kills a genuinely silent subprocess after nudge grace period expires", () => {
    const nudgeAtMs = Date.now() - NUDGE_GRACE_MS - 1000; // grace expired
    const result = shouldKill({
      idleMs: 1_261_000,
      elapsedMs: 1_261_000,
      stallKillMs,
      hardCapMs,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: true,
      nudgeAtMs,
      nowMs: nudgeAtMs + NUDGE_GRACE_MS + 1000,
    });
    expect(result).toEqual({ kill: true, path: "idle" });
  });

  it("defers kill during active nudge grace period", () => {
    const nudgeAtMs = Date.now() - 30_000; // 30s into 60s grace
    const result = shouldKill({
      idleMs: 1_230_000,
      elapsedMs: 1_230_000,
      stallKillMs,
      hardCapMs,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: true,
      nudgeAtMs,
      nowMs: nudgeAtMs + 30_000,
    });
    expect(result.kill).toBe(false);
  });

  it("kills a stage that crosses stage_hard_caps regardless of recent activity (no nudge)", () => {
    // 30s idle (productive) but absolute cap of 30 min reached — hard cap wins immediately.
    const result = shouldKill({
      idleMs: 30_000,
      elapsedMs: 1_801_000,
      stallKillMs,
      hardCapMs: 1_800_000,
      stallKilled: false,
      stallKillDisabled: false,
    });
    expect(result).toEqual({ kill: true, path: "hard_cap" });
  });

  it("hard_cap takes precedence when both thresholds trip in the same tick", () => {
    // Both gates would kill — hard_cap fires immediately (no nudge grace).
    const result = shouldKill({
      idleMs: 1_201_000,
      elapsedMs: 1_801_000,
      stallKillMs,
      hardCapMs: 1_800_000,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: true,
      nudgeAtMs: Date.now() - NUDGE_GRACE_MS - 1000,
      nowMs: Date.now(),
    });
    expect(result).toEqual({ kill: true, path: "hard_cap" });
  });

  it("respects user 'Keep Waiting' opt-out (stallKillDisabled) on both paths", () => {
    expect(
      shouldKill({
        idleMs: 5_000_000,
        elapsedMs: 5_000_000,
        stallKillMs,
        hardCapMs: 1_000_000,
        stallKilled: false,
        stallKillDisabled: true,
      })
    ).toEqual({ kill: false, path: "none" });
  });

  it("treats stallKillMs=0 (cold start / kill disabled) as no idle threshold", () => {
    // A long-idle subprocess with kill disabled — no kill, no nudge.
    expect(
      shouldKill({
        idleMs: 9_999_000,
        elapsedMs: 9_999_000,
        stallKillMs: 0,
        hardCapMs: 0,
        stallKilled: false,
        stallKillDisabled: false,
      })
    ).toEqual({ kill: false, path: "none" });
  });

  it("hard_cap still fires even when idle threshold is disabled", () => {
    // Cold-start (no calibration data → stallKillMs=0) but explicit ceiling set.
    expect(
      shouldKill({
        idleMs: 0,
        elapsedMs: 3_600_000,
        stallKillMs: 0,
        hardCapMs: 3_600_000,
        stallKilled: false,
        stallKillDisabled: false,
      })
    ).toEqual({ kill: true, path: "hard_cap" });
  });

  it("nudge resets when new output arrives before grace expires", () => {
    // idleMs drops below threshold after new output → nudge resets
    const result = shouldKill({
      idleMs: 100, // new output arrived
      elapsedMs: 1_500_000,
      stallKillMs,
      hardCapMs,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: true,
      nudgeAtMs: Date.now() - 30_000,
      nowMs: Date.now(),
    });
    expect(result.kill).toBe(false);
    expect(result.nudge).toBeUndefined();
  });
});

describe("stall_idle_ms override (#3484)", () => {
  it("stall_idle_ms=480000 fires at 8 min idle instead of computed 20 min", () => {
    const stallKillMs = 480_000; // 8 min override

    // At 8 min idle: should nudge (first time threshold reached)
    const nudge = shouldKill({
      idleMs: 480_001,
      elapsedMs: 480_001,
      stallKillMs,
      hardCapMs: 0,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: false,
    });
    expect(nudge.nudge).toBe(true);
    expect(nudge.kill).toBe(false);

    // After grace expires: kill fires
    const nudgeAtMs = Date.now() - NUDGE_GRACE_MS - 1;
    const kill = shouldKill({
      idleMs: 540_001,
      elapsedMs: 540_001,
      stallKillMs,
      hardCapMs: 0,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: true,
      nudgeAtMs,
      nowMs: nudgeAtMs + NUDGE_GRACE_MS + 1,
    });
    expect(kill).toEqual({ kill: true, path: "idle" });
  });

  it("hard-cap kill skips nudge grace entirely", () => {
    const result = shouldKill({
      idleMs: 10_000,
      elapsedMs: 1_800_001,
      stallKillMs: 480_000,
      hardCapMs: 1_800_000,
      stallKilled: false,
      stallKillDisabled: false,
      nudgeAttempted: false,
    });
    expect(result).toEqual({ kill: true, path: "hard_cap" });
  });
});
