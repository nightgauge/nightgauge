/**
 * Guard against adapter-enum drift between the single source of truth
 * (AdapterEnumSchema) and runtime parsing. Pipeline UI surfaces intentionally
 * expose only the subset with agentic tool loops.
 *
 * The `nightgauge.core.adapter` VS Code setting was a third drift surface until
 * it was deleted as unread (#968) — it reached no code, so guarding its enum
 * guarded nothing. The remaining surfaces are the settings webview matrix and
 * the runtime resolver, both of which are live.
 *
 * @see Issue #4030 - settings adapter-enum drift
 */

import { describe, it, expect } from "vitest";
import { AdapterEnumSchema, DefaultModelSchema } from "../../src/config/schema";
import { STAGE_ADAPTER_OPTIONS, STAGE_MODEL_OPTIONS } from "../../src/views/settings/SettingsHtml";
import { VALID_ADAPTERS } from "../../src/utils/resolvers/modelResolver";

const CANONICAL = [...AdapterEnumSchema.options].sort();
const AGENTIC_PIPELINE_ADAPTERS = ["claude", "codex", "copilot", "gemini", "grok"].sort();

describe("adapter enum sync (#4030)", () => {
  it("the per-stage matrix dropdown exposes only agentic adapters", () => {
    const matrixAdapters = STAGE_ADAPTER_OPTIONS.map((o) => o.value)
      .filter((v) => v !== "")
      .sort();
    expect(matrixAdapters).toEqual(AGENTIC_PIPELINE_ADAPTERS);
  });

  it("runtime VALID_ADAPTERS == AdapterEnumSchema (no silent-drop drift, #4030)", () => {
    // The schema comment warns that a drift here silently drops a user's adapter
    // selection to the default. ollama was the historical gap.
    expect([...VALID_ADAPTERS].sort()).toEqual(CANONICAL);
    expect(VALID_ADAPTERS).toContain("ollama");
  });

  it("per-stage model dropdown tiers == DefaultModelSchema (no silent-drop drift)", () => {
    // A tier offered in the UI but missing from the resolver's allow-list/regex
    // would silently no-op (the `fable` gap). Guard tier parity both ways.
    const uiTiers = STAGE_MODEL_OPTIONS.map((o) => o.value)
      .filter((v) => v !== "")
      .sort();
    expect(uiTiers).toEqual([...DefaultModelSchema.options].sort());
  });

  it("Codex is selectable in the canonical enum and the per-stage matrix", () => {
    expect(AdapterEnumSchema.options).toContain("codex");
    expect(STAGE_ADAPTER_OPTIONS.some((o) => o.value === "codex")).toBe(true);
  });
});
