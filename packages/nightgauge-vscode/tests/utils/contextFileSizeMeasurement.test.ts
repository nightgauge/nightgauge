/**
 * contextFileSizeMeasurement.test.ts
 *
 * Tests for context handoff file size measurement and threshold alerting.
 *
 * @see Issue #1009 - Track context handoff file sizes
 */

import { describe, it, expect } from "vitest";
import { validatePipelineState } from "../../src/schemas/pipelineState";
import { HistoryStageDetailSchema } from "../../src/schemas/executionHistory";
import { PipelineConfigSchema } from "../../src/config/schema";

describe("Context File Size Measurement (Issue #1009)", () => {
  it("preserves context_file_size_bytes in persisted stage details", () => {
    expect(
      HistoryStageDetailSchema.parse({ status: "complete", context_file_size_bytes: 102_400 })
        .context_file_size_bytes
    ).toBe(102_400);
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
      expect(result.data?.context_file_size_alert_threshold_bytes).toBe(204800);
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
