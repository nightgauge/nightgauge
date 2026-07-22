/**
 * PlatformQuotaService — Fetches and caches platform-level quota data.
 *
 * Calls platform.getUsageSummary via Go IPC on demand (refresh-on-open
 * strategy — no continuous polling). Transforms the IPC UsageSummaryResult into
 * display-ready PlatformQuotaData and emits threshold notifications.
 *
 * @see Issue #1479 - Add usage metering and quota display
 * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { UsageSummaryResult } from "./IpcClientBase";
import type { NotificationService } from "./NotificationService";
import type { PlatformQuotaData } from "../views/dashboard/DashboardState";
import { getLimitsSettings } from "../config/limitsSettings";

type AlertLevel = "none" | "warning" | "critical";

/** Transform IPC UsageSummaryResult into display-ready PlatformQuotaData */
function toQuotaData(summary: UsageSummaryResult, fetchedAt: string): PlatformQuotaData {
  return {
    pipelineRuns: {
      used: summary.totalRuns,
      limit: null, // Limits are available via license info, not usage summary
      pct: null,
    },
    tokens: {
      used: summary.totalTokens,
      limit: null,
      pct: null,
    },
    period: null, // IPC returns period as string identifier, not start/end
    isCommunity: false, // Determined by license tier, not usage summary
    lastFetchedAt: fetchedAt,
    isStale: false,
  };
}

export class PlatformQuotaService implements vscode.Disposable {
  private cached: PlatformQuotaData | null = null;
  private lastAlertLevel: AlertLevel = "none";
  private fetchInProgress = false;

  constructor(
    private readonly ipcClient: IpcClientGenerated,
    private readonly notificationService: NotificationService
  ) {}

  /**
   * Fetch usage summary via IPC, transform, cache, and return.
   * On error: returns stale cached data with isStale=true.
   * On first-fetch failure with no cache: returns null.
   */
  async fetchAndCache(): Promise<PlatformQuotaData | null> {
    // Single in-flight request guard
    if (this.fetchInProgress) {
      return this.cached;
    }
    this.fetchInProgress = true;

    try {
      const fetchedAt = new Date().toISOString();
      const summary = await this.ipcClient.platformGetUsageSummary();
      const data = toQuotaData(summary, fetchedAt);
      this.cached = data;
      this.maybeNotify(data);
      return data;
    } catch {
      // IPC or network error — return stale cache if available
      if (this.cached !== null) {
        const stale: PlatformQuotaData = { ...this.cached, isStale: true };
        this.cached = stale;
        return stale;
      }
      return null;
    } finally {
      this.fetchInProgress = false;
    }
  }

  /** Returns the last cached value (synchronous, for render use). */
  getCached(): PlatformQuotaData | null {
    return this.cached;
  }

  /** Emit quota notifications based on pipeline run percentage. */
  private maybeNotify(data: PlatformQuotaData): void {
    const settings = getLimitsSettings();
    const pct = data.pipelineRuns.pct;
    if (pct === null) return; // unlimited — no alerts

    if (pct >= settings.quotaBlockThresholdPct) {
      this.notificationService.notifyQuotaWarning("block", pct, "pipeline runs");
    } else if (pct >= settings.quotaCriticalThresholdPct && this.lastAlertLevel !== "critical") {
      this.lastAlertLevel = "critical";
      this.notificationService.notifyQuotaWarning("critical", pct, "pipeline runs");
    } else if (pct >= settings.quotaWarningThresholdPct && this.lastAlertLevel === "none") {
      this.lastAlertLevel = "warning";
      this.notificationService.notifyQuotaWarning("warning", pct, "pipeline runs");
    }
  }

  dispose(): void {
    // No timers or subscriptions to clean up
  }
}
