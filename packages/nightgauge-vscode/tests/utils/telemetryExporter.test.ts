/**
 * telemetryExporter.test.ts
 *
 * Unit tests for the TelemetryExporter pure utility functions.
 * No VSCode dependency — these are format conversion utilities only.
 *
 * @see Issue #1010 - Telemetry Analytics Export
 */

import * as path from "node:path";
import { describe, it, expect, beforeEach } from "vitest";
import {
  exportAsJson,
  exportAsCsvRuns,
  exportAsCsvStages,
} from "../../src/utils/telemetryExporter";
import type {
  ExecutionHistoryRunRecordV2,
  ExecutionHistoryRecord,
} from "../../src/schemas/executionHistory";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";

// ============================================================================
// Shared test fixtures
// ============================================================================

/**
 * A fully-populated v2 run record covering all fields used by the exporter.
 */
const makeRunRecord = (
  overrides: Partial<ExecutionHistoryRunRecordV2> = {}
): ExecutionHistoryRunRecordV2 => ({
  schema_version: "2",
  record_type: "run",
  issue_number: 42,
  title: "Add telemetry export feature",
  branch: "feat/42-telemetry-export",
  base_branch: "main",
  execution_mode: "automatic",
  started_at: "2026-02-19T10:00:00.000Z",
  completed_at: "2026-02-19T10:45:00.000Z",
  total_duration_ms: 2700000,
  outcome: "complete",
  outcome_type: "productive",
  size: "M",
  type: "feature",
  priority: "high",
  labels: ["size:M", "type:feature", "priority:high"],
  stages: {
    "issue-pickup": {
      status: "complete",
      started_at: "2026-02-19T10:00:00.000Z",
      completed_at: "2026-02-19T10:05:00.000Z",
      duration_ms: 300000,
      model_selection: {
        model: "claude-haiku-4-5",
        source: "auto",
        confidence: 0.9,
        complexity: "low",
        mode: "automatic",
        effort: "low",
      },
      context_file_size_bytes: 1024,
    },
    "feature-planning": {
      status: "complete",
      started_at: "2026-02-19T10:05:00.000Z",
      completed_at: "2026-02-19T10:20:00.000Z",
      duration_ms: 900000,
      model_selection: {
        model: "claude-sonnet-4-6",
        source: "config",
        confidence: 0.95,
        complexity: "medium",
        mode: "automatic",
        effort: "medium",
      },
      context_file_size_bytes: 8192,
    },
    "feature-dev": {
      status: "complete",
      started_at: "2026-02-19T10:20:00.000Z",
      completed_at: "2026-02-19T10:40:00.000Z",
      duration_ms: 1200000,
      model_selection: {
        model: "claude-opus-4-6",
        source: "env",
        confidence: 1.0,
        complexity: "high",
        mode: "manual",
        effort: "high",
      },
      context_file_size_bytes: 32768,
    },
    "feature-validate": {
      status: "skipped",
      duration_ms: 0,
      skip_reason: "routing: complexity below threshold",
    },
    "pr-create": {
      status: "complete",
      started_at: "2026-02-19T10:40:00.000Z",
      completed_at: "2026-02-19T10:43:00.000Z",
      duration_ms: 180000,
    },
    "pr-merge": {
      status: "complete",
      started_at: "2026-02-19T10:43:00.000Z",
      completed_at: "2026-02-19T10:45:00.000Z",
      duration_ms: 120000,
    },
  },
  tokens: {
    total_input: 50000,
    total_output: 12000,
    total_cache_read: 25000,
    total_cache_creation: 8000,
    estimated_cost_usd: 0.123456,
    per_stage: {
      "issue-pickup": {
        input: 3000,
        output: 500,
        cache_read: 1000,
        cache_creation: 200,
        cost_usd: 0.005,
        model: "claude-haiku-4-5",
        model_source: "auto",
      },
      "feature-planning": {
        input: 15000,
        output: 4000,
        cache_read: 8000,
        cache_creation: 3000,
        cost_usd: 0.04,
        model: "claude-sonnet-4-6",
        model_source: "config",
      },
      "feature-dev": {
        input: 28000,
        output: 7000,
        cache_read: 15000,
        cache_creation: 4500,
        cost_usd: 0.075,
        model: "claude-opus-4-6",
        model_source: "env",
      },
      "pr-create": {
        input: 3000,
        output: 400,
        cache_read: 900,
        cache_creation: 200,
        cost_usd: 0.003,
        model: "claude-haiku-4-5",
        model_source: "auto",
      },
      "pr-merge": {
        input: 1000,
        output: 100,
        cache_read: 100,
        cache_creation: 100,
        cost_usd: 0.000456,
        model: "claude-haiku-4-5",
        model_source: "default",
      },
    },
  },
  files: {
    read_count: 24,
    written_count: 7,
  },
  routing: {
    complexity_score: 6,
    path: "standard",
    skip_stages: ["feature-validate"],
  },
  tool_calls: [
    {
      tool: "Read",
      target: "src/utils/telemetryExporter.ts",
      stage: "feature-dev",
    },
    {
      tool: "Write",
      target: "src/utils/telemetryExporter.ts",
      stage: "feature-dev",
    },
    { tool: "Bash", target: "npm run build", stage: "feature-validate" },
  ],
  recorded_at: "2026-02-19T10:45:00.000Z",
  ...overrides,
});

