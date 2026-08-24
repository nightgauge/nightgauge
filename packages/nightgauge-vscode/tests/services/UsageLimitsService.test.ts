/**
 * Tests for UsageLimitsService (Issue #683 rewrite)
 *
 * Covers:
 * - No wiring when budget = 0 (disabled)
 * - Evaluates the cached snapshot immediately on initialize()
 * - Reacts to AdapterUsageService.onDidChangeUsage
 * - Warning fires at ≥80% of the monthly window's limit
 * - Critical fires at ≥90%
 * - No duplicate warning at the same threshold crossing
 * - Warning → critical escalation fires both, in order
 * - Bidirectional: usage dropping back under the warning threshold un-latches
 *   the alert level so a later rise notifies again (#683 AC)
 * - No alert when the monthly window has no configured limit
 * - getEffectiveCostUsd() reads the monthly window's `used`, not an all-time
 *   total (#683 — the permanently-red-after-month-one bug)
 * - resetCounter() re-arms alerts without touching the monthly figure
 * - dispose() unsubscribes from the change event
 *
 * @see Issue #1333
 * @see Issue #683
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

vi.mock("vscode", () => ({}));

// Mock limitsSettings to allow per-test override
const mockLimitsSettings = {
  monthlyBudgetUsd: 10,
  warningThresholdPct: 80,
  criticalThresholdPct: 90,
  pollingIntervalSeconds: 300,
};

vi.mock("../../src/config/limitsSettings", () => ({
  getLimitsSettings: vi.fn(() => mockLimitsSettings),
}));

import { UsageLimitsService } from "../../src/services/UsageLimitsService";
import { getLimitsSettings } from "../../src/config/limitsSettings";
import type { UsageSnapshot } from "../../src/services/usage/types";

/** A minimal fake AdapterUsageService: a real emitter plus a settable cache. */
function makeUsageService() {
  const handlers: Array<(snapshot: UsageSnapshot) => void> = [];
  let cached: UsageSnapshot | null = null;
  return {
    onDidChangeUsage: vi.fn((cb: (snapshot: UsageSnapshot) => void) => {
      handlers.push(cb);
      return { dispose: vi.fn(() => handlers.splice(handlers.indexOf(cb), 1)) };
    }),
    getCachedSnapshot: vi.fn(() => cached),
    /** Test helper: set the cache (as if a derivation just completed). */
    setCached(snapshot: UsageSnapshot | null) {
      cached = snapshot;
    },
    /** Test helper: fire onDidChangeUsage as AdapterUsageService.refresh() would. */
    fire(snapshot: UsageSnapshot) {
      cached = snapshot;
      for (const h of [...handlers]) h(snapshot);
    },
    /** Test helper: how many listeners are still subscribed. */
    listenerCount: () => handlers.length,
  };
}

/** Build a snapshot with a single "monthly" window at the given used/limit. */
function monthlySnapshot(used: number, limit: number | null): UsageSnapshot {
  return {
    adapter: "claude",
    plan: { kind: "pay-per-token" },
    capturedAt: new Date(),
    windows: [
      {
        id: "local-telemetry:monthly",
        label: "This month",
        scope: "monthly",
        used,
        limit,
        unit: "usd",
        resetsAt: null,
        confidence: "measured",
      },
    ],
  };
}

function makeNotificationService() {
  return {
    notifyUsageWarning: vi.fn(),
  } as unknown as import("../../src/services/NotificationService").NotificationService;
}

