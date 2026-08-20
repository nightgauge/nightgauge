/**
 * PlatformComplianceService — Fetches and caches compliance report data via IPC.
 *
 * Single-inflight guard prevents duplicate concurrent list requests.
 *
 * @see Issue #3322 — Add Compliance Report Generation UI in Extension
 * @see Issue 743 - typed failures instead of a swallowed catch{}
 * @see PlatformRunsService — pattern reference
 */

import * as vscode from "vscode";
import type { IpcClientGenerated } from "./IpcClient.generated";
import type {
  ComplianceReportsPage,
  ComplianceReportResult,
  ComplianceReportDetail,
} from "./IpcClientBase";
import { Logger } from "../utils/logger";
import { platformOk, reportPlatformFailure, type PlatformResult } from "./platformResult";

const LIST_ENDPOINT = "platform.auditListReports";

export class PlatformComplianceService implements vscode.Disposable {
  private cache: ComplianceReportsPage | null = null;
  private inFlight = false;
  private readonly logger = new Logger("Nightgauge Platform: Compliance");

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch paginated compliance report list via IPC, cache, and return.
   * Single-inflight guard — concurrent calls return current cache wrapped as
   * a success (or a failure if there is nothing cached yet).
   * Returns a typed failure on error (401/403 → no access, etc.). Never throws.
   */
  async fetchAndCache(
    cursor?: string,
    limit?: number
  ): Promise<PlatformResult<ComplianceReportsPage>> {
    if (this.inFlight) {
      return this.cache !== null
        ? platformOk(this.cache)
        : reportPlatformFailure(this.logger, new Error("fetch already in progress"), LIST_ENDPOINT);
    }
    this.inFlight = true;
    try {
      const result = await this.ipcClient.platformAuditListReports(cursor, limit);
      this.cache = result;
      return platformOk(result);
    } catch (err) {
      return reportPlatformFailure(this.logger, err, LIST_ENDPOINT);
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
    this.logger.dispose();
  }
}
