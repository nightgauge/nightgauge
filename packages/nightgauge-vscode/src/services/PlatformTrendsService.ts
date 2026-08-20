/**
 * PlatformTrendsService — Fetches and caches platform analytics trends data.
 *
 * Calls platform.getAnalyticsTrends via Go IPC on demand (lazy-load on tab
 * activation). Per-period cache with single-inflight guard prevents duplicate
 * concurrent requests.
 *
 * @see Issue #3320 - Add Trends Tab to Pipeline Dashboard
 * @see Issue 743 - typed failures instead of a swallowed catch{}
 * @see PlatformRunsService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { AnalyticsTrendsResult } from "./IpcClientBase";
import type { TrendsDateRange } from "../views/dashboard/DashboardState";
import { Logger } from "../utils/logger";
import { platformOk, reportPlatformFailure, type PlatformResult } from "./platformResult";

const ENDPOINT = "platform.getAnalyticsTrends";

export class PlatformTrendsService implements vscode.Disposable {
  private cache: Map<string, AnalyticsTrendsResult> = new Map();
  private inFlight: Set<string> = new Set();
  private readonly logger = new Logger("Nightgauge Platform: Trends");

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch trends via IPC for the given period, cache, and return.
   * Single-inflight guard per period — a concurrent call returns the current
   * cache for that period wrapped as a success (or a failure if there is
   * nothing cached yet).
   * On error: logs once and returns a typed PlatformFailure. Never throws.
   */
  async fetchAndCache(period: TrendsDateRange): Promise<PlatformResult<AnalyticsTrendsResult>> {
    if (this.inFlight.has(period)) {
      const cached = this.cache.get(period);
      return cached !== undefined
        ? platformOk(cached)
        : reportPlatformFailure(this.logger, new Error("fetch already in progress"), ENDPOINT);
    }
    this.inFlight.add(period);
    try {
      const result = await this.ipcClient.platformGetAnalyticsTrends(period);
      this.cache.set(period, result);
      return platformOk(result);
    } catch (err) {
      return reportPlatformFailure(this.logger, err, ENDPOINT);
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
    this.logger.dispose();
  }
}
