/**
 * Tests for PlatformQuotaService
 *
 * Covers:
 * 1. fetchAndCache() with successful IPC response → correct PlatformQuotaData
 * 2. Network failure with cached data → returns stale cache with isStale = true
 * 3. Network failure with no cache → returns null
 * 4. getCached() returns null before first fetch
 * 5. No quota notifications when IPC lacks limits (pct is null)
 * 6. Calls IPC platformGetUsageSummary
 *
 * Note: After migration to Go IPC (#2091), the IPC UsageSummaryResult no longer
 * includes tier limits. Pipeline run percentage (pct) and isCommunity are always
 * null/false respectively. Quota notifications only fire when pct is non-null,
 * which requires limits to be restored from license info in a future iteration.
 *
 * @see Issue #1479 - Add usage metering and quota display
 * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// --- Mocks ---

vi.mock("vscode", () => ({
  Disposable: class {
    dispose() {}
  },
}));

// Mock ConfigBridge used by getLimitsSettings
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => ({
      isInitialized: vi.fn(() => false),
      getUI: vi.fn(() => undefined),
    })),
  },
}));

vi.mock("../../src/config/limitsSettings", () => ({
  getLimitsSettings: vi.fn(() => ({
    monthlyBudgetUsd: 10,
    warningThresholdPct: 80,
    criticalThresholdPct: 90,
    pollingIntervalSeconds: 300,
    quotaWarningThresholdPct: 80,
    quotaCriticalThresholdPct: 90,
    quotaBlockThresholdPct: 100,
  })),
}));

import { PlatformQuotaService } from "../../src/services/PlatformQuotaService";
import type { UsageSummaryResult } from "../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeIpcClient(
  overrides: Partial<{
    platformGetUsageSummary: () => Promise<any>;
  }> = {}
) {
  return {
    platformGetUsageSummary: vi.fn().mockResolvedValue({
      totalRuns: 50,
      successRatePct: 92.0,
      totalCostUsd: 4.75,
      totalTokens: 125000,
      period: "month",
    }),
    ...overrides,
  };
}

function makeNotificationService() {
  return {
    notifyQuotaWarning: vi.fn(),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PlatformQuotaService", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("fetchAndCache() with successful IPC response returns correct PlatformQuotaData", async () => {
    const ipcClient = makeIpcClient();
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    const data = await service.fetchAndCache();

    expect(data).not.toBeNull();
    expect(data!.pipelineRuns.used).toBe(50);
    // IPC doesn't include limits — pct and limit are null
    expect(data!.pipelineRuns.limit).toBeNull();
    expect(data!.pipelineRuns.pct).toBeNull();
    expect(data!.tokens.used).toBe(125000);
    expect(data!.isCommunity).toBe(false);
    expect(data!.isStale).toBe(false);
  });

  it("network failure with cached data → returns stale cache with isStale = true", async () => {
    const ipcClient = makeIpcClient();
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    // First fetch succeeds and populates cache
    await service.fetchAndCache();
    expect(service.getCached()).not.toBeNull();

    // Second fetch fails
    ipcClient.platformGetUsageSummary.mockRejectedValueOnce(new Error("IPC error"));
    const staleData = await service.fetchAndCache();

    expect(staleData).not.toBeNull();
    expect(staleData!.isStale).toBe(true);
    expect(staleData!.pipelineRuns.used).toBe(50); // same cached data
  });

  it("network failure with no cache → returns null", async () => {
    const ipcClient = makeIpcClient({
      platformGetUsageSummary: vi.fn().mockRejectedValue(new Error("IPC error")),
    });
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    const data = await service.fetchAndCache();

    expect(data).toBeNull();
  });

  it("getCached() returns null before first fetch", () => {
    const ipcClient = makeIpcClient();
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    expect(service.getCached()).toBeNull();
  });

  it("no quota notifications when pct is null (IPC has no limits)", async () => {
    const ipcClient = makeIpcClient();
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    await service.fetchAndCache();

    // Since pct is null (no limits in IPC response), no notifications fire
    expect(notificationService.notifyQuotaWarning).not.toHaveBeenCalled();
  });

  it("calls IPC platformGetUsageSummary", async () => {
    const ipcClient = makeIpcClient();
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    await service.fetchAndCache();

    expect(ipcClient.platformGetUsageSummary).toHaveBeenCalledOnce();
  });
});
