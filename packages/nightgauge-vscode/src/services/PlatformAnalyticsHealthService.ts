/**
 * PlatformAnalyticsHealthService — Fetches and caches platform analytics health data.
 *
 * Calls platform.getAnalyticsHealth via Go IPC on demand (lazy-load on tab
 * activation). Single-inflight guard prevents duplicate concurrent requests.
 *
 * @see Issue #3318 - Add Health Tab to Pipeline Dashboard
 * @see Issue 743 - typed failures instead of a swallowed catch{}
 * @see PlatformCostService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { AnalyticsHealthResult } from "./IpcClientBase";
import { Logger } from "../utils/logger";
import { platformOk, reportPlatformFailure, type PlatformResult } from "./platformResult";

const ENDPOINT = "platform.getAnalyticsHealth";

export class PlatformAnalyticsHealthService implements vscode.Disposable {
  private cache: AnalyticsHealthResult | null = null;
  private inFlight = false;
  private readonly logger = new Logger("Nightgauge Platform: Health");

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch analytics health via IPC, cache, and return.
   * Single-inflight guard — a concurrent call returns the current cache
   * wrapped as a success (or a failure if there is nothing cached yet).
   * On error: logs once and returns a typed PlatformFailure. Never throws.
   */
  async fetchAndCache(): Promise<PlatformResult<AnalyticsHealthResult>> {
    if (this.inFlight) {
      return this.cache !== null
        ? platformOk(this.cache)
        : reportPlatformFailure(this.logger, new Error("fetch already in progress"), ENDPOINT);
    }
    this.inFlight = true;
    try {
      const result = await this.ipcClient.platformGetAnalyticsHealth();
      this.cache = result;
      return platformOk(result);
    } catch (err) {
      return reportPlatformFailure(this.logger, err, ENDPOINT);
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
    this.logger.dispose();
  }
}
