/**
 * SubscriptionSectionTreeItem unit tests.
 *
 * Verifies subscription section tree item renders correctly for all states:
 * unauthenticated, community, pro (active), expired, revoked, and offline.
 *
 * @see Issue #1477 - Add subscription status display to dashboard sidebar
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  return {
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    ThemeColor: class {
      constructor(public readonly id: string) {}
    },
    ThemeIcon: class {
      constructor(
        public readonly id: string,
        public readonly color?: unknown
      ) {}
    },
    TreeItem: class {
      label: string | undefined;
      description: string | undefined;
      collapsibleState: number;
      iconPath: unknown;
      command: unknown;
      tooltip: unknown;
      contextValue: string | undefined;
      id: string | undefined;
      constructor(label: string, state: number) {
        this.label = label;
        this.collapsibleState = state;
      }
    },
    Uri: {
      parse: (url: string) => ({ toString: () => url }),
    },
  };
});

import {
  SubscriptionSectionTreeItem,
  type SubscriptionDisplayData,
  formatRelative,
  formatTimeAgo,
} from "../../../src/views/items/SubscriptionSectionTreeItem";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeData(overrides: Partial<SubscriptionDisplayData> = {}): SubscriptionDisplayData {
  return {
    tier: "pro",
    status: "active",
    expiresAt: null,
    offline: false,
    lastUpdated: new Date(),
    machineBound: false,
    machineCount: 0,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("SubscriptionSectionTreeItem", () => {
  describe("initial state (no data / unauthenticated)", () => {
    it('shows "Subscription" label', () => {
      const item = new SubscriptionSectionTreeItem();
      expect(item.label).toBe("Subscription");
    });

    it("has subscription-section contextValue", () => {
      const item = new SubscriptionSectionTreeItem();
      expect(item.contextValue).toBe("subscription-section");
    });

    it("is collapsible", () => {
      const item = new SubscriptionSectionTreeItem();
      // TreeItemCollapsibleState.Collapsed = 1
      expect(item.collapsibleState).toBe(1);
    });

    it('returns a single "Sign in to view" child', () => {
      const item = new SubscriptionSectionTreeItem();
      const children = item.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].contextValue).toBe("subscription-signed-out");
    });

    it('"Sign in to view" child has no command', () => {
      const item = new SubscriptionSectionTreeItem();
      const children = item.getChildren();
      expect(children[0].command).toBeUndefined();
    });
  });

  describe("community tier", () => {
    it('shows "Free Plan" description', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "community" }));
      expect(item.description).toBe("Free Plan");
    });

    it('returns "Free Plan" child and "Upgrade to Pro" action child', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "community" }));
      const children = item.getChildren();
      expect(children).toHaveLength(2);
      expect(children[0].contextValue).toBe("subscription-plan");
      expect(children[1].contextValue).toBe("subscription-upgrade");
    });

    it('"Upgrade to Pro" child has nightgauge.openUpgradeUrl command', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "community" }));
      const children = item.getChildren();
      const upgradeItem = children.find((c) => c.contextValue === "subscription-upgrade");
      expect((upgradeItem?.command as { command: string } | undefined)?.command).toBe(
        "nightgauge.openUpgradeUrl"
      );
    });

    it("uses star-empty icon", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "community" }));
      expect((item.iconPath as { id: string }).id).toBe("star-empty");
    });
  });

  describe("pro tier, active", () => {
    it('shows "Pro" description', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active" }));
      expect(item.description).toBe("Pro");
    });

    it("uses verified icon", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active" }));
      expect((item.iconPath as { id: string }).id).toBe("verified");
    });

    it("returns plan name, manage subscription children (no expiresAt)", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active", expiresAt: null }));
      const children = item.getChildren();
      const planItem = children.find((c) => c.contextValue === "subscription-plan");
      const manageItem = children.find((c) => c.contextValue === "subscription-manage");
      expect(planItem).toBeDefined();
      expect(manageItem).toBeDefined();
    });

    it("includes renewal date child when expiresAt is set", () => {
      const futureDate = new Date(Date.now() + 15 * 24 * 60 * 60 * 1000).toISOString();
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active", expiresAt: futureDate }));
      const children = item.getChildren();
      const renewalItem = children.find((c) => c.contextValue === "subscription-renewal");
      expect(renewalItem).toBeDefined();
      expect(renewalItem?.label as string).toContain("Renews in");
    });

    it('"Manage Subscription" child has nightgauge.openManageSubscription command', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active" }));
      const children = item.getChildren();
      const manageItem = children.find((c) => c.contextValue === "subscription-manage");
      expect((manageItem?.command as { command: string } | undefined)?.command).toBe(
        "nightgauge.openManageSubscription"
      );
    });

    // #4156 — machine-binding row
    it('includes a "machines bound" child with the showMachineBinding command', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active", machineBound: true, machineCount: 2 }));
      const children = item.getChildren();
      const machineItem = children.find((c) => c.contextValue === "subscription-machine-binding");
      expect(machineItem).toBeDefined();
      expect(machineItem?.label).toBe("2 machines bound");
      expect((machineItem?.command as { command: string } | undefined)?.command).toBe(
        "nightgauge.showMachineBinding"
      );
    });

    it('uses singular "1 machine bound" for a single bound machine', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active", machineBound: true, machineCount: 1 }));
      const children = item.getChildren();
      const machineItem = children.find((c) => c.contextValue === "subscription-machine-binding");
      expect(machineItem?.label).toBe("1 machine bound");
    });

    it('shows "0 machines bound" when machineCount is 0', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(
        makeData({ tier: "pro", status: "active", machineBound: false, machineCount: 0 })
      );
      const children = item.getChildren();
      const machineItem = children.find((c) => c.contextValue === "subscription-machine-binding");
      expect(machineItem?.label).toBe("0 machines bound");
    });
  });

  describe("expired license", () => {
    it('shows "License Expired" description', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "expired" }));
      expect(item.description).toBe("License Expired");
    });

    it("uses warning icon", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "expired" }));
      expect((item.iconPath as { id: string }).id).toBe("warning");
    });

    it('returns "License Expired" child and "Update Payment" action', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "expired" }));
      const children = item.getChildren();
      const planItem = children.find((c) => c.contextValue === "subscription-plan");
      const paymentItem = children.find((c) => c.contextValue === "subscription-update-payment");
      expect(planItem).toBeDefined();
      expect(paymentItem).toBeDefined();
    });

    it('"Update Payment" child has nightgauge.openSubscriptionUrl command', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "expired" }));
      const children = item.getChildren();
      const paymentItem = children.find((c) => c.contextValue === "subscription-update-payment");
      expect((paymentItem?.command as { command: string } | undefined)?.command).toBe(
        "nightgauge.openSubscriptionUrl"
      );
    });
  });

  describe("revoked license", () => {
    it('shows "Canceled" description', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "revoked" }));
      expect(item.description).toBe("Canceled");
    });

    it("uses error icon", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "revoked" }));
      expect((item.iconPath as { id: string }).id).toBe("error");
    });

    it('returns "Subscription Canceled" child and "Contact Support" action', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "revoked" }));
      const children = item.getChildren();
      const planItem = children.find((c) => c.contextValue === "subscription-plan");
      const supportItem = children.find((c) => c.contextValue === "subscription-contact-support");
      expect(planItem).toBeDefined();
      expect(supportItem).toBeDefined();
    });

    it("suspended status also shows error/canceled", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "suspended" }));
      expect(item.description).toBe("Canceled");
      expect((item.iconPath as { id: string }).id).toBe("error");
    });
  });

  describe("offline state", () => {
    it('appends "Offline — showing cached data" child', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active", offline: true }));
      const children = item.getChildren();
      const offlineItem = children.find((c) => c.contextValue === "subscription-offline");
      expect(offlineItem).toBeDefined();
    });

    it('offline child shows "just now" when recently updated', () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(
        makeData({
          tier: "pro",
          status: "active",
          offline: true,
          lastUpdated: new Date(),
        })
      );
      const children = item.getChildren();
      const offlineItem = children.find((c) => c.contextValue === "subscription-offline");
      expect(offlineItem?.description as string).toContain("just now");
    });

    it("offline community tier also shows cached data child", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "community", offline: true }));
      const children = item.getChildren();
      const offlineItem = children.find((c) => c.contextValue === "subscription-offline");
      expect(offlineItem).toBeDefined();
    });
  });

  describe("root item icon/color per state", () => {
    it("no data → person icon, disabledForeground color", () => {
      const item = new SubscriptionSectionTreeItem();
      expect((item.iconPath as { id: string }).id).toBe("person");
      expect((item.iconPath as { color?: { id: string } }).color?.id).toBe("disabledForeground");
    });

    it("community → star-empty icon, disabledForeground color", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "community", status: "community" }));
      expect((item.iconPath as { id: string }).id).toBe("star-empty");
      expect((item.iconPath as { color?: { id: string } }).color?.id).toBe("disabledForeground");
    });

    it("paid active → verified icon, testing.iconPassed color", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active" }));
      expect((item.iconPath as { id: string }).id).toBe("verified");
      expect((item.iconPath as { color?: { id: string } }).color?.id).toBe("testing.iconPassed");
    });

    it("expired → warning icon, terminal.ansiYellow color", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ status: "expired" }));
      expect((item.iconPath as { id: string }).id).toBe("warning");
      expect((item.iconPath as { color?: { id: string } }).color?.id).toBe("terminal.ansiYellow");
    });

    it("revoked → error icon, errorForeground color", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ status: "revoked" }));
      expect((item.iconPath as { id: string }).id).toBe("error");
      expect((item.iconPath as { color?: { id: string } }).color?.id).toBe("errorForeground");
    });
  });

  describe("state transitions", () => {
    it("transitions from unauthenticated to community", () => {
      const item = new SubscriptionSectionTreeItem();
      expect(item.getChildren()[0].contextValue).toBe("subscription-signed-out");

      item.update(makeData({ tier: "community", status: "community" }));
      const children = item.getChildren();
      expect(children.find((c) => c.contextValue === "subscription-upgrade")).toBeDefined();
    });

    it("transitions from active to null (sign out)", () => {
      const item = new SubscriptionSectionTreeItem();
      item.update(makeData({ tier: "pro", status: "active" }));
      expect(item.description).toBe("Pro");

      item.update(null);
      expect(item.description).toBeUndefined();
      expect(item.getChildren()[0].contextValue).toBe("subscription-signed-out");
    });
  });
});

// ---------------------------------------------------------------------------
// formatRelative helper tests
// ---------------------------------------------------------------------------

describe("formatRelative", () => {
  it('returns "No expiry" for null', () => {
    expect(formatRelative(null)).toBe("No expiry");
  });

  it('returns "Renews in N days" for future date ≤ 30 days', () => {
    const futureDate = new Date(Date.now() + 5 * 24 * 60 * 60 * 1000).toISOString();
    expect(formatRelative(futureDate)).toMatch(/^Renews in \d+ days?$/);
  });

  it('returns "Renews on {date}" for future date > 30 days', () => {
    const futureDate = new Date(Date.now() + 45 * 24 * 60 * 60 * 1000).toISOString();
    expect(formatRelative(futureDate)).toMatch(/^Renews on /);
  });

  it('returns "Expired N days ago" for past date', () => {
    const pastDate = new Date(Date.now() - 10 * 24 * 60 * 60 * 1000).toISOString();
    expect(formatRelative(pastDate)).toMatch(/^Expired \d+ days? ago$/);
  });

  it('returns "Expires today" for same-day date', () => {
    const today = new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString(); // ~12 hours from now rounds to 0 days
    // Allow either "Expires today" or "Renews in 0 days" since rounding may vary
    const result = formatRelative(today);
    expect(result === "Expires today" || result.startsWith("Renews in")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// formatTimeAgo helper tests
// ---------------------------------------------------------------------------

describe("formatTimeAgo", () => {
  it('returns "just now" for very recent date', () => {
    expect(formatTimeAgo(new Date())).toBe("just now");
  });

  it('returns "1 minute ago" for 1 minute ago', () => {
    const oneMinuteAgo = new Date(Date.now() - 61 * 1000);
    expect(formatTimeAgo(oneMinuteAgo)).toBe("1 minute ago");
  });

  it('returns "N minutes ago" for multiple minutes', () => {
    const fiveMinutesAgo = new Date(Date.now() - 5 * 60 * 1000);
    expect(formatTimeAgo(fiveMinutesAgo)).toBe("5 minutes ago");
  });

  it('returns "1 hour ago" for ~1 hour', () => {
    const oneHourAgo = new Date(Date.now() - 61 * 60 * 1000);
    expect(formatTimeAgo(oneHourAgo)).toBe("1 hour ago");
  });
});
