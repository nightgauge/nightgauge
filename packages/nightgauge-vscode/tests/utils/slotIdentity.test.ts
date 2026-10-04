/**
 * #2412: a running slot is named by repository and issue number. Two
 * repositories' issues with one number are two runs in two slots.
 */
import { describe, it, expect } from "vitest";
import { ActiveRunSet, findActiveSlot } from "../../src/utils/slotIdentity";
import type { ActiveSlot } from "../../src/types/queue";

function slot(slotIndex: number, issueNumber: number, repo?: string): ActiveSlot {
  return {
    slotIndex,
    issueNumber,
    worktreePath: `/worktrees/${repo ?? "unknown"}-${issueNumber}`,
    branch: `feat/${issueNumber}`,
    startedAt: "2026-10-04T00:00:00Z",
    ...(repo ? { repo } : {}),
  };
}

describe("findActiveSlot (#2412)", () => {
  const slots = [slot(0, 21, "example-org/platform"), slot(1, 21, "example-org/app")];

  it("finds each same-numbered issue's own slot and worktree", () => {
    expect(findActiveSlot(slots, 21, "example-org/platform")?.worktreePath).toBe(
      "/worktrees/example-org/platform-21"
    );
    expect(findActiveSlot(slots, 21, "Example-Org/App")?.slotIndex).toBe(1);
  });

  it("does not guess when the number names several slots and no repository is given", () => {
    expect(findActiveSlot(slots, 21)).toBeUndefined();
  });

  it("finds no slot for a third repository", () => {
    expect(findActiveSlot(slots, 21, "example-org/infra")).toBeUndefined();
  });

  it("falls back to the number when it names exactly one slot", () => {
    expect(findActiveSlot([slot(0, 21, "example-org/app")], 21)?.slotIndex).toBe(0);
    expect(findActiveSlot([slot(0, 21)], 21, "example-org/app")?.slotIndex).toBe(0);
  });
});

describe("ActiveRunSet (#2412)", () => {
  it("counts two repositories' same-numbered issues as two runs", () => {
    const runs = new ActiveRunSet();
    expect(runs.start(21, "example-org/platform")).toBe(true);
    expect(runs.start(21, "example-org/app")).toBe(true);
    expect(runs.start(21, "example-org/app")).toBe(false);
    expect(runs.size).toBe(2);

    // One run's end does not end the other.
    expect(runs.end(21, "example-org/platform")).toBe(true);
    expect(runs.size).toBe(1);
    expect(runs.end(21, "example-org/platform")).toBe(false);
    expect(runs.end(21, "example-org/app")).toBe(true);
    expect(runs.size).toBe(0);
  });

  it("ends a run by number alone only when the number names exactly one", () => {
    const runs = new ActiveRunSet();
    runs.start(21, "example-org/platform");
    runs.start(21, "example-org/app");
    expect(runs.end(21)).toBe(false);
    expect(runs.size).toBe(2);

    const single = new ActiveRunSet();
    single.start(34, "example-org/app");
    expect(single.end(34)).toBe(true);
  });

  it("keeps the single-repository case unchanged", () => {
    const runs = new ActiveRunSet();
    expect(runs.start(5)).toBe(true);
    expect(runs.start(5)).toBe(false);
    expect(runs.end(5)).toBe(true);
    expect(runs.end(5)).toBe(false);
  });
});
