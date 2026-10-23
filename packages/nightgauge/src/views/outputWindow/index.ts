/**
 * Output Window barrel export
 *
 * Re-exports all output window classes and types for public API.
 */

// Main output window class
export { OutputWindow, type OutputWindowConfig } from "./OutputWindow";

// State management
export {
  OutputWindowState,
  PIPELINE_STAGES,
  type OutputLevel,
  type OutputEntry,
  type StageStatus,
  type StageProgress,
  type TokenUsage,
} from "./OutputWindowState";

// HTML generation
export { getOutputWindowHtml, escapeHtml } from "./OutputWindowHtml";

// Message handling
export {
  OutputWindowMessageHandler,
  serializeEntry,
  createAppendMessage,
  createClearMessage,
  createCollapseStageMessage,
  createAutoScrollMessage,
  type ExtensionToWebViewMessage,
  type WebViewToExtensionMessage,
  type SerializedOutputEntry,
  type MessageHandlerCallbacks,
} from "./OutputWindowMessageHandler";
