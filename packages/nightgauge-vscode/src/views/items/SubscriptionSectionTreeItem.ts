/**
 * SubscriptionSectionTreeItem — Sidebar subscription status section.
 *
 * Displays the user's current subscription plan, billing status, and renewal
 * date in the pipeline sidebar. Data is pushed from PipelineTreeProvider after
 * LicensePreflight.validate() completes — this item never calls validate()
 * directly (async side-effects in tree items cause hydration ordering issues).
 *
 * States:
 * - No data / signed out: shows "Sign in to view"
 * - Community tier: "Free Plan" + "Upgrade to Pro" action
 * - Paid tier, active: plan name + renewal date + "Manage Subscription" action
 * - Expired: warning + "Update Payment" action
 * - Revoked / suspended: error + "Contact Support" action
 * - Offline: appends "Offline — showing cached data" child
 *
 * @see Issue #1477 - Add subscription status display to dashboard sidebar
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { LicenseStatus } from "../../platform/types";
import type { Tier } from "../../platform/types";

/** Tier display labels mapping internal values to user-facing strings. */
const TIER_LABELS: Record<string, string> = {
  community: "Free Plan",
  pro: "Pro",
  team: "Team",
  enterprise: "Enterprise",
};

/** Subscription state pushed to SubscriptionSectionTreeItem.update(). */
export interface SubscriptionDisplayData {
  tier: Tier;
  /** 'community' = free plan (no paid license). */
  status: LicenseStatus | "community";
  /** License expiry date (ISO 8601), null for community tier (no expiry). */
  expiresAt: string | null;
  /** True when preflight fell back due to network failure. */
  offline: boolean;
  /** When the data was last fetched successfully. */
  lastUpdated: Date;
  /** True when this machine is bound to the license (#4156). */
  machineBound: boolean;
  /** Number of machines currently bound to the license (#4156). */
  machineCount: number;
}

/** Concrete minimal tree item for subscription child nodes. */
class SubscriptionChildItem extends BaseTreeItem {
  constructor(
    label: string,
    options: {
      icon?: string;
      iconColor?: string;
      command?: vscode.Command;
      description?: string;
      contextValue?: string;
      tooltip?: string;
    } = {}
  ) {
    super(label, vscode.TreeItemCollapsibleState.None);
    if (options.icon) {
      if (options.iconColor) {
        this.iconPath = new vscode.ThemeIcon(
          options.icon,
          new vscode.ThemeColor(options.iconColor)
        );
      } else {
        this.iconPath = new vscode.ThemeIcon(options.icon);
      }
    }
    if (options.command) this.command = options.command;
    if (options.description) this.description = options.description;
    if (options.contextValue) this.contextValue = options.contextValue;
    if (options.tooltip) this.tooltip = options.tooltip;
  }
}

/**
 * SubscriptionSectionTreeItem — root subscription node shown in pipeline sidebar.
 *
 * When no data: shows "Sign in to view" child.
 * When community: shows "Free Plan" + "Upgrade to Pro" action.
 * When paid + active: shows plan name, renewal date, machine-binding count
 *   (#4156), and "Manage Subscription" action.
 * When expired: shows warning icon, "Update Payment" action.
 * When revoked/suspended: shows error icon, "Contact Support" action.
 * When offline: appends "Offline — showing cached data" child in any state.
 *
 * @example
 * ```typescript
 * const subscriptionSection = new SubscriptionSectionTreeItem();
 * subscriptionSection.update({
 *   tier: 'pro',
 *   status: 'active',
 *   expiresAt: '2026-06-01T00:00:00Z',
 *   offline: false,
 *   lastUpdated: new Date(),
 *   machineBound: true,
 *   machineCount: 1,
 * });
 * ```
 */
export class SubscriptionSectionTreeItem extends BaseTreeItem {
  private data: SubscriptionDisplayData | null = null;

  constructor() {
    super("Subscription", vscode.TreeItemCollapsibleState.Collapsed);
    this.contextValue = "subscription-section";
    this.render();
  }

