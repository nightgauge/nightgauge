/**
 * skillRunner.connectivityPause.test.ts
 *
 * Contract test for the connectivity-aware stall gating (Issue #3203).
 *
 * When the network goes offline mid-stage, the subagent's Anthropic API and
 * tool-call HTTP requests are blocked. It emits no chunks. Pre-#3203 the
 * idle-kill and hard-cap gates would terminate the stage anyway, costing
 * the user the in-flight stage budget AND wasting the work that had been
 * done before the outage. The fix:
 *
 *   1. While ConnectivityStateBus.state === "offline", suspend ALL kill,
 *      escalation, and warn checks. The ticker still runs; it just exits
 *      early after recording a one-shot `connectivity_paused` stall event.
 *   2. On reconnect (state moves back to online or degraded):
 *      - reset the idle window (`lastChunkAtMs = Date.now()`) so the
 *        subagent gets a fresh idle budget,
 *      - accumulate the offline duration into `connectivityAccumulatedOfflineMs`
 *        so the hard-cap counter does not count time spent waiting for
 *        the network,
 *      - emit a `connectivity_resumed` stall event.
 *
 * #498 — this file used to carry its own copies of that decision
 * (`shouldKillWithConnectivity`, `runStallTick`) and assert against the copies,
 * so it stayed green through any regression in ship (docs/TESTING.md § Testing
 * Anti-Patterns ### 7). The two halves are now named exports —
 * `evaluateConnectivityGate` and `evaluateStallKill` — and the helpers below
 * compose the real ones in the same order the ticker does.
 */

import { describe, it, expect } from "vitest";

import {
  evaluateConnectivityGate,
  evaluateStallKill,
  STALL_NUDGE_GRACE_MS,
  type ConnectivityGateState,
} from "../../src/utils/skillRunner";
import type { ConnectionState } from "../../src/platform/types";

type ConnState = ConnectionState;

/**
 * Runs the ticker's two real decision functions in the ticker's order:
 * connectivity gate first (it may suspend everything), then the stall gate on
 * offline-adjusted elapsed time. Only the result shape is adapted for these
 * cases — every value asserted is produced by shipped code.
 *
 * `nudgeAttempted: true` past its grace window is passed so the idle path
 * resolves to a kill on the tick it trips, matching what these cases describe
 * (the #3484 nudge grace is covered in skillRunner.idleStallKill.test.ts).
 */
function shouldKillWithConnectivity(args: {
  idleMs: number;
  elapsedMs: number;
  stallKillMs: number;
  hardCapMs: number;
  connState: ConnState;
  /** Accumulated offline time across prior outages within this stage. */
  accumulatedOfflineMs: number;
}): { kill: boolean; path: "idle" | "hard_cap" | "quota" | "none"; suspended: boolean } {
  const nowMs = Date.now();
  const gate = evaluateConnectivityGate(
    {
      connectivityOfflineSinceMs: args.connState === "offline" ? nowMs - 1 : null,
      connectivityAccumulatedOfflineMs: args.accumulatedOfflineMs,
    },
    { connState: args.connState, nowMs }
  );
  if (gate.suspendChecks) {
    return { kill: false, path: "none", suspended: true };
  }
  const decision = evaluateStallKill({
    idleMs: args.idleMs,
    stallKillMs: args.stallKillMs,
    elapsedMs: args.elapsedMs - gate.next.connectivityAccumulatedOfflineMs,
    hardCapMs: args.hardCapMs,
    timeCapActive: true,
    noProductiveProgress: true,
    quotaFastFailReached: false,
    stallKilled: false,
    stallKillDisabled: false,
    nudgeAttempted: true,
    nudgeAtMs: nowMs - STALL_NUDGE_GRACE_MS - 1,
    nowMs,
    nudgeGraceMs: STALL_NUDGE_GRACE_MS,
  });
  return { kill: decision.kill, path: decision.path, suspended: false };
}

