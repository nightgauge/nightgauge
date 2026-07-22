import { describe, expect, it } from "vitest";
import { detectAutonomousStall } from "../../src/utils/autonomousStallDetector";

describe("detectAutonomousStall", () => {
  it("identifies a stalled in-progress issue with a green mergeable PR", () => {
    const result = detectAutonomousStall({
      boardStatus: "In Progress",
      updatedAt: "2026-04-21T10:00:00Z",
      prState: "OPEN",
      prCheckStatus: "SUCCESS",
      prMergeable: "MERGEABLE",
      thresholdMinutes: 60,
      now: new Date("2026-04-21T11:15:00Z"),
    });

    expect(result.stalled).toBe(true);
    expect(result.stalledMinutes).toBe(75);
  });

  it("does not flag a PR whose checks are still running", () => {
    const result = detectAutonomousStall({
      boardStatus: "In Progress",
      updatedAt: "2026-04-21T10:00:00Z",
      prState: "OPEN",
      prCheckStatus: "PENDING",
      prMergeable: "MERGEABLE",
      thresholdMinutes: 60,
      now: new Date("2026-04-21T11:15:00Z"),
    });

    expect(result.stalled).toBe(false);
    expect(result.stalledMinutes).toBe(0);
  });
});
