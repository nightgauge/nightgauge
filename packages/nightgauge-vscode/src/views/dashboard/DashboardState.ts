/**
 * DashboardState - State management and persistence for the dashboard
 *
 * Manages current pipeline run state, pipeline history, and tool call logs.
 * Persists history to workspace storage for cross-session access.
 * Includes ROI calculations, time savings estimates, and efficiency metrics.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { PipelineStage, TotalUsage, EpicEstimate } from "@nightgauge/sdk";
import type { RoutingPath, SkippableStage } from "../../utils/changeAnalyzer";
import type { CrossRepoEpicProgress } from "./EpicDashboard";
import type { FirewallFilterState, FirewallAggregates } from "./FirewallTypes";
import { DEFAULT_FIREWALL_FILTERS } from "./FirewallTypes";
import type { ProjectBoardData, ProjectBoardWidgetConfig } from "./ProjectBoardTypes";
import type { BacktrackRecord, ModelEscalationRecord } from "../../schemas/pipelineState";
import { DEFAULT_PROJECT_BOARD_CONFIG } from "./ProjectBoardTypes";
import type { TelemetryStore, HistoryIndexEntry } from "../../services/TelemetryStore";
import type { IssueCostAggregation } from "../../utils/executionHistoryReader";
import type { HealthWidgetData } from "./HealthWidgetTypes";
import type { CostSummary, CostHistoryEntry } from "./CostSummaryCalculator";
import type { PipelineCostEstimate, AutoModelSelectorConfig } from "@nightgauge/sdk";
import type { HealthCheckReport } from "../../types/pipelineHealth";

/**
 * Time savings configuration (user-configurable via VS Code settings)
 * Values represent estimated manual time in minutes for each stage
 *
 * Bookend stages (pipelineStart, pipelineFinish) are orchestration overhead
 * and do not represent human time saved.
 */
export interface TimeSavingsConfig {
  pipelineStart: number; // default: 0 (orchestration overhead)
  issuePickup: number; // default: 5 minutes
  featurePlanning: number; // default: 30 minutes
  featureDev: number; // default: 120 minutes
  featureValidate: number; // default: 15 minutes
  prCreate: number; // default: 10 minutes
  prMerge: number; // default: 5 minutes
  pipelineFinish: number; // default: 0 (orchestration overhead)
}

/**
 * Default time savings configuration
 */
export const DEFAULT_TIME_SAVINGS_CONFIG: TimeSavingsConfig = {
  pipelineStart: 0,
  issuePickup: 5,
  featurePlanning: 30,
  featureDev: 120,
  featureValidate: 15,
  prCreate: 10,
  prMerge: 5,
  pipelineFinish: 0,
};

/**
 * Efficiency metrics per run
 */
export interface EfficiencyMetrics {
  tokensPerMinute: number;
  costPerMinute: number;
  cacheHitRate: number; // 0-1 percentage
  avgStageDurationMs: number;
}

/**
 * Per-stage model routing metrics for dashboard widget (Issue #734)
 */
export interface ModelRoutingStageMetric {
  stage: string;
  totalRuns: number;
  successRate: number;
  totalCostUsd: number;
}

/**
 * Model routing metrics summary for dashboard widget (Issue #734)
 */
export interface ModelRoutingMetrics {
  totalAutoSelectedRuns: number;
  overallSuccessRate: number;
  totalCostUsd: number;
  perStage: ModelRoutingStageMetric[];
  confidenceDistribution: { low: number; medium: number; high: number };
  modelUsage: Record<string, number>;
}

/**
 * Wraps epic data for display — may be fully estimated or partially failed
 *
 * When estimation succeeds, `estimate` contains the full EpicEstimate.
 * When estimation fails (e.g., no sub-issues), `estimate` is null and
 * `warning` contains a user-friendly message.
 *
 * @see Issue #987 - Epic Detection Fails Silently
 */
export interface EpicDisplayEntry {
  epic_number: number;
  epic_title: string;
  /** Full estimate when estimation succeeded, null when it failed */
  estimate: EpicEstimate | null;
  /** Warning message when estimation failed (e.g., "no sub-issues") */
  warning: string | null;
}

/**
 * Per-stage average metrics across historical runs (Issue #1008)
 */
export interface StageAverageMetrics {
  stage: string;
  avgCostUsd: number;
  avgInputTokens: number;
  avgOutputTokens: number;
  avgCacheReadTokens: number;
  avgCacheCreationTokens: number;
  avgDurationMs: number;
  runCount: number;
  primaryModel: string | null;
}

/**
 * Outlier detection result for a single stage in a run (Issue #1008)
 */
export interface StageOutlier {
  stage: string;
  metric: "cost" | "duration";
  value: number;
  avg: number;
  ratio: number;
}

/**
 * Aggregated stats for summary cards
 */
/**
 * Recent-vs-prior 7-day deltas for the headline stat cards.
 *
 * The all-time totals (Pipeline Runs, Time Saved, Total Cost, Success Rate)
 * grow monotonically and lose their signal value once a workspace has a few
 * hundred runs. These deltas restore "is this week going well?" semantics by
 * comparing the trailing 7 days to the 7 days before that.
 *
 * Each `prior` value is the comparable baseline used to compute the delta —
 * the renderer can use it to label the comparison ("vs prior 7 days").
 *
 * `successRatePointsDelta` is in percentage *points* (e.g. +5 means the rate
 * climbed from 70% to 75%), not relative percent — relative deltas around a
 * baseline near 0 explode and confuse readers.
 *
 * `hasEnoughData` is false when there are zero runs in the recent window
 * (no signal to compute). The renderer should suppress delta UI entirely in
 * that case — showing "+0 this week" on a fresh install is misleading.
 */
export interface RecentActivityDelta {
  runsDelta: number;
  runsPrior: number;
  timeSavedDeltaMs: number;
  timeSavedPriorMs: number;
  costDeltaUsd: number;
  costPriorUsd: number;
  successRatePointsDelta: number;
  successRateRecent: number; // 0-1, recent-7d success rate (no successes-with-zero-runs)
  successRatePrior: number; // 0-1, prior-7d success rate
  hasEnoughData: boolean;
  /** Window size in days — encoded into the renderer so label stays in sync. */
  windowDays: number;
}

export interface DashboardAggregates {
  totalRuns: number;
  sessionRuns: number;
  totalTimeSavedMs: number;
  sessionTimeSavedMs: number;
  totalCostUsd: number;
  sessionCostUsd: number;
  successRate: number; // 0-1 percentage
  avgCostPerRun: number;
  avgTimeSavedPerRun: number;
  totalTokens: number;
  sessionTokens: number;
  epicEstimates: EpicDisplayEntry[]; // Open epics with time estimates (may include failed)
  /** Cross-repo epic progress (Issue #330) */
  crossRepoEpicProgress: CrossRepoEpicProgress[];
  /** Firewall aggregates (Issue #387) */
  firewallAggregates: FirewallAggregates | null;
  /** Per-stage averages across historical runs (Issue #1008) */
  stageAverages: StageAverageMetrics[];
  /** Cost aggregated per issue across all runs (last 20 by activity) — Issue #1410 */
  costPerIssue: IssueCostAggregation[];
  /** Trailing-7-day vs prior-7-day deltas for the headline cards */
  recentDelta: RecentActivityDelta;
}

/**
 * Adapter status data for dashboard widget (Issue #1056)
 */
export interface AdapterStatusData {
  adapter: string;
  displayName: string;
  authMethod?: string;
  authConfigured?: boolean;
  model?: string;
}

/**
 * Pagination info for pipeline history
 */
export interface HistoryPaginationInfo {
  totalCount: number;
  hasMore: boolean;
}

/**
 * PTC (Programmatic Tool Calls) metrics display data (Issue #1071)
 */
export interface PTCMetricsDisplayData {
  totalToolCalls: number;
  programmaticCalls: number;
  directCalls: number;
  programmaticRatio: number;
  estimatedTokensSaved: number;
  codeExecutionCount: number;
  containerReuseCount: number;
}

/**
 * Firewall dashboard data (Issue #387)
 */
export interface FirewallDashboardData {
  events: import("./FirewallTypes").SanitizationEvent[];
  filters: FirewallFilterState;
  aggregates: FirewallAggregates;
  timeSeriesData: import("./FirewallTypes").FirewallTimeSeriesPoint[];
  granularity: import("./FirewallTypes").TimeSeriesGranularity;
  suggestions?: import("./FirewallTypes").AllowlistSuggestion[];
}

/**
 * Usage limits data for budget tracking (Issue #1333)
 */
export interface UsageLimitsData {
  costUsd: number;
  budgetUsd: number;
  usagePct: number;
}

/** A single platform quota metric (used vs. limit) */
export interface PlatformQuotaMetric {
  used: number;
  /** null = unlimited (community or no cap) */
  limit: number | null;
  /** null when limit is null */
  pct: number | null;
}

/**
 * Platform-level quota data fetched from GET /v1/analytics/usage.
 * Replaces/augments the UsageSummary with display-ready fields.
 * @see Issue #1479 - Add usage metering and quota display
 */
export interface PlatformQuotaData {
  pipelineRuns: PlatformQuotaMetric;
  tokens: PlatformQuotaMetric;
  /** ISO 8601 period start/end; null when no rows returned */
  period: { start: string; end: string } | null;
  /** true when both pipelineRunsPerMonth and pipelineRunsPerDay limits are null */
  isCommunity: boolean;
  /** ISO 8601 timestamp of the last successful fetch */
  lastFetchedAt: string;
  /** true when serving cached data due to network error */
  isStale: boolean;
}

// ---------------------------------------------------------------------------
// Audit Log Viewer types (Issue #1583)
// ---------------------------------------------------------------------------

/** A single audit event returned from the platform's audit query API. */
export interface AuditLogEntry {
  id: string;
  /** ISO 8601 timestamp */
  timestamp: string;
  userId: string;
  userEmail?: string;
  /** Matches AUDIT_ACTIONS from @nightgauge/sdk */
  action: string;
  resourceType?: string;
  resourceId?: string;
  status: "success" | "failure" | "pending";
  metadata?: Record<string, unknown>;
  costUsd?: number;
}

/** Filter state for the audit log viewer. */
export interface AuditFilterState {
  /** ISO 8601, default: 7 days ago */
  dateFrom: string;
  /** ISO 8601, default: now */
  dateTo: string;
  /** '' = all actions */
  actionFilter: string;
  /** '' = all users */
  userFilter: string;
}

/** Pagination info for the audit log viewer. */
export interface AuditPaginationInfo {
  page: number;
  /** default 50 */
  pageSize: number;
  totalCount: number;
  hasNextPage: boolean;
  hasPrevPage: boolean;
}

/** Discriminated error type for platform-connected dashboard tabs (#3679). */
export type PlatformErrorType =
  | "not_signed_in" // TokenStorage has no accessToken
  | "token_expired" // expiresAt is in the past
  | "no_permission" // 401/403 from platform
  | "ipc_unavailable" // "Go backend not connected"
  | "ipc_timeout" // "IPC request ... timed out"
  | "server_error" // 5xx or service returned null with valid token
  | "unknown"; // catch-all

/** Data bundle for the Health tab (replaces bare AnalyticsHealthResult | null) (#3679). */
export interface AnalyticsHealthData {
  result: import("../../services/IpcClientBase").AnalyticsHealthResult | null;
  hasAccess: boolean;
  isLoading: boolean;
  errorType?: PlatformErrorType;
  errorMessage?: string;
}

/** Data bundle passed to AuditTabHtml renderer. */
export interface AuditLogData {
  entries: AuditLogEntry[];
  filters: AuditFilterState;
  pagination: AuditPaginationInfo;
  isLoading: boolean;
  errorMessage?: string;
  /** Structured error type for contextual error UI (#3679). */
  errorType?: PlatformErrorType;
  /** false = hide tab entirely (user has no access) */
  hasAccess: boolean;
  /** true when serving local telemetry because the platform API is unreachable (Issue #3324) */
  isLocalFallback?: boolean;
  /** Human-readable label shown in the local-mode banner (Issue #3324) */
  localDataLabel?: string;
  /** ISO 8601 timestamp of the last successful platform fetch, if known (Issue #3324) */
  lastPlatformSync?: string;
}

/** Filter state for the Runs tab (#3319). */
export interface RunsFilterState {
  dateFrom: string;
  dateTo: string;
  outcomeFilter: string;
  branchFilter: string;
}

/** Pagination info for the Runs tab using cursor-stack translation (#3319). */
export interface RunsPaginationInfo {
  page: number;
  pageSize: number;
  totalCount: number;
  hasMore: boolean;
  /** cursorStack[i] holds the cursor for page i; page 0 is always undefined. */
  cursorStack: (string | undefined)[];
}

/** Default filter state for the Runs tab. */
export function getDefaultRunsFilters(): RunsFilterState {
  return { dateFrom: "", dateTo: "", outcomeFilter: "", branchFilter: "" };
}

/** Default pagination state for the Runs tab. */
export function getDefaultRunsPagination(): RunsPaginationInfo {
  return { page: 0, pageSize: 20, totalCount: 0, hasMore: false, cursorStack: [undefined] };
}

/** Data bundle passed to RunsTabHtml renderer (#3319). */
export interface RunsListData {
  entries: import("../../services/IpcClientBase").RunsEntry[];
  filters: RunsFilterState;
  pagination: RunsPaginationInfo;
  isLoading: boolean;
  hasAccess: boolean;
  errorMessage?: string;
  /** Structured error type for contextual error UI (#3679). */
  errorType?: PlatformErrorType;
}

/** Date range selection for the Trends tab (#3320). */
export type TrendsDateRange = "30d" | "90d" | "180d";

/** Data bundle passed to TrendsTabHtml renderer (#3320). */
export interface TrendsData {
  result: import("../../services/IpcClientBase").AnalyticsTrendsResult | null;
  isLoading: boolean;
  hasAccess: boolean;
  showComparison: boolean;
  errorMessage?: string;
  /** Structured error type for contextual error UI (#3679). */
  errorType?: PlatformErrorType;
}

/** Filter state for compliance report list (#3322). */
export interface ComplianceFilterState {
  reportType?: string;
  startDate?: string;
  endDate?: string;
}

/** Re-export from IpcClientBase for use in UI components (#3322). */
export type { ComplianceReportEntry } from "../../services/IpcClientBase";

/** Data bundle passed to the Retention & Integrity panel in AuditTabHtml (#3323). */
export interface RetentionIntegrityData {
  retentionConfig: import("../../services/IpcClientBase").RetentionConfig | null;
  integrityResult: import("../../services/IpcClientBase").IntegrityResult | null;
  isLoading: boolean;
  isVerifying: boolean;
  /** false when license tier is below enterprise */
  hasAccess: boolean;
  errorMessage?: string;
  /** Structured error type for contextual error UI (#3679). */
  errorType?: PlatformErrorType;
}

/** Data bundle passed to ComplianceTabHtml renderer (#3322). */
export interface ComplianceData {
  reports: import("../../services/IpcClientBase").ComplianceReportEntry[];
  filters: ComplianceFilterState;
  pagination: { cursor?: string; nextCursor?: string; hasMore: boolean };
  isLoading: boolean;
  hasAccess: boolean;
  isGenerating: boolean;
  errorMessage?: string;
  /** Structured error type for contextual error UI (#3679). */
  errorType?: PlatformErrorType;
}

