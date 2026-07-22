/**
 * Tool Definition Types and Zod Schemas
 *
 * Defines the canonical tool definition shape matching the Anthropic API
 * for future Programmatic Tool Calling (PTC) support. The `allowed_callers`
 * field restricts who can invoke a custom tool (direct LLM call vs.
 * code_execution sandbox).
 *
 * @see docs/spikes/1065-agent-sdk-tool-calling-feasibility.md
 * @see Issue #1066 - SDK Tool Definition Registry
 */

import { z } from "zod";

/**
 * Valid caller types for custom tools.
 *
 * - `direct`: The LLM can call the tool directly
 * - `code_execution_20250825`: Tool can only be called from the code execution sandbox (v1)
 * - `code_execution_20260120`: Tool can only be called from the REPL/daemon sandbox (v2)
 */
export const AllowedCallerSchema = z.enum([
  "direct",
  "code_execution_20250825",
  "code_execution_20260120",
]);

export type AllowedCaller = z.infer<typeof AllowedCallerSchema>;

/**
 * Tool type classification.
 *
 * - `builtin`: A Claude Code built-in tool (Bash, Read, Edit, etc.)
 * - `custom`: A custom tool with full definition and input_schema
 * - `code_execution`: An Anthropic API server-side code execution tool
 */
export const ToolTypeSchema = z.enum(["builtin", "custom", "code_execution"]);

export type ToolType = z.infer<typeof ToolTypeSchema>;

/**
 * Custom tool definition matching the Anthropic API tool shape.
 *
 * This is the canonical format for defining custom tools that can be
 * passed to the Anthropic Messages API for PTC execution.
 */
export const CustomToolDefinitionSchema = z.object({
  name: z
    .string()
    .min(1)
    .regex(
      /^[a-zA-Z_][a-zA-Z0-9_]*$/,
      "Tool name must be a valid identifier (letters, digits, underscores; cannot start with a digit)"
    ),
  description: z.string().min(1),
  input_schema: z.record(z.string(), z.unknown()),
  allowed_callers: z.array(AllowedCallerSchema).optional(),
});

export type CustomToolDefinition = z.infer<typeof CustomToolDefinitionSchema>;

/**
 * A registry entry representing either a built-in tool (name only)
 * or a custom tool (full definition).
 */
export const ToolEntrySchema = z.object({
  type: ToolTypeSchema,
  name: z.string().min(1),
  definition: CustomToolDefinitionSchema.optional(),
});

export type ToolEntry = z.infer<typeof ToolEntrySchema>;
