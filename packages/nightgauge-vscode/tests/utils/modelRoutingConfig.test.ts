/**
 * Unit tests for model routing configuration (Issue #731)
 *
 * Moved from src/utils/__tests__/ (which vitest.config.ts does not include,
 * so the suite never ran in CI) into tests/ as part of #56.
 *
 * Tests the four new config reader functions and the updated
 * routing-mode-aware getStageModel() behavior.
 *
 * @see Issue #731 - Model routing configuration modes
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";

// Mock vscode before importing the module
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

import {
  getModelRoutingMode,
  getComplexityThresholds,
  getMinimumModel,
  getConfidenceThreshold,
  getStageModel,
  getStageEffort,
  DEFAULT_COMPLEXITY_THRESHOLDS,
  DEFAULT_CONFIDENCE_THRESHOLD,
  DEFAULT_STAGE_EFFORTS,
} from "../../src/utils/incrediConfig";
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

// ============================================================================
// getModelRoutingMode
// ============================================================================

describe("getModelRoutingMode (Issue #731)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("defaults to automatic when no config exists", () => {
    mockNoConfig();
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
  });

  it("reads mode from environment variable", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "automatic";
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
    expect(resolveConfigPathSync).not.toHaveBeenCalled();
  });

  it("reads hybrid mode from environment variable", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "hybrid";
    expect(getModelRoutingMode("/test/workspace")).toBe("hybrid");
  });

  it("ignores invalid environment variable values", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "invalid";
    mockNoConfig();
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
  });

  it("reads mode from config file", () => {
    mockConfigFile(`
model_routing:
  mode: automatic
  confidence_threshold: 0.8
`);
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
  });

  it("reads hybrid mode from config file", () => {
    mockConfigFile(`
model_routing:
  mode: hybrid
`);
    expect(getModelRoutingMode("/test/workspace")).toBe("hybrid");
  });

  it("handles quoted values in config", () => {
    mockConfigFile(`
model_routing:
  mode: 'automatic'
`);
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
  });

  it("returns automatic when model_routing section has no mode", () => {
    mockConfigFile(`
model_routing:
  confidence_threshold: 0.8
`);
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
  });

  it("returns automatic when config has no model_routing section", () => {
    mockConfigFile(`
pipeline:
  auto_fix: true
`);
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
  });

  it("prioritizes env var over config file", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockConfigFile(`
model_routing:
  mode: automatic
`);
    expect(getModelRoutingMode("/test/workspace")).toBe("manual");
    expect(fs.readFileSync).not.toHaveBeenCalled();
  });

  it("handles file read errors gracefully", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockImplementation(() => {
      throw new Error("File read error");
    });

    // mergedConfigReader treats an unreadable tier file as absent (empty
    // content), so the mode falls back to the default without an error log —
    // the pre-mergedConfigReader implementation console.error'd here.
    expect(getModelRoutingMode("/test/workspace")).toBe("automatic");
  });

  it("explicit manual in config preserves manual mode", () => {
    mockConfigFile(`
model_routing:
  mode: manual
`);
    expect(getModelRoutingMode("/test/workspace")).toBe("manual");
  });

  it("env var override to manual overrides automatic default", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockNoConfig();
    expect(getModelRoutingMode("/test/workspace")).toBe("manual");
  });
});

// ============================================================================
// getComplexityThresholds
// ============================================================================

describe("getComplexityThresholds (Issue #731)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_HAIKU_MAX;
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_SONNET_MAX;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns defaults when no config exists", () => {
    mockNoConfig();
    expect(getComplexityThresholds("/test/workspace")).toEqual(DEFAULT_COMPLEXITY_THRESHOLDS);
  });

  it("returns defaults of haiku_max=3, sonnet_max=6", () => {
    mockNoConfig();
    const thresholds = getComplexityThresholds("/test/workspace");
    expect(thresholds.haikuMax).toBe(3);
    expect(thresholds.sonnetMax).toBe(6);
  });

  it("reads thresholds from environment variables", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_HAIKU_MAX = "2";
    process.env.NIGHTGAUGE_MODEL_ROUTING_SONNET_MAX = "5";

    const thresholds = getComplexityThresholds("/test/workspace");
    expect(thresholds.haikuMax).toBe(2);
    expect(thresholds.sonnetMax).toBe(5);
  });

  it("reads thresholds from config file", () => {
    mockConfigFile(`
model_routing:
  complexity_thresholds:
    haiku_max: 4
    sonnet_max: 7
`);
    const thresholds = getComplexityThresholds("/test/workspace");
    expect(thresholds.haikuMax).toBe(4);
    expect(thresholds.sonnetMax).toBe(7);
  });

  it("env var overrides config file for haiku_max", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_HAIKU_MAX = "1";
    mockConfigFile(`
model_routing:
  complexity_thresholds:
    haiku_max: 4
    sonnet_max: 7
`);
    const thresholds = getComplexityThresholds("/test/workspace");
    expect(thresholds.haikuMax).toBe(1);
    expect(thresholds.sonnetMax).toBe(7);
  });

  it("ignores invalid env var values", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_HAIKU_MAX = "invalid";
    mockNoConfig();
    const thresholds = getComplexityThresholds("/test/workspace");
    expect(thresholds.haikuMax).toBe(3); // default
  });

  it("ignores out-of-range env var values", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_HAIKU_MAX = "15";
    mockNoConfig();
    const thresholds = getComplexityThresholds("/test/workspace");
    expect(thresholds.haikuMax).toBe(3); // default
  });
});

// ============================================================================
// getMinimumModel
// ============================================================================

describe("getMinimumModel (Issue #731)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_ISSUE_PICKUP;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns undefined when no config exists (no floor)", () => {
    mockNoConfig();
    expect(getMinimumModel("feature-dev", "/test/workspace")).toBeUndefined();
  });

  it("reads minimum model from environment variable", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV = "sonnet";
    expect(getMinimumModel("feature-dev", "/test/workspace")).toBe("sonnet");
    expect(resolveConfigPathSync).not.toHaveBeenCalled();
  });

  it("reads minimum model from config file", () => {
    mockConfigFile(`
model_routing:
  minimum_model:
    feature-dev: sonnet
    issue-pickup: haiku
`);
    expect(getMinimumModel("feature-dev", "/test/workspace")).toBe("sonnet");
  });

  it("accepts fable from the config file (#56 — regex once dropped it silently)", () => {
    mockConfigFile(`
model_routing:
  minimum_model:
    feature-dev: fable
`);
    expect(getMinimumModel("feature-dev", "/test/workspace")).toBe("fable");
  });

  it("returns undefined for stages not in config", () => {
    mockConfigFile(`
model_routing:
  minimum_model:
    feature-dev: sonnet
`);
    expect(getMinimumModel("pr-create", "/test/workspace")).toBeUndefined();
  });

  it("ignores invalid env var model values", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV = "invalid";
    mockNoConfig();
    expect(getMinimumModel("feature-dev", "/test/workspace")).toBeUndefined();
  });
});

// ============================================================================
// getConfidenceThreshold
// ============================================================================

describe("getConfidenceThreshold (Issue #731)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns default 0.7 when no config exists", () => {
    mockNoConfig();
    expect(getConfidenceThreshold("/test/workspace")).toBe(DEFAULT_CONFIDENCE_THRESHOLD);
  });

  it("reads threshold from environment variable", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD = "0.9";
    expect(getConfidenceThreshold("/test/workspace")).toBe(0.9);
    expect(resolveConfigPathSync).not.toHaveBeenCalled();
  });

  it("reads threshold from config file", () => {
    mockConfigFile(`
model_routing:
  confidence_threshold: 0.85
`);
    expect(getConfidenceThreshold("/test/workspace")).toBe(0.85);
  });

  it("prioritizes env var over config file", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD = "0.5";
    mockConfigFile(`
model_routing:
  confidence_threshold: 0.85
`);
    expect(getConfidenceThreshold("/test/workspace")).toBe(0.5);
  });

  it("ignores invalid env var values", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD = "invalid";
    mockNoConfig();
    expect(getConfidenceThreshold("/test/workspace")).toBe(0.7);
  });

  it("ignores out-of-range env var values (>1)", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD = "1.5";
    mockNoConfig();
    expect(getConfidenceThreshold("/test/workspace")).toBe(0.7);
  });

  it("accepts boundary values (0.0 and 1.0)", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD = "0.0";
    expect(getConfidenceThreshold("/test/workspace")).toBe(0.0);

    process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD = "1.0";
    expect(getConfidenceThreshold("/test/workspace")).toBe(1.0);
  });
});

// ============================================================================
// getStageModel — Mode Switching (Issue #731)
// ============================================================================

describe("getStageModel — mode-aware behavior (Issue #731)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    // Clean up all relevant env vars
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_PLANNING;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_VALIDATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_CREATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_MERGE;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  describe("manual mode (explicit config)", () => {
    it("returns default stage models from DEFAULT_STAGE_MODELS in manual mode", () => {
      process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
      mockNoConfig();
      expect(getStageModel("issue-pickup", "/test/workspace")).toBe("haiku");
      expect(getStageModel("feature-planning", "/test/workspace")).toBe("sonnet");
      expect(getStageModel("feature-dev", "/test/workspace")).toBe("sonnet");
      expect(getStageModel("feature-validate", "/test/workspace")).toBe("sonnet");
      expect(getStageModel("pr-create", "/test/workspace")).toBe("haiku");
      // #197: pr-merge defaults to sonnet — its LLM path only runs on
      // deterministic punts (the judgment-heavy cases).
      expect(getStageModel("pr-merge", "/test/workspace")).toBe("sonnet");
    });

    it("reads per-stage override from config file in manual mode", () => {
      process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
      mockConfigFile(`
pipeline:
  stage_models:
    issue-pickup: sonnet
`);
      expect(getStageModel("issue-pickup", "/test/workspace")).toBe("sonnet");
    });
  });

  describe("automatic mode (default)", () => {
    it("returns undefined for all stages (defer to AutoModelSelector)", () => {
      // automatic is now the default — no env var needed
      mockNoConfig();

      expect(getStageModel("issue-pickup", "/test/workspace")).toBeUndefined();
      expect(getStageModel("feature-planning", "/test/workspace")).toBeUndefined();
      expect(getStageModel("feature-dev", "/test/workspace")).toBeUndefined();
      expect(getStageModel("feature-validate", "/test/workspace")).toBeUndefined();
      expect(getStageModel("pr-create", "/test/workspace")).toBeUndefined();
      expect(getStageModel("pr-merge", "/test/workspace")).toBeUndefined();
    });

    it("env var override still wins in automatic mode", () => {
      process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "automatic";
      process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV = "opus";
      mockNoConfig();

      expect(getStageModel("feature-dev", "/test/workspace")).toBe("opus");
    });
  });

  describe("hybrid mode", () => {
    it("returns config override when set, undefined otherwise", () => {
      process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "hybrid";
      mockConfigFile(`
model_routing:
  mode: hybrid
pipeline:
  stage_models:
    feature-dev: opus
`);

      // feature-dev has explicit override → returns opus
      expect(getStageModel("feature-dev", "/test/workspace")).toBe("opus");
      // issue-pickup has no override → returns undefined (defer)
      expect(getStageModel("issue-pickup", "/test/workspace")).toBeUndefined();
    });

    it("env var override wins in hybrid mode", () => {
      process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "hybrid";
      process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP = "sonnet";
      mockNoConfig();

      expect(getStageModel("issue-pickup", "/test/workspace")).toBe("sonnet");
    });
  });

  describe("env var override in all modes", () => {
    const modes: Array<"manual" | "automatic" | "hybrid"> = ["manual", "automatic", "hybrid"];

    for (const mode of modes) {
      it(`env var wins over everything in ${mode} mode`, () => {
        process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = mode;
        process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_CREATE = "opus";
        mockConfigFile(`
model_routing:
  mode: ${mode}
pipeline:
  stage_models:
    pr-create: haiku
`);

        expect(getStageModel("pr-create", "/test/workspace")).toBe("opus");
      });
    }
  });
});

// ============================================================================
// Backward Compatibility
// ============================================================================

describe("Backward compatibility (Issue #731)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("missing model_routing section defaults to automatic mode", () => {
    mockConfigFile(`
pipeline:
  auto_fix: true
  stage_models:
    issue-pickup: haiku
`);
    // Automatic mode → returns undefined (defer to AutoModelSelector)
    // Even though stage_models is set, automatic mode ignores it
    expect(getStageModel("issue-pickup", "/test/workspace")).toBeUndefined();
    expect(getStageModel("feature-dev", "/test/workspace")).toBeUndefined();
  });

  it("existing pipeline.stage_models config still works in manual mode", () => {
    mockConfigFile(`
model_routing:
  mode: manual
pipeline:
  stage_models:
    issue-pickup: sonnet
    feature-dev: opus
`);
    expect(getStageModel("issue-pickup", "/test/workspace")).toBe("sonnet");
    expect(getStageModel("feature-dev", "/test/workspace")).toBe("opus");
  });

  it("existing env var overrides still work in all modes", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP = "opus";
    mockNoConfig();

    // Default mode (automatic) — env var still works
    expect(getStageModel("issue-pickup", "/test/workspace")).toBe("opus");

    // Switch to automatic — env var still wins
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "automatic";
    expect(getStageModel("issue-pickup", "/test/workspace")).toBe("opus");

    // Switch to hybrid — env var still wins
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "hybrid";
    expect(getStageModel("issue-pickup", "/test/workspace")).toBe("opus");
  });
});

// ============================================================================
// DEFAULT_STAGE_MODELS — Explicit Validation (Issue #944)
// ============================================================================

describe("DEFAULT_STAGE_MODELS (Issue #944)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_PLANNING;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_VALIDATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_CREATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_MERGE;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("maps Sonnet 4.6 era defaults correctly in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockNoConfig();
    expect(getStageModel("issue-pickup", "/test/workspace")).toBe("haiku");
    expect(getStageModel("feature-planning", "/test/workspace")).toBe("sonnet");
    expect(getStageModel("feature-dev", "/test/workspace")).toBe("sonnet");
    expect(getStageModel("feature-validate", "/test/workspace")).toBe("sonnet");
    expect(getStageModel("pr-create", "/test/workspace")).toBe("haiku");
    // #197: pr-merge defaults to sonnet — its LLM path only runs on
    // deterministic punts (the judgment-heavy cases).
    expect(getStageModel("pr-merge", "/test/workspace")).toBe("sonnet");
  });
});

// ============================================================================
// DEFAULT_STAGE_EFFORTS (Issue #944)
// ============================================================================

describe("DEFAULT_STAGE_EFFORTS (Issue #944)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_PLANNING;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_VALIDATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_ISSUE_PICKUP;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_PR_CREATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_PR_MERGE;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("exports DEFAULT_STAGE_EFFORTS constant", () => {
    expect(DEFAULT_STAGE_EFFORTS).toEqual({
      "feature-planning": "medium",
      "feature-dev": "medium",
      "feature-validate": "low",
    });
  });

  it("returns medium for feature-planning in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockNoConfig();
    expect(getStageEffort("feature-planning", "/test/workspace")).toBe("medium");
  });

  it("returns medium for feature-dev in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockNoConfig();
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("medium");
  });

  it("returns low for feature-validate in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockNoConfig();
    expect(getStageEffort("feature-validate", "/test/workspace")).toBe("low");
  });

  it("returns undefined for lightweight stages in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    mockNoConfig();
    expect(getStageEffort("issue-pickup", "/test/workspace")).toBeUndefined();
    expect(getStageEffort("pr-create", "/test/workspace")).toBeUndefined();
    expect(getStageEffort("pr-merge", "/test/workspace")).toBeUndefined();
  });

  it("config stage_efforts overrides defaults", () => {
    mockConfigFile(`
model_routing:
  stage_efforts:
    feature-dev: high
`);
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("high");
  });

  it("env var overrides defaults", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "high";
    mockNoConfig();
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("high");
  });
});
