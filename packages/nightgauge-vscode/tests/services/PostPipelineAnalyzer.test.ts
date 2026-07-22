/**
 * PostPipelineAnalyzer.test.ts
 *
 * Unit tests for PostPipelineAnalyzer service:
 * - Record adapter (JSONL run records → SDK analysis records)
 * - Analysis execution (mock ExecutionHistoryReader, verify ModelPerformanceAnalyzer)
 * - Analysis storage and retention
 * - formatSelfCheck output
 * - Non-critical failure handling
 * - Cost-per-issue aggregation
 * - Gate effectiveness analysis
 * - Skill effectiveness tracking
 * - Calibration table update
 * - Stage execution stats computation
 *
 * @see Issue #943 - Integrate ModelPerformanceAnalyzer into post-pipeline feedback loop
 * @see Issue #1045 - Post-pipeline outcome analysis and learning system
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { ExecutionHistoryRunRecord } from "../../src/schemas/executionHistory";

// Mock node:fs/promises
vi.mock("node:fs/promises");

// Mock ExecutionHistoryReader
const mockReadAll = vi.fn();
const mockGetCostByIssue = vi.fn();
vi.mock("../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    readAll: (...args: unknown[]) => mockReadAll(...args),
    getCostByIssue: (...args: unknown[]) => mockGetCostByIssue(...args),
  },
}));

// Mock ModelPerformanceAnalyzer and FailurePatternDetector
const mockAnalyze = vi.fn();
const mockFailureAnalyze = vi.fn();
const mockSkillEffectivenessAnalyze = vi.fn();
vi.mock("@nightgauge/sdk", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@nightgauge/sdk")>();
  return {
    ...actual,
    ModelPerformanceAnalyzer: vi.fn(function () {
      return { analyze: mockAnalyze };
    }),
    FailurePatternDetector: vi.fn(function () {
      return { analyze: mockFailureAnalyze };
    }),
    SkillEffectivenessAnalyzer: {
      analyze: (...args: unknown[]) => mockSkillEffectivenessAnalyze(...args),
    },
  };
});

// Mock GateMetricsWriter
const mockGateMetricsReadAll = vi.fn().mockResolvedValue([]);
vi.mock("../../src/utils/gateMetricsWriter", () => ({
  GateMetricsWriter: {
    readAll: (...args: unknown[]) => mockGateMetricsReadAll(...args),
  },
}));

// Mock node:child_process for getRecentSkillChanges
// Must use the kCustom symbol so promisify() works correctly with the mock.
// Without kCustom, promisify resolves with a string instead of { stdout, stderr }.
// Use vi.hoisted() so the variable is initialized before vi.mock() hoisting.
// @see MEMORY.md — EpicDashboard pattern
const { mockExec } = vi.hoisted(() => {
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const execMock = vi.fn();
  (execMock as any)[kCustom] = (cmd: string, opts: unknown) =>
    new Promise<{ stdout: string; stderr: string }>((resolve, reject) => {
      execMock(cmd, opts, (err: Error | null, stdout: string, stderr: string) => {
        if (err) reject(Object.assign(err, { stdout, stderr }));
        else resolve({ stdout, stderr });
      });
    });
  return { mockExec: execMock };
});
vi.mock("node:child_process", () => ({
  exec: mockExec,
}));

// Mock SkillEffectivenessWriter
const mockSkillEffectivenessAppend = vi.fn().mockResolvedValue(undefined);
vi.mock("../../src/utils/SkillEffectivenessWriter", () => ({
  SkillEffectivenessWriter: {
    appendRecord: (...args: unknown[]) => mockSkillEffectivenessAppend(...args),
    readAll: vi.fn().mockResolvedValue([]),
    enforceRetention: vi.fn().mockResolvedValue(undefined),
    getFilePath: vi.fn().mockReturnValue("/workspace/.nightgauge/health/skill-effectiveness.jsonl"),
  },
}));

// Mock CalibrationService (dynamic import in analyze)
const mockCalibrationBuildFromHistory = vi.fn();
const mockCalibrationSave = vi.fn().mockResolvedValue(undefined);
const mockCalibrationGetDefaultPath = vi
  .fn()
  .mockReturnValue("/workspace/.nightgauge/calibration.json");
// CalibrationService is imported dynamically via await import() in the source,
// so we mock the SDK module above. We need to add it to the SDK mock.
// Since CalibrationService is accessed via dynamic import, we handle it in the
// SDK mock by adding it there.

import {
  PostPipelineAnalyzer,
  type PostPipelineAnalysisResult,
} from "../../src/services/PostPipelineAnalyzer";

// Mock logger
function createMockLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

// Helper: create a minimal run record with model selection data
function createRunRecord(
  overrides: Partial<ExecutionHistoryRunRecord> = {}
): ExecutionHistoryRunRecord {
  return {
    schema_version: "1",
    record_type: "run",
    issue_number: 100,
    title: "Test issue",
    branch: "feat/100-test",
    base_branch: "main",
    execution_mode: "automatic",
    started_at: "2026-02-19T00:00:00Z",
    completed_at: "2026-02-19T01:00:00Z",
    total_duration_ms: 3600000,
    outcome: "complete",
    stages: {
      "feature-dev": {
        status: "complete",
        started_at: "2026-02-19T00:10:00Z",
        completed_at: "2026-02-19T00:30:00Z",
        duration_ms: 1200000,
        model_selection: {
          model: "sonnet",
          source: "auto",
          confidence: 0.85,
          complexity: "M",
          mode: "automatic",
        },
      },
      "pr-create": {
        status: "complete",
        started_at: "2026-02-19T00:30:00Z",
        completed_at: "2026-02-19T00:35:00Z",
        duration_ms: 300000,
        model_selection: {
          model: "haiku",
          source: "auto",
          confidence: 0.92,
          complexity: "S",
          mode: "automatic",
        },
      },
    },
    tokens: {
      total_input: 50000,
      total_output: 10000,
      total_cache_read: 5000,
      total_cache_creation: 2000,
      estimated_cost_usd: 0.5,
      per_stage: {
        "feature-dev": {
          input: 40000,
          output: 8000,
          cache_read: 4000,
          cache_creation: 1500,
          cost_usd: 0.4,
        },
        "pr-create": {
          input: 10000,
          output: 2000,
          cache_read: 1000,
          cache_creation: 500,
          cost_usd: 0.1,
        },
      },
    },
    recorded_at: "2026-02-19T01:00:00Z",
    ...overrides,
  } as ExecutionHistoryRunRecord;
}

describe("PostPipelineAnalyzer", () => {
  const workspaceRoot = "/test/workspace";
  let logger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = createMockLogger();

    // Default: fs.mkdir succeeds
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    // Default: fs.writeFile succeeds
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    // Default: fs.readdir returns empty
    vi.mocked(fs.readdir).mockResolvedValue([] as any);
    // Default: config file doesn't exist
    vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));
    // Default: getCostByIssue returns empty array (non-critical)
    mockGetCostByIssue.mockResolvedValue([]);
    // Default: exec fails immediately so getRecentSkillChanges returns [] without hanging
    mockExec.mockImplementation(
      (_cmd: unknown, _opts: unknown, cb: (...args: unknown[]) => void) => {
        cb(new Error("git not available in tests"), "", "");
      }
    );
    // Default: skill effectiveness analyze returns empty
    mockSkillEffectivenessAnalyze.mockReturnValue({
      analyzedAt: new Date().toISOString(),
      skillChangesFound: 0,
      entries: [],
    });
    // Default: gate metrics returns empty
    mockGateMetricsReadAll.mockResolvedValue([]);
  });

  // =========================================================================
  // Record Adapter Tests
  // =========================================================================

  describe("adaptRecords", () => {
    it("flattens run records into per-stage SDK records", () => {
      const runRecord = createRunRecord();
      const result = PostPipelineAnalyzer.adaptRecords([runRecord]);

      expect(result).toHaveLength(2);

      // feature-dev stage
      const featureDev = result.find((r) => r.stage === "feature-dev");
      expect(featureDev).toBeDefined();
      expect(featureDev!.issueNumber).toBe(100);
      expect(featureDev!.model).toBe("sonnet");
      expect(featureDev!.success).toBe(true);
      expect(featureDev!.inputTokens).toBe(40000);
      expect(featureDev!.outputTokens).toBe(8000);
      expect(featureDev!.costUsd).toBe(0.4);
      expect(featureDev!.durationMs).toBe(1200000);
      expect(featureDev!.selectionSource).toBe("auto");
      expect(featureDev!.autoSelectorConfidence).toBe(0.85);
      expect(featureDev!.autoSelectorComplexity).toBe("M");

      // pr-create stage
      const prCreate = result.find((r) => r.stage === "pr-create");
      expect(prCreate).toBeDefined();
      expect(prCreate!.model).toBe("haiku");
      expect(prCreate!.costUsd).toBe(0.1);
    });

    it("skips outcome records", () => {
      const outcomeRecord = {
        record_type: "outcome",
        issue_number: 100,
        pr_number: 42,
        outcome: "merged",
        recorded_at: "2026-02-19T01:00:00Z",
      };
      const result = PostPipelineAnalyzer.adaptRecords([outcomeRecord]);
      expect(result).toHaveLength(0);
    });

    it("skips stages without model_selection", () => {
      const record = createRunRecord({
        stages: {
          "feature-dev": {
            status: "complete",
            // No model_selection
          },
        },
      });
      const result = PostPipelineAnalyzer.adaptRecords([record]);
      expect(result).toHaveLength(0);
    });

    it("handles failed stages correctly", () => {
      const record = createRunRecord({
        stages: {
          "feature-dev": {
            status: "failed",
            duration_ms: 500000,
            model_selection: {
              model: "opus",
              source: "config",
            },
          },
        },
      });
      const result = PostPipelineAnalyzer.adaptRecords([record]);

      expect(result).toHaveLength(1);
      expect(result[0].success).toBe(false);
      expect(result[0].model).toBe("opus");
      expect(result[0].selectionSource).toBe("config");
    });

    it("sums auto + manual retry counts", () => {
      const record = createRunRecord({
        stages: {
          "feature-dev": {
            status: "complete",
            auto_retry_count: 2,
            manual_retry_count: 1,
            model_selection: {
              model: "sonnet",
              source: "auto",
            },
          },
        },
      });
      const result = PostPipelineAnalyzer.adaptRecords([record]);
      expect(result[0].retries).toBe(3);
    });

    it("uses zero tokens when per_stage data is missing", () => {
      const record = createRunRecord();
      // Remove per_stage tokens
      (record.tokens as any).per_stage = undefined;
      const result = PostPipelineAnalyzer.adaptRecords([record]);

      expect(result[0].inputTokens).toBe(0);
      expect(result[0].outputTokens).toBe(0);
      expect(result[0].costUsd).toBe(0);
    });
  });

  // =========================================================================
  // Analysis Execution Tests
  // =========================================================================

  describe("analyze", () => {
    it("returns null when no execution history", async () => {
      mockReadAll.mockResolvedValue([]);

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 943, logger as any);

      expect(result).toBeNull();
      expect(logger.info).toHaveBeenCalledWith(
        expect.stringContaining("no execution history"),
        expect.any(Object)
      );
    });

    it("returns null when no records have model selection data", async () => {
      // Return run records with no model_selection
      mockReadAll.mockResolvedValue([
        {
          record_type: "run",
          stages: { "feature-dev": { status: "complete" } },
          tokens: { per_stage: {} },
          started_at: "2026-02-19T00:00:00Z",
          issue_number: 100,
        },
      ]);

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 943, logger as any);
      expect(result).toBeNull();
    });

    it("runs analysis and returns result on success", async () => {
      mockReadAll.mockResolvedValue([createRunRecord()]);
      mockAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        recordsAnalyzed: 2,
        stageComparisons: [],
        recommendations: [{ type: "downgrade", estimatedSavingsUsd: 0.01 }],
        summary: {
          totalPotentialSavingsUsd: 0.01,
          stagesWithSufficientData: 1,
          stagesNeedingMoreData: [],
          overallRecommendation: "1 optimization identified.",
        },
        autoSelectionAnalysis: {
          costSavingsVsStaticUsd: 0.005,
          thresholdRecommendations: [],
          totalAutoSelectedRecords: 2,
          overallAutoSuccessRate: 1.0,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
        },
      });

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 943, logger as any);

      expect(result).not.toBeNull();
      expect(result!.recommendationCount).toBe(1);
      expect(result!.totalPotentialSavingsUsd).toBe(0.01);
      expect(result!.costSavingsVsStaticUsd).toBe(0.005);
      expect(result!.overallRecommendation).toBe("1 optimization identified.");
      expect(result!.analysisFile).toContain("analysis-");
    });

    it("catches errors and returns null (non-critical)", async () => {
      mockReadAll.mockRejectedValue(new Error("disk failure"));

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 943, logger as any);

      expect(result).toBeNull();
      expect(logger.warn).toHaveBeenCalledWith(
        "Post-pipeline analysis failed",
        expect.objectContaining({ issueNumber: 943 })
      );
    });

    it("stores analysis to timestamped file and latest.json", async () => {
      mockReadAll.mockResolvedValue([createRunRecord()]);
      mockAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        recordsAnalyzed: 2,
        stageComparisons: [],
        recommendations: [],
        summary: {
          totalPotentialSavingsUsd: 0,
          stagesWithSufficientData: 0,
          stagesNeedingMoreData: [],
          overallRecommendation: "Optimal.",
        },
      });

      await PostPipelineAnalyzer.analyze(workspaceRoot, 943, logger as any);

      // Should create directory
      expect(fs.mkdir).toHaveBeenCalledWith(path.join(workspaceRoot, ".nightgauge/analysis"), {
        recursive: true,
      });

      // Should write timestamped file and latest.json
      const writeCalls = vi.mocked(fs.writeFile).mock.calls;
      const paths = writeCalls.map((c) => c[0] as string);
      expect(paths.some((p) => p.includes("analysis-"))).toBe(true);
      expect(paths.some((p) => p.includes("latest.json"))).toBe(true);
    });
  });

  // =========================================================================
  // Retention Tests
  // =========================================================================

  describe("retention enforcement", () => {
    it("deletes oldest files when exceeding MAX_ANALYSIS_FILES", async () => {
      // Generate 22 files (2 over the limit of 20)
      const files = Array.from({ length: 22 }, (_, i) => {
        const n = String(i).padStart(2, "0");
        return `analysis-2026-02-${n}T12-00-00-000Z.json`;
      });
      files.push("latest.json"); // Should be excluded from count

      mockReadAll.mockResolvedValue([createRunRecord()]);
      mockAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        recordsAnalyzed: 2,
        stageComparisons: [],
        recommendations: [],
        summary: {
          totalPotentialSavingsUsd: 0,
          stagesWithSufficientData: 0,
          stagesNeedingMoreData: [],
          overallRecommendation: "Optimal.",
        },
      });

      vi.mocked(fs.readdir).mockResolvedValue(files as any);
      vi.mocked(fs.unlink).mockResolvedValue(undefined);

      await PostPipelineAnalyzer.analyze(workspaceRoot, 943, logger as any);

      // Should delete the 2 oldest timestamped files
      expect(fs.unlink).toHaveBeenCalledTimes(2);
    });
  });

  // =========================================================================
  // formatSelfCheck
  // =========================================================================

  describe("formatSelfCheck", () => {
    it("produces formatted output with all sections", () => {
      const analysisResult: PostPipelineAnalysisResult = {
        analysisFile: "/test/analysis.json",
        recommendationCount: 0,
        totalPotentialSavingsUsd: 0,
        costSavingsVsStaticUsd: 0,
        overallRecommendation: "Optimal.",
        failurePatterns: null,
        costPerIssue: null,
        gateEffectiveness: null,
        skillEffectiveness: null,
        workflowCalibration: null,
        calibrationUpdated: false,
      };

      const healthEval = {
        score: 85,
        status: "good",
        trend: "stable" as const,
        actions: [],
      };

      const output = PostPipelineAnalyzer.formatSelfCheck(analysisResult, healthEval, 0.42, 0.4);

      expect(output).toContain("Pipeline Self-Check");
      expect(output).toContain("Health: 85");
      expect(output).toContain("Good");
      expect(output).toContain("Model routing: optimal");
      expect(output).toContain("No anomalies detected");
    });

    it("shows cost spike anomaly when cost > 2x average", () => {
      const output = PostPipelineAnalyzer.formatSelfCheck(null, null, 2.5, 1.0);

      expect(output).toContain("cost spike");
    });

    it("shows critical health anomaly", () => {
      const healthEval = {
        score: 40,
        status: "poor",
        trend: "declining" as const,
        actions: [],
      };

      const output = PostPipelineAnalyzer.formatSelfCheck(null, healthEval, 0.5, 0.5);

      expect(output).toContain("critical health");
    });

    it("shows failure pattern count", () => {
      const analysisResult: PostPipelineAnalysisResult = {
        analysisFile: "/test/analysis.json",
        recommendationCount: 2,
        totalPotentialSavingsUsd: 0.05,
        costSavingsVsStaticUsd: 0,
        overallRecommendation: "Some optimizations.",
        failurePatterns: {
          totalFailures: 3,
          topFindings: [
            {
              category: "build-failure",
              occurrenceCount: 2,
              trend: "worsening",
              recommendation: "Fix build",
            },
            {
              category: "test-failure",
              occurrenceCount: 1,
              trend: "stable",
              recommendation: "Fix tests",
            },
          ],
          overallTrend: "worsening",
        },
        costPerIssue: null,
        gateEffectiveness: null,
        skillEffectiveness: null,
        workflowCalibration: null,
        calibrationUpdated: false,
      };

      const output = PostPipelineAnalyzer.formatSelfCheck(analysisResult, null, 0.5, 0.5);

      expect(output).toContain("Failure patterns: 2 detected");
      expect(output).toContain("has recommendations");
    });

    it("handles null inputs gracefully", () => {
      const output = PostPipelineAnalyzer.formatSelfCheck(null, null, 0, 0);

      expect(output).toContain("Pipeline Self-Check");
      expect(output).toContain("Health: N/A");
      expect(output).toContain("Model routing: N/A");
    });

    it("shows gate hit-rates when gate effectiveness data is present", () => {
      const analysisResult: PostPipelineAnalysisResult = {
        analysisFile: "/test/analysis.json",
        recommendationCount: 0,
        totalPotentialSavingsUsd: 0,
        costSavingsVsStaticUsd: 0,
        overallRecommendation: "Optimal.",
        failurePatterns: null,
        costPerIssue: null,
        gateEffectiveness: {
          totalInvocations: 10,
          byGate: [
            {
              gateName: "lint-gate",
              invocations: 5,
              catches: 2,
              passes: 3,
              hitRate: 0.4,
            },
          ],
        },
        skillEffectiveness: null,
        workflowCalibration: null,
        calibrationUpdated: false,
      };

      const output = PostPipelineAnalyzer.formatSelfCheck(analysisResult, null, 0.5, 0.5);

      expect(output).toContain("Gates:");
      expect(output).toContain("lint-gate");
      expect(output).toContain("40%");
    });
  });

  // =========================================================================
  // costPerIssue aggregation (Issue #1410)
  // =========================================================================

  describe("analyze costPerIssue (Issue #1410)", () => {
    function setupSuccessfulAnalyze() {
      mockReadAll.mockResolvedValue([createRunRecord()]);
      mockAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        recordsAnalyzed: 1,
        stageComparisons: [],
        recommendations: [],
        summary: {
          totalPotentialSavingsUsd: 0,
          stagesWithSufficientData: 0,
          stagesNeedingMoreData: [],
          overallRecommendation: "Optimal.",
        },
        autoSelectionAnalysis: null,
      });
    }

    it("analyze() result includes costPerIssue when getCostByIssue returns data", async () => {
      setupSuccessfulAnalyze();
      mockGetCostByIssue.mockResolvedValue([
        {
          issueNumber: 100,
          totalCostUsd: 0.35,
          runCount: 2,
          backtrackCount: 1,
          issueType: "feature",
          sizeLabel: "M",
          firstRunAt: new Date("2026-02-19T00:00:00Z"),
          lastRunAt: new Date("2026-02-19T01:00:00Z"),
        },
      ]);

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.costPerIssue).not.toBeNull();
      expect(result!.costPerIssue).toHaveLength(1);
      expect(result!.costPerIssue![0].issueNumber).toBe(100);
      expect(result!.costPerIssue![0].totalCostUsd).toBeCloseTo(0.35);
    });

    it("analyze() result has costPerIssue as [] when getCostByIssue returns empty", async () => {
      setupSuccessfulAnalyze();
      mockGetCostByIssue.mockResolvedValue([]);

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.costPerIssue).toEqual([]);
    });

    it("analyze() sets costPerIssue to null and logs warning when getCostByIssue throws", async () => {
      setupSuccessfulAnalyze();
      mockGetCostByIssue.mockRejectedValue(new Error("aggregation error"));

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.costPerIssue).toBeNull();
      expect(logger.warn).toHaveBeenCalledWith(
        "Cost-per-issue aggregation failed (non-critical)",
        expect.objectContaining({ err: "aggregation error" })
      );
    });
  });

  // =========================================================================
  // Gate Effectiveness Tests (Issue #1412)
  // =========================================================================

  describe("gate effectiveness (Issue #1412)", () => {
    function setupSuccessfulAnalyze() {
      mockReadAll.mockResolvedValue([createRunRecord()]);
      mockAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        recordsAnalyzed: 1,
        stageComparisons: [],
        recommendations: [],
        summary: {
          totalPotentialSavingsUsd: 0,
          stagesWithSufficientData: 0,
          stagesNeedingMoreData: [],
          overallRecommendation: "Optimal.",
        },
        autoSelectionAnalysis: null,
      });
    }

    it("analyze() includes gateEffectiveness when gate metrics exist", async () => {
      setupSuccessfulAnalyze();
      mockGateMetricsReadAll.mockResolvedValue([
        { gate_name: "lint-gate", result: "pass" },
        { gate_name: "lint-gate", result: "catch" },
        { gate_name: "test-gate", result: "pass" },
      ]);

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.gateEffectiveness).not.toBeNull();
      expect(result!.gateEffectiveness!.totalInvocations).toBe(3);
      expect(result!.gateEffectiveness!.byGate).toHaveLength(2);

      const lintGate = result!.gateEffectiveness!.byGate.find((g) => g.gateName === "lint-gate");
      expect(lintGate).toBeDefined();
      expect(lintGate!.invocations).toBe(2);
      expect(lintGate!.catches).toBe(1);
      expect(lintGate!.passes).toBe(1);
      expect(lintGate!.hitRate).toBe(0.5);
    });

    it("analyze() sets gateEffectiveness to null when no gate metrics exist", async () => {
      setupSuccessfulAnalyze();
      mockGateMetricsReadAll.mockResolvedValue([]);

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.gateEffectiveness).toBeNull();
    });
  });

  // =========================================================================
  // Skill Effectiveness Tracking Tests (Issue #1414)
  // =========================================================================

  describe("skill effectiveness tracking", () => {
    function setupSuccessfulAnalyzeForSkills() {
      mockReadAll.mockResolvedValue([createRunRecord()]);
      mockAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        recordsAnalyzed: 1,
        stageComparisons: [],
        recommendations: [],
        summary: {
          totalPotentialSavingsUsd: 0,
          stagesWithSufficientData: 0,
          stagesNeedingMoreData: [],
          overallRecommendation: "Optimal.",
        },
        autoSelectionAnalysis: null,
      });
      mockGetCostByIssue.mockResolvedValue([]);
    }

    /**
     * Make mockExec call the callback with a git log stdout that includes
     * one SKILL.md change for the given stage.
     */
    const FAKE_COMMIT_HASH = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2";

    function mockGitLogWithChange(stage: string) {
      mockExec.mockImplementation(
        (_cmd: string, _opts: unknown, cb: (...args: unknown[]) => void) => {
          const stdout = [
            `COMMIT ${FAKE_COMMIT_HASH} 2026-01-15T12:00:00Z`,
            `skills/nightgauge-${stage}/SKILL.md`,
            "",
          ].join("\n");
          cb(null, stdout, "");
        }
      );
    }

    it("analyze() result has skillEffectiveness as null when exec fails (non-critical)", async () => {
      setupSuccessfulAnalyzeForSkills();
      // mockExec already set to fail in beforeEach

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.skillEffectiveness).toBeNull();
    });

    it("analyze() result has skillEffectiveness as null when no skill changes found", async () => {
      setupSuccessfulAnalyzeForSkills();
      // Exec returns empty stdout
      mockExec.mockImplementation(
        (_cmd: string, _opts: unknown, cb: (...args: unknown[]) => void) => {
          cb(null, "", "");
        }
      );

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.skillEffectiveness).toBeNull();
    });

    it("analyze() populates skillEffectiveness when skill changes are found with entries", async () => {
      setupSuccessfulAnalyzeForSkills();
      mockGitLogWithChange("feature-planning");
      mockSkillEffectivenessAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        skillChangesFound: 1,
        entries: [
          {
            skillFile: "skills/nightgauge-feature-planning/SKILL.md",
            stage: "feature-planning",
            commitHash: FAKE_COMMIT_HASH,
            changedAt: "2026-01-15T12:00:00Z",
            beforeWindow: { sampleCount: 10, successRate: 0.6 },
            afterWindow: { sampleCount: 10, successRate: 0.85 },
            delta: 0.25,
            classification: "effective" as const,
            confidence: "low" as const,
            analyzedAt: "2026-02-19T01:00:00Z",
          },
        ],
      });

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(result).not.toBeNull();
      expect(result!.skillEffectiveness).not.toBeNull();
      expect(result!.skillEffectiveness!.skillChangesFound).toBe(1);
      expect(result!.skillEffectiveness!.entries).toHaveLength(1);
      expect(result!.skillEffectiveness!.entries[0].classification).toBe("effective");
    });

    it("analyze() persists entries to SkillEffectivenessWriter when entries exist", async () => {
      setupSuccessfulAnalyzeForSkills();
      mockGitLogWithChange("feature-planning");
      mockSkillEffectivenessAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        skillChangesFound: 1,
        entries: [
          {
            skillFile: "skills/nightgauge-feature-planning/SKILL.md",
            stage: "feature-planning",
            commitHash: FAKE_COMMIT_HASH,
            changedAt: "2026-01-15T12:00:00Z",
            beforeWindow: { sampleCount: 10, successRate: 0.6 },
            afterWindow: { sampleCount: 10, successRate: 0.85 },
            delta: 0.25,
            classification: "effective" as const,
            confidence: "low" as const,
            analyzedAt: "2026-02-19T01:00:00Z",
          },
        ],
      });

      await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      expect(mockSkillEffectivenessAppend).toHaveBeenCalledOnce();
      expect(mockSkillEffectivenessAppend).toHaveBeenCalledWith(
        workspaceRoot,
        expect.objectContaining({
          schema_version: "1",
          skill_file: "skills/nightgauge-feature-planning/SKILL.md",
          classification: "effective",
        })
      );
    });

    it("analyze() does not throw when SkillEffectivenessWriter throws (non-critical)", async () => {
      setupSuccessfulAnalyzeForSkills();
      mockGitLogWithChange("feature-planning");
      mockSkillEffectivenessAnalyze.mockReturnValue({
        analyzedAt: "2026-02-19T01:00:00Z",
        skillChangesFound: 1,
        entries: [
          {
            skillFile: "skills/nightgauge-feature-planning/SKILL.md",
            stage: "feature-planning",
            commitHash: FAKE_COMMIT_HASH,
            changedAt: "2026-01-15T12:00:00Z",
            beforeWindow: { sampleCount: 10, successRate: 0.6 },
            afterWindow: { sampleCount: 10, successRate: 0.85 },
            delta: 0.25,
            classification: "effective" as const,
            confidence: "low" as const,
            analyzedAt: "2026-02-19T01:00:00Z",
          },
        ],
      });
      // Make the writer throw
      mockSkillEffectivenessAppend.mockRejectedValue(new Error("disk error"));

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

      // Analysis should still succeed despite writer failure
      expect(result).not.toBeNull();
      expect(logger.warn).toHaveBeenCalledWith(
        "Skill effectiveness tracking failed (non-critical)",
        expect.objectContaining({ issueNumber: 100 })
      );
    });

    describe("formatSelfCheck() skill effectiveness display", () => {
      function makeBaseResult(
        overrides: Partial<PostPipelineAnalysisResult> = {}
      ): PostPipelineAnalysisResult {
        return {
          analysisFile: "/analysis.json",
          recommendationCount: 0,
          totalPotentialSavingsUsd: 0,
          costSavingsVsStaticUsd: 0,
          overallRecommendation: "Optimal.",
          failurePatterns: null,
          costPerIssue: null,
          gateEffectiveness: null,
          skillEffectiveness: null,
          workflowCalibration: null,
          calibrationUpdated: false,
          ...overrides,
        };
      }

      it("includes regression warning in formatSelfCheck output", () => {
        const result = makeBaseResult({
          skillEffectiveness: {
            skillChangesFound: 1,
            entries: [
              {
                skillFile: "skills/nightgauge-feature-dev/SKILL.md",
                stage: "feature-dev",
                commitHash: FAKE_COMMIT_HASH,
                changedAt: "2026-01-15T12:00:00Z",
                beforeWindow: { sampleCount: 10, successRate: 0.9 },
                afterWindow: { sampleCount: 10, successRate: 0.6 },
                delta: -0.3,
                classification: "regression",
                confidence: "low",
              },
            ],
          },
        });

        const output = PostPipelineAnalyzer.formatSelfCheck(result, null, 0.5, 0);

        expect(output).toContain("Skill regression detected");
        expect(output).toContain("skills/nightgauge-feature-dev/SKILL.md");
        expect(output).toContain("-30% delta");
        expect(output).toContain("[low]");
      });

      it("includes effective skill note in formatSelfCheck output", () => {
        const result = makeBaseResult({
          skillEffectiveness: {
            skillChangesFound: 1,
            entries: [
              {
                skillFile: "skills/nightgauge-feature-planning/SKILL.md",
                stage: "feature-planning",
                commitHash: FAKE_COMMIT_HASH,
                changedAt: "2026-01-15T12:00:00Z",
                beforeWindow: { sampleCount: 10, successRate: 0.6 },
                afterWindow: { sampleCount: 10, successRate: 0.85 },
                delta: 0.25,
                classification: "effective",
                confidence: "moderate",
              },
            ],
          },
        });

        const output = PostPipelineAnalyzer.formatSelfCheck(result, null, 0.5, 0);

        expect(output).toContain("Effective skill edits");
        expect(output).toContain("skills/nightgauge-feature-planning/SKILL.md");
        expect(output).toContain("+25% delta");
        expect(output).toContain("[moderate]");
      });

      it("does not show skill effectiveness section when skillEffectiveness is null", () => {
        const result = makeBaseResult({ skillEffectiveness: null });
        const output = PostPipelineAnalyzer.formatSelfCheck(result, null, 0.5, 0);
        expect(output).not.toContain("Skill regression");
        expect(output).not.toContain("Effective skill edits");
      });

      it("does not show skill effectiveness when all entries are neutral/insufficient", () => {
        const result = makeBaseResult({
          skillEffectiveness: {
            skillChangesFound: 2,
            entries: [
              {
                skillFile: "skills/nightgauge-pr-create/SKILL.md",
                stage: "pr-create",
                commitHash: "abc",
                changedAt: "2026-01-15T12:00:00Z",
                beforeWindow: { sampleCount: 2, successRate: 0 },
                afterWindow: { sampleCount: 2, successRate: 0 },
                delta: 0,
                classification: "insufficient_data",
                confidence: "insufficient_data",
              },
              {
                skillFile: "skills/nightgauge-pr-merge/SKILL.md",
                stage: "pr-merge",
                commitHash: "def",
                changedAt: "2026-01-16T12:00:00Z",
                beforeWindow: { sampleCount: 8, successRate: 0.75 },
                afterWindow: { sampleCount: 8, successRate: 0.75 },
                delta: 0,
                classification: "neutral",
                confidence: "low",
              },
            ],
          },
        });

        const output = PostPipelineAnalyzer.formatSelfCheck(result, null, 0.5, 0);
        expect(output).not.toContain("Skill regression");
        expect(output).not.toContain("Effective skill edits");
      });
    });
  });

  // =========================================================================
  // computeStageExecutionStats (Issue #1573)
  // =========================================================================

  describe("computeStageExecutionStats (Issue #1573)", () => {
    it("should compute retry stats from run records", () => {
      const records: ExecutionHistoryRunRecord[] = [
        createRunRecord({
          stages: {
            "feature-dev": {
              status: "complete",
              duration_ms: 120_000,
              auto_retry_count: 2,
              manual_retry_count: 0,
            },
            "pr-create": {
              status: "complete",
              duration_ms: 30_000,
              auto_retry_count: 0,
              manual_retry_count: 0,
            },
          },
        }),
        createRunRecord({
          stages: {
            "feature-dev": {
              status: "complete",
              duration_ms: 100_000,
              auto_retry_count: 0,
              manual_retry_count: 0,
            },
            "pr-create": {
              status: "complete",
              duration_ms: 25_000,
              auto_retry_count: 1,
              manual_retry_count: 0,
            },
          },
        }),
      ];

      const stats = PostPipelineAnalyzer.computeStageExecutionStats(records);

      const devRetry = stats.retryStats.find((s) => s.stage === "feature-dev");
      expect(devRetry).toBeDefined();
      expect(devRetry!.totalRuns).toBe(2);
      expect(devRetry!.runsWithRetries).toBe(1);
      expect(devRetry!.retryRate).toBe(0.5);
      expect(devRetry!.totalRetryCount).toBe(2);

      const prRetry = stats.retryStats.find((s) => s.stage === "pr-create");
      expect(prRetry).toBeDefined();
      expect(prRetry!.totalRuns).toBe(2);
      expect(prRetry!.runsWithRetries).toBe(1);
      expect(prRetry!.retryRate).toBe(0.5);
    });

    it("should compute duration stats with P95, max, median", () => {
      // Create 10 records with known durations for feature-dev
      const records: ExecutionHistoryRunRecord[] = Array.from({ length: 10 }, (_, i) =>
        createRunRecord({
          stages: {
            "feature-dev": {
              status: "complete",
              duration_ms: (i + 1) * 10_000, // 10k, 20k, ..., 100k
            },
          },
        })
      );

      const stats = PostPipelineAnalyzer.computeStageExecutionStats(records);

      const devDuration = stats.durationStats.find((s) => s.stage === "feature-dev");
      expect(devDuration).toBeDefined();
      expect(devDuration!.totalRuns).toBe(10);
      // P95 index = ceil(0.95 * 10) - 1 = 9 → sorted[9] = 100000
      expect(devDuration!.p95DurationMs).toBe(100_000);
      expect(devDuration!.maxDurationMs).toBe(100_000);
      // Median index = floor(10/2) = 5 → sorted[5] = 60000
      expect(devDuration!.medianDurationMs).toBe(60_000);
    });

    it("should skip stages with non-complete/failed status", () => {
      const records: ExecutionHistoryRunRecord[] = [
        createRunRecord({
          stages: {
            "feature-dev": {
              status: "skipped",
              duration_ms: 0,
            },
          },
        }),
      ];

      const stats = PostPipelineAnalyzer.computeStageExecutionStats(records);

      expect(stats.retryStats).toHaveLength(0);
      expect(stats.durationStats).toHaveLength(0);
    });

    it("should return empty stats for no records", () => {
      const stats = PostPipelineAnalyzer.computeStageExecutionStats([]);

      expect(stats.retryStats).toHaveLength(0);
      expect(stats.durationStats).toHaveLength(0);
    });
  });

  // =========================================================================
  // V4 workflow journal fold (Issue #3915)
  // =========================================================================

  describe("readWorkflowJournals + analyze workflow calibration", () => {
    /** Build a V4 WorkflowEvent JSONL journal: 2 agents (1 fail), 2 judges (1 fail). */
    function workflowJournalLines(runId: string): string {
      const ts = "2026-06-03T00:00:00.000Z";
      const runNodeId = `run:${runId}`;
      const phaseNodeId = `phase:${runId}:0`;
      const u = (over: Record<string, unknown> = {}) => ({
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.01,
        estimated: false,
        ...over,
      });
      const nodes = [
        {
          schemaVersion: 4,
          kind: "run",
          nodeId: runNodeId,
          parentId: null,
          seq: 0,
          ts,
          status: "running",
          runId,
          issueNumber: 3915,
          stage: "feature-dev",
          backend: "sdk-fanout",
          startedAt: ts,
        },
        {
          schemaVersion: 4,
          kind: "phase",
          nodeId: phaseNodeId,
          parentId: runNodeId,
          seq: 1,
          ts,
          status: "running",
          name: "find",
          index: 0,
          total: 1,
        },
        {
          schemaVersion: 4,
          kind: "agent",
          nodeId: `agent:${runId}:0:0`,
          parentId: phaseNodeId,
          seq: 2,
          ts,
          status: "succeeded",
          agentId: "a0",
          provider: "claude",
          usage: u({ inputTokens: 200, costUsd: 0.02 }),
          terminalKind: "success",
        },
        {
          schemaVersion: 4,
          kind: "agent",
          nodeId: `agent:${runId}:0:1`,
          parentId: phaseNodeId,
          seq: 3,
          ts,
          status: "failed",
          agentId: "a1",
          provider: "codex",
          usage: u({ costUsd: 0.005, estimated: true }),
          terminalKind: "error",
        },
        {
          schemaVersion: 4,
          kind: "judge",
          nodeId: `judge:${runId}:0:0`,
          parentId: phaseNodeId,
          seq: 4,
          ts,
          status: "succeeded",
          judgeId: "j0",
          provider: "claude",
          target: `agent:${runId}:0:0`,
          verdict: "pass",
          usage: u({ costUsd: 0.001 }),
        },
        {
          schemaVersion: 4,
          kind: "judge",
          nodeId: `judge:${runId}:0:1`,
          parentId: phaseNodeId,
          seq: 5,
          ts,
          status: "succeeded",
          judgeId: "j1",
          provider: "claude",
          target: `agent:${runId}:0:0`,
          verdict: "fail",
          usage: u({ costUsd: 0.001 }),
        },
        {
          schemaVersion: 4,
          kind: "run",
          nodeId: runNodeId,
          parentId: null,
          seq: 6,
          ts,
          status: "failed",
          runId,
          issueNumber: 3915,
          stage: "feature-dev",
          backend: "sdk-fanout",
          startedAt: ts,
          finishedAt: ts,
        },
      ];
      return nodes.map((n) => JSON.stringify(n)).join("\n");
    }

    it("reads workflow-*.jsonl journals and folds the V4 nested agents[]/judgeVerdict tree", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([
        "workflow-r1.jsonl",
        "other-file.jsonl",
        "not-a-journal.txt",
      ] as any);
      vi.mocked(fs.readFile).mockImplementation(async (p: any) => {
        if (String(p).endsWith("workflow-r1.jsonl")) return workflowJournalLines("r1");
        throw new Error("ENOENT");
      });

      const events = await PostPipelineAnalyzer.readWorkflowJournals(workspaceRoot);
      // 7 node emissions from the single workflow journal (txt + non-workflow skipped).
      expect(events).toHaveLength(7);
      expect(events.filter((e) => e.kind === "agent")).toHaveLength(2);
      expect(events.filter((e) => e.kind === "judge")).toHaveLength(2);
    });

    it("surfaces workflowCalibration in analyze() with summed usage + judge/fan-out rates", async () => {
      mockReadAll.mockResolvedValue([]); // no execution history → analyze returns early UNLESS we provide one
      // Provide a minimal run record so analyze proceeds to the workflow fold.
      mockReadAll.mockResolvedValue([createRunRecord()]);
      mockAnalyze.mockReturnValue({
        recommendations: [],
        summary: { totalPotentialSavingsUsd: 0, overallRecommendation: "Optimal." },
        autoSelectionAnalysis: null,
      });
      mockFailureAnalyze.mockReturnValue({
        totalFailures: 0,
        findings: [],
        summary: { overallTrend: "stable" },
      });

      vi.mocked(fs.readdir).mockImplementation(async (p: any) => {
        if (String(p).endsWith(".nightgauge/pipeline")) {
          return ["workflow-r1.jsonl"] as any;
        }
        return [] as any;
      });
      vi.mocked(fs.readFile).mockImplementation(async (p: any) => {
        if (String(p).endsWith("workflow-r1.jsonl")) return workflowJournalLines("r1");
        throw new Error("ENOENT");
      });

      const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 3915, logger as any);
      expect(result).not.toBeNull();
      const wf = result!.workflowCalibration;
      expect(wf).not.toBeNull();
      expect(wf!.runCount).toBe(1);
      expect(wf!.totalAgents).toBe(2);
      expect(wf!.totalJudges).toBe(2);
      // 1 of 2 judges failed → mean rejection 0.5; 1 of 2 agents succeeded → eff 0.5.
      expect(wf!.meanJudgeRejectionRate).toBeCloseTo(0.5, 10);
      expect(wf!.meanFanoutEfficiency).toBeCloseTo(0.5, 10);
      // sdk-fanout backend only → no native runs → cost delta null.
      expect(wf!.nativeVsFanoutCostDeltaUsd).toBeNull();
      expect(wf!.fanoutRunCount).toBe(1);
    });

    it("returns empty when the pipeline directory has no workflow journals", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      const events = await PostPipelineAnalyzer.readWorkflowJournals(workspaceRoot);
      expect(events).toEqual([]);
    });

    it("skips malformed JSONL lines without throwing", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["workflow-bad.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        `${workflowJournalLines("r1")}\n{not valid json\n` as any
      );
      const events = await PostPipelineAnalyzer.readWorkflowJournals(workspaceRoot);
      expect(events).toHaveLength(7); // malformed trailing line skipped
    });
  });
});
