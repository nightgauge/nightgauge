/**
 * PTCMetrics.test.ts
 *
 * Unit tests for PTC metrics aggregation utility.
 *
 * @see Issue #1071 - Track PTC metrics
 */

import { describe, it, expect } from "vitest";
import { aggregatePTCMetrics, type PTCStageUsage } from "../../src/tracking/PTCMetrics.js";

const makeStage = (overrides: Partial<PTCStageUsage> = {}): PTCStageUsage => ({
  stage: "feature-dev",
  programmaticCalls: 5,
  directCalls: 3,
  estimatedTokensSaved: 2000,
  codeExecutionCount: 2,
  containerReuseCount: 1,
  inputTokens: 10000,
  outputTokens: 3000,
  estimatedCostUsd: 0.05,
  ...overrides,
});

describe("aggregatePTCMetrics()", () => {
  it("aggregates a single stage correctly", () => {
    const result = aggregatePTCMetrics([makeStage()]);

    expect(result.totalToolCalls).toBe(8);
    expect(result.programmaticCalls).toBe(5);
    expect(result.directCalls).toBe(3);
    expect(result.programmaticRatio).toBeCloseTo(5 / 8);
    expect(result.estimatedTokensSaved).toBe(2000);
    expect(result.codeExecutionCount).toBe(2);
    expect(result.containerReuseCount).toBe(1);
    expect(result.totalInputTokens).toBe(10000);
    expect(result.totalOutputTokens).toBe(3000);
    expect(result.totalCostUsd).toBeCloseTo(0.05);
    expect(result.perStage).toHaveLength(1);
  });

  it("sums across multiple stages", () => {
    const stages = [
      makeStage({
        stage: "feature-dev",
        programmaticCalls: 10,
        directCalls: 5,
      }),
      makeStage({
        stage: "feature-validate",
        programmaticCalls: 3,
        directCalls: 2,
      }),
    ];

    const result = aggregatePTCMetrics(stages);

    expect(result.programmaticCalls).toBe(13);
    expect(result.directCalls).toBe(7);
    expect(result.totalToolCalls).toBe(20);
    expect(result.programmaticRatio).toBeCloseTo(13 / 20);
    expect(result.perStage).toHaveLength(2);
  });

  it("returns zero ratio for empty stages array", () => {
    const result = aggregatePTCMetrics([]);

    expect(result.totalToolCalls).toBe(0);
    expect(result.programmaticRatio).toBe(0);
    expect(result.perStage).toHaveLength(0);
  });

  it("returns ratio 1.0 when all calls are programmatic", () => {
    const result = aggregatePTCMetrics([makeStage({ programmaticCalls: 10, directCalls: 0 })]);

    expect(result.programmaticRatio).toBe(1.0);
  });

  it("returns ratio 0.0 when all calls are direct", () => {
    const result = aggregatePTCMetrics([makeStage({ programmaticCalls: 0, directCalls: 10 })]);

    expect(result.programmaticRatio).toBe(0.0);
  });
});
