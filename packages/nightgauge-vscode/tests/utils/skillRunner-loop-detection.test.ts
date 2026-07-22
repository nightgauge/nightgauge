/**
 * skillRunner-loop-detection.test.ts
 *
 * Unit tests for AskUserQuestion loop detection in headless pipeline mode.
 *
 * @see Issue #218 - AskUserQuestion Loop Detection
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";

// Mock vscode module
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

// Mock fs module
vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(
    () => `---
name: test-skill
allowed-tools: Read Write Edit Bash
---
# Test Skill
`
  ),
}));

// Create mock process factory
function createMockChildProcess(): ChildProcess {
  const proc = new EventEmitter() as ChildProcess;
  proc.stdout = new EventEmitter() as any;
  proc.stderr = new EventEmitter() as any;
  proc.stdin = {
    write: vi.fn(),
    end: vi.fn(),
    destroyed: false,
  } as any;
  proc.kill = vi.fn();
  proc.killed = false;
  return proc;
}

// Mock child_process module
vi.mock("child_process", () => ({
  spawn: vi.fn(),
  execFile: vi.fn(
    (
      _cmd: string,
      _args: string[],
      _opts: unknown,
      cb: (e: Error | null, s: string, t: string) => void
    ) => {
      cb(new Error("no children"), "", "");
    }
  ),
}));

// Import after mocks
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

describe("skillRunner - AskUserQuestion Loop Detection", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  /**
   * Helper to emit a stream-json tool_use message
   */
  function emitToolUse(toolName: string, input: unknown) {
    const message = JSON.stringify({
      type: "content_block_start",
      content_block: {
        type: "tool_use",
        id: `tool_${Date.now()}`,
        name: toolName,
        input,
      },
    });
    mockProcess.stdout!.emit("data", Buffer.from(message + "\n"));
  }

  it("should abort after 3 consecutive identical AskUserQuestion attempts", async () => {
    const onError = vi.fn();
    const onStderr = vi.fn();

    runStageSkillHeadless("feature-dev", 42, {
      onError,
      onStderr,
    });

    // Emit 3 identical AskUserQuestion tool calls
    const sameInput = { question: "What should I do?", options: ["A", "B"] };
    emitToolUse("AskUserQuestion", sameInput);
    emitToolUse("AskUserQuestion", sameInput);
    emitToolUse("AskUserQuestion", sameInput);

    // Should have called onError with loop detection message
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("Claude attempted AskUserQuestion 3 times"),
      })
    );

    // Should have logged to stderr
    expect(onStderr).toHaveBeenCalledWith(expect.stringContaining("[skillRunner] Loop detected"));

    // Process should be killed
    expect(mockProcess.kill).toHaveBeenCalledWith("SIGTERM");
  });

  it("should reset counter when different AskUserQuestion payload is used", () => {
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    // Emit 2 AskUserQuestion calls with same input
    const input1 = { question: "Question 1" };
    emitToolUse("AskUserQuestion", input1);
    emitToolUse("AskUserQuestion", input1);

    // Now emit with different input
    const input2 = { question: "Question 2" };
    emitToolUse("AskUserQuestion", input2);

    // Counter should have reset, so no error yet
    expect(onError).not.toHaveBeenCalled();
    expect(mockProcess.kill).not.toHaveBeenCalled();

    // Need 3 more of the same to trigger
    emitToolUse("AskUserQuestion", input2);
    emitToolUse("AskUserQuestion", input2);

    // Now should trigger
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it("should reset counter when different tool is called", () => {
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    // Emit 2 AskUserQuestion calls
    const askInput = { question: "What?" };
    emitToolUse("AskUserQuestion", askInput);
    emitToolUse("AskUserQuestion", askInput);

    // Now emit a different tool
    emitToolUse("Read", { file: "/some/path" });

    // Counter should be reset
    // Emit 2 more AskUserQuestion - should NOT trigger (only 2 consecutive)
    emitToolUse("AskUserQuestion", askInput);
    emitToolUse("AskUserQuestion", askInput);

    // No error because we reset
    expect(onError).not.toHaveBeenCalled();

    // One more should trigger
    emitToolUse("AskUserQuestion", askInput);
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it("should not trigger on non-AskUserQuestion tool calls", () => {
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    // Emit many Read tool calls with same input
    const readInput = { file: "/same/path" };
    for (let i = 0; i < 10; i++) {
      emitToolUse("Read", readInput);
    }

    // Should not trigger loop detection
    expect(onError).not.toHaveBeenCalled();
    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it("should allow 2 consecutive identical AskUserQuestion calls", () => {
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    // Emit 2 identical AskUserQuestion calls (threshold is 3)
    const input = { question: "Same question" };
    emitToolUse("AskUserQuestion", input);
    emitToolUse("AskUserQuestion", input);

    // Should NOT trigger yet
    expect(onError).not.toHaveBeenCalled();
    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it("should include attempt count in error message", () => {
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    // Trigger loop detection
    const input = { question: "Loop question" };
    emitToolUse("AskUserQuestion", input);
    emitToolUse("AskUserQuestion", input);
    emitToolUse("AskUserQuestion", input);

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringMatching(/3 times/),
      })
    );
  });

  it("should mention headless mode in error message", () => {
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    // Trigger loop detection
    const input = { question: "Loop question" };
    emitToolUse("AskUserQuestion", input);
    emitToolUse("AskUserQuestion", input);
    emitToolUse("AskUserQuestion", input);

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("headless pipeline mode"),
      })
    );
  });

  it("should call onToolUse callback for first 2 attempts before aborting on 3rd", () => {
    const onToolUse = vi.fn();
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onToolUse, onError });

    // Emit 3 AskUserQuestion calls
    const input = { question: "Test" };
    emitToolUse("AskUserQuestion", input);
    emitToolUse("AskUserQuestion", input);
    emitToolUse("AskUserQuestion", input);

    // onToolUse should have been called for first 2 (before abort on 3rd)
    // On the 3rd attempt, loop detection aborts BEFORE onToolUse callback
    expect(onToolUse).toHaveBeenCalledTimes(2);
    expect(onToolUse).toHaveBeenCalledWith("AskUserQuestion", input, expect.any(String));

    // Error should have been called
    expect(onError).toHaveBeenCalledTimes(1);
  });

  // =========================================================================
  // promptDetected flag (Issue #697)
  // =========================================================================
  describe("promptDetected flag (Issue #697)", () => {
    it("should set promptDetected=true when AskUserQuestion is detected and process exits", async () => {
      const onComplete = vi.fn();

      runStageSkillHeadless("issue-pickup", 42, { onComplete });

      // Emit a single AskUserQuestion (not enough to trigger loop abort)
      emitToolUse("AskUserQuestion", { question: "Reopen issue?" });

      // Process exits with code 0 (appears successful)
      mockProcess.emit("close", 0);

      // Wait for async callbacks
      await new Promise((resolve) => setTimeout(resolve, 10));

      // onComplete should have been called with promptDetected=true
      expect(onComplete).toHaveBeenCalledTimes(1);
      expect(onComplete).toHaveBeenCalledWith(
        expect.objectContaining({
          success: true,
          exitCode: 0,
          promptDetected: true,
        })
      );
    });

    it("should set promptDetected=false when no AskUserQuestion is detected", async () => {
      const onComplete = vi.fn();

      runStageSkillHeadless("issue-pickup", 42, { onComplete });

      // Emit normal tool calls (no AskUserQuestion)
      emitToolUse("Read", { file: "/some/path" });
      emitToolUse("Write", { file: "/some/path", content: "data" });

      // Process exits with code 0
      mockProcess.emit("close", 0);

      // Wait for async callbacks
      await new Promise((resolve) => setTimeout(resolve, 10));

      // onComplete should have been called with promptDetected=false
      expect(onComplete).toHaveBeenCalledTimes(1);
      expect(onComplete).toHaveBeenCalledWith(
        expect.objectContaining({
          success: true,
          exitCode: 0,
          promptDetected: false,
        })
      );
    });
  });
});
