/**
 * Integration tests for AuditLogService local fallback activation (Issue #3324)
 *
 * Covers:
 * - 404 response → local fallback (not error state)
 * - 5xx response → local fallback
 * - Network error → local fallback
 * - 401 response → noAccessState (NOT local fallback)
 * - No localFallback injected → original error state (backward compat)
 * - Successful platform fetch → isLocalFallback is undefined/false
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AuditLogService, getDefaultAuditFilters } from "../../src/services/AuditLogService";
import type { LocalAuditFallbackService } from "../../src/services/LocalAuditFallbackService";
import type { AuditLogData, AuditFilterState } from "../../src/views/dashboard/DashboardState";
import type { TokenStorage } from "../../src/platform/TokenStorage";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeTokenStorage(token: string | null = "valid-token"): TokenStorage {
  return {
    retrieve: vi.fn().mockResolvedValue(token),
    store: vi.fn(),
    delete: vi.fn(),
  } as unknown as TokenStorage;
}

function makeLocalFallback(overrides: Partial<AuditLogData> = {}): LocalAuditFallbackService {
  const base: AuditLogData = {
    entries: [
      {
        id: "local-42-2026-01-01T00:00:00.000Z",
        timestamp: "2026-01-01T00:00:00.000Z",
        userId: "local",
        action: "pipeline_run_completed",
        resourceType: "pipeline_run",
        resourceId: "42",
        status: "success",
        costUsd: 0.05,
      },
    ],
    filters: getDefaultAuditFilters(),
    pagination: { page: 0, pageSize: 50, totalCount: 1, hasNextPage: false, hasPrevPage: false },
    isLoading: false,
    hasAccess: true,
    isLocalFallback: true,
    localDataLabel: "Showing local telemetry — platform unreachable",
    ...overrides,
  };
  return {
    buildLocalAuditData: vi.fn().mockResolvedValue(base),
  } as unknown as LocalAuditFallbackService;
}

function makeFetchResponse(status: number, body: unknown = {}): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AuditLogService — local fallback activation", () => {
  const filters: AuditFilterState = getDefaultAuditFilters();

  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn());
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("404 response → activates local fallback and returns local data", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValue(makeFetchResponse(404));
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetch(filters, 0);

    expect(result.isLocalFallback).toBe(true);
    expect(result.hasAccess).toBe(true);
    expect(result.entries).toHaveLength(1);
    expect(localFallback.buildLocalAuditData).toHaveBeenCalledOnce();
  });

  it("503 response → activates local fallback", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValue(makeFetchResponse(503));
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetch(filters, 0);

    expect(result.isLocalFallback).toBe(true);
    expect(localFallback.buildLocalAuditData).toHaveBeenCalledOnce();
  });

  it("network error (DNS failure) → activates local fallback", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error("getaddrinfo ENOTFOUND api.example.com")
    );
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetch(filters, 0);

    expect(result.isLocalFallback).toBe(true);
    expect(localFallback.buildLocalAuditData).toHaveBeenCalledOnce();
  });

  it("401 response → returns noAccessState, does NOT activate local fallback (ADR-002)", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValue(makeFetchResponse(401));
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetch(filters, 0);

    expect(result.hasAccess).toBe(false);
    expect(result.isLocalFallback).toBeFalsy();
    expect(localFallback.buildLocalAuditData).not.toHaveBeenCalled();
  });

  it("403 response → returns noAccessState, does NOT activate local fallback", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValue(makeFetchResponse(403));
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetch(filters, 0);

    expect(result.hasAccess).toBe(false);
    expect(result.isLocalFallback).toBeFalsy();
    expect(localFallback.buildLocalAuditData).not.toHaveBeenCalled();
  });

  it("no localFallback injected + 404 → returns original error state (backward compat)", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValue(makeFetchResponse(404));
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      null
    );

    const result = await svc.fetch(filters, 0);

    expect(result.isLocalFallback).toBeFalsy();
    expect(result.hasAccess).toBe(true);
    expect(result.errorMessage).toContain("404");
  });

  it("successful platform fetch → isLocalFallback is not set", async () => {
    const platformResponse = {
      items: [
        {
          id: "plat-001",
          createdAt: "2026-01-01T00:00:00.000Z",
          accountId: "user-abc",
          action: "pipeline_run_completed",
        },
      ],
      totalCount: 1,
      nextCursor: null,
    };
    (fetch as ReturnType<typeof vi.fn>).mockResolvedValue(makeFetchResponse(200, platformResponse));
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetch(filters, 0);

    expect(result.isLocalFallback).toBeFalsy();
    expect(result.hasAccess).toBe(true);
    expect(result.entries).toHaveLength(1);
    expect(localFallback.buildLocalAuditData).not.toHaveBeenCalled();
  });

  it("no token → returns noAccessState without calling local fallback", async () => {
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(null),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetch(filters, 0);

    expect(result.hasAccess).toBe(false);
    expect(localFallback.buildLocalAuditData).not.toHaveBeenCalled();
  });

  it("fetchLocal delegates to localFallback.buildLocalAuditData", async () => {
    const localFallback = makeLocalFallback();
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      localFallback
    );

    const result = await svc.fetchLocal(filters, 0);

    expect(result.isLocalFallback).toBe(true);
    expect(localFallback.buildLocalAuditData).toHaveBeenCalledWith(filters, 0);
  });

  it("fetchLocal with no localFallback returns empty local data", async () => {
    const svc = new AuditLogService(
      makeTokenStorage(),
      () => "https://api.example.com",
      false,
      null
    );

    const result = await svc.fetchLocal(filters, 0);

    expect(result.isLocalFallback).toBe(true);
    expect(result.entries).toHaveLength(0);
    expect(result.hasAccess).toBe(true);
  });
});
