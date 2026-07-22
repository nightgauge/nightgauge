/**
 * OutputWindowMessageHandler - Message protocol handler for output window
 *
 * Handles bidirectional message passing between the WebView and extension.
 * Validates incoming messages and routes them to appropriate handlers.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import type {
  OutputEntry,
  TokenUsage,
  StageProgress,
  ExecutionMode,
  StageStatus,
  SlotInfo,
} from "./OutputWindowState";
import type { ToolType, ToolCallSummary } from "./ToolCallIndicator";
import type { AskUserQuestionPayload, QuestionResponse } from "../../types/askUserQuestion";

/**
 * Tool indicator message data
 */
export interface ToolIndicatorMessage {
  /** Unique identifier for this tool call */
  id: string;
  /** The tool being used */
  tool: ToolType;
  /** Target file or resource being operated on */
  target: string;
  /** Whether the tool call is still in progress */
  isActive: boolean;
  /** Timestamp when the tool call started */
  startedAt: string;
}

/**
 * Tool summary message data
 */
export interface ToolSummaryMessage {
  /** Total number of tool calls */
  total: number;
  /** Breakdown by tool type: { Edit: 5, Read: 3, ... } */
  byTool: Record<string, number>;
  /** Formatted summary string: "Used 12 tools: 5 Edit, 4 Read, 3 Bash" */
  formatted: string;
}

/**
 * Question prompt message data (Issue #118)
 * Sent to WebView to display an AskUserQuestion prompt
 */
export interface QuestionPromptMessage {
  /** Unique identifier for this question session */
  id: string;
  /** The questions to display */
  questions: Array<{
    question: string;
    header: string;
    options: Array<{
      label: string;
      description?: string;
    }>;
    multiSelect: boolean;
  }>;
  /** Tool use ID for matching tool_result (optional) */
  toolUseId?: string;
}

/**
 * Pipeline state message data (Issue #431)
 * Sent to WebView to update pipeline-dependent button states
 */
export interface PipelineStateMessage {
  /** Whether a pipeline is currently running */
  isRunning: boolean;
  /** Current pipeline stage, or null if not running */
  currentStage: string | null;
}

/**
 * Search state message data (Issue #158)
 * Sent to WebView to update search state (on load or when extension changes it)
 */
export interface SearchStateMessage {
  /** Current search text */
  searchText: string;
  /** Whether search is case-sensitive */
  caseSensitive: boolean;
  /** Whether to use regex matching */
  useRegex: boolean;
}

/**
 * Execution mode message data (Issue #496)
 * Sent to WebView to update the rendering mode (headless vs interactive)
 */
export interface ExecutionModeMessage {
  /** The execution mode */
  mode: ExecutionMode;
}

/**
 * Messages sent from extension to WebView
 */
export type ExtensionToWebViewMessage =
  | { type: "append"; entry: SerializedOutputEntry }
  | { type: "clear" }
  | { type: "clear-stage"; stage: string }
  | { type: "update-tokens"; usage: TokenUsage }
  | { type: "set-auto-scroll"; enabled: boolean }
  | { type: "set-word-wrap"; enabled: boolean }
  | { type: "set-timestamps"; enabled: boolean }
  | { type: "tool-indicator"; indicator: ToolIndicatorMessage }
  | { type: "tool-indicator-complete"; id: string }
  | { type: "tool-summary"; summary: ToolSummaryMessage }
  | { type: "question-prompt"; question: QuestionPromptMessage }
  | { type: "question-answered"; questionId: string; answer: unknown }
  | { type: "pipeline-state"; state: PipelineStateMessage }
  | { type: "set-search-state"; state: SearchStateMessage }
  | { type: "set-mode"; mode: ExecutionModeMessage }
  | { type: "remove-stall-warnings"; stage: string }
  | { type: "add-stall-warning"; stage: string; entry: SerializedOutputEntry; multiplier: number }
  | { type: "collapse-stage"; stage: string; status: "complete" | "error" | "skipped" }
  /** Remove the N oldest DOM entries from a panel to keep it bounded. */
  | { type: "remove-oldest"; count: number; slotIndex?: number }
  /** Live badge update for a single slot tab (Issue #2815) */
  | {
      type: "slot-badge-update";
      slotIndex: number;
      status: StageStatus;
      startedAt?: number;
      completedAt?: number | null;
      costUsd: number;
    }
  /**
   * Live overview-card update — patches the issue-row card on the
   * Overview panel in place without re-rendering the whole panel.
   * Fires on every event that already drives slot-badge-update plus
   * stage transitions, so all card fields stay current mid-pipeline.
   */
  | {
      type: "overview-card-update";
      slotIndex: number;
      issueNumber: number;
      title?: string;
      repoSlug?: string;
      stage?: string;
      stageLabel: string;
      status: StageStatus;
      statusLabel: string;
      startedAt?: number;
      completedAt?: number | null;
      costUsd: number;
      inputTokens: number;
      outputTokens: number;
      cacheTokens: number;
      archived: boolean;
      /**
       * Active in-stage phase — `null` clears the rendered label without
       * removing the target span (Issue #3010).
       */
      currentPhase: { name: string; index: number; total: number } | null;
    };

