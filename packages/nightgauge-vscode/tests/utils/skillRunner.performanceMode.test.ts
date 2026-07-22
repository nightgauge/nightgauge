/**
 * skillRunner.performanceMode.test.ts (Issue #3009, Issue #19)
 *
 * Validates mode-aware routing in `resolveModel`:
 *   - elevated → open envelope (haiku..opus), router runs unchanged.
 *   - maximum → still pins Opus + effort=high via the performance-mode profile.
 *   - efficiency / frontier → policy ENVELOPES (Issue #19): no per-stage pins;
 *     the router runs and its pick is clamped to the mode's [floor, ceiling].
 *     Frontier reaches Fable only on L/XL planning/dev.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
}));

vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<typeof import("../../src/utils/incrediConfig")>(
    "../../src/utils/incrediConfig"
  );
  return {
    ...actual,
    getPerformanceMode: vi.fn(() => "elevated"),
    getStageEffort: vi.fn(() => "medium"),
    getStageModel: vi.fn(() => undefined),
    getStageOverrideModel: vi.fn(() => undefined),
    getDefaultModel: vi.fn(() => "sonnet"),
    getModelRoutingMode: vi.fn(() => "automatic"),
    getLargeDiffThreshold: vi.fn(() => 500),
    getExperimentConfig: vi.fn(() => undefined),
    getConfidenceThreshold: vi.fn(() => 0.5),
    getMinimumModel: vi.fn(() => undefined),
  };
});

import { resolveModel } from "../../src/utils/skillRunner";
import { getPerformanceMode, getStageEffort } from "../../src/utils/incrediConfig";

const L = { labels: ["size:L"], title: "Large architectural change" };
const XS = { labels: ["size:XS"], title: "Trivial one-line fix" };

describe("resolveModel — performance mode (Issue #3009, #19)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getStageEffort).mockReturnValue("medium");
  });

  it("elevated supplies no pin — falls through to the routing chain", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    const result = resolveModel("feature-dev", "/test/workspace");
    expect(result.source).not.toBe("performance-mode");
    expect(result.source).not.toBe("supercharge");
  });

  it("maximum still pins feature-dev to opus + effort=high", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    const result = resolveModel("feature-dev", "/test/workspace");
    expect(result.source).toBe("performance-mode");
    expect(result.model).toBe("opus");
    expect(result.effort).toBe("high");
  });

  it("maximum overrides effort even when stage default is low", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    vi.mocked(getStageEffort).mockReturnValue("low");
    const result = resolveModel("pr-create", "/test/workspace");
    expect(result.source).toBe("performance-mode");
    expect(result.effort).toBe("high");
  });

  // ---- Efficiency envelope (Issue #19): [haiku, sonnet] ----

  it("efficiency is envelope-driven — no performance-mode pin", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    const result = resolveModel("feature-dev", "/test/workspace", L);
    expect(result.source).not.toBe("performance-mode");
  });

  it("efficiency caps an L feature-dev (would be Opus) down to Sonnet", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    // AutoModelSelector routes L dev → opus; the envelope ceiling clamps to sonnet.
    const result = resolveModel("feature-dev", "/test/workspace", L);
    expect(result.model).toBe("sonnet");
    expect(result.effort).toBe("medium"); // effortCeiling caps at medium
  });

  it("efficiency keeps issue-pickup on haiku (lightweight, within band)", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    const result = resolveModel("issue-pickup", "/test/workspace");
    expect(result.model).toBe("haiku");
  });

  // ---- Frontier envelope (Issue #19): [haiku, fable], Fable on L/XL reasoning only ----

  it("frontier escalates an L feature-dev to Fable", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("frontier");
    const result = resolveModel("feature-dev", "/test/workspace", L);
    expect(result.model).toBe("fable");
  });

  it("frontier escalates an L feature-planning to Fable", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("frontier");
    const result = resolveModel("feature-planning", "/test/workspace", L);
    expect(result.model).toBe("fable");
  });

  it("frontier does NOT use Fable for feature-validate even at L (data-driven rule)", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("frontier");
    const result = resolveModel("feature-validate", "/test/workspace", L);
    // validate L → opus in the matrix; frontier escalation excludes validate.
    expect(result.model).toBe("opus");
    expect(result.model).not.toBe("fable");
  });

  it("frontier does NOT use Fable for a trivial (XS) feature-dev", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("frontier");
    const result = resolveModel("feature-dev", "/test/workspace", XS);
    expect(result.model).not.toBe("fable");
  });

  it("frontier keeps plumbing (pr-create) on Haiku, never Fable", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("frontier");
    const result = resolveModel("pr-create", "/test/workspace", L);
    expect(result.model).toBe("haiku");
  });
});
