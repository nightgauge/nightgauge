/**
 * Adapter barrel export
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 */

export type {
  ICliAdapter,
  IncrediAdapter,
  QueryFunctionOptions,
  ValidateAuthOptions,
} from "./ICliAdapter.js";

export { ClaudeSdkAdapter } from "./ClaudeSdkAdapter.js";
export { ClaudeHeadlessAdapter } from "./ClaudeHeadlessAdapter.js";
export { CodexAdapter } from "./CodexAdapter.js";
export { GeminiAdapter } from "./GeminiAdapter.js";
export { GeminiSdkAdapter } from "./GeminiSdkAdapter.js";
export { LmStudioAdapter } from "./LmStudioAdapter.js";
export { CopilotCliAdapter } from "./CopilotCliAdapter.js";
export { AdapterRegistry, defaultRegistry, isAgenticAdapter } from "./AdapterRegistry.js";

// Canonical Codex model registry — single source of truth for Codex model ids,
// deprecation metadata, and the tier→model routing map (#4018).
export {
  CODEX_MODELS,
  CODEX_TIER_MODEL_MAP,
  CODEX_RECOMMENDED_DEFAULT_MODEL,
  CODEX_DEFAULT_BASE_MODEL,
  isValidCodexModel,
  isDeprecatedCodexModel,
  isResearchPreviewCodexModel,
  listCodexModels,
  resolveCodexModelAlias,
} from "./codexModelRegistry.js";
export type { CodexTier, CodexModelMeta, ListCodexModelsOptions } from "./codexModelRegistry.js";

// Provider-aware model preflight — fail fast on an invalid (adapter, model)
// pair before the model reaches a CLI/SDK (#4021).
export {
  validateModelForAdapter,
  resolveAndValidateModel,
  ADAPTER_MODEL_POLICY,
  GEMINI_MODELS,
} from "./modelPreflight.js";
export type { ModelValidationResult, AdapterModelPolicy, ModelSetKind } from "./modelPreflight.js";

// Map a stage's allowed-tools onto Codex's sandbox mode + approval policy (#4026).
export {
  resolveCodexSandboxMode,
  codexSandboxFlags,
  applyCodexSandboxProfile,
  CODEX_BYPASS_FLAG,
} from "./codexSandbox.js";
export type { CodexSandboxMode } from "./codexSandbox.js";

export { validateCLIAuth, verifyCLIInstalled } from "./validateCLIAuth.js";
export type { AuthSubcommand } from "./validateCLIAuth.js";

export { createCliQueryFn, parseCliArgs, runCliCommand } from "./cliQueryHelper.js";

// Claude native Dynamic Workflows ("ultracode") offload — version gate,
// downgrade signal, and the sink-emitting driver (#3910). The version predicate
// is reused by the WorkflowExecutor (#3908).
export {
  MIN_NATIVE_WORKFLOW_VERSION,
  ULTRACODE_KEYWORD_RENAME_VERSION,
  supportsNativeWorkflow,
  ultracodeKeyword,
  parseVersion,
  preflightNativeWorkflow,
  isNativeWorkflowDisabledByEnv,
  detectClaudeCliVersion,
  detectClaudeSdkVersion,
  mapNativeUsage,
  emitNativeWorkflowTree,
  runClaudeNativeWorkflow,
  NativeWorkflowUnavailableError,
  type NativeWorkflowUnavailableReason,
  type NativeWorkflowReadiness,
  type NativeWorkflowSurface,
  type ClaudeNativeWorkflowOptions,
  type NativeAgentUsageReport,
  type NativeProgressEvent,
} from "./ClaudeNativeWorkflow.js";
