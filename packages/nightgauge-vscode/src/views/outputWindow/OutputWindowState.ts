/**
 * OutputWindowState - State management for the output window
 *
 * Manages output entries, auto-scroll preferences, and session history.
 * Persists state to workspace storage for cross-session access.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { ToolType, ToolCallSummary } from "./ToolCallIndicator";
import {
  LogFileWriter,
  DEFAULT_DISK_LOG_MAX_ENTRY_CHARS,
  type LogFileConfig,
} from "../../utils/log-file-writer";
import type { PipelineLogsConfig } from "../settings/types";
import type { ContentType } from "./contentFormatter";
import { redactSecrets } from "../../utils/redaction";

/**
 * Execution mode for the output window
 *
 * - 'headless': Automated pipeline execution with stream-json output and token tracking
 * - 'interactive': Conversational execution with raw text output and user input support
 *
 * The mode is inferred from execution context (determined by orchestrator/skillRunner),
 * not a user preference. Token tracking is only available in headless mode.
 *
 * @see docs/INTERACTIVE_MODE.md
 * @see Issue #496 - Dual-mode output window rendering
 */
export type ExecutionMode = "headless" | "interactive";

/**
 * Output entry level/type
 */
export type OutputLevel = "info" | "debug" | "warning" | "error" | "tool" | "user";

/**
 * Stage status for progress display
 */
export type StageStatus = "pending" | "running" | "complete" | "error" | "skipped";

/**
 * Single output entry
 */
export interface OutputEntry {
  id: string;
  timestamp: Date;
  level: OutputLevel;
  stage?: PipelineStage;
  text: string;
  collapsible?: boolean;
  collapsed?: boolean;
  details?: string;
  /** Issue number for multi-issue pipeline tracking (Issue #303) */
  issueNumber?: number;
  /** Content type for formatting (Issue #428) */
  contentType?: ContentType;
  /** Language hint for syntax highlighting (Issue #428) */
  language?: string;
  /** Slot index for concurrent pipeline tab routing (Issue #2705) */
  slotIndex?: number;
}

/**
 * Per-slot info for tab bar rendering (Issue #2705, #2815)
 */
export interface SlotInfo {
  slotIndex: number;
  issueNumber: number;
  title: string;
  repoSlug?: string;
  stage?: PipelineStage;
  /** Per-slot stage progress — independent from global stages (Issue #2814) */
  stages: Map<PipelineStage, StageProgress>;
  /** Per-slot cost totals — independent from global tokenUsage (Issue #2814) */
  tokenUsage: TokenUsage;
  /** Derived pipeline status for badge display (Issue #2815) */
  status?: StageStatus;
  /** Epoch ms when this slot started — JSON-safe, no Date serialization (Issue #2815) */
  startedAt?: number;
  /** Epoch ms when this slot completed or failed, null if still running (Issue #2815) */
  completedAt?: number | null;
  /**
   * Active in-stage phase, surfaced on the Overview card (Issue #3010).
   * Mirrors `SlotPhaseSummary` from the Dashboard side; defined inline so
   * the Output module stays self-contained.
   */
  currentPhase?: { name: string; index: number; total: number };
  /**
   * True when this slot was rebuilt from an on-disk log (Issue #2818).
   * Archived slots are not running, are not writable, and render an
   * "archived" chip in the tab header.
   */
  archived?: boolean;
}

/**
 * Token usage statistics
 */
export interface TokenUsage {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
}

/**
 * Stage progress tracking
 */
export interface StageProgress {
  stage: PipelineStage;
  status: StageStatus;
  startedAt?: Date;
  completedAt?: Date;
  durationMs?: number;
}

/**
 * Webview DOM eviction event. Emitted when in-memory entry caps force trimming
 * so the webview can remove the corresponding oldest DOM nodes. Without this,
 * the renderer process accumulates unbounded DOM across a pipeline.
 *
 * - `scope: "aggregate"` → remove from the "All" panel (data-slot="null")
 * - `scope: "slot"` → remove from the slot-specific panel
 * - `scope: "slot-cleared"` → wipe all entries in the slot panel
 */
export type OutputEviction =
  | { scope: "aggregate"; count: number }
  | { scope: "slot"; slotIndex: number; count: number }
  | { scope: "slot-cleared"; slotIndex: number };

/**
 * Serializable version of output state for persistence
 */
interface SerializedOutputState {
  entries: {
    id: string;
    timestamp: string;
    level: OutputLevel;
    stage?: PipelineStage;
    text: string;
    collapsible?: boolean;
    collapsed?: boolean;
    details?: string;
    issueNumber?: number;
    contentType?: ContentType;
    language?: string;
  }[];
  stages: {
    stage: PipelineStage;
    status: StageStatus;
    startedAt?: string;
    completedAt?: string;
    durationMs?: number;
  }[];
  tokenUsage: TokenUsage;
  autoScroll: boolean;
  wordWrap: boolean;
  showTimestamps: boolean;
  issueNumber?: number;
  toolCallCounts?: Record<string, number>;
  toolCallStartedAt?: string;
  /** Search state for output filtering (Issue #158) */
  searchText?: string;
  searchCaseSensitive?: boolean;
  searchUseRegex?: boolean;
  /** Execution mode for dual-mode rendering (Issue #496) */
  executionMode?: ExecutionMode;
  /** Pinned slot indices for persistence (Issue #2816) */
  pinnedSlots?: number[];
}

/**
 * Storage key for output state
 */
const STATE_STORAGE_KEY = "nightgauge.outputWindow.state";

/**
 * Maximum number of entries to keep
 */
const MAX_ENTRIES = 500;

