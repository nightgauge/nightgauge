/**
 * Dashboard - WebView panel manager for the pipeline dashboard
 *
 * Displays pipeline metrics, token usage charts, tool call logs, and history.
 * Subscribes to PipelineStateService events for real-time updates during pipeline execution.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import { DashboardState, type ToolCallEntry } from "./DashboardState";
import {
  getDashboardHtml,
  getPipelineProgressSectionHtml,
  getSummaryCardsSectionHtml,
  getPipelineSlotsSectionHtml,
  getAnalyticsSectionHtml,
  getToolCallsHtml,
  type FirewallDashboardData,
  type AdapterStatusData,
  type UsageLimitsData,
  type PlatformQuotaData,
} from "./DashboardHtml";
import { PipelineSlotsTracker } from "./PipelineSlotsTracker";
import type {
  PipelineSlotsViewData,
  SlotCardData,
  SlotStageStatus,
  QueuedCardData,
} from "./SlotCardTypes";
import type { QueueState, ActiveSlot } from "../../types/queue";
import type { UsageLimitsService } from "../../services/UsageLimitsService";
import type { PlatformQuotaService } from "../../services/PlatformQuotaService";
import { getLimitsSettings } from "../../config/limitsSettings";
import type { HealthWidgetData } from "./HealthWidgetTypes";
import type { HealthCheckReport } from "../../types/pipelineHealth";
import { HealthWidgetService } from "./HealthWidget";
import { PipelineStateService, type PipelineState } from "../../services/PipelineStateService";
import type { BacktrackRecord, ModelEscalationRecord } from "../../schemas/pipelineState";
import { WorkspaceManager } from "../../services/WorkspaceManager";
import { SanitizationLogService } from "../../services/SanitizationLogService";
import type {
  SanitizationEventType,
  SanitizationCategory,
  TimeRangeFilter,
  TimeSeriesGranularity,
} from "./FirewallTypes";
import { ProjectBoardService } from "../../services/ProjectBoardService";
import type { IWorkItemProvider } from "../../services/types/WorkItemProvider";
import { PIPELINE_STAGE_ORDER, type PipelineStage } from "@nightgauge/sdk";
import { ProjectIterationService } from "../../services/ProjectIterationService";
import { IssueQueueService } from "../../services/IssueQueueService";
import { CompletedIssuesService } from "../../services/CompletedIssuesService";
import { RecommendationApplier } from "../../services/RecommendationApplier";
import { AllowlistSuggestionService } from "../../services/AllowlistSuggestionService";
import { IncrediYamlService } from "../settings/IncrediYamlService";
import type { ProjectBoardData, StatusCounts } from "./ProjectBoardTypes";
import type { AllowlistSuggestion } from "./FirewallTypes";
import { Logger } from "../../utils/logger";
import { getCoreSettings } from "../../config/coreSettings";
import type { Container } from "../../bootstrap/Container";
import type { TelemetryStore } from "../../services/TelemetryStore";
import { ExecutionHistoryReader } from "../../utils/executionHistoryReader";
import { ExecutionHistoryWriter } from "../../utils/executionHistoryWriter";
import { getPerformanceMode } from "../../utils/incrediConfig";
import { PERFORMANCE_MODES, type PerformanceMode as ModeProfile } from "../../utils/modeProfiles";
import {
  getCalibratedStallData,
  checkCostCapTightness,
  getCostCapWarningMultiplier,
  DEFAULT_STAGE_COST_CAPS,
  getStageCostCapUsd,
  getStageCostWarnMultiplier,
  getRunwayCeilingUsd,
} from "../../utils/resolvers/monitoringResolver";
import type { CostCapWarningRow } from "./tabs/CostTabHtml";
import type { ToolCallRecord } from "../../schemas/executionHistory";
import {
  exportAsJson,
  exportAsCsvRuns,
  exportAsCsvStages,
  type ExportFormat,
} from "../../utils/telemetryExporter";
import { AuditLogService, getDefaultAuditFilters } from "../../services/AuditLogService";
import { resolvePlatformBaseUrl } from "../../config/schema";
import { ConfigBridge } from "../../services/ConfigBridge";
import { LocalAuditFallbackService } from "../../services/LocalAuditFallbackService";
import type { AuditLogData, AuditFilterState } from "./DashboardState";
import { EventStreamService } from "../../services/EventStreamService";
import type { WorkflowEvent } from "@nightgauge/sdk";
import { TokenStorage } from "../../platform/TokenStorage";
import {
  DiscoveryActivityService,
  type DiscoveryActivityData,
} from "../../services/DiscoveryActivityService";
import { PlatformCostService, type CostDateRange } from "../../services/PlatformCostService";
import { PlatformAnalyticsHealthService } from "../../services/PlatformAnalyticsHealthService";
import { PlatformRunsService } from "../../services/PlatformRunsService";
import { PlatformTrendsService } from "../../services/PlatformTrendsService";
import { PlatformComplianceService } from "../../services/PlatformComplianceService";
import type { CostAnalyticsResult } from "../../services/IpcClientBase";
import type {
  RunsFilterState,
  RunsListData,
  RunsPaginationInfo,
  TrendsData,
  TrendsDateRange,
  ComplianceData,
  RetentionIntegrityData,
  AnalyticsHealthData,
  PlatformErrorType,
} from "./DashboardState";
import { getDefaultRunsFilters, getDefaultRunsPagination } from "./DashboardState";

/**
 * Message from WebView to extension
 */
type WebViewMessage =
  | { type: "refresh" }
  | { type: "export"; format: "json" | "csv"; target: "current" | number }
  | { type: "selectRun"; issueNumber: number }
  | { type: "setScope"; scope: "session" | "all" }
  | {
      type: "firewallFilter";
      filter: "eventType" | "category" | "timeRange" | "search";
      value: SanitizationEventType[] | SanitizationCategory[] | TimeRangeFilter | string;
    }
  | { type: "firewallResetFilters" }
  | { type: "refreshProjectBoard" }
  | { type: "selectProject"; projectName: string | null }
  | { type: "healthToggle"; collapsed: boolean }
  | { type: "healthTrendRange"; range: string }
  | {
      type: "applyRecommendation";
      category: string;
      configPath: string;
      value: unknown;
    }
  | { type: "revertRecommendation"; category: string }
  | {
      type: "firewallAddAllowlist";
      pattern: string;
      suggestionType: "allowlist" | "safe_directory";
    }
  | { type: "firewallDismissSuggestion"; pattern: string }
  | { type: "scrollPosition"; scrollY: number }
  | { type: "loadMoreHistory" }
  | {
      type: "exportAnalytics";
      format: ExportFormat;
      dateRange: "last7" | "last30" | "all";
    }
  | { type: "executeCommand"; command: string }
  | { type: "loadRunDetails"; issueNumber: number }
  | { type: "resetUsageCounter" }
  | { type: "selectTab"; tab: string }
  | { type: "auditFilter"; filters: AuditFilterState }
  | { type: "auditPageChange"; page: number }
  | { type: "auditExportCsv"; filters: AuditFilterState }
  | { type: "openSlotOutput"; slotIndex: number }
  | { type: "auditRefresh" }
  | { type: "auditRetry" }
  | { type: "auditResetFilters" }
  | { type: "discoveryRefresh" }
  | { type: "costDateRangeChange"; range: CostDateRange }
  | { type: "healthRefresh" }
  | {
      type: "setModeFilter";
      mode: "efficiency" | "elevated" | "maximum" | "frontier" | "all";
    }
  | { type: "runsFilter"; filters: RunsFilterState }
  | { type: "runsPageChange"; page: number }
  | { type: "runsExportCsv"; filters: RunsFilterState }
  | { type: "runsRefresh" }
  | { type: "runsResetFilters" }
  | { type: "trendsDateRangeChange"; range: TrendsDateRange }
  | { type: "trendsToggleComparison"; show: boolean }
  | { type: "trendsRefresh" }
  | {
      type: "complianceGenerateReport";
      reportType: string;
      startDate: string;
      endDate: string;
      format: string;
    }
  | { type: "complianceDownloadReport"; reportId: string }
  | { type: "complianceRefresh" }
  | { type: "compliancePageChange"; cursor: string }
  | { type: "retentionRefresh" }
  | { type: "retentionUpdate"; retentionDays: number }
  | { type: "retentionVerifyIntegrity"; windowDays: number }
  | { type: "mergeDependabotPR"; prNodeId: string; owner: string; repo: string }
  | { type: "dependabotRefresh" }
  | { type: "openInBrowser"; tab?: string }
  | { type: "signInWithPlatform" }
  | { type: "retryHealthTab" }
  | { type: "retryRunsTab" }
  | { type: "retryTrendsTab" }
  | { type: "retryComplianceTab" };

/**
 * Dashboard class manages the WebView panel for pipeline metrics
 *
 * @example
 * ```typescript
 * const dashboard = new Dashboard(context.extensionUri, context.workspaceState);
 *
 * // Show the dashboard
 * dashboard.show();
 *
 * // Dashboard automatically subscribes to PipelineStateService events
 * // and updates in real-time during pipeline execution
 * ```
 */
export class Dashboard implements vscode.Disposable {
  /** Delay after run completion before loading tool calls from JSONL (Issue #2578).
   *  Gives writeBackupHistoryRecord time to flush before the JSONL read fires. */
  private static readonly TOOL_CALL_PRELOAD_DELAY_MS = 500;

  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private state: DashboardState;
  private currentScope: "session" | "all" = "all";
  /** Active mode filter for cost / stall / mismatch views (Issue #3218). */
  private currentModeFilter: ModeProfile | "all" = "all";
  private pipelineStateService: PipelineStateService | null = null;
  private workspaceManager: WorkspaceManager | null = null;
  private sanitizationLogService: SanitizationLogService | null = null;
  private workspaceRoot: string | undefined;
  /** Project board service for fetching status counts (Issue #134) */
  private projectBoardService: IWorkItemProvider | null = null;
  /** Project iteration service for sprint info (Issue #134) */
  private projectIterationService: ProjectIterationService | null = null;
  /** Cached health widget data (Issue #655) */
  private healthWidgetData: HealthWidgetData | null = null;
  /** User-selected health trend range */
  private healthTrendRange: import("./HealthWidgetTypes").TrendRange | undefined;
  /** Cached model routing metrics (Issue #734) */
  private modelRoutingMetrics: import("./DashboardState").ModelRoutingMetrics | null = null;
  /** Cached cost summary (Issue #945) */
  private costSummary: import("./CostSummaryCalculator").CostSummary | null = null;
  /** Cached cost history (Issue #945) */
  private costHistory: import("./CostSummaryCalculator").CostHistoryEntry[] = [];
  /** Cached pre-run cost estimate (Issue #948) */
  private costEstimate: import("@nightgauge/sdk").PipelineCostEstimate | null = null;
  /** Recommendation applier for config patches (Issue #787) */
  private recommendationApplier: RecommendationApplier | null = null;
  /** Allowlist suggestion service (Issue #786) */
  private allowlistSuggestionService = new AllowlistSuggestionService();
  /** IncrediYaml service for config writes (Issue #786) */
  private incrediYamlService: IncrediYamlService | null = null;
  /** Cached sanitization config for synchronous render access (Issue #786) */
  private cachedAllowlist: string[] = [];
  private cachedSafeDirs: string[] = [];
  /** Cached health check report from Run Pipeline Health command (Issue #1104) */
  private healthCheckReport: HealthCheckReport | null = null;
  /** Optional usage limits service for budget tracking section (Issue #1333) */
  private usageLimitsService: UsageLimitsService | null = null;
  /** Optional platform quota service for tier quota display (Issue #1479) */
  private platformQuotaService: PlatformQuotaService | null = null;
  private auditLogService: AuditLogService | null = null;
  private auditLogData: AuditLogData | null = null;
  private auditFilters: AuditFilterState = getDefaultAuditFilters();
  /** SSE consumer for real-time audit events (Issue #3321) */
  private eventStreamService: EventStreamService | null = null;
  /** Discovery activity service for autonomous self-improvement dashboard (Issue #2434) */
  private discoveryActivityService: DiscoveryActivityService | null = null;
  private discoveryActivityData: DiscoveryActivityData | null = null;
  /** Platform cost analytics service and cached data (Issue #3317) */
  private platformCostService: PlatformCostService | null = null;
  private platformCostData: CostAnalyticsResult | null = null;
  private costDateRange: CostDateRange = "7d";
  /** Platform analytics health service and cached data (Issue #3318) */
  private platformAnalyticsHealthService: PlatformAnalyticsHealthService | null = null;
  private healthAnalyticsData: AnalyticsHealthData | null = null;
  private healthAnalyticsFetchedAt: Date | null = null;
  /** Platform runs service and cached data (Issue #3319) */
  private platformRunsService: PlatformRunsService | null = null;
  private runsData: RunsListData | null = null;
  private runsFilters: RunsFilterState = getDefaultRunsFilters();
  private runsPagination: RunsPaginationInfo = getDefaultRunsPagination();
  /** Platform trends service and cached data (Issue #3320) */
  private platformTrendsService: PlatformTrendsService | null = null;
  private trendsData: TrendsData | null = null;
  private trendsDateRange: TrendsDateRange = "30d";
  private trendsShowComparison = false;
  /** Platform compliance service and cached data (Issue #3322) */
  private platformComplianceService: PlatformComplianceService | null = null;
  private complianceData: ComplianceData | null = null;
  private compliancePollingTimer: ReturnType<typeof setTimeout> | undefined;
  private _epicRefreshTimer: ReturnType<typeof setTimeout> | null = null;
  /** Audit Retention & Integrity panel data (Issue #3323) */
  private retentionIntegrityData: RetentionIntegrityData | null = null;
  /** Dependabot PR service and cached data (Issue #3116) */
  private dependabotService:
    import("../../services/DependabotPRService").DependabotPRService | null = null;
  private dependabotData: import("../../services/DependabotPRService").DependabotPRData | null =
    null;
  /** User-selected historical run issue number for Analytics tab (Issue #2580) */
  private selectedRunIssueNumber: number | null = null;
  /** Platform runId (UUID) for the selected run — used to filter SSE pipeline events (#3714) */
  private selectedRunId: string | null = null;
  /** Debounce timer for updatePanel to coalesce rapid-fire calls */
  private updatePanelTimer: ReturnType<typeof setTimeout> | undefined;
  /** Periodic tick that refreshes the Overview Pipeline Slots cards (elapsed time) */
  private slotsTicker: ReturnType<typeof setInterval> | undefined;
  /** Per-issue runtime tracker driving the Pipeline Slots cards */
  private slotsTracker: PipelineSlotsTracker | null = null;
  /** Latest queue snapshot (queued items + status) */
  private latestQueueState: QueueState | null = null;
  /** Live active pipeline slots from ConcurrentPipelineManager.onSlotsChanged */
  private latestActiveSlots: ActiveSlot[] = [];
  /** Cached max-concurrent value from Go (refreshed on queue/state events) */
  private maxConcurrent: number = 1;
  /** Reference to the IssueQueueService set via setQueueService */
  private queueServiceRef: IssueQueueService | null = null;
  /** Guard to prevent overlapping project board refreshes (Issue #1233) */
  private boardRefreshInProgress = false;
  /** Guard to prevent overlapping metrics refreshes from concurrent event sources */
  private metricsRefreshInProgress = false;
  /** Guard to prevent duplicate health snapshots for the same pipeline run */
  private lastSnapshotIssueNumber: number | null = null;
  /** Guard: prevent duplicate execution history writes for the same issue */
  private lastHistoryWriteIssueNumber: number | null = null;

  /** DI container — used to resolve ProjectBoardService instead of creating a new instance (Issue #2771) */
  private readonly container: Container | undefined;

  // --- Diagnostic logging and render guard (Issue #780) ---
  /** Diagnostic logger for render cycle and event tracing */
  private logger = new Logger("Nightgauge Dashboard");
  /** Monotonic render counter for correlating log entries */
  private renderCounter = 0;
  /** Render-in-progress flag to prevent overlapping renders */
  private renderInProgress = false;
  /** Tracks the trigger source for the current debounced updatePanel call */
  private lastUpdateTrigger = "init";
  /** Debounce interval in ms — increased from 50ms to 150ms to absorb event bursts (Issue #780) */
  private static readonly DEBOUNCE_MS = 150;

  /**
   * Triggers that use incremental DOM updates via postMessage instead of full HTML re-render.
   * These are pipeline execution events where preserving scroll position is critical (Issue #923).
   * All other triggers use full re-render (safe default for unknown triggers).
   */
  private static readonly INCREMENTAL_TRIGGERS = new Set([
    "onStateChanged",
    "onStageStart",
    "onStageComplete",
    "onStageComplete+projectBoard",
    "onStageError",
    "onTokenUsageUpdated",
    "slot:onTokenUsageUpdated",
    "recordToolCall",
    "startRun",
    "failRun",
    "cancelRun",
    "onCompletedIssuesChanged",
    "onQueueChanged",
    "onBacktrackTriggered",
    "onBacktrackBlocked",
    "onModelEscalated",
  ]);

