/**
 * pipelineState.backtrack.test.ts
 *
 * Schema validation tests for BacktrackRecord and PipelineState backtrack fields.
 *
 * @see Issue #1342 - Orchestrator Backtrack Engine
 */

import { describe, it, expect } from "vitest";
import {
  BacktrackRecordSchema,
  PipelineStateSchema,
  type BacktrackRecord,
} from "../../src/schemas/pipelineState";

describe("BacktrackRecordSchema", () => {
  const validRecord: BacktrackRecord = {
    from_stage: "feature-validate",
    to_stage: "feature-dev",
    signal_type: "VALIDATION_FAILED",
    rationale: "Tests failed after 3 attempts; rewinding to dev stage.",
    timestamp: "2026-02-26T10:00:00.000Z",
    attempt_number: 1,
  };

  describe("valid records", () => {
    it("should parse a fully populated backtrack record", () => {
      const result = BacktrackRecordSchema.safeParse(validRecord);
      expect(result.success).toBe(true);
    });

    it("should parse a record with attempt_number of 1 (minimum)", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        attempt_number: 1,
      });
      expect(result.success).toBe(true);
    });

    it("should parse a record with a high attempt_number", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        attempt_number: 10,
      });
      expect(result.success).toBe(true);
    });

    it("should parse all valid from_stage values", () => {
      const stages = [
        "pipeline-start",
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
        "pipeline-finish",
      ] as const;
      for (const stage of stages) {
        const result = BacktrackRecordSchema.safeParse({
          ...validRecord,
          from_stage: stage,
        });
        expect(result.success).toBe(true);
      }
    });

    it("should parse all valid to_stage values", () => {
      const stages = [
        "pipeline-start",
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
        "pipeline-finish",
      ] as const;
      for (const stage of stages) {
        const result = BacktrackRecordSchema.safeParse({
          ...validRecord,
          to_stage: stage,
        });
        expect(result.success).toBe(true);
      }
    });

    it("should preserve all field values on successful parse", () => {
      const result = BacktrackRecordSchema.safeParse(validRecord);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.from_stage).toBe("feature-validate");
        expect(result.data.to_stage).toBe("feature-dev");
        expect(result.data.signal_type).toBe("VALIDATION_FAILED");
        expect(result.data.rationale).toBe(
          "Tests failed after 3 attempts; rewinding to dev stage."
        );
        expect(result.data.timestamp).toBe("2026-02-26T10:00:00.000Z");
        expect(result.data.attempt_number).toBe(1);
      }
    });
  });

  describe("invalid records — missing required fields", () => {
    it("should reject a record missing from_stage", () => {
      const { from_stage: _, ...without } = validRecord;
      const result = BacktrackRecordSchema.safeParse(without);
      expect(result.success).toBe(false);
    });

    it("should reject a record missing to_stage", () => {
      const { to_stage: _, ...without } = validRecord;
      const result = BacktrackRecordSchema.safeParse(without);
      expect(result.success).toBe(false);
    });

    it("should reject a record missing signal_type", () => {
      const { signal_type: _, ...without } = validRecord;
      const result = BacktrackRecordSchema.safeParse(without);
      expect(result.success).toBe(false);
    });

    it("should reject a record missing rationale", () => {
      const { rationale: _, ...without } = validRecord;
      const result = BacktrackRecordSchema.safeParse(without);
      expect(result.success).toBe(false);
    });

    it("should reject a record missing timestamp", () => {
      const { timestamp: _, ...without } = validRecord;
      const result = BacktrackRecordSchema.safeParse(without);
      expect(result.success).toBe(false);
    });

    it("should reject a record missing attempt_number", () => {
      const { attempt_number: _, ...without } = validRecord;
      const result = BacktrackRecordSchema.safeParse(without);
      expect(result.success).toBe(false);
    });

    it("should reject an empty object", () => {
      const result = BacktrackRecordSchema.safeParse({});
      expect(result.success).toBe(false);
    });
  });

  describe("invalid records — bad field values", () => {
    it("should reject an invalid from_stage value", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        from_stage: "unknown-stage",
      });
      expect(result.success).toBe(false);
    });

    it("should reject an invalid to_stage value", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        to_stage: "bad-stage",
      });
      expect(result.success).toBe(false);
    });

    it("should reject a non-ISO timestamp string", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        timestamp: "2026-02-26 10:00:00",
      });
      expect(result.success).toBe(false);
    });

    it("should reject a plain date string without time component", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        timestamp: "2026-02-26",
      });
      expect(result.success).toBe(false);
    });

    it("should reject attempt_number of 0 (below minimum of 1)", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        attempt_number: 0,
      });
      expect(result.success).toBe(false);
    });

    it("should reject a negative attempt_number", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        attempt_number: -1,
      });
      expect(result.success).toBe(false);
    });

    it("should reject a non-integer attempt_number", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        attempt_number: 1.5,
      });
      expect(result.success).toBe(false);
    });

    it("should reject a string attempt_number", () => {
      const result = BacktrackRecordSchema.safeParse({
        ...validRecord,
        attempt_number: "1",
      });
      expect(result.success).toBe(false);
    });
  });
});

