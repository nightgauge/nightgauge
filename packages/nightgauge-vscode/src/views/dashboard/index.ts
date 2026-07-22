/**
 * Dashboard barrel export
 *
 * Re-exports all dashboard-related classes and types for the metrics WebView.
 */

// Main Dashboard class
export { Dashboard } from "./Dashboard";

// State management
export {
  DashboardState,
  ALL_STAGES,
  DEFAULT_TIME_SAVINGS_CONFIG,
  type PipelineRunStatus,
  type StageRunStatus,
  type StageTokenUsage,
  type ToolCallEntry,
  type StageProgress,
  type PipelineRunSummary,
  type TimeSavingsConfig,
  type EfficiencyMetrics,
  type DashboardAggregates,
  type AdapterStatusData,
  type HistoryPaginationInfo,
  type PTCMetricsDisplayData,
  type UsageLimitsData,
  type FullDashboardRenderState,
} from "./DashboardState";

// Epic Dashboard (Issue #330 - Cross-Repo Progress)
export {
  EpicDashboard,
  type CrossRepoEpicProgress,
  type RepositoryProgress,
} from "./EpicDashboard";

// Firewall Dashboard (Issue #387 - Prompt Injection Firewall)
export {
  type SanitizationEvent,
  type SanitizationEventType,
  type SanitizationCategory,
  type FirewallFilterState,
  type FirewallAggregates,
  type FirewallTimeSeriesPoint,
  type TimeRangeFilter,
  type TimeSeriesGranularity,
  CATEGORY_LABELS,
  CATEGORY_COLORS,
  DEFAULT_FIREWALL_FILTERS,
  EMPTY_FIREWALL_AGGREGATES,
} from "./FirewallTypes";

// Firewall dashboard data type (moved to DashboardState in #1542)
export { type FirewallDashboardData } from "./DashboardState";
