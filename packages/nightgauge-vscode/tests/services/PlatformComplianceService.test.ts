/**
 * Tests for PlatformComplianceService (Issue #3322, updated for #743)
 *
 * Covers:
 * 1. fetchAndCache → calls platformAuditListReports with correct params
 * 2. fetchAndCache → returns { ok: true, value } and caches the result
 * 3. fetchAndCache → each failure kind (unauthorized/forbidden/server_error/
 *    offline/not_configured) → { ok: false, kind, status, endpoint, message }
 * 4. generateReport → calls platformAuditGenerateReport with correct params
 * 5. getReport → calls platformAuditGetReport with correct reportId
 * 6. getCached → returns cached value synchronously
 * 7. dispose → clears cache
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PlatformComplianceService } from "../../src/services/PlatformComplianceService";
import type { IpcClientGenerated } from "../../src/services/IpcClient.generated";
import type {
  ComplianceReportsPage,
  ComplianceReportResult,
  ComplianceReportDetail,
} from "../../src/services/IpcClientBase";

const ENDPOINT = "platform.auditListReports";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeReportsPage(overrides: Partial<ComplianceReportsPage> = {}): ComplianceReportsPage {
  return {
    reports: [],
    hasMore: false,
    ...overrides,
  };
}

function makeIpcClient(overrides: Partial<IpcClientGenerated> = {}): IpcClientGenerated {
  return {
    platformAuditListReports: vi.fn().mockResolvedValue(makeReportsPage()),
    platformAuditGenerateReport: vi.fn().mockResolvedValue({
      id: "rpt-1",
      status: "pending",
      reportType: "soc2",
      startDate: "2026-01-01",
      endDate: "2026-03-31",
      format: "pdf",
      createdAt: "2026-05-01T00:00:00Z",
    } satisfies ComplianceReportResult),
    platformAuditGetReport: vi.fn().mockResolvedValue({
      id: "rpt-1",
      reportType: "soc2",
      status: "ready",
      startDate: "2026-01-01",
      endDate: "2026-03-31",
      format: "pdf",
      downloadUrl: "https://example.com/rpt-1.pdf",
      createdAt: "2026-05-01T00:00:00Z",
    } satisfies ComplianceReportDetail),
    ...overrides,
  } as unknown as IpcClientGenerated;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PlatformComplianceService.fetchAndCache", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("calls platformAuditListReports with correct params", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    await svc.fetchAndCache("cursor-1", 10);
    expect(ipc.platformAuditListReports).toHaveBeenCalledWith("cursor-1", 10);
  });

  it("returns { ok: true, value } and caches the result", async () => {
    const page = makeReportsPage({ hasMore: true });
    const ipc = makeIpcClient({
      platformAuditListReports: vi.fn().mockResolvedValue(page),
    });
    const svc = new PlatformComplianceService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toEqual({ ok: true, value: page });
    expect(svc.getCached()).toEqual(page);
  });

  it("unauthorized (401) survives as kind + status", async () => {
    const ipc = makeIpcClient({
      platformAuditListReports: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: list compliance reports: server returned 401")
        ),
    });
    const svc = new PlatformComplianceService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: ENDPOINT,
    });
  });

  it("forbidden (403) survives as kind + status — no access for this role/tier", async () => {
    const ipc = makeIpcClient({
      platformAuditListReports: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: list compliance reports: server returned 403")
        ),
    });
    const svc = new PlatformComplianceService(ipc);

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
      platformAuditListReports: vi
        .fn()
        .mockRejectedValue(
          new Error("IPC error -32000: list compliance reports: server returned 500")
        ),
    });
    const svc = new PlatformComplianceService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({
      ok: false,
      kind: "server_error",
      status: 500,
      endpoint: ENDPOINT,
    });
  });

  it("offline (network error) has no status", async () => {
    const ipc = makeIpcClient({
      platformAuditListReports: vi
        .fn()
        .mockRejectedValue(new Error("Go backend exited with code 1")),
    });
    const svc = new PlatformComplianceService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({ ok: false, kind: "offline", endpoint: ENDPOINT });
    if (!outcome.ok) expect(outcome.status).toBeUndefined();
  });

  it("not_configured (no compliance credential) has no status", async () => {
    const ipc = makeIpcClient({
      platformAuditListReports: vi
        .fn()
        .mockRejectedValue(new Error("IPC error -32000: compliance service unavailable")),
    });
    const svc = new PlatformComplianceService(ipc);

    const outcome = await svc.fetchAndCache();

    expect(outcome).toMatchObject({ ok: false, kind: "not_configured", endpoint: ENDPOINT });
  });

  it("does not throw on a rejected IPC call", async () => {
    const ipc = makeIpcClient({
      platformAuditListReports: vi.fn().mockRejectedValue(new Error("not configured")),
    });
    const svc = new PlatformComplianceService(ipc);

    await expect(svc.fetchAndCache()).resolves.toMatchObject({ ok: false });
  });
});

describe("PlatformComplianceService.generateReport", () => {
  it("calls platformAuditGenerateReport with correct params", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    await svc.generateReport("iso27001", "2026-01-01", "2026-03-31", "pdf");
    expect(ipc.platformAuditGenerateReport).toHaveBeenCalledWith(
      "iso27001",
      "2026-01-01",
      "2026-03-31",
      "pdf"
    );
  });

  it("returns report result with id and status", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    const result = await svc.generateReport("soc2", "2026-01-01", "2026-03-31", "pdf");
    expect(result.id).toBe("rpt-1");
    expect(result.status).toBe("pending");
  });
});

describe("PlatformComplianceService.getReport", () => {
  it("calls platformAuditGetReport with correct reportId", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    await svc.getReport("rpt-1");
    expect(ipc.platformAuditGetReport).toHaveBeenCalledWith("rpt-1");
  });

  it("returns detail with downloadUrl for ready report", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    const detail = await svc.getReport("rpt-1");
    expect(detail.status).toBe("ready");
    expect(detail.downloadUrl).toBeTruthy();
  });
});

describe("PlatformComplianceService.getCached", () => {
  it("returns null before first fetch", () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    expect(svc.getCached()).toBeNull();
  });

  it("returns cached value after fetch", async () => {
    const page = makeReportsPage({ hasMore: true });
    const ipc = makeIpcClient({
      platformAuditListReports: vi.fn().mockResolvedValue(page),
    });
    const svc = new PlatformComplianceService(ipc);
    await svc.fetchAndCache();
    expect(svc.getCached()).toEqual(page);
  });
});

describe("PlatformComplianceService.dispose", () => {
  it("clears cache on dispose", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    await svc.fetchAndCache();
    svc.dispose();
    expect(svc.getCached()).toBeNull();
  });
});
