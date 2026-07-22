/**
 * contextFileSizeMeasurement.test.ts
 *
 * Tests for context handoff file size measurement and threshold alerting.
 *
 * @see Issue #1009 - Track context handoff file sizes
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import { ExecutionHistoryRunRecordV2Schema } from "../../src/schemas/executionHistory";
import { validatePipelineState } from "../../src/schemas/pipelineState";
import { PipelineConfigSchema } from "../../src/config/schema";

// Mock vscode
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(),
      onDidCreate: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  RelativePattern: vi.fn(),
  EventEmitter: vi.fn(() => ({
    event: vi.fn(),
    fire: vi.fn(),
    dispose: vi.fn(),
  })),
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// Mock node:fs/promises for appendRecord
vi.mock("node:fs/promises", () => ({
  mkdir: vi.fn().mockResolvedValue(undefined),
  appendFile: vi.fn().mockResolvedValue(undefined),
  readFile: vi.fn().mockRejectedValue({ code: "ENOENT" }),
  writeFile: vi.fn().mockResolvedValue(undefined),
  rename: vi.fn().mockResolvedValue(undefined),
}));

describe("Context File Size Measurement (Issue #1009)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("buildRunRecord includes context_file_size_bytes", () => {
    it("should include context_file_size_bytes when present in stage state", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": {
            status: "complete",
            context_file_size_bytes: 4096,
          },
          "feature-planning": {
            status: "complete",
            context_file_size_bytes: 8192,
          },
          "feature-dev": {
            status: "complete",
            context_file_size_bytes: 2048,
          },
          "feature-validate": { status: "complete" },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
          "pipeline-finish": { status: "complete" },
        },
      });

      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.stages["issue-pickup"]?.context_file_size_bytes).toBe(4096);
      expect(record.stages["feature-planning"]?.context_file_size_bytes).toBe(8192);
      expect(record.stages["feature-dev"]?.context_file_size_bytes).toBe(2048);
    });

    it("should omit context_file_size_bytes when not present (backward compat)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      // All stages should not have context_file_size_bytes
      for (const stageDetail of Object.values(record.stages)) {
        expect(stageDetail.context_file_size_bytes).toBeUndefined();
      }
    });
  });

  describe("HistoryStageDetailSchema accepts context_file_size_bytes", () => {
    it("should validate a record with context_file_size_bytes", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": {
            status: "complete",
            context_file_size_bytes: 102400,
          },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "complete" },
          "feature-validate": { status: "complete" },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
          "pipeline-finish": { status: "complete" },
        },
      });

      const record = ExecutionHistoryWriter.buildRunRecord(state);

      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a record without context_file_size_bytes", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
    });
  });

  describe("Pipeline state schema accepts context_file_size_bytes", () => {
    it("should validate stage state with context_file_size_bytes", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": {
            status: "complete",
            context_file_size_bytes: 5120,
          },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "complete" },
          "feature-validate": { status: "complete" },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
          "pipeline-finish": { status: "complete" },
        },
      });

      const result = validatePipelineState(state);
      expect(result.success).toBe(true);
    });
  });

  describe("Config schema accepts threshold", () => {
    it("should validate pipeline config with context_file_size_alert_threshold_bytes", () => {
      // PipelineConfigSchema imported at top

      const result = PipelineConfigSchema.safeParse({
        context_file_size_alert_threshold_bytes: 204800,
      });
      expect(result.success).toBe(true);
      expect(result.data.context_file_size_alert_threshold_bytes).toBe(204800);
    });

    it("should reject negative threshold", () => {
      // PipelineConfigSchema imported at top

      const result = PipelineConfigSchema.safeParse({
        context_file_size_alert_threshold_bytes: -1,
      });
      expect(result.success).toBe(false);
    });

    it("should accept 0 threshold (disabled)", () => {
      // PipelineConfigSchema imported at top

      const result = PipelineConfigSchema.safeParse({
        context_file_size_alert_threshold_bytes: 0,
      });
      expect(result.success).toBe(true);
    });
  });
});

function createMockPipelineState(overrides?: Record<string, unknown>) {
  return {
    schema_version: "1.0",
    issue_number: 42,
    title: "Test issue",
    branch: "feat/42-test",
    base_branch: "main",
    started_at: new Date(Date.now() - 60000).toISOString(),
    updated_at: new Date().toISOString(),
    execution_mode: "automatic",
    paused: false,
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "complete" },
      "feature-validate": { status: "complete" },
      "pr-create": { status: "complete" },
      "pr-merge": { status: "complete" },
      "pipeline-finish": { status: "complete" },
    },
    tokens: {
      total_input: 10000,
      total_output: 5000,
      total_cache_read: 2000,
      total_cache_creation: 1000,
      estimated_cost_usd: 0.1,
    },
    ...overrides,
  };
}
