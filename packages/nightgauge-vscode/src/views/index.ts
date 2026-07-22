/**
 * Views barrel export
 *
 * Re-exports all view-related classes and utilities for the pipeline sidebar.
 */

// Tree Data Providers
export { PipelineTreeProvider } from "./PipelineTreeProvider";

export { ProjectBoardTreeProvider } from "./ProjectBoardTreeProvider";

export { ReadyIssueTreeProvider } from "./ReadyIssueTreeProvider";

// Tree Items
export {
  BaseTreeItem,
  IssueTreeItem,
  StageTreeItem,
  ActionTreeItem,
  ReadyIssueTreeItem,
  type IssueInfo,
  type StageStatus,
  type StageTokenInfo,
  type ActionType,
} from "./items";

// Context File Viewer
export { ContextFileViewer, openContextFile, CONTEXT_URI_SCHEME } from "./ContextFileViewer";

// Approval Dialog
export { ApprovalDialog, type ApprovalAction, type ApprovalResult } from "./approval";

// Dashboard
export {
  Dashboard,
  DashboardState,
  ALL_STAGES,
  type PipelineRunStatus,
  type StageRunStatus,
  type StageTokenUsage,
  type ToolCallEntry,
  type StageProgress,
  type PipelineRunSummary,
} from "./dashboard";

// Output Window
export {
  OutputWindow,
  OutputWindowState,
  PIPELINE_STAGES,
  type OutputWindowConfig,
  type OutputLevel,
  type OutputEntry,
  type StageStatus as OutputStageStatus,
  type StageProgress as OutputStageProgress,
  type TokenUsage,
} from "./outputWindow";

// Pipeline Summary
export { PipelineSummary, getPipelineSummaryHtml } from "./summary";

// Settings Panel
export {
  SettingsPanel,
  IncrediYamlService,
  validateConfig,
  type IncrediConfig,
  DEFAULT_CONFIG,
} from "./settings";

// Repositories Tree View (Issue #329)
export { RepositoriesTreeProvider } from "./RepositoriesTreeProvider";

// Query Results Tree View (Issue #138)
export { QueryResultsTreeProvider } from "./QueryResultsTreeProvider";

// Knowledge Tree View (Issue #1686)
export { KnowledgeTreeProvider } from "./KnowledgeTreeProvider";

// Brownfield Modernization Dashboard (Issue #1163)
export { BrownfieldDashboard } from "./brownfield/BrownfieldDashboard";

// Knowledge Document Link Provider (Issue #1687)
export { KnowledgeDocumentLinkProvider } from "./KnowledgeDocumentLinkProvider";

// Live Workflow Tree — run → phase → agent → judge off the SDK EventBus (Issue #3919)
export {
  WorkflowTreeProvider,
  WorkflowTreeModel,
  type WorkflowEventSource,
  type FoldedRun,
} from "./workflow";

// Action Center — DecisionRequest sidebar tree (ADR 015 / Issue #325)
export {
  AttentionTreeProvider,
  type AttentionIpcSource,
  AttentionTreeItem,
  AttentionGroupTreeItem,
  AttentionRequestTreeItem,
  describeAttentionOption,
} from "./attention";
