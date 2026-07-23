/**
 * SettingsPanel - WebView panel manager for .nightgauge/config.yaml settings
 *
 * Provides a visual interface for configuring Nightgauge pipeline settings.
 * Follows singleton pattern with reveal-on-show behavior.
 * Supports multi-tier configuration with source visibility.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 * @see Issue #440 - Multi-tier config GUI support
 */

import * as vscode from "vscode";
import type { IncrediConfig, ViewTier, EditableTier, TierViewState } from "./types";
import { PIPELINE_LOCKED_SECTIONS } from "./types";
import { IncrediYamlService, setConfigValue, getConfigValue } from "./IncrediYamlService";
import { getSettingsHtml, STAGE_ADAPTER_STAGES, type StageAdapterPreviewRow } from "./SettingsHtml";
import { resolveStageAdapter } from "../../utils/resolvers/adapterResolver";
import { getModeStageAdapterModel } from "../../utils/modeProfiles";
import { getPerformanceMode } from "../../utils/resolvers/monitoringResolver";
import { toIncrediAdapter } from "../../services/HeadlessOrchestrator";
import { validateAdapterAuth } from "@nightgauge/sdk";
import type { PipelineStage } from "@nightgauge/sdk";
import type { ExecutionAdapter } from "../../utils/resolvers/modelResolver";
import {
  SettingsMessageHandler,
  createErrorMessage,
  createTierChangedMessage,
  createTieredSavedMessage,
} from "./SettingsMessageHandler";
import type { PipelineStateService } from "../../services/PipelineStateService";
import type { ConfigMergeResult, TierMetadata } from "../../config/configMergeEngine";
import type { ConfigSourceMap } from "../../config/schema";
import type { RuntimeStateStore } from "../../config/RuntimeStateStore";
import { LmStudioService } from "../../services/LmStudioService";
import { CodexModelCatalogService } from "../../services/CodexModelCatalogService";
import { Logger } from "../../utils/logger";
import type { LmStudioModelInfo } from "../../services/LmStudioService";
import { SecretStorageService, SECRET_KEYS } from "../../services/SecretStorageService";
import { IpcClient } from "../../services/IpcClient";
import type { ForgeInstanceRow } from "./ForgeInstancesSection";
import type { ForgeListEntry, TierAuditEntry } from "../../services/IpcClientBase";

/**
 * Dotted-path config keys that are owned by the runtime tier (Phase 3 of
 * #3313 / #3336). When `handleSave` encounters one of these in the working
 * config it strips the key from the YAML write and routes the value to
 * `RuntimeStateStore` instead. Future tier-3 keys that surface in this
 * panel are added here.
 */
const TIER_3_KEY_PATHS = new Set<string>(["pipeline.max_concurrent"]);

/**
 * Dotted-path config keys that are credentials mirrored to VSCode SecretStorage
 * (OS keychain) on save. `platform.license_key` is ALSO a machine-tier key
 * (see `MACHINE_TIER_KEY_PATHS`) so it persists to
 * `~/.nightgauge/config.yaml` too; the SecretStorage mirror keeps the
 * SecretStorage-first runtime readers (LicensePreflight, forwardPlatformEnv)
 * in sync (#3997).
 */
const SECRET_KEY_PATHS = new Set<string>(["platform.license_key"]);

/**
 * Dotted-path config keys that are personal/machine-specific — routed to
 * `~/.nightgauge/config.yaml` (machine tier) on save and stripped from
 * the project-tier YAML write. Keys here must never appear in a committed file.
 *
 * @see Issue #3337 — Phase 4: Promote Machine Tier to First-Class
 * @see Issue #3997 — license key persisted to the machine tier (not committed)
 */
export const MACHINE_TIER_KEY_PATHS = new Set<string>([
  "ui.core.adapter",
  "ui.core.default_model",
  "ui.core.fallback_model",
  "ui.core.auth_provider",
  "ui.notifications.discord.webhook_env",
  "ui.notifications.mattermost.webhook_env",
  "platform.license_key",
]);

/**
 * SettingsPanel - WebView panel for Nightgauge configuration
 *
 * @example
 * ```typescript
 * const panel = new SettingsPanel(context.extensionUri, workspaceRoot);
 *
 * // Show the settings panel
 * panel.show();
 *
 * // Connect to PipelineStateService for read-only during pipeline
 * panel.setStateService(pipelineStateService);
 * ```
 */
export class SettingsPanel implements vscode.Disposable {
  private static currentPanel: SettingsPanel | undefined;
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private yamlService: IncrediYamlService;
  private messageHandler: SettingsMessageHandler;
  private currentConfig: IncrediConfig = {};
  private stateService: PipelineStateService | null = null;
  private lockedSections: Set<string> = new Set();
  private readonly lmStudioLogger = new Logger("Nightgauge LM Studio");
  private readonly lmStudioService = new LmStudioService(this.lmStudioLogger);
  private readonly codexModelCatalogService = new CodexModelCatalogService();
  private lmStudioModels: LmStudioModelInfo[] = [];
  private codexModels: string[] = [];
  private forgeInstances: ForgeInstanceRow[] = [];

  // Tier drift audit state (Issue #3645)
  private tierAuditEntries: TierAuditEntry[] = [];
  private driftBannerDismissed = false;

  // Multi-tier state (Issue #440)
  private mergeResult: ConfigMergeResult | null = null;
  // Default edit tier is LOCAL (gitignored config.local.yaml): with the
  // project-over-machine precedence chain, local is the highest file tier, so
  // an edit from the merged view always takes effect AND never dirties the
  // working tree. Team policy edits go through the explicit Project tab /
  // "Edit team config" affordance, producing a reviewable commit.
  private tierState: TierViewState = {
    currentTier: "merged",
    defaultEditTier: "local",
    hasGlobalConfig: false,
    hasLocalConfig: false,
    hasProjectConfig: false,
    activeEnvVars: [],
  };
  private projectConfig: IncrediConfig = {};
  private localConfig: IncrediConfig = {};
  // Machine-tier working config (~/.nightgauge/config.yaml). Edited when
  // the Global tab is active so machine-tier keys (e.g. the license key) can be
  // saved through the UI (#3997).
  private globalConfig: IncrediConfig = {};
  private hasUnsavedChanges = false;
  private externalReloadPrompt: Promise<void> | null = null;

