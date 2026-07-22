/**
 * SkillEffectivenessAnalyzer
 *
 * Pure analysis class (no filesystem/git access) that correlates
 * skill change timestamps with before/after stage success rates from
 * execution history.
 *
 * For each SkillChangeRecord, filters execution history by stage,
 * splits into before/after windows around the change timestamp, and
 * computes success rate deltas with confidence bands.
 *
 * Output is informational only. No files are modified.
 *
 * @see Issue #1414 - Skill effectiveness tracking
 */

import type { ExecutionHistoryRecord } from "./types.js";
import type {
  SkillChangeRecord,
  SkillEffectivenessEntry,
  SkillEffectivenessAnalysisResult,
} from "./skill-effectiveness-types.js";

const MIN_SAMPLES = 5;
const IMPROVEMENT_THRESHOLD = 0.1;
const REGRESSION_THRESHOLD = -0.1;
const LOW_CONFIDENCE_MAX = 15;

export class SkillEffectivenessAnalyzer {
  /**
   * Analyze execution history records and return skill effectiveness entries.
   *
   * Each SkillChangeRecord is analyzed independently: history records for
   * the corresponding stage are split into before/after windows around
   * the change timestamp.
   *
   * @param skillChanges - Parsed git log entries for SKILL.md changes
   * @param history - Execution history records (all stages)
   */
  static analyze(
    skillChanges: SkillChangeRecord[],
    history: ExecutionHistoryRecord[]
  ): SkillEffectivenessAnalysisResult {
    const analyzedAt = new Date().toISOString();

    if (skillChanges.length === 0) {
      return { analyzedAt, skillChangesFound: 0, entries: [] };
    }

    const entries: SkillEffectivenessEntry[] = [];

    for (const change of skillChanges) {
      const stageRecords = history.filter((r) => r.stage === change.stage);
      const changeTime = new Date(change.changedAt).getTime();

      const beforeRecords = stageRecords.filter(
        (r) => new Date(r.timestamp).getTime() < changeTime
      );
      const afterRecords = stageRecords.filter(
        (r) => new Date(r.timestamp).getTime() >= changeTime
      );

      const beforeCount = beforeRecords.length;
      const afterCount = afterRecords.length;

      if (beforeCount < MIN_SAMPLES || afterCount < MIN_SAMPLES) {
        entries.push({
          skillFile: change.skillFile,
          stage: change.stage,
          commitHash: change.commitHash,
          changedAt: change.changedAt,
          beforeWindow: {
            sampleCount: beforeCount,
            successRate:
              beforeCount > 0 ? SkillEffectivenessAnalyzer.successRate(beforeRecords) : 0,
          },
          afterWindow: {
            sampleCount: afterCount,
            successRate: afterCount > 0 ? SkillEffectivenessAnalyzer.successRate(afterRecords) : 0,
          },
          delta: 0,
          classification: "insufficient_data",
          confidence: "insufficient_data",
          analyzedAt,
        });
        continue;
      }

      const beforeRate = SkillEffectivenessAnalyzer.successRate(beforeRecords);
      const afterRate = SkillEffectivenessAnalyzer.successRate(afterRecords);
      const delta = afterRate - beforeRate;

      const minCount = Math.min(beforeCount, afterCount);
      const confidence =
        minCount < MIN_SAMPLES
          ? "insufficient_data"
          : minCount <= LOW_CONFIDENCE_MAX
            ? "low"
            : "moderate";

      const classification =
        delta > IMPROVEMENT_THRESHOLD
          ? "effective"
          : delta < REGRESSION_THRESHOLD
            ? "regression"
            : "neutral";

      entries.push({
        skillFile: change.skillFile,
        stage: change.stage,
        commitHash: change.commitHash,
        changedAt: change.changedAt,
        beforeWindow: { sampleCount: beforeCount, successRate: beforeRate },
        afterWindow: { sampleCount: afterCount, successRate: afterRate },
        delta,
        classification,
        confidence,
        analyzedAt,
      });
    }

    return {
      analyzedAt,
      skillChangesFound: skillChanges.length,
      entries,
    };
  }

  private static successRate(records: ExecutionHistoryRecord[]): number {
    if (records.length === 0) return 0;
    const successes = records.filter((r) => r.success).length;
    return successes / records.length;
  }
}
