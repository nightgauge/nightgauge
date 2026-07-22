/**
 * AuditLogService — Fetches audit events from the platform's REST API.
 *
 * Calls the canonical cursor-paginated `/v1/audit-log` endpoint and translates
 * the response into the page-based `AuditLogData` shape consumed by
 * `AuditTabHtml.ts`. An optional legacy fallback (`useLegacyEndpoint`) routes
 * to the deprecated `/api/v1/audit/events` alias for safe rollback while the
 * alias remains live (sunset 2027-05-08).
 *
 * Follows the no-throw design principle: all error paths return a degraded
 * AuditLogData object rather than throwing.
 *
 * @see Issue #1583 — Audit Log Viewer Dashboard Widget
 * @see Issue #3314 — Migrate to /v1/audit-log
 */

import type {
  AuditLogData,
  AuditFilterState,
  AuditPaginationInfo,
  AuditLogEntry,
} from "../views/dashboard/DashboardState";
import type { TokenStorage } from "../platform/TokenStorage";
import type { LocalAuditFallbackService } from "./LocalAuditFallbackService";

const PAGE_SIZE = 50;

/** Default filter values used on first load (last 7 days, no filters). */
export function getDefaultAuditFilters(): AuditFilterState {
  const now = new Date();
  const sevenDaysAgo = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000);
  return {
    dateFrom: sevenDaysAgo.toISOString(),
    dateTo: now.toISOString(),
    actionFilter: "",
    userFilter: "",
  };
}

function emptyPagination(page = 0): AuditPaginationInfo {
  return {
    page,
    pageSize: PAGE_SIZE,
    totalCount: 0,
    hasNextPage: false,
    hasPrevPage: page > 0,
  };
}

function noAccessState(filters: AuditFilterState): AuditLogData {
  return {
    entries: [],
    filters,
    pagination: emptyPagination(0),
    isLoading: false,
    hasAccess: false,
  };
}

function emptyErrorState(filters: AuditFilterState, errorMessage: string): AuditLogData {
  return {
    entries: [],
    filters,
    pagination: emptyPagination(0),
    isLoading: false,
    errorMessage,
    hasAccess: true,
  };
}

/**
 * AuditLogService — Fetch and cache audit events from the platform REST API.
 */
export class AuditLogService {
  private cached: AuditLogData | null = null;
  private fetchInProgress = false;

  // Cursor stack translating page-based external API ↔ cursor pagination.
  // cursorStack[i] is the cursor required to fetch page i. Index 0 is always
  // undefined (the first page has no cursor).
  private cursorStack: (string | undefined)[] = [undefined];
  private filterSignature: string | null = null;

  // One-shot deprecation warning when the legacy alias is in use.
  private legacyDeprecationWarned = false;

  constructor(
    private readonly tokenStorage: TokenStorage,
    private readonly getPlatformUrl: () => string,
    private readonly useLegacyEndpoint: boolean = false,
    private readonly localFallback: LocalAuditFallbackService | null = null
  ) {}

