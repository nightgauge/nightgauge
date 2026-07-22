/**
 * PlatformCostService — Fetches and caches platform cost analytics data.
 *
 * Calls platform.getCostAnalytics via Go IPC on demand (lazy-load on tab
 * activation strategy). Caches results per date range to avoid duplicate
 * in-flight requests.
 *
 * @see Issue #3317 - Add Cost Tab to Pipeline Dashboard
 * @see PlatformQuotaService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { CostAnalyticsResult } from "./IpcClientBase";

export type CostDateRange = "7d" | "30d" | "90d";

function dateRangeToParams(range: CostDateRange): { startDate: string; endDate: string } {
  const end = new Date();
  const start = new Date(end);
  const days = range === "7d" ? 7 : range === "30d" ? 30 : 90;
  start.setDate(start.getDate() - days);
  return {
    startDate: start.toISOString().slice(0, 10),
    endDate: end.toISOString().slice(0, 10),
  };
}

export class PlatformCostService implements vscode.Disposable {
  private cache = new Map<CostDateRange, CostAnalyticsResult>();
  private inFlight = new Set<CostDateRange>();

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch cost analytics via IPC for the given date range, cache, and return.
   * Single-inflight guard per range — concurrent calls return the current cache.
   * On error: returns null.
   */
  async fetchAndCache(range: CostDateRange): Promise<CostAnalyticsResult | null> {
    if (this.inFlight.has(range)) {
      return this.cache.get(range) ?? null;
    }
    this.inFlight.add(range);

    try {
      const { startDate, endDate } = dateRangeToParams(range);
      const result = await this.ipcClient.platformGetCostAnalytics(startDate, endDate);
      this.cache.set(range, result);
      return result;
    } catch {
      return this.cache.get(range) ?? null;
    } finally {
      this.inFlight.delete(range);
    }
  }

  /** Returns the last cached value for the given range (synchronous, for render use). */
  getCached(range: CostDateRange): CostAnalyticsResult | null {
    return this.cache.get(range) ?? null;
  }

  dispose(): void {
    this.cache.clear();
  }
}