  /** Saved scroll Y for full re-render scroll restoration (Issue #923) */
  private savedScrollY: number | undefined;
  /** Active tab for tabbed navigation (Issue #1539) */
  private activeTab: string = "overview";
  /** Number of history items currently displayed — incremented by "Load More" (Issue #983) */
  private historyDisplayCount: number = 20;

  constructor(
    private readonly extensionUri: vscode.Uri,
    workspaceState: vscode.Memento,
    workspaceRoot?: string,
    telemetryStore?: TelemetryStore,
    container?: Container
  ) {
    this.container = container;
    this.state = new DashboardState(workspaceState, workspaceRoot, telemetryStore);
    this.workspaceRoot = workspaceRoot;
    this.historyDisplayCount = this.state.getHistoryPageSize();

    // The slots tracker listens directly to IPC; it's safe to start it
    // even before workspaceRoot is wired so that incoming pipeline events
    // are captured for whichever workspace the dashboard is later shown for.
    this.slotsTracker = new PipelineSlotsTracker();
    this.disposables.push(
      this.slotsTracker.onChanged(() => {
        this.updatePanel("onSlotsTrackerChanged");
      })
    );

    // Subscribe to PipelineStateService if workspace root is provided
    if (workspaceRoot) {
      this.subscribeToPipelineStateService(workspaceRoot);
      this.subscribeToWorkspaceManager(workspaceRoot, workspaceState);
      this.initializeSanitizationLogService(workspaceRoot);
      this.initializeProjectBoardService(workspaceRoot);
      if (this.projectBoardService instanceof ProjectBoardService) {
        this.disposables.push(
          this.projectBoardService.onStatusChanged(() => {
            if (this._epicRefreshTimer !== null) clearTimeout(this._epicRefreshTimer);
            this._epicRefreshTimer = setTimeout(() => {
              this._epicRefreshTimer = null;
              void this.state.refreshEpicEstimates().catch(() => {
                /* non-fatal */
              });
            }, 1_000);
          })
        );
      }
      this.recommendationApplier = new RecommendationApplier(workspaceRoot);
      this.incrediYamlService = new IncrediYamlService(workspaceRoot);
      this.refreshSanitizationConfigCache();
      this.discoveryActivityService = new DiscoveryActivityService(workspaceRoot);

      // Load history from TelemetryStore index (Issue #1007)
      // Runs async in background — dashboard renders immediately with Memento cache,
      // then updates when TelemetryStore index loads with complete JSONL-sourced history.
      if (telemetryStore) {
        this.loadHistoryFromTelemetryStore();
      } else {
        // Fallback: backfill from pipeline artifacts (Issue #614)
        this.backfillHistoryFromArtifacts();
      }
    }
  }

  /**
   * Reload dashboard history from TelemetryStore after a new run is recorded.
   *
   * Called externally (e.g., from the pipeline.complete IPC handler in services.ts)
   * after a run has been written to TelemetryStore. No-op if panel is not open.
   */
  async reloadHistory(): Promise<void> {
    return this.loadHistoryFromTelemetryStore();
  }

  /**
   * Load dashboard history from TelemetryStore index (Issue #1007).
   *
   * Runs async in background. Dashboard renders immediately with Memento cache,
   * then updates when TelemetryStore index loads.
   */
  private async loadHistoryFromTelemetryStore(): Promise<void> {
    const loaded = await this.state.loadFromTelemetryStore();
    if (loaded > 0 && this.panel) {
      this.updatePanel("telemetryStoreLoad");
    }
  }

  /**
   * Backfill dashboard history from pipeline run artifacts on disk
   *
   * Ensures the dashboard shows historical data even when the VSCode
   * workspace state (Memento) is empty or was cleared.
   *
   * @see Issue #614 - Backfill dashboard from existing pipeline history
   * @deprecated When TelemetryStore is available (Issue #1007), backfill
   *   delegates to loadFromTelemetryStore() automatically.
   */
  private async backfillHistoryFromArtifacts(): Promise<void> {
    const imported = await this.state.backfillFromPipelineArtifacts();
    if (imported > 0 && this.panel) {
      this.updatePanel("backfill");
    }
  }

  /**
   * Rescrub dashboard history — clears existing history and rebuilds from
   * all pipeline artifacts on disk (state files + JSONL history records).
   *
   * Useful for priming the dashboard with accurate historical token/cost data
   * after the history JSONL files have accumulated pipeline run records.
   *
   * @returns Number of runs imported
   */
  async rescrubHistory(): Promise<number> {
    const imported = await this.state.backfillFromPipelineArtifacts({
      rescrub: true,
    });
    if (this.panel) {
      await this.refreshHealthWidgetData();
      this.updatePanel("rescrub");
    }
    return imported;
  }

  /**
   * Initialize the ProjectBoardService for project board widget.
   *
   * Resolves from the DI container when available (Issue #2771), falling back to
   * direct instantiation for backward compatibility (e.g. tests, standalone use).
   *
   * @see Issue #134 - Project Board Dashboard Widget
   */
  private initializeProjectBoardService(workspaceRoot: string): void {
    if (this.container?.has("projectBoardService")) {
      this.projectBoardService = this.container.get("projectBoardService");
      this.projectIterationService = ProjectIterationService.getInstance(workspaceRoot);
      return;
    }

    const config = this.state.getProjectBoardConfig();
    const cacheTtlMs = config.cacheTtlMinutes * 60 * 1000;

    this.projectBoardService = new ProjectBoardService(workspaceRoot, cacheTtlMs);
    this.projectIterationService = ProjectIterationService.getInstance(workspaceRoot);
  }

  /**
   * Inject an IWorkItemProvider from bootstrap.
   *
   * Called by bootstrap when the provider needs to be supplied externally
   * (e.g. when the workspace root changes and a new provider is created).
   * Replaces the internally-created instance from initializeProjectBoardService.
   */
  setProjectBoardService(provider: IWorkItemProvider): void {
    this.projectBoardService = provider;
  }

  /**
   * Initialize the SanitizationLogService for firewall dashboard
   * @see Issue #387 - Prompt Injection Firewall Dashboard
   */
  private async initializeSanitizationLogService(workspaceRoot: string): Promise<void> {
    this.sanitizationLogService = new SanitizationLogService(workspaceRoot);

    // Subscribe to log changes
    const eventsChangedDisposable = this.sanitizationLogService.onEventsChanged(() => {
      // Update firewall aggregates when events change
      const filters = this.state.getFirewallFilters();
      const events = this.sanitizationLogService!.getFilteredEvents(filters);
      const aggregates = this.sanitizationLogService!.getAggregates(filters);
      this.state.setFirewallAggregates(aggregates);
      this.updatePanel("onEventsChanged");
    });

    this.disposables.push(eventsChangedDisposable);
    this.disposables.push(this.sanitizationLogService);

    // Initialize the service
    await this.sanitizationLogService.initialize();
  }

  /**
   * Refresh the cached sanitization config for synchronous render access (Issue #786).
   * Called on init and after config mutations; errors are silently ignored
   * so render remains non-blocking.
   */
  private async refreshSanitizationConfigCache(): Promise<void> {
    try {
      const result = await this.incrediYamlService?.read();
      this.cachedAllowlist = result?.config?.sanitization?.allowlist ?? [];
      this.cachedSafeDirs = result?.config?.sanitization?.safe_directories ?? [];
    } catch {
      // Config read failed — keep existing cache values
    }
  }

  /**
   * Subscribe to WorkspaceManager for multi-repo workspace events
   *
   * Refreshes cross-repo epic estimates when the active repository changes.
   * @see Issue #330 - Epic Dashboard with Cross-Repo Progress
   */
  private subscribeToWorkspaceManager(workspaceRoot: string, workspaceState: vscode.Memento): void {
    this.workspaceManager = WorkspaceManager.getInstance(workspaceRoot, workspaceState);
  }

  /**
   * Subscribe to PipelineStateService for unified state management
   *
   * This ensures the dashboard reflects the authoritative pipeline state
   * from pipeline-state.json, providing consistency across UI components.
   */
  private subscribeToPipelineStateService(workspaceRoot: string): void {
    if (this.container?.has("pipelineStateService")) {
      this.pipelineStateService = this.container.get("pipelineStateService");
    } else {
      this.pipelineStateService = PipelineStateService.getInstance(workspaceRoot);
    }

    // Subscribe to state changes
    const stateChangedDisposable = this.pipelineStateService.onStateChanged((state) => {
      this.logger.debug("event:onStateChanged", {
        issueNumber: state?.issue_number,
        hasStages: !!state?.stages,
      });
      if (state) {
        this.syncFromPipelineState(state);
      }
      this.updatePanel("onStateChanged");
    });

    // Subscribe to stage events for granular updates
    const stageStartDisposable = this.pipelineStateService.onStageStart(
      ({ stage, issueNumber }) => {
        this.logger.debug("event:onStageStart", { stage, issueNumber });
        // Start a run if not already started
        if (!this.state.getCurrentRun()) {
          const pipelineState = this.getPipelineStateSync();
          if (pipelineState) {
            this.state.startRun(
              pipelineState.issue_number,
              pipelineState.title,
              pipelineState.branch
            );
          } else {
            this.state.startRun(issueNumber, `Issue #${issueNumber}`, `feat/${issueNumber}`);
          }
        }
        this.state.setStageRunning(stage as PipelineStage);
        this.ensureSlotsTicker();
        this.updatePanel("onStageStart");
      }
    );

    const stageCompleteDisposable = this.pipelineStateService.onStageComplete(({ stage }) => {
      this.logger.debug("event:onStageComplete", { stage });
      this.state.setStageComplete(stage as PipelineStage);
      this.state.clearCurrentPhase();
      // Refresh project board when stages complete (Issue #134)
      // Issue status may have changed (e.g., pr-create moves to In Review)
      if (stage === "issue-pickup" || stage === "pr-create" || stage === "pr-merge") {
        this.refreshProjectBoardData().then(() => this.updatePanel("onStageComplete+projectBoard"));
      } else {
        this.updatePanel("onStageComplete");
      }
    });

    // Subscribe to phase events so the Overview "Running Now" widget shows
    // the active phase within a stage (e.g. feature-dev → "implementing 2/4").
    const phaseStartDisposable = this.pipelineStateService.onPhaseStart(
      ({ stage, phase, index, total }) => {
        this.logger.debug("event:onPhaseStart", { stage, phase, index, total });
        this.state.setCurrentPhase({
          stage: stage as PipelineStage,
          phase,
          index,
          total,
          startedAt: new Date(),
        });
        this.ensureSlotsTicker();
        this.updatePanel("onPhaseStart");
      }
    );

    const phaseCompleteDisposable = this.pipelineStateService.onPhaseComplete(
      ({ stage, phase }) => {
        this.logger.debug("event:onPhaseComplete", { stage, phase });
        const current = this.state.getCurrentPhase();
        if (current && current.stage === stage && current.phase === phase) {
          this.state.clearCurrentPhase();
        }
        this.updatePanel("onPhaseComplete");
      }
    );

    const stageErrorDisposable = this.pipelineStateService.onStageError(({ stage }) => {
      this.logger.debug("event:onStageError", { stage });
      this.state.setStageFailed(stage as PipelineStage);
      this.updatePanel("onStageError");
    });

    // Subscribe to unified token events (Issue #404)
    const tokenUsageDisposable = this.pipelineStateService.onTokenUsageUpdated((tokenUpdate) => {
      this.logger.debug("event:onTokenUsageUpdated", {
        stage: tokenUpdate?.stage,
      });
      // Accumulate token usage into the live dashboard run so that
      // currentRun.usage.costUsd reflects real costs at completion time.
      // Without this, the run's costUsd stays 0 and health snapshots
      // record zero cost. (Fix: health-history.jsonl always had costUsd=0)
      if (tokenUpdate?.stage) {
        this.state.recordTokenUsage({
          stage: tokenUpdate.stage as PipelineStage,
          inputTokens: tokenUpdate.inputTokens,
          outputTokens: tokenUpdate.outputTokens,
          cacheReadTokens: tokenUpdate.cacheReadTokens ?? 0,
          cacheCreationTokens: tokenUpdate.cacheCreationTokens ?? 0,
          costUsd: tokenUpdate.costUsd ?? 0,
          timestamp: new Date(),
        });
      }
      this.updatePanel("onTokenUsageUpdated");
    });

    // Subscribe to tool call events (Issue #639)
    // This bridges tool calls from HeadlessOrchestrator → PipelineStateService → Dashboard
    const toolCallDisposable = this.pipelineStateService.onToolCallRecorded((record) => {
      this.logger.debug("event:onToolCallRecorded", { tool: record.tool });
      this.recordToolCall({
        tool: record.tool,
        target: record.target,
        timestamp: record.timestamp,
        durationMs: record.durationMs,
        args: record.args,
        result: record.result,
        error: record.error,
      });
    });

    // Subscribe to feedback events (Issue #1349)
    const backtrackTriggeredDisposable = this.pipelineStateService.onBacktrackTriggered(
      (record) => {
        this.logger.debug("event:onBacktrackTriggered", { record });
        this.updatePanel("onBacktrackTriggered");
      }
    );

    const backtrackBlockedDisposable = this.pipelineStateService.onBacktrackBlocked((record) => {
      this.logger.debug("event:onBacktrackBlocked", { record });
      vscode.window.showWarningMessage(
        `⛔ Backtrack Limit Reached: ${record.signal_type} at ${record.from_stage} — ${record.rationale}. Manual review required.`
      );
      this.updatePanel("onBacktrackBlocked");
    });

    const modelEscalatedDisposable = this.pipelineStateService.onModelEscalated((record) => {
      this.logger.debug("event:onModelEscalated", { record });
      this.updatePanel("onModelEscalated");
    });

    const historyRecordedDisposable = this.pipelineStateService.onHistoryRecorded(
      ({ issueNumber }) => {
        this.logger.debug("event:onHistoryRecorded", { issueNumber });
        this.refreshAllMetrics().then(() => this.updatePanel("onHistoryRecorded"));
      }
    );

    this.disposables.push(
      stateChangedDisposable,
      stageStartDisposable,
      stageCompleteDisposable,
      stageErrorDisposable,
      phaseStartDisposable,
      phaseCompleteDisposable,
      tokenUsageDisposable,
      toolCallDisposable,
      backtrackTriggeredDisposable,
      backtrackBlockedDisposable,
      modelEscalatedDisposable,
      historyRecordedDisposable
    );
  }

