/**
 * Centralized command registration for Nightgauge Pipeline extension
 *
 * Extracts ALL command registrations from extension.ts into a single
 * `registerAllCommands()` function, reducing extension.ts line count
 * and grouping command wiring in one place.
 *
 * @see Issue #1902 - Reactive UI shell / event-driven tree providers
 */

import * as vscode from "vscode";

// Register functions from './index' (barrel export)
import {
  registerRunStageCommand,
  registerRunInteractiveStageCommand,
  registerShowDashboardCommand,
  registerRescrubDashboardCommand,
  registerStopPipelineCommand,
  registerStopBatchAfterCurrentCommand,
  registerStopSlotCommand,
  registerStopEpicCommand,
  registerPipelineQuickActionsCommand,
  registerPausePipelineCommand,
  registerResumePipelineCommand,
  registerRefreshPipelineCommand,
  registerViewContextCommand,
  registerRetryStageCommand,
  registerRetryFromPhaseCommand,
  registerRefreshProjectBoardCommands,
  registerSortProjectBoardCommand,
  registerFilterProjectBoardCommand,
  registerSearchProjectBoardCommand,
  registerClearSearchProjectBoardCommand,
  registerPickupIssueCommand,
  registerViewIssueOnGitHubCommand,
  registerResetPipelineCommand,
  registerAbortPipelineCommand,
  registerShowPipelineSummaryCommand,
  registerSelectTargetBranchCommand,
  registerShowSettingsCommand,
  registerSwitchAdapterCommand,
  registerDisableAutoAcceptCommand,
  registerEpicGroupCommands,
  registerStartPipelineForIssueCommand,
  registerQueueCommands,
  registerClearPipelineHistoryCommand,
  registerQueryProjectItemsCommand,
  registerClearQueryCommand,
  registerSaveQueryCommand,
  registerSaveQueryAsCommand,
  registerLoadSavedQueryCommand,
  registerDeleteSavedQueryCommand,
  registerManageSavedQueriesCommand,
  registerExportTelemetryCommand,
  registerRunPipelineHealthCommand,
  registerRecalibrateHealthCommand,
  registerRunPipelineWithModelCommand,
  registerKnowledgeNewEntryCommand,
  registerKnowledgeScaffoldForIssueCommand,
  registerKnowledgeNewADRCommand,
  registerAutonomousCommands,
  registerSelectPerformanceModeCommand,
  registerShowNotifierSettingsCommand,
  registerEditTeamConfigCommand,
  registerAuditDashboardCommands,
  registerSwitchPlatformEnvironmentCommand,
} from "./index";

import type { ProjectBoardProviders } from "./index";

