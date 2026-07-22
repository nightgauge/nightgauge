/**
 * executionHistory.test.ts
 *
 * Schema validation tests for ExecutionHistoryRecord types.
 *
 * @see Issue #649 - Execution History Persistence
 */

import { describe, it, expect } from "vitest";
import {
  ExecutionHistoryRunRecordSchema,
  ExecutionHistoryRunRecordV2Schema,
  ExecutionOutcomeRecordSchema,
  ExecutionOutcomeRecordV2Schema,
  ExecutionHistoryRecordSchema,
  AnyRunRecordSchema,
  AnyOutcomeRecordSchema,
  ToolCallRecordSchema,
  HistoryStageTokenUsageSchema,
} from "../../src/schemas/executionHistory";
import { PipelineStateSchema, validatePipelineState } from "../../src/schemas/pipelineState";

describe("ExecutionHistory Schemas", () => {
  describe("ExecutionHistoryRunRecordSchema", () => {
    const validRunRecord = {
      schema_version: "1" as const,
      record_type: "run" as const,
      issue_number: 42,
      title: "Add photo upload",
      branch: "feat/42-photo-upload",
      base_branch: "main",
      execution_mode: "automatic" as const,
      started_at: "2026-02-13T10:00:00.000Z",
      completed_at: "2026-02-13T10:30:00.000Z",
      total_duration_ms: 1800000,
      outcome: "complete" as const,
      stages: {
        "pipeline-start": { status: "complete" as const },
        "issue-pickup": {
          status: "complete" as const,
          started_at: "2026-02-13T10:00:01.000Z",
          completed_at: "2026-02-13T10:02:00.000Z",
          duration_ms: 119000,
        },
        "feature-planning": {
          status: "complete" as const,
          duration_ms: 300000,
        },
        "feature-dev": { status: "complete" as const, duration_ms: 600000 },
        "feature-validate": {
          status: "complete" as const,
          duration_ms: 120000,
        },
        "pr-create": { status: "complete" as const, duration_ms: 60000 },
        "pr-merge": { status: "complete" as const, duration_ms: 30000 },
        "pipeline-finish": { status: "complete" as const },
      },
      tokens: {
        total_input: 50000,
        total_output: 15000,
        total_cache_read: 10000,
        total_cache_creation: 5000,
        estimated_cost_usd: 0.25,
      },
      recorded_at: "2026-02-13T10:30:00.000Z",
    };

    it("should validate a complete run record", () => {
      const result = ExecutionHistoryRunRecordSchema.safeParse(validRunRecord);
      expect(result.success).toBe(true);
    });

    it("should validate a run record with per-stage tokens", () => {
      const record = {
        ...validRunRecord,
        tokens: {
          ...validRunRecord.tokens,
          per_stage: {
            "issue-pickup": {
              input: 5000,
              output: 2000,
              cache_read: 1000,
              cache_creation: 500,
              cost_usd: 0.03,
            },
          },
        },
      };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a run record with optional fields", () => {
      const record = {
        ...validRunRecord,
        files: { read_count: 15, written_count: 8 },
        routing: {
          complexity_score: 5,
          path: "standard",
          skip_stages: ["feature-validate"],
        },
      };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should accept records with optional fields omitted", () => {
      // Minimal valid record — no files, no routing, no per_stage tokens
      const result = ExecutionHistoryRunRecordSchema.safeParse(validRunRecord);
      expect(result.success).toBe(true);
    });

    it("should reject records with missing required fields", () => {
      const { issue_number: _, ...incomplete } = validRunRecord;
      const result = ExecutionHistoryRunRecordSchema.safeParse(incomplete);
      expect(result.success).toBe(false);
    });

    it("should reject invalid schema_version", () => {
      const record = { ...validRunRecord, schema_version: "2" };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject invalid outcome value", () => {
      const record = { ...validRunRecord, outcome: "unknown" };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject negative token values", () => {
      const record = {
        ...validRunRecord,
        tokens: { ...validRunRecord.tokens, total_input: -1 },
      };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should validate failed outcome with error in stages", () => {
      const record = {
        ...validRunRecord,
        outcome: "failed" as const,
        stages: {
          ...validRunRecord.stages,
          "feature-dev": {
            status: "failed" as const,
            error: "Build failed with exit code 1",
          },
          "feature-validate": { status: "pending" as const },
          "pr-create": { status: "pending" as const },
          "pr-merge": { status: "pending" as const },
          "pipeline-finish": { status: "pending" as const },
        },
      };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate stage with execution_mode and retry counts", () => {
      const record = {
        ...validRunRecord,
        stages: {
          ...validRunRecord.stages,
          "feature-dev": {
            status: "complete" as const,
            execution_mode: "headless" as const,
            auto_retry_count: 2,
            manual_retry_count: 1,
          },
        },
      };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a run record with labels and extracted fields (Issue #844)", () => {
      const record = {
        ...validRunRecord,
        labels: ["type:feature", "size:M", "priority:high", "status:in-progress"],
        size: "M",
        type: "feature",
        priority: "high",
      };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should accept empty labels array", () => {
      const record = {
        ...validRunRecord,
        labels: [],
        size: null,
        type: null,
        priority: null,
      };
      const result = ExecutionHistoryRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should accept records without labels fields (backward compatibility)", () => {
      // Records written before #844 won't have these fields
      const result = ExecutionHistoryRunRecordSchema.safeParse(validRunRecord);
      expect(result.success).toBe(true);
    });
  });

  describe("ExecutionOutcomeRecordSchema", () => {
    it("should validate a merged outcome record", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "merged" as const,
        merged_at: "2026-02-13T12:00:00.000Z",
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = ExecutionOutcomeRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a closed outcome record", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "closed" as const,
        closed_at: "2026-02-13T12:00:00.000Z",
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = ExecutionOutcomeRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should reject missing pr_number", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        outcome: "merged" as const,
        recorded_at: "2026-02-13T12:00:00.000Z",
      };
      const result = ExecutionOutcomeRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject invalid outcome value", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "abandoned",
        recorded_at: "2026-02-13T12:00:00.000Z",
      };
      const result = ExecutionOutcomeRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });
  });

  describe("ToolCallRecordSchema", () => {
    it("should validate a minimal tool call with only required field", () => {
      const record = {
        tool: "Bash",
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a full tool call with all optional fields", () => {
      const record = {
        tool: "Bash",
        target: "/usr/bin/bash",
        timestamp: "2026-02-13T10:05:00.000Z",
        duration_ms: 1250,
        args: { command: "npm run build", cwd: "/workspace" },
        result: "Build succeeded",
        error: undefined,
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should reject a tool call missing the required tool field", () => {
      const record = {
        target: "/usr/bin/bash",
        duration_ms: 500,
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject a tool call with a negative duration_ms", () => {
      const record = {
        tool: "Read",
        duration_ms: -100,
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should validate a tool call with optional stage field (Issue #1004)", () => {
      const record = {
        tool: "Read",
        target: "src/index.ts",
        stage: "feature-dev",
        timestamp: "2026-02-19T10:00:00.000Z",
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should accept a tool call without stage field (backward compat)", () => {
      const record = {
        tool: "Bash",
        duration_ms: 500,
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });
  });

  describe("ExecutionHistoryRunRecordV2Schema", () => {
    const validV2RunRecord = {
      schema_version: "2" as const,
      record_type: "run" as const,
      issue_number: 42,
      title: "Add photo upload",
      branch: "feat/42-photo-upload",
      base_branch: "main",
      execution_mode: "automatic" as const,
      started_at: "2026-02-13T10:00:00.000Z",
      completed_at: "2026-02-13T10:30:00.000Z",
      total_duration_ms: 1800000,
      outcome: "complete" as const,
      stages: {
        "pipeline-start": { status: "complete" as const },
        "issue-pickup": { status: "complete" as const },
        "feature-planning": { status: "complete" as const },
        "feature-dev": { status: "complete" as const },
        "feature-validate": { status: "complete" as const },
        "pr-create": { status: "complete" as const },
        "pr-merge": { status: "complete" as const },
        "pipeline-finish": { status: "complete" as const },
      },
      tokens: {
        total_input: 50000,
        total_output: 15000,
        total_cache_read: 10000,
        total_cache_creation: 5000,
        estimated_cost_usd: 0.25,
      },
      files: { read_count: 15, written_count: 8 },
      routing: { complexity_score: 5, path: "standard", skip_stages: [] },
      recorded_at: "2026-02-13T10:30:00.000Z",
    };

    it("should validate a complete v2 run record with required files and routing", () => {
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(validV2RunRecord);
      expect(result.success).toBe(true);
    });

    it("should validate a v2 run record with all optional fields present", () => {
      const record = {
        ...validV2RunRecord,
        outcome_type: "productive" as const,
        tool_calls: [
          { tool: "Bash", duration_ms: 500 },
          { tool: "Read", target: "src/index.ts" },
        ],
        labels: ["type:feature", "size:M"],
        size: "M",
        type: "feature",
        priority: "high",
      };
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should reject a v2 run record missing required files field", () => {
      const { files: _, ...withoutFiles } = validV2RunRecord;
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(withoutFiles);
      expect(result.success).toBe(false);
    });

    it("should reject a v2 run record missing required routing field", () => {
      const { routing: _, ...withoutRouting } = validV2RunRecord;
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(withoutRouting);
      expect(result.success).toBe(false);
    });

    it("should validate a v2 run record with a non-empty tool_calls array", () => {
      const record = {
        ...validV2RunRecord,
        tool_calls: [
          {
            tool: "Bash",
            target: "npm run build",
            timestamp: "2026-02-13T10:05:00.000Z",
            duration_ms: 12000,
            args: { command: "npm run build" },
            result: "success",
          },
        ],
      };
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should reject a v2 run record with schema_version 1", () => {
      const record = { ...validV2RunRecord, schema_version: "1" };
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject a v2 run record with an invalid outcome_type", () => {
      const record = { ...validV2RunRecord, outcome_type: "unknown-type" };
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject a v2 run record with negative files counts", () => {
      const record = {
        ...validV2RunRecord,
        files: { read_count: -1, written_count: 0 },
      };
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject a v2 run record with negative complexity_score", () => {
      const record = {
        ...validV2RunRecord,
        routing: { complexity_score: -5, path: "standard", skip_stages: [] },
      };
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should validate each outcome_type variant", () => {
      const outcomeTypes = ["productive", "verify-and-close", "already-resolved"] as const;
      for (const outcome_type of outcomeTypes) {
        const record = { ...validV2RunRecord, outcome_type };
        const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
        expect(result.success).toBe(true);
      }
    });

    it("should accept run_id when present (Issue #3558)", () => {
      const record = { ...validV2RunRecord, run_id: "abc-123-uuid" };
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.run_id).toBe("abc-123-uuid");
      }
    });

    it("should remain valid when run_id is absent — backward compatible (Issue #3558)", () => {
      const record = { ...validV2RunRecord };
      delete (record as Partial<typeof record>).run_id;
      const result = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
    });
  });

  describe("ExecutionOutcomeRecordV2Schema", () => {
    it("should validate a v2 merged outcome record", () => {
      const record = {
        schema_version: "2" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "merged" as const,
        merged_at: "2026-02-13T12:00:00.000Z",
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = ExecutionOutcomeRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a v2 closed outcome record", () => {
      const record = {
        schema_version: "2" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "closed" as const,
        closed_at: "2026-02-13T12:00:00.000Z",
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = ExecutionOutcomeRecordV2Schema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should reject a v2 outcome record with schema_version 1", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "merged" as const,
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = ExecutionOutcomeRecordV2Schema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should reject a v2 outcome record missing pr_number", () => {
      const record = {
        schema_version: "2" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        outcome: "merged" as const,
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = ExecutionOutcomeRecordV2Schema.safeParse(record);
      expect(result.success).toBe(false);
    });
  });

  describe("AnyRunRecordSchema", () => {
    it("should accept a v1 run record", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "run" as const,
        issue_number: 10,
        title: "Fix bug",
        branch: "fix/10-bug",
        base_branch: "main",
        execution_mode: "automatic" as const,
        started_at: "2026-02-13T08:00:00.000Z",
        completed_at: "2026-02-13T08:20:00.000Z",
        total_duration_ms: 1200000,
        outcome: "complete" as const,
        stages: {},
        tokens: {
          total_input: 1000,
          total_output: 500,
          total_cache_read: 0,
          total_cache_creation: 0,
          estimated_cost_usd: 0.01,
        },
        recorded_at: "2026-02-13T08:20:00.000Z",
      };
      const result = AnyRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should accept a v2 run record", () => {
      const record = {
        schema_version: "2" as const,
        record_type: "run" as const,
        issue_number: 42,
        title: "Add photo upload",
        branch: "feat/42-photo-upload",
        base_branch: "main",
        execution_mode: "automatic" as const,
        started_at: "2026-02-13T10:00:00.000Z",
        completed_at: "2026-02-13T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete" as const,
        stages: {
          "pipeline-start": { status: "complete" as const },
          "issue-pickup": { status: "complete" as const },
          "feature-planning": { status: "complete" as const },
          "feature-dev": { status: "complete" as const },
          "feature-validate": { status: "complete" as const },
          "pr-create": { status: "complete" as const },
          "pr-merge": { status: "complete" as const },
          "pipeline-finish": { status: "complete" as const },
        },
        tokens: {
          total_input: 50000,
          total_output: 15000,
          total_cache_read: 10000,
          total_cache_creation: 5000,
          estimated_cost_usd: 0.25,
        },
        files: { read_count: 15, written_count: 8 },
        routing: { complexity_score: 5, path: "standard", skip_stages: [] },
        recorded_at: "2026-02-13T10:30:00.000Z",
      };
      const result = AnyRunRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should reject a record that is neither v1 nor v2", () => {
      const record = {
        schema_version: "3",
        record_type: "run",
        issue_number: 42,
        recorded_at: "2026-02-13T10:30:00.000Z",
      };
      const result = AnyRunRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });
  });

  describe("AnyOutcomeRecordSchema", () => {
    it("should accept a v1 outcome record", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "merged" as const,
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = AnyOutcomeRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should accept a v2 outcome record", () => {
      const record = {
        schema_version: "2" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "closed" as const,
        closed_at: "2026-02-13T12:00:00.000Z",
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = AnyOutcomeRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });
  });

  describe("ExecutionHistoryRecordSchema (union)", () => {
    it("should parse run records via discriminated union", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "run" as const,
        issue_number: 42,
        title: "Test",
        branch: "feat/42",
        base_branch: "main",
        execution_mode: "automatic" as const,
        started_at: "2026-02-13T10:00:00.000Z",
        completed_at: "2026-02-13T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete" as const,
        stages: {},
        tokens: {
          total_input: 0,
          total_output: 0,
          total_cache_read: 0,
          total_cache_creation: 0,
          estimated_cost_usd: 0,
        },
        recorded_at: "2026-02-13T10:30:00.000Z",
      };
      const result = ExecutionHistoryRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.record_type).toBe("run");
      }
    });

    it("should parse outcome records via discriminated union", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "merged" as const,
        recorded_at: "2026-02-13T12:00:00.000Z",
      };
      const result = ExecutionHistoryRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.record_type).toBe("outcome");
      }
    });

    it("should reject unknown record_type", () => {
      const record = {
        schema_version: "1" as const,
        record_type: "unknown",
        issue_number: 42,
        recorded_at: "2026-02-13T12:00:00.000Z",
      };
      const result = ExecutionHistoryRecordSchema.safeParse(record);
      expect(result.success).toBe(false);
    });

    it("should parse v2 run records via union", () => {
      const record = {
        schema_version: "2" as const,
        record_type: "run" as const,
        issue_number: 42,
        title: "Add photo upload",
        branch: "feat/42-photo-upload",
        base_branch: "main",
        execution_mode: "automatic" as const,
        started_at: "2026-02-13T10:00:00.000Z",
        completed_at: "2026-02-13T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete" as const,
        stages: {
          "pipeline-start": { status: "complete" as const },
          "issue-pickup": { status: "complete" as const },
          "feature-planning": { status: "complete" as const },
          "feature-dev": { status: "complete" as const },
          "feature-validate": { status: "complete" as const },
          "pr-create": { status: "complete" as const },
          "pr-merge": { status: "complete" as const },
          "pipeline-finish": { status: "complete" as const },
        },
        tokens: {
          total_input: 50000,
          total_output: 15000,
          total_cache_read: 10000,
          total_cache_creation: 5000,
          estimated_cost_usd: 0.25,
        },
        files: { read_count: 15, written_count: 8 },
        routing: { complexity_score: 5, path: "standard", skip_stages: [] },
        recorded_at: "2026-02-13T10:30:00.000Z",
      };
      const result = ExecutionHistoryRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.record_type).toBe("run");
        expect(result.data.schema_version).toBe("2");
      }
    });

    it("should parse v2 outcome records via union", () => {
      const record = {
        schema_version: "2" as const,
        record_type: "outcome" as const,
        issue_number: 42,
        pr_number: 100,
        outcome: "merged" as const,
        merged_at: "2026-02-13T12:00:00.000Z",
        recorded_at: "2026-02-13T12:00:01.000Z",
      };
      const result = ExecutionHistoryRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.record_type).toBe("outcome");
        expect(result.data.schema_version).toBe("2");
      }
    });
  });

  describe("HistoryStageTokenUsageSchema model fields (Issue #1006)", () => {
    const baseTokenUsage = {
      input: 5000,
      output: 2000,
      cache_read: 1000,
      cache_creation: 500,
      cost_usd: 0.03,
    };

    it("should validate token usage without model fields (backward compat)", () => {
      const result = HistoryStageTokenUsageSchema.safeParse(baseTokenUsage);
      expect(result.success).toBe(true);
    });

    it("should validate token usage with model and model_source", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        model: "sonnet",
        model_source: "auto",
      });
      expect(result.success).toBe(true);
    });

    it("should validate token usage with only model (no model_source)", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        model: "opus",
      });
      expect(result.success).toBe(true);
    });

    it("should reject invalid model_source value", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        model: "sonnet",
        model_source: "invalid",
      });
      expect(result.success).toBe(false);
    });

    it("should accept all valid model_source variants", () => {
      for (const source of ["env", "config", "auto", "default"] as const) {
        const result = HistoryStageTokenUsageSchema.safeParse({
          ...baseTokenUsage,
          model: "sonnet",
          model_source: source,
        });
        expect(result.success).toBe(true);
      }
    });
  });

  describe("HistoryStageTokenUsageSchema cache_hit_rate (Issue #2459)", () => {
    const baseTokenUsage = {
      input: 5000,
      output: 2000,
      cache_read: 1000,
      cache_creation: 500,
      cost_usd: 0.03,
    };

    it("should validate token usage without cache_hit_rate (backward compat)", () => {
      const result = HistoryStageTokenUsageSchema.safeParse(baseTokenUsage);
      expect(result.success).toBe(true);
    });

    it("should validate token usage with a typical cache_hit_rate value", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        cache_hit_rate: 0.42,
      });
      expect(result.success).toBe(true);
    });

    it("should validate cache_hit_rate at boundary 0 (no cache)", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        cache_hit_rate: 0,
      });
      expect(result.success).toBe(true);
    });

    it("should validate cache_hit_rate at boundary 1 (full cache hit)", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        cache_hit_rate: 1,
      });
      expect(result.success).toBe(true);
    });

    it("should reject cache_hit_rate above 1", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        cache_hit_rate: 1.5,
      });
      expect(result.success).toBe(false);
    });

    it("should reject cache_hit_rate below 0", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        cache_hit_rate: -0.1,
      });
      expect(result.success).toBe(false);
    });

    it("should accept record with cache_hit_rate alongside model fields", () => {
      const result = HistoryStageTokenUsageSchema.safeParse({
        ...baseTokenUsage,
        model: "sonnet",
        model_source: "auto",
        cache_hit_rate: 0.75,
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.cache_hit_rate).toBe(0.75);
      }
    });
  });

  describe("PipelineStateSchema outcome_type (Issue #1005)", () => {
    const validState = {
      schema_version: "1.0" as const,
      issue_number: 42,
      title: "Test issue",
      branch: "feat/42-test",
      base_branch: "main",
      started_at: "2026-02-19T10:00:00.000Z",
      updated_at: "2026-02-19T10:30:00.000Z",
      execution_mode: "automatic" as const,
      paused: false,
      stages: {
        "pipeline-start": { status: "complete" as const },
        "issue-pickup": { status: "complete" as const },
        "feature-dev": { status: "complete" as const },
        "pipeline-finish": { status: "complete" as const },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.1,
      },
    };

    it("should accept state without outcome_type (backward compat)", () => {
      const result = PipelineStateSchema.safeParse(validState);
      expect(result.success).toBe(true);
    });

    it("should accept state with outcome_type productive", () => {
      const result = PipelineStateSchema.safeParse({
        ...validState,
        outcome_type: "productive",
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.outcome_type).toBe("productive");
      }
    });

    it("should accept all outcome_type variants", () => {
      for (const ot of ["productive", "verify-and-close", "already-resolved"] as const) {
        const result = PipelineStateSchema.safeParse({
          ...validState,
          outcome_type: ot,
        });
        expect(result.success).toBe(true);
      }
    });

    it("should reject invalid outcome_type value", () => {
      const result = PipelineStateSchema.safeParse({
        ...validState,
        outcome_type: "invalid",
      });
      expect(result.success).toBe(false);
    });

    it("should preserve outcome_type through validatePipelineState", () => {
      const result = validatePipelineState({
        ...validState,
        outcome_type: "verify-and-close",
      });
      expect(result.success).toBe(true);
      expect(result.data?.outcome_type).toBe("verify-and-close");
    });
  });
});
