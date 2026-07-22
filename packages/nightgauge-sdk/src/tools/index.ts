/**
 * Tools Module - Tool Definition Registry
 *
 * Provides infrastructure for managing both built-in tool names and
 * custom tool definitions with `allowed_callers` for Programmatic
 * Tool Calling (PTC).
 *
 * @see Issue #1066 - SDK Tool Definition Registry
 */

export {
  // Zod Schemas
  AllowedCallerSchema,
  ToolTypeSchema,
  CustomToolDefinitionSchema,
  ToolEntrySchema,
  // Types
  type AllowedCaller,
  type ToolType,
  type CustomToolDefinition,
  type ToolEntry,
} from "./ToolDefinition.js";

export { ToolRegistry } from "./ToolRegistry.js";

// PTC - Programmatic Tool Calling (Issue #1069)
export { PTCExecutor, type PTCResult, type PTCExecutorOptions } from "./PTCExecutor.js";
export {
  PTCValidationRunner,
  isPTCAvailable,
  type ValidationResult,
  type DevContextInput,
  type PTCValidationRunnerOptions,
} from "./PTCValidationRunner.js";
export {
  type ToolHandler,
  type ToolResult,
  RunBuildHandler,
  RunLintHandler,
  RunTestsHandler,
  RunTypecheckHandler,
  createValidationHandlers,
  createContextHandlers,
  createGitHandlers,
} from "./tool-handlers.js";

// Context Handlers (Issue #1070)
export { ReadContextFileHandler, ListContextFilesHandler } from "./context-handlers.js";

// Git Handlers (Issue #1070)
export {
  GitDiffSummaryHandler,
  GitLogStructuredHandler,
  GitStatusStructuredHandler,
} from "./git-handlers.js";

// PTC Context Gatherer (Issue #1070)
export {
  PTCContextGatherer,
  type ContextGatherResult,
  type ContextGatherInput,
  type PTCContextGathererOptions,
} from "./PTCContextGatherer.js";

// Selective Test Runner (Issue #1973)
export {
  SelectiveTestRunner,
  buildVitestArgs,
  type SelectiveTestRunnerConfig,
  type SelectiveTestResult,
} from "./selective-test-runner/index.js";

// Integration Test Gate (Issue #2909)
export {
  classifyIntegrationOutcome,
  detectIntegrationRequirement,
  evaluateGate,
  type ClassifiedIntegrationOutcome,
  type IntegrationDetectionSignals,
  type IntegrationGateDecision,
  type IntegrationGateMode,
  type IntegrationRequirement,
  type IntegrationRunOutcome,
} from "./integration-test-gate/index.js";

// Pipeline Tool Definitions (Issue #1068)
export {
  // Individual tool definitions
  RUN_BUILD_TOOL,
  RUN_LINT_TOOL,
  RUN_TESTS_TOOL,
  RUN_TYPECHECK_TOOL,
  READ_CONTEXT_FILE_TOOL,
  WRITE_CONTEXT_FILE_TOOL,
  LIST_CONTEXT_FILES_TOOL,
  GIT_DIFF_SUMMARY_TOOL,
  GIT_LOG_STRUCTURED_TOOL,
  GIT_STATUS_STRUCTURED_TOOL,
  // Category arrays
  VALIDATION_TOOLS,
  CONTEXT_TOOLS,
  GIT_TOOLS,
  // Factory functions
  getAllPipelineToolDefinitions,
  registerPipelineTools,
} from "./definitions/index.js";
