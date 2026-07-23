/**
 * Service initialization for the Nightgauge VS Code Extension.
 *
 * Extracted from extension.ts activate() to reduce that file to command
 * registration and auto-resume logic only.
 *
 * @see docs/ARCHITECTURE.md for architectural overview
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import { readFileSync } from "node:fs";
import * as path from "node:path";
import { type PipelineStage, parsePhaseMarker } from "@nightgauge/sdk";
import { Logger } from "../utils/logger";
import { StatusBarManager } from "../utils/statusBar";
import { resolveActiveRepository } from "../utils/resolveActiveRepository";
import {
  getNextStage,
  runStageSkillHeadless,
  getStageLabel,
  type SkillRunCallbacks,
} from "../utils/skillRunner";
import {
  getSettings,
  getWorkspaceRoot,
  getIncrediRoot,
  getWorkItemSourceConfig,
} from "../config/settings";
import type { IWorkItemProvider } from "../services/types/WorkItemProvider";

import { getOutputWindowSettings } from "../config/outputWindowSettings";
import { getPipelineUISettings } from "../config/pipelineUISettings";
import type { ProjectBoardProviders } from "../commands";
import {
  PipelineTreeProvider,
  ContextFileViewer,
  CONTEXT_URI_SCHEME,
  Dashboard,
  ProjectBoardTreeProvider,
  ReadyIssueTreeProvider,
  OutputWindow,
  PipelineSummary,
  SettingsPanel,
  IncrediYamlService,
  RepositoriesTreeProvider,
  QueryResultsTreeProvider,
  BaseTreeItem,
  WorkflowTreeProvider,
  AttentionTreeProvider,
  type AttentionTreeItem,
} from "../views";
import { IssueDragAndDropController } from "../views/IssueDragAndDropController";
import { RepositoriesDragAndDropController } from "../views/RepositoriesDragAndDropController";
import { type TabId } from "../types/TabConfig";
import { Container } from "./Container";
import { GitHubService } from "../services/GitHubService";
import { ProjectBoardService } from "../services/ProjectBoardService";
import { createWorkItemProvider } from "./workItemProviderFactory";
import { ContextWatcherService } from "../services/ContextWatcherService";
import { RefreshTriggerService } from "../services/RefreshTriggerService";
import { PluginSetupService } from "../services/PluginSetupService";
import { CodexSetupService } from "../services/CodexSetupService";
import { NotificationService } from "../services/NotificationService";
import { PipelineStateService, type PipelineState } from "../services/PipelineStateService";
import { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import { IssueQueueService } from "../services/IssueQueueService";
import { CompletedIssuesService } from "../services/CompletedIssuesService";
import { WorkspaceManager } from "../services/WorkspaceManager";
import { RepositoryContextLoader } from "../services/RepositoryContextLoader";
import { ConfigBridge } from "../services/ConfigBridge";
import { RuntimeStateStore } from "../config/RuntimeStateStore";
import {
  createSequentialRepoConfigService,
  type SequentialRepoConfigService,
} from "../utils/sequentialRepoConfig";
import {
  createEnabledReposConfigService,
  type EnabledReposConfigService,
} from "../utils/enabledReposConfig";
import { QueryService } from "../services/QueryService";
import { SavedQueriesService } from "../services/SavedQueriesService";
import { getInitialExecutionMode } from "../utils/incrediConfig";
import { createStreamOutputHandler } from "../utils/streamOutputHandler";
import { createPhaseTracker } from "../utils/phaseTracker";
import { isStreamJsonEnvelope, isEnvelopeFragment } from "../utils/streamJsonFilter";
import { ensureGitignore } from "../utils/ensureGitignore";
import { classifyRuntimeStub } from "../utils/runtimeStubSweep";
import {
  isRepoInitialized,
  refreshRepoInitializedContext,
  registerQuickstartCommands,
  maybeShowGettingStartedOnActivate,
} from "../commands/quickstart";
import { ExecutionHistoryWriter, type IssueMetadataInput } from "../utils/executionHistoryWriter";
import { TelemetryStore } from "../services/TelemetryStore";
import { TelemetryService } from "../services/TelemetryService";
import { TelemetryConsentService } from "../services/TelemetryConsentService";
import { TelemetryUploaderService } from "../services/TelemetryUploaderService";
import { AgentHeartbeatService } from "../services/AgentHeartbeatService";
import { AgentCommandStreamService } from "../services/AgentCommandStreamService";
import type { CommandHandler } from "../services/AgentCommandStreamService";
import { TriggerCommandHandler } from "../services/TriggerCommandHandler";
import { CancelCommandHandler } from "../services/CancelCommandHandler";
import { ApproveCommandHandler } from "../services/ApproveCommandHandler";
import { RejectCommandHandler } from "../services/RejectCommandHandler";
import { AgentRegistrationService } from "../services/AgentRegistrationService";
import { IpcClient } from "../services/IpcClient";
import { SecretStorageService, SECRET_KEYS } from "../services/SecretStorageService";
import { getGlobalConfigPath } from "../utils/globalConfigResolver";
import { migrateLegacyGeminiApiKey } from "../commands/migrateConfig";
import { NotifierStatusTracker } from "../services/notifications/NotifierStatusTracker";
import { OAuthDeviceFlowService } from "../services/OAuthDeviceFlowService";
import { GitHubAuthService } from "../services/GitHubAuthService";
import { AutomationService } from "../services/AutomationService";
import { UsageLimitsService } from "../services/UsageLimitsService";
import { PlatformQuotaService } from "../services/PlatformQuotaService";
import { BrownfieldDataService } from "../services/BrownfieldDataService";
import { BrownfieldDashboard } from "../views/brownfield/BrownfieldDashboard";
import { KnowledgeValueDashboard } from "../views/dashboard/KnowledgeValueDashboard";
import { DiscordService } from "../services/DiscordService";
import { MattermostService } from "../services/notifications/MattermostService";
import { MattermostCommandDispatcher } from "../services/notifications/MattermostCommandDispatcher";
import { NotificationDispatcher } from "../services/notifications/NotificationDispatcher";
import { NotificationRouter } from "../services/notifications/NotificationRouter";
import type { NotifierRoutingRule } from "../config/schema";
import { resolvePlatformBaseUrl, resolvePlatformHostKey } from "../config/schema";
import { registerShowPlatformStatusCommand } from "../commands/showPlatformStatus";
import { registerShowMachineBindingCommand } from "../commands/showMachineBinding";
import { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import { StaleSlotRecoveryService } from "../services/StaleSlotRecoveryService";
import {
  CliPipelineReconciliationService,
  type RegisteredPipelineRoot,
} from "../services/CliPipelineReconciliationService";
import { SlotOutputManager } from "../views/SlotOutputManager";
import { getConcurrentPipelineConfig, getPerformanceMode } from "../utils/incrediConfig";
import { hasCustomStageOverrides } from "../utils/customStageModels";
import { migrateSuperchargeToPerformanceMode } from "../utils/migratePerformanceMode";
import { KnowledgeDocumentLinkProvider } from "../views/KnowledgeDocumentLinkProvider";
import { RemoteCommandStatusBarItem } from "../platform/RemoteCommandStatusBarItem";
import { RemoteCommandStatusService } from "../services/RemoteCommandStatusService";
import { PipelineConnectivityStatusItem } from "../views/PipelineConnectivityStatusItem";
import { getRepoIdentity } from "../utils/configPathResolver";

import {
  ConnectivityStateBus,
  MachineFingerprint,
  OfflineManager,
  PlatformStatusBarItem,
  TrialStateStore,
  TokenStorage,
  TokenRefreshManager,
  type IOnDemandTokenRefresher,
  SessionManager,
  LicensePreflight,
  TierGate,
  SkillContextAssembler,
} from "../platform";
import { PipelineBridge } from "../services/PipelineBridge";
import { PromptTemplateService } from "../services/PromptTemplateService";
import { RepositorySettingsService } from "../services/RepositorySettingsService";
import { BinaryResolver } from "../services/BinaryResolver";

// createWorkItemProvider lives in its own focused module (#3754) so it can be
// imported without the whole service graph. Re-exported here (imported above)
// to preserve the existing import surface for callers of bootstrap/services.
export { createWorkItemProvider };

/**
 * All services and UI components initialized by `initializeServices()`.
 */
export interface ExtensionServices {
  /** DI container — typed registry for GitHub services (Part 1 of series). @see Issue #2771 */
  container: Container;
  logger: Logger;
  statusBar: StatusBarManager;
  headlessOrchestrator: HeadlessOrchestrator | null;
  treeProvider: PipelineTreeProvider;
  projectBoardProviders: Map<TabId, ProjectBoardTreeProvider>;
  projectBoardService: ProjectBoardService;
  contextViewer: ContextFileViewer;
  dashboard: Dashboard;
  outputWindow: OutputWindow;
  pluginSetupService: PluginSetupService;
  codexSetupService: CodexSetupService;
  notificationService: NotificationService;
  pipelineStateService: PipelineStateService | null;
  issueQueueService: IssueQueueService | null;
  completedIssuesService: CompletedIssuesService;
  workspaceManager: WorkspaceManager | null;
  repositoriesTreeProvider: RepositoriesTreeProvider | null;
  /**
   * Phase 3 of #3313 — runtime-tier writer for per-repo concurrency / sequential.
   * Wired to all consumers in bootstrap; legacy direct imports of the YAML
   * helpers were removed in #3336.
   */
  sequentialRepoConfigService: SequentialRepoConfigService | null;
  /**
   * Phase 3 of #3313 — runtime-tier writer for `autonomous.enabled_repos`.
   */
  enabledReposConfigService: EnabledReposConfigService | null;
  /**
   * Phase 2 of #3313 — runtime-tier memento store. Returned so command
   * registrations (e.g. SettingsPanel tier-3 routing) can subscribe to
   * `onDidChange` and write tier-3 keys.
   */
  runtimeStateStore: RuntimeStateStore | null;
  concurrentPipelineManager: ConcurrentPipelineManager | null;
  queryService: QueryService | null;
  savedQueriesService: SavedQueriesService | null;
  queryResultsProvider: QueryResultsTreeProvider | null;
  slotOutputManager: SlotOutputManager | null;
  brownfieldDashboard: BrownfieldDashboard | null;
  knowledgeValueDashboard: KnowledgeValueDashboard | null;
  usageLimitsService: UsageLimitsService | null;
  platformQuotaService: PlatformQuotaService | null;
  discordService: DiscordService | null;
  notifier: NotificationDispatcher | null;
  telemetryStore: TelemetryStore | null;
  offlineManager: OfflineManager | null;
  tokenStorage: TokenStorage | null;
  oauthDeviceFlowService: OAuthDeviceFlowService | null;
  gitHubAuthService: GitHubAuthService | null;
  tokenRefreshManager: TokenRefreshManager | null;
  sessionManager: SessionManager | null;
  machineFingerprint: MachineFingerprint | null;
  telemetryService: TelemetryService | null;
  telemetryConsentService: TelemetryConsentService | null;
  telemetryUploaderService: TelemetryUploaderService | null;
  agentHeartbeatService: AgentHeartbeatService | null;
  agentCommandStreamService: AgentCommandStreamService | null;
  agentRegistrationService: AgentRegistrationService | null;
  tierGate: TierGate | null;
  licensePreflight: LicensePreflight | null;
  skillContextAssembler: SkillContextAssembler | null;
  incrediRoot: string | null;
  projectBoardViews: vscode.TreeView<BaseTreeItem>[];
  treeView: vscode.TreeView<BaseTreeItem>;
  platformStatusBarItem: PlatformStatusBarItem;
  promptTemplateService: PromptTemplateService;
  automationService: AutomationService | null;
  repositorySettingsService: RepositorySettingsService | null;
  /**
   * Live workflow sidebar tree (run → phase → agent → judge) driven off the SDK
   * EventBus node stream. Wired to the EventStreamService in extension.ts once
   * the user authenticates and the SSE stream opens (#3919).
   */
  workflowTreeProvider: WorkflowTreeProvider;
  /**
   * Action Center sidebar tree (ADR 015 / #325) — severity-ordered
   * DecisionRequest cards, live-updated off the local `attention.event` IPC
   * push (no polling, no hosted platform required).
   */
  attentionTreeProvider: AttentionTreeProvider;
  attentionTreeView: vscode.TreeView<AttentionTreeItem>;
}

/**
 * Module-level tracking for extension-initiated stage runs.
 * Used to distinguish between extension-initiated and chat-initiated runs.
 * @see Issue #81 - Pipeline auto-continues even for chat commands
 */
let activeExtensionExecutions: Set<PipelineStage> = new Set();

/**
 * Track which issues have fired the pipeline.complete IPC event from the Go scheduler.
 * Used to skip the duplicate pipeline-finish history write for Go-driven runs.
 * Go scheduler writes the authoritative record (with real token/cost data) via
 * pipeline.complete — the pipeline-finish bookend write must be skipped to avoid
 * zero-cost ghost records in execution history.
 * @see Issue #2545 - Pipeline-finish bookend writes duplicate zero-cost ghost records
 */
const pipelineCompleteIssues = new Set<number>();

/** Debounce timer for concurrent fillSlots — coalesces rapid enqueues */
let debouncedFillSlotsTimer: ReturnType<typeof setTimeout> | null = null;

/**
 * Resolved git root for .nightgauge directory placement.
 * Ensures pipeline files are always at repository root, not subdirectories.
 */
let incrediRoot: string | null = null;

/**
 * Lazy-initialized PipelineSummary panel, created on first pipeline completion.
 */
let pipelineSummary: PipelineSummary;

/**
 * Lazy-initialized SettingsPanel (reserved for future use).
 */
let settingsPanel: SettingsPanel;

/**
 * Resolve the root path used to construct the agent runner (IssueQueueService /
 * ConcurrentPipelineManager / transitively AgentCommandStreamService).
 *
 * Previously these were gated strictly on `incrediRoot` — the git root of
 * `workspaceFolders[0]` — which meant a multi-root `.code-workspace` where the
 * first folder isn't the intended target repo (or isn't resolvable at all)
 * could leave the runner unconstructed even though `WorkspaceManager` already
 * discovered every repo in the workspace. Prefer `incrediRoot` when it
 * resolved (no behavior change for the common single-root case); otherwise
 * fall back to the first repository `WorkspaceManager` discovered, so the
 * runner still constructs whenever there is at least one known repo.
 *
 * This value only backs the manager's single-root DEFAULT WorktreeManager
 * (used for same-repo / non-cross-repo items). Per-command dispatch does not
 * depend on it — it resolves the correct target repo independently via
 * `ConcurrentPipelineManager.resolveWorktreeManager` /
 * `WorkspaceManager.findRepositoryByGitHub`.
 *
 * Returns null when there is truly nothing to run against (no git root and
 * no discovered repositories) — callers should skip runner construction
 * entirely in that case, matching prior behavior for the no-workspace case.
 *
 * @see Issue #4117 — agent runner gated on a single incrediRoot
 */
export function resolveAgentRunnerRoot(
  incrediRootValue: string | null,
  workspaceManagerValue: WorkspaceManager | null
): string | null {
  return incrediRootValue ?? workspaceManagerValue?.getAllRepositories()[0]?.path ?? null;
}

/**
 * Read GitHub labels for an issue from its issue context JSON file.
 * Returns undefined (graceful fallback) when the file is absent or malformed.
 */
export function readIssueLabels(issueNumber: number): string[] | undefined {
  if (!incrediRoot) {
    return undefined;
  }
  try {
    const filePath = path.join(incrediRoot, ".nightgauge", "pipeline", `issue-${issueNumber}.json`);
    const raw = readFileSync(filePath, "utf8");
    const parsed = JSON.parse(raw) as { labels?: unknown };
    if (Array.isArray(parsed.labels)) {
      return parsed.labels.filter((l): l is string => typeof l === "string");
    }
  } catch {
    // File absent or unreadable — not an error condition
  }
  return undefined;
}

/**
 * Extract size, type, and priority from a labels array.
 * Used by both the Go pipeline.complete handler and the pipeline-finish bookend
 * to populate metadata fields in JSONL history records. Mirrors the extraction
 * logic in HeadlessOrchestrator (extractSizeLabel/extractTypeLabel/extractPriorityLabel).
 */
function extractMetadata(labels: string[] | undefined): {
  size: string | null;
  type: string | null;
  priority: string | null;
} {
  if (!labels) {
    return { size: null, type: null, priority: null };
  }

  let size: string | null = null;
  let type: string | null = null;
  let priority: string | null = null;

  for (const label of labels) {
    if (!size && label.startsWith("size:")) {
      const value = label.slice("size:".length).toUpperCase();
      if (["XS", "S", "M", "L", "XL"].includes(value)) {
        size = value;
      }
    }
    if (!type && label.startsWith("type:")) {
      const value = label.slice("type:".length).toLowerCase();
      if (["feature", "bug", "docs", "refactor", "chore", "test", "verification"].includes(value)) {
        type = value;
      }
    }
    if (!priority && label.startsWith("priority:")) {
      const value = label.slice("priority:".length).toLowerCase();
      if (["critical", "high", "medium", "low"].includes(value)) {
        priority = value;
      }
    }
  }

  return { size, type, priority };
}

/**
 * Initialize ALL extension services, tree views, event wiring, and subscriptions.
 *
 * This is a pure extraction from extension.ts activate() — no logic changes.
 */