/**
 * Messages sent from WebView to extension
 */
export type WebViewToExtensionMessage =
  | { type: "interrupt" }
  | { type: "clear-logs" }
  | { type: "copy-to-clipboard" }
  | { type: "export"; format: "txt" | "json" }
  | { type: "toggle-auto-scroll"; enabled: boolean }
  | { type: "toggle-word-wrap"; enabled: boolean }
  | { type: "toggle-timestamps"; enabled: boolean }
  | { type: "toggle-entry"; entryId: string }
  | {
      type: "question-response";
      questionId: string;
      response: QuestionResponse | null;
    }
  | { type: "search-text-change"; text: string }
  | { type: "toggle-search-case-sensitive"; enabled: boolean }
  | { type: "toggle-search-use-regex"; enabled: boolean }
  | { type: "send-message"; text: string }
  /** Slot tab clicked — switch active slot (Issue #2705) */
  | { type: "tab:switch"; slotIndex: number | null }
  /** Per-tab action triggered from context menu or keyboard shortcut (Issue #2816) */
  | {
      type: "slot:action";
      slotIndex: number;
      action: "close" | "pin" | "reveal-github" | "open-log";
    };

/**
 * Serialized output entry for message passing (dates as ISO strings)
 */
export interface SerializedOutputEntry {
  id: string;
  timestamp: string;
  level: string;
  stage?: string;
  text: string;
  collapsible?: boolean;
  collapsed?: boolean;
  details?: string;
  /** Issue number for multi-issue pipeline tracking (Issue #303) */
  issueNumber?: number;
  /** Slot index for concurrent pipeline tab routing (Issue #2705) */
  slotIndex?: number;
}

/**
 * Callbacks for handling WebView messages
 */
export interface MessageHandlerCallbacks {
  onInterrupt?: () => void;
  onClearLogs?: () => void;
  /** Called when user clicks copy button (Issue #156) */
  onCopyToClipboard?: () => void;
  onExport?: (format: "txt" | "json") => void;
  onToggleAutoScroll?: (enabled: boolean) => void;
  onToggleWordWrap?: (enabled: boolean) => void;
  /** Called when user toggles timestamp display (Issue #160) */
  onToggleTimestamps?: (enabled: boolean) => void;
  onToggleEntry?: (entryId: string) => void;
  /** Called when user responds to an AskUserQuestion prompt (Issue #118) */
  onQuestionResponse?: (questionId: string, response: QuestionResponse | null) => void;
  /** Called when user changes search text (Issue #158) */
  onSearchTextChange?: (text: string) => void;
  /** Called when user toggles case-sensitive search (Issue #158) */
  onToggleSearchCaseSensitive?: (enabled: boolean) => void;
  /** Called when user toggles regex search (Issue #158) */
  onToggleSearchUseRegex?: (enabled: boolean) => void;
  /** Called when execution mode changes (Issue #496) */
  onModeChange?: (mode: ExecutionMode) => void;
  /** Called when user sends a message to the running agent (Issue #497) */
  onSendMessage?: (text: string) => void;
  /** Called when user switches output tabs for concurrent slots (Issue #2705) */
  onTabSwitch?: (slotIndex: number | null) => void;
  /** Called when user triggers a per-tab action (close, pin, reveal-github, open-log) (Issue #2816) */
  onSlotAction?: (slotIndex: number, action: string) => void;
}

/**
 * OutputWindowMessageHandler class for handling WebView messages
 *
 * @example
 * ```typescript
 * const handler = new OutputWindowMessageHandler({
 *   onInterrupt: () => orchestrator.stop(),
 *   onClearLogs: () => state.clear(),
 * });
 *
 * panel.webview.onDidReceiveMessage(handler.handleMessage);
 * ```
 */
export class OutputWindowMessageHandler {
  private callbacks: MessageHandlerCallbacks;

