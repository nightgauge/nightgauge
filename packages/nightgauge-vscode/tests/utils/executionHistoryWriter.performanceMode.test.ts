/**
 * executionHistoryWriter.performanceMode.test.ts
 *
 * Validates that the per-stage `performance_mode` field plumbed by Issue #3215
 * flows through buildRunRecord and that legacy records (no per-stage mode)
 * still parse via the V2 Zod schema.
 *
 * @see Issue #3215 - Add performance_mode to per-stage history schema
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import { ExecutionHistoryRunRecordV2Schema } from "../../src/schemas/executionHistory";

vi.mock("node:fs/promises");

describe("ExecutionHistoryWriter — per-stage performance_mode (#3215)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("threads performance_mode through buildRunRecord when set on stage state", () => {
    const state = createMockPipelineState({
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete", performance_mode: "efficiency" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "complete", performance_mode: "maximum" },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
        "pipeline-finish": { status: "complete" },
      },
    });

    const record = ExecutionHistoryWriter.buildRunRecord(state);

    expect(record.stages["issue-pickup"]?.performance_mode).toBe("efficiency");
    expect(record.stages["feature-dev"]?.performance_mode).toBe("maximum");
  });

  it("omits performance_mode when not present on stage state (Go omitempty parity)", () => {
    const state = createMockPipelineState();
    const record = ExecutionHistoryWriter.buildRunRecord(state);

    for (const [, stage] of Object.entries(record.stages)) {
      expect(stage?.performance_mode).toBeUndefined();
    }
  });

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

function createMockPipelineState(overrides?: Record<string, unknown>) {
  return {
    schema_version: "1.0",
    issue_number: 42,
    title: "Test issue",
    branch: "feat/42-test",
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
