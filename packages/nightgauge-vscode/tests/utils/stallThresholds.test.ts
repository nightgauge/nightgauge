/**
 * Unit tests for stall threshold configuration
 *
 * Tests per-stage stall warning thresholds for Issue #769.
 * Tests history-calibrated stall thresholds for Issue #2654.
 *
 * @see Issue #769 - Configurable stall thresholds
 * @see Issue #2654 - History-calibrated stall thresholds
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";

// Mock vscode before importing the module. workspaceFolders is undefined:
// every test that needs a workspace passes an explicit root, and the
// no-workspace-root test needs the auto-detect rung to come up empty.
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
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

// Mock StageDurationAnalyzer for calibration tests. Calibration is
// mode-bucketed since #3216 — the resolver calls getStageStatsByMode.
vi.mock("../../src/utils/StageDurationAnalyzer", () => ({
  StageDurationAnalyzer: {
    getStageStatsByMode: vi.fn(),
  },
}));

import {
  getStallThresholds,
  getStallKillMultiplier,
  DEFAULT_STALL_THRESHOLDS,
  roundUpTo30s,
  computeKillThreshold,
  getStallCalibrationMinRuns,
  precomputeCalibratedStallThresholds,
  getCalibratedStallData,
} from "../../src/utils/incrediConfig";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";
import { StageDurationAnalyzer } from "../../src/utils/StageDurationAnalyzer";

describe("incrediConfig - Stall Thresholds", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    // resetAllMocks (not clearAllMocks): mockReturnValue implementations
    // survive clearAllMocks, so one test's config fixture leaks into the
    // next test's reads.
    vi.resetAllMocks();
    process.env = { ...originalEnv };
    // Clear all stall threshold env vars
    for (const stage of Object.keys(DEFAULT_STALL_THRESHOLDS)) {
      const envKey = `NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_${stage.toUpperCase().replace(/-/g, "_")}`;
      delete process.env[envKey];
    }
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  describe("DEFAULT_STALL_THRESHOLDS", () => {
    it("has correct default values matching acceptance criteria", () => {
      expect(DEFAULT_STALL_THRESHOLDS["issue-pickup"]).toBe(180);
      expect(DEFAULT_STALL_THRESHOLDS["feature-planning"]).toBe(180);
      expect(DEFAULT_STALL_THRESHOLDS["feature-dev"]).toBe(600);
      expect(DEFAULT_STALL_THRESHOLDS["feature-validate"]).toBe(300);
      expect(DEFAULT_STALL_THRESHOLDS["pr-create"]).toBe(180);
      expect(DEFAULT_STALL_THRESHOLDS["pr-merge"]).toBe(420);
    });

    it("has all six pipeline stages", () => {
      expect(Object.keys(DEFAULT_STALL_THRESHOLDS)).toHaveLength(6);
    });
  });

  describe("getStallThresholds", () => {
    it("returns defaults when no config file exists", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds).toEqual(DEFAULT_STALL_THRESHOLDS);
    });

    it("reads per-stage values from config file", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  ci_timeout: 300
  stall_thresholds:
    issue-pickup: 90
    feature-planning: 240
    feature-dev: 900
    feature-validate: 450
    pr-create: 90
    pr-merge: 240
enforcement:
  dependencies:
    enabled: true
`);

      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds["issue-pickup"]).toBe(90);
      expect(thresholds["feature-planning"]).toBe(240);
      expect(thresholds["feature-dev"]).toBe(900);
      expect(thresholds["feature-validate"]).toBe(450);
      expect(thresholds["pr-create"]).toBe(90);
      expect(thresholds["pr-merge"]).toBe(240);
    });

    it("falls back to defaults for missing stages in config", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_thresholds:
    feature-dev: 900
`);

      const thresholds = getStallThresholds("/test/workspace");

      // Config override applied
      expect(thresholds["feature-dev"]).toBe(900);
      // Defaults for non-overridden stages
      expect(thresholds["issue-pickup"]).toBe(180);
      expect(thresholds["feature-planning"]).toBe(180);
      expect(thresholds["feature-validate"]).toBe(300);
      expect(thresholds["pr-create"]).toBe(180);
      expect(thresholds["pr-merge"]).toBe(420);
    });

    it("respects env var overrides per stage", () => {
      process.env.NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV = "1200";

      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds["feature-dev"]).toBe(1200);
      // Other stages use defaults
      expect(thresholds["issue-pickup"]).toBe(180);
    });

    it("env var overrides take precedence over config file", () => {
      process.env.NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV = "1200";

      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_thresholds:
    feature-dev: 900
`);

      const thresholds = getStallThresholds("/test/workspace");

      // Env var wins over config file
      expect(thresholds["feature-dev"]).toBe(1200);
    });

    it("ignores env var values below minimum (30s)", () => {
      process.env.NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV = "10";

      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      const thresholds = getStallThresholds("/test/workspace");

      // Falls back to default because 10 < 30
      expect(thresholds["feature-dev"]).toBe(600);
    });

    it("ignores config values below minimum (30s)", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_thresholds:
    feature-dev: 15
`);

      const thresholds = getStallThresholds("/test/workspace");

      // Falls back to default because 15 < 30
      expect(thresholds["feature-dev"]).toBe(600);
    });

    it("ignores invalid (non-numeric) env var values", () => {
      process.env.NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV = "abc";

      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds["feature-dev"]).toBe(600);
    });

    it("handles stall_thresholds section ending at next pipeline subsection", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  ci_timeout: 300
  stall_thresholds:
    feature-dev: 900
  stage_models:
    feature-dev: opus
`);

      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds["feature-dev"]).toBe(900);
    });

    it("handles stall_thresholds section ending at next top-level section", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_thresholds:
    feature-dev: 900
    pr-create: 120
enforcement:
  dependencies:
    enabled: true
`);

      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds["feature-dev"]).toBe(900);
      expect(thresholds["pr-create"]).toBe(120);
    });

    it("returns defaults when no workspace root available", () => {
      // File-level vscode mock has workspaceFolders undefined, so the
      // auto-detect rung resolves no root.
      const thresholds = getStallThresholds();

      expect(thresholds).toEqual(DEFAULT_STALL_THRESHOLDS);
    });

    it("handles config file read errors gracefully", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockImplementation(() => {
        throw new Error("Permission denied");
      });

      // mergedConfigReader treats an unreadable tier file as absent (empty
      // content), so thresholds fall back to defaults without an error log —
      // the pre-mergedConfigReader implementation console.error'd here.
      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds).toEqual(DEFAULT_STALL_THRESHOLDS);
    });

    it("handles comments in stall_thresholds section", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_thresholds:
    # Long-running stage
    feature-dev: 900
    # Quick stage
    pr-create: 90
`);

      const thresholds = getStallThresholds("/test/workspace");

      expect(thresholds["feature-dev"]).toBe(900);
      expect(thresholds["pr-create"]).toBe(90);
    });
  });

  describe("getStallKillMultiplier", () => {
    it("returns default of 8 when no config exists", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      expect(getStallKillMultiplier("/test/workspace")).toBe(8);
    });

    it("reads value from config file", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_kill_multiplier: 3
`);

      expect(getStallKillMultiplier("/test/workspace")).toBe(3);
    });

    it("supports 0 to disable auto-kill", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_kill_multiplier: 0
`);

      expect(getStallKillMultiplier("/test/workspace")).toBe(0);
    });

    it("respects env var override", () => {
      process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER = "8";

      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      expect(getStallKillMultiplier("/test/workspace")).toBe(8);
      delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER;
    });

    it("env var overrides config file", () => {
      process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER = "10";

      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_kill_multiplier: 3
`);

      expect(getStallKillMultiplier("/test/workspace")).toBe(10);
      delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER;
    });

    // #3020 — feature-validate has a per-stage default of 4 (was 8) so a
    // stuck validate run is killed at 20 min instead of 40 min.
    it("uses per-stage default 4 for feature-validate", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(4);
      // other stages still use the global 8
      expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(8);
    });

    it("per-stage env var beats global env and per-stage default", () => {
      process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER = "10";
      process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_FEATURE_VALIDATE = "2";
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });
      try {
        expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(2);
        // unrelated stage still uses the global env override
        expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(10);
      } finally {
        delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER;
        delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_FEATURE_VALIDATE;
      }
    });

    it("per-stage YAML override wins over per-stage default and global YAML", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });
      vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_kill_multiplier: 6
  stall_kill_multipliers:
    feature-validate: 2
`);
      expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(2);
      // feature-dev not in stage map → global YAML wins (6)
      expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(6);
    });

    it("handles config read errors gracefully", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockImplementation(() => {
        throw new Error("Permission denied");
      });

      expect(getStallKillMultiplier("/test/workspace")).toBe(8);
    });
  });
});

// ============================================================================
// Issue #2654 — History-Calibrated Stall Thresholds
// ============================================================================

describe("roundUpTo30s", () => {
  it("rounds up to the nearest 30s boundary", () => {
    expect(roundUpTo30s(601)).toBe(630);
    expect(roundUpTo30s(631)).toBe(660);
    expect(roundUpTo30s(599)).toBe(600);
  });

  it("returns exact boundary unchanged", () => {
    expect(roundUpTo30s(600)).toBe(600);
    expect(roundUpTo30s(3000)).toBe(3000);
    expect(roundUpTo30s(30)).toBe(30);
  });

  it("rounds 1s up to 30s", () => {
    expect(roundUpTo30s(1)).toBe(30);
  });
});

describe("computeKillThreshold", () => {
  it("uses max×2 when that is larger than warn×3", () => {
    // max=1000s, warn=300s → max*2=2000, warn*3=900 → 2000
    expect(computeKillThreshold(1000, 300)).toBe(2000);
  });

  it("uses warn×3 when that is larger than max×2", () => {
    // max=400s, warn=600s → max*2=800, warn*3=1800 → 1800
    expect(computeKillThreshold(400, 600)).toBe(1800);
  });

  it("uses warn×3 when equal", () => {
    // max=300s, warn=200s → max*2=600, warn*3=600 → 600
    expect(computeKillThreshold(300, 200)).toBe(600);
  });
});

describe("getStallCalibrationMinRuns", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.resetAllMocks();
    process.env = { ...originalEnv };
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns default of 10 when no config file exists", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });

    expect(getStallCalibrationMinRuns("/test/workspace")).toBe(10);
  });

  it("reads stall_calibration_min_runs from config file", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });

    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_calibration_min_runs: 5
`);

    expect(getStallCalibrationMinRuns("/test/workspace")).toBe(5);
  });

  it("supports 0 to disable calibration", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });

    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_calibration_min_runs: 0
`);

    expect(getStallCalibrationMinRuns("/test/workspace")).toBe(0);
  });
});

describe("precomputeCalibratedStallThresholds + getCalibratedStallData", () => {
  const workspace = "/test/calibration-workspace";
  const originalEnv = process.env;

  beforeEach(() => {
    vi.resetAllMocks();
    process.env = { ...originalEnv };
    // Clear all stall threshold env vars
    for (const stage of Object.keys(DEFAULT_STALL_THRESHOLDS)) {
      const envKey = `NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_${stage.toUpperCase().replace(/-/g, "_")}`;
      delete process.env[envKey];
    }
    // Default: config file exists with no stall threshold overrides
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: `${workspace}/.nightgauge/config.yaml`,
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue("pipeline:\n");
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("uses calibrated warn=p95×1.5 rounded to 30s when count >= min_runs", async () => {
    // p95 = 400000ms = 400s → 400 * 1.5 = 600s → roundUpTo30s(600) = 600s
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue({
      stage: "feature-dev",
      count: 15,
      mean_ms: 350000,
      p50_ms: 340000,
      p75_ms: 380000,
      p95_ms: 400000,
      p99_ms: 450000,
      max_ms: 500000,
      min_ms: 200000,
      stddev_ms: 60000,
      last_updated: new Date().toISOString(),
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data).toBeDefined();
    expect(data!.source).toBe("calibrated");
    expect(data!.isColdStart).toBe(false);
    expect(data!.warnSec).toBe(600); // roundUpTo30s(400 * 1.5) = roundUpTo30s(600) = 600
  });

  it("computes kill = max(max×2, warn×3) correctly", async () => {
    // p95 = 600000ms → warn = roundUpTo30s(900) = 900s
    // max = 800000ms = 800s → kill = max(800*2, 900*3) = max(1600, 2700) = 2700s
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue({
      stage: "feature-dev",
      count: 12,
      mean_ms: 500000,
      p50_ms: 480000,
      p75_ms: 560000,
      p95_ms: 600000,
      p99_ms: 750000,
      max_ms: 800000,
      min_ms: 300000,
      stddev_ms: 100000,
      last_updated: new Date().toISOString(),
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data!.warnSec).toBe(900); // roundUpTo30s(600 * 1.5) = 900
    expect(data!.killSec).toBe(2700); // max(800*2, 900*3) = max(1600, 2700) = 2700
  });

  it("cold start (count < min_runs): warn=static default, kill=disabled", async () => {
    // count=5, min_runs=10 → cold start
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue({
      stage: "feature-dev",
      count: 5,
      mean_ms: 400000,
      p50_ms: 390000,
      p75_ms: 420000,
      p95_ms: 450000,
      p99_ms: 480000,
      max_ms: 500000,
      min_ms: 300000,
      stddev_ms: 60000,
      last_updated: new Date().toISOString(),
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data!.source).toBe("static");
    expect(data!.isColdStart).toBe(true);
    expect(data!.warnSec).toBe(DEFAULT_STALL_THRESHOLDS["feature-dev"]); // 600
    expect(data!.killSec).toBe(0); // kill disabled in cold start
  });

  it("cold start with no history (stats undefined)", async () => {
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue(undefined);

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data!.isColdStart).toBe(true);
    expect(data!.killSec).toBe(0);
    expect(data!.warnSec).toBe(DEFAULT_STALL_THRESHOLDS["feature-dev"]);
  });

  it("exactly at min_runs threshold uses calibrated values", async () => {
    // count=10, min_runs=10 → should use calibrated
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue({
      stage: "issue-pickup",
      count: 10,
      mean_ms: 100000,
      p50_ms: 95000,
      p75_ms: 110000,
      p95_ms: 120000, // 120s → *1.5 = 180s → roundUpTo30s(180) = 180s
      p99_ms: 140000,
      max_ms: 160000, // kill = max(160*2, 180*3) = max(320, 540) = 540s
      min_ms: 60000,
      stddev_ms: 20000,
      last_updated: new Date().toISOString(),
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "issue-pickup");

    expect(data!.source).toBe("calibrated");
    expect(data!.isColdStart).toBe(false);
    expect(data!.warnSec).toBe(180);
    expect(data!.killSec).toBe(540);
  });

  it("env var override takes highest precedence over calibrated values", async () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV = "1200";

    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue({
      stage: "feature-dev",
      count: 20,
      mean_ms: 400000,
      p50_ms: 380000,
      p75_ms: 420000,
      p95_ms: 450000,
      p99_ms: 500000,
      max_ms: 600000,
      min_ms: 200000,
      stddev_ms: 80000,
      last_updated: new Date().toISOString(),
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data!.source).toBe("env");
    expect(data!.warnSec).toBe(1200); // env var wins
    expect(data!.isColdStart).toBe(false);
  });

  it("config.yaml override takes precedence over calibrated values", async () => {
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_thresholds:
    feature-dev: 900
`);

    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue({
      stage: "feature-dev",
      count: 20,
      mean_ms: 400000,
      p50_ms: 380000,
      p75_ms: 420000,
      p95_ms: 450000,
      p99_ms: 500000,
      max_ms: 600000,
      min_ms: 200000,
      stddev_ms: 80000,
      last_updated: new Date().toISOString(),
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data!.source).toBe("config");
    expect(data!.warnSec).toBe(900); // config.yaml wins
  });

  it("stall_calibration_min_runs=0 disables calibration (all static)", async () => {
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_calibration_min_runs: 0
`);

    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue({
      stage: "feature-dev",
      count: 100,
      mean_ms: 400000,
      p50_ms: 380000,
      p75_ms: 420000,
      p95_ms: 450000,
      p99_ms: 500000,
      max_ms: 600000,
      min_ms: 200000,
      stddev_ms: 80000,
      last_updated: new Date().toISOString(),
    });

    await precomputeCalibratedStallThresholds(workspace);
    // When min_runs=0, cache is set to empty — no data returned
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data).toBeUndefined();
  });

  it("getCalibratedStallData returns undefined before precompute is called", () => {
    const unknownWorkspace = "/test/never-computed-workspace";
    expect(getCalibratedStallData(unknownWorkspace, "feature-dev")).toBeUndefined();
  });

  it("calibration failure falls back to static cold-start behavior", async () => {
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockRejectedValue(new Error("disk error"));

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev");

    expect(data!.source).toBe("static");
    expect(data!.isColdStart).toBe(true);
    expect(data!.killSec).toBe(0);
  });

  it("p95 rounding edge cases are handled correctly", () => {
    // 599s → roundUpTo30s(599) = 600s
    expect(roundUpTo30s(599)).toBe(600);
    // 601s → roundUpTo30s(601) = 630s
    expect(roundUpTo30s(601)).toBe(630);
    // 3000s stays 3000s
    expect(roundUpTo30s(3000)).toBe(3000);
    // 1s → 30s (minimum 30s boundary)
    expect(roundUpTo30s(1)).toBe(30);
  });
});
