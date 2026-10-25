/**
 * skillRunner.costAware.test.ts (Issue #21)
 *
 * Verifies resolveModel wires cost-per-success context into the selector and
 * that the resulting pick stays within the mode envelope:
 *   - with history where Sonnet has a comparable-or-better cost-per-success,
 *     the selector picks Sonnet for an Elevated L feature-dev, but the Opus
 *     stage default (#2512) is a floor, so the dispatched tier stays Opus.
 *   - the envelope still clamps: the same context under Efficiency never
 *     exceeds Sonnet, and cost-aware routing can't push below the floor.
 *   - when cost-aware routing is disabled, no downgrade occurs.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }] },
}));

vi.mock("../../src/utils/nightgaugeConfig", async () => {
  const actual = await vi.importActual<typeof import("../../src/utils/nightgaugeConfig")>(
    "../../src/utils/nightgaugeConfig"
  );
  return {
    ...actual,
    getPerformanceMode: vi.fn(() => "elevated"),
    getStageEffort: vi.fn(() => "high"),
    getStageModel: vi.fn(() => undefined),
    getDefaultModel: vi.fn(() => "sonnet"),
    getModelRoutingMode: vi.fn(() => "automatic"),
    getLargeDiffThreshold: vi.fn(() => 500),
    getExperimentConfig: vi.fn(() => undefined),
    getConfidenceThreshold: vi.fn(() => 0.5),
    getMinimumModel: vi.fn(() => undefined),
  };
});

// Control cost-aware routing directly.
const { enabledMock, contextMock } = vi.hoisted(() => ({
  enabledMock: vi.fn(() => true),
  contextMock: vi.fn(),
}));
vi.mock("../../src/utils/costAwareRouting", () => ({
  isCostAwareRoutingEnabled: enabledMock,
  getCostPerSuccessContext: contextMock,
}));

import { resolveModel } from "../../src/utils/skillRunner";
import { getPerformanceMode } from "../../src/utils/nightgaugeConfig";

const L = { labels: ["size:L"], title: "Large change" };

// History where Sonnet's cost-per-success is far better than Opus for
// feature-dev (both above min samples / success rate), so #2458 prefers Sonnet.
function favorableContext() {
  return {
    history: {
      "opus:feature-dev": { totalCostUsd: 60, successCount: 6, totalCount: 8 },
      "sonnet:feature-dev": { totalCostUsd: 6, successCount: 6, totalCount: 8 },
    },
  };
}

describe("resolveModel — cost-aware routing within the envelope (Issue #21)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    enabledMock.mockReturnValue(true);
    contextMock.mockReturnValue(favorableContext());
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
  });

  it("the selector downgrades L feature-dev to Sonnet on favorable CPS, but the Opus floor holds (#2512)", () => {
    const result = resolveModel("feature-dev", "/test/workspace", L);
    // Cost-aware routing still reaches the selector and picks Sonnet...
    expect(result.selectionResult?.model).toBe("sonnet");
    // ...but the stage default is raise-only: the dispatched tier stays Opus.
    expect(result.model).toBe("opus");
    expect(result.source).toBe("auto");
  });

  it("does NOT downgrade when cost-aware routing is disabled", () => {
    enabledMock.mockReturnValue(false);
    const result = resolveModel("feature-dev", "/test/workspace", L);
    expect(result.model).toBe("opus");
  });

  it("stays within the Efficiency ceiling regardless of CPS history", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    const result = resolveModel("feature-dev", "/test/workspace", L);
    // Efficiency caps at sonnet anyway; cost-aware can't push it above the band.
    expect(result.model).toBe("sonnet");
  });
});
