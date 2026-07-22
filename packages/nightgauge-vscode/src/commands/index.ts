/**
 * Command registrations for Nightgauge Pipeline extension
 *
 * Re-exports all command registration functions.
 */

export { registerRunStageCommand } from "./runStage";
export { registerRunInteractiveStageCommand } from "./runInteractiveStage";
export { registerShowDashboardCommand, registerRescrubDashboardCommand } from "./showDashboard";
export { registerStopPipelineCommand } from "./stopPipeline";
export { registerStopBatchAfterCurrentCommand } from "./stopBatchAfterCurrent";
export { registerStopSlotCommand } from "./stopSlot";
export { registerStopEpicCommand } from "./stopEpic";
export { registerPipelineQuickActionsCommand } from "./pipelineQuickActions";
export { registerRefreshPipelineCommand } from "./refreshPipeline";
export { registerViewContextCommand } from "./viewContext";
export { registerRetryStageCommand } from "./retryStage";
export { registerRetryFromPhaseCommand } from "./retryFromPhase";

export {
  registerRefreshProjectBoardCommands,
  type ProjectBoardProviders,
} from "./refreshProjectBoard";
export { registerSortProjectBoardCommand } from "./sortProjectBoard";
export { registerFilterProjectBoardCommand } from "./filterProjectBoard";
export {
  registerSearchProjectBoardCommand,
  registerClearSearchProjectBoardCommand,
} from "./searchProjectBoard";
export { registerPickupIssueCommand, registerViewIssueOnGitHubCommand } from "./pickupIssue";
export { registerResetPipelineCommand } from "./resetPipeline";
export { registerAbortPipelineCommand } from "./abortPipeline";
export { registerShowPipelineSummaryCommand } from "./showPipelineSummary";
export { registerSelectTargetBranchCommand } from "./selectTargetBranch";
export { registerShowSettingsCommand } from "./showSettings";
export { registerSwitchAdapterCommand } from "./switchAdapter";
export { registerDisableAutoAcceptCommand } from "./disableAutoAccept";
export { registerEpicGroupCommands } from "./epicGroupCommands";
export {
  registerStartPipelineForIssueCommand,
  registerQueueCommands,
} from "./startPipelineForIssue";
export { registerPausePipelineCommand } from "./pausePipeline";
export { registerResumePipelineCommand } from "./resumePipeline";
export { registerMoveQueueItemUpCommand } from "./moveQueueItemUp";
export { registerMoveQueueItemDownCommand } from "./moveQueueItemDown";
export { registerRemoveQueueItemCommand } from "./removeQueueItem";
export { registerRetryQueueItemCommand } from "./retryQueueItem";
export { registerClearPipelineHistoryCommand } from "./clearPipelineHistory";
export {
  registerCheckEpicCompletionCommand,
  runEpicCompletionSweep,
  type EpicSweepResult,
} from "./checkEpicCompletion";

// Query commands
export { registerQueryProjectItemsCommand, registerClearQueryCommand } from "./queryProjectItems";
export { registerSaveQueryCommand, registerSaveQueryAsCommand } from "./saveQuery";
export {
  registerLoadSavedQueryCommand,
  registerDeleteSavedQueryCommand,
  registerManageSavedQueriesCommand,
} from "./loadSavedQuery";
export { registerExportTelemetryCommand } from "./exportTelemetry";
export { registerRunPipelineHealthCommand } from "./runPipelineHealth";
export { registerRecalibrateHealthCommand } from "./recalibrateHealth";
export { registerRunPipelineWithModelCommand } from "./runPipelineWithModel";
export { registerKnowledgeNewEntryCommand } from "./knowledge/newEntry.js";
export { registerKnowledgeScaffoldForIssueCommand } from "./knowledge/scaffoldForIssue.js";
export { registerKnowledgeNewADRCommand } from "./knowledge/newADR.js";
export { registerSignInCommand } from "./signIn";
export { registerSignOutCommand } from "./signOut";
export { registerManageSubscriptionCommand } from "./manageSubscription";

// Autonomous mode commands (Issue #2373)
export { registerAutonomousCommands, disposeAutonomousOutputChannel } from "./autonomousCommands";

// Performance mode selector (Issue #3009 — replaces Supercharge from #2433)
export { registerSelectPerformanceModeCommand } from "./selectPerformanceMode";

// Auto-merge guard command (Issue #2720)
export { registerFixAutoMergeSettingCommand } from "./fixAutoMergeSetting";

// Notifier Settings panel (Issue #3379)
export { registerShowNotifierSettingsCommand } from "./showNotifierSettings";

// Edit team config — open .nightgauge/config.yaml with status bar reminder (Issue #3337)
export { registerEditTeamConfigCommand } from "./editTeamConfig";

// Dashboard deep-link commands (Issue #3325)
export { registerAuditDashboardCommands } from "./auditCommands";

// Platform environment switcher (Issue #3720)
export { registerSwitchPlatformEnvironmentCommand } from "./switchPlatformEnvironment";