/**
 * Debounce window for persisting state to `workspaceState`.
 *
 * High-frequency callers (`addEntry`, `addToolCall`, `setTokenUsage`) fire
 * many times per second during active pipelines. Persisting on every
 * mutation previously forced VSCode to serialize and disk-flush the full
 * 500-entry state on each call — enough synchronous work to trip VSCode's
 * UNRESPONSIVE detector and kill the extension host.
 */
const SAVE_DEBOUNCE_MS = 500;

/**
 * Per-entry text cap applied only when persisting to `workspaceState`.
 *
 * In-memory entries keep their full content for the webview. The cap only
 * guards the memento against a pathological log-dump entry inflating
 * persisted state past the `mainThreadStorage` warning threshold.
 */
const MAX_PERSISTED_ENTRY_BYTES = 16 * 1024;

/**
 * All pipeline stages in order (including bookend stages)
 *
 * IMPORTANT: This must align with PipelineTreeProvider.STAGE_ORDER and
 * StatusBarManager stage lists for consistent UI display across components.
 *
 * @see Issue #284 - OutputWindow status chips for bookend stages
 */
export const PIPELINE_STAGES: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/**
 * OutputWindowState class for managing output window data
 *
 * @example
 * ```typescript
 * const state = new OutputWindowState(context.workspaceState);
 *
 * // Add output entries
 * state.addEntry('Starting pipeline...', 'info', 'issue-pickup');
 *
 * // Update stage status
 * state.updateStageStatus('issue-pickup', 'running');
 *
 * // Get all entries
 * const entries = state.getEntries();
 * ```
 */
export class OutputWindowState {
  private entries: OutputEntry[] = [];
  private stages: Map<PipelineStage, StageProgress> = new Map();
  private tokenUsage: TokenUsage = {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd: 0,
  };
  private autoScroll = true;
  private wordWrap = true;
  private showTimestamps = true;
  private issueNumber: number | undefined;
  private workspaceState: vscode.Memento | null = null;

  // Tool call aggregation for indicators
  private toolCallCounts: Map<ToolType, number> = new Map();
  private toolCallStartedAt: Date | null = null;

  // Search state (Issue #158)
  private searchText = "";
  private searchCaseSensitive = false;
  private searchUseRegex = false;

  // Execution mode for dual-mode rendering (Issue #496)
  // Default to 'headless' for backward compatibility
  private executionMode: ExecutionMode = "headless";

  // Per-slot buffer state (Issue #2705)
  private perSlotBuffers: Map<number, OutputEntry[]> = new Map(); // keyed by slotIndex
  private slotInfos: Map<number, SlotInfo> = new Map(); // keyed by slotIndex
  private activeSlotIndex: number | null = null; // null = "All" aggregated tab

  // Pinned and running slot tracking (Issue #2816)
  private pinnedSlots: Set<number> = new Set();
  private runningSlots: Set<number> = new Set();

  // Log file persistence (Issue #190)
  private workspaceRoot: string | null = null;
  private logConfig: LogFileConfig | null = null;

  // Debounced persistence — see SAVE_DEBOUNCE_MS constant for rationale.
  private saveTimer: ReturnType<typeof setTimeout> | null = null;
  private disposed = false;

  // Accumulated webview DOM eviction events. The webview's DOM mirror grows
  // one node per append message but nothing tells it to shrink when in-memory
  // entries are trimmed past MAX_ENTRIES — leaving the renderer process to
  // accumulate gigabytes of stale DOM over long pipelines. Callers drain this
  // after each mutating operation and emit the corresponding webview messages.
  private pendingEvictions: OutputEviction[] = [];

  constructor(workspaceState?: vscode.Memento) {
    if (workspaceState) {
      this.workspaceState = workspaceState;
      this.loadState();
    }
    this.initializeStages();
  }

  /**
   * Configure log file persistence
   *
   * Must be called with workspace root to enable disk logging.
   *
   * @param workspaceRoot - Absolute path to workspace root
   * @param config - Optional log config from config.yaml
   *
   * @see Issue #190 - Pipeline logs persistence
   */
  setLogConfig(workspaceRoot: string, config?: PipelineLogsConfig): void {
    this.workspaceRoot = workspaceRoot;
    this.logConfig = config
      ? {
          retain: config.retain ?? true,
          dir: config.dir ?? ".nightgauge/logs",
          max_age_days: config.max_age_days,
          max_count: config.max_count,
          max_entry_chars: config.max_entry_chars,
        }
      : { retain: true, dir: ".nightgauge/logs" };
  }

  /**
   * Initialize stage progress tracking
   */
  private initializeStages(): void {
    for (const stage of PIPELINE_STAGES) {
      if (!this.stages.has(stage)) {
        this.stages.set(stage, { stage, status: "pending" });
      }
    }
  }

  /**
   * Load state from workspace storage
   */
  private loadState(): void {
    if (!this.workspaceState) return;

    const serialized = this.workspaceState.get<SerializedOutputState>(STATE_STORAGE_KEY);

    if (serialized) {
      this.entries = serialized.entries.map((entry) => ({
        ...entry,
        timestamp: new Date(entry.timestamp),
      }));

      this.stages = new Map(
        serialized.stages.map((stage) => [
          stage.stage,
          {
            ...stage,
            startedAt: stage.startedAt ? new Date(stage.startedAt) : undefined,
            completedAt: stage.completedAt ? new Date(stage.completedAt) : undefined,
          },
        ])
      );

      this.tokenUsage = serialized.tokenUsage;
      this.autoScroll = serialized.autoScroll;
      this.wordWrap = serialized.wordWrap ?? true;
      this.showTimestamps = serialized.showTimestamps ?? true;
      this.issueNumber = serialized.issueNumber;

      // Load tool call aggregation
      if (serialized.toolCallCounts) {
        this.toolCallCounts = new Map(
          Object.entries(serialized.toolCallCounts) as [ToolType, number][]
        );
      }
      if (serialized.toolCallStartedAt) {
        this.toolCallStartedAt = new Date(serialized.toolCallStartedAt);
      }

      // Load search state (Issue #158)
      if (serialized.searchText !== undefined) {
        this.searchText = serialized.searchText;
      }
      if (serialized.searchCaseSensitive !== undefined) {
        this.searchCaseSensitive = serialized.searchCaseSensitive;
      }
      if (serialized.searchUseRegex !== undefined) {
        this.searchUseRegex = serialized.searchUseRegex;
      }

      // Load execution mode (Issue #496)
      if (serialized.executionMode !== undefined) {
        this.executionMode = serialized.executionMode;
      }

      // Load pinned slots (Issue #2816)
      if (serialized.pinnedSlots) {
        this.pinnedSlots = new Set(serialized.pinnedSlots);
      }
    }
  }