describe("PipelineStateSchema — backtrack fields (Issue #1342)", () => {
  const minimalValidState = {
    schema_version: "1.0" as const,
    issue_number: 1342,
    title: "Orchestrator Backtrack Engine",
    branch: "feat/1342-orchestrator-backtrack-engine",
    base_branch: "main",
    started_at: "2026-02-26T09:00:00.000Z",
    updated_at: "2026-02-26T09:45:00.000Z",
    execution_mode: "automatic" as const,
    paused: false,
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
      total_input: 80000,
      total_output: 20000,
      total_cache_read: 15000,
      total_cache_creation: 5000,
      estimated_cost_usd: 0.45,
    },
  };

  const sampleBacktrackRecord: BacktrackRecord = {
    from_stage: "feature-validate",
    to_stage: "feature-dev",
    signal_type: "VALIDATION_FAILED",
    rationale: "Build failed; rewinding to feature-dev.",
    timestamp: "2026-02-26T09:20:00.000Z",
    attempt_number: 1,
  };

  describe("full state with backtrack fields", () => {
    it("should parse a state with backtrack_count and backtracks", () => {
      const state = {
        ...minimalValidState,
        backtrack_count: 1,
        backtracks: [sampleBacktrackRecord],
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(true);
    });

    it("should preserve backtrack_count and backtracks values", () => {
      const state = {
        ...minimalValidState,
        backtrack_count: 1,
        backtracks: [sampleBacktrackRecord],
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.backtrack_count).toBe(1);
        expect(result.data.backtracks).toHaveLength(1);
        expect(result.data.backtracks![0].from_stage).toBe("feature-validate");
        expect(result.data.backtracks![0].to_stage).toBe("feature-dev");
        expect(result.data.backtracks![0].attempt_number).toBe(1);
      }
    });

    it("should parse a state with multiple backtrack records", () => {
      const secondRecord: BacktrackRecord = {
        from_stage: "feature-validate",
        to_stage: "feature-planning",
        signal_type: "PLAN_REVISION_NEEDED",
        rationale: "Scope changed after second validation failure.",
        timestamp: "2026-02-26T09:35:00.000Z",
        attempt_number: 2,
      };
      const state = {
        ...minimalValidState,
        backtrack_count: 2,
        backtracks: [sampleBacktrackRecord, secondRecord],
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.backtrack_count).toBe(2);
        expect(result.data.backtracks).toHaveLength(2);
      }
    });

    it("should parse a state with backtrack_count of 0 and empty backtracks array", () => {
      const state = {
        ...minimalValidState,
        backtrack_count: 0,
        backtracks: [],
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.backtrack_count).toBe(0);
        expect(result.data.backtracks).toHaveLength(0);
      }
    });

    it("should allow backtrack_count without backtracks (independent optionals)", () => {
      const state = {
        ...minimalValidState,
        backtrack_count: 1,
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.backtrack_count).toBe(1);
        expect(result.data.backtracks).toBeUndefined();
      }
    });

    it("should allow backtracks without backtrack_count (independent optionals)", () => {
      const state = {
        ...minimalValidState,
        backtracks: [sampleBacktrackRecord],
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.backtrack_count).toBeUndefined();
        expect(result.data.backtracks).toHaveLength(1);
      }
    });

    it("should reject a negative backtrack_count", () => {
      const state = {
        ...minimalValidState,
        backtrack_count: -1,
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(false);
    });

    it("should reject a non-integer backtrack_count", () => {
      const state = {
        ...minimalValidState,
        backtrack_count: 1.5,
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(false);
    });

    it("should reject backtracks containing an invalid record", () => {
      const state = {
        ...minimalValidState,
        backtrack_count: 1,
        backtracks: [
          {
            from_stage: "feature-validate",
            to_stage: "feature-dev",
            signal_type: "VALIDATION_FAILED",
            rationale: "Tests failed.",
            timestamp: "not-a-datetime",
            attempt_number: 1,
          },
        ],
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(false);
    });

    it("should reject backtracks containing a record with attempt_number of 0", () => {
      const state = {
        ...minimalValidState,
        backtracks: [{ ...sampleBacktrackRecord, attempt_number: 0 }],
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(false);
    });
  });

  describe("backward compatibility — no backtrack fields", () => {
    it("should parse a minimal state without backtrack_count or backtracks", () => {
      const result = PipelineStateSchema.safeParse(minimalValidState);
      expect(result.success).toBe(true);
    });

    it("should leave backtrack_count undefined when omitted", () => {
      const result = PipelineStateSchema.safeParse(minimalValidState);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.backtrack_count).toBeUndefined();
      }
    });

    it("should leave backtracks undefined when omitted", () => {
      const result = PipelineStateSchema.safeParse(minimalValidState);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.backtracks).toBeUndefined();
      }
    });

    it("should still require other mandatory fields when backtrack fields are absent", () => {
      const { issue_number: _, ...withoutIssueNumber } = minimalValidState;
      const result = PipelineStateSchema.safeParse(withoutIssueNumber);
      expect(result.success).toBe(false);
    });

    it("should parse a state that has other optional fields but no backtrack fields", () => {
      const state = {
        ...minimalValidState,
        outcome_type: "productive" as const,
      };
      const result = PipelineStateSchema.safeParse(state);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.outcome_type).toBe("productive");
        expect(result.data.backtrack_count).toBeUndefined();
        expect(result.data.backtracks).toBeUndefined();
      }
    });
  });
});
