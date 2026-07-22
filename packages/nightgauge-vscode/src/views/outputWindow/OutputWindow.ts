/**
 * OutputWindow - WebView panel manager for the output window
 *
 * Displays real-time pipeline output, tool calls, and token usage.
 * Supports user input for mid-execution messaging and interrupt capability.
 * Now supports PipelineStateService as single source of truth (Issue #154).
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 * @see docs/ARCHITECTURE_DIAGRAMS.md - State Management Architecture
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { HeadlessOrchestrator } from "../../services/HeadlessOrchestrator";
import {
  OutputWindowState,
  type OutputLevel,
  type OutputEntry,
  type StageStatus,
  type ExecutionMode,
  type SlotInfo,
} from "./OutputWindowState";
import {
  getOutputWindowHtml,
  escapeHtml,
  formatStageName,
  type SearchState,
} from "./OutputWindowHtml";
import {
  detectContentType,
  detectLanguage,
  shouldCollapse,
  createCollapsibleEntry,
  CODE_COLLAPSE_THRESHOLD,
  type ContentType,
} from "./contentFormatter";
import {
  OutputWindowMessageHandler,
  createAppendMessage,
  createClearMessage,
  createClearStageMessage,
  createAutoScrollMessage,
  createWordWrapMessage,
  createTimestampsMessage,
  createToolIndicatorMessage,
  createToolIndicatorCompleteMessage,
  createToolSummaryMessage,
  createQuestionPromptMessage,
  createQuestionAnsweredMessage,
  createPipelineStateMessage,
  createSearchStateMessage,
  createSetModeMessage,
  createRemoveStallWarningsMessage,
  createAddStallWarningMessage,
  createCollapseStageMessage,
  createSlotBadgeUpdateMessage,
  createOverviewCardUpdateMessage,
  createRemoveOldestMessage,
} from "./OutputWindowMessageHandler";
import type {
  AskUserQuestionPayload,
  QuestionResponse,
  ActiveQuestionState,
} from "../../types/askUserQuestion";
import { generateQuestionId } from "../../types/askUserQuestion";
import { formatToolSummary, formatToolIndicator, type ToolCallData } from "./ToolCallIndicator";
import type { PipelineStateService, PipelineState } from "../../services/PipelineStateService";
import { IpcClient } from "../../services/IpcClient";
import { getActiveInteractiveProcess } from "../../utils/skillRunner";
import { stripAnsi } from "../../utils/ansiStripper";
import { isReasoningLine } from "./reasoningDetector";
import { LogFileWriter, type LogFileConfig } from "../../utils/log-file-writer";
import { ExecutionHistoryReader } from "../../utils/executionHistoryReader";

/**
 * Configuration options for the output window
 */
export interface OutputWindowConfig {
  autoOpen?: boolean;
  autoScroll?: boolean;
  wordWrap?: boolean;
  verboseLevel?: "minimal" | "normal" | "verbose" | "debug";
  showTokenUsage?: boolean;
  /**
   * When true, rebuilds archived tabs from on-disk logs on first panel
   * open after a reload (Issue #2818). Defaults to true.
   */
  rehydrateFromLogs?: boolean;
}

/**
 * Default configuration
 */
const DEFAULT_CONFIG: Required<OutputWindowConfig> = {
  autoOpen: true,
  autoScroll: true,
  wordWrap: true,
  verboseLevel: "normal",
  showTokenUsage: true,
  rehydrateFromLogs: true,
};

/**
 * OutputWindow class manages the WebView panel for pipeline output
 *
 * @example
 * ```typescript
 * const outputWindow = new OutputWindow(context.extensionUri, context.workspaceState);
 *
 * // Show the window
 * outputWindow.show();
 *
 * // Append output
 * outputWindow.appendLine('Starting pipeline...', 'info', 'issue-pickup');
 *
 * // Subscribe to orchestrator events
 * orchestrator.events.on('stage:start', (event) => {
 *   outputWindow.updateStageStatus(event.stage, 'running');
 * });
 * ```
 */
