/**
 * Tests for the grading & scoring engine (Issue #4173). Pure scoring + a mocked
 * judge — no LLM calls. Includes the judge-reliability-guard-fires case.
 */

import { describe, it, expect } from "vitest";
import {
  scoreCell,
  computeCorrectness,
  computeAutomatedScore,
  aggregateJudge,
  runJudgeWithReliabilityGuard,
  JOB_CLASS_WEIGHTS,
  type EvalJudge,
  type EvalJudgeVerdict,
  type AutomatedBaseline,
  type AutomatedMetrics,
} from "../../src/eval/qualityScorer.js";
import type { EvalRubric, GateResult } from "../../src/eval/modelEvalSchemas.js";

const BASELINE: AutomatedBaseline = { attempts: 1, latencyMs: 60_000, costUsd: 0.5 };
const ON_BASELINE: AutomatedMetrics = { attemptsToGreen: 1, latencyMs: 60_000, costUsd: 0.5 };
const RUBRIC: EvalRubric = {
  criteria: [
    { dimension: "ux_quality", weight: 0.6, guidance: "?" },
    { dimension: "correctness", weight: 0.4, guidance: "?" },
  ],
};
const GATES_PASS: GateResult[] = [
  { name: "build", passed: true },
  { name: "test", passed: true },
];

describe("computeCorrectness", () => {
  it("is the fraction of gates that passed", () => {
    expect(
      computeCorrectness(
        [
          { name: "a", passed: true },
          { name: "b", passed: false },
        ],
        "fail"
      )
    ).toBe(50);
    expect(computeCorrectness(GATES_PASS, "pass")).toBe(100);
  });
  it("falls back to the verdict when there are no gates", () => {
    expect(computeCorrectness([], "pass")).toBe(100);
    expect(computeCorrectness([], "fail")).toBe(0);
  });
});

describe("computeAutomatedScore", () => {
  it("is 100 at (or under) baseline", () => {
    expect(computeAutomatedScore(ON_BASELINE, BASELINE)).toBe(100);
    expect(
      computeAutomatedScore({ attemptsToGreen: 1, latencyMs: 30_000, costUsd: 0.1 }, BASELINE)
    ).toBe(100);
  });
  it("penalizes extra attempts (20 pts each)", () => {
    expect(computeAutomatedScore({ ...ON_BASELINE, attemptsToGreen: 3 }, BASELINE)).toBe(60);
  });
  it("penalizes latency/cost overage and clamps at 0", () => {
    const score = computeAutomatedScore(
      { attemptsToGreen: 5, latencyMs: 300_000, costUsd: 5 },
      BASELINE
    );
    expect(score).toBe(0);
  });
});

describe("aggregateJudge", () => {
  it("weights dimension scores by the rubric", () => {
    const verdict: EvalJudgeVerdict = {
      dimensions: [
        { dimension: "ux_quality", score: 80 },
        { dimension: "correctness", score: 100 },
      ],
    };
    const { score, dimensions } = aggregateJudge(verdict, RUBRIC);
    expect(score).toBeCloseTo(80 * 0.6 + 100 * 0.4, 6); // 88
    expect(dimensions.find((d) => d.dimension === "ux_quality")?.weight).toBe(0.6);
  });
});

describe("scoreCell", () => {
  it("blends all three components per the job-class weights (with judge)", () => {
    const score = scoreCell({
      jobClass: "ui-creation",
      verdict: "pass",
      gates: GATES_PASS,
      metrics: ON_BASELINE,
      baseline: BASELINE,
      judge: { verdict: { dimensions: [{ dimension: "ux_quality", score: 60 }] }, rubric: RUBRIC },
    });
    const w = JOB_CLASS_WEIGHTS["ui-creation"];
    // correctness 100, automated 100, judge 60 (only ux_quality present → its own weight normalizes to 1)
    expect(score.composite).toBeCloseTo(100 * w.correctness + 100 * w.automated + 60 * w.judge, 2);
    expect(score.judge_used).toBe(true);
  });

  it("deterministic-only mode renormalizes without the judge", () => {
    const score = scoreCell({
      jobClass: "bugfix",
      verdict: "pass",
      gates: GATES_PASS,
      metrics: { ...ON_BASELINE, attemptsToGreen: 2 }, // automated = 80
      baseline: BASELINE,
    });
    const w = JOB_CLASS_WEIGHTS.bugfix;
    const expected = (100 * w.correctness + 80 * w.automated) / (w.correctness + w.automated);
    expect(score.judge_used).toBe(false);
    expect(score.dimensions).toHaveLength(0);
    expect(score.composite).toBeCloseTo(expected, 2);
  });

  it("propagates low_confidence from the reliability guard", () => {
    const score = scoreCell({
      jobClass: "ux-styling",
      verdict: "pass",
      gates: GATES_PASS,
      metrics: ON_BASELINE,
      baseline: BASELINE,
      judge: {
        verdict: { dimensions: [{ dimension: "ux_quality", score: 55 }] },
        rubric: RUBRIC,
        lowConfidence: new Set(["ux_quality"]),
      },
    });
    expect(score.low_confidence).toBe(true);
    expect(score.dimensions[0].low_confidence).toBe(true);
  });
});

describe("runJudgeWithReliabilityGuard", () => {
  it("does NOT flag a stable judge", async () => {
    const stable: EvalJudge = {
      judge: async () => ({ dimensions: [{ dimension: "ux_quality", score: 80 }] }),
    };
    const { verdict, lowConfidence } = await runJudgeWithReliabilityGuard(stable, RUBRIC, {
      samples: 3,
    });
    expect(lowConfidence.size).toBe(0);
    expect(verdict.dimensions[0].score).toBe(80);
  });

  it("FLAGS an inconsistent judge (guard fires)", async () => {
    let call = 0;
    const flaky: EvalJudge = {
      judge: async () => ({
        dimensions: [{ dimension: "ux_quality", score: call++ % 2 === 0 ? 20 : 95 }],
      }),
    };
    const { verdict, lowConfidence } = await runJudgeWithReliabilityGuard(flaky, RUBRIC, {
      samples: 4,
      varianceThreshold: 10,
    });
    expect(lowConfidence.has("ux_quality")).toBe(true);
    // mean of {20,95,20,95} ≈ 57.5
    expect(verdict.dimensions[0].score).toBeCloseTo(57.5, 1);
  });
});