// Additional register functions imported individually
import { registerResetUsageCounterCommand } from "./resetUsageCounter";
import { registerClearCompletedIssuesCommand } from "./clearCompletedIssues";
import { registerClearFailedIssuesCommand } from "./clearFailedIssues";
import { registerRetryFailedIssueCommand } from "./retryFailedIssue";
import { registerCheckEpicCompletionCommand } from "./checkEpicCompletion";
import { registerAdapterDoctorCommand } from "./adapterDoctor";
import { registerAddIssueToPipelineCommand } from "./addIssueToPipeline";
import { registerAddEpicToPipelineCommand } from "./addEpicToPipeline";
import { registerStopQueueAfterCurrentCommand } from "./stopQueueAfterCurrent";
import { registerRemoveIssueFromPipelineCommand } from "./removeIssueFromPipeline";
import { registerFocusPipelineViewCommand } from "./focusPipelineView";
import { registerFocusProjectBoardViewCommand } from "./focusProjectBoardView";
import { registerAttentionCommands } from "./attentionCommands";
import type { AttentionTreeProvider, AttentionTreeItem } from "../views/attention";
import { registerConfigureForgeInstanceCommand } from "./configureForgeInstance";
import { registerConfigureDiscordWebhookCommand } from "./configureDiscordWebhook";
import { registerConfigureMattermostWebhookCommand } from "./configureMattermostWebhook";
import { registerConfigureMattermostWorkspaceCommand } from "./configureMattermostWorkspace";
import { registerSignInCommand } from "./signIn";
import { registerSignOutCommand } from "./signOut";
import { registerManageSubscriptionCommand } from "./manageSubscription";
import { registerActivateLicenseCommand } from "./activateLicense";
import { registerStartTrialCommand } from "./startTrial";
import { TrialStateStore } from "../platform/TrialState";
import { registerTelemetrySettingsCommand } from "./telemetrySettings";
import type { GitHubAuthService } from "../services/GitHubAuthService";
import type { SessionManager } from "../platform/SessionManager";
import type { IOnDemandTokenRefresher } from "../platform/TokenRefreshManager";
import { registerSortRepositoriesViewCommand } from "./sortRepositoriesView";
import { registerFilterRepositoriesViewCommand } from "./filterRepositoriesView";
import { registerToggleSequentialRepoCommand } from "./toggleSequentialRepo";
import { registerSetRepoMaxConcurrentCommand } from "./setRepoMaxConcurrent";
import { registerToggleAllReposInAutonomousScanCommands } from "./toggleAllReposInAutonomousScan";
import { registerSearchRepositoriesViewCommand } from "./searchRepositoriesView";
import { registerFixAutoMergeSettingCommand } from "./fixAutoMergeSetting";
import { registerSetConcurrentSlotsCommand } from "./setConcurrentSlots";
import type { RepositorySettingsService } from "../services/RepositorySettingsService";
import type { EpicGroupTreeItem } from "../views/items/EpicGroupTreeItem";
import type { RepositoryTreeItem } from "../views/items/RepositoryTreeItem";
import type { SequentialRepoConfigService } from "../utils/sequentialRepoConfig";
import type { EnabledReposConfigService } from "../utils/enabledReposConfig";
import type { RuntimeStateStore } from "../config/RuntimeStateStore";

// Types
// Views
import type {
  PipelineTreeProvider,
  ContextFileViewer,
  Dashboard,
  OutputWindow,
  RepositoriesTreeProvider,
  QueryResultsTreeProvider,
} from "../views";
import type { BrownfieldDashboard } from "../views/brownfield/BrownfieldDashboard";
import type { KnowledgeValueDashboard } from "../views/dashboard/KnowledgeValueDashboard";
import type { SlotOutputManager } from "../views/SlotOutputManager";

// Services
import type { PluginSetupService } from "../services/PluginSetupService";
import type { CodexSetupService } from "../services/CodexSetupService";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { CompletedIssuesService } from "../services/CompletedIssuesService";
import type { WorkspaceManager } from "../services/WorkspaceManager";
import type { QueryService } from "../services/QueryService";
import type { SavedQueriesService } from "../services/SavedQueriesService";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { UsageLimitsService } from "../services/UsageLimitsService";
import type { OAuthDeviceFlowService } from "../services/OAuthDeviceFlowService";
import type { TelemetryService } from "../services/TelemetryService";
import type { TelemetryConsentService } from "../services/TelemetryConsentService";

// Platform
import type { TierGate } from "../platform/TierGate";
import type { LicensePreflight } from "../platform/LicensePreflight";

// Utils
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";

/**
 * All dependencies needed by command registrations.
 *
 * Nullable fields use `| null` for services that may not be initialized
 * depending on workspace configuration.
 */
