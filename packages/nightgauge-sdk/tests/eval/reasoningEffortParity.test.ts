/**
 * #435 (absorbed by #581): the Codex reasoning-effort vocabulary derives from
 * ONE authority — `REASONING_EFFORT_LEVELS = ["none", ...EFFORT_LEVELS]` —
 * and every derived spelling stays pinned to it, matching the #394 pattern
 * for the plain effort axis. The four pre-#435 hand-listed copies (the
 * modelResolver literal union + regex alternation, the config schema's Zod
 * enum, the adapter's vocabulary list) all derive now; this test is the drift
 * alarm.
 */

import { describe, it, expect } from "vitest";
import {
  EFFORT_LEVELS,
  REASONING_EFFORT_LEVELS,
  REASONING_EFFORT_ALTERNATION,
} from "../../src/eval/modelEvalSchemas.js";
import { CODEX_REASONING_EFFORTS } from "../../src/cli/adapters/codexEffort.js";

describe("REASONING_EFFORT_LEVELS — the single authority (#435)", () => {
  it("is exactly none + the canonical effort ladder, in ladder order", () => {
    expect(REASONING_EFFORT_LEVELS).toEqual(["none", ...EFFORT_LEVELS]);
    // Pin the concrete spelling so an accidental EFFORT_LEVELS edit is loud
    // here too, not just in the effort-authority tests.
    expect(REASONING_EFFORT_LEVELS).toEqual(["none", "low", "medium", "high", "xhigh", "max"]);
  });

  it("derives the adapter vocabulary — same array, not a copy", () => {
    expect(CODEX_REASONING_EFFORTS).toBe(REASONING_EFFORT_LEVELS);
  });

  it("derives the regex alternation", () => {
    expect(REASONING_EFFORT_ALTERNATION).toBe("none|low|medium|high|xhigh|max");
    // The alternation must round-trip every level and nothing else.
    const re = new RegExp(`^(${REASONING_EFFORT_ALTERNATION})$`);
    for (const level of REASONING_EFFORT_LEVELS) {
      expect(re.test(level)).toBe(true);
    }
    expect(re.test("minimal")).toBe(false);
  });
});
