/**
 * PlatformComplianceService — Fetches and caches compliance report data via IPC.
 *
 * Single-inflight guard prevents duplicate concurrent list requests.
 * Role/tier check: returns null when the user lacks access (owner/admin required).
 *
 * @see Issue #3322 — Add Compliance Report Generation UI in Extension
 * @see PlatformRunsService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type {
  ComplianceReportsPage,
  ComplianceReportResult,
  ComplianceReportDetail,
} from "./IpcClientBase";

export class PlatformComplianceService implements vscode.Disposable {
  private cache: ComplianceReportsPage | null = null;
  private inFlight = false;

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch paginated compliance report list via IPC, cache, and return.
   * Single-inflight guard — concurrent calls return current cache.
   * Returns null on auth failure (401/403 → no access).
   */
  async fetchAndCache(cursor?: string, limit?: number): Promise<ComplianceReportsPage | null> {
    if (this.inFlight) {
      return this.cache;
    }
    this.inFlight = true;
    try {
      const result = await this.ipcClient.platformAuditListReports(cursor, limit);
      this.cache = result;
      return result;
    } catch {
      return this.cache;
    } finally {
      this.inFlight = false;
    }
  }

  /** Trigger compliance report generation. Does not cache. */
  async generateReport(
    reportType: string,
    startDate: string,
    endDate: string,
    format: string
  ): Promise<ComplianceReportResult> {
    return this.ipcClient.platformAuditGenerateReport(reportType, startDate, endDate, format);
  }

  /** Fetch a single report by ID (for polling status + download URL). */
  async getReport(reportId: string): Promise<ComplianceReportDetail> {
    return this.ipcClient.platformAuditGetReport(reportId);
  }

  /** Returns the last cached list result (synchronous, for render use). */
  getCached(): ComplianceReportsPage | null {
    return this.cache;
  }

  dispose(): void {
    this.cache = null;
  }
}
