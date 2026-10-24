/**
 * The one health time-series entry (#1898): what the engine computed, and
 * nothing it did not.
 *
 * A dimension without data must not be persisted as its placeholder score, and
 * a run whose every dimension was starved must be recorded as `null` — not
 * omitted, and not coerced to a number.
 */

import { describe, it, expect } from "vitest";
import { buildHealthTrendEntry } from "../../../src/analysis/health/healthSeries.js";
import { HealthAnalysisEngine } from "../../../src/analysis/health/HealthAnalysisEngine.js";
import type {
  DimensionResult,
  HealthAnalysisInput,
  HealthAnalysisResult,
  HealthDimension,
} from "../../../src/analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../src/analysis/health/types.js";

function dim(
  dimension: HealthDimension,
  score: number,
  hasEnoughData: boolean,
  insufficientDataReason?: string
): DimensionResult {
  return {
    dimension,
    score,
    status: "fair",
    findings: [],
    metrics: {},
    hasEnoughData,
    sampleSize: hasEnoughData ? 20 : 0,
    ...(insufficientDataReason ? { insufficientDataReason } : {}),
  } as DimensionResult;
}

function result(dims: DimensionResult[], overallScore: number | null): HealthAnalysisResult {
  return {
    dimensions: Object.fromEntries(dims.map((d) => [d.dimension, d])),
    crossReferences: [],
    overallScore,
    overallStatus: overallScore === null ? "no-data" : "good",
    summary: "",
    analyzedAt: "2026-10-24T10:00:00.000Z",
    config: DEFAULT_HEALTH_CONFIG,
  };
}

describe("buildHealthTrendEntry", () => {
  it("records only dimensions with data, and names the excluded ones", () => {
    const entry = buildHealthTrendEntry(
      result(
        [
          dim("reliability", 92, true),
          dim("skill-drift", 50, false, "no skill-effectiveness records"),
        ],
        92
      ),
      { issueNumber: 7, runId: "run-7" }
    );

    expect(entry.schema_version).toBe("2");
    expect(entry.dimensions).toEqual({ reliability: 92 });
    // The starved analyzer's placeholder 50 is not a measurement.
    expect(entry.dimensions["skill-drift"]).toBeUndefined();
    expect(entry.excluded_dimensions).toEqual({
      "skill-drift": "no skill-effectiveness records",
    });
    expect(entry.overall_score).toBe(92);
    expect(entry.status).toBe("good");
    expect(entry.run_id).toBe("run-7");
    expect(entry.issue_number).toBe(7);
  });

  it("records an all-starved run as null, with status no-data", () => {
    const entry = buildHealthTrendEntry(
      result([dim("reliability", 50, false), dim("cost-health", 50, false)], null),
      { issueNumber: 3, runId: "run-3" }
    );

    expect(entry.overall_score).toBeNull();
    expect(entry.status).toBe("no-data");
    expect(entry.dimensions).toEqual({});
    expect(Object.keys(entry.excluded_dimensions).sort()).toEqual(["cost-health", "reliability"]);
    // A reason is always named, even when the analyzer gave none.
    expect(entry.excluded_dimensions.reliability).toMatch(/insufficient data/);
  });

  it("persists the same number the engine computed", () => {
    const empty: HealthAnalysisInput = {
      executionHistory: [],
      healthScores: [],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    };
    const analysis = new HealthAnalysisEngine().analyze(empty);
    const entry = buildHealthTrendEntry(analysis, { issueNumber: 1, runId: "r" });
    expect(entry.overall_score).toBe(analysis.overallScore);
    expect(entry.status).toBe(analysis.overallStatus);
  });
});
