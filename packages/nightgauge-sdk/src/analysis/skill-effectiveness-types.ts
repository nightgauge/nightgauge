/**
 * Types for skill effectiveness tracking.
 *
 * Correlates git change timestamps on skills SKILL.md files with
 * before/after stage success rates from execution history to measure
 * whether skill edits improved pipeline outcomes.
 *
 * @see Issue #1414 - Skill effectiveness tracking
 */

/** Input: a parsed git log entry for a SKILL.md change */
export interface SkillChangeRecord {
  /** e.g. 'skills/nightgauge-feature-planning/SKILL.md' */
  skillFile: string;
  /** e.g. 'feature-planning' */
  stage: string;
  commitHash: string;
  /** ISO 8601 */
  changedAt: string;
}

/** Per-skill-change effectiveness analysis result */
export interface SkillEffectivenessEntry {
  skillFile: string;
  stage: string;
  commitHash: string;
  /** ISO 8601 */
  changedAt: string;
  beforeWindow: { sampleCount: number; successRate: number };
  afterWindow: { sampleCount: number; successRate: number };
  /** afterSuccessRate - beforeSuccessRate (range: -1.0 to 1.0) */
  delta: number;
  classification: "effective" | "regression" | "neutral" | "insufficient_data";
  confidence: "insufficient_data" | "low" | "moderate";
  /** ISO 8601 */
  analyzedAt: string;
}

export interface SkillEffectivenessAnalysisResult {
  /** ISO 8601 */
  analyzedAt: string;
  skillChangesFound: number;
  entries: SkillEffectivenessEntry[];
}
