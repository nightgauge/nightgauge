/**
 * Tests for EpicQueueFilter.
 *
 * Validates the pre-filter that runs when a user drags an epic onto the
 * pipeline queue: sub-issues must be in an eligible project-board status
 * and should not already have an open PR.
 *
 * @see Issue #2992
 */

import { describe, it, expect, vi } from "vitest";

import { filterEligibleSubIssues, summarizeSkipped } from "../../src/services/EpicQueueFilter";
import type { ReadyIssue } from "../../src/services/ProjectBoardService";

function mkIssue(number: number, status: string, opts?: Partial<ReadyIssue>): ReadyIssue {
  return {
    number,
    title: `Issue #${number}`,
    labels: [],
    priority: null,
    size: null,
    url: `https://github.com/test/repo/issues/${number}`,
    status,
    ...opts,
  };
}

const R = "test/repo";

/** Sub-issues of the board's repository, by number. */
function refs(...numbers: number[]) {
  return numbers.map((number) => ({ repo: R, number }));
}

function nums(list: { number: number }[]): number[] {
  return list.map((r) => r.number);
}

function mkBoardService(issues: ReadyIssue[]) {
  return {
    getAllItems: vi.fn().mockResolvedValue(issues),
  };
}

describe("filterEligibleSubIssues", () => {
  it("returns all sub-issues when every one is Ready and no PRs exist", async () => {
    const board = mkBoardService([
      mkIssue(10, "Ready"),
      mkIssue(11, "Ready"),
      mkIssue(12, "Ready"),
    ]);
    const prLookup = vi.fn().mockResolvedValue(null);

    const result = await filterEligibleSubIssues({
      subIssues: refs(10, 11, 12),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup,
    });

    expect(nums(result.eligible).sort()).toEqual([10, 11, 12]);
    expect(result.skipped).toEqual([]);
    // PR lookup runs for every candidate that passes the status filter.
    expect(prLookup).toHaveBeenCalledTimes(3);
    // Each lookup runs in the sub-issue's own repository (#2382).
    expect(prLookup).toHaveBeenCalledWith(10, "/ws", R);
  });

  it("skips Backlog and in-review items with the right reason", async () => {
    const board = mkBoardService([
      mkIssue(10, "Ready"),
      mkIssue(11, "Backlog"),
      mkIssue(12, "In review"),
    ]);

    const result = await filterEligibleSubIssues({
      subIssues: refs(10, 11, 12),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup: vi.fn().mockResolvedValue(null),
    });

    expect(nums(result.eligible)).toEqual([10]);
    expect(result.skipped).toEqual([
      { number: 11, repo: R, reason: "status", detail: "Backlog" },
      { number: 12, repo: R, reason: "status", detail: "In review" },
    ]);
  });

  it("skips Ready items that already have an open PR when skipIfOpenPR=true", async () => {
    const board = mkBoardService([mkIssue(10, "Ready"), mkIssue(11, "Ready")]);
    const prLookup = vi.fn().mockImplementation(async (n: number) => {
      return n === 11 ? { number: 42, url: "https://github.com/test/repo/pull/42" } : null;
    });

    const result = await filterEligibleSubIssues({
      subIssues: refs(10, 11),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup,
    });

    expect(nums(result.eligible)).toEqual([10]);
    expect(result.skipped).toEqual([
      { number: 11, repo: R, reason: "open-pr", detail: "https://github.com/test/repo/pull/42" },
    ]);
  });

  it("keeps Ready items with open PRs when skipIfOpenPR=false", async () => {
    const board = mkBoardService([mkIssue(10, "Ready")]);
    const prLookup = vi.fn().mockResolvedValue({ number: 99, url: "https://example.com/pr/99" });

    const result = await filterEligibleSubIssues({
      subIssues: refs(10),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      skipIfOpenPR: false,
      prLookup,
    });

    expect(nums(result.eligible)).toEqual([10]);
    expect(result.skipped).toEqual([]);
    // PR lookup is bypassed entirely when the caller opts out.
    expect(prLookup).not.toHaveBeenCalled();
  });

  it("conservatively skips sub-issues not found in the board cache", async () => {
    const board = mkBoardService([mkIssue(10, "Ready")]);

    const result = await filterEligibleSubIssues({
      subIssues: refs(10, 404),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup: vi.fn().mockResolvedValue(null),
    });

    expect(nums(result.eligible)).toEqual([10]);
    expect(result.skipped).toEqual([{ number: 404, repo: R, reason: "missing" }]);
  });

  it("returns empty sets when subIssues is empty and does not touch the board", async () => {
    const board = mkBoardService([]);
    const prLookup = vi.fn();

    const result = await filterEligibleSubIssues({
      subIssues: refs(),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup,
    });

    expect(nums(result.eligible)).toEqual([]);
    expect(result.skipped).toEqual([]);
    expect(board.getAllItems).not.toHaveBeenCalled();
    expect(prLookup).not.toHaveBeenCalled();
  });

  it("treats eligibleStatuses as case-insensitive", async () => {
    const board = mkBoardService([mkIssue(10, "ready"), mkIssue(11, "READY")]);

    const result = await filterEligibleSubIssues({
      subIssues: refs(10, 11),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      eligibleStatuses: ["Ready"],
      prLookup: vi.fn().mockResolvedValue(null),
    });

    expect(nums(result.eligible).sort()).toEqual([10, 11]);
  });

  it("tolerates prLookup throwing (gh offline) by treating the issue as PR-free", async () => {
    const board = mkBoardService([mkIssue(10, "Ready")]);
    const prLookup = vi.fn().mockRejectedValue(new Error("gh offline"));

    const result = await filterEligibleSubIssues({
      subIssues: refs(10),
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup,
    });

    expect(nums(result.eligible)).toEqual([10]);
    expect(result.skipped).toEqual([]);
  });
});

