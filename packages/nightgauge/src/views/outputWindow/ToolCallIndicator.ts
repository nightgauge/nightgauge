/**
 * ToolCallIndicator - Tool call visual indicator system
 *
 * Provides animated indicators for AI tool calls, replacing verbose
 * `[Tool: Edit]` entries with engaging visual feedback.
 *
 * Features:
 * - Tool-specific icons and colors
 * - Target file extraction from tool arguments
 * - Animation timing configuration
 * - Aggregation for stage summaries
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

/**
 * Supported tool types for indicators
 */
export type ToolType =
  | "Edit"
  | "Read"
  | "Write"
  | "Bash"
  | "Glob"
  | "Grep"
  | "Task"
  | "WebFetch"
  | "WebSearch"
  | "AskUserQuestion"
  | "TodoWrite"
  | "Unknown";

/**
 * Tool indicator configuration
 */
export interface ToolIndicatorConfig {
  /** Icon to display (VS Code codicon or emoji) */
  icon: string;
  /** Display label for the tool */
  label: string;
  /** CSS color class for the indicator */
  colorClass: string;
  /** Animation type when active */
  animation: "pulse" | "spin" | "bounce";
}

/**
 * Tool call data for indicator display
 */
export interface ToolCallData {
  /** The tool being used */
  tool: ToolType;
  /** Target file or resource */
  target: string;
  /** Whether this tool call is currently active */
  isActive: boolean;
  /** Unique identifier for this tool call */
  id: string;
  /** Timestamp when the tool call started */
  startedAt: Date;
  /** Raw arguments (for verbose/debug display) */
  args?: Record<string, unknown>;
}

/**
 * Tool aggregation for stage summary
 */
export interface ToolCallSummary {
  /** Total number of tool calls */
  total: number;
  /** Breakdown by tool type */
  byTool: Map<ToolType, number>;
  /** Start time of aggregation period */
  startedAt: Date;
  /** End time (when summary is generated) */
  endedAt: Date;
}

/**
 * Tool indicator configurations
 */
const TOOL_CONFIGS: Record<ToolType, ToolIndicatorConfig> = {
  Edit: {
    icon: "$(edit)",
    label: "Edit",
    colorClass: "tool-edit",
    animation: "pulse",
  },
  Read: {
    icon: "$(file)",
    label: "Read",
    colorClass: "tool-read",
    animation: "pulse",
  },
  Write: {
    icon: "$(new-file)",
    label: "Write",
    colorClass: "tool-write",
    animation: "pulse",
  },
  Bash: {
    icon: "$(terminal)",
    label: "Bash",
    colorClass: "tool-bash",
    animation: "spin",
  },
  Glob: {
    icon: "$(search)",
    label: "Glob",
    colorClass: "tool-search",
    animation: "pulse",
  },
  Grep: {
    icon: "$(search)",
    label: "Grep",
    colorClass: "tool-search",
    animation: "pulse",
  },
  Task: {
    icon: "$(tasklist)",
    label: "Task",
    colorClass: "tool-task",
    animation: "bounce",
  },
  WebFetch: {
    icon: "$(cloud-download)",
    label: "Fetch",
    colorClass: "tool-web",
    animation: "spin",
  },
  WebSearch: {
    icon: "$(globe)",
    label: "Search",
    colorClass: "tool-web",
    animation: "spin",
  },
  AskUserQuestion: {
    icon: "$(question)",
    label: "Question",
    colorClass: "tool-question",
    animation: "bounce",
  },
  TodoWrite: {
    icon: "$(checklist)",
    label: "Todo",
    colorClass: "tool-todo",
    animation: "pulse",
  },
  Unknown: {
    icon: "$(tools)",
    label: "Tool",
    colorClass: "tool-unknown",
    animation: "pulse",
  },
};

/**
 * Get the configuration for a tool type
 */
export function getToolConfig(tool: ToolType): ToolIndicatorConfig {
  return TOOL_CONFIGS[tool] || TOOL_CONFIGS.Unknown;
}

/**
 * Parse a tool name string into a ToolType
 */
export function parseToolType(name: string): ToolType {
  // Normalize the name
  const normalized = name.trim();

  // Direct match
  if (normalized in TOOL_CONFIGS) {
    return normalized as ToolType;
  }

  // Case-insensitive match
  const lower = normalized.toLowerCase();
  for (const key of Object.keys(TOOL_CONFIGS)) {
    if (key.toLowerCase() === lower) {
      return key as ToolType;
    }
  }

  // Handle common variations
  const variations: Record<string, ToolType> = {
    editfile: "Edit",
    readfile: "Read",
    writefile: "Write",
    command: "Bash",
    shell: "Bash",
    exec: "Bash",
    find: "Glob",
    search: "Grep",
    rg: "Grep",
    ripgrep: "Grep",
    subagent: "Task",
    agent: "Task",
    fetch: "WebFetch",
    websearch: "WebSearch",
    question: "AskUserQuestion",
    ask: "AskUserQuestion",
    todo: "TodoWrite",
    todolist: "TodoWrite",
  };

  return variations[lower] || "Unknown";
}

