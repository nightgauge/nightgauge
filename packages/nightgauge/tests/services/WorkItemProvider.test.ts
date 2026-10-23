/**
 * Unit tests for WorkItemProvider types and helpers.
 *
 * Validates:
 * - normalizeToWorkItem() maps all ReadyIssue fields to WorkItem correctly
 * - isBlocked() type guard based on blockedBy state
 * - isEpicItem() type narrowing for epics with sub-issues
 * - WorkItem interface completeness (construct with all optional fields, no TS errors)
 *
 * @see Issue #2565
 */

import { describe, it, expect } from "vitest";
import {
  normalizeToWorkItem,
  isBlocked,
  isEpicItem,
  type WorkItem,
  type WorkItemSource,
} from "../../src/services/types/WorkItemProvider";
import type { ReadyIssue, BlockingIssue } from "../../src/services/ProjectBoardService";

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const OPEN_BLOCKER: BlockingIssue = {
  number: 100,
  title: "Blocking issue",
  url: "https://github.com/owner/repo/issues/100",
  state: "OPEN",
};

const CLOSED_BLOCKER: BlockingIssue = {
  number: 101,
  title: "Old blocker",
  url: "https://github.com/owner/repo/issues/101",
  state: "CLOSED",
};

function makeReadyIssue(overrides: Partial<ReadyIssue> = {}): ReadyIssue {
  return {
    number: 42,
    title: "Test issue",
    labels: ["feature", "priority:high"],
    priority: "P1",
    size: "M",
    url: "https://github.com/owner/repo/issues/42",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// normalizeToWorkItem()
// ---------------------------------------------------------------------------

describe("normalizeToWorkItem", () => {
  it("maps all required ReadyIssue fields to WorkItem", () => {
    const issue = makeReadyIssue();
    const item = normalizeToWorkItem(issue);

    expect(item.number).toBe(42);
    expect(item.title).toBe("Test issue");
    expect(item.labels).toEqual(["feature", "priority:high"]);
    expect(item.priority).toBe("P1");
    expect(item.size).toBe("M");
    expect(item.url).toBe("https://github.com/owner/repo/issues/42");
  });

  it("maps optional fields when present", () => {
    const issue = makeReadyIssue({
      status: "Ready",
      epicRef: 10,
      epicTitle: "Epic: Foundation",
      isEpic: false,
      subIssueNumbers: [],
      blockedBy: [OPEN_BLOCKER],
      blocks: [CLOSED_BLOCKER],
    });
    const item = normalizeToWorkItem(issue);

    expect(item.status).toBe("Ready");
    expect(item.epicRef).toBe(10);
    expect(item.epicTitle).toBe("Epic: Foundation");
    expect(item.isEpic).toBe(false);
    expect(item.subIssueNumbers).toEqual([]);
    expect(item.blockedBy).toEqual([OPEN_BLOCKER]);
    expect(item.blocks).toEqual([CLOSED_BLOCKER]);
  });

  it("sets source when provided", () => {
    const source: WorkItemSource = { provider: "github", repository: "owner/repo", projectId: 7 };
    const item = normalizeToWorkItem(makeReadyIssue(), source);

    expect(item.source).toEqual(source);
  });

  it("leaves source undefined when not provided", () => {
    const item = normalizeToWorkItem(makeReadyIssue());
    expect(item.source).toBeUndefined();
  });

  it("produces a lossless mapping — no field truncation", () => {
    const issue = makeReadyIssue({
      priority: "P0",
      size: "XL",
      blockedBy: [OPEN_BLOCKER, CLOSED_BLOCKER],
      blocks: [OPEN_BLOCKER],
      isEpic: true,
      subIssueNumbers: [1, 2, 3],
    });
    const item = normalizeToWorkItem(issue);

    expect(item.priority).toBe("P0");
    expect(item.size).toBe("XL");
    expect(item.blockedBy).toHaveLength(2);
    expect(item.blocks).toHaveLength(1);
    expect(item.isEpic).toBe(true);
    expect(item.subIssueNumbers).toEqual([1, 2, 3]);
  });

  it("maps null priority and size", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ priority: null, size: null }));
    expect(item.priority).toBeNull();
    expect(item.size).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// isBlocked()
// ---------------------------------------------------------------------------

describe("isBlocked", () => {
  it("returns false when blockedBy is absent", () => {
    const item = normalizeToWorkItem(makeReadyIssue());
    expect(isBlocked(item)).toBe(false);
  });

  it("returns false when blockedBy is empty", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ blockedBy: [] }));
    expect(isBlocked(item)).toBe(false);
  });

  it("returns false when all blockers are CLOSED", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ blockedBy: [CLOSED_BLOCKER] }));
    expect(isBlocked(item)).toBe(false);
  });

  it("returns true when at least one blocker is OPEN", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ blockedBy: [OPEN_BLOCKER] }));
    expect(isBlocked(item)).toBe(true);
  });

  it("returns true when mixed OPEN and CLOSED blockers", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ blockedBy: [CLOSED_BLOCKER, OPEN_BLOCKER] }));
    expect(isBlocked(item)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// isEpicItem()
// ---------------------------------------------------------------------------

describe("isEpicItem", () => {
  it("returns false when isEpic is absent", () => {
    const item = normalizeToWorkItem(makeReadyIssue());
    expect(isEpicItem(item)).toBe(false);
  });

  it("returns false when isEpic is false", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ isEpic: false }));
    expect(isEpicItem(item)).toBe(false);
  });

  it("returns false when isEpic is true but subIssueNumbers is empty", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ isEpic: true, subIssueNumbers: [] }));
    expect(isEpicItem(item)).toBe(false);
  });

  it("returns false when isEpic is true but subIssueNumbers is absent", () => {
    const item = normalizeToWorkItem(makeReadyIssue({ isEpic: true }));
    expect(isEpicItem(item)).toBe(false);
  });

  it("returns true when isEpic is true and subIssueNumbers has entries", () => {
    const item = normalizeToWorkItem(
      makeReadyIssue({ isEpic: true, subIssueNumbers: [10, 11, 12] })
    );
    expect(isEpicItem(item)).toBe(true);
  });

  it("narrows type: subIssueNumbers is number[] after guard passes", () => {
    const item: WorkItem = normalizeToWorkItem(
      makeReadyIssue({ isEpic: true, subIssueNumbers: [10, 11] })
    );
    if (isEpicItem(item)) {
      // TypeScript should allow this without error after narrowing
      const nums: number[] = item.subIssueNumbers;
      expect(nums).toEqual([10, 11]);
    } else {
      throw new Error("Expected isEpicItem to return true");
    }
  });
});

// ---------------------------------------------------------------------------
// WorkItem interface completeness
// ---------------------------------------------------------------------------

describe("WorkItem interface completeness", () => {
  it("accepts a WorkItem with all optional fields set", () => {
    const item: WorkItem = {
      number: 99,
      title: "Full item",
      labels: ["bug"],
      priority: "P2",
      size: "S",
      url: "https://github.com/owner/repo/issues/99",
      status: "In progress",
      epicRef: 5,
      epicTitle: "Epic title",
      blockedBy: [OPEN_BLOCKER],
      blocks: [CLOSED_BLOCKER],
      isEpic: false,
      subIssueNumbers: [],
      source: { provider: "github", repository: "owner/repo" },
    };

    // No TypeScript errors means the interface is structurally complete
    expect(item.number).toBe(99);
    expect(item.source?.provider).toBe("github");
  });

  it("accepts a minimal WorkItem with only required fields", () => {
    const item: WorkItem = {
      number: 1,
      title: "Minimal",
      labels: [],
      priority: null,
      size: null,
      url: "https://github.com/owner/repo/issues/1",
    };

    expect(item.number).toBe(1);
    expect(item.status).toBeUndefined();
    expect(item.source).toBeUndefined();
  });
});
