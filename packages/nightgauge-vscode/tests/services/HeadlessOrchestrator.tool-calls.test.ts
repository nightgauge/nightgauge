/**
 * HeadlessOrchestrator.tool-calls.test.ts
 *
 * Tests for tool call accumulation, arg sanitization, and JSONL persistence.
 * Tests the sanitization logic pattern and schema integration for Issue #1004.
 *
 * @see Issue #1004 - Persist Tool Calls in JSONL Execution History
 */

import { describe, it, expect } from "vitest";
import {
  ToolCallRecordSchema,
  ExecutionHistoryRunRecordV2Schema,
} from "../../src/schemas/executionHistory";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";

/**
 * Mirror of the sanitizeToolCallArgs logic from HeadlessOrchestrator.ts
 * for testing purposes. The actual function is module-private.
 */
const SENSITIVE_KEYS_PATTERN = /token|secret|key|password|auth|credential/i;
const MAX_ARG_VALUE_LENGTH = 200;

function sanitizeToolCallArgs(
  args: Record<string, unknown> | undefined
): Record<string, unknown> | undefined {
  if (!args) return undefined;
  const sanitized: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(args)) {
    if (SENSITIVE_KEYS_PATTERN.test(k)) {
      sanitized[k] = "[REDACTED]";
    } else if (typeof v === "string" && v.length > MAX_ARG_VALUE_LENGTH) {
      sanitized[k] = v.substring(0, MAX_ARG_VALUE_LENGTH) + "…";
    } else {
      sanitized[k] = v;
    }
  }
  return sanitized;
}

describe("Tool Call Accumulation and Persistence (Issue #1004)", () => {
  describe("sanitizeToolCallArgs", () => {
    it("should return undefined for undefined input", () => {
      expect(sanitizeToolCallArgs(undefined)).toBeUndefined();
    });

    it("should pass through safe args unchanged", () => {
      const args = { command: "npm run build", cwd: "/workspace" };
      expect(sanitizeToolCallArgs(args)).toEqual(args);
    });

    it("should redact sensitive keys matching pattern", () => {
      const args = {
        command: "curl",
        token: "ghp_abc123",
        api_secret: "sk-secret-value",
        password: "hunter2",
        auth_header: "Bearer xyz",
        credential_path: "/home/.ssh/id_rsa",
        api_key: "AIzaSy...",
      };

      const sanitized = sanitizeToolCallArgs(args)!;
      expect(sanitized.command).toBe("curl");
      expect(sanitized.token).toBe("[REDACTED]");
      expect(sanitized.api_secret).toBe("[REDACTED]");
      expect(sanitized.password).toBe("[REDACTED]");
      expect(sanitized.auth_header).toBe("[REDACTED]");
      expect(sanitized.credential_path).toBe("[REDACTED]");
      expect(sanitized.api_key).toBe("[REDACTED]");
    });

    it("should truncate long string values to 200 chars", () => {
      const longValue = "x".repeat(300);
      const args = { content: longValue };

      const sanitized = sanitizeToolCallArgs(args)!;
      expect(sanitized.content).toHaveLength(201); // 200 chars + ellipsis
      expect((sanitized.content as string).endsWith("…")).toBe(true);
    });

    it("should not truncate strings at or under 200 chars", () => {
      const exactValue = "x".repeat(200);
      const args = { content: exactValue };

      const sanitized = sanitizeToolCallArgs(args)!;
      expect(sanitized.content).toBe(exactValue);
    });

    it("should handle non-string values without truncation", () => {
      const args = { count: 42, nested: { a: 1 }, flag: true };
      expect(sanitizeToolCallArgs(args)).toEqual(args);
    });

    it("should handle empty args object", () => {
      expect(sanitizeToolCallArgs({})).toEqual({});
    });
  });

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

  describe("buildRunRecord with tool_calls", () => {
    it("should include tool_calls array when provided", () => {
      const state = createMockPipelineState();
      const toolCalls = [
        {
          tool: "Read",
          target: "src/index.ts",
          stage: "feature-dev",
          timestamp: "2026-02-19T10:00:00.000Z",
        },
        {
          tool: "Bash",
          target: "npm run build",
          stage: "feature-dev",
          timestamp: "2026-02-19T10:01:00.000Z",
          duration_ms: 5000,
        },
      ];

      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        tool_calls: toolCalls,
      });

      expect(record.tool_calls).toHaveLength(2);
      expect(record.tool_calls![0].tool).toBe("Read");
      expect(record.tool_calls![0].stage).toBe("feature-dev");
      expect(record.tool_calls![1].duration_ms).toBe(5000);
    });

    it("should leave tool_calls undefined when not provided", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tool_calls).toBeUndefined();
    });

    it("should produce a valid v2 record with tool_calls", () => {
      const state = createMockPipelineState();
      const toolCalls = [
        { tool: "Read", target: "README.md", stage: "issue-pickup" },
        { tool: "Bash", stage: "feature-dev", args: { command: "npm test" } },
      ];

      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        tool_calls: toolCalls,
      });

      const validation = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(validation.success).toBe(true);
    });

    it("should produce a valid v2 record with empty tool_calls", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        tool_calls: undefined,
      });

      const validation = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(validation.success).toBe(true);
      expect(validation.success && validation.data.tool_calls).toBeUndefined();
    });
  });
});

// ============================================================================
// Test Helpers
// ============================================================================

function createMockPipelineState(overrides?: Record<string, unknown>) {
  return {
    schema_version: "1.0",
    issue_number: 1004,
    title: "Persist tool calls in JSONL execution history",
    branch: "feat/1004-persist-tool-calls-jsonl",
    base_branch: "main",
    started_at: new Date(Date.now() - 60000).toISOString(),
    updated_at: new Date().toISOString(),
    execution_mode: "automatic" as const,
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
