/**
 * contextFileSizeMeasurement.test.ts
 *
 * Tests for context handoff file size measurement and threshold alerting.
 *
 * @see Issue #1009 - Track context handoff file sizes
 */

import { describe, it, expect } from "vitest";
import { StageStateSchema } from "../../src/schemas/pipelineState";
import { HistoryStageDetailSchema } from "../../src/schemas/executionHistory";
import { PipelineConfigSchema } from "../../src/config/schema";

describe("Context File Size Measurement (Issue #1009)", () => {
  it("preserves context_file_size_bytes in persisted stage details", () => {
    expect(
      HistoryStageDetailSchema.parse({ status: "complete", context_file_size_bytes: 102_400 })
        .context_file_size_bytes
    ).toBe(102_400);
  });

  describe("Stage state schema accepts context_file_size_bytes", () => {
    it("should validate stage state with context_file_size_bytes", () => {
      const result = StageStateSchema.safeParse({
        status: "complete",
        context_file_size_bytes: 5120,
      });
      expect(result.success).toBe(true);
      // Assert the field SURVIVES the parse, not merely that the parse
      // succeeded: Zod strips unknown keys and still reports success, so a
      // success-only assertion stays green even if the schema drops the field
      // entirely. (It did — verified by mutation while repointing this off the
      // deleted `validatePipelineState` in #467. Repointed again in #471 at
      // the live stage-state schema, since the writer-less pipeline state
      // schema that used to wrap it is gone.)
      expect(result.data?.context_file_size_bytes).toBe(5120);
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
