/**
 * skillRunner.supercharge.test.ts
 *
 * Issue #2433 introduced supercharge as a binary toggle. Issue #3009
 * replaced it with the explicit `performance_mode` selector. The legacy
 * `isSuperchargeModeActive` / `getSuperchargeModel` functions still exist
 * as thin deprecation wrappers (returning true iff mode === "maximum").
 *
 * These tests assert that resolveModel still produces a heavy-tier
 * decision when a caller exercises the legacy code path — i.e. when the
 * resolved performance mode is `maximum`. The new source value is
 * `performance-mode`, not `supercharge`, but the model + effort match the
 * historical Supercharge envelope.
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

describe("resolveModel — Maximum mode (replaces legacy Supercharge from #2433)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getStageEffort).mockReturnValue("medium");
  });

  it("forces effort=high when Maximum mode is active, even if stage default is medium", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    const result = resolveModel("feature-dev", "/test/workspace");

    expect(result.source).toBe("performance-mode");
    expect(result.model).toBe("opus");
    expect(result.effort).toBe("high");
  });

  it("does not force effort=high when mode is Elevated", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    vi.mocked(getStageEffort).mockReturnValue("low");

    const result = resolveModel("feature-dev", "/test/workspace");

    expect(result.source).not.toBe("performance-mode");
    expect(result.effort).toBe("low");
  });
});