  constructor(callbacks: MessageHandlerCallbacks = {}) {
    this.callbacks = callbacks;
  }

  /**
   * Handle incoming message from WebView
   */
  handleMessage = (message: unknown): void => {
    if (!this.isValidMessage(message)) {
      console.warn("Invalid message received from output window WebView:", message);
      return;
    }

    const msg = message as WebViewToExtensionMessage;

    switch (msg.type) {
      case "interrupt":
        this.callbacks.onInterrupt?.();
        break;

      case "clear-logs":
        this.callbacks.onClearLogs?.();
        break;

      case "copy-to-clipboard":
        this.callbacks.onCopyToClipboard?.();
        break;

      case "export":
        if (this.isValidExportMessage(msg)) {
          this.callbacks.onExport?.(msg.format);
        }
        break;

      case "toggle-auto-scroll":
        if (this.isValidToggleAutoScrollMessage(msg)) {
          this.callbacks.onToggleAutoScroll?.(msg.enabled);
        }
        break;

      case "toggle-word-wrap":
        if (this.isValidToggleWordWrapMessage(msg)) {
          this.callbacks.onToggleWordWrap?.(msg.enabled);
        }
        break;

      case "toggle-timestamps":
        if (this.isValidToggleTimestampsMessage(msg)) {
          this.callbacks.onToggleTimestamps?.(msg.enabled);
        }
        break;

      case "toggle-entry":
        if (this.isValidToggleEntryMessage(msg)) {
          this.callbacks.onToggleEntry?.(msg.entryId);
        }
        break;

      case "question-response":
        if (this.isValidQuestionResponseMessage(msg)) {
          this.callbacks.onQuestionResponse?.(msg.questionId, msg.response);
        }
        break;

      case "search-text-change":
        if (this.isValidSearchTextChangeMessage(msg)) {
          this.callbacks.onSearchTextChange?.(msg.text);
        }
        break;

      case "toggle-search-case-sensitive":
        if (this.isValidToggleSearchCaseSensitiveMessage(msg)) {
          this.callbacks.onToggleSearchCaseSensitive?.(msg.enabled);
        }
        break;

      case "toggle-search-use-regex":
        if (this.isValidToggleSearchUseRegexMessage(msg)) {
          this.callbacks.onToggleSearchUseRegex?.(msg.enabled);
        }
        break;

      case "send-message":
        if (this.isValidSendMessageMessage(msg)) {
          this.callbacks.onSendMessage?.(msg.text);
        }
        break;

      case "tab:switch":
        if (this.isValidTabSwitchMessage(msg)) {
          this.callbacks.onTabSwitch?.(msg.slotIndex);
        }
        break;

      default:
        console.warn("Unknown message type from output window WebView:", msg);
    }
  };

  /**
   * Type guard for valid message structure
   */
  private isValidMessage(message: unknown): message is { type: string } {
    return (
      typeof message === "object" &&
      message !== null &&
      "type" in message &&
      typeof (message as { type: unknown }).type === "string"
    );
  }

