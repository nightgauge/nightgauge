/**
 * Validation Tool Definitions
 *
 * Custom tool definitions for build, lint, test, and typecheck operations.
 * These tools enable pipeline stages to invoke validation commands from
 * code execution sandboxes via Programmatic Tool Calling (PTC).
 *
 * @see Issue #1068 - Pipeline: Expose stage operations as programmatic-callable custom tools
 */

import type { CustomToolDefinition } from "../ToolDefinition.js";

export const RUN_BUILD_TOOL: CustomToolDefinition = {
  name: "run_build",
  description:
    "Execute the project build command (e.g., npm run build). " +
    "Returns JSON: { success: boolean, exit_code: number, stdout: string, " +
    "stderr: string, duration_ms: number }",
  input_schema: {
    type: "object",
    properties: {
      command: {
        type: "string",
        description: "Build command to run. Defaults to 'npm run build' if omitted.",
      },
      cwd: {
        type: "string",
        description: "Working directory. Defaults to repository root.",
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};

export const RUN_LINT_TOOL: CustomToolDefinition = {
  name: "run_lint",
  description:
    "Execute the project lint command (e.g., npm run lint). " +
    "Returns JSON: { success: boolean, exit_code: number, stdout: string, " +
    "stderr: string, warning_count: number, error_count: number, duration_ms: number }",
  input_schema: {
    type: "object",
    properties: {
      command: {
        type: "string",
        description: "Lint command to run. Defaults to 'npm run lint' if omitted.",
      },
      cwd: {
        type: "string",
        description: "Working directory. Defaults to repository root.",
      },
      fix: {
        type: "boolean",
        description: "Whether to auto-fix lint issues. Defaults to false.",
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};

export const RUN_TESTS_TOOL: CustomToolDefinition = {
  name: "run_tests",
  description:
    "Execute the project test command (e.g., npm test). " +
    "Returns JSON: { success: boolean, exit_code: number, passed: number, " +
    "failed: number, skipped: number, coverage?: number, stdout: string, " +
    "stderr: string, duration_ms: number }",
  input_schema: {
    type: "object",
    properties: {
      command: {
        type: "string",
        description: "Test command to run. Defaults to 'npm test' if omitted.",
      },
      cwd: {
        type: "string",
        description: "Working directory. Defaults to repository root.",
      },
      pattern: {
        type: "string",
        description: 'Test file pattern to filter (e.g., "*.test.ts").',
      },
      coverage: {
        type: "boolean",
        description: "Whether to collect coverage data. Defaults to false.",
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};

export const RUN_TYPECHECK_TOOL: CustomToolDefinition = {
  name: "run_typecheck",
  description:
    "Execute the TypeScript type checker (e.g., npx tsc --noEmit). " +
    "Returns JSON: { success: boolean, exit_code: number, stdout: string, " +
    "stderr: string, error_count: number, duration_ms: number }",
  input_schema: {
    type: "object",
    properties: {
      command: {
        type: "string",
        description: "Typecheck command to run. Defaults to 'npx tsc --noEmit' if omitted.",
      },
      cwd: {
        type: "string",
        description: "Working directory. Defaults to repository root.",
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};
