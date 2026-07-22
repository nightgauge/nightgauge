/**
 * prMergeReadiness.test.ts — regression for the production pr-merge punt
 * condition (Issue #297).
 *
 * Every overnight bowlsheet-flutter run paid the LLM pr-merge path ($3–4.44)
 * because pr-merge starts immediately after pr-create, so CI is still in-flight
 * (mergeStateStatus BLOCKED/UNSTABLE, checks pending) and there was no
 * deterministic path that would WAIT for it. These tests pin the decision core:
 * a PR blocked ONLY by pending CI must classify `pending` (→ wait), while a
 * real blocker must classify `blocked` (→ punt), and a green/clean PR `ready`.
 */

import { describe, it, expect } from "vitest";
import {
  classifyMergeReadiness,
  type MergeSnapshot,
} from "../../../src/orchestrator/stages/prMergeReadiness";

const base: MergeSnapshot = {
  state: "OPEN",
  mergeable: "MERGEABLE",
  mergeStateStatus: "CLEAN",
  reviewDecision: "APPROVED",
  checks: [],
};

describe("classifyMergeReadiness", () => {
  it("the production punt condition: BLOCKED + in-flight CI → pending (wait, do not punt)", () => {
    const snap: MergeSnapshot = {
      ...base,
      mergeStateStatus: "BLOCKED",
      checks: [{ name: "flutter-ci", conclusion: "" }],
    };
    expect(classifyMergeReadiness(snap)).toEqual({ kind: "pending" });
  });

  it("UNSTABLE + a queued check → pending", () => {
    const snap: MergeSnapshot = {
      ...base,
      mergeStateStatus: "UNSTABLE",
      checks: [
        { name: "unit", conclusion: "SUCCESS" },
        { name: "integration", conclusion: "QUEUED" },
      ],
    };
    expect(classifyMergeReadiness(snap)).toEqual({ kind: "pending" });
  });

  it("clean + mergeable + green → ready (merge now)", () => {
    const snap: MergeSnapshot = {
      ...base,
      checks: [{ name: "flutter-ci", conclusion: "SUCCESS" }],
    };
    expect(classifyMergeReadiness(snap)).toEqual({ kind: "ready" });
  });

  it("already merged → merged", () => {
    expect(classifyMergeReadiness({ ...base, state: "MERGED" })).toEqual({ kind: "merged" });
  });

  it("a failed check is a hard blocker even while other checks are pending → blocked, never pending", () => {
    const snap: MergeSnapshot = {
      ...base,
      mergeStateStatus: "UNSTABLE",
      checks: [
        { name: "lint", conclusion: "FAILURE" },
        { name: "build", conclusion: "" },
      ],
    };
    const r = classifyMergeReadiness(snap);
    expect(r.kind).toBe("blocked");
    expect((r as { reason: string }).reason).toContain("failed-ci-checks");
  });

  it("conflict → blocked (not-mergeable), never waits", () => {
    const snap: MergeSnapshot = { ...base, mergeable: "CONFLICTING", mergeStateStatus: "DIRTY" };
    const r = classifyMergeReadiness(snap);
    expect(r.kind).toBe("blocked");
    expect((r as { reason: string }).reason).toContain("not-mergeable");
  });

  it("review required blocks even with pending CI → blocked", () => {
    const snap: MergeSnapshot = {
      ...base,
      mergeStateStatus: "BLOCKED",
      reviewDecision: "REVIEW_REQUIRED",
      checks: [{ name: "ci", conclusion: "" }],
    };
    const r = classifyMergeReadiness(snap);
    expect(r.kind).toBe("blocked");
    expect((r as { reason: string }).reason).toContain("review-not-approved");
  });

  it("BLOCKED with all checks concluded (no pending) → blocked, does not spin", () => {
    const snap: MergeSnapshot = {
      ...base,
      mergeStateStatus: "BLOCKED",
      checks: [{ name: "ci", conclusion: "SUCCESS" }],
    };
    const r = classifyMergeReadiness(snap);
    expect(r.kind).toBe("blocked");
    expect((r as { reason: string }).reason).toContain("dirty-merge-state");
  });

  it("BEHIND (branch out of date) → blocked, not pending", () => {
    const snap: MergeSnapshot = {
      ...base,
      mergeStateStatus: "BEHIND",
      checks: [{ name: "ci", conclusion: "" }],
    };
    expect(classifyMergeReadiness(snap).kind).toBe("blocked");
  });
});
