/**
 * PlatformQuotaService — Fetches and caches platform-level quota data.
 *
 * Calls platform.getUsageSummary via Go IPC on demand (refresh-on-open
 * strategy — no continuous polling). Transforms the IPC UsageSummaryResult into
 * display-ready PlatformQuotaData and emits threshold notifications.
 *
 * @see Issue #1479 - Add usage metering and quota display
 * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
 * @see Issue 743 - typed failures instead of a swallowed catch{}
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type { UsageSummaryResult } from "./IpcClientBase";
import type { NotificationService } from "./NotificationService";
import type { PlatformQuotaData } from "../views/dashboard/DashboardState";
import { getLimitsSettings } from "../config/limitsSettings";
import { Logger } from "../utils/logger";
import { platformOk, reportPlatformFailure, type PlatformResult } from "./platformResult";

type AlertLevel = "none" | "warning" | "critical";

const ENDPOINT = "platform.getUsageSummary";

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
  private readonly logger = new Logger("Nightgauge Platform: Quota");

  constructor(
    private readonly ipcClient: IpcClientGenerated,
    private readonly notificationService: NotificationService
  ) {}

  /**
   * Fetch usage summary via IPC, transform, cache, and return.
   * On error with a cache already populated: logs the failure, marks the
   * cache stale, and returns it — the dashboard footer shows "Showing cached
   * data" rather than losing the numbers over a transient blip.
   * On error with no cache yet: logs the failure and returns a typed
   * PlatformFailure (never `null`) so the caller knows why. Never throws.
   */
  async fetchAndCache(): Promise<PlatformResult<PlatformQuotaData>> {
    // Single in-flight request guard
    if (this.fetchInProgress) {
      return this.cached !== null
        ? platformOk(this.cached)
        : reportPlatformFailure(this.logger, new Error("fetch already in progress"), ENDPOINT);
    }
    this.fetchInProgress = true;

    try {
      const fetchedAt = new Date().toISOString();
      const summary = await this.ipcClient.platformGetUsageSummary();
      const data = toQuotaData(summary, fetchedAt);
      this.cached = data;
      this.maybeNotify(data);
      return platformOk(data);
    } catch (err) {
      const failure = reportPlatformFailure(this.logger, err, ENDPOINT);
      // IPC or network error — degrade to stale cache if available, rather
      // than losing the last-known numbers over a transient failure. The
      // failure itself is still logged above with its real kind/status.
      if (this.cached !== null) {
        const stale: PlatformQuotaData = { ...this.cached, isStale: true };
        this.cached = stale;
        return platformOk(stale);
      }
      return failure;
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
    this.logger.dispose();
  }
}
