/**
 * PTCExecutor - Programmatic Tool Calling Executor
 *
 * Wraps the Anthropic Messages API to enable code_execution sandbox
 * with server-side custom tool handling. The executor manages the
 * conversation loop: sending messages, intercepting tool_use blocks,
 * executing tool handlers, and returning tool_result blocks.
 *
 * This is the core engine for PTC-enabled pipeline stages. It does NOT
 * replace the Agent SDK / CLI path — it supplements it for stages that
 * benefit from programmatic tool orchestration.
 *
 * @see docs/spikes/1065-agent-sdk-tool-calling-feasibility.md
 * @see Issue #1069 - Refactor feature-validate for PTC
 */

import Anthropic from "@anthropic-ai/sdk";
import type { CustomToolDefinition } from "./ToolDefinition.js";
import type { ToolHandler, ToolResult } from "./tool-handlers.js";

/** Result from a PTC execution run */
export interface PTCResult {
  /** Whether the execution completed successfully */
  success: boolean;
  /** Structured output extracted from code execution */
  output: unknown;
  /** Raw text output from the conversation */
  textOutput: string;
  /** Token usage for the entire execution */
  usage: {
    inputTokens: number;
    outputTokens: number;
  };
  /** Number of conversation turns taken */
  turns: number;
  /** Total number of tool calls made during execution (Issue #1071) */
  toolCallCount: number;
  /** Number of code_execution blocks run (Issue #1071) */
  codeExecutionCount: number;
  /** True when the model ended the run with stop_reason "refusal" (Issue #75) */
  refusal?: boolean;
  /** Error message if execution failed */
  error?: string;
}

/** Options for constructing a PTCExecutor */
export interface PTCExecutorOptions {
  /** Anthropic API key */
  apiKey: string;
  /** Model to use (defaults to claude-sonnet-4-5-20250929) */
  model?: string;
  /** Max tokens per response (defaults to 16384) */
  maxTokens?: number;
  /** Custom tool definitions to expose to the sandbox */
  tools: CustomToolDefinition[];
  /** Server-side handlers for custom tools */
  toolHandlers: Map<string, ToolHandler>;
  /** Working directory for tool execution */
  cwd: string;
  /** Maximum conversation turns before aborting (defaults to 10) */
  maxTurns?: number;
}

/**
 * Executes a PTC session: sends a prompt to Claude with code_execution
 * and custom tools, handles the tool_use/tool_result loop, and returns
 * the final structured result.
 */
export class PTCExecutor {
  private readonly client: Anthropic;
  private readonly model: string;
  private readonly maxTokens: number;
  private readonly tools: CustomToolDefinition[];
  private readonly toolHandlers: Map<string, ToolHandler>;
  private readonly cwd: string;
  private readonly maxTurns: number;

  constructor(options: PTCExecutorOptions) {
    this.client = new Anthropic({ apiKey: options.apiKey });
    this.model = options.model ?? "claude-sonnet-4-5-20250929";
    this.maxTokens = options.maxTokens ?? 16384;
    this.tools = options.tools;
    this.toolHandlers = options.toolHandlers;
    this.cwd = options.cwd;
    this.maxTurns = options.maxTurns ?? 10;
  }