/**
 * Consolidated render state passed to tab renderers.
 * Bundles all parameters of getDashboardHtml() for tab-level composition.
 */
export interface FullDashboardRenderState {
  webview: vscode.Webview;
  currentRun: PipelineRunSummary | null;
  history: PipelineRunSummary[];
  aggregates: DashboardAggregates;
  timeSavingsConfig: TimeSavingsConfig;
  scope: "session" | "all";
  firewallData?: FirewallDashboardData;
  projectBoardData?: ProjectBoardData | null;
  healthWidgetData?: HealthWidgetData | null;
  modelRoutingMetrics?: ModelRoutingMetrics | null;
  appliedCategories?: string[];
  costSummary?: CostSummary | null;
  costHistory?: CostHistoryEntry[];
  costEstimate?: PipelineCostEstimate | null;
  historyPagination?: HistoryPaginationInfo;
  ptcMetrics?: PTCMetricsDisplayData | null;
  adapterStatusData?: AdapterStatusData | null;
  healthCheckReport?: HealthCheckReport | null;
  backtracks?: BacktrackRecord[];
  modelEscalations?: ModelEscalationRecord[];
  usageLimitsData?: UsageLimitsData | null;
  platformQuotaData?: PlatformQuotaData | null;
  nonce: string;
  renderTs: number;
}

/**
 * Status of a pipeline run
 */
export type PipelineRunStatus = "running" | "complete" | "failed" | "cancelled";

/**
 * Status of a pipeline stage within a run
 */
export type StageRunStatus = "pending" | "running" | "complete" | "failed" | "skipped" | "deferred";

/**
 * Token usage for a single stage
 */
export interface StageTokenUsage {
  stage: PipelineStage;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  timestamp: Date;
  /** Model used for this stage (e.g., 'haiku', 'sonnet', 'opus') - Issue #945 */
  model?: string;
  /** Per-stage cache hit rate [0, 1]. Absent when no tokens used. (Issue #2459) */
  cacheHitRate?: number;
}

/**
 * Entry for a tool call in the log
 */
export interface ToolCallEntry {
  tool: string;
  target: string;
  timestamp: Date;
  durationMs?: number;
  args?: Record<string, unknown>;
  result?: string;
  error?: string;
}

/**
 * Stage progress within a pipeline run
 */
export interface StageProgress {
  stage: PipelineStage;
  status: StageRunStatus;
  startedAt?: Date;
  completedAt?: Date;
  durationMs?: number;
  tokenUsage?: StageTokenUsage;
  /**
   * Performance mode active when this stage executed (Issue #3218).
   * Sourced from `HistoryStageDetail.performance_mode` in JSONL records.
   * Absent on pre-#3215 records — readers MUST treat as mode-unknown.
   */
  performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
  /**
   * Execution path for this stage (Issue #3264 / #3269).
   * `"deterministic"` — completed by Go-side code; token cost ≈ $0.
   * `"llm"` — ran via the LLM skill path.
   * Absent on pre-#3264 records — treat as `"unknown"`.
   */
  execution_path?: "deterministic" | "llm";
}

/**
 * Routing information for complexity-based stage routing
 */
export interface PipelineRunRouting {
  /** The route taken (trivial, standard, extensive) */
  route: RoutingPath;
  /** Stages that were skipped due to routing */
  skippedStages: SkippableStage[];
  /** Whether the route was overridden by config or flag */
  wasOverridden: boolean;
  /** Original route (if overridden) */
  originalRoute?: RoutingPath;
  /** Estimated time from routing decision */
  estimatedTimeMinutes: number;
}

/**
 * Live phase-within-stage info for the Overview activity widget.
 */
export interface CurrentPhaseInfo {
  stage: PipelineStage;
  phase: string;
  index: number;
  total: number;
  startedAt: Date;
}

/**
 * Summary of a pipeline run for history display
 */
export interface PipelineRunSummary {
  issueNumber: number;
  title: string;
  branch: string;
  startedAt: Date;
  completedAt?: Date;
  status: PipelineRunStatus;
  stages: StageProgress[];
  currentStage?: PipelineStage;
  usage: TotalUsage;
  toolCalls: ToolCallEntry[];
  /** Estimated manual time in ms (calculated from time savings config) */
  manualEstimateMs?: number;
  /** Time saved in ms (manualEstimate - actualDuration) */
  timeSavedMs?: number;
  /** Efficiency metrics for this run */
  efficiency?: EfficiencyMetrics;
  /** Routing information for complexity-based stage routing (Issue #216) */
  routing?: PipelineRunRouting;
  /** True when this run resumed a previously-failed pipeline (Issue #1261) */
  is_recovery?: boolean;
  /** True when this run used supercharge mode — Opus + max effort (Issue #2433) */
  is_supercharge?: boolean;
  /** Issue type from GitHub labels (e.g., 'feature', 'bug') — null if not labeled (Issue #2546) */
  issueType?: string | null;
  /** Size label from GitHub labels (e.g., 'S', 'M', 'L', 'XL') — null if not labeled (Issue #2546) */
  sizeLabel?: string | null;
  /**
   * Run-level performance mode at run start (Issue #3218).
   * Sourced from history index `performance_mode` field. Absent on pre-#3215
   * records.
   */
  performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
}

/**
 * Serializable version of PipelineRunSummary for storage
 */
interface SerializedPipelineRun {
  issueNumber: number;
  title: string;
  branch: string;
  startedAt: string;
  completedAt?: string;
  status: PipelineRunStatus;
  stages: {
    stage: PipelineStage;
    status: StageRunStatus;
    startedAt?: string;
    completedAt?: string;
    durationMs?: number;
    tokenUsage?: {
      stage: PipelineStage;
      inputTokens: number;
      outputTokens: number;
      cacheReadTokens: number;
      cacheCreationTokens: number;
      costUsd: number;
      timestamp: string;
    };
    performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
  }[];
  currentStage?: PipelineStage;
  usage: TotalUsage;
  toolCalls: {
    tool: string;
    target: string;
    timestamp: string;
    durationMs?: number;
    args?: Record<string, unknown>;
    result?: string;
    error?: string;
  }[];
  manualEstimateMs?: number;
  timeSavedMs?: number;
  efficiency?: EfficiencyMetrics;
  routing?: PipelineRunRouting;
  /** True when this run resumed a previously-failed pipeline (Issue #1261) */
  is_recovery?: boolean;
  /** True when this run used supercharge mode — Opus + max effort (Issue #2433) */
  is_supercharge?: boolean;
  /** Issue type from GitHub labels (Issue #2546) */
  issueType?: string | null;
  /** Size label from GitHub labels (Issue #2546) */
  sizeLabel?: string | null;
  /** Performance mode (Issue #3218). */
  performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
}

/**
 * All pipeline stages in order
 */
export const ALL_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

/**
 * Storage key for dashboard history
 */
const HISTORY_STORAGE_KEY = "nightgauge.dashboard.history";

/**
 * Storage key for applied recommendation categories (Issue #787)
 */
const APPLIED_RECOMMENDATIONS_KEY = "nightgauge.dashboard.appliedRecommendations";

/**
 * Storage key for dismissed allowlist suggestions (Issue #786)
 */
const DISMISSED_SUGGESTIONS_KEY = "nightgauge.dashboard.dismissedSuggestions";

/**
 * Storage key for session start time (persists across VSCode restarts)
 * V2 uses calendar day boundaries instead of timeout-based sessions
 */
const SESSION_START_KEY_V2 = "nightgauge.dashboard.sessionStart.v2";

/**
 * Legacy storage key for session start time (timeout-based)
 * Used for migration from v1 to v2
 */
const SESSION_START_KEY = "nightgauge.dashboard.sessionStart";

/**
 * Session timeout: 30 minutes (LEGACY - only used for migration)
 * V2 uses calendar day boundaries instead of timeout-based sessions
 */
const SESSION_TIMEOUT_MS = 30 * 60 * 1000;

/**
 * Default maximum number of runs to keep in history.
 * Can be overridden via `nightgauge.dashboard.history.limit` setting.
 *
 * @see Issue #983 - Configurable history limit
 */
const DEFAULT_HISTORY_LIMIT = 50;

/**
 * Number of most-recent runs to eagerly hydrate with per-stage token data
 * and durations on dashboard open. Older runs stay as index summaries.
 *
 * Hydrating every historical run was a 500 MB+ memory hit (each JSONL
 * record is 20–100 KB when parsed). The window is chosen so per-stage
 * averages reflect recent behavior — historical runs beyond the window
 * rarely change the average meaningfully and can always be hydrated
 * on-demand when the user expands them.
 */
const EAGER_HYDRATION_LIMIT = 20;

/**
 * Map pipeline stage to time savings config key
 * Includes bookend stages (pipeline-start, pipeline-finish)
 */
const STAGE_TO_CONFIG_KEY: Record<PipelineStage, keyof TimeSavingsConfig> = {
  "pipeline-start": "pipelineStart",
  "issue-pickup": "issuePickup",
  "feature-planning": "featurePlanning",
  "feature-dev": "featureDev",
  "feature-validate": "featureValidate",
  "pr-create": "prCreate",
  "pr-merge": "prMerge",
  "pipeline-finish": "pipelineFinish",
};

/**
 * DashboardState class for managing dashboard data
 *
 * @example
 * ```typescript
 * const state = new DashboardState(context.workspaceState);
 *
 * // Start a new run
 * state.startRun(42, 'Add dashboard', 'feat/42-dashboard');
 *
 * // Update stage progress
 * state.setStageRunning('feature-planning');
 *
 * // Record token usage (a per-stage StageTokenUsage snapshot)
 * state.recordTokenUsage(stageTokenUsage);
 *
 * // Complete the run
 * state.completeRun();
 * ```
 */
export class DashboardState {
  private currentRun: PipelineRunSummary | null = null;
  private history: PipelineRunSummary[] = [];
  private workspaceState: vscode.Memento | null = null;
  private sessionStartTime: Date;
  private timeSavingsConfig: TimeSavingsConfig;
  private epicEstimates: EpicDisplayEntry[] = [];
  private crossRepoEpicProgress: CrossRepoEpicProgress[] = [];
  private workspaceRoot: string | undefined;
  /** Firewall filter state (Issue #387) */
  private firewallFilters: FirewallFilterState = {
    ...DEFAULT_FIREWALL_FILTERS,
  };
  /** Firewall aggregates (Issue #387) - set by Dashboard from service */
  private firewallAggregates: FirewallAggregates | null = null;
  /** Project board data (Issue #134) - set by Dashboard from service */
  private projectBoardData: ProjectBoardData | null = null;
  /** Project board widget configuration (Issue #134) */
  private projectBoardConfig: ProjectBoardWidgetConfig = {
    ...DEFAULT_PROJECT_BOARD_CONFIG,
  };
  /** Timestamp of last project board data refresh */
  private projectBoardLastRefresh: Date | null = null;
  /** Timestamp of last dashboard data refresh (Issue #614) */
  private _lastRefreshedAt: Date = new Date();
  /** Configurable history storage limit (Issue #983) */
  private historyLimit: number = DEFAULT_HISTORY_LIMIT;
  /** TelemetryStore for JSONL-based history (Issue #1007) */
  private telemetryStore: TelemetryStore | null = null;
  /** Backtrack events for the current pipeline run (Issue #1349) */
  private _currentBacktracks: BacktrackRecord[] = [];
  /** Model escalation events for the current pipeline run (Issue #1349) */
  private _currentModelEscalations: ModelEscalationRecord[] = [];
  /** Currently running phase within the active stage (live activity widget) */
  private _currentPhase: CurrentPhaseInfo | null = null;

  constructor(
    workspaceState?: vscode.Memento,
    workspaceRoot?: string,
    telemetryStore?: TelemetryStore
  ) {
    this.sessionStartTime = new Date();
    this.timeSavingsConfig = { ...DEFAULT_TIME_SAVINGS_CONFIG };
    this.workspaceRoot = workspaceRoot;
    this.telemetryStore = telemetryStore ?? null;

    if (workspaceState) {
      this.workspaceState = workspaceState;
      this.loadHistoryConfig();
      this.loadHistory();
      this.loadTimeSavingsConfig();
      this.loadFirewallFilters();
      this.loadProjectBoardConfig();
      this.initializeSession();
    }
  }

  /**
   * Initialize or restore session from persistent storage
   *
   * V2 uses calendar day boundaries instead of timeout-based sessions.
   * If VSCode restarts within the same calendar day, we restore the session.
   * Otherwise, session starts at midnight of the current day.
   *
   * Migration: Detects old timeout-based storage (SESSION_START_KEY) and
   * migrates to new calendar-day storage (SESSION_START_KEY_V2) if within
   * current calendar day.
   */
  private initializeSession(): void {
    if (!this.workspaceState) {
      // No workspace state: use in-memory session starting at midnight
      this.sessionStartTime = this.getSessionStartOfDay();
      return;
    }

    // Try V2 storage first (calendar day boundaries)
    const storedSessionStartV2 = this.workspaceState.get<string>(SESSION_START_KEY_V2);

    if (storedSessionStartV2) {
      try {
        const storedTime = new Date(storedSessionStartV2);

        // Validate it's a real date
        if (isNaN(storedTime.getTime())) {
          throw new Error("Invalid date in storage");
        }

        // If within current calendar day, restore session
        if (this.isWithinCurrentDay(storedTime)) {
          this.sessionStartTime = storedTime;
          return;
        }
      } catch (error) {
        console.warn("Failed to parse stored session start (V2), starting new session:", error);
      }
    }

    // Try legacy V1 storage (timeout-based) for migration
    const storedSessionStartV1 = this.workspaceState.get<string>(SESSION_START_KEY);

    if (storedSessionStartV1 && !storedSessionStartV2) {
      try {
        const storedTime = new Date(storedSessionStartV1);

        if (!isNaN(storedTime.getTime()) && this.isWithinCurrentDay(storedTime)) {
          // Migrate to V2 storage
          this.sessionStartTime = storedTime;
          this.workspaceState.update(SESSION_START_KEY_V2, this.sessionStartTime.toISOString());
          return;
        }
      } catch (error) {
        console.warn("Failed to migrate legacy session storage:", error);
      }
    }

    // No valid stored session: start new session at midnight of current day
    this.sessionStartTime = this.getSessionStartOfDay();
    this.workspaceState.update(SESSION_START_KEY_V2, this.sessionStartTime.toISOString());
  }