  /**
   * Type guard for export message
   */
  private isValidExportMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "export"; format: "txt" | "json" } {
    return (
      msg.type === "export" && "format" in msg && (msg.format === "txt" || msg.format === "json")
    );
  }

  /**
   * Type guard for toggle-auto-scroll message
   */
  private isValidToggleAutoScrollMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "toggle-auto-scroll"; enabled: boolean } {
    return (
      msg.type === "toggle-auto-scroll" && "enabled" in msg && typeof msg.enabled === "boolean"
    );
  }

  /**
   * Type guard for toggle-word-wrap message
   */
  private isValidToggleWordWrapMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "toggle-word-wrap"; enabled: boolean } {
    return msg.type === "toggle-word-wrap" && "enabled" in msg && typeof msg.enabled === "boolean";
  }

  /**
   * Type guard for toggle-timestamps message (Issue #160)
   */
  private isValidToggleTimestampsMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "toggle-timestamps"; enabled: boolean } {
    return msg.type === "toggle-timestamps" && "enabled" in msg && typeof msg.enabled === "boolean";
  }

  /**
   * Type guard for toggle-entry message
   */
  private isValidToggleEntryMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "toggle-entry"; entryId: string } {
    return msg.type === "toggle-entry" && "entryId" in msg && typeof msg.entryId === "string";
  }

  /**
   * Type guard for question-response message (Issue #118)
   */
  private isValidQuestionResponseMessage(msg: WebViewToExtensionMessage): msg is {
    type: "question-response";
    questionId: string;
    response: QuestionResponse | null;
  } {
    return (
      msg.type === "question-response" &&
      "questionId" in msg &&
      typeof msg.questionId === "string" &&
      "response" in msg
    );
  }

  /**
   * Type guard for search-text-change message (Issue #158)
   */
  private isValidSearchTextChangeMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "search-text-change"; text: string } {
    return msg.type === "search-text-change" && "text" in msg && typeof msg.text === "string";
  }

  /**
   * Type guard for toggle-search-case-sensitive message (Issue #158)
   */
  private isValidToggleSearchCaseSensitiveMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "toggle-search-case-sensitive"; enabled: boolean } {
    return (
      msg.type === "toggle-search-case-sensitive" &&
      "enabled" in msg &&
      typeof msg.enabled === "boolean"
    );
  }

  /**
   * Type guard for toggle-search-use-regex message (Issue #158)
   */
  private isValidToggleSearchUseRegexMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "toggle-search-use-regex"; enabled: boolean } {
    return (
      msg.type === "toggle-search-use-regex" && "enabled" in msg && typeof msg.enabled === "boolean"
    );
  }

  /**
   * Type guard for send-message message (Issue #497)
   */
  private isValidSendMessageMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "send-message"; text: string } {
    return msg.type === "send-message" && "text" in msg && typeof msg.text === "string";
  }

  /**
   * Type guard for tab:switch message (Issue #2705)
   */
  private isValidTabSwitchMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "tab:switch"; slotIndex: number | null } {
    return (
      msg.type === "tab:switch" &&
      "slotIndex" in msg &&
      (typeof (msg as { type: string; slotIndex: unknown }).slotIndex === "number" ||
        (msg as { type: string; slotIndex: unknown }).slotIndex === null)
    );
  }

  /**
   * Update callbacks
   */
  setCallbacks(callbacks: Partial<MessageHandlerCallbacks>): void {
    this.callbacks = { ...this.callbacks, ...callbacks };
  }
}

/**
 * Serialize an OutputEntry for message passing
 */
export function serializeEntry(entry: OutputEntry): SerializedOutputEntry {
  return {
    id: entry.id,
    timestamp: entry.timestamp.toISOString(),
    level: entry.level,
    stage: entry.stage,
    text: entry.text,
    collapsible: entry.collapsible,
    collapsed: entry.collapsed,
    details: entry.details,
    issueNumber: entry.issueNumber,
    slotIndex: entry.slotIndex,
  };
}

/**
 * Create an append message for the WebView
 */
export function createAppendMessage(entry: OutputEntry): ExtensionToWebViewMessage {
  return {
    type: "append",
    entry: serializeEntry(entry),
  };
}

/**
 * Create a clear message for the WebView
 */
export function createClearMessage(): ExtensionToWebViewMessage {
  return { type: "clear" };
}

/**
 * Create a remove-oldest message for the WebView.
 *
 * Drops the N oldest `.output-entry` DOM nodes from the target panel. Mirrors
 * the in-memory MAX_ENTRIES cap so the renderer's DOM doesn't grow unbounded.
 * When `slotIndex` is omitted, the aggregate ("All") panel is targeted.
 */
export function createRemoveOldestMessage(
  count: number,
  slotIndex?: number
): ExtensionToWebViewMessage {
  return slotIndex === undefined
    ? { type: "remove-oldest", count }
    : { type: "remove-oldest", count, slotIndex };
}

/**
 * Create a clear-stage message for the WebView
 *
 * Removes all output entries for a specific stage (e.g., on retry).
 */
export function createClearStageMessage(stage: string): ExtensionToWebViewMessage {
  return { type: "clear-stage", stage };
}

/**
 * Create a token update message for the WebView
 */
export function createTokenUpdateMessage(usage: TokenUsage): ExtensionToWebViewMessage {
  return { type: "update-tokens", usage };
}

/**
 * Create an auto-scroll message for the WebView
 */
export function createAutoScrollMessage(enabled: boolean): ExtensionToWebViewMessage {
  return { type: "set-auto-scroll", enabled };
}

/**
 * Create a word wrap message for the WebView
 */
export function createWordWrapMessage(enabled: boolean): ExtensionToWebViewMessage {
  return { type: "set-word-wrap", enabled };
}

/**
 * Create a timestamps message for the WebView (Issue #160)
 */
export function createTimestampsMessage(enabled: boolean): ExtensionToWebViewMessage {
  return { type: "set-timestamps", enabled };
}