  /**
   * Update the subscription section with the latest preflight data.
   * Call this whenever LicensePreflight.validate() returns a new result.
   * Pass null to reset to the "no data / signed out" state.
   */
  update(data: SubscriptionDisplayData | null): void {
    this.data = data;
    this.render();
  }

  override getChildren(): BaseTreeItem[] {
    return this.buildChildren();
  }

  // ---------------------------------------------------------------------------
  // Private
  // ---------------------------------------------------------------------------

  private render(): void {
    if (!this.data) {
      // No data — unauthenticated or waiting
      this.label = "Subscription";
      this.description = undefined;
      this.iconPath = new vscode.ThemeIcon("person", new vscode.ThemeColor("disabledForeground"));
      this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
      return;
    }

    const { tier, status } = this.data;

    // Check terminal states first — status takes precedence over tier so that
    // licenses that were downgraded to community tier after expiry/revocation
    // still render with the correct billing state.
    if (status === "expired") {
      this.label = "Subscription";
      this.description = "License Expired";
      this.iconPath = new vscode.ThemeIcon("warning", new vscode.ThemeColor("terminal.ansiYellow"));
      this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
      return;
    }

    if (status === "revoked" || status === "suspended") {
      this.label = "Subscription";
      this.description = "Canceled";
      this.iconPath = new vscode.ThemeIcon("error", new vscode.ThemeColor("errorForeground"));
      this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
      return;
    }

    if (status === "community" || tier === "community") {
      this.label = "Subscription";
      this.description = "Free Plan";
      this.iconPath = new vscode.ThemeIcon(
        "star-empty",
        new vscode.ThemeColor("disabledForeground")
      );
      this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
      return;
    }

    // Paid tier, active
    this.label = "Subscription";
    this.description = TIER_LABELS[tier] ?? tier;
    this.iconPath = new vscode.ThemeIcon("verified", new vscode.ThemeColor("testing.iconPassed"));
    this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
  }

