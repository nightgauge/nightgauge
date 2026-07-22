/**
 * Tests for pipelineRunV4Mapper — the V2(snake_case) → V4(camelCase) transform
 * that lets the uploader satisfy the platform's strict ExecutionHistoryRunRecordV4
 * contract. This is the producer-side guard against the "dashboard shows 0 runs"
 * regression: if the mapper drifts from the platform schema, these assertions
 * (and the platform's deploy-gate telemetry canary) fail.
 */

import { describe, it, expect } from "vitest";
import {
  mapHistoryRecordToV4,
  TELEMETRY_SCHEMA_VERSION_V4,
} from "../../src/services/telemetry/pipelineRunV4Mapper";

/** A representative V2 record as written by the Go history producer. */
function v2Record(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: "2",
    record_type: "run",
    issue_number: 82,
    repo: "nightgauge/acmeapp-platform",
    title: "Some issue title",
    branch: "feat/82-thing",
    base_branch: "main",
    execution_mode: "automatic",
    started_at: "2026-06-06T17:55:45.567-06:00", // local offset — must normalise to Z
    completed_at: "2026-06-07T00:29:00.937Z",
    total_duration_ms: 1995370,
    outcome: "complete",
    size: "M",
    tokens: {
      total_input: 1000,
      total_output: 500,
      estimated_cost_usd: 0.42,
      per_stage: {
        "feature-dev": {
          input: 800,
          output: 400,
          cache_read: 100,
          cost_usd: 0.3,
          adapter: "claude",
        },
      },
    },
    routing: {
      complexity_score: 3,
      path: "issue-pickup,feature-planning,feature-dev",
      skip_stages: [],
    },
    stages: {
      "feature-dev": {
        status: "complete",
        duration_ms: 120000,
        model_selection: { model: "claude-opus-4", source: "router" },
      },
    },
    is_recovery: true,
    performance_mode: "maximum",
    ...overrides,
  };
}