  /**
   * Subscribe Dashboard to a per-slot PipelineStateService instance.
   *
   * Concurrent slots run on per-worktree PipelineStateService instances created
   * by `ConcurrentPipelineManager`'s orchestrator factory. Their stage/phase/
   * token events fire on those instances, NOT the global singleton this
   * Dashboard subscribes to in `subscribeToPipelineStateService()`. Without
   * this, the Pipeline tab's progress bar sat at 0% for the whole concurrent
   * run because `currentRun` was never started or advanced.
   *
   * Mirrors `OutputWindow.subscribeSlotToStateService()` (Issue #2979). Caller
   * disposes the returned disposable when the slot completes / fails / is
   * cleaned, alongside the OutputWindow subscription disposal.
   */
  subscribeSlotToStateService(stateService: PipelineStateService): vscode.Disposable {
    const subs: vscode.Disposable[] = [];

    subs.push(
      stateService.onStateChanged((state) => {
        this.logger.debug("slot:event:onStateChanged", {
          issueNumber: state?.issue_number,
          hasStages: !!state?.stages,
        });
        if (state) this.syncFromPipelineState(state);
        this.updatePanel("slot:onStateChanged");
      })
    );

    subs.push(
      stateService.onStageStart(({ stage, issueNumber }) => {
        this.logger.debug("slot:event:onStageStart", { stage, issueNumber });
        if (!this.state.getCurrentRun()) {
          // Pull title/branch from the per-slot state if available, otherwise
          // fall back to the issue number so the run record is at least valid.
          stateService
            .getState()
            .then((s) => {
              if (s) {
                this.state.startRun(s.issue_number, s.title, s.branch);
              } else {
                this.state.startRun(issueNumber, `Issue #${issueNumber}`, `feat/${issueNumber}`);
              }
            })
            .catch(() => {
              this.state.startRun(issueNumber, `Issue #${issueNumber}`, `feat/${issueNumber}`);
            });
        }
        this.state.setStageRunning(stage as PipelineStage);
        this.ensureSlotsTicker();
        this.updatePanel("slot:onStageStart");
      })
    );

    subs.push(
      stateService.onStageComplete(({ stage }) => {
        this.logger.debug("slot:event:onStageComplete", { stage });
        this.state.setStageComplete(stage as PipelineStage);
        this.state.clearCurrentPhase();
        if (stage === "issue-pickup" || stage === "pr-create" || stage === "pr-merge") {
          this.refreshProjectBoardData().then(() =>
            this.updatePanel("slot:onStageComplete+projectBoard")
          );
        } else {
          this.updatePanel("slot:onStageComplete");
        }
      })
    );

    subs.push(
      stateService.onStageError(({ stage }) => {
        this.logger.debug("slot:event:onStageError", { stage });
        this.state.setStageFailed(stage as PipelineStage);
        this.updatePanel("slot:onStageError");
      })
    );

    subs.push(
      stateService.onPhaseStart(({ stage, phase, index, total }) => {
        this.logger.debug("slot:event:onPhaseStart", { stage, phase, index, total });
        this.state.setCurrentPhase({
          stage: stage as PipelineStage,
          phase,
          index,
          total,
          startedAt: new Date(),
        });
        this.ensureSlotsTicker();
        this.updatePanel("slot:onPhaseStart");
      })
    );

    subs.push(
      stateService.onPhaseComplete(({ stage, phase }) => {
        this.logger.debug("slot:event:onPhaseComplete", { stage, phase });
        const current = this.state.getCurrentPhase();
        if (current && current.stage === stage && current.phase === phase) {
          this.state.clearCurrentPhase();
        }
        this.updatePanel("slot:onPhaseComplete");
      })
    );

    subs.push(
      stateService.onTokenUsageUpdated((tokenUpdate) => {
        this.logger.debug("slot:event:onTokenUsageUpdated", { stage: tokenUpdate?.stage });
        if (tokenUpdate?.stage) {
          this.state.recordTokenUsage({
            stage: tokenUpdate.stage as PipelineStage,
            inputTokens: tokenUpdate.inputTokens,
            outputTokens: tokenUpdate.outputTokens,
            cacheReadTokens: tokenUpdate.cacheReadTokens ?? 0,
            cacheCreationTokens: tokenUpdate.cacheCreationTokens ?? 0,
            costUsd: tokenUpdate.costUsd ?? 0,
            timestamp: new Date(),
          });
        }
        // Wire mid-stage token deltas into the slot card tracker so the
        // Overview slot cards show live cost/token data. pipeline.stateChanged
        // (cumulative) corrects any drift at each stage boundary.
        if (tokenUpdate?.issueNumber && this.slotsTracker) {
          this.slotsTracker.applyTokenDelta(tokenUpdate.issueNumber, {
            inputTokens: tokenUpdate.inputTokens,
            outputTokens: tokenUpdate.outputTokens,
            cacheReadTokens: tokenUpdate.cacheReadTokens,
            costUsd: tokenUpdate.costUsd,
          });
        }
        this.updatePanel("slot:onTokenUsageUpdated");
      })
    );

    return new vscode.Disposable(() => {
      for (const s of subs) s.dispose();
    });
  }

  /**
   * Start a 1 Hz ticker while at least one pipeline slot is active so the
   * Overview "Pipeline Slots" cards' elapsed-time counters stay live. The
   * tick posts only the pipeline-slots section — cheap and incremental.
   * Stops itself when no slot is active.
   */
  private ensureSlotsTicker(): void {
    if (this.slotsTicker) return;
    this.slotsTicker = setInterval(() => {
      const view = this.buildPipelineSlotsView();
      if (!view || view.slots.length === 0) {
        this.stopSlotsTicker();
        return;
      }
      if (!this.panel) return;
      this.panel.webview.postMessage({
        type: "incrementalUpdate",
        section: "pipeline-slots",
        html: getPipelineSlotsSectionHtml(view),
      });
    }, 1000);
  }

  private stopSlotsTicker(): void {
    if (this.slotsTicker) {
      clearInterval(this.slotsTicker);
      this.slotsTicker = undefined;
    }
  }

  /**
   * Build the data shape that backs the Overview tab's Pipeline Slots section.
   * Joins the per-issue runtime tracker with Go's authoritative queue state.
   */
  private buildPipelineSlotsView(): PipelineSlotsViewData {
    const queueState = this.latestQueueState;
    const tracker = this.slotsTracker;

    const activeSlots = this.latestActiveSlots;
    const slots: SlotCardData[] = activeSlots.map((active) => {
      const snapshot = tracker?.getSnapshot(active.issueNumber);
      const stages = (
        [
          "pipeline-start",
          "issue-pickup",
          "feature-planning",
          "feature-dev",
          "feature-validate",
          "pr-create",
          "pr-merge",
          "pipeline-finish",
        ] as PipelineStage[]
      ).map((stage) => {
        const entry = snapshot?.stages[stage];
        const status: SlotStageStatus = entry?.status ?? "pending";
        return {
          stage,
          status,
          durationMs: entry?.durationMs,
          inputTokens: entry?.inputTokens,
          outputTokens: entry?.outputTokens,
          costUsd: entry?.costUsd,
        };
      });
      const completedStageCount = stages.filter(
        (s) => s.status === "complete" || s.status === "skipped" || s.status === "deferred"
      ).length;

      const status: SlotCardData["status"] = snapshot?.paused
        ? "paused"
        : stages.some((s) => s.status === "failed")
          ? "failed"
          : "running";

      return {
        slotIndex: active.slotIndex,
        issueNumber: active.issueNumber,
        title: snapshot?.title ?? `Issue #${active.issueNumber}`,
        branch: active.branch ?? snapshot?.branch,
        worktreePath: active.worktreePath,
        repoName: snapshot?.repo,
        epicNumber: (active.epicNumber ?? snapshot?.currentPhase) ? undefined : active.epicNumber,
        status,
        startedAt: active.startedAt ?? snapshot?.startedAt,
        currentStage: (snapshot?.currentStage ?? (active.currentStage as PipelineStage)) as
          PipelineStage | undefined,
        currentPhase: snapshot?.currentPhase
          ? {
              name: snapshot.currentPhase.name,
              index: snapshot.currentPhase.index,
              total: snapshot.currentPhase.total,
            }
          : undefined,
        stages,
        completedStageCount,
        totalStageCount: stages.length,
        hasIssues: snapshot?.hasIssues,
        inputTokens: snapshot?.inputTokens ?? 0,
        outputTokens: snapshot?.outputTokens ?? 0,
        cacheReadTokens: snapshot?.cacheReadTokens ?? 0,
        costUsd: snapshot?.costUsd ?? 0,
      };
    });

    const activeIssueNumbers = new Set(activeSlots.map((s) => s.issueNumber));
    const queued: QueuedCardData[] = (queueState?.items ?? [])
      .filter((item) => !activeIssueNumbers.has(item.issueNumber))
      .map((item) => {
        const labels = item.labels ?? [];
        const priorityLabel = labels.find((l) => /^P[012]$/.test(l)) as
          "P0" | "P1" | "P2" | undefined;
        const blockers = (item.blockedBy ?? []).filter((b) => b.state === "OPEN");
        return {
          issueNumber: item.issueNumber,
          title: item.title,
          position: item.position,
          status: item.status === "completed" ? "ready" : item.status,
          isBlocked: blockers.length > 0,
          blockerCount: blockers.length,
          blockerNumbers: blockers.map((b) => b.number),
          labels,
          priority: priorityLabel,
          repoName: item.repoName,
          epicNumber: item.epicNumber,
          addedAt: item.addedAt,
          // Issue #3001 — surface paused-reason so the slot card can render
          // the paused-clock indicator and tooltip.
          pausedReason: item.pausedReason,
        };
      });

    return {
      maxConcurrent: this.maxConcurrent,
      queueStatus:
        activeSlots.length > 0
          ? "processing"
          : ((queueState?.status as PipelineSlotsViewData["queueStatus"]) ?? "idle"),
      slots,
      queued,
    };
  }

  /**
   * Get pipeline state synchronously (for initialization)
   */
  private getPipelineStateSync(): PipelineState | null {
    // Note: This is a workaround - in production, we'd use async/await
    // but for event handlers, we need synchronous access
    return null; // Will be populated by next state change
  }

  /**
   * Sync dashboard state from authoritative PipelineStateService
   *
   * This ensures per-stage token data is preserved even if VSCode restarts.
   * If the pipeline completed while the dashboard was closed, triggers
   * a backfill to import the completed run from disk artifacts (Issue #639).
   */
  private syncFromPipelineState(pipelineState: PipelineState): void {
    const currentRun = this.state.getCurrentRun();

    // Check if pipeline is finished: every canonical stage must be accounted
    // for — present in state.stages with a terminal status, or listed in
    // routing.skip_stages. Sparse state.stages was the trigger for issue
    // #2994: when only pipeline-start, issue-pickup, and feature-planning had
    // run, the prior `length >= 3 && every-terminal` check would fire and the
    // backup write produced an outcome="complete" record with stage_count=3
    // mid-pipeline. Compare against PIPELINE_STAGE_ORDER instead.
    const isTerminal = (s: string | undefined) =>
      s === "complete" || s === "skipped" || s === "deferred" || s === "failed";
    const skipStages = new Set(pipelineState.pipeline_meta?.skip_stages ?? []);
    const stagesMap = pipelineState.stages ?? {};
    const allStagesTerminal = PIPELINE_STAGE_ORDER.every(
      (stage) => skipStages.has(stage) || isTerminal(stagesMap[stage]?.status)
    );

    if (!currentRun && pipelineState.issue_number) {
      if (allStagesTerminal) {
        // Pipeline already completed - write backup history record, then backfill
        // from disk artifacts so health/cost/sparklines reflect the completion.
        const writePromise = this.writeBackupHistoryRecord(pipelineState);
        writePromise
          .then(() => this.backfillHistoryFromArtifacts())
          .then(() => this.refreshAllMetrics())
          .then(() => this.updatePanel("autoRefreshMetrics"));
        return;
      }
      // Pipeline still running - start tracking and reconcile completed stages (Issue #639)
      this.lastSnapshotIssueNumber = null; // Reset guard for new run
      this.lastHistoryWriteIssueNumber = null; // Reset guard for new run
      this.state.startRun(
        pipelineState.issue_number,
        pipelineState.title ?? `Issue #${pipelineState.issue_number}`,
        pipelineState.branch ?? `feat/${pipelineState.issue_number}`
      );

      // Reconcile stages that already completed while panel was closed
      if (pipelineState.stages) {
        for (const [stageName, stageData] of Object.entries(pipelineState.stages)) {
          const stage = stageName as import("@nightgauge/sdk").PipelineStage;
          if (stageData.status === "complete" || stageData.status === "deferred") {
            // Deferred stages (pr-merge awaiting human review) are terminal
            // for dashboard purposes — treat them as complete.
            this.state.setStageComplete(stage);
          } else if (stageData.status === "running") {
            this.state.setStageRunning(stage);
          } else if (stageData.status === "failed") {
            this.state.setStageFailed(stage as PipelineStage);
          } else if (stageData.status === "skipped") {
            this.state.setStageSkipped(stage);
          }
        }
      }
    }

    // Sync feedback events from authoritative pipeline state (Issue #1349)
    this.state.setBacktracks((pipelineState.backtracks ?? []) as BacktrackRecord[]);
    this.state.setModelEscalations(
      (pipelineState.model_escalations ??
        pipelineState.modelEscalations ??
        []) as unknown as ModelEscalationRecord[]
    );

    // Auto-complete the current run when all stages reach terminal state.
    // This handles the deferred-stages case where pipeline-finish is never
    // marked 'complete' but pr-merge/pipeline-finish are 'deferred'.
    if (currentRun && allStagesTerminal) {
      const hasFailure = Object.values(pipelineState.stages).some(
        (stage) => stage.status === "failed"
      );
      if (hasFailure) {
        this.state.failRun();
      } else {
        this.state.completeRun();

        // After run completes, trigger tool call load so they display in the
        // Pipeline tab without a manual "Load" click (Issue #2578).
        // Delay gives the JSONL backup write time to flush to disk.
        if (currentRun?.issueNumber) {
          setTimeout(() => {
            this.handleLoadRunDetails(currentRun.issueNumber);
          }, Dashboard.TOOL_CALL_PRELOAD_DELAY_MS);
        }
      }

      // Record health snapshot on pipeline completion (Issue #789)
      // Non-critical: wrapped in try/catch so snapshot failure never breaks pipeline
      // Cost comes from two sources: (1) live token events accumulated into
      // currentRun.usage.costUsd via recordTokenUsage(), and (2) the authoritative
      // pipeline state.json tokens.estimated_cost_usd. We prefer the pipeline state
      // value for snapshots because it's the single source of truth written by the
      // orchestrator; live token events may lag if the dashboard panel wasn't open.
      // Guard: only record once per issue to prevent duplicate snapshots from
      // repeated syncFromPipelineState calls after all stages are terminal.
      if (this.lastSnapshotIssueNumber !== currentRun.issueNumber) {
        this.lastSnapshotIssueNumber = currentRun.issueNumber;
        this.recordHealthSnapshot(currentRun, pipelineState.tokens?.estimated_cost_usd ?? 0).catch(
          () => {}
        );
      }

      // Backup execution history write: ensure JSONL record exists for this run.
      // The primary write paths (Go recordV2History + TS pipeline.complete IPC handler)
      // may not fire reliably in all execution modes. This backup guarantees the
      // dashboard's JSONL data source stays current.
      // Pass currentRun.toolCalls before completeRun() nulls this.currentRun (Issue #2578).
      this.writeBackupHistoryRecord(pipelineState, currentRun.toolCalls).catch(() => {});

      // Auto-refresh all metrics after pipeline completion (Issue #998)
      // refreshAllMetrics() is async and non-blocking; the subsequent
      // updatePanel('autoRefreshMetrics') triggers a full re-render so all
      // widget data (health, cost, sparklines, recommendations, etc.) is shown.
      this.refreshAllMetrics().then(() => this.updatePanel("autoRefreshMetrics"));
    }
  }

  /**
   * Refresh all dashboard metrics after pipeline run completion (Issue #998)
   *
   * Called automatically when a pipeline run reaches terminal state.
   * Non-blocking: errors in individual refreshes don't prevent others.
   */
  private async refreshAllMetrics(): Promise<void> {
    // Prevent overlapping refreshes from concurrent event sources
    // (e.g., syncFromPipelineState + CompletedIssuesService firing together)
    if (this.metricsRefreshInProgress) {
      this.logger.debug("refreshAllMetrics:skipped (already in progress)");
      return;
    }
    this.metricsRefreshInProgress = true;

    this.logger.debug("refreshAllMetrics:start");

    // Signal webview that metrics are refreshing (visual feedback)
    if (this.panel) {
      this.panel.webview.postMessage({
        type: "metricsRefreshing",
        active: true,
      });
    }

    try {
      // Backfill must complete first — health/cost computations read from
      // history, so running them in parallel with backfill causes stale data.
      await this.backfillHistoryFromArtifacts();
      await Promise.all([
        this.refreshHealthWidgetData(),
        this.refreshCostSummary(),
        this.refreshModelRoutingMetrics(),
        this.refreshProjectBoardData(),
      ]);
    } catch (err) {
      this.logger.debug("refreshAllMetrics:error", { error: String(err) });
    } finally {
      this.metricsRefreshInProgress = false;
      if (this.panel) {
        this.panel.webview.postMessage({
          type: "metricsRefreshing",
          active: false,
        });
      }
      this.logger.debug("refreshAllMetrics:complete");
    }
  }

  /**
   * Record a health score snapshot after pipeline completion (Issue #789)
   *
   * Creates a HealthWidgetService to compute and persist the snapshot.
   * Non-critical: errors are caught silently.
   */
  private async recordHealthSnapshot(
    run: import("./DashboardState").PipelineRunSummary,
    costUsd?: number
  ): Promise<void> {
    try {
      const service = new HealthWidgetService(this.state, this.workspaceRoot);
      // Prefer authoritative cost from pipeline state; fall back to run usage
      const cost = costUsd ?? run.usage.costUsd;
      await service.recordSnapshot(run.issueNumber, cost);
    } catch {
      // Non-critical — snapshot failure must not break pipeline
    }
  }

  /**
   * Record a health snapshot for a completed pipeline run.
   *
   * Public entry point for the pipeline.complete IPC handler and
   * concurrent slot completion callback. The private recordHealthSnapshot()
   * requires a PipelineRunSummary, but callers outside Dashboard only have
   * issueNumber + costUsd. This method writes directly to health-history.jsonl
   * via HealthWidgetService.
   *
   * Must be called AFTER reloadHistory() so DashboardState has the latest
   * run data for health score computation.
   *
   * @see Issue #2245 — health snapshots missing for concurrent pipeline runs
   */
  async recordHealthSnapshotForRun(issueNumber: number, costUsd: number): Promise<void> {
    try {
      const service = new HealthWidgetService(this.state, this.workspaceRoot);
      await service.recordSnapshot(issueNumber, costUsd);

      // Refresh health widget data and trigger a panel update so the
      // dashboard shows the new data point without manual refresh.
      // updatePanel is debounced and preserves scroll position (Issue #923).
      await this.refreshHealthWidgetData();
      this.updatePanel("healthSnapshotRecorded");
    } catch {
      // Non-critical — snapshot failure must not break pipeline
    }
  }

