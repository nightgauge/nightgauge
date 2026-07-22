/**
 * Nightgauge VS Code Extension
 *
 * Main entry point for the VS Code extension that integrates
 * with @nightgauge/sdk for pipeline orchestration.
 *
 * Service initialization is in bootstrap/services.ts.
 * Command registration is in commands/register-all.ts.
 *
 * @see docs/ARCHITECTURE.md for architectural overview
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { getStageLabel, killAllActiveProcesses } from "./utils/skillRunner";
import { applyPerRepoGitHubTokenEnv } from "./utils/perRepoGitHubTokenEnv";
import { CONTEXT_URI_SCHEME } from "./views";
import { initializeServices, type ExtensionServices } from "./bootstrap/services";
import { registerAllCommands } from "./commands/register-all";
import { IpcClient } from "./services/IpcClient";
import { ProjectEventSubscriber } from "./services/ProjectEventSubscriber";
import { setProjectEventSubscriber } from "./commands/autonomousCommands";
import { EventStreamService } from "./services/EventStreamService";
import { runMaxConcurrentMigration } from "./utils/maxConcurrentMigration";
import { runLegacyKeysMigration } from "./utils/legacyKeysMigration";
import { WorkspaceRegistrationPayloadBuilder } from "./services/WorkspaceRegistrationPayloadBuilder";
import { WorkspaceSyncStatusItem } from "./views/WorkspaceSyncStatusItem";
import { EventStreamStatusBarItem } from "./views/EventStreamStatusBarItem";
import { PlatformEnvironmentStatusBarItem } from "./platform/PlatformEnvironmentStatusBarItem";
import {
  shouldPersistWorkspaceSyncState,
  shouldRestoreWorkspaceSyncState,
} from "./views/workspaceSyncState";
import type { WorkspaceSyncSidebarState } from "./views/items/WorkspaceSyncSidebarItem";

/**
 * Extension services — initialized in activate(), used in deactivate().
 */
let services: ExtensionServices | null = null;

/**
 * Extension context — promoted to module scope so deactivate() can read globalState.
 */
let extensionContext: vscode.ExtensionContext | null = null;

/**
 * Extension activation
 *
 * Called when the extension is activated based on activation events
 * defined in package.json.
 */
