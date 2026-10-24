/**
 * Skill Drift Dimension Analyzer (re-based, #1900)
 *
 * Asks: when a skill's instructions change, does its stage get worse?
 *
 * Input — a change to it must change this analyzer, or it starves:
 *
 * | Input | Path | Fields |
 * | --- | --- | --- |
 * | Skill effectiveness | `<git-dir>/nightgauge-worktree/health/skill-effectiveness.jsonl` | `skill_file`, `commit_hash`, `changed_at`, `classification`, `confidence`, `delta`, `before_*`, `after_*` |
 *
 * The caller supplies the latest record per (skill_file, commit_hash) whose
 * change falls inside the analysis period.
 *
 * Rules:
 * - A change classified `insufficient_data` is a coverage gap
 *   (`unclassifiedChanges`), not a sample.
 * - At least `minimumSampleSizes.basic` classified changes are required;
 *   otherwise `hasEnoughData: false` with the reason.
 * - Score = share of classified changes that did NOT regress their stage's
 *   success rate, 0-100. Each regression is a finding.
 *
 * @see Issue #1900 - re-base skill-drift
 * @see Issue #1414 - skill effectiveness tracking (the record's producer)
 */

import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
} from "../types.js";
import { getHealthStatus } from "../types.js";
import { clamp } from "../statistics.js";

export function analyzeSkillDrift(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  _baseline?: HealthAnalysisInput
): DimensionResult {
  const minimum = config.minimumSampleSizes.basic;
  const entries = dataset.skillEffectiveness;
  const all = entries ?? [];
  const classified = all.filter((e) => e.classification !== "insufficient_data");
  const regressions = classified.filter((e) => e.classification === "regression");

  const metrics: Record<string, number> = {
    skillChanges: all.length,
    classifiedChanges: classified.length,
    unclassifiedChanges: all.length - classified.length,
    regressions: regressions.length,
    effective: classified.filter((e) => e.classification === "effective").length,
    affectedSkillCount: new Set(regressions.map((e) => e.skillFile)).size,
  };

  if (!entries || classified.length < minimum) {
    return {
      dimension: "skill-drift",
      score: 50,
      status: getHealthStatus(50),
      findings: [],
      metrics,
      hasEnoughData: false,
      sampleSize: classified.length,
      insufficientDataReason: !entries
        ? "no skill change records (health/skill-effectiveness.jsonl) were supplied"
        : `skill-effectiveness.jsonl has ${classified.length} classified skill change(s) ` +
          `(${all.length - classified.length} insufficient_data); needs ${minimum}`,
    };
  }

  const score = clamp(
    Math.round((100 * (classified.length - regressions.length)) / classified.length),
    0,
    100
  );

  const findings: Finding[] = regressions.map((e, i) => ({
    id: `skill-drift-${i}`,
    dimension: "skill-drift" as const,
    severity: e.confidence === "moderate" ? "medium" : "low",
    title: `Skill change regressed ${e.stage}`,
    description:
      `${e.skillFile} at ${e.commitHash.slice(0, 8)} moved ${e.stage}'s success rate from ` +
      `${Math.round(e.beforeWindow.successRate * 100)}% (n=${e.beforeWindow.sampleCount}) to ` +
      `${Math.round(e.afterWindow.successRate * 100)}% (n=${e.afterWindow.sampleCount}).`,
    impact:
      "Instructions that drifted from the code they describe make every later run pay for it.",
    recommendation: `Review the change to ${e.skillFile} at ${e.commitHash.slice(0, 8)} against the failures that followed it.`,
    evidence: {
      skillFile: e.skillFile,
      commitHash: e.commitHash,
      changedAt: e.changedAt,
      delta: e.delta,
    },
    confidence: e.confidence === "moderate" ? "medium" : "low",
  }));

  return {
    dimension: "skill-drift",
    score,
    status: getHealthStatus(score),
    findings,
    metrics,
    hasEnoughData: true,
    sampleSize: classified.length,
  };
}
