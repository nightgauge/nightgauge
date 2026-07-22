import { describe, it, expect } from "vitest";
import { SkillSelfAssessmentSynthesizer } from "../../analysis/SkillSelfAssessmentSynthesizer.js";
import {
  AssessmentRecordSchema,
  type AssessmentRecord,
} from "../../analysis/self-assessment-types.js";

// ── Test Data Factories ────────────────────────────────────────────

function makeRecord(
  skill: string,
  issueNumber: number,
  friction: AssessmentRecord["friction"],
  daysAgo: number = 0
): AssessmentRecord {
  const ts = new Date(Date.now() - daysAgo * 24 * 60 * 60 * 1000);
  return {
    schema_version: "1",
    skill,
    skill_file: `skills/nightgauge-${skill}/SKILL.md`,
    issue_number: issueNumber,
    timestamp: ts.toISOString(),
    friction,
  };
}

function friction(
  type: AssessmentRecord["friction"][0]["type"],
  description: string,
  severity: "high" | "medium" | "low" = "medium",
  suggested_fix: string = "Fix the instruction"
): AssessmentRecord["friction"][0] {
  return { type, severity, description, suggested_fix };
}

// ── Schema Validation ──────────────────────────────────────────────

describe("AssessmentRecordSchema", () => {
  it("validates a well-formed assessment record", () => {
    const record = makeRecord("issue-pickup", 42, [
      friction("command_failure", "script not found"),
    ]);
    const result = AssessmentRecordSchema.safeParse(record);
    expect(result.success).toBe(true);
  });

  it("rejects record with empty friction array", () => {
    const record = {
      schema_version: "1",
      skill: "issue-pickup",
      skill_file: "skills/nightgauge-issue-pickup/SKILL.md",
      issue_number: 42,
      timestamp: new Date().toISOString(),
      friction: [],
    };
    const result = AssessmentRecordSchema.safeParse(record);
    expect(result.success).toBe(false);
  });

  it("rejects record with invalid friction type", () => {
    const record = makeRecord("pr-create", 10, [
      {
        type: "invalid_type" as any,
        severity: "high",
        description: "test",
        suggested_fix: "fix",
      },
    ]);
    const result = AssessmentRecordSchema.safeParse(record);
    expect(result.success).toBe(false);
  });

  it("rejects record with wrong schema_version", () => {
    const record = {
      schema_version: "2",
      skill: "issue-pickup",
      skill_file: "skills/nightgauge-issue-pickup/SKILL.md",
      issue_number: 42,
      timestamp: new Date().toISOString(),
      friction: [friction("command_failure", "test")],
    };
    const result = AssessmentRecordSchema.safeParse(record);
    expect(result.success).toBe(false);
  });
});

// ── Synthesizer ────────────────────────────────────────────────────

