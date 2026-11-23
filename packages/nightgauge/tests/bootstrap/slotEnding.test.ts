/**
 * #3487: a partial-delivery no-op does not join a sequential run's Failed
 * issues list, decided on the run result's structured field only.
 */

import { describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { endedPartialNoOp, settleSequentialFailedList } from "../../src/bootstrap/slotEnding";

describe("settleSequentialFailedList (#3487)", () => {
  it("takes a partial-delivery no-op off the Failed issues list", () => {
    const failed = { removeFromFailed: vi.fn() };
    const settled = settleSequentialFailedList(
      { success: false, partialNoOp: { remainder: undefined } },
      3487,
      failed
    );
    expect(settled).toBe(true);
    expect(failed.removeFromFailed).toHaveBeenCalledWith(3487);
  });

  it("keeps an ordinary failure on the list, whatever its text says", () => {
    const failed = { removeFromFailed: vi.fn() };
    expect(settleSequentialFailedList({ success: false }, 12, failed)).toBe(false);
    expect(failed.removeFromFailed).not.toHaveBeenCalled();
  });

  it("does not treat a success as a no-op", () => {
    expect(endedPartialNoOp({ success: true, partialNoOp: {} })).toBe(false);
    expect(endedPartialNoOp({ success: false, partialNoOp: {} })).toBe(true);
    expect(endedPartialNoOp({ success: false })).toBe(false);
  });

  it("is wired into the sequential run's completion, after its stage error recorded the issue", () => {
    const src = readFileSync(path.resolve(__dirname, "../../src/bootstrap/services.ts"), "utf-8");
    expect(src).toContain("sequentialFailedIssue = issueNumber;");
    const complete = src.slice(src.indexOf("onPipelineComplete: (result) => {"));
    expect(complete.slice(0, 600)).toContain(
      "settleSequentialFailedList(result, sequentialFailedIssue, completedIssuesService)"
    );
  });
});
