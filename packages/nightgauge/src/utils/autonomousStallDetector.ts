import { BOARD_STATUS, boardStatusEquals } from "./projectFieldMapping";

export interface StallDetectionInput {
  /**
   * The RAW board Status option label, exactly as the board reports it —
   * never a canonicalized value. Compare it with `boardStatusEquals`, never
   * `===` (#623).
   */
  boardStatus: string;
  updatedAt?: string;
  prState?: string;
  prCheckStatus?: string;
  prMergeable?: string;
  thresholdMinutes: number;
  now?: Date;
}

export interface StallDetectionResult {
  stalled: boolean;
  stalledMinutes: number;
}

export function detectAutonomousStall(input: StallDetectionInput): StallDetectionResult {
  const now = input.now ?? new Date();
  const updatedAt = input.updatedAt ? new Date(input.updatedAt) : null;
  // #623 — fold the board-status comparison. `boardStatus` is the raw
  // single-select option label; nightgauge's own provisioner spells this
  // column "In progress" while a hand-made board commonly spells it
  // "In Progress". The previous exact `!== "In Progress"` made this guard
  // return early on every provisioned board, so the watchdog never fired.
  // The PR fields below are GitHub GraphQL enums (fixed, uppercase, not
  // board-provenance-dependent) and are correctly compared exactly.
  if (
    !boardStatusEquals(input.boardStatus, BOARD_STATUS.inProgress) ||
    !updatedAt ||
    Number.isNaN(updatedAt.getTime()) ||
    input.prState !== "OPEN" ||
    input.prCheckStatus !== "SUCCESS" ||
    input.prMergeable !== "MERGEABLE"
  ) {
    return { stalled: false, stalledMinutes: 0 };
  }

  const elapsedMs = now.getTime() - updatedAt.getTime();
  if (elapsedMs < 0) {
    return { stalled: false, stalledMinutes: 0 };
  }

  const stalledMinutes = Math.floor(elapsedMs / 60_000);
  return {
    stalled: stalledMinutes >= input.thresholdMinutes,
    stalledMinutes,
  };
}
