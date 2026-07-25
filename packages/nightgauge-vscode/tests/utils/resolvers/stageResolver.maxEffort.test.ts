/**
 * `max` effort tier — end-to-end representability guard (#75).
 *
 * Opus 5 introduced `max` at the top of the effort ladder. Before this issue the
 * level was unreachable from every configuration surface: the `ClaudeEffort`
 * union, the validator array, and both config-parsing regexes stopped at
 * `xhigh`. These tests assert the level survives each hop, and that an
 * unsupported level fails loudly rather than being silently downgraded.
 *
 * @see Issue #75 - thread the max effort tier end-to-end
 */

import { describe, it, expect } from "vitest";
import {
  assertEffortSupported,
  conformEffortForFable,
  type ClaudeEffort,
} from "../../../src/utils/resolvers/stageResolver";
import { EFFORT_LEVELS } from "@nightgauge/sdk";

describe("max effort tier", () => {
  it("is present in the SDK effort enum (parity with ClaudeEffort)", () => {
    // A compile-time guard in modelEvalSchemas.ts already enforces that these
    // two stay identical; this asserts the runtime array actually shipped it,
    // since the guard passes on a type-only change.
    expect(EFFORT_LEVELS).toContain("max");
  });

  it("orders above xhigh so envelope clamping treats it as the ceiling", () => {
    const order = [...EFFORT_LEVELS];
    expect(order.indexOf("max")).toBeGreaterThan(order.indexOf("xhigh"));
  });

  it("passes through Fable conformance untouched when set explicitly", () => {
    // Only low/medium are floored; max is already at or above Fable's default.
    const result = conformEffortForFable("max" as ClaudeEffort, "max" as ClaudeEffort, "config");
    expect(result.effort).toBe("max");
    expect(result.coerced).toBe(false);
  });
});

describe("assertEffortSupported", () => {
  const OPUS_5 = ["low", "medium", "high", "xhigh", "max"];
  const OPUS_4_8 = ["low", "medium", "high", "xhigh"];

  it("accepts a level the model supports", () => {
    expect(() => assertEffortSupported("max", "claude-opus-5", OPUS_5)).not.toThrow();
  });

  it("throws — never downgrades — when the model lacks the level", () => {
    expect(() => assertEffortSupported("max", "claude-opus-4-8", OPUS_4_8)).toThrow(
      /not supported/
    );
  });

  it("names the model, the level, and what is supported", () => {
    let message = "";
    try {
      assertEffortSupported("max", "claude-opus-4-8", OPUS_4_8, "feature-dev");
    } catch (err) {
      message = (err as Error).message;
    }
    expect(message).toContain("max");
    expect(message).toContain("claude-opus-4-8");
    expect(message).toContain("feature-dev");
    expect(message).toContain("xhigh");
  });

  it("skips validation for models the registry does not know", () => {
    // Local ollama/lm-studio models have no registry entry by design — there is
    // nothing to validate against, and rejecting would break local runs.
    expect(() => assertEffortSupported("max", "qwen3-coder:32b", undefined)).not.toThrow();
    expect(() => assertEffortSupported("max", "qwen3-coder:32b", [])).not.toThrow();
  });
});
