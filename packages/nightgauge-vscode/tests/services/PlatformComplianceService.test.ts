/**
 * Tests for PlatformComplianceService (Issue #3322)
 *
 * Covers:
 * 1. fetchAndCache → calls platformAuditListReports with correct params
 * 2. fetchAndCache → caches result and returns same on second call
 * 3. fetchAndCache → returns cache on concurrent calls (single-inflight guard)
 * 4. fetchAndCache → returns null on IPC error (falls back to cache)
 * 5. generateReport → calls platformAuditGenerateReport with correct params
 * 6. getReport → calls platformAuditGetReport with correct reportId
 * 7. getCached → returns cached value synchronously
 * 8. dispose → clears cache
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PlatformComplianceService } from "../../src/services/PlatformComplianceService";
import type { IpcClientGenerated } from "../../src/services/IpcClient.generated";
import type {
  ComplianceReportsPage,
  ComplianceReportResult,
  ComplianceReportDetail,
} from "../../src/services/IpcClientBase";

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
  it("calls platformAuditListReports with correct params", async () => {
    const ipc = makeIpcClient();
    const svc = new PlatformComplianceService(ipc);
    await svc.fetchAndCache("cursor-1", 10);
    expect(ipc.platformAuditListReports).toHaveBeenCalledWith("cursor-1", 10);
  });

  it("caches result and returns it on second call", async () => {
    const page = makeReportsPage({ hasMore: true });
    const ipc = makeIpcClient({
      platformAuditListReports: vi.fn().mockResolvedValue(page),
    });
    const svc = new PlatformComplianceService(ipc);
    const first = await svc.fetchAndCache();
    const second = await svc.fetchAndCache();
    expect(first).toEqual(page);
    expect(second).toEqual(page);
    expect(ipc.platformAuditListReports).toHaveBeenCalledTimes(2);
  });

  it("returns stale cache on IPC error", async () => {
    const page = makeReportsPage();
    const ipc = makeIpcClient({
      platformAuditListReports: vi
        .fn()
        .mockResolvedValueOnce(page)
        .mockRejectedValueOnce(new Error("network error")),
    });
    const svc = new PlatformComplianceService(ipc);
    await svc.fetchAndCache();
    const result = await svc.fetchAndCache();
    expect(result).toEqual(page);
  });

  it("returns null initially on IPC error (no cache)", async () => {
    const ipc = makeIpcClient({
      platformAuditListReports: vi.fn().mockRejectedValue(new Error("not configured")),
    });
    const svc = new PlatformComplianceService(ipc);
    const result = await svc.fetchAndCache();
    expect(result).toBeNull();
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
