// Tests for composeStallKilledError() — Issue #3442.
//
// Pre-fix the stallKilled branch in HeadlessOrchestrator synthesized a
// generic `[stall-killed] {stage} terminated...` Error and discarded the
// upstream `result.error` from skillRunner. That destroyed the
// `[rate-limit-quota-exhausted]` kill marker (#3386) before it could reach
// the terminal-kind rule table (internal/terminalkind/table.json, rule
// `rate-limit-quota-exhausted`), which in turn starved PR #3440's Go-side
// fallback of any usable failureDetail. Net
// effect: the global Anthropic-quota cooldown (#3434) was silently bypassed
// on every quota-exhausted kill, and the lifetime failure cap incremented
// instead.
//
// These tests assert the helper now preserves recognizable markers so the
// downstream classification chain still works.

import { describe, expect, it } from "vitest";

import {
  composeStageCapKilledError,
  composeStallKilledError,
} from "../../src/services/HeadlessOrchestrator";
import { describeStageCapKill, isStageCapCeiling } from "../../src/utils/killCeiling";

describe("composeStallKilledError", () => {
  it("preserves the [rate-limit-quota-exhausted] marker in the composed message", () => {
    const upstream = new Error(
      "[skillRunner] Stage [rate-limit-quota-exhausted] idle 2m 14s after rate_limit_event with " +
        "overage rejected (five_hour bucket; resetsAt=1778446800) — forcibly terminating process after 4m 0s"
    );
    const composed = composeStallKilledError("pr-create", 240_000, upstream);

    // Still announces the stall-kill classification for retro/dashboards.
    expect(composed.message).toMatch(/^\[stall-killed\] pr-create terminated:/);
    // CRITICAL: must carry the rate-limit-quota-exhausted marker through so
    // the rate-limit-quota-exhausted rule in internal/terminalkind/table.json
    // still claims it.
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

describe("composeStageCapKilledError (#2812)", () => {
  const hard = "2700000ms (pipeline.stage_hard_caps.feature-dev (total runtime, progress-gated))";
  const time =
    "1200000ms (min(pipeline.stage_hard_caps.feature-dev=2700000ms, " +
    "pipeline.stage_time_caps.feature-dev=1200000ms) — adapter=ollama has provider_scale=0)";

  it.each([
    ["stage-hard-cap", hard, "pipeline.stage_hard_caps"],
    ["stage-time-cap", time, "pipeline.stage_time_caps"],
  ])("names the cap, value and elapsed time for %s, never a stall", (ceiling, value, key) => {
    const { message } = composeStageCapKilledError(
      "feature-dev",
      2_700_000,
      ceiling,
      value,
      undefined
    );
    expect(message).toMatch(/^\[stage-cap-killed\] feature-dev terminated:/);
    expect(message).toContain(`${key}=${value}`);
    expect(message).toContain("after 2700s");
    expect(message).not.toMatch(/stall/i);
  });

  it("keeps an upstream quota marker", () => {
    const { message } = composeStageCapKilledError(
      "feature-dev",
      1000,
      "stage-hard-cap",
      hard,
      new Error("[rate-limit-quota-exhausted] idle 2m 0s")
    );
    expect(message).toContain("Upstream signal: [rate-limit-quota-exhausted]");
  });

  it("leaves the idle kill's text byte-identical", () => {
    expect(composeStallKilledError("pr-create", 1_800_000, undefined).message).toBe(
      "[stall-killed] pr-create terminated: subagent process exceeded stall kill threshold. " +
        "The process ran for 1800s without completing. " +
        "Configure pipeline.stall_kill_multiplier or pipeline.stage_hard_caps to adjust (0 to disable)."
    );
  });

  it("recognises only the two cap ceilings", () => {
    expect(isStageCapCeiling("stage-hard-cap")).toBe(true);
    expect(isStageCapCeiling("stage-time-cap")).toBe(true);
    expect(isStageCapCeiling("stall-idle")).toBe(false);
    expect(isStageCapCeiling("nx-stall-multiple")).toBe(false);
    expect(isStageCapCeiling(undefined)).toBe(false);
  });

  it("gives SkillRunner the same marker, cap and elapsed time", () => {
    expect(describeStageCapKill("pr-create", 900_000, "stage-time-cap", "900000ms")).toBe(
      "[stage-cap-killed] pr-create terminated: reached pipeline.stage_time_caps=900000ms after 900s"
    );
  });
});
