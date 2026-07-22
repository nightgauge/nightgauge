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
// extractor matches — see AutoRetroService.test.ts. Keep the two in sync.

import { describe, expect, it } from "vitest";

import { describeMergeBlocker } from "../../src/services/HeadlessOrchestrator";

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

  it("produces a line the AutoRetroService merge-blocked extractor matches", () => {
    const blocker = describeMergeBlocker("MERGEABLE", "UNSTABLE", [
      { name: "Sync E2E (Docker)", conclusion: "FAILURE" },
    ]);
    // Mirror of the extractor regex in AutoRetroService.ts.
    expect(blocker).toMatch(
      /blocked by (?:failing check|required review|review|merge conflict|non-mergeable state)/i
    );
  });
});