/**
 * A v2 outcome record (filtered out by run-only exporters).
 */
const outcomeRecord: ExecutionHistoryRecord = {
  schema_version: "2",
  record_type: "outcome",
  issue_number: 42,
  pr_number: 101,
  outcome: "merged",
  merged_at: "2026-02-19T11:00:00.000Z",
  recorded_at: "2026-02-19T11:00:01.000Z",
};

// ============================================================================
// exportAsJson
// ============================================================================

describe("exportAsJson()", () => {
  it("returns a valid JSON array for a single run record", () => {
    const record = makeRunRecord();
    const output = exportAsJson([record]);

    const parsed = JSON.parse(output);
    expect(Array.isArray(parsed)).toBe(true);
    expect(parsed).toHaveLength(1);
  });

  it("preserves all top-level fields in the output", () => {
    const record = makeRunRecord();
    const output = exportAsJson([record]);
    const [parsed] = JSON.parse(output);

    expect(parsed.schema_version).toBe("2");
    expect(parsed.record_type).toBe("run");
    expect(parsed.issue_number).toBe(42);
    expect(parsed.title).toBe("Add telemetry export feature");
    expect(parsed.outcome_type).toBe("productive");
    expect(parsed.tool_calls).toHaveLength(3);
    expect(parsed.files).toEqual({ read_count: 24, written_count: 7 });
    expect(parsed.routing).toEqual({
      complexity_score: 6,
      path: "standard",
      skip_stages: ["feature-validate"],
    });
  });

  it("returns a valid JSON array for an empty records array", () => {
    const output = exportAsJson([]);
    const parsed = JSON.parse(output);
    expect(parsed).toEqual([]);
  });

  it("includes all record types (runs and outcomes)", () => {
    const run = makeRunRecord();
    const output = exportAsJson([run, outcomeRecord]);
    const parsed = JSON.parse(output);

    expect(parsed).toHaveLength(2);
    expect(parsed[0].record_type).toBe("run");
    expect(parsed[1].record_type).toBe("outcome");
  });

  it("produces pretty-printed JSON with 2-space indentation", () => {
    const record = makeRunRecord();
    const output = exportAsJson([record]);

    // Pretty-printed JSON has newlines and indentation
    expect(output).toContain("\n");
    expect(output).toContain("  ");
  });

  it("handles multiple run records", () => {
    const record1 = makeRunRecord({ issue_number: 10 });
    const record2 = makeRunRecord({ issue_number: 20 });
    const output = exportAsJson([record1, record2]);
    const parsed = JSON.parse(output);

    expect(parsed).toHaveLength(2);
    expect(parsed[0].issue_number).toBe(10);
    expect(parsed[1].issue_number).toBe(20);
  });
});

// ============================================================================
// exportAsCsvRuns
// ============================================================================

