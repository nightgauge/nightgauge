/**
 * analyzeSkillDrift — re-based on skill-effectiveness.jsonl (#1900).
 *
 * Input: the latest record per (skill_file, commit_hash) from
 * `<git-dir>/nightgauge-worktree/health/skill-effectiveness.jsonl`. A change
 * classified `insufficient_data` is a coverage gap, not a sample.
 */

import { describe, it, expect } from "vitest";
import { analyzeSkillDrift } from "../../../../src/analysis/health/dimensions/skillDrift.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../src/analysis/health/types.js";
import type { HealthAnalysisInput } from "../../../../src/analysis/health/types.js";
import type { SkillEffectivenessEntry } from "../../../../src/analysis/skill-effectiveness-types.js";
import { makeEmptyDataset } from "../fixtures.js";

function change(
  classification: SkillEffectivenessEntry["classification"],
  i: number
): SkillEffectivenessEntry {
  return {
    skillFile: `skills/example-${i}/SKILL.md`,
    stage: `stage-${i}`,
    commitHash: `c${i}`,
    changedAt: "2026-03-01T00:00:00Z",
    beforeWindow: { sampleCount: 20, successRate: 0.9 },
    afterWindow: { sampleCount: 10, successRate: 0.9 },
    delta: classification === "regression" ? -0.2 : 0,
    classification,
    confidence: classification === "insufficient_data" ? "insufficient_data" : "moderate",
    analyzedAt: "2026-03-02T00:00:00Z",
  };
}

const run = (entries: SkillEffectivenessEntry[] | undefined) =>
  analyzeSkillDrift(
    { ...makeEmptyDataset(), skillEffectiveness: entries } as HealthAnalysisInput,
    DEFAULT_HEALTH_CONFIG
  );

describe("skill-drift — absent inputs are excluded, never proxied", () => {
  it("no records: hasEnoughData false, reason names skill-effectiveness.jsonl", () => {
    const r = run(undefined);
    expect(r.hasEnoughData).toBe(false);
    expect(r.insufficientDataReason).toMatch(/skill-effectiveness\.jsonl/);
  });

  it("insufficient_data classifications are not samples", () => {
    const r = run(Array.from({ length: 30 }, (_, i) => change("insufficient_data", i)));
    expect(r.hasEnoughData).toBe(false);
    expect(r.sampleSize).toBe(0);
    expect(r.metrics.unclassifiedChanges).toBe(30);
  });

  it("fewer classified changes than minimumSampleSizes.basic is insufficient", () => {
    const r = run([0, 1, 2, 3].map((i) => change("neutral", i)));
    expect(r.hasEnoughData).toBe(false);
  });
});

describe("skill-drift — scored from classified skill changes", () => {
  it("no regressions scores 100 with n >= basic", () => {
    const r = run([0, 1, 2, 3, 4, 5].map((i) => change(i % 2 ? "effective" : "neutral", i)));
    expect(r.hasEnoughData).toBe(true);
    expect(r.sampleSize).toBe(6);
    expect(r.score).toBe(100);
    expect(r.insufficientDataReason).toBeUndefined();
  });

  it("regressions lower the score and each one is a finding", () => {
    const entries = [
      ...[0, 1, 2, 3, 4, 5, 6, 7].map((i) => change("neutral", i)),
      change("regression", 8),
      change("regression", 9),
      change("insufficient_data", 10),
    ];
    const r = run(entries);
    expect(r.sampleSize).toBe(10);
    expect(r.score).toBe(80);
    expect(r.metrics.regressions).toBe(2);
    expect(r.findings).toHaveLength(2);
    expect(r.findings[0].evidence.skillFile).toBe("skills/example-8/SKILL.md");
  });
});
