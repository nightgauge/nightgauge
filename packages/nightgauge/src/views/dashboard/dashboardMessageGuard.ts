/**
 * dashboardMessageGuard - runtime validation for messages the Dashboard
 * webview posts to the extension host.
 *
 * A webview message is untrusted input at a process boundary, so the host
 * checks every field a handler reads before it acts. Each message type has its
 * own validator; a message of an unknown type, or one whose fields fail its
 * validator, is dropped. `executeCommand` runs only the exact command ids the
 * Dashboard renders a button for.
 */

import type { WebViewMessage } from "./Dashboard";

/**
 * The commands the Dashboard webview may ask the host to run. Every entry is
 * a button the Dashboard renders; a command not listed here is never run.
 */
export const DASHBOARD_EXECUTABLE_COMMANDS: readonly string[] = Object.freeze([
  "nightgauge.exportTelemetry",
  "nightgauge.enableClaudeUsageStatusLine",
  "nightgauge.declareClaudePlan",
]);

type Fields = Record<string, unknown>;
type Validator = (m: Fields) => boolean;

const MAX_STRING = 4096;

const isStr = (v: unknown): v is string => typeof v === "string" && v.length <= MAX_STRING;
const isNonEmptyStr = (v: unknown): v is string => isStr(v) && v.length > 0;
const isOptStr = (v: unknown): boolean => v === undefined || isStr(v);
const isIndex = (v: unknown): boolean => Number.isInteger(v) && (v as number) >= 0;
const isPositiveInt = (v: unknown): boolean => Number.isInteger(v) && (v as number) > 0;
const oneOf =
  (...allowed: readonly string[]) =>
  (v: unknown): boolean =>
    typeof v === "string" && allowed.includes(v);
const none: Validator = () => true;

/** A run reference: an issue number plus an optional run id / start time. */
const isRunRef = (m: Fields): boolean =>
  isPositiveInt(m.issueNumber) && isOptStr(m.runId) && isOptStr(m.startedAt);

const isAuditFilters = (v: unknown): boolean => {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return false;
  const f = v as Fields;
  return [f.dateFrom, f.dateTo, f.actionFilter, f.userFilter].every(isStr);
};

const isFirewallValue = (v: unknown): boolean =>
  isStr(v) || (Array.isArray(v) && v.length <= 64 && v.every(isStr));

/**
 * One validator per message type. Typed as a full record over the union's
 * `type` values, so adding a message type without a validator fails to
 * compile.
 */
const VALIDATORS: Record<WebViewMessage["type"], Validator> = {
  refresh: none,
  export: (m) =>
    oneOf("json", "csv")(m.format) &&
    (m.target === "current" ||
      (typeof m.target === "object" && m.target !== null && isRunRef(m.target as Fields))),
  selectRun: isRunRef,
  loadRunDetails: isRunRef,
  setScope: (m) => oneOf("session", "all")(m.scope),
  firewallFilter: (m) =>
    oneOf("eventType", "category", "timeRange", "search")(m.filter) && isFirewallValue(m.value),
  firewallResetFilters: none,
  refreshProjectBoard: none,
  healthToggle: (m) => m.collapsed === undefined || typeof m.collapsed === "boolean",
  healthTrendRange: (m) => isStr(m.range),
  applyRecommendation: (m) => isNonEmptyStr(m.category) && isNonEmptyStr(m.configPath),
  revertRecommendation: (m) => isNonEmptyStr(m.category),
  scrollPosition: (m) => typeof m.scrollY === "number",
  loadMoreHistory: none,
  exportAnalytics: (m) =>
    oneOf("json", "csv-runs", "csv-stages")(m.format) &&
    oneOf("last7", "last30", "all")(m.dateRange),
  executeCommand: (m) => oneOf(...DASHBOARD_EXECUTABLE_COMMANDS)(m.command),
  resetUsageCounter: none,
  selectTab: (m) => isNonEmptyStr(m.tab),
  auditFilter: (m) => isAuditFilters(m.filters),
  auditPageChange: (m) => isIndex(m.page),
  auditExportCsv: (m) => isAuditFilters(m.filters),
  openSlotOutput: (m) => isIndex(m.slotIndex),
  auditRefresh: none,
  auditRetry: none,
  auditResetFilters: none,
  discoveryRefresh: none,
  costDateRangeChange: (m) => oneOf("7d", "30d", "90d")(m.range),
  healthRefresh: none,
  setModeFilter: (m) => oneOf("efficiency", "elevated", "maximum", "frontier", "all")(m.mode),
  runsPageChange: (m) => isIndex(m.page),
  runsExportCsv: none,
  runsRefresh: none,
  trendsDateRangeChange: (m) => oneOf("30d", "90d", "180d")(m.range),
  trendsRefresh: none,
  complianceGenerateReport: (m) =>
    isNonEmptyStr(m.reportType) &&
    isStr(m.startDate) &&
    isStr(m.endDate) &&
    isNonEmptyStr(m.format),
  complianceDownloadReport: (m) => isNonEmptyStr(m.reportId),
  complianceRefresh: none,
  retentionRefresh: none,
  retentionUpdate: (m) => isPositiveInt(m.retentionDays) && (m.retentionDays as number) <= 3650,
  retentionVerifyIntegrity: (m) => isPositiveInt(m.windowDays) && (m.windowDays as number) <= 3650,
  mergeDependabotPR: (m) => isNonEmptyStr(m.prNodeId) && isStr(m.owner) && isStr(m.repo),
  dependabotRefresh: none,
  openInBrowser: (m) => isOptStr(m.tab),
  signInWithPlatform: none,
};

/** Outcome of validating one raw webview message. */
export type DashboardMessageCheck =
  | { ok: true; message: WebViewMessage }
  | { ok: false; reason: "malformed" | "unknown-type" | "invalid-fields"; type?: string };

/** Validate a raw message posted by the Dashboard webview. */
export function checkDashboardMessage(raw: unknown): DashboardMessageCheck {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    return { ok: false, reason: "malformed" };
  }
  const type = (raw as Fields).type;
  if (typeof type !== "string") return { ok: false, reason: "malformed" };
  if (!Object.prototype.hasOwnProperty.call(VALIDATORS, type)) {
    return { ok: false, reason: "unknown-type", type: type.slice(0, 64) };
  }
  const validate = VALIDATORS[type as WebViewMessage["type"]];
  if (!validate(raw as Fields)) return { ok: false, reason: "invalid-fields", type };
  return { ok: true, message: raw as WebViewMessage };
}