  // Callback fired after a runtime-tier write changes pipeline.max_concurrent
  // (Phase 3 of #3313 / #3336). The bootstrap wires this to push the new
  // value to ConcurrentPipelineManager + IPC autonomous scheduler so the
  // change applies live without restarting autonomous mode. Triggered from
  // RuntimeStateStore.onDidChange instead of post-YAML-save reload — see
  // `subscribeToRuntimeChanges` for the wiring.
  private onMaxConcurrentChanged: ((value: number) => void | Promise<void>) | null = null;
  private lastSavedMaxConcurrent: number | undefined;

  /**
   * Phase 3 of #3313 (#3336) — runtime-tier store for tier-3 keys
   * (currently `pipeline.max_concurrent`; future tier-3 keys are added to
   * `TIER_3_KEY_PATHS` in `handleSave`). Optional for backwards
   * compatibility with the singleton `getInstance()` factory; the bootstrap
   * passes a real store via `setRuntimeStateStore()`.
   */
  private runtimeStateStore: RuntimeStateStore | null = null;

  constructor(
    private readonly extensionUri: vscode.Uri,
    private readonly workspaceRoot: string
  ) {
    this.yamlService = new IncrediYamlService(workspaceRoot);
    this.disposables.push(this.lmStudioLogger);

    // Set up message handler with tier-aware callbacks
    this.messageHandler = new SettingsMessageHandler({
      onChange: this.handleChange.bind(this),
      onListAdd: this.handleListAdd.bind(this),
      onListRemove: this.handleListRemove.bind(this),
      onSave: this.handleSave.bind(this),
      onReset: this.handleReset.bind(this),
      onResetSetting: this.handleResetSetting.bind(this),
      onSwitchTier: this.handleSwitchTier.bind(this),
      onOpenTierFile: this.handleOpenTierFile.bind(this),
      onOpenDoc: this.handleOpenDoc.bind(this),
      onAction: this.handleAction.bind(this),
      onForgeAdd: this.handleForgeAdd.bind(this),
      onForgeAction: this.handleForgeAction.bind(this),
      onDismissDriftBanner: this.handleDismissDriftBanner.bind(this),
      onShowDriftedKeysOnly: this.handleShowDriftedKeysOnly.bind(this),
      onMoveTierKey: this.handleMoveTierKey.bind(this),
    });

    // Subscribe to file changes - reload all tiers
    const fileChangeDisposable = this.yamlService.onDidChange(() => {
      void this.handleExternalConfigChange();
    });
    this.disposables.push(fileChangeDisposable);
    this.disposables.push(this.yamlService);
  }

  /**
   * Get or create the singleton panel instance
   */
  static getInstance(extensionUri: vscode.Uri, workspaceRoot: string): SettingsPanel {
    if (!SettingsPanel.currentPanel) {
      SettingsPanel.currentPanel = new SettingsPanel(extensionUri, workspaceRoot);
    }
    return SettingsPanel.currentPanel;
  }

  /**
   * Wire a callback that applies pipeline.max_concurrent live (no restart).
   * Called whenever the runtime tier value for that key changes — fired
   * either from the panel's own save handler (Phase 3 of #3313 / #3336)
   * or from any other writer (e.g. an IPC tool).
   */
  setOnMaxConcurrentChanged(cb: (value: number) => void | Promise<void>): void {
    this.onMaxConcurrentChanged = cb;
  }

  /**
   * Wire the runtime tier store for tier-3 key writes. Called from the
   * extension bootstrap (Phase 3 of #3313 / #3336). Subscribes to
   * `onDidChange` so external writes to `pipeline.max_concurrent` (e.g. an
   * IPC tool) also fire the live-apply callback.
   */
  setRuntimeStateStore(store: RuntimeStateStore): void {
    this.runtimeStateStore = store;
    const subscription = store.onDidChange((evt) => {
      // Only react to global-scoped tier-3 keys we routed through here.
      if (evt.path === "pipeline.max_concurrent" && evt.scope === "global") {
        void this.maybeApplyMaxConcurrentLive();
      }
    });
    this.disposables.push(subscription);
  }

  /**
   * Connect to PipelineStateService for read-only mode during pipeline
   */
  setStateService(stateService: PipelineStateService): void {
    this.stateService = stateService;

    // Subscribe to pipeline running state
    const disposable = stateService.onStateChanged((state) => {
      // Check if any stage is running
      const isRunning = state
        ? Object.values(state.stages).some((s) => s.status === "running")
        : false;
      const newLocked = isRunning ? new Set(PIPELINE_LOCKED_SECTIONS) : new Set<string>();
      if (!this.setsEqual(this.lockedSections, newLocked)) {
        this.lockedSections = newLocked;
        this.updatePanel();
      }
    });
    this.disposables.push(disposable);
  }