/**
 * Create a tool indicator message for the WebView
 *
 * Shows an animated indicator while a tool is executing.
 *
 * @param id Unique identifier for this tool call
 * @param tool The tool type being used
 * @param target Target file or resource
 * @param isActive Whether the tool is still running
 * @param startedAt When the tool call started
 */
export function createToolIndicatorMessage(
  id: string,
  tool: ToolType,
  target: string,
  isActive: boolean,
  startedAt: Date
): ExtensionToWebViewMessage {
  return {
    type: "tool-indicator",
    indicator: {
      id,
      tool,
      target,
      isActive,
      startedAt: startedAt.toISOString(),
    },
  };
}

/**
 * Create a tool indicator complete message for the WebView
 *
 * Marks a tool indicator as complete (stops animation).
 *
 * @param id The unique identifier of the tool call to mark complete
 */
export function createToolIndicatorCompleteMessage(id: string): ExtensionToWebViewMessage {
  return { type: "tool-indicator-complete", id };
}

/**
 * Create a slot badge update message for live tab badge refresh (Issue #2815).
 *
 * Fires on every token update and status change for a slot.
 * The WebView uses this to update cost, status icon, and elapsed timer
 * without a full panel reload.
 */
export function createSlotBadgeUpdateMessage(
  slotIndex: number,
  slot: SlotInfo
): ExtensionToWebViewMessage {
  return {
    type: "slot-badge-update",
    slotIndex,
    status: slot.status ?? "pending",
    startedAt: slot.startedAt,
    completedAt: slot.completedAt,
    costUsd: slot.tokenUsage?.costUsd ?? 0,
  };
}

/**
 * Create an overview-card update message (companion to slot-badge-update).
 *
 * Fires alongside every slot-badge-update plus on stage transitions, so each
 * Overview-panel issue card stays current mid-pipeline (status, stage label,
 * elapsed-timer anchors, cost, tokens) without re-rendering the whole panel.
 *
 * `stageLabel` and `statusLabel` are computed extension-side so the WebView
 * doesn't need to know about PipelineStage labelling.
 */
export function createOverviewCardUpdateMessage(
  slot: SlotInfo,
  stageLabel: string,
  statusLabel: string
): ExtensionToWebViewMessage {
  const usage = slot.tokenUsage;
  const cacheTokens = (usage?.cacheReadTokens ?? 0) + (usage?.cacheCreationTokens ?? 0);
  return {
    type: "overview-card-update",
    slotIndex: slot.slotIndex,
    issueNumber: slot.issueNumber,
    title: slot.title,
    repoSlug: slot.repoSlug,
    stage: slot.stage,
    stageLabel,
    status: slot.status ?? "pending",
    statusLabel,
    startedAt: slot.startedAt,
    completedAt: slot.completedAt,
    costUsd: usage?.costUsd ?? 0,
    inputTokens: usage?.inputTokens ?? 0,
    outputTokens: usage?.outputTokens ?? 0,
    cacheTokens,
    archived: slot.archived === true,
    currentPhase: slot.currentPhase ?? null,
  };
}

/**
 * Create a tool summary message for the WebView
 *
 * Shows a summary of tool usage after stage completion.
 *
 * @param summary The tool call summary from OutputWindowState
 * @param formatted Pre-formatted summary string
 */
export function createToolSummaryMessage(
  summary: ToolCallSummary,
  formatted: string
): ExtensionToWebViewMessage {
  // Convert Map to plain object for serialization
  const byTool: Record<string, number> = {};
  for (const [tool, count] of summary.byTool.entries()) {
    byTool[tool] = count;
  }

  return {
    type: "tool-summary",
    summary: {
      total: summary.total,
      byTool,
      formatted,
    },
  };
}

/**
 * Create a question prompt message for the WebView (Issue #118)
 *
 * Displays an AskUserQuestion prompt with options for user selection.
 *
 * @param id Unique identifier for this question session
 * @param payload The AskUserQuestion payload from Claude
 * @param toolUseId Optional tool_use ID for matching tool_result
 */
export function createQuestionPromptMessage(
  id: string,
  payload: AskUserQuestionPayload,
  toolUseId?: string
): ExtensionToWebViewMessage {
  return {
    type: "question-prompt",
    question: {
      id,
      questions: payload.questions.map((q) => ({
        question: q.question,
        header: q.header,
        options: q.options.map((opt) => ({
          label: opt.label,
          description: opt.description,
        })),
        multiSelect: q.multiSelect,
      })),
      toolUseId,
    },
  };
}

