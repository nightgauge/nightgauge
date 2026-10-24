/**
 * analyzeLearningEffectiveness — re-based on the outcome corpus (#1900).
 *
 * Inputs: `<clone>/pipeline/history/outcomes.jsonl` predicted/actual pairs and
 * `<clone>/pipeline/calibration.json` `updated_at`. An empty half of a pair is
 * an unknown, excluded from the accuracy denominator (#340), and counted as a
 * coverage gap (#304) — never as a miss.
 */

import { describe, it, expect } from "vitest";
import { analyzeLearningEffectiveness } from "../../../../src/analysis/health/dimensions/learningEffectiveness.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../src/analysis/health/types.js";
import type {
  HealthAnalysisInput,
  LearningOutcomeEntry,
} from "../../../../src/analysis/health/types.js";
import { makeEmptyDataset } from "../fixtures.js";

const BASE = Date.parse("2026-03-01T00:00:00Z");
const day = (n: number) => new Date(BASE + n * 86_400_000).toISOString();

function outcome(overrides: Partial<LearningOutcomeEntry> = {}): LearningOutcomeEntry {
  return {
    completedAt: day(0),
    predictedModel: "sonnet",
    actualModel: "sonnet",
    predictedSize: "medium",
    actualSize: "medium",
    ...overrides,
  };
}

function dataset(
  outcomes: LearningOutcomeEntry[] | undefined,
  calibrationUpdatedAt: string | null = day(0)
): HealthAnalysisInput {
  return {
    ...makeEmptyDataset(),
    learningSignals: outcomes === undefined ? undefined : { outcomes, calibrationUpdatedAt },
  };
}

const run = (d: HealthAnalysisInput) => analyzeLearningEffectiveness(d, DEFAULT_HEALTH_CONFIG);

describe("learning-effectiveness — absent inputs are excluded, never proxied", () => {
  it("no outcome corpus: hasEnoughData false and the reason names outcomes.jsonl", () => {
    const r = run(dataset(undefined));
    expect(r.hasEnoughData).toBe(false);
    expect(r.sampleSize).toBe(0);
    expect(r.insufficientDataReason).toMatch(/outcomes\.jsonl/);
  });

  it("rows with no complete pair are coverage gaps, not samples", () => {
    const rows = Array.from({ length: 12 }, (_, i) =>
      outcome({ completedAt: day(i), predictedModel: "", actualSize: "" })
    );
    const r = run(dataset(rows));
    expect(r.hasEnoughData).toBe(false);
    expect(r.sampleSize).toBe(0);
    expect(r.metrics.outcomeRows).toBe(12);
    expect(r.insufficientDataReason).toMatch(/pair/);
  });

  it("fewer complete pairs than minimumSampleSizes.basic is insufficient", () => {
    const r = run(dataset(Array.from({ length: 4 }, (_, i) => outcome({ completedAt: day(i) }))));
    expect(DEFAULT_HEALTH_CONFIG.minimumSampleSizes.basic).toBe(5);
    expect(r.hasEnoughData).toBe(false);
  });
});

describe("learning-effectiveness — scored from prediction accuracy", () => {
  it("a healthy corpus scores with hasEnoughData true and n >= basic", () => {
    const rows = Array.from({ length: 10 }, (_, i) => outcome({ completedAt: day(i) }));
    const r = run(dataset(rows, day(9)));
    expect(r.hasEnoughData).toBe(true);
    expect(r.sampleSize).toBeGreaterThanOrEqual(DEFAULT_HEALTH_CONFIG.minimumSampleSizes.basic);
    expect(r.metrics.routingAccuracy).toBe(1);
    expect(r.metrics.sizeAccuracy).toBe(1);
    expect(r.score).toBe(100);
    expect(r.insufficientDataReason).toBeUndefined();
  });

  it("an empty half is excluded from the denominator, not booked as a miss (#340)", () => {
    const rows = [
      ...Array.from({ length: 6 }, (_, i) => outcome({ completedAt: day(i) })),
      ...Array.from({ length: 6 }, (_, i) => outcome({ completedAt: day(i), actualModel: "" })),
    ];
    const r = run(dataset(rows, day(6)));
    expect(r.metrics.routingPairs).toBe(6);
    expect(r.metrics.routingAccuracy).toBe(1);
  });

  it("misses lower the score", () => {
    const rows = Array.from({ length: 10 }, (_, i) =>
      outcome({ completedAt: day(i), actualSize: i < 5 ? "large" : "medium" })
    );
    const r = run(dataset(rows, day(9)));
    expect(r.metrics.sizeAccuracy).toBe(0.5);
    expect(r.score).toBe(75);
  });

  it("a component below the minimum is left out of the score rather than guessed", () => {
    const rows = Array.from({ length: 8 }, (_, i) =>
      outcome({ completedAt: day(i), predictedModel: i < 2 ? "opus" : "", actualSize: "large" })
    );
    const r = run(dataset(rows, day(7)));
    expect(r.hasEnoughData).toBe(true);
    expect(r.metrics.routingPairs).toBe(2);
    expect(r.metrics.sizeAccuracy).toBe(0);
    expect(r.score).toBe(0);
  });

  it("missing predictions are reported as a coverage finding (#304)", () => {
    const rows = [
      ...Array.from({ length: 5 }, (_, i) => outcome({ completedAt: day(i) })),
      ...Array.from({ length: 10 }, (_, i) =>
        outcome({ completedAt: day(i), predictedModel: "", predictedSize: "" })
      ),
    ];
    const r = run(dataset(rows, day(9)));
    expect(r.metrics.predictionCoverage).toBeCloseTo(5 / 15);
    expect(r.findings.some((f) => /coverage/i.test(f.title))).toBe(true);
  });

  it("calibration older than the newest outcome by more than 14 days costs 10 points", () => {
    const rows = Array.from({ length: 10 }, (_, i) => outcome({ completedAt: day(20 + i) }));
    const fresh = run(dataset(rows, day(29)));
    const stale = run(dataset(rows, day(0)));
    const absent = run(dataset(rows, null));
    expect(fresh.score).toBe(100);
    expect(stale.score).toBe(90);
    expect(absent.score).toBe(90);
    expect(stale.findings.some((f) => /calibration/i.test(f.title))).toBe(true);
    expect(stale.metrics.calibrationLagDays).toBe(29);
  });

  it("accuracy falling against the baseline emits a worsening finding (cross-reference rule 3)", () => {
    const good = Array.from({ length: 10 }, (_, i) => outcome({ completedAt: day(i) }));
    const bad = Array.from({ length: 10 }, (_, i) =>
      outcome({ completedAt: day(i), actualModel: "opus", actualSize: "large" })
    );
    const r = analyzeLearningEffectiveness(
      dataset(bad, day(9)),
      DEFAULT_HEALTH_CONFIG,
      dataset(good, day(9))
    );
    expect(r.periodComparison?.direction).toBe("degrading");
    expect(r.findings.some((f) => f.title.toLowerCase().includes("worsening"))).toBe(true);
  });
});
