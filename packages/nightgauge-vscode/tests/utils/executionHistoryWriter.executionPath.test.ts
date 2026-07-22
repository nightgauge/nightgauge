/**
 * executionHistoryWriter.executionPath.test.ts
 *
 * Regression for the observability half of Issue #297. The legacy
 * HeadlessOrchestrator write path never populated `execution_path` (the schema
 * allowed it since #3264, but the writer's stage-record literal omitted it) and
 * `punt_reason` did not exist — so a worktree-mode dogfood run's pr-stage
 * decision was unobservable. buildRunRecord now projects the orchestrator's
 * per-stage execution-path map onto the history record's `execution_path` +
 * `punt_reason`, matching Go's BuildV2Record.
 */

import { describe, it, expect } from "vitest";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import { ExecutionHistoryRunRecordV2Schema } from "../../src/schemas/executionHistory";

function mockState(overrides?: Record<string, unknown>) {
  return {
    schema_version: "1.0",
    issue_number: 297,
    title: "Deterministic pr-stage observability",
    branch: "fix/297",
    base_branch: "main",
    started_at: new Date(Date.now() - 60_000).toISOString(),
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
      total_input: 10_000,
      total_output: 5_000,
      total_cache_read: 2_000,
      total_cache_creation: 1_000,
      estimated_cost_usd: 0.1,
    },
    ...overrides,
  };
}

describe("ExecutionHistoryWriter — execution_path + punt_reason (#297)", () => {
  it("projects the per-stage execution-path map onto execution_path + punt_reason", () => {
    const stageExecutionPaths = new Map<
      string,
      { path: "deterministic" | "llm"; puntReason?: string }
    >([
      ["pr-create", { path: "deterministic" }],
      ["pr-merge", { path: "llm", puntReason: "dirty-merge-state: BLOCKED" }],
    ]);

    const record = ExecutionHistoryWriter.buildRunRecord(mockState(), undefined, undefined, {
      stageExecutionPaths,
    });

    expect(record.stages["pr-create"]?.execution_path).toBe("deterministic");
    expect(record.stages["pr-create"]?.punt_reason).toBeUndefined();

    expect(record.stages["pr-merge"]?.execution_path).toBe("llm");
    expect(record.stages["pr-merge"]?.punt_reason).toBe("dirty-merge-state: BLOCKED");

    // Stages with no decision recorded keep both fields absent (Go omitempty parity).
    expect(record.stages["feature-dev"]?.execution_path).toBeUndefined();
    expect(record.stages["feature-dev"]?.punt_reason).toBeUndefined();

    // The record must still satisfy the V2 schema with the new fields present.
    expect(() => ExecutionHistoryRunRecordV2Schema.parse(record)).not.toThrow();
  });

  it("omits both fields when no execution-path map is supplied (backward compatible)", () => {
    const record = ExecutionHistoryWriter.buildRunRecord(mockState());
    for (const [, stage] of Object.entries(record.stages)) {
      expect(stage?.execution_path).toBeUndefined();
      expect(stage?.punt_reason).toBeUndefined();
    }
    expect(() => ExecutionHistoryRunRecordV2Schema.parse(record)).not.toThrow();
  });
});
