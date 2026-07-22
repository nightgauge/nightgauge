/**
 * ToolRegistry - Manages built-in tool names and custom tool definitions.
 *
 * Distinguishes between:
 * - **Built-in tools**: String names (Bash, Read, Edit, etc.) for Claude Code's allowedTools
 * - **Custom tools**: Full definitions with input_schema and optional allowed_callers for PTC
 *
 * The registry prepares tool definitions for both the existing Agent SDK path
 * (getBuiltinToolNames() → allowedTools string[]) and the future PTC executor
 * path (getCustomToolDefinitions() → Anthropic API tool objects).
 *
 * @see docs/spikes/1065-agent-sdk-tool-calling-feasibility.md
 * @see Issue #1066 - SDK Tool Definition Registry
 */

import {
  CustomToolDefinitionSchema,
  type CustomToolDefinition,
  type ToolEntry,
} from "./ToolDefinition.js";

/**
 * Registry for managing tool definitions across the pipeline.
 *
 * @example
 * ```typescript
 * // Create from SKILL.md frontmatter data
 * const registry = ToolRegistry.fromSkillFrontmatter(
 *   ['Read', 'Write', 'Bash'],
 *   ['query_database'],
 *   [{ name: 'query_database', description: 'Run SQL', input_schema: { type: 'object', properties: {} } }]
 * );
 *
 * // Get built-in tool names for allowedTools
 * registry.getBuiltinToolNames(); // ['Read', 'Write', 'Bash']
 *
 * // Get custom definitions for future PTC executor
 * registry.getCustomToolDefinitions(); // [{ name: 'query_database', ... }]
 * ```
 */
export class ToolRegistry {
  private readonly entries = new Map<string, ToolEntry>();

  /**
   * Register a built-in tool by name.
   * Built-in tools are Claude Code's native tools (Bash, Read, Edit, etc.).
   */
  registerBuiltinTool(name: string): void {
    this.entries.set(name, { type: "builtin", name });
  }

  /**
   * Register a custom tool with a full definition.
   * Validates the definition against CustomToolDefinitionSchema.
   *
   * @throws {ZodError} if the definition fails validation
   */
  registerCustomTool(definition: CustomToolDefinition): void {
    const validated = CustomToolDefinitionSchema.parse(definition);
    this.entries.set(validated.name, {
      type: "custom",
      name: validated.name,
      definition: validated,
    });
  }

  /**
   * Get a tool entry by name.
   */
  get(name: string): ToolEntry | undefined {
    return this.entries.get(name);
  }

  /**
   * Check if a tool is registered.
   */
  has(name: string): boolean {
    return this.entries.has(name);
  }

  /**
   * Get all built-in tool names.
   * Returns the string[] compatible with the Agent SDK's allowedTools parameter.
   */
  getBuiltinToolNames(): string[] {
    return [...this.entries.values()]
      .filter((entry) => entry.type === "builtin")
      .map((entry) => entry.name);
  }

  /**
   * Get all custom tool definitions.
   * Returns the CustomToolDefinition[] for future PTC executor consumption.
   */
  getCustomToolDefinitions(): CustomToolDefinition[] {
    return [...this.entries.values()]
      .filter(
        (entry): entry is ToolEntry & { definition: CustomToolDefinition } =>
          entry.type === "custom" && entry.definition !== undefined
      )
      .map((entry) => entry.definition);
  }

  /**
   * Get all tool entries.
   */
  getAll(): ToolEntry[] {
    return [...this.entries.values()];
  }

  /**
   * Get the number of registered tools.
   */
  get size(): number {
    return this.entries.size;
  }

  /**
   * Create a ToolRegistry from parsed SKILL.md frontmatter data.
   *
   * @param allowedTools - Built-in tool names from `allowed-tools:` frontmatter
   * @param programmaticTools - Tool names from `programmatic-tools:` frontmatter (optional)
   * @param customDefinitions - Full definitions for programmatic tools (optional)
   */
  static fromSkillFrontmatter(
    allowedTools: string[],
    programmaticTools?: string[],
    customDefinitions?: CustomToolDefinition[]
  ): ToolRegistry {
    const registry = new ToolRegistry();

    // Register all built-in tools
    for (const name of allowedTools) {
      registry.registerBuiltinTool(name);
    }

    // Register custom tool definitions if provided
    if (customDefinitions) {
      for (const definition of customDefinitions) {
        registry.registerCustomTool(definition);
      }
    }

    // For programmatic tool names without definitions, register as custom
    // with a placeholder entry. This allows the registry to track that
    // these tools are referenced even before definitions are provided.
    if (programmaticTools) {
      for (const name of programmaticTools) {
        if (!registry.has(name)) {
          registry.entries.set(name, { type: "custom", name });
        }
      }
    }

    return registry;
  }
}
