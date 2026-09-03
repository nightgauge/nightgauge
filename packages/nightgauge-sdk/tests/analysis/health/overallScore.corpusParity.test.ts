/**
 * overallScore.corpusParity.test.ts
 *
 * The overall health score has two implementations that must answer alike
 * for the same dimension results (#1197): the SDK engine's
 * `computeOverallHealthScore`, and the `nightgauge-pipeline-health` SKILL's
 * Phase 3 rule, which an agent applies by hand. Before #1197 they disagreed on
 * run one — the SKILL rendered a starved dimension as N/A while the engine
 * scored it 50 at full weight.
 *
 * Both are pinned to ONE corpus, `overall-score-corpus.json`, which lives
 * beside the SKILL because the rule is the skill's contract and the engine is
 * its executable form. Every case is asserted against the engine here; the
 * SKILL is held to the corpus by name so a rename or removal of the file
 * lands as a diff, not as silent drift.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import {
  computeOverallHealthScore,
  getOverallHealthStatus,
  HealthAnalysisEngine,
  type ScoredDimension,
} from "../../../src/analysis/health/index.js";
import type { HealthDimension } from "../../../src/analysis/health/types.js";

const REPO_ROOT = path.resolve(__dirname, "../../../../..");
const SKILL_DIR = path.join(REPO_ROOT, "skills/nightgauge-pipeline-health");
const CORPUS_NAME = "overall-score-corpus.json";

interface CorpusCase {
  id: string;
  rationale: string;
  dimensions: Array<ScoredDimension & { weight: number }>;
  expected: number | null;
}

const corpus: { cases: CorpusCase[] } = JSON.parse(
  readFileSync(path.join(SKILL_DIR, CORPUS_NAME), "utf-8")
);

describe("overall health score — shared corpus (#1197)", () => {
  it("has a non-trivial corpus with unique ids", () => {
    expect(corpus.cases.length).toBeGreaterThanOrEqual(5);
    expect(new Set(corpus.cases.map((c) => c.id)).size).toBe(corpus.cases.length);
  });

  for (const c of corpus.cases) {
    it(`engine reproduces "${c.id}"`, () => {
      const weights: Partial<Record<HealthDimension, number>> = {};
      for (const d of c.dimensions) weights[d.dimension] = d.weight;
      expect(computeOverallHealthScore(c.dimensions, weights)).toBe(c.expected);
    });
  }

  it("the SKILL cites the corpus by name, so it cannot drift silently", () => {
    const skill = readFileSync(path.join(SKILL_DIR, "SKILL.md"), "utf-8");
    expect(skill).toContain(CORPUS_NAME);
  });

  it("null maps to the no-data status, never to a numeric band", () => {
    expect(getOverallHealthStatus(null)).toBe("no-data");
    expect(getOverallHealthStatus(0)).toBe("critical");
  });
});

describe("HealthAnalysisEngine.analyze() honours the corpus rule (#1197)", () => {
  const empty = {
    executionHistory: [],
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
    recommendationHistory: [],
  };

  it("an empty dataset yields overallScore null and status no-data, not 50/fair or 0/critical", () => {
    const result = new HealthAnalysisEngine().analyze(empty);

    for (const dim of Object.values(result.dimensions)) {
      expect(dim.hasEnoughData).toBe(false);
    }
    expect(result.overallScore).toBeNull();
    expect(result.overallStatus).toBe("no-data");
    expect(result.summary).toContain("N/A");
  });
});