describe("mapHistoryRecordToV4", () => {
  it("maps a full V2 record to a schema-valid V4 record", () => {
    const result = mapHistoryRecordToV4(v2Record());
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const r = result.record;

    expect(r.schemaVersion).toBe(TELEMETRY_SCHEMA_VERSION_V4);
    expect(r.issueNumber).toBe(82);
    expect(r.repo).toBe("nightgauge/acmeapp-platform");
    expect(r.outcome).toBe("complete");
    expect(r.durationMs).toBe(1995370);
    expect(r.totalCostUsd).toBe(0.42);
    expect(r.complexityScore).toBe(3);
    expect(r.actualSize).toBe("M");
    expect(r.retries).toBe(0);
    expect(r.agents).toEqual([]);
    expect(r.routingPath).toEqual(["issue-pickup", "feature-planning", "feature-dev"]);

    // No snake_case / disallowed keys leaked through (the schema is .strict()).
    const keys = Object.keys(r);
    expect(keys).not.toContain("schema_version");
    expect(keys).not.toContain("record_type");
    expect(keys).not.toContain("issue_number");
    expect(keys).not.toContain("title");
    expect(keys).not.toContain("branch");
    expect(keys).not.toContain("is_recovery");
    expect(keys).not.toContain("performance_mode");
  });

  it("normalises local-offset timestamps to UTC ISO with a trailing Z", () => {
    const result = mapHistoryRecordToV4(v2Record());
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.startedAt.endsWith("Z")).toBe(true);
    // 17:55:45.567 at -06:00 == 23:55:45.567Z
    expect(result.record.startedAt).toBe("2026-06-06T23:55:45.567Z");
    expect(result.record.completedAt).toBe("2026-06-07T00:29:00.937Z");
  });

  it("maps stages to V4 StageMetric entries with per-stage tokens", () => {
    const result = mapHistoryRecordToV4(v2Record());
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.stages).toHaveLength(1);
    const stage = result.record.stages[0]!;
    expect(stage.stageId).toBe("feature-dev");
    expect(stage.stageName).toBe("feature-dev");
    expect(stage.attempt).toBe(1);
    expect(stage.model).toBe("claude-opus-4");
    expect(stage.durationMs).toBe(120000);
    expect(stage.inputTokens).toBe(800);
    expect(stage.outputTokens).toBe(400);
    expect(stage.totalTokens).toBe(1200);
    expect(stage.costUsd).toBe(0.3);
    expect(stage.success).toBe(true);
  });

  it("marks a failed stage as success:false", () => {
    const result = mapHistoryRecordToV4(
      v2Record({
        stages: { "feature-dev": { status: "failed", duration_ms: 10 } },
      })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.stages[0]!.success).toBe(false);
  });

  it("nulls a non-Fibonacci complexity score", () => {
    const result = mapHistoryRecordToV4(
      v2Record({ routing: { complexity_score: 4, path: "x", skip_stages: [] } })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.complexityScore).toBeNull();
  });

  it("returns null routingPath for a placeholder 'standard' path", () => {
    const result = mapHistoryRecordToV4(
      v2Record({ routing: { complexity_score: 3, path: "standard", skip_stages: [] } })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.routingPath).toBeNull();
  });

  it("sets completedAt to null when the run has no completion timestamp", () => {
    const result = mapHistoryRecordToV4(v2Record({ completed_at: "" }));
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.completedAt).toBeNull();
  });

  // ── Skip cases ──────────────────────────────────────────────────────────────

  it("skips a record with no repo (pre-repo history line)", () => {
    const { repo: _omit, ...noRepo } = v2Record();
    void _omit;
    const result = mapHistoryRecordToV4(noRepo);
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.reason).toContain("repo");
  });

  it("skips a record whose repo is not owner/name", () => {
    const result = mapHistoryRecordToV4(v2Record({ repo: "no-slash" }));
    expect(result.ok).toBe(false);
  });

  it("skips a non-run record_type", () => {
    const result = mapHistoryRecordToV4(v2Record({ record_type: "outcome" }));
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.reason).toContain("record_type");
  });

  it("skips an unmappable outcome", () => {
    const result = mapHistoryRecordToV4(v2Record({ outcome: "in_progress" }));
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.reason).toContain("outcome");
  });

  it("skips a record with an unparseable started_at", () => {
    const result = mapHistoryRecordToV4(v2Record({ started_at: "not-a-date" }));
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.reason).toContain("started_at");
  });

  it("skips a record with a missing/invalid issue_number", () => {
    const result = mapHistoryRecordToV4(v2Record({ issue_number: 0 }));
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.reason).toContain("issue_number");
  });

  it("skips a non-object input", () => {
    expect(mapHistoryRecordToV4(null).ok).toBe(false);
    expect(mapHistoryRecordToV4("string").ok).toBe(false);
    expect(mapHistoryRecordToV4([]).ok).toBe(false);
  });

  // ── pipelineRunId threading ───────────────────────────────────────────────
  //
  // The V3 JSONL record's `run_id` must land on the wire record as
  // `pipelineRunId` — UUID-guarded, mirroring the Go authoritative push's
  // validTelemetryRunID — so the uploader's batch upsert converges on the
  // SAME pipeline_runs row instead of the platform minting a derived-id
  // duplicate.

  it("threads a valid UUID run_id onto the wire record as pipelineRunId", () => {
    const runId = "01890a5d-ac96-774b-bcce-b302099a8057";
    const result = mapHistoryRecordToV4(v2Record({ run_id: runId }));
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.pipelineRunId).toBe(runId);
  });

  it("omits pipelineRunId when run_id is missing", () => {
    const result = mapHistoryRecordToV4(v2Record());
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.pipelineRunId).toBeUndefined();
    // JSON.stringify drops undefined-valued keys — the wire body must not
    // carry a `pipelineRunId` key at all (the schema declares it optional,
    // not nullable, so sending `null` would still fail validation).
    expect(Object.keys(JSON.parse(JSON.stringify(result.record)) as object)).not.toContain(
      "pipelineRunId"
    );
  });

  it("omits pipelineRunId when run_id is not a well-formed UUID", () => {
    const result = mapHistoryRecordToV4(v2Record({ run_id: "not-a-uuid" }));
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.pipelineRunId).toBeUndefined();
  });

  it("omits pipelineRunId when run_id is not a string", () => {
    const result = mapHistoryRecordToV4(v2Record({ run_id: 12345 }));
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.record.pipelineRunId).toBeUndefined();
  });
});