describe("UsageLimitsService", () => {
  beforeEach(() => {
    mockLimitsSettings.monthlyBudgetUsd = 10;
    mockLimitsSettings.warningThresholdPct = 80;
    mockLimitsSettings.criticalThresholdPct = 90;
    mockLimitsSettings.pollingIntervalSeconds = 300;
    (getLimitsSettings as Mock).mockImplementation(() => ({
      ...mockLimitsSettings,
    }));
  });

  describe("initialize()", () => {
    it("does not subscribe when monthlyBudgetUsd = 0 (disabled)", () => {
      mockLimitsSettings.monthlyBudgetUsd = 0;
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);

      service.initialize();

      expect(usage.onDidChangeUsage).not.toHaveBeenCalled();
      expect(notif.notifyUsageWarning).not.toHaveBeenCalled();
    });

    it("evaluates the already-cached snapshot immediately", () => {
      const usage = makeUsageService();
      usage.setCached(monthlySnapshot(8, 10)); // 80%
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);

      service.initialize();

      expect(notif.notifyUsageWarning).toHaveBeenCalledWith("warning", 80, 8, 10);
    });

    it("does nothing on initialize when there is no cached snapshot yet", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);

      service.initialize();

      expect(notif.notifyUsageWarning).not.toHaveBeenCalled();
    });
  });

  describe("threshold alerts", () => {
    it("fires warning when usage >= 80% of the monthly window's limit", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(8, 10)); // 80%

      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();
      expect(notif.notifyUsageWarning).toHaveBeenCalledWith("warning", 80, 8, 10);
    });

    it("fires critical when usage >= 90%", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(9.5, 10)); // 95%

      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();
      expect(notif.notifyUsageWarning).toHaveBeenCalledWith("critical", 95, 9.5, 10);
    });

    it("does not fire when usage is below the warning threshold", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(5, 10)); // 50%

      expect(notif.notifyUsageWarning).not.toHaveBeenCalled();
    });

    it("does not fire when the monthly window has no configured limit", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(500, null)); // no budget configured on this window

      expect(notif.notifyUsageWarning).not.toHaveBeenCalled();
    });
  });

  describe("deduplication and escalation", () => {
    it("does not fire a duplicate warning on a second change event at the same level", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(8.2, 10)); // 82% — warning
      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();

      usage.fire(monthlySnapshot(8.3, 10)); // still warning-band
      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();
    });

    it("escalates from warning to critical correctly", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(8.2, 10)); // 82%
      expect(notif.notifyUsageWarning).toHaveBeenLastCalledWith("warning", 82, 8.2, 10);

      usage.fire(monthlySnapshot(9.3, 10)); // 93%
      expect(notif.notifyUsageWarning).toHaveBeenCalledTimes(2);
      expect(notif.notifyUsageWarning).toHaveBeenLastCalledWith("critical", 93, 9.3, 10);
    });
  });

  describe("bidirectional thresholds (#683 AC)", () => {
    it("un-latches the alert level once usage drops back below warning, re-firing on a later rise", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(8.5, 10)); // 85% — warning fires
      expect(notif.notifyUsageWarning).toHaveBeenCalledTimes(1);

      // Calendar rolls over to a fresh month — the monthly window resets to 0.
      usage.fire(monthlySnapshot(0, 10)); // 0% — should not fire, and should un-latch
      expect(notif.notifyUsageWarning).toHaveBeenCalledTimes(1);

      // Usage climbs past the warning threshold again this month.
      usage.fire(monthlySnapshot(8.1, 10)); // 81%
      expect(notif.notifyUsageWarning).toHaveBeenCalledTimes(2);
      expect(notif.notifyUsageWarning).toHaveBeenLastCalledWith(
        "warning",
        expect.closeTo(81, 0),
        8.1,
        10
      );
    });
  });

  describe("getEffectiveCostUsd()", () => {
    it("reads the monthly window's used figure, not an all-time total", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(3.5, 10));

      expect(service.getEffectiveCostUsd()).toBe(3.5);
    });

    it("returns 0 before any snapshot has arrived", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);

      expect(service.getEffectiveCostUsd()).toBe(0);
    });
  });

  describe("resetCounter()", () => {
    it("re-arms alerts without changing getEffectiveCostUsd()", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();

      usage.fire(monthlySnapshot(8, 10)); // 80% — warning fires
      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();

      service.resetCounter();
      // The monthly figure is untouched by a manual reset — there is no
      // second offset to apply (see the class doc comment on resetCounter).
      expect(service.getEffectiveCostUsd()).toBe(8);

      // Same 80% snapshot fires again because the alert level was cleared.
      usage.fire(monthlySnapshot(8, 10));
      expect(notif.notifyUsageWarning).toHaveBeenCalledTimes(2);
    });
  });

  describe("dispose()", () => {
    it("unsubscribes from the change event", () => {
      const usage = makeUsageService();
      const notif = makeNotificationService();
      const service = new UsageLimitsService(usage as any, notif as any);
      service.initialize();
      expect(usage.listenerCount()).toBe(1);

      service.dispose();
      expect(usage.listenerCount()).toBe(0);

      // Further changes must not evaluate — dispose fully detaches.
      usage.fire(monthlySnapshot(9.9, 10));
      expect(notif.notifyUsageWarning).not.toHaveBeenCalled();
    });
  });
});