  /**
   * Get the start of the current calendar day (midnight in local timezone)
   *
   * @returns Date object set to midnight of current day
   */
  private getSessionStartOfDay(): Date {
    const now = new Date();
    return new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0, 0);
  }

  /**
   * Check if a date is within the current calendar day
   *
   * @param date The date to check
   * @returns true if date is within current calendar day, false otherwise
   */
  private isWithinCurrentDay(date: Date): boolean {
    const now = new Date();
    const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0, 0);
    const startOfTomorrow = new Date(
      now.getFullYear(),
      now.getMonth(),
      now.getDate() + 1,
      0,
      0,
      0,
      0
    );

    return date >= startOfToday && date < startOfTomorrow;
  }

  /**
   * Force a new session (e.g., when user explicitly resets)
   *
   * Resets session to midnight of current day and persists to V2 storage.
   */
  async resetSession(): Promise<void> {
    this.sessionStartTime = this.getSessionStartOfDay();
    if (this.workspaceState) {
      await this.workspaceState.update(SESSION_START_KEY_V2, this.sessionStartTime.toISOString());
    }
  }

  /**
   * Load time savings configuration from VS Code settings
   */
  private loadTimeSavingsConfig(): void {
    const config = vscode.workspace.getConfiguration("nightgauge.dashboard");
    this.timeSavingsConfig = {
      pipelineStart: 0, // Bookend stage - no time savings
      issuePickup: config.get<number>("timeSavings.issuePickup", 5),
      featurePlanning: config.get<number>("timeSavings.featurePlanning", 30),
      featureDev: config.get<number>("timeSavings.featureDev", 120),
      featureValidate: config.get<number>("timeSavings.featureValidate", 15),
      prCreate: config.get<number>("timeSavings.prCreate", 10),
      prMerge: config.get<number>("timeSavings.prMerge", 5),
      pipelineFinish: 0, // Bookend stage - no time savings
    };
  }

  /**
   * Get the current time savings configuration
   */
  getTimeSavingsConfig(): TimeSavingsConfig {
    return { ...this.timeSavingsConfig };
  }

  /**
   * Update time savings configuration
   */
  setTimeSavingsConfig(config: Partial<TimeSavingsConfig>): void {
    this.timeSavingsConfig = { ...this.timeSavingsConfig, ...config };
  }

  /**
   * Load history configuration from VS Code settings (Issue #983)
   */
  private loadHistoryConfig(): void {
    const config = vscode.workspace.getConfiguration("nightgauge.dashboard");
    const limit = config.get<number>("history.limit", DEFAULT_HISTORY_LIMIT);
    // Validate limit is one of the allowed values
    if (limit === 50 || limit === 100 || limit === 200) {
      this.historyLimit = limit;
    } else {
      this.historyLimit = DEFAULT_HISTORY_LIMIT;
    }
  }

  /**
   * Get the configured history limit (Issue #983)
   */
  getHistoryLimit(): number {
    return this.historyLimit;
  }

  /**
   * Get the configured page size for history display (Issue #983)
   */
  getHistoryPageSize(): number {
    const config = vscode.workspace.getConfiguration("nightgauge.dashboard");
    return config.get<number>("history.page_size", 20);
  }

  /**
   * Get a paginated slice of history (Issue #983)
   *
   * @param offset - Start index
   * @param limit - Number of items to return
   * @returns Paginated result with items, total count, and hasMore flag
   */
  getHistoryPage(
    offset: number,
    limit: number
  ): { items: PipelineRunSummary[]; total: number; hasMore: boolean } {
    const total = this.history.length;
    const items = this.history.slice(offset, offset + limit);
    return { items, total, hasMore: offset + limit < total };
  }

  /**
   * Get the session start time
   */
  getSessionStartTime(): Date {
    return this.sessionStartTime;
  }

  /**
   * Load history from workspace storage
   */
  private loadHistory(): void {
    if (!this.workspaceState) return;

    const serialized = this.workspaceState.get<SerializedPipelineRun[]>(HISTORY_STORAGE_KEY, []);

    this.history = serialized.map((run) => this.deserializeRun(run));
  }

  /**
   * Load history from TelemetryStore index (Issue #1007).
   *
   * Replaces Memento as the primary data source. Creates PipelineRunSummary
   * objects from lightweight index entries. Full JSONL details are loaded
   * on-demand when a user expands a specific run.
   *
   * After loading, writes through to Memento as a cache for fast startup.
   *
   * @returns Number of runs loaded
   */
  async loadFromTelemetryStore(): Promise<number> {
    if (!this.telemetryStore) return 0;

    try {
      // Invalidate cached index so we re-read from disk and pick up
      // any runs recorded since the last load (fixes stale metrics).
      this.telemetryStore.invalidateCache();
      const entries = await this.telemetryStore.getAllRunSummaries();
      const runs = entries.map((entry) => this.indexEntryToRunSummary(entry));

      // Hydrate only the most recent runs — older ones stay as lightweight
      // index summaries until the user expands them.
      //
      // Previously we hydrated every historical run on dashboard open. Each
      // hydration reads the full JSONL record (per-stage tokens, durations,
      // tool calls) into memory, and at ~50 KB/run × hundreds of historical
      // runs this put hundreds of MB of permanent resident state in the
      // extension host — all to populate averages that are already more
      // meaningful over a recent window anyway.
      //
      // getPerStageAverages() gracefully skips stages without tokenUsage or
      // durationMs, so unhydrated runs simply don't contribute to the
      // average. handleLoadRunDetails() hydrates on expansion. (Issue #2577)
      const recent = runs.slice(0, EAGER_HYDRATION_LIMIT);
      await Promise.all(recent.map((run) => this.hydrateRunTokenData(run)));

      // Pre-load tool calls for the most-recent run so they display immediately
      // on dashboard open without requiring a manual "Load" click. (Issue #2578)
      if (runs.length > 0) {
        await this.preloadMostRecentToolCalls(runs[0]);
      }

      // Replace history with index-based data (no limit — JSONL retention is the bound)
      this.history = runs;

      // Write through to Memento as cache
      await this.saveHistory();

      return runs.length;
    } catch (error) {
      console.warn(
        "[Nightgauge] Failed to load from TelemetryStore, falling back to Memento:",
        error
      );
      return 0;
    }
  }

  /**
   * Convert a TelemetryStore index entry to a PipelineRunSummary.
   *
   * Creates a summary-level object for the history list. Per-stage details
   * and tool calls are empty — loaded on-demand from JSONL when expanded.
   */
  private indexEntryToRunSummary(entry: HistoryIndexEntry): PipelineRunSummary {
    let status: PipelineRunStatus;
    switch (entry.outcome) {
      case "complete":
        status = "complete";
        break;
      case "failed":
        status = "failed";
        break;
      case "cancelled":
        status = "cancelled";
        break;
      default:
        status = "complete";
    }

    const run: PipelineRunSummary = {
      issueNumber: entry.issue_number,
      title: entry.title,
      branch: entry.branch,
      startedAt: new Date(entry.started_at),
      completedAt: entry.recorded_at ? new Date(entry.recorded_at) : undefined,
      status,
      stages: ALL_STAGES.map((stage) => ({
        stage,
        status: "complete" as StageRunStatus,
      })),
      usage: {
        inputTokens: entry.total_input_tokens ?? 0,
        outputTokens: entry.total_output_tokens ?? 0,
        cacheReadTokens: entry.total_cache_read_tokens ?? 0,
        cacheCreationTokens: entry.total_cache_creation_tokens ?? 0,
        costUsd: entry.cost_usd,
        durationMs: entry.duration_ms,
        stageCount: entry.stage_count,
      },
      toolCalls: [],
      is_recovery: entry.is_recovery,
      is_supercharge: entry.is_supercharge,
      issueType: entry.type ?? null,
      sizeLabel: entry.size ?? null,
      performance_mode: entry.performance_mode,
    };

    // Calculate ROI metrics
    run.manualEstimateMs = this.calculateManualEstimate(run);
    run.timeSavedMs = this.calculateTimeSaved(run);
    if (entry.duration_ms > 0) {
      run.efficiency = this.calculateEfficiency(run);
    }

    return run;
  }

  /**
   * Pre-load tool calls for the most-recent history run from JSONL (Issue #2578).
   *
   * Eagerly fetches tool calls for only the most-recent run so they display
   * immediately when the dashboard opens, without requiring a manual "Load" click.
   * Older runs still use the lazy-load pattern for performance.
   *
   * Non-critical: any failure is silently swallowed so dashboard load is unaffected.
   */
  private async preloadMostRecentToolCalls(run: PipelineRunSummary): Promise<void> {
    if (!this.telemetryStore) return;
    try {
      const record = await this.telemetryStore.getRunRecord(run.issueNumber);
      if (record?.tool_calls && record.tool_calls.length > 0) {
        run.toolCalls = record.tool_calls.map((tc) => ({
          tool: tc.tool,
          target: tc.target ?? "",
          timestamp: tc.timestamp ? new Date(tc.timestamp) : new Date(),
          durationMs: tc.duration_ms,
          args: tc.args,
          result: tc.result,
          error: tc.error,
        }));
      }
    } catch (error) {
      // Non-critical: preload failure never blocks dashboard load
      console.debug("[Nightgauge] preloadMostRecentToolCalls skipped:", error);
    }
  }

  /**
   * Get the TelemetryStore instance (Issue #1007).
   * Used by Dashboard for on-demand detail loading.
   */
  getTelemetryStore(): TelemetryStore | null {
    return this.telemetryStore;
  }

  /**
   * Save history to workspace storage
   */
  private async saveHistory(): Promise<void> {
    if (!this.workspaceState) return;

    const serialized = this.history.map((run) => this.serializeRun(run));
    await this.workspaceState.update(HISTORY_STORAGE_KEY, serialized);
  }

  /**
   * Serialize a run for storage
   */
  private serializeRun(run: PipelineRunSummary): SerializedPipelineRun {
    return {
      issueNumber: run.issueNumber,
      title: run.title,
      branch: run.branch,
      startedAt: run.startedAt.toISOString(),
      completedAt: run.completedAt?.toISOString(),
      status: run.status,
      stages: run.stages.map((stage) => ({
        stage: stage.stage,
        status: stage.status,
        startedAt: stage.startedAt?.toISOString(),
        completedAt: stage.completedAt?.toISOString(),
        durationMs: stage.durationMs,
        tokenUsage: stage.tokenUsage
          ? {
              ...stage.tokenUsage,
              timestamp: stage.tokenUsage.timestamp.toISOString(),
            }
          : undefined,
        performance_mode: stage.performance_mode,
      })),
      currentStage: run.currentStage,
      usage: run.usage,
      // Tool calls are NOT persisted to workspaceState — they would inflate
      // memento serialization to multi-MB which blocks the extension host
      // event loop and causes VSCode to kill the host as unresponsive.
      // Tool calls are loaded on-demand from the TelemetryStore (JSONL files
      // in .nightgauge/pipeline/history/) via preloadMostRecentToolCalls
      // and the dashboard's lazy-load pattern. Memento is only a metadata
      // cache for fast startup per Issue #1007.
      toolCalls: [],
      manualEstimateMs: run.manualEstimateMs,
      timeSavedMs: run.timeSavedMs,
      efficiency: run.efficiency,
      routing: run.routing,

      is_recovery: run.is_recovery,
      is_supercharge: run.is_supercharge,
      issueType: run.issueType,
      sizeLabel: run.sizeLabel,
      performance_mode: run.performance_mode,
    };
  }

  /**
   * Deserialize a run from storage
   */
  private deserializeRun(run: SerializedPipelineRun): PipelineRunSummary {
    return {
      issueNumber: run.issueNumber,
      title: run.title,
      branch: run.branch,
      startedAt: new Date(run.startedAt),
      completedAt: run.completedAt ? new Date(run.completedAt) : undefined,
      status: run.status,
      stages: run.stages.map((stage) => ({
        stage: stage.stage,
        status: stage.status,
        startedAt: stage.startedAt ? new Date(stage.startedAt) : undefined,
        completedAt: stage.completedAt ? new Date(stage.completedAt) : undefined,
        durationMs: stage.durationMs,
        tokenUsage: stage.tokenUsage
          ? {
              ...stage.tokenUsage,
              timestamp: new Date(stage.tokenUsage.timestamp),
            }
          : undefined,
        performance_mode: stage.performance_mode,
      })),
      currentStage: run.currentStage,
      usage: run.usage,
      toolCalls: run.toolCalls.map((tc) => ({
        ...tc,
        timestamp: new Date(tc.timestamp),
      })),
      manualEstimateMs: run.manualEstimateMs,
      timeSavedMs: run.timeSavedMs,
      efficiency: run.efficiency,
      routing: run.routing,

      is_recovery: run.is_recovery,
      is_supercharge: run.is_supercharge,
      issueType: run.issueType,
      sizeLabel: run.sizeLabel,
      performance_mode: run.performance_mode,
    };
  }

  /**
   * Start a new pipeline run
   */
  startRun(issueNumber: number, title: string, branch: string): void {
    this.currentRun = {
      issueNumber,
      title,
      branch,
      startedAt: new Date(),
      status: "running",
      stages: ALL_STAGES.map((stage) => ({
        stage,
        status: "pending",
      })),
      usage: {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
        durationMs: 0,
        stageCount: 0,
      },
      toolCalls: [],
    };
    this._currentBacktracks = [];
    this._currentModelEscalations = [];
  }

  /**
   * Get the current run
   */
  getCurrentRun(): PipelineRunSummary | null {
    return this.currentRun;
  }

  /**
   * Set the backtrack records for the current pipeline run (Issue #1349)
   */
  setBacktracks(backtracks: BacktrackRecord[]): void {
    this._currentBacktracks = backtracks;
  }

  /**
   * Set the model escalation records for the current pipeline run (Issue #1349)
   */
  setModelEscalations(escalations: ModelEscalationRecord[]): void {
    this._currentModelEscalations = escalations;
  }

  /**
   * Get backtracks for the current pipeline run (Issue #1349)
   */
  getCurrentRunBacktracks(): BacktrackRecord[] {
    return this._currentBacktracks;
  }

  /**
   * Get model escalations for the current pipeline run (Issue #1349)
   */
  getCurrentRunModelEscalations(): ModelEscalationRecord[] {
    return this._currentModelEscalations;
  }

  /**
   * Get the number of backtracks for the current pipeline run (Issue #1349)
   */
  getCurrentRunBacktrackCount(): number {
    return this._currentBacktracks.length;
  }

  setCurrentPhase(info: CurrentPhaseInfo): void {
    this._currentPhase = info;
  }

  clearCurrentPhase(): void {
    this._currentPhase = null;
  }

  getCurrentPhase(): CurrentPhaseInfo | null {
    return this._currentPhase;
  }

  /**
   * Set a stage as running
   */
  setStageRunning(stage: PipelineStage): void {
    if (!this.currentRun) return;

    const stageProgress = this.currentRun.stages.find((s) => s.stage === stage);
    if (stageProgress) {
      stageProgress.status = "running";
      stageProgress.startedAt = new Date();
    }
    this.currentRun.currentStage = stage;
    if (this._currentPhase && this._currentPhase.stage !== stage) {
      this._currentPhase = null;
    }
  }

  /**
   * Set a stage as complete
   */
  setStageComplete(stage: PipelineStage, durationMs?: number): void {
    if (!this.currentRun) return;

    const stageProgress = this.currentRun.stages.find((s) => s.stage === stage);
    if (stageProgress) {
      stageProgress.status = "complete";
      stageProgress.completedAt = new Date();
      stageProgress.durationMs = durationMs;
    }
  }

  /**
   * Set a stage as failed
   */
  setStageFailed(stage: PipelineStage): void {
    if (!this.currentRun) return;

    const stageProgress = this.currentRun.stages.find((s) => s.stage === stage);
    if (stageProgress) {
      stageProgress.status = "failed";
      stageProgress.completedAt = new Date();
    }
  }

  /**
   * Set a stage as skipped
   */
  setStageSkipped(stage: PipelineStage): void {
    if (!this.currentRun) return;

    const stageProgress = this.currentRun.stages.find((s) => s.stage === stage);
    if (stageProgress) {
      stageProgress.status = "skipped";
      stageProgress.completedAt = new Date();
    }
  }

  /**
   * Set routing information for the current run
   *
   * Called after issue-pickup when routing decision is loaded.
   */
  setRouting(routing: PipelineRunRouting): void {
    if (!this.currentRun) return;
    this.currentRun.routing = routing;
  }

  /**
   * Get the routing information for the current run
   */
  getRouting(): PipelineRunRouting | undefined {
    return this.currentRun?.routing;
  }

  /**
   * Record token usage for a stage.
   *
   * Takes the dashboard's own {@link StageTokenUsage} shape — a per-stage token
   * snapshot folded from the workflow event tree's agent-node usage upstream.
   */
  recordTokenUsage(event: StageTokenUsage): void {
    if (!this.currentRun) return;

    const stageProgress = this.currentRun.stages.find((s) => s.stage === event.stage);
    if (stageProgress) {
      stageProgress.tokenUsage = {
        stage: event.stage,
        inputTokens: event.inputTokens,
        outputTokens: event.outputTokens,
        cacheReadTokens: event.cacheReadTokens,
        cacheCreationTokens: event.cacheCreationTokens,
        costUsd: event.costUsd,
        timestamp: event.timestamp,
      };
    }

    // Update total usage
    this.currentRun.usage.inputTokens += event.inputTokens;
    this.currentRun.usage.outputTokens += event.outputTokens;
    this.currentRun.usage.cacheReadTokens += event.cacheReadTokens;
    this.currentRun.usage.cacheCreationTokens += event.cacheCreationTokens;
    this.currentRun.usage.costUsd += event.costUsd;
    this.currentRun.usage.stageCount++;
  }

  /**
   * Add a tool call to the log
   */
  addToolCall(entry: ToolCallEntry): void {
    if (!this.currentRun) return;
    this.currentRun.toolCalls.push(entry);
  }

  /**
   * Complete the current run
   */
  async completeRun(): Promise<void> {
    if (!this.currentRun) return;

    this.currentRun.status = "complete";
    this.currentRun.completedAt = new Date();
    this.currentRun.currentStage = undefined;
    this._currentPhase = null;

    // Calculate total duration
    this.currentRun.usage.durationMs =
      this.currentRun.completedAt.getTime() - this.currentRun.startedAt.getTime();

    // Calculate ROI metrics
    this.currentRun.manualEstimateMs = this.calculateManualEstimate(this.currentRun);
    this.currentRun.timeSavedMs = this.calculateTimeSaved(this.currentRun);
    this.currentRun.efficiency = this.calculateEfficiency(this.currentRun);

    // Add to history
    this.addToHistory(this.currentRun);
    this.currentRun = null;
  }

  /**
   * Mark the current run as failed
   *
   * Issue #3001: when the run failed terminally we keep `currentRun` referenced
   * via `failedRun` so the dashboard's RunningNow widget can render the full
   * stage timeline (instead of collapsing to a single alert). The reference is
   * cleared by the operator's Retry / Skip / Discard action — handled by the
   * Go scheduler via IPC.
   */
  async failRun(): Promise<void> {
    if (!this.currentRun) return;

    this.currentRun.status = "failed";
    this.currentRun.completedAt = new Date();
    this._currentPhase = null;

    this.currentRun.usage.durationMs =
      this.currentRun.completedAt.getTime() - this.currentRun.startedAt.getTime();

    // Calculate ROI metrics (even for failed runs)
    this.currentRun.manualEstimateMs = this.calculateManualEstimate(this.currentRun);
    this.currentRun.timeSavedMs = this.calculateTimeSaved(this.currentRun);
    this.currentRun.efficiency = this.calculateEfficiency(this.currentRun);

    this.addToHistory(this.currentRun);
    // Preserve the failed run reference for the RunningNow widget timeline
    // before clearing currentRun. Cleared by discardFailedRun() once the
    // operator acknowledges. See Issue #3001.
    this.failedRun = this.currentRun;
    this.currentRun = null;
  }

  /**
   * The most recently failed run, retained until the operator dismisses it.
   * Powers the dashboard's persistent "Failed" RunningNow widget. (Issue #3001)
   */
  failedRun: PipelineRunSummary | null = null;

  /**
   * Clear the retained failedRun reference. Called by the dashboard webview
   * after the operator clicks Retry / Skip / Discard. (Issue #3001)
   */
  discardFailedRun(): void {
    this.failedRun = null;
  }

  /**
   * Cancel the current run
   */
  async cancelRun(): Promise<void> {
    if (!this.currentRun) return;

    this.currentRun.status = "cancelled";
    this.currentRun.completedAt = new Date();
    this._currentPhase = null;

    this.currentRun.usage.durationMs =
      this.currentRun.completedAt.getTime() - this.currentRun.startedAt.getTime();

    // Calculate ROI metrics (even for cancelled runs)
    this.currentRun.manualEstimateMs = this.calculateManualEstimate(this.currentRun);
    this.currentRun.timeSavedMs = this.calculateTimeSaved(this.currentRun);
    this.currentRun.efficiency = this.calculateEfficiency(this.currentRun);

    this.addToHistory(this.currentRun);
    this.currentRun = null;
  }

  /**
   * Add a run to history (at the front)
   *
   * When TelemetryStore is active (Issue #1007), the history limit cap is
   * removed — JSONL retention (90 days) provides the natural bound instead.
   * Memento is still written as a cache for fast startup.
   */
  private addToHistory(run: PipelineRunSummary): void {
    this.history.unshift(run);

    // Only apply cap when using Memento as primary source (Issue #983, #1007)
    if (!this.telemetryStore && this.history.length > this.historyLimit) {
      this.history = this.history.slice(0, this.historyLimit);
    }

    this.saveHistory();
  }

  /**
   * Get pipeline history
   */
  getHistory(): PipelineRunSummary[] {
    return this.history;
  }

  /**
   * Get a specific run from history by issue number
   */
  getHistoryRun(issueNumber: number): PipelineRunSummary | undefined {
    return this.history.find((run) => run.issueNumber === issueNumber);
  }

  /**
   * Resolve the platform runId (UUID) for a given issue number using cached runs data.
   * Returns null when the RunsEntry cache lacks a run_id field — callers fall back
   * to issueNumber-based matching for SSE pipeline event filtering (#3714).
   */
  getRunIdForIssue(_issueNumber: number): string | null {
    // RunsEntry (IpcClientBase) does not expose run_id; matching degrades to issueNumber.
    return null;
  }

  /**
   * Update tool calls for a historical run after on-demand loading (Issue #1032).
   * Returns true if the run was found and updated.
   */
  updateRunToolCalls(issueNumber: number, toolCalls: ToolCallEntry[]): boolean {
    const run = this.history.find((r) => r.issueNumber === issueNumber);
    if (!run) return false;
    run.toolCalls = toolCalls;
    return true;
  }

  /**
   * Calculate progress percentage for a run
   */
  getProgressPercent(run: PipelineRunSummary): number {
    const completedStages = run.stages.filter(
      (s) => s.status === "complete" || s.status === "skipped"
    ).length;
    return Math.round((completedStages / ALL_STAGES.length) * 100);
  }

  /**
   * Get cumulative token usage over time for charting
   */
  getCumulativeUsage(run: PipelineRunSummary): { stage: string; tokens: number; cost: number }[] {
    const result: { stage: string; tokens: number; cost: number }[] = [];
    let cumulativeTokens = 0;
    let cumulativeCost = 0;

    for (const stage of run.stages) {
      if (stage.tokenUsage) {
        cumulativeTokens += stage.tokenUsage.inputTokens + stage.tokenUsage.outputTokens;
        cumulativeCost += stage.tokenUsage.costUsd;
      }
      result.push({
        stage: formatStageName(stage.stage),
        tokens: cumulativeTokens,
        cost: cumulativeCost,
      });
    }

    return result;
  }

  /**
   * Export run data as JSON
   */
  exportAsJson(run: PipelineRunSummary): string {
    return JSON.stringify(this.serializeRun(run), null, 2);
  }

  /**
   * Export run data as CSV
   */
  exportAsCsv(run: PipelineRunSummary): string {
    const lines: string[] = [];

    // Header
    lines.push(
      "Stage,Status,Input Tokens,Output Tokens,Cache Read,Cache Created,Cost USD,Duration MS"
    );

    // Stage rows
    for (const stage of run.stages) {
      const usage = stage.tokenUsage;
      lines.push(
        [
          stage.stage,
          stage.status,
          usage?.inputTokens ?? 0,
          usage?.outputTokens ?? 0,
          usage?.cacheReadTokens ?? 0,
          usage?.cacheCreationTokens ?? 0,
          usage?.costUsd?.toFixed(4) ?? "0.0000",
          stage.durationMs ?? 0,
        ].join(",")
      );
    }

    // Totals row
    lines.push(
      [
        "TOTAL",
        run.status,
        run.usage.inputTokens,
        run.usage.outputTokens,
        run.usage.cacheReadTokens,
        run.usage.cacheCreationTokens,
        run.usage.costUsd.toFixed(4),
        run.usage.durationMs,
      ].join(",")
    );

    // ROI Metrics section
    lines.push("");
    lines.push("ROI Metrics");
    lines.push(`Manual Estimate (ms),${run.manualEstimateMs ?? 0}`);
    lines.push(`Time Saved (ms),${run.timeSavedMs ?? 0}`);
    lines.push(`Time Saved (minutes),${Math.round((run.timeSavedMs ?? 0) / 60000)}`);

    // Efficiency metrics
    if (run.efficiency) {
      lines.push("");
      lines.push("Efficiency Metrics");
      lines.push(`Tokens per Minute,${Math.round(run.efficiency.tokensPerMinute)}`);
      lines.push(`Cost per Minute,$${run.efficiency.costPerMinute.toFixed(4)}`);
      lines.push(`Cache Hit Rate,${Math.round(run.efficiency.cacheHitRate * 100)}%`);
      lines.push(`Avg Stage Duration (ms),${Math.round(run.efficiency.avgStageDurationMs)}`);
    }

    return lines.join("\n");
  }

  /**
   * Calculate the estimated manual time for a run based on completed stages
   */
  calculateManualEstimate(run: PipelineRunSummary): number {
    let totalMinutes = 0;
    for (const stage of run.stages) {
      if (stage.status === "complete") {
        const configKey = STAGE_TO_CONFIG_KEY[stage.stage];
        totalMinutes += this.timeSavingsConfig[configKey];
      }
    }
    return totalMinutes * 60 * 1000; // Convert to milliseconds
  }

  /**
   * Calculate time saved for a run (manual estimate - actual duration)
   */
  calculateTimeSaved(run: PipelineRunSummary): number {
    const manualEstimate = this.calculateManualEstimate(run);
    const actualDuration = run.usage.durationMs;
    return Math.max(0, manualEstimate - actualDuration);
  }

  /**
   * Calculate efficiency metrics for a run
   */
  calculateEfficiency(run: PipelineRunSummary): EfficiencyMetrics {
    const durationMinutes = run.usage.durationMs / 60000;
    const totalTokens = run.usage.inputTokens + run.usage.outputTokens;
    const totalCacheTokens = run.usage.cacheReadTokens + run.usage.cacheCreationTokens;
    const cacheableTokens = totalTokens + totalCacheTokens;

    // Calculate average stage duration
    const completedStages = run.stages.filter((s) => s.status === "complete" && s.durationMs);
    const avgStageDurationMs =
      completedStages.length > 0
        ? completedStages.reduce((sum, s) => sum + (s.durationMs ?? 0), 0) / completedStages.length
        : 0;

    return {
      tokensPerMinute: durationMinutes > 0 ? totalTokens / durationMinutes : 0,
      costPerMinute: durationMinutes > 0 ? run.usage.costUsd / durationMinutes : 0,
      cacheHitRate: cacheableTokens > 0 ? run.usage.cacheReadTokens / cacheableTokens : 0,
      avgStageDurationMs,
    };
  }

  /**
   * Get runs from the current session
   */
  getSessionRuns(): PipelineRunSummary[] {
    return this.history.filter((run) => run.startedAt >= this.sessionStartTime);
  }

  /**
   * Get aggregated statistics for the dashboard
   */
  getAggregates(scope: "session" | "all" = "all"): DashboardAggregates {
    const allRuns = this.history;
    const sessionRuns = this.getSessionRuns();
    const runs = scope === "session" ? sessionRuns : allRuns;

    const successfulRuns = runs.filter((r) => r.status === "complete");

    let totalTimeSavedMs = 0;
    let sessionTimeSavedMs = 0;
    let totalTokens = 0;
    let sessionTokens = 0;

    for (const run of allRuns) {
      const timeSaved = run.timeSavedMs ?? this.calculateTimeSaved(run);
      totalTimeSavedMs += timeSaved;
      totalTokens += run.usage.inputTokens + run.usage.outputTokens;
    }

    const totalCostUsd = allRuns.reduce((sum, r) => sum + r.usage.costUsd, 0);

    for (const run of sessionRuns) {
      const timeSaved = run.timeSavedMs ?? this.calculateTimeSaved(run);
      sessionTimeSavedMs += timeSaved;
      sessionTokens += run.usage.inputTokens + run.usage.outputTokens;
    }

    const sessionCostUsd = sessionRuns.reduce((sum, r) => sum + r.usage.costUsd, 0);

    const targetRuns = scope === "session" ? sessionRuns : allRuns;
    const targetSuccessful =
      scope === "session" ? sessionRuns.filter((r) => r.status === "complete") : successfulRuns;

    return {
      totalRuns: allRuns.length,
      sessionRuns: sessionRuns.length,
      totalTimeSavedMs,
      sessionTimeSavedMs,
      totalCostUsd,
      sessionCostUsd,
      successRate: targetRuns.length > 0 ? targetSuccessful.length / targetRuns.length : 0,
      avgCostPerRun: (() => {
        const runsWithCost = targetRuns.filter((r) => r.usage.costUsd > 0);
        if (runsWithCost.length === 0) return 0;
        const cost = scope === "session" ? sessionCostUsd : totalCostUsd;
        return cost / runsWithCost.length;
      })(),
      avgTimeSavedPerRun:
        targetRuns.length > 0
          ? (scope === "session" ? sessionTimeSavedMs : totalTimeSavedMs) / targetRuns.length
          : 0,
      totalTokens,
      sessionTokens,
      epicEstimates: this.epicEstimates,
      crossRepoEpicProgress: this.crossRepoEpicProgress,
      firewallAggregates: this.firewallAggregates,
      stageAverages: this.getPerStageAverages(scope),
      costPerIssue: this.getCostPerIssueAggregations(),
      recentDelta: this.computeRecentActivityDelta(7),
    };
  }

  /**
   * Compare the trailing `windowDays` to the `windowDays` immediately prior.
   *
   * Returns recent-vs-prior deltas for the headline metrics shown on the
   * Overview tab. Buckets are computed from `startedAt` and Date.now() — no
   * wall-clock alignment to midnight, so a freshly-installed dashboard never
   * reports a misleading "+0 this week" before any time has passed.
   *
   * Recovery runs are intentionally NOT excluded here: the headline stats
   * are about activity volume / cost as a user experiences it, including
   * the infrastructure cost of recoveries. Excluding them would understate
   * the bill the user actually pays.
   */
  private computeRecentActivityDelta(windowDays: number): RecentActivityDelta {
    const now = Date.now();
    const windowMs = windowDays * 24 * 60 * 60 * 1000;
    const recentStart = now - windowMs;
    const priorStart = now - 2 * windowMs;

    // Boundary convention: recent window is half-open `(recentStart, now]`,
    // prior is `(priorStart, recentStart]`. A run timestamped at exactly the
    // recent/prior boundary lands in `prior` (it's already "7 days old").
    const recent: PipelineRunSummary[] = [];
    const prior: PipelineRunSummary[] = [];
    for (const run of this.history) {
      const t = run.startedAt.getTime();
      if (t > recentStart && t <= now) recent.push(run);
      else if (t > priorStart && t <= recentStart) prior.push(run);
    }

    const sumCost = (rs: PipelineRunSummary[]) => rs.reduce((s, r) => s + r.usage.costUsd, 0);
    const sumTimeSaved = (rs: PipelineRunSummary[]) =>
      rs.reduce((s, r) => s + (r.timeSavedMs ?? this.calculateTimeSaved(r)), 0);
    const successRate = (rs: PipelineRunSummary[]) =>
      rs.length === 0 ? 0 : rs.filter((r) => r.status === "complete").length / rs.length;

    const recentRuns = recent.length;
    const priorRuns = prior.length;
    const recentSuccess = successRate(recent);
    const priorSuccess = successRate(prior);

    return {
      runsDelta: recentRuns - priorRuns,
      runsPrior: priorRuns,
      timeSavedDeltaMs: sumTimeSaved(recent) - sumTimeSaved(prior),
      timeSavedPriorMs: sumTimeSaved(prior),
      costDeltaUsd: sumCost(recent) - sumCost(prior),
      costPriorUsd: sumCost(prior),
      // Percentage *points*, not relative percent. (75 - 70) = +5pp, not +7%.
      // A relative comparison around a small baseline (e.g. 0 → 50) explodes
      // and isn't meaningful for a 0-1 ratio.
      successRatePointsDelta: Math.round((recentSuccess - priorSuccess) * 100),
      successRateRecent: recentSuccess,
      successRatePrior: priorSuccess,
      // No signal in the recent window — renderer should suppress delta UI.
      hasEnoughData: recentRuns > 0,
      windowDays,
    };
  }

  /**
   * Compute cost aggregations per issue from in-memory pipeline history.
   *
   * Groups runs by issueNumber, sums usage.costUsd, and returns last 20
   * issues sorted by most-recent activity. Computed synchronously for
   * live dashboard updates.
   *
   * @see Issue #1410 - Cost-per-issue aggregation
   */
  private getCostPerIssueAggregations(): IssueCostAggregation[] {
    const byIssue = new Map<number, PipelineRunSummary[]>();
    for (const run of this.history) {
      const arr = byIssue.get(run.issueNumber) ?? [];
      arr.push(run);
      byIssue.set(run.issueNumber, arr);
    }

    const result: IssueCostAggregation[] = [];
    for (const [issueNumber, runs] of byIssue) {
      const totalCostUsd = runs.reduce((sum, r) => sum + r.usage.costUsd, 0);
      if (totalCostUsd === 0) continue;

      const backtrackCount = runs.filter((r) => r.is_recovery === true).length;
      const sorted = [...runs].sort((a, b) => a.startedAt.getTime() - b.startedAt.getTime());

      // Use the most recent run's type/size — reflects current issue labels (Issue #2546)
      const mostRecentRun = sorted[sorted.length - 1];

      result.push({
        issueNumber,
        totalCostUsd,
        runCount: runs.length,
        backtrackCount,
        issueType: mostRecentRun.issueType ?? null,
        sizeLabel: mostRecentRun.sizeLabel ?? null,
        firstRunAt: sorted[0].startedAt,
        lastRunAt: sorted[sorted.length - 1].startedAt,
      });
    }

    return result.sort((a, b) => b.lastRunAt.getTime() - a.lastRunAt.getTime()).slice(0, 20);
  }

  /**
   * Compute per-stage average metrics across historical runs (Issue #1008)
   *
   * Iterates over history (filtered by session scope if requested) and
   * accumulates cost, tokens, duration, and model frequency per pipeline stage.
   */
  getPerStageAverages(scope: "session" | "all" = "all"): StageAverageMetrics[] {
    const runs = scope === "session" ? this.getSessionRuns() : this.history;

    const stageData = new Map<
      string,
      {
        costSum: number;
        inputSum: number;
        outputSum: number;
        cacheReadSum: number;
        cacheCreationSum: number;
        durationSum: number;
        count: number;
        modelCounts: Map<string, number>;
      }
    >();

    for (const run of runs) {
      for (const stage of run.stages) {
        if (!stage.tokenUsage && stage.durationMs === undefined) {
          continue;
        }

        let data = stageData.get(stage.stage);
        if (!data) {
          data = {
            costSum: 0,
            inputSum: 0,
            outputSum: 0,
            cacheReadSum: 0,
            cacheCreationSum: 0,
            durationSum: 0,
            count: 0,
            modelCounts: new Map(),
          };
          stageData.set(stage.stage, data);
        }

        if (stage.tokenUsage) {
          data.costSum += stage.tokenUsage.costUsd;
          data.inputSum += stage.tokenUsage.inputTokens;
          data.outputSum += stage.tokenUsage.outputTokens;
          data.cacheReadSum += stage.tokenUsage.cacheReadTokens;
          data.cacheCreationSum += stage.tokenUsage.cacheCreationTokens;

          if (stage.tokenUsage.model) {
            const prev = data.modelCounts.get(stage.tokenUsage.model) ?? 0;
            data.modelCounts.set(stage.tokenUsage.model, prev + 1);
          }
        }

        if (stage.durationMs !== undefined) {
          data.durationSum += stage.durationMs;
        }

        data.count++;
      }
    }

    const results: StageAverageMetrics[] = [];
    for (const stageName of ALL_STAGES) {
      const data = stageData.get(stageName);
      if (!data || data.count === 0) continue;

      let primaryModel: string | null = null;
      let maxModelCount = 0;
      for (const [model, count] of data.modelCounts) {
        if (count > maxModelCount) {
          maxModelCount = count;
          primaryModel = model;
        }
      }

      results.push({
        stage: stageName,
        avgCostUsd: data.costSum / data.count,
        avgInputTokens: data.inputSum / data.count,
        avgOutputTokens: data.outputSum / data.count,
        avgCacheReadTokens: data.cacheReadSum / data.count,
        avgCacheCreationTokens: data.cacheCreationSum / data.count,
        avgDurationMs: data.durationSum / data.count,
        runCount: data.count,
        primaryModel,
      });
    }

    return results;
  }

  /**
   * Computes the historical median cost (USD) per stage from all stored runs.
   * Only considers non-zero-cost stage entries. Returns an empty record for
   * stages with no history. Stages with fewer than 3 non-zero samples have a
   * sampleCount < 3 in the result — callers use this to suppress warnings.
   *
   * @see Issue #3276
   */
  getPerStageMedianCosts(): Record<string, { median: number; sampleCount: number }> {
    const stageCosts = new Map<string, number[]>();
    for (const run of this.history) {
      for (const stage of run.stages) {
        const cost = stage.tokenUsage?.costUsd ?? 0;
        if (cost <= 0) continue;
        const arr = stageCosts.get(stage.stage) ?? [];
        arr.push(cost);
        stageCosts.set(stage.stage, arr);
      }
    }

    const result: Record<string, { median: number; sampleCount: number }> = {};
    for (const [stage, costs] of stageCosts) {
      const sorted = [...costs].sort((a, b) => a - b);
      const mid = Math.floor(sorted.length / 2);
      const median = sorted.length % 2 === 0 ? (sorted[mid - 1] + sorted[mid]) / 2 : sorted[mid];
      result[stage] = { median, sampleCount: sorted.length };
    }
    return result;
  }

  /**
   * Identify stages in a run that exceed 2x the historical average (Issue #1008)
   *
   * Compares each stage's cost and duration against historical averages.
   * Returns outlier entries for stages that exceed the 2x threshold.
   */
  getStageOutliers(run: PipelineRunSummary, averages?: StageAverageMetrics[]): StageOutlier[] {
    const avgs = averages ?? this.getPerStageAverages("all");
    const avgMap = new Map<string, StageAverageMetrics>();
    for (const avg of avgs) {
      avgMap.set(avg.stage, avg);
    }

    const outliers: StageOutlier[] = [];
    const OUTLIER_THRESHOLD = 2.0;

    for (const stage of run.stages) {
      const avg = avgMap.get(stage.stage);
      if (!avg || avg.runCount < 2) continue;

      // Check cost outlier
      if (
        stage.tokenUsage &&
        avg.avgCostUsd > 0 &&
        stage.tokenUsage.costUsd > avg.avgCostUsd * OUTLIER_THRESHOLD
      ) {
        outliers.push({
          stage: stage.stage,
          metric: "cost",
          value: stage.tokenUsage.costUsd,
          avg: avg.avgCostUsd,
          ratio: stage.tokenUsage.costUsd / avg.avgCostUsd,
        });
      }

      // Check duration outlier
      if (
        stage.durationMs !== undefined &&
        avg.avgDurationMs > 0 &&
        stage.durationMs > avg.avgDurationMs * OUTLIER_THRESHOLD
      ) {
        outliers.push({
          stage: stage.stage,
          metric: "duration",
          value: stage.durationMs,
          avg: avg.avgDurationMs,
          ratio: stage.durationMs / avg.avgDurationMs,
        });
      }
    }

    return outliers;
  }

  /**
   * Refresh epic estimates by fetching all open epics and estimating their time
   *
   * This method queries GitHub for all open issues with type:epic label,
   * then uses EpicEstimator to calculate time estimates for each.
   */
  async refreshEpicEstimates(): Promise<void> {
    if (!this.workspaceRoot) {
      this.epicEstimates = [];
      return;
    }

    try {
      const { IpcClient } = await import("../../services/IpcClient");
      const { getRepoIdentity } = await import("../../utils/configPathResolver");
      const { EpicEstimator } = await import("@nightgauge/sdk");

      // Query GitHub for all open epics via Go binary IPC
      const identity = await getRepoIdentity(this.workspaceRoot);
      if (!identity) {
        this.epicEstimates = [];
        return;
      }
      const ipc = IpcClient.getInstance();
      const issues = await ipc.issueList(identity.owner, identity.repo, {
        labels: ["type:epic"],
      });

      const epics = issues.map((issue) => ({
        number: issue.number,
        title: issue.title,
      }));

      if (epics.length === 0) {
        this.epicEstimates = [];
        return;
      }

      // Estimate each epic
      const estimator = new EpicEstimator(
        `${this.workspaceRoot}/.nightgauge/complexity-model.yaml`,
        this.workspaceRoot
      );
      const entries: EpicDisplayEntry[] = [];

      for (const epic of epics) {
        try {
          const estimate = await estimator.estimateEpic(epic.number);
          entries.push({
            epic_number: epic.number,
            epic_title: estimate.epic_title,
            estimate,
            warning: null,
          });
        } catch (error) {
          // Include epics that can't be estimated with a warning instead of skipping
          const message = error instanceof Error ? error.message : "Unknown error";
          console.warn(`Failed to estimate epic #${epic.number}:`, message);
          entries.push({
            epic_number: epic.number,
            epic_title: epic.title,
            estimate: null,
            warning: message,
          });
        }
      }

      // Sort: estimated epics first (by remaining time desc), then failed epics
      entries.sort((a, b) => {
        if (a.estimate && b.estimate) {
          return b.estimate.total_remaining_minutes - a.estimate.total_remaining_minutes;
        }
        if (a.estimate && !b.estimate) return -1;
        if (!a.estimate && b.estimate) return 1;
        return 0;
      });

      this.epicEstimates = entries;
    } catch (error) {
      console.error("Failed to refresh epic estimates:", error);
      this.epicEstimates = [];
    }
  }

  /**
   * Refresh cross-repo epic estimates using EpicDashboard service
   *
   * This method queries all repositories in the workspace for epic sub-issues
   * and aggregates progress by repository. Requires WorkspaceManager.
   *
   * @param workspaceManager - WorkspaceManager instance for multi-repo access
   * @see Issue #330 - Epic Dashboard with Cross-Repo Progress
   */
  async refreshCrossRepoEpicEstimates(
    workspaceManager: import("../../services/WorkspaceManager").WorkspaceManager
  ): Promise<void> {
    if (!workspaceManager.isMultiWorkspace()) {
      // Not in multi-repo mode - clear cross-repo data
      this.crossRepoEpicProgress = [];
      return;
    }

    try {
      const { EpicDashboard } = await import("./EpicDashboard");
      const epicDashboard = new EpicDashboard(workspaceManager);

      // Get cross-repo progress for all open epics
      const progress = await epicDashboard.getAllCrossRepoProgress();

      this.crossRepoEpicProgress = progress;
    } catch (error) {
      console.error("Failed to refresh cross-repo epic estimates:", error);
      this.crossRepoEpicProgress = [];
    }
  }

  /**
   * Get cross-repo epic progress data
   */
  getCrossRepoEpicProgress(): CrossRepoEpicProgress[] {
    return this.crossRepoEpicProgress;
  }

  /**
   * Set cross-repo epic progress data (for testing or direct updates)
   */
  setCrossRepoEpicProgress(progress: CrossRepoEpicProgress[]): void {
    this.crossRepoEpicProgress = progress;
  }

  // =========================================================================
  // Firewall State Management (Issue #387)
  // =========================================================================

  /**
   * Storage key for firewall filter state
   */
  private static readonly FIREWALL_FILTERS_KEY = "nightgauge.dashboard.firewallFilters";

  /**
   * Load firewall filter state from workspace storage
   */
  private loadFirewallFilters(): void {
    if (!this.workspaceState) return;

    const stored = this.workspaceState.get<FirewallFilterState>(
      DashboardState.FIREWALL_FILTERS_KEY
    );

    if (stored) {
      this.firewallFilters = { ...DEFAULT_FIREWALL_FILTERS, ...stored };
    }
  }

  /**
   * Save firewall filter state to workspace storage
   */
  private async saveFirewallFilters(): Promise<void> {
    if (!this.workspaceState) return;

    await this.workspaceState.update(DashboardState.FIREWALL_FILTERS_KEY, this.firewallFilters);
  }

  /**
   * Get the current firewall filter state
   */
  getFirewallFilters(): FirewallFilterState {
    return { ...this.firewallFilters };
  }

  /**
   * Update firewall filters and persist to storage
   */
  async setFirewallFilters(filters: Partial<FirewallFilterState>): Promise<void> {
    this.firewallFilters = { ...this.firewallFilters, ...filters };
    await this.saveFirewallFilters();
  }

  /**
   * Reset firewall filters to defaults
   */
  async resetFirewallFilters(): Promise<void> {
    this.firewallFilters = { ...DEFAULT_FIREWALL_FILTERS };
    await this.saveFirewallFilters();
  }

  /**
   * Get firewall aggregates
   */
  getFirewallAggregates(): FirewallAggregates | null {
    return this.firewallAggregates;
  }

  /**
   * Set firewall aggregates (called by Dashboard from service)
   */
  setFirewallAggregates(aggregates: FirewallAggregates | null): void {
    this.firewallAggregates = aggregates;
  }

  /**
   * Get historical data for sparkline charts
   */
  getHistoricalData(
    metric: "cost" | "tokens" | "duration" | "timeSaved",
    limit: number = 20
  ): number[] {
    // Filter out zero-token runs (infrastructure, skipped) for cost/token
    // sparklines — they drag down trend signals without representing real work.
    const isTokenMetric = metric === "cost" || metric === "tokens";
    const filtered = isTokenMetric
      ? this.history.filter((r) => r.usage.inputTokens + r.usage.outputTokens > 0)
      : this.history;
    const runs = filtered.slice(0, limit).reverse();
    return runs.map((run) => {
      switch (metric) {
        case "cost":
          return run.usage.costUsd;
        case "tokens":
          // Show only generated tokens (input + output) — cache reads are an
          // implementation detail, not "work done". Cache data is visible in
          // the per-stage breakdown table and the cache-hit-rate sparkline.
          return run.usage.inputTokens + run.usage.outputTokens;
        case "duration":
          return run.usage.durationMs;
        case "timeSaved":
          return run.timeSavedMs ?? this.calculateTimeSaved(run);
      }
    });
  }

  /**
   * Clear all history
   */
  async clearHistory(): Promise<void> {
    this.history = [];
    await this.saveHistory();
  }

  /**
   * Get the timestamp of the last dashboard data refresh
   *
   * Returns a full Date for use in "Last updated: <date/time>" display.
   * Updated whenever the dashboard panel re-renders.
   *
   * @see Issue #614 - Dashboard should show dated last-updated
   */
  getLastRefreshedAt(): Date {
    return this._lastRefreshedAt;
  }

  /**
   * Mark the dashboard data as freshly refreshed
   *
   * @see Issue #614 - Dashboard should show dated last-updated
   */
  markRefreshed(): void {
    this._lastRefreshedAt = new Date();
  }

  /**
   * Backfill dashboard history from pipeline run artifacts on disk
   *
   * Reads completed pipeline state files from .nightgauge/pipeline/
   * AND execution history JSONL files from .nightgauge/pipeline/history/
   * and imports them into dashboard history if not already present.
   * This ensures the dashboard shows historical data even when the
   * VSCode workspace state (Memento) is empty or was cleared.
   *
   * Only imports runs where all core stages are complete or the pipeline
   * has a terminal state (all stages resolved). Deduplicates by issue number.
   *
   * @param options.rescrub - If true, clears existing history before importing
   *   to rebuild from scratch using all available disk artifacts.
   * @see Issue #614 - Backfill dashboard from existing pipeline history
   * @deprecated When TelemetryStore is available (Issue #1007), use
   *   loadFromTelemetryStore() instead. This method becomes a no-op.
   */
  async backfillFromPipelineArtifacts(options: { rescrub?: boolean } = {}): Promise<number> {
    // When TelemetryStore is active, delegate to it instead (Issue #1007)
    if (this.telemetryStore) {
      if (options.rescrub) {
        await this.telemetryStore.rebuildIndex();
      }
      return this.loadFromTelemetryStore();
    }

    if (!this.workspaceRoot) return 0;

    const pipelineDir = path.join(this.workspaceRoot, ".nightgauge", "pipeline");

    // Rescrub: clear existing history to rebuild from disk
    if (options.rescrub) {
      this.history = [];
    }

    let imported = 0;

    try {
      const files = await fs.readdir(pipelineDir);

      // 1. Import from state*.json files (current + archived snapshots)
      const stateFiles = files.filter(
        (f) => (f === "state.json" || f.match(/^state-\d+\.json$/)) && !f.includes(".corrupt")
      );

      for (const file of stateFiles) {
        try {
          const filePath = path.join(pipelineDir, file);
          const content = await fs.readFile(filePath, "utf-8");
          const parsed = JSON.parse(content);
          if (this.importParsedRunRecord(parsed)) imported++;
        } catch {
          continue;
        }
      }

      // 2. Import from history/*.jsonl files (complete execution records)
      const historyDir = path.join(pipelineDir, "history");
      try {
        const historyFiles = await fs.readdir(historyDir);
        const jsonlFiles = historyFiles.filter((f) => f.endsWith(".jsonl"));

        for (const file of jsonlFiles) {
          try {
            const content = await fs.readFile(path.join(historyDir, file), "utf-8");
            for (const line of content.split("\n")) {
              if (!line.trim()) continue;
              try {
                const record = JSON.parse(line);
                if (this.importParsedRunRecord(record)) imported++;
              } catch {
                // Skip malformed JSONL lines
              }
            }
          } catch {
            // Skip unreadable files
          }
        }
      } catch {
        // History directory doesn't exist - that's OK
      }

      if (imported > 0) {
        // Sort history by startedAt (most recent first)
        this.history.sort((a, b) => b.startedAt.getTime() - a.startedAt.getTime());
        // Trim to max size
        if (this.history.length > this.historyLimit) {
          this.history = this.history.slice(0, this.historyLimit);
        }
        await this.saveHistory();
      }
    } catch {
      // Pipeline directory doesn't exist or can't be read - that's OK
    }

    return imported;
  }

  /**
   * Import a single parsed pipeline run record into history.
   *
   * Handles both state.json format and JSONL history record format
   * (they share the same schema for stages and tokens).
   *
   * @returns true if the record was imported, false if skipped
   */
  private importParsedRunRecord(parsed: Record<string, unknown>): boolean {
    // Basic shape validation
    if (!parsed.issue_number || !parsed.title || !parsed.branch || !parsed.stages) {
      return false;
    }

    const issueNumber = parsed.issue_number as number;
    const startedAtStr = parsed.started_at as string | undefined;

    // Deduplicate by issue number + startedAt timestamp to allow multiple
    // runs of the same issue (e.g., retries) while preventing true duplicates
    // from being imported twice from different source files (#990)
    const alreadyExists = this.history.some(
      (run) =>
        run.issueNumber === issueNumber &&
        (!startedAtStr || run.startedAt.getTime() === new Date(startedAtStr).getTime())
    );
    if (alreadyExists) return false;

    // Skip if this record matches the current active run (same issue + startedAt)
    if (
      this.currentRun?.issueNumber === issueNumber &&
      (!startedAtStr || this.currentRun.startedAt.getTime() === new Date(startedAtStr).getTime())
    )
      return false;

    // Determine run status from stage states
    const stages = parsed.stages as Record<
      string,
      {
        status: string;
        started_at?: string;
        completed_at?: string;
        duration_ms?: number;
        performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
      }
    >;

    const stageEntries: StageProgress[] = ALL_STAGES.map((stageName) => {
      const s = stages[stageName];
      const status = s?.status ?? "pending";
      return {
        stage: stageName,
        status: status as StageRunStatus,
        startedAt: s?.started_at ? new Date(s.started_at) : undefined,
        completedAt: s?.completed_at ? new Date(s.completed_at) : undefined,
        durationMs: s?.duration_ms,
        tokenUsage: this.extractStageTokenUsage(
          stageName,
          (parsed.tokens as Record<string, unknown>)?.per_stage as Record<
            string,
            {
              input: number;
              output: number;
              cache_read: number;
              cache_creation: number;
              cost_usd: number;
            }
          >
        ),
        performance_mode: s?.performance_mode,
      };
    });

    // Determine overall run status
    const hasFailure = stageEntries.some((s) => s.status === "failed");
    const allResolved = stageEntries.every(
      (s) => s.status === "complete" || s.status === "skipped" || s.status === "failed"
    );
    const hasAnyComplete = stageEntries.some((s) => s.status === "complete");
    const hasAnyRunning = stageEntries.some((s) => s.status === "running");

    // Import runs that have completed work OR are currently in-progress (Issue #639)
    if (!hasAnyComplete && !hasAnyRunning) return false;

    // Use outcome field from JSONL records if available, otherwise derive
    let runStatus: PipelineRunStatus = "running";
    const outcome = parsed.outcome as string | undefined;
    if (outcome === "complete") {
      runStatus = "complete";
    } else if (outcome === "failed") {
      runStatus = "failed";
    } else if (outcome === "cancelled") {
      runStatus = "cancelled";
    } else if (allResolved && hasFailure) {
      runStatus = "failed";
    } else if (allResolved) {
      runStatus = "complete";
    }

    // Build total usage from tokens field
    const tokens = (parsed.tokens ?? {}) as Record<string, unknown>;
    const totalUsage: TotalUsage = {
      inputTokens: (tokens.total_input as number) ?? 0,
      outputTokens: (tokens.total_output as number) ?? 0,
      cacheReadTokens: (tokens.total_cache_read as number) ?? 0,
      cacheCreationTokens: (tokens.total_cache_creation as number) ?? 0,
      costUsd: (tokens.estimated_cost_usd as number) ?? 0,
      durationMs: 0,
      stageCount: stageEntries.filter((s) => s.status === "complete").length,
    };

    // Calculate duration: prefer total_duration_ms from JSONL, else derive from timestamps
    const startedAt = parsed.started_at ? new Date(parsed.started_at as string) : new Date();

    const totalDurationMs = parsed.total_duration_ms as number | undefined;
    const completedAtStr = parsed.completed_at as string | undefined;
    let completedAt: Date | undefined;

    if (totalDurationMs) {
      totalUsage.durationMs = totalDurationMs;
      completedAt = completedAtStr ? new Date(completedAtStr) : undefined;
    } else {
      const lastCompletedStage = stageEntries
        .filter((s) => s.completedAt)
        .sort((a, b) => b.completedAt!.getTime() - a.completedAt!.getTime())[0];
      completedAt = lastCompletedStage?.completedAt;
      if (completedAt) {
        totalUsage.durationMs = completedAt.getTime() - startedAt.getTime();
      }
    }

    const runMode = (
      parsed as { performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier" }
    ).performance_mode;

    const run: PipelineRunSummary = {
      issueNumber: issueNumber,
      title: parsed.title as string,
      branch: parsed.branch as string,
      startedAt,
      completedAt,
      status: runStatus,
      stages: stageEntries,
      usage: totalUsage,
      toolCalls: [],
      performance_mode: runMode,
    };

    // Calculate ROI metrics for the imported run
    run.manualEstimateMs = this.calculateManualEstimate(run);
    run.timeSavedMs = this.calculateTimeSaved(run);
    if (totalUsage.durationMs > 0) {
      run.efficiency = this.calculateEfficiency(run);
    }

    this.history.push(run);
    return true;
  }

  /**
   * Extract per-stage token usage from pipeline state per_stage data
   */
  private extractStageTokenUsage(
    stage: PipelineStage,
    perStage?: Record<
      string,
      {
        input: number;
        output: number;
        cache_read: number;
        cache_creation: number;
        cost_usd: number;
        model?: string;
        model_source?: string;
        cache_hit_rate?: number;
      }
    >
  ): StageTokenUsage | undefined {
    if (!perStage || !perStage[stage]) return undefined;
    const s = perStage[stage];
    return {
      stage,
      inputTokens: s.input ?? 0,
      outputTokens: s.output ?? 0,
      cacheReadTokens: s.cache_read ?? 0,
      cacheCreationTokens: s.cache_creation ?? 0,
      costUsd: s.cost_usd ?? 0,
      model: s.model,
      cacheHitRate: s.cache_hit_rate,
      timestamp: new Date(),
    };
  }

  /**
   * Get efficiency trend analysis comparing recent vs older runs
   *
   * Compares cost-per-stage between recent and older non-recovery runs
   * to determine if pipeline efficiency is improving or declining.
   *
   * Uses cost-per-stage instead of tokensPerMinute because budget
   * adjustments influence cost but not model throughput speed.
   *
   * Excludes recovery runs — their cost reflects infrastructure failures,
   * not normal feature complexity (same pattern as getCostTrend).
   * Excludes supercharge runs — forced Opus + max effort reflects an
   * intentional quality override, not an efficiency regression (Issue #2433).
   *
   * @param recentCount - Number of recent runs to compare (default: 5)
   * @param olderCount - Number of older runs to compare against (default: 5)
   * @returns Object with improving boolean and percent change
   */
  getEfficiencyTrend(
    recentCount: number = 5,
    olderCount: number = 5
  ): { improving: boolean; percentChange: number; hasEnoughData: boolean } {
    // Exclude recovery + supercharge runs, and runs with 0 completed stages
    const validHistory = this.history.filter(
      (r) =>
        !r.is_recovery &&
        !r.is_supercharge &&
        (r.usage.stageCount ?? r.stages.filter((s) => s.status === "complete").length) > 0
    );
    const recent = validHistory.slice(0, recentCount);
    const older = validHistory.slice(recentCount, recentCount + olderCount);

    // Need at least 3 runs in each group for meaningful comparison
    if (recent.length < 3 || older.length < 3) {
      return { improving: true, percentChange: 0, hasEnoughData: false };
    }

    // Cost per completed stage — measures actual resource consumption per
    // unit of work, which budget adjustments can directly influence.
    const avgCostPerStage = (runs: PipelineRunSummary[]): number => {
      if (runs.length === 0) return 0;
      return (
        runs.reduce((sum, r) => {
          const stageCount =
            r.usage.stageCount ?? r.stages.filter((s) => s.status === "complete").length;
          return sum + (stageCount > 0 ? r.usage.costUsd / stageCount : 0);
        }, 0) / runs.length
      );
    };

    const recentAvg = avgCostPerStage(recent);
    const olderAvg = avgCostPerStage(older);

    if (olderAvg === 0) {
      return { improving: true, percentChange: 0, hasEnoughData: false };
    }

    const percentChange = ((recentAvg - olderAvg) / olderAvg) * 100;

    // Lower cost per stage = improving (negative percent change is good)
    return {
      improving: percentChange < 0,
      percentChange: Math.round(percentChange * 10) / 10,
      hasEnoughData: true,
    };
  }

  /**
   * Get cost trend analysis comparing recent vs older runs
   *
   * Compares the average cost per run between recent and older runs.
   *
   * @param recentCount - Number of recent runs to compare (default: 5)
   * @param olderCount - Number of older runs to compare against (default: 5)
   * @returns Object with improving boolean and percent change
   */
  getCostTrend(
    recentCount: number = 5,
    olderCount: number = 5
  ): { improving: boolean; percentChange: number; hasEnoughData: boolean } {
    // Exclude recovery runs — their cost reflects an infrastructure failure, not
    // normal feature complexity. Including them would inflate the cost baseline
    // after resume-and-complete sequences (Issue #1261).
    //
    // Exclude supercharge runs — they use forced Opus + max effort with no
    // budget limits, so their cost reflects an intentional quality override,
    // not a regression. Mixing them into normal-mode baselines would produce
    // false-positive cost trend degradation (Issue #2433).
    const trendHistory = this.history.filter((r) => !r.is_recovery && !r.is_supercharge);
    const recent = trendHistory.slice(0, recentCount);
    const older = trendHistory.slice(recentCount, recentCount + olderCount);

    if (recent.length < 3 || older.length < 3) {
      return { improving: true, percentChange: 0, hasEnoughData: false };
    }

    // Use median instead of mean for outlier resistance. A single anomalous
    // run (e.g. 2.5× expected cost) would skew the mean of a 5-run window
    // enough to tank the score to 0. Median ignores up to ⌊n/2⌋ outliers.
    const recentMedian = median(recent.map((r) => r.usage.costUsd));
    const olderMedian = median(older.map((r) => r.usage.costUsd));

    if (olderMedian === 0) {
      return { improving: true, percentChange: 0, hasEnoughData: false };
    }

    const percentChange = ((recentMedian - olderMedian) / olderMedian) * 100;

    // Lower cost = improving (negative percent change is good)
    return {
      improving: percentChange < 0,
      percentChange: Math.round(percentChange * 10) / 10,
      hasEnoughData: true,
    };
  }

  /**
   * Get token usage trend analysis
   *
   * @param recentCount - Number of recent runs to compare (default: 5)
   * @param olderCount - Number of older runs to compare against (default: 5)
   * @returns Object with trend direction and percent change
   */
  getTokenTrend(
    recentCount: number = 5,
    olderCount: number = 5
  ): {
    direction: "up" | "down" | "stable";
    percentChange: number;
    hasEnoughData: boolean;
  } {
    // Exclude recovery + supercharge runs so trend reflects normal-mode
    // consumption only — matches getCostTrend / getEfficiencyTrend pattern
    // (Issue #2433).
    const trendHistory = this.history.filter((r) => !r.is_recovery && !r.is_supercharge);
    const recent = trendHistory.slice(0, recentCount);
    const older = trendHistory.slice(recentCount, recentCount + olderCount);

    if (recent.length < 3 || older.length < 3) {
      return { direction: "stable", percentChange: 0, hasEnoughData: false };
    }

    // Use median for parity with getCostTrend. A single Opus blow-out run
    // would otherwise skew the mean of a 5-run window enough to flip the
    // trend direction.
    const tokensOf = (r: PipelineRunSummary): number => r.usage.inputTokens + r.usage.outputTokens;
    const recentMedian = median(recent.map(tokensOf));
    const olderMedian = median(older.map(tokensOf));

    if (olderMedian === 0) {
      return { direction: "stable", percentChange: 0, hasEnoughData: false };
    }

    const percentChange = ((recentMedian - olderMedian) / olderMedian) * 100;
    const roundedChange = Math.round(percentChange * 10) / 10;

    let direction: "up" | "down" | "stable" = "stable";
    if (Math.abs(percentChange) >= 5) {
      direction = percentChange > 0 ? "up" : "down";
    }

    return {
      direction,
      percentChange: roundedChange,
      hasEnoughData: true,
    };
  }

  /**
   * Get velocity insights from work-time feedback
   *
   * Reads complexity-model.yaml to display actual vs estimated work time.
   * Returns null if feedback file doesn't exist or no observations available.
   *
   * @param workspacePath - Workspace root path
   * @returns Velocity insights with accuracy metrics or null
   * @see Issue #310 - Work-time feedback loop
   */
  async getVelocityInsights(workspacePath: string): Promise<{
    totalObservations: number;
    avgEstimated: number;
    avgActual: number;
    accuracyPercent: number;
  } | null> {
    try {
      const feedbackPath = `${workspacePath}/.nightgauge/complexity-model.yaml`;
      const { readWorkTimeFeedback } = await import("../../utils/workTimeFeedback");
      const feedback = await readWorkTimeFeedback(feedbackPath);

      if (!feedback?.enabled || feedback.observations.length === 0) {
        return null;
      }

      // Calculate averages across all observations
      const totalEstimated = feedback.observations.reduce(
        (sum, obs) => sum + obs.estimated_minutes,
        0
      );
      const totalActual = feedback.observations.reduce(
        (sum, obs) => sum + obs.actual_work_minutes,
        0
      );

      const avgEstimated = Math.round(totalEstimated / feedback.observations.length);
      const avgActual = Math.round(totalActual / feedback.observations.length);

      // Calculate accuracy: how much over/under estimate
      // Positive = over estimate (took less time), Negative = under estimate (took more time)
      const accuracyPercent =
        avgEstimated > 0 ? Math.round(((avgEstimated - avgActual) / avgEstimated) * 100) : 0;

      return {
        totalObservations: feedback.observations.length,
        avgEstimated,
        avgActual,
        accuracyPercent,
      };
    } catch (error) {
      // Feedback not available - return null
      return null;
    }
  }

  /**
   * Get work-time accuracy trend
   *
   * Compares recent vs older observations to determine if estimates are improving.
   * Uses same pattern as getEfficiencyTrend() and getCostTrend().
   *
   * @param workspacePath - Workspace root path
   * @param recentCount - Number of recent observations to compare (default: 5)
   * @param olderCount - Number of older observations to compare against (default: 5)
   * @returns Trend analysis or null if insufficient data
   * @see Issue #310 - Work-time feedback loop
   */
  async getAccuracyTrend(
    workspacePath: string,
    recentCount: number = 5,
    olderCount: number = 5
  ): Promise<{
    improving: boolean;
    percentChange: number;
    hasEnoughData: boolean;
  } | null> {
    try {
      const feedbackPath = `${workspacePath}/.nightgauge/complexity-model.yaml`;
      const { readWorkTimeFeedback } = await import("../../utils/workTimeFeedback");
      const feedback = await readWorkTimeFeedback(feedbackPath);

      if (!feedback?.enabled || feedback.observations.length === 0) {
        return null;
      }

      // Most recent observations are at the end of the array
      const allObs = feedback.observations;
      const recent = allObs.slice(-recentCount);
      const older = allObs.slice(
        Math.max(0, allObs.length - recentCount - olderCount),
        allObs.length - recentCount
      );

      // Need at least 3 observations in each group for meaningful comparison
      if (recent.length < 3 || older.length < 3) {
        return { improving: true, percentChange: 0, hasEnoughData: false };
      }

      // Calculate average accuracy for each group
      // Accuracy = (estimated - actual) / estimated
      // Higher accuracy = closer to estimate = better
      const avgAccuracy = (
        obs: Array<{
          estimated_minutes: number;
          actual_work_minutes: number;
        }>
      ): number => {
        const validObs = obs.filter((o) => o.estimated_minutes > 0);
        if (validObs.length === 0) return 0;

        const accuracySum = validObs.reduce((sum, o) => {
          const accuracy =
            Math.abs(o.estimated_minutes - o.actual_work_minutes) / o.estimated_minutes;
          return sum + accuracy;
        }, 0);

        return accuracySum / validObs.length;
      };

      const recentAvg = avgAccuracy(recent);
      const olderAvg = avgAccuracy(older);

      // Protect against division by zero
      if (olderAvg === 0) {
        return { improving: true, percentChange: 0, hasEnoughData: false };
      }

      const percentChange = ((recentAvg - olderAvg) / olderAvg) * 100;

      // Lower accuracy deviation = improving (negative percent change is good)
      return {
        improving: percentChange < 0,
        percentChange: Math.round(percentChange * 10) / 10,
        hasEnoughData: true,
      };
    } catch (error) {
      return null;
    }
  }

  /**
   * Get complexity prediction accuracy from the model YAML's prediction_accuracy section.
   *
   * This is the fallback source for Prediction Accuracy when work-time feedback
   * observations are not yet available. The prediction_accuracy section tracks
   * how often the complexity model correctly predicted the size bucket (XS/S/M/L/XL)
   * for each completed pipeline run.
   *
   * @param workspacePath - Workspace root path
   * @returns Accuracy as a 0–100 percentage, or null if no data
   */
  async getPredictionAccuracyFromModel(
    workspacePath: string
  ): Promise<{ accuracyPercent: number; totalPredictions: number } | null> {
    try {
      const yamlPath = `${workspacePath}/.nightgauge/complexity-model.yaml`;
      const raw = await fs.readFile(yamlPath, "utf-8");
      const { load } = await import("js-yaml");
      const model = load(raw) as Record<string, unknown>;
      const pa = model?.prediction_accuracy as
        { total_predictions?: number; correct_predictions?: number } | undefined;
      if (!pa || typeof pa.total_predictions !== "number" || pa.total_predictions === 0) {
        return null;
      }
      const correct = typeof pa.correct_predictions === "number" ? pa.correct_predictions : 0;
      const accuracyPercent = Math.round((correct / pa.total_predictions) * 100);
      return { accuracyPercent, totalPredictions: pa.total_predictions };
    } catch {
      return null;
    }
  }

  // =========================================================================
  // Project Board Widget State Management (Issue #134)
  // =========================================================================

  /**
   * Load project board widget configuration from VS Code settings
   */
  private loadProjectBoardConfig(): void {
    const config = vscode.workspace.getConfiguration("nightgauge.dashboard");
    this.projectBoardConfig = {
      enabled: config.get<boolean>("projectBoard.enabled", true),
      cacheTtlMinutes: config.get<number>("projectBoard.cacheTtlMinutes", 5),
      maxReadyIssues: config.get<number>("projectBoard.maxReadyIssues", 5),
    };
  }

  /**
   * Get the project board widget configuration
   */
  getProjectBoardConfig(): ProjectBoardWidgetConfig {
    return { ...this.projectBoardConfig };
  }

  /**
   * Update project board widget configuration
   */
  setProjectBoardConfig(config: Partial<ProjectBoardWidgetConfig>): void {
    this.projectBoardConfig = { ...this.projectBoardConfig, ...config };
  }

  /**
   * Get the cached project board data
   */
  getProjectBoardData(): ProjectBoardData | null {
    return this.projectBoardData;
  }

  /**
   * Set project board data (called by Dashboard from service)
   */
  setProjectBoardData(data: ProjectBoardData | null): void {
    this.projectBoardData = data;
    this.projectBoardLastRefresh = data ? new Date() : null;
  }

  /**
   * Check if the project board cache is stale
   */
  isProjectBoardCacheStale(): boolean {
    if (!this.projectBoardLastRefresh) return true;

    const ttlMs = this.projectBoardConfig.cacheTtlMinutes * 60 * 1000;
    return Date.now() - this.projectBoardLastRefresh.getTime() > ttlMs;
  }

  /**
   * Get project board last refresh time
   */
  getProjectBoardLastRefresh(): Date | null {
    return this.projectBoardLastRefresh;
  }

  // =========================================================================
  // Health Widget Data Aggregation (Issue #655)
  // =========================================================================

  // =========================================================================
  // Applied Recommendations Tracking (Issue #787)
  // =========================================================================

  /**
   * Get applied recommendation categories from workspace storage
   */
  getAppliedRecommendations(): string[] {
    if (!this.workspaceState) return [];
    return this.workspaceState.get<string[]>(APPLIED_RECOMMENDATIONS_KEY) ?? [];
  }

  /**
   * Add a category to applied recommendations in workspace storage
   */
  async addAppliedRecommendation(category: string): Promise<void> {
    if (!this.workspaceState) return;
    const applied = this.getAppliedRecommendations();
    if (!applied.includes(category)) {
      applied.push(category);
      await this.workspaceState.update(APPLIED_RECOMMENDATIONS_KEY, applied);
    }
  }

  /**
   * Remove a category from applied recommendations in workspace storage
   */
  async removeAppliedRecommendation(category: string): Promise<void> {
    if (!this.workspaceState) return;
    const applied = this.getAppliedRecommendations().filter((c) => c !== category);
    await this.workspaceState.update(APPLIED_RECOMMENDATIONS_KEY, applied);
  }

  // =========================================================================
  // Dismissed Allowlist Suggestions (Issue #786)
  // =========================================================================

  /**
   * Get dismissed suggestion patterns from workspace storage
   */
  getDismissedSuggestions(): string[] {
    if (!this.workspaceState) return [];
    return this.workspaceState.get<string[]>(DISMISSED_SUGGESTIONS_KEY) ?? [];
  }

  /**
   * Dismiss a suggestion pattern (persist to workspace storage)
   */
  async dismissSuggestion(pattern: string): Promise<void> {
    if (!this.workspaceState) return;
    const dismissed = this.getDismissedSuggestions();
    if (!dismissed.includes(pattern)) {
      dismissed.push(pattern);
      await this.workspaceState.update(DISMISSED_SUGGESTIONS_KEY, dismissed);
    }
  }

  // =========================================================================
  // Model Routing Metrics (Issue #734)
  // =========================================================================

  /**
   * Get model routing metrics from execution history
   *
   * Reads execution history JSONL files, runs ModelPerformanceAnalyzer,
   * and returns a summary suitable for the dashboard widget.
   *
   * @returns Model routing metrics or null if no data available
   * @see Issue #734 - Learning Feedback Loop & Model Routing Report
   */
  async getModelRoutingMetrics(): Promise<ModelRoutingMetrics | null> {
    if (!this.workspaceRoot) return null;

    try {
      const historyDir = path.join(this.workspaceRoot, ".nightgauge", "pipeline", "history");

      // Read all JSONL files from history directory
      let files: string[];
      try {
        files = await fs.readdir(historyDir);
      } catch {
        return null; // No history directory
      }

      const jsonlFiles = files.filter((f) => f.endsWith(".jsonl"));
      if (jsonlFiles.length === 0) return null;

      // Parse records from JSONL files
      const records: Array<{
        stage: string;
        success: boolean;
        retries: number;
        inputTokens: number;
        outputTokens: number;
        costUsd: number;
        durationMs: number;
        timestamp: string;
        model?: string;
        modelSelectionMode?: string;
        selectedModel?: string;
        selectionSource?: string;
        autoSelectorConfidence?: number;
        autoSelectorComplexity?: string;
      }> = [];

      for (const file of jsonlFiles) {
        try {
          const content = await fs.readFile(path.join(historyDir, file), "utf-8");
          for (const line of content.split("\n")) {
            if (!line.trim()) continue;
            try {
              const record = JSON.parse(line);
              // Extract per-stage records from run records
              if (record.stages && Array.isArray(record.stages)) {
                for (const stage of record.stages) {
                  if (stage.model_selection) {
                    records.push({
                      stage: stage.stage,
                      success: stage.status === "complete",
                      retries: stage.auto_retry_count ?? 0,
                      inputTokens: stage.tokens?.input ?? 0,
                      outputTokens: stage.tokens?.output ?? 0,
                      costUsd: stage.tokens?.cost_usd ?? 0,
                      durationMs: stage.duration_ms ?? 0,
                      timestamp: stage.started_at ?? record.started_at ?? "",
                      model: stage.model_selection.model,
                      modelSelectionMode: stage.model_selection.mode,
                      selectedModel: stage.model_selection.model,
                      selectionSource: stage.model_selection.source,
                      autoSelectorConfidence: stage.model_selection.confidence,
                      autoSelectorComplexity: stage.model_selection.complexity,
                    });
                  }
                }
              }
            } catch {
              // Skip malformed lines
            }
          }
        } catch {
          // Skip unreadable files
        }
      }

      // Filter to auto-selected records
      const autoRecords = records.filter((r) => r.selectionSource === "auto");

      if (autoRecords.length === 0) return null;

      // Compute metrics
      const successCount = autoRecords.filter((r) => r.success).length;
      const totalCost = autoRecords.reduce((sum, r) => sum + r.costUsd, 0);

      // Per-stage breakdown
      const stageMap = new Map<string, { total: number; success: number; cost: number }>();
      for (const r of autoRecords) {
        const entry = stageMap.get(r.stage) ?? {
          total: 0,
          success: 0,
          cost: 0,
        };
        entry.total++;
        if (r.success) entry.success++;
        entry.cost += r.costUsd;
        stageMap.set(r.stage, entry);
      }

      const perStage: ModelRoutingStageMetric[] = [];
      for (const [stage, data] of stageMap) {
        perStage.push({
          stage,
          totalRuns: data.total,
          successRate: data.total > 0 ? data.success / data.total : 0,
          totalCostUsd: data.cost,
        });
      }

      // Confidence distribution
      const confidenceBuckets = { low: 0, medium: 0, high: 0 };
      for (const r of autoRecords) {
        const conf = r.autoSelectorConfidence ?? 0;
        if (conf >= 0.8) confidenceBuckets.high++;
        else if (conf >= 0.5) confidenceBuckets.medium++;
        else confidenceBuckets.low++;
      }

      // Model usage distribution
      const modelUsage = new Map<string, number>();
      for (const r of autoRecords) {
        const model = r.selectedModel ?? "unknown";
        modelUsage.set(model, (modelUsage.get(model) ?? 0) + 1);
      }

      return {
        totalAutoSelectedRuns: autoRecords.length,
        overallSuccessRate: autoRecords.length > 0 ? successCount / autoRecords.length : 0,
        totalCostUsd: totalCost,
        perStage,
        confidenceDistribution: confidenceBuckets,
        modelUsage: Object.fromEntries(modelUsage),
      };
    } catch {
      return null;
    }
  }

  // =========================================================================
  // Pre-Run Cost Estimate (Issue #948)
  // =========================================================================

  /** Cached pre-run cost estimate */
  private costEstimate: import("@nightgauge/sdk").PipelineCostEstimate | null = null;

  /**
   * Compute pre-run cost estimate from issue metadata using AutoModelSelector.
   *
   * Call when issue metadata is available but pipeline has not yet started.
   *
   * @param metadata - Issue metadata (labels, title, size)
   * @param skipStages - Stages to skip (e.g., from routing decisions)
   * @returns The computed cost estimate
   * @see Issue #948 - Effort-Aware Cost Estimation
   */
  async computeCostEstimate(
    metadata: import("@nightgauge/sdk").IssueMetadata,
    skipStages?: string[]
  ): Promise<import("@nightgauge/sdk").PipelineCostEstimate> {
    const { AutoModelSelector, CalibrationService } = await import("@nightgauge/sdk");
    const { getStageModelsMatrix } = await import("../../utils/incrediConfig");
    const stageMatrix = getStageModelsMatrix(this.workspaceRoot);
    const selector = new AutoModelSelector(
      stageMatrix
        ? { stageMatrix: stageMatrix as AutoModelSelectorConfig["stageMatrix"] }
        : undefined
    );
    const calibration = this.workspaceRoot
      ? await CalibrationService.load(CalibrationService.getDefaultPath(this.workspaceRoot))
      : null;
    // Issue #3216: pre-run cost estimate consults the active performance mode
    // bucket so per-mode calibration baselines (efficiency / elevated / maximum)
    // drive the displayed estimate. Falls back to elevated when the active
    // mode bucket is empty.
    let mode: import("@nightgauge/sdk").CalibrationMode = "elevated";
    if (this.workspaceRoot) {
      const { getPerformanceMode } = await import("../../utils/resolvers/monitoringResolver");
      mode = getPerformanceMode(this.workspaceRoot);
    }
    this.costEstimate = selector.estimatePipelineCost(metadata, skipStages, calibration, mode);
    return this.costEstimate;
  }

  /**
   * Get the cached pre-run cost estimate
   */
  getCostEstimate(): import("@nightgauge/sdk").PipelineCostEstimate | null {
    return this.costEstimate;
  }

  /**
   * Clear the pre-run cost estimate (e.g., when pipeline starts)
   */
  clearCostEstimate(): void {
    this.costEstimate = null;
  }

  // =========================================================================
  // Cost Summary (Issue #945)
  // =========================================================================

  /**
   * Get pipeline cost summary with model attribution for the current or given run
   *
   * Reads model data from execution history JSONL files (same source as
   * getModelRoutingMetrics) and delegates to CostSummaryCalculator.
   *
   * @param run - Pipeline run to analyze (defaults to most recent completed run)
   * @returns Cost summary or null if no data available
   * @see Issue #945 - Per-Pipeline Cost Summary
   */
  async getPipelineCostSummary(
    run?: PipelineRunSummary,
    modeFilter?: import("./CostSummaryCalculator").ModeFilter
  ): Promise<import("./CostSummaryCalculator").CostSummary | null> {
    // Accept any run with real token data — "cancelled" runs often have full
    // per-stage metrics and should be treated as valid for analytics display.
    const targetRun =
      run ??
      this.currentRun ??
      this.history.find(
        (r) => r.status === "complete" || r.usage.inputTokens + r.usage.outputTokens > 0
      );
    if (!targetRun) return null;

    // If stages lack tokenUsage (index-loaded lightweight summaries),
    // hydrate from the full JSONL record on-demand.
    const hasTokenData = targetRun.stages.some((s) => s.tokenUsage);
    if (!hasTokenData && this.telemetryStore) {
      await this.hydrateRunTokenData(targetRun);
    }

    try {
      const { calculateCostSummary } = await import("./CostSummaryCalculator");
      const stageModels = await this.getStageModelInfo(targetRun);
      return calculateCostSummary(targetRun, stageModels, undefined, undefined, modeFilter);
    } catch {
      return null;
    }
  }

  /**
   * Hydrate a lightweight run summary with per-stage token data and duration from JSONL.
   *
   * Index-loaded runs (via indexEntryToRunSummary) have no tokenUsage or durationMs on
   * their stages. This loads the full JSONL record and populates both
   * StageProgress.tokenUsage and StageProgress.durationMs so analytics and cost
   * summaries can render per-stage metrics.
   *
   * @see Issue #2577 — extended to also hydrate durationMs for getPerStageAverages()
   */
  private async hydrateRunTokenData(run: PipelineRunSummary): Promise<void> {
    if (!this.telemetryStore) return;
    try {
      const record = await this.telemetryStore.getRunRecord(run.issueNumber);
      if (!record) return;

      const perStage = record.tokens?.per_stage as
        | Record<
            string,
            {
              input: number;
              output: number;
              cache_read: number;
              cache_creation: number;
              cost_usd: number;
              model?: string;
              model_source?: string;
            }
          >
        | undefined;

      const stageRecords = record.stages as
        | Record<
            string,
            {
              duration_ms?: number;
              performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
              execution_path?: "deterministic" | "llm";
            }
          >
        | undefined;

      // Run-level performance_mode (Issue #3218) — surface for the mode-mismatch
      // advisory and the per-mode rollup when stage-level data is absent.
      const runMode = (
        record as { performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier" }
      ).performance_mode;
      if (runMode && !run.performance_mode) {
        run.performance_mode = runMode;
      }

      for (const stageProgress of run.stages) {
        // Hydrate token usage
        const tokenData = perStage?.[stageProgress.stage];
        if (tokenData) {
          stageProgress.tokenUsage = {
            stage: stageProgress.stage,
            inputTokens: tokenData.input ?? 0,
            outputTokens: tokenData.output ?? 0,
            cacheReadTokens: tokenData.cache_read ?? 0,
            cacheCreationTokens: tokenData.cache_creation ?? 0,
            costUsd: tokenData.cost_usd ?? 0,
            model: tokenData.model,
            timestamp: new Date(),
          };
        }

        // Hydrate duration from stages record (Issue #2577)
        // stages is Record<stageName, StageDetail>, keyed by stage name
        const stageRecord = stageRecords?.[stageProgress.stage];
        if (stageRecord?.duration_ms !== undefined) {
          stageProgress.durationMs = stageRecord.duration_ms;
        }
        if (stageRecord?.performance_mode) {
          stageProgress.performance_mode = stageRecord.performance_mode;
        }
        if (stageRecord?.execution_path) {
          stageProgress.execution_path = stageRecord.execution_path;
        }
      }

      // Fallback: if the JSONL record had no run-level performance_mode (pre-fix
      // Go records), derive it from stage modes — most common non-empty value.
      if (!run.performance_mode && stageRecords) {
        const counts: Record<string, number> = {};
        for (const stageProgress of run.stages) {
          const m = stageRecords[stageProgress.stage]?.performance_mode;
          if (m) counts[m] = (counts[m] ?? 0) + 1;
        }
        const dominant = (["maximum", "elevated", "efficiency"] as const).find(
          (m) => (counts[m] ?? 0) > 0
        );
        if (dominant) run.performance_mode = dominant;
      }
    } catch {
      // Non-critical — metrics will show null
    }
  }

  /**
   * Get cost history for trend visualization
   *
   * @param limit - Maximum entries to return (default: 10)
   * @returns Array of cost history entries (oldest first)
   * @see Issue #945 - Per-Pipeline Cost Summary
   */
  getCostHistory(limit: number = 10): import("./CostSummaryCalculator").CostHistoryEntry[] {
    const { calculateCostHistory } =
      require("./CostSummaryCalculator") as typeof import("./CostSummaryCalculator");
    return calculateCostHistory(this.history, limit);
  }

  /**
   * Extract per-stage model info from execution history JSONL or state
   *
   * Uses the same JSONL parsing pattern as getModelRoutingMetrics().
   */
  private async getStageModelInfo(
    run: PipelineRunSummary
  ): Promise<import("./CostSummaryCalculator").StageModelInfo[]> {
    const result: import("./CostSummaryCalculator").StageModelInfo[] = [];

    if (!this.workspaceRoot) {
      // Fallback: return default model for all stages
      for (const stage of run.stages) {
        if (stage.tokenUsage) {
          result.push({
            stage: stage.stage,
            model: "sonnet",
            effort: "medium",
            source: "fallback",
          });
        }
      }
      return result;
    }

    try {
      const historyDir = path.join(this.workspaceRoot, ".nightgauge", "pipeline", "history");

      // Try to find a JSONL record matching this run's issue number
      let files: string[];
      try {
        files = await fs.readdir(historyDir);
      } catch {
        files = [];
      }

      const jsonlFiles = files.filter((f) => f.endsWith(".jsonl"));
      let found = false;

      for (const file of jsonlFiles) {
        if (found) break;
        try {
          const content = await fs.readFile(path.join(historyDir, file), "utf-8");
          for (const line of content.split("\n")) {
            if (!line.trim()) continue;
            try {
              const record = JSON.parse(line);
              if (record.issue_number !== run.issueNumber) continue;

              // Extract model info from stages array
              if (record.stages && Array.isArray(record.stages)) {
                for (const stageRec of record.stages) {
                  if (stageRec.model_selection) {
                    result.push({
                      stage: stageRec.stage,
                      model: stageRec.model_selection.model ?? "sonnet",
                      effort: stageRec.model_selection.complexity ?? "medium",
                      source: "history",
                    });
                  }
                }
                found = true;
                break;
              }
            } catch {
              // Skip malformed lines
            }
          }
        } catch {
          // Skip unreadable files
        }
      }

      // Also try state.json for current run model_selection
      if (!found) {
        try {
          const statePath = path.join(this.workspaceRoot, ".nightgauge", "pipeline", "state.json");
          const stateContent = await fs.readFile(statePath, "utf-8");
          const state = JSON.parse(stateContent);

          if (state.issue_number === run.issueNumber && state.stages) {
            for (const [stageName, stageData] of Object.entries(
              state.stages as Record<
                string,
                { model_selection?: { model?: string; complexity?: string } }
              >
            )) {
              if (stageData?.model_selection) {
                result.push({
                  stage: stageName as PipelineStage,
                  model: stageData.model_selection.model ?? "sonnet",
                  effort: stageData.model_selection.complexity ?? "medium",
                  source: "state",
                });
              }
            }
          }
        } catch {
          // State file not available
        }
      }

      // Fill in fallback for stages with token data but no model info
      const knownStages = new Set(result.map((r) => r.stage));
      for (const stage of run.stages) {
        if (stage.tokenUsage && !knownStages.has(stage.stage)) {
          result.push({
            stage: stage.stage,
            model: "sonnet",
            effort: "medium",
            source: "fallback",
          });
        }
      }
    } catch {
      // On any error, return fallback for all stages
      for (const stage of run.stages) {
        if (stage.tokenUsage) {
          result.push({
            stage: stage.stage,
            model: "sonnet",
            effort: "medium",
            source: "fallback",
          });
        }
      }
    }

    return result;
  }

  /**
   * Get health widget data using HealthWidgetService
   *
   * Creates a HealthWidgetService instance and delegates data collection.
   * This method provides a convenient entry point for Dashboard.ts.
   *
   * @returns Complete health widget data or null if service unavailable
   * @see Issue #655 - Pipeline Health Dashboard Widget
   */
  async getHealthData(
    cacheAlertThreshold?: number,
    trendRange?: import("./HealthWidgetTypes").TrendRange,
    dependabotData?: import("../../services/DependabotPRService").DependabotPRData | null
  ): Promise<import("./HealthWidgetTypes").HealthWidgetData | null> {
    try {
      const { HealthWidgetService } = await import("./HealthWidget");
      const service = new HealthWidgetService(
        this,
        this.workspaceRoot,
        undefined,
        cacheAlertThreshold
      );
      return await service.getData(trendRange, dependabotData);
    } catch {
      return null;
    }
  }
}

/**
 * Median of a numeric series. Returns 0 for an empty input so callers can
 * branch on `=== 0` to detect the no-data case (matching the historical
 * inline implementation used by getCostTrend).
 *
 * Used by trend calculations that prefer median over mean for outlier
 * resistance — a single anomalous run shouldn't flip the trend direction.
 */
function median(values: number[]): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 0 ? (sorted[mid - 1] + sorted[mid]) / 2 : sorted[mid];
}

/**
 * Format stage name for display
 */
function formatStageName(stage: PipelineStage): string {
  const labels: Record<string, string> = {
    "pipeline-start": "Initialize",
    "issue-pickup": "Issue Pickup",
    "feature-planning": "Feature Planning",
    "feature-dev": "Feature Development",
    "feature-validate": "Feature Validation",
    "pr-create": "PR Creation",
    "pr-merge": "PR Merge",
    "pipeline-finish": "Completion",
  };
  return (
    labels[stage] ??
    stage
      .split("-")
      .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
      .join(" ")
  );
}
