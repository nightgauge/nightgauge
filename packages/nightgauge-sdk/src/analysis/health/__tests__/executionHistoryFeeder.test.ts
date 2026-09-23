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
import { flattenRunRecords, type HistoryRunRecordInput } from "../executionHistoryFeeder.js";

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

  it("gives the analyzer a context-window pattern for a stage group across its threshold", () => {
    const records = flattenRunRecords([runRecord(1, 0.93), runRecord(2, 0.93), runRecord(3, 0.93)]);
    const analyzer = new TokenEfficiencyAnalyzer({
      thresholds: { contextUtilizationMinimum: 0.95 },
    });
    const patterns = analyzer.detectContextWindowUtilization(records);
    expect(patterns).toHaveLength(1);
    expect(patterns[0].category).toBe("context-window-utilization");
    expect(patterns[0].affectedStages).toEqual(["feature-dev"]);
    expect(patterns[0].evidence.avgUtilization).toBe(0.93);
    expect(patterns[0].evidence.recordCount).toBe(3);
  });
});