describe("connectivity-aware stall gating (#3203)", () => {
  const stallKillMs = 1_200_000; // 20 min idle threshold (feature-validate)
  const hardCapMs = 1_800_000; // 30 min absolute ceiling

  it("suspends kill checks while offline regardless of elapsed/idle time", () => {
    // The exact scenario that killed issue #360: 102 min elapsed, idle for 78 min,
    // network went offline 78 min ago. Pre-#3203 this killed the stage.
    const result = shouldKillWithConnectivity({
      idleMs: 78 * 60_000,
      elapsedMs: 102 * 60_000,
      stallKillMs,
      hardCapMs,
      connState: "offline",
      accumulatedOfflineMs: 0,
    });
    expect(result).toEqual({ kill: false, path: "none", suspended: true });
  });

  it("subtracts accumulated offline time from elapsed for hard-cap comparison", () => {
    // 32 min wall-clock elapsed, 5 min of which was offline. Effective elapsed = 27 min,
    // below the 30-min hard cap. Must NOT kill.
    const result = shouldKillWithConnectivity({
      idleMs: 30_000, // recent activity
      elapsedMs: 32 * 60_000,
      stallKillMs,
      hardCapMs,
      connState: "online",
      accumulatedOfflineMs: 5 * 60_000,
    });
    expect(result).toEqual({ kill: false, path: "none", suspended: false });
  });

  it("still kills via hard-cap when productive time alone exceeds the cap", () => {
    // 31 min effective elapsed (32 wall-clock - 1 offline), past the 30 min cap.
    const result = shouldKillWithConnectivity({
      idleMs: 30_000,
      elapsedMs: 32 * 60_000,
      stallKillMs,
      hardCapMs,
      connState: "online",
      accumulatedOfflineMs: 1 * 60_000,
    });
    expect(result).toEqual({ kill: true, path: "hard_cap", suspended: false });
  });

  it("kills via idle path normally when online with no offline accumulation", () => {
    const result = shouldKillWithConnectivity({
      idleMs: 1_201_000, // past idle threshold
      elapsedMs: 1_300_000,
      stallKillMs,
      hardCapMs,
      connState: "online",
      accumulatedOfflineMs: 0,
    });
    expect(result).toEqual({ kill: true, path: "idle", suspended: false });
  });

  it("treats degraded connectivity as a non-suspended state (kill checks remain active)", () => {
    // Degraded means SOME requests are succeeding. The pipeline can still make
    // progress, so the kill gates should run normally. Only `offline` suspends.
    const result = shouldKillWithConnectivity({
      idleMs: 1_201_000,
      elapsedMs: 1_300_000,
      stallKillMs,
      hardCapMs,
      connState: "degraded",
      accumulatedOfflineMs: 0,
    });
    expect(result).toEqual({ kill: true, path: "idle", suspended: false });
  });

  it("never kills when accumulated offline equals or exceeds elapsed (degenerate case)", () => {
    // Pathological: 5 min elapsed, 5 min of which was offline (effective = 0).
    // Should be safe even with aggressive hard cap.
    const result = shouldKillWithConnectivity({
      idleMs: 0,
      elapsedMs: 5 * 60_000,
      stallKillMs,
      hardCapMs: 1 * 60_000, // 1 min cap
      connState: "online",
      accumulatedOfflineMs: 5 * 60_000,
    });
    expect(result).toEqual({ kill: false, path: "none", suspended: false });
  });
});

// ============================================================================
// Resume-tick contract (#3247)
//
// PR #3205 had a bug: `idleMs` was computed at the top of the stall ticker
// BEFORE the connectivity-resume branch ran. The resume branch reset
// `lastChunkAtMs` but the kill check that ran later in the same tick still
// used the stale (very large) `idleMs` and fired the kill on the SAME tick
// the connectivity-resumed event was emitted. Production sighting (#3220):
//
//   22:33:39 [skillRunner] Connectivity restored — resuming stall checks
//            (was offline for 24m 5s).
//   22:33:39 [skillRunner] Stage exceeded stall idle threshold (24m 0s
//            without output) — forcibly terminating after 58m 56s.
//
// Fix: the resume branch returns from the ticker callback. The next ticker
// fire (HEADLESS_STALL_CHECK_INTERVAL_MS later) recomputes `idleMs` against
// the freshly-reset `lastChunkAtMs` and runs the normal flow. This contract
// is encoded below as a small simulation of the ticker's resume-vs-kill
// ordering — keep in sync with the code in skillRunner.ts (search for
// `connectivityOfflineSinceMs !== null`).
// ============================================================================

interface TickerState {
  startedAtMs: number;
  lastChunkAtMs: number;
  connectivityOfflineSinceMs: number | null;
  connectivityAccumulatedOfflineMs: number;
}

interface TickerResult {
  killed: boolean;
  path: "idle" | "hard_cap" | "quota" | "none";
  emittedResume: boolean;
}

/**
 * The ticker's tick, assembled from the two real exports in the real order.
 * `idleMs` is computed FIRST — before the gate runs — exactly as the ticker
 * does, which is what made #3220 possible; the contract under test is that
 * `evaluateConnectivityGate` still reports `suspendChecks` on the resume tick,
 * so that stale `idleMs` never reaches `evaluateStallKill`.
 */
