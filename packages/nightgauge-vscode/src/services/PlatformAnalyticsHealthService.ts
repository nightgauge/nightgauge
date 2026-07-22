/**
 * PlatformAnalyticsHealthService — Fetches and caches platform analytics health data.
 *
 * Calls platform.getAnalyticsHealth via Go IPC on demand (lazy-load on tab
 * activation). Single-inflight guard prevents duplicate concurrent requests.
 * Falls back to an empty result on error.
 *
 * @see Issue #3318 - Add Health Tab to Pipeline Dashboard
 * @see PlatformCostService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { AnalyticsHealthResult } from "./IpcClientBase";

export class PlatformAnalyticsHealthService implements vscode.Disposable {
  private cache: AnalyticsHealthResult | null = null;
  private inFlight = false;

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch analytics health via IPC, cache, and return.
   * Single-inflight guard — concurrent calls return the current cache.
   * On error: returns stale cache or null.
   */
  async fetchAndCache(): Promise<AnalyticsHealthResult | null> {
    if (this.inFlight) {
      return this.cache;
    }
    this.inFlight = true;
    try {
      const result = await this.ipcClient.platformGetAnalyticsHealth();
      this.cache = result;
      return result;
    } catch {
      return this.cache;
    } finally {
      this.inFlight = false;
    }
  }

  /** Returns the last cached value (synchronous, for render use). */
  getCached(): AnalyticsHealthResult | null {
    return this.cache;
  }

  dispose(): void {
    this.cache = null;
  }
}