  /**
   * Schedule a debounced save. Callers trigger this on every mutation; the
   * debounce coalesces rapid bursts into one `workspaceState.update` call.
   *
   * Why: during active multi-pipeline runs, `addEntry`/`addToolCall`/
   * `setTokenUsage` can fire dozens of times per second. Persisting on every
   * call blocked the extension host event loop and caused VSCode to kill it
   * as UNRESPONSIVE. The debounce cuts persisted writes to at most 2/sec per
   * instance while still capturing the latest state within 500 ms.
   */
  private scheduleSave(): void {
    if (!this.workspaceState || this.disposed) return;
    if (this.saveTimer) return;
    this.saveTimer = setTimeout(() => {
      this.saveTimer = null;
      void this.saveState().catch((err) => {
        console.warn("[Nightgauge] OutputWindowState save failed:", err);
      });
    }, SAVE_DEBOUNCE_MS);
  }

  /**
   * Flush any pending debounced save immediately.
   *
   * Call from dispose() or before the extension host unloads to avoid
   * losing the final in-flight mutations.
   */
  async flush(): Promise<void> {
    if (this.saveTimer) {
      clearTimeout(this.saveTimer);
      this.saveTimer = null;
    }
    await this.saveState();
  }

  /**
   * Cancel any pending save and mark this instance unusable for future saves.
   * Typically called from OutputWindow.dispose().
   */
  dispose(): void {
    if (this.saveTimer) {
      clearTimeout(this.saveTimer);
      this.saveTimer = null;
    }
    this.disposed = true;
  }

  /**
   * Save state to workspace storage
   */
  private async saveState(): Promise<void> {
    if (!this.workspaceState) return;

    const serialized: SerializedOutputState = {
      entries: this.entries.map((entry) => ({
        ...entry,
        text: OutputWindowState.capForPersistence(entry.text),
        details: entry.details ? OutputWindowState.capForPersistence(entry.details) : undefined,
        timestamp: entry.timestamp.toISOString(),
      })),
      stages: Array.from(this.stages.values()).map((stage) => ({
        ...stage,
        startedAt: stage.startedAt?.toISOString(),
        completedAt: stage.completedAt?.toISOString(),
      })),
      tokenUsage: this.tokenUsage,
      autoScroll: this.autoScroll,
      wordWrap: this.wordWrap,
      showTimestamps: this.showTimestamps,
      issueNumber: this.issueNumber,
      toolCallCounts: Object.fromEntries(this.toolCallCounts),
      toolCallStartedAt: this.toolCallStartedAt?.toISOString(),
      searchText: this.searchText || undefined,
      searchCaseSensitive: this.searchCaseSensitive,
      searchUseRegex: this.searchUseRegex,
      executionMode: this.executionMode,
      pinnedSlots: this.pinnedSlots.size > 0 ? Array.from(this.pinnedSlots) : undefined,
    };

    await this.workspaceState.update(STATE_STORAGE_KEY, serialized);
  }

  /**
   * Truncate oversized text for persistence only. In-memory stays full so the
   * webview renders the complete content; only the memento copy is capped.
   */
  private static capForPersistence(text: string): string {
    if (text.length <= MAX_PERSISTED_ENTRY_BYTES) return text;
    const keep = text.slice(0, MAX_PERSISTED_ENTRY_BYTES);
    const truncated = text.length - MAX_PERSISTED_ENTRY_BYTES;
    return `${keep}\n…[truncated ${truncated} bytes for persistence]`;
  }