/**
 * Extract the target file or resource from tool arguments
 */
export function extractTarget(tool: ToolType, args?: Record<string, unknown>): string {
  if (!args) {
    return "";
  }

  // Common argument names for file paths
  const filePathKeys = ["file_path", "filePath", "path", "file", "filename", "target"];

  for (const key of filePathKeys) {
    if (typeof args[key] === "string" && args[key]) {
      return extractFilename(args[key] as string);
    }
  }

  // Tool-specific extraction
  switch (tool) {
    case "Bash":
      if (typeof args.command === "string") {
        return truncateCommand(args.command);
      }
      break;

    case "Glob":
    case "Grep":
      if (typeof args.pattern === "string") {
        return args.pattern;
      }
      break;

    case "WebFetch":
    case "WebSearch":
      if (typeof args.url === "string") {
        return extractDomain(args.url);
      }
      if (typeof args.query === "string") {
        return truncateText(args.query, 30);
      }
      break;

    case "Task":
      if (typeof args.description === "string") {
        return truncateText(args.description, 30);
      }
      break;

    case "AskUserQuestion":
      // Handle both single question format and questions array format
      if (typeof args.question === "string") {
        return truncateText(args.question, 30);
      }
      // Extract first question from questions array (Issue #118)
      if (Array.isArray(args.questions) && args.questions.length > 0) {
        const firstQuestion = args.questions[0] as Record<string, unknown>;
        if (typeof firstQuestion?.header === "string") {
          return truncateText(firstQuestion.header, 20);
        }
        if (typeof firstQuestion?.question === "string") {
          return truncateText(firstQuestion.question, 30);
        }
      }
      break;

    case "TodoWrite":
      if (Array.isArray(args.todos)) {
        return `${args.todos.length} items`;
      }
      break;
  }

  return "";
}

/**
 * Extract filename from a full path
 */
function extractFilename(path: string): string {
  const parts = path.split(/[/\\]/);
  return parts[parts.length - 1] || path;
}

/**
 * Truncate a command for display
 */
function truncateCommand(command: string): string {
  // Get first meaningful part of command
  const firstLine = command.split("\n")[0].trim();
  const firstWord = firstLine.split(/\s+/)[0];

  if (firstLine.length <= 40) {
    return firstLine;
  }

  // Show command name + truncated args
  return `${firstWord} ...`;
}

/**
 * Extract domain from URL
 */
function extractDomain(url: string): string {
  try {
    const parsed = new URL(url);
    return parsed.hostname;
  } catch {
    return truncateText(url, 30);
  }
}

/**
 * Truncate text with ellipsis
 */
function truncateText(text: string, maxLength: number): string {
  if (text.length <= maxLength) {
    return text;
  }
  return text.substring(0, maxLength - 3) + "...";
}

/**
 * Generate a unique ID for a tool call
 */
export function generateToolCallId(): string {
  return `tool-${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
}

/**
 * Create a ToolCallData object
 */
export function createToolCallData(
  toolName: string,
  args?: Record<string, unknown>,
  isActive = true
): ToolCallData {
  const tool = parseToolType(toolName);
  const target = extractTarget(tool, args);

  return {
    tool,
    target,
    isActive,
    id: generateToolCallId(),
    startedAt: new Date(),
    args,
  };
}

/**
 * Format a tool summary for display
 *
 * @example
 * "Used 12 tools: 5 Edit, 4 Read, 3 Bash"
 */
export function formatToolSummary(summary: ToolCallSummary): string {
  if (summary.total === 0) {
    return "No tools used";
  }

  // Sort by count descending
  const sorted = Array.from(summary.byTool.entries())
    .filter(([, count]) => count > 0)
    .sort((a, b) => b[1] - a[1]);

  if (sorted.length === 0) {
    return `Used ${summary.total} tools`;
  }

  const breakdown = sorted.map(([tool, count]) => `${count} ${tool}`).join(", ");

  return `Used ${summary.total} tools: ${breakdown}`;
}

/**
 * Format a tool indicator line for display
 *
 * @example
 * "Edit src/utils/helper.ts"
 * "Bash npm test"
 * "Read package.json"
 */
export function formatToolIndicator(data: ToolCallData): string {
  const config = getToolConfig(data.tool);

  if (data.target) {
    return `${config.label} ${data.target}`;
  }

  return config.label;
}
