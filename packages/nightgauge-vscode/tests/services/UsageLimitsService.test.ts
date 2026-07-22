/**
 * Tests for UsageLimitsService
 *
 * Covers:
 * - No polling when budget = 0 (disabled)
 * - Warning fires at ≥80% threshold
 * - Critical fires at ≥90% threshold
 * - No duplicate warning at the same threshold crossing
 * - Warning → critical escalation fires both, in order
 * - Reset counter sets offset and clears alert level
 * - dispose() clears interval and hides usage status bar item
 *
 * @see Issue #1333
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// --- Mocks ---

vi.mock("vscode", () => ({
  window: {
    createStatusBarItem: vi.fn(() => ({
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: undefined,
    })),
  },
  StatusBarAlignment: { Left: 1 },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
}));

// Mock ConfigBridge - used by getLimitsSettings
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => ({
      isInitialized: vi.fn(() => false), // returns defaults
      getUI: vi.fn(() => undefined),
    })),
  },
}));

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

// Helper: create a mock DashboardState with a configurable totalCostUsd
function makeDashboardState(totalCostUsd: number) {
  return {
    getAggregates: vi.fn(() => ({
      totalCostUsd,
      sessionCostUsd: 0,
      totalRuns: 0,
      sessionRuns: 0,
      totalTimeSavedMs: 0,
      sessionTimeSavedMs: 0,
      successRate: 1,
      avgCostPerRun: 0,
      avgTimeSavedPerRun: 0,
      stageAverages: [],
      epics: [],
      crossRepoEpics: undefined,
    })),
  } as unknown as import("../../src/views/dashboard/DashboardState").DashboardState;
}

// Helper: create a mock NotificationService
function makeNotificationService() {
  return {
    notifyUsageWarning: vi.fn(),
  } as unknown as import("../../src/services/NotificationService").NotificationService;
}

// Helper: create a mock StatusBarManager
function makeStatusBar() {
  return {
    showUsage: vi.fn(),
    hideUsage: vi.fn(),
  } as unknown as import("../../src/utils/statusBar").StatusBarManager;
}

describe("UsageLimitsService", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    // Reset mock settings to defaults
    mockLimitsSettings.monthlyBudgetUsd = 10;
    mockLimitsSettings.warningThresholdPct = 80;
    mockLimitsSettings.criticalThresholdPct = 90;
    mockLimitsSettings.pollingIntervalSeconds = 300;
    (getLimitsSettings as ReturnType<typeof vi.fn>).mockImplementation(() => ({
      ...mockLimitsSettings,
    }));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe("initialize()", () => {
    it("does not start polling when monthlyBudgetUsd = 0 (disabled)", () => {
      mockLimitsSettings.monthlyBudgetUsd = 0;
      const state = makeDashboardState(5);
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();

      // Advance time — no poll should happen
      vi.advanceTimersByTime(60_000);

      expect(state.getAggregates).not.toHaveBeenCalled();
      expect(notif.notifyUsageWarning).not.toHaveBeenCalled();
      expect(bar.showUsage).not.toHaveBeenCalled();

      service.dispose();
    });

    it("polls immediately on initialize when budget > 0", () => {
      const state = makeDashboardState(1); // 10% of $10
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();

      expect(state.getAggregates).toHaveBeenCalledTimes(1);
      expect(bar.showUsage).toHaveBeenCalledWith(1, 10);

      service.dispose();
    });
  });

  describe("threshold alerts", () => {
    it("fires warning when usage ≥ 80%", () => {
      const state = makeDashboardState(8); // 80% of $10
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();

      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();
      expect(notif.notifyUsageWarning).toHaveBeenCalledWith("warning", 80, 8, 10);

      service.dispose();
    });

    it("fires critical when usage ≥ 90%", () => {
      const state = makeDashboardState(9.5); // 95% of $10
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();

      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();
      expect(notif.notifyUsageWarning).toHaveBeenCalledWith("critical", 95, 9.5, 10);

      service.dispose();
    });

    it("does not fire when usage is below warning threshold", () => {
      const state = makeDashboardState(5); // 50% of $10
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();

      expect(notif.notifyUsageWarning).not.toHaveBeenCalled();

      service.dispose();
    });
  });

  describe("deduplication", () => {
    it("does not fire duplicate warning when polling twice at same threshold", () => {
      const state = makeDashboardState(8.2); // 82% — warning
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();
      // First poll fired warning
      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();

      // Advance to trigger second poll
      vi.advanceTimersByTime(300_000);
      // Still at 82% — should NOT fire again
      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();

      service.dispose();
    });

    it("escalates from warning to critical correctly", () => {
      let cost = 8.2; // 82% — warning
      const state = {
        getAggregates: vi.fn(() => ({
          totalCostUsd: cost,
          sessionCostUsd: 0,
          totalRuns: 0,
          sessionRuns: 0,
          totalTimeSavedMs: 0,
          sessionTimeSavedMs: 0,
          successRate: 1,
          avgCostPerRun: 0,
          avgTimeSavedPerRun: 0,
          stageAverages: [],
          epics: [],
        })),
      };
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();
      // First poll: warning at 82%
      expect(notif.notifyUsageWarning).toHaveBeenCalledTimes(1);
      expect(notif.notifyUsageWarning).toHaveBeenLastCalledWith(
        "warning",
        expect.closeTo(82, 0),
        8.2,
        10
      );

      // Cost increases to 93%
      cost = 9.3;
      vi.advanceTimersByTime(300_000);

      // Second poll: critical at 93%
      expect(notif.notifyUsageWarning).toHaveBeenCalledTimes(2);
      expect(notif.notifyUsageWarning).toHaveBeenLastCalledWith(
        "critical",
        expect.closeTo(93, 0),
        9.3,
        10
      );

      service.dispose();
    });
  });

  describe("resetCounter()", () => {
    it("sets offset so effective cost = 0 and resets alert level", () => {
      const state = makeDashboardState(8); // 80% — warning fires
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();
      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce();

      // Reset the counter — total is $8, offset becomes 8
      service.resetCounter();
      expect(bar.hideUsage).toHaveBeenCalled();

      // Effective cost is now 0, alert level reset
      expect(service.getEffectiveCostUsd()).toBe(0);

      // Poll again — no warning should fire since effective cost = 0
      vi.advanceTimersByTime(300_000);
      expect(notif.notifyUsageWarning).toHaveBeenCalledOnce(); // still just once

      service.dispose();
    });
  });

  describe("dispose()", () => {
    it("clears polling interval and hides usage item", () => {
      const state = makeDashboardState(1);
      const notif = makeNotificationService();
      const bar = makeStatusBar();
      const service = new UsageLimitsService(state as any, notif as any, bar as any);

      service.initialize();
      const callCountBefore = (state.getAggregates as ReturnType<typeof vi.fn>).mock.calls.length;

      service.dispose();
      expect(bar.hideUsage).toHaveBeenCalled();

      // After dispose, polling should not continue
      vi.advanceTimersByTime(300_000 * 3);
      const callCountAfter = (state.getAggregates as ReturnType<typeof vi.fn>).mock.calls.length;
      expect(callCountAfter).toBe(callCountBefore);
    });
  });
});
