/**
 * SkillSelfAssessmentSynthesizer
 *
 * Aggregates assessment records from `.nightgauge/pipeline/assessments/`
 * and identifies recurring friction patterns using the Two-Strike Rule:
 * a finding that appears in ≥2 distinct issue executions is a signal that
 * the SKILL.md needs fixing, not a one-off agent error.
 *
 * Pure analysis class — no filesystem access. Callers read assessment JSON
 * files and pass them in; the synthesizer returns structured proposals.
 *
 * @see docs/SKILL_SELF_ASSESSMENT.md — Synthesis algorithm specification
 * @see Issue #1986 — Skill Self-Assessment epic
 */

import type {
  AssessmentRecord,
  FrictionSeverity,
  SkillImprovementProposal,
  SynthesisResult,
} from "./self-assessment-types.js";

/** Severity ordering for picking the "worst" severity in a group */
const SEVERITY_ORDER: Record<FrictionSeverity, number> = {
  high: 3,
  medium: 2,
  low: 1,
};

/** Minimum distinct issue numbers to classify a finding as RECURRING */
const TWO_STRIKE_THRESHOLD = 2;

/** Default retention window in days (90 days) */
export const DEFAULT_RETENTION_DAYS = 90;

/**
 * Normalize a description for grouping: lowercase, strip issue numbers
 * (e.g. "#1234"), and collapse whitespace.
 */
function normalizeDescription(desc: string): string {
  return desc.toLowerCase().replace(/#\d+/g, "").replace(/\s+/g, " ").trim();
}

/** Internal grouping key for friction items */
interface FrictionGroup {
  skill: string;
  skillFile: string;
  frictionType: string;
  normalizedDescription: string;
  /** Distinct issue numbers that encountered this friction */
  issueNumbers: Set<number>;
  /** All suggested fixes across occurrences */
  suggestedFixes: string[];
  /** All severities seen */
  severities: FrictionSeverity[];
  /** Timestamps for first/last seen */
  timestamps: string[];
}

export class SkillSelfAssessmentSynthesizer {
  /**
   * Synthesize assessment records into improvement proposals.
   *
   * @param records - Parsed assessment record objects (caller handles file I/O)
   * @param retentionDays - Only consider records within this many days (0 = no limit)
   * @returns Synthesis result with proposals for recurring findings
   */
  static synthesize(
    records: AssessmentRecord[],
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): SynthesisResult {
    const now = new Date();
    const cutoff =
      retentionDays > 0 ? new Date(now.getTime() - retentionDays * 24 * 60 * 60 * 1000) : null;

    // Filter by retention window
    const activeRecords = cutoff ? records.filter((r) => new Date(r.timestamp) >= cutoff) : records;

    // Group friction items by (skill, type, normalized description)
    const groups = new Map<string, FrictionGroup>();
    let totalFrictionItems = 0;

    for (const record of activeRecords) {
      for (const friction of record.friction) {
        totalFrictionItems++;
        const normalized = normalizeDescription(friction.description);
        const key = `${record.skill}::${friction.type}::${normalized}`;

        let group = groups.get(key);
        if (!group) {
          group = {
            skill: record.skill,
            skillFile: record.skill_file,
            frictionType: friction.type,
            normalizedDescription: normalized,
            issueNumbers: new Set(),
            suggestedFixes: [],
            severities: [],
            timestamps: [],
          };
          groups.set(key, group);
        }

        group.issueNumbers.add(record.issue_number);
        group.suggestedFixes.push(friction.suggested_fix);
        group.severities.push(friction.severity);
        group.timestamps.push(record.timestamp);
      }
    }

    // Classify groups: RECURRING (≥2 distinct issues) vs ISOLATED
    const proposals: SkillImprovementProposal[] = [];
    let isolatedCount = 0;

    for (const group of groups.values()) {
      if (group.issueNumbers.size >= TWO_STRIKE_THRESHOLD) {
        proposals.push({
          skill_file: group.skillFile,
          finding_pattern: `${group.frictionType}: ${group.normalizedDescription}`,
          occurrence_count: group.issueNumbers.size,
          affected_issues: [...group.issueNumbers].sort((a, b) => a - b),
          severity: SkillSelfAssessmentSynthesizer.worstSeverity(group.severities),
          proposed_change: SkillSelfAssessmentSynthesizer.bestFix(group.suggestedFixes),
          first_seen: SkillSelfAssessmentSynthesizer.earliest(group.timestamps),
          last_seen: SkillSelfAssessmentSynthesizer.latest(group.timestamps),
        });
      } else {
        isolatedCount++;
      }
    }

    // Sort: high severity first, then by occurrence count descending
    proposals.sort((a, b) => {
      const sevDiff = SEVERITY_ORDER[b.severity] - SEVERITY_ORDER[a.severity];
      if (sevDiff !== 0) return sevDiff;
      return b.occurrence_count - a.occurrence_count;
    });

    return {
      synthesized_at: now.toISOString(),
      records_analyzed: activeRecords.length,
      total_friction_items: totalFrictionItems,
      proposals,
      isolated_count: isolatedCount,
    };
  }

  /**
   * Filter out assessment records older than the retention window.
   * Returns records that should be deleted (for caller to handle).
   */
  static findExpiredRecords(
    records: AssessmentRecord[],
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): AssessmentRecord[] {
    if (retentionDays <= 0) return [];
    const cutoff = new Date(Date.now() - retentionDays * 24 * 60 * 60 * 1000);
    return records.filter((r) => new Date(r.timestamp) < cutoff);
  }

  /** Pick the highest severity from a list */
  private static worstSeverity(severities: FrictionSeverity[]): FrictionSeverity {
    let worst: FrictionSeverity = "low";
    for (const s of severities) {
      if (SEVERITY_ORDER[s] > SEVERITY_ORDER[worst]) {
        worst = s;
      }
    }
    return worst;
  }

  /** Pick the longest (most specific) suggested fix */
  private static bestFix(fixes: string[]): string {
    let best = fixes[0];
    for (const fix of fixes) {
      if (fix.length > best.length) {
        best = fix;
      }
    }
    return best;
  }

  /** Find the earliest timestamp in a list */
  private static earliest(timestamps: string[]): string {
    return timestamps.reduce((a, b) => (a < b ? a : b));
  }

  /** Find the latest timestamp in a list */
  private static latest(timestamps: string[]): string {
    return timestamps.reduce((a, b) => (a > b ? a : b));
  }
}
