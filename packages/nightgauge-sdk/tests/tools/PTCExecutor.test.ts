import { describe, it, expect, vi, beforeEach } from "vitest";
import { PTCExecutor, type PTCExecutorOptions } from "../../src/tools/PTCExecutor.js";
import type { ToolHandler, ToolResult } from "../../src/tools/tool-handlers.js";

// ---------------------------------------------------------------------------
// Anthropic SDK mock
//
// The factory stores the `mockCreate` function on the module object so we can
// retrieve it via `__mockCreate` after the module has been loaded.  The
// Anthropic default export is a constructor, so we return a class-like mock
// via `vi.fn().mockImplementation(...)`.
// ---------------------------------------------------------------------------
vi.mock("@anthropic-ai/sdk", () => {
  const mockCreate = vi.fn();
  return {
    default: vi.fn(function () {
      return { messages: { create: mockCreate } };
    }),
    __mockCreate: mockCreate,
  };
});

// Pull the shared mock after the module system has resolved the mock factory.
const { __mockCreate: mockCreate } = (await import("@anthropic-ai/sdk")) as {
  __mockCreate: ReturnType<typeof vi.fn>;
  default: unknown;
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Build a minimal tool definition accepted by PTCExecutor. */
function makeToolDef(name: string) {
  return {
    name,
    description: `A test tool called ${name}`,
    input_schema: {
      type: "object",
      properties: { value: { type: "string" } },
      required: [],
    },
  };
}

/** Build a minimal ToolHandler that resolves to the given result. */
function makeHandler(result: ToolResult): ToolHandler {
  return {
    name: result.output?.toString() ?? "handler",
    execute: vi.fn().mockResolvedValue(result),
  };
}

/** Build a minimal Anthropic Messages.Message response. */
function makeResponse(
  overrides: Partial<{
    stop_reason: string;
    content: unknown[];
    input_tokens: number;
    output_tokens: number;
  }> = {}
) {
  return {
    id: "msg_test",
    type: "message",
    role: "assistant",
    model: "claude-test",
    stop_reason: overrides.stop_reason ?? "end_turn",
    stop_sequence: null,
    content: overrides.content ?? [],
    usage: {
      input_tokens: overrides.input_tokens ?? 10,
      output_tokens: overrides.output_tokens ?? 20,
    },
  };
}

/** Default PTCExecutorOptions used across most tests. */
function makeOptions(extra: Partial<PTCExecutorOptions> = {}): PTCExecutorOptions {
  return {
    apiKey: "test-api-key",
    tools: [],
    toolHandlers: new Map(),
    cwd: "/tmp/test-cwd",
    ...extra,
  };
}

// ---------------------------------------------------------------------------
// Test suites
// ---------------------------------------------------------------------------

describe("PTCExecutor", () => {
  beforeEach(() => {
    mockCreate.mockReset();
  });

  // -------------------------------------------------------------------------
  // Constructor / defaults
  // -------------------------------------------------------------------------

  describe("constructor", () => {
    it("initialises with required options and applies defaults", () => {
      // Construction must not throw — default model and maxTurns are applied
      // internally; we verify indirectly through execute() behaviour below.
      const executor = new PTCExecutor(makeOptions());
      expect(executor).toBeDefined();
    });

    it("accepts custom model, maxTokens and maxTurns without throwing", () => {
      const executor = new PTCExecutor(
        makeOptions({ model: "claude-opus-4-6", maxTokens: 4096, maxTurns: 3 })
      );
      expect(executor).toBeDefined();
    });
  });

  // -------------------------------------------------------------------------
  // Successful execution — end_turn with text response
  // -------------------------------------------------------------------------

  describe("execute() — end_turn with text response", () => {
    it("returns success with text content when model responds with end_turn", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "end_turn",
          content: [{ type: "text", text: "Hello from Claude." }],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("Say hello.");

      expect(result.success).toBe(true);
      expect(result.textOutput).toBe("Hello from Claude.");
      expect(result.turns).toBe(1);
      expect(result.error).toBeUndefined();
    });

    it("passes the prompt as the first user message", async () => {
      mockCreate.mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "ok" }] }));

      const executor = new PTCExecutor(makeOptions());
      await executor.execute("My custom prompt");

      const callArgs = mockCreate.mock.calls[0][0];
      expect(callArgs.messages[0]).toEqual({
        role: "user",
        content: "My custom prompt",
      });
    });

    it("includes code_execution as the first tool in every API call", async () => {
      mockCreate.mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "ok" }] }));

      const executor = new PTCExecutor(makeOptions());
      await executor.execute("test");

      const callArgs = mockCreate.mock.calls[0][0];
      expect(callArgs.tools[0]).toMatchObject({ name: "code_execution" });
    });

    it("includes registered custom tools in the API call", async () => {
      mockCreate.mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "ok" }] }));

      const toolDef = makeToolDef("run_build");
      const executor = new PTCExecutor(makeOptions({ tools: [toolDef] }));
      await executor.execute("test");

      const callArgs = mockCreate.mock.calls[0][0];
      const toolNames = callArgs.tools.map((t: { name: string }) => t.name);
      expect(toolNames).toContain("run_build");
    });

    it("includes allowed_callers on custom tools when defined", async () => {
      mockCreate.mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "ok" }] }));

      const toolDef = {
        ...makeToolDef("run_build"),
        allowed_callers: ["code_execution_20250825"] as const,
      };
      const executor = new PTCExecutor(makeOptions({ tools: [toolDef] }));
      await executor.execute("test");

      const callArgs = mockCreate.mock.calls[0][0];
      const runBuildDef = callArgs.tools.find((t: { name: string }) => t.name === "run_build");
      expect(runBuildDef).toMatchObject({
        allowed_callers: ["code_execution_20250825"],
      });
    });

    it("uses provided model and maxTokens in the API call", async () => {
      mockCreate.mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "ok" }] }));

      const executor = new PTCExecutor(makeOptions({ model: "claude-opus-4-6", maxTokens: 4096 }));
      await executor.execute("test");

      const callArgs = mockCreate.mock.calls[0][0];
      expect(callArgs.model).toBe("claude-opus-4-6");
      expect(callArgs.max_tokens).toBe(4096);
    });

    it("falls back to default model when none is provided", async () => {
      mockCreate.mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "ok" }] }));

      const executor = new PTCExecutor(makeOptions());
      await executor.execute("test");

      const callArgs = mockCreate.mock.calls[0][0];
      expect(callArgs.model).toBe("claude-sonnet-4-5-20250929");
    });
  });

  // -------------------------------------------------------------------------
  // JSON extraction from text output
  // -------------------------------------------------------------------------

  describe("execute() — JSON extraction from text", () => {
    it("parses a JSON code block from the text output", async () => {
      const json = { status: "ok", count: 3 };
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [
            {
              type: "text",
              text: "Here is the result:\n```json\n" + JSON.stringify(json) + "\n```",
            },
          ],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.output).toEqual(json);
    });

    it("parses a bare JSON object from the end of text output", async () => {
      const json = { pass: true };
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [{ type: "text", text: "Result: " + JSON.stringify(json) }],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.output).toEqual(json);
    });

    it("falls back to raw text when output is not JSON", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [{ type: "text", text: "plain text output" }],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.output).toBe("plain text output");
    });
  });

  // -------------------------------------------------------------------------
  // tool_use → tool_result conversation loop
  // -------------------------------------------------------------------------

  describe("execute() — tool_use / tool_result loop", () => {
    it("handles a single tool_use block and continues to end_turn", async () => {
      const handler = makeHandler({
        success: true,
        output: { built: true },
      });
      const handlers = new Map<string, ToolHandler>([["run_build", handler]]);

      // Turn 1: model requests tool_use
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "tool_use",
          content: [
            {
              type: "tool_use",
              id: "tu_001",
              name: "run_build",
              input: { command: "npm run build" },
            },
          ],
          input_tokens: 100,
          output_tokens: 50,
        })
      );

      // Turn 2: model ends after seeing tool_result
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "end_turn",
          content: [{ type: "text", text: "Build succeeded." }],
          input_tokens: 200,
          output_tokens: 30,
        })
      );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("run_build")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("Run build");

      expect(result.success).toBe(true);
      expect(result.turns).toBe(2);
      expect(result.textOutput).toBe("Build succeeded.");
      expect(handler.execute).toHaveBeenCalledOnce();
      expect(handler.execute).toHaveBeenCalledWith({ command: "npm run build" }, "/tmp/test-cwd");
    });

    it("sends tool_result back to the model after handler execution", async () => {
      const handler = makeHandler({
        success: true,
        output: { exit_code: 0 },
      });
      const handlers = new Map<string, ToolHandler>([["run_build", handler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [
              {
                type: "tool_use",
                id: "tu_abc",
                name: "run_build",
                input: {},
              },
            ],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "done" }] }));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("run_build")],
          toolHandlers: handlers,
        })
      );
      await executor.execute("test");

      const secondCallMessages = mockCreate.mock.calls[1][0].messages;
      // messages[0] = initial user prompt
      // messages[1] = assistant response with tool_use
      // messages[2] = user message with tool_result
      const toolResultMessage = secondCallMessages[2];
      expect(toolResultMessage.role).toBe("user");
      expect(toolResultMessage.content[0]).toMatchObject({
        type: "tool_result",
        tool_use_id: "tu_abc",
        is_error: false,
      });
    });

    it("handles multiple tool_use blocks in a single response", async () => {
      const buildHandler = makeHandler({
        success: true,
        output: { built: true },
      });
      const lintHandler = makeHandler({
        success: true,
        output: { clean: true },
      });
      const handlers = new Map<string, ToolHandler>([
        ["run_build", buildHandler],
        ["run_lint", lintHandler],
      ]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [
              { type: "tool_use", id: "tu_1", name: "run_build", input: {} },
              { type: "tool_use", id: "tu_2", name: "run_lint", input: {} },
            ],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "all good" }] }));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("run_build"), makeToolDef("run_lint")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(buildHandler.execute).toHaveBeenCalledOnce();
      expect(lintHandler.execute).toHaveBeenCalledOnce();

      // Both tool results must be in the same user message
      const secondCallMessages = mockCreate.mock.calls[1][0].messages;
      const toolResultContent = secondCallMessages[2].content;
      expect(toolResultContent).toHaveLength(2);
      expect(toolResultContent[0].tool_use_id).toBe("tu_1");
      expect(toolResultContent[1].tool_use_id).toBe("tu_2");
    });

    it("sends is_error: false when handler succeeds", async () => {
      const handler = makeHandler({ success: true, output: { ok: true } });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_1", name: "my_tool", input: {} }],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [] }));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
        })
      );
      await executor.execute("test");

      const toolResult = mockCreate.mock.calls[1][0].messages[2].content[0];
      expect(toolResult.is_error).toBe(false);
    });

    it("sends is_error: true when handler returns success: false", async () => {
      const handler = makeHandler({
        success: false,
        output: { error: "build failed" },
      });
      const handlers = new Map([["run_build", handler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_1", name: "run_build", input: {} }],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "done" }] }));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("run_build")],
          toolHandlers: handlers,
        })
      );
      await executor.execute("test");

      const toolResult = mockCreate.mock.calls[1][0].messages[2].content[0];
      expect(toolResult.is_error).toBe(true);
    });

    it("terminates when stop_reason is end_turn even if tool_use blocks are present", async () => {
      // Anthropic may sometimes return both tool_use content and end_turn — the
      // executor should honour end_turn and not continue the loop.
      const handler = makeHandler({ success: true, output: { ok: true } });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "end_turn",
          content: [{ type: "tool_use", id: "tu_1", name: "my_tool", input: {} }],
        })
      );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.turns).toBe(1);
      // The API was only called once because stop_reason === end_turn
      expect(mockCreate).toHaveBeenCalledTimes(1);
    });
  });

  // -------------------------------------------------------------------------
  // Refusal handling (Issue #75)
  // -------------------------------------------------------------------------

  describe("execute() — refusal stop_reason", () => {
    it("returns failure with the refusal marker when the model refuses", async () => {
      // A refusal turn has no tool_use blocks; before the fix it fell through
      // the !hasToolUse guard and was reported as success.
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "refusal",
          content: [{ type: "text", text: "I can't help with that." }],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.refusal).toBe(true);
      expect(result.error).toContain("refusal");
      expect(mockCreate).toHaveBeenCalledTimes(1);
    });

    it("keeps refusal text out of output, even when it contains JSON", async () => {
      // Refusal text must never become downstream context. The JSON blob in
      // the refusal message must not be extracted as structured output.
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "refusal",
          content: [{ type: "text", text: 'Declining. {"status":"ok"}' }],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.output).toBeNull();
      // textOutput is preserved for diagnostics only
      expect(result.textOutput).toContain("Declining.");
    });

    it("terminates the loop on refusal after a prior tool_use turn", async () => {
      const handler = makeHandler({ success: true, output: { ok: true } });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_1", name: "my_tool", input: {} }],
            input_tokens: 100,
            output_tokens: 40,
          })
        )
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "refusal",
            content: [{ type: "text", text: "Stopping here." }],
            input_tokens: 200,
            output_tokens: 10,
          })
        );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.refusal).toBe(true);
      expect(result.turns).toBe(2);
      expect(mockCreate).toHaveBeenCalledTimes(2);
      // Usage from both turns is still reported
      expect(result.usage.inputTokens).toBe(300);
      expect(result.usage.outputTokens).toBe(50);
    });

    it("leaves the refusal marker unset on a genuine end_turn success", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "end_turn",
          content: [{ type: "text", text: "All done." }],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.refusal).toBeUndefined();
      expect(result.error).toBeUndefined();
    });
  });

  // -------------------------------------------------------------------------
  // API error handling
  // -------------------------------------------------------------------------

  describe("execute() — API error handling", () => {
    it("returns failure when the Anthropic API throws an Error", async () => {
      mockCreate.mockRejectedValueOnce(new Error("rate limit exceeded"));

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.error).toContain("Anthropic API error");
      expect(result.error).toContain("rate limit exceeded");
    });

    it("returns failure for non-Error API throws (string)", async () => {
      mockCreate.mockRejectedValueOnce("something went wrong");

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.error).toContain("Anthropic API error");
      expect(result.error).toContain("Unknown API error");
    });

    it("preserves accumulated text output and token counts on API failure mid-loop", async () => {
      const handler = makeHandler({ success: true, output: { ok: true } });
      const handlers = new Map([["my_tool", handler]]);

      // Turn 1 succeeds with tool_use
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          stop_reason: "tool_use",
          content: [
            { type: "text", text: "Partial output. " },
            { type: "tool_use", id: "tu_1", name: "my_tool", input: {} },
          ],
          input_tokens: 50,
          output_tokens: 25,
        })
      );

      // Turn 2 fails
      mockCreate.mockRejectedValueOnce(new Error("server error"));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.textOutput).toBe("Partial output. ");
      expect(result.usage.inputTokens).toBe(50);
      expect(result.usage.outputTokens).toBe(25);
      expect(result.turns).toBe(2);
    });
  });

  // -------------------------------------------------------------------------
  // Max turns exceeded
  // -------------------------------------------------------------------------

  describe("execute() — max turns exceeded", () => {
    it("returns failure when maxTurns is reached without end_turn", async () => {
      // Every call requests another tool_use — loop never ends naturally.
      const handler = makeHandler({ success: true, output: { ok: true } });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate.mockResolvedValue(
        makeResponse({
          stop_reason: "tool_use",
          content: [{ type: "tool_use", id: "tu_x", name: "my_tool", input: {} }],
        })
      );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
          maxTurns: 3,
        })
      );
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.error).toContain("Max turns (3) exceeded");
      expect(result.turns).toBe(3);
      expect(mockCreate).toHaveBeenCalledTimes(3);
    });

    it("reports correct turn count equal to maxTurns when exceeded", async () => {
      const handler = makeHandler({ success: true, output: {} });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate.mockResolvedValue(
        makeResponse({
          stop_reason: "tool_use",
          content: [{ type: "tool_use", id: "tu_x", name: "my_tool", input: {} }],
        })
      );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
          maxTurns: 5,
        })
      );
      const result = await executor.execute("test");

      expect(result.turns).toBe(5);
    });

    it("uses default maxTurns of 10 when not specified", async () => {
      const handler = makeHandler({ success: true, output: {} });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate.mockResolvedValue(
        makeResponse({
          stop_reason: "tool_use",
          content: [{ type: "tool_use", id: "tu_x", name: "my_tool", input: {} }],
        })
      );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.turns).toBe(10);
      expect(result.error).toContain("Max turns (10) exceeded");
    });
  });

  // -------------------------------------------------------------------------
  // Tool handler errors (handler throws)
  // -------------------------------------------------------------------------

  describe("execute() — tool handler errors", () => {
    it("returns is_error: true with error message when handler throws an Error", async () => {
      const throwingHandler: ToolHandler = {
        name: "broken_tool",
        execute: vi.fn().mockRejectedValue(new Error("handler crashed")),
      };
      const handlers = new Map([["broken_tool", throwingHandler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [
              {
                type: "tool_use",
                id: "tu_err",
                name: "broken_tool",
                input: {},
              },
            ],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "recovered" }] }));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("broken_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      // The executor must continue the loop after handler failure
      expect(result.success).toBe(true);

      const toolResult = mockCreate.mock.calls[1][0].messages[2].content[0];
      expect(toolResult.is_error).toBe(true);
      expect(toolResult.content).toContain("handler crashed");
    });

    it("handles non-Error handler throws (strings)", async () => {
      const throwingHandler: ToolHandler = {
        name: "broken_tool",
        execute: vi.fn().mockRejectedValue("non-error thrown"),
      };
      const handlers = new Map([["broken_tool", throwingHandler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [
              {
                type: "tool_use",
                id: "tu_str",
                name: "broken_tool",
                input: {},
              },
            ],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [] }));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("broken_tool")],
          toolHandlers: handlers,
        })
      );
      await executor.execute("test");

      const toolResult = mockCreate.mock.calls[1][0].messages[2].content[0];
      expect(toolResult.is_error).toBe(true);
      expect(toolResult.content).toContain("Handler execution failed");
    });

    it("continues the loop after a handler error and returns success if model ends cleanly", async () => {
      const throwingHandler: ToolHandler = {
        name: "flaky_tool",
        execute: vi.fn().mockRejectedValue(new Error("timeout")),
      };
      const handlers = new Map([["flaky_tool", throwingHandler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_f", name: "flaky_tool", input: {} }],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "all done" }] }));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("flaky_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.textOutput).toBe("all done");
    });
  });

  // -------------------------------------------------------------------------
  // Unknown tool handling
  // -------------------------------------------------------------------------

  describe("execute() — unknown tool handling", () => {
    it("returns is_error: true for a tool_use with no registered handler", async () => {
      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [
              {
                type: "tool_use",
                id: "tu_unk",
                name: "nonexistent_tool",
                input: {},
              },
            ],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [{ type: "text", text: "ok" }] }));

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(true);

      const toolResult = mockCreate.mock.calls[1][0].messages[2].content[0];
      expect(toolResult.is_error).toBe(true);
      expect(toolResult.content).toContain("Unknown tool: nonexistent_tool");
    });

    it("includes the tool name in the unknown tool error message", async () => {
      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_x", name: "mystery_tool", input: {} }],
          })
        )
        .mockResolvedValueOnce(makeResponse({ content: [] }));

      const executor = new PTCExecutor(makeOptions());
      await executor.execute("test");

      const toolResult = mockCreate.mock.calls[1][0].messages[2].content[0];
      const parsed = JSON.parse(toolResult.content);
      expect(parsed.error).toBe("Unknown tool: mystery_tool");
    });
  });

  // -------------------------------------------------------------------------
  // code_execution_result block parsing
  // -------------------------------------------------------------------------

  describe("execute() — code_execution_result block parsing", () => {
    it("appends code_execution_result output to textOutput", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [
            {
              type: "code_execution_result",
              output: "stdout from sandbox\n",
            } as object,
          ],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.textOutput).toBe("stdout from sandbox\n");
    });

    it("parses JSON from code_execution_result output as structured output", async () => {
      const codeOutput = JSON.stringify({ passed: 5, failed: 0 });
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [{ type: "code_execution_result", output: codeOutput } as object],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.success).toBe(true);
      expect(result.output).toEqual({ passed: 5, failed: 0 });
    });

    it("falls back to raw string for non-JSON code_execution_result output", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [{ type: "code_execution_result", output: "plain output" } as object],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.output).toBe("plain output");
    });

    it("handles code_execution_result blocks that have no output field", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [{ type: "code_execution_result" } as object],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      // No crash; output falls back through the text path
      expect(result.success).toBe(true);
    });

    it("prefers code_execution_result structured output over text JSON", async () => {
      const codeJson = { source: "code_execution" };
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [
            // code_execution_result comes BEFORE text block
            {
              type: "code_execution_result",
              output: JSON.stringify(codeJson),
            } as object,
            { type: "text", text: '{"source":"text"}' },
          ],
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      // lastOutput is set by code_execution_result first; the text JSON
      // extraction only runs if lastOutput === null after the loop.
      expect(result.output).toEqual(codeJson);
    });
  });

  // -------------------------------------------------------------------------
  // Token usage accumulation
  // -------------------------------------------------------------------------

  describe("execute() — token usage accumulation", () => {
    it("accumulates input and output tokens across a single turn", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({
          content: [{ type: "text", text: "ok" }],
          input_tokens: 150,
          output_tokens: 75,
        })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.usage.inputTokens).toBe(150);
      expect(result.usage.outputTokens).toBe(75);
    });

    it("accumulates tokens across multiple turns", async () => {
      const handler = makeHandler({ success: true, output: {} });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_1", name: "my_tool", input: {} }],
            input_tokens: 100,
            output_tokens: 40,
          })
        )
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_2", name: "my_tool", input: {} }],
            input_tokens: 200,
            output_tokens: 60,
          })
        )
        .mockResolvedValueOnce(
          makeResponse({
            content: [{ type: "text", text: "done" }],
            input_tokens: 300,
            output_tokens: 20,
          })
        );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.usage.inputTokens).toBe(600); // 100 + 200 + 300
      expect(result.usage.outputTokens).toBe(120); // 40 + 60 + 20
    });

    it("still reports accumulated tokens when API fails mid-loop", async () => {
      const handler = makeHandler({ success: true, output: {} });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate
        .mockResolvedValueOnce(
          makeResponse({
            stop_reason: "tool_use",
            content: [{ type: "tool_use", id: "tu_1", name: "my_tool", input: {} }],
            input_tokens: 111,
            output_tokens: 222,
          })
        )
        .mockRejectedValueOnce(new Error("server error"));

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
        })
      );
      const result = await executor.execute("test");

      expect(result.success).toBe(false);
      expect(result.usage.inputTokens).toBe(111);
      expect(result.usage.outputTokens).toBe(222);
    });

    it("handles missing usage fields gracefully (defaults to 0)", async () => {
      const responseWithoutUsage = {
        ...makeResponse({ content: [{ type: "text", text: "ok" }] }),
        usage: undefined,
      };
      mockCreate.mockResolvedValueOnce(responseWithoutUsage);

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result.usage.inputTokens).toBe(0);
      expect(result.usage.outputTokens).toBe(0);
    });

    it("still reports accumulated tokens when max turns is exceeded", async () => {
      const handler = makeHandler({ success: true, output: {} });
      const handlers = new Map([["my_tool", handler]]);

      mockCreate.mockResolvedValue(
        makeResponse({
          stop_reason: "tool_use",
          content: [{ type: "tool_use", id: "tu_x", name: "my_tool", input: {} }],
          input_tokens: 10,
          output_tokens: 5,
        })
      );

      const executor = new PTCExecutor(
        makeOptions({
          tools: [makeToolDef("my_tool")],
          toolHandlers: handlers,
          maxTurns: 4,
        })
      );
      const result = await executor.execute("test");

      expect(result.usage.inputTokens).toBe(40); // 4 turns × 10
      expect(result.usage.outputTokens).toBe(20); // 4 turns × 5
    });
  });

  // -------------------------------------------------------------------------
  // PTCResult shape verification
  // -------------------------------------------------------------------------

  describe("PTCResult shape", () => {
    it("always includes all required fields on success", async () => {
      mockCreate.mockResolvedValueOnce(
        makeResponse({ content: [{ type: "text", text: "result" }] })
      );

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result).toHaveProperty("success");
      expect(result).toHaveProperty("output");
      expect(result).toHaveProperty("textOutput");
      expect(result).toHaveProperty("usage");
      expect(result).toHaveProperty("usage.inputTokens");
      expect(result).toHaveProperty("usage.outputTokens");
      expect(result).toHaveProperty("turns");
    });

    it("always includes all required fields on failure", async () => {
      mockCreate.mockRejectedValueOnce(new Error("API error"));

      const executor = new PTCExecutor(makeOptions());
      const result = await executor.execute("test");

      expect(result).toHaveProperty("success", false);
      expect(result).toHaveProperty("output");
      expect(result).toHaveProperty("textOutput");
      expect(result).toHaveProperty("usage");
      expect(result).toHaveProperty("turns");
      expect(result).toHaveProperty("error");
    });
  });
});
