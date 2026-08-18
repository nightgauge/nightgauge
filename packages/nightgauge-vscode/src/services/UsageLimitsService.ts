/**
 * UsageLimitsService - Fires threshold notifications when the active
 * adapter's monthly spend approaches the configured budget.
 *
 * Historically this read `DashboardState.getAggregates("all")` — an
 * unbounded, all-time total compared against a *monthly* budget, which read
 * as permanently over-budget on any workspace older than a month (Issue
 * #683). It now reads the `monthly` window from `AdapterUsageService`
 * (Issue #658 / #659), whose boundary is the 1st of the local month —
 * see docs/decisions/018-adapter-usage-quota-model.md.
 *
 * Status-bar rendering of the usage meter itself is no longer this service's
 * job: `StatusBarManager.showUsageSnapshot()` renders directly off
 * `AdapterUsageService.onDidChangeUsage` (wired in bootstrap/services.ts),
 * so the meter and this service's alerts read the same window instead of two
 * independently-computed figures.
 *
 * @see Issue #1333 - Show Claude Code usage limits and alert users
 * @see Issue #683 - Status-bar meter compared all-time cost to a monthly budget
 * @see Issue #659 - Adapter usage meter in the status bar
 */

import * as vscode from "vscode";
import type { AdapterUsageService } from "./usage/AdapterUsageService";
import type { UsageSnapshot } from "./usage/types";
import type { NotificationService } from "./NotificationService";
import { getLimitsSettings } from "../config/limitsSettings";

/**
 * Alert level for deduplication guard
 */
type AlertLevel = "none" | "warning" | "critical";

/**
 * UsageLimitsService - Budget threshold alert service
 *
 * Reacts to `AdapterUsageService.onDidChangeUsage`, evaluates the snapshot's
 * `monthly` window against `monthlyBudgetUsd`, and fires warning/critical
 * notifications via `NotificationService`. A no-op when no budget is
 * configured.
 *
 * @example
 * ```typescript
 * const usageLimits = new UsageLimitsService(adapterUsageService, notificationService);
 * usageLimits.initialize();
 * context.subscriptions.push(usageLimits);
 * ```
 */
export class UsageLimitsService implements vscode.Disposable {
  private lastAlertLevel: AlertLevel = "none";
  private changeSubscription: vscode.Disposable | null = null;

  constructor(
    private readonly usageService: AdapterUsageService,
    private readonly notificationService: NotificationService
  ) {}

  /**
   * Start reacting to usage changes. Call once after construction.
   * No-op when monthlyBudgetUsd is 0 (disabled) — matches the pre-#683
   * behaviour of not wiring anything up when no budget is configured.
   */
  initialize(): void {
    if (getLimitsSettings().monthlyBudgetUsd <= 0) {
      return;
    }
    this.changeSubscription = this.usageService.onDidChangeUsage((snapshot) =>
      this.evaluate(snapshot)
    );
    const cached = this.usageService.getCachedSnapshot();
    if (cached) {
      this.evaluate(cached);
    }
  }

  /**
   * Evaluate one snapshot's `monthly` window against the configured budget,
   * firing a warning/critical notification on a threshold crossing.
   *
   * Bidirectional: when usage drops back under the warning threshold — a
   * fresh calendar month, or a `resetCounter()` call — `lastAlertLevel`
   * un-latches to `"none"`, so a later rise notifies again instead of
   * staying silently latched at whatever level last fired (#683 AC: the
   * 80%/90% thresholds must be reachable in both directions).
   */
  private evaluate(snapshot: UsageSnapshot): void {
    const settings = getLimitsSettings();
    if (settings.monthlyBudgetUsd <= 0) {
      return;
    }
    const monthly = snapshot.windows.find((w) => w.scope === "monthly");
    if (!monthly || monthly.limit === null || monthly.limit <= 0) {
      return; // No priced monthly window yet, or the adapter isn't metered.
    }

    const usagePct = (monthly.used / monthly.limit) * 100;

    if (usagePct >= settings.criticalThresholdPct && this.lastAlertLevel !== "critical") {
      this.lastAlertLevel = "critical";
      this.notificationService.notifyUsageWarning(
        "critical",
        usagePct,
        monthly.used,
        monthly.limit
      );
    } else if (usagePct >= settings.warningThresholdPct && this.lastAlertLevel === "none") {
      this.lastAlertLevel = "warning";
      this.notificationService.notifyUsageWarning("warning", usagePct, monthly.used, monthly.limit);
    } else if (usagePct < settings.warningThresholdPct && this.lastAlertLevel !== "none") {
      this.lastAlertLevel = "none";
    }
  }

  /**
   * Re-arm alerts without waiting for the calendar month boundary.
   *
   * Before #683 this recorded a manual dollar offset subtracted from an
   * all-time total — the only reset mechanism that existed. Now that the
   * `monthly` window itself resets automatically at the 1st, a second,
   * independently-tracked offset would only reintroduce the bug this ticket
   * fixes: the moment the calendar rolls over, a stale offset captured in the
   * old month would outlive it and either mask real spend in the new month or
   * (once `Math.max(0, …)` stopped clamping it) go negative. So there is
   * nothing left to subtract — this simply un-latches `lastAlertLevel`,
   * matching what a fresh month already does on its own, so the operator can
   * acknowledge and re-arm mid-month without waiting for it.
   *
   * @see Issue #683 AC — "manualCostOffsetUsd is either removed or its
   *      remaining purpose is documented"
   */
  resetCounter(): void {
    this.lastAlertLevel = "none";
  }

  /**
   * The active adapter's cost for the current calendar month — the same
   * figure the status-bar meter's "This month" window shows, and what backs
   * the dashboard's pre-existing budget-vs-spend widget
   * (`Dashboard.getUsageLimitsData()`). Returns 0 before the first snapshot
   * or when the monthly window isn't priced.
   */
  getEffectiveCostUsd(): number {
    const monthly = this.usageService
      .getCachedSnapshot()
      ?.windows.find((w) => w.scope === "monthly");
    return monthly?.used ?? 0;
  }

  dispose(): void {
    this.changeSubscription?.dispose();
    this.changeSubscription = null;
  }
}
