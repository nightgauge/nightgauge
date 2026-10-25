/**
 * Execution-history feeder — context-window utilization (#1653).
 *
 * Go writes `stages[<name>].context_window_utilization` (peak single-step
 * prompt ÷ the window the stage ran with). The feeder is the one mapper from
 * run records to analyzer records, so it is where the value must become
 * `contextWindowUtilization`, the field
 * `TokenEfficiencyAnalyzer.detectContextWindowUtilization` reads.
 */

import { describe, expect, it } from "vitest";
import { TokenEfficiencyAnalyzer } from "../../TokenEfficiencyAnalyzer.js";
import {
  flattenRunRecords,
  MAX_PLAUSIBLE_STAGE_DURATION_MS,
  type HistoryRunRecordInput,
} from "../executionHistoryFeeder.js";

function runRecord(issueNumber: number, utilization?: number): HistoryRunRecordInput {
  return {
    record_type: "run",
    issue_number: issueNumber,
    started_at: "2026-09-16T00:00:00Z",
    stages: {
      "feature-dev": {
        status: "complete",
        started_at: "2026-09-16T00:00:00Z",
        duration_ms: 60_000,
        model_selection: { model: "qwen/qwen3.8-27b", source: "scheduler", adapter: "opencode" },
        ...(utilization === undefined ? {} : { context_window_utilization: utilization }),
      },
    },
    tokens: { per_stage: { "feature-dev": { input: 12_010, output: 400, cost_usd: 0.5 } } },
  };
}

describe("flattenRunRecords — context_window_utilization (#1653)", () => {
  it("maps context_window_utilization to contextWindowUtilization", () => {
    const [record] = flattenRunRecords([runRecord(1653, 0.93)]);
    expect(record.contextWindowUtilization).toBe(0.93);
  });

  it("leaves the field absent when the stage carries no utilization", () => {
    const [record] = flattenRunRecords([runRecord(1653)]);
    expect("contextWindowUtilization" in record).toBe(false);
  });

  it("gives the analyzer a low-utilization pattern for a stage group below the minimum, informational only (#2017)", () => {
    const records = flattenRunRecords([runRecord(1, 0.93), runRecord(2, 0.93), runRecord(3, 0.93)]);
    const analyzer = new TokenEfficiencyAnalyzer({
      thresholds: { contextUtilizationMinimum: 0.95, contextNearWindowThreshold: 0.99 },
    });
    const patterns = analyzer.detectContextWindowUtilization(records);
    expect(patterns).toHaveLength(1);
    expect(patterns[0].category).toBe("context-window-utilization");
    expect(patterns[0].title).toContain("Low context window utilization");
    expect(patterns[0].severity).toBe("info");
    expect(patterns[0].affectedStages).toEqual(["feature-dev"]);
    expect(patterns[0].evidence.avgUtilization).toBe(0.93);
    expect(patterns[0].evidence.recordCount).toBe(3);
    // Low utilization is informational only — it must not book wasted tokens
    // or savings, since a small prompt against a large window is not waste.
    expect(patterns[0].wastedTokens).toBe(0);
    expect(patterns[0].estimatedSavingsUsd).toBe(0);
  });
});

describe("flattenRunRecords — near-window pattern (#2017)", () => {
  it("gives the analyzer a near-window pattern naming the stage and model when the median crosses the threshold", () => {
    const records = flattenRunRecords([runRecord(1, 0.93), runRecord(2, 0.9), runRecord(3, 0.95)]);
    const analyzer = new TokenEfficiencyAnalyzer({
      thresholds: { contextNearWindowThreshold: 0.8 },
    });
    const patterns = analyzer.detectContextWindowUtilization(records);
    expect(patterns).toHaveLength(1);
    expect(patterns[0].category).toBe("context-window-utilization");
    expect(patterns[0].affectedStages).toEqual(["feature-dev"]);
    expect(patterns[0].title).toContain("feature-dev");
    expect(patterns[0].title).toContain("qwen/qwen3.8-27b");
    expect(patterns[0].evidence.model).toBe("qwen/qwen3.8-27b");
    expect(patterns[0].evidence.medianUtilization).toBe(0.93);
    // Near-window is a risk signal, not waste — no tokens/savings booked.
    expect(patterns[0].wastedTokens).toBe(0);
    expect(patterns[0].estimatedSavingsUsd).toBe(0);
  });

  it("does not flag a stage group whose median utilization stays below the near-window threshold", () => {
    const records = flattenRunRecords([runRecord(1, 0.5), runRecord(2, 0.55), runRecord(3, 0.6)]);
    const analyzer = new TokenEfficiencyAnalyzer();
    const patterns = analyzer.detectContextWindowUtilization(records);
    expect(patterns).toHaveLength(0);
  });

  it("real-world low-utilization case (12k of 131k) contributes nothing to overallEfficiencyScore (#2017)", () => {
    const records = flattenRunRecords([
      runRecord(1, 12_010 / 131_072),
      runRecord(2, 12_010 / 131_072),
      runRecord(3, 12_010 / 131_072),
      runRecord(4, 12_010 / 131_072),
      runRecord(5, 12_010 / 131_072),
    ]);
    const analyzer = new TokenEfficiencyAnalyzer();
    const analysis = analyzer.analyze(records);
    const contextPatterns = analysis.wastePatterns.filter(
      (p) => p.category === "context-window-utilization"
    );
    expect(contextPatterns).toHaveLength(1);
    expect(contextPatterns[0].title).toContain("Low context window utilization");
    expect(contextPatterns[0].wastedTokens).toBe(0);
    expect(contextPatterns[0].estimatedSavingsUsd).toBe(0);
    // Only the low-utilization signal for this category is under test here;
    // it must not itself book any wasted tokens toward the overall score,
    // regardless of what other categories (e.g. cache-miss) contribute.
    expect(analysis.summary.categorySummary["context-window-utilization"].totalWastedTokens).toBe(
      0
    );
  });
});

describe("flattenRunRecords — corrupt stage rows (#2536)", () => {
  function withStage(name: string, durationMs: number): HistoryRunRecordInput {
    return {
      record_type: "run",
      issue_number: 2536,
      started_at: "2026-10-25T00:00:00Z",
      stages: {
        "feature-dev": { status: "complete", duration_ms: 60_000 },
        [name]: { status: "complete", duration_ms: durationMs },
      },
    };
  }

  it("drops an empty stage key", () => {
    const records = flattenRunRecords([withStage("", 1_000)]);
    expect(records.map((r) => r.stage)).toEqual(["feature-dev"]);
  });

  it("drops a negative duration", () => {
    const records = flattenRunRecords([withStage("feature-planning", -1)]);
    expect(records.map((r) => r.stage)).toEqual(["feature-dev"]);
  });

  it("drops a saturated MaxInt64 duration (ns booked as ms)", () => {
    const records = flattenRunRecords([withStage("feature-planning", 9_223_372_036_854)]);
    expect(records.map((r) => r.stage)).toEqual(["feature-dev"]);
  });

  it("keeps a duration at the plausibility ceiling", () => {
    const records = flattenRunRecords([
      withStage("feature-planning", MAX_PLAUSIBLE_STAGE_DURATION_MS),
    ]);
    expect(records.map((r) => r.stage)).toEqual(["feature-dev", "feature-planning"]);
  });

  it("never yields an empty-named stage from a polluted history", () => {
    const records = flattenRunRecords([
      withStage("", 9_223_372_036_854),
      runRecord(1),
      withStage("pr-create", 30_000),
    ]);
    expect(records.some((r) => r.stage.trim() === "")).toBe(false);
    expect(records.length).toBe(4);
  });
});