export interface AllCommandDeps {
  context: vscode.ExtensionContext;
  logger: Logger;
  statusBar: StatusBarManager;
  treeProvider: PipelineTreeProvider;
  projectBoardProviders: ProjectBoardProviders;
  contextViewer: ContextFileViewer;
  dashboard: Dashboard;
  outputWindow: OutputWindow;
  pluginSetupService: PluginSetupService;
  codexSetupService: CodexSetupService;
  headlessOrchestrator: HeadlessOrchestrator | null;
  pipelineStateService: PipelineStateService | null;
  issueQueueService: IssueQueueService | null;
  completedIssuesService: CompletedIssuesService;
  concurrentPipelineManager: ConcurrentPipelineManager | null;
  queryService: QueryService | null;
  savedQueriesService: SavedQueriesService | null;
  queryResultsProvider: QueryResultsTreeProvider | null;
  workspaceManager: WorkspaceManager | null;
  repositoriesTreeProvider: RepositoriesTreeProvider | null;
  slotOutputManager: SlotOutputManager | null;
  brownfieldDashboard: BrownfieldDashboard | null;
  knowledgeValueDashboard: KnowledgeValueDashboard | null;
  usageLimitsService: UsageLimitsService | null;
  oauthDeviceFlowService: OAuthDeviceFlowService | null;
  gitHubAuthService: GitHubAuthService | null;
  sessionManager: SessionManager | null;
  tokenRefreshManager: IOnDemandTokenRefresher | null;
  tierGate: TierGate | null;
  licensePreflight: LicensePreflight | null;
  incrediRoot: string | null;
  telemetryService: TelemetryService | null;
  telemetryConsentService: TelemetryConsentService | null;
  repositorySettingsService: RepositorySettingsService | null;
  /**
   * Phase 3 of #3313 (#3336) — runtime-tier writers, threaded through to
   * the autonomous and settings commands so they don't need to import the
   * legacy YAML helpers.
   */
  sequentialRepoConfigService: SequentialRepoConfigService | null;
  enabledReposConfigService: EnabledReposConfigService | null;
  runtimeStateStore: RuntimeStateStore | null;
  /** Action Center sidebar tree (ADR 015 / #325). */
  attentionTreeProvider: AttentionTreeProvider;
  attentionTreeView: vscode.TreeView<AttentionTreeItem>;
}

/**
 * Register ALL commands for the Nightgauge extension.
 *
 * This consolidates every command registration that was previously
 * scattered across extension.ts into a single orchestration point.
 */
