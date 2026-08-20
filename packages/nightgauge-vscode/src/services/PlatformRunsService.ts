/**
 * PlatformRunsService — Fetches and caches platform analytics runs data.
 *
 * Calls platform.getAnalyticsRuns via Go IPC on demand (lazy-load on tab
 * activation). Single-inflight guard prevents duplicate concurrent requests.
 *
 * @see Issue #3319 - Add Runs Tab to Pipeline Dashboard
 * @see Issue 743 - typed failures instead of a swallowed catch{}
 * @see PlatformAnalyticsHealthService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { AnalyticsRunsResult } from "./IpcClientBase";
import type { RunsFilterState } from "../views/dashboard/DashboardState";
import { Logger } from "../utils/logger";
import { platformOk, reportPlatformFailure, type PlatformResult } from "./platformResult";

const ENDPOINT = "platform.getAnalyticsRuns";

export class PlatformRunsService implements vscode.Disposable {
  private cache: AnalyticsRunsResult | null = null;
  private inFlight = false;
  private readonly logger = new Logger("Nightgauge Platform: Runs");

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch analytics runs via IPC with optional filters and cursor, cache, and return.
   * Single-inflight guard — a concurrent call returns the current cache
   * wrapped as a success (or a failure if there is nothing cached yet).
   * On error: logs once and returns a typed PlatformFailure. Never throws.
   */
  async fetchAndCache(
    filters: RunsFilterState,
    cursor?: string,
    limit?: number
  ): Promise<PlatformResult<AnalyticsRunsResult>> {
    if (this.inFlight) {
      return this.cache !== null
        ? platformOk(this.cache)
        : reportPlatformFailure(this.logger, new Error("fetch already in progress"), ENDPOINT);
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
      return platformOk(result);
    } catch (err) {
      return reportPlatformFailure(this.logger, err, ENDPOINT);
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
    this.logger.dispose();
  }
}
