import { describe, it, expect } from "vitest";
import { SkillAmendmentDetector } from "../../analysis/SkillAmendmentDetector.js";
import type { ValidationErrorRecord } from "../../analysis/skill-amendment-types.js";

// ── Test Data Factories ────────────────────────────────────────────

function makeRecord(
  issueNumber: number,
  stageErrors: Record<string, ValidationErrorRecord[]> = {}
) {
  return {
    issue_number: issueNumber,
    stages: Object.fromEntries(
      Object.entries(stageErrors).map(([stage, errs]) => [stage, { validation_errors: errs }])
    ),
  };
}

const enumErr = (path: string, received: string, expected: string[]): ValidationErrorRecord => ({
  path,
  code: "invalid_enum_value",
  message: `Invalid enum value. Expected ${expected.join(" | ")}, received "${received}"`,
  received,
  expected,
});

const typeErr = (path: string, received: string): ValidationErrorRecord => ({
  path,
  code: "invalid_type",
  message: `Expected array, received ${received}`,
  received,
  expected: ["array"],
});

// ── Tests ──────────────────────────────────────────────────────────

describe("SkillAmendmentDetector", () => {
  describe("analyze()", () => {
    it("returns no proposals for empty records", () => {
      const result = SkillAmendmentDetector.analyze([]);
      expect(result.proposals).toHaveLength(0);
      expect(result.recordsAnalyzed).toBe(0);
    });

    it("returns no proposals when no stages have validation_errors", () => {
      const records = [
        { issue_number: 1, stages: { "issue-pickup": {} } },
        { issue_number: 2, stages: { "issue-pickup": {} } },
      ];
      const result = SkillAmendmentDetector.analyze(records);
      expect(result.proposals).toHaveLength(0);
    });

    it("returns no proposal for a single-occurrence error (below threshold)", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [
            enumErr("type", "documentation", ["feature", "bug", "docs", "refactor", "spike"]),
          ],
        }),
      ];
      const result = SkillAmendmentDetector.analyze(records);
      expect(result.proposals).toHaveLength(0);
    });

    it("returns a proposal when the same field fails in 2+ distinct runs", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [
            enumErr("type", "documentation", ["feature", "bug", "docs", "refactor", "spike"]),
          ],
        }),
        makeRecord(101, {
          "issue-pickup": [
            enumErr("type", "documentation", ["feature", "bug", "docs", "refactor", "spike"]),
          ],
        }),
      ];

      const result = SkillAmendmentDetector.analyze(records);
      expect(result.proposals).toHaveLength(1);

      const proposal = result.proposals[0];
      expect(proposal.stage).toBe("issue-pickup");
      expect(proposal.field).toBe("type");
      expect(proposal.errorCode).toBe("invalid_enum_value");
      expect(proposal.occurrenceCount).toBe(2);
      expect(proposal.affectedRuns).toEqual([100, 101]);
      expect(proposal.receivedValues).toContain("documentation");
      expect(proposal.expectedValues).toEqual(["feature", "bug", "docs", "refactor", "spike"]);
    });

    it("generates separate proposals for two different failing fields", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [
            enumErr("type", "documentation", ["feature", "bug", "docs", "refactor", "spike"]),
            typeErr("requirements.technical_notes", "string"),
          ],
        }),
        makeRecord(101, {
          "issue-pickup": [
            enumErr("type", "documentation", ["feature", "bug", "docs", "refactor", "spike"]),
            typeErr("requirements.technical_notes", "string"),
          ],
        }),
      ];

      const result = SkillAmendmentDetector.analyze(records);
      expect(result.proposals).toHaveLength(2);

      const fields = result.proposals.map((p) => p.field).sort();
      expect(fields).toContain("type");
      expect(fields).toContain("requirements.technical_notes");
    });

    it("deduplicates receivedValues across runs", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [enumErr("type", "documentation", ["feature", "bug", "docs"])],
        }),
        makeRecord(101, {
          "issue-pickup": [
            // same bad value in a different run
            enumErr("type", "documentation", ["feature", "bug", "docs"]),
          ],
        }),
        makeRecord(102, {
          "issue-pickup": [
            // different bad value, third run
            enumErr("type", "docs-only", ["feature", "bug", "docs"]),
          ],
        }),
      ];

      const result = SkillAmendmentDetector.analyze(records);
      expect(result.proposals).toHaveLength(1);
      const proposal = result.proposals[0];
      expect(proposal.occurrenceCount).toBe(3);
      // receivedValues should be deduped: "documentation" and "docs-only", not 3 entries
      expect(proposal.receivedValues).toHaveLength(2);
      expect(proposal.receivedValues).toContain("documentation");
      expect(proposal.receivedValues).toContain("docs-only");
    });
  });

  describe("proposedConstraint text", () => {
    it("generates correct text for invalid_enum_value", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [
            enumErr("type", "documentation", ["feature", "bug", "docs", "refactor", "spike"]),
          ],
        }),
        makeRecord(101, {
          "issue-pickup": [
            enumErr("type", "documentation", ["feature", "bug", "docs", "refactor", "spike"]),
          ],
        }),
      ];

      const { proposals } = SkillAmendmentDetector.analyze(records);
      expect(proposals[0].proposedConstraint).toContain("MUST be one of:");
      expect(proposals[0].proposedConstraint).toContain("feature | bug | docs | refactor | spike");
      expect(proposals[0].proposedConstraint).toContain("Never use:");
      expect(proposals[0].proposedConstraint).toContain('"documentation"');
    });

    it("generates correct text for invalid_type (array)", () => {
      const records = [
        makeRecord(100, {
          "feature-dev": [typeErr("requirements.technical_notes", "string")],
        }),
        makeRecord(101, {
          "feature-dev": [typeErr("requirements.technical_notes", "string")],
        }),
      ];

      const { proposals } = SkillAmendmentDetector.analyze(records);
      expect(proposals[0].proposedConstraint).toContain("JSON array");
      expect(proposals[0].proposedConstraint).toContain("Never a plain string");
    });

    it("generates fallback text for unknown error codes", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [{ path: "some.field", code: "custom_error", message: "bad" }],
        }),
        makeRecord(101, {
          "issue-pickup": [{ path: "some.field", code: "custom_error", message: "bad" }],
        }),
      ];

      const { proposals } = SkillAmendmentDetector.analyze(records);
      expect(proposals[0].proposedConstraint).toContain("CONTEXT_ARCHITECTURE.md");
    });
  });

  describe("STAGE_TO_SKILL mapping", () => {
    const allStages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];

    it.each(allStages)("maps %s to a SKILL.md path", (stage) => {
      const records = [
        makeRecord(100, { [stage]: [enumErr("type", "bad", ["good"])] }),
        makeRecord(101, { [stage]: [enumErr("type", "bad", ["good"])] }),
      ];

      const { proposals } = SkillAmendmentDetector.analyze(records);
      expect(proposals[0].skillFile).toMatch(/SKILL\.md$/);
      expect(proposals[0].skillFile).toContain(stage);
    });
  });

  describe("recentRunLimit", () => {
    it("only analyzes the last N records when recentRunLimit is set", () => {
      const records = [
        // Old records (should be excluded with limit=2)
        makeRecord(100, {
          "issue-pickup": [enumErr("type", "doc", ["docs"])],
        }),
        makeRecord(101, {
          "issue-pickup": [enumErr("type", "doc", ["docs"])],
        }),
        // Recent records (no errors)
        makeRecord(102, {}),
        makeRecord(103, {}),
      ];

      // Without limit: old violations surface (2 runs with errors)
      const unlimited = SkillAmendmentDetector.analyze(records);
      expect(unlimited.proposals).toHaveLength(1);
      expect(unlimited.recordsAnalyzed).toBe(4);

      // With limit: only last 2 records analyzed (no errors in those)
      const limited = SkillAmendmentDetector.analyze(records, {
        recentRunLimit: 2,
      });
      expect(limited.proposals).toHaveLength(0);
      expect(limited.recordsAnalyzed).toBe(2);
    });

    it("behaves normally when recentRunLimit is undefined", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [enumErr("type", "doc", ["docs"])],
        }),
        makeRecord(101, {
          "issue-pickup": [enumErr("type", "doc", ["docs"])],
        }),
      ];

      const result = SkillAmendmentDetector.analyze(records, {});
      expect(result.proposals).toHaveLength(1);
      expect(result.recordsAnalyzed).toBe(2);
    });

    it("returns all records when recentRunLimit exceeds record count", () => {
      const records = [
        makeRecord(100, {
          "issue-pickup": [enumErr("type", "doc", ["docs"])],
        }),
        makeRecord(101, {
          "issue-pickup": [enumErr("type", "doc", ["docs"])],
        }),
      ];

      const result = SkillAmendmentDetector.analyze(records, {
        recentRunLimit: 100,
      });
      expect(result.proposals).toHaveLength(1);
      expect(result.recordsAnalyzed).toBe(2);
    });
  });

  describe("sorting", () => {
    it("sorts proposals by occurrenceCount descending", () => {
      const records = [
        // field A: 3 runs
        makeRecord(100, { "issue-pickup": [enumErr("type", "doc", ["docs"])] }),
        makeRecord(101, { "issue-pickup": [enumErr("type", "doc", ["docs"])] }),
        makeRecord(102, { "issue-pickup": [enumErr("type", "doc", ["docs"])] }),
        // field B: 2 runs
        makeRecord(103, {
          "feature-dev": [typeErr("requirements.technical_notes", "string")],
        }),
        makeRecord(104, {
          "feature-dev": [typeErr("requirements.technical_notes", "string")],
        }),
      ];

      const { proposals } = SkillAmendmentDetector.analyze(records);
      expect(proposals).toHaveLength(2);
      expect(proposals[0].occurrenceCount).toBeGreaterThanOrEqual(proposals[1].occurrenceCount);
    });
  });
});
