import { describe, expect, it } from "vitest";
import {
  PipelineFeedbackSignalTypeSchema,
  PipelineFeedbackSignalSchema,
  ConflictContextSchema,
} from "../../../context/schemas/index.js";

// Issue #4072 — CONFLICT_RESOLUTION_NEEDED signal + conflict-context-{N}.json.

describe("PipelineFeedbackSignalTypeSchema", () => {
  it.each([
    "PLAN_REVISION_NEEDED",
    "SCOPE_DISCOVERED",
    "COMPLEXITY_UNDERESTIMATED",
    "MODEL_ESCALATION_NEEDED",
    "ACCEPTANCE_CRITERIA_AMBIGUOUS",
    "CONFLICT_RESOLUTION_NEEDED",
    "OPERATOR_STEER",
  ])('accepts signal type "%s"', (signalType) => {
    expect(PipelineFeedbackSignalTypeSchema.safeParse(signalType).success).toBe(true);
  });

  it("rejects an unknown signal type", () => {
    expect(PipelineFeedbackSignalTypeSchema.safeParse("WILDCARD").success).toBe(false);
  });

  it("validates a full CONFLICT_RESOLUTION_NEEDED signal targeting feature-dev", () => {
    const signal = {
      signal_type: "CONFLICT_RESOLUTION_NEEDED",
      emitted_by_stage: "pr-merge",
      backtrack_target_stage: "feature-dev",
      rationale: "Rebase onto origin/main hit a non-trivial conflict",
      evidence: ["internal/foo.go", "internal/bar.go"],
      severity: "blocking",
    };
    expect(PipelineFeedbackSignalSchema.safeParse(signal).success).toBe(true);
  });
});

describe("ConflictContextSchema", () => {
  const valid = {
    schema_version: "1.0",
    issue_number: 143,
    pr_number: 200,
    branch: "feat/143-thing",
    base_ref: "main",
    conflicting_files: [{ path: "src/a.ts", ours: "const a = 1;", theirs: "const a = 2;" }],
    created_at: "2026-06-25T00:00:00.000Z",
  };

  it("validates a well-formed conflict context", () => {
    expect(ConflictContextSchema.safeParse(valid).success).toBe(true);
  });

  it("rejects input missing conflicting_files", () => {
    const { conflicting_files: _omit, ...withoutFiles } = valid;
    expect(ConflictContextSchema.safeParse(withoutFiles).success).toBe(false);
  });

  it("rejects an empty conflicting_files array", () => {
    expect(ConflictContextSchema.safeParse({ ...valid, conflicting_files: [] }).success).toBe(
      false
    );
  });

  it("rejects a conflicting file missing the theirs blob", () => {
    expect(
      ConflictContextSchema.safeParse({
        ...valid,
        conflicting_files: [{ path: "src/a.ts", ours: "x" }],
      }).success
    ).toBe(false);
  });

  it("tolerates extra fields (passthrough) such as a captured hunk", () => {
    const result = ConflictContextSchema.safeParse({
      ...valid,
      conflicting_files: [{ path: "src/a.ts", ours: "x", theirs: "y", hunk: "@@ -1 +1 @@" }],
    });
    expect(result.success).toBe(true);
  });
});
