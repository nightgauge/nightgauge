/**
 * Git Operation Tool Definitions
 *
 * Custom tool definitions for structured git diff, log, and status
 * operations. These tools enable pipeline stages to inspect repository
 * state from code execution sandboxes via Programmatic Tool Calling (PTC).
 *
 * @see Issue #1068 - Pipeline: Expose stage operations as programmatic-callable custom tools
 */

import type { CustomToolDefinition } from "../ToolDefinition.js";

export const GIT_DIFF_SUMMARY_TOOL: CustomToolDefinition = {
  name: "git_diff_summary",
  description:
    "Get a structured summary of git diff between two refs. " +
    "Returns JSON: { success: boolean, files_changed: number, " +
    "insertions: number, deletions: number, entries: Array<{ file: string, " +
    "status: string, insertions: number, deletions: number }> }",
  input_schema: {
    type: "object",
    properties: {
      base: {
        type: "string",
        description: "Base ref for comparison. Defaults to HEAD~1.",
      },
      head: {
        type: "string",
        description: "Head ref for comparison. Defaults to HEAD.",
      },
      staged_only: {
        type: "boolean",
        description: "If true, only show staged changes (git diff --cached). Defaults to false.",
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};

export const GIT_LOG_STRUCTURED_TOOL: CustomToolDefinition = {
  name: "git_log_structured",
  description:
    "Get structured git log entries. " +
    "Returns JSON: { success: boolean, commits: Array<{ sha: string, " +
    "short_sha: string, message: string, author: string, date: string, " +
    "files_changed: number }>, total: number }",
  input_schema: {
    type: "object",
    properties: {
      count: {
        type: "number",
        description: "Number of commits to return. Defaults to 10.",
      },
      since: {
        type: "string",
        description:
          'Only show commits after this date (ISO 8601 or relative, e.g., "2 days ago").',
      },
      path: {
        type: "string",
        description: "Only show commits affecting this file or directory path.",
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};

export const GIT_STATUS_STRUCTURED_TOOL: CustomToolDefinition = {
  name: "git_status_structured",
  description:
    "Get structured git status of the working tree. " +
    "Returns JSON: { success: boolean, branch: string, " +
    "staged: Array<{ file: string, status: string }>, " +
    "unstaged: Array<{ file: string, status: string }>, " +
    "untracked: string[], is_clean: boolean }",
  input_schema: {
    type: "object",
    properties: {
      short: {
        type: "boolean",
        description: "If true, return abbreviated status output. Defaults to false.",
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};