describe("SkillSelfAssessmentSynthesizer", () => {
  describe("synthesize()", () => {
    it("returns empty proposals for empty records", () => {
      const result = SkillSelfAssessmentSynthesizer.synthesize([]);
      expect(result.proposals).toHaveLength(0);
      expect(result.records_analyzed).toBe(0);
      expect(result.total_friction_items).toBe(0);
      expect(result.isolated_count).toBe(0);
    });

    it("classifies single-occurrence findings as isolated", () => {
      const records = [
        makeRecord("issue-pickup", 100, [friction("command_failure", "script.sh not found")]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.proposals).toHaveLength(0);
      expect(result.isolated_count).toBe(1);
      expect(result.total_friction_items).toBe(1);
    });

    it("generates proposal when same friction appears in ≥2 distinct issues", () => {
      const records = [
        makeRecord("issue-pickup", 100, [
          friction("command_failure", "add-to-project.sh not found", "high", "Use Go binary"),
        ]),
        makeRecord("issue-pickup", 200, [
          friction(
            "command_failure",
            "add-to-project.sh not found",
            "high",
            "Replace with nightgauge project add"
          ),
        ]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.proposals).toHaveLength(1);

      const proposal = result.proposals[0];
      expect(proposal.skill_file).toBe("skills/nightgauge-issue-pickup/SKILL.md");
      expect(proposal.occurrence_count).toBe(2);
      expect(proposal.affected_issues).toEqual([100, 200]);
      expect(proposal.severity).toBe("high");
    });

    it("does NOT count re-runs of the same issue as distinct occurrences", () => {
      const records = [
        makeRecord("pr-create", 100, [friction("stale_reference", "path does not exist")]),
        makeRecord("pr-create", 100, [friction("stale_reference", "path does not exist")]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      // Same issue number — only 1 distinct issue, so isolated
      expect(result.proposals).toHaveLength(0);
      expect(result.isolated_count).toBe(1);
    });

    it("normalizes descriptions: strips issue numbers and collapses whitespace", () => {
      const records = [
        makeRecord("feature-dev", 100, [
          friction("workaround", "Step 5 calls #1234 script that fails"),
        ]),
        makeRecord("feature-dev", 200, [
          friction("workaround", "Step 5 calls #5678   script   that fails"),
        ]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      // After normalization both become "step 5 calls script that fails"
      expect(result.proposals).toHaveLength(1);
      expect(result.proposals[0].occurrence_count).toBe(2);
    });

    it("picks the worst severity across occurrences", () => {
      const records = [
        makeRecord("pr-merge", 100, [friction("command_failure", "binary not found", "low")]),
        makeRecord("pr-merge", 200, [friction("command_failure", "binary not found", "high")]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.proposals[0].severity).toBe("high");
    });

    it("picks the longest (most specific) suggested_fix", () => {
      const records = [
        makeRecord("issue-pickup", 100, [
          friction("stale_reference", "old path", "medium", "Update path"),
        ]),
        makeRecord("issue-pickup", 200, [
          friction(
            "stale_reference",
            "old path",
            "medium",
            "Update the path from hooks/lib/old.sh to nightgauge binary"
          ),
        ]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.proposals[0].proposed_change).toContain("nightgauge binary");
    });

    it("sorts proposals: high severity first, then by occurrence count", () => {
      const records = [
        // Medium severity, 3 occurrences
        makeRecord("feature-dev", 100, [friction("workaround", "medium issue", "medium")]),
        makeRecord("feature-dev", 200, [friction("workaround", "medium issue", "medium")]),
        makeRecord("feature-dev", 300, [friction("workaround", "medium issue", "medium")]),
        // High severity, 2 occurrences
        makeRecord("pr-create", 400, [friction("command_failure", "critical failure", "high")]),
        makeRecord("pr-create", 500, [friction("command_failure", "critical failure", "high")]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.proposals).toHaveLength(2);
      expect(result.proposals[0].severity).toBe("high");
      expect(result.proposals[1].severity).toBe("medium");
    });

    it("handles multiple friction items in a single record", () => {
      const records = [
        makeRecord("feature-validate", 100, [
          friction("command_failure", "problem A"),
          friction("stale_reference", "problem B"),
        ]),
        makeRecord("feature-validate", 200, [friction("command_failure", "problem A")]),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.total_friction_items).toBe(3);
      // Problem A appears in 2 issues → proposal
      expect(result.proposals).toHaveLength(1);
      expect(result.proposals[0].finding_pattern).toContain("problem a");
      // Problem B is isolated
      expect(result.isolated_count).toBe(1);
    });

    it("tracks first_seen and last_seen timestamps", () => {
      const records = [
        makeRecord("issue-pickup", 100, [friction("command_failure", "test issue")], 10), // 10 days ago
        makeRecord("issue-pickup", 200, [friction("command_failure", "test issue")], 2), // 2 days ago
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      const proposal = result.proposals[0];
      expect(new Date(proposal.first_seen).getTime()).toBeLessThan(
        new Date(proposal.last_seen).getTime()
      );
    });
  });

  describe("retention filtering", () => {
    it("excludes records older than retention window", () => {
      const records = [
        makeRecord("issue-pickup", 100, [friction("command_failure", "old issue")], 100), // 100 days ago
        makeRecord("issue-pickup", 200, [friction("command_failure", "old issue")], 95), // 95 days ago
      ];
      // Default 90-day retention — both records are outside window
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.records_analyzed).toBe(0);
      expect(result.proposals).toHaveLength(0);
    });

    it("includes records within retention window", () => {
      const records = [
        makeRecord("issue-pickup", 100, [friction("command_failure", "recent issue")], 30), // 30 days ago
        makeRecord("issue-pickup", 200, [friction("command_failure", "recent issue")], 5), // 5 days ago
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records);
      expect(result.records_analyzed).toBe(2);
      expect(result.proposals).toHaveLength(1);
    });

    it("disables retention filtering when retentionDays is 0", () => {
      const records = [
        makeRecord("issue-pickup", 100, [friction("command_failure", "ancient issue")], 365), // 1 year ago
        makeRecord("issue-pickup", 200, [friction("command_failure", "ancient issue")], 300),
      ];
      const result = SkillSelfAssessmentSynthesizer.synthesize(records, 0);
      expect(result.records_analyzed).toBe(2);
      expect(result.proposals).toHaveLength(1);
    });
  });

  describe("findExpiredRecords()", () => {
    it("returns records older than retention window", () => {
      const records = [
        makeRecord("issue-pickup", 100, [friction("command_failure", "old")], 100),
        makeRecord("issue-pickup", 200, [friction("command_failure", "recent")], 5),
      ];
      const expired = SkillSelfAssessmentSynthesizer.findExpiredRecords(records);
      expect(expired).toHaveLength(1);
      expect(expired[0].issue_number).toBe(100);
    });

    it("returns empty array when retentionDays is 0", () => {
      const records = [makeRecord("issue-pickup", 100, [friction("command_failure", "old")], 1000)];
      const expired = SkillSelfAssessmentSynthesizer.findExpiredRecords(records, 0);
      expect(expired).toHaveLength(0);
    });
  });
});
