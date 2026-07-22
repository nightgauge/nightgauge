/**
 * executionHistoryWriter.test.ts
 *
 * Unit tests for ExecutionHistoryWriter utility class.
 *
 * @see Issue #649 - Execution History Persistence
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import { ExecutionHistoryRunRecordV2Schema } from "../../src/schemas/executionHistory";

// Mock node:fs/promises
vi.mock("node:fs/promises");

describe("ExecutionHistoryWriter", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("getFilenameForDate()", () => {
    it("should generate YYYY-MM-DD.jsonl filename", () => {
      const date = new Date("2026-02-13T14:30:00Z");
      expect(ExecutionHistoryWriter.getFilenameForDate(date)).toBe("2026-02-13.jsonl");
    });

    it("should use current date when no date provided", () => {
      const today = new Date().toISOString().split("T")[0];
      expect(ExecutionHistoryWriter.getFilenameForDate()).toBe(`${today}.jsonl`);
    });

    it("should handle date boundaries correctly", () => {
      expect(ExecutionHistoryWriter.getFilenameForDate(new Date("2026-12-31T23:59:59Z"))).toBe(
        "2026-12-31.jsonl"
      );

      expect(ExecutionHistoryWriter.getFilenameForDate(new Date("2026-01-01T00:00:00Z"))).toBe(
        "2026-01-01.jsonl"
      );
    });
  });

  describe("getHistoryDir()", () => {
    it("should return correct history directory path", () => {
      expect(ExecutionHistoryWriter.getHistoryDir(workspaceRoot)).toBe(
        "/test/workspace/.nightgauge/pipeline/history"
      );
    });
  });

  describe("appendRecord()", () => {
    it("should write a valid run record as JSONL", async () => {
      const record = ExecutionHistoryWriter.buildRunRecord(createMockPipelineState());

      await ExecutionHistoryWriter.appendRecord(workspaceRoot, record);

      expect(fs.mkdir).toHaveBeenCalledWith(
        expect.stringContaining(".nightgauge/pipeline/history"),
        { recursive: true }
      );

      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.stringMatching(/\.jsonl$/),
        expect.stringMatching(/^\{.*\}\n$/),
        "utf-8"
      );
    });

    it("should write multiple records to same day file", async () => {
      const record1 = ExecutionHistoryWriter.buildRunRecord(
        createMockPipelineState({ issue_number: 42 })
      );
      const record2 = ExecutionHistoryWriter.buildRunRecord(
        createMockPipelineState({ issue_number: 43 })
      );

      await ExecutionHistoryWriter.appendRecord(workspaceRoot, record1);
      await ExecutionHistoryWriter.appendRecord(workspaceRoot, record2);

      expect(fs.appendFile).toHaveBeenCalledTimes(2);
    });

    it("should warn but still write records with schema issues", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      const invalidRecord = {
        schema_version: "2" as const,
        record_type: "run" as const,
        // Missing required fields — schema will flag issues
        recorded_at: new Date().toISOString(),
      } as any;

      await ExecutionHistoryWriter.appendRecord(workspaceRoot, invalidRecord);

      // Record is written despite schema warnings (Issue #2249)
      expect(fs.appendFile).toHaveBeenCalled();
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("schema issues"));

      warnSpy.mockRestore();
    });

    it("should handle fs errors gracefully without throwing", async () => {
      vi.mocked(fs.mkdir).mockRejectedValue(new Error("EACCES: permission denied"));
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      const record = ExecutionHistoryWriter.buildRunRecord(createMockPipelineState());

      const result = await ExecutionHistoryWriter.appendRecord(workspaceRoot, record);
      expect(result).toBe(false);

      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining("Failed to write execution history")
      );

      warnSpy.mockRestore();
    });
  });

  describe("buildRunRecord()", () => {
    it("should map PipelineState to run record correctly", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.schema_version).toBe("2");
      expect(record.record_type).toBe("run");
      expect(record.issue_number).toBe(42);
      expect(record.title).toBe("Test issue");
      expect(record.branch).toBe("feat/42-test");
      expect(record.base_branch).toBe("main");
      expect(record.execution_mode).toBe("automatic");
      expect(record.started_at).toBe(state.started_at);
      expect(record.total_duration_ms).toBeGreaterThanOrEqual(0);
      expect(record.recorded_at).toBeDefined();
    });

    it("should detect failed outcome from stage statuses", () => {
      const state = createMockPipelineState();
      state.stages["feature-dev"] = {
        status: "failed",
        error: "Build error",
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.outcome).toBe("failed");
    });

    it("should detect complete outcome when all stages pass", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.outcome).toBe("complete");
    });

    // Issue #2994: sparse state.stages (only 3 of 8 canonical stages present)
    // previously yielded outcome="complete" because no stage was "failed".
    // Now it must yield "cancelled" so the run isn't misclassified mid-flight.
    it("should mark outcome as cancelled when state.stages is sparse mid-pipeline", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
        },
      });
      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.outcome).toBe("cancelled");
    });

    it("should treat routing.skip_stages as accounted-for when computing outcome", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          // feature-dev / feature-validate skipped via routing
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
          "pipeline-finish": { status: "complete" },
        },
      });
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        routing: {
          complexity_score: 1,
          path: "trivial",
          skip_stages: ["feature-dev", "feature-validate"],
        },
      });
      expect(record.outcome).toBe("complete");
    });

    it("should treat in-state status='skipped' as terminal for outcome", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "skipped" },
          "feature-validate": { status: "skipped" },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
          "pipeline-finish": { status: "complete" },
        },
      });
      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.outcome).toBe("complete");
    });

    it("should treat status='deferred' as terminal for outcome (pr-merge awaiting review)", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "complete" },
          "feature-validate": { status: "complete" },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "deferred" },
          "pipeline-finish": { status: "deferred" },
        },
      });
      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.outcome).toBe("complete");
    });

    it("should map running stages to complete in history", () => {
      const state = createMockPipelineState();
      state.stages["pipeline-finish"] = { status: "running" };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.stages["pipeline-finish"]?.status).toBe("complete");
    });

    it("should copy token totals from state", () => {
      const state = createMockPipelineState();
      state.tokens.total_input = 50000;
      state.tokens.total_output = 15000;
      state.tokens.estimated_cost_usd = 0.25;

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tokens.total_input).toBe(50000);
      expect(record.tokens.total_output).toBe(15000);
      expect(record.tokens.estimated_cost_usd).toBe(0.25);
    });

    it("should compute token totals from per_stage when top-level accumulators are zero (Issue #2617)", () => {
      // Simulate an interim write where top-level totals are not yet populated
      // but per_stage data is available from completed stages.
      const state = createMockPipelineState();
      state.tokens.total_input = 0;
      state.tokens.total_output = 0;
      state.tokens.total_cache_read = 0;
      state.tokens.total_cache_creation = 0;
      state.tokens.estimated_cost_usd = 0;
      state.tokens.per_stage = {
        "issue-pickup": {
          input: 10000,
          output: 4000,
          cache_read: 2000,
          cache_creation: 500,
          cost_usd: 0.05,
        },
        "feature-planning": {
          input: 20000,
          output: 8000,
          cache_read: 3000,
          cache_creation: 1000,
          cost_usd: 0.1,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tokens.total_input).toBe(30000); // 10000 + 20000
      expect(record.tokens.total_output).toBe(12000); // 4000 + 8000
      expect(record.tokens.total_cache_read).toBe(5000); // 2000 + 3000
      expect(record.tokens.total_cache_creation).toBe(1500); // 500 + 1000
      expect(record.tokens.estimated_cost_usd).toBeCloseTo(0.15); // 0.05 + 0.10
    });

    it("should prefer top-level token totals over per_stage computation when non-zero (Issue #2617)", () => {
      // When the top-level totals are populated, they take precedence over
      // per-stage computation (top-level values may be more accurate due to
      // cache_creation tracking that per-stage may lack).
      const state = createMockPipelineState();
      state.tokens.total_input = 99999;
      state.tokens.total_output = 55555;
      state.tokens.estimated_cost_usd = 1.23;
      state.tokens.per_stage = {
        "issue-pickup": {
          input: 10000,
          output: 4000,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.05,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      // Top-level totals should win because they are non-zero.
      expect(record.tokens.total_input).toBe(99999);
      expect(record.tokens.total_output).toBe(55555);
      expect(record.tokens.estimated_cost_usd).toBeCloseTo(1.23);
    });

    it("JSONL records non-zero cost when budget-exceeded terminates before final result (Issue #2777)", () => {
      // When a stage is budget-terminated before the Claude CLI emits total_cost_usd,
      // usage.costUsd is 0 at termination time. The JSONL must still record the
      // accumulated cost from completed prior stages via per_stage fallback.
      const state = createMockPipelineState();
      state.tokens.total_input = 0; // top-level may be 0 if stage was mid-run
      state.tokens.total_output = 0;
      state.tokens.estimated_cost_usd = 0; // top-level cost is 0 (not yet flushed)
      state.tokens.per_stage = {
        "issue-pickup": {
          input: 15000,
          output: 2000,
          cache_read: 10000,
          cache_creation: 0,
          cost_usd: 0.08,
        },
        "feature-planning": {
          input: 45000,
          output: 8000,
          cache_read: 35000,
          cache_creation: 0,
          cost_usd: 0.42,
        },
        "feature-dev": {
          input: 120000,
          output: 25000,
          cache_read: 90000,
          cache_creation: 0,
          cost_usd: 1.85,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);

      // The per_stage fallback must produce non-zero cost even when
      // top-level estimated_cost_usd is 0.
      expect(record.tokens.estimated_cost_usd).toBeCloseTo(0.08 + 0.42 + 1.85); // 2.35
      expect(record.tokens.per_stage?.["issue-pickup"]?.cost_usd).toBe(0.08);
      expect(record.tokens.per_stage?.["feature-planning"]?.cost_usd).toBe(0.42);
      expect(record.tokens.per_stage?.["feature-dev"]?.cost_usd).toBe(1.85);
    });

    it("should return zero tokens when both top-level and per_stage are empty (Issue #2617)", () => {
      const state = createMockPipelineState();
      state.tokens.total_input = 0;
      state.tokens.total_output = 0;
      state.tokens.total_cache_read = 0;
      state.tokens.total_cache_creation = 0;
      state.tokens.estimated_cost_usd = 0;
      // No per_stage data either (very early interim write)

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tokens.total_input).toBe(0);
      expect(record.tokens.total_output).toBe(0);
      expect(record.tokens.estimated_cost_usd).toBe(0);
    });

    it("should include per-stage token data when available", () => {
      const state = createMockPipelineState();
      state.tokens.per_stage = {
        "feature-dev": {
          input: 30000,
          output: 10000,
          cache_read: 5000,
          cache_creation: 2000,
          cost_usd: 0.15,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tokens.per_stage?.["feature-dev"]).toBeDefined();
      expect(record.tokens.per_stage?.["feature-dev"]?.input).toBe(30000);
    });

    it("records per-stage adapter from state.stages and falls back to defaultAdapter (Issue #3224)", () => {
      const state = createMockPipelineState({
        stages: {
          "issue-pickup": { status: "complete", adapter: "claude" },
          "feature-planning": { status: "complete" }, // no adapter — fallback
          "feature-dev": { status: "complete", adapter: "gemini" },
          "feature-validate": { status: "complete" },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
        },
      });
      state.tokens.per_stage = {
        "issue-pickup": {
          input: 1000,
          output: 500,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.05,
        },
        "feature-planning": {
          input: 1500,
          output: 600,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.07,
        },
        "feature-dev": {
          input: 2000,
          output: 700,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.12,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        defaultAdapter: "codex",
      });

      // Explicit per-stage adapter wins.
      expect(record.tokens.per_stage?.["issue-pickup"]?.adapter).toBe("claude");
      expect(record.tokens.per_stage?.["feature-dev"]?.adapter).toBe("gemini");
      // Stage without per-stage adapter falls back to defaultAdapter.
      expect(record.tokens.per_stage?.["feature-planning"]?.adapter).toBe("codex");

      // Round-trip through the V2 schema — confirms the field is accepted.
      const parsed = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(parsed.success).toBe(true);
    });

    it("records adapter_fallback_chain_used when state has length ≥ 2 (Issue #3231)", () => {
      const state = createMockPipelineState({
        stages: {
          "issue-pickup": { status: "complete", adapter: "gemini" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "complete",
            adapter: "gemini",
            adapter_source: "fallback",
            adapter_fallback_chain_used: ["claude", "codex", "gemini"],
          },
          "feature-validate": { status: "complete" },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
        },
      });
      state.tokens.per_stage = {
        "feature-dev": {
          input: 2000,
          output: 700,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.12,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tokens.per_stage?.["feature-dev"]?.adapter_fallback_chain_used).toEqual([
        "claude",
        "codex",
        "gemini",
      ]);
      // Round-trip through the V2 schema.
      const parsed = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(parsed.success).toBe(true);
    });

    it("omits adapter_fallback_chain_used when length < 2 (terse for primary-success, Issue #3231)", () => {
      const state = createMockPipelineState({
        stages: {
          "feature-dev": {
            status: "complete",
            adapter: "claude",
            // Length-1 case — represents primary-success; should not be persisted.
            adapter_fallback_chain_used: ["claude"],
          },
        },
      });
      state.tokens.per_stage = {
        "feature-dev": {
          input: 2000,
          output: 700,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.12,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tokens.per_stage?.["feature-dev"]?.adapter_fallback_chain_used).toBeUndefined();
    });

    it("omits adapter_fallback_chain_used when state field is absent (Issue #3231)", () => {
      const state = createMockPipelineState({
        stages: {
          "feature-dev": { status: "complete", adapter: "claude" },
        },
      });
      state.tokens.per_stage = {
        "feature-dev": {
          input: 2000,
          output: 700,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.12,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.tokens.per_stage?.["feature-dev"]?.adapter_fallback_chain_used).toBeUndefined();
    });

    it("omits adapter field on per-stage records when neither state nor default supplies one (Issue #3224)", () => {
      const state = createMockPipelineState();
      state.tokens.per_stage = {
        "feature-dev": {
          input: 2000,
          output: 700,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.12,
        },
      };

      const record = ExecutionHistoryWriter.buildRunRecord(state);

      // adapter must be absent (undefined), not the empty string — preserves
      // the back-compat contract for dashboards predating Issue #3224.
      expect(record.tokens.per_stage?.["feature-dev"]?.adapter).toBeUndefined();

      // Schema validates without issue (the field is .optional()).
      const parsed = ExecutionHistoryRunRecordV2Schema.safeParse(record);
      expect(parsed.success).toBe(true);
    });

    it("parses legacy per-stage records that lack the adapter field (Issue #3224 back-compat)", () => {
      // Construct a record by hand to mimic an existing JSONL line written
      // before #3224. The schema must accept it cleanly with adapter === undefined.
      const legacyRecord = {
        schema_version: "2",
        record_type: "run",
        issue_number: 3224,
        title: "legacy run",
        branch: "feat/legacy",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: "2026-05-07T10:00:00.000Z",
        completed_at: "2026-05-07T10:05:00.000Z",
        total_duration_ms: 300000,
        outcome: "complete",
        labels: [],
        size: null,
        type: null,
        priority: null,
        stages: { "feature-dev": { status: "complete" } },
        tokens: {
          total_input: 2000,
          total_output: 700,
          total_cache_read: 0,
          total_cache_creation: 0,
          estimated_cost_usd: 0.12,
          per_stage: {
            "feature-dev": {
              input: 2000,
              output: 700,
              cache_read: 0,
              cache_creation: 0,
              cost_usd: 0.12,
            },
          },
        },
        files: { read_count: 0, written_count: 0 },
        routing: { complexity_score: 0, path: "standard", skip_stages: [] },
        recorded_at: "2026-05-07T10:05:00.000Z",
      };

      const parsed = ExecutionHistoryRunRecordV2Schema.safeParse(legacyRecord);
      expect(parsed.success).toBe(true);
      if (parsed.success) {
        expect(parsed.data.tokens.per_stage?.["feature-dev"]?.adapter).toBeUndefined();
      }
    });

    it("should include labels and extracted fields when issueMetadata provided (Issue #844)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, {
        labels: ["type:feature", "size:M", "priority:high"],
        size: "M",
        type: "feature",
        priority: "high",
      });

      expect(record.labels).toEqual(["type:feature", "size:M", "priority:high"]);
      expect(record.size).toBe("M");
      expect(record.type).toBe("feature");
      expect(record.priority).toBe("high");
    });

    it("should default to empty labels and null extracted fields when no issueMetadata", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.labels).toEqual([]);
      expect(record.size).toBeNull();
      expect(record.type).toBeNull();
      expect(record.priority).toBeNull();
    });

    it("should include skip_reason in stage details when present (Issue #843)", () => {
      const state = createMockPipelineState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "complete" },
          "feature-validate": {
            status: "skipped",
            skip_reason: "Skipped via trivial route: docs-only change, no code to validate",
          },
          "pr-create": { status: "complete" },
          "pr-merge": { status: "complete" },
          "pipeline-finish": { status: "complete" },
        },
      });

      const record = ExecutionHistoryWriter.buildRunRecord(state);
      expect(record.stages["feature-validate"]?.skip_reason).toBe(
        "Skipped via trivial route: docs-only change, no code to validate"
      );
    });

    it("should omit skip_reason when not present (backward compat, Issue #843)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      for (const [, stageData] of Object.entries(record.stages)) {
        expect(stageData?.skip_reason).toBeUndefined();
      }
    });

    it("should default files to zeros when not provided in options", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.files).toEqual({ read_count: 0, written_count: 0 });
    });

    it("should use provided files counts from options (Issue #1005)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        files: { read_count: 25, written_count: 12 },
      });

      expect(record.files).toEqual({ read_count: 25, written_count: 12 });
    });

    it("should default routing to unknown when not provided in options", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.routing).toEqual({
        complexity_score: 0,
        path: "unknown",
        skip_stages: [],
      });
    });

    it("should use provided routing from options (Issue #1005)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        routing: {
          complexity_score: 4,
          path: "standard",
          skip_stages: ["feature-validate"],
        },
      });

      expect(record.routing).toEqual({
        complexity_score: 4,
        path: "standard",
        skip_stages: ["feature-validate"],
      });
    });

    it("should populate all three telemetry fields together (Issue #1005)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        outcome_type: "productive",
        files: { read_count: 30, written_count: 10 },
        routing: {
          complexity_score: 3,
          path: "standard",
          skip_stages: [],
        },
      });

      expect(record.outcome_type).toBe("productive");
      expect(record.files).toEqual({ read_count: 30, written_count: 10 });
      expect(record.routing).toEqual({
        complexity_score: 3,
        path: "standard",
        skip_stages: [],
      });
    });

    it("should include run_id when provided in options (Issue #3558)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        run_id: "abc-123-uuid",
      });

      expect(record.run_id).toBe("abc-123-uuid");
    });

    it("should omit run_id when not provided in options (Issue #3558)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.run_id).toBeUndefined();
    });

    it("should include outcome_type when provided in options", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        outcome_type: "productive",
      });

      expect(record.outcome_type).toBe("productive");
    });

    it("should include tool_calls when provided in options", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        tool_calls: [{ tool: "Read", target: "src/index.ts" }],
      });

      expect(record.tool_calls).toHaveLength(1);
    });

    it("should include tool_calls with stage field (Issue #1004)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        tool_calls: [
          {
            tool: "Bash",
            target: "npm run build",
            stage: "feature-dev",
            timestamp: "2026-02-19T10:00:00.000Z",
            duration_ms: 5000,
            args: { command: "npm run build" },
          },
        ],
      });

      expect(record.tool_calls).toHaveLength(1);
      expect(record.tool_calls![0].stage).toBe("feature-dev");
      expect(record.tool_calls![0].duration_ms).toBe(5000);
    });

    it("should default outcome_type and tool_calls to undefined", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.outcome_type).toBeUndefined();
      expect(record.tool_calls).toBeUndefined();
    });

    it("should set is_recovery to true when options.is_recovery is true (Issue #1261)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        is_recovery: true,
      });

      expect(record.is_recovery).toBe(true);
    });

    it("should omit is_recovery when not provided (default non-recovery run)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);

      expect(record.is_recovery).toBeUndefined();
    });

    it("should propagate is_recovery into buildIndexEntry (Issue #1261)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
        is_recovery: true,
      });

      const entry = ExecutionHistoryWriter.buildIndexEntry(record);
      expect(entry.is_recovery).toBe(true);
    });

    it("should omit is_recovery from index entry for normal runs (Issue #1261)", () => {
      const state = createMockPipelineState();
      const record = ExecutionHistoryWriter.buildRunRecord(state);
      const entry = ExecutionHistoryWriter.buildIndexEntry(record);

      expect(entry.is_recovery).toBeUndefined();
    });
  });

  describe("buildOutcomeRecord() removal (Issue #319)", () => {
    it("should no longer exist as a static method — the legacy pr-merge outcome writer was deleted, not repaired", () => {
      // Issue #319: buildOutcomeRecord() produced identity-less "outcome"
      // records (no repo, no run_id) appended via bootstrap-level shared
      // state instead of the completing run's own identity, which let them
      // escape every idempotency/identity guard and land in a sibling
      // repo's history file. The Go authoritative writer already records
      // the merged outcome on its own "run" record, so the second writer
      // was deleted outright rather than repaired. This assertion guards
      // against silently reintroducing it.
      expect(
        (ExecutionHistoryWriter as unknown as { buildOutcomeRecord?: unknown }).buildOutcomeRecord
      ).toBeUndefined();
    });
  });

  describe("cleanupOldFiles()", () => {
    it("should delete files older than retention period", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["2026-01-01.jsonl", "2026-02-13.jsonl"] as any);
      vi.mocked(fs.unlink).mockResolvedValue();

      // Set retention to 30 days — with today being 2026-02-13,
      // the cutoff would be ~Jan 14, so Jan 1 is old enough to delete
      const result = await ExecutionHistoryWriter.cleanupOldFiles(workspaceRoot, 30);

      expect(fs.unlink).toHaveBeenCalledWith(expect.stringContaining("2026-01-01.jsonl"));
      expect(result.deleted).toContain("2026-01-01.jsonl");
    });

    it("should preserve files within retention period", async () => {
      const today = new Date().toISOString().split("T")[0];
      vi.mocked(fs.readdir).mockResolvedValue([`${today}.jsonl`] as any);

      const result = await ExecutionHistoryWriter.cleanupOldFiles(workspaceRoot, 90);

      expect(fs.unlink).not.toHaveBeenCalled();
      expect(result.deleted).toHaveLength(0);
    });

    it("should handle missing history directory gracefully", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const result = await ExecutionHistoryWriter.cleanupOldFiles(workspaceRoot, 90);

      expect(result.deleted).toHaveLength(0);
    });

    it("should skip non-JSONL files", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([".gitkeep", "notes.txt", "2020-01-01.jsonl"] as any);
      vi.mocked(fs.unlink).mockResolvedValue();

      await ExecutionHistoryWriter.cleanupOldFiles(workspaceRoot, 1);

      // Only the .jsonl file should be considered
      const unlinkCalls = vi.mocked(fs.unlink).mock.calls;
      expect(unlinkCalls.every(([p]) => String(p).endsWith(".jsonl"))).toBe(true);
    });

    it("should skip files with unparseable date names", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["invalid-date.jsonl", "2020-01-01.jsonl"] as any);
      vi.mocked(fs.unlink).mockResolvedValue();

      await ExecutionHistoryWriter.cleanupOldFiles(workspaceRoot, 1);

      const unlinkPaths = vi.mocked(fs.unlink).mock.calls.map(([p]) => String(p));
      expect(unlinkPaths.some((p) => p.includes("invalid-date"))).toBe(false);
    });
  });
});

// ============================================================================
// Stall Event Tests (Issue #2652)
// ============================================================================

describe("buildRunRecord() with stall events", () => {
  it("should not include stall_events in stages when stageStallEvents not provided", () => {
    const state = createMockPipelineState();
    const record = ExecutionHistoryWriter.buildRunRecord(state);

    for (const stageDetail of Object.values(record.stages)) {
      expect(stageDetail.stall_events).toBeUndefined();
    }
  });

  it("should not include stall_events when stageStallEvents map is empty", () => {
    const state = createMockPipelineState();
    const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
      stageStallEvents: new Map(),
    });

    for (const stageDetail of Object.values(record.stages)) {
      expect(stageDetail.stall_events).toBeUndefined();
    }
  });

  it("should include stall_events for a stage when stageStallEvents has entries", () => {
    const state = createMockPipelineState();
    const warnEvent = {
      timestamp: "2026-04-11T13:15:22.456Z",
      elapsed_ms: 120000,
      threshold_ms: 120000,
      action: "warn" as const,
    };
    const stageStallEvents = new Map([["feature-dev", [warnEvent]]]);
    const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
      stageStallEvents,
    });

    expect(record.stages["feature-dev"].stall_events).toHaveLength(1);
    expect(record.stages["feature-dev"].stall_events![0].action).toBe("warn");
    expect(record.stages["feature-dev"].stall_events![0].elapsed_ms).toBe(120000);
  });

  it("should include multiple stall events for a stage", () => {
    const state = createMockPipelineState();
    const events = [
      {
        timestamp: "2026-04-11T13:15:22.456Z",
        elapsed_ms: 120000,
        threshold_ms: 120000,
        action: "warn" as const,
      },
      {
        timestamp: "2026-04-11T13:17:00.000Z",
        elapsed_ms: 220000,
        threshold_ms: 120000,
        action: "keep_waiting" as const,
      },
    ];
    const stageStallEvents = new Map([["feature-dev", events]]);
    const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
      stageStallEvents,
    });

    expect(record.stages["feature-dev"].stall_events).toHaveLength(2);
    expect(record.stages["feature-dev"].stall_events![1].action).toBe("keep_waiting");
  });

  it("should only include stall_events for stages that had stalls", () => {
    const state = createMockPipelineState();
    const killEvent = {
      timestamp: "2026-04-11T13:20:00.000Z",
      elapsed_ms: 400000,
      threshold_ms: 240000,
      action: "kill" as const,
    };
    const stageStallEvents = new Map([["feature-dev", [killEvent]]]);
    const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
      stageStallEvents,
    });

    // feature-dev has stall events
    expect(record.stages["feature-dev"].stall_events).toHaveLength(1);
    expect(record.stages["feature-dev"].stall_events![0].action).toBe("kill");

    // other stages have no stall events
    expect(record.stages["issue-pickup"].stall_events).toBeUndefined();
    expect(record.stages["feature-planning"].stall_events).toBeUndefined();
  });

  it("should not include stall_events field when stage has empty array", () => {
    const state = createMockPipelineState();
    // Empty array should not write the field (preserves storage)
    const stageStallEvents = new Map([["feature-dev", [] as any[]]]);
    const record = ExecutionHistoryWriter.buildRunRecord(state, undefined, undefined, {
      stageStallEvents,
    });

    expect(record.stages["feature-dev"].stall_events).toBeUndefined();
  });
});

// ============================================================================
// Test Helpers
// ============================================================================

function createMockPipelineState(overrides?: Record<string, unknown>) {
  return {
    schema_version: "1.0",
    issue_number: 42,
    title: "Test issue",
    branch: "feat/42-test",
    base_branch: "main",
    started_at: new Date(Date.now() - 60000).toISOString(),
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
      total_input: 10000,
      total_output: 5000,
      total_cache_read: 2000,
      total_cache_creation: 1000,
      estimated_cost_usd: 0.1,
    },
    ...overrides,
  };
}
