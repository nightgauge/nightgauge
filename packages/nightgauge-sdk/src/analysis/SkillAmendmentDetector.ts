/**
 * SkillAmendmentDetector
 *
 * Scans execution history records for recurring schema validation failures and
 * generates amendment proposals for the responsible SKILL.md files.
 *
 * Only surfaces proposals when the same field fails in ≥2 distinct pipeline
 * runs — single-occurrence errors are noise, recurring errors are signal.
 *
 * Supports a `recentRunLimit` option to only analyze the most recent N records,
 * so stale violations from already-fixed schemas naturally age out.
 *
 * Output is informational only. No files are modified; proposals are surfaced
 * in the post-pipeline self-check terminal output.
 */

import type {
  ValidationErrorRecord,
  SkillAmendmentProposal,
  SkillAmendmentAnalysisResult,
} from "./skill-amendment-types.js";

/** Maps pipeline stage names to their responsible SKILL.md paths */
const STAGE_TO_SKILL: Record<string, string> = {
  "issue-pickup": "skills/nightgauge-issue-pickup/SKILL.md",
  "feature-planning": "skills/nightgauge-feature-planning/SKILL.md",
  "feature-dev": "skills/nightgauge-feature-dev/SKILL.md",
  "feature-validate": "skills/nightgauge-feature-validate/SKILL.md",
  "pr-create": "skills/nightgauge-pr-create/SKILL.md",
  "pr-merge": "skills/nightgauge-pr-merge/SKILL.md",
};

/**
 * A run record shape that SkillAmendmentDetector needs.
 * Only requires the fields it actually reads.
 */
/**
 * `stages` is optional because JSONL files contain both `run` records (with
 * stages) and `outcome` records (without stages). Records lacking stages are
 * silently skipped.
 */
interface RunRecordForAmendment {
  issue_number: number;
  stages?: Record<
    string,
    {
      validation_errors?: ValidationErrorRecord[];
    }
  >;
}

interface GroupAccumulator {
  stage: string;
  field: string;
  errorCode: string;
  receivedValues: Set<string>;
  expectedValues: string[];
  affectedRuns: Set<number>;
}

export class SkillAmendmentDetector {
  /**
   * Analyze execution history records and return amendment proposals for
   * recurring schema validation failures.
   *
   * @param records - Execution history records (sorted by recorded_at ascending)
   * @param options.recentRunLimit - Only analyze the last N records. Stale
   *   violations from fixed schemas age out after N clean runs. Default: no limit.
   */
  static analyze(
    records: RunRecordForAmendment[],
    options?: { recentRunLimit?: number }
  ): SkillAmendmentAnalysisResult {
    const groups = new Map<string, GroupAccumulator>();

    // Window to recent records when limit is set
    const effectiveRecords =
      options?.recentRunLimit && options.recentRunLimit > 0
        ? records.slice(-options.recentRunLimit)
        : records;

    for (const record of effectiveRecords) {
      if (!record.stages) continue;
      for (const [stage, stageData] of Object.entries(record.stages)) {
        const errors = stageData.validation_errors;
        if (!errors || errors.length === 0) continue;

        for (const err of errors) {
          const key = `${stage}|${err.path}|${err.code}`;

          if (!groups.has(key)) {
            groups.set(key, {
              stage,
              field: err.path,
              errorCode: err.code,
              receivedValues: new Set(),
              expectedValues: err.expected ?? [],
              affectedRuns: new Set(),
            });
          }

          const group = groups.get(key)!;
          if (err.received !== undefined) {
            group.receivedValues.add(err.received);
          }
          // Merge expected values (in case different runs have different schema info)
          if (err.expected && err.expected.length > 0 && group.expectedValues.length === 0) {
            group.expectedValues = err.expected;
          }
          group.affectedRuns.add(record.issue_number);
        }
      }
    }

    const proposals: SkillAmendmentProposal[] = [];

    for (const group of groups.values()) {
      if (group.affectedRuns.size < 2) continue; // single-occurrence = noise

      const receivedValues = [...group.receivedValues];
      const expectedValues = group.expectedValues;
      const affectedRuns = [...group.affectedRuns].sort((a, b) => a - b);

      proposals.push({
        stage: group.stage,
        skillFile: STAGE_TO_SKILL[group.stage] ?? `skills/${group.stage}/SKILL.md`,
        field: group.field,
        errorCode: group.errorCode,
        receivedValues,
        expectedValues,
        occurrenceCount: group.affectedRuns.size,
        affectedRuns,
        proposedConstraint: SkillAmendmentDetector.buildConstraintText(
          group.errorCode,
          expectedValues,
          receivedValues
        ),
      });
    }

    // Sort by occurrenceCount descending (most frequent first)
    proposals.sort((a, b) => b.occurrenceCount - a.occurrenceCount);

    return {
      analyzedAt: new Date().toISOString(),
      recordsAnalyzed: effectiveRecords.length,
      proposals,
    };
  }

  private static buildConstraintText(
    errorCode: string,
    expectedValues: string[],
    receivedValues: string[]
  ): string {
    switch (errorCode) {
      case "invalid_enum_value": {
        const expected = expectedValues.length > 0 ? expectedValues.join(" | ") : "(see schema)";
        const never =
          receivedValues.length > 0
            ? ` Never use: ${receivedValues.map((v) => `"${v}"`).join(", ")}.`
            : "";
        return `MUST be one of: ${expected}.${never}`;
      }
      case "invalid_type": {
        const expected = expectedValues[0] ?? "array";
        if (expected === "array") {
          return 'MUST be a JSON array (e.g. ["item1", "item2"]) or null. Never a plain string.';
        }
        return `MUST be of type ${expected}, not ${receivedValues[0] ?? "current value"}.`;
      }
      default:
        return "Received unexpected value. Check docs/CONTEXT_ARCHITECTURE.md for the full schema.";
    }
  }
}
