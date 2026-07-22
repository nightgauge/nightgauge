/**
 * Pipeline Tool Definitions - Barrel Export
 *
 * Exports all pipeline tool definitions organized by category,
 * plus convenience functions for bulk registration and retrieval.
 *
 * @see Issue #1068 - Pipeline: Expose stage operations as programmatic-callable custom tools
 */

import type { CustomToolDefinition } from "../ToolDefinition.js";
import { type ToolRegistry } from "../ToolRegistry.js";

// Re-export individual tool definitions
export { RUN_BUILD_TOOL, RUN_LINT_TOOL, RUN_TESTS_TOOL, RUN_TYPECHECK_TOOL } from "./validation.js";

export {
  READ_CONTEXT_FILE_TOOL,
  WRITE_CONTEXT_FILE_TOOL,
  LIST_CONTEXT_FILES_TOOL,
} from "./context.js";

export {
  GIT_DIFF_SUMMARY_TOOL,
  GIT_LOG_STRUCTURED_TOOL,
  GIT_STATUS_STRUCTURED_TOOL,
} from "./git.js";

// Import for use in arrays and functions
import { RUN_BUILD_TOOL, RUN_LINT_TOOL, RUN_TESTS_TOOL, RUN_TYPECHECK_TOOL } from "./validation.js";

import {
  READ_CONTEXT_FILE_TOOL,
  WRITE_CONTEXT_FILE_TOOL,
  LIST_CONTEXT_FILES_TOOL,
} from "./context.js";

import {
  GIT_DIFF_SUMMARY_TOOL,
  GIT_LOG_STRUCTURED_TOOL,
  GIT_STATUS_STRUCTURED_TOOL,
} from "./git.js";

/** Validation tool definitions: build, lint, test, typecheck */
export const VALIDATION_TOOLS: CustomToolDefinition[] = [
  RUN_BUILD_TOOL,
  RUN_LINT_TOOL,
  RUN_TESTS_TOOL,
  RUN_TYPECHECK_TOOL,
];

/** Context file tool definitions: read, write, list */
export const CONTEXT_TOOLS: CustomToolDefinition[] = [
  READ_CONTEXT_FILE_TOOL,
  WRITE_CONTEXT_FILE_TOOL,
  LIST_CONTEXT_FILES_TOOL,
];

/** Git operation tool definitions: diff, log, status */
export const GIT_TOOLS: CustomToolDefinition[] = [
  GIT_DIFF_SUMMARY_TOOL,
  GIT_LOG_STRUCTURED_TOOL,
  GIT_STATUS_STRUCTURED_TOOL,
];

/** Get all pipeline tool definitions for bulk operations */
export function getAllPipelineToolDefinitions(): CustomToolDefinition[] {
  return [...VALIDATION_TOOLS, ...CONTEXT_TOOLS, ...GIT_TOOLS];
}

/** Register all pipeline tools into a ToolRegistry instance */
export function registerPipelineTools(registry: ToolRegistry): void {
  for (const tool of getAllPipelineToolDefinitions()) {
    registry.registerCustomTool(tool);
  }
}
