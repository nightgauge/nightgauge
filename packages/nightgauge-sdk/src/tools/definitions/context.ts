/**
 * Context File Tool Definitions
 *
 * Custom tool definitions for reading, writing, and listing pipeline
 * context files in `.nightgauge/pipeline/`. These tools enable
 * pipeline stages to manage context handoff from code execution sandboxes.
 *
 * @see Issue #1068 - Pipeline: Expose stage operations as programmatic-callable custom tools
 */

import type { CustomToolDefinition } from "../ToolDefinition.js";

export const READ_CONTEXT_FILE_TOOL: CustomToolDefinition = {
  name: "read_context_file",
  description:
    "Read a pipeline context file from .nightgauge/pipeline/. " +
    "Returns JSON: { success: boolean, filename: string, content: object, " +
    "schema_version: string }",
  input_schema: {
    type: "object",
    properties: {
      filename: {
        type: "string",
        description: 'Context filename to read (e.g., "issue-42.json", "planning-42.json").',
      },
    },
    required: ["filename"],
  },
  allowed_callers: ["code_execution_20250825"],
};

export const WRITE_CONTEXT_FILE_TOOL: CustomToolDefinition = {
  name: "write_context_file",
  description:
    "Write a pipeline context file to .nightgauge/pipeline/. " +
    "Returns JSON: { success: boolean, filename: string, path: string, " +
    "bytes_written: number }",
  input_schema: {
    type: "object",
    properties: {
      filename: {
        type: "string",
        description: 'Context filename to write (e.g., "dev-42.json", "pr-42.json").',
      },
      content: {
        type: "object",
        description: "JSON content to write to the context file.",
      },
    },
    required: ["filename", "content"],
  },
  allowed_callers: ["code_execution_20250825"],
};

export const LIST_CONTEXT_FILES_TOOL: CustomToolDefinition = {
  name: "list_context_files",
  description:
    "List pipeline context files in .nightgauge/pipeline/. " +
    "Returns JSON: { success: boolean, files: Array<{ filename: string, " +
    "size_bytes: number, modified_at: string }>, count: number }",
  input_schema: {
    type: "object",
    properties: {
      pattern: {
        type: "string",
        description: 'Optional regex pattern to filter filenames (e.g., "issue-.*\\.json").',
      },
    },
    required: [],
  },
  allowed_callers: ["code_execution_20250825"],
};
