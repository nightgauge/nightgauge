/**
 * PlatformCostService — Fetches and caches platform cost analytics data.
 *
 * Calls platform.getCostAnalytics via Go IPC on demand (lazy-load on tab
 * activation strategy). Caches results per date range to avoid duplicate
 * in-flight requests.
 *
 * @see Issue #3317 - Add Cost Tab to Pipeline Dashboard
 * @see Issue 743 - typed failures instead of a swallowed catch{}
 * @see PlatformQuotaService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { CostAnalyticsResult } from "./IpcClientBase";
import { Logger } from "../utils/logger";
import { platformOk, reportPlatformFailure, type PlatformResult } from "./platformResult";

export type CostDateRange = "7d" | "30d" | "90d";

const ENDPOINT = "platform.getCostAnalytics";

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
  private readonly logger = new Logger("Nightgauge Platform: Cost");

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch cost analytics via IPC for the given date range, cache, and return.
   * Single-inflight guard per range — a concurrent call returns the current
   * cache for that range wrapped as a success (or a failure if there is
   * nothing cached yet).
   * On error: logs once and returns a typed PlatformFailure. Never throws.
   */
  async fetchAndCache(range: CostDateRange): Promise<PlatformResult<CostAnalyticsResult>> {
    if (this.inFlight.has(range)) {
      const cached = this.cache.get(range);
      return cached !== undefined
        ? platformOk(cached)
        : reportPlatformFailure(this.logger, new Error("fetch already in progress"), ENDPOINT);
    }
    this.inFlight.add(range);

    try {
      const { startDate, endDate } = dateRangeToParams(range);
      const result = await this.ipcClient.platformGetCostAnalytics(startDate, endDate);
      this.cache.set(range, result);
      return platformOk(result);
    } catch (err) {
      return reportPlatformFailure(this.logger, err, ENDPOINT);
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
    this.logger.dispose();
  }
}