describe("exportAsCsvRuns()", () => {
  it("produces the correct header row", () => {
    const output = exportAsCsvRuns([]);
    const lines = output.split("\n");

    expect(lines).toHaveLength(1);
    expect(lines[0]).toBe(
      "issue_number,title,outcome,outcome_type,started_at,completed_at," +
        "duration_ms,total_cost_usd,total_input_tokens,total_output_tokens," +
        "cache_read_tokens,cache_creation_tokens,cache_hit_rate,model," +
        "stage_count,tool_call_count,files_read,files_written," +
        "routing_complexity,routing_path,size,type,priority,execution_mode," +
        "ptc_programmatic_calls,ptc_direct_calls,ptc_programmatic_ratio," +
        "ptc_estimated_tokens_saved,ptc_code_execution_count," +
        "ptc_container_reuse_count"
    );
  });

  it("produces one data row per run record", () => {
    const record1 = makeRunRecord({ issue_number: 10 });
    const record2 = makeRunRecord({ issue_number: 20 });
    const output = exportAsCsvRuns([record1, record2]);
    const lines = output.split("\n");

    // header + 2 data rows
    expect(lines).toHaveLength(3);
  });

  it("filters out outcome records — only run records produce rows", () => {
    const run = makeRunRecord();
    const output = exportAsCsvRuns([run, outcomeRecord]);
    const lines = output.split("\n");

    // header + 1 run row (outcome is excluded)
    expect(lines).toHaveLength(2);
  });

  it("populates data row fields correctly", () => {
    const record = makeRunRecord();
    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    expect(dataRow[0]).toBe("42"); // issue_number
    expect(dataRow[1]).toBe("Add telemetry export feature"); // title (no escaping needed)
    expect(dataRow[2]).toBe("complete"); // outcome
    expect(dataRow[3]).toBe("productive"); // outcome_type
    expect(dataRow[4]).toBe("2026-02-19T10:00:00.000Z"); // started_at
    expect(dataRow[5]).toBe("2026-02-19T10:45:00.000Z"); // completed_at
    expect(dataRow[6]).toBe("2700000"); // duration_ms
    expect(dataRow[7]).toBe("0.123456"); // total_cost_usd
    expect(dataRow[8]).toBe("50000"); // total_input_tokens
    expect(dataRow[9]).toBe("12000"); // total_output_tokens
    expect(dataRow[10]).toBe("25000"); // cache_read_tokens
    expect(dataRow[11]).toBe("8000"); // cache_creation_tokens
    // cache_hit_rate: 25000 / (50000 + 25000) = 0.3333
    expect(dataRow[12]).toBe("0.3333"); // cache_hit_rate
    expect(dataRow[15]).toBe("3"); // tool_call_count
    expect(dataRow[16]).toBe("24"); // files_read
    expect(dataRow[17]).toBe("7"); // files_written
    expect(dataRow[18]).toBe("6"); // routing_complexity
    expect(dataRow[19]).toBe("standard"); // routing_path
    expect(dataRow[20]).toBe("M"); // size
    expect(dataRow[21]).toBe("feature"); // type
    expect(dataRow[22]).toBe("high"); // priority
    expect(dataRow[23]).toBe("automatic"); // execution_mode
  });

  it("selects primary model from feature-dev per_stage token data", () => {
    const record = makeRunRecord();
    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    // model column (index 13) should be from feature-dev token data
    expect(dataRow[13]).toBe("claude-opus-4-6");
  });

  it("falls back to feature-planning model when feature-dev has no model", () => {
    const record = makeRunRecord();
    // Remove model from feature-dev token data AND its stage model_selection
    // so that getPrimaryModel falls through to feature-planning
    const tokens = { ...record.tokens };
    if (tokens.per_stage?.["feature-dev"]) {
      tokens.per_stage = {
        ...tokens.per_stage,
        "feature-dev": { ...tokens.per_stage["feature-dev"], model: undefined },
      };
    }
    const stages = { ...record.stages };
    if (stages["feature-dev"]) {
      stages["feature-dev"] = {
        ...stages["feature-dev"],
        model_selection: undefined,
      };
    }
    const modifiedRecord = { ...record, stages, tokens };

    const output = exportAsCsvRuns([modifiedRecord]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    expect(dataRow[13]).toBe("claude-sonnet-4-6");
  });

  it("falls back to stage model_selection when per_stage token has no model", () => {
    const record = makeRunRecord();
    // Remove per_stage entirely, rely on model_selection in stages
    const tokens = { ...record.tokens, per_stage: undefined };
    const modifiedRecord = { ...record, tokens };

    const output = exportAsCsvRuns([modifiedRecord]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    // Should get model from stages['feature-dev'].model_selection.model
    expect(dataRow[13]).toBe("claude-opus-4-6");
  });

  it("returns empty string for primary model when no model data is present", () => {
    const record = makeRunRecord();
    const stages = { ...record.stages };
    // Remove model_selection from feature-dev and feature-planning stages
    if (stages["feature-dev"]) {
      stages["feature-dev"] = {
        ...stages["feature-dev"],
        model_selection: undefined,
      };
    }
    if (stages["feature-planning"]) {
      stages["feature-planning"] = {
        ...stages["feature-planning"],
        model_selection: undefined,
      };
    }
    const tokens = { ...record.tokens, per_stage: undefined };
    const modifiedRecord = { ...record, stages, tokens };

    const output = exportAsCsvRuns([modifiedRecord]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    expect(dataRow[13]).toBe("");
  });

  it("counts only non-pending stages in stage_count", () => {
    const record = makeRunRecord({
      stages: {
        "issue-pickup": { status: "complete", duration_ms: 1000 },
        "feature-planning": { status: "complete", duration_ms: 2000 },
        "feature-dev": {
          status: "failed",
          duration_ms: 500,
          error: "Build failed",
        },
        "feature-validate": { status: "pending" },
        "pr-create": { status: "pending" },
        "pr-merge": { status: "pending" },
      },
    });

    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    // 3 non-pending stages: complete, complete, failed
    expect(dataRow[14]).toBe("3");
  });

  it("outputs zero tool_call_count when tool_calls is undefined", () => {
    const record = makeRunRecord({ tool_calls: undefined });
    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    expect(dataRow[15]).toBe("0");
  });

  it("outputs empty string for outcome_type when not set", () => {
    const record = makeRunRecord({ outcome_type: undefined });
    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    expect(dataRow[3]).toBe("");
  });

  it("outputs empty string for size/type/priority when not set", () => {
    const record = makeRunRecord({
      size: undefined,
      type: undefined,
      priority: undefined,
    });
    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    expect(dataRow[20]).toBe("");
    expect(dataRow[21]).toBe("");
    expect(dataRow[22]).toBe("");
  });

  it("outputs null size/type/priority as empty string", () => {
    const record = makeRunRecord({ size: null, type: null, priority: null });
    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    // null ?? '' resolves to ''
    expect(dataRow[20]).toBe("");
    expect(dataRow[21]).toBe("");
    expect(dataRow[22]).toBe("");
  });

  it("outputs PTC metrics columns when ptc_metrics is present", () => {
    const record = makeRunRecord();
    // Add ptc_metrics to tokens
    const tokens = {
      ...record.tokens,
      ptc_metrics: {
        total_tool_calls: 15,
        programmatic_calls: 10,
        direct_calls: 5,
        programmatic_ratio: 0.6667,
        estimated_tokens_saved: 5000,
        code_execution_count: 3,
        container_reuse_count: 2,
      },
    };
    const modifiedRecord = { ...record, tokens };

    const output = exportAsCsvRuns([modifiedRecord]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    // PTC columns start at index 24
    expect(dataRow[24]).toBe("10"); // ptc_programmatic_calls
    expect(dataRow[25]).toBe("5"); // ptc_direct_calls
    expect(dataRow[26]).toBe("0.6667"); // ptc_programmatic_ratio
    expect(dataRow[27]).toBe("5000"); // ptc_estimated_tokens_saved
    expect(dataRow[28]).toBe("3"); // ptc_code_execution_count
    expect(dataRow[29]).toBe("2"); // ptc_container_reuse_count
  });

  it("outputs empty PTC columns when ptc_metrics is absent", () => {
    const record = makeRunRecord(); // no ptc_metrics in default fixture
    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const dataRow = lines[1].split(",");

    expect(dataRow[24]).toBe(""); // ptc_programmatic_calls
    expect(dataRow[25]).toBe(""); // ptc_direct_calls
    expect(dataRow[26]).toBe(""); // ptc_programmatic_ratio
    expect(dataRow[27]).toBe(""); // ptc_estimated_tokens_saved
    expect(dataRow[28]).toBe(""); // ptc_code_execution_count
    expect(dataRow[29]).toBe(""); // ptc_container_reuse_count
  });
});

// ============================================================================
// exportAsCsvStages
// ============================================================================

describe("exportAsCsvStages()", () => {
  it("produces the correct header row", () => {
    const output = exportAsCsvStages([]);
    const lines = output.split("\n");

    expect(lines).toHaveLength(1);
    expect(lines[0]).toBe(
      "issue_number,stage,status,duration_ms,input_tokens,output_tokens," +
        "cache_read,cache_creation,cost_usd,model,model_source," +
        "context_file_size_bytes,error"
    );
  });

  it("produces one row per stage per run", () => {
    const record = makeRunRecord();
    const stageCount = Object.keys(record.stages).length;
    const output = exportAsCsvStages([record]);
    const lines = output.split("\n");

    // header + one row per stage
    expect(lines).toHaveLength(1 + stageCount);
  });

  it("produces rows for all stages across multiple run records", () => {
    const record1 = makeRunRecord({ issue_number: 10 });
    const record2 = makeRunRecord({ issue_number: 20 });
    const stages1 = Object.keys(record1.stages).length;
    const stages2 = Object.keys(record2.stages).length;
    const output = exportAsCsvStages([record1, record2]);
    const lines = output.split("\n");

    expect(lines).toHaveLength(1 + stages1 + stages2);
  });

  it("filters out outcome records — only run records produce rows", () => {
    const run = makeRunRecord();
    const stageCount = Object.keys(run.stages).length;
    const output = exportAsCsvStages([run, outcomeRecord]);
    const lines = output.split("\n");

    expect(lines).toHaveLength(1 + stageCount);
  });

  it("populates per-stage token data from per_stage when available", () => {
    const record = makeRunRecord();
    const output = exportAsCsvStages([record]);
    const lines = output.split("\n");

    // Find the feature-dev row
    const featureDevRow = lines.find((l) => l.split(",")[1] === "feature-dev");
    expect(featureDevRow).toBeDefined();

    const cols = featureDevRow!.split(",");
    expect(cols[0]).toBe("42"); // issue_number
    expect(cols[1]).toBe("feature-dev"); // stage
    expect(cols[2]).toBe("complete"); // status
    expect(cols[3]).toBe("1200000"); // duration_ms
    expect(cols[4]).toBe("28000"); // input_tokens
    expect(cols[5]).toBe("7000"); // output_tokens
    expect(cols[6]).toBe("15000"); // cache_read
    expect(cols[7]).toBe("4500"); // cache_creation
    expect(cols[8]).toBe("0.075000"); // cost_usd
    expect(cols[9]).toBe("claude-opus-4-6"); // model
    expect(cols[10]).toBe("env"); // model_source
    expect(cols[11]).toBe("32768"); // context_file_size_bytes
    expect(cols[12]).toBe(""); // error (none)
  });

  it("falls back to stage model_selection when per_stage is missing for a stage", () => {
    const record = makeRunRecord();
    // Remove per_stage entirely to force fallback to model_selection
    const tokens = { ...record.tokens, per_stage: undefined };
    const modifiedRecord = { ...record, tokens };

    const output = exportAsCsvStages([modifiedRecord]);
    const lines = output.split("\n");

    const featureDevRow = lines.find((l) => l.split(",")[1] === "feature-dev");
    expect(featureDevRow).toBeDefined();

    const cols = featureDevRow!.split(",");
    // Token columns are empty when per_stage is absent
    expect(cols[4]).toBe(""); // input_tokens
    expect(cols[5]).toBe(""); // output_tokens
    // model and model_source come from model_selection
    expect(cols[9]).toBe("claude-opus-4-6");
    expect(cols[10]).toBe("env");
  });

  it("outputs empty strings for missing token data on a stage", () => {
    // A stage that has no per_stage entry and no model_selection
    const record = makeRunRecord({
      stages: {
        "pr-merge": {
          status: "complete",
          duration_ms: 120000,
        },
      },
    });
    const tokens = { ...record.tokens, per_stage: undefined };
    const modifiedRecord = { ...record, tokens };

    const output = exportAsCsvStages([modifiedRecord]);
    const lines = output.split("\n");

    // header + 1 stage row
    expect(lines).toHaveLength(2);
    const cols = lines[1].split(",");
    expect(cols[4]).toBe(""); // input_tokens
    expect(cols[5]).toBe(""); // output_tokens
    expect(cols[6]).toBe(""); // cache_read
    expect(cols[7]).toBe(""); // cache_creation
    expect(cols[8]).toBe(""); // cost_usd
    expect(cols[9]).toBe(""); // model
    expect(cols[10]).toBe(""); // model_source
  });

  it("outputs the error field when a stage has an error", () => {
    const record = makeRunRecord({
      stages: {
        "feature-dev": {
          status: "failed",
          duration_ms: 500,
          error: "Build timed out",
        },
      },
    });

    const output = exportAsCsvStages([record]);
    const lines = output.split("\n");
    const featureDevRow = lines.find((l) => l.split(",")[1] === "feature-dev");
    expect(featureDevRow).toBeDefined();

    const cols = featureDevRow!.split(",");
    expect(cols[12]).toBe("Build timed out");
  });

  it("outputs empty context_file_size_bytes when not set", () => {
    const record = makeRunRecord({
      stages: {
        "pr-create": {
          status: "complete",
          duration_ms: 180000,
          // no context_file_size_bytes
        },
      },
    });

    const output = exportAsCsvStages([record]);
    const lines = output.split("\n");

    const prCreateRow = lines.find((l) => l.split(",")[1] === "pr-create");
    expect(prCreateRow).toBeDefined();

    const cols = prCreateRow!.split(",");
    expect(cols[11]).toBe("");
  });
});

// ============================================================================
// CSV escaping
// ============================================================================

describe("CSV escaping", () => {
  describe("exportAsCsvRuns() title escaping", () => {
    it("wraps title containing a comma in double quotes", () => {
      const record = makeRunRecord({
        title: "Add feature, fix bug",
      });

      const output = exportAsCsvRuns([record]);
      const lines = output.split("\n");

      // The second column should be the quoted title
      expect(lines[1]).toContain('"Add feature, fix bug"');
    });

    it("escapes double quotes in title by doubling them", () => {
      const record = makeRunRecord({
        title: 'Add "telemetry" support',
      });

      const output = exportAsCsvRuns([record]);
      const lines = output.split("\n");

      expect(lines[1]).toContain('"Add ""telemetry"" support"');
    });

    it("wraps title containing a newline in double quotes", () => {
      const record = makeRunRecord({
        title: "Add feature\nwith newline",
      });

      const output = exportAsCsvRuns([record]);

      // Cannot split on '\n' here because the quoted field itself contains '\n'.
      // Check the full output string instead.
      expect(output).toContain('"Add feature\nwith newline"');
    });

    it("does not quote a plain title with no special characters", () => {
      const record = makeRunRecord({
        title: "Simple title",
      });

      const output = exportAsCsvRuns([record]);
      const lines = output.split("\n");

      // Should not be wrapped in quotes
      expect(lines[1]).toContain(",Simple title,");
    });
  });

  describe("exportAsCsvRuns() routing_path escaping", () => {
    it("wraps routing path containing a comma in double quotes", () => {
      const record = makeRunRecord({
        routing: {
          complexity_score: 3,
          path: "standard,fast",
          skip_stages: [],
        },
      });

      const output = exportAsCsvRuns([record]);
      const lines = output.split("\n");

      expect(lines[1]).toContain('"standard,fast"');
    });
  });

  describe("exportAsCsvStages() error field escaping", () => {
    it("wraps error containing a comma in double quotes", () => {
      const record = makeRunRecord({
        stages: {
          "feature-dev": {
            status: "failed",
            duration_ms: 1000,
            error: "Error: build failed, exit code 1",
          },
        },
      });

      const output = exportAsCsvStages([record]);
      const lines = output.split("\n");
      const featureDevRow = lines.find((l) => l.split(",")[1] === "feature-dev");
      expect(featureDevRow).toBeDefined();

      expect(featureDevRow!).toContain('"Error: build failed, exit code 1"');
    });

    it("escapes double quotes in error by doubling them", () => {
      const record = makeRunRecord({
        stages: {
          "feature-dev": {
            status: "failed",
            duration_ms: 1000,
            error: 'Error: "unexpected" token',
          },
        },
      });

      const output = exportAsCsvStages([record]);
      const lines = output.split("\n");
      const featureDevRow = lines.find((l) => l.split(",")[1] === "feature-dev");
      expect(featureDevRow).toBeDefined();

      expect(featureDevRow!).toContain('"Error: ""unexpected"" token"');
    });
  });
});

// ============================================================================
// Empty records array
// ============================================================================

describe("empty records array", () => {
  it("exportAsCsvRuns() returns header-only CSV with no newline", () => {
    const output = exportAsCsvRuns([]);

    expect(output).not.toContain("\n");
    expect(output.startsWith("issue_number,")).toBe(true);
  });

  it("exportAsCsvStages() returns header-only CSV with no newline", () => {
    const output = exportAsCsvStages([]);

    expect(output).not.toContain("\n");
    expect(output.startsWith("issue_number,")).toBe(true);
  });

  it("exportAsJson() returns an empty JSON array", () => {
    const output = exportAsJson([]);
    expect(JSON.parse(output)).toEqual([]);
  });
});

// ============================================================================
// v2 normalized records (missing optional fields)
// ============================================================================

describe("v2 normalized records (minimal optional fields)", () => {
  /**
   * A v2 record that was normalized from v1 — outcome_type, tool_calls,
   * size/type/priority, and per_stage tokens are all absent.
   */
  const minimalRecord: ExecutionHistoryRunRecordV2 = {
    schema_version: "2",
    record_type: "run",
    issue_number: 99,
    title: "Minimal v1-normalized record",
    branch: "feat/99-minimal",
    base_branch: "main",
    execution_mode: "manual",
    started_at: "2026-01-01T08:00:00.000Z",
    completed_at: "2026-01-01T08:30:00.000Z",
    total_duration_ms: 1800000,
    outcome: "complete",
    // outcome_type absent
    // tool_calls absent
    // size/type/priority absent
    // labels absent
    stages: {
      "feature-dev": {
        status: "complete",
        duration_ms: 1800000,
        // no model_selection
        // no context_file_size_bytes
      },
    },
    tokens: {
      total_input: 10000,
      total_output: 2000,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0.05,
      // per_stage absent
    },
    files: {
      read_count: 0,
      written_count: 0,
    },
    routing: {
      complexity_score: 0,
      path: "unknown",
      skip_stages: [],
    },
    recorded_at: "2026-01-01T08:30:00.000Z",
  };

  it("exportAsCsvRuns() handles a minimal record without errors", () => {
    const output = exportAsCsvRuns([minimalRecord]);
    const lines = output.split("\n");

    expect(lines).toHaveLength(2);
    const cols = lines[1].split(",");
    expect(cols[0]).toBe("99");
    expect(cols[2]).toBe("complete"); // outcome
    expect(cols[3]).toBe(""); // outcome_type — absent
    expect(cols[13]).toBe(""); // model — none available
    expect(cols[15]).toBe("0"); // tool_call_count — no tool_calls
    expect(cols[16]).toBe("0"); // files_read
    expect(cols[17]).toBe("0"); // files_written
    expect(cols[18]).toBe("0"); // routing_complexity
    expect(cols[19]).toBe("unknown"); // routing_path
    expect(cols[20]).toBe(""); // size — absent
    expect(cols[21]).toBe(""); // type — absent
    expect(cols[22]).toBe(""); // priority — absent
  });

  it("exportAsCsvStages() handles a minimal record without errors", () => {
    const output = exportAsCsvStages([minimalRecord]);
    const lines = output.split("\n");

    // header + 1 stage
    expect(lines).toHaveLength(2);
    const cols = lines[1].split(",");
    expect(cols[0]).toBe("99");
    expect(cols[1]).toBe("feature-dev");
    expect(cols[2]).toBe("complete");
    expect(cols[3]).toBe("1800000");
    // All token columns are empty since per_stage is absent
    expect(cols[4]).toBe("");
    expect(cols[5]).toBe("");
    expect(cols[6]).toBe("");
    expect(cols[7]).toBe("");
    expect(cols[8]).toBe("");
    // model and model_source are empty since no per_stage or model_selection
    expect(cols[9]).toBe("");
    expect(cols[10]).toBe("");
    expect(cols[11]).toBe(""); // context_file_size_bytes
    expect(cols[12]).toBe(""); // error
  });
});

// ============================================================================
// cache_hit_rate calculation
// ============================================================================

describe("cache_hit_rate calculation", () => {
  it("calculates cache_hit_rate as cache_read / (total_input + cache_read)", () => {
    // total_input=75000, cache_read=25000 → 25000 / 100000 = 0.2500
    const record = makeRunRecord({
      tokens: {
        total_input: 75000,
        total_output: 10000,
        total_cache_read: 25000,
        total_cache_creation: 5000,
        estimated_cost_usd: 0.1,
      },
    });

    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const cols = lines[1].split(",");

    expect(cols[12]).toBe("0.2500");
  });

  it("returns 0.0000 cache_hit_rate when both total_input and cache_read are zero", () => {
    const record = makeRunRecord({
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    });

    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const cols = lines[1].split(",");

    expect(cols[12]).toBe("0.0000");
  });

  it("returns 1.0000 cache_hit_rate when all tokens are from cache (total_input = 0)", () => {
    // total_input=0, cache_read=50000 → 50000 / (0 + 50000) = 1.0000
    const record = makeRunRecord({
      tokens: {
        total_input: 0,
        total_output: 5000,
        total_cache_read: 50000,
        total_cache_creation: 0,
        estimated_cost_usd: 0.02,
      },
    });

    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const cols = lines[1].split(",");

    expect(cols[12]).toBe("1.0000");
  });

  it("outputs cache_hit_rate rounded to 4 decimal places", () => {
    // total_input=100000, cache_read=33333 → 33333 / 133333 ≈ 0.24999...
    const record = makeRunRecord({
      tokens: {
        total_input: 100000,
        total_output: 10000,
        total_cache_read: 33333,
        total_cache_creation: 5000,
        estimated_cost_usd: 0.1,
      },
    });

    const output = exportAsCsvRuns([record]);
    const lines = output.split("\n");
    const cols = lines[1].split(",");

    // Verify it's exactly 4 decimal places
    expect(cols[12]).toMatch(/^\d+\.\d{4}$/);
  });
});

// ============================================================================
// Real-data fixture tests (#2794)
// ============================================================================

const FIXTURE_DIR = path.join(__dirname, "../fixtures/telemetry");

async function loadFixture(filename: string) {
  const filePath = path.join(FIXTURE_DIR, filename);
  return ExecutionHistoryReader.parseJsonlFile(filePath);
}

describe("Real-data fixture tests (#2794)", () => {
  beforeEach(() => {
    ExecutionHistoryReader.clearCache();
  });

  describe("health-history-multi-run.jsonl — exportAsCsvRuns", () => {
    it("loads 4 run records from fixture", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const runs = records.filter((r) => r.record_type === "run");
      expect(runs).toHaveLength(4);
    });

    it("all run records have non-zero total_cost_usd", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1); // skip header
      expect(lines).toHaveLength(4);
      for (const line of lines) {
        const cols = line.split(",");
        const cost = parseFloat(cols[7]); // total_cost_usd
        expect(cost).toBeGreaterThan(0);
      }
    });

    it("all run records have non-zero token counts", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1);
      for (const line of lines) {
        const cols = line.split(",");
        const inputTokens = parseInt(cols[8], 10); // total_input_tokens
        const outputTokens = parseInt(cols[9], 10); // total_output_tokens
        expect(inputTokens).toBeGreaterThan(0);
        expect(outputTokens).toBeGreaterThan(0);
      }
    });

    it("primary model column is non-empty for all runs", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1);
      for (const line of lines) {
        const cols = line.split(",");
        expect(cols[13]).not.toBe(""); // model
      }
    });

    it("run #101 uses opus model from feature-dev per_stage", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1);
      const opusLine = lines.find((l) => l.startsWith("101,"));
      expect(opusLine).toBeDefined();
      const cols = opusLine!.split(",");
      expect(cols[13]).toBe("claude-opus-4-7");
    });

    it("failed run #102 has outcome=failed and non-zero cost", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1);
      const failedLine = lines.find((l) => l.startsWith("102,"));
      expect(failedLine).toBeDefined();
      const cols = failedLine!.split(",");
      expect(cols[2]).toBe("failed"); // outcome
      expect(parseFloat(cols[7])).toBeGreaterThan(0); // total_cost_usd
    });

    it("routing_complexity column is non-zero for all runs", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1);
      for (const line of lines) {
        const cols = line.split(",");
        expect(parseInt(cols[18], 10)).toBeGreaterThan(0); // routing_complexity
      }
    });

    it("files_read and files_written are non-zero for complete runs", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1);
      // Runs #100, #101, #102, #103 all have files data
      for (const line of lines) {
        const cols = line.split(",");
        expect(parseInt(cols[16], 10)).toBeGreaterThan(0); // files_read
        expect(parseInt(cols[17], 10)).toBeGreaterThan(0); // files_written
      }
    });
  });

  describe("health-history-multi-run.jsonl — exportAsCsvStages", () => {
    it("produces one row per stage per run (no phantom zeros from missing per_stage)", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvStages(records);
      const lines = csv.split("\n").slice(1); // skip header
      expect(lines.length).toBeGreaterThan(0);
    });

    it("feature-dev stage rows have non-zero input_tokens", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvStages(records);
      const lines = csv.split("\n").slice(1);
      const devRows = lines.filter((l) => l.split(",")[1] === "feature-dev");
      expect(devRows.length).toBeGreaterThan(0);
      for (const row of devRows) {
        const cols = row.split(",");
        expect(parseInt(cols[4], 10)).toBeGreaterThan(0); // input_tokens
        expect(parseInt(cols[5], 10)).toBeGreaterThan(0); // output_tokens
        expect(parseFloat(cols[8])).toBeGreaterThan(0); // cost_usd
      }
    });

    it("feature-dev stage rows have non-empty model column", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvStages(records);
      const lines = csv.split("\n").slice(1);
      const devRows = lines.filter((l) => l.split(",")[1] === "feature-dev");
      for (const row of devRows) {
        const cols = row.split(",");
        expect(cols[9]).not.toBe(""); // model
      }
    });

    it("failed stage row includes error message", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvStages(records);
      const lines = csv.split("\n");
      // Run #102 has a failed feature-dev stage with an error message
      const failedRow = lines.find(
        (l) => l.split(",")[0] === "102" && l.split(",")[1] === "feature-dev"
      );
      expect(failedRow).toBeDefined();
      const cols = failedRow!.split(",");
      expect(cols[2]).toBe("failed"); // status
      // error column should contain the TypeScript error message (last col, may be quoted)
      expect(failedRow).toContain("TypeScript compilation failed");
    });

    it("issue-pickup stage rows have duration_ms populated", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvStages(records);
      const lines = csv.split("\n").slice(1);
      const pickupRows = lines.filter((l) => l.split(",")[1] === "issue-pickup");
      for (const row of pickupRows) {
        const cols = row.split(",");
        expect(parseInt(cols[3], 10)).toBeGreaterThan(0); // duration_ms
      }
    });
  });

  describe("health-history-multi-run.jsonl — exportAsJson", () => {
    it("produces valid JSON preserving all 4 records", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const json = exportAsJson(records);
      const parsed = JSON.parse(json);
      expect(Array.isArray(parsed)).toBe(true);
      expect(parsed).toHaveLength(4);
    });

    it("all records have non-zero estimated_cost_usd in JSON output", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const json = exportAsJson(records);
      const parsed = JSON.parse(json) as ExecutionHistoryRunRecordV2[];
      for (const record of parsed) {
        expect(record.tokens.estimated_cost_usd).toBeGreaterThan(0);
      }
    });

    it("per_stage data is preserved in JSON output", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const json = exportAsJson(records);
      const parsed = JSON.parse(json) as ExecutionHistoryRunRecordV2[];
      const run100 = parsed.find((r) => r.issue_number === 100);
      expect(run100).toBeDefined();
      expect(run100!.tokens.per_stage?.["feature-dev"]?.input).toBe(38000);
      expect(run100!.tokens.per_stage?.["feature-dev"]?.cost_usd).toBeGreaterThan(0);
    });
  });

  describe("health-history-edge-cases.jsonl", () => {
    it("loads records including v1-normalized record", async () => {
      const records = await loadFixture("health-history-edge-cases.jsonl");
      const runs = records.filter((r) => r.record_type === "run");
      expect(runs).toHaveLength(3);
    });

    it("v1 record (#202) is normalized to v2 shape", async () => {
      const records = await loadFixture("health-history-edge-cases.jsonl");
      const v1Run = records.find((r) => r.record_type === "run" && r.issue_number === 202);
      expect(v1Run).toBeDefined();
      // After normalization, schema_version should be "2"
      expect((v1Run as ExecutionHistoryRunRecordV2).schema_version).toBe("2");
    });

    it("run without per_stage (#200) still exports as CSV without errors", async () => {
      const records = await loadFixture("health-history-edge-cases.jsonl");
      const run200 = records.filter((r) => r.record_type === "run" && r.issue_number === 200);
      expect(() => exportAsCsvRuns(run200)).not.toThrow();
      expect(() => exportAsCsvStages(run200)).not.toThrow();
      const csv = exportAsCsvRuns(run200);
      const lines = csv.split("\n").slice(1);
      expect(lines).toHaveLength(1);
      const cols = lines[0].split(",");
      expect(parseFloat(cols[7])).toBeGreaterThan(0); // total_cost_usd still non-zero
    });

    it("run with auto_retry (#201) has non-zero tokens in stage CSV rows", async () => {
      const records = await loadFixture("health-history-edge-cases.jsonl");
      const csv = exportAsCsvStages(records);
      const lines = csv.split("\n").slice(1);
      const devRow = lines.find(
        (l) => l.split(",")[0] === "201" && l.split(",")[1] === "feature-dev"
      );
      expect(devRow).toBeDefined();
      const cols = devRow!.split(",");
      expect(parseInt(cols[4], 10)).toBeGreaterThan(0); // input_tokens from per_stage
    });

    it("title with special chars is CSV-escaped correctly", async () => {
      const records = await loadFixture("health-history-edge-cases.jsonl");
      // Run #201 title contains commas: "Fix: race condition in pipeline state, fix: retry logic"
      const csv = exportAsCsvRuns(records);
      const lines = csv.split("\n").slice(1);
      const line201 = lines.find((l) => l.startsWith("201,"));
      expect(line201).toBeDefined();
      // Title with comma must be quoted
      expect(line201).toContain('"Fix: race condition in pipeline state, fix: retry logic"');
    });
  });

  describe("filter coverage — date range and record count", () => {
    it("runs from multi-run fixture span April 1–10 (7+ day range)", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const runs = records.filter((r) => r.record_type === "run") as ExecutionHistoryRunRecordV2[];
      const dates = runs.map((r) => new Date(r.started_at));
      const earliest = new Date(Math.min(...dates.map((d) => d.getTime())));
      const latest = new Date(Math.max(...dates.map((d) => d.getTime())));
      const daysDiff = (latest.getTime() - earliest.getTime()) / (1000 * 60 * 60 * 24);
      expect(daysDiff).toBeGreaterThanOrEqual(7);
    });

    it("multi-run fixture includes all three outcome types: complete, failed", async () => {
      const records = await loadFixture("health-history-multi-run.jsonl");
      const runs = records.filter((r) => r.record_type === "run") as ExecutionHistoryRunRecordV2[];
      const outcomes = new Set(runs.map((r) => r.outcome));
      expect(outcomes.has("complete")).toBe(true);
      expect(outcomes.has("failed")).toBe(true);
    });

    it("multi-run fixture primary models include sonnet and opus (from feature-dev per_stage)", async () => {
      // getPrimaryModel picks feature-dev first, then feature-planning.
      // Runs #100/#102/#103 use sonnet in feature-dev; run #101 uses opus.
      // Haiku is only used in issue-pickup/pr-create/pr-merge stages, not primary.
      const records = await loadFixture("health-history-multi-run.jsonl");
      const csv = exportAsCsvRuns(records);
      const models = csv
        .split("\n")
        .slice(1)
        .map((l) => l.split(",")[13]);
      expect(models.some((m) => m.includes("sonnet"))).toBe(true);
      expect(models.some((m) => m.includes("opus"))).toBe(true);
    });
  });
});
