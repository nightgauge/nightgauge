/**
 * PlatformRunsService — Fetches and caches platform analytics runs data.
 *
 * Calls platform.getAnalyticsRuns via Go IPC on demand (lazy-load on tab
 * activation). Single-inflight guard prevents duplicate concurrent requests.
 * Falls back to an empty result on error.
 *
 * @see Issue #3319 - Add Runs Tab to Pipeline Dashboard
 * @see PlatformAnalyticsHealthService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { AnalyticsRunsResult } from "./IpcClientBase";
import type { RunsFilterState } from "../views/dashboard/DashboardState";

export class PlatformRunsService implements vscode.Disposable {
  private cache: AnalyticsRunsResult | null = null;
  private inFlight = false;

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch analytics runs via IPC with optional filters and cursor, cache, and return.
   * Single-inflight guard — concurrent calls return the current cache.
   * On error: returns stale cache or null.
   */
  async fetchAndCache(
    filters: RunsFilterState,
    cursor?: string,
    limit?: number
  ): Promise<AnalyticsRunsResult | null> {
    if (this.inFlight) {
      return this.cache;
    }
    this.inFlight = true;
    try {
      const result = await this.ipcClient.platformGetAnalyticsRuns(
        filters.dateFrom || undefined,
        filters.dateTo || undefined,
        cursor,
        filters.outcomeFilter || undefined,
        filters.branchFilter || undefined,
        limit
      );
      this.cache = result;
      return result;
    } catch {
      return this.cache;
    } finally {
      this.inFlight = false;
    }
  }

  /** Returns the last cached value (synchronous, for render use). */
  getCached(): AnalyticsRunsResult | null {
    return this.cache;
  }

  dispose(): void {
    this.cache = null;
  }
}
