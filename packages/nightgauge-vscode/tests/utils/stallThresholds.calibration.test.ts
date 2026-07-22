/**
 * Unit tests for history-calibrated stall threshold logic.
 *
 * Tests the calibration algorithm: p95×1.5, max×2 (floor: warn×3),
 * cold start behavior, override precedence, edge cases, and the
 * per-mode bucketing introduced in issue #3216.
 *
 * @see Issue #2654 - Replace static stall thresholds with history-calibrated values
 * @see Issue #3216 - Calibration bucketing by (size, mode)
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

// Mock StageDurationAnalyzer — issue #3216 added getStageStatsByMode which is
// what the precompute loop now consumes. Tests configure the mock with
// per-mode return values via mockImplementation so different modes can yield
// different stats.
vi.mock("../../src/utils/StageDurationAnalyzer", () => ({
  StageDurationAnalyzer: {
    getStageStats: vi.fn(),
    getStageStatsByMode: vi.fn(),
  },
}));

import {
  DEFAULT_STALL_THRESHOLDS,
  roundUpTo30s,
  computeKillThreshold,
  getStallCalibrationMinRuns,
  precomputeCalibratedStallThresholds,
  getCalibratedStallData,
} from "../../src/utils/incrediConfig";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";
import { StageDurationAnalyzer } from "../../src/utils/StageDurationAnalyzer";
import type { PerformanceMode } from "../../src/utils/modeProfiles";

// ============================================================================
// Helpers
// ============================================================================

interface FakeStats {
  count: number;
  p95_ms: number;
  max_ms: number;
}

/**
 * Configure `getStageStatsByMode` to return a per-mode fake stats map for a
 * specific stage. Modes not present in the map yield `undefined` (no data).
 */
function setStageStatsByMode(
  stage: string,
  perMode: Partial<Record<PerformanceMode, FakeStats | undefined>>
) {
  vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockImplementation(
    async (_root, requestedStage, requestedMode) => {
      if (requestedStage !== stage) return undefined;
      const fake = perMode[requestedMode];
      if (!fake) return undefined;
      return {
        stage: requestedStage,
        count: fake.count,
        mean_ms: fake.p95_ms * 0.7,
        p50_ms: fake.p95_ms * 0.7,
        p75_ms: fake.p95_ms * 0.85,
        p95_ms: fake.p95_ms,
        p99_ms: fake.p95_ms * 1.05,
        max_ms: fake.max_ms,
        min_ms: fake.p95_ms * 0.4,
        stddev_ms: fake.p95_ms * 0.15,
        last_updated: new Date().toISOString(),
      };
    }
  );
}

// ============================================================================
// roundUpTo30s
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

  it("handles zero", () => {
    expect(roundUpTo30s(0)).toBe(0);
  });
});

// ============================================================================
// computeKillThreshold
// ============================================================================

describe("computeKillThreshold", () => {
  it("uses max×2 when larger than warn×3", () => {
    expect(computeKillThreshold(1000, 300)).toBe(2000);
  });

  it("uses warn×3 when larger than max×2", () => {
    expect(computeKillThreshold(400, 600)).toBe(1800);
  });

  it("uses warn×3 when equal to max×2", () => {
    expect(computeKillThreshold(300, 200)).toBe(600);
  });
});

// ============================================================================
// getStallCalibrationMinRuns
// ============================================================================

