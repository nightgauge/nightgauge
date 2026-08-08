import { describe, expect, it } from "vitest";
import {
  PipelineFeedbackSignalTypeSchema,
  PipelineFeedbackSignalSchema,
  ConflictContextSchema,
  isUnrecordedConflictFile,
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

  // #301: the presence/mode fields must survive parsing. `.passthrough()` is on
  // the TOP-LEVEL object only; ConflictFileSchema is a plain z.object, and zod
  // strips unknown keys from those — so before they were declared here, the
  // disambiguation between "deleted on that side" and "empty file" was silently
  // dropped for every TypeScript consumer.
  it("keeps the per-side presence and mode fields on a parsed entry", () => {
    const result = ConflictContextSchema.safeParse({
      ...valid,
      conflict_operation: "rebase",
      conflicting_files: [
        {
          path: "sub",
          ours: "",
          theirs: "",
          ours_present: true,
          theirs_present: true,
          ours_mode: "160000",
          theirs_mode: "160000",
          ours_commit: "1111111111111111111111111111111111111111",
          theirs_commit: "2222222222222222222222222222222222222222",
        },
      ],
    });
    expect(result.success).toBe(true);
    const entry = result.success ? result.data.conflicting_files[0] : undefined;
    expect(entry?.ours_mode).toBe("160000");
    expect(entry?.ours_commit).toBe("1111111111111111111111111111111111111111");
    expect(entry?.theirs_present).toBe(true);
  });

  it("accepts the capture_failed marker the shell writer sets on a failed capture", () => {
    expect(ConflictContextSchema.safeParse({ ...valid, capture_failed: true }).success).toBe(true);
  });
});

// #301 round-2 findings 2/4 — the reader-side predicate that decides whether an
// entry actually recorded its conflict. Mirrors
// conflictContextEntry.unexplainedEmpty in the Go recovery package.
describe("isUnrecordedConflictFile", () => {
  it("flags the silent-empty pair a failed `git show :2:<path>` produces", () => {
    expect(isUnrecordedConflictFile({ path: "caf\\303\\251.txt", ours: "", theirs: "" })).toBe(
      true
    );
  });

  it("does not flag a gitlink, whose content is a commit id rather than bytes", () => {
    expect(
      isUnrecordedConflictFile({
        path: "sub",
        ours: "",
        theirs: "",
        ours_mode: "160000",
        theirs_mode: "160000",
        ours_commit: "1111111111111111111111111111111111111111",
        theirs_commit: "2222222222222222222222222222222222222222",
      })
    ).toBe(false);
  });

  it("does not flag a side the index genuinely does not carry", () => {
    expect(
      isUnrecordedConflictFile({
        path: "gone.txt",
        ours: "",
        theirs: "",
        ours_present: false,
        theirs_present: false,
      })
    ).toBe(false);
  });

  it("does not flag an entry with content on either side", () => {
    expect(isUnrecordedConflictFile({ path: "a.ts", ours: "x", theirs: "" })).toBe(false);
    expect(isUnrecordedConflictFile({ path: "a.ts", ours: "", theirs: "y" })).toBe(false);
  });

  it("flags a symlink pair with both sides empty — 120000 is an ordinary blob", () => {
    expect(
      isUnrecordedConflictFile({
        path: "link",
        ours: "",
        theirs: "",
        ours_mode: "120000",
        theirs_mode: "120000",
      })
    ).toBe(true);
  });
});
