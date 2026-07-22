// Tests for composeStallKilledError() — Issue #3442.
//
// Pre-fix the stallKilled branch in HeadlessOrchestrator synthesized a
// generic `[stall-killed] {stage} terminated...` Error and discarded the
// upstream `result.error` from skillRunner. That destroyed the
// `[rate-limit-quota-exhausted]` kill marker (#3386) before it could reach
// bootstrap/services.ts:1261's terminalFailureKind regex, which in turn
// starved PR #3440's Go-side fallback of any usable failureDetail. Net
// effect: the global Anthropic-quota cooldown (#3434) was silently bypassed
// on every quota-exhausted kill, and the lifetime failure cap incremented
// instead.
//
// These tests assert the helper now preserves recognizable markers so the
// downstream classification chain still works.

import { describe, expect, it } from "vitest";

import { composeStallKilledError } from "../../src/services/HeadlessOrchestrator";

describe("composeStallKilledError", () => {
  it("preserves the [rate-limit-quota-exhausted] marker in the composed message", () => {
    const upstream = new Error(
      "[skillRunner] Stage [rate-limit-quota-exhausted] idle 2m 14s after rate_limit_event with " +
        "overage rejected (five_hour bucket; resetsAt=1778446800) — forcibly terminating process after 4m 0s"
    );
    const composed = composeStallKilledError("pr-create", 240_000, upstream);

    // Still announces the stall-kill classification for retro/dashboards.
    expect(composed.message).toMatch(/^\[stall-killed\] pr-create terminated:/);
    // CRITICAL: must carry the rate-limit-quota-exhausted marker through to
    // bootstrap/services.ts so terminalFailureKind classification matches.
    expect(composed.message).toContain("[rate-limit-quota-exhausted]");
    // Resets-at hint must survive so the Go scheduler can extract it for
    // computeQuotaCooldownUntil().
    expect(composed.message).toContain("resetsAt=1778446800");
  });

  it("preserves a stream-idle-timeout marker (the other quota-adjacent kill path)", () => {
    const upstream = new Error("stage feature-dev failed: stream idle timeout after 240s");
    const composed = composeStallKilledError("feature-dev", 240_000, upstream);

    expect(composed.message).toMatch(/^\[stall-killed\] feature-dev terminated:/);
    expect(composed.message.toLowerCase()).toContain("stream idle timeout");
  });

  it("falls back to the generic stall-killed message when upstream error is missing", () => {
    const composed = composeStallKilledError("pr-create", 240_000, undefined);

    expect(composed.message).toMatch(/^\[stall-killed\] pr-create terminated:/);
    expect(composed.message).not.toContain("Upstream signal:");
    // Does not invent a marker that wasn't there — the Go fallback should
    // route this through the GENERIC branch as a true stall.
    expect(composed.message).not.toContain("[rate-limit-quota-exhausted]");
  });

  it("falls back to the generic message when upstream error has no recognized marker", () => {
    const upstream = new Error("Process exited with code 1");
    const composed = composeStallKilledError("pr-create", 240_000, upstream);

    expect(composed.message).toMatch(/^\[stall-killed\] pr-create terminated:/);
    expect(composed.message).not.toContain("Upstream signal:");
  });

  it("rounds duration to seconds in the explanation", () => {
    const composed = composeStallKilledError("pr-create", 12_345, undefined);
    expect(composed.message).toContain("ran for 12s");
  });
});
