/**
 * skillRunner.costAware.test.ts (Issue #21)
 *
 * Verifies resolveModel wires cost-per-success context into the selector and
 * that the resulting pick stays within the mode envelope:
 *   - with history where Sonnet has a comparable-or-better cost-per-success,
 *     an Elevated L feature-dev (normally Opus) is routed to Sonnet.
 *   - the envelope still clamps: the same context under Efficiency never
 *     exceeds Sonnet, and cost-aware routing can't push below the floor.
 *   - when cost-aware routing is disabled, no downgrade occurs.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }] },
}));

vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<typeof import("../../src/utils/incrediConfig")>(
    "../../src/utils/incrediConfig"
  );
  return {
    ...actual,
    getPerformanceMode: vi.fn(() => "elevated"),
    getStageEffort: vi.fn(() => "high"),
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
import { getPerformanceMode } from "../../src/utils/incrediConfig";

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

  it("downgrades an Elevated L feature-dev from Opus to Sonnet on favorable CPS", () => {
    const result = resolveModel("feature-dev", "/test/workspace", L);
    expect(result.model).toBe("sonnet");
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
