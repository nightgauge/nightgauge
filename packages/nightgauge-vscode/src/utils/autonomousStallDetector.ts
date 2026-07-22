export interface StallDetectionInput {
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
  if (
    input.boardStatus !== "In Progress" ||
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
