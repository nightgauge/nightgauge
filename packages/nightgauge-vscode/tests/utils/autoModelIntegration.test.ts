/**
 * Integration tests for AutoModelSelector pipeline integration (Issue #732)
 *
 * Tests the full model resolution chain:
 * 1. env var override (highest priority)
 * 2. config stage override (manual/hybrid)
 * 3. AutoModelSelector (automatic/hybrid)
 * 4. global default model
 * 5. hardcoded fallback (sonnet)
 *
 * Also tests: confidence threshold, minimum model floor, graceful fallback.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";

// Mock vscode before importing modules
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [
      {
        uri: {
          fsPath: "/test/workspace",
        },
      },
    ],
  },
}));

// Mock fs module
vi.mock("node:fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

// Mock configPathResolver
vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

// Mock AutoModelSelector from SDK
vi.mock("@nightgauge/sdk", async () => {
  const actual = await vi.importActual<typeof import("@nightgauge/sdk")>("@nightgauge/sdk");
  return {
    ...actual,
    AutoModelSelector: vi.fn(function () {
      return { selectModel: vi.fn() };
    }),
  };
});

import { AutoModelSelector, type IssueMetadata } from "@nightgauge/sdk";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";

// ============================================================================
// Helpers
// ============================================================================

function mockNoConfig(): void {
  vi.mocked(resolveConfigPathSync).mockReturnValue({
    path: "/test/workspace/.nightgauge/config.yaml",
    exists: false,
    isLegacy: false,
  });
}

function mockConfigFile(content: string): void {
  vi.mocked(resolveConfigPathSync).mockReturnValue({
    path: "/test/workspace/.nightgauge/config.yaml",
    exists: true,
    isLegacy: false,
  });
  vi.mocked(fs.readFileSync).mockReturnValue(content);
}

function getMockAutoModelSelector(): {
  selectModel: ReturnType<typeof vi.fn>;
} {
  const MockClass = vi.mocked(AutoModelSelector);
  // Get the last constructed instance
  const lastCall = MockClass.mock.results[MockClass.mock.results.length - 1];
  return lastCall?.value as ReturnType<typeof vi.fn>;
}

const defaultMetadata: IssueMetadata = {
  labels: ["size:M", "type:feature"],
  title: "Add user authentication",
};

// ============================================================================
// Tests
// ============================================================================

describe("Model Resolution Chain (Issue #732)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    process.env = { ...originalEnv };
    // Clean up model routing env vars
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD;
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_UI_CORE_DEFAULT_MODEL;
  });

  afterEach(() => {
    process.env = originalEnv;
    vi.restoreAllMocks();
  });

  describe("enforceMinimumModel", () => {
    // Test the helper via the resolution chain
    it("upgrades haiku to sonnet when minimum is sonnet", async () => {
      // Configure automatic mode with minimum_model
      mockConfigFile(`
model_routing:
  mode: automatic
  minimum_model:
    feature-dev: sonnet
`);

      // AutoModelSelector returns haiku (below minimum)
      const mockSelector = {
        selectModel: vi.fn().mockReturnValue({
          model: "haiku",
          confidence: 0.9,
          reasoning: "XS complexity",
          complexity: "XS",
          stage: "feature-dev",
        }),
      };
      vi.mocked(AutoModelSelector).mockImplementation(function () {
        return mockSelector as any;
      });

      // Re-import to get fresh module with mocks
      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);

      expect(result.model).toBe("sonnet");
      expect(result.source).toBe("auto");
    });
  });

  describe("automatic mode", () => {
    it("consults AutoModelSelector when getStageModel returns undefined", async () => {
      mockConfigFile(`
model_routing:
  mode: automatic
`);

      const mockSelector = {
        selectModel: vi.fn().mockReturnValue({
          model: "sonnet",
          confidence: 0.9,
          reasoning: "M complexity from size label",
          complexity: "M",
          stage: "feature-dev",
        }),
      };
      vi.mocked(AutoModelSelector).mockImplementation(function () {
        return mockSelector as any;
      });

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);

      expect(result.model).toBe("sonnet");
      expect(result.source).toBe("auto");
      expect(result.selectionResult).toBeDefined();
      expect(result.selectionResult?.complexity).toBe("M");
    });

    it("XS issue selects haiku for lightweight stages via stage-default", async () => {
      // Since Issue #972, lightweight stages (issue-pickup, pr-create, pr-merge)
      // are intercepted by LIGHTWEIGHT_STAGE_DEFAULTS in Step 1.5, before
      // AutoModelSelector is consulted. The model is still haiku but the source
      // is now 'stage-default' instead of 'auto'.
      mockConfigFile(`
model_routing:
  mode: automatic
`);

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("issue-pickup" as any, "/test/workspace", {
        labels: ["size:XS"],
        title: "Fix typo",
      });

      expect(result.model).toBe("haiku");
      expect(result.source).toBe("stage-default");
    });
  });

  describe("hybrid mode", () => {
    it("uses config override when present", async () => {
      mockConfigFile(`
model_routing:
  mode: hybrid
pipeline:
  stage_models:
    feature-dev: opus
`);

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);

      expect(result.model).toBe("opus");
      expect(result.source).toBe("config");
    });

    it("defers to AutoModelSelector for non-overridden stages", async () => {
      mockConfigFile(`
model_routing:
  mode: hybrid
pipeline:
  stage_models:
    feature-dev: opus
`);

      const mockSelector = {
        selectModel: vi.fn().mockReturnValue({
          model: "sonnet",
          confidence: 0.85,
          reasoning: "M complexity",
          complexity: "M",
          stage: "feature-validate",
        }),
      };
      vi.mocked(AutoModelSelector).mockImplementation(function () {
        return mockSelector as any;
      });

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-validate" as any, "/test/workspace", defaultMetadata);

      expect(result.model).toBe("sonnet");
      expect(result.source).toBe("auto");
    });
  });

  describe("env var override", () => {
    it("env var always wins regardless of mode", async () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV = "haiku";
      mockConfigFile(`
model_routing:
  mode: automatic
`);

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);

      expect(result.model).toBe("haiku");
      expect(result.source).toBe("env");
    });
  });

  describe("confidence threshold", () => {
    it("falls back to default when confidence below threshold", async () => {
      mockConfigFile(`
model_routing:
  mode: automatic
  confidence_threshold: 0.8
`);

      const mockSelector = {
        selectModel: vi.fn().mockReturnValue({
          model: "haiku",
          confidence: 0.5, // Below 0.8 threshold
          reasoning: "Low confidence",
          complexity: "M",
          stage: "feature-dev",
        }),
      };
      vi.mocked(AutoModelSelector).mockImplementation(function () {
        return mockSelector as any;
      });

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);

      // Should fall back to default model, not use the low-confidence haiku
      expect(result.source).toBe("default");
    });
  });

  describe("graceful fallback", () => {
    it("falls back to default when AutoModelSelector throws", async () => {
      mockConfigFile(`
model_routing:
  mode: automatic
`);

      const mockSelector = {
        selectModel: vi.fn().mockImplementation(() => {
          throw new Error("Selector crashed");
        }),
      };
      vi.mocked(AutoModelSelector).mockImplementation(function () {
        return mockSelector as any;
      });

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);

      // Should fall back gracefully
      expect(result.source).toBe("default");
      expect(result.model).toBe("sonnet");
    });

    it("falls back to default when no issue metadata provided", async () => {
      mockConfigFile(`
model_routing:
  mode: automatic
`);

      const { resolveModel } = await getResolveModel();

      // No issueMetadata — can't run selector
      const result = resolveModel("feature-dev" as any, "/test/workspace", undefined);

      expect(result.source).toBe("default");
    });
  });

  describe("manual mode", () => {
    it("uses DEFAULT_STAGE_MODELS without consulting selector", async () => {
      process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
      mockNoConfig();

      const { resolveModel } = await getResolveModel();

      const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);

      // Manual mode: getStageModel returns DEFAULT_STAGE_MODELS['feature-dev'] = 'sonnet'
      expect(result.model).toBe("sonnet");
      expect(result.source).toBe("config");
      expect(result.selectionResult).toBeUndefined();
    });
  });
});

// ============================================================================
// Lightweight Stage Defaults — Issue #972
// ============================================================================

describe("Lightweight Stage Defaults (Issue #972)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_PLANNING;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_VALIDATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_CREATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_MERGE;
    delete process.env.NIGHTGAUGE_PIPELINE_DEFAULT_MODEL;
    delete process.env.NIGHTGAUGE_UI_CORE_DEFAULT_MODEL;
  });

  afterEach(() => {
    process.env = originalEnv;
    vi.restoreAllMocks();
  });

  it("issue-pickup defaults to haiku via stage-default in automatic mode", async () => {
    mockNoConfig();
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("issue-pickup" as any, "/test/workspace");
    expect(result.model).toBe("haiku");
    expect(result.source).toBe("stage-default");
  });

  it("pr-create defaults to haiku via stage-default in automatic mode", async () => {
    mockNoConfig();
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("pr-create" as any, "/test/workspace");
    expect(result.model).toBe("haiku");
    expect(result.source).toBe("stage-default");
  });

  it("pr-create escalates to sonnet when diff exceeds threshold", async () => {
    mockNoConfig();
    const { resolveModel, setDiffLineCount } = await getResolveModel();

    setDiffLineCount(600); // Above default threshold of 500
    const result = resolveModel("pr-create" as any, "/test/workspace");
    expect(result.model).toBe("sonnet");
    expect(result.source).toBe("stage-default");
  });

  it("pr-create stays haiku when diff is below threshold", async () => {
    mockNoConfig();
    const { resolveModel, setDiffLineCount } = await getResolveModel();

    setDiffLineCount(200); // Below default threshold of 500
    const result = resolveModel("pr-create" as any, "/test/workspace");
    expect(result.model).toBe("haiku");
    expect(result.source).toBe("stage-default");
  });

  it("pr-create stays haiku when threshold is disabled (set to 0)", async () => {
    process.env.NIGHTGAUGE_PIPELINE_LARGE_DIFF_THRESHOLD = "0";
    mockNoConfig();
    const { resolveModel, setDiffLineCount } = await getResolveModel();

    setDiffLineCount(1000); // Large diff, but threshold disabled
    const result = resolveModel("pr-create" as any, "/test/workspace");
    expect(result.model).toBe("haiku");
    expect(result.source).toBe("stage-default");
  });

  it("pr-merge is NOT affected by diff-size escalation", async () => {
    mockNoConfig();
    const { resolveModel, setDiffLineCount } = await getResolveModel();

    setDiffLineCount(1000);
    const result = resolveModel("pr-merge" as any, "/test/workspace");
    expect(result.model).toBe("sonnet");
    expect(result.source).toBe("stage-default");
  });

  it("pr-merge defaults to sonnet — its LLM path only runs on deterministic punts (#197)", async () => {
    mockNoConfig();
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("pr-merge" as any, "/test/workspace");
    expect(result.model).toBe("sonnet");
    expect(result.source).toBe("stage-default");
  });

  it("env var override takes priority over stage-default", async () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP = "opus";
    mockNoConfig();
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("issue-pickup" as any, "/test/workspace");
    expect(result.model).toBe("opus");
    expect(result.source).toBe("env");
  });

  it("config stage_models override takes priority over stage-default in manual mode", async () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockConfigFile(`
model_routing:
  mode: manual
pipeline:
  stage_models:
    issue-pickup: sonnet
`);
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("issue-pickup" as any, "/test/workspace");
    expect(result.model).toBe("sonnet");
    expect(result.source).toBe("config");
  });

  it("feature-dev does NOT get stage-default", async () => {
    mockNoConfig();
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("feature-dev" as any, "/test/workspace", defaultMetadata);
    expect(result.source).not.toBe("stage-default");
  });

  it("feature-planning does NOT get stage-default", async () => {
    mockNoConfig();
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("feature-planning" as any, "/test/workspace", defaultMetadata);
    expect(result.source).not.toBe("stage-default");
  });

  it("feature-validate does NOT get stage-default", async () => {
    mockNoConfig();
    const { resolveModel } = await getResolveModel();

    const result = resolveModel("feature-validate" as any, "/test/workspace", defaultMetadata);
    expect(result.source).not.toBe("stage-default");
  });

  it("stage-default works in automatic and hybrid modes", async () => {
    for (const mode of ["automatic", "hybrid"] as const) {
      process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = mode;
      mockNoConfig();
      const { resolveModel } = await getResolveModel();

      const result = resolveModel("pr-create" as any, "/test/workspace");
      expect(result.model).toBe("haiku");
      expect(result.source).toBe("stage-default");
      expect(result.mode).toBe(mode);
    }
  });
});

// ============================================================================
// Dynamic import helper for fresh module state
// ============================================================================

async function getResolveModel() {
  // Dynamic import to get fresh module with current mocks
  // resolveModel is not exported, so we test through the module's behavior
  // by importing the functions it uses
  // Test resolveModel logic through the incrediConfig functions
  // since resolveModel is a module-private function in skillRunner
  const config = await import("../../src/utils/incrediConfig");
  const { AutoModelSelector: MockSelector } = await import("@nightgauge/sdk");

  // Simulated diff line count for testing diff-size escalation.
  // Tests can set this before calling resolveModel to simulate large diffs.
  let _diffLineCount = 0;

  // Lightweight stage defaults (Issue #972)
  const LIGHTWEIGHT_STAGE_DEFAULTS: Record<string, string> = {
    "issue-pickup": "haiku",
    "pr-create": "haiku",
    // #197: pr-merge's LLM path only runs on deterministic punts — sonnet.
    "pr-merge": "sonnet",
  };

  // Build a manual resolveModel for testing
  function resolveModel(stage: any, workspaceRoot: string, issueMetadata?: IssueMetadata) {
    const routingMode = config.getModelRoutingMode(workspaceRoot);

    const stageModel = config.getStageModel(stage, workspaceRoot);
    if (stageModel !== undefined) {
      const envKey = `NIGHTGAUGE_PIPELINE_STAGE_MODEL_${(stage as string).toUpperCase().replace(/-/g, "_")}`;
      const source = process.env[envKey] ? "env" : "config";
      return {
        model: stageModel,
        source,
        selectionResult: undefined,
        mode: routingMode,
      };
    }

    // Step 1.5: Per-stage defaults for lightweight stages (Issue #972)
    // For pr-create, escalate to sonnet when diff exceeds threshold.
    const lightweightDefault = LIGHTWEIGHT_STAGE_DEFAULTS[stage as string];
    if (lightweightDefault !== undefined) {
      if (stage === "pr-create") {
        const threshold = config.getLargeDiffThreshold(workspaceRoot);
        if (threshold > 0 && _diffLineCount > threshold) {
          return {
            model: "sonnet",
            source: "stage-default" as const,
            selectionResult: undefined,
            mode: routingMode,
          };
        }
      }
      return {
        model: lightweightDefault,
        source: "stage-default" as const,
        selectionResult: undefined,
        mode: routingMode,
      };
    }

    if (issueMetadata) {
      try {
        const selector = new MockSelector();
        const result = (selector as any).selectModel(stage, issueMetadata);
        const threshold = config.getConfidenceThreshold(workspaceRoot);
        const minModel = config.getMinimumModel(stage, workspaceRoot);

        if (result.confidence >= threshold) {
          const tiers: Record<string, number> = {
            haiku: 0,
            sonnet: 1,
            opus: 2,
          };
          const model = minModel && tiers[result.model] < tiers[minModel] ? minModel : result.model;
          return { model, source: "auto" as const, selectionResult: result };
        }
      } catch {
        // Fall through
      }
    }

    const defaultModel = config.getDefaultModel(workspaceRoot);
    if (defaultModel) {
      return {
        model: defaultModel,
        source: "default" as const,
        selectionResult: undefined,
      };
    }

    return {
      model: "sonnet" as const,
      source: "default" as const,
      selectionResult: undefined,
    };
  }

  function setDiffLineCount(count: number) {
    _diffLineCount = count;
  }

  return { resolveModel, setDiffLineCount };
}
