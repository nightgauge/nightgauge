/**
 * Guard against adapter-enum drift between the single source of truth
 * (AdapterEnumSchema) and runtime parsing. Pipeline UI surfaces intentionally
 * expose only the subset with agentic tool loops.
 *
 * @see Issue #4030 - settings adapter-enum drift
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { AdapterEnumSchema, DefaultModelSchema } from "../../src/config/schema";
import { STAGE_ADAPTER_OPTIONS, STAGE_MODEL_OPTIONS } from "../../src/views/settings/SettingsHtml";
import { VALID_ADAPTERS } from "../../src/utils/resolvers/modelResolver";

const CANONICAL = [...AdapterEnumSchema.options].sort();
const AGENTIC_PIPELINE_ADAPTERS = ["claude", "codex", "copilot", "gemini"].sort();

function readCoreAdapterEnum(): { enum: string[]; enumDescriptions: string[] } {
  const pkgPath = resolve(__dirname, "../../package.json");
  const pkg = JSON.parse(readFileSync(pkgPath, "utf-8"));
  const cfg = pkg.contributes.configuration;
  const blocks = Array.isArray(cfg) ? cfg : [cfg];
  for (const blk of blocks) {
    const prop = blk?.properties?.["nightgauge.core.adapter"];
    if (prop) return { enum: prop.enum, enumDescriptions: prop.enumDescriptions };
  }
  throw new Error("nightgauge.core.adapter not found in package.json contributions");
}

describe("adapter enum sync (#4030)", () => {
  it("package.json nightgauge.core.adapter enum exposes only agentic adapters", () => {
    const { enum: pkgEnum } = readCoreAdapterEnum();
    expect([...pkgEnum].sort()).toEqual(AGENTIC_PIPELINE_ADAPTERS);
  });

  it("package.json provides one enumDescription per adapter (no count drift)", () => {
    const { enum: pkgEnum, enumDescriptions } = readCoreAdapterEnum();
    expect(enumDescriptions).toHaveLength(pkgEnum.length);
  });

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

  it("Codex is selectable in both the global enum and the per-stage matrix", () => {
    const { enum: pkgEnum } = readCoreAdapterEnum();
    expect(pkgEnum).toContain("codex");
    expect(STAGE_ADAPTER_OPTIONS.some((o) => o.value === "codex")).toBe(true);
  });
});