export function registerAllCommands(deps: AllCommandDeps): void {
  const {
    context,
    logger,
    statusBar,
    treeProvider,
    projectBoardProviders,
    contextViewer,
    dashboard,
    outputWindow,
    pluginSetupService,
    codexSetupService,
    headlessOrchestrator,
    pipelineStateService,
    issueQueueService,
    completedIssuesService,
    concurrentPipelineManager,
    queryService,
    savedQueriesService,
    queryResultsProvider,
    workspaceManager,
    repositoriesTreeProvider,
    slotOutputManager,
    brownfieldDashboard,
    knowledgeValueDashboard,
    usageLimitsService,
    oauthDeviceFlowService,
    gitHubAuthService,
    sessionManager,
    tokenRefreshManager,
    tierGate,
    licensePreflight,
    incrediRoot,
    telemetryConsentService,
    repositorySettingsService,
    sequentialRepoConfigService,
    enabledReposConfigService,
    runtimeStateStore,
    attentionTreeProvider,
    attentionTreeView,
  } = deps;

  // Local free-trial record (countdown source). Shared by the status bar
  // (constructed separately in bootstrap over the same globalState) and the
  // trial/activation/sign-out commands below (#1138).
  const trialStore = new TrialStateStore(context.globalState);

  // ── Inline commands ──────────────────────────────────────────────────

  // Show output window — explicit user action from the command palette,
  // so force the panel to the foreground via reveal().
  const showOutputWindowCommand = vscode.commands.registerCommand(
    "nightgauge.showOutputWindow",
    () => {
      outputWindow.reveal();
    }
  );

  // Show slot output — reveals the per-slot VSCode output channel (Issue #1635)
  const showSlotOutputCommand = vscode.commands.registerCommand(
    "nightgauge-pipeline.showSlotOutput",
    (item: { slotIndex: number }) => {
      if (slotOutputManager && item != null) {
        slotOutputManager.revealSlotChannel(item.slotIndex);
      }
    }
  );

  // Clear output window (Issue #157)
  const clearOutputWindowCommand = vscode.commands.registerCommand(
    "nightgauge.clearOutputWindow",
    async () => {
      await outputWindow.clearWithConfirmation();
    }
  );

  // Copy to clipboard (Issue #156)
  const copyOutputToClipboardCommand = vscode.commands.registerCommand(
    "nightgauge.copyOutputToClipboard",
    async () => {
      await outputWindow.copyToClipboard();
    }
  );

  // Cleanup session logs — prunes on-disk session logs beyond retention
  // window. Prevents 999+ stale logs from spawning archived tabs on reload.
  const cleanupSessionLogsCommand = vscode.commands.registerCommand(
    "nightgauge.cleanupSessionLogs",
    async () => {
      if (!incrediRoot) {
        vscode.window.showWarningMessage(
          "Nightgauge: No workspace root detected; cannot clean session logs."
        );
        return;
      }
      const confirmed = await vscode.window.showWarningMessage(
        "Delete old session logs? This removes all but the 10 most recent logs from the last 24 hours.",
        { modal: true },
        "Delete"
      );
      if (confirmed !== "Delete") return;
      const { LogFileWriter } = await import("../utils/log-file-writer");
      const result = await LogFileWriter.cleanupLogs(incrediRoot, {
        max_count: 10,
        max_age_days: 1,
      });
      vscode.window.showInformationMessage(
        `Nightgauge: Session logs cleaned — kept ${result.kept}, deleted ${result.deleted}${result.failed > 0 ? `, failed ${result.failed}` : ""}.`
      );
    }
  );

  // Plugin setup
  const setupPluginsCommand = vscode.commands.registerCommand(
    "nightgauge.setupPlugins",
    async () => {
      await pluginSetupService.showSetupPrompt();
    }
  );

  // Codex setup
  const setupCodexCommand = vscode.commands.registerCommand("nightgauge.setupCodex", async () => {
    await codexSetupService.showSetupPrompt();
  });

  // Reset session
  const resetSessionCommand = vscode.commands.registerCommand(
    "nightgauge.resetSession",
    async () => {
      await dashboard.resetSession();
      vscode.window.showInformationMessage("Nightgauge: Session metrics reset successfully");
    }
  );

  // Subscription URL commands (Issue #1477)
  const openUpgradeUrlCommand = vscode.commands.registerCommand(
    "nightgauge.openUpgradeUrl",
    async () => {
      await vscode.env.openExternal(vscode.Uri.parse("https://nightgauge.dev/upgrade"));
    }
  );

  const openManageSubscriptionCommand = vscode.commands.registerCommand(
    "nightgauge.openManageSubscription",
    async () => {
      await vscode.env.openExternal(vscode.Uri.parse("https://nightgauge.dev/account"));
    }
  );

  const openSubscriptionUrlCommand = vscode.commands.registerCommand(
    "nightgauge.openSubscriptionUrl",
    async (url: string) => {
      if (url) {
        await vscode.env.openExternal(vscode.Uri.parse(url));
      }
    }
  );

  // Brownfield dashboard (Issue #1163)
  const showBrownfieldDashboardCommand = vscode.commands.registerCommand(
    "nightgauge.showBrownfieldDashboard",
    () => {
      if (brownfieldDashboard) {
        brownfieldDashboard.show();
      } else {
        vscode.window.showWarningMessage(
          "Brownfield Dashboard requires a workspace with .nightgauge directory."
        );
      }
    }
  );

  // Knowledge Value dashboard (Issue #3600)
  const showKnowledgeValueDashboardCommand = vscode.commands.registerCommand(
    "nightgauge.openKnowledgeValueDashboard",
    () => {
      if (knowledgeValueDashboard) {
        knowledgeValueDashboard.show();
      } else {
        vscode.window.showWarningMessage(
          "Knowledge Value Dashboard requires a workspace with .nightgauge directory."
        );
      }
    }
  );

  // ── Repository commands (Issue #329) ─────────────────────────────────

  // `nightgauge.switchToRepository` and `nightgauge.switchRepository`
  // were removed in the current-repo refactor. Pipeline and board routing
  // now pick the right repo from each call site's context (issue metadata,
  // active editor, or explicit argument) rather than a workspace-global
  // pointer. See docs/MULTI_REPO_WORKSPACE.md.

  const openRepoInGitHubCommand = vscode.commands.registerCommand(
    "nightgauge.openRepoInGitHub",
    async (repoItem: RepositoryTreeItem) => {
      if (!repoItem || !repoItem.repository) {
        logger.warn("No repository item provided");
        return;
      }

      const repo = repoItem.repository;
      const github = repo.github;

      if (!github) {
        // Try to load config if not loaded
        await repo.loadConfig();
        if (!repo.github) {
          vscode.window.showWarningMessage(`No GitHub configuration found for ${repo.name}`);
          return;
        }
      }

      const url = `https://github.com/${repo.github.owner}/${repo.github.repo}`;
      await vscode.env.openExternal(vscode.Uri.parse(url));
      logger.info("Opened repository in GitHub", { repoName: repo.name, url });
    }
  );

  const refreshRepositoriesCommand = vscode.commands.registerCommand(
    "nightgauge.refreshRepositories",
    async () => {
      if (!repositoriesTreeProvider) {
        logger.warn("RepositoriesTreeProvider not initialized");
        return;
      }

      repositoriesTreeProvider.invalidateAndRefreshRepo();
      logger.info("Refreshed repositories tree (cache cleared)");
    }
  );

  // #3437 — diagnostic dump of all autonomous-toggle state. Prints to the
  // "Nightgauge Pipeline" Output channel so the user can capture
  // before/after a toggle and we can compare. Temporary while we hunt
  // the toggle-cascade bug.
  const debugDumpAutonomousStateCommand = vscode.commands.registerCommand(
    "nightgauge.debugDumpAutonomousState",
    async () => {
      if (!repositoriesTreeProvider) {
        logger.warn("[debug-dump] RepositoriesTreeProvider not initialized");
        return;
      }
      try {
        const dump = (
          repositoriesTreeProvider as unknown as {
            dumpAutonomousState?: () => Record<string, unknown>;
          }
        ).dumpAutonomousState?.();
        logger.info("[debug-dump] autonomous state", dump ?? { error: "no dump method" });
        vscode.window.showInformationMessage(
          "Autonomous state dumped to 'Nightgauge Pipeline' output channel."
        );
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        logger.error("[debug-dump] failed", { error: msg });
      }
    }
  );

  // Auto-refresh toggle — pauses IPC-driven and onItemsUpdated-driven refreshes
  // on the Repositories view. Workspace/config changes and manual refresh still
  // fire, so correctness is preserved; the goal is to suppress the steady
  // stream of GraphQL-backed refreshes when the user wants to conserve quota.
  const toggleAutoRefresh = async () => {
    if (!repositoriesTreeProvider) {
      logger.warn("RepositoriesTreeProvider not initialized");
      return;
    }
    const next = !repositoriesTreeProvider.isAutoRefreshEnabled();
    repositoriesTreeProvider.setAutoRefreshEnabled(next);
    await vscode.commands.executeCommand("setContext", "nightgauge.repositoriesAutoRefresh", next);
    vscode.window.showInformationMessage(
      next
        ? "Repositories view: auto-refresh enabled."
        : "Repositories view: auto-refresh paused. Use Refresh to update manually."
    );
  };

  const toggleRepositoriesAutoRefreshCommand = vscode.commands.registerCommand(
    "nightgauge.repositoriesToggleAutoRefresh",
    toggleAutoRefresh
  );
  // Alias: same handler, different icon in the view-title toolbar when paused.
  // See manifest entry `nightgauge.repositoriesToggleAutoRefresh.resume`.
  const resumeRepositoriesAutoRefreshCommand = vscode.commands.registerCommand(
    "nightgauge.repositoriesToggleAutoRefresh.resume",
    toggleAutoRefresh
  );

  const refreshRepositoryCommand = vscode.commands.registerCommand(
    "nightgauge.refreshRepository",
    async (repoItem: RepositoryTreeItem) => {
      if (!repoItem || !repoItem.repository) {
        logger.warn("No repository item provided");
        return;
      }

      const repo = repoItem.repository;
      const github = repo.github;

      if (!github) {
        await repo.loadConfig();
        if (!repo.github) {
          vscode.window.showWarningMessage(`No GitHub configuration for ${repo.name}`);
          return;
        }
      }

      const repoSlug = `${repo.github!.owner}/${repo.github!.repo}`;
      repositoriesTreeProvider?.invalidateAndRefreshRepo(repoSlug);
      logger.info("Refreshed repository", { repoSlug });
    }
  );

  // ── Run epic batch (Issue #213) ──────────────────────────────────────
  // Routes through the queue so concurrent mode is respected (same as addEpicToPipeline).
  const runEpicBatchCommand = vscode.commands.registerCommand(
    "nightgauge.runEpicBatch",
    async (epicItem: EpicGroupTreeItem) => {
      if (!epicItem || !epicItem.getChildIssueNumbers) {
        vscode.window.showWarningMessage("Please select an epic group to run batch pipeline");
        return;
      }

      const issueNumbers = epicItem.getChildIssueNumbers();
      if (issueNumbers.length === 0) {
        vscode.window.showWarningMessage("No issues found in the selected epic");
        return;
      }

      const epicNumber = epicItem.epic?.number;
      const title = epicItem.epic?.title ?? `Epic #${epicNumber}`;

      logger.info("Running epic batch", {
        epicNumber,
        issueCount: issueNumbers.length,
      });

      // Route through the queue so concurrent pipeline is used when max_concurrent > 1.
      // This unifies behavior: both "Run All" and "Add to Pipeline" buttons
      // respect the same concurrency config.
      await vscode.commands.executeCommand("nightgauge.addEpicToPipeline", epicItem);
    }
  );

  // Refresh query results — triggers tree refresh from queryService state
  const refreshQueryResultsCommand = vscode.commands.registerCommand(
    "nightgauge.refreshQueryResults",
    () => {
      if (queryResultsProvider) {
        queryResultsProvider.refresh();
      }
    }
  );

  // ── Register all command functions ───────────────────────────────────

  // Usage counter reset (registered separately — pushes to subscriptions internally)
  if (usageLimitsService) {
    registerResetUsageCounterCommand(context, usageLimitsService);
  }

  // Push all disposable commands into subscriptions
  context.subscriptions.push(
    // Stage execution commands
    registerRunStageCommand(
      null,
      logger,
      statusBar,
      treeProvider,
      outputWindow,
      pipelineStateService
    ),
    registerRunInteractiveStageCommand(
      logger,
      statusBar,
      treeProvider,
      outputWindow,
      pipelineStateService
    ),

    // Dashboard commands
    registerShowDashboardCommand(dashboard, logger),
    registerRescrubDashboardCommand(dashboard, logger),

    // Telemetry / health commands (require incrediRoot)
    ...(incrediRoot
      ? [
          registerExportTelemetryCommand(incrediRoot, logger),
          registerRunPipelineHealthCommand(incrediRoot, logger, dashboard),
          registerRecalibrateHealthCommand(incrediRoot, logger),
        ]
      : []),

    // Pipeline lifecycle commands
    registerStopPipelineCommand(
      headlessOrchestrator,
      logger,
      statusBar,
      pipelineStateService,
      concurrentPipelineManager
    ),
    registerStopBatchAfterCurrentCommand(
      headlessOrchestrator,
      logger,
      statusBar,
      concurrentPipelineManager
    ),
    registerStopSlotCommand(logger, concurrentPipelineManager),
    registerStopEpicCommand(logger, concurrentPipelineManager),
    registerPipelineQuickActionsCommand(logger, concurrentPipelineManager),
    registerPausePipelineCommand(
      headlessOrchestrator,
      pipelineStateService ?? null,
      logger,
      statusBar
    ),
    registerResumePipelineCommand(
      headlessOrchestrator,
      pipelineStateService ?? null,
      logger,
      statusBar,
      concurrentPipelineManager
    ),
    registerRefreshPipelineCommand(treeProvider, logger, pipelineStateService ?? null),
    registerViewContextCommand(contextViewer, treeProvider, logger, concurrentPipelineManager),

    // Retry commands
    registerRetryStageCommand(
      headlessOrchestrator,
      pipelineStateService ?? null,
      logger,
      statusBar,
      outputWindow
    ),
    registerRetryFromPhaseCommand(
      headlessOrchestrator,
      pipelineStateService ?? null,
      logger,
      statusBar,
      outputWindow
    ),

    // Tree view refresh commands
    ...registerRefreshProjectBoardCommands(projectBoardProviders, logger),

    // Project board sorting / filtering / search
    registerSortProjectBoardCommand(projectBoardProviders, logger),
    registerFilterProjectBoardCommand(projectBoardProviders, logger),
    registerSearchProjectBoardCommand(projectBoardProviders, logger),
    registerClearSearchProjectBoardCommand(projectBoardProviders, logger),

    // Repositories view sorting / filtering / search (Issue #2189)
    ...(repositoriesTreeProvider
      ? [
          registerSortRepositoriesViewCommand(repositoriesTreeProvider, logger),
          registerFilterRepositoriesViewCommand(repositoriesTreeProvider, logger),
          registerSearchRepositoriesViewCommand(repositoriesTreeProvider, logger),
          registerToggleSequentialRepoCommand(repositoriesTreeProvider),
          registerSetRepoMaxConcurrentCommand(repositoriesTreeProvider, tierGate, licensePreflight),
          ...registerToggleAllReposInAutonomousScanCommands(repositoriesTreeProvider),
        ]
      : []),

    // Issue pickup
    registerPickupIssueCommand(
      logger,
      statusBar,
      treeProvider,
      outputWindow,
      pipelineStateService!,
      issueQueueService ?? undefined,
      concurrentPipelineManager ?? undefined
    ),

    // Model selection
    registerRunPipelineWithModelCommand(logger, headlessOrchestrator, statusBar),

    // Issue actions
    registerViewIssueOnGitHubCommand(logger),
    registerResetPipelineCommand(
      logger,
      pipelineStateService,
      headlessOrchestrator,
      treeProvider,
      statusBar,
      completedIssuesService
    ),
    registerAbortPipelineCommand(
      headlessOrchestrator,
      logger,
      statusBar,
      pipelineStateService,
      treeProvider,
      concurrentPipelineManager
    ),

    // Summary / settings / adapter
    registerShowPipelineSummaryCommand(context.extensionUri, pipelineStateService, logger),
    registerSelectTargetBranchCommand(logger, pipelineStateService!),
    registerShowSettingsCommand(
      context.extensionUri,
      pipelineStateService,
      logger,
      concurrentPipelineManager,
      runtimeStateStore
    ),
    registerSwitchAdapterCommand(logger),
    registerSwitchPlatformEnvironmentCommand(logger, sessionManager),
    registerDisableAutoAcceptCommand(logger),

    // Epic group commands
    ...registerEpicGroupCommands(projectBoardProviders, logger),

    // Queue / pipeline-for-issue commands
    registerStartPipelineForIssueCommand(
      logger,
      headlessOrchestrator,
      issueQueueService ?? null,
      concurrentPipelineManager
    ),
    ...registerQueueCommands(
      logger,
      issueQueueService ?? null,
      headlessOrchestrator,
      concurrentPipelineManager
    ),

    // Completed / failed issues
    registerClearCompletedIssuesCommand(context),
    registerClearFailedIssuesCommand(context),

    // Forge instance configuration wizard (Issue #3364)
    registerConfigureForgeInstanceCommand(),

    // Discord webhook configuration
    registerConfigureDiscordWebhookCommand(),

    // Mattermost webhook configuration (Issue #3373)
    registerConfigureMattermostWebhookCommand(),

    // Mattermost full workspace configuration (Issue #3378)
    registerConfigureMattermostWorkspaceCommand(),

    // Notifier Settings panel — manage Discord/Mattermost instances (Issue #3379)
    registerShowNotifierSettingsCommand(context),

    // Edit team config — open project-tier config.yaml with status bar reminder (Issue #3337)
    registerEditTeamConfigCommand(),

    // Telemetry Settings — opt-in/opt-out (Issue #1481)
    ...(telemetryConsentService ? [registerTelemetrySettingsCommand(telemetryConsentService)] : []),

    // OAuth sign-in / sign-out (Issue #1464, #1467)
    ...(oauthDeviceFlowService && gitHubAuthService
      ? [
          registerSignInCommand(oauthDeviceFlowService, gitHubAuthService, logger),
          registerSignOutCommand(oauthDeviceFlowService, logger, trialStore),
          vscode.commands.registerCommand("nightgauge.signInWithGitHub", async () => {
            await gitHubAuthService.signInWithGitHub();
          }),
        ]
      : []),

    // Subscription management — opens Stripe Customer Portal (Issue #1478)
    registerManageSubscriptionCommand(
      sessionManager,
      licensePreflight,
      logger,
      tokenRefreshManager
    ),

    // License activation — verify + store a purchased/trial key (Issue #1138)
    registerActivateLicenseCommand(licensePreflight, logger, trialStore),

    // Start Free Trial — issue a 14-day Pro trial for the signed-in user (#1138)
    registerStartTrialCommand(
      sessionManager,
      licensePreflight,
      logger,
      tokenRefreshManager,
      trialStore
    ),

    // Retry / restart failed issues
    registerRetryFailedIssueCommand(context, headlessOrchestrator!, pipelineStateService!),
    registerClearPipelineHistoryCommand(pipelineStateService ?? null, logger),
    // Accessibility commands (Issue #304)
    registerAddIssueToPipelineCommand(issueQueueService!, logger),
    registerAddEpicToPipelineCommand(issueQueueService!, logger, tierGate, licensePreflight),
    registerStopQueueAfterCurrentCommand(
      headlessOrchestrator,
      logger,
      statusBar,
      concurrentPipelineManager,
      issueQueueService
    ),
    registerRemoveIssueFromPipelineCommand(issueQueueService!, logger),
    registerFocusPipelineViewCommand(logger),
    registerFocusProjectBoardViewCommand(logger),

    // Epic lifecycle commands (Issue #520)
    registerCheckEpicCompletionCommand(logger, outputWindow),
    registerAdapterDoctorCommand(context, logger, outputWindow),

    // Query commands (Issue #138)
    registerQueryProjectItemsCommand(queryService!, savedQueriesService!, logger),
    registerClearQueryCommand(queryService!, logger),
    registerSaveQueryCommand(queryService!, savedQueriesService!, logger),
    registerSaveQueryAsCommand(savedQueriesService!, logger),
    registerLoadSavedQueryCommand(queryService!, savedQueriesService!, logger),
    registerDeleteSavedQueryCommand(savedQueriesService!, logger),
    registerManageSavedQueriesCommand(savedQueriesService!, logger),

    // Knowledge commands
    registerKnowledgeNewEntryCommand(logger),
    registerKnowledgeScaffoldForIssueCommand(logger),
    registerKnowledgeNewADRCommand(logger),

    // Autonomous mode commands (Issue #2373)
    ...registerAutonomousCommands(
      logger,
      statusBar,
      issueQueueService,
      enabledReposConfigService,
      concurrentPipelineManager
        ? () => concurrentPipelineManager.fillSlots().then(() => undefined)
        : undefined,
      workspaceManager
    ),

    // Action Center — tree, badge, toast, quick-pick resolve (ADR 015 / #325)
    ...registerAttentionCommands({
      provider: attentionTreeProvider,
      treeView: attentionTreeView,
      logger,
    }),

    // Performance mode selector (Issue #3009 — replaces Supercharge from #2433)
    registerSelectPerformanceModeCommand(logger, statusBar),

    // Auto-merge guard command (Issue #2720)
    ...(repositorySettingsService
      ? [registerFixAutoMergeSettingCommand(logger, repositorySettingsService)]
      : []),

    // Concurrent slots — on-the-fly control from the pipeline view title bar
    registerSetConcurrentSlotsCommand(
      concurrentPipelineManager,
      incrediRoot,
      tierGate,
      licensePreflight
    ),

    // Inline command disposables
    refreshQueryResultsCommand,
    runEpicBatchCommand,
    openRepoInGitHubCommand,
    refreshRepositoriesCommand,
    debugDumpAutonomousStateCommand,
    toggleRepositoriesAutoRefreshCommand,
    resumeRepositoriesAutoRefreshCommand,
    refreshRepositoryCommand,
    showOutputWindowCommand,
    showSlotOutputCommand,
    clearOutputWindowCommand,
    copyOutputToClipboardCommand,
    cleanupSessionLogsCommand,
    setupPluginsCommand,
    setupCodexCommand,
    resetSessionCommand,
    showBrownfieldDashboardCommand,
    showKnowledgeValueDashboardCommand,
    openUpgradeUrlCommand,
    openManageSubscriptionCommand,
    openSubscriptionUrlCommand,

    // Dashboard deep-link commands (Issue #3325)
    // accountId not yet in TokenStorage — ADR-002 in decisions.md
    ...registerAuditDashboardCommands(() => undefined, tierGate, licensePreflight)
  );
}
