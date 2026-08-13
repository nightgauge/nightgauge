/**
 * toolCallRecording.test.ts
 *
 * Integration tests for the tool call recording pipeline (Issue #639).
 *
 * Tests the flow: skillRunner detects tool_use blocks in stream-json
 * -> fires onToolCall callback -> HeadlessOrchestrator bridges to
 * PipelineCallbacks.onToolCall with target extraction logic.
 *
 * Since skillRunner spawns real processes, we do not test it directly.
 * Instead, we test:
 * 1. The SkillRunCallbacks interface includes onToolCall
 * 2. The HeadlessOrchestrator callback bridge target extraction logic
 * 3. The ToolCallData shape matches PipelineCallbacks.onToolCall
 */

import { describe, it, expect, vi } from "vitest";

// Minimal vscode mock - only what is needed for type imports
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(undefined),
    })),
  },
  EventEmitter: class EventEmitter {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import type { SkillRunCallbacks } from "../../src/utils/skillRunner";
import type { PipelineCallbacks, ToolCallData } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStage } from "@nightgauge/sdk";

/**
 * Replicate the HeadlessOrchestrator onToolCall bridge logic for isolated testing.
 *
 * This is the exact logic from HeadlessOrchestrator.runStage() that transforms
 * a raw (toolName, toolInput) pair from SkillRunCallbacks.onToolCall into the
 * ToolCallData shape expected by PipelineCallbacks.onToolCall.
 */
function extractToolCallData(
  stage: PipelineStage,
  toolName: string,
  toolInput: unknown,
  callbacks?: PipelineCallbacks
): void {
  if (callbacks?.onToolCall) {
    const input = toolInput as Record<string, unknown> | undefined;
    callbacks.onToolCall(stage, {
      tool: toolName,
      target:
        typeof input?.file_path === "string"
          ? input.file_path
          : typeof input?.command === "string"
            ? input.command.substring(0, 100)
            : typeof input?.pattern === "string"
              ? input.pattern
              : "",
      args: input as Record<string, unknown> | undefined,
    });
  }
}

describe("Tool Call Recording - SkillRunCallbacks Interface", () => {
  it("should accept onToolCall as an optional callback", () => {
    const onToolCallSpy = vi.fn();

    const callbacks: SkillRunCallbacks = {
      onToolCall: onToolCallSpy,
    };

    // The callback should be assignable and callable
    expect(callbacks.onToolCall).toBeDefined();
    callbacks.onToolCall?.("Read", { file_path: "/src/index.ts" });
    expect(onToolCallSpy).toHaveBeenCalledWith("Read", {
      file_path: "/src/index.ts",
    });
  });

  it("should allow omitting onToolCall (optional property)", () => {
    const callbacks: SkillRunCallbacks = {
      onStdout: vi.fn(),
    };

    // onToolCall is undefined when not provided
    expect(callbacks.onToolCall).toBeUndefined();
  });

  it("should coexist with onToolUse without conflict", () => {
    const onToolUseSpy = vi.fn();
    const onToolCallSpy = vi.fn();

    const callbacks: SkillRunCallbacks = {
      onToolUse: onToolUseSpy,
      onToolCall: onToolCallSpy,
    };

    // Both should be independently callable
    callbacks.onToolUse?.("Read", { file_path: "/a.ts" }, "toolu_123");
    callbacks.onToolCall?.("Read", { file_path: "/a.ts" });

    expect(onToolUseSpy).toHaveBeenCalledOnce();
    expect(onToolCallSpy).toHaveBeenCalledOnce();
  });
});