  /**
   * Fetch audit events for the given filters and page.
   * Never throws — returns degraded state on any error.
   */
  async fetch(filters: AuditFilterState, page: number): Promise<AuditLogData> {
    if (this.fetchInProgress) {
      return this.cached ?? emptyErrorState(filters, "Fetch in progress");
    }
    this.fetchInProgress = true;

    try {
      const token = await this.tokenStorage.retrieve("accessToken");
      if (!token) {
        return noAccessState(filters);
      }

      this.resetCursorStackIfFiltersChanged(filters);

      const url = this.useLegacyEndpoint
        ? this.buildLegacyUrl(filters, page)
        : this.buildCanonicalUrl(filters, page);

      if (this.useLegacyEndpoint && !this.legacyDeprecationWarned) {
        this.legacyDeprecationWarned = true;

        console.warn(
          "AuditLogService: using deprecated /api/v1/audit/events alias " +
            "(auditLogLegacyEndpoint=true). The alias is sunset 2027-05-08."
        );
      }

      let response: Response;
      try {
        response = await fetch(url, {
          headers: {
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
          },
        });
      } catch (networkErr: unknown) {
        // Network failure (DNS error, connection refused, timeout) — activate local fallback
        if (this.localFallback) {
          return await this.localFallback.buildLocalAuditData(filters, page);
        }
        const msg = networkErr instanceof Error ? networkErr.message : "Network error";
        const staleResult = this.cached
          ? { ...this.cached, errorMessage: `Using cached data: ${msg}` }
          : emptyErrorState(filters, "Platform audit API not available");
        this.cached = staleResult;
        return staleResult;
      }

      if (response.status === 401 || response.status === 403) {
        // Auth failure — user has no access, not an offline scenario (ADR-002)
        return noAccessState(filters);
      }

      if (response.status === 422) {
        return emptyErrorState(filters, "Platform audit API rejected query (HTTP 422)");
      }

      if (!response.ok) {
        // 5xx, 404, or other non-200 — platform temporarily unavailable, activate local fallback
        if (this.localFallback) {
          return await this.localFallback.buildLocalAuditData(filters, page);
        }
        const errorResult = this.cached
          ? {
              ...this.cached,
              errorMessage: `Platform audit API not available (HTTP ${response.status})`,
            }
          : emptyErrorState(filters, `Platform audit API not available (HTTP ${response.status})`);
        this.cached = errorResult;
        return errorResult;
      }

      const body = (await response.json()) as Record<string, unknown>;

      const result = this.useLegacyEndpoint
        ? this.parseLegacyResponse(body, filters, page)
        : this.parseCanonicalResponse(body, filters, page);

      this.cached = result;
      return result;
    } finally {
      this.fetchInProgress = false;
    }
  }

  /** Returns the last cached value synchronously (for render use). */
  getCached(): AuditLogData | null {
    return this.cached;
  }

  /**
   * Fetch local telemetry directly, bypassing the platform API.
   * Used by the "Retry" button flow to re-attempt after the user restores connectivity,
   * and by callers that know the platform is unavailable.
   * Returns empty local data if no LocalAuditFallbackService is wired.
   */
  async fetchLocal(filters: AuditFilterState, page = 0): Promise<AuditLogData> {
    if (this.localFallback) {
      return this.localFallback.buildLocalAuditData(filters, page);
    }
    return {
      entries: [],
      filters,
      pagination: {
        page: 0,
        pageSize: PAGE_SIZE,
        totalCount: 0,
        hasNextPage: false,
        hasPrevPage: false,
      },
      isLoading: false,
      hasAccess: true,
      isLocalFallback: true,
      localDataLabel: "Showing local telemetry — platform unreachable",
    };
  }

  // ───────────────────────────────────────────────────────────────────────────
  // Internal helpers
  // ───────────────────────────────────────────────────────────────────────────

  private resetCursorStackIfFiltersChanged(filters: AuditFilterState): void {
    const sig = JSON.stringify(filters);
    if (sig !== this.filterSignature) {
      this.cursorStack = [undefined];
      this.filterSignature = sig;
    }
  }

  private buildCanonicalUrl(filters: AuditFilterState, page: number): string {
    const params = new URLSearchParams({
      from: filters.dateFrom,
      to: filters.dateTo,
      limit: String(PAGE_SIZE),
    });
    if (filters.actionFilter) {
      params.set("action", filters.actionFilter);
    }
    if (filters.userFilter) {
      // Platform validates `actor` as a UUID; non-UUID values surface as 422
      // and are caught by the no-throw error path.
      params.set("actor", filters.userFilter);
    }
    const cursor = this.cursorStack[page];
    if (cursor) {
      params.set("cursor", cursor);
    }
    return `${this.getPlatformUrl()}/v1/audit-log?${params.toString()}`;
  }

  private buildLegacyUrl(filters: AuditFilterState, page: number): string {
    const params = new URLSearchParams({
      from: filters.dateFrom,
      to: filters.dateTo,
      page: String(page),
      pageSize: String(PAGE_SIZE),
    });
    if (filters.actionFilter) {
      params.set("action", filters.actionFilter);
    }
    if (filters.userFilter) {
      params.set("userId", filters.userFilter);
    }
    return `${this.getPlatformUrl()}/api/v1/audit/events?${params.toString()}`;
  }

