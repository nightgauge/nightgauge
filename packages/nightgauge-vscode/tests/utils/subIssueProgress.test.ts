import { describe, it, expect } from "vitest";
import {
  calculateProgress,
  formatProgressText,
  SubIssue,
  Progress,
} from "../../src/utils/subIssueProgress";
import { createMockSubIssue } from "../mocks/sub-issues";

describe("calculateProgress", () => {
  it("should return zero progress for empty array", () => {
    const result = calculateProgress([]);

    expect(result).toEqual({
      open: 0,
      closed: 0,
      total: 0,
    });
  });

  it("should return 100% when all issues are closed", () => {
    const subIssues: SubIssue[] = [
      createMockSubIssue({ number: 1, state: "CLOSED" }),
      createMockSubIssue({ number: 2, state: "CLOSED" }),
      createMockSubIssue({ number: 3, state: "CLOSED" }),
    ];

    const result = calculateProgress(subIssues);

    expect(result).toEqual({
      open: 0,
      closed: 3,
      total: 3,
    });
  });

  it("should return 0% when all issues are open", () => {
    const subIssues: SubIssue[] = [
      createMockSubIssue({ number: 1, state: "OPEN" }),
      createMockSubIssue({ number: 2, state: "OPEN" }),
      createMockSubIssue({ number: 3, state: "OPEN" }),
    ];

    const result = calculateProgress(subIssues);

    expect(result).toEqual({
      open: 3,
      closed: 0,
      total: 3,
    });
  });

  it("should correctly calculate progress for mixed states", () => {
    const subIssues: SubIssue[] = [
      createMockSubIssue({ number: 1, state: "OPEN" }),
      createMockSubIssue({ number: 2, state: "CLOSED" }),
      createMockSubIssue({ number: 3, state: "OPEN" }),
      createMockSubIssue({ number: 4, state: "CLOSED" }),
      createMockSubIssue({ number: 5, state: "CLOSED" }),
    ];

    const result = calculateProgress(subIssues);

    expect(result).toEqual({
      open: 2,
      closed: 3,
      total: 5,
    });
  });

  it("should handle single issue", () => {
    const subIssues: SubIssue[] = [createMockSubIssue({ number: 1, state: "CLOSED" })];

    const result = calculateProgress(subIssues);

    expect(result).toEqual({
      open: 0,
      closed: 1,
      total: 1,
    });
  });
});

describe("formatProgressText", () => {
  it('should format empty progress as "0% (0/0)"', () => {
    const progress: Progress = { open: 0, closed: 0, total: 0 };

    const result = formatProgressText(progress);

    expect(result).toBe("0% (0/0)");
  });

  it("should format 100% completion correctly", () => {
    const progress: Progress = { open: 0, closed: 5, total: 5 };

    const result = formatProgressText(progress);

    expect(result).toBe("100% (5/5)");
  });

  it("should format 0% completion correctly", () => {
    const progress: Progress = { open: 5, closed: 0, total: 5 };

    const result = formatProgressText(progress);

    expect(result).toBe("0% (0/5)");
  });

  it("should format 60% completion correctly", () => {
    const progress: Progress = { open: 2, closed: 3, total: 5 };

    const result = formatProgressText(progress);

    expect(result).toBe("60% (3/5)");
  });

  it("should format 67% completion correctly (rounded)", () => {
    const progress: Progress = { open: 1, closed: 2, total: 3 };

    const result = formatProgressText(progress);

    expect(result).toBe("67% (2/3)");
  });

  it("should format 33% completion correctly (rounded down)", () => {
    const progress: Progress = { open: 2, closed: 1, total: 3 };

    const result = formatProgressText(progress);

    expect(result).toBe("33% (1/3)");
  });

  it("should handle single issue completion", () => {
    const progress: Progress = { open: 0, closed: 1, total: 1 };

    const result = formatProgressText(progress);

    expect(result).toBe("100% (1/1)");
  });

  it("should handle large numbers correctly", () => {
    const progress: Progress = { open: 25, closed: 75, total: 100 };

    const result = formatProgressText(progress);

    expect(result).toBe("75% (75/100)");
  });
});
