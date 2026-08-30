// Tests for describeMergeBlocker() — Issue #3924.
//
// A pr-merge stage can legitimately decline to merge an OPEN PR that is held
// by a deterministically-known blocker (a failing non-required check →
// mergeStateStatus=UNSTABLE, a required review, or a merge conflict). The
// pre-#3924 path discarded that reason and surfaced a generic "reported
// success but PR is not merged" alarm, which the retro classifier then logged
// as `unknown`. describeMergeBlocker renders the blocker into one
// classifier-friendly line so the failure error names the real cause and the
// retro emits `merge-blocked` instead.
//
// The strings here are also the inputs the AutoRetroService `merge-blocked`
// extractor matches. The final describe block pins that cross-module contract
// against the REAL classifier rather than a copy of its regex (#498).

import { describe, expect, it } from "vitest";

import { describeMergeBlocker } from "../../src/services/HeadlessOrchestrator";
import { AutoRetroService } from "../../src/services/AutoRetroService";

describe("describeMergeBlocker", () => {
  it("names the failing check(s) and the merge state for an UNSTABLE PR", () => {
    const blocker = describeMergeBlocker("MERGEABLE", "UNSTABLE", [
      { name: "Sync E2E (Docker)", conclusion: "FAILURE" },
    ]);
    expect(blocker).toBe(
      'blocked by failing check "Sync E2E (Docker)" (mergeStateStatus=UNSTABLE).'
    );
  });

  it("lists multiple failing checks", () => {
    const blocker = describeMergeBlocker("MERGEABLE", "UNSTABLE", [
      { name: "Sync E2E (Docker)", conclusion: "FAILURE" },
      { name: "Lint", conclusion: "ERROR" },
    ]);
    expect(blocker).toContain('"Sync E2E (Docker)"');
    expect(blocker).toContain('"Lint"');
    expect(blocker).toContain("mergeStateStatus=UNSTABLE");
  });

  it("identifies a merge conflict (DIRTY / CONFLICTING)", () => {
    expect(describeMergeBlocker("CONFLICTING", "DIRTY", [])).toMatch(/merge conflict/i);
    expect(describeMergeBlocker("UNKNOWN", "DIRTY", [])).toMatch(/merge conflict/i);
  });

  it("identifies a branch-behind state", () => {
    expect(describeMergeBlocker("MERGEABLE", "BEHIND", [])).toMatch(/BEHIND/);
  });

  it("identifies a review / branch-protection block", () => {
    expect(describeMergeBlocker("MERGEABLE", "BLOCKED", [])).toMatch(/review|branch protection/i);
  });

  it("falls back to a generic non-mergeable description", () => {
    const blocker = describeMergeBlocker("UNKNOWN", "UNKNOWN", []);
    expect(blocker).toMatch(/non-mergeable state/i);
    expect(blocker).toContain("mergeable=UNKNOWN");
  });

  // #498 — this block used to assert the rendered line against a LOCAL COPY of
  // the AutoRetroService extractor regex, so drift on either side of the
  // contract left it green. It now feeds the shipped renderer's output to the
  // shipped classifier: a change to describeMergeBlocker's wording OR to the
  // extractor's pattern breaks it.
  describe("renders lines the shipped AutoRetroService classifier reads as merge-blocked", () => {
    const cases: Array<{
      label: string;
      mergeable: string;
      mergeState: string;
      checks: Array<{ name: string; conclusion: string }>;
    }> = [
      {
        label: "failing check / UNSTABLE",
        mergeable: "MERGEABLE",
        mergeState: "UNSTABLE",
        checks: [{ name: "Sync E2E (Docker)", conclusion: "FAILURE" }],
      },
      {
        label: "merge conflict / DIRTY",
        mergeable: "CONFLICTING",
        mergeState: "DIRTY",
        checks: [],
      },
      { label: "branch behind / BEHIND", mergeable: "MERGEABLE", mergeState: "BEHIND", checks: [] },
      {
        label: "required review / BLOCKED",
        mergeable: "MERGEABLE",
        mergeState: "BLOCKED",
        checks: [],
      },
      {
        label: "generic non-mergeable state",
        mergeable: "UNKNOWN",
        mergeState: "UNKNOWN",
        checks: [],
      },
    ];

    for (const { label, mergeable, mergeState, checks } of cases) {
      it(`classifies ${label} as merge-blocked, not skill-no-op`, () => {
        const blocker = describeMergeBlocker(mergeable, mergeState, checks);
        const findings = AutoRetroService.classifyFailure(
          {
            // The shape the orchestrator actually emits: the generic
            // "not merged" alarm with the blocker reason appended.
            text: `pr-merge reported success but PR #73 is not merged (state: OPEN). ${blocker}`,
            sourcesAnalyzed: ["terminal_reason"],
          },
          "pr-merge"
        );
        expect(findings[0].category).toBe("merge-blocked");
      });
    }

    it("carries the failing check name through into the retro evidence", () => {
      const blocker = describeMergeBlocker("MERGEABLE", "UNSTABLE", [
        { name: "Sync E2E (Docker)", conclusion: "FAILURE" },
      ]);
      const findings = AutoRetroService.classifyFailure(
        { text: blocker, sourcesAnalyzed: ["terminal_reason"] },
        "pr-merge"
      );
      expect(findings[0].category).toBe("merge-blocked");
      expect(findings[0].evidence.join(" ")).toContain("Sync E2E (Docker)");
    });
  });
});