  private buildChildren(): BaseTreeItem[] {
    if (!this.data) {
      return [
        new SubscriptionChildItem("Sign in to view subscription", {
          icon: "person",
          iconColor: "disabledForeground",
          contextValue: "subscription-signed-out",
        }),
      ];
    }

    const { tier, status, expiresAt, offline, lastUpdated, machineCount } = this.data;
    const children: BaseTreeItem[] = [];

    // Check terminal states before community (same priority as render())
    if (status === "expired") {
      children.push(
        new SubscriptionChildItem("License Expired", {
          icon: "warning",
          iconColor: "terminal.ansiYellow",
          contextValue: "subscription-plan",
          tooltip: "Your license has expired. Renew to restore paid features.",
        })
      );
      children.push(
        new SubscriptionChildItem("Update Payment", {
          icon: "credit-card",
          iconColor: "terminal.ansiYellow",
          command: {
            command: "nightgauge.openSubscriptionUrl",
            title: "Update Payment",
            arguments: ["https://nightgauge.dev/account/renew"],
          },
          tooltip: "Update your payment to renew your license",
          contextValue: "subscription-update-payment",
        })
      );
    } else if (status === "revoked" || status === "suspended") {
      children.push(
        new SubscriptionChildItem("Subscription Canceled", {
          icon: "error",
          iconColor: "errorForeground",
          contextValue: "subscription-plan",
          tooltip: "Your subscription has been canceled or revoked.",
        })
      );
      children.push(
        new SubscriptionChildItem("Contact Support", {
          icon: "comment-discussion",
          command: {
            command: "nightgauge.openSubscriptionUrl",
            title: "Contact Support",
            arguments: ["https://github.com/nightgauge/nightgauge/issues"],
          },
          tooltip: "Contact support for assistance",
          contextValue: "subscription-contact-support",
        })
      );
    } else if (status === "community" || tier === "community") {
      // Free tier
      children.push(
        new SubscriptionChildItem("Free Plan", {
          icon: "star-empty",
          iconColor: "disabledForeground",
          description: "Community tier",
          contextValue: "subscription-plan",
        })
      );
      children.push(
        new SubscriptionChildItem("Upgrade to Pro", {
          icon: "rocket",
          iconColor: "terminal.ansiCyan",
          command: {
            command: "nightgauge.openUpgradeUrl",
            title: "Upgrade to Pro",
          },
          tooltip: "Open the Nightgauge upgrade page",
          contextValue: "subscription-upgrade",
        })
      );
    } else if (status === "active") {
      // Paid tier, active
      const tierLabel = TIER_LABELS[tier] ?? tier;
      children.push(
        new SubscriptionChildItem(tierLabel, {
          icon: "verified",
          iconColor: "testing.iconPassed",
          description: "Active subscription",
          contextValue: "subscription-plan",
        })
      );

      if (expiresAt) {
        const renewalLabel = formatRelative(expiresAt);
        children.push(
          new SubscriptionChildItem(renewalLabel, {
            icon: "calendar",
            contextValue: "subscription-renewal",
            tooltip: new Date(expiresAt).toLocaleDateString(undefined, {
              year: "numeric",
              month: "long",
              day: "numeric",
            }),
          })
        );
      }

      // Machine binding (#4156): shows bound-machine count for this license.
      // No limit/list/unbind API exists on the platform yet (see
      // commands/showMachineBinding.ts) — this is deliberately read-only.
      children.push(
        new SubscriptionChildItem(
          machineCount === 1 ? "1 machine bound" : `${machineCount} machines bound`,
          {
            icon: "vm",
            command: {
              command: "nightgauge.showMachineBinding",
              title: "Machine Binding",
            },
            tooltip: "View machine binding details for this license",
            contextValue: "subscription-machine-binding",
          }
        )
      );

      children.push(
        new SubscriptionChildItem("Manage Subscription", {
          icon: "gear",
          command: {
            command: "nightgauge.openManageSubscription",
            title: "Manage Subscription",
          },
          tooltip: "Open subscription management page",
          contextValue: "subscription-manage",
        })
      );
    }

    if (offline) {
      children.push(
        new SubscriptionChildItem("Offline — showing cached data", {
          icon: "plug",
          iconColor: "terminal.ansiYellow",
          description: `Last updated: ${formatTimeAgo(lastUpdated)}`,
          contextValue: "subscription-offline",
        })
      );
    }

    return children;
  }
}

// ---------------------------------------------------------------------------
// Helpers (exported for testing)
// ---------------------------------------------------------------------------

/**
 * Format a license expiry date relative to now.
 * - null → "No expiry" (community tier)
 * - Future ≤ 30 days → "Renews in N days"
 * - Future > 30 days → "Renews on {date}"
 * - Past → "Expired N days ago"
 */
export function formatRelative(isoDate: string | null): string {
  if (!isoDate) return "No expiry";

  const now = Date.now();
  const target = new Date(isoDate).getTime();
  const diffMs = target - now;
  const diffDays = Math.round(diffMs / (1000 * 60 * 60 * 24));

  if (diffDays < 0) {
    const absDays = Math.abs(diffDays);
    return `Expired ${absDays} ${absDays === 1 ? "day" : "days"} ago`;
  }

  if (diffDays === 0) return "Expires today";

  if (diffDays <= 30) {
    return `Renews in ${diffDays} ${diffDays === 1 ? "day" : "days"}`;
  }

  return `Renews on ${new Date(isoDate).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  })}`;
}

/**
 * Format a date as a relative "time ago" string for the offline indicator.
 */
export function formatTimeAgo(date: Date): string {
  const diffMs = Date.now() - date.getTime();
  const diffMinutes = Math.floor(diffMs / (1000 * 60));

  if (diffMinutes < 1) return "just now";
  if (diffMinutes === 1) return "1 minute ago";
  if (diffMinutes < 60) return `${diffMinutes} minutes ago`;

  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours === 1) return "1 hour ago";
  if (diffHours < 24) return `${diffHours} hours ago`;

  const diffDays = Math.floor(diffHours / 24);
  return `${diffDays} ${diffDays === 1 ? "day" : "days"} ago`;
}