  /**
   * Show the settings panel
   */
  async show(): Promise<void> {
    // If we already have a panel, reveal it
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One);
      return;
    }

    // Load all config tiers
    await this.loadAllTiers();

    // Check if project config exists - prompt to create if not
    if (!this.tierState.hasProjectConfig) {
      const create = await vscode.window.showInformationMessage(
        "No .nightgauge/config.yaml found. Create one with default settings?",
        "Create",
        "Cancel"
      );

      if (create === "Create") {
        // Prompt for project number
        const projectNumber = await vscode.window.showInputBox({
          prompt: "Enter your GitHub Project number",
          placeHolder: "123",
          validateInput: (value) => {
            if (!value) return "Project number is required";
            const num = parseInt(value, 10);
            if (isNaN(num) || num <= 0) return "Must be a positive number";
            return null;
          },
        });

        if (projectNumber) {
          await this.yamlService.create(parseInt(projectNumber, 10));
          await this.loadAllTiers();
        } else {
          return; // User cancelled
        }
      } else {
        return; // User cancelled
      }
    }

    // Check if pipeline is running
    if (this.stateService) {
      const state = await this.stateService.getState();
      const isRunning = state
        ? Object.values(state.stages).some((s) => s.status === "running")
        : false;
      this.lockedSections = isRunning ? new Set(PIPELINE_LOCKED_SECTIONS) : new Set<string>();
    }

    // Create the WebView panel
    this.panel = vscode.window.createWebviewPanel(
      "incrediSettings",
      "Nightgauge Settings",
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "src", "views", "settings")],
      }
    );

    // Set initial content
    this.updatePanel();

    // Handle messages from the WebView
    this.panel.webview.onDidReceiveMessage(
      this.messageHandler.handleMessage,
      undefined,
      this.disposables
    );

    if (this.currentConfig.ui?.core?.adapter === "lm-studio") {
      void this.refreshLmStudioModels(true);
    }
    if (this.currentConfig.ui?.core?.adapter === "codex") {
      this.refreshCodexModels(true);
    }

    // Handle panel disposal
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
  }

  /**
   * Load all configuration tiers using the merge engine
   *
   * Updates internal state with merged config, source map, and tier metadata.
   */
  private async loadAllTiers(): Promise<void> {
    // Use the 6-tier merge engine
    this.mergeResult = await this.yamlService.readEffective();

    // Update tier state
    this.tierState = {
      currentTier: this.tierState.currentTier, // Preserve current view
      // Merged-view edits are personal overrides. Project/team writes require
      // explicitly selecting the Project tab.
      defaultEditTier: this.tierState.defaultEditTier,
      hasGlobalConfig: this.mergeResult.tiers.hasGlobal,
      hasLocalConfig: this.mergeResult.tiers.hasLocal,
      hasProjectConfig: this.mergeResult.tiers.hasProject,
      activeEnvVars: this.mergeResult.envVarsApplied,
    };

    // Update current effective config
    this.currentConfig = this.mergeResult.config;

    // Load individual tier configs for editing
    const [projectResult, localResult, globalResult] = await Promise.all([
      this.yamlService.read(),
      this.yamlService.readLocal(),
      this.yamlService.readGlobal(),
    ]);

    this.projectConfig = projectResult.config ?? {};
    this.localConfig = localResult.config ?? {};
    this.globalConfig = globalResult.config ?? {};

    // Load forge instances from IPC for the Forge Instances section.
    try {
      const ipc = IpcClient.getInstance();
      const forgeResult = await ipc.forgeList();
      const secretSvc = SecretStorageService.getInstance();
      this.forgeInstances = await Promise.all(
        (forgeResult.forges ?? []).map(async (f: ForgeListEntry) => {
          const lastTested = secretSvc ? await secretSvc.getForgeLastTested(f.id) : undefined;
          return {
            id: f.id,
            kind: f.kind,
            base_url: f.base_url,
            auth_method: f.auth_method,
            ca_bundle: f.ca_bundle,
            lastTested,
          };
        })
      );
    } catch {
      // IPC may not be ready yet — keep the previous forge list
    }

    // Load tier audit data for drift banner + per-key badges (Issue #3645).
    try {
      const ipc = IpcClient.getInstance();
      const auditResult = await ipc.configTierAudit();
      this.tierAuditEntries = auditResult.entries ?? [];
    } catch {
      // IPC not ready or workspace not configured — keep previous audit
    }

    // Resolve the license key for display. It is a machine-tier key persisted
    // to ~/.nightgauge/config.yaml (and mirrored to SecretStorage so the
    // SecretStorage-first runtime readers stay correct). Prefer the machine
    // YAML value; fall back to SecretStorage when the YAML has no value yet
    // (e.g. a key stored before this became a machine-tier key) (#3997).
    const machineKey = this.globalConfig.platform?.license_key;
    let displayKey: string | undefined = machineKey;
    const secretSvc = SecretStorageService.getInstance();
    if (!displayKey && secretSvc) {
      displayKey = await secretSvc.getSecret(SECRET_KEYS.platformLicenseKey);
    }
    if (displayKey) {
      // Surface the resolved value across the merged view and every working
      // config so the License Key field shows the current value on any tab.
      for (const cfg of [this.currentConfig, this.globalConfig, this.projectConfig]) {
        if (!cfg.platform) cfg.platform = {};
        cfg.platform.license_key = displayKey;
      }
    }
  }

  /**
   * Update the panel content
   */
  private updatePanel(): void {
    if (!this.panel) return;

    // Get config for current tier view
    const configForView = this.getConfigForTier(this.tierState.currentTier);

    const previewMode = this.safeGetPerformanceMode();
    const stageAdapterPreview = this.computeStageAdapterPreview(previewMode);

    this.panel.webview.html = getSettingsHtml(
      this.panel.webview,
      configForView,
      this.lockedSections,
      this.mergeResult?.sources ?? {},
      this.tierState,
      {
        codexModels: this.codexModels,
        lmStudioModels: this.lmStudioModels,
        stageAdapterPreview,
        performanceMode: previewMode,
        forgeInstances: this.forgeInstances,
        defaultForgeId: getConfigValue(this.currentConfig, "default_forge") as string | undefined,
        tierAuditEntries: this.tierAuditEntries,
        driftBannerDismissed: this.driftBannerDismissed,
      }
    );
  }

  private safeGetPerformanceMode(): string {
    try {
      return getPerformanceMode(this.workspaceRoot);
    } catch {
      return "elevated";
    }
  }

  /**
   * Compute the read-only `(adapter, model, source)` preview row for each
   * pipeline stage under the active performance mode. Errors in the
   * resolver fall through to a best-effort row so the UI never crashes.
   *
   * @see Issue #3225
   */
  private computeStageAdapterPreview(mode: string): StageAdapterPreviewRow[] {
    return STAGE_ADAPTER_STAGES.map((stage) => {
      try {
        const decision = resolveStageAdapter(stage as PipelineStage, this.workspaceRoot);
        const modeMode = mode as Parameters<typeof getModeStageAdapterModel>[0];
        const modeModel = getModeStageAdapterModel(
          modeMode,
          stage as PipelineStage,
          decision.adapter as ExecutionAdapter
        );
        return {
          stage,
          adapter: decision.adapter,
          source: decision.source,
          model: modeModel?.model ?? "(adapter default)",
          modelMismatch: modeModel?.mismatch ?? false,
        };
      } catch {
        return {
          stage,
          adapter: "claude",
          source: "default",
          model: "(adapter default)",
        };
      }
    });
  }

  /**
   * Get the config object for a specific tier view
   */
  private getConfigForTier(tier: ViewTier): IncrediConfig {
    switch (tier) {
      case "merged":
        return this.currentConfig;
      case "project":
        return this.projectConfig;
      case "local":
        return this.localConfig;
      case "global":
        // Machine tier (~/.nightgauge/config.yaml) — editable (#3997).
        return this.globalConfig;
      case "default":
      case "env":
        // These are read-only, return merged config for display
        return this.currentConfig;
      default:
        return this.currentConfig;
    }
  }

  /**
   * Get the working config object for a target tier
   */
  private getWorkingConfigForTier(tier: EditableTier): IncrediConfig {
    switch (tier) {
      case "local":
        return this.localConfig;
      case "global":
        return this.globalConfig;
      case "project":
      default:
        return this.projectConfig;
    }
  }

  /**
   * Handle setting value change
   */
  private handleChange(path: string, value: unknown, targetTier?: EditableTier): void {
    if (this.isSectionLocked(path)) {
      vscode.window.showWarningMessage(
        "This setting cannot be changed while a pipeline is running"
      );
      return;
    }

    // Determine target tier
    const tier = targetTier ?? this.getEditTargetTier();
    const config = this.getWorkingConfigForTier(tier);

    // Select inputs send strings; coerce known numeric select paths back to integers.
    const numericSelectPaths = ["pipeline.max_concurrent"];
    const coerced =
      numericSelectPaths.includes(path) && typeof value === "string" ? parseInt(value, 10) : value;

    // For pipeline.stage_adapters.<stage> and pipeline.stage_models.<stage>, an
    // empty string represents "(Use global default)" — delete the leaf rather
    // than persisting "". The resolver would ignore an empty value anyway, so
    // storing "" is pure clutter (Issue #3225, #4030).
    if (
      (path.startsWith("pipeline.stage_adapters.") || path.startsWith("pipeline.stage_models.")) &&
      (coerced === "" || coerced === undefined)
    ) {
      this.removeConfigValue(config, path);
      this.removeConfigValue(this.currentConfig, path);
      this.hasUnsavedChanges = true;
      return;
    }

    // platform.tier_override "" means "Auto" — delete rather than persisting
    // an invalid enum value.
    if (path === "platform.tier_override" && (coerced === "" || coerced === undefined)) {
      this.removeConfigValue(config, path);
      this.removeConfigValue(this.currentConfig, path);
      this.hasUnsavedChanges = true;
      return;
    }

    setConfigValue(config, path, coerced);
    setConfigValue(this.currentConfig, path, coerced);
    this.hasUnsavedChanges = true;
  }

  /**
   * Handle list item add
   */
  private handleListAdd(path: string, value: string, targetTier?: EditableTier): void {
    if (this.isSectionLocked(path)) {
      vscode.window.showWarningMessage(
        "This setting cannot be changed while a pipeline is running"
      );
      return;
    }

    const tier = targetTier ?? this.getEditTargetTier();
    const config = this.getWorkingConfigForTier(tier);

    const currentList = (getConfigValue(config, path) as string[]) ?? [];
    const nextList = [...currentList, value];
    setConfigValue(config, path, nextList);
    setConfigValue(this.currentConfig, path, nextList);
    this.hasUnsavedChanges = true;
    this.updatePanel();
  }

  /**
   * Handle list item remove
   */
  private handleListRemove(path: string, index: number, targetTier?: EditableTier): void {
    if (this.isSectionLocked(path)) {
      vscode.window.showWarningMessage(
        "This setting cannot be changed while a pipeline is running"
      );
      return;
    }

    const tier = targetTier ?? this.getEditTargetTier();
    const config = this.getWorkingConfigForTier(tier);

    const currentList = (getConfigValue(config, path) as string[]) ?? [];
    const newList = [...currentList];
    newList.splice(index, 1);
    setConfigValue(config, path, newList);
    setConfigValue(this.currentConfig, path, newList);
    this.hasUnsavedChanges = true;
    this.updatePanel();
  }

  /**
   * Handle save request.
   *
   * Phase 3 of #3313 (#3336): tier-3 keys (see `TIER_3_KEY_PATHS`) are
   * stripped from the YAML write and routed to `RuntimeStateStore`
   * instead, so saving the panel from a clean working tree leaves the tree
   * clean (no `.nightgauge/config.yaml` mutation for those keys).
   * The runtime overlay then shadows any pre-existing project-tier values.
   */
  private async handleSave(targetTier?: EditableTier): Promise<void> {
    const tier = targetTier ?? this.getEditTargetTier();
    const config = this.getWorkingConfigForTier(tier);

    // Partition tier-3 keys out of the YAML write. Capture the values so we
    // can route them to RuntimeStateStore after the YAML write succeeds.
    const tier3Captured = new Map<string, unknown>();
    for (const path of TIER_3_KEY_PATHS) {
      const value = getConfigValue(config, path);
      if (value !== undefined) {
        tier3Captured.set(path, value);
        this.removeConfigValue(config, path);
        // Mirror the strip in currentConfig so the merged view reflects it
        // until the next loadAllTiers() pulls the runtime overlay back in.
        this.removeConfigValue(this.currentConfig, path);
      }
    }

    // Capture secret values (e.g. the license key) BEFORE any stripping so we
    // can mirror them to SecretStorage. The mirror keeps the SecretStorage-first
    // runtime readers in sync; the values still persist to the machine YAML via
    // the machine-tier routing below (#3997). Captured even when empty so a
    // cleared key clears the keychain entry too.
    const secretsCaptured = new Map<string, string>();
    for (const secretPath of SECRET_KEY_PATHS) {
      const value = getConfigValue(config, secretPath);
      secretsCaptured.set(secretPath, value === undefined ? "" : String(value));
    }

    // When editing the Global tab, the working config IS the machine YAML and
    // is written directly via writeGlobal() — machine-tier keys belong in that
    // write and must NOT be partitioned out. Only the project/local writes
    // strip machine-tier keys and route them to ~/.nightgauge/config.yaml.
    const machineTierCaptured = new Map<string, unknown>();
    if (tier !== "global") {
      for (const machinePath of MACHINE_TIER_KEY_PATHS) {
        const value = getConfigValue(config, machinePath);
        if (value !== undefined) {
          machineTierCaptured.set(machinePath, value);
        }
        // Always strip machine-tier keys from the project/local write, even
        // when empty, so they never land in a committed file.
        this.removeConfigValue(config, machinePath);
        this.removeConfigValue(this.currentConfig, machinePath);
      }
    }

    let result;
    if (tier === "local") {
      result = await this.yamlService.writeLocal(config);
    } else if (tier === "global") {
      result = await this.yamlService.writeGlobal(config);
    } else {
      result = await this.yamlService.write(config, "project");
    }

    if (result.success) {
      this.hasUnsavedChanges = false;
      // Route tier-3 keys to the runtime store. Failures are logged but do
      // not fail the save — the YAML write already persisted everything else.
      if (tier3Captured.size > 0 && this.runtimeStateStore) {
        for (const [path, value] of tier3Captured) {
          try {
            await this.runtimeStateStore.set(path, value);
          } catch (err) {
            console.warn(`[SettingsPanel] runtime-tier write failed for ${path}:`, err);
          }
        }
      } else if (tier3Captured.size > 0) {
        console.warn(
          "[SettingsPanel] tier-3 keys present in save but RuntimeStateStore not wired:",
          Array.from(tier3Captured.keys())
        );
      }

      // Mirror secrets to SecretStorage (OS keychain) so the SecretStorage-first
      // runtime readers stay in sync with the machine YAML. An empty value
      // clears the keychain entry.
      const secretSvc = SecretStorageService.getInstance();
      if (secretSvc && secretsCaptured.size > 0) {
        const secretKeyMap: Record<string, string> = {
          "platform.license_key": SECRET_KEYS.platformLicenseKey,
        };
        for (const [path, value] of secretsCaptured) {
          const storageKey = secretKeyMap[path];
          if (storageKey) {
            try {
              if (value === "") {
                await secretSvc.deleteSecret(storageKey);
              } else {
                await secretSvc.setSecret(storageKey, value);
              }
            } catch (err) {
              console.warn(`[SettingsPanel] SecretStorage mirror failed for ${path}:`, err);
            }
          }
        }
      }

      // Route machine-tier keys to ~/.nightgauge/config.yaml. Failures
      // warn but do not fail the save — the YAML write already persisted
      // everything else (Phase 4 of #3337).
      if (machineTierCaptured.size > 0) {
        const machineConfig: Partial<import("./types").IncrediConfig> = {};
        for (const [dotPath, value] of machineTierCaptured) {
          setConfigValue(machineConfig as import("./types").IncrediConfig, dotPath, value);
        }
        const machineResult = await this.yamlService.writeGlobal(machineConfig);
        if (!machineResult.success) {
          vscode.window.showWarningMessage(
            `Saved project config, but failed to write machine-tier keys: ${machineResult.error}`
          );
        }
      }

      this.panel?.webview.postMessage(createTieredSavedMessage(tier));
      const machineTierNote =
        machineTierCaptured.size > 0
          ? ` (${machineTierCaptured.size} key(s) saved to machine tier)`
          : "";
      const savedLocation =
        tier === "local"
          ? ".nightgauge/config.local.yaml"
          : tier === "global"
            ? "~/.nightgauge/config.yaml"
            : ".nightgauge/config.yaml";
      vscode.window.showInformationMessage(`Settings saved to ${savedLocation}${machineTierNote}`);
      // Reload all tiers to refresh merged view (picks up runtime overlay).
      await this.loadAllTiers();
      this.updatePanel();
      // Live-apply also fires from runtime onDidChange when the store is
      // wired (subscribeToRuntimeChanges in setRuntimeStateStore). Call it
      // here as a safety net for the no-store path.
      await this.maybeApplyMaxConcurrentLive();
    } else {
      this.panel?.webview.postMessage(createErrorMessage(result.error ?? "Save failed"));
      vscode.window.showErrorMessage(`Failed to save settings: ${result.error}`);
    }
  }

  /**
   * Protect in-panel edits from file-watcher reloads. Multiple watcher events
   * from one atomic save share one prompt. Keeping edits leaves the working
   * copies untouched; explicit reload is the only destructive choice.
   */
  private async handleExternalConfigChange(): Promise<void> {
    if (!this.hasUnsavedChanges) {
      await this.loadAllTiers();
      this.updatePanel();
      vscode.window.showInformationMessage("Nightgauge settings reloaded from file");
      return;
    }
    if (this.externalReloadPrompt) return this.externalReloadPrompt;

    this.externalReloadPrompt = (async () => {
      const choice = await vscode.window.showWarningMessage(
        "Nightgauge settings changed on disk while this panel has unsaved edits.",
        { modal: true },
        "Keep My Edits",
        "Reload from Disk"
      );
      if (choice === "Reload from Disk") {
        this.hasUnsavedChanges = false;
        await this.loadAllTiers();
        this.updatePanel();
      } else {
        vscode.window.showInformationMessage(
          "Kept your unsaved Nightgauge settings. Save when ready to apply them."
        );
      }
    })().finally(() => {
      this.externalReloadPrompt = null;
    });
    return this.externalReloadPrompt;
  }

  /**
   * After a save, if the merged pipeline.max_concurrent value changed, push it
   * to the running ConcurrentPipelineManager + autonomous scheduler so the new
   * slot count takes effect immediately. Otherwise the user would have to
   * stop/start autonomous mode for the change to apply.
   */
  private async maybeApplyMaxConcurrentLive(): Promise<void> {
    const next = this.readMergedMaxConcurrent();
    if (next === undefined) return;
    if (this.lastSavedMaxConcurrent === next) return;
    this.lastSavedMaxConcurrent = next;
    if (this.onMaxConcurrentChanged) {
      try {
        await this.onMaxConcurrentChanged(next);
      } catch (err) {
        // Non-fatal — the YAML save already succeeded; live-apply is best-effort.
        vscode.window.showWarningMessage(
          `Saved, but failed to apply Max Concurrent Slots live: ${(err as Error).message}. Restart autonomous mode to pick it up.`
        );
      }
    }
  }

  private readMergedMaxConcurrent(): number | undefined {
    const merged = this.mergeResult?.config ?? this.currentConfig;
    const raw = (merged as { pipeline?: { max_concurrent?: number } })?.pipeline?.max_concurrent;
    if (typeof raw === "number" && raw >= 1 && raw <= 10) return raw;
    return undefined;
  }

  /**
   * Handle reset to defaults
   */
  private async handleReset(): Promise<void> {
    if (this.lockedSections.size > 0) {
      vscode.window.showWarningMessage(
        "Cannot reset all settings while a pipeline is running. Some sections are locked."
      );
      return;
    }

    const confirm = await vscode.window.showWarningMessage(
      "Reset all settings to defaults? This will clear both project and local configs.",
      { modal: true },
      "Reset"
    );

    if (confirm === "Reset") {
      // Keep project number, reset everything else in project config
      const projectNumber = this.projectConfig.project?.number;
      this.projectConfig = {
        project: {
          number: projectNumber,
        },
      };
      // Clear local config entirely
      this.localConfig = {};

      // Save both
      await Promise.all([
        this.yamlService.write(this.projectConfig, "project"),
        this.yamlService.writeLocal(this.localConfig),
      ]);

      await this.loadAllTiers();
      this.updatePanel();
    }
  }

  /**
   * Handle reset of a specific setting to a target tier
   */
  private async handleResetSetting(path: string, toTier: ViewTier): Promise<void> {
    if (this.isSectionLocked(path)) {
      vscode.window.showWarningMessage(
        "This setting cannot be changed while a pipeline is running"
      );
      return;
    }

    // Remove from higher tiers based on target
    switch (toTier) {
      case "default":
        // Remove from both project and local
        this.removeConfigValue(this.projectConfig, path);
        this.removeConfigValue(this.localConfig, path);
        break;
      case "global":
        // Remove from project and local (keep global)
        this.removeConfigValue(this.projectConfig, path);
        this.removeConfigValue(this.localConfig, path);
        break;
      case "project":
        // Remove from local only
        this.removeConfigValue(this.localConfig, path);
        break;
      default:
        return;
    }

    // Save affected configs
    await Promise.all([
      this.yamlService.write(this.projectConfig, "project"),
      this.yamlService.writeLocal(this.localConfig),
    ]);

    await this.loadAllTiers();
    this.updatePanel();
  }

  /**
   * Handle tier tab switch
   */
  private handleSwitchTier(tier: ViewTier): void {
    this.tierState = { ...this.tierState, currentTier: tier };
    this.panel?.webview.postMessage(createTierChangedMessage(tier));
    this.updatePanel();
  }

  /**
   * Handle open tier file in editor
   */
  private handleOpenTierFile(tier: ViewTier): void {
    let filePath: string | undefined;

    switch (tier) {
      case "project":
        filePath = this.yamlService.getConfigPath();
        break;
      case "local":
        filePath = this.yamlService.getLocalConfigPath();
        break;
      case "global":
        filePath = this.yamlService.getGlobalConfigPath();
        break;
      default:
        return;
    }

    if (filePath) {
      vscode.commands.executeCommand("vscode.open", vscode.Uri.file(filePath));
    }
  }

  /**
   * Handle open documentation link
   */
  private handleOpenDoc(path: string): void {
    // Open the doc file in the workspace
    const docPath = vscode.Uri.file(`${this.workspaceRoot}/${path}`);
    vscode.commands.executeCommand("vscode.open", docPath);
  }

  private async handleAction(action: string, payload?: Record<string, unknown>): Promise<void> {
    this.lmStudioLogger.info("LM Studio settings action invoked", {
      action,
      payload,
    });
    switch (action) {
      case "lm-studio-start-server":
        await this.handleLmStudioStartServer();
        break;
      case "lm-studio-refresh-models":
        await this.refreshLmStudioModels(false, payload);
        break;
      case "codex-refresh-models":
        this.refreshCodexModels(false);
        break;
      case "lm-studio-load-model":
        await this.handleLmStudioLoadModel(payload);
        break;
      case "validate-stage-adapter":
        await this.handleValidateStageAdapter(payload);
        break;
      case "preview-stage-resolution":
        this.handlePreviewStageResolution();
        break;
      case "edit-team-config": {
        await vscode.commands.executeCommand("nightgauge.editTeamConfig");
        break;
      }
      default:
        console.warn("Unknown settings action:", action);
    }
  }

  /**
   * Probe the auth status of the chosen adapter for a single stage and post
   * a `stage-adapter-auth-result` message back to the WebView.
   *
   * Always async, never blocks save (per ADR-003 / #3225). Failures collapse
   * to a `{ status: "error" }` reply with a human-readable reason rather
   * than throwing, so the indicator can render even on registry misses.
   */
  private async handleForgeAdd(): Promise<void> {
    await vscode.commands.executeCommand("nightgauge.configureForgeInstance");
    // Reload forge list after wizard completes
    await this.loadAllTiers();
    this.updatePanel();
  }

  private async handleForgeAction(
    action: "edit" | "delete" | "test" | "set-default",
    instanceId: string
  ): Promise<void> {
    switch (action) {
      case "edit":
        await vscode.commands.executeCommand("nightgauge.configureForgeInstance", instanceId);
        await this.loadAllTiers();
        this.updatePanel();
        break;

      case "delete": {
        const confirm = await vscode.window.showWarningMessage(
          `Delete forge instance "${instanceId}"? This will also remove its stored credentials.`,
          { modal: true },
          "Delete"
        );
        if (confirm !== "Delete") return;

        try {
          const secretSvc = SecretStorageService.getInstance();
          if (secretSvc) {
            await secretSvc.deleteForgeSecret(instanceId);
            await secretSvc.deleteForgeSecret(`${instanceId}.lastTested`);
          }
          // Remove from config.yaml by deleting the forges.<id> key
          const { config: projectCfg } = await this.yamlService.read();
          if (projectCfg?.forges) {
            delete (projectCfg.forges as Record<string, unknown>)[instanceId];
            await this.yamlService.write(projectCfg, "project");
          }
          vscode.window.showInformationMessage(`Forge instance "${instanceId}" removed.`);
        } catch (err) {
          vscode.window.showErrorMessage(
            `Failed to delete forge "${instanceId}": ${err instanceof Error ? err.message : String(err)}`
          );
        }
        await this.loadAllTiers();
        this.updatePanel();
        break;
      }

      case "test": {
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Testing connection to "${instanceId}"…`,
            cancellable: false,
          },
          async () => {
            try {
              const secretSvc = SecretStorageService.getInstance();
              const token = secretSvc ? ((await secretSvc.getForgeSecret(instanceId)) ?? "") : "";
              const result = await IpcClient.getInstance().forgeConnectionTest(instanceId, token);
              if (result.ok) {
                if (secretSvc) {
                  await secretSvc.setForgeLastTested(instanceId, new Date().toISOString());
                }
                vscode.window.showInformationMessage(
                  `Forge "${instanceId}" connected ✓ (${result.latency_ms}ms)`
                );
              } else {
                vscode.window.showErrorMessage(
                  `Forge "${instanceId}" connection failed: ${result.error ?? "unknown error"}`
                );
              }
            } catch (err) {
              vscode.window.showErrorMessage(
                `Connection test error: ${err instanceof Error ? err.message : String(err)}`
              );
            }
          }
        );
        await this.loadAllTiers();
        this.updatePanel();
        break;
      }

      case "set-default": {
        try {
          const { config: projectCfg } = await this.yamlService.read();
          const cfg = projectCfg ?? {};
          setConfigValue(cfg, "default_forge", instanceId);
          await this.yamlService.write(cfg, "project");
          vscode.window.showInformationMessage(`Default forge set to "${instanceId}".`);
        } catch (err) {
          vscode.window.showErrorMessage(
            `Failed to set default forge: ${err instanceof Error ? err.message : String(err)}`
          );
        }
        await this.loadAllTiers();
        this.updatePanel();
        break;
      }
    }
  }

  private async handleValidateStageAdapter(payload?: Record<string, unknown>): Promise<void> {
    const stage = typeof payload?.stage === "string" ? (payload.stage as string) : "";
    const adapter = typeof payload?.adapter === "string" ? (payload.adapter as string) : "";
    if (!stage || !adapter) {
      return;
    }

    let status: "ok" | "error" | "warn" = "error";
    let reason: string | undefined;
    try {
      const sdkAdapter = toIncrediAdapter(adapter as ExecutionAdapter, process.env);
      const result = await validateAdapterAuth(sdkAdapter);
      if (result.ok) {
        status = "ok";
      } else {
        status = result.category === "AUTH_MISSING" ? "warn" : "error";
        reason = result.reason;
      }
    } catch (err) {
      reason = err instanceof Error ? err.message : String(err);
    }

    this.panel?.webview.postMessage({
      type: "stage-adapter-auth-result",
      stage,
      adapter,
      status,
      reason,
    });
  }

  /**
   * Recompute the mode-aware resolution preview and re-render the panel so
   * the table reflects the latest adapter selections without requiring a
   * full save first. Lightweight — `resolveStageAdapter` reads file-system
   * state synchronously, but only six stages are queried.
   */
  private handlePreviewStageResolution(): void {
    this.updatePanel();
  }

  private handleDismissDriftBanner(): void {
    this.driftBannerDismissed = true;
    this.updatePanel();
  }

  private handleShowDriftedKeysOnly(): void {
    // Future: filter setting rows to only drift entries.
    // For now, scroll to the first drifted key via a status message.
    const driftCount = this.tierAuditEntries.filter((e) => e.status.startsWith("DRIFT")).length;
    if (driftCount > 0) {
      vscode.window.showInformationMessage(
        `${driftCount} setting${driftCount !== 1 ? "s have" : " has"} tier drift. Search for "Drift" in the settings panel to locate them.`
      );
    }
  }

  private async handleMoveTierKey(key: string, targetTier: string): Promise<void> {
    const answer = await vscode.window.showWarningMessage(
      `Move "${key}" to the ${targetTier} tier? This will update your config files.`,
      { modal: true },
      "Move"
    );
    if (answer !== "Move") return;
    await vscode.commands.executeCommand("nightgauge.runSettingsMigration");
    await this.loadAllTiers();
    this.updatePanel();
  }

  private getLmStudioPayloadString(
    payload: Record<string, unknown> | undefined,
    key: string,
    fallback: string
  ): string {
    const value = payload?.[key];
    return typeof value === "string" && value.trim() ? value.trim() : fallback;
  }

  private getLmStudioPayloadNumber(
    payload: Record<string, unknown> | undefined,
    key: string,
    fallback?: number
  ): number | undefined {
    const value = payload?.[key];
    return typeof value === "number" && Number.isFinite(value) ? value : fallback;
  }

  private async handleLmStudioStartServer(): Promise<void> {
    const baseUrl = this.currentConfig.lm_studio?.base_url ?? "http://127.0.0.1:1234/v1";
    const apiKey = this.currentConfig.lm_studio?.api_key ?? "lm-studio";

    try {
      this.lmStudioLogger.info("Start Server clicked");
      await this.lmStudioService.startServer(baseUrl, apiKey);
      await this.refreshLmStudioModels(true);
      this.lmStudioLogger.info("LM Studio server start flow completed");
      vscode.window.showInformationMessage("LM Studio server started.");
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to start LM Studio server.";
      this.lmStudioLogger.error("LM Studio server start flow failed", {
        message,
      });
      vscode.window.showErrorMessage(message);
    }
  }

  private async refreshLmStudioModels(
    silent: boolean,
    payload?: Record<string, unknown>
  ): Promise<void> {
    const baseUrl = this.getLmStudioPayloadString(
      payload,
      "lm_studio.base_url",
      this.currentConfig.lm_studio?.base_url ?? "http://127.0.0.1:1234/v1"
    );
    const apiKey = this.getLmStudioPayloadString(
      payload,
      "lm_studio.api_key",
      this.currentConfig.lm_studio?.api_key ?? "lm-studio"
    );

    this.lmStudioLogger.info("Refreshing LM Studio models", {
      silent,
      baseUrl,
    });

    try {
      const models = await this.lmStudioService.listModels(baseUrl, apiKey);
      if (models.length === 0) {
        this.lmStudioModels = [];
        this.updatePanel();
        this.lmStudioLogger.warn("LM Studio model refresh returned zero models", { baseUrl });
        if (!silent) {
          vscode.window.showWarningMessage(
            "LM Studio returned no models. Start the server and make sure at least one model is downloaded."
          );
        }
        return;
      }

      this.lmStudioModels = models;
      this.updatePanel();
      this.lmStudioLogger.info("LM Studio models refreshed", {
        count: models.length,
        models: models.map((model) => ({ id: model.id, loaded: model.loaded })),
      });
      if (!silent) {
        vscode.window.showInformationMessage(
          `LM Studio models refreshed: ${models.length} available.`
        );
      }
    } catch (error) {
      this.lmStudioModels = [];
      const message =
        error instanceof Error
          ? error.message
          : "Failed to fetch models from LM Studio. Start the server and try again.";
      this.lmStudioLogger.error("LM Studio model refresh failed", { message, baseUrl });
      if (!silent) {
        vscode.window.showErrorMessage(message);
      }
    }
  }

  private refreshCodexModels(silent: boolean): void {
    this.codexModels = this.codexModelCatalogService.listModels();
    this.updatePanel();

    if (!silent) {
      vscode.window.showInformationMessage(
        `Codex models refreshed: ${this.codexModels.length} available.`
      );
    }
  }

  private async handleLmStudioLoadModel(payload?: Record<string, unknown>): Promise<void> {
    const baseUrl = this.getLmStudioPayloadString(
      payload,
      "lm_studio.base_url",
      this.currentConfig.lm_studio?.base_url ?? "http://127.0.0.1:1234/v1"
    );
    const apiKey = this.getLmStudioPayloadString(
      payload,
      "lm_studio.api_key",
      this.currentConfig.lm_studio?.api_key ?? "lm-studio"
    );
    const model = this.getLmStudioPayloadString(
      payload,
      "lm_studio.model",
      this.currentConfig.lm_studio?.model ?? ""
    );
    const contextLength = this.getLmStudioPayloadNumber(
      payload,
      "lm_studio.context_length",
      this.currentConfig.lm_studio?.context_length
    );

    this.lmStudioLogger.info("Load Model clicked", {
      model,
      contextLength,
    });

    if (!model) {
      this.lmStudioLogger.warn("Load Model requested without a selected model");
      vscode.window.showWarningMessage("Select an LM Studio model first, then load it.");
      return;
    }

    try {
      await this.lmStudioService.startServer(baseUrl, apiKey);
      await this.lmStudioService.loadModel(model, contextLength);
      await this.refreshLmStudioModels(true, payload);
      this.lmStudioLogger.info("LM Studio model load flow completed", {
        model,
        contextLength,
      });
      vscode.window.showInformationMessage(
        `LM Studio model loaded: ${model}${contextLength ? ` (context ${contextLength})` : ""}.`
      );
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to load LM Studio model.";
      this.lmStudioLogger.error("LM Studio model load flow failed", {
        model,
        contextLength,
        message,
      });
      vscode.window.showErrorMessage(message);
    }
  }

  /**
   * Determine which tier to edit based on current view
   */
  private getEditTargetTier(): EditableTier {
    if (this.tierState.currentTier === "project") return "project";
    if (this.tierState.currentTier === "local") return "local";
    if (this.tierState.currentTier === "global") return "global";
    // For merged/other views, use the default
    return this.tierState.defaultEditTier;
  }

  /**
   * Extract the settings section ID from a config path.
   *
   * Config paths follow the pattern "section.field" or "section.nested.field".
   * Special cases map config prefixes to their section IDs.
   */
  private getSectionForPath(path: string): string | undefined {
    const prefix = path.split(".")[0];
    const sectionMap: Record<string, string> = {
      pr: "pull_request",
      pull_request: "pull_request",
      ui: "core",
      lm_studio: "core",
      ollama: "core",
      model_routing: "routing",
    };
    return sectionMap[prefix] ?? prefix;
  }

  /**
   * Check if the section owning a config path is currently locked.
   */
  private isSectionLocked(path: string): boolean {
    const section = this.getSectionForPath(path);
    return section !== undefined && this.lockedSections.has(section);
  }

  /**
   * Compare two sets for equality.
   */
  private setsEqual(a: Set<string>, b: Set<string>): boolean {
    if (a.size !== b.size) return false;
    for (const item of a) {
      if (!b.has(item)) return false;
    }
    return true;
  }

  /**
   * Remove a value at a config path
   */
  private removeConfigValue(config: IncrediConfig, path: string): void {
    const parts = path.split(".");
    let current: Record<string, unknown> = config as Record<string, unknown>;

    for (let i = 0; i < parts.length - 1; i++) {
      const part = parts[i];
      if (typeof current[part] !== "object" || current[part] === null) {
        return; // Path doesn't exist
      }
      current = current[part] as Record<string, unknown>;
    }

    const lastKey = parts[parts.length - 1];
    delete current[lastKey];
  }

  /**
   * Handle panel closed by user
   */
  private handlePanelClosed(): void {
    this.panel = undefined;
    SettingsPanel.currentPanel = undefined;
  }

  /**
   * Check if the panel is currently visible
   */
  isVisible(): boolean {
    return this.panel?.visible ?? false;
  }

  /**
   * Dispose of the panel and clean up resources
   */
  dispose(): void {
    SettingsPanel.currentPanel = undefined;

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
  }
}