/**
 * Create a question answered message for the WebView (Issue #118)
 *
 * Marks a question prompt as answered with the user's response.
 *
 * @param questionId The unique identifier of the question
 * @param answer The user's answer
 */
export function createQuestionAnsweredMessage(
  questionId: string,
  answer: unknown
): ExtensionToWebViewMessage {
  return {
    type: "question-answered",
    questionId,
    answer,
  };
}

/**
 * Create a pipeline state message for the WebView (Issue #431)
 *
 * Notifies the WebView of pipeline running state changes so it can
 * update pipeline-dependent UI elements (e.g., Stop button).
 *
 * @param isRunning Whether a pipeline is running
 * @param _isBatchRunning Deprecated - kept for call-site compatibility
 * @param currentStage Current pipeline stage name, or null if not running
 */
export function createPipelineStateMessage(
  isRunning: boolean,
  _isBatchRunning: boolean,
  currentStage: string | null
): ExtensionToWebViewMessage {
  return {
    type: "pipeline-state",
    state: {
      isRunning,
      currentStage,
    },
  };
}

/**
 * Create a search state message for the WebView (Issue #158)
 *
 * Notifies the WebView of search state (on initial load or when extension changes it).
 *
 * @param searchText Current search text
 * @param caseSensitive Whether search is case-sensitive
 * @param useRegex Whether to use regex matching
 */
export function createSearchStateMessage(
  searchText: string,
  caseSensitive: boolean,
  useRegex: boolean
): ExtensionToWebViewMessage {
  return {
    type: "set-search-state",
    state: {
      searchText,
      caseSensitive,
      useRegex,
    },
  };
}

/**
 * Create an execution mode message for the WebView (Issue #496)
 *
 * Notifies the WebView of the current execution mode for rendering.
 *
 * @param mode The execution mode ('headless' or 'interactive')
 */
export function createSetModeMessage(mode: ExecutionMode): ExtensionToWebViewMessage {
  return {
    type: "set-mode",
    mode: { mode },
  };
}

/**
 * Create a remove-stall-warnings message for the WebView (Issue #797)
 *
 * Removes stall warning entries from the output when a stage completes.
 * This auto-clears transient stall warnings that are no longer relevant.
 *
 * @param stage The pipeline stage whose stall warnings should be removed
 */
export function createRemoveStallWarningsMessage(stage: string): ExtensionToWebViewMessage {
  return { type: "remove-stall-warnings", stage };
}

/**
 * Create a collapse-stage message for the WebView
 *
 * Wraps all output entries for a completed stage into a collapsed <details>
 * group so past stages take minimal vertical space. The currently running
 * stage remains expanded for live visibility.
 */
export function createCollapseStageMessage(
  stage: string,
  status: "complete" | "error" | "skipped"
): ExtensionToWebViewMessage {
  return { type: "collapse-stage", stage, status };
}

/**
 * Create an add-stall-warning message for the WebView (Issue #2655)
 *
 * Notifies the WebView of a stall warning at 1×, 2×, or 3× threshold
 * so it can render escalation labels and timestamp the entry.
 *
 * @param stage - The pipeline stage that is stalled
 * @param elapsedMs - Elapsed time in milliseconds since stage started
 * @param thresholdMs - Configured stall threshold in milliseconds
 * @param multiplier - Escalation level (1 = first warning, 2 = 2×, 3 = 3×, etc.)
 */
export function createAddStallWarningMessage(
  stage: string,
  elapsedMs: number,
  thresholdMs: number,
  multiplier: number
): ExtensionToWebViewMessage {
  const elapsedSec = Math.floor(elapsedMs / 1000);
  const thresholdSec = Math.floor(thresholdMs / 1000);
  const multiplierLabel =
    multiplier === 1
      ? "Stall Warning"
      : multiplier === 2
        ? "⚠️ Stall Warning (2×)"
        : multiplier === 3
          ? "⚠️⚠️ Stall Warning (3×)"
          : `Stall Warning (${multiplier}×)`;
  const text = `[${new Date().toISOString()}] ${multiplierLabel}: Stage has been running for ${elapsedSec}s (threshold: ${thresholdSec}s)`;

  return {
    type: "add-stall-warning",
    stage,
    entry: {
      id: `stall-${stage}-${elapsedMs}-${multiplier}`,
      text,
      level: "warning",
      stage,
      timestamp: new Date().toISOString(),
    },
    multiplier,
  };
}
