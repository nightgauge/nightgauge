/**
 * Skill Drift Dimension Analyzer
 *
 * Evaluates skill instruction health based on self-assessment synthesis data.
 * Scores 0-100 where 100 = no friction, 0 = skills fundamentally broken.
 *
 * Uses the synthesis result from SkillSelfAssessmentSynthesizer to detect
 * recurring friction patterns across skill executions.
 *
 * @see docs/SKILL_SELF_ASSESSMENT.md — Scoring table
 * @see Issue #2320 — Add Skill Drift dimension to health widget
 */

import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
  Severity,
  Confidence,
} from "../types.js";
import { getHealthStatus } from "../types.js";
import { clamp } from "../statistics.js";

/** Severity weight for scoring: high findings penalize more than low */
const SEVERITY_PENALTY: Record<string, number> = {
  high: 20,
  medium: 10,
  low: 5,
};

export function analyzeSkillDrift(
  dataset: HealthAnalysisInput,
  _config: HealthAnalysisConfig,
  _baseline?: HealthAnalysisInput
): DimensionResult {
  const synthesis = dataset.skillAssessmentSynthesis;

  // No assessment data — return perfect score with insufficient data flag
  if (!synthesis) {
    return {
      dimension: "skill-drift",
      score: 100,
      status: "excellent",
      findings: [],
      metrics: {
        recordsAnalyzed: 0,
        totalFrictionItems: 0,
        recurringProposalCount: 0,
        isolatedCount: 0,
        highSeverityCount: 0,
        mediumSeverityCount: 0,
        lowSeverityCount: 0,
        affectedSkillCount: 0,
      },
      hasEnoughData: false,
      sampleSize: 0,
    };
  }

  const { proposals, recordsAnalyzed, totalFrictionItems, isolatedCount } = synthesis;

  // Count by severity
  const highCount = proposals.filter((p) => p.severity === "high").length;
  const mediumCount = proposals.filter((p) => p.severity === "medium").length;
  const lowCount = proposals.filter((p) => p.severity === "low").length;

  // Distinct skills affected
  const affectedSkills = new Set(proposals.map((p) => p.skillFile));

  // Compute score: start at 100, subtract penalties per recurring proposal
  let score = 100;
  for (const proposal of proposals) {
    score -= SEVERITY_PENALTY[proposal.severity] ?? 10;
  }
  score = clamp(score, 0, 100);

  // Generate findings for each recurring proposal
  const findings: Finding[] = proposals.map((p, i) => {
    const severity: Severity =
      p.severity === "high" ? "high" : p.severity === "medium" ? "medium" : "low";
    const confidence: Confidence =
      p.occurrenceCount >= 5 ? "high" : p.occurrenceCount >= 3 ? "medium" : "low";

    return {
      id: `skill-drift-${i}`,
      dimension: "skill-drift" as const,
      severity,
      title: `Recurring friction in ${p.skillFile.replace("skills/nightgauge-", "").replace("/SKILL.md", "")}`,
      description: `${p.findingPattern} — seen in ${p.occurrenceCount} distinct issues`,
      impact: `Agents work around this on every run, wasting tokens and risking silent failures`,
      recommendation: p.proposedChange,
      evidence: {
        skillFile: p.skillFile,
        findingPattern: p.findingPattern,
        occurrenceCount: p.occurrenceCount,
      },
      confidence,
    };
  });

  return {
    dimension: "skill-drift",
    score,
    status: getHealthStatus(score),
    findings,
    metrics: {
      recordsAnalyzed,
      totalFrictionItems,
      recurringProposalCount: proposals.length,
      isolatedCount,
      highSeverityCount: highCount,
      mediumSeverityCount: mediumCount,
      lowSeverityCount: lowCount,
      affectedSkillCount: affectedSkills.size,
    },
    hasEnoughData: recordsAnalyzed >= 2,
    sampleSize: recordsAnalyzed,
  };
}
