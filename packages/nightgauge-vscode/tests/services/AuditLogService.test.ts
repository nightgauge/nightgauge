/**
 * Tests for AuditLogService (Issues #1583, #3314)
 *
 * Covers the canonical `/v1/audit-log` cursor-paginated path, the
 * `useLegacyEndpoint` fallback to `/api/v1/audit/events`, and all
 * no-throw error branches.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AuditLogService, getDefaultAuditFilters } from "../../src/services/AuditLogService";
import type { AuditFilterState } from "../../src/views/dashboard/DashboardState";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

const mockRetrieve = vi.fn<[string], Promise<string | null>>();
const mockTokenStorage = {
  retrieve: mockRetrieve,
} as unknown as import("../../src/platform/TokenStorage").TokenStorage;

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const PLATFORM_URL = "https://api.example.com";

const DEFAULT_FILTERS: AuditFilterState = {
  dateFrom: "2026-03-07T00:00:00Z",
  dateTo: "2026-03-14T00:00:00Z",
  actionFilter: "",
  userFilter: "",
};

function makeOkResponse(body: object): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response;
}

function makeErrorResponse(status: number): Response {
  return {
    ok: false,
    status,
    json: async () => ({}),
  } as unknown as Response;
}

function lastFetchUrl(): string {
  const calls = mockFetch.mock.calls;
  return String(calls[calls.length - 1]?.[0] ?? "");
}

// Canonical platform item shape (from /v1/audit-log).
function canonicalItem(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: "evt-1",
    accountId: "acct-1",
    action: "auth.login",
    resourceType: "user",
    resourceId: "user-1",
    metadata: { ip: "1.2.3.4" },
    ipAddress: "1.2.3.4",
    userAgent: "vscode/1.0",
    correlationId: null,
    createdAt: "2026-03-14T10:00:00Z",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AuditLogService — canonical /v1/audit-log path", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("no token → returns hasAccess: false, no fetch call", async () => {
    mockRetrieve.mockResolvedValue(null);
    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);

    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(result.hasAccess).toBe(false);
    expect(mockFetch).not.toHaveBeenCalled();
    expect(result.entries).toHaveLength(0);
  });

  it("401 response → returns hasAccess: false", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeErrorResponse(401));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(result.hasAccess).toBe(false);
  });

  it("403 response → returns hasAccess: false", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeErrorResponse(403));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(result.hasAccess).toBe(false);
  });

  it("issues GET to /v1/audit-log on the happy path", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeOkResponse({ items: [], nextCursor: null, totalCount: 0 }));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    await service.fetch(DEFAULT_FILTERS, 0);

    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(lastFetchUrl()).toContain("/v1/audit-log?");
    expect(lastFetchUrl()).not.toContain("/api/v1/audit/events");
  });

  it("200 response → maps platform items to AuditLogEntry shape", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(
      makeOkResponse({
        items: [
          canonicalItem({
            id: "evt-1",
            accountId: "acct-1",
            action: "auth.login",
            createdAt: "2026-03-14T10:00:00Z",
            metadata: { source: "test" },
          }),
        ],
        nextCursor: null,
        totalCount: 1,
      })
    );

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(result.hasAccess).toBe(true);
    expect(result.entries).toHaveLength(1);
    const e = result.entries[0];
    expect(e.id).toBe("evt-1");
    expect(e.userId).toBe("acct-1"); // accountId → userId
    expect(e.timestamp).toBe("2026-03-14T10:00:00Z"); // createdAt → timestamp
    expect(e.action).toBe("auth.login");
    expect(e.resourceType).toBe("user");
    expect(e.resourceId).toBe("user-1");
    expect(e.metadata).toEqual({ source: "test" });
    // Platform does not return these → defaulted
    expect(e.userEmail).toBeUndefined();
    expect(e.costUsd).toBeUndefined();
    expect(e.status).toBe("success");
    expect(result.errorMessage).toBeUndefined();
  });

  it("URL-encodes filter params (actionFilter→action, userFilter→actor, dateFrom→from, dateTo→to)", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeOkResponse({ items: [], nextCursor: null, totalCount: 0 }));

    const filters: AuditFilterState = {
      dateFrom: "2026-03-07T00:00:00Z",
      dateTo: "2026-03-14T00:00:00Z",
      actionFilter: "pipeline.started",
      userFilter: "550e8400-e29b-41d4-a716-446655440000",
    };

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    await service.fetch(filters, 0);

    const url = new URL(lastFetchUrl());
    expect(url.pathname).toBe("/v1/audit-log");
    expect(url.searchParams.get("from")).toBe("2026-03-07T00:00:00Z");
    expect(url.searchParams.get("to")).toBe("2026-03-14T00:00:00Z");
    expect(url.searchParams.get("action")).toBe("pipeline.started");
    expect(url.searchParams.get("actor")).toBe("550e8400-e29b-41d4-a716-446655440000");
    expect(url.searchParams.get("limit")).toBe("50");
    // No cursor on the first page
    expect(url.searchParams.get("cursor")).toBeNull();
  });

  it("empty page → hasNextPage=false, entries empty", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeOkResponse({ items: [], nextCursor: null, totalCount: 0 }));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(result.entries).toHaveLength(0);
    expect(result.pagination.hasNextPage).toBe(false);
    expect(result.pagination.hasPrevPage).toBe(false);
    expect(result.pagination.totalCount).toBe(0);
  });

  it("last page → hasNextPage=false, hasPrevPage=true when page>0", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    // page 0 returns a cursor
    mockFetch.mockResolvedValueOnce(
      makeOkResponse({
        items: [canonicalItem()],
        nextCursor: "cursor-page-1",
        totalCount: 51,
      })
    );
    // page 1 returns no cursor (last page)
    mockFetch.mockResolvedValueOnce(
      makeOkResponse({
        items: [canonicalItem({ id: "evt-2" })],
        nextCursor: null,
        totalCount: 51,
      })
    );

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    await service.fetch(DEFAULT_FILTERS, 0);
    const last = await service.fetch(DEFAULT_FILTERS, 1);

    expect(last.pagination.hasNextPage).toBe(false);
    expect(last.pagination.hasPrevPage).toBe(true);
    expect(last.pagination.page).toBe(1);
  });

  it("sequential page walk sends the cursor returned by the prior page", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValueOnce(
      makeOkResponse({
        items: [canonicalItem()],
        nextCursor: "cursor-XYZ",
        totalCount: 100,
      })
    );
    mockFetch.mockResolvedValueOnce(
      makeOkResponse({
        items: [canonicalItem({ id: "evt-2" })],
        nextCursor: null,
        totalCount: 100,
      })
    );

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    await service.fetch(DEFAULT_FILTERS, 0);
    await service.fetch(DEFAULT_FILTERS, 1);

    const url = new URL(lastFetchUrl());
    expect(url.searchParams.get("cursor")).toBe("cursor-XYZ");
  });

  it("422 → errorMessage set, no throw, entries empty", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeErrorResponse(422));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    const result = await service.fetch({ ...DEFAULT_FILTERS, userFilter: "not-a-uuid" }, 0);

    expect(result.hasAccess).toBe(true);
    expect(result.errorMessage).toContain("HTTP 422");
    expect(result.entries).toHaveLength(0);
  });

  it("changing filter signature resets the cursor stack", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValueOnce(
      makeOkResponse({
        items: [canonicalItem()],
        nextCursor: "stale-cursor",
        totalCount: 100,
      })
    );
    mockFetch.mockResolvedValueOnce(makeOkResponse({ items: [], nextCursor: null, totalCount: 0 }));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    await service.fetch(DEFAULT_FILTERS, 0);

    // Different filters → page 0 walk must NOT send the prior cursor.
    await service.fetch({ ...DEFAULT_FILTERS, actionFilter: "pipeline.completed" }, 0);

    const url = new URL(lastFetchUrl());
    expect(url.searchParams.get("cursor")).toBeNull();
    expect(url.searchParams.get("action")).toBe("pipeline.completed");
  });

  it("404 → graceful error message (defense-in-depth, expected unreachable)", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeErrorResponse(404));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(result.errorMessage).toContain("HTTP 404");
    expect(result.entries).toHaveLength(0);
    expect(lastFetchUrl()).toContain("/v1/audit-log");
  });

  it("non-200 non-auth error → returns error state with errorMessage", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeErrorResponse(500));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(result.hasAccess).toBe(true);
    expect(result.errorMessage).toContain("HTTP 500");
    expect(result.entries).toHaveLength(0);
  });

  it("network error on first call → empty error state, no throw", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockRejectedValue(new Error("ECONNREFUSED"));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    let result;
    await expect(
      (async () => {
        result = await service.fetch(DEFAULT_FILTERS, 0);
      })()
    ).resolves.toBeUndefined();

    expect(result!.hasAccess).toBe(true);
    expect(result!.errorMessage).toContain("Platform audit API not available");
    expect(result!.entries).toHaveLength(0);
  });

  it("network error after a cached success → returns stale cache with errorMessage", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValueOnce(
      makeOkResponse({
        items: [canonicalItem({ id: "evt-cached" })],
        nextCursor: null,
        totalCount: 1,
      })
    );
    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    await service.fetch(DEFAULT_FILTERS, 0);

    mockFetch.mockRejectedValueOnce(new Error("timeout"));
    const stale = await service.fetch(DEFAULT_FILTERS, 0);

    expect(stale.entries).toHaveLength(1);
    expect(stale.entries[0].id).toBe("evt-cached");
    expect(stale.errorMessage).toContain("Using cached data");
  });

  it("getCached() returns last result after a successful fetch", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeOkResponse({ items: [], nextCursor: null, totalCount: 0 }));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL);
    expect(service.getCached()).toBeNull();

    await service.fetch(DEFAULT_FILTERS, 0);
    expect(service.getCached()).not.toBeNull();
    expect(service.getCached()!.hasAccess).toBe(true);
  });
});

describe("AuditLogService — legacy /api/v1/audit/events fallback", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("useLegacyEndpoint=true → URL contains /api/v1/audit/events and parses legacy shape", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(
      makeOkResponse({
        entries: [
          {
            id: "evt-1",
            timestamp: "2026-03-14T10:00:00Z",
            userId: "user-1",
            userEmail: "alice@example.com",
            action: "auth.login",
            status: "success",
            costUsd: 0.005,
          },
        ],
        totalCount: 1,
        page: 0,
      })
    );

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL, true);
    const result = await service.fetch(DEFAULT_FILTERS, 0);

    expect(lastFetchUrl()).toContain("/api/v1/audit/events");
    expect(lastFetchUrl()).not.toContain("/v1/audit-log");
    expect(result.entries).toHaveLength(1);
    expect(result.entries[0].userEmail).toBe("alice@example.com");
    expect(result.entries[0].costUsd).toBe(0.005);
    expect(result.entries[0].status).toBe("success");
  });

  it("useLegacyEndpoint=true uses page-based pagination params", async () => {
    mockRetrieve.mockResolvedValue("valid-token");
    mockFetch.mockResolvedValue(makeOkResponse({ entries: [], totalCount: 120, page: 1 }));

    const service = new AuditLogService(mockTokenStorage, () => PLATFORM_URL, true);
    const result = await service.fetch(DEFAULT_FILTERS, 1);

    const url = new URL(lastFetchUrl());
    expect(url.searchParams.get("page")).toBe("1");
    expect(url.searchParams.get("pageSize")).toBe("50");
    expect(result.pagination.hasNextPage).toBe(true); // 2*50 < 120
    expect(result.pagination.hasPrevPage).toBe(true);
  });
});

describe("getDefaultAuditFilters", () => {
  it("returns valid filter object with 7-day range", () => {
    const filters = getDefaultAuditFilters();
    expect(filters.actionFilter).toBe("");
    expect(filters.userFilter).toBe("");

    const from = new Date(filters.dateFrom);
    const to = new Date(filters.dateTo);
    const diffDays = (to.getTime() - from.getTime()) / (1000 * 60 * 60 * 24);
    expect(diffDays).toBeCloseTo(7, 0);
  });
});