export async function activate(context: vscode.ExtensionContext): Promise<void> {
  extensionContext = context;
  // Inject per-repo GH_TOKEN/GITHUB_TOKEN into the integrated-terminal env so
  // every `gh` call authenticates as this workspace's configured user instead of
  // the machine-global `gh auth` active account. This is what lets concurrent
  // windows owned by different GitHub users coexist. Fail-safe — never throws.
  applyPerRepoGitHubTokenEnv(context);
  // Initialize all services, tree views, and event wiring
  try {
    services = await initializeServices(context);
  } catch (error) {
    const msg = error instanceof Error ? error.message : String(error);
    console.error("[Nightgauge] Service initialization failed:", msg, error);
    vscode.window.showErrorMessage(`Nightgauge failed to activate: ${msg}`);
    throw error; // Re-throw so VSCode marks extension as failed
  }

  const {
    logger,
    statusBar,
    headlessOrchestrator,
    treeProvider,
    projectBoardProviders,
    contextViewer,
    dashboard,
    workflowTreeProvider,
    outputWindow,
    pluginSetupService,
    codexSetupService,
    pipelineStateService,
    issueQueueService,
    completedIssuesService,
    workspaceManager,
    repositoriesTreeProvider,
    concurrentPipelineManager,
    queryService,
    savedQueriesService,
    queryResultsProvider,
    slotOutputManager,
    brownfieldDashboard,
    knowledgeValueDashboard,
    usageLimitsService,
    notificationService,
    oauthDeviceFlowService,
    gitHubAuthService,
    sessionManager,
    tokenRefreshManager,
    tierGate,
    licensePreflight,
    incrediRoot,
    projectBoardViews,
    treeView,
    telemetryService,
    telemetryConsentService,
    telemetryUploaderService,
    repositorySettingsService,
    sequentialRepoConfigService,
    enabledReposConfigService,
    runtimeStateStore,
    attentionTreeProvider,
    attentionTreeView,
  } = services;

  // Register all commands
  try {
    registerAllCommands({
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
      telemetryService,
      telemetryConsentService,
      repositorySettingsService,
      sequentialRepoConfigService,
      enabledReposConfigService,
      runtimeStateStore,
      attentionTreeProvider,
      attentionTreeView,
    });
    logger.info("All commands registered successfully");
  } catch (error) {
    const msg = error instanceof Error ? error.message : String(error);
    const stack = error instanceof Error ? error.stack : "";
    logger.error("Command registration failed", { error: msg, stack });
    vscode.window.showErrorMessage(`Nightgauge command registration failed: ${msg}`);
    throw error;
  }

  // Push core disposables to context.subscriptions
  try {
    context.subscriptions.push(
      pluginSetupService,
      notificationService,
      vscode.workspace.registerTextDocumentContentProvider(CONTEXT_URI_SCHEME, contextViewer),
      treeView,
      ...projectBoardViews,
      treeProvider,
      ...projectBoardProviders.values(),
      contextViewer,
      dashboard,
      outputWindow,
      statusBar.item,
      statusBar.targetBranchItem,
      services.platformStatusBarItem,
      logger.getChannel()
    );
    if (telemetryUploaderService) {
      context.subscriptions.push(telemetryUploaderService);
    }
    logger.info("Core disposables pushed to subscriptions");
  } catch (error) {
    const msg = error instanceof Error ? error.message : String(error);
    logger.error("Disposable push failed", { error: msg });
    throw error;
  }

  // Auto-resume interrupted pipeline detection (Issue #1202)
  // Must run AFTER all services are initialized.
  // Use setTimeout to ensure all async initialization completes.
  if (pipelineStateService && headlessOrchestrator) {
    const resumeOrchestrator = headlessOrchestrator;
    const resumeStateService = pipelineStateService;

    setTimeout(async () => {
      try {
        // Don't prompt if pipeline is already running (e.g., from scanExistingContext)
        if (resumeOrchestrator.getIsRunning()) {
          logger.debug("Pipeline already running, skipping auto-resume check");
          return;
        }

        const interruptedInfo = await resumeStateService.getInterruptedPipelineInfo();
        if (!interruptedInfo) {
          return;
        }

        logger.info("Interrupted pipeline detected", {
          issueNumber: interruptedInfo.issueNumber,
          lastCompletedStage: interruptedInfo.lastCompletedStage,
          interruptedStage: interruptedInfo.interruptedStage,
          nextResumeStage: interruptedInfo.nextResumeStage,
          stagesCompleted: interruptedInfo.stagesCompleted,
          stagesRemaining: interruptedInfo.stagesRemaining,
        });

        const nextLabel = getStageLabel(interruptedInfo.nextResumeStage as PipelineStage);
        const action = await vscode.window.showInformationMessage(
          `Pipeline for #${interruptedInfo.issueNumber} was interrupted. ` +
            `${interruptedInfo.stagesCompleted} stage(s) completed. ` +
            `Resume from ${nextLabel}?`,
          "Resume",
          "Cancel"
        );

        if (action === "Resume") {
          // Don't resume if something else started running while prompt was showing
          if (resumeOrchestrator.getIsRunning()) {
            logger.info("Pipeline started while resume prompt was showing, skipping");
            return;
          }

          const resumeStage = await resumeStateService.prepareForResume();
          logger.info("Pipeline prepared for resume", {
            resumeStage,
            issueNumber: interruptedInfo.issueNumber,
          });

          // Set UI context for running state
          vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
          vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", false);

          // Fire-and-forget: runPipeline handles the full execution loop
          resumeOrchestrator
            .runPipeline(interruptedInfo.issueNumber)
            .then((result) => {
              if (result.success) {
                logger.info("Auto-resumed pipeline completed successfully", {
                  issueNumber: interruptedInfo.issueNumber,
                  completedStages: result.completedStages,
                });
              } else {
                logger.error("Auto-resumed pipeline failed", {
                  issueNumber: interruptedInfo.issueNumber,
                  failedStage: result.failedStage,
                  error: result.error,
                });
              }
            })
            .catch((err) => {
              logger.error("Auto-resume pipeline error", {
                issueNumber: interruptedInfo.issueNumber,
                error: err instanceof Error ? err.message : String(err),
              });
            });
        } else {
          logger.info("User declined auto-resume, clearing stale state", {
            issueNumber: interruptedInfo.issueNumber,
          });
          // Clear the stale state so it doesn't block future pipeline starts
          // or keep prompting on every reload (Issue #1643)
          await resumeStateService.clearPipeline();
        }
      } catch (error) {
        logger.warn("Auto-resume detection failed", { error });
      }
    }, 2000);
  }

  // One-time consolidation of legacy autonomous.max_concurrent into the
  // unified pipeline.max_concurrent key (Issue #3195). Best-effort: never
  // blocks activation, surfaces a modal only when both keys are set with
  // disagreeing values.
  if (incrediRoot) {
    setTimeout(() => {
      runMaxConcurrentMigration(context, incrediRoot, logger).catch((err) => {
        logger.warn("Max-concurrent migration threw", {
          error: err instanceof Error ? err.message : String(err),
        });
      });
    }, 3000);
  }

  // One-time migration of legacy project-tier keys to runtime/machine tiers (Issue #3338).
  // Runs slightly after the max-concurrent migration to avoid concurrent YAML writes.
  if (incrediRoot && runtimeStateStore) {
    const capturedStore = runtimeStateStore;
    setTimeout(() => {
      runLegacyKeysMigration(context, incrediRoot, capturedStore, logger).catch((err) => {
        logger.warn("Legacy keys migration threw", {
          error: err instanceof Error ? err.message : String(err),
        });
      });
    }, 3500);

    // Palette command: force-run the migration regardless of completion state
    context.subscriptions.push(
      vscode.commands.registerCommand("nightgauge.runSettingsMigration", () => {
        runLegacyKeysMigration(context, incrediRoot, capturedStore, logger, true).catch((err) => {
          logger.warn("Manual legacy keys migration threw", {
            error: err instanceof Error ? err.message : String(err),
          });
        });
      })
    );
  }

  // Auto-cleanup stale local branches (default: enabled)
  const autoCleanup = vscode.workspace
    .getConfiguration("nightgauge.git")
    .get<boolean>("autoCleanupBranches", true);
  if (autoCleanup) {
    setTimeout(async () => {
      try {
        const result = await IpcClient.getInstance().gitCleanupMergedBranches();
        if (result.count > 0) {
          logger.info(`Auto-cleanup: deleted ${result.count} stale local branch(es)`, {
            branches: result.deleted,
          });
        }
      } catch {
        // Non-fatal: IPC binary may not be running yet
      }
    }, 5000);
  }

  // ProjectEventSubscriber — wire into session lifecycle (Issue #3025).
  // Connect when authenticated + event_stream_enabled is true; disconnect on sign-out.
  if (services.sessionManager) {
    services.sessionManager.onSessionChanged((event) => {
      try {
        const configBridge = services!.sessionManager
          ? // Reach ConfigBridge via the services container (already initialised)
            (
              services as unknown as {
                configBridge?: {
                  getEffectiveConfig(): { config?: import("./config/schema").IncrediConfig } | null;
                };
              }
            ).configBridge
          : null;
        const cfg = configBridge?.getEffectiveConfig()?.config;
        const eventStreamEnabled = cfg?.autonomous?.event_stream_enabled ?? false;

        if (event.current === "authenticated" && eventStreamEnabled) {
          const accessToken = event.data.accessToken;
          if (!accessToken) return;

          const platformBaseUrl = cfg?.platform?.api_url ?? "https://api.nightgauge.dev";

          const subscriber = ProjectEventSubscriber.getInstance({
            context,
            logger,
            enabledRepos: [],
            // Wire token refresh so SSE 401s trigger an immediate re-auth (#3711)
            onAuthRequired: () =>
              services?.tokenRefreshManager?.forceRefresh() ?? Promise.resolve(null),
            // Fast-path: direct cache invalidation for status-change events (#3712)
            onStatusChanged: (repoSlug, statuses) => {
              services?.projectBoardService?.invalidateStatusCache(repoSlug, statuses);
            },
            // Prompt sign-in when SSE 401 occurs with no refresh token (#3723)
            onSignInRequired: () => {
              void vscode.commands.executeCommand("nightgauge.platform.signIn");
            },
          });
          setProjectEventSubscriber(subscriber);
          subscriber.connect(platformBaseUrl, accessToken);
          logger.info("ProjectEventSubscriber started", { platformBaseUrl });
        } else if (event.current === "unauthenticated" || event.current === "error") {
          ProjectEventSubscriber.resetInstance();
          setProjectEventSubscriber(null);
        }
      } catch (err) {
        logger.warn("ProjectEventSubscriber session handler failed", {
          error: err instanceof Error ? err.message : String(err),
        });
      }
    });
  }

  // EventStreamService — wire into session lifecycle (Issue #3321).
  // Connect when authenticated; disconnect on sign-out. Mirrors ProjectEventSubscriber pattern.
  if (services.sessionManager) {
    services.sessionManager.onSessionChanged((event) => {
      try {
        const configBridge = services!.sessionManager
          ? (
              services as unknown as {
                configBridge?: {
                  getEffectiveConfig(): { config?: import("./config/schema").IncrediConfig } | null;
                };
              }
            ).configBridge
          : null;
        const cfg = configBridge?.getEffectiveConfig()?.config;

        if (event.current === "authenticated") {
          const accessToken = event.data.accessToken;
          if (!accessToken) return;

          const platformBaseUrl = cfg?.platform?.api_url ?? "https://api.nightgauge.dev";
          const eventStreamService = EventStreamService.getInstance({
            context,
            logger,
            // Wire token refresh so SSE 401s trigger an immediate re-auth (#3711)
            tokenRefreshManager: services!.tokenRefreshManager!,
            // Prompt sign-in when SSE 401 occurs with no refresh token (#3723)
            onSignInRequired: () => {
              void vscode.commands.executeCommand("nightgauge.platform.signIn");
            },
          });
          eventStreamService.reconnect(platformBaseUrl, accessToken);
          dashboard?.setEventStreamService(eventStreamService);
          // Drive the live workflow sidebar tree off the SDK EventBus node
          // stream re-served over SSE (#3919). attach() is idempotent per
          // service instance; reset clears any stale fold from a prior session.
          workflowTreeProvider?.reset();
          workflowTreeProvider?.attach(eventStreamService);
          logger.info("EventStreamService started", { platformBaseUrl });

          // Wire stream health into the status bar (Issue #3715).
          // #3925 — use the non-throwing accessor: this EventStreamService
          // handler runs on every `authenticated` transition, but the
          // ProjectEventSubscriber is only constructed when
          // `event_stream_enabled` is true. The bare getInstance() threw
          // `requires options on first call` here (swallowed as a WARN) and
          // the status bar never attached. Skip the attach when absent.
          const subscriber = ProjectEventSubscriber.getInstanceOrNull();
          if (subscriber) {
            eventStreamStatusBarItem.attachStreams(eventStreamService, subscriber);
          }
        } else if (event.current === "unauthenticated" || event.current === "error") {
          EventStreamService.resetInstance();
          workflowTreeProvider?.reset();
        }
      } catch (err) {
        logger.warn("EventStreamService session handler failed", {
          error: err instanceof Error ? err.message : String(err),
        });
      }
    });
  }

  // WorkspaceSyncStatusItem — shows workspace sync state in the status bar.
  const workspaceSyncStatusItem = new WorkspaceSyncStatusItem();
  context.subscriptions.push(workspaceSyncStatusItem);

  // EventStreamStatusBarItem — aggregated SSE stream health indicator (Issue #3715).
  const eventStreamStatusBarItem = new EventStreamStatusBarItem();
  context.subscriptions.push(eventStreamStatusBarItem);

  // PlatformEnvironmentStatusBarItem — active environment indicator (Issue #3721).
  context.subscriptions.push(new PlatformEnvironmentStatusBarItem());
  context.subscriptions.push(
    vscode.commands.registerCommand("nightgauge.reconnectEventStreams", async () => {
      const token = await services?.tokenRefreshManager?.forceRefresh();
      if (!token) {
        void vscode.window.showWarningMessage(
          "Not signed in — please sign in to reconnect event streams."
        );
        return;
      }

      const configBridge = (
        services as unknown as {
          configBridge?: {
            getEffectiveConfig(): { config?: import("./config/schema").IncrediConfig } | null;
          };
        }
      ).configBridge;
      const cfg = configBridge?.getEffectiveConfig()?.config;
      const platformBaseUrl = cfg?.platform?.api_url ?? "https://api.nightgauge.dev";

      // #3925 — non-throwing accessors: the `?.` only helps if getInstance
      // doesn't throw first. With getInstanceOrNull a not-yet-initialized
      // stream is a no-op reconnect rather than a thrown error.
      EventStreamService.getInstanceOrNull()?.reconnect(platformBaseUrl, token);
      ProjectEventSubscriber.getInstanceOrNull()?.connect(platformBaseUrl, token);
      void vscode.window.showInformationMessage("Reconnecting event streams…");
    })
  );

  // syncSidebarStatus — mirrors status bar updates into the sidebar tree view and persists to globalState.
  const syncSidebarStatus = (
    status: import("./views/WorkspaceSyncStatusItem").WorkspaceSyncStatus,
    repoCount: number,
    errorMessage?: string
  ): void => {
    const workspaceName = workspaceManager?.getWorkspaceConfig()?.workspace?.name;
    const allRepos =
      workspaceManager
        ?.getAllRepositories()
        .map((r) =>
          r.github?.owner && r.github?.repo ? `${r.github.owner}/${r.github.repo}` : null
        )
        .filter((s): s is string => s !== null) ?? [];
    const state: WorkspaceSyncSidebarState = {
      status,
      repoCount,
      errorMessage,
      workspaceName,
      repos: allRepos,
    };
    treeProvider.setWorkspaceSyncStatus(state);
    // Only persist terminal states — "syncing" is transient (see
    // workspaceSyncState.ts) and would otherwise restore a stuck spinner.
    if (shouldPersistWorkspaceSyncState(status)) {
      void context.globalState.update("nightgauge.lastWorkspaceSyncState", state);
    }
  };

  // Restore last-known workspace sync state across reloads. A transient
  // "syncing" is never restored (also clears any such value persisted by an
  // older build), since no sync is in progress on a fresh activation.
  const lastSyncState = context.globalState.get<WorkspaceSyncSidebarState>(
    "nightgauge.lastWorkspaceSyncState"
  );
  if (lastSyncState && shouldRestoreWorkspaceSyncState(lastSyncState.status)) {
    treeProvider.setWorkspaceSyncStatus(lastSyncState);
  }

  // Stable signature of the repos an agent is registered to serve. Used to
  // re-register when the open workspace's repos change across a reload —
  // otherwise the heartbeat-only reload path keeps the agent bound to a stale
  // repo set and commands for newly-opened repos never route to it.
  const reposSignature = (repos: Array<{ owner: string; repo: string }>): string =>
    repos
      .map((r) => `${r.owner}/${r.repo}`.toLowerCase())
      .sort()
      .join(",");

  // AgentRegistrationService — register agent on authentication (#3544).
  // Fires once per authenticated session; re-registers only when stored agentId is absent or cleared.
  if (services.sessionManager && services.agentRegistrationService) {
    // Register/re-sync the agent for the current session. Defined as a function
    // so it runs from BOTH the session-change event below AND immediately at
    // activation when the session is already authenticated. On a reload,
    // SessionManager.restore() fires the "authenticated" transition during
    // bootstrap — before this listener is attached — so relying on the event
    // alone left the agent unregistered after every reload, and
    // dashboard-triggered pipelines sat queued forever (#3544).
    const registerOrSyncAgent = (): void => {
      void (async () => {
        const machineFingerprint = services!.machineFingerprint;
        const machineId = machineFingerprint?.getMachineId() ?? "";
        const agentVersion =
          vscode.extensions.getExtension("nightgauge.nightgauge-vscode")?.packageJSON?.version ??
          "0.1.0";
        const vsCodeVersion = vscode.version;

        // Priority 1: workspace config repos (#3546)
        const allRepos = workspaceManager?.getAllRepositories() ?? [];
        const workspaceRepos: Array<{ owner: string; repo: string }> = [];
        for (const repo of allRepos) {
          await repo.loadConfig();
          const gh = repo.github;
          if (gh?.owner && gh?.repo) {
            workspaceRepos.push({ owner: gh.owner, repo: gh.repo });
          }
        }

        let repos: Array<{ owner: string; repo: string }>;
        if (workspaceRepos.length > 0) {
          repos = workspaceRepos;
        } else {
          // Priority 2: enabledRepos fallback
          const repoSlugs = enabledReposConfigService?.readEnabledRepos() ?? [];
          if (repoSlugs.length > 0) {
            repos = repoSlugs
              .map((slug) => {
                const [owner, repo] = slug.split("/");
                return owner && repo ? { owner, repo } : null;
              })
              .filter((r): r is { owner: string; repo: string } => r !== null);
          } else {
            // Priority 3: platform config fallback
            const projCfg = (
              services as unknown as {
                configBridge?: {
                  getEffectiveConfig(): {
                    config?: import("./config/schema").IncrediConfig;
                  } | null;
                };
              }
            ).configBridge?.getEffectiveConfig()?.config;
            const platformCfg = projCfg?.platform as unknown as
              { owner?: string; defaultRepo?: string } | undefined;
            repos =
              platformCfg?.owner && platformCfg?.defaultRepo
                ? [{ owner: platformCfg.owner, repo: platformCfg.defaultRepo }]
                : [];
          }
        }

        const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(
          workspaceManager?.getWorkspaceConfig() ?? null
        );

        // Already registered (typically across a reload): keep the agent
        // alive via heartbeat and reconcile the indicator to "synced".
        // Registration does not re-run here, so without this an indicator
        // state persisted before the reload (e.g. a transient "syncing")
        // would otherwise stay shown indefinitely.
        const storedAgentId = context.globalState.get<string>("nightgauge.agentId");
        const currentReposSig = reposSignature(repos);
        const registeredReposSig = context.globalState.get<string>("nightgauge.agentRepos");
        // Reuse the existing registration ONLY when the open workspace's
        // repos are unchanged. If they changed (e.g. a different repo was
        // opened before this reload), fall through to re-register so the
        // agent serves the current repos — otherwise dashboard triggers for
        // the newly-opened repo stay queued forever (#3544 follow-up).
        if (storedAgentId && registeredReposSig === currentReposSig) {
          services!.agentHeartbeatService?.start(storedAgentId);
          // Open the SSE command stream so remotely-triggered pipelines are
          // received and acked, not just left queued (#3544).
          services!.agentCommandStreamService?.start(storedAgentId);
          if (workspaceMeta) {
            workspaceSyncStatusItem.setStatus("synced", repos.length);
            syncSidebarStatus("synced", repos.length);
          }
          return;
        }

        if (workspaceMeta) {
          workspaceSyncStatusItem.setStatus("syncing");
          syncSidebarStatus("syncing", 0);
        }
        const agentId = await services!.agentRegistrationService!.register({
          agent_version: agentVersion,
          capabilities: ["headless", "interactive"],
          repos,
          machine_id: machineId,
          vscode_version: vsCodeVersion,
          workspace: workspaceMeta,
        });

        if (agentId) {
          await context.globalState.update("nightgauge.agentId", agentId);
          // Remember which repos this registration covers so a later reload
          // can detect a repo change and re-register (#3544 follow-up).
          await context.globalState.update("nightgauge.agentRepos", currentReposSig);
          services!.agentHeartbeatService?.start(agentId);
          // Open the SSE command stream so remotely-triggered pipelines are
          // received and acked, not just left queued (#3544).
          services!.agentCommandStreamService?.start(agentId);
          if (workspaceMeta) {
            workspaceSyncStatusItem.setStatus("synced", repos.length);
            syncSidebarStatus("synced", repos.length);
          }
        } else {
          await context.globalState.update("nightgauge.agentId", undefined);
          // Surface the REAL failure (expired token / 5xx / network / bad body)
          // instead of the opaque "Registration returned no agentId" (#360).
          const detail =
            services!.agentRegistrationService!.getLastFailureDetail() ??
            "registration returned no agentId";
          logger.warn("Workspace sync registration failed", { detail });
          if (workspaceMeta) {
            workspaceSyncStatusItem.setStatus("failed", 0, detail);
            syncSidebarStatus("failed", 0, detail);
          }
        }
      })();
    };

    services.sessionManager.onSessionChanged((event) => {
      try {
        if (event.current === "authenticated") {
          registerOrSyncAgent();
        } else if (event.current === "unauthenticated" || event.current === "error") {
          void context.globalState.update("nightgauge.agentId", undefined);
        }
      } catch (err) {
        logger.warn("AgentRegistrationService session handler failed", {
          error: err instanceof Error ? err.message : String(err),
        });
      }
    });

    // Reload path: the session is already authenticated at activation, so the
    // event above never fires — register now so the agent is online and ready to
    // claim dashboard-triggered commands instead of leaving them queued (#3544).
    if (services.sessionManager.state === "authenticated") {
      registerOrSyncAgent();
    }
  }

  // Re-register agent when workspace config changes (#3546, #3668).
  // Mirrors IncrediYamlService debounce pattern with 500ms delay for workspace-level changes.
  if (services.sessionManager && services.agentRegistrationService && workspaceManager) {
    const workspaceConfigPattern = new vscode.RelativePattern(
      vscode.workspace.workspaceFolders?.[0]?.uri ?? vscode.Uri.file(incrediRoot ?? "."),
      ".vscode/nightgauge-workspace.yaml"
    );
    const workspaceConfigWatcher = vscode.workspace.createFileSystemWatcher(workspaceConfigPattern);

    let reRegisterTimer: ReturnType<typeof setTimeout> | undefined;
    const scheduleReRegister = () => {
      clearTimeout(reRegisterTimer);
      reRegisterTimer = setTimeout(async () => {
        try {
          await workspaceManager.reload();
          await context.globalState.update("nightgauge.agentId", undefined);

          if (services!.sessionManager?.state === "authenticated") {
            const allRepos = workspaceManager.getAllRepositories();
            const workspaceRepos: Array<{ owner: string; repo: string }> = [];
            for (const repo of allRepos) {
              await repo.loadConfig();
              const gh = repo.github;
              if (gh?.owner && gh?.repo) {
                workspaceRepos.push({ owner: gh.owner, repo: gh.repo });
              }
            }
            const agentVersion =
              vscode.extensions.getExtension("nightgauge.nightgauge-vscode")?.packageJSON
                ?.version ?? "0.1.0";
            const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(
              workspaceManager.getWorkspaceConfig()
            );
            if (workspaceMeta) {
              workspaceSyncStatusItem.setStatus("syncing");
              syncSidebarStatus("syncing", 0);
            }
            const agentId = await services!.agentRegistrationService!.register({
              agent_version: agentVersion,
              capabilities: ["headless", "interactive"],
              repos: workspaceRepos,
              machine_id: services!.machineFingerprint?.getMachineId() ?? "",
              vscode_version: vscode.version,
              workspace: workspaceMeta,
            });
            if (agentId) {
              await context.globalState.update("nightgauge.agentId", agentId);
              await context.globalState.update(
                "nightgauge.agentRepos",
                reposSignature(workspaceRepos)
              );
              services!.agentHeartbeatService?.start(agentId);
              // Open the SSE command stream so remotely-triggered pipelines are
              // received and acked, not just left queued (#3544).
              services!.agentCommandStreamService?.start(agentId);
              if (workspaceMeta) {
                workspaceSyncStatusItem.setStatus("synced", workspaceRepos.length);
                syncSidebarStatus("synced", workspaceRepos.length);
              }
            } else if (workspaceMeta) {
              // Surface the REAL re-registration failure so a retry that keeps
              // failing tells the operator WHY, not just "no agentId" (#360).
              const detail =
                services!.agentRegistrationService!.getLastFailureDetail() ??
                "re-registration returned no agentId";
              logger.warn("Workspace re-sync registration failed", { detail });
              workspaceSyncStatusItem.setStatus("failed", 0, detail);
              syncSidebarStatus("failed", 0, detail);
            }
          }
        } catch (err) {
          const errMsg = err instanceof Error ? err.message : String(err);
          logger.warn("Workspace config change re-registration failed", { error: errMsg });
          workspaceSyncStatusItem.setStatus("failed", 0, errMsg);
          syncSidebarStatus("failed", 0, errMsg);
        }
      }, 500);
    };

    workspaceConfigWatcher.onDidChange(scheduleReRegister);
    workspaceConfigWatcher.onDidCreate(scheduleReRegister);
    workspaceConfigWatcher.onDidDelete(scheduleReRegister);
    context.subscriptions.push(workspaceConfigWatcher);

    // Watch per-repo .nightgauge/config.yaml — changes affect the repos array (#3668).
    const repoConfigPattern = new vscode.RelativePattern(
      vscode.workspace.workspaceFolders?.[0]?.uri ?? vscode.Uri.file(incrediRoot ?? "."),
      "**/.nightgauge/config.yaml"
    );
    const repoConfigWatcher = vscode.workspace.createFileSystemWatcher(repoConfigPattern);
    repoConfigWatcher.onDidChange(scheduleReRegister);
    repoConfigWatcher.onDidCreate(scheduleReRegister);
    repoConfigWatcher.onDidDelete(scheduleReRegister);
    context.subscriptions.push(repoConfigWatcher);

    // Allow status item click to trigger immediate re-registration.
    context.subscriptions.push(
      vscode.commands.registerCommand("nightgauge.retryWorkspaceSyncInternal", () => {
        scheduleReRegister();
      })
    );
  }

  logger.info("Nightgauge extension activated");
}

