/**
 * BoardItem and ReadyIssue factory helpers for tests.
 *
 * Centralizes mock data construction so individual test files stay focused on
 * assertions rather than fixture boilerplate.
 */

import type { BoardItem } from "../../src/services/IpcClient";
import type { ReadyIssue, BlockingIssue } from "../../src/services/ProjectBoardService";

/**
 * Create a BoardItem (shape returned by IpcClient.boardList) with sensible
 * defaults and optional overrides.
 */
export function createBoardItem(overrides: Partial<BoardItem> = {}): BoardItem {
  const number = overrides.number ?? 1;
  return {
    id: `item-${number}`,
    number,
    title: `Issue #${number}`,
    state: "OPEN",
    status: "Ready",
    priority: "",
    size: "",
    labels: [],
    assignees: [],
    repo: "nightgauge/nightgauge",
    url: `https://github.com/nightgauge/nightgauge/issues/${number}`,
    isEpic: false,
    blockedBy: [],
    blocking: [],
    ...overrides,
  };
}

/**
 * Create a ReadyIssue (shape returned by ProjectBoardService.getIssuesByStatus)
 * with sensible defaults and optional overrides.
 */
export function createReadyIssue(overrides: Partial<ReadyIssue> = {}): ReadyIssue {
  const number = overrides.number ?? 1;
  return {
    number,
    title: `Issue #${number}`,
    labels: [],
    priority: null,
    size: null,
    url: `https://github.com/nightgauge/nightgauge/issues/${number}`,
    status: "Ready",
    ...overrides,
  };
}

/**
 * Create a BlockingIssue reference (used in blockedBy / blocks arrays).
 */
export function createBlockingRef(
  number: number,
  state: "OPEN" | "CLOSED" = "OPEN",
  overrides: Partial<BlockingIssue> = {}
): BlockingIssue {
  return {
    number,
    title: `Issue #${number}`,
    url: `https://github.com/nightgauge/nightgauge/issues/${number}`,
    state,
    ...overrides,
  };
}
