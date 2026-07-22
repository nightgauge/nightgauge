/**
 * Token Economics dimension — per-stage cache-hit-rate tests (Issue #3804).
 *
 * Covers the per-stage cache breakdown and low-reuse finding added to the
 * Token Economics dimension:
 *  - per-stage rate computed with the canonical formula
 *    (cache_read / (cache_read + cache_creation + input));
 *  - a stage below its threshold emits a finding with the right severity;
 *  - a stage with zero cacheable input reports null (no metric, no finding);
 *  - per-stage threshold overrides are respected.
 */

import { describe, it, expect } from "vitest";
import { analyzeTokenEconomics } from "../tokenEconomics.js";
import {
  DEFAULT_HEALTH_CONFIG,
  DEFAULT_CACHE_THRESHOLD,
  type HealthAnalysisConfig,
  type HealthAnalysisInput,
} from "../../types.js";
import type { ExecutionHistoryRecord } from "../../../types.js";

function makeRecord(overrides: Partial<ExecutionHistoryRecord> = {}): ExecutionHistoryRecord {
  return {
    issueNumber: 100,
    stage: "feature-dev",
    success: true,
    retries: 0,
    inputTokens: 0,
    outputTokens: 5000,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd: 0.1,
    durationMs: 60000,
    timestamp: "2025-01-15T10:00:00Z",
    ...overrides,
  };
}

/** Build a dataset with `count` identical records for one stage. */
function dataset(records: ExecutionHistoryRecord[]): HealthAnalysisInput {
  return {
    executionHistory: records,
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
  };
}

/** A config that supplies a per-stage threshold override. */
function configWith(
  cacheThresholds: HealthAnalysisConfig["cacheThresholds"]
): HealthAnalysisConfig {
  return { ...DEFAULT_HEALTH_CONFIG, cacheThresholds };
}

describe("Token Economics — per-stage cache hit rate (#3804)", () => {
  it("computes the per-stage rate with the canonical cache_creation-inclusive formula", () => {
    // cache_read=80, cache_creation=10, input=10 → 80 / 100 = 0.8
    const records = Array.from({ length: 6 }, () =>
      makeRecord({ cacheReadTokens: 80, cacheCreationTokens: 10, inputTokens: 10 })
    );
    const result = analyzeTokenEconomics(dataset(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["perStageCacheHitRate.feature-dev"]).toBeCloseTo(0.8, 5);
    expect(result.metrics.stagesWithCacheData).toBe(1);
    // 0.8 is above the 0.4 default — no low-reuse finding for this stage.
    expect(result.findings.some((f) => f.title.includes("Low cache hit rate for"))).toBe(false);
  });

  it("emits a medium-severity finding when a stage falls below threshold", () => {
    // cache_read=20, cache_creation=10, input=70 → 20 / 100 = 0.2 (< 0.4 default, >= 0.1)
    const records = Array.from({ length: 6 }, () =>
      makeRecord({ cacheReadTokens: 20, cacheCreationTokens: 10, inputTokens: 70 })
    );
    const result = analyzeTokenEconomics(dataset(records), DEFAULT_HEALTH_CONFIG);

    const finding = result.findings.find((f) => f.title === "Low cache hit rate for feature-dev");
    expect(finding).toBeDefined();
    expect(finding?.severity).toBe("medium");
    expect(finding?.evidence.stage).toBe("feature-dev");
    expect(finding?.evidence.cacheHitRate).toBeCloseTo(0.2, 5);
    expect(finding?.evidence.threshold).toBe(DEFAULT_CACHE_THRESHOLD);
  });

  it("escalates to high severity when the rate is below 10%", () => {
    // cache_read=5, cache_creation=5, input=90 → 0.05 (< 0.1)
    const records = Array.from({ length: 6 }, () =>
      makeRecord({ cacheReadTokens: 5, cacheCreationTokens: 5, inputTokens: 90 })
    );
    const result = analyzeTokenEconomics(dataset(records), DEFAULT_HEALTH_CONFIG);

    const finding = result.findings.find((f) => f.title === "Low cache hit rate for feature-dev");
    expect(finding?.severity).toBe("high");
  });

  it("reports null (omits the metric) and emits no finding for a stage with zero cacheable input", () => {
    // All token dimensions zero → denominator 0 → null.
    const records = Array.from({ length: 6 }, () =>
      makeRecord({
        stage: "issue-pickup",
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        inputTokens: 0,
      })
    );
    const result = analyzeTokenEconomics(dataset(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["perStageCacheHitRate.issue-pickup"]).toBeUndefined();
    expect(result.metrics.stagesWithCacheData).toBe(0);
    expect(result.findings.some((f) => f.title.includes("Low cache hit rate for"))).toBe(false);
  });

  it("respects a per-stage threshold override", () => {
    // Rate 0.5: above the 0.4 default (no finding) but below a 0.6 override (finding).
    const records = Array.from({ length: 6 }, () =>
      makeRecord({ cacheReadTokens: 50, cacheCreationTokens: 10, inputTokens: 40 })
    );

    const defaultResult = analyzeTokenEconomics(dataset(records), DEFAULT_HEALTH_CONFIG);
    expect(
      defaultResult.findings.some((f) => f.title === "Low cache hit rate for feature-dev")
    ).toBe(false);

    const overrideResult = analyzeTokenEconomics(
      dataset(records),
      configWith({ default: DEFAULT_CACHE_THRESHOLD, byStage: { "feature-dev": 0.6 } })
    );
    expect(
      overrideResult.findings.some((f) => f.title === "Low cache hit rate for feature-dev")
    ).toBe(true);
  });

  it("does not emit a per-stage finding for a stage below the basic sample minimum", () => {
    // Dimension has enough total records (5 healthy feature-dev), but the
    // low-reuse "pr-merge" stage has only 2 records (< basic minimum of 5),
    // so no finding fires despite its 5% rate. Its metric is still surfaced.
    const records = [
      ...Array.from({ length: 5 }, () =>
        makeRecord({
          stage: "feature-dev",
          cacheReadTokens: 80,
          cacheCreationTokens: 10,
          inputTokens: 10,
        })
      ),
      ...Array.from({ length: 2 }, () =>
        makeRecord({
          stage: "pr-merge",
          cacheReadTokens: 5,
          cacheCreationTokens: 5,
          inputTokens: 90,
        })
      ),
    ];
    const result = analyzeTokenEconomics(dataset(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title === "Low cache hit rate for pr-merge")).toBe(false);
    expect(result.metrics["perStageCacheHitRate.pr-merge"]).toBeCloseTo(0.05, 5);
  });
});
