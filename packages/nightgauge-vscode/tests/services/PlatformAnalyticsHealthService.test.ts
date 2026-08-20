/**
 * Tests for PlatformAnalyticsHealthService (Issue #743)
 *
 * Covers:
 * 1. fetchAndCache() with successful IPC response → { ok: true, value }
 * 2. Each failure kind (unauthorized/forbidden/server_error/offline/not_configured)
 *    → { ok: false, kind, status, endpoint, message } — status survives to the caller
 * 3. getCached() reflects the last successful fetch, unaffected by a later failure
 * 4. Never throws — the caller does not need try/catch
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PlatformAnalyticsHealthService } from "../../src/services/PlatformAnalyticsHealthService";
import type { IpcClientGenerated } from "../../src/services/IpcClient.generated";
import type { AnalyticsHealthResult } from "../../src/services/IpcClientBase";

const ENDPOINT = "platform.getAnalyticsHealth";

function makeHealthResult(overrides: Partial<AnalyticsHealthResult> = {}): AnalyticsHealthResult {
  return {
    overall_score: 92,
    dimensions: [],
    generated_at: "2026-03-14T10:00:00Z",
    period_days: 30,
    total_runs: 50,
    ...overrides,
  };
}

function makeIpcClient(
  overrides: Partial<{ platformGetAnalyticsHealth: () => Promise<AnalyticsHealthResult> }> = {}
): IpcClientGenerated {
  return {
    platformGetAnalyticsHealth: vi.fn().mockResolvedValue(makeHealthResult()),
    ...overrides,
  } as unknown as IpcClientGenerated;
}

describe("PlatformAnalyticsHealthService.fetchAndCache", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns { ok: true, value } on success and caches it", async () => {
    const result = makeHealthResult({ total_runs: 77 });
    const ipc = makeIpcClient({ platformGetAnalyticsHealth: vi.fn().mockResolvedValue(result) });
    const svc = new PlatformAnalyticsHealthService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toEqual({ ok: true, value: result });
    expect(svc.getCached()).toEqual(result);
  });

  it("unauthorized (401) survives as kind + status on the returned failure", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsHealth: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics health: server returned 401")
        ),
    });
    const svc = new PlatformAnalyticsHealthService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome.ok).toBe(false);
    if (!outcome.ok) {
      expect(outcome.kind).toBe("unauthorized");
      expect(outcome.status).toBe(401);
      expect(outcome.endpoint).toBe(ENDPOINT);
      expect(outcome.message).toContain("401");
    }
  });

  it("forbidden (403) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsHealth: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics health: server returned 403")
        ),
    });
    const svc = new PlatformAnalyticsHealthService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({
      ok: false,
      kind: "forbidden",
      status: 403,
      endpoint: ENDPOINT,
    });
  });

  it("server_error (500) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsHealth: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: get analytics health: server returned 500")
        ),
    });
    const svc = new PlatformAnalyticsHealthService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({
      ok: false,
      kind: "server_error",
      status: 500,
      endpoint: ENDPOINT,
    });
  });

  it("offline (dead Go backend) has no status but a kind of offline", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsHealth: vi
        .fn()
        .mockRejectedValue(new Error("Go backend exited with code 1")),
    });
    const svc = new PlatformAnalyticsHealthService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({ ok: false, kind: "offline", endpoint: ENDPOINT });
    if (!outcome.ok) expect(outcome.status).toBeUndefined();
  });

  it("not_configured (no analytics credential) has no status", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsHealth: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: analytics service unavailable")),
    });
    const svc = new PlatformAnalyticsHealthService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({ ok: false, kind: "not_configured", endpoint: ENDPOINT });
    if (!outcome.ok) expect(outcome.status).toBeUndefined();
  });

  it("does not throw — a failing IPC call resolves rather than rejects", async () => {
    const ipc = makeIpcClient({
      platformGetAnalyticsHealth: vi.fn().mockRejectedValue(new Error("boom")),
    });
    const svc = new PlatformAnalyticsHealthService(ipc);

    await expect(svc.fetchAndCache()).resolves.toMatchObject({ ok: false });
  });

  it("getCached() returns null before first fetch", () => {
    const ipc = makeIpcClient();
    const svc = new PlatformAnalyticsHealthService(ipc);
    expect(svc.getCached()).toBeNull();
  });

  it("a later failure does not clear a prior successful cache", async () => {
    const result = makeHealthResult();
    const ipc = makeIpcClient({
      platformGetAnalyticsHealth: vi
        .fn()
        .mockResolvedValueOnce(result)
        .mockRejectedValueOnce(new Error("boom")),
    });
    const svc = new PlatformAnalyticsHealthService(ipc);

    await svc.fetchAndCache();
    const second = await svc.fetchAndCache();

    expect(second.ok).toBe(false);
    expect(svc.getCached()).toEqual(result);
  });

  it("dispose() clears the cache", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformAnalyticsHealthService(ipc);
    await svc.fetchAndCache();
    svc.dispose();
    expect(svc.getCached()).toBeNull();
  });
});
