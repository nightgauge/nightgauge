/**
 * LocalAuditFallbackService — Local telemetry fallback for the Audit Log tab.
 *
 * When the platform audit API is unreachable (non-401/403 failure), or when
 * `platform.enabled` is false and the platform is never asked, this service
 * reads `pipelineStateDir(root)/history/index.json` via TelemetryStore and maps
 * HistoryIndexEntry records to AuditLogEntry objects that the AuditTabHtml renderer
 * can display.
 *
 * Design constraints (ADR-003):
 * - No vscode imports — must be unit-testable without the extension host
 * - Never throws — all errors return an empty AuditLogData with isLocalFallback: true
 *
 * @see Issue #3324 — Local-first audit fallback when platform unreachable
 */

import { TelemetryStore, type HistoryIndexEntry } from "./TelemetryStore";
import type {
  AuditLogData,
  AuditFilterState,
  AuditLogEntry,
  AuditPaginationInfo,
} from "../views/dashboard/DashboardState";

const PAGE_SIZE = 50;

/**
 * Why local telemetry is shown instead of the platform's audit log:
 * `unreachable` — the platform was asked and failed, so Retry may help;
 * `disabled` — `platform.enabled` is false, so the platform is never asked
 * and there is nothing to retry.
 */
export type LocalAuditSource = "unreachable" | "disabled";

const LOCAL_DATA_LABELS: Record<LocalAuditSource, string> = {
  unreachable: "Showing local telemetry — platform unreachable",
  disabled: "Showing local telemetry — platform communication is off (platform.enabled: false)",
};

function localMode(
  source: LocalAuditSource
): Pick<AuditLogData, "isLocalFallback" | "localDataLabel" | "localRetryable"> {
  return {
    isLocalFallback: true,
    localDataLabel: LOCAL_DATA_LABELS[source],
    localRetryable: source === "unreachable",
  };
}

function emptyLocalData(filters: AuditFilterState, source: LocalAuditSource): AuditLogData {
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
    ...localMode(source),
  };
}

function mapOutcomeToAction(outcome: HistoryIndexEntry["outcome"]): string {
  switch (outcome) {
    case "complete":
      return "pipeline_run_completed";
    case "failed":
      return "pipeline_run_failed";
    case "cancelled":
      return "pipeline_run_cancelled";
    default:
      return "pipeline_run_completed";
  }
}

function mapOutcomeToStatus(
  outcome: HistoryIndexEntry["outcome"]
): "success" | "failure" | "pending" {
  switch (outcome) {
    case "complete":
      return "success";
    case "failed":
      return "failure";
    case "cancelled":
      return "pending";
    default:
      return "pending";
  }
}

function entryToAuditLogEntry(entry: HistoryIndexEntry): AuditLogEntry {
  return {
    id: `local-${entry.issue_number}-${entry.recorded_at}`,
    timestamp: entry.recorded_at,
    userId: "local",
    action: mapOutcomeToAction(entry.outcome),
    resourceType: "pipeline_run",
    resourceId: String(entry.issue_number),
    status: mapOutcomeToStatus(entry.outcome),
    metadata: {
      title: entry.title,
      cost_usd: entry.cost_usd,
      duration_ms: entry.duration_ms,
      branch: entry.branch,
    },
    costUsd: entry.cost_usd,
  };
}

function isInDateRange(timestamp: string, dateFrom: string, dateTo: string): boolean {
  const ts = timestamp ? new Date(timestamp).getTime() : NaN;
  if (isNaN(ts)) return false;
  const from = dateFrom ? new Date(dateFrom).getTime() : -Infinity;
  const to = dateTo ? new Date(dateTo).getTime() : Infinity;
  return ts >= from && ts <= to;
}

/**
 * Reads local pipeline history and returns it as AuditLogData.
 *
 * Used by AuditLogService as a fallback when the platform API is unreachable.
 * Must not import vscode — testable in pure Node.js environment.
 */
export class LocalAuditFallbackService {
  private readonly telemetryStore: TelemetryStore;

  constructor(workspaceRoot: string) {
    this.telemetryStore = new TelemetryStore(workspaceRoot);
  }

  /**
   * Build AuditLogData from local history index.
   * Applies date filters from the AuditFilterState and paginates in-memory.
   * `source` says why local data is shown; it sets the banner label and
   * whether Retry is offered.
   * Never throws — errors return empty AuditLogData with isLocalFallback: true.
   */
  async buildLocalAuditData(
    filters: AuditFilterState,
    page = 0,
    source: LocalAuditSource = "unreachable"
  ): Promise<AuditLogData> {
    let entries: AuditLogEntry[];
    try {
      // Every read here is a user action (tab open, filter, page, refresh), so
      // it reflects the history on disk now. The store caches its index for
      // its lifetime, which is this service's: without this, runs recorded
      // after the tab first loaded never appeared until a window reload.
      this.telemetryStore.invalidateCache();
      const summaries = await this.telemetryStore.getAllRunSummaries();
      const filtered = summaries.filter((entry) =>
        isInDateRange(entry.recorded_at, filters.dateFrom, filters.dateTo)
      );
      // Apply action filter if set — match against our mapped action strings
      const actionFiltered = filters.actionFilter
        ? filtered.filter((e) => mapOutcomeToAction(e.outcome) === filters.actionFilter)
        : filtered;

      entries = actionFiltered.map(entryToAuditLogEntry);
    } catch (err) {
      console.warn("[LocalAuditFallbackService] Failed to read local history index:", err);
      return emptyLocalData(filters, source);
    }

    const totalCount = entries.length;
    const start = page * PAGE_SIZE;
    const pageEntries = entries.slice(start, start + PAGE_SIZE);

    const pagination: AuditPaginationInfo = {
      page,
      pageSize: PAGE_SIZE,
      totalCount,
      hasNextPage: start + PAGE_SIZE < totalCount,
      hasPrevPage: page > 0,
    };

    return {
      entries: pageEntries,
      filters,
      pagination,
      isLoading: false,
      hasAccess: true,
      ...localMode(source),
    };
  }
}