describe("Tool Call Recording - HeadlessOrchestrator Target Extraction", () => {
  it("should extract file_path as target for file-based tools", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData(
      "feature-dev",
      "Read",
      {
        file_path: "/src/services/HeadlessOrchestrator.ts",
      },
      callbacks
    );

    expect(onToolCall).toHaveBeenCalledWith("feature-dev", {
      tool: "Read",
      target: "/src/services/HeadlessOrchestrator.ts",
      args: { file_path: "/src/services/HeadlessOrchestrator.ts" },
    });
  });

  it("should extract command as target for Bash tool (truncated to 100 chars)", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    const longCommand =
      "npm run build && npm run test -- --reporter=verbose --coverage --run " +
      "packages/nightgauge-vscode/tests/services/toolCallRecording.test.ts";

    extractToolCallData(
      "feature-validate",
      "Bash",
      {
        command: longCommand,
      },
      callbacks
    );

    expect(onToolCall).toHaveBeenCalledOnce();
    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.tool).toBe("Bash");
    expect(callData.target).toBe(longCommand.substring(0, 100));
    expect(callData.target.length).toBe(100);
  });

  it("should extract short command without truncation", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData(
      "feature-dev",
      "Bash",
      {
        command: "npm test",
      },
      callbacks
    );

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.target).toBe("npm test");
  });

  it("should extract pattern as target for Grep/Glob tools", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData(
      "feature-dev",
      "Grep",
      {
        pattern: "onToolCall",
        path: "/src",
      },
      callbacks
    );

    expect(onToolCall).toHaveBeenCalledWith("feature-dev", {
      tool: "Grep",
      target: "onToolCall",
      args: { pattern: "onToolCall", path: "/src" },
    });
  });

  it("should produce empty target for unknown/empty input", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData(
      "feature-planning",
      "WebSearch",
      {
        query: "vitest mocking patterns",
      },
      callbacks
    );

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.target).toBe("");
  });

  it("should produce empty target when toolInput is undefined", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData("issue-pickup", "Task", undefined, callbacks);

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.target).toBe("");
    expect(callData.args).toBeUndefined();
  });

  it("should produce empty target when toolInput is null", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData("issue-pickup", "Task", null, callbacks);

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.target).toBe("");
  });

  it("should prefer file_path over command and pattern", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    // Input has all three keys; file_path should win
    extractToolCallData(
      "feature-dev",
      "Edit",
      {
        file_path: "/src/index.ts",
        command: "should-not-appear",
        pattern: "should-not-appear",
      },
      callbacks
    );

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.target).toBe("/src/index.ts");
  });

  it("should prefer command over pattern when file_path is absent", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData(
      "feature-dev",
      "Bash",
      {
        command: "git status",
        pattern: "should-not-appear",
      },
      callbacks
    );

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.target).toBe("git status");
  });

  it("should not call PipelineCallbacks.onToolCall when callback is undefined", () => {
    // Should not throw even when callbacks.onToolCall is not set
    expect(() => {
      extractToolCallData(
        "feature-dev",
        "Read",
        {
          file_path: "/src/index.ts",
        },
        {}
      );
    }).not.toThrow();
  });

  it("should not call PipelineCallbacks.onToolCall when callbacks object is undefined", () => {
    expect(() => {
      extractToolCallData(
        "feature-dev",
        "Read",
        {
          file_path: "/src/index.ts",
        },
        undefined
      );
    }).not.toThrow();
  });

  it("should pass the correct stage through to the callback", () => {
    const stages: PipelineStage[] = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];

    for (const stage of stages) {
      const onToolCall = vi.fn();
      const callbacks: PipelineCallbacks = { onToolCall };

      extractToolCallData(stage, "Read", { file_path: "/a.ts" }, callbacks);

      expect(onToolCall).toHaveBeenCalledWith(
        stage,
        expect.objectContaining({
          tool: "Read",
        })
      );
    }
  });
});

describe("Tool Call Recording - ToolCallData Shape", () => {
  it("should include required tool and target fields", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    extractToolCallData(
      "feature-dev",
      "Write",
      {
        file_path: "/src/new-file.ts",
        content: "export const x = 1;",
      },
      callbacks
    );

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData).toHaveProperty("tool");
    expect(callData).toHaveProperty("target");
    expect(typeof callData.tool).toBe("string");
    expect(typeof callData.target).toBe("string");
  });

  it("should include args as the full toolInput record", () => {
    const onToolCall = vi.fn();
    const callbacks: PipelineCallbacks = { onToolCall };

    const input = {
      file_path: "/src/index.ts",
      old_string: "foo",
      new_string: "bar",
    };

    extractToolCallData("feature-dev", "Edit", input, callbacks);

    const callData: ToolCallData = onToolCall.mock.calls[0][1];
    expect(callData.args).toEqual(input);
  });

  it("should allow optional durationMs, result, and error fields", () => {
    // ToolCallData allows these optional fields which are populated later
    // by PipelineStateService. The bridge only sets tool, target, and args.
    const toolCall: ToolCallData = {
      tool: "Bash",
      target: "npm test",
      durationMs: 5000,
      args: { command: "npm test" },
      result: "All tests passed",
      error: undefined,
    };

    expect(toolCall.durationMs).toBe(5000);
    expect(toolCall.result).toBe("All tests passed");
    expect(toolCall.error).toBeUndefined();
  });

  it("should match the PipelineCallbacks.onToolCall signature", () => {
    // Verify the callback accepts (PipelineStage, ToolCallData)
    const mockCallback: PipelineCallbacks["onToolCall"] = vi.fn(
      (stage: PipelineStage, toolCall: ToolCallData) => {
        // Type assertions to verify the shape compiles
        const _stage: PipelineStage = stage;
        const _tool: string = toolCall.tool;
        const _target: string = toolCall.target;
        const _args: Record<string, unknown> | undefined = toolCall.args;
        const _duration: number | undefined = toolCall.durationMs;

        // Suppress unused variable warnings
        void _stage;
        void _tool;
        void _target;
        void _args;
        void _duration;
      }
    );

    // Should be callable with valid arguments
    mockCallback?.("feature-dev", {
      tool: "Read",
      target: "/src/index.ts",
      args: { file_path: "/src/index.ts" },
    });

    expect(mockCallback).toHaveBeenCalledOnce();
  });
});