/**
 * Extension deactivation
 *
 * Called when the extension is deactivated.
 */
export function deactivate(): void {
  if (!services) {
    return;
  }

  const {
    logger,
    concurrentPipelineManager,
    headlessOrchestrator,
    agentHeartbeatService,
    agentRegistrationService,
  } = services;

  logger?.info(
    "Deactivating Nightgauge extension — " +
      "persistent logs in .nightgauge/logs/ " +
      "(go-backend.log, ipc-client.log, autonomous-exits.jsonl)"
  );

  // Stop heartbeat before deregister to prevent race with fire-and-forget DELETE
  agentHeartbeatService?.dispose();

  // Fire-and-forget deregister: notify platform the agent is going offline
  const agentId = extensionContext?.globalState.get<string>("nightgauge.agentId");
  if (agentId && agentRegistrationService) {
    void agentRegistrationService.deregister(agentId).catch(() => {});
  }

  // Disconnect project event SSE subscriber (Issue #3025)
  ProjectEventSubscriber.resetInstance();
  setProjectEventSubscriber(null);

  // Disconnect audit/pipeline event SSE consumer (Issue #3321)
  EventStreamService.resetInstance();

  // Stop unified pipeline manager (handles all worktree-based slots, #1831)
  if ((concurrentPipelineManager?.activeSlotCount ?? 0) > 0) {
    concurrentPipelineManager!.abortAll().catch(() => {});
  }

  // Also stop main orchestrator if running (batch processing, resume)
  if (headlessOrchestrator?.getIsRunning()) {
    headlessOrchestrator.stop();
  }

  // Backstop: ensure no Claude CLI child processes survive extension deactivation.
  // stop()/abortAll() already do this, but both are best-effort and can miss
  // handles if a close event was pending. Iterating the registry one more time
  // catches anything that slipped through.
  try {
    killAllActiveProcesses();
  } catch {
    // Best effort — deactivation must not throw
  }

  // Cleanup is handled by disposables in context.subscriptions
}