function runStallTick(
  state: TickerState,
  args: { now: number; connState: ConnState; stallKillMs: number; hardCapMs: number }
): TickerResult {
  const idleMs = args.now - state.lastChunkAtMs;
  const elapsed = args.now - state.startedAtMs;

  const gateState: ConnectivityGateState = {
    connectivityOfflineSinceMs: state.connectivityOfflineSinceMs,
    connectivityAccumulatedOfflineMs: state.connectivityAccumulatedOfflineMs,
  };
  const gate = evaluateConnectivityGate(gateState, { connState: args.connState, nowMs: args.now });
  state.connectivityOfflineSinceMs = gate.next.connectivityOfflineSinceMs;
  state.connectivityAccumulatedOfflineMs = gate.next.connectivityAccumulatedOfflineMs;
  if (gate.resetIdleWindow) {
    state.lastChunkAtMs = args.now;
  }
  if (gate.suspendChecks) {
    return { killed: false, path: "none", emittedResume: gate.action === "resume" };
  }

  const decision = evaluateStallKill({
    idleMs,
    stallKillMs: args.stallKillMs,
    elapsedMs: elapsed - state.connectivityAccumulatedOfflineMs,
    hardCapMs: args.hardCapMs,
    timeCapActive: true,
    noProductiveProgress: true,
    quotaFastFailReached: false,
    stallKilled: false,
    stallKillDisabled: false,
    nudgeAttempted: true,
    nudgeAtMs: args.now - STALL_NUDGE_GRACE_MS - 1,
    nowMs: args.now,
    nudgeGraceMs: STALL_NUDGE_GRACE_MS,
  });
  return { killed: decision.kill, path: decision.path, emittedResume: false };
}

describe("resume-tick early return (#3247)", () => {
  const stallKillMs = 1_200_000; // 20 min
  const hardCapMs = 1_800_000; // 30 min

  it("does NOT fire the kill on the same tick connectivity is restored, even when idleMs is huge", () => {
    // Reproduces #3220's exact scenario: 24m offline, 42m idle by wall-clock.
    // Pre-#3247 the kill fired on the same second as the resume event.
    const state: TickerState = {
      startedAtMs: 0,
      lastChunkAtMs: 0,
      connectivityOfflineSinceMs: null,
      connectivityAccumulatedOfflineMs: 0,
    };
    // t=10min: agent goes idle (last chunk at 10 min).
    state.lastChunkAtMs = 10 * 60_000;
    // t=20min: connectivity drops.
    let res = runStallTick(state, {
      now: 20 * 60_000,
      connState: "offline",
      stallKillMs,
      hardCapMs,
    });
    expect(res.killed).toBe(false);
    expect(state.connectivityOfflineSinceMs).toBe(20 * 60_000);
    // t=44min: still offline, no kill.
    res = runStallTick(state, {
      now: 44 * 60_000,
      connState: "offline",
      stallKillMs,
      hardCapMs,
    });
    expect(res.killed).toBe(false);
    // t=44min10s: connectivity restored. Resume tick MUST NOT kill, even
    // though idleMs at the top of the tick was 34min10s (over the 20min
    // threshold).
    res = runStallTick(state, {
      now: 44 * 60_000 + 10_000,
      connState: "online",
      stallKillMs,
      hardCapMs,
    });
    expect(res.killed).toBe(false);
    expect(res.emittedResume).toBe(true);
    // lastChunkAtMs was reset to the resume timestamp.
    expect(state.lastChunkAtMs).toBe(44 * 60_000 + 10_000);
    // accumulated offline duration is recorded.
    expect(state.connectivityAccumulatedOfflineMs).toBe(24 * 60_000 + 10_000);
  });

  it("the NEXT tick after resume runs normally with a fresh idleMs", () => {
    // Same setup as above, then advance 30s and verify the next tick uses
    // the fresh idleMs (30s, well under the 20min threshold).
    const state: TickerState = {
      startedAtMs: 0,
      lastChunkAtMs: 10 * 60_000,
      connectivityOfflineSinceMs: 20 * 60_000,
      connectivityAccumulatedOfflineMs: 0,
    };
    // Resume tick.
    runStallTick(state, {
      now: 44 * 60_000 + 10_000,
      connState: "online",
      stallKillMs,
      hardCapMs,
    });
    // Next tick 30s later — agent still hasn't emitted a chunk.
    const next = runStallTick(state, {
      now: 44 * 60_000 + 40_000,
      connState: "online",
      stallKillMs,
      hardCapMs,
    });
    // 30s of idle is well under the 20min threshold — no kill.
    expect(next.killed).toBe(false);
    expect(next.path).toBe("none");
  });

  it("a kill DOES fire on the second tick if the agent is still idle past the threshold", () => {
    // After resume, give the agent a fresh idle window. If they STILL go
    // silent for stallKillMs after the resume, the idle kill must fire.
    // Use a generous hard cap so idle is the deciding factor (otherwise
    // hard-cap on effective-elapsed would fire first).
    const generousHardCap = 0; // disabled
    const state: TickerState = {
      startedAtMs: 0,
      lastChunkAtMs: 10 * 60_000,
      connectivityOfflineSinceMs: 20 * 60_000,
      connectivityAccumulatedOfflineMs: 0,
    };
    runStallTick(state, {
      now: 44 * 60_000 + 10_000,
      connState: "online",
      stallKillMs,
      hardCapMs: generousHardCap,
    });
    // Fast-forward 21 min past the resume — agent never spoke. Idle kill fires.
    const next = runStallTick(state, {
      now: 44 * 60_000 + 10_000 + 21 * 60_000,
      connState: "online",
      stallKillMs,
      hardCapMs: generousHardCap,
    });
    expect(next.killed).toBe(true);
    expect(next.path).toBe("idle");
  });
});
