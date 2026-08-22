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
  ComplianceReportsResult,
  ComplianceReportResult,
  ComplianceReportDetail,
  ComplianceReportDownload,
} from "./IpcClientBase";
import { Logger } from "../utils/logger";
import { platformOk, reportPlatformFailure, type PlatformResult } from "./platformResult";

const LIST_ENDPOINT = "platform.auditListReports";

export class PlatformComplianceService implements vscode.Disposable {
  private cache: ComplianceReportsResult | null = null;
  private inFlight = false;
  private readonly logger = new Logger("Nightgauge Platform: Compliance");

  constructor(private readonly ipcClient: IpcClientGenerated) {}

  /**
   * Fetch the account's compliance report list via IPC, cache, and return.
   * Single-inflight guard — concurrent calls return current cache wrapped as
   * a success (or a failure if there is nothing cached yet).
   * Returns a typed failure on error (401/403 → no access, etc.). Never throws.
   *
   * Takes no cursor or limit: the endpoint has neither (#803).
   */
  async fetchAndCache(): Promise<PlatformResult<ComplianceReportsResult>> {
    if (this.inFlight) {
      return this.cache !== null
        ? platformOk(this.cache)
        : reportPlatformFailure(this.logger, new Error("fetch already in progress"), LIST_ENDPOINT);
    }
    this.inFlight = true;
    try {
      const result = await this.ipcClient.platformAuditListReports();
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

  /** Fetch a single report by ID (for polling generation status). */
  async getReport(reportId: string): Promise<ComplianceReportDetail> {
    return this.ipcClient.platformAuditGetReport(reportId);
  }

  /**
   * Resolve a report's artifact — a signed URL, the JSON payload inline, or
   * "still generating". The detail endpoint carries no download URL (#803).
   */
  async downloadReport(reportId: string): Promise<ComplianceReportDownload> {
    return this.ipcClient.platformAuditDownloadReport(reportId);
  }

  /** Returns the last cached list result (synchronous, for render use). */
  getCached(): ComplianceReportsResult | null {
    return this.cache;
  }

  dispose(): void {
    this.cache = null;
    this.logger.dispose();
  }
}
