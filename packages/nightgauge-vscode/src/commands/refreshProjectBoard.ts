/**
 * Refresh Project Board commands
 *
 * Registers a single unified refresh command that clears the per-status cache,
 * pre-fetches each status using server-side filtering, then fires
 * onDidChangeTreeData for all tabs.
 *
 * Per-tab aliases are kept for backward compatibility (inline command links,
 * welcome views) but all route to the same global refresh.
 */

import * as vscode from "vscode";
import type { ProjectBoardTreeProvider } from "../views/ProjectBoardTreeProvider";
import type { Logger } from "../utils/logger";
import type { TabId } from "../types/TabConfig";
import { IpcClientBase } from "../services/IpcClient";

/**
 * Map of tab IDs to their providers for refresh commands
 */
export type ProjectBoardProviders = Map<TabId, ProjectBoardTreeProvider>;

/**
 * Refresh all project board views using server-side status filtering.
 *
 * Strategy: Show loading → clear per-status cache → pre-fetch each status
 * in parallel (using ProjectV2.items(query: "status:X is:open")) → clear
 * loading → update title counts.
 *
 * Each status query returns only matching items (e.g., 15 Ready items instead
 * of 677 total), eliminating the fetch-all-and-filter-locally pattern and
 * the progressive-rendering race condition that caused empty views.
 */
async function refreshAllBoards(providers: ProjectBoardProviders, logger: Logger): Promise<void> {
  logger.debug("Refreshing all project board views (server-filtered)");
  IpcClientBase.activeCallSource = "user-refresh";

  const firstProvider = providers.values().next().value;
  if (!firstProvider) {
    logger.warn("refreshAllBoards: no providers found");
    return;
  }

  // Step 1: Show loading spinners on all tabs immediately
  for (const provider of providers.values()) {
    provider.setLoading(true);
  }

  // Step 2: Invalidate cache timestamps so fresh data is fetched, but keep
  // stale issue lists in memory as a fallback. If the GitHub API is
  // rate-limited during Step 3, fetchIssuesForStatus returns the stale data
  // instead of an empty array — the tree views continue showing the last known
  // state rather than going blank.
  const service = firstProvider.getProjectBoardService();
  service.softInvalidate();

  // Step 3: Pre-fetch each active status in parallel.
  // Each status makes a small server-filtered API call (~15-46 items)
  // instead of one large unfiltered call (537 items, 11s+).
  // Epic grouping resolves titles from per-status caches — no getAllItems() needed.
  const statuses = Array.from(providers.values()).map((p) => p.getStatus());
  try {
    await Promise.all(statuses.map((status) => service.getIssuesByStatus(status)));
    logger.debug("Project board refresh complete", {
      tabCount: providers.size,
      statuses,
    });
  } catch (error) {
    // Log but don't block — tabs will try to fetch individually on render
    logger.warn("Project board refresh failed, tabs will fetch on demand", {
      error: error instanceof Error ? error.message : String(error),
    });
  }

  // Step 4: Clear loading state — getChildren() reads from warm per-status cache
  for (const provider of providers.values()) {
    provider.setLoading(false);
  }

  // Step 5: Eagerly update title counts for ALL providers (including collapsed
  // views whose getChildren() won't run). Reads from warm per-status cache.
  const titleUpdates = Array.from(providers.values()).map((p) => p.refreshTitleCount());
  await Promise.all(titleUpdates);
}

/**
 * Register all Project Board refresh commands.
 *
 * Registers:
 * - `nightgauge.refreshProjectBoard` — canonical global refresh
 * - `nightgauge.refreshProjectBoard.<tabId>` — per-tab aliases that
 *   delegate to the global refresh (kept for backward compat with inline
 *   command links and welcome views)
 */
export function registerRefreshProjectBoardCommands(
  providers: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable[] {
  const disposables: vscode.Disposable[] = [];

  // Canonical global refresh command
  disposables.push(
    vscode.commands.registerCommand("nightgauge.refreshProjectBoard", async () =>
      refreshAllBoards(providers, logger)
    )
  );

  // Per-tab aliases — all delegate to the global refresh so any refresh
  // button on any tab refreshes every view from one API call.
  for (const tabId of providers.keys()) {
    disposables.push(
      vscode.commands.registerCommand(`nightgauge.refreshProjectBoard.${tabId}`, async () =>
        refreshAllBoards(providers, logger)
      )
    );
  }

  return disposables;
}