  private parseCanonicalResponse(
    body: Record<string, unknown>,
    filters: AuditFilterState,
    page: number
  ): AuditLogData {
    const rawItems = Array.isArray(body["items"]) ? (body["items"] as unknown[]) : [];
    const entries = parseCanonicalItems(rawItems);
    const totalCount =
      typeof body["totalCount"] === "number" ? (body["totalCount"] as number) : entries.length;
    const nextCursor =
      typeof body["nextCursor"] === "string" ? (body["nextCursor"] as string) : null;

    if (nextCursor) {
      this.cursorStack[page + 1] = nextCursor;
    } else {
      this.cursorStack.length = page + 1;
    }

    return {
      entries,
      filters,
      pagination: {
        page,
        pageSize: PAGE_SIZE,
        totalCount,
        hasNextPage: nextCursor !== null,
        hasPrevPage: page > 0,
      },
      isLoading: false,
      hasAccess: true,
    };
  }

  private parseLegacyResponse(
    body: Record<string, unknown>,
    filters: AuditFilterState,
    page: number
  ): AuditLogData {
    const rawEntries = Array.isArray(body["entries"]) ? (body["entries"] as unknown[]) : [];
    const entries = parseLegacyEntries(rawEntries);
    const totalCount =
      typeof body["totalCount"] === "number" ? (body["totalCount"] as number) : entries.length;
    const currentPage = typeof body["page"] === "number" ? (body["page"] as number) : page;

    return {
      entries,
      filters,
      pagination: {
        page: currentPage,
        pageSize: PAGE_SIZE,
        totalCount,
        hasNextPage: (currentPage + 1) * PAGE_SIZE < totalCount,
        hasPrevPage: currentPage > 0,
      },
      isLoading: false,
      hasAccess: true,
    };
  }
}

/**
 * Map canonical platform `/v1/audit-log` items → `AuditLogEntry`.
 *
 * Platform fields not modeled in `AuditLogEntry` (`ipAddress`, `userAgent`,
 * `correlationId`) are dropped at the boundary. Client-typed fields not
 * provided by the platform are defaulted: `userEmail` → undefined,
 * `costUsd` → undefined, `status` → "success" (the platform does not
 * represent failure outcomes today).
 */
function parseCanonicalItems(raw: unknown[]): AuditLogEntry[] {
  return raw
    .filter((item): item is Record<string, unknown> => typeof item === "object" && item !== null)
    .map((item) => ({
      id: String(item["id"] ?? ""),
      timestamp: String(item["createdAt"] ?? new Date().toISOString()),
      userId: String(item["accountId"] ?? ""),
      userEmail: undefined,
      action: String(item["action"] ?? ""),
      resourceType: typeof item["resourceType"] === "string" ? item["resourceType"] : undefined,
      resourceId: typeof item["resourceId"] === "string" ? item["resourceId"] : undefined,
      status: "success" as const,
      metadata:
        typeof item["metadata"] === "object" && item["metadata"] !== null
          ? (item["metadata"] as Record<string, unknown>)
          : undefined,
      costUsd: undefined,
    }));
}

/** Parse legacy `/api/v1/audit/events` entries (used when fallback flag is on). */
function parseLegacyEntries(raw: unknown[]): AuditLogEntry[] {
  return raw
    .filter((item): item is Record<string, unknown> => typeof item === "object" && item !== null)
    .map((item) => ({
      id: String(item["id"] ?? ""),
      timestamp: String(item["timestamp"] ?? new Date().toISOString()),
      userId: String(item["userId"] ?? ""),
      userEmail: typeof item["userEmail"] === "string" ? item["userEmail"] : undefined,
      action: String(item["action"] ?? ""),
      resourceType: typeof item["resourceType"] === "string" ? item["resourceType"] : undefined,
      resourceId: typeof item["resourceId"] === "string" ? item["resourceId"] : undefined,
      status: parseStatus(item["status"]),
      metadata:
        typeof item["metadata"] === "object" && item["metadata"] !== null
          ? (item["metadata"] as Record<string, unknown>)
          : undefined,
      costUsd: typeof item["costUsd"] === "number" ? item["costUsd"] : undefined,
    }));
}

function parseStatus(v: unknown): "success" | "failure" | "pending" {
  if (v === "success" || v === "failure" || v === "pending") return v;
  return "pending";
}
