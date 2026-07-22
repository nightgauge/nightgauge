/**
 * UsageLimitsService - Tracks cumulative pipeline cost against a monthly budget
 *
 * Reads accumulated cost from DashboardState, polls on interval,
 * fires threshold-based notifications, and updates the status bar.
 *
 * No upstream API is available for Claude Code Max quota — tracking is
 * local and budget-based via user-configured monthly budget.
 *
 * @see Issue #1333 - Show Claude Code usage limits and alert users
 */

import * as vscode from "vscode";
import type { DashboardState } from "../views/dashboard/DashboardState";
import type { NotificationService } from "./NotificationService";
import type { StatusBarManager } from "../utils/statusBar";
import { getLimitsSettings } from "../config/limitsSettings";

/**
 * Alert level for deduplication guard
 */
type AlertLevel = "none" | "warning" | "critical";

/**
 * UsageLimitsService - Budget tracking and threshold alert service
 *
 * Polls DashboardState.getAggregates() on a configurable interval,
 * shows live cost in the status bar when a budget is configured,
 * and fires warning/critical notifications via NotificationService.
 *
 * @example
 * ```typescript
 * const usageLimits = new UsageLimitsService(dashboardState, notificationService, statusBar);
 * usageLimits.initialize();
 * context.subscriptions.push(usageLimits);
 * ```
 */
export class UsageLimitsService implements vscode.Disposable {
  private pollingTimer: ReturnType<typeof setInterval> | null = null;
  private lastAlertLevel: AlertLevel = "none";
  /** Subtracted from totalCostUsd when user resets the counter */
  private manualCostOffsetUsd = 0;

  constructor(
    private readonly dashboardState: DashboardState,
    private readonly notificationService: NotificationService,
    private readonly statusBar: StatusBarManager
  ) {}

  /**
   * Start usage polling. Call once after construction.
   * No-op when monthlyBudgetUsd is 0 (disabled).
   */
  initialize(): void {
    this.startPolling();
  }

  private startPolling(): void {
    const settings = getLimitsSettings();
    if (settings.monthlyBudgetUsd <= 0) {
      return; // Disabled — no budget configured
    }

    // Run immediately then on interval
    this.poll();
    this.pollingTimer = setInterval(() => this.poll(), settings.pollingIntervalSeconds * 1000);
  }

  private poll(): void {
    const settings = getLimitsSettings();
    if (settings.monthlyBudgetUsd <= 0) {
      return;
    }

    const aggregates = this.dashboardState.getAggregates("all");
    const effectiveCost = Math.max(0, (aggregates.totalCostUsd ?? 0) - this.manualCostOffsetUsd);
    const usagePct = (effectiveCost / settings.monthlyBudgetUsd) * 100;

    // Update status bar
    this.statusBar.showUsage(effectiveCost, settings.monthlyBudgetUsd);

    // Critical threshold check (higher priority, checked first)
    if (usagePct >= settings.criticalThresholdPct && this.lastAlertLevel !== "critical") {
      this.lastAlertLevel = "critical";
      this.notificationService.notifyUsageWarning(
        "critical",
        usagePct,
        effectiveCost,
        settings.monthlyBudgetUsd
      );
    } else if (usagePct >= settings.warningThresholdPct && this.lastAlertLevel === "none") {
      // Warning threshold — only fire if not already warned or critical
      this.lastAlertLevel = "warning";
      this.notificationService.notifyUsageWarning(
        "warning",
        usagePct,
        effectiveCost,
        settings.monthlyBudgetUsd
      );
    }
  }

  /**
   * Reset the usage counter by recording the current total as the new baseline.
   *
   * This does not clear DashboardState — it records an offset so future
   * reads show usage since this reset point. The alert level is also reset
   * so warnings can fire again.
   */
  resetCounter(): void {
    const aggregates = this.dashboardState.getAggregates("all");
    this.manualCostOffsetUsd = aggregates.totalCostUsd ?? 0;
    this.lastAlertLevel = "none";
    this.statusBar.hideUsage();
  }

  /**
   * Get the current effective cost (after applying manual offset)
   */
  getEffectiveCostUsd(): number {
    const aggregates = this.dashboardState.getAggregates("all");
    return Math.max(0, (aggregates.totalCostUsd ?? 0) - this.manualCostOffsetUsd);
  }

  dispose(): void {
    if (this.pollingTimer !== null) {
      clearInterval(this.pollingTimer);
      this.pollingTimer = null;
    }
    this.statusBar.hideUsage();
  }
}
