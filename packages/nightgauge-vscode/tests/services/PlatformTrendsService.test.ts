/**
 * Tests for PlatformTrendsService (Issue #743)
 *
 * Covers:
 * 1. fetchAndCache() with successful IPC response → { ok: true, value }, cached per period
 * 2. Each failure kind (unauthorized/forbidden/server_error/offline/not_configured)
 *    → { ok: false, kind, status, endpoint, message } — status survives to the caller
 * 3. getCached(period) reflects the last successful fetch for that period
 * 4. Never throws
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PlatformTrendsService } from "../../src/services/PlatformTrendsService";
import type { IpcClientGenerated } from "../../src/services/IpcClient.generated";
import type { AnalyticsTrendsResult } from "../../src/services/IpcClientBase";

const ENDPOINT = "platform.getAnalyticsTrends";

function makeTrendsResult(overrides: Partial<AnalyticsTrendsResult> = {}): AnalyticsTrendsResult {
  return {
    current: [],
    previous: [],
    period: "30d",
    ...overrides,
  };
}

function makeIpcClient(
  overrides: Partial<{
    platformGetAnalyticsTrends: (...args: unknown[]) => Promise<AnalyticsTrendsResult>;
  }> = {}
): IpcClientGenerated {
  return {
    platformGetAnalyticsTrends: vi.fn().mockResolvedValue(makeTrendsResult()),
    ...overrides,
  } as unknown as IpcClientGenerated;
}

describe("PlatformTrendsService.fetchAndCache", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns { ok: true, value } on success and caches it per period", async () => {
    const result = makeTrendsResult({ period: "90d" });
    const ipc = makeIpcClient({ platformGetAnalyticsTrends: vi.fn().mockResolvedValue(result) });
    const svc = new PlatformTrendsService(ipc);

    const outcome = await svc.fetchAndCache("90d");

    expect(outcome).toEqual({ ok: true, value: result });
    expect(svc.getCached("90d")).toEqual(result);
    expect(svc.getCached("30d")).toBeNull();
  });

  it("unauthorized (401) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsTrends: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics trends: server returned 401")
        ),
    });
    const svc = new PlatformTrendsService(ipc);

    const outcome = await svc.fetchAndCache("30d");

    expect(outcome).toMatchObject({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: ENDPOINT,
    });
  });

  it("forbidden (403) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsTrends: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics trends: server returned 403")
        ),
    });
    const svc = new PlatformTrendsService(ipc);

    const outcome = await svc.fetchAndCache("30d");

    expect(outcome).toMatchObject({
      ok: false,
      kind: "forbidden",
      status: 403,
      endpoint: ENDPOINT,
    });
  });

  it("server_error (500) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsTrends: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics trends: server returned 500")
        ),
    });
    const svc = new PlatformTrendsService(ipc);

    const outcome = await svc.fetchAndCache("180d");

    expect(outcome).toMatchObject({
      ok: false,
      kind: "server_error",
      status: 500,
      endpoint: ENDPOINT,
    });
  });

  it("offline has no status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsTrends: vi
        .fn()
        .mockRejectedValue(new Error("Go backend exited with code 1")),
    });
    const svc = new PlatformTrendsService(ipc);

    const outcome = await svc.fetchAndCache("30d");

    expect(outcome).toMatchObject({ ok: false, kind: "offline", endpoint: ENDPOINT });
    if (!outcome.ok) expect(outcome.status).toBeUndefined();
  });

  it("not_configured has no status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsTrends: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: analytics service unavailable")),
    });
    const svc = new PlatformTrendsService(ipc);

    const outcome = await svc.fetchAndCache("30d");

    expect(outcome).toMatchObject({ ok: false, kind: "not_configured", endpoint: ENDPOINT });
  });

  it("does not throw on a rejected IPC call", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsTrends: vi.fn().mockRejectedValue(new Error("boom")),
    });
    const svc = new PlatformTrendsService(ipc);

    await expect(svc.fetchAndCache("30d")).resolves.toMatchObject({ ok: false });
  });

  it("getCached(period) returns null before first fetch for that period", () => {
    const ipc = makeIpcClient();
    const svc = new PlatformTrendsService(ipc);
    expect(svc.getCached("30d")).toBeNull();
  });

  it("dispose() clears the cache", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformTrendsService(ipc);
    await svc.fetchAndCache("30d");
    svc.dispose();
    expect(svc.getCached("30d")).toBeNull();
  });
});