export async function initializeServices(
  context: vscode.ExtensionContext
): Promise<ExtensionServices> {
  // ── 1. SecretStorage & Logger ─────────────────────────────────────────

  // Initialize SecretStorageService for secure API key management (Issue #1056)
  SecretStorageService.initialize(context.secrets);

  // Initialize NotifierStatusTracker for live notifier send-outcome tracking (#3379)
  NotifierStatusTracker.initialize();

  // Initialize TokenStorage for typed platform token persistence (Issue #1465)
  // getHostKey resolves at call time so config is always current (#3722)
  const secretServiceForTokenStorage = SecretStorageService.getInstance()!;
  TokenStorage.initialize(secretServiceForTokenStorage, () =>
    resolvePlatformHostKey(ConfigBridge.getInstance().getPlatform())
  );

  // One-time migration: move legacy unscoped tokens to production-scoped keys (#3722)
  TokenStorage.getInstance()
    ?.migrateFromLegacy()
    .catch((err) => logger.warn("Legacy token migration failed", { error: err }));

  // Pre-load Gemini API key from SecretStorage into process.env for child processes
  const secretService = SecretStorageService.getInstance();
  if (secretService) {
    // The historical contribution was an ordinary plaintext settings.json
    // value despite claiming SecretStorage. Migrate it once, then erase every
    // configuration scope before loading the key for child processes.
    await migrateLegacyGeminiApiKey(secretService);
    secretService.getApiKey("gemini").then((key) => {
      if (key && !process.env.GEMINI_API_KEY) {
        process.env.GEMINI_API_KEY = key;
      }
    });
  }

  // ── License key — startup resolution (#3519, #3997) ────────────────────
  // The license key is a machine-tier key: it lives in
  // ~/.nightgauge/config.yaml and is mirrored to SecretStorage so the
  // SecretStorage-first runtime readers (LicensePreflight, forwardPlatformEnv)
  // see it. On startup we:
  //   1. Strip any license key still embedded in the PROJECT config.yaml — it
  //      must never sit in a committed file — seeding SecretStorage from it.
  //   2. Otherwise seed SecretStorage from the MACHINE config.yaml when
  //      SecretStorage is empty (fresh machine / new install).
  // Cache the resolved key for sync consumers (LicensePreflight, TelemetryUploader).
  let cachedLicenseKey: string | undefined;

  /** Extract platform.license_key from a YAML file via a line scan (no parser). */
  const extractLicenseKeyLine = (raw: string): { key?: string; lineIndex: number } => {
    const lines = raw.split("\n");
    let inPlatform = false;
    for (let i = 0; i < lines.length; i++) {
      const trimmed = lines[i].trim();
      if (trimmed === "platform:") {
        inPlatform = true;
        continue;
      }
      if (
        inPlatform &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !lines[i].startsWith(" ") &&
        !lines[i].startsWith("\t")
      ) {
        inPlatform = false;
        continue;
      }
      if (inPlatform) {
        const m = trimmed.match(/^license_key:\s*(.+)$/);
        if (m) {
          return { key: m[1].replace(/^['"]|['"]$/g, "").trim(), lineIndex: i };
        }
      }
    }
    return { lineIndex: -1 };
  };

  const primaryWorkspaceForMigration = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (secretService) {
    void (async () => {
      const fsLib = await import("fs");
      const pathLib = await import("path");

      // 1. Strip + migrate any key embedded in the project config (committed).
      if (primaryWorkspaceForMigration) {
        const cfgPath = pathLib.join(primaryWorkspaceForMigration, ".nightgauge", "config.yaml");
        if (fsLib.existsSync(cfgPath)) {
          const raw = fsLib.readFileSync(cfgPath, "utf-8");
          const { key: foundKey, lineIndex } = extractLicenseKeyLine(raw);
          if (foundKey) {
            await secretService.setSecret(SECRET_KEYS.platformLicenseKey, foundKey);
            cachedLicenseKey = foundKey;
            const lines = raw.split("\n");
            lines.splice(lineIndex, 1);
            fsLib.writeFileSync(cfgPath, lines.join("\n"), "utf-8");
            return;
          }
        }
      }

      // 2. Seed SecretStorage from the machine config when it has no value yet.
      cachedLicenseKey = await secretService.getSecret(SECRET_KEYS.platformLicenseKey);
      if (!cachedLicenseKey) {
        const machineCfgPath = getGlobalConfigPath();
        if (fsLib.existsSync(machineCfgPath)) {
          const machineRaw = fsLib.readFileSync(machineCfgPath, "utf-8");
          const { key: machineKey, lineIndex } = extractLicenseKeyLine(machineRaw);
          if (machineKey) {
            await secretService.setSecret(SECRET_KEYS.platformLicenseKey, machineKey);
            cachedLicenseKey = machineKey;
            const lines = machineRaw.split("\n");
            lines.splice(lineIndex, 1);
            fsLib.writeFileSync(machineCfgPath, lines.join("\n"), "utf-8");
          }
        }
      }
    })();
  }

  // Keep cache in sync when user updates the license key through the Settings panel.
  context.subscriptions.push(
    context.secrets.onDidChange((e) => {
      if (e.key === SECRET_KEYS.platformLicenseKey) {
        void secretService?.getSecret(SECRET_KEYS.platformLicenseKey).then((k) => {
          cachedLicenseKey = k;
        });
      }
    })
  );

  // Initialize logger
  const logger = new Logger("Nightgauge");
  logger.info("Activating Nightgauge extension");

  // Initialize status bar
  const statusBar = new StatusBarManager();

  // Migrate the legacy `.nightgauge/supercharge.yaml` to the new
  // `.nightgauge/performance-mode.yaml` on first activation (Issue #3009).
  // Idempotent: short-circuits when the new file already exists. Surfaces a
  // one-time toast when migration actually runs so users see the mapping
  // (active=true → Maximum, active=false → Elevated).
  try {
    const primaryWorkspace = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (primaryWorkspace) {
      const migration = migrateSuperchargeToPerformanceMode(primaryWorkspace);
      if (migration.migrated && migration.mode) {
        const modeLabel = migration.mode.charAt(0).toUpperCase() + migration.mode.slice(1);
        vscode.window.showInformationMessage(
          `Supercharge replaced by performance modes — your previous setting was migrated to ${modeLabel}.`
        );
        logger.info("Migrated legacy supercharge.yaml to performance-mode.yaml", {
          mode: migration.mode,
          legacyBackupPath: migration.legacyBackupPath,
        });
      } else if (migration.error) {
        logger.warn("Performance-mode migration encountered a non-fatal error", {
          error: migration.error,
        });
      }
    }
  } catch {
    // Non-critical — read path falls back to DEFAULT_PERFORMANCE_MODE.
  }

  // Hydrate the performance-mode selector from persisted state so the status
  // bar reflects the active mode on activation.
  try {
    statusBar.setPerformanceMode(getPerformanceMode());
  } catch {
    // Non-critical — defaults to Elevated render if state cannot be read.
  }

  // Hydrate the custom per-stage-model badge from config (Issue #20) so the
  // status bar shows "Custom" on activation when pins are active.
  try {
    const incrediRootForCustom = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (incrediRootForCustom) {
      statusBar.setCustomOverridesActive(hasCustomStageOverrides(incrediRootForCustom));
    }
  } catch {
    // Non-critical — badge stays off if config cannot be read.
  }

  // Warm the Codex model catalog once at activation so the first Maximum-mode
  // pipeline run doesn't have to parse the cache synchronously. The service
  // itself is file-backed — no network — so this is cheap. We log the top
  // tier so users can verify dynamic discovery picked the right model.
  try {
    const { CodexModelCatalogService } = await import("../services/CodexModelCatalogService");
    const catalog = new CodexModelCatalogService().listModels();
    if (catalog.length > 0) {
      logger.info("Codex model catalog warmed", {
        top: catalog[0],
        count: catalog.length,
      });
    }
  } catch {
    // Non-critical — Maximum mode falls back to the static default model.
  }

  // ── 2. Pipeline context ───────────────────────────────────────────────

  // Set initial pipeline running state for button visibility
  vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);

  // Resolve nightgauge root (git root) for consistent .nightgauge directory placement
  // This ensures pipeline files are always at repository root, even when
  // VSCode workspace is opened in a subdirectory.
  const workspaceRootForNightgauge = getWorkspaceRoot();
  if (workspaceRootForNightgauge) {
    incrediRoot = await getIncrediRoot(workspaceRootForNightgauge);
    logger.info("Resolved nightgauge root", {
      workspaceRoot: workspaceRootForNightgauge,
      incrediRoot,
    });

    // Only scaffold .nightgauge/.gitignore and its subdirectories after
    // the repo has been initialized (i.e. .nightgauge/config.yaml exists).
    // Previously this ran on every activation and caused an .nightgauge/
    // folder to appear in every workspace the extension touched — even ones
    // the user never intended to onboard. Now the user opts in by running
    // /nightgauge:repo-init from the welcome view.
    const alreadyInitialized = await isRepoInitialized(incrediRoot);
    await vscode.commands.executeCommand(
      "setContext",
      "nightgauge.repoInitialized",
      alreadyInitialized
    );
    if (alreadyInitialized) {
      ensureGitignore(incrediRoot)
        .then((result) => {
          if (result.created) {
            logger.info("Created .nightgauge/.gitignore");
          } else if (result.updated) {
            logger.info("Updated .nightgauge/.gitignore to latest version");
          }
        })
        .catch((error) => {
          logger.warn("Failed to ensure .nightgauge/.gitignore", { error });
        });
    } else {
      logger.info(
        "Skipping .nightgauge scaffold — repo not initialized (run /nightgauge:repo-init)"
      );
    }

    // First-run onboarding (Issue #4155): auto-open the Getting Started panel
    // exactly once per install, the first time this workspace hasn't been
    // initialized yet. Reuses the `alreadyInitialized` signal computed above
    // instead of re-deriving it — see maybeShowGettingStartedOnActivate and
    // onboardingGate.ts for the activation-condition logic.
    void maybeShowGettingStartedOnActivate(context, alreadyInitialized, logger);

    // Watch for config.yaml appearing/disappearing so the welcome view flips
    // automatically when the user runs repo-init in a terminal.
    try {
      const configGlob = new vscode.RelativePattern(
        workspaceRootForNightgauge,
        ".nightgauge/config.yaml"
      );
      const watcher = vscode.workspace.createFileSystemWatcher(configGlob);
      const onChange = () => refreshRepoInitializedContext(incrediRoot, logger);
      watcher.onDidCreate(onChange);
      watcher.onDidDelete(onChange);
      context.subscriptions.push(watcher);
    } catch (error) {
      logger.warn("Failed to create config.yaml watcher", { error });
    }
  }

  // Register quickstart onboarding commands (usable even before init).
  registerQuickstartCommands(context, incrediRoot, logger);

  // ── 3. Setup services ────────────────────────────────────────────────

  // Initialize plugin setup service and check for Claude Code plugins
  const pluginSetupService = new PluginSetupService(context);
  pluginSetupService.checkAndPromptSetup().catch((error) => {
    logger.warn("Plugin setup check failed", { error });
  });
  context.subscriptions.push(pluginSetupService);

  // Initialize Codex setup service and check for Codex command installation
  const codexSetupService = new CodexSetupService(context);
  codexSetupService.checkAndPromptSetup().catch((error) => {
    logger.warn("Codex setup check failed", { error });
  });
  context.subscriptions.push(codexSetupService);

  // ── 4. Notification service ───────────────────────────────────────────

  // Initialize notification service for sounds and alerts
  const notificationService = new NotificationService();

  // ── 5. Core services ─────────────────────────────────────────────────

  let pipelineStateService: PipelineStateService | null = null;
  let workspaceManager: WorkspaceManager | null = null;
  let workspaceInitPromise: Promise<void> | undefined;
  let repositoryContextLoader: RepositoryContextLoader | null = null;
  let configBridge: ConfigBridge = ConfigBridge.getInstance();
  let discordService: DiscordService | null = null;
  let notifier: NotificationDispatcher | null = null;
  let offlineManager: OfflineManager | null = null;
  // RepositorySwitcher removed — multi-repo is now native to the pipeline
  let repositoriesTreeProvider: RepositoriesTreeProvider | null = null;
  let repositoriesDnDController: RepositoriesDragAndDropController | null = null;
  let runtimeStateStoreInstance: RuntimeStateStore | null = null;
  let sequentialRepoConfigService: SequentialRepoConfigService | null = null;
  let enabledReposConfigService: EnabledReposConfigService | null = null;
  let skillContextAssemblerService: SkillContextAssembler | null = null;
  let automationService: AutomationService | null = null;

  // Shared getter — resolves the current platform base URL on every call so
  // all services pick up config changes without an extension restart (#3719).
  const getPlatformUrl = () => resolvePlatformBaseUrl(ConfigBridge.getInstance().getPlatform());

  // Initialize PipelineStateService for unified state management (Issue #154)
  // Use incrediRoot (git root) instead of workspace root for correct file placement
  if (incrediRoot) {
    pipelineStateService = PipelineStateService.getInstance(incrediRoot);

    // Check for crash recovery on startup
    pipelineStateService.recoverFromCrash().catch((error) => {
      logger.warn("Pipeline crash recovery failed", { error });
    });

    // Reconcile local state with GitHub (clears stale state for closed issues)
    pipelineStateService.reconcileWithGitHub().catch((error) => {
      logger.warn("Pipeline GitHub reconciliation failed", { error });
    });

    // Restore paused context on extension activation (Issue #535)
    // This ensures the UI shows the correct play/pause button after
    // VS Code restart or extension reload.
    pipelineStateService
      .getState()
      .then((state) => {
        if (state) {
          // Restore paused context flag
          vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", state.paused);
          // If pipeline exists but is paused, set running to false
          // so the play button appears instead of pause
          if (state.paused) {
            vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);
            logger.info("Pipeline paused state restored from disk", {
              issueNumber: state.issue_number,
              paused: state.paused,
            });
          }
        }
      })
      .catch((error) => {
        logger.warn("Failed to restore paused context", { error });
      });

    // Clean up orphaned context files from previous pipeline runs
    // Only cleans files older than 24 hours to avoid disrupting active work
    pipelineStateService.cleanOrphanedFiles(24).then((result) => {
      if (result.cleaned.length > 0) {
        logger.info("Cleaned orphaned pipeline files", {
          cleaned: result.cleaned,
        });
      }
      if (result.errors.length > 0) {
        logger.warn("Errors during orphan cleanup", { errors: result.errors });
      }
    });

    // Clean up old execution history JSONL files (Issue #649)
    // Retention defaults to 90 days; configurable via pipeline.logs.history_retention_days
    ExecutionHistoryWriter.cleanupOldFiles(incrediRoot, 90)
      .then((result) => {
        if (result.deleted.length > 0) {
          logger.info("Cleaned old execution history files", {
            deleted: result.deleted,
          });
        }
      })
      .catch((err) => {
        logger.warn("Execution history cleanup failed", { err });
      });

    context.subscriptions.push(pipelineStateService);

    // Initialize AutomationService for workflow automation triggers (Issue #137).
    // Skip on uninitialized repos — initialize() calls fs.mkdir on
    // .nightgauge/logs which would resurrect the folder we just
    // intentionally skipped in ensureGitignore.
    if (await isRepoInitialized(incrediRoot)) {
      automationService = new AutomationService(pipelineStateService, incrediRoot);
      automationService.initialize().catch((error) => {
        logger.warn("AutomationService initialization failed", { error });
      });
      context.subscriptions.push(automationService);
    }

    // Initialize WorkspaceManager for multi-repository support (Issue #324)
    // Uses incrediRoot as workspace root and workspaceState for session persistence
    workspaceManager = WorkspaceManager.getInstance(incrediRoot, context.workspaceState);
    // initialize() is intentionally NOT awaited here so the rest of the
    // synchronous service setup can proceed immediately. The onWorkspaceChanged
    // handler (registered in section 10) will fire once initialize() completes.
    // However, because there are `await` statements between here and section 10,
    // initialize() may resolve and fire its event *before* the handler is
    // registered. To guard against this race, section 10 also performs a manual
    // startup sync after handler registration. See "Startup sync" comment below.
    workspaceInitPromise = workspaceManager.initialize().catch((error) => {
      logger.warn("WorkspaceManager initialization failed", { error });
    });
    context.subscriptions.push(workspaceManager);
    logger.info("WorkspaceManager initialized", {
      mode: workspaceManager.detectWorkspaceMode(),
    });

    // Initialize RepositoryContextLoader for repository-scoped paths (Issue #327)
    // Must be initialized after WorkspaceManager
    repositoryContextLoader = RepositoryContextLoader.getInstance();
    repositoryContextLoader.initialize(workspaceManager).catch((error) => {
      logger.warn("RepositoryContextLoader initialization failed", { error });
    });
    context.subscriptions.push(repositoryContextLoader);
    logger.info("RepositoryContextLoader initialized");

    // Connect PipelineStateService to RepositoryContextLoader for repository-scoped paths
    pipelineStateService.setContextLoader(repositoryContextLoader);

    // Initialize SkillContextAssembler for workspace language/framework detection (Issue #1475)
    // Must be initialized after WorkspaceManager — uses onWorkspaceChanged for cache invalidation
    skillContextAssemblerService = SkillContextAssembler.initialize(workspaceManager);
    context.subscriptions.push(skillContextAssemblerService);
    logger.info("SkillContextAssembler initialized");

    // Initialize RuntimeStateStore — memento-backed runtime config tier (Issue #3335)
    // Phase 2 of epic #3313. The store is wired through ConfigBridge so the
    // 7-tier merge engine sees runtime values. Phase 3 (#3336) migrated the
    // four UI writers (sequential / max_concurrent / enabled_repos /
    // pipeline.max_concurrent) to write through this store.
    runtimeStateStoreInstance = new RuntimeStateStore(context.globalState, context.workspaceState);
    context.subscriptions.push(runtimeStateStoreInstance);
    logger.info("RuntimeStateStore initialized");

    // Initialize ConfigBridge for unified 7-tier config access (Issue #473, #3335)
    // Must be initialized after WorkspaceManager - provides typed config getters
    // and fires onConfigChanged events when config files change
    configBridge = ConfigBridge.getInstance();
    try {
      await configBridge.initialize(workspaceManager, incrediRoot, runtimeStateStoreInstance);
    } catch (error) {
      logger.warn("ConfigBridge initialization failed", { error });
    }
    context.subscriptions.push(configBridge);
    logger.info("ConfigBridge initialized");

    // #3641 — autonomous policy services. After Phase 3 (#3336) routed
    // writes through RuntimeStateStore (workspaceState memento), #3641
    // reclassified these keys to Machine tier because workspaceState is
    // per-workspace-folder-URI and therefore breaks under git worktrees.
    // Writes now target ~/.nightgauge/config.yaml via
    // IncrediYamlService.writeGlobal(); reads still come through the
    // 7-tier merge engine via ConfigBridge. The RuntimeStateStore is
    // retained for best-effort cleanup of legacy memento entries.
    const machineYamlWriter = new IncrediYamlService(incrediRoot ?? "");
    context.subscriptions.push(machineYamlWriter);
    sequentialRepoConfigService = createSequentialRepoConfigService(
      runtimeStateStoreInstance,
      configBridge,
      machineYamlWriter
    );
    enabledReposConfigService = createEnabledReposConfigService(
      runtimeStateStoreInstance,
      configBridge,
      machineYamlWriter
    );

    // Issue #3650 (Part C) — defensive cleanup of stale `autonomous.enabled_repos`
    // runtime mementos in THIS workspace, run on every activation.
    //
    // Why this is needed: `runLegacyKeysMigration` (Issue #3338) is gated by a
    // *globalState* STATE_KEY, so it runs at most once per VSCode install —
    // not once per workspace. In a multi-worktree setup (e.g. the user has
    // `nightgauge`, `acme-platform`, `acme-mobile`,
    // `acme-dashboard` all open across separate windows), the
    // migration's `promoteV1MementoToMachine` step clears the v1 memento ONLY
    // for the workspace it ran in. Every other workspace keeps its stale
    // workspaceState memento at `nightgauge.runtime.autonomous.enabled_repos`,
    // which the merge engine's runtime tier (priority above the machine YAML
    // we now write) overlays back on top of the machine value every reload.
    //
    // Symptom: clicking a single repo's checkbox in the autonomous tree view
    // appears to succeed (machine YAML is rewritten correctly), but the
    // immediate post-write reload re-reads the stale workspaceState overlay
    // and the row visually flips back to its pre-click state. This was the
    // user-reported "Flutter checkbox un-checks itself" bug — Flutter is
    // omitted from the stale runtime list in workspaces that never ran the
    // migration, so the bounce only affects the repo(s) missing from that
    // legacy list.
    //
    // We deliberately clear BOTH scopes (workspace + global) every activation
    // rather than gating on another STATE_KEY: the operation is idempotent
    // (memento.update(key, undefined) is a no-op when absent), and the
    // additional safety against future legacy-key regressions outweighs the
    // tiny startup cost (two memento writes). The machine YAML is the
    // authoritative source post-#3641 — there is no legitimate reason for a
    // runtime overlay to exist at this path anymore.
    void (async () => {
      try {
        await runtimeStateStoreInstance.delete("autonomous.enabled_repos", {
          scope: "workspace",
        });
        await runtimeStateStoreInstance.delete("autonomous.enabled_repos", {
          scope: "global",
        });
        // Refresh ConfigBridge so the cleared snapshot is reflected in the
        // first effective read after activation — without this, the initial
        // tree render could still see the stale overlay until the next file
        // event triggers a reload.
        await configBridge.reload();
      } catch (err) {
        // Best-effort — defensive cleanup that fails should not block
        // activation. The bounce will recur for this user on the next click
        // but the rest of the extension remains functional.
        logger.warn("autonomous.enabled_repos runtime cleanup failed (non-fatal)", {
          error: err instanceof Error ? err.message : String(err),
        });
      }
    })();

    // Initialize OfflineManager for platform health monitoring (Issue #1459)
    const platformConfig = configBridge.getEffectiveConfig()?.config?.platform;
    const healthCheckConfig = platformConfig?.health_check;
    // IPC-backed health checker adapter (#2090) — delegates to Go binary
    const ipcHealthChecker = {
      async checkHealth(): Promise<{ reachable: boolean; degraded?: boolean }> {
        try {
          const resp = await IpcClient.getInstance().platformHealthCheck();
          const degraded = resp.status !== "ok";
          return { reachable: true, degraded };
        } catch {
          return { reachable: false };
        }
      },
    };

    offlineManager = new OfflineManager(
      {
        getBaseUrl: getPlatformUrl,
        intervalMs: healthCheckConfig?.interval_ms,
        timeoutMs: healthCheckConfig?.timeout_ms,
        failureThreshold: healthCheckConfig?.failure_threshold,
      },
      ipcHealthChecker,
      logger
    );
    offlineManager.start();
    offlineManager.register("skillResolve", () => "community");
    context.subscriptions.push(offlineManager);

    // Forward OfflineManager state to the process-wide ConnectivityStateBus so
    // skillRunner.ts can gate stall-kill on offline state without taking a
    // direct OfflineManager reference (Issue #3203).
    ConnectivityStateBus.set(offlineManager.state);
    context.subscriptions.push(
      offlineManager.onStateChanged((evt) => {
        ConnectivityStateBus.set(evt.current);
      })
    );

    logger.info("OfflineManager initialized", { baseUrl: getPlatformUrl() });

    // Initialize Discord + Mattermost notifiers and wrap them in a
    // NotificationDispatcher so future Slack / Teams notifiers can be added
    // without re-wiring bootstrap. The dispatcher owns lifecycle disposal —
    // adding it to context.subscriptions is enough to dispose every wrapped
    // notifier. Issue #3372 introduced the dispatcher; Issue #3373 added
    // Mattermost; Issue #3374 adds per-channel routing rules.
    discordService = new DiscordService(pipelineStateService, configBridge, logger);
    const mattermostService = new MattermostService(pipelineStateService, configBridge, logger);

    // Issue #3605 bullet C: wire the autonomous safety-pause notifier to
    // DiscordService.notifySafetyPause so cascading-failure / rate-limit
    // pauses surface to Discord in addition to the VSCode toast.
    // setAutonomousSafetyNotifier is module-scoped in autonomousCommands so
    // the wiring lives wherever the lifecycle of the DiscordService lives.
    // The dynamic import keeps bootstrap free of a top-level dependency on
    // the commands module (cycle avoidance).
    void import("../commands/autonomousCommands").then((mod) => {
      mod.setAutonomousSafetyNotifier((triggeredBy, reason) =>
        discordService!.notifySafetyPause(triggeredBy, reason)
      );
    });

    // Build routing rules from config and construct a NotificationRouter.
    // Pair-registry: ids must match notifiers[] entries in config (ADR-001).
    const rawNotifiers = configBridge.getEffectiveConfig()?.config?.notifiers ?? [];
    let validatedRules: NotifierRoutingRule[] = [];

    // Surface schema validation errors for the notifiers block as a VSCode warning.
    // Only use validated rules — invalid configs fall back to no-routing (all events delivered).
    if (rawNotifiers.length > 0) {
      const { NotifiersConfigSchema } = await import("../config/schema");
      const parseResult = NotifiersConfigSchema.safeParse(rawNotifiers);
      if (parseResult.success) {
        validatedRules = parseResult.data;
      } else {
        const firstError = parseResult.error.issues[0];
        const message = firstError ? firstError.message : "Invalid notifiers config";
        const choice = await vscode.window.showWarningMessage(
          `Nightgauge: Invalid notifiers config — ${message}`,
          "Open Config"
        );
        if (choice === "Open Config") {
          const workspaceRoot = getWorkspaceRoot() ?? "";
          const configUri = vscode.Uri.file(path.join(workspaceRoot, ".nightgauge", "config.yaml"));
          await vscode.window.showTextDocument(configUri);
        }
      }
    }

    const router = new NotificationRouter(validatedRules);
    notifier = new NotificationDispatcher(
      [
        { id: "discord", notifier: discordService },
        { id: "mattermost", notifier: mattermostService },
      ],
      logger,
      router
    );
    notifier.initialize().catch((error) => {
      logger.warn("Notifier initialization failed", { error });
    });
    context.subscriptions.push(notifier);

    // Wire the Mattermost slash-command dispatcher (#3376 + #3377).
    // The authorize closure calls notifications.checkAuthorization on the Go binary,
    // which performs user-mapping lookup, permission cache check, and audit logging.
    const ipcInstance = IpcClient.getInstance();
    const mattermostCommandDispatcher = new MattermostCommandDispatcher(
      ipcInstance,
      logger,
      async (event) => {
        const cmd = event.parsed_command;
        // Resolve repo slug: use explicit --repo flag from command, then fall
        // back to project.default_repo from config (for pause/resume/stop).
        let repoSlug = cmd.repo ?? "";
        if (!repoSlug) {
          try {
            const projCfg = await ipcInstance.configGetProjectConfig();
            if (projCfg.defaultRepo && projCfg.owner) {
              repoSlug = `${projCfg.owner}/${projCfg.defaultRepo}`;
            }
          } catch {
            // Config unavailable — let Go side handle empty repo slug (deny).
          }
        }
        const result = await ipcInstance.notificationsCheckAuthorization(
          event.user_id ?? "",
          cmd.type,
          repoSlug,
          event.channel_id ?? ""
        );
        return {
          allowed: result.allowed,
          reason: result.reason,
          mappedIdentity: result.mappedIdentity,
        };
      }
    );
    context.subscriptions.push(mattermostCommandDispatcher);

    // Initialize RepositoriesTreeProvider for repositories view (Issue #329)
    // Must be initialized after WorkspaceManager. Pass factory so the view
    // never calls new ProjectBoardService() directly (IWorkItemProvider boundary).
    // Pass the runtime-tier services (Phase 3 of #3313 / #3336) so the view
    // reads/writes per-repo concurrency and enabled_repos through the runtime
    // tier rather than touching .nightgauge/config.yaml.
    repositoriesTreeProvider = new RepositoriesTreeProvider(
      workspaceManager,
      (path) => new ProjectBoardService(path),
      {
        sequentialRepoConfigService,
        enabledReposConfigService,
      }
    );
    context.subscriptions.push(repositoriesTreeProvider);

    // Create drag-and-drop controller for the Repositories tree view (Issue #2189)
    repositoriesDnDController = new RepositoriesDragAndDropController(repositoriesTreeProvider);
    repositoriesDnDController.setLogger(logger);
    // Wire provider refresh after a cross-status drop
    repositoriesDnDController.setRepoRefreshCallback((repoName) => {
      repositoriesTreeProvider!.refreshRepository(repoName);
    });
    context.subscriptions.push(repositoriesDnDController);

    // Create tree view for repositories (with DnD support).
    // `manageCheckboxStateManually: true` lets the provider compute checkbox
    // state from config.yaml (autonomous.enabled_repos) rather than having
    // VS Code persist independent checkbox state.
    const repositoriesTreeView = vscode.window.createTreeView("nightgauge.repositoriesView", {
      treeDataProvider: repositoriesTreeProvider,
      showCollapseAll: true,
      dragAndDropController: repositoriesDnDController,
      manageCheckboxStateManually: true,
    });
    context.subscriptions.push(repositoriesTreeView);

    // Forward checkbox toggles to the provider so the autonomous enabled_repos
    // allowlist stays in sync with what the user checks/unchecks inline.
    context.subscriptions.push(
      repositoriesTreeView.onDidChangeCheckboxState((event) => {
        void repositoriesTreeProvider!.handleCheckboxChange(event);
      })
    );

    // Connect TreeView to provider for title updates
    repositoriesTreeProvider.setTreeView(repositoriesTreeView);

    // Set multi-repo mode context for view visibility
    vscode.commands.executeCommand(
      "setContext",
      "nightgauge.multiRepoMode",
      workspaceManager.isMultiWorkspace()
    );

    // Prime the view-title auto-refresh toggle context so the correct icon
    // shows on initial render (pause vs resume).
    vscode.commands.executeCommand(
      "setContext",
      "nightgauge.repositoriesAutoRefresh",
      repositoriesTreeProvider.isAutoRefreshEnabled()
    );

    logger.info("RepositoriesTreeProvider initialized", {
      repoCount: workspaceManager.getRepositoryCount(),
    });
  }

  // ── 6. HeadlessOrchestrator ───────────────────────────────────────────

  // Create HeadlessOrchestrator for CLI-based pipeline execution and batch operations
  const headlessOrchestrator = new HeadlessOrchestrator(pipelineStateService ?? null, logger);
  context.subscriptions.push(headlessOrchestrator);

  // Connect HeadlessOrchestrator to RepositoryContextLoader if available
  if (repositoryContextLoader) {
    headlessOrchestrator.setContextLoader(repositoryContextLoader);
  }

  // Restore paused pipeline state from runtime-*.json files (Issue #2008)
  // The existing getState() call above returns null on startup since Go hasn't
  // emitted pipeline.stateChanged yet. Scanning runtime files directly gives us
  // the persisted pause flag without requiring the Go binary to be running.
  //
  // #307 stale-stub sweep: BEFORE trusting a runtime file, classify it. A stub
  // with empty repo/stage (the never-cleaned "initialized" snapshot the Go IPC
  // server used to strand in the launch repo), or one whose `repo` field does
  // not match the repo that contains it, is cross-contamination from a
  // concurrent multi-repo run — ignore it AND delete it so it can never be
  // resurrected as a zombie run in a repo that never ran the issue.
  if (incrediRoot) {
    const pipelineDir = path.join(incrediRoot, ".nightgauge", "pipeline");
    // Best-effort: the "owner/repo" (or short name) of the repo that owns this
    // pipeline dir, for the repo-mismatch check. Undefined → mismatch check is
    // skipped (empty-identity check still applies).
    const containingRepo = workspaceManager
      ?.getAllRepositories()
      .find((r) => r.path === incrediRoot);
    const gh = containingRepo?.github;
    const containingRepoSlug = gh ? `${gh.owner}/${gh.repo}` : containingRepo?.name;
    (async () => {
      try {
        const files = await fs.readdir(pipelineDir).catch(() => [] as string[]);
        const runtimeFiles = files.filter((f) => /^runtime-\d+\.json$/.test(f));
        for (const file of runtimeFiles) {
          const filePath = path.join(pipelineDir, file);
          try {
            const content = await fs.readFile(filePath, "utf-8");
            const runtime = JSON.parse(content) as {
              paused?: boolean;
              issueNumber?: number;
              repo?: string | null;
              stage?: string | null;
            };
            const verdict = classifyRuntimeStub(runtime, containingRepoSlug);
            if (verdict.action === "delete") {
              logger.warn("Sweeping stale/cross-contaminated runtime stub (#307)", {
                file,
                reason: verdict.reason,
                repo: runtime.repo ?? null,
                stage: runtime.stage ?? null,
                issueNumber: runtime.issueNumber,
                containingRepoSlug,
              });
              await fs.unlink(filePath).catch(() => {});
              continue;
            }
            if (runtime.paused) {
              logger.info("Paused pipeline detected on activation", {
                issueNumber: runtime.issueNumber,
                file,
              });
              vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", true);
              vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);
              const action = await vscode.window.showInformationMessage(
                `Pipeline for #${runtime.issueNumber} is paused. Resume from where you left off?`,
                "Resume",
                "Cancel"
              );
              if (action === "Resume") {
                if (pipelineStateService) {
                  await pipelineStateService.resumePipeline();
                }
                headlessOrchestrator.runPipeline(runtime.issueNumber!).catch((err) => {
                  logger.error("Failed to resume paused pipeline", { err });
                });
              }
            }
          } catch {
            // Ignore malformed runtime files
          }
        }
      } catch {
        // Non-critical — skip if pipeline dir doesn't exist
      }
    })();
  }

  // ── 7. IssueQueueService ──────────────────────────────────────────────

  let issueQueueService: IssueQueueService | null = null;
  let concurrentPipelineManager: ConcurrentPipelineManager | null = null;
  let slotOutputManager: SlotOutputManager | null = null;

  // Forward-declare treeProvider so concurrent pipeline callbacks can reference it.
  // Actual creation happens in step 8, but the callbacks only fire after
  // pipelines start (well after this function returns).
  let treeProvider: PipelineTreeProvider;
  let projectBoardService: ProjectBoardService;

  // Initialize IssueQueueService for queue management (Issue #236, #305)
  // Use incrediRoot (git root) for correct .nightgauge directory location,
  // falling back to the first repo WorkspaceManager discovered when no single
  // incrediRoot resolved (multi-root .code-workspace) — see #4117. Gating on
  // `runnerRoot` instead of `incrediRoot` alone means the agent runner still
  // constructs whenever WorkspaceManager knows about at least one repository.
  // Pass workspaceState for cross-session persistence (Issue #305)
  const runnerRoot = resolveAgentRunnerRoot(incrediRoot, workspaceManager);
  if (runnerRoot) {
    issueQueueService = IssueQueueService.getInstance(
      runnerRoot,
      undefined,
      context.workspaceState
    );
    context.subscriptions.push(issueQueueService);

    // Initialize ConcurrentPipelineManager BEFORE queue callbacks so onItemAdded
    // can route through it (Issue #1621)
    const concurrentConfig = getConcurrentPipelineConfig(runnerRoot);
    const orchestratorFactory = (worktreePath: string, issueNumber: number) => {
      // Each worktree slot needs its own PipelineStateService so it reads/writes
      // state.json from the worktree's .nightgauge/ directory, not the
      // shared singleton's path. Without this, pipeline-start writes state.json
      // in the worktree but validateStageTransition reads the main repo's
      // (empty) state.json and blocks issue-pickup.
      // issueNumber is pre-set so startStage() never passes issueNumber:0 to Go.
      // Without it, pipeline.stateChanged events get issueNumber:0 which fails
      // z.number().positive() in ExecutionHistoryRunRecordV2Schema → no history.
      const slotStateService = PipelineStateService.createForWorktree(worktreePath, issueNumber);
      const slotOrchestrator = new HeadlessOrchestrator(slotStateService, logger);
      slotOrchestrator.setWorktreeOverride(worktreePath);
      slotOrchestrator.setMainRepoRoot(runnerRoot);
      return { orchestrator: slotOrchestrator, stateService: slotStateService };
    };

    // ConcurrentPipelineManager's `runnerRoot` argument only backs the DEFAULT
    // (single-root) WorktreeManager, used for same-repo items. Cross-repo /
    // multi-root dispatch resolves the correct target repo PER COMMAND via
    // resolveWorktreeManager() → workspaceManager.findRepositoryByGitHub(),
    // independent of this fixed root — see ConcurrentPipelineManager.ts (#2245,
    // #4117). Passing `workspaceManager` here is what enables that per-command
    // path; without it every item silently falls back to the single default root.
    concurrentPipelineManager = new ConcurrentPipelineManager(
      runnerRoot,
      issueQueueService,
      orchestratorFactory,
      logger,
      {
        maxConcurrent: concurrentConfig.maxConcurrent,
        worktreeBase: concurrentConfig.worktreeBase,
      },
      workspaceManager ?? undefined
    );
    context.subscriptions.push(concurrentPipelineManager);

    // Wire the stop-control guard: reject enqueue attempts while a Stop /
    // Abort is in progress. Blocks delayed autonomous.dispatch events from
    // re-populating the queue after the user has cleared it.
    const cpmRef = concurrentPipelineManager;
    issueQueueService.setShutdownGuard(() => cpmRef.isShutdownInProgress);

    // Set up per-slot output channels
    slotOutputManager = new SlotOutputManager();
    context.subscriptions.push(slotOutputManager);

    // Per-slot phase trackers for concurrent pipeline progress (keyed by issueNumber)
    const slotPhaseTrackers = new Map<number, ReturnType<typeof createPhaseTracker>>();

    // Per-slot OutputWindow state subscriptions (keyed by issueNumber).
    // Each concurrent slot runs on its own PipelineStateService; without these
    // subscriptions the Overview cards never see stage transitions or token
    // updates after the slot starts. Disposed when the slot completes, fails,
    // or is cleaned. Issue #2979.
    const slotStateSubscriptions = new Map<number, vscode.Disposable>();
    const disposeSlotStateSubscription = (issueNumber: number) => {
      const sub = slotStateSubscriptions.get(issueNumber);
      if (sub) {
        sub.dispose();
        slotStateSubscriptions.delete(issueNumber);
      }
    };
    // Per-slot Dashboard state subscriptions (keyed by issueNumber). Mirrors
    // the OutputWindow wiring above — the Dashboard's Pipeline tab also reads
    // off PipelineStateService events, but its singleton subscription never
    // fires for concurrent slots, leaving the progress bar pinned at 0%.
    const dashboardSlotStateSubscriptions = new Map<number, vscode.Disposable>();
    const disposeDashboardSlotStateSubscription = (issueNumber: number) => {
      const sub = dashboardSlotStateSubscriptions.get(issueNumber);
      if (sub) {
        sub.dispose();
        dashboardSlotStateSubscriptions.delete(issueNumber);
      }
    };

    /** Helper: update status bar with current concurrent slot state */
    const updateConcurrentStatusBar = () => {
      const activeSlots = concurrentPipelineManager!.getActiveSlots();
      statusBar.showConcurrentRunning(
        concurrentPipelineManager!.activeSlotCount,
        concurrentConfig.maxConcurrent,
        activeSlots.map((s) => s.issueNumber)
      );
      // Set context key for per-epic controls (Issue #2261)
      const hasEpics = activeSlots.some((s) => s.epicNumber != null);
      vscode.commands.executeCommand("setContext", "nightgauge.hasRunningEpics", hasEpics);
    };

    concurrentPipelineManager.setCallbacks({
      onSlotPreparing: (issueNumber, title, epicNumber) => {
        // Show immediate feedback in the tree view while worktree is created
        treeProvider.addPreparingSlot(issueNumber, title, epicNumber);
        updateConcurrentStatusBar();
        logger.info("Concurrent slot preparing (creating worktree)", {
          issueNumber,
        });
      },
      onSlotStarted: (slotIndex, issueNumber, title, slotStateService, epicNumber, repoSlug) => {
        // #191: scope this slot's disk session log to the run's TARGET repo.
        // The bootstrap log root is workspaceFolders[0]'s git root — for a
        // cross-repo run that is a different repository, and forensics
        // landed where nobody looks (bowlsheet#233's log lived in the
        // sibling infra repo).
        //
        // #216 / #307 follow-up: BOTH the disk-log root (setSlotLogRoot) and
        // the slot's identity record (registerSlotInfo) MUST be seeded before
        // any output for the slot is emitted. updateStage() below fires the
        // dispatch banner ("Starting issue-pickup for issue #N...") through
        // onStageChanged, which resolves the slot's issue number from
        // OutputWindowState's `slotInfos` (via getSlotIssueNumber / addEntry's
        // owning-slot lookup) — NOT from the closure's `issueNumber` param.
        // registerSlotInfo is what populates that record, so it must run
        // BEFORE updateStage, not after: #216 only moved setSlotLogRoot ahead
        // and left registerSlotInfo trailing, so the preamble's very first
        // line at spawn still resolved via slotInfos.get(slotIndex) being
        // undefined (fresh slot) or a stale prior occupant's entry (slot
        // index reuse) — falling through to the shared `this.issueNumber`
        // scalar, which is whatever a sibling slot (or a restored prior
        // session) last set it to. Registering first closes that gap: the
        // owning slot's record exists before the first byte can be written.
        const slotRepoPath = repoSlug
          ? (workspaceManager?.findRepositoryByGitHub(repoSlug)?.path ?? null)
          : null;
        outputWindow.setSlotLogRoot(slotIndex, slotRepoPath);
        outputWindow.registerSlotInfo(slotIndex, issueNumber, title, repoSlug);
        slotOutputManager!.createSlotChannel(slotIndex, issueNumber, title);
        slotOutputManager!.updateStage(issueNumber, "issue-pickup");

        // Wire this slot's PipelineStateService to the OutputWindow so the
        // Overview card reflects live stage transitions, token totals, and
        // derived status. The global singleton OutputWindow subscribes to
        // (via setStateService) never sees per-slot state emissions, so
        // without this the card froze at its initial state. Issue #2979.
        disposeSlotStateSubscription(issueNumber);
        disposeDashboardSlotStateSubscription(issueNumber);
        slotStateSubscriptions.set(
          issueNumber,
          outputWindow.subscribeSlotToStateService(slotIndex, slotStateService)
        );

        // Same problem the OutputWindow had — the Dashboard subscribes to the
        // workspace-singleton PipelineStateService, so per-slot worktree state
        // never reaches its Pipeline tab. Wire it here so the progress bar /
        // current run / phase indicator advance live during concurrent runs.
        dashboardSlotStateSubscriptions.set(
          issueNumber,
          dashboard.subscribeSlotToStateService(slotStateService)
        );

        // Create per-slot phase tracker for progress display (2/16 - [phase])
        slotPhaseTrackers.set(issueNumber, createPhaseTracker(slotStateService));
        // Single-slot: show stage name; multi-slot: show aggregated count
        if (concurrentPipelineManager!.maxConcurrentSlots === 1) {
          statusBar.showRunning("issue-pickup" as PipelineStage);
        } else {
          updateConcurrentStatusBar();
        }

        // Subscribe Discord to this slot's state events (Issue #1750)
        notifier!.subscribeToSlot(issueNumber, slotStateService, repoSlug);

        // Replace preparing placeholder with full concurrent slot
        treeProvider.addConcurrentSlot(slotIndex, issueNumber, title, slotStateService, epicNumber);

        // Invalidate ready + in-progress so the Repositories view doesn't keep
        // showing the dispatched issue under Ready for the full duration of
        // the run. Without this, the Ready count stays stale (the user sees N
        // until they manually refresh) even though Go has already moved the
        // issue to In progress on the board.
        if (repoSlug) {
          projectBoardService.invalidateStatusCache(repoSlug, ["ready", "in-progress"]);
        }

        logger.info("Concurrent slot added to pipeline view", {
          slotIndex,
          issueNumber,
        });
      },
      onSlotStageChanged: (_slotIndex, issueNumber, stage) => {
        // Complete phases for the previous stage before updating
        slotPhaseTrackers.get(issueNumber)?.completeStagePhases(stage);
        slotOutputManager!.updateStage(issueNumber, stage);
        // In single-slot mode, show per-stage status bar (mirrors pre-#1831 UX).
        // In multi-slot mode, show aggregated "Pipelines: N/M" display.
        if (concurrentPipelineManager!.maxConcurrentSlots === 1) {
          statusBar.showRunning(stage);
        } else {
          updateConcurrentStatusBar();
        }
      },
      onSlotOutput: (_slotIndex, issueNumber, data, stage) => {
        // Detect phase markers in stdout for progress display (2/16 - [phase])
        if (stage) {
          const marker = parsePhaseMarker(data);
          if (marker) {
            slotPhaseTrackers.get(issueNumber)?.onPhaseDetected(stage, marker);
            return; // Phase markers are metadata, not user-visible output
          }
        }
        slotOutputManager!.appendOutput(issueNumber, data);
      },
      onSlotError: (_slotIndex, issueNumber, data) => {
        slotOutputManager!.appendError(issueNumber, data);
      },
      onSlotPhaseStart: (_slotIndex, issueNumber, stage, name, index, total) => {
        slotPhaseTrackers.get(issueNumber)?.onPhaseDetected(stage, {
          name,
          index,
          total,
          stage,
        });
      },
      onSlotCompleted: (slotIndex, issueNumber, result, tokens, repoSlug) => {
        const costUsd = tokens.estimated_cost_usd;
        slotPhaseTrackers.get(issueNumber)?.completeAllStages();
        slotPhaseTrackers.delete(issueNumber);
        disposeSlotStateSubscription(issueNumber);
        disposeDashboardSlotStateSubscription(issueNumber);
        slotOutputManager!.markCompleted(issueNumber, true);
        // Flip the Output Window tab badge from the running spinner to the
        // terminal "complete" state with the final cost. Without this, the
        // badge stays stuck on the mid-run spinner because neither the
        // token-delta path nor the state-sync path fires again post-completion.
        outputWindow.notifySlotCompleted(slotIndex, "complete", costUsd);
        treeProvider.updateConcurrentSlotStatus(issueNumber, "completed");
        notifier!.unsubscribeFromSlot(issueNumber); // Issue #1750
        logger.info("Concurrent slot completed", {
          slotIndex,
          issueNumber,
          durationMs: result.totalDurationMs,
          costUsd,
        });
        updateConcurrentStatusBar();
        // Invalidate only the affected statuses for this repo so the Repositories
        // view fires targeted per-status refreshes instead of a global refresh
        // across all repos. A completed slot moves an issue through ready →
        // in-progress → done, so we invalidate all three. Issue #2912.
        if (repoSlug) {
          // Include in-review: a successful run typically opens a PR that
          // moves the issue to In review before pr-merge transitions it to
          // Done. Without in-review here, that tab stays stale.
          projectBoardService.invalidateStatusCache(repoSlug, [
            "ready",
            "in-progress",
            "in-review",
            "done",
          ]);
          // Mirror the sequential onPipelineComplete path: clear the
          // RepositoriesTreeProvider's own issueSummaryCache and re-render
          // that repo. Without this, completed issues linger in the
          // Repositories list view because the tree-item cache still holds
          // stale references even though ProjectBoardService is fresh.
          repositoriesTreeProvider?.invalidateAndRefreshRepo(repoSlug);
        } else {
          // Without a repoSlug we can't scope the refresh; skip rather than
          // invalidating every repo's cache and lighting up all fresh
          // indicators. The user can refresh manually if needed.
          logger.warn("Slot completed without repoSlug — skipping refresh", {
            issueNumber,
          });
        }

        // Record health snapshot — concurrent slots bypass Go scheduler so
        // pipeline.complete IPC never fires. Record directly here.
        // Issue #2245: health snapshots missing for concurrent pipeline runs.
        dashboard
          .reloadHistory()
          .then(() => dashboard.recordHealthSnapshotForRun(issueNumber, costUsd))
          .then(() => logger.info("Health snapshot recorded", { issueNumber, costUsd }))
          .catch(() => {
            // Non-critical
          });

        // The run-record history JSONL is NOT written here. The Go binary's
        // pipeline.notifyComplete handler is the sole authoritative writer for
        // every extension/HeadlessOrchestrator run — concurrent slots included,
        // since each slot's runPipeline() fires firePipelineComplete →
        // notifyComplete. The TS write that used to live here (#3704) built a
        // record with EMPTY `stages: {}` and NO run_id and raced the Go writer,
        // producing the degraded skeleton duplicate reported in #313. Removed:
        // health snapshot above + autonomous-complete notify below are the only
        // run-end side effects this hook still owns.

        // Notify Go autonomous scheduler that this run completed, so it
        // frees the slot and can dispatch the next candidate. Safe to call
        // even for non-autonomous runs — the Go side ignores unknown issues.
        const [slotOwner, slotRepo] = (repoSlug ?? "").split("/");
        if (slotOwner && slotRepo) {
          IpcClient.getInstance()
            .autonomousComplete(slotOwner, slotRepo, issueNumber, true)
            .catch(() => {
              // Non-critical — autonomous scheduler may not be running
            });
        }
      },
      onSlotFailed: (slotIndex, issueNumber, error, costUsd, repoSlug) => {
        slotPhaseTrackers.get(issueNumber)?.completeAllStages();
        slotPhaseTrackers.delete(issueNumber);
        disposeSlotStateSubscription(issueNumber);
        disposeDashboardSlotStateSubscription(issueNumber);
        // Remove preparing placeholder if worktree creation failed
        treeProvider.removePreparingSlot(issueNumber);
        slotOutputManager!.markCompleted(issueNumber, false);
        // Flip the Output Window tab badge to the terminal "error" state
        // with the final cost. Mirror of the onSlotCompleted wiring above.
        outputWindow.notifySlotCompleted(slotIndex, "error", costUsd);
        treeProvider.updateConcurrentSlotStatus(issueNumber, "failed");
        notifier!.unsubscribeFromSlot(issueNumber); // Issue #1750
        logger.warn("Concurrent slot failed", {
          slotIndex,
          issueNumber,
          error: error.message,
          costUsd,
        });
        updateConcurrentStatusBar();
        // Invalidate only the affected statuses for this repo. A failed slot
        // may have moved the issue into in-progress before failing, so invalidate
        // ready + in-progress. Issue #2912.
        if (repoSlug) {
          projectBoardService.invalidateStatusCache(repoSlug, ["ready", "in-progress"]);
          // Mirror onSlotCompleted: clear the RepositoriesTreeProvider's own
          // issueSummaryCache and re-render. PR #3189's auto-revert moves a
          // failed issue back to Ready, so the tree-item cache for both
          // ready and in-progress goes stale.
          repositoriesTreeProvider?.invalidateAndRefreshRepo(repoSlug);
        } else {
          // Without a repoSlug we can't scope the refresh; skip rather than
          // invalidating every repo's cache.
          logger.warn("Slot failed without repoSlug — skipping refresh", {
            issueNumber,
          });
        }

        // Record health snapshot even for failures — tracks reliability trends.
        if (costUsd > 0) {
          dashboard
            .reloadHistory()
            .then(() => dashboard.recordHealthSnapshotForRun(issueNumber, costUsd))
            .then(() =>
              logger.info("Health snapshot recorded (failed run)", {
                issueNumber,
                costUsd,
              })
            )
            .catch(() => {
              // Non-critical
            });
        }

        // Notify Go autonomous scheduler of failure (frees the slot).
        // Check for a conflict-restart signal so Go can skip the circuit
        // breaker — concurrent-branch collisions are infrastructure failures,
        // not code failures, and self-heal once we create a fresh branch.
        const [failOwner, failRepo] = (repoSlug ?? "").split("/");
        if (failOwner && failRepo) {
          const signalPath = incrediRoot
            ? path.join(
                incrediRoot,
                ".nightgauge",
                "pipeline",
                `conflict-restart-${issueNumber}.json`
              )
            : null;
          const conflictRestartCheck = signalPath
            ? fs
                .access(signalPath)
                .then(() => true)
                .catch(() => false)
            : Promise.resolve(false);
          // Detect environmental Anthropic-API failures from the error
          // text so the Go scheduler can apply the long environmental-
          // failure backoff and skip the lifetime-failure-cap increment.
          // - stream-idle-timeout (#3398): mid-stream cut while producing
          // - rate-limit-quota-exhausted (#3386): silent stall while
          //   waiting for the 5-hour bucket to reset
          // The Go OnPipelineComplete wiring will also classify independently
          // from runtime stage errors — sending the explicit kind here is a
          // defense-in-depth signal for IPC-mode runs where the TS layer
          // observed the result envelope first.
          const errMsg = error?.message ?? "";
          let terminalFailureKind: string | undefined;
          if (/stream idle timeout/i.test(errMsg)) {
            terminalFailureKind = "stream_idle_timeout";
          } else if (/github-quota-low/i.test(errMsg)) {
            // #3896: transient GitHub-API quota dip at pipeline-start. Forward
            // the explicit kind so Go applies the GitHub-quota cooldown (issue
            // stays Ready, no lifetime-cap increment) rather than treating it
            // as a real failure. The Go fallback also matches the embedded
            // token, but sending the kind keeps IPC-mode runs unambiguous.
            terminalFailureKind = "github_quota_low";
          } else if (
            /rate-limit-quota-exhausted/i.test(errMsg) ||
            // Anthropic session/usage limit — same environmental-quota class,
            // so Go applies the cooldown-until-reset backoff and skips the
            // lifetime-failure-cap increment. #3792.
            /\b(?:session|usage)\s+limit\b/i.test(errMsg)
          ) {
            terminalFailureKind = "rate_limit_quota_exhausted";
          } else if (/overloaded/i.test(errMsg)) {
            // Anthropic API 529 "Overloaded" — a transient capacity blip. Go's
            // ClassifyTerminalKind already matches "overloaded" from the
            // forwarded failureDetail, but sending the explicit kind keeps
            // IPC-mode runs unambiguous (matches the other branches) and routes
            // to the api_overloaded recovery path: 5-minute per-issue backoff,
            // board→Ready, no lifetime-cap increment, no pause.
            terminalFailureKind = "api_overloaded";
          } else if (/github-network-outage/i.test(errMsg)) {
            // #4002: api.github.com unreachable at pipeline-start. Routes to
            // the github_network_outage recovery path: short GLOBAL cooldown,
            // board→Ready, no lifetime-cap increment, no pause.
            terminalFailureKind = "github_network_outage";
          } else if (
            /socket connection was closed/i.test(errMsg) ||
            /socket hang up/i.test(errMsg)
          ) {
            // #4002: Anthropic API transport drop (local network blip killed
            // the stream mid-stage). Same recovery path as api_overloaded:
            // 5-minute per-issue backoff, board→Ready, no lifetime-cap
            // increment, no pause.
            terminalFailureKind = "api_connection_lost";
          } else if (/\[adapter-auth-failed\]|adapter_auth_failed/i.test(errMsg)) {
            // #312: the pipeline-start adapter auth gate refused to launch — a
            // probe timed out under a concurrent dispatch burst (transient
            // starvation; auth was never broken) or the adapter CLI is logged
            // out. Forward the explicit kind so the Go scheduler routes it to
            // the adapter_auth_failed recovery path (short backoff, board→Ready,
            // NO lifetime-cap increment, NO cascade feed, NO pause) instead of
            // the generic subagent_crash path that would pause the queue and
            // count three burst false-negatives toward the cascade breaker.
            terminalFailureKind = "adapter_auth_failed";
          }
          // #3431: forward the raw failure text so the autonomous
          // scheduler can extract `resetsAt=<unix>` from the
          // `[rate-limit-quota-exhausted]` kill marker and run the
          // global quota cooldown until the actual bucket reset rather
          // than a fixed 1-hour floor.
          //
          // #3442: previously this was gated on terminalFailureKind being
          // set, which meant a TS-side regex miss starved the Go-side
          // fallback (#3440) of any input. Forward errMsg whenever it's
          // non-empty so ClassifyTerminalKind can re-match on the Go side
          // as defense-in-depth, even if the TS regex above missed.
          const failureDetail = errMsg ? errMsg : undefined;
          void conflictRestartCheck.then((isConflictRestart) => {
            IpcClient.getInstance()
              .autonomousComplete(
                failOwner,
                failRepo,
                issueNumber,
                false,
                isConflictRestart,
                terminalFailureKind,
                failureDetail
              )
              .catch(() => {
                // Non-critical
              });
          });
        }
      },
      onSlotDeferred: (slotIndex, issueNumber, result, costUsd, repoSlug) => {
        // #305: pickup DEFERRED because the issue's native blockedBy edges are
        // still open. This is NOT a failure — no failure notification, no
        // autonomous pause, no failure telemetry. Free the slot, keep the issue
        // Ready/eligible, and signal the Go scheduler that this was a
        // non-failure deferral so it neither pauses nor bumps the
        // lifetime-failure cap.
        slotPhaseTrackers.get(issueNumber)?.completeAllStages();
        slotPhaseTrackers.delete(issueNumber);
        disposeSlotStateSubscription(issueNumber);
        disposeDashboardSlotStateSubscription(issueNumber);
        treeProvider.removePreparingSlot(issueNumber);
        slotOutputManager!.markCompleted(issueNumber, true);
        // Neutral terminal badge — a deferral is neither an error nor a
        // success. Use the non-error "complete" badge so the tab settles; the
        // issue itself stays Ready on the board.
        outputWindow.notifySlotCompleted(slotIndex, "complete", costUsd);
        treeProvider.removeConcurrentSlot(issueNumber);
        notifier!.unsubscribeFromSlot(issueNumber); // Issue #1750
        logger.info("Concurrent slot deferred — issue blocked by open dependencies", {
          slotIndex,
          issueNumber,
          costUsd,
        });
        updateConcurrentStatusBar();
        // The issue stays Ready — refresh that status so the tree reflects it.
        if (repoSlug) {
          projectBoardService.invalidateStatusCache(repoSlug, ["ready", "in-progress"]);
          repositoriesTreeProvider?.invalidateAndRefreshRepo(repoSlug);
        }

        // Tell the Go autonomous scheduler this run DEFERRED (non-failure).
        // terminalFailureKind="blocked_dependency" routes it to the scheduler's
        // non-failure branch: no lifetime-failure-cap increment, no
        // cascade-breaker feed, no pause; board status returns to Ready and the
        // blocker-close requeue re-dispatches once the blocker closes. The
        // `[blocked-dependency]` marker in the detail is defense-in-depth for
        // the Go ClassifyTerminalKind fallback.
        const [defOwner, defRepo] = (repoSlug ?? "").split("/");
        if (defOwner && defRepo) {
          IpcClient.getInstance()
            .autonomousComplete(
              defOwner,
              defRepo,
              issueNumber,
              false,
              false,
              "blocked_dependency",
              result.error?.message ??
                `[blocked-dependency] issue #${issueNumber} deferred — blocked by open dependencies`
            )
            .catch(() => {
              // Non-critical — autonomous scheduler may not be running
            });
        }
      },
      onSlotCleaned: (_slotIndex, issueNumber) => {
        // Defensive: in case the slot was cleaned without a prior
        // completion/failure callback, ensure the state subscription is
        // released so listeners don't leak across re-enqueued runs.
        disposeSlotStateSubscription(issueNumber);
        disposeDashboardSlotStateSubscription(issueNumber);
        treeProvider.removeConcurrentSlot(issueNumber);
      },
      onReEnqueueFailed: (issueNumber, error) => {
        // A re-enqueue failure after a slot-start failure means we almost
        // lost the item silently (the old bare catch {} swallowed it). Show
        // a user-visible toast so they know the item is gone and can manually
        // re-queue with full context. See Issue #2992.
        logger.error("Re-enqueue after slot failure failed", {
          issueNumber,
          error: error.message,
          stack: error.stack,
        });
        void vscode.window.showErrorMessage(
          `Failed to re-enqueue #${issueNumber}: ${error.message}`
        );
      },
      onAllComplete: () => {
        treeProvider.clearConcurrentSlots();
        statusBar.showIdle();
        logger.info("All concurrent pipeline slots completed");

        // Safety net: run epic completion sweep after all slots finish.
        // This catches epics whose last sub-issue just completed but the
        // per-issue completion check in reconcileCompletionSideEffects()
        // may have missed it due to timing or partial failures.
        const workspaceRoot = getWorkspaceRoot();
        if (workspaceRoot) {
          import("../commands/checkEpicCompletion")
            .then(({ runEpicCompletionSweep }) => runEpicCompletionSweep(workspaceRoot, logger))
            .then((sweepResult) => {
              if (sweepResult.epics_closed > 0) {
                const epicNumbers = sweepResult.closed_epics
                  .map((e: { number: number }) => `#${e.number}`)
                  .join(", ");
                logger.info("Epic sweep after all slots: closed epics", {
                  epicNumbers,
                  count: sweepResult.epics_closed,
                });
              }
            })
            .catch((err: unknown) => {
              logger.warn("Epic completion sweep after all slots failed", {
                err: err instanceof Error ? err.message : String(err),
              });
            });
        }
      },
    });

    // All pipelines now use worktrees (unified path, #1831) — always run
    // stale recovery and orphan cleanup regardless of maxConcurrent.
    logger.info("Worktree pipeline mode enabled", {
      maxConcurrent: concurrentConfig.maxConcurrent,
      worktreeBase: concurrentConfig.worktreeBase,
    });

    // Direct `nightgauge run` processes do not pass through this extension's
    // IPC server. Reconcile their atomic runtime snapshots from registered
    // roots so terminal/agent/automation launches remain visible (#27).
    let cliRoots: RegisteredPipelineRoot[] = [];
    const cliStateServices = new Map<
      string,
      { issueNumber: number; service: PipelineStateService }
    >();
    let nextCliSlotIndex = concurrentConfig.maxConcurrent;
    const refreshCliRoots = async (): Promise<void> => {
      if (!workspaceManager) return;
      const roots: RegisteredPipelineRoot[] = [];
      for (const repo of workspaceManager.getAllRepositories()) {
        await repo.loadConfig();
        const github = repo.github;
        if (github?.owner && github.repo) {
          roots.push({ path: repo.path, repo: `${github.owner}/${github.repo}` });
        }
      }
      cliRoots = roots;
    };
    const cliReconciler = new CliPipelineReconciliationService(() => cliRoots, {
      onDiscovered: (run) => {
        // An IPC-managed slot with the same issue is already authoritative.
        if (treeProvider.getConcurrentSlot(run.snapshot.issueNumber)) return;
        const stateService = PipelineStateService.createForWorktree(
          run.root,
          run.snapshot.issueNumber
        );
        stateService.applyRuntimeSnapshot(run.snapshot);
        cliStateServices.set(run.key, {
          issueNumber: run.snapshot.issueNumber,
          service: stateService,
        });
        treeProvider.addConcurrentSlot(
          nextCliSlotIndex++,
          run.snapshot.issueNumber,
          run.snapshot.title || `Issue #${run.snapshot.issueNumber}`,
          stateService
        );
        logger.info("Discovered direct CLI pipeline", {
          repo: run.snapshot.repo,
          issueNumber: run.snapshot.issueNumber,
          runId: run.snapshot.runId,
        });
      },
      onUpdated: (run) => {
        cliStateServices.get(run.key)?.service.applyRuntimeSnapshot(run.snapshot);
      },
      onSettled: (run) => {
        const tracked = cliStateServices.get(run.key);
        if (!tracked) return;
        treeProvider.removeConcurrentSlotIfOwned(tracked.issueNumber, tracked.service);
        tracked.service.dispose();
        cliStateServices.delete(run.key);
        logger.info("Direct CLI pipeline settled", {
          repo: run.snapshot.repo,
          issueNumber: run.snapshot.issueNumber,
          runId: run.snapshot.runId,
        });
      },
    });
    const disposeCliWorkspaceListener = workspaceManager?.onWorkspaceChanged(() => {
      void refreshCliRoots().then(() => cliReconciler.scan());
    });
    void workspaceInitPromise?.then(async () => {
      await refreshCliRoots();
      cliReconciler.start();
    });
    context.subscriptions.push(cliReconciler, {
      dispose: () => {
        disposeCliWorkspaceListener?.dispose();
        for (const tracked of cliStateServices.values()) tracked.service.dispose();
        cliStateServices.clear();
      },
    });

    // Recover stale concurrent slots from previous session (Issue #1643)
    const staleRecovery = new StaleSlotRecoveryService(
      runnerRoot,
      concurrentConfig.worktreeBase,
      logger
    );
    staleRecovery
      .recoverStaleSlots()
      .then((recovered) => {
        if (recovered.length === 0) return;

        logger.info("Recovered stale concurrent slots", {
          count: recovered.length,
          issues: recovered.map((r) => r.issueNumber),
        });

        // Surface recovered slots in the Pipeline tree view as failed
        for (const slot of recovered) {
          const slotStateService = PipelineStateService.createForWorktree(slot.worktreePath);
          treeProvider.addConcurrentSlot(
            0, // slot index doesn't matter for display
            slot.issueNumber,
            slot.title,
            slotStateService
          );
          treeProvider.updateConcurrentSlotStatus(slot.issueNumber, "failed");
        }

        // Notify user
        const issueList = recovered.map((r) => `#${r.issueNumber}`).join(", ");
        vscode.window.showWarningMessage(
          `Recovered ${recovered.length} stale pipeline slot(s): ${issueList}. ` +
            "Stages were marked as failed due to extension reload."
        );
      })
      .catch((err) => {
        logger.warn("Stale slot recovery failed", {
          error: err instanceof Error ? err.message : String(err),
        });
      });

    // Set callbacks for queue events (Issue #299 - failure handling, Issue #820 - blocked warning, Issue #1402 - auto-start)
    issueQueueService.setCallbacks({
      onPipelineFailure: async (
        failedIssueNumber: number,
        queueLength: number
      ): Promise<boolean> => {
        // Show dialog: [Stop Queue] [Continue to Next]
        const action = await vscode.window.showErrorMessage(
          `Pipeline failed for issue #${failedIssueNumber}. ` +
            `${queueLength} issue${queueLength > 1 ? "s" : ""} remaining in queue.`,
          { modal: true },
          "Continue to Next",
          "Stop Queue"
        );

        // Return true to continue, false to stop
        return action === "Continue to Next";
      },
      onBlockedWarning: async (
        issueNumber: number,
        issueTitle: string,
        blockerTitles: string[]
      ): Promise<boolean> => {
        const blockerList = blockerTitles.join(", ");
        const message =
          `Issue #${issueNumber} is blocked by ${blockerList}. ` + `Add to queue anyway?`;

        const choice = await vscode.window.showWarningMessage(
          message,
          { modal: true },
          "Add Anyway",
          "Cancel"
        );

        return choice === "Add Anyway";
      },
      onItemAdded: async (item) => {
        // Unified worktree path: always route through ConcurrentPipelineManager (#1831)
        const availSlots = concurrentPipelineManager?.availableSlotCount ?? 0;
        const autoStart = issueQueueService!.getConfig().autoStart;
        logger.info("onItemAdded callback fired", {
          issueNumber: item.issueNumber,
          availableSlots: availSlots,
          autoStart,
          hasCPM: !!concurrentPipelineManager,
        });
        if (concurrentPipelineManager && availSlots > 0 && autoStart) {
          // Debounce fillSlots so rapid-fire enqueues (e.g. epic sub-issues)
          // coalesce into a single fill after all items are in the queue.
          // Without this, the first onItemAdded awaits worktree creation
          // (which blocks subsequent enqueues from firing onItemAdded),
          // causing only 1 slot to fill instead of N.
          if (debouncedFillSlotsTimer) {
            clearTimeout(debouncedFillSlotsTimer);
          }
          debouncedFillSlotsTimer = setTimeout(async () => {
            debouncedFillSlotsTimer = null;
            if ((concurrentPipelineManager?.availableSlotCount ?? 0) > 0) {
              await concurrentPipelineManager?.fillSlots();
            }
          }, 100);
        }
      },
    });

    // Connect HeadlessOrchestrator to queue service for batch auto-start.
    // ConcurrentPipelineManager handles slot-based queue processing (#1831),
    // but the main orchestrator still needs the queue service for batch
    // pipeline completion → handleQueueAutoStart → startNextQueuedIssue.
    headlessOrchestrator.setQueueService(issueQueueService);

    // Track queue state for "Stop After Current" button visibility.
    //
    // Two distinct signals:
    // - queueHasWaitingItems: there are pending/ready items behind the current run
    //   (i.e. more work would auto-start after the current pipeline completes).
    // - queueHasActiveItems: there are ANY items the queue is responsible for —
    //   pending, ready, or the currently-processing one. This is the correct
    //   gate for "Stop queue after current issue": the user wants to signal the
    //   queue to idle as soon as the running pipeline finishes, even when the
    //   running item is the only one left in the queue. Without this, the
    //   button vanishes the moment the last pending item transitions to
    //   processing, and the user cannot reach it from the view-title menu.
    context.subscriptions.push(
      issueQueueService.onQueueChanged((state) => {
        const items = state?.items ?? [];
        const hasWaiting = items.some((i) => i.status === "pending" || i.status === "ready");
        const hasActive = hasWaiting || items.some((i) => i.status === "processing");
        vscode.commands.executeCommand("setContext", "nightgauge.queueHasWaitingItems", hasWaiting);
        vscode.commands.executeCommand("setContext", "nightgauge.queueHasActiveItems", hasActive);
      })
    );

    // Load persisted queue state on activation (Issue #305)
    issueQueueService.loadState().catch((error) => {
      logger.error("Failed to load queue state:", error);
    });

    // Listen for workspace folder changes to clear queue state (Issue #305)
    context.subscriptions.push(
      vscode.workspace.onDidChangeWorkspaceFolders(async () => {
        logger.info("Workspace folders changed, clearing queue state");
        await issueQueueService?.clearAllState();
      })
    );

    logger.info("IssueQueueService initialized");
  }

  // ── 8. Tree views ─────────────────────────────────────────────────────

  // Initialize drag and drop controller for issue tree items (Issue #296)
  // This controller is shared between ProjectBoardTreeProvider and PipelineTreeProvider
  const dragController = new IssueDragAndDropController();
  dragController.setLogger(logger);
  context.subscriptions.push(dragController);

  // Initialize pipeline tree view
  treeProvider = new PipelineTreeProvider();
  treeProvider.dragAndDropController = dragController;

  // Connect TreeProvider to PipelineStateService for unified state management
  // This enables automatic UI sync from the authoritative state file
  if (pipelineStateService) {
    treeProvider.setStateService(pipelineStateService);
    dragController.setStateService(pipelineStateService);
  }

  // Connect TreeProvider to IssueQueueService for queue section
  if (issueQueueService) {
    treeProvider.setQueueService(issueQueueService);
    dragController.setQueueService(issueQueueService);
  }

  // Connect ConcurrentPipelineManager for active slot validation (#2436)
  if (concurrentPipelineManager) {
    dragController.setConcurrentPipelineManager(concurrentPipelineManager);
  }

  // Connect context for warning dialog persistence (Issue #307)
  dragController.setContext(context);

  // Initialize CompletedIssuesService for completed/failed issue tracking (Issue #301)
  const completedIssuesService = CompletedIssuesService.getInstance(context.workspaceState);
  context.subscriptions.push(completedIssuesService);

  // Connect TreeProvider to CompletedIssuesService for completed/failed sections
  treeProvider.setCompletedIssuesService(completedIssuesService);

  // Subscribe to pipeline state events to track completions/failures
  if (pipelineStateService) {
    // Track pipeline completion (when state is cleared)
    let lastPipelineState: PipelineState | null = null;
    // Guards against a failed run being moved to Completed when state.json is
    // cleared after a failure. Set true by onStageError; reset after each run.
    // Fix for Issue #1502.
    let currentRunHadFailure = false;

    context.subscriptions.push(
      pipelineStateService.onStateChanged((state) => {
        // If state transitions from non-null to null, pipeline run ended
        if (lastPipelineState && !state) {
          if (!currentRunHadFailure) {
            // Only mark as completed if no stage failed during this run.
            // When a stage fails, addFailed() was already called via onStageError
            // and the issue must remain in the Failed section.
            completedIssuesService.removeFromFailed(lastPipelineState.issue_number);
            completedIssuesService.addCompleted(
              lastPipelineState.issue_number,
              lastPipelineState.title,
              lastPipelineState.branch,
              readIssueLabels(lastPipelineState.issue_number),
              headlessOrchestrator.getLastCostAnomalyExceeded()
            );
          }
          // Reset for the next pipeline run
          currentRunHadFailure = false;
        } else if (!lastPipelineState && state) {
          // New pipeline run starting — reset flag so a prior failure from a
          // different run cannot suppress completion of this run.
          currentRunHadFailure = false;
        }
        lastPipelineState = state;
      })
    );

    // Track pipeline failures (when a stage fails)
    // Use lastPipelineState (captured synchronously via onStateChanged) instead
    // of async getState() to avoid race condition in batch mode where state may
    // be overwritten by the next issue before the async read completes.
    context.subscriptions.push(
      pipelineStateService.onStageError(({ stage, issueNumber, error }) => {
        // Mark the current run as failed so the onStateChanged null-transition
        // handler skips addCompleted() when state.json is later cleared.
        currentRunHadFailure = true;
        const state = lastPipelineState;
        if (state && state.issue_number === issueNumber) {
          completedIssuesService.addFailed(
            issueNumber,
            state.title,
            state.branch,
            stage,
            error,
            readIssueLabels(issueNumber)
          );
        } else {
          // Fallback: record with minimal info if state doesn't match
          completedIssuesService.addFailed(issueNumber, `Issue #${issueNumber}`, "", stage, error);
        }
      })
    );
  }

  const treeView = vscode.window.createTreeView("nightgauge.pipelineView", {
    treeDataProvider: treeProvider,
    dragAndDropController: dragController,
    showCollapseAll: true,
  });

  // Connect TreeView to provider for title updates (Issue #306)
  treeProvider.setTreeView(treeView);

  // Live workflow tree (run → phase → agent → judge) off the SDK EventBus node
  // stream (#3919). Folding + rendering live here; it is attached to the
  // EventStreamService in extension.ts once the SSE stream opens.
  const workflowTreeProvider = new WorkflowTreeProvider();
  context.subscriptions.push(workflowTreeProvider);
  const workflowTreeView = vscode.window.createTreeView("nightgauge.workflowView", {
    treeDataProvider: workflowTreeProvider,
    showCollapseAll: true,
  });
  context.subscriptions.push(workflowTreeView);

  // Action Center — severity-ordered DecisionRequest cards (ADR 015 / #325).
  // Local-first (ADR 015 §C): attaches directly to the local Go daemon's IPC
  // connection, not the hosted-platform SSE stream, so it is a complete
  // surface with zero account/session dependency.
  const attentionTreeProvider = new AttentionTreeProvider();
  context.subscriptions.push(attentionTreeProvider);
  const attentionTreeView = vscode.window.createTreeView("nightgauge.attentionView", {
    treeDataProvider: attentionTreeProvider,
  });
  context.subscriptions.push(attentionTreeView);
  attentionTreeProvider.attach(IpcClient.getInstance());

  // Initialize project board tree views (one per status tab)
  const workspaceRoot = getWorkspaceRoot();
  const settings = getSettings();

  const workItemSourceConfig = getWorkItemSourceConfig();
  if (workspaceRoot) {
    projectBoardService = createWorkItemProvider(
      workItemSourceConfig,
      workspaceRoot
    ) as ProjectBoardService;
  } else {
    // Create with empty workspace - will show error state
    projectBoardService = createWorkItemProvider(workItemSourceConfig, "") as ProjectBoardService;
  }
  context.subscriptions.push(projectBoardService);

  // Wire the project board service into the Repositories view drag-and-drop
  // controller now that it exists (the controller is constructed earlier, in
  // the workspaceManager block). Without this, cross-status drops onto a
  // section header silently fall through (the cross-column guard requires a
  // resolvable workspace root + board) and epic drops can't cascade sub-issues.
  repositoriesDnDController?.setBoardService(projectBoardService);

  // Wire GitHub GraphQL rate-limit state → status bar counter (real-time quota visibility).
  // ProjectBoardService fires onRateLimitState on every checkRateLimit() call (each board fetch).
  context.subscriptions.push(
    projectBoardService.onRateLimitState((state) => statusBar.updateRateLimit(state))
  );

  // ── DI Container (Issue #2771 — Part 1: GitHub services; Issue #2772 — Part 2: Pipeline services) ──
  // Simple typed registry. Services are registered here and resolved by callers
  // (e.g. Dashboard) instead of creating new instances directly.
  const container = new Container();

  // Register the primary project board service.
  container.register("projectBoardService", projectBoardService);

  // Register GitHubService for sub-issue operations via IPC.
  // owner/repo are derived from project config and workspace name.
  // GitHubService is registered for future call sites; no current callers in Part 1.
  const gitHubOwner = configBridge.getProject()?.owner ?? "";
  const gitHubRepo = workspaceRoot ? path.basename(workspaceRoot) : "";
  const githubService = new GitHubService(gitHubOwner, gitHubRepo);
  container.register("githubService", githubService);

  // Register pipeline services in container (Issue #2772)
  // pipelineStateService was created above (line ~417); register it now that the container exists.
  if (pipelineStateService) {
    container.register("pipelineStateService", pipelineStateService);
  }

  // Register Part 3 services in container (Issue #2773)
  // These services are initialized before the container is created (earlier in activation),
  // so they are batch-registered here after the container is available.
  container.register("configBridge", configBridge);
  if (offlineManager) {
    container.register("offlineManager", offlineManager);
  }
  if (discordService) {
    container.register("discordService", discordService);
  }
  if (notifier) {
    container.register("notifier", notifier);
  }
  if (repositoriesTreeProvider) {
    container.register("repositoriesTreeProvider", repositoriesTreeProvider);
  }
  if (slotOutputManager) {
    container.register("slotOutputManager", slotOutputManager);
  }

  // Initialize QueryService and SavedQueriesService for GQL queries (Issue #138)
  // Must be initialized after projectBoardService
  let queryService: QueryService | null = null;
  let savedQueriesService: SavedQueriesService | null = null;
  let queryResultsProvider: QueryResultsTreeProvider | null = null;

  if (incrediRoot) {
    queryService = new QueryService(projectBoardService, context.workspaceState);
    savedQueriesService = new SavedQueriesService(incrediRoot);
    context.subscriptions.push(queryService);
    context.subscriptions.push(savedQueriesService);

    // Initialize QueryResultsTreeProvider for query results display
    queryResultsProvider = new QueryResultsTreeProvider(queryService);
    context.subscriptions.push(queryResultsProvider);
    container.register("queryResultsTreeProvider", queryResultsProvider);

    // Create tree view for query results
    const queryResultsTreeView = vscode.window.createTreeView("nightgauge.queryResults", {
      treeDataProvider: queryResultsProvider,
      showCollapseAll: true,
    });
    context.subscriptions.push(queryResultsTreeView);

    // Set context variable for query results visibility
    vscode.commands.executeCommand("setContext", "nightgauge.hasQueryResults", false);

    // Update context when query completes
    queryService.onQueryComplete(() => {
      vscode.commands.executeCommand("setContext", "nightgauge.hasQueryResults", true);
    });

    logger.info("QueryService and SavedQueriesService initialized");
  }

  // Initialize KnowledgeTreeProvider for knowledge base view (Issue #1686,
  // rewired to three-section model in #2964 — requires PipelineStateService
  // and IpcClient)
  if (workspaceRoot && pipelineStateService) {
    const { KnowledgeTreeProvider } = await import("../views/KnowledgeTreeProvider");
    const knowledgeTreeProvider = new KnowledgeTreeProvider(
      workspaceRoot,
      pipelineStateService,
      IpcClient.getInstance()
    );
    context.subscriptions.push(knowledgeTreeProvider);

    const knowledgeTreeView = vscode.window.createTreeView("nightgauge.knowledgeView", {
      treeDataProvider: knowledgeTreeProvider,
      showCollapseAll: true,
    });
    context.subscriptions.push(knowledgeTreeView);

    workspaceManager?.onWorkspaceChanged(() => {
      const newRoot = getWorkspaceRoot();
      if (newRoot) knowledgeTreeProvider.updateWorkspaceRoot(newRoot);
    });

    // Expose refresh command
    context.subscriptions.push(
      vscode.commands.registerCommand("nightgauge.refreshKnowledgeView", () =>
        knowledgeTreeProvider.refresh()
      )
    );

    // Search Knowledge command (#2964) — prompts for a query and routes the
    // results into the Search section of the rewired tree.
    const { searchKnowledge } = await import("../commands/searchKnowledge");
    context.subscriptions.push(
      vscode.commands.registerCommand("nightgauge.searchKnowledge", () =>
        searchKnowledge(IpcClient.getInstance(), knowledgeTreeProvider)
      )
    );

    // Copy wiki-link context menu — emits [[#NNNN]] for issue dirs or
    // [[relative-path]] for other markdown files.
    context.subscriptions.push(
      vscode.commands.registerCommand(
        "nightgauge.copyWikiLink",
        async (item: { filePath?: string } | undefined) => {
          if (!item?.filePath) {
            vscode.window.showWarningMessage("No knowledge file selected");
            return;
          }
          const rel = item.filePath.startsWith(workspaceRoot)
            ? item.filePath.slice(workspaceRoot.length).replace(/^[/\\]/, "")
            : item.filePath;
          // Best-effort: detect an issue directory ".../features/NNNN-slug/..."
          const issueMatch = rel.match(/(?:features|epics)\/(\d+)-/);
          const wikiLink = issueMatch ? `[[#${issueMatch[1]}]]` : `[[${rel}]]`;
          await vscode.env.clipboard.writeText(wikiLink);
          vscode.window.showInformationMessage(`Copied wiki-link: ${wikiLink}`);
        }
      )
    );

    container.register("knowledgeTreeProvider", knowledgeTreeProvider);
    logger.info("KnowledgeTreeProvider initialized (three-section model #2964)");

    // Initialize ActiveIssueKnowledgeProvider for the dedicated PRD/decisions
    // panel (Issue #3599; migrated to IPC in #2964)
    if (pipelineStateService) {
      const { ActiveIssueKnowledgeProvider } =
        await import("../providers/ActiveIssueKnowledgeProvider");
      const activeKnowledgeProvider = new ActiveIssueKnowledgeProvider(
        workspaceRoot,
        pipelineStateService,
        IpcClient.getInstance()
      );
      context.subscriptions.push(activeKnowledgeProvider);

      const activeKnowledgeView = vscode.window.createTreeView(
        "nightgauge.activeIssueKnowledgeView",
        { treeDataProvider: activeKnowledgeProvider, showCollapseAll: true }
      );
      context.subscriptions.push(activeKnowledgeView);

      context.subscriptions.push(
        vscode.commands.registerCommand("nightgauge.activeKnowledge.refresh", () =>
          activeKnowledgeProvider.refresh()
        )
      );

      context.subscriptions.push(
        vscode.commands.registerCommand(
          "nightgauge.activeKnowledge.openFile",
          async (filePath: string) => {
            await vscode.commands.executeCommand("vscode.open", vscode.Uri.file(filePath));
            // Telemetry is best-effort
            IpcClient.getInstance()
              .call("knowledge.telemetry", { event: "knowledge.read", path: filePath })
              .catch(() => {});
          }
        )
      );

      container.register("activeKnowledgeProvider", activeKnowledgeProvider);
      logger.info("ActiveIssueKnowledgeProvider initialized");
    }
  }

  // Register KnowledgeDocumentLinkProvider for [[wiki-link]] support (Issue #1687)
  // Scoped to incrediRoot so we pass the correct workspace root for knowledge resolution
  if (incrediRoot) {
    const knowledgeDocumentLinkProvider = new KnowledgeDocumentLinkProvider(
      incrediRoot,
      logger,
      workspaceManager?.getWorkspaceConfig() ?? undefined
    );
    context.subscriptions.push(
      vscode.languages.registerDocumentLinkProvider(
        { language: "markdown" },
        knowledgeDocumentLinkProvider
      )
    );
    context.subscriptions.push(knowledgeDocumentLinkProvider);
    container.register("knowledgeDocumentLinkProvider", knowledgeDocumentLinkProvider);
    logger.info("KnowledgeDocumentLinkProvider registered for markdown");
  }

  // Top-level project board tab views removed (Issue #2184).
  // Status groups now live under each repository in the Repositories view (#2183).
  const projectBoardViews: vscode.TreeView<BaseTreeItem>[] = [];
  const projectBoardProviders: Map<TabId, ProjectBoardTreeProvider> = new Map();

  // ── 9. Workspace startup sync ────────────────────────────────────────
  //
  // Previously this section subscribed to an explicit `onRepositoryChanged`
  // event fired by the (now removed) "Switch to Repository" flow. With the
  // current-repo pointer gone, we only need a one-time sync on init:
  // point the project board singleton + queue service + pipeline manager at
  // the resolved active repo, and wire per-repo services for the
  // Repositories tree. Cross-repo routing now happens via explicit
  // repo arguments at each call site (or via the autonomous allowlist).

  if (workspaceManager) {
    context.subscriptions.push(
      workspaceManager.onWorkspaceChanged(async () => {
        const active = resolveActiveRepository(workspaceManager);
        if (active) {
          logger.info("onWorkspaceChanged: syncing to active repo", {
            repo: active.name,
            path: active.path,
          });
          // Tell the Go binary about the workspace root. MUST await so the
          // binary's workspaceRoot is updated before any subsequent
          // config.getProjectConfig call (dispatched in a goroutine) reads it.
          try {
            await IpcClient.getInstance().workspaceSetRoot(active.path);
          } catch (err) {
            logger.warn("Failed to update Go workspace root on startup", {
              error: err,
            });
          }

          // updateWorkspaceRoot() already clears all caches. Use refreshView()
          // instead of refresh() so providers don't each call clearCache() again,
          // which would null fetchAllItemsInFlight and defeat deduplication.
          projectBoardService.updateWorkspaceRoot(active.path);
          for (const provider of projectBoardProviders.values()) {
            provider.refreshView();
          }

          issueQueueService?.updateWorkspaceRoot(active.path);
          concurrentPipelineManager?.updateRepoRoot(active.path);
        }

        if (!workspaceManager!.isMultiWorkspace() || !repositoriesTreeProvider) {
          return;
        }

        // Wire one IWorkItemProvider per repo for live count display.
        // Use createWorkItemProvider so the per-repo provider respects the
        // configured work_item_source.mode (e.g., "composite" enables repo-only
        // issue discovery via CompositeAdapter + ReadyIssueTreeProvider).
        const wic = getWorkItemSourceConfig();
        const perRepoServices = new Map<string, IWorkItemProvider>();
        for (const repo of workspaceManager!.getAllRepositories()) {
          perRepoServices.set(
            repo.name,
            new ReadyIssueTreeProvider(createWorkItemProvider(wic, repo.path))
          );
        }
        repositoriesTreeProvider.setProjectBoardServices(perRepoServices);
        logger.info("Per-repo project board services wired", {
          repos: Array.from(perRepoServices.keys()),
        });
      })
    );
  }

  // ── Startup sync ──────────────────────────────────────────────────────
  // WorkspaceManager.initialize() fires onWorkspaceChanged once, but because
  // it was NOT awaited (section 2), intervening `await`s may let it resolve
  // and fire the event before the handler above is registered. Handle that
  // race by running the same sync directly after initialize() resolves.
  if (workspaceManager) {
    workspaceInitPromise?.then(() => {
      const active = resolveActiveRepository(workspaceManager);
      if (!active) return;

      // If the active repo is the same as the startup root, no sync needed.
      if (active.path === incrediRoot) return;

      logger.info("Startup sync: event handler missed, syncing manually", {
        repo: active.name,
        path: active.path,
      });

      IpcClient.getInstance()
        .workspaceSetRoot(active.path)
        .then(() => {
          projectBoardService.updateWorkspaceRoot(active.path);
          for (const provider of projectBoardProviders.values()) {
            provider.refreshView();
          }
          issueQueueService?.updateWorkspaceRoot(active.path);
          concurrentPipelineManager?.updateRepoRoot(active.path);

          if (workspaceManager!.isMultiWorkspace() && repositoriesTreeProvider) {
            const wic = getWorkItemSourceConfig();
            const perRepoServices = new Map<string, IWorkItemProvider>();
            for (const repo of workspaceManager!.getAllRepositories()) {
              perRepoServices.set(
                repo.name,
                new ReadyIssueTreeProvider(createWorkItemProvider(wic, repo.path))
              );
            }
            repositoriesTreeProvider.setProjectBoardServices(perRepoServices);
          }
        })
        .catch((err) => logger.warn("Startup sync failed", { error: err }));
    });
  }

  // ── 11. Context viewer ────────────────────────────────────────────────

  // Initialize context file viewer
  // Use incrediRoot (git root) for correct .nightgauge directory location
  const contextPath = incrediRoot ? `${incrediRoot}/${settings.contextPath}` : settings.contextPath;
  const contextViewer = new ContextFileViewer(contextPath);

  // ── 12. Dashboard & output ────────────────────────────────────────────

  // Initialize TelemetryStore for JSONL-based history (Issue #1007)
  // Must be initialized before Dashboard so it can be passed as dependency.
  let telemetryStore: TelemetryStore | null = null;
  if (incrediRoot) {
    telemetryStore = new TelemetryStore(incrediRoot);
    container.register("telemetryStore", telemetryStore);
  }

  // Forward reference set after Dashboard is created (line ~1569).
  // Used by the pipeline.complete handler to trigger a history reload after write.
  let dashboardHistoryReloader: (() => Promise<void>) | null = null;

  // Subscribe to Go pipeline.complete for history writing (Issue #1984)
  // The Go scheduler emits this event on every pipeline completion (success or failure).
  // Legacy HeadlessOrchestrator path writes history via the 'pipeline-finish' stage handler
  // above — these two paths are mutually exclusive, no double-write risk.
  {
    const ipc = IpcClient.getInstance();
    const disposeGoHistoryWriter = ipc.on("pipeline.complete", async (data: unknown) => {
      if (!incrediRoot || !telemetryStore) return;
      const d = data as {
        issueNumber: number;
        success: boolean;
        totalInputTokens: number;
        totalOutputTokens: number;
        totalCostUSD: number;
        durationMs?: number;
        startedAt?: string;
        perStage: Array<{
          stage: string;
          inputTokens: number;
          outputTokens: number;
          cacheRead?: number;
          costUsd?: number;
        }>;
      };

      try {
        // Mark as Go-driven before any writes so pipeline-finish handler can
        // detect this and skip its duplicate history write (Issue #2545).
        pipelineCompleteIssues.add(d.issueNumber);

        // Read issue context for metadata (title, branch, labels, routing).
        // Try new subdirectory format first (Go scheduler path writes
        // issue-{N}/issue-pickup-context.json), then fall back to the flat
        // format (HeadlessOrchestrator path writes issue-{N}.json).
        const pipelineDir = path.join(incrediRoot, ".nightgauge", "pipeline");
        const issueContextPaths = [
          path.join(pipelineDir, `issue-${d.issueNumber}`, "issue-pickup-context.json"),
          path.join(pipelineDir, `issue-${d.issueNumber}.json`),
        ];
        let issueCtx: {
          title?: string;
          branch?: string;
          base_branch?: string;
          labels?: string[];
          routing?: {
            complexity_score?: number;
            suggested_route?: string;
            skip_stages?: string[];
          };
        } = {};
        for (const issueContextPath of issueContextPaths) {
          try {
            const raw = await fs.readFile(issueContextPath, "utf-8");
            issueCtx = JSON.parse(raw) as typeof issueCtx;
            break; // Stop at first readable file
          } catch {
            // Try next path
          }
        }

        const now = new Date().toISOString();
        const startedAt = d.startedAt ?? now;
        const durationMs = d.durationMs ?? 0;

        // Build per-stage token map (keyed by stage name)
        const perStageTokens: Record<
          string,
          {
            input: number;
            output: number;
            cache_read: number;
            cache_creation: number;
            cost_usd: number;
          }
        > = {};
        for (const s of d.perStage) {
          perStageTokens[s.stage] = {
            input: s.inputTokens,
            output: s.outputTokens,
            cache_read: s.cacheRead ?? 0,
            cache_creation: 0,
            cost_usd: s.costUsd ?? 0,
          };
        }

        // Build stages record — mark all ran stages as complete, last as failed if pipeline failed
        const stages: Record<string, { status: "complete" | "failed" | "skipped" | "pending" }> =
          {};
        for (const s of d.perStage) {
          stages[s.stage] = { status: "complete" };
        }
        if (!d.success && d.perStage.length > 0) {
          const lastStage = d.perStage[d.perStage.length - 1].stage;
          stages[lastStage] = { status: "failed" };
        }

        const goRecordMetadata = extractMetadata(issueCtx.labels);
        const record = {
          schema_version: "2" as const,
          record_type: "run" as const,
          issue_number: d.issueNumber,
          title: issueCtx.title ?? `Issue #${d.issueNumber}`,
          branch: issueCtx.branch ?? "",
          base_branch: issueCtx.base_branch ?? "main",
          execution_mode: "automatic" as const,
          started_at: startedAt,
          completed_at: now,
          total_duration_ms: durationMs,
          outcome: (d.success ? "complete" : "failed") as "complete" | "failed" | "cancelled",
          labels: issueCtx.labels,
          size: goRecordMetadata.size,
          type: goRecordMetadata.type,
          priority: goRecordMetadata.priority,
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          stages: stages as any,
          tokens: {
            total_input: d.totalInputTokens,
            total_output: d.totalOutputTokens,
            total_cache_read: 0,
            total_cache_creation: 0,
            estimated_cost_usd: d.totalCostUSD,
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            per_stage: perStageTokens as any,
          },
          files: { read_count: 0, written_count: 0 },
          routing: {
            complexity_score: issueCtx.routing?.complexity_score ?? 0,
            path: issueCtx.routing?.suggested_route ?? "standard",
            skip_stages: issueCtx.routing?.skip_stages ?? [],
          },
          recorded_at: now,
        };

        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const written = await telemetryStore.appendRunRecord(record as any);
        if (written) {
          logger.info("Go pipeline history record written", {
            issueNumber: d.issueNumber,
          });
          // Reload dashboard history so health sparklines reflect the new run
          // without requiring the user to close and reopen the dashboard panel.
          // Then record a health snapshot to health-history.jsonl — this was
          // previously only triggered via Dashboard.syncFromPipelineState(),
          // which never fires for concurrent slots because per-slot
          // PipelineStateServices don't notify the Dashboard singleton.
          // Issue #2245: health snapshots missing for concurrent pipeline runs.
          try {
            await dashboardHistoryReloader?.();
            await dashboard.recordHealthSnapshotForRun(d.issueNumber, d.totalCostUSD);
            logger.info("Health snapshot recorded for concurrent slot", {
              issueNumber: d.issueNumber,
              costUsd: d.totalCostUSD,
            });
          } catch {
            // Non-critical — panel may not be open
          }
        } else {
          logger.warn("Go pipeline history record REJECTED by schema validation", {
            issueNumber: d.issueNumber,
          });
        }
      } catch (err) {
        logger.warn("Failed to write Go pipeline history record", { err });
      }

      // Trigger a telemetry upload after pipeline completion (#3315). This is a
      // redundant, idempotent flush: the active-run counter + cadence are driven
      // by the pipeline lifecycle IPC wiring (onRunStarted/onRunCompleted, set up
      // near the uploader construction), so onPipelineCompleted() intentionally
      // does NOT touch activeRunCount — no double-decrement (#234).
      telemetryUploaderService?.onPipelineCompleted();
    });
    context.subscriptions.push({
      dispose: () => disposeGoHistoryWriter.dispose(),
    });

    // Model-unavailable fallback notifications (#42). The Go scheduler emits
    // pipeline.modelFallback when the API rejects a stage's model (not on
    // plan / unknown ID / model usage cap) and the run substitutes the
    // next-best tier. Surface it on both user-facing channels: a VSCode
    // warning toast and a Discord embed.
    const firstLine = (text: string): string => {
      const line = text.split("\n", 1)[0] ?? "";
      return line.length > 160 ? `${line.slice(0, 157)}...` : line;
    };
    const disposeModelFallback = ipc.on("pipeline.modelFallback", (data: unknown) => {
      const d = data as {
        repo?: string;
        issueNumber?: number;
        stage?: string;
        fromModel?: string;
        toModel?: string;
        reason?: string;
      };
      if (!d?.fromModel || !d?.toModel) return;
      const summary =
        `${d.fromModel} was rejected by the API` +
        (d.reason ? ` (${firstLine(d.reason)})` : "") +
        ` — ${d.stage ?? "stage"} fell back to ${d.toModel} for the rest of this run.`;
      logger.warn("Model fallback", {
        repo: d.repo,
        issueNumber: d.issueNumber,
        stage: d.stage,
        fromModel: d.fromModel,
        toModel: d.toModel,
      });
      void vscode.window.showWarningMessage(`Nightgauge: ${summary}`);
      void discordService?.notifyModelFallback(d.issueNumber ?? 0, summary);
    });
    context.subscriptions.push({
      dispose: () => disposeModelFallback.dispose(),
    });
  }

  // Initialize dashboard
  // Pass incrediRoot so Dashboard can subscribe to PipelineStateService
  // and receive real-time pipeline updates.
  // Pass TelemetryStore for JSONL-sourced history (Issue #1007).
  const dashboard = new Dashboard(
    context.extensionUri,
    context.workspaceState,
    incrediRoot ?? undefined,
    telemetryStore ?? undefined,
    container
  );

  // Wire up the history reloader so the pipeline.complete handler above can
  // trigger a dashboard refresh after writing a new run record to TelemetryStore.
  dashboardHistoryReloader = () => dashboard.reloadHistory();

  // Initialize usage limits tracking service (Issue #1333)
  const usageLimitsService: UsageLimitsService = new UsageLimitsService(
    dashboard.getState(),
    notificationService,
    statusBar
  );
  usageLimitsService.initialize();
  context.subscriptions.push(usageLimitsService);
  dashboard.setUsageLimitsService(usageLimitsService);

  // Connect Dashboard to CompletedIssuesService for live updates (Issue #1164)
  dashboard.setCompletedIssuesService(completedIssuesService);

  // Connect Dashboard to IssueQueueService for live queue updates (Issue #1164)
  if (issueQueueService) {
    dashboard.setQueueService(issueQueueService);
  }

  // Connect Dashboard to ConcurrentPipelineManager for live active-slot data.
  // Go's queue.changed omits active slots (items are dequeued before running),
  // so the TS-side manager is the authoritative source for what's executing.
  if (concurrentPipelineManager) {
    dashboard.setConcurrentPipelineManager(concurrentPipelineManager);
  }

  // Initialize Brownfield Modernization Dashboard (Issue #1163)
  let brownfieldDashboard: BrownfieldDashboard | null = null;
  if (incrediRoot) {
    const brownfieldDataService = new BrownfieldDataService(incrediRoot);
    brownfieldDashboard = new BrownfieldDashboard(context.extensionUri, brownfieldDataService);
    context.subscriptions.push(brownfieldDataService, brownfieldDashboard);
  }

  // Initialize Knowledge Value Dashboard (Issue #3600)
  const knowledgeValueDashboard = new KnowledgeValueDashboard(context.extensionUri);
  context.subscriptions.push(knowledgeValueDashboard);

  // Initialize output window using ConfigBridge (Issue #476)
  const outputWindowSettings = getOutputWindowSettings();
  const outputWindowConfig = {
    autoOpen: outputWindowSettings.autoOpen,
    autoScroll: outputWindowSettings.autoScroll,
    wordWrap: outputWindowSettings.wordWrap,
    verboseLevel: outputWindowSettings.verboseLevel as "minimal" | "normal" | "verbose" | "debug",
    showTokenUsage: outputWindowSettings.showTokenUsage,
    rehydrateFromLogs: outputWindowSettings.rehydrateFromLogs,
  };
  const outputWindow = new OutputWindow(
    context.extensionUri,
    context.workspaceState,
    outputWindowConfig
  );

  // Connect OutputWindow to HeadlessOrchestrator for interrupt control
  if (headlessOrchestrator) {
    outputWindow.setOrchestrator(headlessOrchestrator);
  }

  // Connect OutputWindow to PipelineStateService for unified state management
  // This enables automatic token usage and stage status sync
  if (pipelineStateService) {
    outputWindow.setStateService(pipelineStateService);
  }

  // Wire concurrent slot output to OutputWindow so automated-mode output
  // is visible in the Output view (not just per-slot OutputChannel tabs).
  if (slotOutputManager) {
    // Track current stage per issue so onOutput can tag lines correctly
    const slotCurrentStage = new Map<number, PipelineStage>();

    slotOutputManager.setCallbacks({
      onOutput: (slotIndex, issueNumber, text, level) => {
        const stage = slotCurrentStage.get(issueNumber);
        // Route output to per-slot buffer in the OutputWindow (Issue #2705)
        outputWindow.appendLine(text, level === "error" ? "error" : "info", stage, { slotIndex });
      },
      onStageChanged: (slotIndex, issueNumber, stage) => {
        // #307 follow-up: the dispatch-seed call (onSlotStarted's direct
        // updateStage("issue-pickup")) and the real stage-start event
        // (relayed from the per-slot orchestrator's onStageStart, once the
        // stage actually begins) both target "issue-pickup" for a fresh slot
        // (#230). SlotOutputManager already gates repeats of the same stage
        // via its own per-channel `lastStage` field, but that gate lives on
        // the SlotChannel object's lifecycle, not on this callback — so a
        // channel-vs-event ordering gap (e.g. the seed racing ahead of
        // createSlotChannel) can let both calls through here. Guard
        // independently so the dispatch banner — and the disk-log line and
        // webview entry it produces — can never be written twice for a
        // stage this slot already reported.
        const isRepeatStage = slotCurrentStage.get(issueNumber) === stage;
        slotCurrentStage.set(issueNumber, stage);
        // Automated per-stage update — ensure the panel exists without
        // stealing the user's active tab (no reveal).
        outputWindow.show();
        outputWindow.setIssueNumber(issueNumber);
        outputWindow.updateStageStatus(stage, "running");
        // Update slot stage label for tab header (Issue #2705)
        outputWindow.updateSlotStage(slotIndex, stage);
        if (!isRepeatStage) {
          outputWindow.appendLine(`Starting ${stage} for issue #${issueNumber}...`, "info", stage, {
            slotIndex,
          });
        }
      },
    });
  }

  // Wire default pipeline callbacks for queue-initiated runs (Issue #447)
  // These provide OutputWindow integration when the orchestrator starts
  // pipelines from queue auto-start or manual resume.
  // Phase tracking wired here so phases appear for ALL execution paths
  // (queue auto-start, resume, etc.), not just pickupIssue. @see Issue #1115
  if (headlessOrchestrator) {
    const defaultPhaseTracker = createPhaseTracker(pipelineStateService!);
    const defaultStreamHandler = createStreamOutputHandler(outputWindow, {
      onPhaseDetected: defaultPhaseTracker.onPhaseDetected,
    });

    // Sequential slot registration state (Issue #2812)
    // Tracks one slot per pipeline run so the output window tab bar renders
    // for single-issue (non-concurrent) runs.
    let nextSequentialSlotIndex = 0;
    let sequentialSlotRegistered = false;

    headlessOrchestrator.setDefaultPipelineCallbacks({
      onStageStart: (stage) => {
        // Automated per-stage update — ensure the panel exists without
        // stealing the user's active tab (no reveal).
        outputWindow.show();
        // #191: scope the sequential run's disk session log to its target
        // repo (cross-repo runs previously logged into workspaceFolders[0]'s
        // repo). Idempotent per stage; cleared on pipeline completion.
        const runOverrideSlug = headlessOrchestrator!.getRepoOverride();
        outputWindow.setRunLogRoot(
          runOverrideSlug
            ? (workspaceManager?.findRepositoryByGitHub(runOverrideSlug)?.path ?? null)
            : null
        );
        // Set issue number from pipeline state (async, fire-and-forget).
        // On the first stage of each pipeline run, also register a sequential
        // slot so the output window tab bar renders for single-slot runs (#2812).
        pipelineStateService
          ?.getCurrentIssueNumber()
          .then((num) => {
            if (num) {
              outputWindow.setIssueNumber(num);
              if (!sequentialSlotRegistered) {
                sequentialSlotRegistered = true;
                outputWindow.registerSlotInfo(nextSequentialSlotIndex++, num, `Issue #${num}`);
              }
            }
          })
          .catch(() => {});
        outputWindow.updateStageStatus(stage, "running");
        outputWindow.appendLine(`Starting ${getStageLabel(stage)}...`, "info", stage);
        statusBar.showRunning(stage);
        treeProvider.updateStageStatus(stage, "running");
      },
      onStageComplete: (stage, result) => {
        // Flush the stream buffer for this stage so any remaining
        // phase markers are detected before we complete the last phase.
        defaultStreamHandler.flushStage(stage);
        defaultPhaseTracker.completeStagePhases(stage);

        if (result.success) {
          outputWindow.updateStageStatus(stage, "complete");
          treeProvider.updateStageStatus(stage, "complete");
          outputWindow.appendLine(`\u2713 ${getStageLabel(stage)} completed`, "info", stage);
          statusBar.showComplete(stage);
        } else {
          outputWindow.updateStageStatus(stage, "error");
          treeProvider.updateStageStatus(stage, "failed");
          outputWindow.appendLine(
            `\u2717 ${getStageLabel(stage)} failed: ${result.error?.message || "Unknown error"}`,
            "error",
            stage
          );
          statusBar.showError(result.error?.message || "Stage failed");
        }
      },
      onStageError: (_stage, _error) => {
        // Platform telemetry for stage errors is emitted by the Go IPC layer
        // when PipelineStateService reports the failed transition. No-op here.
      },
      onStdout: (stage, data) => defaultStreamHandler.onStdout(stage, data),
      onStderr: (stage, data) => defaultStreamHandler.onStderr(stage, data),
      onStallWarningClear: (stage) => {
        outputWindow.removeStallWarnings(stage);
      },
      onPipelineComplete: (result) => {
        // Terminal platform telemetry (pipeline_done) is emitted inside
        // HeadlessOrchestrator.firePipelineComplete via the run's state service,
        // so it covers every completion path uniformly. This callback only
        // handles in-process UI refresh below.
        // Refresh tree views after ANY pipeline completion path (success,
        // failure, budget kill, stall kill, user abort, early exit).
        // Scope to the pipeline's actual target repo so only that repo's
        // fresh indicator lights up — not every repository in the workspace.
        // Issue #2340: tree views didn't refresh after pipeline completion.
        // Follow-up: constrain to the pipeline's repo (matches concurrent-slot
        // behavior — see onSlotCompleted above).
        const scopedRefresh = (repoSlug: string | undefined) => {
          if (repoSlug) {
            projectBoardService.invalidateStatusCache(repoSlug, [
              "ready",
              "in-progress",
              "in-review",
              "done",
            ]);
            repositoriesTreeProvider?.invalidateAndRefreshRepo(repoSlug);
          } else {
            logger.warn("Pipeline completed without resolvable repoSlug — skipping refresh");
          }
        };

        // Prefer the orchestrator's repoOverride — that's the authoritative
        // target repo for this run. Fall back to active workspace detection
        // only when no override is set (single-repo workspaces).
        const overrideSlug = headlessOrchestrator!.getRepoOverride();
        if (overrideSlug) {
          scopedRefresh(overrideSlug);
        } else {
          const workspaceRoot = getWorkspaceRoot();
          if (workspaceRoot) {
            void getRepoIdentity(workspaceRoot)
              .then((identity) => {
                scopedRefresh(identity ? `${identity.owner}/${identity.repo}` : undefined);
              })
              .catch(() => scopedRefresh(undefined));
          } else {
            scopedRefresh(undefined);
          }
        }
        // Reset per-run slot flag so the next pipeline run registers a new slot (#2812)
        sequentialSlotRegistered = false;
        // Clear the per-run log root so non-run output falls back to the
        // bootstrap default (#191).
        outputWindow.setRunLogRoot(null);
      },
    });
  }

  // Configure disk logging and pipeline settings from config.yaml
  // Reads pipeline.logs config and pr.auto_merge for pipeline execution behavior
  if (incrediRoot) {
    const workspaceRootForLogs = incrediRoot; // Capture for closure
    const yamlService = new IncrediYamlService(workspaceRootForLogs);
    yamlService
      .read()
      .then((result) => {
        if (result.success && result.config) {
          const logsConfig = result.config.pipeline?.logs;
          outputWindow.setLogConfig(workspaceRootForLogs, logsConfig);
          logger.debug("Disk logging configured", { logsConfig });

          // Wire pr.auto_merge to orchestrator deferMerge setting
          // auto_merge=true means DON'T defer (run full pipeline)
          // auto_merge=false means DO defer (require manual PR merge)
          const prConfig = result.config.pull_request ?? result.config.pr;
          if (headlessOrchestrator && prConfig) {
            const autoMerge = prConfig.auto_merge ?? true;
            headlessOrchestrator.setConfig({ deferMerge: !autoMerge });
            logger.debug("Pipeline deferMerge configured", {
              autoMerge,
              deferMerge: !autoMerge,
            });
          }
        } else {
          // Use defaults if no config
          outputWindow.setLogConfig(workspaceRootForLogs);
          logger.debug("Disk logging configured with defaults");
        }
        yamlService.dispose();
      })
      .catch((error) => {
        // Log but don't fail - disk logging is non-critical
        logger.warn("Failed to read config.yaml for disk logging config", {
          error,
        });
        outputWindow.setLogConfig(workspaceRootForLogs);
      });
  }

  // ── 13. Context watcher ───────────────────────────────────────────────

  // Initialize context watcher for Ready Issues → Pipeline integration
  // This watches .nightgauge/pipeline/ for context files created by Claude Code terminal
  // Use incrediRoot (git root) so we watch the correct directory
  if (incrediRoot) {
    const contextWatcher = new ContextWatcherService(incrediRoot, logger);

    // Helper to run a stage with OutputWindow status updates via headless CLI
    // Uses PipelineStateService as single source of truth (Issue #154)
    const runStageWithOutput = async (
      stage: import("@nightgauge/sdk").PipelineStage,
      issueNumber: number
    ) => {
      // Guard: bookend stages must not run through skill runner (no SKILL.md)
      // They are handled synchronously in autoContinueToNextStage and onIssuePickedUp
      if (stage === "pipeline-start" || stage === "pipeline-finish") {
        logger.warn("runStageWithOutput called with bookend stage - skipping skill runner", {
          stage,
        });
        return;
      }

      // Track that extension is executing this stage
      // This distinguishes extension-initiated from chat-initiated runs (Issue #81)
      activeExtensionExecutions.add(stage);

      // Update PipelineStateService (single source of truth)
      // This will trigger onStateChanged which updates UI components
      if (pipelineStateService) {
        try {
          await pipelineStateService.startStage(stage);
        } catch (error) {
          logger.warn("Failed to update pipeline state on stage start", {
            stage,
            error,
          });
        }
      }

      // Show and configure output window — runStageWithOutput is driven
      // by the ContextWatcher (chat-initiated flow), not a direct user
      // action on this window, so ensure-created (no reveal) to avoid
      // yanking the active tab away from the user.
      outputWindow.show();
      outputWindow.setIssueNumber(issueNumber);
      outputWindow.updateStageStatus(stage, "running");
      outputWindow.appendLine(
        `Starting ${getStageLabel(stage)} for issue #${issueNumber}...`,
        "info",
        stage
      );

      statusBar.showRunning(stage);

      // CRITICAL: Update TreeProvider to show running state (syncs sidebar with output)
      treeProvider.updateStageStatus(stage, "running");

      // Track previous cumulative totals to compute deltas for updateTokens().
      // onTokenUsage receives cumulative running totals from TokenAccumulator,
      // but updateTokens() is additive. @see Issue #843
      let prevUsage = {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      };

      // Set up callbacks for streaming output to OutputWindow
      const callbacks: SkillRunCallbacks = {
        onStdout: (data) => {
          // Parse stream-json output for display
          for (const line of data.split("\n")) {
            if (!line.trim()) continue;
            if (isStreamJsonEnvelope(line)) continue;
            try {
              const parsed = JSON.parse(line);
              if (parsed.type === "assistant" && parsed.message?.content) {
                for (const block of parsed.message.content) {
                  if (block.type === "text" && block.text) {
                    outputWindow.appendLine(block.text, "info", stage);
                  } else if (block.type === "tool_use") {
                    outputWindow.appendLine(`[Tool: ${block.name}]`, "tool", stage);
                  }
                }
              } else if (parsed.type === "content_block_delta" && parsed.delta?.text) {
                outputWindow.appendLine(parsed.delta.text, "info", stage);
              }
              // Note: Token usage is now handled by onTokenUsage callback
            } catch {
              // Not JSON, use as plain text — filter envelope fragments
              if (line.trim() && !isEnvelopeFragment(line)) {
                outputWindow.appendLine(line, "info", stage);
              }
            }
          }
        },
        // Token usage callback - updates PipelineStateService (single source of truth)
        // Token updates flow through: PipelineStateService → onStateChanged → OutputWindow.syncFromState()
        // Do NOT call outputWindow.updateTokenUsage() directly - it causes duplicate updates (Issue #162)
        // Convert cumulative totals to deltas before passing to updateTokens(). @see Issue #843
        onTokenUsage: (usage) => {
          const delta = {
            inputTokens: usage.inputTokens - prevUsage.inputTokens,
            outputTokens: usage.outputTokens - prevUsage.outputTokens,
            cacheReadTokens: usage.cacheReadTokens - prevUsage.cacheReadTokens,
            cacheCreationTokens: usage.cacheCreationTokens - prevUsage.cacheCreationTokens,
            costUsd: usage.costUsd - prevUsage.costUsd,
          };
          prevUsage = { ...usage };

          if (pipelineStateService) {
            pipelineStateService
              .updateTokens({
                inputTokens: delta.inputTokens,
                outputTokens: delta.outputTokens,
                cacheReadTokens: delta.cacheReadTokens,
                cacheCreationTokens: delta.cacheCreationTokens,
                costUsd: delta.costUsd,
                stage,
              })
              .catch((err) => {
                logger.warn("Failed to update pipeline state tokens", {
                  err,
                });
              });
          }

          logger.debug("Token usage from skillRunner", {
            stage,
            inputTokens: usage.inputTokens,
            outputTokens: usage.outputTokens,
          });
        },
        // Live in-stage token/cost estimate (#233), already throttled to >=5s in
        // skillRunner. Mirror the Go-driven path (PipelineBridge): fire-and-forget
        // a stage_progress event to the platform via Go. The local UI is already
        // driven by onTokenUsage above, so this does NOT touch pipelineStateService
        // (avoids double-counting) — it only streams the estimate to the platform.
        onStageProgress: (usage) => {
          IpcClient.getInstance()
            .call("pipeline.notifyStageProgress", {
              repo: "",
              issueNumber,
              stage,
              inputTokens: usage.inputTokens,
              outputTokens: usage.outputTokens,
              cacheReadTokens: usage.cacheReadTokens,
              costUsd: usage.costUsd,
            })
            .catch((err) => {
              logger.warn("Failed to notify stage progress", { stage, err });
            });
        },
        onStderr: (data) => {
          for (const line of data.split("\n")) {
            if (!line.trim()) continue;
            if (isStreamJsonEnvelope(line)) continue;
            if (isEnvelopeFragment(line)) continue;
            const isError =
              line.toLowerCase().includes("error") || line.toLowerCase().includes("failed");
            outputWindow.appendLine(line, isError ? "error" : "warning", stage);
          }
        },
        onComplete: async (result) => {
          // Remove from active executions first (Issue #81)
          activeExtensionExecutions.delete(stage);

          if (result.success) {
            logger.info("Stage completed successfully", { stage, issueNumber });

            // Update PipelineStateService (single source of truth)
            if (pipelineStateService) {
              try {
                // Attribute the stage to the served model (#91) + executing
                // adapter so the Go notify handler records them for BuildV2Record
                // (#268: by-model cost breakdown + Adapter Mix donut).
                await pipelineStateService.completeStage(stage, {
                  model: result.servedModel ?? result.modelDecision?.model,
                  adapter: result.adapterDecision?.adapter,
                });
              } catch (error) {
                logger.warn("Failed to update pipeline state on stage complete", {
                  stage,
                  error,
                });
              }
            }

            // CRITICAL: Update BOTH OutputWindow AND TreeProvider for state alignment
            outputWindow.updateStageStatus(stage, "complete");
            treeProvider.updateStageStatus(stage, "complete");

            outputWindow.appendLine(`\u2713 ${getStageLabel(stage)} completed`, "info", stage);
            statusBar.showComplete(stage);

            // Trigger auto-continue if enabled (for stages not handled by ContextWatcher)
            // This ensures feature-validate and pr-merge can auto-continue
            autoContinueToNextStage(stage, issueNumber);
          } else {
            logger.error("Stage failed", {
              stage,
              issueNumber,
              error: result.error?.message ?? "Unknown error",
            });

            // Update PipelineStateService with failure
            if (pipelineStateService) {
              try {
                await pipelineStateService.failStage(
                  stage,
                  result.error?.message || "Unknown error"
                );
              } catch (error) {
                logger.warn("Failed to update pipeline state on stage failure", {
                  stage,
                  error,
                });
              }
            }

            // Update BOTH OutputWindow AND TreeProvider for error state
            outputWindow.updateStageStatus(stage, "error");
            treeProvider.updateStageStatus(stage, "failed");

            outputWindow.appendLine(
              `\u2717 ${getStageLabel(stage)} failed: ${result.error?.message || "Unknown error"}`,
              "error",
              stage
            );
            statusBar.showError(result.error?.message || "Stage failed");
          }
        },
        onError: (error) => {
          // Clean up active execution tracking on error (Issue #81)
          activeExtensionExecutions.delete(stage);

          logger.error("Stage execution error", { stage, issueNumber, error });
          outputWindow.appendLine(`Error: ${error.message}`, "error", stage);
        },
      };

      // Run stage via headless Claude Code CLI
      // SKILL.md instructions are passed as prompt
      // Each command starts a NEW conversation (context isolation)
      const handle = runStageSkillHeadless(stage, issueNumber, callbacks);

      if (!handle.process) {
        // Error already reported via callbacks
        return;
      }

      outputWindow.appendLine(`Running ${getStageLabel(stage)} in headless mode...`, "info", stage);
    };

    // Helper to auto-continue to next stage if enabled
    // Helper to auto-continue to next stage if enabled (Issue #476 - Uses ConfigBridge)
    const autoContinueToNextStage = async (completedStage: string, issueNumber: number) => {
      const pipelineUISettings = getPipelineUISettings();

      if (!pipelineUISettings.autoContinue) {
        return;
      }

      const nextStage = getNextStage(completedStage as import("@nightgauge/sdk").PipelineStage);
      if (!nextStage) {
        // Pipeline complete - handle completion flow
        logger.debug("No next stage - pipeline complete");
        handlePipelineComplete(issueNumber);
        return;
      }

      // Handle pipeline-finish bookend stage synchronously (zero AI tokens)
      // This stage runs deterministically — no SKILL.md, no Claude CLI
      if (nextStage === "pipeline-finish") {
        logger.info("Running pipeline-finish bookend stage", { issueNumber });
        if (pipelineStateService) {
          try {
            await pipelineStateService.startStage("pipeline-finish", {
              forceBackward: true,
            });
            treeProvider.updateStageStatus("pipeline-finish", "running");
            // Update OutputWindow status chip for bookend stage (Issue #284)
            outputWindow.updateStageStatus("pipeline-finish", "running");
            // Brief pause so the tree renders "running" before completing
            await new Promise((r) => setTimeout(r, 500));

            // History RunRecord write (#232): the Go pipeline.notifyComplete
            // handler is now the SOLE authoritative writer of the interactive
            // RunRecord — it writes to the run's TARGET repo for both success
            // and failure. This TS bookend no longer writes one. The two former
            // TS writers (the pipeline.complete subscriber for Go-scheduler runs,
            // and this pipeline-finish write for interactive runs) both wrote to
            // the launch root and the interactive one didn't fire on failure/kill.
            // Disabling this write avoids a duplicate zero-cost ghost record
            // (Issue #2545); the dashboard TelemetryStore index self-heals by
            // rebuilding from the Go-written JSONL when stale, so nothing breaks.
            const state = await pipelineStateService.getState();
            const isGoDriven = state != null && pipelineCompleteIssues.has(state.issue_number);
            if (isGoDriven) {
              logger.info(
                "Skipping pipeline-finish history write - Go-driven execution already wrote record",
                { issueNumber: state.issue_number }
              );
            } else {
              logger.info(
                "Skipping pipeline-finish history write — the Go notifyComplete handler now writes the authoritative RunRecord to the target repo (#232)",
                { issueNumber }
              );
            }

            await pipelineStateService.completeStage("pipeline-finish");
            treeProvider.updateStageStatus("pipeline-finish", "complete");
            // Update OutputWindow status chip for bookend stage (Issue #284)
            outputWindow.updateStageStatus("pipeline-finish", "complete");
            logger.info("Pipeline-finish bookend stage completed");
          } catch (error) {
            logger.warn("Failed to complete pipeline-finish stage", { error });
            try {
              await pipelineStateService.failStage(
                "pipeline-finish",
                error instanceof Error ? error.message : "Unknown error"
              );
              treeProvider.updateStageStatus("pipeline-finish", "failed");
              // Update OutputWindow status chip for error state (Issue #284)
              outputWindow.updateStageStatus("pipeline-finish", "error");
            } catch (stateErr) {
              logger.warn("Failed to mark pipeline-finish as failed", {
                stateErr,
              });
            }
          }
        }
        handlePipelineComplete(issueNumber);
        return;
      }

      // Check if pipeline is paused
      const isPaused = await pipelineStateService?.isPaused();
      if (isPaused) {
        logger.info("Pipeline is paused, not auto-continuing", {
          completedStage,
          nextStage,
        });
        return;
      }

      // Check execution mode from PipelineStateService
      const executionMode = await pipelineStateService?.getExecutionMode();

      // Get delay from ConfigBridge (Issue #476)
      const delay = pipelineUISettings.autoContinueDelay;

      if (executionMode === "automatic") {
        // AUTOMATIC MODE: No notification, just run
        logger.info("Auto-continuing to next stage (automatic mode)", {
          completedStage,
          nextStage,
          issueNumber,
          delay,
        });

        // IMMEDIATE: Show spinner before delay for instant visual feedback (#634)
        // setStatus() is idempotent — runStageWithOutput will call these again harmlessly
        treeProvider.updateStageStatus(nextStage, "running");
        outputWindow.updateStageStatus(nextStage, "running");
        statusBar.showRunning(nextStage);

        setTimeout(() => {
          runStageWithOutput(nextStage, issueNumber);
        }, delay);
      } else {
        // MANUAL MODE: Show notification with proper dismiss handling
        logger.info("Auto-continuing to next stage (manual mode)", {
          completedStage,
          nextStage,
          issueNumber,
          delay,
        });

        // Delay before continuing (allows UI to update)
        setTimeout(() => {
          vscode.window
            .showInformationMessage(
              `${getStageLabel(completedStage as import("@nightgauge/sdk").PipelineStage)} complete. Continue to ${getStageLabel(nextStage)}?`,
              "Run Now",
              "Yes to All",
              "Pause"
            )
            .then(async (selection) => {
              if (selection === "Run Now") {
                // User explicitly chose to continue
                await pipelineStateService?.resumePipeline();
                runStageWithOutput(nextStage, issueNumber);
              } else if (selection === "Yes to All") {
                // User chose to run all remaining stages automatically
                logger.info("User selected Yes to All - switching to automatic mode", {
                  completedStage,
                  nextStage,
                  issueNumber,
                });
                await pipelineStateService?.setExecutionMode("automatic");
                await pipelineStateService?.resumePipeline();
                runStageWithOutput(nextStage, issueNumber);
              } else {
                // User clicked "Pause" OR dismissed (selection === undefined)
                // Treat both as "pause" - this fixes the dismiss = continue bug
                logger.info("Pipeline paused by user", {
                  nextStage,
                  dismissed: selection === undefined,
                });
                await pipelineStateService?.pausePipeline();
                vscode.window.showInformationMessage(
                  `Pipeline paused. Run "${getStageLabel(nextStage)}" to continue.`
                );
              }
            });
        }, delay);
      }
    };

    // Handle pipeline completion after pr-merge
    const handlePipelineComplete = async (issueNumber: number) => {
      logger.info("Pipeline complete", { issueNumber });

      // Notify user with completion sound
      if (notificationService) {
        notificationService.notifyPipelineComplete(issueNumber);
      }

      // Show the pipeline summary panel (Issue #103)
      // This displays comprehensive metrics and provides reset option
      if (pipelineStateService) {
        try {
          const state = await pipelineStateService.getState();
          if (state) {
            // Initialize summary panel if not already created
            if (!pipelineSummary) {
              pipelineSummary = new PipelineSummary(context.extensionUri);
            }
            await pipelineSummary.show(state);
            logger.info("Pipeline summary displayed", { issueNumber });
            return;
          }
        } catch (error) {
          logger.warn("Failed to show pipeline summary, falling back to notification", { error });
        }
      }

      // Fallback: Show simple completion notification if summary fails
      const selection = await vscode.window.showInformationMessage(
        `Pipeline complete for issue #${issueNumber}!`,
        "Complete & Reset",
        "Keep Open"
      );

      if (selection === "Complete & Reset") {
        await resetPipeline(issueNumber);
      }
    };

    // Reset pipeline state and context files
    // Uses incrediRoot (git root) for correct .nightgauge directory location
    const resetPipeline = async (issueNumber?: number) => {
      if (!incrediRoot) {
        return;
      }

      try {
        // Clear PipelineStateService
        await pipelineStateService?.clearPipeline();

        // Delete context files
        const contextDir = `${incrediRoot}/.nightgauge/pipeline`;
        const plansDir = `${incrediRoot}/.nightgauge/plans`;

        if (issueNumber) {
          // Clean up specific issue files
          const filesToDelete = [
            `issue-${issueNumber}.json`,
            `planning-${issueNumber}.json`,
            `dev-${issueNumber}.json`,
            `validate-${issueNumber}.json`,
            `pr-${issueNumber}.json`,
          ];

          for (const filename of filesToDelete) {
            const files = await vscode.workspace.findFiles(
              new vscode.RelativePattern(contextDir, filename)
            );
            for (const file of files) {
              try {
                await vscode.workspace.fs.delete(file);
              } catch {
                // Ignore if file doesn't exist
              }
            }
          }

          // Delete running-*.json files
          const runningFiles = await vscode.workspace.findFiles(
            new vscode.RelativePattern(contextDir, "running-*.json")
          );
          for (const file of runningFiles) {
            try {
              await vscode.workspace.fs.delete(file);
            } catch {
              // Ignore if file doesn't exist
            }
          }

          // Delete plan files for this issue
          const planFiles = await vscode.workspace.findFiles(
            new vscode.RelativePattern(plansDir, `${issueNumber}-*.md`)
          );
          for (const file of planFiles) {
            try {
              await vscode.workspace.fs.delete(file);
            } catch {
              // Ignore if file doesn't exist
            }
          }
        }

        // Reset TreeView
        treeProvider.clearIssue();
        treeProvider.resetAllStages();

        // Update status bar
        statusBar.showIdle();

        logger.info("Pipeline reset complete", { issueNumber });
        vscode.window.showInformationMessage("Pipeline reset. Ready for next issue.");
      } catch (error) {
        logger.error("Failed to reset pipeline", { error });
        vscode.window.showErrorMessage("Failed to reset pipeline");
      }
    };

    // Connect context watcher events to pipeline tree provider and PipelineStateService
    contextWatcher.onIssuePickedUp(async (issueInfo) => {
      logger.info("Issue picked up via context file", {
        issueNumber: issueInfo.number,
        title: issueInfo.title,
        baseBranch: issueInfo.baseBranch,
      });

      // Initialize PipelineStateService for this issue
      if (pipelineStateService) {
        try {
          await pipelineStateService.initializePipeline(
            issueInfo.number,
            issueInfo.title,
            issueInfo.branch,
            issueInfo.baseBranch ?? "main"
          );
          // pipeline-start bookend is completed in pickupIssue.ts before
          // issue-pickup starts. No need to repeat it here.

          // Mark issue-pickup as complete since context file exists
          await pipelineStateService.completeStage("issue-pickup");

          // Set execution mode based on config.yaml auto_accept_stages config
          // If true, pipeline will auto-progress without showing notifications
          const configuredMode = getInitialExecutionMode(incrediRoot ?? undefined);
          await pipelineStateService.setExecutionMode(configuredMode);
          logger.debug("Pipeline execution mode set from config", {
            executionMode: configuredMode,
          });
        } catch (error) {
          logger.warn("Failed to initialize pipeline state", { error });
        }
      }

      // Update status bar with target branch
      const baseBranch = issueInfo.baseBranch ?? "main";
      statusBar.setTargetBranch(baseBranch);

      treeProvider.setIssue(issueInfo);
      treeProvider.updateStageStatus("issue-pickup", "complete");

      // ONLY auto-continue if extension initiated this run
      // Chat-initiated runs should stop here (Issue #81)
      if (activeExtensionExecutions.has("issue-pickup")) {
        autoContinueToNextStage("issue-pickup", issueInfo.number);
      } else {
        logger.info("Chat-initiated issue pickup - not auto-continuing", {
          issueNumber: issueInfo.number,
        });
      }
    });

    contextWatcher.onIssueCleared(async (issueNumber) => {
      logger.info("Issue context cleared - pipeline complete", { issueNumber });

      // Clean up pipeline-complete tracking set (Issue #2545)
      pipelineCompleteIssues.delete(issueNumber);

      // DO NOT clear pipeline state here - let summary panel handle cleanup
      // The state must be preserved until user clicks "Reset & Start New"
      // See Issue #113 for details on this race condition fix

      // Mark pr-merge as complete when context files are cleaned up
      // This is the signal that pr-merge has finished successfully
      treeProvider.updateStageStatus("pr-merge", "complete");
      // Update OutputWindow status chip for pr-merge completion (Issue #284)
      outputWindow.updateStageStatus("pr-merge", "complete");

      // Issue #649's outcome-record writer used to append a second, separate
      // "outcome" record here on pr-merge cleanup, keyed off bootstrap-level
      // shared state (`incrediRoot`/`telemetryStore`) rather than the
      // completing run's own identity. In a multi-repo/multi-run workspace
      // that shared state can point at a DIFFERENT repo's active slot than
      // the run that just finished, so the record landed in the wrong
      // repo's history with no `repo`/`run_id` to identify it — invisible to
      // every identity/idempotency guard (#307, #313, #316) because those
      // guards key off fields this writer never populated. Deleted outright
      // (Issue #319): the Go authoritative writer (internal/state/history.go)
      // already records the merged outcome on its own "run" record for this
      // pipeline, keyed by the completing run's real identity, so this
      // second writer added no information — only a well-documented way to
      // corrupt a sibling repo's history file.

      // Note: Status bar, tree clearing, and state cleanup are now handled by
      // resetPipeline command when user clicks "Reset & Start New" in summary panel
    });

    contextWatcher.onStageComplete(async ({ issueNumber, stage }) => {
      logger.debug("Stage complete detected from context file", {
        issueNumber,
        stage,
      });

      // Update PipelineStateService
      if (pipelineStateService && stage !== "issue-pickup") {
        try {
          await pipelineStateService.completeStage(stage);
        } catch (error) {
          logger.warn("Failed to update pipeline state on stage complete", {
            stage,
            error,
          });
        }
      }

      treeProvider.updateStageStatus(stage, "complete");

      // ONLY auto-continue if extension initiated this run
      // Chat-initiated runs should NOT auto-continue (Issue #81)
      // Also skip issue-pickup as it's handled in onIssuePickedUp
      if (stage !== "issue-pickup") {
        if (activeExtensionExecutions.has(stage)) {
          // Extension-initiated: auto-continue is handled by runStageWithOutput.onComplete
          // Do NOT call autoContinueToNextStage here to avoid double-trigger
          logger.debug("Extension-initiated stage complete - skipping duplicate auto-continue", {
            stage,
            issueNumber,
          });
        } else {
          // Stage not in activeExtensionExecutions — either chat-initiated,
          // or extension-initiated but onComplete already cleared the tracking.
          // Either way, do NOT auto-continue here (onComplete handles it for extension runs).
          logger.debug("Context watcher stage complete - auto-continue handled elsewhere", {
            stage,
            issueNumber,
          });
        }
      }
    });

    // All pipelines now run in worktrees with isolated context files (#1831).
    // Suspend the main-repo context watcher to prevent stale context files
    // from corrupting the singleton state service and showing ghost entries
    // in the pipeline tree view (Issue #1540).
    contextWatcher.suspend();
    contextWatcher.cleanStaleContextFiles().catch((error) => {
      logger.warn("Failed to clean stale context files", { error });
    });

    context.subscriptions.push(contextWatcher);
  }

  // ── 14. RefreshTriggerService ─────────────────────────────────────────

  // Initialize RefreshTriggerService for CLI-triggered refresh (Issue #308)
  // Watches .nightgauge/.refresh-trigger file to auto-refresh tree views when
  // CLI tools create/update issues
  if (incrediRoot) {
    const refreshTriggerService = new RefreshTriggerService(incrediRoot, logger);

    // Register all tree providers to refresh on trigger
    refreshTriggerService.registerTreeProvider(treeProvider);
    for (const [tabId, provider] of projectBoardProviders) {
      refreshTriggerService.registerTreeProvider(provider);
    }

    context.subscriptions.push(refreshTriggerService);
    logger.info("RefreshTriggerService initialized");
  }

  // ── Platform connection status bar item (Issue #1461) ────────────────

  const platformConfig = ConfigBridge.getInstance().getPlatform();
  const platformEnabled = platformConfig?.enabled ?? false;

  let oauthDeviceFlowService: OAuthDeviceFlowService | null = null;
  let gitHubAuthService: GitHubAuthService | null = null;

  // IPC client for platform services — replaces direct HTTP calls (#2090)
  const ipcClient = IpcClient.getInstance();

  // Register pipeline services in container (Issue #2772)
  container.register("ipcClient", ipcClient);

  // Telemetry consent service is constructed early so the submission singleton
  // can read the consent gate from its first event (#3327).
  const telemetryConsentService = new TelemetryConsentService(context, logger);
  container.register("telemetryConsentService", telemetryConsentService);
  // Show the first-run modal on activation (per-workspace bookkeeping). No
  // session/tier dependency — the prompt is shown to every user regardless
  // of subscription state.
  void telemetryConsentService.maybeShowFirstRunPrompt();

  // Initialize TelemetryUploaderService — ships local JSONL history to the
  // platform's POST /v1/telemetry/pipeline-run endpoint (#3315).
  let telemetryUploaderService: TelemetryUploaderService | null = null;
  if (incrediRoot) {
    telemetryUploaderService = new TelemetryUploaderService(
      () => cachedLicenseKey ?? null,
      telemetryConsentService,
      getPlatformUrl,
      incrediRoot,
      logger,
      // JWT fallback so device-flow / community accounts (no license key) can
      // still upload telemetry — platform ingest accepts either credential.
      () => TokenStorage.getInstance()?.retrieve("accessToken") ?? Promise.resolve(null),
      // All workspace repo roots (primary + target repos), so the
      // pipeline-run/trace stream scans cover interactive runs executed
      // against a non-primary target repo in a multi-repo workspace (#247).
      // Reuses the existing WorkspaceManager resolution mechanism rather than
      // inventing a new one — evaluated lazily so it reflects repos added
      // after this service is constructed (e.g. workspace config reload).
      () => workspaceManager?.getAllRepositories().map((repo) => repo.path) ?? []
    );
    telemetryUploaderService.initialize();
    container.register("telemetryUploaderService", telemetryUploaderService);

    // Continuous trace upload during active runs (#234 / ADR 014). Drive the
    // uploader's active-run cadence from the Go pipeline lifecycle IPC events —
    // the single source of truth. A per-issue active set pairs the FIRST
    // stage.start of a run with its terminal pipeline.complete/error, so
    // activeRunCount is exact (and correct under concurrent slots) and can
    // never be double-counted. NOTE: the Go history-writer's own
    // pipeline.complete handler (above) still nudges onPipelineCompleted() as a
    // redundant, idempotent flush — that path deliberately does NOT touch
    // activeRunCount, so there is no double-decrement.
    const uploader = telemetryUploaderService;
    const activeRunIssues = new Set<number>();
    const uploaderLifecycleDisposers = [
      ipcClient.on("stage.start", (data: unknown) => {
        const issueNumber = (data as { issueNumber?: number }).issueNumber;
        if (typeof issueNumber === "number" && !activeRunIssues.has(issueNumber)) {
          activeRunIssues.add(issueNumber);
          uploader.onRunStarted();
        } else {
          uploader.onRunProgress();
        }
      }),
      ipcClient.on("stage.complete", () => {
        uploader.onRunProgress();
      }),
      ipcClient.on("pipeline.complete", (data: unknown) => {
        const issueNumber = (data as { issueNumber?: number }).issueNumber;
        if (typeof issueNumber === "number" && activeRunIssues.delete(issueNumber)) {
          uploader.onRunCompleted();
        }
      }),
      ipcClient.on("pipeline.error", (data: unknown) => {
        const issueNumber = (data as { issueNumber?: number }).issueNumber;
        if (typeof issueNumber === "number" && activeRunIssues.delete(issueNumber)) {
          uploader.onRunCompleted();
        }
      }),
    ];
    for (const disposer of uploaderLifecycleDisposers) {
      context.subscriptions.push({ dispose: () => disposer.dispose() });
    }
  }

  // Real-time pipeline telemetry (stage_started/completed/error → live
  // Pipelines view) is emitted by the Go IPC layer when PipelineStateService
  // reports stage transitions, and the terminal pipeline_done is emitted by
  // HeadlessOrchestrator.firePipelineComplete. This reuses the Go binary's
  // proven AnalyticsService emitter + license, so the extension no longer
  // re-implements the platform event contract in TypeScript. (#3556)

  // Initialize AgentHeartbeatService — sends PUT /v1/agents/{id}/heartbeat every 30s (#3545).
  // start(agentId) is called by the registration service once agentId is available (#3544).
  const agentHeartbeatTokenStorage = TokenStorage.getInstance();
  // Forward-declared so heartbeat/registration (built here) can route their 401
  // recovery through the single TokenRefreshManager (assigned below). A lazy
  // delegator avoids reordering this large bootstrap while still funneling every
  // refresh through one single-use-token dedup guard (#3751).
  let tokenRefreshManager: TokenRefreshManager | null = null;
  const onDemandTokenRefresher: IOnDemandTokenRefresher = {
    forceRefresh: () => tokenRefreshManager?.forceRefresh() ?? Promise.resolve(null),
  };
  let agentHeartbeatService: AgentHeartbeatService | null = null;
  if (agentHeartbeatTokenStorage) {
    agentHeartbeatService = new AgentHeartbeatService(
      getPlatformUrl,
      agentHeartbeatTokenStorage,
      logger,
      onDemandTokenRefresher
    );
    context.subscriptions.push(agentHeartbeatService);
  }

  let agentRegistrationService: AgentRegistrationService | null = null;
  if (agentHeartbeatTokenStorage) {
    agentRegistrationService = new AgentRegistrationService(
      getPlatformUrl,
      agentHeartbeatTokenStorage,
      logger,
      onDemandTokenRefresher
    );
    context.subscriptions.push(agentRegistrationService);
  }

  // Initialize AgentCommandStreamService — SSE subscription to GET /v1/agents/{id}/commands (#3550).
  // start(agentId) is called by the registration service once agentId is available (#3544).
  const agentCommandStreamTokenStorage = TokenStorage.getInstance();
  let agentCommandStreamService: AgentCommandStreamService | null = null;
  if (agentCommandStreamTokenStorage && concurrentPipelineManager && issueQueueService) {
    const triggerCommandHandler = new TriggerCommandHandler(
      ipcClient,
      concurrentPipelineManager,
      issueQueueService,
      logger,
      workspaceManager ?? undefined
    );
    const cancelCommandHandler = new CancelCommandHandler(concurrentPipelineManager, logger);
    const approveCommandHandler = new ApproveCommandHandler(concurrentPipelineManager, logger);
    const rejectCommandHandler = new RejectCommandHandler(concurrentPipelineManager, logger);
    // CompositeCommandHandler fans out each received command to all registered
    // handlers. AgentCommandStreamService accepts a single CommandHandler, so
    // this thin composite lets all command types coexist without changing the
    // service's interface. (#3552, #3553)
    const compositeCommandHandler: CommandHandler = {
      handle(cmd) {
        triggerCommandHandler.handle(cmd);
        cancelCommandHandler.handle(cmd);
        approveCommandHandler.handle(cmd);
        rejectCommandHandler.handle(cmd);
      },
      // Fan the agentId out to handlers that ack commands. Only the trigger
      // handler acks today (POST /v1/agents/{agentId}/commands/{id}/ack, #3551);
      // others act on local slots. AgentCommandStreamService.start() invokes
      // this so the agentId is in place before the first command arrives.
      setAgentId(agentId) {
        triggerCommandHandler.setAgentId(agentId);
      },
    };
    agentCommandStreamService = new AgentCommandStreamService(
      getPlatformUrl,
      agentCommandStreamTokenStorage,
      context,
      logger,
      compositeCommandHandler
    );
    context.subscriptions.push(agentCommandStreamService);
    // start(agentId) is invoked from extension.ts alongside the heartbeat once
    // registration yields an agentId; start() also calls
    // compositeCommandHandler.setAgentId(agentId) so trigger acks work (#3544).
  }

  if (platformEnabled) {
    // Initialize TelemetryService submission singleton (#1480, #3327).
    TelemetryService.initialize(ipcClient, configBridge, telemetryConsentService, logger);

    // Initialize OAuth Device Flow service (Issue #1464 — via IPC, #2090)
    oauthDeviceFlowService = new OAuthDeviceFlowService(ipcClient, logger);
    context.subscriptions.push(oauthDeviceFlowService);

    // Initialize GitHub Auth service (Issue #1467 — via IPC, #2090)
    gitHubAuthService = new GitHubAuthService(ipcClient, logger);
    context.subscriptions.push(gitHubAuthService);

    // Register in DI container (Issue #2771)
    container.register("gitHubAuthService", gitHubAuthService);
  }

  // Initialize Token Refresh Manager (Issue #1466). Declared earlier so agent
  // heartbeat/registration can delegate their refresh to it (#3751).
  const tokenStorage = TokenStorage.getInstance();
  if (platformEnabled && tokenStorage && offlineManager) {
    tokenRefreshManager = new TokenRefreshManager(
      tokenStorage,
      ipcClient,
      offlineManager,
      () => oauthDeviceFlowService?.signOut() ?? Promise.resolve(),
      logger,
      () => resolvePlatformHostKey(ConfigBridge.getInstance().getPlatform()),
      configBridge
    );
    void tokenRefreshManager.start();
    context.subscriptions.push(tokenRefreshManager);
  }

  // Initialize Session Manager (Issue #1468)
  let sessionManager: SessionManager | null = null;
  if (
    platformEnabled &&
    tokenStorage &&
    tokenRefreshManager &&
    oauthDeviceFlowService &&
    gitHubAuthService
  ) {
    sessionManager = new SessionManager(
      tokenStorage,
      tokenRefreshManager,
      oauthDeviceFlowService,
      gitHubAuthService,
      logger,
      configBridge
    );
    void sessionManager.restore(); // fire-and-forget session restoration
    context.subscriptions.push(sessionManager);
  }

  // Initialize MachineFingerprint (Issue #1471)
  const machineFingerprint = platformEnabled ? MachineFingerprint.initialize() : null;

  // Initialize LicensePreflight (Issue #1470 — via IPC, #2090)
  let licensePreflight: LicensePreflight | null = null;
  if (platformEnabled && machineFingerprint) {
    licensePreflight = new LicensePreflight(
      ipcClient,
      machineFingerprint,
      () => cachedLicenseKey,
      () => ConfigBridge.getInstance().getPlatform()?.tier_override
    );
  }

  // Initialize TierGate (Issue #1472)
  // TierGate is stateless — no dependencies needed beyond instantiation.
  const tierGate = new TierGate();

  // Initialize PipelineBridge (Issue #1470)
  // Wires Go pipeline orchestration events (pipeline.runStage, pipeline.abort,
  // pipeline.validateLicense) to TypeScript services.
  // OutputWindow, statusBar, and treeProvider are passed so automated-mode
  // stage output is visible in the Output view (same as interactive mode).
  const pipelineBridge = new PipelineBridge(
    ipcClient,
    logger,
    pipelineStateService ?? null,
    licensePreflight,
    offlineManager,
    outputWindow,
    statusBar,
    treeProvider
  );
  context.subscriptions.push(pipelineBridge);
  // Register pipeline services in container (Issue #2772)
  container.register("pipelineBridge", pipelineBridge);

  // Adapt OfflineManager → ConnectionStateEmitter for PlatformStatusBarItem
  const connectionStateEmitter = offlineManager
    ? (() => {
        const _onConnectionStateChanged = new vscode.EventEmitter<
          "connected" | "disconnected" | "degraded"
        >();
        const _onRateLimitRetry = new vscode.EventEmitter<{
          retryInSeconds: number;
          attempt: number;
        }>();
        const mapState = (
          s: "online" | "offline" | "degraded"
        ): "connected" | "disconnected" | "degraded" =>
          s === "online" ? "connected" : s === "offline" ? "disconnected" : "degraded";
        offlineManager.onStateChanged((evt) => {
          _onConnectionStateChanged.fire(mapState(evt.current));
        });
        context.subscriptions.push(_onConnectionStateChanged, _onRateLimitRetry);
        return {
          getConnectionState: () => mapState(offlineManager.state),
          onConnectionStateChanged: _onConnectionStateChanged.event,
          onRateLimitRetry: _onRateLimitRetry.event,
        };
      })()
    : null;

  const platformStatusBarItem = new PlatformStatusBarItem(
    connectionStateEmitter,
    platformConfig,
    sessionManager,
    new TrialStateStore(context.globalState)
  );
  context.subscriptions.push(platformStatusBarItem);
  context.subscriptions.push(
    registerShowPlatformStatusCommand(platformStatusBarItem, licensePreflight)
  );
  // Machine-binding detail command (#4156) — surfaces machineBound/machineCount
  // from the same LicensePreflight result. Entry points: sidebar Subscription
  // section row and the command palette.
  context.subscriptions.push(registerShowMachineBindingCommand(licensePreflight));

  // Pipeline-aware connectivity badge (Issue #3203). Shown only when a
  // pipeline stage is running and ConnectivityStateBus reports degraded or
  // offline. Click → quick pick for cancel-vs-wait.
  const pipelineConnectivityStatusItem = new PipelineConnectivityStatusItem();
  context.subscriptions.push(pipelineConnectivityStatusItem);

  // Initialize remote command status indicator (Issue #2170)
  const remoteStatusBarItem = new RemoteCommandStatusBarItem();
  context.subscriptions.push(remoteStatusBarItem);
  if (ipcClient) {
    const remoteCommandStatusService = new RemoteCommandStatusService(
      ipcClient,
      remoteStatusBarItem,
      configBridge
    );
    remoteCommandStatusService.start();
    context.subscriptions.push(remoteCommandStatusService);
  }

  // Initialize platform quota service for tier quota display (Issue #1479, #2091 IPC migration)
  let platformQuotaService: PlatformQuotaService | null = null;
  if (platformEnabled && ipcClient) {
    platformQuotaService = new PlatformQuotaService(ipcClient, notificationService);
    dashboard.registerPlatformQuotaService(platformQuotaService);
    context.subscriptions.push(platformQuotaService);
  }

  // Connect SessionManager to PipelineTreeProvider for tier tracking (Issue #1469)
  if (sessionManager) {
    treeProvider.setSessionManager(sessionManager);

    // Connect team member list for Team+ tier users (Issue #1482, #2091 IPC migration)
    if (ipcClient) {
      treeProvider.setTeamMembers(sessionManager, ipcClient);
    }

    // Connect subscription status display (#4156 — re-wires
    // SubscriptionSectionTreeItem into the actual sidebar tree provider).
    if (licensePreflight) {
      treeProvider.setLicensePreflight(sessionManager, licensePreflight);
    }

    // Set VSCode context key for when-clause filtering (Issue #1472)
    sessionManager.onSessionChanged((event) => {
      const tier = event.data.userTier ?? "community";
      void vscode.commands.executeCommand("setContext", "nightgauge.userTier", tier);
    });

    // Set role context key for when-clause driven command visibility (#1483)
    sessionManager.onSessionChanged((event) => {
      const role = event.data.userRole ?? "owner"; // default to owner for non-team
      void vscode.commands.executeCommand("setContext", "nightgauge.userRole", role);
    });
  }

  // Set initial tier context key (Issue #1472)
  void vscode.commands.executeCommand("setContext", "nightgauge.userTier", "community");

  // Set initial role context key (#1483) — defaults to owner so non-team users have full access
  void vscode.commands.executeCommand("setContext", "nightgauge.userRole", "owner");

  // ── Cloud master switch (free-local product; cloud off by default) ────
  // `nightgauge.cloud.enabled` (default false) is the single reversible
  // switch that reveals the cloud/account command surface and the
  // subscription/team sidebar sections. Mirror it into the
  // `nightgauge.cloudEnabled` context key for when-clause driven
  // command-palette visibility, and refresh both the key and the tree
  // provider whenever the setting changes so flipping it takes effect live.
  const applyCloudEnabled = (): void => {
    // VSCode-only extension setting (not a 6-tier config.yaml value), so read
    // the `nightgauge.cloud` section directly — see the config regression
    // guard's ALLOWED_CONFIG_SECTIONS.
    const cloudEnabled = vscode.workspace
      .getConfiguration("nightgauge.cloud")
      .get<boolean>("enabled", false);
    void vscode.commands.executeCommand("setContext", "nightgauge.cloudEnabled", cloudEnabled);
    treeProvider.setCloudEnabled(cloudEnabled);
  };
  applyCloudEnabled();
  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("nightgauge.cloud.enabled")) {
        applyCloudEnabled();
      }
    })
  );

  // Connect TierGate to PipelineTreeProvider for lock icon rendering (Issue #1472)
  treeProvider.setTierGate(tierGate);

  // TelemetryService submission instance — consent UX moved to
  // TelemetryConsentService (#3327). The submission singleton is initialized
  // earlier in this function (when platformEnabled). The consent service is
  // also exposed via the DI container for command registration.
  const telemetryService = TelemetryService.getInstance();
  if (telemetryService) {
    container.register("telemetryService", telemetryService);
  }

  logger.info("Platform status bar item initialized", {
    enabled: platformEnabled,
    apiUrl: platformConfig?.api_url,
  });

  // ── Repository Settings Service (Issue #2720) ─────────────────────────
  // Detects and warns when allow_auto_merge is enabled on the active repo.
  // Runs async on startup — never blocks extension activation.

  let repositorySettingsService: RepositorySettingsService | null = null;
  if (workspaceRootForNightgauge) {
    repositorySettingsService = new RepositorySettingsService(logger, workspaceRootForNightgauge);
    container.register("repositorySettingsService", repositorySettingsService);

    // Subscribe to auto-merge detection events and show a warning notification
    repositorySettingsService.onAutoMergeDetected(({ owner, repo }) => {
      void vscode.window
        .showWarningMessage(
          `Auto-merge is enabled on ${owner}/${repo}. ` +
            "This bypasses the pipeline's pr-merge stage and recovery mechanisms.",
          "Disable Auto-Merge",
          "Learn More"
        )
        .then((choice) => {
          if (choice === "Disable Auto-Merge") {
            void vscode.commands.executeCommand("nightgauge.fixAutoMergeSetting");
          } else if (choice === "Learn More") {
            void vscode.env.openExternal(
              vscode.Uri.parse(
                "https://github.com/nightgauge/nightgauge/blob/main/docs/GIT_WORKFLOW.md#auto-merge-and-pipeline-control"
              )
            );
          }
        });
    });

    // Run detection in background — fail-safe, never blocks activation
    const detectOwnerRepo = (): { owner: string; repo: string } | null => {
      try {
        const { execSync: exec } = require("child_process") as typeof import("child_process");
        const remoteUrl = exec("git remote get-url origin", {
          cwd: workspaceRootForNightgauge,
          encoding: "utf8",
          stdio: ["pipe", "pipe", "pipe"],
        }).trim();
        const match =
          remoteUrl.match(/github\.com[:/]([^/]+)\/([^/.]+)/) ??
          remoteUrl.match(/github\.com\/([^/]+)\/([^/.]+)/);
        return match ? { owner: match[1], repo: match[2] } : null;
      } catch {
        return null;
      }
    };

    void (async () => {
      const repoInfo = detectOwnerRepo();
      if (repoInfo) {
        await repositorySettingsService!.detectAutoMerge(repoInfo.owner, repoInfo.repo);
      }
    })();
  }

  // ── Prompt Template Service (Issue #9) ────────────────────────────────

  const promptTemplateService = new PromptTemplateService(context.extensionPath);
  // Initialize in background — templates are optional; no await to avoid blocking activation
  promptTemplateService.initialize().catch((err: unknown) => {
    logger.warn("PromptTemplateService: failed to load templates", {
      error: err instanceof Error ? err.message : String(err),
    });
  });

  // ── Return all services ───────────────────────────────────────────────

  return {
    container,
    logger,
    statusBar,
    headlessOrchestrator,
    treeProvider,
    projectBoardProviders,
    projectBoardService,
    contextViewer,
    dashboard,
    outputWindow,
    pluginSetupService,
    codexSetupService,
    notificationService,
    pipelineStateService,
    issueQueueService,
    completedIssuesService,
    workspaceManager,
    repositoriesTreeProvider,
    sequentialRepoConfigService,
    enabledReposConfigService,
    runtimeStateStore: runtimeStateStoreInstance,
    concurrentPipelineManager,
    queryService,
    savedQueriesService,
    queryResultsProvider,
    slotOutputManager,
    brownfieldDashboard,
    knowledgeValueDashboard,
    usageLimitsService,
    platformQuotaService,
    discordService,
    notifier,
    telemetryStore,
    telemetryService,
    telemetryConsentService,
    telemetryUploaderService,
    agentHeartbeatService,
    agentCommandStreamService,
    agentRegistrationService,
    offlineManager,
    tokenStorage: TokenStorage.getInstance(),
    oauthDeviceFlowService,
    gitHubAuthService,
    tokenRefreshManager,
    sessionManager,
    machineFingerprint,
    tierGate,
    licensePreflight,
    skillContextAssembler: skillContextAssemblerService,
    incrediRoot,
    projectBoardViews,
    treeView,
    platformStatusBarItem,
    promptTemplateService,
    automationService,
    repositorySettingsService,
    workflowTreeProvider,
    attentionTreeProvider,
    attentionTreeView,
  };
}