describe("filterEligibleSubIssues across repositories (#2382)", () => {
  // The board holds test/repo#21 (Ready). The epic's sub-issues are
  // test/repo#21 and test/app#21: the same number in two repositories.
  it("matches a sub-issue to the board item of its own repository only", async () => {
    const board = mkBoardService([mkIssue(21, "Ready")]);
    const prLookup = vi.fn().mockResolvedValue(null);

    const result = await filterEligibleSubIssues({
      subIssues: [
        { repo: R, number: 21 },
        { repo: "test/app", number: 21 },
      ],
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup,
    });

    expect(result.eligible).toEqual([{ repo: R, number: 21 }]);
    expect(result.skipped).toEqual([{ number: 21, repo: "test/app", reason: "missing" }]);
    expect(prLookup).toHaveBeenCalledTimes(1);
  });

  it("does not let another repository's Ready #21 admit this one", async () => {
    const board = mkBoardService([
      mkIssue(21, "Ready", { repo: "test/app", url: "https://github.com/test/app/issues/21" }),
      mkIssue(21, "Backlog"),
    ]);

    const result = await filterEligibleSubIssues({
      subIssues: [{ repo: R, number: 21 }],
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup: vi.fn().mockResolvedValue(null),
    });

    expect(result.eligible).toEqual([]);
    expect(result.skipped).toEqual([{ number: 21, repo: R, reason: "status", detail: "Backlog" }]);
  });

  it("compares repositories case-insensitively", async () => {
    const board = mkBoardService([mkIssue(21, "Ready")]);

    const result = await filterEligibleSubIssues({
      subIssues: [{ repo: "Test/Repo", number: 21 }],
      workspaceRoot: "/ws",
      projectBoardService: board as any,
      prLookup: vi.fn().mockResolvedValue(null),
    });

    expect(nums(result.eligible)).toEqual([21]);
  });
});

describe("summarizeSkipped", () => {
  it("returns empty string for an empty list", () => {
    expect(summarizeSkipped([])).toBe("");
  });

  it("counts skipped reasons by their human label", () => {
    const out = summarizeSkipped([
      { number: 1, repo: R, reason: "status", detail: "Backlog" },
      { number: 2, repo: R, reason: "status", detail: "Backlog" },
      { number: 3, repo: R, reason: "status", detail: "In review" },
      { number: 4, repo: R, reason: "open-pr", detail: "https://x.test/pr/1" },
      { number: 5, repo: R, reason: "missing" },
    ]);

    expect(out).toContain("Backlog: 2");
    expect(out).toContain("In review: 1");
    expect(out).toContain("open PR: 1");
    expect(out).toContain("not in board cache: 1");
  });
});
