/**
 * executionHistoryWriter.performanceMode.test.ts
 *
 * Validates the persisted per-stage `performance_mode` contract from Issue
 * #3215, including records written by the authoritative Go runtime.
 *
 * @see Issue #3215 - Add performance_mode to per-stage history schema
 */

import { describe, it, expect } from "vitest";
import { ExecutionHistoryRunRecordV2Schema } from "../../src/schemas/executionHistory";

describe("Execution history schema — per-stage performance_mode (#3215)", () => {
  it("Zod parses legacy V2 records without per-stage performance_mode (backward compat)", () => {
    const legacyRecord = {
      schema_version: "2" as const,
      record_type: "run" as const,
      issue_number: 3215,
      title: "legacy",
      branch: "feat/legacy",
      base_branch: "main",
      execution_mode: "automatic" as const,
      started_at: "2026-03-15T10:00:00Z",
      completed_at: "2026-03-15T10:30:00Z",
      total_duration_ms: 1_800_000,
      outcome: "complete" as const,
      stages: {
        "issue-pickup": { status: "complete" as const },
        "feature-dev": { status: "complete" as const },
      },
      tokens: {
        total_input: 1000,
        total_output: 500,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0.01,
      },
      files: { read_count: 0, written_count: 0 },
      routing: { complexity_score: 0, path: "standard", skip_stages: [] },
      recorded_at: "2026-03-15T10:30:00Z",
    };

    const parsed = ExecutionHistoryRunRecordV2Schema.parse(legacyRecord);
    expect(parsed.stages["issue-pickup"]?.performance_mode).toBeUndefined();
    expect(parsed.stages["feature-dev"]?.performance_mode).toBeUndefined();
  });

  it("Zod accepts a Go-emitted record with per-stage performance_mode (cross-runtime parity)", () => {
    // Mirrors what Go's BuildV2Record emits when StageModes is populated. The
    // shape MUST round-trip cleanly so the dashboard renders Go-written
    // history without schema validation drift.
    const goRecord = {
      schema_version: "2" as const,
      record_type: "run" as const,
      issue_number: 3215,
      title: "go-emitted",
      branch: "feat/3215",
      base_branch: "main",
      execution_mode: "automatic" as const,
      started_at: "2026-03-15T10:00:00Z",
      completed_at: "2026-03-15T10:30:00Z",
      total_duration_ms: 1_800_000,
      outcome: "complete" as const,
      stages: {
        "issue-pickup": {
          status: "complete" as const,
          performance_mode: "efficiency" as const,
        },
        "feature-dev": {
          status: "complete" as const,
          performance_mode: "maximum" as const,
        },
      },
      tokens: {
        total_input: 1000,
        total_output: 500,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0.01,
      },
      files: { read_count: 0, written_count: 0 },
      routing: { complexity_score: 0, path: "standard", skip_stages: [] },
      recorded_at: "2026-03-15T10:30:00Z",
    };

    const parsed = ExecutionHistoryRunRecordV2Schema.parse(goRecord);
    expect(parsed.stages["issue-pickup"]?.performance_mode).toBe("efficiency");
    expect(parsed.stages["feature-dev"]?.performance_mode).toBe("maximum");
  });
});
