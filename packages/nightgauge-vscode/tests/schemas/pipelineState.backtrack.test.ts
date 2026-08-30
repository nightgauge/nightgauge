/**
 * pipelineState.backtrack.test.ts
 *
 * Schema validation tests for BacktrackRecord.
 *
 * @see Issue #1342 - Orchestrator Backtrack Engine
 */

import { describe, it, expect } from "vitest";
import { BacktrackRecordSchema, type BacktrackRecord } from "../../src/schemas/pipelineState";

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
