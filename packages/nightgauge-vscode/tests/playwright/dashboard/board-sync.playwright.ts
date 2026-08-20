/**
 * board-sync.test.ts — E2E tests for board sync refresh workflows (Issue #2749).
 *
 * Tests three critical board sync scenarios:
 *   1. Refresh board and display updated items
 *   2. Group issues by epic (sub-issues appear under epic header)
 *   3. Handle blocking relationships (lock icon on blocked issues)
 *
 * All IPC calls are mocked — no real GitHub API required.
 */

import { test, expect } from "@playwright/test";
import { createIpcMock } from "../helpers/ipc-mock.js";
import {
  loadDashboard,
  waitForDashboard,
  waitForBoardItem,
  getBoardItemCount,
  getDashboardMessages,
} from "../helpers/dashboard-loader.js";
import {
  makeDashboardHtml,
  makeMockBoard,
  makeMockIssue,
  makeMockEpic,
} from "../helpers/test-data.js";

// ---------------------------------------------------------------------------
// Scenario 1: Refresh Board and Display Updates
// ---------------------------------------------------------------------------

test("Refresh board: clicking Refresh Project Board triggers boardList IPC call and updates item count", async ({
  page,
}) => {
  // Start: 2 items in HTML; after refresh: 4 items from mock
  const initialItems = makeMockBoard({ readyCount: 2, inProgressCount: 0 });
  const refreshedItems = [
    ...makeMockBoard({ readyCount: 3, inProgressCount: 1 }),
    makeMockIssue({ number: 999, status: "Ready", title: "New issue from refresh" }),
  ];

  const mock = createIpcMock({ boardItems: refreshedItems });
  const html = makeDashboardHtml({ boardItems: initialItems });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Initial count
  const initialCount = await getBoardItemCount(page);
  expect(initialCount).toBe(2);

  // Click Refresh
  await page.click('[data-test-id="refresh-board-btn"]');

  // Wait for new item to appear
  await waitForBoardItem(page, 999);

  // Item count should increase to match refreshed items
  const newCount = await getBoardItemCount(page);
  expect(newCount).toBe(refreshedItems.length);

  // Verify boardList IPC call was made
  const calls = await mock.getCallsFor(page, "boardList");
  expect(calls.length).toBeGreaterThanOrEqual(1);

  // Verify refresh message was posted
  const messages = await getDashboardMessages(page);
  expect(messages).toContainEqual(expect.objectContaining({ type: "refreshBoard" }));
});

test("Refresh board: all returned items are rendered with correct issue numbers", async ({
  page,
}) => {
  const refreshedItems = [
    makeMockIssue({ number: 201, status: "Ready" }),
    makeMockIssue({ number: 202, status: "Ready" }),
    makeMockIssue({ number: 203, status: "In Progress" }),
  ];

  const mock = createIpcMock({ boardItems: refreshedItems });
  const html = makeDashboardHtml({ boardItems: [] });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Trigger refresh
  await page.click('[data-test-id="refresh-board-btn"]');

  // All items should be visible
  for (const item of refreshedItems) {
    await waitForBoardItem(page, item.number);
  }

  // Count matches
  const count = await getBoardItemCount(page);
  expect(count).toBe(refreshedItems.length);
});

// ---------------------------------------------------------------------------
// Scenario 2: Group Issues by Epic
// ---------------------------------------------------------------------------

test("Epic grouping: epic header is rendered when board includes an epic item", async ({
  page,
}) => {
  const subIssues = [
    { number: 301, title: "Sub-issue 1", state: "open" },
    { number: 302, title: "Sub-issue 2", state: "open" },
  ];

  const epic = makeMockEpic(300, subIssues);
  const sub1 = makeMockIssue({
    number: 301,
    title: "Sub-issue 1",
    status: "Ready",
    parentIssueNumber: 300,
    parentIssueTitle: "Epic #300",
  });
  const sub2 = makeMockIssue({
    number: 302,
    title: "Sub-issue 2",
    status: "Ready",
    parentIssueNumber: 300,
    parentIssueTitle: "Epic #300",
  });

  const mock = createIpcMock({ boardItems: [epic, sub1, sub2] });
  const html = makeDashboardHtml({ boardItems: [] });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Trigger refresh to get epic items rendered
  await page.click('[data-test-id="refresh-board-btn"]');

  // Epic header should be present
  await page.waitForSelector('[data-test-id="epic-header-300"]', { timeout: 10_000 });

  // Sub-issues should appear in the board
  await waitForBoardItem(page, 301);
  await waitForBoardItem(page, 302);

  // Verify epic header text
  const epicHeaderText = await page.textContent('[data-test-id="epic-header-300"]');
  expect(epicHeaderText).toContain("Epic #300");
});

test("Epic grouping: sub-issues are rendered without duplication", async ({ page }) => {
  const subIssues = [{ number: 401, title: "Deduplicated sub-issue", state: "open" }];
  const epic = makeMockEpic(400, subIssues);
  const sub = makeMockIssue({
    number: 401,
    title: "Deduplicated sub-issue",
    status: "Ready",
    parentIssueNumber: 400,
  });

  const mock = createIpcMock({ boardItems: [epic, sub] });
  const html = makeDashboardHtml({ boardItems: [] });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  await page.click('[data-test-id="refresh-board-btn"]');
  await waitForBoardItem(page, 401);

  // Issue 401 should appear exactly once
  const items = await page.locator('[data-test-id="board-item-401"]').all();
  expect(items.length).toBe(1);
});

// ---------------------------------------------------------------------------
// Scenario 3: Handle Blocking Relationships
// ---------------------------------------------------------------------------

test("Blocking relationships: blocked issue displays lock icon", async ({ page }) => {
  const blockerIssue = makeMockIssue({ number: 501, status: "Ready" });
  const blockedIssue = makeMockIssue({
    number: 500,
    status: "Ready",
    blockedBy: [{ number: 501, title: blockerIssue.title, state: "open" }],
  });
  blockerIssue.blocking = [{ number: 500, title: blockedIssue.title, state: "open" }];

  const mock = createIpcMock({ boardItems: [blockedIssue, blockerIssue] });
  const html = makeDashboardHtml({ boardItems: [] });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  await page.click('[data-test-id="refresh-board-btn"]');
  await waitForBoardItem(page, 500);

  // Blocked issue should have a lock icon
  const lockedItem = page.locator('[data-test-id="board-item-500"]');
  const lockIcon = lockedItem.locator('[data-test-id="lock-icon"]');
  await expect(lockIcon).toBeVisible();

  // Non-blocked blocker should NOT have a lock icon
  const blockerItem = page.locator('[data-test-id="board-item-501"]');
  const blockerLock = blockerItem.locator('[data-test-id="lock-icon"]');
  await expect(blockerLock).not.toBeVisible();
});

test("Blocking relationships: non-blocked issues do not show lock icon", async ({ page }) => {
  const normalIssues = makeMockBoard({ readyCount: 3, inProgressCount: 0 });

  const mock = createIpcMock({ boardItems: normalIssues });
  const html = makeDashboardHtml({ boardItems: [] });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  await page.click('[data-test-id="refresh-board-btn"]');
  await waitForBoardItem(page, normalIssues[0].number);

  // No lock icons should be present
  const lockIcons = await page.locator('[data-test-id="lock-icon"]').all();
  expect(lockIcons.length).toBe(0);
});