  /**
   * Generate unique ID for entries
   */
  private generateId(): string {
    return `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
  }

  /**
   * Add an output entry
   *
   * Also writes to disk log file if configured via setLogConfig().
   * Stores the current issueNumber with the entry (Issue #303).
   * Supports content type metadata for formatting (Issue #428).
   */
  addEntry(
    text: string,
    level: OutputLevel,
    stage?: PipelineStage,
    options?: {
      collapsible?: boolean;
      details?: string;
      contentType?: ContentType;
      language?: string;
      /** Skip writing to disk log — for replayed entries (Issue #1352) */
      skipDiskWrite?: boolean;
      /** Slot index for concurrent pipeline tab routing (Issue #2705) */
      slotIndex?: number;
    }
  ): OutputEntry {
    // Sanitize before constructing the retained entry. Output entries are
    // serialized into workspace state and rendered in the WebView; disk
    // logging receives the same sanitized values.
    text = redactSecrets(text);
    if (options?.details !== undefined) {
      options = { ...options, details: redactSecrets(options.details) };
    }
    // #307: an entry's per-run identity (issue number, and below, the disk log
    // root) is bound to the OWNING slot — the explicit `options.slotIndex`
    // captured at stage spawn — never to a shared mutable "current". A slot's
    // issue number lives in its immutable `slotInfos` record; the shared
    // `this.issueNumber` is only the fallback for non-slot (sequential
    // single-run) output. Reading `this.issueNumber` for a concurrent slot's
    // entry was the cross-contamination this fixes: slot A's lines were filed
    // under whatever issue last called setIssueNumber(), flipping at every
    // stage boundary as sibling slots advanced.
    const ownerSlotIndex = options?.slotIndex;
    const ownerInfo = ownerSlotIndex !== undefined ? this.slotInfos.get(ownerSlotIndex) : undefined;
    const resolvedIssueNumber = ownerInfo?.issueNumber ?? this.issueNumber;

    const entry: OutputEntry = {
      id: this.generateId(),
      timestamp: new Date(),
      level,
      stage,
      text,
      collapsible: options?.collapsible,
      collapsed: options?.collapsible ? true : undefined,
      details: options?.details,
      issueNumber: resolvedIssueNumber, // Per-slot issue number (Issue #303, #307)
      contentType: options?.contentType, // Store content type (Issue #428)
      language: options?.language, // Store language hint (Issue #428)
      slotIndex: options?.slotIndex, // Store slot index for tab routing (Issue #2705)
    };

    // Route to per-slot buffer: explicit slotIndex → active slot → none (Issue #2705, #2814)
    const effectiveSlot = options?.slotIndex ?? this.activeSlotIndex ?? undefined;
    if (effectiveSlot !== undefined) {
      entry.slotIndex = effectiveSlot;
    }

    this.entries.push(entry);

    if (effectiveSlot !== undefined) {
      const si = effectiveSlot;
      if (!this.perSlotBuffers.has(si)) {
        this.perSlotBuffers.set(si, []);
      }
      const slotBuffer = this.perSlotBuffers.get(si)!;
      slotBuffer.push(entry);
      if (slotBuffer.length > MAX_ENTRIES) {
        const trimmed = slotBuffer.length - MAX_ENTRIES;
        this.perSlotBuffers.set(si, slotBuffer.slice(-MAX_ENTRIES));
        this.pendingEvictions.push({ scope: "slot", slotIndex: si, count: trimmed });
      }
    }

    // Trim to max size
    if (this.entries.length > MAX_ENTRIES) {
      const trimmed = this.entries.length - MAX_ENTRIES;
      this.entries = this.entries.slice(-MAX_ENTRIES);
      this.pendingEvictions.push({ scope: "aggregate", count: trimmed });
    }

    // Write to disk log file (Issue #190)
    // Truncate for disk only — in-memory entry retains full content for WebView (Issue #770)
    // Fire-and-forget: don't block on disk I/O
    // Skip for replayed entries to avoid re-logging (Issue #1352)
    if (this.workspaceRoot && this.logConfig && !options?.skipDiskWrite) {
      // #191: scope the log destination to the RUN's target repo. The
      // bootstrap-time workspaceRoot is workspaceFolders[0]'s git root —
      // for cross-repo runs that is a different repository, and forensics
      // ended up where nobody looks. Per-slot roots win (concurrent runs in
      // different repos each log to their own), then the sequential run's
      // root, then the bootstrap default for non-run output.
      //
      // #307: attribute by `ownerSlotIndex` (the EXPLICIT owning slot), NOT
      // `effectiveSlot` — which folds in `activeSlotIndex`, the tab the user is
      // viewing. Which tab is focused is a UI concern and must never decide
      // where bytes land on disk; using it let one slot's output stream into a
      // sibling slot's (repo × issue) log file at stage boundaries. The root
      // and the issue number are resolved from the SAME owning slot so a
      // slot's bytes can never land under one repo with another slot's issue.
      const logRoot =
        (ownerSlotIndex !== undefined ? this.slotLogRoots.get(ownerSlotIndex) : undefined) ??
        this.runLogRoot ??
        this.workspaceRoot;
      // #192: FULL FIDELITY to disk. Truncation/collapse is a UI concern
      // that leaked into the only persistent record: entries were capped at
      // 200 chars and collapsed code blocks kept their real body only in
      // `details` — which was never written, so the forbidden `--admin`
      // command left no trace on disk. Write the summary text PLUS the
      // details body, under a generous per-entry cap (64KB default,
      // pipeline.logs.max_entry_chars). Redaction happens inside
      // appendToLog (redactSecrets, #170) and covers the details body since
      // it flows through the same message parameter.
      const fullText = options?.details ? `${text}\n${options.details}` : text;
      const logText = LogFileWriter.truncateForLog(
        fullText,
        this.logConfig.max_entry_chars ?? DEFAULT_DISK_LOG_MAX_ENTRY_CHARS
      );
      LogFileWriter.appendToLog(
        logRoot,
        resolvedIssueNumber ?? null,
        level,
        stage ?? null,
        logText,
        this.logConfig
      ).catch(() => {
        // Silently ignore - disk logging is non-critical
        // Errors are already logged to console by LogFileWriter
      });
    }

    this.scheduleSave();
    return entry;
  }

  /**
   * Get all output entries
   */
  getEntries(): OutputEntry[] {
    return [...this.entries];
  }

  /**
   * Consume and clear pending webview eviction events.
   *
   * Callers should drain after any state mutation that may have trimmed the
   * in-memory buffer (addEntry, clearSlot, removeSlot) and forward the events
   * as `remove-oldest` / `clear-slot` messages to the webview. Keeps the DOM
   * node count aligned with the in-memory buffer cap so the renderer doesn't
   * grow unbounded across a pipeline.
   */
  drainEvictions(): OutputEviction[] {
    if (this.pendingEvictions.length === 0) return [];
    const drained = this.pendingEvictions;
    this.pendingEvictions = [];
    return drained;
  }

  // =========================================
  // Per-slot buffer methods (Issue #2705)
  // =========================================

  /**
   * Register a concurrent pipeline slot for tab display.
   *
   * Called when a new slot starts via SlotOutputManager.createSlotChannel.
   * Idempotent — safe to call again to update title. Clears the archived
   * flag so resuming a tab in a live run replaces the rehydrated copy.
   */
  /**
   * Per-slot disk-log roots (#191): concurrent runs in different repos must
   * each write their session log to their own repo. Keyed by slot index;
   * absent → fall back to runLogRoot, then the bootstrap workspaceRoot.
   */
  private slotLogRoots = new Map<number, string>();
  /** Sequential (non-slot) run's target repo root, or null outside a run (#191). */
  private runLogRoot: string | null = null;

  /** Set (or clear with null) the disk-log root for a slot's run (#191). */
  setSlotLogRoot(slotIndex: number, repoRoot: string | null): void {
    if (repoRoot) {
      this.slotLogRoots.set(slotIndex, repoRoot);
    } else {
      this.slotLogRoots.delete(slotIndex);
    }
  }

  /** Set (or clear with null) the disk-log root for the sequential run (#191). */
  setRunLogRoot(repoRoot: string | null): void {
    this.runLogRoot = repoRoot;
  }

  registerSlot(slotIndex: number, issueNumber: number, title: string, repoSlug?: string): void {
    const existing = this.slotInfos.get(slotIndex);
    const stages = existing?.stages ?? new Map<PipelineStage, StageProgress>();
    if (!existing) {
      for (const s of PIPELINE_STAGES) {
        stages.set(s, { stage: s, status: "pending" });
      }
    }
    const tokenUsage: TokenUsage = existing?.tokenUsage ?? {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };
    this.slotInfos.set(slotIndex, {
      slotIndex,
      issueNumber,
      title,
      repoSlug,
      stage: existing?.stage,
      stages,
      tokenUsage,
      status: existing?.status ?? "pending",
      startedAt: existing?.startedAt ?? Date.now(),
      completedAt: existing?.completedAt ?? null,
      archived: false,
    });
    this.runningSlots.add(slotIndex);
  }

  /**
   * Register a slot rebuilt from an on-disk log (Issue #2818).
   *
   * Archived slots are not added to `runningSlots` and are flagged so the
   * webview can render an "Archived" chip in the tab header. All per-slot
   * stages are initialised with `status: "complete"` to reflect that the
   * run already finished by the time its log was written.
   *
   * If a slot with the same index is already registered and running, this
   * call is ignored so live output is never overwritten by archived data.
   */
  registerArchivedSlot(
    slotIndex: number,
    issueNumber: number,
    title: string,
    repoSlug?: string
  ): void {
    const existing = this.slotInfos.get(slotIndex);
    if (existing && this.runningSlots.has(slotIndex)) return;

    const stages = new Map<PipelineStage, StageProgress>();
    for (const s of PIPELINE_STAGES) {
      stages.set(s, { stage: s, status: "complete" });
    }

    this.slotInfos.set(slotIndex, {
      slotIndex,
      issueNumber,
      title,
      repoSlug,
      stage: undefined,
      stages,
      tokenUsage: {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      },
      status: "complete",
      archived: true,
    });
  }

  /**
   * Update the current stage label for a slot's tab header.
   *
   * Clears any active `currentPhase` when the stage actually changes so a
   * stale phase label from the previous stage doesn't bleed into the next
   * one. Mirrors `DashboardState.setStageRunning` which calls
   * `clearCurrentPhase()` on every transition (Issue #3010).
   */
  updateSlotStage(slotIndex: number, stage: PipelineStage): void {
    const info = this.slotInfos.get(slotIndex);
    if (info) {
      if (info.stage !== stage) {
        info.currentPhase = undefined;
      }
      info.stage = stage;
      this.slotInfos.set(slotIndex, info);
    }
  }

  /**
   * Set the active in-stage phase for a slot (Issue #3010).
   *
   * Called from the OutputWindow's `phase.start` IPC subscription. Pass
   * `undefined` to clear unconditionally.
   */
  updateSlotPhase(
    slotIndex: number,
    phase: { name: string; index: number; total: number } | undefined
  ): void {
    const info = this.slotInfos.get(slotIndex);
    if (info) {
      info.currentPhase = phase;
      this.slotInfos.set(slotIndex, info);
    }
  }

  /**
   * Clear the active phase for a slot — but only if it matches the named
   * phase + stage. Mirrors `PipelineSlotsTracker`'s matching-clear logic
   * so a `phase.complete` that lands after the next `phase.start` doesn't
   * erase the new phase (Issue #3010).
   */
  clearSlotPhase(slotIndex: number, name: string, stage: PipelineStage): void {
    const info = this.slotInfos.get(slotIndex);
    if (!info) return;
    if (info.currentPhase?.name === name && info.stage === stage) {
      info.currentPhase = undefined;
      this.slotInfos.set(slotIndex, info);
    }
  }

  /**
   * Update a single stage status for a specific slot (Issue #2814).
   */
  updateSlotStageStatus(slotIndex: number, stage: PipelineStage, status: StageStatus): void {
    const info = this.slotInfos.get(slotIndex);
    if (info) {
      const current = info.stages.get(stage);
      info.stages.set(stage, { ...current, stage, status });
    }
  }

  /**
   * Update the derived pipeline status for a slot's badge (Issue #2815).
   *
   * @param slotIndex  - Target slot
   * @param status     - Derived status (error > running > complete > skipped > pending)
   * @param completedAt - Epoch ms timestamp; set when status is 'complete' or 'error'
   */
  updateSlotStatus(slotIndex: number, status: StageStatus, completedAt?: number): void {
    const info = this.slotInfos.get(slotIndex);
    if (info) {
      info.status = status;
      if (completedAt !== undefined) {
        info.completedAt = completedAt;
      }
      this.slotInfos.set(slotIndex, info);
    }
  }

  /**
   * Accumulate a token usage delta into a slot's running total (Issue #2815).
   */
  updateSlotTokenUsage(slotIndex: number, delta: TokenUsage): void {
    const info = this.slotInfos.get(slotIndex);
    if (!info) return;
    const current = info.tokenUsage ?? {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };
    info.tokenUsage = {
      inputTokens: current.inputTokens + delta.inputTokens,
      outputTokens: current.outputTokens + delta.outputTokens,
      cacheReadTokens: current.cacheReadTokens + delta.cacheReadTokens,
      cacheCreationTokens: current.cacheCreationTokens + delta.cacheCreationTokens,
      costUsd: current.costUsd + delta.costUsd,
    };
    this.slotInfos.set(slotIndex, info);
  }

  /**
   * Set authoritative token totals for a specific slot (Issue #2814).
   */
  setSlotTokenUsage(slotIndex: number, usage: TokenUsage): void {
    const info = this.slotInfos.get(slotIndex);
    if (info) {
      info.tokenUsage = { ...usage };
    }
  }

  /**
   * Get per-slot stage progress as an array for HTML generation (Issue #2814).
   */
  getSlotStageProgress(slotIndex: number): StageProgress[] {
    const info = this.slotInfos.get(slotIndex);
    if (!info) return [];
    return Array.from(info.stages.values());
  }

  /**
   * Get per-slot token usage (Issue #2814 / #2815).
   */
  getSlotTokenUsage(slotIndex: number): TokenUsage {
    const info = this.slotInfos.get(slotIndex);
    return (
      info?.tokenUsage ?? {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      }
    );
  }

  /**
   * Find a slot by its issue number (Issue #2815).
   *
   * Used to route token updates to the correct slot when only issueNumber is known.
   */
  getSlotByIssueNumber(issueNumber: number): SlotInfo | undefined {
    for (const info of this.slotInfos.values()) {
      if (info.issueNumber === issueNumber) return info;
    }
    return undefined;
  }

  /**
   * Find the slot index registered for a given issue number (Issue #2814).
   */
  findSlotIndexByIssue(issueNumber: number): number | undefined {
    for (const [slotIndex, info] of this.slotInfos) {
      if (info.issueNumber === issueNumber) return slotIndex;
    }
    return undefined;
  }

  /**
   * Return an unused slot index that does not collide with any registered slot.
   *
   * Callers that synthesize slots (e.g., log rehydration in Issue #2818) use
   * this to avoid overwriting live slots.
   */
  getNextSlotIndex(): number {
    let next = 0;
    while (this.slotInfos.has(next)) next += 1;
    return next;
  }

  /**
   * Clear a single slot's entries and reset its stage/token state (Issue #2814).
   *
   * Preserves all other slots unchanged. When the active slot tab is cleared
   * via the "Clear output" button, only this slot is affected.
   */
  clearSlot(slotIndex: number): void {
    this.perSlotBuffers.delete(slotIndex);
    this.perSlotBuffers.set(slotIndex, []);
    this.pendingEvictions.push({ scope: "slot-cleared", slotIndex });

    const info = this.slotInfos.get(slotIndex);
    if (info) {
      for (const s of PIPELINE_STAGES) {
        info.stages.set(s, { stage: s, status: "pending" });
      }
      info.tokenUsage = {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      };
      info.stage = undefined;
    }

    this.entries = this.entries.filter((e) => e.slotIndex !== slotIndex);
    this.scheduleSave();
  }

  /**
   * Get entries for a specific slot or the aggregated "All" buffer.
   *
   * @param slotIndex - Slot index, or null for the aggregated view
   */
  getSlotEntries(slotIndex: number | null): OutputEntry[] {
    if (slotIndex === null) {
      return [...this.entries];
    }
    return [...(this.perSlotBuffers.get(slotIndex) ?? [])];
  }

  /**
   * Set the active slot index for tab state tracking.
   *
   * @param slotIndex - Slot index, or null for the "All" aggregated tab
   */
  setActiveSlot(slotIndex: number | null): void {
    this.activeSlotIndex = slotIndex;
  }

  /**
   * Get the currently active slot index.
   *
   * @returns The active slot index, or null for the "All" tab
   */
  getActiveSlotIndex(): number | null {
    return this.activeSlotIndex;
  }

  /**
   * Get all registered active slots — pinned slots first, then unpinned, each group sorted by index.
   *
   * Returns an empty array when no concurrent slots are active (single-slot mode).
   */
  getActiveSlots(): SlotInfo[] {
    const all = Array.from(this.slotInfos.values());
    const pinned = all
      .filter((s) => this.pinnedSlots.has(s.slotIndex))
      .sort((a, b) => a.slotIndex - b.slotIndex);
    const unpinned = all
      .filter((s) => !this.pinnedSlots.has(s.slotIndex))
      .sort((a, b) => a.slotIndex - b.slotIndex);
    return [...pinned, ...unpinned];
  }

  // =========================================
  // Slot management methods (Issue #2816)
  // =========================================

  /**
   * Remove a slot and its buffer. Deactivates if currently active.
   *
   * Safe to call if slotIndex does not exist — returns silently.
   */
  removeSlot(slotIndex: number): void {
    if (!this.slotInfos.has(slotIndex)) return;
    this.perSlotBuffers.delete(slotIndex);
    this.slotInfos.delete(slotIndex);
    this.pinnedSlots.delete(slotIndex);
    this.runningSlots.delete(slotIndex);
    this.pendingEvictions.push({ scope: "slot-cleared", slotIndex });
    if (this.activeSlotIndex === slotIndex) {
      this.activeSlotIndex = null;
    }
    this.scheduleSave();
  }

  /**
   * Toggle pin state for a slot. Pinned slots appear before unpinned in getActiveSlots().
   */
  togglePinSlot(slotIndex: number): void {
    if (this.pinnedSlots.has(slotIndex)) {
      this.pinnedSlots.delete(slotIndex);
    } else {
      this.pinnedSlots.add(slotIndex);
    }
    this.scheduleSave();
  }

  /**
   * Get sorted array of pinned slot indices.
   */
  getPinnedSlots(): number[] {
    return Array.from(this.pinnedSlots).sort((a, b) => a - b);
  }

  /**
   * Check whether a slot is currently running.
   *
   * Slots are marked running on registerSlot() and stopped via markSlotStopped().
   */
  isSlotRunning(slotIndex: number): boolean {
    return this.runningSlots.has(slotIndex);
  }

  /**
   * Mark a slot as stopped (no longer running).
   *
   * Call when a slot's pipeline completes or errors.
   */
  markSlotStopped(slotIndex: number): void {
    this.runningSlots.delete(slotIndex);
  }

  /**
   * Get the number of output entries
   *
   * Used for confirmation dialogs when clearing large amounts of output.
   * @see Issue #157 - Clear output confirmation
   */
  getEntryCount(): number {
    return this.entries.length;
  }

  /**
   * Get the most recent entry (for separator detection)
   *
   * Returns null if no entries exist.
   * Used to detect issue number changes between entries (Issue #303).
   */
  getPreviousEntry(): OutputEntry | null {
    if (this.entries.length === 0) {
      return null;
    }
    return this.entries[this.entries.length - 1];
  }

  /**
   * Get entries for a specific stage
   */
  getEntriesForStage(stage: PipelineStage): OutputEntry[] {
    return this.entries.filter((entry) => entry.stage === stage);
  }

  /**
   * Update stage status
   */
  updateStageStatus(stage: PipelineStage, status: StageStatus): void {
    const progress = this.stages.get(stage) || { stage, status: "pending" };

    progress.status = status;

    if (status === "running") {
      progress.startedAt = new Date();
    } else if (status === "complete" || status === "error" || status === "skipped") {
      progress.completedAt = new Date();
      if (progress.startedAt) {
        progress.durationMs = progress.completedAt.getTime() - progress.startedAt.getTime();
      }
    }

    this.stages.set(stage, progress);
    this.scheduleSave();
  }

  /**
   * Get stage progress
   */
  getStageProgress(stage: PipelineStage): StageProgress | undefined {
    return this.stages.get(stage);
  }

  /**
   * Get all stage progress
   */
  getAllStageProgress(): StageProgress[] {
    return PIPELINE_STAGES.map((stage) => this.stages.get(stage) || { stage, status: "pending" });
  }

  /**
   * DEPRECATED: Update token usage by accumulating (Issue #154)
   *
   * Token accumulation should be handled by PipelineStateService.
   * Use setTokenUsage() to set authoritative values from PipelineStateService.
   *
   * @deprecated Use setTokenUsage() with values from PipelineStateService
   */
  updateTokenUsage(usage: Partial<TokenUsage>): void {
    if (usage.inputTokens !== undefined) {
      this.tokenUsage.inputTokens += usage.inputTokens;
    }
    if (usage.outputTokens !== undefined) {
      this.tokenUsage.outputTokens += usage.outputTokens;
    }
    if (usage.cacheReadTokens !== undefined) {
      this.tokenUsage.cacheReadTokens += usage.cacheReadTokens;
    }
    if (usage.cacheCreationTokens !== undefined) {
      this.tokenUsage.cacheCreationTokens += usage.cacheCreationTokens;
    }
    if (usage.costUsd !== undefined) {
      this.tokenUsage.costUsd += usage.costUsd;
    }

    this.scheduleSave();
  }

  /**
   * Set token usage from authoritative source (Issue #154)
   *
   * This method sets (replaces, not accumulates) token usage values.
   * Call this with values from PipelineStateService, which owns
   * the authoritative token accumulation.
   *
   * @param usage Complete token usage to display (not incremental)
   */
  setTokenUsage(usage: TokenUsage): void {
    this.tokenUsage = { ...usage };
    this.scheduleSave();
  }

  /**
   * Get token usage
   */
  getTokenUsage(): TokenUsage {
    return { ...this.tokenUsage };
  }

  /**
   * Set auto-scroll preference
   */
  setAutoScroll(enabled: boolean): void {
    this.autoScroll = enabled;
    this.scheduleSave();
  }

  /**
   * Get auto-scroll preference
   */
  getAutoScroll(): boolean {
    return this.autoScroll;
  }

  /**
   * Set word wrap preference
   */
  setWordWrap(enabled: boolean): void {
    this.wordWrap = enabled;
    this.scheduleSave();
  }

  /**
   * Get word wrap preference
   */
  getWordWrap(): boolean {
    return this.wordWrap;
  }

  /**
   * Set show timestamps preference (Issue #160)
   */
  setShowTimestamps(enabled: boolean): void {
    this.showTimestamps = enabled;
    this.scheduleSave();
  }

  /**
   * Get show timestamps preference (Issue #160)
   */
  getShowTimestamps(): boolean {
    return this.showTimestamps;
  }

  // =========================================
  // Search State Methods (Issue #158)
  // =========================================

  /**
   * Set search text for output filtering
   */
  setSearchText(text: string): void {
    this.searchText = text;
    this.scheduleSave();
  }

  /**
   * Get search text
   */
  getSearchText(): string {
    return this.searchText;
  }

  /**
   * Set case-sensitive search preference
   */
  setSearchCaseSensitive(enabled: boolean): void {
    this.searchCaseSensitive = enabled;
    this.scheduleSave();
  }

  /**
   * Get case-sensitive search preference
   */
  getSearchCaseSensitive(): boolean {
    return this.searchCaseSensitive;
  }

  /**
   * Set regex search preference
   */
  setSearchUseRegex(enabled: boolean): void {
    this.searchUseRegex = enabled;
    this.scheduleSave();
  }

  /**
   * Get regex search preference
   */
  getSearchUseRegex(): boolean {
    return this.searchUseRegex;
  }

  /**
   * Clear search state
   */
  clearSearch(): void {
    this.searchText = "";
    this.scheduleSave();
  }

  // =========================================
  // Execution Mode Methods (Issue #496)
  // =========================================

  /**
   * Set execution mode for dual-mode rendering
   *
   * Mode determines how output is rendered:
   * - 'headless': Stream-json parsing, token tracking, tool indicators
   * - 'interactive': Raw text display, ANSI stripping, no token tracking
   *
   * @param mode - The execution mode
   */
  setExecutionMode(mode: ExecutionMode): void {
    this.executionMode = mode;
    this.scheduleSave();
  }

  /**
   * Get current execution mode
   *
   * @returns Current execution mode ('headless' or 'interactive')
   */
  getExecutionMode(): ExecutionMode {
    return this.executionMode;
  }

  /**
   * Set issue number
   */
  setIssueNumber(issueNumber: number): void {
    this.issueNumber = issueNumber;
    this.scheduleSave();
  }

  /**
   * Get issue number
   */
  getIssueNumber(): number | undefined {
    return this.issueNumber;
  }

  /**
   * Get a slot's immutable issue number from its registered {@link SlotInfo}.
   *
   * The per-slot record is the single source of truth for a concurrent run's
   * identity (#307): unlike the shared `this.issueNumber`, it does not flip as
   * sibling slots advance. Returns undefined when the slot is not registered.
   */
  getSlotIssueNumber(slotIndex: number): number | undefined {
    return this.slotInfos.get(slotIndex)?.issueNumber;
  }

  /**
   * Toggle entry collapsed state
   */
  toggleEntryCollapsed(entryId: string): boolean {
    const entry = this.entries.find((e) => e.id === entryId);
    if (entry && entry.collapsible) {
      entry.collapsed = !entry.collapsed;
      this.scheduleSave();
      return entry.collapsed;
    }
    return false;
  }

  /**
   * Remove all entries for a specific stage
   *
   * Used when retrying a failed stage so stale output from the
   * previous attempt is cleared before the new run begins.
   */
  clearStageEntries(stage: PipelineStage): void {
    this.entries = this.entries.filter((entry) => entry.stage !== stage);
    this.scheduleSave();
  }

  /**
   * Remove stall warning entries for a specific stage (Issue #797)
   *
   * Filters out entries that match the stall warning pattern emitted by
   * skillRunner.ts when a stage exceeds its configured stall threshold.
   *
   * @param stage The pipeline stage whose stall warnings should be removed
   */
  removeStallWarningEntries(stage: PipelineStage): void {
    this.entries = this.entries.filter(
      (entry) =>
        !(
          entry.stage === stage &&
          (entry.text.includes("Stage still running after") ||
            entry.text.includes("Stall Warning:") ||
            entry.text.includes("Stall Warning ("))
        )
    );
    this.scheduleSave();
  }

  /**
   * Clear all entries and reset state
   */
  clear(): void {
    this.entries = [];
    this.tokenUsage = {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };
    this.issueNumber = undefined;

    // Reset stage progress
    for (const stage of PIPELINE_STAGES) {
      this.stages.set(stage, { stage, status: "pending" });
    }

    // Reset tool call aggregation
    this.toolCallCounts.clear();
    this.toolCallStartedAt = null;

    // Reset execution mode to default (Issue #496)
    this.executionMode = "headless";

    // Reset per-slot state (Issue #2705)
    this.perSlotBuffers.clear();
    this.slotInfos.clear();
    this.activeSlotIndex = null;

    // Reset pinned and running slot tracking (Issue #2816)
    this.pinnedSlots.clear();
    this.runningSlots.clear();

    this.scheduleSave();
  }

  // =========================================
  // Tool Call Aggregation Methods
  // =========================================

  /**
   * Add a tool call to the aggregation
   *
   * @param toolType The type of tool that was called
   */
  addToolCall(toolType: ToolType): void {
    // Initialize start time on first tool call
    if (!this.toolCallStartedAt) {
      this.toolCallStartedAt = new Date();
    }

    // Increment count for this tool type
    const currentCount = this.toolCallCounts.get(toolType) || 0;
    this.toolCallCounts.set(toolType, currentCount + 1);

    this.scheduleSave();
  }

  /**
   * Get the current tool call summary
   *
   * @returns Summary with total count and breakdown by tool type
   */
  getToolSummary(): ToolCallSummary {
    let total = 0;
    for (const count of this.toolCallCounts.values()) {
      total += count;
    }

    return {
      total,
      byTool: new Map(this.toolCallCounts),
      startedAt: this.toolCallStartedAt || new Date(),
      endedAt: new Date(),
    };
  }

  /**
   * Get the count for a specific tool type
   *
   * @param toolType The tool type to query
   * @returns The number of times this tool was called
   */
  getToolCallCount(toolType: ToolType): number {
    return this.toolCallCounts.get(toolType) || 0;
  }

  /**
   * Get the total number of tool calls
   *
   * @returns Total count across all tool types
   */
  getTotalToolCalls(): number {
    let total = 0;
    for (const count of this.toolCallCounts.values()) {
      total += count;
    }
    return total;
  }

  /**
   * Reset tool call aggregation (typically at stage start)
   */
  resetToolCalls(): void {
    this.toolCallCounts.clear();
    this.toolCallStartedAt = null;
    this.scheduleSave();
  }

  /**
   * Export state as JSON for debugging
   */
  exportAsJson(): string {
    return JSON.stringify(
      {
        entries: this.entries.map((e) => ({
          ...e,
          timestamp: e.timestamp.toISOString(),
        })),
        stages: Array.from(this.stages.values()),
        tokenUsage: this.tokenUsage,
        issueNumber: this.issueNumber,
      },
      null,
      2
    );
  }
}
