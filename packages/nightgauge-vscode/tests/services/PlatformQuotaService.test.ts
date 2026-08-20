/**
 * Tests for PlatformQuotaService
 *
 * Covers:
 * 1. fetchAndCache() with successful IPC response → { ok: true, value } with
 *    correct PlatformQuotaData
 * 2. Network failure with cached data → degrades to stale cache
 *    ({ ok: true, value: { ...cached, isStale: true } }) rather than losing
 *    the last-known numbers
 * 3. Network failure with no cache → typed PlatformFailure, never null
 * 4. Each failure kind (unauthorized/forbidden/server_error/offline/
 *    not_configured) survives to the caller with status, when there is no
 *    cache to degrade to
 * 5. getCached() returns null before first fetch
 * 6. No quota notifications when IPC lacks limits (pct is null)
 * 7. Calls IPC platformGetUsageSummary
 *
 * Note: After migration to Go IPC (#2091), the IPC UsageSummaryResult no longer
 * includes tier limits. Pipeline run percentage (pct) and isCommunity are always
 * null/false respectively. Quota notifications only fire when pct is non-null,
 * which requires limits to be restored from license info in a future iteration.
 *
 * @see Issue #1479 - Add usage metering and quota display
 * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
 * @see Issue #743 - typed failures instead of a swallowed catch{}
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// --- Mocks ---

vi.mock("vscode", () => ({
  Disposable: class {
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
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

const ENDPOINT = "platform.getUsageSummary";

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

  it("fetchAndCache() with successful IPC response returns { ok: true } with correct PlatformQuotaData", async () => {
    const ipcClient = makeIpcClient();
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    const outcome = await service.fetchAndCache();

    expect(outcome.ok).toBe(true);
    if (!outcome.ok) throw new Error("expected success");
    expect(outcome.value.pipelineRuns.used).toBe(50);
    // IPC doesn't include limits — pct and limit are null
    expect(outcome.value.pipelineRuns.limit).toBeNull();
    expect(outcome.value.pipelineRuns.pct).toBeNull();
    expect(outcome.value.tokens.used).toBe(125000);
    expect(outcome.value.isCommunity).toBe(false);
    expect(outcome.value.isStale).toBe(false);
  });

  it("network failure with cached data → degrades to stale cache, isStale = true", async () => {
    const ipcClient = makeIpcClient();
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    // First fetch succeeds and populates cache
    await service.fetchAndCache();
    expect(service.getCached()).not.toBeNull();

    // Second fetch fails
    ipcClient.platformGetUsageSummary.mockRejectedValueOnce(
      new Error("IPC error -32000: get analytics dashboard: unexpected response 500")
    );
    const outcome = await service.fetchAndCache();

    expect(outcome.ok).toBe(true);
    if (!outcome.ok) throw new Error("expected degraded success");
    expect(outcome.value.isStale).toBe(true);
    expect(outcome.value.pipelineRuns.used).toBe(50); // same cached data
    expect(service.getCached()?.isStale).toBe(true);
  });

  it("network failure with no cache → typed PlatformFailure, not null", async () => {
    const ipcClient = makeIpcClient({
      platformGetUsageSummary: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics dashboard: unexpected response 500")
        ),
    });
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    const outcome = await service.fetchAndCache();

    expect(outcome).toMatchObject({
      ok: false,
      kind: "server_error",
      status: 500,
      endpoint: ENDPOINT,
    });
  });

  it("unauthorized (401, no cache) survives as kind + status", async () => {
    const ipcClient = makeIpcClient({
      platformGetUsageSummary: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics dashboard: unexpected response 401")
        ),
    });
    const service = new PlatformQuotaService(ipcClient as any, makeNotificationService() as any);

    const outcome = await service.fetchAndCache();

    expect(outcome).toMatchObject({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: ENDPOINT,
    });
  });

  it("forbidden (403, no cache) survives as kind + status", async () => {
    const ipcClient = makeIpcClient({
      platformGetUsageSummary: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics dashboard: unexpected response 403")
        ),
    });
    const service = new PlatformQuotaService(ipcClient as any, makeNotificationService() as any);

    const outcome = await service.fetchAndCache();

    expect(outcome).toMatchObject({
      ok: false,
      kind: "forbidden",
      status: 403,
      endpoint: ENDPOINT,
    });
  });

  it("offline (no cache) has no status", async () => {
    const ipcClient = makeIpcClient({
      platformGetUsageSummary: vi
        .fn()
        .mockRejectedValue(new Error("Go backend exited with code 1")),
    });
    const service = new PlatformQuotaService(ipcClient as any, makeNotificationService() as any);

    const outcome = await service.fetchAndCache();

    expect(outcome).toMatchObject({ ok: false, kind: "offline", endpoint: ENDPOINT });
    if (!outcome.ok) expect(outcome.status).toBeUndefined();
  });

  it("not_configured (no platform credential, no cache) has no status", async () => {
    const ipcClient = makeIpcClient({
      platformGetUsageSummary: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: platform client not configured")),
    });
    const service = new PlatformQuotaService(ipcClient as any, makeNotificationService() as any);

    const outcome = await service.fetchAndCache();

    expect(outcome).toMatchObject({ ok: false, kind: "not_configured", endpoint: ENDPOINT });
  });

  it("does not throw on a rejected IPC call", async () => {
    const ipcClient = makeIpcClient({
      platformGetUsageSummary: vi.fn().mockRejectedValue(new Error("IPC error")),
    });
    const notificationService = makeNotificationService();
    const service = new PlatformQuotaService(ipcClient as any, notificationService as any);

    await expect(service.fetchAndCache()).resolves.toMatchObject({ ok: false });
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