export class OutputWindow implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private state: OutputWindowState;
  private messageHandler: OutputWindowMessageHandler;
  private config: Required<OutputWindowConfig>;
  private orchestrator: HeadlessOrchestrator | null = null;

  /**
   * Active question state for AskUserQuestion tool support (Issue #118)
   * When set, the pipeline is waiting for user input
   */
  private activeQuestion: ActiveQuestionState | null = null;

  /**
   * Current active tool indicator ID (Issue #170)
   * Used to auto-complete previous indicator when new content arrives
   */
  private currentActiveToolId: string | null = null;

  /**
   * Last known pipeline running state (Issue #431)
   * Used to detect state changes and send updates to WebView
   */
  private lastPipelineRunning: boolean = false;

  /**
   * Track whether the last appended line was blank (Issue #794)
   * Used to collapse consecutive blank lines in output
   */
  private lastLineWasBlank: boolean = false;

  /**
   * Buffer for consecutive reasoning lines (Issue #796)
   * Flushed as a single collapsible entry when a substantive line arrives,
   * a stage completes, or the output is cleared.
   */
  private reasoningBuffer: string[] = [];

  // Log replay state (Issue #1352)
  private hasReplayed = false;
  private replayWorkspaceRoot: string | null = null;
  private replayLogConfig: Partial<LogFileConfig> | null = null;

  // Cross-session log rehydration state (Issue #2818)
  private hasRehydrated = false;

  // Rehydration safety caps applied when the user has not explicitly set
  // pipeline.logs.max_count / max_age_days in config.yaml. Protects workspaces
  // with large on-disk log histories (observed 999+ files) from spawning one
  // archived tab per historical issue on panel open.
  private static readonly REHYDRATE_DEFAULT_MAX_COUNT = 10;
  private static readonly REHYDRATE_DEFAULT_MAX_AGE_DAYS = 1;

  constructor(
    private readonly extensionUri: vscode.Uri,
    workspaceState: vscode.Memento,
    config?: OutputWindowConfig
  ) {
    this.state = new OutputWindowState(workspaceState);
    this.config = { ...DEFAULT_CONFIG, ...config };

    // Initialize message handler with callbacks
    this.messageHandler = new OutputWindowMessageHandler({
      onInterrupt: () => this.handleInterrupt(),
      onClearLogs: () => this.handleClearLogs(),
      onCopyToClipboard: () => this.handleCopyToClipboard(),
      onExport: (format) => this.handleExport(format),
      onToggleAutoScroll: (enabled) => this.handleToggleAutoScroll(enabled),
      onToggleWordWrap: (enabled) => this.handleToggleWordWrap(enabled),
      onToggleTimestamps: (enabled) => this.handleToggleTimestamps(enabled),
      onToggleEntry: (entryId) => this.handleToggleEntry(entryId),
      onQuestionResponse: (questionId, response) =>
        this.handleQuestionResponse(questionId, response),
      onSearchTextChange: (text) => this.handleSearchTextChange(text),
      onToggleSearchCaseSensitive: (enabled) => this.handleToggleSearchCaseSensitive(enabled),
      onToggleSearchUseRegex: (enabled) => this.handleToggleSearchUseRegex(enabled),
      onSendMessage: (text) => this.handleSendMessage(text),
      onTabSwitch: (slotIndex) => this.handleTabSwitch(slotIndex),
    });

    this.subscribeToPhaseEvents();
  }

  /**
   * Subscribe to `phase.start` / `phase.complete` IPC events so the
   * Overview card surfaces the active in-stage phase (e.g. "research 2/4")
   * and clears it on transitions. Mirrors `PipelineSlotsTracker`'s
   * matching-clear semantics — `phase.complete` only clears the active
   * phase if name + stage still match (Issue #3010).
   */
  private subscribeToPhaseEvents(): void {
    const ipc = IpcClient.getInstance();

    const phaseStartDisposable = ipc.on("phase.start", (data) => {
      const event = data as
        | { issueNumber?: number; stage?: string; name?: string; index?: number; total?: number }
        | undefined;
      if (
        !event ||
        typeof event.issueNumber !== "number" ||
        typeof event.name !== "string" ||
        typeof event.index !== "number" ||
        typeof event.total !== "number"
      ) {
        return;
      }
      const slot = this.state.getSlotByIssueNumber(event.issueNumber);
      if (!slot) return;
      this.state.updateSlotPhase(slot.slotIndex, {
        name: event.name,
        index: event.index,
        total: event.total,
      });
      this.sendSlotBadgeUpdate(slot.slotIndex);
    });
    this.disposables.push(phaseStartDisposable);

    const phaseCompleteDisposable = ipc.on("phase.complete", (data) => {
      const event = data as { issueNumber?: number; stage?: string; name?: string } | undefined;
      if (
        !event ||
        typeof event.issueNumber !== "number" ||
        typeof event.name !== "string" ||
        typeof event.stage !== "string"
      ) {
        return;
      }
      const slot = this.state.getSlotByIssueNumber(event.issueNumber);
      if (!slot) return;
      this.state.clearSlotPhase(slot.slotIndex, event.name, event.stage as PipelineStage);
      this.sendSlotBadgeUpdate(slot.slotIndex);
    });
    this.disposables.push(phaseCompleteDisposable);
  }

  /**
   * Ensure the output window WebView panel is created.
   *
   * If a panel already exists, this is a no-op — callers that want to
   * force the panel to the foreground should use {@link reveal} instead.
   * Historically `show()` always re-revealed the panel, which stole the
   * active-tab slot in its ViewColumn on every pipeline stage transition
   * and interrupted the user (e.g., pulling them out of a Claude Code
   * tab). Automated per-stage updates now call this "ensure-created"
   * variant exclusively.
   */
  show(): void {
    // If we already have a panel, do nothing — do NOT reveal it,
    // because reveal() switches the ViewColumn's active tab to the
    // Output Window even with preserveFocus=true.
    if (this.panel) {
      return;
    }

    // Create the WebView panel
    this.panel = vscode.window.createWebviewPanel(
      "incrediOutputWindow",
      "Nightgauge Output",
      { viewColumn: vscode.ViewColumn.Two, preserveFocus: true },
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [
          vscode.Uri.joinPath(this.extensionUri, "src", "views", "outputWindow"),
        ],
      }
    );

    // Set initial content
    this.updatePanel();

    // Trigger log replay if conditions are met (Issue #1352)
    this.maybeReplayPersistedLog();

    // Rebuild archived tabs from on-disk logs after a reload (Issue #2818)
    this.maybeRehydrateFromLogs();

    // Handle messages from the WebView
    this.panel.webview.onDidReceiveMessage(
      this.messageHandler.handleMessage,
      undefined,
      this.disposables
    );

    // Handle panel disposal
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
  }

  /**
   * Force the output window panel to the foreground.
   *
   * Intended for explicit user intent (clicking an "Open Output" button,
   * running the `nightgauge.showOutputWindow` command, manually
   * invoking a stage) — NOT for automated per-stage pipeline updates,
   * which must use {@link show} to avoid stealing the user's active tab.
   *
   * `preserveFocus` is kept `true` so the text cursor stays where it is.
   */
  reveal(): void {
    if (!this.panel) {
      this.show();
    }
    // `this.show()` just created the panel — if it's still undefined
    // (e.g., because createWebviewPanel threw), there's nothing to reveal.
    this.panel?.reveal(vscode.ViewColumn.Two, true); // preserveFocus=true
  }

  /**
   * Set the HeadlessOrchestrator for interrupt control
   *
   * The orchestrator is used for the interrupt button functionality.
   * State synchronization should use setStateService() instead.
   */
  setOrchestrator(orchestrator: HeadlessOrchestrator): void {
    this.orchestrator = orchestrator;
  }

  /**
   * Connect to PipelineStateService for unified state management
   *
   * When connected, the OutputWindow subscribes to state changes and
   * automatically syncs token usage and stage statuses from the
   * authoritative state file.
   *
   * @param stateService - The PipelineStateService singleton
   */
  setStateService(stateService: PipelineStateService): void {
    // Subscribe to state changes
    const stateDisposable = stateService.onStateChanged((state) => {
      if (state) {
        this.syncFromState(state);

        // Track pipeline running state and send to WebView (Issue #431)
        // Determine running state by checking if any stage has status === 'running'
        const runningStage = this.findRunningStage(state);
        const isRunning = runningStage !== null;
        if (isRunning !== this.lastPipelineRunning) {
          this.lastPipelineRunning = isRunning;
          this.sendPipelineState(isRunning, runningStage);
        }
      } else {
        // State is null - pipeline not running
        if (this.lastPipelineRunning) {
          this.lastPipelineRunning = false;
          this.sendPipelineState(false, null);
        }
      }
      // When state is null, preserve logs for user review.
      // Logs are cleared when a new pipeline starts (above) or via explicit reset.
    });
    this.disposables.push(stateDisposable);

    // Subscribe to unified token events (Issue #404)
    // Per-stage cost summary lines are logged inline in the output
    const tokenDisposable = stateService.onTokenUsageUpdated((tokenUpdate) => {
      if (
        tokenUpdate.stage &&
        (tokenUpdate.inputTokens > 0 ||
          tokenUpdate.outputTokens > 0 ||
          (tokenUpdate.costUsd ?? 0) > 0)
      ) {
        const totalTokens = ((tokenUpdate.inputTokens + tokenUpdate.outputTokens) / 1000).toFixed(
          1
        );
        const cost = (tokenUpdate.costUsd ?? 0).toFixed(4);
        this.appendLine(
          `  \u2192 $${cost} | ${totalTokens}K tokens`,
          "info",
          tokenUpdate.stage as PipelineStage
        );
      }

      // Route token delta to owning slot for badge display (Issue #2815)
      if (tokenUpdate.issueNumber != null) {
        const slot = this.state.getSlotByIssueNumber(tokenUpdate.issueNumber);
        if (slot !== undefined) {
          this.state.updateSlotTokenUsage(slot.slotIndex, {
            inputTokens: tokenUpdate.inputTokens,
            outputTokens: tokenUpdate.outputTokens,
            cacheReadTokens: tokenUpdate.cacheReadTokens ?? 0,
            cacheCreationTokens: tokenUpdate.cacheCreationTokens ?? 0,
            costUsd: tokenUpdate.costUsd ?? 0,
          });
          this.sendSlotBadgeUpdate(slot.slotIndex);
        }
      }
    });
    this.disposables.push(tokenDisposable);

    // Initial sync
    stateService.getState().then((state) => {
      if (state) {
        this.syncFromState(state);
        // Send initial pipeline state to WebView (Issue #431)
        const runningStage = this.findRunningStage(state);
        const isRunning = runningStage !== null;
        this.lastPipelineRunning = isRunning;
        this.sendPipelineState(isRunning, runningStage);
      } else {
        // No state - ensure WebView shows pipeline as not running
        this.sendPipelineState(false, null);
      }
    });
  }

  /**
   * Configure disk logging for pipeline output
   *
   * Enables writing log entries to .nightgauge/logs/ directory.
   * Must be called after construction with workspace root path.
   *
   * @param workspaceRoot - Absolute path to workspace root
   * @param config - Optional log configuration from config.yaml pipeline.logs
   * @see Issue #190 - Pipeline logs persistence
   */
  setLogConfig(
    workspaceRoot: string,
    config?: {
      retain?: boolean;
      dir?: string;
      max_age_days?: number;
      max_count?: number;
    }
  ): void {
    this.replayWorkspaceRoot = workspaceRoot;
    this.replayLogConfig = config ?? null;
    this.state.setLogConfig(workspaceRoot, config);
    this.maybeReplayPersistedLog(); // Trigger replay if all conditions met (Issue #1352)
    this.maybeRehydrateFromLogs(); // Rebuild archived tabs from on-disk logs (Issue #2818)
  }

  /**
   * Set the execution mode for dual-mode rendering (Issue #496)
   *
   * Mode determines how output is rendered and what UI elements are shown:
   * - 'headless': Stream-json parsing, token tracking visible, tool indicators
   * - 'interactive': Raw text display, token area hidden, ANSI stripping
   *
   * @param mode - The execution mode ('headless' or 'interactive')
   */
  setMode(mode: ExecutionMode): void {
    this.state.setExecutionMode(mode);

    // Send mode update to WebView
    if (this.panel) {
      this.panel.webview.postMessage(createSetModeMessage(mode));
    }
  }

  /**
   * Get the current execution mode
   *
   * @returns Current execution mode ('headless' or 'interactive')
   */
  getMode(): ExecutionMode {
    return this.state.getExecutionMode();
  }

  /**
   * Sync output window state from PipelineState
   *
   * Called whenever PipelineStateService emits a state change.
   * Updates token usage and stage statuses from the authoritative state.
   */
  private syncFromState(state: PipelineState): void {
    // Update issue number if changed
    const currentIssue = this.state.getIssueNumber();
    if (currentIssue !== state.issue_number) {
      this.state.setIssueNumber(state.issue_number);
    }

    // Sync global stage statuses (for "All" tab backward compat)
    // Note: PipelineStateService uses 'failed', OutputWindowState uses 'error'
    for (const [stageName, stageState] of Object.entries(state.stages)) {
      const stage = stageName as PipelineStage;
      const currentProgress = this.state.getStageProgress(stage);

      const mappedStatus: StageStatus =
        stageState.status === "failed"
          ? "error"
          : stageState.status === "deferred"
            ? "skipped"
            : stageState.status;

      if (currentProgress?.status !== mappedStatus) {
        this.state.updateStageStatus(stage, mappedStatus);
      }
    }

    // Route slot-specific fields to the slot owning this issue (Issue #2814/#2815).
    const slotIndex = this.state.findSlotIndexByIssue(state.issue_number);
    if (slotIndex !== undefined) {
      this.syncSlotFromState(slotIndex, state);
    }
  }

  /**
   * Apply a PipelineState snapshot to a specific slot (Issue #2979).
   *
   * Factored out of syncFromState so per-slot PipelineStateService instances
   * (created per-worktree by the ConcurrentPipelineManager factory) can drive
   * slot card updates directly, without depending on the global singleton's
   * issue_number to route events.
   *
   * Updates: per-stage statuses, current running stage label, authoritative
   * token totals, and derived slot-level status/badge.
   */
  private syncSlotFromState(slotIndex: number, state: PipelineState): void {
    // Per-stage statuses
    for (const [stageName, stageState] of Object.entries(state.stages)) {
      const stage = stageName as PipelineStage;
      const mappedStatus: StageStatus =
        stageState.status === "failed"
          ? "error"
          : stageState.status === "deferred"
            ? "skipped"
            : stageState.status;
      this.state.updateSlotStageStatus(slotIndex, stage, mappedStatus);
    }

    // Current running stage label — picks the first stage with status running.
    // Without this, the Overview card's stage label stays stuck on whatever
    // value the stdout-driven slotOutputManager bridge last wrote.
    const runningEntry = Object.entries(state.stages).find(([, s]) => s.status === "running");
    if (runningEntry) {
      this.state.updateSlotStage(slotIndex, runningEntry[0] as PipelineStage);
    }

    // Authoritative token totals
    if (state.tokens) {
      this.state.setSlotTokenUsage(slotIndex, {
        inputTokens: state.tokens.total_input ?? state.tokens.input ?? 0,
        outputTokens: state.tokens.total_output ?? state.tokens.output ?? 0,
        cacheReadTokens: state.tokens.total_cache_read ?? state.tokens.cacheRead ?? 0,
        cacheCreationTokens: state.tokens.total_cache_creation ?? state.tokens.cacheCreation ?? 0,
        costUsd: state.tokens.estimated_cost_usd ?? state.tokens.cost_usd ?? 0,
      });
    }

    // Derived slot-level status for the badge
    const derivedStatus = this.deriveSlotStatus(state);
    const isTerminal = derivedStatus === "complete" || derivedStatus === "error";
    this.state.updateSlotStatus(slotIndex, derivedStatus, isTerminal ? Date.now() : undefined);
    this.sendSlotBadgeUpdate(slotIndex);
  }

  /**
   * Subscribe this OutputWindow to a per-slot PipelineStateService (Issue #2979).
   *
   * Concurrent pipeline slots run on per-worktree PipelineStateService instances
   * (created in ConcurrentPipelineManager's orchestratorFactory). Their
   * onStateChanged/onTokenUsageUpdated events never reach the global singleton
   * this OutputWindow subscribes to via setStateService(), so Overview cards
   * were frozen at their initial state for the full slot lifetime.
   *
   * Callers should dispose the returned Disposable when the slot completes
   * (e.g., in ConcurrentPipelineManager's onSlotCompleted/onSlotFailed/onSlotCleaned)
   * so event listeners don't leak.
   *
   * @param slotIndex    - 0-based slot index that owns this state service
   * @param stateService - Per-slot PipelineStateService instance
   * @returns Disposable that unsubscribes the state listener
   */
  subscribeSlotToStateService(
    slotIndex: number,
    stateService: PipelineStateService
  ): vscode.Disposable {
    const sub = stateService.onStateChanged((state) => {
      if (!state) return;
      this.syncSlotFromState(slotIndex, state);
    });

    // Apply any existing state synchronously-ish so the card is never blank.
    stateService
      .getState()
      .then((state) => {
        if (state) this.syncSlotFromState(slotIndex, state);
      })
      .catch(() => {
        // State read failures are non-fatal — the subsequent onStateChanged
        // events will still populate the slot.
      });

    return sub;
  }

  /**
   * Derive a single slot-level status from all stage statuses in the given state.
   *
   * Priority: error > running > complete > skipped > pending
   */
  private deriveSlotStatus(state: PipelineState): StageStatus {
    let hasRunning = false;
    let hasError = false;
    let allDone = true;
    let anySkipped = false;

    for (const stageState of Object.values(state.stages)) {
      const s = stageState.status;
      if (s === "failed") {
        hasError = true;
      } else if (s === "running") {
        hasRunning = true;
        allDone = false;
      } else if (s === "complete" || s === "skipped" || s === "deferred") {
        if (s === "skipped" || s === "deferred") anySkipped = true;
      } else {
        allDone = false;
      }
    }

    if (hasError) return "error";
    if (hasRunning) return "running";
    if (allDone && Object.keys(state.stages).length > 0) {
      return anySkipped && !Object.values(state.stages).some((s) => s.status === "complete")
        ? "skipped"
        : "complete";
    }
    return "pending";
  }

  /**
   * Post a slot-badge-update message to the WebView for a single slot (Issue #2815).
   */
  private sendSlotBadgeUpdate(slotIndex: number): void {
    if (!this.panel) return;
    // Direct O(1) lookup via getSlotByIssueNumber is unavailable here since we have slotIndex,
    // not issueNumber. Use the map-backed getActiveSlots and locate by index.
    // Slot counts are small (typically 1–4) so linear scan is negligible.
    const slot = this.state.getActiveSlots().find((s) => s.slotIndex === slotIndex);
    if (!slot) return;
    this.panel.webview.postMessage(createSlotBadgeUpdateMessage(slotIndex, slot));
    // Companion overview-card update so the Overview panel's per-issue card
    // refreshes its cost/tokens/stage in place — same cadence as the badges.
    const stageLabel = slot.stage ? formatStageName(slot.stage as PipelineStage) : "—";
    const status = slot.status ?? "pending";
    const statusLabel =
      status === "running"
        ? "Running"
        : status === "complete"
          ? "Complete"
          : status === "error"
            ? "Error"
            : status === "skipped"
              ? "Skipped"
              : "Pending";
    this.panel.webview.postMessage(createOverviewCardUpdateMessage(slot, stageLabel, statusLabel));
  }

  /**
   * Send pipeline state to the WebView (Issue #431)
   *
   * Notifies the WebView of pipeline running state changes so it can
   * update pipeline-dependent UI elements (e.g., Stop button enabled state).
   *
   * @param isRunning - Whether a pipeline is running
   * @param currentStage - Current pipeline stage name, or null if not running
   */
  private sendPipelineState(isRunning: boolean, currentStage: string | null): void {
    if (this.panel) {
      this.panel.webview.postMessage(createPipelineStateMessage(isRunning, false, currentStage));
    }
  }

  /**
   * Find the currently running stage from PipelineState (Issue #431)
   *
   * PipelineState doesn't have a top-level status field - running state
   * is determined by checking if any stage has status === 'running'.
   *
   * @param state - The pipeline state
   * @returns The name of the running stage, or null if no stage is running
   */
  private findRunningStage(state: PipelineState): string | null {
    for (const [stageName, stageState] of Object.entries(state.stages)) {
      if (stageState.status === "running") {
        return stageName;
      }
    }
    return null;
  }

  /**
   * Post an entry to the webview and drain any pending eviction events.
   *
   * Keeps the webview DOM aligned with the in-memory entry cap. Without this,
   * the TS side trims `entries` / `perSlotBuffers` past MAX_ENTRIES but the
   * renderer keeps every appended `.output-entry` node — so its heap grows
   * unbounded across a long pipeline.
   */
  private postEntryToWebview(entry: OutputEntry): void {
    if (!this.panel) return;
    this.panel.webview.postMessage(createAppendMessage(entry));
    const evictions = this.state.drainEvictions();
    for (const ev of evictions) {
      if (ev.scope === "aggregate") {
        this.panel.webview.postMessage(createRemoveOldestMessage(ev.count));
      } else if (ev.scope === "slot") {
        this.panel.webview.postMessage(createRemoveOldestMessage(ev.count, ev.slotIndex));
      }
      // slot-cleared events are already handled via createClearMessage
      // in the clearSlot/clearActive paths.
    }
  }

  /**
   * Flush buffered reasoning lines as a single collapsible entry (Issue #796)
   *
   * Creates a collapsed entry with count summary and all reasoning lines
   * as expandable details. No-op if buffer is empty.
   *
   * @param stage - Optional pipeline stage for context
   */
  private flushReasoningBuffer(stage?: PipelineStage): void {
    if (this.reasoningBuffer.length === 0) {
      return;
    }

    const count = this.reasoningBuffer.length;
    const summary = `▶ ${count} reasoning step${count !== 1 ? "s" : ""}`;
    const details = this.reasoningBuffer.join("\n");

    const entry = this.state.addEntry(summary, "info", stage, {
      collapsible: true,
      details,
    });

    this.postEntryToWebview(entry);

    this.reasoningBuffer = [];
  }

  /**
   * Append a line of output
   *
   * Automatically detects content type (diff, JSON, structured-patch) and
   * applies appropriate formatting. Large content is automatically collapsed.
   *
   * @param text - The text content to append
   * @param level - Output level (info, debug, warning, error, tool, user)
   * @param stage - Optional pipeline stage for context
   * @param options - Additional options for collapsible content
   * @see Issue #428 - Format raw tool output in Nightgauge Output view
   */
  appendLine(
    text: string,
    level: OutputLevel = "info",
    stage?: PipelineStage,
    options?: {
      collapsible?: boolean;
      details?: string;
      contentType?: ContentType;
      language?: string;
      /** Slot index for concurrent pipeline tab routing (Issue #2705) */
      slotIndex?: number;
    }
  ): void {
    // Strip ANSI escape codes from pipeline output (Issue #793)
    text = stripAnsi(text);
    if (options?.details) {
      options = { ...options, details: stripAnsi(options.details) };
    }

    // Normalize literal \n escape sequences to real newlines
    // Claude's stream-json can emit file contents with double-escaped newlines
    // (\\n in JSON becomes literal \n in the string), causing 45KB+ content to
    // appear as a single line and bypass line-count collapse thresholds.
    if (text.length > 500 && text.includes("\\n")) {
      text = text.replace(/\\n/g, "\n");
      if (options?.details && options.details.includes("\\n")) {
        options = {
          ...options,
          details: options.details.replace(/\\n/g, "\n"),
        };
      }
    }

    // Trim leading/trailing blank lines from entry text (Issue #846)
    text = text.replace(/^\n+|\n+$/g, "");

    // Normalize consecutive blank lines in output (Issue #794)
    // Collapse runs of 2+ blank lines within multi-line text to at most one
    text = text.replace(/\n{3,}/g, "\n\n");

    // Suppress blank-only entries that follow another blank entry
    const isBlank = text.trim().length === 0;
    if (isBlank && this.lastLineWasBlank) {
      return;
    }
    this.lastLineWasBlank = isBlank;

    // Detect and buffer reasoning lines (Issue #796)
    // Check after ANSI stripping and blank normalization, before content type detection
    if (!isBlank && !options?.collapsible && isReasoningLine(text)) {
      this.reasoningBuffer.push(text);
      return;
    }

    // Flush any buffered reasoning before adding a substantive line
    if (!isBlank && this.reasoningBuffer.length > 0) {
      this.flushReasoningBuffer(stage);
    }

    // Filter based on verbose level
    if (!this.shouldShowLevel(level)) {
      return;
    }

    // Auto-insert separator if issue number changed (Issue #303).
    // #307: for slot-attributed output resolve the issue number from the OWNING
    // slot's immutable record, not the shared `getIssueNumber()` scalar which
    // flips as sibling concurrent slots advance. This keeps the "#N " prefix and
    // the change-separator aligned with the disk log the entry actually lands in.
    const issueNumber =
      options?.slotIndex !== undefined
        ? (this.state.getSlotIssueNumber(options.slotIndex) ?? this.state.getIssueNumber())
        : this.state.getIssueNumber();
    const previousEntry = this.state.getPreviousEntry();

    if (
      issueNumber !== undefined &&
      previousEntry &&
      previousEntry.issueNumber !== undefined &&
      previousEntry.issueNumber !== issueNumber
    ) {
      // Issue number changed - insert separator. Attribute it to the
      // triggering line's slot so its disk write resolves the same log root
      // — an unattributed separator falls through to the active slot or the
      // bootstrap root and strands a stub file in the wrong repo (#216).
      this.insertIssueSeparator(options?.slotIndex);
    }

    // Auto-prefix with issue number if set (Issue #303)
    const prefixedText =
      issueNumber !== undefined ? this.formatIssuePrefix(issueNumber) + text : text;

    // Auto-detect content type if not provided (Issue #428)
    const contentType = options?.contentType ?? detectContentType(prefixedText);

    // Detect language for code blocks (Issue #639)
    const language =
      options?.language ?? (contentType === "code" ? detectLanguage(prefixedText) : undefined);

    // Build options with content type
    const entryOptions: {
      collapsible?: boolean;
      details?: string;
      contentType?: ContentType;
      language?: string;
      slotIndex?: number;
    } = {
      ...options,
      contentType: contentType !== "text" ? contentType : undefined,
      language: language !== "text" ? language : undefined,
    };

    // Auto-collapse code blocks at lower threshold (Issue #639)
    // Code blocks use CODE_COLLAPSE_THRESHOLD (8 lines) for cleaner output
    if (
      !entryOptions.collapsible &&
      contentType === "code" &&
      shouldCollapse(prefixedText, CODE_COLLAPSE_THRESHOLD)
    ) {
      const lineCount = prefixedText.split("\n").length;
      const langLabel = language && language !== "text" ? `, ${language}` : "";
      const summary = `Code block (${lineCount} lines${langLabel})`;
      entryOptions.collapsible = true;
      entryOptions.details = prefixedText;
      const entry = this.state.addEntry(summary, level, stage, entryOptions);

      this.postEntryToWebview(entry);
      return;
    }

    // Auto-collapse large content (Issue #428)
    // Only auto-collapse if not already marked collapsible
    if (!entryOptions.collapsible && shouldCollapse(prefixedText)) {
      const { summary, details } = createCollapsibleEntry(prefixedText);
      entryOptions.collapsible = true;
      entryOptions.details = details;
      // Use summary as the display text
      const entry = this.state.addEntry(summary, level, stage, entryOptions);

      // Send to WebView if panel is open
      this.postEntryToWebview(entry);
      return;
    }

    const entry = this.state.addEntry(prefixedText, level, stage, entryOptions);

    // Send to WebView if panel is open
    this.postEntryToWebview(entry);
  }

  /**
   * Log a tool call (legacy method - verbose format)
   *
   * @deprecated Use logToolIndicator() for animated indicators
   */
  logToolCall(tool: string, target: string, args?: Record<string, unknown>, result?: string): void {
    const argsStr = args ? JSON.stringify(args, null, 2) : undefined;
    const details = [
      argsStr ? `Arguments:\n${argsStr}` : null,
      result ? `Result:\n${result.substring(0, 500)}${result.length > 500 ? "..." : ""}` : null,
    ]
      .filter(Boolean)
      .join("\n\n");

    this.appendLine(`${tool}: ${target}`, "tool", undefined, {
      collapsible: !!details,
      details: details || undefined,
    });
  }

  /**
   * Log a tool call with animated indicator
   *
   * Displays an animated spinner while the tool is executing,
   * then marks it complete. Also aggregates tool usage for summary.
   *
   * Auto-completes the previous tool indicator when a new one arrives (Issue #170).
   *
   * @param toolData Structured tool call data from ToolCallIndicator
   * @param stage Optional pipeline stage for context
   */
  logToolIndicator(toolData: ToolCallData, stage?: PipelineStage): void {
    // Mark previous indicator as complete before adding new one (Issue #170)
    if (this.currentActiveToolId) {
      this.markToolComplete(this.currentActiveToolId);
    }

    // Add to aggregation for summary
    this.state.addToolCall(toolData.tool);

    // At verbose/debug level, also log the traditional format
    if (this.config.verboseLevel === "verbose" || this.config.verboseLevel === "debug") {
      const details = toolData.args
        ? `Arguments:\n${JSON.stringify(toolData.args, null, 2)}`
        : undefined;
      this.appendLine(formatToolIndicator(toolData), "tool", stage, {
        collapsible: !!details,
        details,
      });
    }

    // Send indicator to WebView
    if (this.panel) {
      this.panel.webview.postMessage(
        createToolIndicatorMessage(
          toolData.id,
          toolData.tool,
          toolData.target,
          toolData.isActive,
          toolData.startedAt
        )
      );
    }

    // Track new indicator as active (Issue #170)
    this.currentActiveToolId = toolData.id;
  }

  /**
   * Mark a tool indicator as complete
   *
   * @param toolId The unique ID of the tool call to mark complete
   */
  markToolComplete(toolId: string): void {
    if (this.panel) {
      this.panel.webview.postMessage(createToolIndicatorCompleteMessage(toolId));
    }
  }

  /**
   * Show a summary of tool usage for the current stage
   *
   * Called when a stage completes to display aggregate tool usage.
   * Also marks the final active tool indicator as complete (Issue #170).
   */
  showToolSummary(): void {
    // Mark final indicator as complete when stage ends (Issue #170)
    if (this.currentActiveToolId) {
      this.markToolComplete(this.currentActiveToolId);
      this.currentActiveToolId = null;
    }

    const summary = this.state.getToolSummary();

    // Only show if there were tool calls
    if (summary.total === 0) {
      return;
    }

    const formatted = formatToolSummary(summary);

    // Send summary to WebView
    if (this.panel) {
      this.panel.webview.postMessage(createToolSummaryMessage(summary, formatted));
    }

    // Reset for next stage
    this.state.resetToolCalls();
  }

  /**
   * Update stage status
   */
  updateStageStatus(stage: PipelineStage, status: StageStatus): void {
    // Flush reasoning buffer on stage completion (Issue #796)
    if (status === "complete" || status === "error" || status === "skipped") {
      this.flushReasoningBuffer(stage);
    }

    // When a stage starts (or restarts), clear its previous output
    // so retried stages show fresh logs instead of stale entries
    if (status === "running") {
      this.state.resetToolCalls();
      this.clearStageOutput(stage);
    }

    this.state.updateStageStatus(stage, status);

    // Collapse completed stage entries into a <details> group so past
    // stages take minimal vertical space while the active stage stays open.
    if ((status === "complete" || status === "error" || status === "skipped") && this.panel) {
      this.panel.webview.postMessage(createCollapseStageMessage(stage, status));
    }

    // Note: No auto-log here — all callers already emit their own
    // status message via appendLine() with proper stage labels (e.g.,
    // "Feature Planning" instead of raw "feature-planning").
    // @see Issue #941 — duplicate completion messages
  }

  /**
   * Display a model selection decision in the output panel (Issue #732)
   *
   * Surfaces the AutoModelSelector decision with model, source, confidence,
   * and reasoning for observability.
   *
   * @param stage - Pipeline stage the decision is for
   * @param model - Selected model name
   * @param source - Where the model was resolved from ('env', 'config', 'auto', 'default')
   * @param reasoning - Optional reasoning string from AutoModelSelector
   */
  appendModelDecision(
    stage: PipelineStage,
    model: string,
    source: string,
    reasoning?: string
  ): void {
    const reasoningSuffix = reasoning ? ` — ${reasoning}` : "";
    this.appendLine(`Model: ${model} (source: ${source})${reasoningSuffix}`, "info", stage);
  }

  /**
   * Set the issue number for the current pipeline run
   */
  setIssueNumber(issueNumber: number): void {
    this.state.setIssueNumber(issueNumber);
    this.updatePanel();
    this.maybeReplayPersistedLog(); // Trigger replay if all conditions met (Issue #1352)
  }

  // =========================================
  // Concurrent slot tab methods (Issue #2705)
  // =========================================

  /**
   * Register a concurrent pipeline slot for tab display.
   *
   * Called from services.ts after SlotOutputManager.createSlotChannel().
   * Idempotent — safe to call multiple times for the same slot.
   *
   * @param slotIndex  - 0-based slot index
   * @param issueNumber - Issue number being processed in this slot
   * @param title      - Issue title for the tab label
   */
  registerSlotInfo(slotIndex: number, issueNumber: number, title: string, repoSlug?: string): void {
    this.state.registerSlot(slotIndex, issueNumber, title, repoSlug);
    this.updatePanel();
  }

  /** Scope a slot's disk session log to its run's target repo (#191). */
  setSlotLogRoot(slotIndex: number, repoRoot: string | null): void {
    this.state.setSlotLogRoot(slotIndex, repoRoot);
  }

  /** Scope the sequential run's disk session log to its target repo (#191). */
  setRunLogRoot(repoRoot: string | null): void {
    this.state.setRunLogRoot(repoRoot);
  }

  /**
   * Update the stage label shown in a slot's tab.
   *
   * Called from services.ts when a slot's stage changes.
   */
  updateSlotStage(slotIndex: number, stage: PipelineStage): void {
    this.state.updateSlotStage(slotIndex, stage);
    this.updatePanel();
  }

  /**
   * Notify the window that a slot's pipeline has reached a terminal state.
   *
   * Stamps the slot's SlotInfo with the terminal status, completion timestamp,
   * and the authoritative final cost, then pushes a `slot-badge-update` to the
   * webview so the tab badge flips immediately from the running spinner to
   * the terminal state. Without this, the ConcurrentPipelineManager's
   * onSlotCompleted/onSlotFailed callbacks never tell the badge to update,
   * leaving finished slots stuck on the mid-run spinner and partial cost.
   *
   * If the slot has already been removed (user closed the tab before completion)
   * this is a no-op — we don't recreate the slot just to mark it complete.
   *
   * @param slotIndex  - 0-based slot index
   * @param status     - Terminal status ("complete" or "error")
   * @param costUsd    - Authoritative final cost in USD (from the pipeline callback)
   * @param completedAt - Epoch ms timestamp; defaults to Date.now()
   */
  notifySlotCompleted(
    slotIndex: number,
    status: "complete" | "error",
    costUsd: number,
    completedAt: number = Date.now()
  ): void {
    // Guard: if the slot was removed (e.g., user closed the tab), do nothing —
    // never recreate a slot just to mark it finished.
    const existing = this.state.getActiveSlots().find((s) => s.slotIndex === slotIndex);
    if (!existing) return;

    // Stamp the authoritative final cost onto the slot without clobbering the
    // other token totals (input/output/cache), which the badge doesn't render
    // but are preserved for post-reload rehydration.
    const current = this.state.getSlotTokenUsage(slotIndex);
    this.state.setSlotTokenUsage(slotIndex, {
      inputTokens: current.inputTokens,
      outputTokens: current.outputTokens,
      cacheReadTokens: current.cacheReadTokens,
      cacheCreationTokens: current.cacheCreationTokens,
      costUsd,
    });

    // Stamp status and completedAt on the in-memory SlotInfo so any
    // subsequent updatePanel() (e.g., tab switch) re-renders with the final
    // state. Slot metadata is not persisted to workspaceState; post-reload
    // rehydration comes from on-disk logs.
    this.state.updateSlotStatus(slotIndex, status, completedAt);

    this.sendSlotBadgeUpdate(slotIndex);
  }

  /**
   * Set the active slot tab (called from the tab:switch message handler).
   *
   * @param slotIndex - Slot index, or null for the "All" aggregated tab
   */
  setActiveSlot(slotIndex: number | null): void {
    this.state.setActiveSlot(slotIndex);
    // No full updatePanel needed — tab switch is CSS-driven client-side.
    // State is tracked here so the active tab is preserved on re-opens.
  }

  /**
   * Handle tab:switch message from WebView (Issue #2705)
   */
  private handleTabSwitch(slotIndex: number | null): void {
    this.setActiveSlot(slotIndex);
  }

  /**
   * Trigger log replay if all conditions are met
   *
   * Conditions:
   * 1. Panel is showing (was just created)
   * 2. issueNumber is known
   * 3. workspaceRoot is known (setLogConfig was called)
   * 4. Memento entries are empty (no data to duplicate)
   * 5. Has not already replayed this session
   *
   * @see Issue #1352
   */
  private maybeReplayPersistedLog(): void {
    if (this.hasReplayed) return;
    if (!this.panel) return;
    if (!this.replayWorkspaceRoot) return;
    const issueNumber = this.state.getIssueNumber();
    if (!issueNumber) return;
    if (this.state.getEntryCount() > 0) return; // Memento has entries, skip

    this.hasReplayed = true;
    this.replayPersistedLog(issueNumber).catch(() => {
      // Non-critical: replay failure should not disrupt the UI
    });
  }

  /**
   * Replay persisted log entries from disk into the output panel
   *
   * Called async from maybeReplayPersistedLog(). Reads the session log file
   * for the current issue, adds a visual separator, then replays each entry
   * into the state and WebView. Entries are saved to Memento (skipDiskWrite=true)
   * so they survive future panel close/open without re-reading disk.
   *
   * @param issueNumber - The current issue number
   * @see Issue #1352
   */
  private async replayPersistedLog(issueNumber: number): Promise<void> {
    const logEntries = await LogFileWriter.readEntriesForIssue(
      this.replayWorkspaceRoot!,
      issueNumber,
      this.replayLogConfig ?? undefined
    );

    if (logEntries.length === 0) return; // No log file — no-op (AC3)

    // Add separator entry (visual distinction between historical and live output)
    const separatorEntry = this.state.addEntry(
      "── Resumed from log (prior to reload) ──",
      "info",
      undefined,
      { skipDiskWrite: true }
    );
    this.postEntryToWebview(separatorEntry);

    // Level map from log file format to OutputLevel
    const levelMap: Record<string, OutputLevel> = {
      INFO: "info",
      DEBUG: "debug",
      WARNING: "warning",
      ERROR: "error",
      TOOL: "tool",
      USER: "user",
    };

    // Replay each log entry
    for (const logEntry of logEntries) {
      const level: OutputLevel = levelMap[logEntry.level.toUpperCase()] ?? "info";
      const entry = this.state.addEntry(
        logEntry.text,
        level,
        logEntry.stage as PipelineStage | undefined,
        { skipDiskWrite: true }
      );
      this.postEntryToWebview(entry);
    }
  }

  /**
   * Trigger cross-session tab rehydration from on-disk logs (Issue #2818).
   *
   * Conditions:
   * 1. Panel is showing
   * 2. `config.rehydrateFromLogs` switch is enabled
   * 3. workspaceRoot is known (setLogConfig was called)
   * 4. Has not already rehydrated this session
   */
  private maybeRehydrateFromLogs(): void {
    if (this.hasRehydrated) return;
    if (!this.panel) return;
    if (!this.config.rehydrateFromLogs) return;
    if (!this.replayWorkspaceRoot) return;

    this.hasRehydrated = true;
    this.rehydrateFromLogs().catch(() => {
      // Non-critical: rehydration failure should not disrupt the UI
    });
  }

  /**
   * Rebuild archived tabs from on-disk session logs.
   *
   * Enumerates all eligible session logs via {@link LogFileWriter.listLogs},
   * skips any whose issue is already registered as a running slot, and
   * registers each remaining log as an archived slot with its per-slot
   * buffer populated from the log contents. Archived tabs render an
   * "Archived" chip so users can distinguish resumed runs from live ones.
   *
   * @see Issue #2818 - Rehydrate output window tabs from disk logs on restart
   */
  private async rehydrateFromLogs(): Promise<void> {
    const userConfig = this.replayLogConfig ?? {};
    const rehydrateConfig = {
      ...userConfig,
      max_count: userConfig.max_count ?? OutputWindow.REHYDRATE_DEFAULT_MAX_COUNT,
      max_age_days: userConfig.max_age_days ?? OutputWindow.REHYDRATE_DEFAULT_MAX_AGE_DAYS,
    };
    const descriptors = await LogFileWriter.listLogs(this.replayWorkspaceRoot!, rehydrateConfig);
    if (descriptors.length === 0) return;

    const levelMap: Record<string, OutputLevel> = {
      INFO: "info",
      DEBUG: "debug",
      WARNING: "warning",
      ERROR: "error",
      TOOL: "tool",
      USER: "user",
    };

    let rehydratedAny = false;
    for (const descriptor of descriptors) {
      // Dedup: skip if a running slot already covers this issue
      const existingSlot = this.state.findSlotIndexByIssue(descriptor.issueNumber);
      if (existingSlot !== undefined && this.state.isSlotRunning(existingSlot)) {
        continue;
      }
      // Dedup: skip if any archived slot already exists for this issue
      if (existingSlot !== undefined) continue;

      const entries = await LogFileWriter.readLog(descriptor.filePath);
      if (entries.length === 0) continue;

      const slotIndex = this.state.getNextSlotIndex();
      const title = `Issue #${descriptor.issueNumber}`;
      this.state.registerArchivedSlot(slotIndex, descriptor.issueNumber, title);

      // Rehydrate token totals from execution history (Issue #3708)
      try {
        const historyRecords = await ExecutionHistoryReader.readForIssue(
          this.replayWorkspaceRoot!,
          descriptor.issueNumber
        );
        const runRecords = historyRecords.filter((r) => r.record_type === "run");
        if (runRecords.length > 0) {
          const latest = runRecords[runRecords.length - 1];
          const t = latest.tokens;
          this.state.setSlotTokenUsage(slotIndex, {
            inputTokens: t.total_input,
            outputTokens: t.total_output,
            cacheReadTokens: t.total_cache_read,
            cacheCreationTokens: t.total_cache_creation,
            costUsd: t.estimated_cost_usd,
          });
        }
      } catch {
        // Best-effort — slot remains at zeros if history is unavailable
      }

      for (const logEntry of entries) {
        const level: OutputLevel = levelMap[logEntry.level.toUpperCase()] ?? "info";
        this.state.addEntry(logEntry.text, level, logEntry.stage as PipelineStage | undefined, {
          skipDiskWrite: true,
          slotIndex,
        });
      }
      rehydratedAny = true;
    }

    if (rehydratedAny) {
      this.updatePanel();
    }
  }

  /**
   * Format issue number prefix (Issue #303)
   *
   * @param issueNumber The issue number to format
   * @returns Formatted prefix like "[#123] "
   */
  private formatIssuePrefix(issueNumber: number): string {
    return `[#${issueNumber}] `;
  }

  /**
   * Insert a visual separator line between issues (Issue #303)
   *
   * Called automatically when issue number changes from previous entry.
   * Uses box-drawing characters for ASCII compatibility.
   */
  private insertIssueSeparator(slotIndex?: number): void {
    const separatorLine = "═".repeat(60);

    // Prevent duplicate separator lines (Issue #794)
    const previousEntry = this.state.getPreviousEntry();
    if (previousEntry?.text === separatorLine) {
      return;
    }

    // Don't auto-prefix the separator itself
    const entry = this.state.addEntry(
      separatorLine,
      "info",
      undefined,
      slotIndex !== undefined ? { slotIndex } : undefined
    );

    // Send to WebView if panel is open
    this.postEntryToWebview(entry);
  }

  /**
   * Clear the active slot, or everything when the "All" tab is active (Issue #2814).
   *
   * When a slot tab is active, only that slot's entries/stages/tokens are cleared.
   * When the "All" aggregated tab is active, delegates to full clear() for
   * backward compatibility (Wave 3 will replace "All" with an overview dashboard).
   */
  private clearActive(): void {
    const activeSlot = this.state.getActiveSlotIndex();
    if (activeSlot !== null) {
      this.state.clearSlot(activeSlot);
      if (this.panel) {
        this.panel.webview.postMessage(createClearMessage());
      }
      this.updatePanel();
    } else {
      this.clear();
    }
  }

  /**
   * Clear all output
   */
  clear(): void {
    this.hasReplayed = false; // Allow replay for next issue (Issue #1352)
    this.state.clear();
    this.currentActiveToolId = null; // Reset tool indicator tracking (Issue #170)
    this.lastLineWasBlank = false; // Reset blank line tracking (Issue #794)
    this.reasoningBuffer = []; // Reset reasoning buffer (Issue #796)
    if (this.panel) {
      this.panel.webview.postMessage(createClearMessage());
    }
    this.updatePanel();
  }

  /**
   * Clear all output with confirmation for large output
   *
   * Shows a confirmation dialog if there are more than 1000 entries
   * to prevent accidental loss of substantial output.
   * Intended for use by the command palette command.
   *
   * @see Issue #157 - Add clear/reset button to Nightgauge Output window
   */
  async clearWithConfirmation(): Promise<void> {
    const entryCount = this.state.getEntryCount();

    // Show confirmation for substantial output
    if (entryCount > OutputWindow.CLEAR_CONFIRMATION_THRESHOLD) {
      const result = await vscode.window.showWarningMessage(
        `Clear ${entryCount.toLocaleString()} output entries?`,
        { modal: true },
        "Clear All",
        "Cancel"
      );

      if (result !== "Clear All") {
        return; // User cancelled
      }
    }

    this.clearActive();
  }

  /**
   * Clear output entries for a specific stage
   *
   * Used when retrying a failed stage so the output window shows
   * fresh output instead of stale logs from the previous attempt.
   */
  clearStageOutput(stage: PipelineStage): void {
    this.state.clearStageEntries(stage);
    if (this.panel) {
      this.panel.webview.postMessage(createClearStageMessage(stage));
    }
  }

  /**
   * Remove stall warning entries for a stage (Issue #797)
   *
   * Called when a stage completes after showing stall warnings.
   * Removes the transient "[skillRunner] Stage still running after..." entries
   * from the output since they are no longer relevant.
   *
   * @param stage The pipeline stage whose stall warnings should be removed
   */
  removeStallWarnings(stage: PipelineStage): void {
    this.state.removeStallWarningEntries(stage);
    if (this.panel) {
      this.panel.webview.postMessage(createRemoveStallWarningsMessage(stage));
    }
  }

  /**
   * Add a stall warning entry to the output window (Issue #2655)
   *
   * Called when a stage exceeds its stall threshold at 1×, 2×, or 3× escalation.
   * Appends a timestamped warning entry to the output and notifies the WebView
   * with multiplier info for escalation-aware rendering.
   *
   * @param stage - The pipeline stage that is stalled
   * @param elapsedMs - Elapsed time in milliseconds since stage started
   * @param thresholdMs - Configured stall threshold in milliseconds
   * @param multiplier - Escalation level (1 = first warning, 2 = 2×, 3 = 3×, etc.)
   */
  addStallWarning(
    stage: PipelineStage,
    elapsedMs: number,
    thresholdMs: number,
    multiplier: number
  ): void {
    const elapsedSec = Math.floor(elapsedMs / 1000);
    const thresholdSec = Math.floor(thresholdMs / 1000);
    const label = multiplier <= 1 ? "Stall Warning:" : `⚠️ Stall Warning (${multiplier}×):`;
    this.appendLine(
      `${label} Stage has been running for ${elapsedSec}s (threshold: ${thresholdSec}s)`,
      "warning",
      stage
    );
    if (this.panel) {
      this.panel.webview.postMessage(
        createAddStallWarningMessage(stage, elapsedMs, thresholdMs, multiplier)
      );
    }
  }

  /**
   * Check if the panel is currently visible
   */
  isVisible(): boolean {
    return this.panel?.visible ?? false;
  }

  /**
   * Get the current state (for testing or external access)
   */
  getState(): OutputWindowState {
    return this.state;
  }

  /**
   * Update configuration
   */
  setConfig(config: Partial<OutputWindowConfig>): void {
    this.config = { ...this.config, ...config };
    if (config.autoScroll !== undefined) {
      this.state.setAutoScroll(config.autoScroll);
      if (this.panel) {
        this.panel.webview.postMessage(createAutoScrollMessage(config.autoScroll));
      }
    }
    if (config.wordWrap !== undefined) {
      this.state.setWordWrap(config.wordWrap);
      if (this.panel) {
        this.panel.webview.postMessage(createWordWrapMessage(config.wordWrap));
      }
    }
  }

  /**
   * Dispose of the output window and clean up resources
   */
  dispose(): void {
    // Flush any pending debounced state persistence before teardown so the
    // final window of entries/tool calls isn't lost on extension host death.
    void this.state.flush().catch(() => {
      /* best-effort — dispose must not throw */
    });
    this.state.dispose();

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

  /**
   * Update the panel content
   */
  private updatePanel(): void {
    if (!this.panel) return;

    const searchState: SearchState = {
      searchText: this.state.getSearchText(),
      caseSensitive: this.state.getSearchCaseSensitive(),
      useRegex: this.state.getSearchUseRegex(),
    };

    // Build per-slot entry and stage maps for multi-slot tab panel rendering (Issue #2705, #2814)
    const activeSlots: SlotInfo[] = this.state.getActiveSlots();
    const slotEntries = new Map<number, OutputEntry[]>();
    const slotStages = new Map<number, import("./OutputWindowState").StageProgress[]>();
    for (const slot of activeSlots) {
      slotEntries.set(slot.slotIndex, this.state.getSlotEntries(slot.slotIndex));
      slotStages.set(slot.slotIndex, this.state.getSlotStageProgress(slot.slotIndex));
    }

    this.panel.webview.html = getOutputWindowHtml(
      this.panel.webview,
      this.state.getEntries(),
      this.state.getAllStageProgress(),
      this.state.getAutoScroll(),
      this.state.getWordWrap(),
      this.state.getShowTimestamps(),
      this.state.getIssueNumber(),
      searchState,
      activeSlots,
      this.state.getActiveSlotIndex(),
      slotEntries,
      slotStages
    );
  }

  /**
   * Check if a log level should be shown based on verbosity setting
   */
  private shouldShowLevel(level: OutputLevel): boolean {
    const levelPriority: Record<OutputLevel, number> = {
      error: 0,
      warning: 1,
      info: 2,
      tool: 3,
      user: 3,
      debug: 4,
    };

    const configPriority: Record<string, number> = {
      minimal: 1,
      normal: 2,
      verbose: 3,
      debug: 4,
    };

    return levelPriority[level] <= configPriority[this.config.verboseLevel];
  }

  /**
   * Handle interrupt request from WebView
   *
   * Delegates to the abortPipeline command which performs full cleanup:
   * stop orchestrator, clear state.json, reset TreeView/StatusBar,
   * update VS Code context variables, and reset GitHub issue status.
   *
   * @see Issue #851 - Stop button leaves pipeline in zombie state
   * @see src/commands/abortPipeline.ts
   */
  private handleInterrupt(): void {
    if (this.orchestrator?.getIsRunning()) {
      this.appendLine("Pipeline stop requested — running full abort cleanup", "warning");
      vscode.commands.executeCommand("nightgauge.abortPipeline");
    } else {
      vscode.window.showInformationMessage("No pipeline is currently running");
    }
  }

  /**
   * Threshold for showing confirmation dialog before clearing output
   * @see Issue #157 - Clear output confirmation
   */
  private static readonly CLEAR_CONFIRMATION_THRESHOLD = 1000;

  /**
   * Handle clear logs request from WebView
   *
   * Shows a confirmation dialog if there are more than 1000 entries
   * to prevent accidental loss of substantial output.
   * @see Issue #157 - Add clear/reset button to Nightgauge Output window
   */
  private async handleClearLogs(): Promise<void> {
    const entryCount = this.state.getEntryCount();

    // Show confirmation for substantial output
    if (entryCount > OutputWindow.CLEAR_CONFIRMATION_THRESHOLD) {
      const result = await vscode.window.showWarningMessage(
        `Clear ${entryCount.toLocaleString()} output entries?`,
        { modal: true },
        "Clear All",
        "Cancel"
      );

      if (result !== "Clear All") {
        return; // User cancelled
      }
    }

    this.clearActive();
  }

  /**
   * Handle copy to clipboard request from WebView (Issue #156)
   *
   * Copies all output content to the system clipboard as plain text.
   * Provides visual feedback via toast notification on success or failure.
   */
  private async handleCopyToClipboard(): Promise<void> {
    const content = this.exportAsTxt();

    if (!content || content.trim().length === 0) {
      vscode.window.showInformationMessage("No output to copy");
      return;
    }

    try {
      await vscode.env.clipboard.writeText(content);
      vscode.window.showInformationMessage("Output copied to clipboard");
    } catch (error) {
      vscode.window.showErrorMessage(
        `Failed to copy to clipboard: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }

  /**
   * Copy output to clipboard (command handler for Issue #156)
   *
   * Can be called from command palette or keybinding.
   * Wraps handleCopyToClipboard for external access.
   */
  async copyToClipboard(): Promise<void> {
    await this.handleCopyToClipboard();
  }

  /**
   * Handle export request from WebView
   */
  private async handleExport(format: "txt" | "json"): Promise<void> {
    const content = format === "json" ? this.state.exportAsJson() : this.exportAsTxt();

    const extension = format;
    const issueNumber = this.state.getIssueNumber();
    const filename = issueNumber
      ? `nightgauge-output-${issueNumber}.${extension}`
      : `nightgauge-output.${extension}`;

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
   * Export output as plain text
   */
  private exportAsTxt(): string {
    const entries = this.state.getEntries();
    return entries
      .map((entry) => {
        const timestamp = entry.timestamp.toLocaleTimeString("en-US", {
          hour: "2-digit",
          minute: "2-digit",
          second: "2-digit",
          hour12: false,
        });
        const stage = entry.stage ? `[${entry.stage}]` : "";
        const level = `[${entry.level.toUpperCase()}]`;
        let line = `${timestamp} ${level} ${stage} ${entry.text}`;
        if (entry.details) {
          line += `\n  ${entry.details.replace(/\n/g, "\n  ")}`;
        }
        return line;
      })
      .join("\n");
  }

  /**
   * Handle toggle auto-scroll from WebView
   */
  private handleToggleAutoScroll(enabled: boolean): void {
    this.state.setAutoScroll(enabled);
    this.config.autoScroll = enabled;
  }

  /**
   * Handle toggle word wrap from WebView (Issue #161)
   */
  private handleToggleWordWrap(enabled: boolean): void {
    this.state.setWordWrap(enabled);
    this.config.wordWrap = enabled;
  }

  /**
   * Handle toggle timestamps from WebView (Issue #160)
   */
  private handleToggleTimestamps(enabled: boolean): void {
    this.state.setShowTimestamps(enabled);
  }

  /**
   * Handle search text change from WebView (Issue #158)
   */
  private handleSearchTextChange(text: string): void {
    this.state.setSearchText(text);
  }

  /**
   * Handle toggle case-sensitive search from WebView (Issue #158)
   */
  private handleToggleSearchCaseSensitive(enabled: boolean): void {
    this.state.setSearchCaseSensitive(enabled);
  }

  /**
   * Handle toggle regex search from WebView (Issue #158)
   */
  private handleToggleSearchUseRegex(enabled: boolean): void {
    this.state.setSearchUseRegex(enabled);
  }

  /**
   * Handle toggle entry collapsed state from WebView
   */
  private handleToggleEntry(entryId: string): void {
    this.state.toggleEntryCollapsed(entryId);
  }

  /**
   * Handle panel closed by user
   */
  private handlePanelClosed(): void {
    this.panel = undefined;
    // Clean up disposables specific to this panel
  }

  // ===== Interactive Message Support (Issue #497) =====

  /**
   * Handle user sending a message to the running interactive agent
   *
   * This method is called when the user types a message in the input field
   * and sends it. Only works in interactive mode when an interactive process
   * is running with open stdin.
   *
   * @param text - The message text from the user
   * @returns true if message was sent successfully, false otherwise
   */
  private handleSendMessage(text: string): boolean {
    // Validate input
    if (!text || text.trim().length === 0) {
      return false;
    }

    // Check if we're in interactive mode
    const mode = this.state.getExecutionMode();
    if (mode !== "interactive") {
      console.warn("[OutputWindow] Cannot send message: not in interactive mode");
      this.sendMessageFeedback(false, "Not in interactive mode");
      return false;
    }

    // Get the active interactive process
    const handle = getActiveInteractiveProcess();
    if (!handle) {
      console.warn("[OutputWindow] Cannot send message: no interactive process running");
      this.sendMessageFeedback(false, "No interactive process running");
      return false;
    }

    // Write to stdin
    const success = handle.writeToStdin?.(text) ?? false;

    if (success) {
      // Log the user message in the output window
      this.appendLine(`→ ${text}`, "user");
      this.sendMessageFeedback(true);
    } else {
      console.warn("[OutputWindow] Failed to write to stdin");
      this.sendMessageFeedback(false, "Failed to send message");
    }

    return success;
  }

  /**
   * Send feedback to WebView about message send result
   *
   * @param success - Whether the message was sent successfully
   * @param error - Optional error message to display
   */
  private sendMessageFeedback(success: boolean, error?: string): void {
    if (this.panel) {
      this.panel.webview.postMessage({
        type: "message-sent-feedback",
        success,
        error,
      });
    }
  }

  // ===== AskUserQuestion Support (Issue #118) =====

  /**
   * Display an AskUserQuestion prompt and wait for user response
   *
   * This method is called when the AskUserQuestion tool is detected in
   * the Claude CLI stream-json output. It displays the question in the
   * OutputWindow and returns a Promise that resolves when the user responds.
   *
   * @param payload - The AskUserQuestion payload from Claude
   * @param toolUseId - Optional tool_use ID for matching tool_result
   * @returns Promise that resolves with the user's response, or null if cancelled
   */
  showQuestionPrompt(
    payload: AskUserQuestionPayload,
    toolUseId?: string
  ): Promise<QuestionResponse | null> {
    return new Promise((resolve) => {
      const id = generateQuestionId();

      // Store active question state
      this.activeQuestion = {
        id,
        payload,
        toolUseId,
        displayedAt: new Date(),
        resolve,
      };

      // Log that we're waiting for input
      this.appendLine("Waiting for user input...", "info");

      // Send question prompt to WebView
      if (this.panel) {
        this.panel.webview.postMessage(createQuestionPromptMessage(id, payload, toolUseId));
      } else {
        // Panel not open - resolve with null (skip)
        console.warn("OutputWindow panel not open when AskUserQuestion received");
        this.activeQuestion = null;
        resolve(null);
      }
    });
  }

  /**
   * Handle user response to a question prompt
   *
   * Called by the message handler when the WebView sends a question-response message.
   *
   * @param questionId - The question ID that was answered
   * @param response - The user's response, or null if cancelled/skipped
   */
  private handleQuestionResponse(questionId: string, response: QuestionResponse | null): void {
    if (!this.activeQuestion || this.activeQuestion.id !== questionId) {
      console.warn(`Received response for unknown question: ${questionId}`);
      return;
    }

    const { resolve } = this.activeQuestion;

    // Update WebView to show answered state
    if (this.panel) {
      this.panel.webview.postMessage(createQuestionAnsweredMessage(questionId, response));
    }

    // Log the response
    if (response) {
      const answerSummary = Object.values(response.answers).flat().join(", ");
      this.appendLine(`→ User responded: ${answerSummary}`, "user");
    } else {
      this.appendLine("→ User skipped question", "user");
    }

    // Clear active question state
    this.activeQuestion = null;

    // Resolve the promise to continue pipeline execution
    resolve(response);
  }

  /**
   * Check if there is an active question waiting for response
   */
  hasActiveQuestion(): boolean {
    return this.activeQuestion !== null;
  }

  /**
   * Get the active question state (for testing or debugging)
   */
  getActiveQuestion(): ActiveQuestionState | null {
    return this.activeQuestion;
  }

  /**
   * Cancel the current active question
   * Used when the pipeline is interrupted or times out
   */
  cancelActiveQuestion(): void {
    if (this.activeQuestion) {
      const { id, resolve } = this.activeQuestion;

      // Update WebView
      if (this.panel) {
        this.panel.webview.postMessage(createQuestionAnsweredMessage(id, null));
      }

      this.appendLine("→ Question cancelled", "warning");
      this.activeQuestion = null;
      resolve(null);
    }
  }
}
