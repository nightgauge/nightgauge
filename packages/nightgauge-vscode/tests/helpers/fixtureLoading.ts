/**
 * Epic and pipeline fixture generators for E2E tests.
 *
 * Provides richer fixtures built on top of workspaceSetup helpers.
 */

import type { BoardItem } from "../../src/services/IpcClient";
import { createBoardItem } from "../mocks/board-item";
import {
  createTempWorkspace,
  makeIssueContext,
  makePlanningContext,
  makeDevContext,
  type TempWorkspace,
} from "./workspaceSetup";

export interface EpicFixture {
  epic: BoardItem;
  sub1: BoardItem;
  sub2: BoardItem;
  sub3: BoardItem;
}

/**
 * Create a 3-sub-issue epic with sequential blocking relationships.
 * sub2 is blocked by sub1 (CLOSED), sub3 is blocked by sub2 (OPEN).
 */
export function createEpicFixture(baseNumber = 100): EpicFixture {
  const epic = createBoardItem({
    number: baseNumber,
    title: `Epic #${baseNumber}`,
    status: "Ready",
    labels: ["type:epic", "priority:high"],
    isEpic: true,
    subIssues: [
      { number: baseNumber + 1, title: `Sub 1`, state: "CLOSED" },
      { number: baseNumber + 2, title: `Sub 2`, state: "OPEN" },
      { number: baseNumber + 3, title: `Sub 3`, state: "OPEN" },
    ],
  });

  const sub1 = createBoardItem({
    number: baseNumber + 1,
    title: "Sub 1 — foundation",
    status: "Done",
    labels: ["type:feature", "size:S"],
  });

  const sub2 = createBoardItem({
    number: baseNumber + 2,
    title: "Sub 2 — depends on sub1",
    status: "Ready",
    labels: ["type:feature", "size:M"],
    blockedBy: [{ number: baseNumber + 1, title: "Sub 1", state: "CLOSED" }],
  });

  const sub3 = createBoardItem({
    number: baseNumber + 3,
    title: "Sub 3 — depends on sub2",
    status: "Ready",
    labels: ["type:feature", "size:L"],
    blockedBy: [{ number: baseNumber + 2, title: "Sub 2", state: "OPEN" }],
  });

  return { epic, sub1, sub2, sub3 };
}

/**
 * Create a temp workspace pre-seeded with a full pipeline context chain:
 * issue-{N}.json → planning-{N}.json → dev-{N}.json
 */
export function createPipelineWorkspace(
  issueNumber: number,
  overrides: {
    issue?: Record<string, unknown>;
    planning?: Record<string, unknown>;
    dev?: Record<string, unknown>;
  } = {}
): TempWorkspace {
  const issueCtx = makeIssueContext(issueNumber, overrides.issue ?? {});
  const planCtx = makePlanningContext(issueNumber, overrides.planning ?? {});
  const devCtx = makeDevContext(issueNumber, overrides.dev ?? {});

  return createTempWorkspace([
    {
      relativePath: `.nightgauge/pipeline/issue-${issueNumber}.json`,
      content: issueCtx,
    },
    {
      relativePath: `.nightgauge/pipeline/planning-${issueNumber}.json`,
      content: planCtx,
    },
    {
      relativePath: `.nightgauge/pipeline/dev-${issueNumber}.json`,
      content: devCtx,
    },
  ]);
}
