/**
 * Settings module barrel export
 *
 * Re-exports all settings-related classes and utilities.
 */

// Main panel
export { SettingsPanel } from "./SettingsPanel";

// YAML Service
export { NightgaugeYamlService, type ReadResult, type WriteResult } from "./NightgaugeYamlService";

// Config Utilities (pure functions, no VSCode dependency)
export {
  validateConfig,
  mergeWithDefaults,
  getConfigValue,
  setConfigValue,
  removeUndefined,
} from "./configUtils";

// HTML Generator
export { getSettingsHtml } from "./SettingsHtml";

// Message Handler
export {
  SettingsMessageHandler,
  createUpdateMessage,
  createSavedMessage,
  createErrorMessage,
  createLockedMessage,
  type ExtensionToWebViewMessage as SettingsExtensionToWebViewMessage,
  type WebViewToExtensionMessage as SettingsWebViewToExtensionMessage,
  type SettingsMessageCallbacks,
} from "./SettingsMessageHandler";

// Types
export {
  type NightgaugeConfig,
  type ProjectConfig,
  type PullRequestConfig,
  type BranchConfig,
  type IssueConfig,
  type PipelineConfig,
  type CommandsConfig,
  type ValidationConfig,
  type SanitizationConfig,
  type MergeStrategy,
  type ValidationError,
  type ValidationResult,
  type SettingsSectionMeta,
  DEFAULT_CONFIG,
  SETTINGS_SECTIONS,
} from "./types";
