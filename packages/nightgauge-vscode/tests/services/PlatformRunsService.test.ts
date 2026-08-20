/**
 * Tests for PlatformRunsService (Issue #743)
 *
 * Covers:
 * 1. fetchAndCache() with successful IPC response → { ok: true, value }
 * 2. Each failure kind (unauthorized/forbidden/server_error/offline/not_configured)
 *    → { ok: false, kind, status, endpoint, message } — status survives to the caller
 * 3. getCached() reflects the last successful fetch
 * 4. Never throws
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PlatformRunsService } from "../../src/services/PlatformRunsService";
import type { IpcClientGenerated } from "../../src/services/IpcClient.generated";
import type { AnalyticsRunsResult } from "../../src/services/IpcClientBase";
import type { RunsFilterState } from "../../src/views/dashboard/DashboardState";

const ENDPOINT = "platform.getAnalyticsRuns";

const noFilters: RunsFilterState = {
  dateFrom: "",
  dateTo: "",
  outcomeFilter: "",
  branchFilter: "",
};

function makeRunsResult(overrides: Partial<AnalyticsRunsResult> = {}): AnalyticsRunsResult {
  return {
    entries: [],
    total_count: 0,
    has_more: false,
    ...overrides,
  };
}

function makeIpcClient(
  overrides: Partial<{
    platformGetAnalyticsRuns: (...args: unknown[]) => Promise<AnalyticsRunsResult>;
  }> = {}
): IpcClientGenerated {
  return {
    platformGetAnalyticsRuns: vi.fn().mockResolvedValue(makeRunsResult()),
    ...overrides,
  } as unknown as IpcClientGenerated;
}

describe("PlatformRunsService.fetchAndCache", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns { ok: true, value } on success and caches it", async () => {
    const result = makeRunsResult({ total_count: 5 });
    const ipc = makeIpcClient({ platformGetAnalyticsRuns: vi.fn().mockResolvedValue(result) });
    const svc = new PlatformRunsService(ipc);

    const outcome = await svc.fetchAndCache(noFilters);

    expect(outcome).toEqual({ ok: true, value: result });
    expect(svc.getCached()).toEqual(result);
  });

  it("unauthorized (401) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsRuns: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: get analytics runs: server returned 401")),
    });
    const svc = new PlatformRunsService(ipc);

    const outcome = await svc.fetchAndCache(noFilters);

    expect(outcome).toMatchObject({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: ENDPOINT,
    });
  });

  it("forbidden (403) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsRuns: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: get analytics runs: server returned 403")),
    });
    const svc = new PlatformRunsService(ipc);

    const outcome = await svc.fetchAndCache(noFilters);

    expect(outcome).toMatchObject({
      ok: false,
      kind: "forbidden",
      status: 403,
      endpoint: ENDPOINT,
    });
  });

  it("server_error (500) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsRuns: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: get analytics runs: server returned 500")),
    });
    const svc = new PlatformRunsService(ipc);

    const outcome = await svc.fetchAndCache(noFilters);

    expect(outcome).toMatchObject({
      ok: false,
      kind: "server_error",
      status: 500,
      endpoint: ENDPOINT,
    });
  });

  it("offline has no status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsRuns: vi
        .fn()
        .mockRejectedValue(new Error("Go backend exited with code 1")),
    });
    const svc = new PlatformRunsService(ipc);

    const outcome = await svc.fetchAndCache(noFilters);

    expect(outcome).toMatchObject({ ok: false, kind: "offline", endpoint: ENDPOINT });
    if (!outcome.ok) expect(outcome.status).toBeUndefined();
  });

  it("not_configured has no status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsRuns: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: analytics service unavailable")),
    });
    const svc = new PlatformRunsService(ipc);

    const outcome = await svc.fetchAndCache(noFilters);

    expect(outcome).toMatchObject({ ok: false, kind: "not_configured", endpoint: ENDPOINT });
  });

  it("does not throw on a rejected IPC call", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsRuns: vi.fn().mockRejectedValue(new Error("boom")),
    });
    const svc = new PlatformRunsService(ipc);

    await expect(svc.fetchAndCache(noFilters)).resolves.toMatchObject({ ok: false });
  });

  it("getCached() returns null before first fetch", () => {
    const ipc = makeIpcClient();
    const svc = new PlatformRunsService(ipc);
    expect(svc.getCached()).toBeNull();
  });

  it("dispose() clears the cache", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformRunsService(ipc);
    await svc.fetchAndCache(noFilters);
    svc.dispose();
    expect(svc.getCached()).toBeNull();
  });
});
