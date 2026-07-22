/**
 * incrediConfig.test.ts
 *
 * Unit tests for getStageBudget() and getBudgetEnforcementConfig() in incrediConfig.ts.
 *
 * @see Issue #638 - Pipeline token efficiency
 * @see Issue #835 - Enforce hard budget limits
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";

// Mock vscode module
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
}));

// Mock configPathResolver
vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

// Mock fs
vi.mock("node:fs", () => ({
  readFileSync: vi.fn(),
  existsSync: vi.fn(),
}));

import {
  getStageBudget,
  getBudgetEnforcementConfig,
  getStageEffort,
  getModelDefaultEffort,
  modelSupportsEffort,
  EFFORT_SUPPORTING_MODELS,
  conformEffortForFable,
  getMcpToolsConfig,
} from "../../src/utils/incrediConfig";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";

describe("getStageBudget", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    // Default: no config file
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  describe("default budgets (size M fallback)", () => {
    it("returns default M budget for issue-pickup", () => {
      const budget = getStageBudget("issue-pickup");
      expect(budget).toEqual({ maxCostUsd: 1.5 });
    });

    it("returns default M budget for feature-dev", () => {
      const budget = getStageBudget("feature-dev");
      expect(budget).toEqual({ maxCostUsd: 24.0 });
    });

    it("returns default M budget for feature-validate", () => {
      const budget = getStageBudget("feature-validate");
      expect(budget).toEqual({ maxCostUsd: 20.0 });
    });

    it("returns default M budget for pr-create", () => {
      const budget = getStageBudget("pr-create");
      // Bumped in #265: was $0.30, now $3.00 — the LLM fallback path was
      // repeatedly observed at $1.97-$2.10 REAL, undersizing the standard
      // preset's warn threshold. See DEFAULT_SIZE_AWARE_BUDGETS in
      // budgetEnforcer.ts.
      expect(budget).toEqual({ maxCostUsd: 3.0 });
    });

    it("returns default M budget for pr-merge", () => {
      const budget = getStageBudget("pr-merge");
      // Bumped in #3650: was $0.8, now $1.5 to give CI-watching headroom
      // (effectiveLimit after generous+grace climbs from $2.40 → $4.50).
      // Bumped again in #265: was $1.5, now $2.0 — a real $4.51 CI-watching
      // spend tripped the $4.50 generous+grace ceiling by one cent.
      // See DEFAULT_SIZE_AWARE_BUDGETS in budgetEnforcer.ts.
      expect(budget).toEqual({ maxCostUsd: 2.0 });
    });

    it("returns undefined for pipeline-start (no budget)", () => {
      const budget = getStageBudget("pipeline-start");
      expect(budget).toBeUndefined();
    });

    it("returns undefined for pipeline-finish (no budget)", () => {
      const budget = getStageBudget("pipeline-finish");
      expect(budget).toBeUndefined();
    });
  });

  describe("size-aware default budgets", () => {
    it("returns XS budget when sizeLabel is XS", () => {
      const budget = getStageBudget("feature-dev", undefined, "XS");
      expect(budget).toEqual({ maxCostUsd: 4.0 });
    });

    it("returns XL budget when sizeLabel is XL", () => {
      const budget = getStageBudget("feature-dev", undefined, "XL");
      expect(budget).toEqual({ maxCostUsd: 80.0 });
    });

    it("returns L budget for feature-validate", () => {
      const budget = getStageBudget("feature-validate", undefined, "L");
      expect(budget).toEqual({ maxCostUsd: 40.0 });
    });
  });

  describe("environment variable override", () => {
    it("uses env var when set for feature-dev", () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_BUDGET_FEATURE_DEV = "8.50";
      const budget = getStageBudget("feature-dev");
      expect(budget).toEqual({ maxCostUsd: 8.5 });
    });

    it("uses env var when set for issue-pickup", () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_BUDGET_ISSUE_PICKUP = "3.00";
      const budget = getStageBudget("issue-pickup");
      expect(budget).toEqual({ maxCostUsd: 3.0 });
    });

    it("uses env var when set for pr-merge", () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_BUDGET_PR_MERGE = "4.00";
      const budget = getStageBudget("pr-merge");
      expect(budget).toEqual({ maxCostUsd: 4.0 });
    });

    it("ignores invalid env var (non-numeric)", () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_BUDGET_FEATURE_DEV = "abc";
      const budget = getStageBudget("feature-dev");
      // Falls through to default M
      expect(budget).toEqual({ maxCostUsd: 24.0 });
    });

    it("ignores zero env var", () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_BUDGET_FEATURE_DEV = "0";
      const budget = getStageBudget("feature-dev");
      expect(budget).toEqual({ maxCostUsd: 24.0 });
    });

    it("ignores negative env var", () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_BUDGET_FEATURE_DEV = "-1";
      const budget = getStageBudget("feature-dev");
      expect(budget).toEqual({ maxCostUsd: 24.0 });
    });
  });

  describe("config file override", () => {
    beforeEach(() => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        isLegacy: false,
        exists: true,
      });
    });

    it("reads budget from config file (legacy max_cost_usd)", () => {
      vi.mocked(fs.readFileSync).mockReturnValue(
        `pipeline:
  stage_budgets:
    feature-dev:
      max_cost_usd: 7.50
`
      );
      const budget = getStageBudget("feature-dev", "/test/workspace");
      expect(budget).toEqual({ maxCostUsd: 7.5 });
    });

    it("reads flat budget from config file", () => {
      vi.mocked(fs.readFileSync).mockReturnValue(
        `pipeline:
  stage_budgets:
    feature-dev: 20.0
`
      );
      const budget = getStageBudget("feature-dev", "/test/workspace");
      expect(budget).toEqual({ maxCostUsd: 20.0 });
    });

    it("reads budget for pr-merge from config file", () => {
      vi.mocked(fs.readFileSync).mockReturnValue(
        `pipeline:
  stage_budgets:
    pr-merge:
      max_cost_usd: 3.00
`
      );
      const budget = getStageBudget("pr-merge", "/test/workspace");
      expect(budget).toEqual({ maxCostUsd: 3.0 });
    });

    it("falls back to default M when stage not in config", () => {
      vi.mocked(fs.readFileSync).mockReturnValue(
        `pipeline:
  stage_budgets:
    feature-dev:
      max_cost_usd: 7.50
`
      );
      const budget = getStageBudget("issue-pickup", "/test/workspace");
      expect(budget).toEqual({ maxCostUsd: 1.5 });
    });

    it("env var takes priority over config file", () => {
      process.env.NIGHTGAUGE_PIPELINE_STAGE_BUDGET_FEATURE_DEV = "10.00";
      vi.mocked(fs.readFileSync).mockReturnValue(
        `pipeline:
  stage_budgets:
    feature-dev:
      max_cost_usd: 7.50
`
      );
      const budget = getStageBudget("feature-dev", "/test/workspace");
      expect(budget).toEqual({ maxCostUsd: 10.0 });
    });

    it("handles config without stage_budgets section", () => {
      vi.mocked(fs.readFileSync).mockReturnValue(
        `pipeline:
  ci_timeout: 300
  auto_fix: true
`
      );
      const budget = getStageBudget("feature-dev", "/test/workspace");
      expect(budget).toEqual({ maxCostUsd: 24.0 });
    });

    it("handles config file read error gracefully", () => {
      vi.mocked(fs.readFileSync).mockImplementation(() => {
        throw new Error("ENOENT");
      });
      const budget = getStageBudget("feature-dev", "/test/workspace");
      // Falls back to default M
      expect(budget).toEqual({ maxCostUsd: 24.0 });
    });
  });
});

describe("getBudgetEnforcementConfig", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns defaults when no config or env vars", () => {
    const config = getBudgetEnforcementConfig();
    expect(config.mode).toBe("hard");
    expect(config.gracePercent).toBe(50);
  });

  it("uses env var for budget mode", () => {
    process.env.NIGHTGAUGE_PIPELINE_BUDGET_MODE = "soft";
    const config = getBudgetEnforcementConfig();
    expect(config.mode).toBe("soft");
  });

  it("uses env var for grace percent", () => {
    process.env.NIGHTGAUGE_PIPELINE_BUDGET_GRACE_PERCENT = "75";
    const config = getBudgetEnforcementConfig();
    expect(config.gracePercent).toBe(75);
  });

  it("ignores invalid budget mode env var", () => {
    process.env.NIGHTGAUGE_PIPELINE_BUDGET_MODE = "invalid";
    const config = getBudgetEnforcementConfig();
    expect(config.mode).toBe("hard");
  });

  it("reads budget mode from config file", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `pipeline:
  budget_mode: soft
  budget_grace_percent: 25
`
    );
    const config = getBudgetEnforcementConfig("/test/workspace");
    expect(config.mode).toBe("soft");
    expect(config.gracePercent).toBe(25);
  });

  it("env var takes priority over config file", () => {
    process.env.NIGHTGAUGE_PIPELINE_BUDGET_MODE = "threshold";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `pipeline:
  budget_mode: soft
`
    );
    const config = getBudgetEnforcementConfig("/test/workspace");
    expect(config.mode).toBe("threshold");
  });
});

describe("getStageEffort", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("uses explicit stage effort environment override", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "high";
    const effort = getStageEffort("feature-dev", "/test/workspace", {
      labels: ["size:S"],
      title: "Test",
    });
    expect(effort).toBe("high");
  });

  it("reads explicit stage effort from config", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `model_routing:
  mode: hybrid
  stage_efforts:
    feature-dev: medium
`
    );

    const effort = getStageEffort("feature-dev", "/test/workspace");
    expect(effort).toBe("medium");
  });

  it("env stage effort overrides config stage effort", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "low";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `model_routing:
  mode: hybrid
  stage_efforts:
    feature-dev: high
`
    );

    const effort = getStageEffort("feature-dev", "/test/workspace");
    expect(effort).toBe("low");
  });

  it("auto-derives effort in automatic mode when enabled", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `model_routing:
  mode: automatic
  effort_auto: true
`
    );

    const effort = getStageEffort("feature-dev", "/test/workspace", {
      labels: ["size:M"],
      title: "Implement feature",
    });
    expect(effort).toBe("medium");
  });

  it("auto-derives low effort for lightweight stage", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `model_routing:
  mode: hybrid
  effort_auto: true
`
    );

    // pr-create is a lightweight stage (always low effort)
    const effort = getStageEffort("pr-create", "/test/workspace", {
      labels: ["size:XL"],
      title: "Large issue",
    });
    expect(effort).toBe("low");
  });

  it("auto-derives complexity-based effort for issue-pickup (Issue #1593)", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `model_routing:
  mode: hybrid
  effort_auto: true
`
    );

    // issue-pickup is now a classification stage, not lightweight
    const effort = getStageEffort("issue-pickup", "/test/workspace", {
      labels: ["size:XL"],
      title: "Large issue",
    });
    expect(effort).toBe("high");
  });

  it("returns undefined when auto mode is enabled without metadata", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `model_routing:
  mode: automatic
  effort_auto: true
`
    );

    const effort = getStageEffort("feature-dev", "/test/workspace");
    expect(effort).toBeUndefined();
  });
});

// ============================================================================
// DEFAULT_STAGE_EFFORTS — Sonnet 4.6 Era Defaults (Issue #944)
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
    // Default: no config file (automatic mode since #946)
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns medium for feature-planning in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    expect(getStageEffort("feature-planning", "/test/workspace")).toBe("medium");
  });

  it("returns medium for feature-dev in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("medium");
  });

  it("returns low for feature-validate in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    expect(getStageEffort("feature-validate", "/test/workspace")).toBe("low");
  });

  it("returns undefined for lightweight stages in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    expect(getStageEffort("issue-pickup", "/test/workspace")).toBeUndefined();
    expect(getStageEffort("pr-create", "/test/workspace")).toBeUndefined();
    expect(getStageEffort("pr-merge", "/test/workspace")).toBeUndefined();
  });

  it("config stage_efforts overrides defaults", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `model_routing:
  stage_efforts:
    feature-dev: high
`
    );
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("high");
  });

  it("env var overrides defaults", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "high";
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("high");
  });
});

// ============================================================================
// modelSupportsEffort + EFFORT_SUPPORTING_MODELS (Issue #1235)
// ============================================================================

describe("modelSupportsEffort (Issue #1235)", () => {
  it("returns true for sonnet", () => {
    expect(modelSupportsEffort("sonnet")).toBe(true);
  });

  it("returns true for opus", () => {
    expect(modelSupportsEffort("opus")).toBe(true);
  });

  it("returns false for haiku", () => {
    expect(modelSupportsEffort("haiku")).toBe(false);
  });

  it("returns true for fable (#73 — effort must reach the CLI so xhigh/high are expressible)", () => {
    expect(modelSupportsEffort("fable")).toBe(true);
  });

  it("EFFORT_SUPPORTING_MODELS contains sonnet, opus, and fable but not haiku", () => {
    expect(EFFORT_SUPPORTING_MODELS.has("sonnet")).toBe(true);
    expect(EFFORT_SUPPORTING_MODELS.has("opus")).toBe(true);
    expect(EFFORT_SUPPORTING_MODELS.has("fable")).toBe(true);
    expect(EFFORT_SUPPORTING_MODELS.has("haiku")).toBe(false);
  });
});

// ============================================================================
// conformEffortForFable (#73)
// ============================================================================

describe("conformEffortForFable (#73)", () => {
  it("floors an explicit low/medium at high — Fable's own documented default", () => {
    expect(conformEffortForFable("low", "low", "config")).toEqual({
      effort: "high",
      coerced: true,
    });
    expect(conformEffortForFable("medium", "medium", "env")).toEqual({
      effort: "high",
      coerced: true,
    });
  });

  it("honors an explicit high or xhigh unchanged", () => {
    expect(conformEffortForFable("high", "high", "config")).toEqual({
      effort: "high",
      coerced: false,
    });
    expect(conformEffortForFable("xhigh", "xhigh", "config")).toEqual({
      effort: "xhigh",
      coerced: false,
    });
  });

  it("router-selected fable gets xhigh — the L/XL escalation IS the capability-sensitive case", () => {
    expect(conformEffortForFable("high", undefined, "auto")).toEqual({
      effort: "xhigh",
      coerced: true,
    });
    expect(conformEffortForFable(undefined, undefined, "auto-router")).toEqual({
      effort: "xhigh",
      coerced: false,
    });
  });

  it("deliberate fable pin without explicit effort omits the flag (server default high)", () => {
    expect(conformEffortForFable("medium", undefined, "config")).toEqual({
      effort: undefined,
      coerced: false,
    });
    expect(conformEffortForFable(undefined, undefined, "default")).toEqual({
      effort: undefined,
      coerced: false,
    });
    expect(conformEffortForFable("high", undefined, "performance-mode")).toEqual({
      effort: undefined,
      coerced: false,
    });
  });
});

// ============================================================================
// getModelDefaultEffort (Issue #1235)
// ============================================================================

describe("getModelDefaultEffort (Issue #1235)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT;
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns undefined when no config and no env var", () => {
    expect(getModelDefaultEffort("/test/workspace")).toBeUndefined();
  });

  it("reads default_effort from config file", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`model_routing:
  default_effort: high
`);
    expect(getModelDefaultEffort("/test/workspace")).toBe("high");
  });

  it("env var overrides config file", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT = "low";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`model_routing:
  default_effort: high
`);
    expect(getModelDefaultEffort("/test/workspace")).toBe("low");
  });

  it("ignores invalid env var values", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT = "ultra";
    expect(getModelDefaultEffort("/test/workspace")).toBeUndefined();
  });
});

// ============================================================================
// getStageEffort — default_effort precedence (Issue #1235)
// ============================================================================

describe("getStageEffort with default_effort (Issue #1235)", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("default_effort overrides DEFAULT_STAGE_EFFORTS in manual mode", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MODE = "manual";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`model_routing:
  default_effort: high
`);
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("high");
  });

  it("per-stage stage_efforts overrides default_effort", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`model_routing:
  default_effort: high
  stage_efforts:
    feature-dev: low
`);
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("low");
  });

  it("env var stage effort overrides default_effort", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "medium";
    process.env.NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT = "high";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
    expect(getStageEffort("feature-dev", "/test/workspace")).toBe("medium");
  });
});

// ============================================================================
// getMcpToolsConfig
// ============================================================================

describe("getMcpToolsConfig", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns empty array when no config file exists", () => {
    expect(getMcpToolsConfig("/test/workspace", "feature-dev")).toEqual([]);
  });

  it("returns empty array when config has no mcp-tools section", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`pipeline:
  auto_fix: true
`);
    expect(getMcpToolsConfig("/test/workspace", "feature-dev")).toEqual([]);
  });

  it("returns global tools when only global is configured", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`pipeline:
  mcp-tools:
    global:
      - mcp__sentry__capture_error
      - mcp__datadog__log_event
`);
    expect(getMcpToolsConfig("/test/workspace", "feature-dev")).toEqual([
      "mcp__sentry__capture_error",
      "mcp__datadog__log_event",
    ]);
  });

  it("returns stage-specific tools when only stages is configured", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`pipeline:
  mcp-tools:
    stages:
      feature-dev:
        - mcp__playwright__browser_navigate
        - mcp__playwright__browser_click
`);
    expect(getMcpToolsConfig("/test/workspace", "feature-dev")).toEqual([
      "mcp__playwright__browser_navigate",
      "mcp__playwright__browser_click",
    ]);
  });

  it("does not return tools for a different stage", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`pipeline:
  mcp-tools:
    stages:
      feature-dev:
        - mcp__playwright__browser_navigate
`);
    expect(getMcpToolsConfig("/test/workspace", "feature-validate")).toEqual([]);
  });

  it("merges global and per-stage tools (union, deduped)", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`pipeline:
  mcp-tools:
    global:
      - mcp__sentry__capture_error
      - mcp__playwright__browser_navigate
    stages:
      feature-dev:
        - mcp__playwright__browser_navigate
        - mcp__playwright__browser_click
`);
    const result = getMcpToolsConfig("/test/workspace", "feature-dev");
    // All tools present, no duplicates
    expect(result).toContain("mcp__sentry__capture_error");
    expect(result).toContain("mcp__playwright__browser_navigate");
    expect(result).toContain("mcp__playwright__browser_click");
    expect(result.length).toBe(3);
  });

  it("returns empty array when mcp-tools section is empty", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`pipeline:
  mcp-tools:
`);
    expect(getMcpToolsConfig("/test/workspace", "feature-dev")).toEqual([]);
  });

  it("returns global tools when stage is not provided", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`pipeline:
  mcp-tools:
    global:
      - mcp__sentry__capture_error
    stages:
      feature-dev:
        - mcp__playwright__browser_navigate
`);
    // No stage — only global tools returned
    const result = getMcpToolsConfig("/test/workspace");
    expect(result).toEqual(["mcp__sentry__capture_error"]);
  });

  it("returns empty array on config read error", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockImplementation(() => {
      throw new Error("read error");
    });
    expect(getMcpToolsConfig("/test/workspace", "feature-dev")).toEqual([]);
  });
});
