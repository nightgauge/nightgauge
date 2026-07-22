/**
 * modeProfiles.test.ts (Issue #3009)
 *
 * Asserts the centralized mode → per-stage profile table is well-formed
 * for every PipelineStage in STAGE_ORDER. Future tuning that drops a
 * stage from a mode profile is caught here.
 */
import { describe, it, expect } from "vitest";
import {
  MODE_PROFILES,
  PERFORMANCE_MODES,
  DEFAULT_PERFORMANCE_MODE,
  isPerformanceMode,
  getModeStageProfile,
  getModeEnvelope,
  type PerformanceMode,
} from "../../src/utils/modeProfiles";

const SUB_AGENT_STAGES: ReadonlyArray<
  | "issue-pickup"
  | "feature-planning"
  | "feature-dev"
  | "feature-validate"
  | "pr-create"
  | "pr-merge"
> = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

describe("modeProfiles", () => {
  it("exposes exactly four modes", () => {
    expect(PERFORMANCE_MODES).toEqual(["efficiency", "elevated", "maximum", "frontier"]);
  });

  it("defaults to elevated", () => {
    expect(DEFAULT_PERFORMANCE_MODE).toBe("elevated");
  });

  it("isPerformanceMode rejects unknown strings", () => {
    expect(isPerformanceMode("supercharge")).toBe(false);
    expect(isPerformanceMode("Maximum")).toBe(false);
    expect(isPerformanceMode("")).toBe(false);
    expect(isPerformanceMode(null)).toBe(false);
    expect(isPerformanceMode("efficiency")).toBe(true);
    expect(isPerformanceMode("elevated")).toBe(true);
    expect(isPerformanceMode("maximum")).toBe(true);
    expect(isPerformanceMode("frontier")).toBe(true);
  });

  it("every mode has a label, description, and costHint", () => {
    for (const mode of PERFORMANCE_MODES) {
      const profile = MODE_PROFILES[mode];
      expect(profile.label.length).toBeGreaterThan(0);
      expect(profile.description.length).toBeGreaterThan(0);
      expect(profile.costHint.length).toBeGreaterThan(0);
    }
  });

  // ---- Envelopes (Issue #19) ----

  it("efficiency, elevated, frontier are envelope-driven (no per-stage pins)", () => {
    for (const mode of ["efficiency", "elevated", "frontier"] as const) {
      expect(MODE_PROFILES[mode].stages).toEqual({});
      for (const stage of SUB_AGENT_STAGES) {
        expect(getModeStageProfile(mode, stage)).toBeUndefined();
      }
    }
  });

  it("elevated is the open envelope: haiku..opus", () => {
    expect(getModeEnvelope("elevated")).toMatchObject({ floor: "haiku", ceiling: "opus" });
    expect(MODE_PROFILES.elevated.pipeline).toEqual({});
  });

  it("efficiency envelope caps at Sonnet with an effort ceiling", () => {
    const env = getModeEnvelope("efficiency");
    expect(env.floor).toBe("haiku");
    expect(env.ceiling).toBe("sonnet");
    expect(env.effortCeiling).toBe("medium");
  });

  it("frontier envelope lifts the ceiling to Fable, floor stays Haiku", () => {
    expect(getModeEnvelope("frontier")).toMatchObject({ floor: "haiku", ceiling: "fable" });
  });

  it("frontier is the ONLY mode whose envelope ceiling reaches Fable", () => {
    for (const mode of ["efficiency", "elevated", "maximum"] as const) {
      expect(getModeEnvelope(mode).ceiling).not.toBe("fable");
    }
    expect(getModeEnvelope("frontier").ceiling).toBe("fable");
  });

  it("frontier keeps the budget ceiling ENABLED (Fable is the most expensive tier)", () => {
    // Unlike maximum, frontier must NOT disable the budget ceiling.
    expect(MODE_PROFILES.frontier.pipeline.disableBudgetCeiling).toBeUndefined();
    expect(MODE_PROFILES.frontier.pipeline.stallKillMultiplier).toBe(10);
  });

  // ---- Maximum still pins (deliberate "cost no object" mode) ----

  it("maximum forces opus + effort=high on every sub-agent stage", () => {
    for (const stage of SUB_AGENT_STAGES) {
      const profile = getModeStageProfile("maximum", stage);
      expect(profile).toBeDefined();
      expect(profile?.model).toBe("opus");
      expect(profile?.effort).toBe("high");
    }
  });

  it("maximum sets stallKillMultiplier=10 and disables the budget ceiling", () => {
    expect(MODE_PROFILES.maximum.pipeline.stallKillMultiplier).toBe(10);
    expect(MODE_PROFILES.maximum.pipeline.disableBudgetCeiling).toBe(true);
  });

  it("every PerformanceMode is reachable via PERFORMANCE_MODES", () => {
    const reachable = new Set<PerformanceMode>(PERFORMANCE_MODES);
    expect(reachable.has("efficiency")).toBe(true);
    expect(reachable.has("elevated")).toBe(true);
    expect(reachable.has("maximum")).toBe(true);
    expect(reachable.has("frontier")).toBe(true);
    expect(reachable.size).toBe(4);
  });
});