describe("getStallCalibrationMinRuns", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
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

  it("supports 0 to disable calibration entirely", () => {
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

  it("supports large values", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });

    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_calibration_min_runs: 100
`);

    expect(getStallCalibrationMinRuns("/test/workspace")).toBe(100);
  });

  it("returns default on config read error", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });

    vi.mocked(fs.readFileSync).mockImplementation(() => {
      throw new Error("Permission denied");
    });

    expect(getStallCalibrationMinRuns("/test/workspace")).toBe(10);
  });
});

// ============================================================================
// precomputeCalibratedStallThresholds + getCalibratedStallData
// ============================================================================

describe("precomputeCalibratedStallThresholds + getCalibratedStallData", () => {
  let workspace: string;
  let testId = 0;

  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    for (const stage of Object.keys(DEFAULT_STALL_THRESHOLDS)) {
      const envKey = `NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_${stage.toUpperCase().replace(/-/g, "_")}`;
      delete process.env[envKey];
    }
    workspace = `/test/calibration-workspace-${++testId}`;
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: `${workspace}/.nightgauge/config.yaml`,
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue("pipeline:\n");
    // Default: per-mode lookup returns no data — every cell cold-starts.
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockResolvedValue(undefined);
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("computes calibrated warn=p95×1.5 (rounded to 30s) when count >= min_runs (elevated mode)", async () => {
    setStageStatsByMode("feature-dev", {
      elevated: { count: 15, p95_ms: 400000, max_ms: 500000 },
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev", "elevated");

    expect(data).toBeDefined();
    expect(data!.source).toBe("calibrated");
    expect(data!.isColdStart).toBe(false);
    expect(data!.warnSec).toBe(600);
  });

  it("computes kill = max(max×2, warn×3) correctly", async () => {
    // p95 = 600s → warn = 900s; max = 800s → kill = max(1600, 2700) = 2700
    setStageStatsByMode("feature-dev", {
      elevated: { count: 12, p95_ms: 600000, max_ms: 800000 },
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev", "elevated");

    expect(data!.warnSec).toBe(900);
    expect(data!.killSec).toBe(2700);
  });

  it("cold start (count < min_runs): warn=static default, kill=disabled", async () => {
    setStageStatsByMode("feature-dev", {
      elevated: { count: 5, p95_ms: 450000, max_ms: 500000 },
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev", "elevated");

    expect(data!.source).toBe("static");
    expect(data!.isColdStart).toBe(true);
    expect(data!.warnSec).toBe(DEFAULT_STALL_THRESHOLDS["feature-dev"]);
    expect(data!.killSec).toBe(0);
  });

  it("cold start with no history (stats=undefined for all modes)", async () => {
    // Default mock returns undefined — every (stage, mode) cold-starts
    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev", "elevated");

    expect(data!.isColdStart).toBe(true);
    expect(data!.killSec).toBe(0);
    expect(data!.warnSec).toBe(DEFAULT_STALL_THRESHOLDS["feature-dev"]);
  });

  it("exactly at min_runs boundary uses calibrated values (count === min_runs)", async () => {
    setStageStatsByMode("issue-pickup", {
      elevated: { count: 10, p95_ms: 120000, max_ms: 160000 },
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "issue-pickup", "elevated");

    expect(data!.source).toBe("calibrated");
    expect(data!.isColdStart).toBe(false);
    expect(data!.warnSec).toBe(180);
    expect(data!.killSec).toBe(540);
  });

  it("env var override takes highest precedence and applies to ALL modes", async () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV = "1200";

    setStageStatsByMode("feature-dev", {
      elevated: { count: 20, p95_ms: 450000, max_ms: 600000 },
      efficiency: { count: 20, p95_ms: 200000, max_ms: 250000 },
    });

    await precomputeCalibratedStallThresholds(workspace);

    for (const mode of ["efficiency", "elevated", "maximum"] as PerformanceMode[]) {
      const data = getCalibratedStallData(workspace, "feature-dev", mode);
      expect(data!.source).toBe("env");
      expect(data!.warnSec).toBe(1200);
      expect(data!.isColdStart).toBe(false);
    }
  });

  it("config.yaml override takes precedence and applies to ALL modes", async () => {
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_thresholds:
    feature-dev: 900
`);

    setStageStatsByMode("feature-dev", {
      elevated: { count: 20, p95_ms: 450000, max_ms: 600000 },
      maximum: { count: 20, p95_ms: 250000, max_ms: 300000 },
    });

    await precomputeCalibratedStallThresholds(workspace);

    for (const mode of ["efficiency", "elevated", "maximum"] as PerformanceMode[]) {
      const data = getCalibratedStallData(workspace, "feature-dev", mode);
      expect(data!.source).toBe("config");
      expect(data!.warnSec).toBe(900);
    }
  });

  it("stall_calibration_min_runs=0 disables calibration (cache set to empty)", async () => {
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_calibration_min_runs: 0
`);

    setStageStatsByMode("feature-dev", {
      elevated: { count: 100, p95_ms: 450000, max_ms: 600000 },
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev", "elevated");

    expect(data).toBeUndefined();
  });

  it("getCalibratedStallData returns undefined before precompute is called", () => {
    const unknownWorkspace = "/test/never-computed-workspace";
    expect(getCalibratedStallData(unknownWorkspace, "feature-dev", "elevated")).toBeUndefined();
  });

  it("calibration failure for one mode falls back to per-mode cold start", async () => {
    vi.mocked(StageDurationAnalyzer.getStageStatsByMode).mockImplementation(async (_r, _s, m) => {
      if (m === "elevated") throw new Error("disk error");
      return undefined;
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-dev", "elevated");

    expect(data!.source).toBe("static");
    expect(data!.isColdStart).toBe(true);
    expect(data!.killSec).toBe(0);
  });

  it("custom stall_calibration_min_runs is respected", async () => {
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_calibration_min_runs: 3
`);

    setStageStatsByMode("pr-create", {
      elevated: { count: 3, p95_ms: 100000, max_ms: 120000 },
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "pr-create", "elevated");

    expect(data!.source).toBe("calibrated");
    expect(data!.warnSec).toBe(150);
    expect(data!.killSec).toBe(450);
  });

  it("single run (count=1) below default min_runs=10 triggers cold start", async () => {
    setStageStatsByMode("feature-planning", {
      elevated: { count: 1, p95_ms: 120000, max_ms: 120000 },
    });

    await precomputeCalibratedStallThresholds(workspace);
    const data = getCalibratedStallData(workspace, "feature-planning", "elevated");

    expect(data!.isColdStart).toBe(true);
    expect(data!.warnSec).toBe(DEFAULT_STALL_THRESHOLDS["feature-planning"]);
    expect(data!.killSec).toBe(0);
  });

  // ============================================================================
  // Per-mode bucketing (issue #3216)
  // ============================================================================

  describe("per-mode bucketing (issue #3216)", () => {
    it("efficiency and maximum produce different thresholds on the same stage", async () => {
      // Efficiency runs are fast; maximum runs are long. Each mode's bucket
      // should produce its own warn/kill thresholds rather than smearing.
      setStageStatsByMode("feature-dev", {
        efficiency: { count: 12, p95_ms: 150000, max_ms: 180000 }, // 150s → 230s? warn=roundUpTo30s(225)=240
        elevated: { count: 12, p95_ms: 400000, max_ms: 500000 },
        maximum: { count: 12, p95_ms: 1200000, max_ms: 1800000 }, // 1200s → 1800s warn
      });

      await precomputeCalibratedStallThresholds(workspace);

      const efficiency = getCalibratedStallData(workspace, "feature-dev", "efficiency");
      const elevated = getCalibratedStallData(workspace, "feature-dev", "elevated");
      const maximum = getCalibratedStallData(workspace, "feature-dev", "maximum");

      expect(efficiency!.source).toBe("calibrated");
      expect(elevated!.source).toBe("calibrated");
      expect(maximum!.source).toBe("calibrated");

      // Critical: each mode's warning differs — no cross-mode contamination
      expect(efficiency!.warnSec).toBeLessThan(elevated!.warnSec);
      expect(elevated!.warnSec).toBeLessThan(maximum!.warnSec);

      // Concrete: efficiency warn = roundUpTo30s(150 * 1.5) = roundUpTo30s(225) = 240
      expect(efficiency!.warnSec).toBe(240);
      // elevated = roundUpTo30s(600) = 600
      expect(elevated!.warnSec).toBe(600);
      // maximum = roundUpTo30s(1200 * 1.5) = roundUpTo30s(1800) = 1800
      expect(maximum!.warnSec).toBe(1800);
    });

    it("falls back to elevated bucket when active mode bucket has insufficient samples (AC3)", async () => {
      // efficiency: empty (no data). elevated: rich. maximum: empty.
      setStageStatsByMode("feature-dev", {
        elevated: { count: 20, p95_ms: 400000, max_ms: 500000 },
      });

      await precomputeCalibratedStallThresholds(workspace);

      const elevated = getCalibratedStallData(workspace, "feature-dev", "elevated");
      const efficiency = getCalibratedStallData(workspace, "feature-dev", "efficiency");
      const maximum = getCalibratedStallData(workspace, "feature-dev", "maximum");

      // Elevated has data → calibrated
      expect(elevated!.source).toBe("calibrated");
      expect(elevated!.warnSec).toBe(600);

      // Efficiency falls back to elevated values
      expect(efficiency!.source).toBe("calibrated");
      expect(efficiency!.warnSec).toBe(600);
      expect(efficiency!.killSec).toBe(elevated!.killSec);
      expect(efficiency!.isColdStart).toBe(false);

      // Maximum also falls back to elevated values
      expect(maximum!.source).toBe("calibrated");
      expect(maximum!.warnSec).toBe(600);
      expect(maximum!.isColdStart).toBe(false);
    });

    it("when elevated bucket is also empty, non-elevated modes cold-start", async () => {
      // No data for any mode → all cold start
      // Default mock returns undefined for all queries
      await precomputeCalibratedStallThresholds(workspace);

      for (const mode of ["efficiency", "elevated", "maximum"] as PerformanceMode[]) {
        const data = getCalibratedStallData(workspace, "feature-dev", mode);
        expect(data!.isColdStart).toBe(true);
        expect(data!.killSec).toBe(0);
        expect(data!.warnSec).toBe(DEFAULT_STALL_THRESHOLDS["feature-dev"]);
      }
    });

    it("getCalibratedStallData defaults mode to active performance mode when omitted", async () => {
      // The default mode resolves to elevated when no env var / state file.
      setStageStatsByMode("feature-dev", {
        elevated: { count: 12, p95_ms: 400000, max_ms: 500000 },
      });

      await precomputeCalibratedStallThresholds(workspace);

      const explicit = getCalibratedStallData(workspace, "feature-dev", "elevated");
      const implicit = getCalibratedStallData(workspace, "feature-dev");
      expect(implicit).toEqual(explicit);
    });

    it("each mode is calibrated independently — efficiency does not pull elevated thresholds upward", async () => {
      // elevated has long p95; efficiency has short p95. The bug pre-#3216
      // was that an unfiltered history would pull efficiency's thresholds
      // toward the long elevated p95. Verify that does not happen now.
      setStageStatsByMode("feature-dev", {
        efficiency: { count: 15, p95_ms: 100000, max_ms: 130000 },
        elevated: { count: 15, p95_ms: 600000, max_ms: 800000 },
      });

      await precomputeCalibratedStallThresholds(workspace);

      const efficiency = getCalibratedStallData(workspace, "feature-dev", "efficiency");
      // efficiency warn = roundUpTo30s(100 * 1.5) = roundUpTo30s(150) = 150
      expect(efficiency!.warnSec).toBe(150);
      // No upward pull from elevated's 600s p95 — efficiency stages won't be
      // killed prematurely after a mode switch (the issue described in #3216).
    });
  });
});