  /**
   * Execute a PTC session with the given prompt.
   *
   * The conversation loop:
   * 1. Send prompt with code_execution + custom tools
   * 2. If response contains tool_use → execute handler → send tool_result
   * 3. If response is end_turn → extract result and return
   * 4. Repeat until end_turn or maxTurns reached
   */
  async execute(prompt: string): Promise<PTCResult> {
    const apiTools: Anthropic.Messages.Tool[] = [
      {
        type: "code_execution_20250825" as "computer_20250124",
        name: "code_execution",
      } as unknown as Anthropic.Messages.Tool,
      ...this.tools.map(
        (t) =>
          ({
            type: "custom" as const,
            name: t.name,
            description: t.description,
            input_schema: t.input_schema as unknown as Anthropic.Messages.Tool.InputSchema,
            ...(t.allowed_callers ? { allowed_callers: t.allowed_callers } : {}),
          }) as unknown as Anthropic.Messages.Tool
      ),
    ];

    const messages: Anthropic.Messages.MessageParam[] = [{ role: "user", content: prompt }];

    let totalInputTokens = 0;
    let totalOutputTokens = 0;
    let turns = 0;
    let toolCallCount = 0;
    let codeExecutionCount = 0;
    let textOutput = "";
    let lastOutput: unknown = null;

    while (turns < this.maxTurns) {
      turns++;

      let response: Anthropic.Messages.Message;
      try {
        response = await this.client.messages.create({
          model: this.model,
          max_tokens: this.maxTokens,
          tools: apiTools,
          messages,
        });
      } catch (err: unknown) {
        const errMsg = err instanceof Error ? err.message : "Unknown API error";
        return {
          success: false,
          output: null,
          textOutput,
          usage: {
            inputTokens: totalInputTokens,
            outputTokens: totalOutputTokens,
          },
          turns,
          toolCallCount,
          codeExecutionCount,
          error: `Anthropic API error: ${errMsg}`,
        };
      }

      totalInputTokens += response.usage?.input_tokens ?? 0;
      totalOutputTokens += response.usage?.output_tokens ?? 0;

      // Process content blocks
      const toolResults: Anthropic.Messages.ToolResultBlockParam[] = [];
      let hasToolUse = false;

      for (const block of response.content) {
        if (block.type === "text") {
          textOutput += block.text;
        } else if (block.type === "tool_use") {
          hasToolUse = true;
          toolCallCount++;
          const handler = this.toolHandlers.get(block.name);
          if (handler) {
            let result: ToolResult;
            try {
              result = await handler.execute(block.input as Record<string, unknown>, this.cwd);
            } catch (handlerErr: unknown) {
              const errMsg =
                handlerErr instanceof Error ? handlerErr.message : "Handler execution failed";
              result = {
                success: false,
                output: { error: errMsg },
              };
            }
            toolResults.push({
              type: "tool_result",
              tool_use_id: block.id,
              content: JSON.stringify(result.output),
              is_error: !result.success,
            });
          } else {
            // Unknown tool — return error
            toolResults.push({
              type: "tool_result",
              tool_use_id: block.id,
              content: JSON.stringify({
                error: `Unknown tool: ${block.name}`,
              }),
              is_error: true,
            });
          }
        } else {
          // Handle code_execution_result and other block types
          const anyBlock = block as unknown as Record<string, unknown>;
          if (anyBlock.type === "code_execution_result") {
            codeExecutionCount++;
            const codeResult = anyBlock as {
              output?: string;
              return_value?: unknown;
            };
            if (codeResult.output) {
              textOutput += codeResult.output;
            }
            // Try to parse JSON from code execution output
            if (codeResult.output) {
              try {
                lastOutput = JSON.parse(codeResult.output);
              } catch {
                // Not JSON — keep as text
                lastOutput = codeResult.output;
              }
            }
          }
        }
      }

      // A refusal turn carries no tool_use blocks, so it must be caught
      // before the no-tool-use guard below or it reads as a normal
      // completion. The refusal text stays out of output — it must never
      // become downstream context for later stages.
      if (response.stop_reason === "refusal") {
        return {
          success: false,
          output: null,
          textOutput,
          usage: {
            inputTokens: totalInputTokens,
            outputTokens: totalOutputTokens,
          },
          turns,
          toolCallCount,
          codeExecutionCount,
          refusal: true,
          error: "Model refused the request (stop_reason: refusal)",
        };
      }

      // If no tool_use blocks, we're done
      if (!hasToolUse || response.stop_reason === "end_turn") {
        // Try to extract structured output from the last text block
        if (lastOutput === null && textOutput) {
          // Try to find JSON in the text output
          const jsonMatch = textOutput.match(/```json\s*([\s\S]*?)```|(\{[\s\S]*\})\s*$/);
          if (jsonMatch) {
            try {
              lastOutput = JSON.parse(jsonMatch[1] ?? jsonMatch[2]);
            } catch {
              lastOutput = textOutput;
            }
          } else {
            lastOutput = textOutput;
          }
        }

        return {
          success: true,
          output: lastOutput,
          textOutput,
          usage: {
            inputTokens: totalInputTokens,
            outputTokens: totalOutputTokens,
          },
          turns,
          toolCallCount,
          codeExecutionCount,
        };
      }

      // Send tool results back as assistant + user messages
      messages.push({ role: "assistant", content: response.content });
      messages.push({ role: "user", content: toolResults });
    }

    // Max turns exceeded
    return {
      success: false,
      output: lastOutput,
      textOutput,
      usage: {
        inputTokens: totalInputTokens,
        outputTokens: totalOutputTokens,
      },
      turns,
      toolCallCount,
      codeExecutionCount,
      error: `Max turns (${this.maxTurns}) exceeded`,
    };
  }
}
