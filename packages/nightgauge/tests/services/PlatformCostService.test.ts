/**
 * Tests for PlatformCostService (Issue #743)
 *
 * Covers:
 * 1. fetchAndCache() with successful IPC response → { ok: true, value }, cached per range
 * 2. Each failure kind (unauthorized/forbidden/server_error/offline/not_configured)
 *    → { ok: false, kind, status, endpoint, message } — status survives to the caller
 * 3. getCached(range) reflects the last successful fetch for that range
 * 4. Never throws
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PlatformCostService } from "../../src/services/PlatformCostService";
import type { IpcClientGenerated } from "../../src/services/IpcClient.generated";
import type { CostAnalyticsResult } from "../../src/services/IpcClientBase";

const ENDPOINT = "platform.getCostAnalytics";

function makeCostResult(overrides: Partial<CostAnalyticsResult> = {}): CostAnalyticsResult {
  return {
    totalInputTokens: 1000,
    totalOutputTokens: 500,
    totalTokens: 1500,
    totalCostUsd: "12.50",
    breakdown: { byModel: [], byProject: [], byDay: [] },
    ...overrides,
  };
}

function makeIpcClient(
  overrides: Partial<{
    platformGetCostAnalytics: (...args: unknown[]) => Promise<CostAnalyticsResult>;
  }> = {}
): IpcClientGenerated {
  return {
    platformGetCostAnalytics: vi.fn().mockResolvedValue(makeCostResult()),
    ...overrides,
  } as unknown as IpcClientGenerated;
}

describe("PlatformCostService.fetchAndCache", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns { ok: true, value } on success and caches it per range", async () => {
    const result = makeCostResult({ totalCostUsd: "42.00" });
    const ipc = makeIpcClient({ platformGetCostAnalytics: vi.fn().mockResolvedValue(result) });
    const svc = new PlatformCostService(ipc);

    const outcome = await svc.fetchAndCache("30d");

    expect(outcome).toEqual({ ok: true, value: result });
    expect(svc.getCached("30d")).toEqual(result);
    expect(svc.getCached("7d")).toBeNull();
  });

  it("unauthorized (401) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetCostAnalytics: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: get cost analytics: server returned 401")),
    });
    const svc = new PlatformCostService(ipc);

    const outcome = await svc.fetchAndCache("7d");

    expect(outcome).toMatchObject({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: ENDPOINT,
    });
  });

  it("forbidden (403) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetCostAnalytics: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: get cost analytics: server returned 403")),
    });
    const svc = new PlatformCostService(ipc);

    const outcome = await svc.fetchAndCache("7d");

    expect(outcome).toMatchObject({
      ok: false,
      kind: "forbidden",
      status: 403,
      endpoint: ENDPOINT,
    });
  });

  it("server_error (500) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetCostAnalytics: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: get cost analytics: server returned 500")),
    });
    const svc = new PlatformCostService(ipc);

    const outcome = await svc.fetchAndCache("90d");

    expect(outcome).toMatchObject({
      ok: false,
      kind: "server_error",
      status: 500,
      endpoint: ENDPOINT,
    });
  });

  it("offline has no status", async () => {
    const ipc = makeIpcClient({
      platformGetCostAnalytics: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC request platform.getCostAnalytics timed out after 30000ms")
        ),
    });
    const svc = new PlatformCostService(ipc);

    const outcome = await svc.fetchAndCache("7d");

    expect(outcome).toMatchObject({ ok: false, kind: "offline", endpoint: ENDPOINT });
    if (!outcome.ok) expect(outcome.status).toBeUndefined();
  });

  it("not_configured has no status", async () => {
    const ipc = makeIpcClient({
      platformGetCostAnalytics: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: analytics service unavailable")),
    });
    const svc = new PlatformCostService(ipc);

    const outcome = await svc.fetchAndCache("7d");

    expect(outcome).toMatchObject({ ok: false, kind: "not_configured", endpoint: ENDPOINT });
  });

  it("does not throw on a rejected IPC call", async () => {
    const ipc = makeIpcClient({
      platformGetCostAnalytics: vi.fn().mockRejectedValue(new Error("boom")),
    });
    const svc = new PlatformCostService(ipc);

    await expect(svc.fetchAndCache("7d")).resolves.toMatchObject({ ok: false });
  });

  it("getCached(range) returns null before first fetch for that range", () => {
    const ipc = makeIpcClient();
    const svc = new PlatformCostService(ipc);
    expect(svc.getCached("30d")).toBeNull();
  });

  it("dispose() clears the cache", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformCostService(ipc);
    await svc.fetchAndCache("7d");
    svc.dispose();
    expect(svc.getCached("7d")).toBeNull();
  });
});
