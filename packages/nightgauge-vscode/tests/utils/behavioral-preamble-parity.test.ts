/**
 * behavioral-preamble-parity.test.ts
 *
 * Pins the extension's copy of the #77 behavioral preamble to the measured
 * eval variant (evals/variants/behavioral-preamble.json .prepend — the
 * single source of truth), and locks the Haiku-only injection semantics.
 *
 * Counterparts: internal/execution/preamble_test.go (Go) and
 * packages/nightgauge-sdk/src/orchestrator/__tests__/behavioralPreamble.test.ts.
 * Update all copies together only behind a new measurement (docs/spikes/77-*.md).
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

import {
  BEHAVIORAL_PREAMBLE,
  isHaikuModelId,
  withBehavioralPreamble,
} from "../../src/utils/behavioralPreamble";

const VARIANT_PATH = path.resolve(__dirname, "../../../../evals/variants/behavioral-preamble.json");

describe("behavioral preamble parity", () => {
  it("matches the measured eval variant byte-for-byte", () => {
    const variant = JSON.parse(readFileSync(VARIANT_PATH, "utf-8")) as { prepend?: string };
    expect(variant.prepend).toBeTruthy();
    expect(BEHAVIORAL_PREAMBLE).toBe(variant.prepend);
  });

  it("detects Haiku tier for short and dated model ids", () => {
    expect(isHaikuModelId("haiku")).toBe(true);
    expect(isHaikuModelId("claude-haiku-4-5-20251001")).toBe(true);
    expect(isHaikuModelId("sonnet")).toBe(false);
    expect(isHaikuModelId("claude-opus-4-8")).toBe(false);
    expect(isHaikuModelId(undefined)).toBe(false);
  });

  it("prepends with the eval-treatment join for Haiku only", () => {
    const prompt = "# Stage skill body";
    expect(withBehavioralPreamble(prompt, "claude-haiku-4-5-20251001")).toBe(
      `${BEHAVIORAL_PREAMBLE}\n\n${prompt}`
    );
    // Measured skip (#77): every other tier passes through unchanged.
    for (const model of [
      "sonnet",
      "claude-sonnet-5",
      "claude-opus-4-8",
      "claude-fable-5",
      undefined,
    ]) {
      expect(withBehavioralPreamble(prompt, model)).toBe(prompt);
    }
  });
});
