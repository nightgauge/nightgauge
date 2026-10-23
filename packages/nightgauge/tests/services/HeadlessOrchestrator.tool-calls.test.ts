/**
 * HeadlessOrchestrator.tool-calls.test.ts
 *
 * Tests the persisted tool-call schema introduced by Issue #1004.
 *
 * @see Issue #1004 - Persist Tool Calls in JSONL Execution History
 */

import { describe, it, expect } from "vitest";
import { ToolCallRecordSchema } from "../../src/schemas/executionHistory";

describe("Persisted tool-call schema (Issue #1004)", () => {
  describe("ToolCallRecord with stage field", () => {
    it("should validate a tool call record with stage field", () => {
      const record = {
        tool: "Read",
        target: "src/index.ts",
        stage: "feature-dev",
        timestamp: "2026-02-19T10:00:00.000Z",
        duration_ms: 50,
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a tool call record without stage field (backward compat)", () => {
      const record = {
        tool: "Bash",
        target: "npm run build",
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a tool call with sanitized args", () => {
      const record = {
        tool: "Bash",
        target: "git push",
        stage: "pr-create",
        timestamp: "2026-02-19T10:05:00.000Z",
        args: { command: "git push origin HEAD", token: "[REDACTED]" },
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });

    it("should validate a tool call record with error field", () => {
      const record = {
        tool: "Bash",
        target: "npm test",
        stage: "feature-validate",
        error: "Process exited with code 1",
      };
      const result = ToolCallRecordSchema.safeParse(record);
      expect(result.success).toBe(true);
    });
  });
});
