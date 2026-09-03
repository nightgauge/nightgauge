/**
 * Model Routing dimension — fed by the production mapper (#461).
 *
 * The dimension filters on `record.selectionSource`, which no health-path
 * feeder populated before #461: the health mappers built one record per RUN
 * with neither `model` nor `selectionSource`, so the dimension reported
 * "no data" for every corpus. These tests build the dimension's input the way
 * production does — `flattenRunRecords` over JSONL run records — and assert
 * the dimension actually scores it.
 */

import { describe, it, expect } from "vitest";
import { analyzeModelRouting } from "../modelRouting.js";
import { flattenRunRecords } from "../../executionHistoryFeeder.js";
import { DEFAULT_HEALTH_CONFIG, type HealthAnalysisConfig } from "../../types.js";
import type { HealthAnalysisInput } from "../../types.js";
import { AUTOMATIC_MODEL_SELECTION_SOURCE } from "../../../types.js";

/** A JSONL run record as the Go history writer emits it (subset). */
function routedRunRecord() {
  return {
    schema_version: "2",
    record_type: "run",
    issue_number: 461,
    started_at: "2026-09-01T10:00:00Z",
    outcome: "complete",
    stages: {
      "feature-dev": {
        status: "complete",
        started_at: "2026-09-01T10:05:00Z",
        duration_ms: 600_000,
        model_selection: {
          model: "claude-sonnet-4-5",
          source: AUTOMATIC_MODEL_SELECTION_SOURCE,
          mode: "automatic",
          confidence: 0.8,
          complexity: "M",
          adapter: "claude",
        },
      },
      "pr-create": {
        status: "complete",
        started_at: "2026-09-01T10:20:00Z",
        duration_ms: 60_000,
        model_selection: {
          model: "claude-haiku-4-5",
          source: AUTOMATIC_MODEL_SELECTION_SOURCE,
          mode: "automatic",
          confidence: 0.9,
          complexity: "S",
          adapter: "claude",
        },
      },
      // Skipped: dispatched no model, must not become a record.
      "feature-validate": { status: "skipped", skip_reason: "docs-only" },
    },
    tokens: {
      per_stage: {
        "feature-dev": { input: 40_000, output: 8_000, cost_usd: 0.4 },
        "pr-create": { input: 5_000, output: 500, cost_usd: 0.02 },
      },
    },
  };
}

function inputFrom(records: Array<Record<string, unknown>>): HealthAnalysisInput {
  return {
    executionHistory: flattenRunRecords(records),
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
  };
}

const config: HealthAnalysisConfig = {
  ...DEFAULT_HEALTH_CONFIG,
  minimumSampleSizes: { ...DEFAULT_HEALTH_CONFIG.minimumSampleSizes, basic: 2 },
};

describe("analyzeModelRouting fed by flattenRunRecords", () => {
  it("reports non-empty data for a corpus produced by the real feeder", () => {
    const input = inputFrom([routedRunRecord()]);

    // The feeder populated the fields the dimension filters on.
    expect(input.executionHistory).toHaveLength(2);
    for (const record of input.executionHistory) {
      expect(record.selectionSource).toBe(AUTOMATIC_MODEL_SELECTION_SOURCE);
      expect(record.model).toBeDefined();
    }

    const result = analyzeModelRouting(input, config);

    expect(result.hasEnoughData).toBe(true);
    expect(result.sampleSize).toBe(2);
    expect(result.metrics["autoSelectionTotal"]).toBe(2);
    expect(result.metrics["distinctModelCount"]).toBe(2);
    expect(result.metrics["model.claude-sonnet-4-5.sampleSize"]).toBe(1);
    expect(result.metrics["model.claude-haiku-4-5.sampleSize"]).toBe(1);
    expect(Object.keys(result.metrics).some((k) => k.startsWith("model.unknown."))).toBe(false);
  });

  it("ignores non-run records and stages without a model_selection block still count for cost", () => {
    const run = routedRunRecord();
    (run.stages as Record<string, unknown>)["pipeline-finish"] = {
      status: "complete",
      duration_ms: 10,
    };
    const input = inputFrom([
      { record_type: "outcome", issue_number: 461, outcome: "merged" },
      run,
    ]);

    expect(input.executionHistory).toHaveLength(3);
    const unrouted = input.executionHistory.find((r) => r.stage === "pipeline-finish");
    expect(unrouted?.selectionSource).toBeUndefined();
    expect(unrouted?.model).toBeUndefined();

    const result = analyzeModelRouting(input, config);
    // Two routed records feed the auto-selection population; the unrouted
    // one buckets as "unknown" and never counts as auto-selected.
    expect(result.metrics["autoSelectionTotal"]).toBe(2);
    expect(result.metrics["model.unknown.sampleSize"]).toBe(1);
  });
});
