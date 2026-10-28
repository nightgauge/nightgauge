/**
 * Per-change outcome metrics (#1284).
 */

import { describe, it, expect } from "vitest";
import {
  computePerChangeFirstPass,
  computePlanFidelity,
  computeChangeFailureRate,
} from "../../../src/analysis/health/changeOutcomes.js";
import type { SurvivalVerdictEntry } from "../../../src/analysis/health/types.js";
import { makeExecutionRecord } from "./fixtures.js";

const rec = (issueNumber: number, stage: string, retries = 0, success = true) =>
  makeExecutionRecord({ issueNumber, stage, retries, success });

const survival = (verdict: SurvivalVerdictEntry["verdict"], n = 1): SurvivalVerdictEntry => ({
  issueNumber: n,
  prNumber: n,
  mergeCommitSha: `sha${n}`,
  mergedAt: "2026-01-01T00:00:00Z",
  verdict,
});

describe("computePerChangeFirstPass", () => {
  it("counts a clean merge as first-pass", () => {
    const r = computePerChangeFirstPass([rec(1, "feature-dev"), rec(1, "pr-merge")], []);
    expect(r).toEqual({ mergedChanges: 1, firstPassChanges: 1, rate: 1 });
  });

  it("a retry, a repeated stage, or a backtrack each disqualify the change", () => {
    const history = [
      rec(1, "feature-dev", 1),
      rec(1, "pr-merge"),
      rec(2, "feature-dev"),
      rec(2, "feature-dev"),
      rec(2, "pr-merge"),
      rec(3, "feature-dev"),
      rec(3, "pr-merge"),
      rec(4, "feature-dev"),
      rec(4, "pr-merge"),
    ];
    const r = computePerChangeFirstPass(history, [{ issueNumber: 3, runId: "r", ts: "t" }]);
    expect(r).toEqual({ mergedChanges: 4, firstPassChanges: 1, rate: 0.25 });
  });

  it("an unmerged issue is not counted; nothing merged gives a null rate", () => {
    const r = computePerChangeFirstPass([rec(5, "feature-dev"), rec(5, "pr-merge", 0, false)], []);
    expect(r).toEqual({ mergedChanges: 0, firstPassChanges: 0, rate: null });
  });
});

describe("computePlanFidelity", () => {
  it("scores identical, disjoint and partial overlaps by Jaccard", () => {
    const r = computePlanFidelity([
      { issueNumber: 1, plannedFiles: ["a.ts", "./b.ts"], mergedFiles: ["b.ts", "a.ts"] },
      { issueNumber: 2, plannedFiles: ["a.ts"], mergedFiles: ["z.ts"] },
      { issueNumber: 3, plannedFiles: ["a.ts", "b.ts"], mergedFiles: ["b.ts", "c.ts"] },
    ]);
    expect(r.scored).toBe(3);
    expect(r.noPlan).toBe(0);
    expect(r.meanFidelity).toBeCloseTo((1 + 0 + 1 / 3) / 3);
    expect(r.lowFidelityShare).toBeCloseTo(2 / 3);
  });

  it("a missing plan is counted in noPlan, never scored as 0", () => {
    const r = computePlanFidelity([
      { issueNumber: 1, plannedFiles: null, mergedFiles: ["a.ts"] },
      { issueNumber: 2, plannedFiles: ["a.ts"], mergedFiles: ["a.ts"] },
    ]);
    expect(r).toEqual({ scored: 1, noPlan: 1, meanFidelity: 1, lowFidelityShare: 0 });
  });

  it("nothing scorable gives null", () => {
    expect(computePlanFidelity([]).meanFidelity).toBeNull();
  });
});

describe("computeChangeFailureRate", () => {
  it("reverted and broke are failures over finalized verdicts", () => {
    const r = computeChangeFailureRate([
      survival("survived"),
      survival("survived"),
      survival("reverted"),
      survival("broke"),
    ]);
    expect(r.rate).toBe(0.5);
    expect(r.finalized).toBe(4);
    expect(r.failures).toBe(2);
  });

  it("excludes pending and unobserved from the denominator and counts them", () => {
    const r = computeChangeFailureRate([
      survival("survived"),
      survival("reverted"),
      survival("pending"),
      survival("pending"),
      survival("unobserved"),
    ]);
    expect(r).toEqual({
      finalized: 2,
      failures: 1,
      excludedPending: 2,
      excludedUnobserved: 1,
      rate: 0.5,
    });
  });

  it("all pending gives a null rate", () => {
    expect(computeChangeFailureRate([survival("pending")]).rate).toBeNull();
  });
});
