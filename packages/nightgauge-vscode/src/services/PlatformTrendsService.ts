/**
 * PlatformTrendsService — Fetches and caches platform analytics trends data.
 *
 * Calls platform.getAnalyticsTrends via Go IPC on demand (lazy-load on tab
 * activation). Per-period cache with single-inflight guard prevents duplicate
 * concurrent requests. Falls back to cached or null on error.
 *
 * @see Issue #3320 - Add Trends Tab to Pipeline Dashboard
 * @see PlatformRunsService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { AnalyticsTrendsResult } from "./IpcClientBase";
import type { TrendsDateRange } from "../views/dashboard/DashboardState";

export class PlatformTrendsService implements vscode.Disposable {
  private cache: Map<string, AnalyticsTrendsResult> = new Map();
  private inFlight: Set<string> = new Set();

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch trends via IPC for the given period, cache, and return.
   * Single-inflight guard per period — concurrent calls return the current cache.
   * On error: returns stale cache for the period or null.
   */
  async fetchAndCache(period: TrendsDateRange): Promise<AnalyticsTrendsResult | null> {
    if (this.inFlight.has(period)) {
      return this.cache.get(period) ?? null;
    }
    this.inFlight.add(period);
    try {
      const result = await this.ipcClient.platformGetAnalyticsTrends(period);
      this.cache.set(period, result);
      return result;
    } catch {
      return this.cache.get(period) ?? null;
    } finally {
      this.inFlight.delete(period);
    }
  }

  /** Returns the last cached value for the given period (synchronous, for render use). */
  getCached(period: TrendsDateRange): AnalyticsTrendsResult | null {
    return this.cache.get(period) ?? null;
  }

  dispose(): void {
    this.cache.clear();
    this.inFlight.clear();
  }
}
