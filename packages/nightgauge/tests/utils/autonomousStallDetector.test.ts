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

  // -------------------------------------------------------------------------
  // #623 — the board-status comparison must fold.
  //
  // `boardStatus` is the RAW single-select option label. Nightgauge's own
  // provisioner creates the column as "In progress"
  // (`DefaultFieldSchema` in internal/github/project.go, mirrored by
  // `state.StatusInProgress`); a hand-made board commonly spells it
  // "In Progress". The pre-fix guard compared `!== "In Progress"` exactly, so
  // on every provisioned board it returned early and the watchdog never
  // fired — silently.
  //
  // These are load-bearing: against the pre-fix source the "In progress" and
  // "in progress" cases return `{ stalled: false, stalledMinutes: 0 }`.
  // -------------------------------------------------------------------------
  describe("board status is compared case-insensitively (#623)", () => {
    /** Identical except for the board's spelling of the In-progress column. */
    const stalledInput = (boardStatus: string) => ({
      boardStatus,
      updatedAt: "2026-04-21T10:00:00Z",
      prState: "OPEN",
      prCheckStatus: "SUCCESS",
      prMergeable: "MERGEABLE",
      thresholdMinutes: 60,
      now: new Date("2026-04-21T11:15:00Z"),
    });

    it("fires on a nightgauge-provisioned board, whose column is spelled 'In progress'", () => {
      const result = detectAutonomousStall(stalledInput("In progress"));

      expect(result.stalled).toBe(true);
      expect(result.stalledMinutes).toBe(75);
    });

    it("reaches the SAME decision for every capitalization of the same column", () => {
      // "In Progress" is the hand-made-board spelling the pre-fix guard was
      // written against; "In progress" is what the provisioner writes. Both —
      // and any other casing of the same label — name one column, so they must
      // produce byte-identical results.
      const provisioned = detectAutonomousStall(stalledInput("In progress"));
      const handMade = detectAutonomousStall(stalledInput("In Progress"));

      expect(provisioned).toEqual(handMade);
      expect(provisioned.stalled).toBe(true);

      for (const spelling of ["in progress", "IN PROGRESS", "iN pRoGrEsS"]) {
        expect(detectAutonomousStall(stalledInput(spelling))).toEqual(handMade);
      }
    });

    it("still rejects a DIFFERENT column, in any capitalization", () => {
      // Folding must not widen the guard into "any status passes". Only the
      // In-progress column may reach the elapsed-time computation.
      for (const spelling of ["In review", "In Review", "Ready", "done", "Backlog", ""]) {
        const result = detectAutonomousStall(stalledInput(spelling));
        expect(result).toEqual({ stalled: false, stalledMinutes: 0 });
      }
    });

    it("does not fold the PR fields — those are fixed GitHub GraphQL enums", () => {
      // GraphQL enum values are not board-provenance-dependent, so they stay
      // exact. A lowercase enum value is a wire-shape bug, not a spelling
      // variant, and must not be silently accepted.
      expect(
        detectAutonomousStall({ ...stalledInput("In progress"), prState: "open" }).stalled
      ).toBe(false);
      expect(
        detectAutonomousStall({ ...stalledInput("In progress"), prCheckStatus: "success" }).stalled
      ).toBe(false);
      expect(
        detectAutonomousStall({ ...stalledInput("In progress"), prMergeable: "mergeable" }).stalled
      ).toBe(false);
    });
  });
});