  /**
   * Show the dashboard WebView panel
   *
   * If a panel already exists, it will be revealed.
   * Otherwise, a new panel is created.
   */
  show(): void {
    // If we already have a panel, reveal it
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One);
      this.updatePanel("show:reveal");
      // Refresh platform quota on re-open (Issue #1479)
      this.platformQuotaService
        ?.fetchAndCache()
        .then(() => this.updatePanel("show:platformQuota"))
        .catch(() => {});
      return;
    }

    // Create the WebView panel
    this.panel = vscode.window.createWebviewPanel(
      "incrediDashboard",
      "Nightgauge Dashboard",
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "src", "views", "dashboard")],
      }
    );

    // Set initial content
    this.updatePanel("show:initial");

    // Fetch project board data in background (Issue #134)
    this.refreshProjectBoardData().then(() => this.updatePanel("show:projectBoard"));

    // Fetch health widget data in background (Issue #655)
    this.refreshHealthWidgetData().then(() => this.updatePanel("show:healthWidget"));

    // Fetch model routing metrics in background (Issue #734)
    this.refreshModelRoutingMetrics().then(() => this.updatePanel("show:modelRouting"));

    // Fetch cost summary in background (Issue #945)
    this.refreshCostSummary().then(() => this.updatePanel("show:costSummary"));

    // Compute pre-run cost estimate in background (Issue #948)
    this.refreshCostEstimate().then(() => this.updatePanel("show:costEstimate"));

    // Pre-fetch discovery activity data on open (Issue #2579)
    this.refreshDiscoveryActivityData()
      .then(() => this.updatePanel("show:discoveryActivity"))
      .catch(() => {});

    // Fetch platform quota on dashboard open (Issue #1479)
    this.platformQuotaService
      ?.fetchAndCache()
      .then(() => this.updatePanel("show:platformQuota"))
      .catch(() => {
        // fetchAndCache handles errors internally and returns stale data
      });

    // Handle messages from the WebView
    this.panel.webview.onDidReceiveMessage(
      (message: WebViewMessage) => this.handleMessage(message),
      undefined,
      this.disposables
    );

    // Handle panel disposal
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
  }

  /**
   * Update the panel content (debounced to coalesce rapid-fire calls)
   *
   * Routes to incremental DOM update for pipeline events (preserves scroll)
   * or full HTML re-render for everything else (Issue #923).
   *
   * @param trigger - Diagnostic label identifying what caused this update (Issue #780)
   */
  private updatePanel(trigger = "unknown"): void {
    if (!this.panel) return;

    this.lastUpdateTrigger = trigger;

    const useIncremental = Dashboard.INCREMENTAL_TRIGGERS.has(trigger);

    // Debounce: coalesce multiple rapid calls into one render
    if (this.updatePanelTimer) {
      clearTimeout(this.updatePanelTimer);
      this.logger.debug("updatePanel:coalesced", { trigger, useIncremental });
    } else {
      this.logger.debug("updatePanel:scheduled", { trigger, useIncremental });
    }
    this.updatePanelTimer = setTimeout(
      () => (useIncremental ? this.postIncrementalUpdate() : this.renderPanel()),
      Dashboard.DEBOUNCE_MS
    );
  }

  /**
   * Render the panel content (called by debounced updatePanel)
   *
   * Includes a render-in-progress guard to prevent overlapping renders
   * and structured diagnostic logging for every render cycle (Issue #780).
   */
  private renderPanel(): void {
    if (!this.panel) return;

    // Render guard: skip if a render is already in progress (Issue #780)
    if (this.renderInProgress) {
      this.logger.debug("renderPanel:skipped (render in progress)", {
        trigger: this.lastUpdateTrigger,
      });
      // Re-schedule so the latest data is eventually rendered
      this.updatePanelTimer = setTimeout(() => this.renderPanel(), Dashboard.DEBOUNCE_MS);
      return;
    }

    this.renderInProgress = true;
    const renderId = ++this.renderCounter;
    const renderStart = performance.now();

    this.logger.debug("renderPanel:start", {
      renderId,
      trigger: this.lastUpdateTrigger,
    });

    try {
      // Mark refresh timestamp for "Last updated" display (Issue #614)
      this.state.markRefreshed();

      const currentRun = this.state.getCurrentRun();
      const fullHistory = this.state.getHistory();
      const aggregates = this.state.getAggregates(this.currentScope);
      const timeSavingsConfig = this.state.getTimeSavingsConfig();

      // Paginate history for display (Issue #983)
      const historyPage = this.state.getHistoryPage(0, this.historyDisplayCount);
      const history = historyPage.items;

      const dataGatherMs = performance.now() - renderStart;

      // Get firewall data if service is available
      let firewallData: FirewallDashboardData | undefined;
      if (this.sanitizationLogService) {
        const filters = this.state.getFirewallFilters();
        const events = this.sanitizationLogService.getFilteredEvents(filters);
        const firewallAggregates = this.sanitizationLogService.getAggregates(filters);
        const granularity: TimeSeriesGranularity =
          filters.timeRange === "hour" || filters.timeRange === "24h" ? "hour" : "day";
        const timeSeriesData = this.sanitizationLogService.getTimeSeriesData(filters, granularity);

        // Generate allowlist suggestions using cached config (Issue #786)
        const dismissedPatterns = this.state.getDismissedSuggestions();
        const suggestions = this.allowlistSuggestionService.generateSuggestions(
          this.sanitizationLogService!.getEvents(),
          this.cachedAllowlist,
          this.cachedSafeDirs,
          dismissedPatterns
        );

        firewallData = {
          events,
          filters,
          aggregates: firewallAggregates,
          timeSeriesData,
          granularity,
          suggestions,
        };
      }

      // Get project board data
      const projectBoardData = this.state.getProjectBoardData();

      const htmlGenStart = performance.now();

      // Merge in-memory + persisted applied recommendation categories (Issue #787)
      const appliedCategories = [
        ...new Set([
          ...(this.recommendationApplier?.getAppliedCategories() ?? []),
          ...this.state.getAppliedRecommendations(),
        ]),
      ];

      // Capture scroll position to restore after full re-render (Issue #923)
      const scrollYToRestore = this.savedScrollY;
      this.savedScrollY = undefined;

      // Get PTC metrics from most recent history record if available (Issue #1071)
      let ptcMetricsData: import("./DashboardHtml").PTCMetricsDisplayData | null = null;
      if (fullHistory.length > 0) {
        const latestRun = fullHistory[0];
        const pm = (latestRun as { tokens?: { ptc_metrics?: Record<string, number> } }).tokens
          ?.ptc_metrics;
        if (pm) {
          ptcMetricsData = {
            totalToolCalls: pm.total_tool_calls ?? 0,
            programmaticCalls: pm.programmatic_calls ?? 0,
            directCalls: pm.direct_calls ?? 0,
            programmaticRatio: pm.programmatic_ratio ?? 0,
            estimatedTokensSaved: pm.estimated_tokens_saved ?? 0,
            codeExecutionCount: pm.code_execution_count ?? 0,
            containerReuseCount: pm.container_reuse_count ?? 0,
          };
        }
      }

      // Compute adapter status data (Issue #1056)
      const adapterStatusData = this.getAdapterStatusData();

      // Issue #3218 — mode-aware data for header chip, cost rollup, stall table.
      const perModeRollup = this.computePerModeRollup();
      const stallThresholdRows = this.buildStallThresholdRows();
      const modeMismatchAdvisory = this.computeModeMismatchAdvisory();
      // Issue #3276 — cost cap tightness warning rows
      const costCapWarningRows = this.buildCostCapWarningRows();
      // Issue #3269 — budget vs actual stats
      const budgetVsActualStats = this.computeBudgetVsActualStats();

      this.panel.webview.html = getDashboardHtml(
        this.panel.webview,
        currentRun,
        history,
        aggregates,
        timeSavingsConfig,
        this.currentScope,
        firewallData,
        projectBoardData,
        this.healthWidgetData,
        this.modelRoutingMetrics,
        appliedCategories,
        this.costSummary,
        this.costHistory,
        this.costEstimate,
        { totalCount: historyPage.total, hasMore: historyPage.hasMore },
        ptcMetricsData,
        adapterStatusData,
        this.healthCheckReport,
        this.state.getCurrentRunBacktracks(),
        this.state.getCurrentRunModelEscalations(),
        this.getUsageLimitsData(),
        this.activeTab,
        this.getPlatformQuotaData(),
        this.auditLogData,
        this.discoveryActivityData,
        this.buildPipelineSlotsView(),
        this.currentModeFilter,
        perModeRollup,
        stallThresholdRows,
        modeMismatchAdvisory,
        costCapWarningRows,
        budgetVsActualStats,
        this.platformCostData,
        this.costDateRange,
        this.healthAnalyticsData,
        this.healthAnalyticsFetchedAt,
        this.runsData,
        this.trendsData,
        this.complianceData,
        this.retentionIntegrityData,
        this.dependabotData
      );

      // Restore scroll position if we had one saved (Issue #923)
      if (scrollYToRestore !== undefined && scrollYToRestore > 0) {
        this.panel.webview.postMessage({
          type: "restoreScrollPosition",
          scrollY: scrollYToRestore,
        });
      }

      const htmlGenMs = performance.now() - htmlGenStart;
      const totalMs = performance.now() - renderStart;

      this.logger.debug("renderPanel:complete (no Chart.js)", {
        renderId,
        dataGatherMs: Math.round(dataGatherMs * 100) / 100,
        htmlGenMs: Math.round(htmlGenMs * 100) / 100,
        totalMs: Math.round(totalMs * 100) / 100,
        historyLength: history.length,
        hasCurrentRun: !!currentRun,
        hasFirewall: !!firewallData,
        stageCount: currentRun?.stages.length ?? 0,
        hasHealth: !!this.healthWidgetData,
        scrollRestored: scrollYToRestore !== undefined && scrollYToRestore > 0,
      });
    } finally {
      this.renderInProgress = false;
    }
  }

  /**
   * Post incremental DOM updates via postMessage (Issue #923).
   *
   * Instead of replacing the entire HTML document (which destroys scroll position),
   * this method sends section-level HTML updates to the webview. The webview's
   * message handler patches only the targeted DOM sections via innerHTML.
   *
   * Uses the same renderInProgress guard and debounce as renderPanel().
   */
  private postIncrementalUpdate(): void {
    if (!this.panel) return;

    if (this.renderInProgress) {
      this.logger.debug("postIncrementalUpdate:skipped (render in progress)", {
        trigger: this.lastUpdateTrigger,
      });
      this.updatePanelTimer = setTimeout(() => this.postIncrementalUpdate(), Dashboard.DEBOUNCE_MS);
      return;
    }

    this.renderInProgress = true;
    const renderId = ++this.renderCounter;
    const renderStart = performance.now();

    this.logger.debug("postIncrementalUpdate:start", {
      renderId,
      trigger: this.lastUpdateTrigger,
    });

    try {
      this.state.markRefreshed();

      const currentRun = this.state.getCurrentRun();
      const history = this.state.getHistory();
      const aggregates = this.state.getAggregates(this.currentScope);
      const timeSavingsConfig = this.state.getTimeSavingsConfig();
      const displayRun = currentRun || (history.length > 0 ? history[0] : null);

      // Send incremental updates for each section
      if (currentRun) {
        this.panel.webview.postMessage({
          type: "incrementalUpdate",
          section: "pipeline-progress",
          html: getPipelineProgressSectionHtml(
            currentRun,
            this.state.getCurrentRunBacktracks(),
            this.state.getCurrentRunModelEscalations()
          ),
        });
      }

      this.panel.webview.postMessage({
        type: "incrementalUpdate",
        section: "pipeline-slots",
        html: getPipelineSlotsSectionHtml(this.buildPipelineSlotsView()),
      });

      this.panel.webview.postMessage({
        type: "incrementalUpdate",
        section: "summary-cards",
        html: getSummaryCardsSectionHtml(aggregates, this.currentScope),
      });

      this.panel.webview.postMessage({
        type: "incrementalUpdate",
        section: "analytics",
        html: getAnalyticsSectionHtml(
          this.costSummary,
          this.costHistory ?? [],
          displayRun,
          timeSavingsConfig,
          aggregates.stageAverages,
          history,
          aggregates.costPerIssue ?? [],
          null
        ),
      });

      // Request scroll position so it's available if a full re-render follows (Issue #923)
      this.panel.webview.postMessage({ type: "requestScrollPosition" });

      const totalMs = performance.now() - renderStart;
      this.logger.debug("postIncrementalUpdate:complete", {
        renderId,
        totalMs: Math.round(totalMs * 100) / 100,
        hasCurrentRun: !!currentRun,
      });
    } finally {
      this.renderInProgress = false;
    }
  }

  /**
   * Handle messages from the WebView
   */
  private async handleMessage(message: WebViewMessage): Promise<void> {
    switch (message.type) {
      case "refresh":
        // Reset pagination on refresh (Issue #983)
        this.historyDisplayCount = this.state.getHistoryPageSize();
        // Refresh ALL widget data (Issue #639)
        await Promise.all([
          this.refreshProjectBoardData(),
          this.backfillHistoryFromArtifacts(),
          this.refreshHealthWidgetData(),
          this.refreshModelRoutingMetrics(),
          this.refreshCostSummary(),
        ]);
        // Re-initialize firewall data if service is available
        if (this.sanitizationLogService) {
          await this.sanitizationLogService.initialize();
        }
        this.updatePanel("msg:refresh");
        break;

      case "resetUsageCounter":
        // Issue #1333 — triggered by the Reset Counter button in the Usage & Limits section
        vscode.commands.executeCommand("nightgauge.resetUsageCounter");
        this.updatePanel("msg:resetUsageCounter");
        break;

      case "openSlotOutput":
        // Pipeline Slots card click — reveal the per-slot output channel
        vscode.commands.executeCommand("nightgauge-pipeline.showSlotOutput", {
          slotIndex: message.slotIndex,
        });
        break;

      case "export":
        this.handleExport(message.format, message.target);
        break;

      case "selectRun":
        this.handleSelectRun(message.issueNumber);
        break;

      case "loadRunDetails":
        await this.handleLoadRunDetails(message.issueNumber);
        break;

      case "setScope":
        this.currentScope = message.scope;
        this.historyDisplayCount = this.state.getHistoryPageSize();
        this.updatePanel("msg:setScope");
        break;

      case "setModeFilter":
        // Issue #3218 — mode-filter chip in dashboard header. Default `"all"`
        // preserves existing behavior; concrete modes scope cost rollup,
        // per-mode card, and the active-mode badge in the stall threshold table.
        this.currentModeFilter = message.mode;
        this.updatePanel("msg:setModeFilter");
        break;

      case "loadMoreHistory":
        this.historyDisplayCount += this.state.getHistoryPageSize();
        this.updatePanel("msg:loadMoreHistory");
        break;

      case "firewallFilter":
        this.handleFirewallFilter(message.filter, message.value);
        break;

      case "firewallResetFilters":
        this.state.resetFirewallFilters();
        this.updatePanel("msg:firewallResetFilters");
        break;

      case "refreshProjectBoard":
        await this.refreshProjectBoardData();
        this.updatePanel("msg:refreshProjectBoard");
        break;

      case "selectProject":
        // Multi-project mode: switch project selection
        // setSelectedProject is ProjectBoardService-specific (not on IWorkItemProvider)
        if (this.projectBoardService instanceof ProjectBoardService) {
          this.projectBoardService.setSelectedProject(message.projectName ?? "");
          await this.refreshProjectBoardData();
          this.updatePanel("msg:selectProject");
        }
        break;

      case "healthToggle":
        // Health widget collapse/expand toggle (Issue #655)
        // Persist state so widget remembers collapse across refreshes
        break;

      case "healthTrendRange": {
        // User changed the health trend range selector
        const validRanges = ["24h", "7d", "30d", "90d"];
        if (validRanges.includes(message.range)) {
          this.healthTrendRange = message.range as import("./HealthWidgetTypes").TrendRange;
          // Capture scroll position before the full re-render so the user
          // doesn't lose their place when the page rebuilds (Issue #923).
          if (this.panel) {
            this.panel.webview.postMessage({ type: "requestScrollPosition" });
            // Brief delay to let the webview respond with scroll position
            await new Promise((resolve) => setTimeout(resolve, 50));
          }
          await this.refreshHealthWidgetData();
          this.updatePanel("msg:healthTrendRange");
        }
        break;
      }

      case "applyRecommendation":
        // Apply a recommendation config patch (Issue #787)
        if (this.recommendationApplier) {
          const applyResult = await this.recommendationApplier.apply(
            message.category,
            message.configPath,
            message.value
          );
          if (applyResult.success) {
            await this.state.addAppliedRecommendation(message.category);
            vscode.window.showInformationMessage(
              `Applied recommendation: ${message.category}. You can revert within 30 seconds.`
            );
          } else {
            vscode.window.showErrorMessage(`Failed to apply recommendation: ${applyResult.error}`);
          }
          this.updatePanel("msg:applyRecommendation");
        }
        break;

      case "revertRecommendation":
        // Revert a previously applied recommendation (Issue #787)
        if (this.recommendationApplier) {
          const revertResult = await this.recommendationApplier.revert(message.category);
          if (revertResult.success) {
            await this.state.removeAppliedRecommendation(message.category);
            vscode.window.showInformationMessage(`Reverted recommendation: ${message.category}`);
          } else {
            vscode.window.showErrorMessage(`Failed to revert: ${revertResult.error}`);
          }
          this.updatePanel("msg:revertRecommendation");
        }
        break;

      case "firewallAddAllowlist":
        // Add a suggested pattern to config.yaml (Issue #786)
        if (
          this.incrediYamlService &&
          typeof message.pattern === "string" &&
          message.pattern.length > 0 &&
          message.pattern.length <= 500 &&
          (message.suggestionType === "allowlist" || message.suggestionType === "safe_directory")
        ) {
          // Validate safe_directory paths are workspace-relative
          if (
            message.suggestionType === "safe_directory" &&
            (!message.pattern.startsWith("./") || message.pattern.includes(".."))
          ) {
            break;
          }
          const addResult =
            message.suggestionType === "safe_directory"
              ? await this.incrediYamlService.addToSanitizationSafeDirectories(message.pattern)
              : await this.incrediYamlService.addToSanitizationAllowlist(message.pattern);
          if (addResult.success) {
            vscode.window.showInformationMessage(
              `Added to ${message.suggestionType === "safe_directory" ? "safe_directories" : "allowlist"}: ${message.pattern}`
            );
          } else {
            vscode.window.showErrorMessage(`Failed to update config: ${addResult.error}`);
          }
          await this.refreshSanitizationConfigCache();
          this.updatePanel("msg:firewallAddAllowlist");
        }
        break;

      case "firewallDismissSuggestion":
        // Dismiss a suggestion (Issue #786)
        if (
          typeof message.pattern === "string" &&
          message.pattern.length > 0 &&
          message.pattern.length <= 500
        ) {
          await this.state.dismissSuggestion(message.pattern);
          this.updatePanel("msg:firewallDismissSuggestion");
        }
        break;

      case "exportAnalytics":
        this.handleExportAnalytics(message.format, message.dateRange);
        break;

      case "executeCommand":
        // Only allow nightgauge commands from webview
        if (message.command.startsWith("nightgauge.")) {
          vscode.commands.executeCommand(message.command);
        }
        break;

      case "selectTab": {
        // Webview reports active tab for server-side pre-rendering (Issue #1539)
        const VALID_TABS = [
          "overview",
          "pipeline",
          "analytics",
          "history",
          "audit",
          "discovery",
          "cost",
          "health",
          "runs",
          "trends",
          "compliance",
          "dependencies",
        ];
        if (VALID_TABS.includes(message.tab)) {
          this.activeTab = message.tab;
          // Lazy-load audit data on first tab activation (ADR-001)
          if (message.tab === "audit" && this.auditLogData === null) {
            this.refreshAuditLogData().catch(() => {});
          }
          // Lazy-load discovery data on first tab activation (Issue #2434)
          // Only load when null — prevents duplicate fetches on re-click (Issue #2582)
          if (message.tab === "discovery" && this.discoveryActivityData === null) {
            this.refreshDiscoveryActivityData().catch(() => {});
          }
          // Lazy-load cost analytics on first activation (Issue #3317)
          if (message.tab === "cost" && this.platformCostData === null) {
            this.refreshCostData().catch(() => {});
          }
          // Lazy-load health analytics on first activation (Issue #3318)
          if (message.tab === "health" && this.healthAnalyticsData === null) {
            this.refreshHealthAnalyticsData().catch(() => {});
          }
          // Lazy-load runs data on first activation (Issue #3319)
          if (message.tab === "runs" && this.runsData === null) {
            this.refreshRunsData().catch(() => {});
          }
          // Lazy-load trends data on first activation (Issue #3320)
          if (message.tab === "trends" && this.trendsData === null) {
            this.fetchTrendsData().catch(() => {});
          }
          // Lazy-load compliance data on first activation (Issue #3322)
          if (message.tab === "compliance" && this.complianceData === null) {
            this.refreshComplianceData().catch(() => {});
          }
          // Lazy-load retention & integrity data on first audit tab activation (Issue #3323)
          if (message.tab === "audit" && this.retentionIntegrityData === null) {
            this.refreshRetentionData().catch(() => {});
          }
          // Lazy-load dependabot PRs on first activation (Issue #3116)
          if (message.tab === "dependencies" && this.dependabotData === null) {
            this.refreshDependabotData().catch(() => {});
          }
        }
        break;
      }

      case "auditFilter":
        this.refreshAuditLogData(message.filters, 0).catch(() => {});
        break;

      case "auditPageChange":
        this.refreshAuditLogData(this.auditFilters, message.page).catch(() => {});
        break;

      case "auditExportCsv":
        this.exportAuditCsv(message.filters).catch(() => {});
        break;

      case "auditRefresh":
        this.refreshAuditLogData().catch(() => {});
        break;

      case "auditRetry":
        // Re-attempt platform fetch; LocalAuditFallbackService activates automatically on failure
        this.auditLogData = this.auditLogData ? { ...this.auditLogData, isLoading: true } : null;
        this.updatePanel("auditRetry");
        this.refreshAuditLogData(this.auditFilters, this.auditLogData?.pagination.page ?? 0).catch(
          () => {}
        );
        break;

      case "auditResetFilters":
        this.auditFilters = getDefaultAuditFilters();
        this.refreshAuditLogData(this.auditFilters, 0).catch(() => {});
        break;

      case "discoveryRefresh":
        this.refreshDiscoveryActivityData().catch(() => {});
        break;

      case "costDateRangeChange":
        this.costDateRange = message.range;
        this.platformCostData = null; // invalidate cache for new range
        this.updatePanel("costDateRangeChange");
        this.refreshCostData().catch(() => {});
        break;

      case "healthRefresh":
        this.refreshHealthAnalyticsData().catch(() => {});
        break;

      case "runsFilter":
        this.runsFilters = message.filters;
        this.runsPagination = getDefaultRunsPagination();
        this.runsData = null;
        this.refreshRunsData().catch(() => {});
        break;

      case "runsPageChange": {
        const targetPage = message.page;
        const cursor = targetPage > 0 ? this.runsPagination.cursorStack[targetPage] : undefined;
        this.runsPagination = { ...this.runsPagination, page: targetPage };
        this.refreshRunsData(cursor).catch(() => {});
        break;
      }

      case "runsExportCsv":
        this.exportRunsCsv(message.filters).catch(() => {});
        break;

      case "runsRefresh":
        this.runsData = null;
        this.runsPagination = getDefaultRunsPagination();
        this.refreshRunsData().catch(() => {});
        break;

      case "runsResetFilters":
        this.runsFilters = getDefaultRunsFilters();
        this.runsPagination = getDefaultRunsPagination();
        this.runsData = null;
        this.refreshRunsData().catch(() => {});
        break;

      case "trendsDateRangeChange":
        if (message.range === "30d" || message.range === "90d" || message.range === "180d") {
          this.trendsDateRange = message.range;
          this.trendsData = null;
          this.fetchTrendsData().catch(() => {});
        }
        break;

      case "trendsToggleComparison":
        this.trendsShowComparison = message.show;
        if (this.trendsData) {
          this.trendsData = { ...this.trendsData, showComparison: message.show };
        }
        this.updatePanel("trendsToggleComparison");
        break;

      case "trendsRefresh":
        this.trendsData = null;
        this.fetchTrendsData().catch(() => {});
        break;

      case "complianceRefresh":
        this.complianceData = null;
        this.refreshComplianceData().catch(() => {});
        break;

      case "complianceGenerateReport":
        this.generateComplianceReport(
          message.reportType,
          message.startDate,
          message.endDate,
          message.format
        ).catch(() => {});
        break;

      case "complianceDownloadReport":
        this.downloadComplianceReport(message.reportId).catch(() => {});
        break;

      case "compliancePageChange":
        this.refreshComplianceData(message.cursor || undefined).catch(() => {});
        break;

      case "retentionRefresh":
        this.refreshRetentionData().catch(() => {});
        break;

      case "retentionUpdate":
        this.updateRetention(message.retentionDays).catch(() => {});
        break;

      case "retentionVerifyIntegrity":
        this.verifyIntegrity(message.windowDays).catch(() => {});
        break;

      case "dependabotRefresh":
        this.dependabotData = null;
        this.dependabotService?.invalidate();
        this.refreshDependabotData().catch(() => {});
        break;

      case "mergeDependabotPR":
        this.mergeDependabotPR(message.owner, message.repo, message.prNodeId).catch(() => {});
        break;

      case "signInWithPlatform":
        await vscode.commands.executeCommand("nightgauge.signIn");
        break;

      case "retryHealthTab":
        await this.refreshHealthAnalyticsData();
        break;

      case "retryRunsTab":
        await this.refreshRunsData();
        break;

      case "retryTrendsTab":
        await this.fetchTrendsData();
        break;

      case "retryComplianceTab":
        await this.refreshComplianceData();
        break;

      case "openInBrowser": {
        const tab = typeof message.tab === "string" ? message.tab : undefined;
        await vscode.commands.executeCommand("nightgauge.openCurrentTabInBrowser", tab);
        break;
      }

      case "scrollPosition":
        // Webview reports its scroll position before a full re-render (Issue #923)
        if (Number.isFinite(message.scrollY) && message.scrollY >= 0) {
          this.savedScrollY = message.scrollY;
        }
        break;
    }
  }

  /**
   * Handle firewall filter changes
   * @see Issue #387 - Prompt Injection Firewall Dashboard
   */
  private handleFirewallFilter(
    filter: "eventType" | "category" | "timeRange" | "search",
    value: SanitizationEventType[] | SanitizationCategory[] | TimeRangeFilter | string
  ): void {
    const currentFilters = this.state.getFirewallFilters();

    switch (filter) {
      case "eventType":
        this.state.setFirewallFilters({
          ...currentFilters,
          eventTypes: value as SanitizationEventType[],
        });
        break;
      case "category":
        this.state.setFirewallFilters({
          ...currentFilters,
          categories: value as SanitizationCategory[],
        });
        break;
      case "timeRange":
        this.state.setFirewallFilters({
          ...currentFilters,
          timeRange: value as TimeRangeFilter,
        });
        break;
      case "search":
        this.state.setFirewallFilters({
          ...currentFilters,
          searchText: value as string,
        });
        break;
    }

    this.updatePanel("msg:firewallFilter");
  }

  /**
   * Handle export request
   */
  private async handleExport(format: "json" | "csv", target: "current" | number): Promise<void> {
    // Get the run to export
    let run = this.state.getCurrentRun();
    if (target !== "current" && typeof target === "number") {
      run = this.state.getHistoryRun(target) || null;
    }

    if (!run) {
      vscode.window.showWarningMessage("No pipeline data to export.");
      return;
    }

    // For history runs, enrich stage token data from the full JSONL record (#2794).
    // History runs loaded from the TelemetryStore index lack per-stage tokenUsage,
    // causing zero values in exported CSV stage rows. The full JSONL record has the
    // per_stage breakdown needed to populate each stage's token/cost columns.
    if (target !== "current" && this.workspaceRoot) {
      const store = this.state.getTelemetryStore();
      if (store) {
        const fullRecord = await store.getRunRecord(run.issueNumber);
        if (fullRecord?.tokens?.per_stage) {
          const perStage = fullRecord.tokens.per_stage;
          const startedAt = run.startedAt;
          run = {
            ...run,
            stages: run.stages.map((stage) => {
              if (stage.tokenUsage) return stage;
              const st = perStage[stage.stage as keyof typeof perStage];
              if (!st) return stage;
              return {
                ...stage,
                tokenUsage: {
                  stage: stage.stage,
                  inputTokens: st.input,
                  outputTokens: st.output,
                  cacheReadTokens: st.cache_read,
                  cacheCreationTokens: st.cache_creation,
                  costUsd: st.cost_usd,
                  timestamp: startedAt,
                  model: st.model,
                  cacheHitRate: st.cache_hit_rate,
                },
              };
            }),
          };
        }
      }
    }

    // Generate export content
    const content = format === "json" ? this.state.exportAsJson(run) : this.state.exportAsCsv(run);

    const extension = format === "json" ? "json" : "csv";
    const filename = `pipeline-${run.issueNumber}.${extension}`;

    // Create a data URI and download
    const uri = await vscode.window.showSaveDialog({
      defaultUri: vscode.Uri.file(filename),
      filters: {
        [format.toUpperCase()]: [extension],
      },
    });

    if (uri) {
      await vscode.workspace.fs.writeFile(uri, Buffer.from(content, "utf-8"));
      vscode.window.showInformationMessage(`Exported to ${uri.fsPath}`);
    }
  }

  /**
   * Handle export analytics request from dashboard UI (Issue #1010)
   */
  private async handleExportAnalytics(
    format: ExportFormat,
    dateRange: "last7" | "last30" | "all"
  ): Promise<void> {
    if (!this.workspaceRoot) {
      vscode.window.showWarningMessage("No workspace root available.");
      return;
    }

    try {
      // Resolve date range
      let records;
      if (dateRange === "all") {
        records = await ExecutionHistoryReader.readAll(this.workspaceRoot);
      } else {
        const end = new Date();
        const start = new Date();
        start.setDate(start.getDate() - (dateRange === "last7" ? 7 : 30));
        records = await ExecutionHistoryReader.readDateRange(this.workspaceRoot, start, end);
      }

      if (records.length === 0) {
        vscode.window.showWarningMessage("No telemetry records found for the selected date range.");
        return;
      }

      // Convert to export format
      let content: string;
      switch (format) {
        case "json":
          content = exportAsJson(records);
          break;
        case "csv-runs":
          content = exportAsCsvRuns(records);
          break;
        case "csv-stages":
          content = exportAsCsvStages(records);
          break;
      }

      const ext = format === "json" ? "json" : "csv";
      const defaultName = `telemetry-export.${ext}`;

      const uri = await vscode.window.showSaveDialog({
        defaultUri: vscode.Uri.file(defaultName),
        filters: { [ext.toUpperCase()]: [ext] },
      });

      if (uri) {
        await vscode.workspace.fs.writeFile(uri, Buffer.from(content, "utf-8"));
        vscode.window.showInformationMessage(`Exported ${records.length} records to ${uri.fsPath}`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "Unknown error";
      vscode.window.showErrorMessage(`Failed to export analytics: ${msg}`);
    }
  }

  /**
   * Handle history run selection — updates Analytics tab to show selected run's data.
   * @see Issue #2580 - Fix Analytics tab always showing most recent run
   */
  private handleSelectRun(issueNumber: number): void {
    const run = this.state.getHistoryRun(issueNumber);
    if (run) {
      this.selectedRunIssueNumber = issueNumber;
      this.selectedRunId = this.state.getRunIdForIssue(issueNumber);
      this.refreshCostSummary().then(() => {
        this.updatePanel("msg:selectRun");
      });
    }
  }

  /**
   * Load full run details (tool calls) on-demand from JSONL (Issue #1032).
   */
  private async handleLoadRunDetails(issueNumber: number): Promise<void> {
    const store = this.state.getTelemetryStore();
    if (!store) {
      this.logger.warn("handleLoadRunDetails: TelemetryStore not available");
      return;
    }

    try {
      const record = await store.getRunRecord(issueNumber);
      const toolCalls: ToolCallEntry[] = (record?.tool_calls ?? []).map((tc) => ({
        tool: tc.tool,
        target: tc.target ?? "",
        timestamp: tc.timestamp ? new Date(tc.timestamp) : new Date(),
        durationMs: tc.duration_ms,
        args: tc.args,
        result: tc.result,
        error: tc.error,
      }));

      this.state.updateRunToolCalls(issueNumber, toolCalls);

      // Send incremental update to replace the tool calls section
      if (this.panel) {
        this.panel.webview.postMessage({
          type: "incrementalUpdate",
          section: "tool-calls",
          html: getToolCallsHtml(toolCalls),
        });
      }
    } catch (error) {
      this.logger.warn("handleLoadRunDetails: failed to load", {
        issueNumber,
        error,
      });
    }
  }

  /**
   * Apply a live {@link WorkflowEvent} node emission to the open Run Detail
   * panel (#3919, reworked from the #3714 flat-event handler).
   *
   * The canonical node tree replaces the old `pipeline.*` flat events:
   * - a `run` terminal (succeeded / failed / cancelled) → `allComplete` marker
   *   plus a snapshot refresh for final counts;
   * - a `phase` node → a per-stage status update (the phase `name` is the stage,
   *   its `status` maps onto the run-detail stage row).
   *
   * No-op when no panel is open or the emission belongs to a different run.
   */
  private handleWorkflowEvent(event: WorkflowEvent): void {
    if (!this.panel) return;
    if (this.selectedRunIssueNumber === null) return;
    if (!this.eventBelongsToSelectedRun(event)) return;

    if (event.kind === "run") {
      if (
        event.status === "succeeded" ||
        event.status === "failed" ||
        event.status === "cancelled"
      ) {
        this.panel.webview.postMessage({
          type: "runDetailLiveUpdate",
          issueNumber: this.selectedRunIssueNumber,
          update: { allComplete: true },
        });
        void this.refreshRunDetailSnapshot();
      }
      return;
    }

    if (event.kind === "phase") {
      const status =
        event.status === "succeeded"
          ? "completed"
          : event.status === "failed"
            ? "failed"
            : event.status === "skipped"
              ? "skipped"
              : "running";
      this.panel.webview.postMessage({
        type: "runDetailLiveUpdate",
        issueNumber: this.selectedRunIssueNumber,
        update: { stage: event.name, status },
      });
    }
  }

  /**
   * Whether a workflow node emission belongs to the currently-selected run. The
   * root `run` node carries `runId` / `issueNumber`; descendant nodes are matched
   * by their nodeId carrying the run's issue number (`run:NNN`, `phase:NNN:…`).
   */
  private eventBelongsToSelectedRun(event: WorkflowEvent): boolean {
    if (event.kind === "run") {
      if (this.selectedRunId !== null && event.runId === this.selectedRunId) return true;
      return event.issueNumber === this.selectedRunIssueNumber;
    }
    // Node ids are namespaced by issue number (e.g. "phase:42:feature-dev").
    return event.nodeId.includes(`:${this.selectedRunIssueNumber}:`);
  }

  /**
   * Re-fetch run details after pipeline completes to refresh final token counts (#3714).
   */
  private async refreshRunDetailSnapshot(): Promise<void> {
    if (this.selectedRunIssueNumber === null) return;
    await this.handleLoadRunDetails(this.selectedRunIssueNumber);
  }

  /**
   * Handle panel closed by user
   */
  private handlePanelClosed(): void {
    this.panel = undefined;
    this.dispose();
  }

  /**
   * Start a new pipeline run (call this from runPipeline command)
   */
  startRun(issueNumber: number, title: string, branch: string): void {
    this.state.startRun(issueNumber, title, branch);
    if (this.panel) {
      this.updatePanel("startRun");
    }
  }

  /**
   * Record a tool call (can be called from orchestrator hooks)
   */
  recordToolCall(entry: ToolCallEntry): void {
    this.state.addToolCall(entry);
    if (this.panel) {
      this.updatePanel("recordToolCall");
    }
  }

  /**
   * Mark current run as failed
   */
  async failRun(): Promise<void> {
    await this.state.failRun();
    if (this.panel) {
      this.updatePanel("failRun");
    }
  }

  /**
   * Mark current run as cancelled
   */
  async cancelRun(): Promise<void> {
    await this.state.cancelRun();
    if (this.panel) {
      this.updatePanel("cancelRun");
    }
  }

  /**
   * Reset session metrics to midnight of current day
   */
  async resetSession(): Promise<void> {
    await this.state.resetSession();
    if (this.panel) {
      this.updatePanel("resetSession");
    }
  }

  /**
   * Refresh project board data from GitHub
   * @see Issue #134 - Project Board Dashboard Widget
   */
  async refreshProjectBoardData(): Promise<void> {
    if (!this.projectBoardService || !this.workspaceRoot) {
      this.state.setProjectBoardData({
        statusCounts: {
          ready: 0,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: new Date(),
        projectUrl: null,
        isConfigured: false,
        loadingState: "loaded",
      });
      return;
    }

    // Prevent overlapping refreshes: if one is already running, skip (Issue #1233)
    if (this.boardRefreshInProgress) {
      return;
    }
    this.boardRefreshInProgress = true;

    // Publish a `loading` state immediately so the widget can show "Loading…"
    // on first dashboard open instead of a frozen 0/0/0/0. We preserve any
    // previously-fetched counts to avoid the widget visually "resetting" on
    // every manual refresh.
    const prior = this.state.getProjectBoardData();
    this.state.setProjectBoardData({
      statusCounts: prior?.statusCounts ?? {
        ready: 0,
        inProgress: 0,
        inReview: 0,
        done: 0,
        backlog: 0,
      },
      topReadyIssues: prior?.topReadyIssues ?? [],
      currentSprint: prior?.currentSprint ?? null,
      lastRefreshed: prior?.lastRefreshed ?? new Date(),
      projectUrl: prior?.projectUrl ?? null,
      isConfigured: true,
      loadingState: "loading",
      diagnostics: prior?.diagnostics,
      projects: prior?.projects,
      selectedProject: prior?.selectedProject,
      multiProjectMode: prior?.multiProjectMode,
    });

    try {
      const config = this.state.getProjectBoardConfig();

      // Single prefetch populates allItemsCache (force=true bypasses TTL without
      // destroying cache). Dashboard then derives all 5 status columns from the
      // warm cache — eliminating 4 redundant API round-trips (Issue #1233).
      await this.projectBoardService.prefetchAllItems({ force: true });

      // prefetchAllItems() swallows IPC errors (existing contract for tree
      // providers). Read them through the diagnostic getter so the dashboard
      // widget can surface them instead of silently rendering 0/0/0/0.
      const prefetchError =
        this.projectBoardService instanceof ProjectBoardService
          ? this.projectBoardService.getLastPrefetchError()
          : null;
      if (prefetchError) {
        throw new Error(prefetchError);
      }

      const [readyIssues, inProgressIssues, inReviewIssues, doneIssues, backlogIssues] =
        await Promise.all([
          this.projectBoardService.getItemsByStatusFromCache("Ready", "board", "asc"),
          this.projectBoardService.getItemsByStatusFromCache("In progress"),
          this.projectBoardService.getItemsByStatusFromCache("In review"),
          this.projectBoardService.getItemsByStatusFromCache("Done"),
          this.projectBoardService.getItemsByStatusFromCache("Backlog"),
        ]);

      const statusCounts: StatusCounts = {
        ready: readyIssues.length,
        inProgress: inProgressIssues.length,
        inReview: inReviewIssues.length,
        done: doneIssues.length,
        backlog: backlogIssues.length,
      };

      // Get top N ready issues
      const topReadyIssues = readyIssues.slice(0, config.maxReadyIssues).map((issue) => ({
        number: issue.number,
        title: issue.title,
        priority: issue.priority,
        url: issue.url,
      }));

      // Get current sprint
      let currentSprint = null;
      if (this.projectIterationService) {
        try {
          const iterations = await this.projectIterationService.getIterations();
          // Current iteration is the one containing today's date
          const today = new Date();
          currentSprint =
            iterations.find((iter) => {
              const startDate = new Date(iter.startDate);
              const endDate = new Date(startDate);
              endDate.setDate(endDate.getDate() + iter.duration);
              return today >= startDate && today < endDate;
            }) ?? null;
        } catch {
          // Sprint not configured - that's OK
        }
      }

      // Build project URL from workspace config
      let projectUrl: string | null = null;
      try {
        const fs = await import("fs");
        const path = await import("path");
        const { promisify } = await import("util");
        const { exec } = await import("child_process");
        const execAsync = promisify(exec);

        // Read project number from config.yaml
        const configPath = path.join(this.workspaceRoot, ".nightgauge", "nightgauge.yaml");
        const content = await fs.promises.readFile(configPath, "utf-8");
        const numberMatch = content.match(/^\s*number:\s*(\d+)/m);
        const projectNumber = numberMatch ? parseInt(numberMatch[1], 10) : null;

        // Get owner from gh CLI
        const result = await execAsync("gh repo view --json owner -q .owner.login", {
          cwd: this.workspaceRoot,
        });
        const owner = result.stdout.trim();

        if (projectNumber && owner) {
          projectUrl = `https://github.com/orgs/${owner}/projects/${projectNumber}`;
        }
      } catch {
        // Config not available - that's OK
      }

      // Get multi-project info
      // getProjects/getSelectedProject are ProjectBoardService-specific (not on IWorkItemProvider)
      const projects =
        this.projectBoardService instanceof ProjectBoardService
          ? await this.projectBoardService.getProjects()
          : [];
      const multiProjectMode = projects.length > 1;
      const selectedProject =
        this.projectBoardService instanceof ProjectBoardService
          ? this.projectBoardService.getSelectedProject()
          : undefined;

      const diagnostics =
        this.projectBoardService instanceof ProjectBoardService
          ? (this.projectBoardService.getLastPrefetchDiagnostics() ?? undefined)
          : undefined;

      const projectBoardData: ProjectBoardData = {
        statusCounts,
        topReadyIssues,
        currentSprint,
        lastRefreshed: new Date(),
        projectUrl,
        isConfigured: true,
        projects: multiProjectMode ? projects : undefined,
        selectedProject: multiProjectMode ? selectedProject : undefined,
        multiProjectMode,
        loadingState: "loaded",
        diagnostics,
      };

      this.state.setProjectBoardData(projectBoardData);
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Unknown error";

      this.state.setProjectBoardData({
        statusCounts: {
          ready: 0,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: new Date(),
        projectUrl: null,
        isConfigured: true,
        error: errorMessage,
        loadingState: "error",
      });
    } finally {
      this.boardRefreshInProgress = false;
    }
  }

  /**
   * Write a backup execution history record from pipeline state.
   * Ensures JSONL data stays current even if the primary write paths
   * (Go recordV2History, TS pipeline.complete IPC handler) don't fire.
   * Guarded by lastHistoryWriteIssueNumber to prevent duplicate writes.
   */
  private async writeBackupHistoryRecord(
    pipelineState: PipelineState,
    toolCalls?: ToolCallEntry[]
  ): Promise<void> {
    if (!pipelineState.issue_number || !pipelineState.started_at) return;
    if (this.lastHistoryWriteIssueNumber === pipelineState.issue_number) return;
    this.lastHistoryWriteIssueNumber = pipelineState.issue_number;

    const store = this.state.getTelemetryStore();
    if (!store) return;

    try {
      const toolCallRecords =
        toolCalls && toolCalls.length > 0
          ? toolCalls.map((tc): ToolCallRecord => ({
              tool: tc.tool,
              target: tc.target || undefined,
              timestamp: tc.timestamp.toISOString(),
              duration_ms: tc.durationMs,
              args: tc.args,
              result: tc.result,
              error: tc.error,
            }))
          : undefined;

      // Preserve the performance-mode tag on the backup write so dashboards,
      // cost-trend exclusions, and outcome calibration do not silently
      // mis-classify non-baseline runs as elevated (Issues #2433, #3009).
      const workspaceFolder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
      const performanceMode = workspaceFolder ? getPerformanceMode(workspaceFolder) : "elevated";
      const supercharge = performanceMode === "maximum" || undefined;

      // Read run_id from run-state.json for batch telemetry deduplication (#3558)
      let runIdForHistory: string | undefined;
      try {
        if (this.workspaceRoot) {
          const pathLib = await import("node:path");
          const runStatePath = pathLib.join(
            this.workspaceRoot,
            ".nightgauge",
            "pipeline",
            "run-state.json"
          );
          const runStateBytes = await vscode.workspace.fs.readFile(vscode.Uri.file(runStatePath));
          const runStateParsed = JSON.parse(Buffer.from(runStateBytes).toString("utf8")) as {
            run_id?: string;
          };
          runIdForHistory = runStateParsed.run_id || undefined;
        }
      } catch {
        // Non-fatal — run_id is best-effort for deduplication
      }

      const record = ExecutionHistoryWriter.buildRunRecord(
        pipelineState as Parameters<typeof ExecutionHistoryWriter.buildRunRecord>[0],
        undefined,
        undefined,
        {
          tool_calls: toolCallRecords,
          performance_mode: performanceMode,
          is_supercharge: supercharge,
          run_id: runIdForHistory,
        }
      );
      const written = await store.appendRunRecord(record);
      if (written) {
        this.logger.info("Backup execution history record written", {
          issueNumber: pipelineState.issue_number,
          toolCallCount: toolCallRecords?.length ?? 0,
        });
      }
    } catch {
      // Non-critical: backup write failure should never break dashboard
    }
  }

  /**
   * Refresh health widget data from DashboardState
   * @see Issue #655 - Pipeline Health Dashboard Widget
   */
  async refreshHealthWidgetData(): Promise<void> {
    try {
      this.healthWidgetData = await this.state.getHealthData(
        undefined,
        this.healthTrendRange,
        this.dependabotData
      );
    } catch {
      this.healthWidgetData = null;
    }
  }

  /**
   * Refresh model routing metrics from execution history
   * @see Issue #734 - Learning Feedback Loop & Model Routing Report
   */
  async refreshModelRoutingMetrics(): Promise<void> {
    try {
      this.modelRoutingMetrics = await this.state.getModelRoutingMetrics();
    } catch {
      this.modelRoutingMetrics = null;
    }
  }

  /**
   * Refresh cost summary and history for the dashboard widget.
   * When a historical run is selected (Issue #2580), uses that run's data.
   * Cost history (sparkline) always spans all runs regardless of selection.
   * @see Issue #945 - Per-Pipeline Cost Summary
   * @see Issue #2580 - Fix Analytics tab always showing most recent run
   */
  async refreshCostSummary(): Promise<void> {
    try {
      const selectedRun =
        this.selectedRunIssueNumber !== null
          ? this.state.getHistoryRun(this.selectedRunIssueNumber)
          : undefined;
      this.costSummary = await this.state.getPipelineCostSummary(
        selectedRun,
        this.currentModeFilter
      );
      this.costHistory = this.state.getCostHistory(10);
    } catch {
      this.costSummary = null;
      this.costHistory = [];
    }
  }

  /**
   * Refresh pre-run cost estimate from current run's issue metadata.
   *
   * Reads the current pipeline state to extract issue metadata, then
   * computes the cost estimate via DashboardState.computeCostEstimate().
   *
   * @see Issue #948 - Effort-Aware Cost Estimation
   */
  async refreshCostEstimate(): Promise<void> {
    try {
      const currentRun = this.state.getCurrentRun();
      if (currentRun) {
        // If a run is active, compute estimate from its metadata
        const metadata: import("@nightgauge/sdk").IssueMetadata = {
          labels: [],
          title: currentRun.title,
        };

        // Try to read labels from pipeline state file
        try {
          const fs = await import("node:fs/promises");
          const path = await import("node:path");
          const workspaceRoot = (await import("vscode")).workspace.workspaceFolders?.[0]?.uri
            .fsPath;
          if (workspaceRoot) {
            const statePath = path.join(workspaceRoot, ".nightgauge", "pipeline", "state.json");
            const content = await fs.readFile(statePath, "utf-8");
            const state = JSON.parse(content);
            if (state.labels && Array.isArray(state.labels)) {
              metadata.labels = state.labels;
            }
          }
        } catch {
          // State file not available - use empty labels
        }

        const routing = this.state.getRouting();
        const skipStages = routing?.skippedStages ?? [];
        this.costEstimate = await this.state.computeCostEstimate(metadata, skipStages);
      } else {
        this.costEstimate = this.state.getCostEstimate();
      }
    } catch {
      this.costEstimate = null;
    }
  }

  /**
   * Set a health check report for display in the dashboard.
   * Called by the runPipelineHealth command after analysis completes.
   * @see Issue #1104 - Pipeline Health VSCode Command & Dashboard Integration
   */
  setHealthCheckReport(report: HealthCheckReport): void {
    this.healthCheckReport = report;
    if (this.panel) {
      this.updatePanel("setHealthCheckReport");
    }
  }

  /**
   * Wire in the UsageLimitsService so the dashboard can render the usage section (Issue #1333)
   */
  setUsageLimitsService(service: UsageLimitsService): void {
    this.usageLimitsService = service;
  }

  /**
   * Wire in the PlatformQuotaService so the dashboard can render the quota section (Issue #1479)
   */
  registerPlatformQuotaService(service: PlatformQuotaService): void {
    this.platformQuotaService = service;
  }

  /**
   * Get cached platform quota data for the render (Issue #1479)
   */
  private getPlatformQuotaData(): PlatformQuotaData | null {
    return this.platformQuotaService?.getCached() ?? null;
  }

  /**
   * Wire the EventStreamService into the Dashboard (Issue #3321).
   *
   * Subscribes to audit live events and stream status changes, forwarding both
   * to the webview via postMessage. Call once after EventStreamService is initialized
   * in extension.ts. Idempotent — subsequent calls with the same instance are no-ops.
   */
  setEventStreamService(service: EventStreamService): void {
    if (this.eventStreamService === service) return;
    this.eventStreamService = service;

    this.disposables.push(
      service.onAuditLiveEvent((entry) => {
        this.panel?.webview.postMessage({ type: "auditLiveEvent", entry });
      })
    );

    this.disposables.push(
      service.onStreamStatusChanged((statusEvent) => {
        this.panel?.webview.postMessage({
          type: "streamStatusChanged",
          status: statusEvent.status,
          label: statusEvent.label,
        });
      })
    );

    this.disposables.push(
      service.onWorkflowEvent((event) => {
        this.handleWorkflowEvent(event);
      })
    );
  }

  /**
   * Fetch audit events (lazy — only when audit tab is active or refresh is called).
   * Never throws — AuditLogService handles all error paths.
   * @see Issue #1583
   */
  async refreshAuditLogData(filters?: AuditFilterState, page = 0): Promise<void> {
    const tokenStorage = TokenStorage.getInstance();
    if (!tokenStorage) {
      return;
    }
    this.auditLogService ??= this.createAuditLogService(tokenStorage);
    const activeFilters = filters ?? this.auditFilters;
    this.auditFilters = activeFilters;
    this.auditLogData = await this.auditLogService.fetch(activeFilters, page);
    this.updatePanel("auditRefresh");
  }

  private createAuditLogService(tokenStorage: TokenStorage): AuditLogService {
    const useLegacy = vscode.workspace
      .getConfiguration("nightgauge.audit")
      .get<boolean>("legacyEndpoint", false);
    const localFallback = this.workspaceRoot
      ? new LocalAuditFallbackService(this.workspaceRoot)
      : null;
    return new AuditLogService(
      tokenStorage,
      () => resolvePlatformBaseUrl(ConfigBridge.getInstance().getPlatform()),
      useLegacy,
      localFallback
    );
  }

  /**
   * Refresh discovery activity data from local state files (Issue #2434).
   * Reads creation-log.json and improvement-runs/latest.json written by GitHub Actions.
   * Never throws — all errors are handled internally by DiscoveryActivityService.
   */
  async refreshDiscoveryActivityData(): Promise<void> {
    if (!this.discoveryActivityService) {
      return;
    }
    this.discoveryActivityData = await this.discoveryActivityService.getActivityData();
    this.updatePanel("discoveryRefresh");
  }

  /**
   * Refresh platform cost analytics data via IPC (Issue #3317).
   * Lazy-loaded on first cost tab activation. Re-fetches on date range change.
   * Never throws — errors are handled by PlatformCostService.
   */
  async refreshCostData(): Promise<void> {
    try {
      const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
      this.platformCostService ??= new PlatformCostService(ipc);
      this.platformCostData = await this.platformCostService.fetchAndCache(this.costDateRange);
      this.updatePanel("costRefresh");
    } catch {
      // IPC unavailable — leave data null, don't throw
    }
  }

  /** Classify an IPC/platform error into a structured PlatformErrorType (#3679). */
  private classifyPlatformError(err: unknown): PlatformErrorType {
    if (err instanceof Error) {
      if (err.message.includes("Go backend not connected")) return "ipc_unavailable";
      if (err.message.includes("Failed to write to Go backend")) return "ipc_unavailable";
      if (err.message.includes("timed out")) return "ipc_timeout";
    }
    return "server_error";
  }

  /**
   * Check token state before making a platform call (#3679).
   * Returns a PlatformErrorType if the token is missing/expired, or null if valid.
   */
  private async checkPlatformTokenState(): Promise<PlatformErrorType | null> {
    const tokenStorage = TokenStorage.getInstance();
    if (!tokenStorage) return "not_signed_in";
    const accessToken = await tokenStorage.retrieve("accessToken");
    if (!accessToken) return "not_signed_in";
    const expiresAt = await tokenStorage.retrieve("expiresAt");
    if (expiresAt && new Date(expiresAt) < new Date()) return "token_expired";
    return null;
  }

  /**
   * Refresh platform analytics health data via IPC (Issue #3318).
   * Lazy-loaded on first health tab activation. Re-fetches on healthRefresh message.
   * Never throws — errors are handled by PlatformAnalyticsHealthService.
   */
  async refreshHealthAnalyticsData(): Promise<void> {
    try {
      const tokenError = await this.checkPlatformTokenState();
      if (tokenError) {
        this.healthAnalyticsData = {
          result: null,
          hasAccess: false,
          isLoading: false,
          errorType: tokenError,
          errorMessage:
            tokenError === "not_signed_in"
              ? "Sign in to view health data"
              : "Session expired — sign in again",
        };
        this.logger.info("platform:health-tab-error", { errorType: tokenError });
        this.updatePanel("healthRefresh");
        return;
      }
      const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
      this.platformAnalyticsHealthService ??= new PlatformAnalyticsHealthService(ipc);
      const result = await this.platformAnalyticsHealthService.fetchAndCache();
      if (result === null) {
        const errorType: PlatformErrorType = "server_error";
        this.healthAnalyticsData = {
          result: null,
          hasAccess: false,
          isLoading: false,
          errorType,
          errorMessage: "Platform health API returned an error",
        };
        this.logger.info("platform:health-tab-error", { errorType });
      } else {
        this.healthAnalyticsData = { result, hasAccess: true, isLoading: false };
        this.healthAnalyticsFetchedAt = new Date();
      }
      this.updatePanel("healthRefresh");
    } catch (err) {
      const errorType = this.classifyPlatformError(err);
      this.healthAnalyticsData = {
        result: null,
        hasAccess: false,
        isLoading: false,
        errorType,
        errorMessage: "Failed to load health data",
      };
      this.logger.info("platform:health-tab-error", { errorType });
      this.updatePanel("healthRefresh");
    }
  }

  /**
   * Refresh pipeline runs data via IPC (Issue #3319).
   * Lazy-loaded on first runs tab activation. Re-fetches on filter/page change.
   * Never throws — errors are handled by PlatformRunsService.
   */
  async refreshRunsData(cursor?: string): Promise<void> {
    try {
      const tokenError = await this.checkPlatformTokenState();
      if (tokenError) {
        this.runsData = {
          entries: [],
          filters: this.runsFilters,
          pagination: this.runsPagination,
          isLoading: false,
          hasAccess: false,
          errorType: tokenError,
        };
        this.logger.info("platform:runs-tab-error", { errorType: tokenError });
        this.updatePanel("runsRefresh");
        return;
      }
      const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
      this.platformRunsService ??= new PlatformRunsService(ipc);

      const loading: RunsListData = {
        entries: [],
        filters: this.runsFilters,
        pagination: this.runsPagination,
        isLoading: true,
        hasAccess: true,
      };
      this.runsData = loading;
      this.updatePanel("runsRefresh");

      const result = await this.platformRunsService.fetchAndCache(this.runsFilters, cursor, 20);

      if (result === null) {
        const errorType: PlatformErrorType = "server_error";
        this.runsData = {
          entries: [],
          filters: this.runsFilters,
          pagination: this.runsPagination,
          isLoading: false,
          hasAccess: false,
          errorType,
        };
        this.logger.info("platform:runs-tab-error", { errorType });
      } else {
        // Store next cursor in the stack for the next page
        const updatedStack = [...this.runsPagination.cursorStack];
        const nextPage = this.runsPagination.page + 1;
        if (result.has_more && result.next_cursor) {
          updatedStack[nextPage] = result.next_cursor;
        }
        this.runsPagination = {
          ...this.runsPagination,
          totalCount: result.total_count,
          hasMore: result.has_more,
          cursorStack: updatedStack,
        };
        this.runsData = {
          entries: result.entries,
          filters: this.runsFilters,
          pagination: this.runsPagination,
          isLoading: false,
          hasAccess: true,
        };
      }
      this.updatePanel("runsRefresh");
    } catch (err) {
      const errorType = this.classifyPlatformError(err);
      this.runsData = {
        entries: [],
        filters: this.runsFilters,
        pagination: this.runsPagination,
        isLoading: false,
        hasAccess: false,
        errorType,
        errorMessage: "Failed to load runs data",
      };
      this.logger.info("platform:runs-tab-error", { errorType });
      this.updatePanel("runsRefresh");
    }
  }

  /**
   * Fetch platform trends data for the Trends tab (Issue #3320).
   * Mirrors refreshRunsData() — lazy-loaded on first tab activation.
   */
  private async fetchTrendsData(): Promise<void> {
    try {
      const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
      this.platformTrendsService ??= new PlatformTrendsService(ipc);

      const loading: TrendsData = {
        result: null,
        isLoading: true,
        hasAccess: true,
        showComparison: this.trendsShowComparison,
      };
      this.trendsData = loading;
      this.updatePanel("trendsRefresh");

      const result = await this.platformTrendsService.fetchAndCache(this.trendsDateRange);

      if (result === null) {
        this.trendsData = {
          result: null,
          isLoading: false,
          hasAccess: false,
          showComparison: this.trendsShowComparison,
          errorMessage: "Failed to load trends data. Ensure the platform is connected.",
        };
      } else {
        this.trendsData = {
          result,
          isLoading: false,
          hasAccess: true,
          showComparison: this.trendsShowComparison,
        };
      }
      this.updatePanel("trendsRefresh");
    } catch {
      this.trendsData = {
        result: null,
        isLoading: false,
        hasAccess: false,
        showComparison: this.trendsShowComparison,
        errorMessage: "Failed to load trends data. Ensure the platform is connected.",
      };
      this.updatePanel("trendsRefresh");
    }
  }

  /**
   * Refresh compliance reports list via IPC (Issue #3322).
   * Lazy-loaded on first compliance tab activation. Re-fetches on page change or refresh.
   * Never throws — errors are surfaced in complianceData.errorMessage.
   */
  private async refreshComplianceData(cursor?: string): Promise<void> {
    try {
      const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
      this.platformComplianceService ??= new PlatformComplianceService(ipc);

      const loading: ComplianceData = {
        reports: [],
        filters: {},
        pagination: { hasMore: false },
        isLoading: true,
        hasAccess: true,
        isGenerating: this.complianceData?.isGenerating ?? false,
      };
      this.complianceData = loading;
      this.updatePanel("complianceRefresh");

      const result = await this.platformComplianceService.fetchAndCache(cursor, 20);

      if (result === null) {
        this.complianceData = {
          reports: [],
          filters: {},
          pagination: { hasMore: false },
          isLoading: false,
          hasAccess: false,
          isGenerating: false,
        };
      } else {
        this.complianceData = {
          reports: result.reports,
          filters: {},
          pagination: {
            cursor,
            nextCursor: result.nextCursor,
            hasMore: result.hasMore,
          },
          isLoading: false,
          hasAccess: true,
          isGenerating: this.complianceData?.isGenerating ?? false,
        };
      }
      this.updatePanel("complianceRefresh");
    } catch {
      this.complianceData = {
        reports: [],
        filters: {},
        pagination: { hasMore: false },
        isLoading: false,
        hasAccess: false,
        isGenerating: false,
        errorMessage: "Failed to load compliance reports. Ensure the platform is connected.",
      };
      this.updatePanel("complianceRefresh");
    }
  }

  /**
   * Trigger compliance report generation, then poll until complete (Issue #3322).
   * Polling: every 2s for first 30s, then every 5s until ready/failed.
   */
  private async generateComplianceReport(
    reportType: string,
    startDate: string,
    endDate: string,
    format: string
  ): Promise<void> {
    if (!this.platformComplianceService) {
      const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
      this.platformComplianceService = new PlatformComplianceService(ipc);
    }

    // Mark generating in progress
    if (this.complianceData) {
      this.complianceData = { ...this.complianceData, isGenerating: true };
      this.updatePanel("complianceGenerate");
    }

    let reportId: string;
    try {
      const result = await this.platformComplianceService.generateReport(
        reportType,
        startDate,
        endDate,
        format
      );
      reportId = result.id;
    } catch {
      if (this.complianceData) {
        this.complianceData = {
          ...this.complianceData,
          isGenerating: false,
          errorMessage: "Failed to generate compliance report. Check your plan and permissions.",
        };
        this.updatePanel("complianceGenerate");
      }
      return;
    }

    // Polling: 2s for first 30s, then 5s — recursive setTimeout to avoid nested setInterval leaks
    const FAST_INTERVAL_MS = 2000;
    const SLOW_INTERVAL_MS = 5000;
    const FAST_DURATION_MS = 30000;
    let elapsedMs = 0;

    this.stopCompliancePolling();

    const scheduleNext = () => {
      const interval = elapsedMs < FAST_DURATION_MS ? FAST_INTERVAL_MS : SLOW_INTERVAL_MS;
      this.compliancePollingTimer = setTimeout(async () => {
        try {
          const detail = await this.platformComplianceService!.getReport(reportId);
          if (detail.status === "ready" || detail.status === "failed") {
            this.stopCompliancePolling();
            if (this.complianceData) {
              this.complianceData = { ...this.complianceData, isGenerating: false };
            }
            await this.refreshComplianceData();
            return;
          }
        } catch {
          // Polling failure is non-fatal — reschedule
        }
        elapsedMs += interval;
        scheduleNext();
      }, interval);
    };
    scheduleNext();
  }

  /** Stop any active compliance polling timer. */
  private stopCompliancePolling(): void {
    if (this.compliancePollingTimer) {
      clearTimeout(this.compliancePollingTimer);
      this.compliancePollingTimer = undefined;
    }
  }

  /**
   * Open the compliance report download URL in the browser (Issue #3322).
   */
  private async downloadComplianceReport(reportId: string): Promise<void> {
    if (!this.platformComplianceService) {
      return;
    }
    try {
      const detail = await this.platformComplianceService.getReport(reportId);
      if (detail.downloadUrl) {
        await vscode.env.openExternal(vscode.Uri.parse(detail.downloadUrl));
      } else {
        vscode.window.showInformationMessage("Download URL not yet available. Try again shortly.");
      }
    } catch {
      vscode.window.showErrorMessage("Failed to fetch report download URL.");
    }
  }

  /**
   * Fetch current retention config and populate retentionIntegrityData (Issue #3323).
   * Never throws — errors are surfaced in retentionIntegrityData.errorMessage.
   */
  private async refreshRetentionData(): Promise<void> {
    const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
    const loading: RetentionIntegrityData = {
      retentionConfig: this.retentionIntegrityData?.retentionConfig ?? null,
      integrityResult: this.retentionIntegrityData?.integrityResult ?? null,
      isLoading: true,
      isVerifying: this.retentionIntegrityData?.isVerifying ?? false,
      hasAccess: this.retentionIntegrityData?.hasAccess ?? true,
    };
    this.retentionIntegrityData = loading;
    this.updatePanel("retentionRefresh-loading");
    try {
      const config = await ipc.auditGetRetentionConfig();
      this.retentionIntegrityData = {
        retentionConfig: config,
        integrityResult: this.retentionIntegrityData?.integrityResult ?? null,
        isLoading: false,
        isVerifying: false,
        hasAccess: true,
      };
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      const isEnterpriseOnly = msg.includes("enterprise only") || msg.includes("403");
      this.retentionIntegrityData = {
        retentionConfig: null,
        integrityResult: null,
        isLoading: false,
        isVerifying: false,
        hasAccess: !isEnterpriseOnly,
        errorMessage: isEnterpriseOnly ? undefined : msg,
      };
    }
    this.updatePanel("retentionRefresh");
  }

  /**
   * Update the audit retention period (Issue #3323).
   * Never throws — errors are surfaced in retentionIntegrityData.errorMessage.
   */
  private async updateRetention(retentionDays: number): Promise<void> {
    const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
    try {
      const config = await ipc.auditUpdateRetentionConfig(retentionDays);
      if (this.retentionIntegrityData) {
        this.retentionIntegrityData = {
          ...this.retentionIntegrityData,
          retentionConfig: config,
          errorMessage: undefined,
        };
        this.updatePanel("retentionUpdate");
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      if (this.retentionIntegrityData) {
        this.retentionIntegrityData = { ...this.retentionIntegrityData, errorMessage: msg };
        this.updatePanel("retentionUpdate-error");
      }
    }
  }

  /**
   * Trigger audit log integrity verification (Issue #3323).
   * Shows spinner while in progress. Never throws.
   */
  private async refreshDependabotData(): Promise<void> {
    const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
    if (!this.workspaceRoot) return;
    const { getRepoIdentity } = await import("../../utils/configPathResolver");
    const identity = await getRepoIdentity(this.workspaceRoot).catch(() => null);
    const owner = identity?.owner ?? "";
    const repo = identity?.repo ?? "";
    if (!owner || !repo) return;
    if (!this.dependabotService) {
      const { DependabotPRService } = await import("../../services/DependabotPRService");
      this.dependabotService = new DependabotPRService(ipc, owner, repo, this.logger);
    }
    try {
      this.dependabotData = await this.dependabotService.getData(true);
    } catch {
      this.dependabotData = { prs: [], staleCount: 0, securityCount: 0, fetchedAt: "" };
    }
    this.updatePanel("dependabotRefresh");
  }

  private async mergeDependabotPR(owner: string, repo: string, prNodeId: string): Promise<void> {
    const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
    try {
      await ipc.prMerge(owner, repo, prNodeId);
      vscode.window.showInformationMessage("Dependabot PR merged successfully.");
      this.dependabotData = null;
      this.dependabotService?.invalidate();
      this.refreshDependabotData().catch(() => {});
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      vscode.window.showErrorMessage(`Failed to merge PR: ${msg}`);
    }
  }

  private async verifyIntegrity(windowDays: number): Promise<void> {
    const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
    if (this.retentionIntegrityData) {
      this.retentionIntegrityData = { ...this.retentionIntegrityData, isVerifying: true };
      this.updatePanel("verifyIntegrity-start");
    }
    try {
      const result = await ipc.auditVerifyIntegrity(windowDays);
      if (this.retentionIntegrityData) {
        this.retentionIntegrityData = {
          ...this.retentionIntegrityData,
          integrityResult: result,
          isVerifying: false,
          errorMessage: undefined,
        };
        this.updatePanel("verifyIntegrity-done");
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      if (this.retentionIntegrityData) {
        this.retentionIntegrityData = {
          ...this.retentionIntegrityData,
          isVerifying: false,
          errorMessage: msg,
        };
        this.updatePanel("verifyIntegrity-error");
      }
    }
  }

  /**
   * Export runs as CSV and open in VS Code (Issue #3319).
   */
  private async exportRunsCsv(filters: RunsFilterState): Promise<void> {
    if (!this.runsData || this.runsData.entries.length === 0) {
      vscode.window.showInformationMessage("No runs to export.");
      return;
    }

    const header = "StartedAt,IssueNumber,Title,Branch,Outcome,DurationMs,TotalCostUsd";
    const rows = this.runsData.entries.map((e) =>
      [
        e.started_at,
        e.issue_number,
        `"${e.title.replace(/"/g, '""')}"`,
        e.branch,
        e.outcome,
        e.duration_ms,
        e.total_cost_usd,
      ].join(",")
    );
    const csv = [header, ...rows].join("\n");

    const doc = await vscode.workspace.openTextDocument({
      content: csv,
      language: "plaintext",
    });
    await vscode.window.showTextDocument(doc);
    void filters; // intentionally unused — filter state already applied to entries
  }

  /**
   * Export audit events as CSV and open in VS Code (Issue #1583).
   */
  private async exportAuditCsv(filters: AuditFilterState): Promise<void> {
    const tokenStorage = TokenStorage.getInstance();
    if (!tokenStorage) {
      return;
    }
    this.auditLogService ??= this.createAuditLogService(tokenStorage);

    // Fetch all results (page 0 with large pageSize handled server-side)
    const data = await this.auditLogService.fetch(filters, 0);
    if (!data.hasAccess || data.entries.length === 0) {
      vscode.window.showInformationMessage("No audit events to export.");
      return;
    }

    const header = "Timestamp,User,Action,ResourceType,ResourceId,Status,CostUsd";
    const rows = data.entries.map((e) =>
      [
        e.timestamp,
        e.userEmail ?? e.userId,
        e.action,
        e.resourceType ?? "",
        e.resourceId ?? "",
        e.status,
        e.costUsd !== undefined ? e.costUsd.toFixed(4) : "",
      ]
        .map((v) => `"${String(v).replace(/"/g, '""')}"`)
        .join(",")
    );
    const csv = [header, ...rows].join("\n");

    const uri = vscode.Uri.joinPath(this.extensionUri, `audit-export-${Date.now()}.csv`);
    await vscode.workspace.fs.writeFile(uri, Buffer.from(csv, "utf-8"));
    await vscode.env.openExternal(uri);
  }

  /**
   * Build UsageLimitsData for the dashboard render (Issue #1333)
   */
  private getUsageLimitsData(): UsageLimitsData | null {
    const settings = getLimitsSettings();
    if (settings.monthlyBudgetUsd <= 0 || !this.usageLimitsService) {
      return null;
    }
    const costUsd = this.usageLimitsService.getEffectiveCostUsd();
    const budgetUsd = settings.monthlyBudgetUsd;
    const usagePct = (costUsd / budgetUsd) * 100;
    return { costUsd, budgetUsd, usagePct };
  }

  /**
   * Get the cached health check report (for rendering).
   * @see Issue #1104
   */
  getHealthCheckReport(): HealthCheckReport | null {
    return this.healthCheckReport;
  }

  /**
   * Get the dashboard state (for testing or external access)
   */
  getState(): DashboardState {
    return this.state;
  }

  /**
   * Check if the panel is currently visible
   */
  isVisible(): boolean {
    return this.panel?.visible ?? false;
  }

  /**
   * Get adapter status data for the dashboard widget (Issue #1056)
   */
  private getAdapterStatusData(): AdapterStatusData {
    const ADAPTER_DISPLAY_NAMES: Record<string, string> = {
      claude: "Claude Code",
      codex: "OpenAI Codex",
      gemini: "Gemini CLI",
      "gemini-sdk": "Gemini SDK",
    };

    const settings = getCoreSettings();
    const adapter = settings.executionAdapter;
    const isGemini = adapter === "gemini" || adapter === "gemini-sdk";

    return {
      adapter,
      displayName: ADAPTER_DISPLAY_NAMES[adapter] ?? adapter,
      authMethod: isGemini ? settings.geminiAuthMethod : undefined,
      authConfigured: isGemini
        ? !!process.env.GEMINI_API_KEY || !!process.env.GOOGLE_API_KEY
        : undefined,
      model: isGemini ? settings.geminiModel : undefined,
    };
  }

  /**
   * Compute the per-mode cost rollup from current history (Issue #3218).
   *
   * Wraps `calculatePerModeCostRollup()` with the runs DashboardState already
   * holds. Returns `null` when no runs are loaded so callers can skip the card.
   */
  private computePerModeRollup(): import("./CostSummaryCalculator").PerModeCostRollup | null {
    const history = this.state.getHistory();
    if (history.length === 0) return null;
    try {
      const { calculatePerModeCostRollup } =
        require("./CostSummaryCalculator") as typeof import("./CostSummaryCalculator");
      return calculatePerModeCostRollup(history);
    } catch {
      return null;
    }
  }

  /**
   * Compute per-stage, per-execution-path budget vs actual stats (Issue #3269).
   */
  private computeBudgetVsActualStats(): import("./CostSummaryCalculator").BudgetVsActualStageStat[] {
    const history = this.state.getHistory();
    if (history.length === 0) return [];
    try {
      const { computeBudgetVsActual } =
        require("./CostSummaryCalculator") as typeof import("./CostSummaryCalculator");
      const { DEFAULT_SIZE_AWARE_BUDGETS } =
        require("../../utils/budgetEnforcer") as typeof import("../../utils/budgetEnforcer");
      return computeBudgetVsActual(history, DEFAULT_SIZE_AWARE_BUDGETS);
    } catch {
      return [];
    }
  }

  /**
   * Build the stall-threshold table data for the Performance tab (Issue #3218).
   *
   * Iterates `PIPELINE_STAGE_ORDER × PERFORMANCE_MODES` and resolves each
   * `(stage, mode)` cell from `getCalibratedStallData()`. Cells with no
   * calibrated data fall back to a `static` row with `warnSec`/`killSec` set
   * to `null` so the renderer can show "—" — the static defaults live in
   * Go-side calibration code and are not duplicated here.
   *
   * Size keying is reserved (ADR-002) — the `size` column is hard-coded to
   * `"all"` until `getCalibratedStallData()` exposes the size dimension.
   *
   * @returns one row per `(stage, mode)` pair in stage-then-mode order.
   */
  private buildStallThresholdRows(): import("./tabs/PerformanceTabHtml").StallThresholdRow[] {
    const rows: import("./tabs/PerformanceTabHtml").StallThresholdRow[] = [];
    if (!this.workspaceRoot) return rows;

    for (const stage of PIPELINE_STAGE_ORDER) {
      for (const mode of PERFORMANCE_MODES) {
        const data = getCalibratedStallData(this.workspaceRoot, stage, mode);
        if (data) {
          rows.push({
            stage,
            mode,
            size: "all",
            warnSec: data.warnSec,
            killSec: data.killSec,
            source: data.source,
            isColdStart: data.isColdStart,
          });
        } else {
          rows.push({
            stage,
            mode,
            size: "all",
            warnSec: null,
            killSec: null,
            source: "static",
            isColdStart: false,
          });
        }
      }
    }
    return rows;
  }

  /**
   * Compute the mode-mismatch advisory for the dashboard header (Issue #3218).
   *
   * Counts run-level `performance_mode` over the most-recent 10 completed runs
   * and returns advisory data when the active mode is present in fewer than
   * 70% of those runs (ADR-003). Returns `null` when there are too few runs to
   * judge or when no mismatch exists.
   *
   * Pre-#3215 records lacking `performance_mode` are included in the
   * denominator but never count toward any specific mode — they pull the
   * active-mode ratio down honestly rather than silently being attributed.
   */
  private computeModeMismatchAdvisory(): import("./DashboardHtml").ModeMismatchAdvisoryData | null {
    if (!this.workspaceRoot) return null;
    const activeMode = getPerformanceMode(this.workspaceRoot);
    // Use all recent runs regardless of status — filtering to "complete" only
    // skips cancelled runs that have real performance_mode data and causes the
    // advisory to read from much older history, producing stale mode counts.
    const recent = this.state.getHistory().slice(0, 10);
    if (recent.length < 3) return null;

    const counts: Record<ModeProfile, number> = {
      efficiency: 0,
      elevated: 0,
      maximum: 0,
      frontier: 0,
    };
    for (const run of recent) {
      const mode = run.performance_mode;
      if (mode) counts[mode] += 1;
    }
    const activeCount = counts[activeMode];
    const threshold = Math.ceil(recent.length * 0.7);
    if (activeCount >= threshold) return null;

    // Identify the dominant non-active mode for the advisory copy.
    let dominantMode: ModeProfile = activeMode;
    let dominantCount = activeCount;
    for (const mode of PERFORMANCE_MODES) {
      if (mode === activeMode) continue;
      if (counts[mode] > dominantCount) {
        dominantMode = mode;
        dominantCount = counts[mode];
      }
    }
    if (dominantCount === 0 || dominantMode === activeMode) return null;

    return {
      activeMode,
      dominantMode,
      dominantCount,
      windowSize: recent.length,
    };
  }

  /**
   * Builds per-stage cost cap tightness rows for the Cost tab dashboard panel.
   *
   * @see Issue #3276
   */
  private buildCostCapWarningRows(): CostCapWarningRow[] {
    const medianCosts = this.state.getPerStageMedianCosts();
    const multiplier = getCostCapWarningMultiplier(this.workspaceRoot ?? undefined);
    return Object.keys(DEFAULT_STAGE_COST_CAPS).map((stage) => {
      const effectiveCap = getStageCostCapUsd(stage, this.workspaceRoot ?? undefined);
      const entry = medianCosts[stage] ?? { median: 0, sampleCount: 0 };
      const decision = checkCostCapTightness(
        stage,
        effectiveCap,
        entry.median,
        multiplier,
        entry.sampleCount
      );
      // Issue #3508: compute warn threshold and runaway ceiling for dashboard display.
      const warnMultiplier = getStageCostWarnMultiplier(stage, this.workspaceRoot ?? undefined);
      const warnThresholdUsd =
        entry.median > 0 && warnMultiplier > 0 ? entry.median * warnMultiplier : 0;
      const ceilingUsd = getRunwayCeilingUsd(effectiveCap, this.workspaceRoot ?? undefined);
      return {
        stage,
        effectiveCap,
        historicalMedian: entry.median,
        threshold: decision.threshold,
        multiplier,
        capEnvKey: decision.capEnvKey,
        capConfigPath: decision.capConfigPath,
        isTight: decision.shouldWarn,
        warnThresholdUsd,
        ceilingUsd,
      };
    });
  }

  /**
   * Connect to CompletedIssuesService for live dashboard updates (Issue #1164)
   *
   * When issues complete or fail during batch/queued processing,
   * the dashboard auto-updates without requiring manual refresh.
   */
  setCompletedIssuesService(service: CompletedIssuesService): void {
    const disposable = service.onStateChanged(() => {
      this.updatePanel("onCompletedIssuesChanged");
      // Refresh all metrics so health/cost/sparklines reflect the new completion
      this.refreshAllMetrics().then(() => this.updatePanel("autoRefreshMetrics"));
    });
    this.disposables.push(disposable);
  }

  /**
   * Connect to IssueQueueService for live queue updates (Issue #1164)
   *
   * When queue state changes during pipeline execution,
   * the dashboard auto-updates without requiring manual refresh.
   */
  setQueueService(queueService: IssueQueueService): void {
    this.queueServiceRef = queueService;
    const disposable = queueService.onQueueChanged((state) => {
      this.latestQueueState = state ?? null;
      this.updatePanel("onQueueChanged");
    });
    this.disposables.push(disposable);
    // Prime the cache so the first render has data even before the first event.
    void this.refreshQueueState();
    void this.refreshMaxConcurrent();
  }

  /**
   * Connect Dashboard to ConcurrentPipelineManager so active slot data
   * reaches buildPipelineSlotsView(). Go's queue.changed events omit active
   * slots (items are dequeued before execution begins), so the authoritative
   * source is the TypeScript-side manager.
   */
  setConcurrentPipelineManager(manager: {
    onSlotsChanged: vscode.Event<ActiveSlot[]>;
    getActiveSlots(): ActiveSlot[];
  }): void {
    // Prime immediately so the first render has data.
    this.latestActiveSlots = manager.getActiveSlots();
    const disposable = manager.onSlotsChanged((slots) => {
      this.latestActiveSlots = slots;
      // Forget stale per-issue runtime for slots that finished.
      if (this.slotsTracker) {
        const activeIssues = new Set(slots.map((s) => s.issueNumber));
        for (const issueNumber of this.slotsTracker.getSnapshots().keys()) {
          if (!activeIssues.has(issueNumber)) {
            this.slotsTracker.forget(issueNumber);
          }
        }
      }
      if (slots.length > 0) {
        this.ensureSlotsTicker();
      }
      this.updatePanel("onSlotsChanged");
    });
    this.disposables.push(disposable);
  }

  /** Pull the latest queue state from Go via IPC and trigger a re-render. */
  private async refreshQueueState(): Promise<void> {
    if (!this.queueServiceRef) return;
    try {
      this.latestQueueState = await this.queueServiceRef.getQueue();
      this.updatePanel("queueStatePrimed");
    } catch (err) {
      this.logger.debug("refreshQueueState:failed", { err: String(err) });
    }
  }

  /** Pull max-concurrent from Go and update the cached value. */
  private async refreshMaxConcurrent(): Promise<void> {
    try {
      const ipc = (await import("../../services/IpcClient")).IpcClient.getInstance();
      const result = await ipc.pipelineGetMaxConcurrent();
      if (result && typeof result.maxConcurrent === "number") {
        this.maxConcurrent = result.maxConcurrent;
      }
    } catch (err) {
      this.logger.debug("refreshMaxConcurrent:failed", { err: String(err) });
    }
  }

  /**
   * Dispose of the dashboard and clean up resources
   */
  dispose(): void {
    // Clear debounce timers
    if (this.updatePanelTimer) {
      clearTimeout(this.updatePanelTimer);
      this.updatePanelTimer = undefined;
    }
    if (this._epicRefreshTimer !== null) {
      clearTimeout(this._epicRefreshTimer);
      this._epicRefreshTimer = null;
    }

    this.stopCompliancePolling();
    this.stopSlotsTicker();
    this.slotsTracker?.dispose();
    this.slotsTracker = null;

    // Dispose of the panel
    if (this.panel) {
      this.panel.dispose();
      this.panel = undefined;
    }

    // Dispose of all disposables
    while (this.disposables.length) {
      const disposable = this.disposables.pop();
      if (disposable) {
        disposable.dispose();
      }
    }

    // Dispose recommendation applier (Issue #787)
    this.recommendationApplier?.dispose();

    // Dispose IncrediYaml service (Issue #786)
    this.incrediYamlService?.dispose();

    // Dispose diagnostic logger (Issue #780)
    this.logger.dispose();
  }

  // --- Diagnostic accessors for testing (Issue #780) ---

  /** Get the render counter (for testing) */
  getRenderCounter(): number {
    return this.renderCounter;
  }

  /** Get the render-in-progress flag (for testing) */
  getRenderInProgress(): boolean {
    return this.renderInProgress;
  }

  /** Get the debounce interval in ms */
  static getDebounceMs(): number {
    return Dashboard.DEBOUNCE_MS;
  }

  /** Get the set of triggers that use incremental updates (for testing) */
  static getIncrementalTriggers(): ReadonlySet<string> {
    return Dashboard.INCREMENTAL_TRIGGERS;
  }
}
