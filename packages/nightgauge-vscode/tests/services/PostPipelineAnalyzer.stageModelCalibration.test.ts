/**
 * PostPipelineAnalyzer.stageModelCalibration.test.ts
 *
 * Unit tests for the per-(stage, model) calibration table persistence block
 * added to PostPipelineAnalyzer.analyze() (Issue #142). Mirrors the existing
 * `CalibrationService` update test setup in PostPipelineAnalyzer.test.ts.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "node:fs/promises";
import type { ExecutionHistoryRunRecord } from "../../src/schemas/executionHistory";

vi.mock("node:fs/promises");

const mockReadAll = vi.fn();
const mockGetCostByIssue = vi.fn();
vi.mock("../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    readAll: (...args: unknown[]) => mockReadAll(...args),
    getCostByIssue: (...args: unknown[]) => mockGetCostByIssue(...args),
  },
}));

const mockAnalyze = vi.fn();
const mockFailureAnalyze = vi.fn();
const mockSkillEffectivenessAnalyze = vi.fn();
// StageModelCalibrationService is mocked explicitly rather than exercised via
// `actual` — its real implementation is resolved through the SDK's compiled
// `dist/` output (a separate module graph from this test's `vi.mock("node:fs/promises")`
// interception), so a real call would hit the actual filesystem instead of the mock.
const mockStageModelBuildFromHistory = vi.fn();
const mockStageModelSave = vi.fn().mockResolvedValue(undefined);
const mockStageModelGetDefaultPath = vi
  .fn()
  .mockReturnValue("/workspace/.nightgauge/pipeline/stage-model-calibration.json");
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
    StageModelCalibrationService: {
      ...actual.StageModelCalibrationService,
      buildFromHistory: (...args: unknown[]) => mockStageModelBuildFromHistory(...args),
      save: (...args: unknown[]) => mockStageModelSave(...args),
      getDefaultPath: (...args: unknown[]) => mockStageModelGetDefaultPath(...args),
    },
  };
});

const mockGateMetricsReadAll = vi.fn().mockResolvedValue([]);
vi.mock("../../src/utils/gateMetricsWriter", () => ({
  GateMetricsWriter: {
    readAll: (...args: unknown[]) => mockGateMetricsReadAll(...args),
  },
}));

const { mockExec, mockExecFile } = vi.hoisted(() => {
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const execMock = vi.fn();
  (execMock as any)[kCustom] = (cmd: string, opts: unknown) =>
    new Promise<{ stdout: string; stderr: string }>((resolve, reject) => {
      execMock(cmd, opts, (err: Error | null, stdout: string, stderr: string) => {
        if (err) reject(Object.assign(err, { stdout, stderr }));
        else resolve({ stdout, stderr });
      });
    });
  const execFileMock = vi.fn();
  (execFileMock as any)[kCustom] = () =>
    Promise.resolve<{ stdout: string; stderr: string }>({ stdout: "", stderr: "" });
  return { mockExec: execMock, mockExecFile: execFileMock };
});
vi.mock("node:child_process", () => ({
  exec: mockExec,
  execFile: mockExecFile,
}));

const mockSkillEffectivenessAppend = vi.fn().mockResolvedValue(undefined);
vi.mock("../../src/utils/SkillEffectivenessWriter", () => ({
  SkillEffectivenessWriter: {
    appendRecord: (...args: unknown[]) => mockSkillEffectivenessAppend(...args),
    readAll: vi.fn().mockResolvedValue([]),
    enforceRetention: vi.fn().mockResolvedValue(undefined),
    getFilePath: vi.fn().mockReturnValue("/workspace/.nightgauge/health/skill-effectiveness.jsonl"),
  },
}));

import { PostPipelineAnalyzer } from "../../src/services/PostPipelineAnalyzer";

function createMockLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

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
    size: "M",
    stages: {
      "feature-dev": {
        status: "complete",
        started_at: "2026-02-19T00:10:00Z",
        completed_at: "2026-02-19T00:30:00Z",
        duration_ms: 1200000,
        model_selection: {
          model: "sonnet",
          source: "scheduler",
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
          source: "scheduler",
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
          model: "sonnet",
        },
        "pr-create": {
          input: 10000,
          output: 2000,
          cache_read: 1000,
          cache_creation: 500,
          cost_usd: 0.1,
          model: "haiku",
        },
      },
    },
    recorded_at: "2026-02-19T01:00:00Z",
    ...overrides,
  } as ExecutionHistoryRunRecord;
}

function mockAnalyzeSuccess() {
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
}

describe("PostPipelineAnalyzer — per-(stage, model) calibration (Issue #142)", () => {
  const workspaceRoot = "/test/workspace";
  let logger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = createMockLogger();

    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    vi.mocked(fs.readdir).mockResolvedValue([] as any);
    vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));
    mockGetCostByIssue.mockResolvedValue([]);
    mockExec.mockImplementation(
      (_cmd: unknown, _opts: unknown, cb: (...args: unknown[]) => void) => {
        cb(new Error("git not available in tests"), "", "");
      }
    );
    mockSkillEffectivenessAnalyze.mockReturnValue({
      analyzedAt: new Date().toISOString(),
      skillChangesFound: 0,
      entries: [],
    });
    mockGateMetricsReadAll.mockResolvedValue([]);
    mockStageModelBuildFromHistory.mockImplementation((records: unknown[]) => ({
      schema_version: "1",
      updated_at: "2026-02-19T01:00:00Z",
      total_records_analyzed: records.length,
      buckets: {},
      __inputRecords: records,
    }));
    mockStageModelSave.mockResolvedValue(undefined);
    mockStageModelGetDefaultPath.mockReturnValue(
      "/workspace/.nightgauge/pipeline/stage-model-calibration.json"
    );
  });

  it("flattens per_stage into one (stage, model) input record per stage and persists the built table", async () => {
    mockReadAll.mockResolvedValue([createRunRecord()]);
    mockAnalyzeSuccess();

    await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

    expect(mockStageModelBuildFromHistory).toHaveBeenCalledTimes(1);
    const inputRecords = mockStageModelBuildFromHistory.mock.calls[0][0] as Array<{
      stage: string;
      model: string;
      cost_usd: number;
      input_tokens: number;
      output_tokens: number;
    }>;

    expect(inputRecords).toHaveLength(2);
    const dev = inputRecords.find((r) => r.stage === "feature-dev")!;
    expect(dev.model).toBe("sonnet");
    expect(dev.cost_usd).toBe(0.4);
    // input_tokens includes raw input + cache_read + cache_creation
    expect(dev.input_tokens).toBe(40000 + 4000 + 1500);
    expect(dev.output_tokens).toBe(8000);

    const prCreate = inputRecords.find((r) => r.stage === "pr-create")!;
    expect(prCreate.model).toBe("haiku");

    expect(mockStageModelSave).toHaveBeenCalledTimes(1);
    expect(mockStageModelSave).toHaveBeenCalledWith(
      "/workspace/.nightgauge/pipeline/stage-model-calibration.json",
      expect.objectContaining({ schema_version: "1" })
    );
  });

  it("skips per_stage entries without a model field", async () => {
    const record = createRunRecord();
    delete (record.tokens.per_stage!["pr-create"] as any).model;
    mockReadAll.mockResolvedValue([record]);
    mockAnalyzeSuccess();

    await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

    const inputRecords = mockStageModelBuildFromHistory.mock.calls[0][0] as Array<{
      stage: string;
    }>;
    expect(inputRecords).toHaveLength(1);
    expect(inputRecords[0].stage).toBe("feature-dev");
  });

  it("does not build or save a table when no run has per_stage model data", async () => {
    const record = createRunRecord();
    delete (record.tokens.per_stage!["feature-dev"] as any).model;
    delete (record.tokens.per_stage!["pr-create"] as any).model;
    mockReadAll.mockResolvedValue([record]);
    mockAnalyzeSuccess();

    await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

    expect(mockStageModelBuildFromHistory).not.toHaveBeenCalled();
    expect(mockStageModelSave).not.toHaveBeenCalled();
  });

  it("does not throw analyze() when the calibration save fails (non-critical)", async () => {
    mockReadAll.mockResolvedValue([createRunRecord()]);
    mockAnalyzeSuccess();
    mockStageModelSave.mockRejectedValue(new Error("disk full"));

    const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

    expect(result).not.toBeNull();
    expect(logger.debug).toHaveBeenCalledWith(
      "Stage-model calibration table update skipped",
      expect.any(Object)
    );
  });

  it("does not throw analyze() when buildFromHistory throws (non-critical)", async () => {
    mockReadAll.mockResolvedValue([createRunRecord()]);
    mockAnalyzeSuccess();
    mockStageModelBuildFromHistory.mockImplementation(() => {
      throw new Error("malformed input");
    });

    const result = await PostPipelineAnalyzer.analyze(workspaceRoot, 100, logger as any);

    expect(result).not.toBeNull();
    expect(mockStageModelSave).not.toHaveBeenCalled();
    expect(logger.debug).toHaveBeenCalledWith(
      "Stage-model calibration table update skipped",
      expect.any(Object)
    );
  });
});
