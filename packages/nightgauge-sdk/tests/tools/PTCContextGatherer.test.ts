/**
 * PTCContextGatherer Tests
 *
 * Tests for the context-gathering PTC orchestration runner.
 * PTCExecutor and handler factories are mocked at module level
 * so no real API calls or filesystem access occurs.
 *
 * @see Issue #1070 - Optimize context file and git batch operations
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// Module-level mocks
// ---------------------------------------------------------------------------

const mockExecute = vi.fn();

vi.mock("../../src/tools/PTCExecutor.js", () => {
  return {
    PTCExecutor: vi.fn(function () {
      return { execute: mockExecute };
    }),
  };
});

vi.mock("../../src/tools/context-handlers.js", () => ({
  createContextHandlers: vi.fn().mockReturnValue(new Map()),
}));

vi.mock("../../src/tools/git-handlers.js", () => ({
  createGitHandlers: vi.fn().mockReturnValue(new Map()),
}));

// ---------------------------------------------------------------------------
// Imports (after mocks)
// ---------------------------------------------------------------------------

import { PTCContextGatherer } from "../../src/tools/PTCContextGatherer.js";
import type {
  ContextGatherInput,
  ContextGatherResult,
  PTCContextGathererOptions,
} from "../../src/tools/PTCContextGatherer.js";
import { PTCExecutor } from "../../src/tools/PTCExecutor.js";
import { createContextHandlers } from "../../src/tools/context-handlers.js";
import { createGitHandlers } from "../../src/tools/git-handlers.js";
import type { PTCResult } from "../../src/tools/PTCExecutor.js";

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const BASE_GATHER_INPUT: ContextGatherInput = {
  issueNumber: 42,
  baseBranch: "main",
  stages: ["issue", "planning", "dev"],
};

const BASE_OPTIONS: PTCContextGathererOptions = {
  apiKey: "sk-ant-test-key",
  model: "claude-test-model",
  cwd: "/tmp/test-project",
  gatherInput: BASE_GATHER_INPUT,
};

function makeSuccessPTCResult(outputOverride?: Record<string, unknown>): PTCResult {
  return {
    success: true,
    output: outputOverride ?? {
      contexts: {
        issue: { schema_version: "1.0", issue_number: 42 },
        planning: { schema_version: "1.1", plan_file: "PLAN.md" },
        dev: { schema_version: "1.1", commit_sha: "abc123" },
      },
      git: {
        diff: { success: true, files_changed: 3 },
        log: { success: true, commits: [], total: 0 },
        status: { success: true, branch: "feat/42", is_clean: true },
      },
    },
    textOutput: "",
    usage: { inputTokens: 800, outputTokens: 300 },
    turns: 4,
  };
}

function makeFailurePTCResult(error?: string): PTCResult {
  return {
    success: false,
    output: null,
    textOutput: "",
    usage: { inputTokens: 50, outputTokens: 10 },
    turns: 1,
    error: error ?? "Something went wrong",
  };
}

function createGatherer(overrides?: Partial<PTCContextGathererOptions>): PTCContextGatherer {
  return new PTCContextGatherer({ ...BASE_OPTIONS, ...overrides });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PTCContextGatherer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockExecute.mockResolvedValue(makeSuccessPTCResult());
  });

  // -------------------------------------------------------------------------
  // Constructor / dependency wiring
  // -------------------------------------------------------------------------

  describe("constructor", () => {
    it("instantiates PTCExecutor with correct options", async () => {
      await createGatherer().run();

      expect(PTCExecutor).toHaveBeenCalledOnce();
      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.apiKey).toBe("sk-ant-test-key");
      expect(ctorCall.model).toBe("claude-test-model");
      expect(ctorCall.cwd).toBe("/tmp/test-project");
      expect(ctorCall.maxTurns).toBe(10);
    });

    it("passes combined context + git tools (excluding write_context_file)", async () => {
      await createGatherer().run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(Array.isArray(ctorCall.tools)).toBe(true);
      // Should have read_context_file + list_context_files + 3 git tools = 5
      const toolNames = ctorCall.tools.map((t) => t.name);
      expect(toolNames).not.toContain("write_context_file");
    });

    it("calls createContextHandlers and createGitHandlers", async () => {
      await createGatherer().run();

      expect(createContextHandlers).toHaveBeenCalledOnce();
      expect(createGitHandlers).toHaveBeenCalledOnce();
    });

    it("combines handler maps from both factories", async () => {
      const contextMap = new Map([
        ["read_context_file", { name: "read_context_file", execute: vi.fn() }],
      ]);
      const gitMap = new Map([
        ["git_diff_summary", { name: "git_diff_summary", execute: vi.fn() }],
      ]);
      vi.mocked(createContextHandlers).mockReturnValueOnce(
        contextMap as Map<string, import("../../src/tools/tool-handlers.js").ToolHandler>
      );
      vi.mocked(createGitHandlers).mockReturnValueOnce(
        gitMap as Map<string, import("../../src/tools/tool-handlers.js").ToolHandler>
      );

      await createGatherer().run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.toolHandlers.size).toBe(2);
      expect(ctorCall.toolHandlers.has("read_context_file")).toBe(true);
      expect(ctorCall.toolHandlers.has("git_diff_summary")).toBe(true);
    });
  });

  // -------------------------------------------------------------------------
  // Successful gather with structured output
  // -------------------------------------------------------------------------

  describe("run() — successful structured output", () => {
    it("returns success: true when executor succeeds", async () => {
      const result = await createGatherer().run();
      expect(result.success).toBe(true);
    });

    it("maps contexts correctly from output", async () => {
      const result = await createGatherer().run();

      expect(result.contexts["issue"]).toEqual({
        schema_version: "1.0",
        issue_number: 42,
      });
      expect(result.contexts["planning"]).toEqual({
        schema_version: "1.1",
        plan_file: "PLAN.md",
      });
      expect(result.contexts["dev"]).toEqual({
        schema_version: "1.1",
        commit_sha: "abc123",
      });
    });

    it("maps git sections correctly from output", async () => {
      const result = await createGatherer().run();

      expect(result.git.diff).toEqual({ success: true, files_changed: 3 });
      expect(result.git.log).toEqual({
        success: true,
        commits: [],
        total: 0,
      });
      expect(result.git.status).toEqual({
        success: true,
        branch: "feat/42",
        is_clean: true,
      });
    });

    it("maps tokenUsage correctly", async () => {
      const result = await createGatherer().run();

      expect(result.tokenUsage.inputTokens).toBe(800);
      expect(result.tokenUsage.outputTokens).toBe(300);
    });

    it("maps turns correctly", async () => {
      const result = await createGatherer().run();
      expect(result.turns).toBe(4);
    });

    it("does not set error field on success", async () => {
      const result = await createGatherer().run();
      expect(result.error).toBeUndefined();
    });
  });

  // -------------------------------------------------------------------------
  // Fallback text output parsing
  // -------------------------------------------------------------------------

  describe("run() — fallback text output parsing", () => {
    it("parses valid JSON from textOutput when output is null", async () => {
      const jsonPayload = {
        contexts: { issue: { schema_version: "1.0" } },
        git: {
          diff: { success: true },
          log: { success: true },
          status: { success: true },
        },
      };
      mockExecute.mockResolvedValueOnce({
        success: true,
        output: null,
        textOutput: JSON.stringify(jsonPayload),
        usage: { inputTokens: 200, outputTokens: 100 },
        turns: 2,
      } satisfies PTCResult);

      const result = await createGatherer().run();

      expect(result.success).toBe(true);
      expect(result.contexts["issue"]).toEqual({ schema_version: "1.0" });
    });

    it("returns error when textOutput is not valid JSON", async () => {
      mockExecute.mockResolvedValueOnce({
        success: true,
        output: null,
        textOutput: "not json",
        usage: { inputTokens: 100, outputTokens: 50 },
        turns: 1,
      } satisfies PTCResult);

      const result = await createGatherer().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("Failed to parse PTC output as JSON");
    });
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  describe("run() — error handling", () => {
    it("returns error result when executor throws", async () => {
      mockExecute.mockRejectedValueOnce(new Error("Network timeout"));

      const result = await createGatherer().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("Network timeout");
    });

    it("returns error result when executor throws non-Error", async () => {
      mockExecute.mockRejectedValueOnce("raw string error");

      const result = await createGatherer().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("PTC execution failed");
    });

    it("returns zero token usage on executor throw", async () => {
      mockExecute.mockRejectedValueOnce(new Error("fail"));

      const result = await createGatherer().run();

      expect(result.tokenUsage.inputTokens).toBe(0);
      expect(result.tokenUsage.outputTokens).toBe(0);
    });

    it("returns 0 turns on executor throw", async () => {
      mockExecute.mockRejectedValueOnce(new Error("fail"));

      const result = await createGatherer().run();

      expect(result.turns).toBe(0);
    });

    it("returns error when PTCResult.success is false", async () => {
      mockExecute.mockResolvedValueOnce(makeFailurePTCResult("Max turns exceeded"));

      const result = await createGatherer().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("Max turns exceeded");
    });

    it("uses fallback error message when PTCResult has no error field", async () => {
      const failResult = makeFailurePTCResult();
      delete failResult.error;
      mockExecute.mockResolvedValueOnce(failResult);

      const result = await createGatherer().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("PTC execution returned failure");
    });

    it("error result has empty default sub-fields", async () => {
      mockExecute.mockRejectedValueOnce(new Error("fail"));

      const result = await createGatherer().run();

      expect(result.contexts).toEqual({});
      expect(result.git).toEqual({ diff: {}, log: {}, status: {} });
    });
  });

  // -------------------------------------------------------------------------
  // Prompt construction
  // -------------------------------------------------------------------------

  describe("prompt construction", () => {
    async function runAndCapturePrompt(input?: Partial<ContextGatherInput>): Promise<string> {
      mockExecute.mockResolvedValueOnce(makeSuccessPTCResult());
      const gatherer = createGatherer({
        gatherInput: { ...BASE_GATHER_INPUT, ...input },
      });
      await gatherer.run();
      return mockExecute.mock.calls[0][0] as string;
    }

    it("includes the issue number in the prompt", async () => {
      const prompt = await runAndCapturePrompt({ issueNumber: 99 });
      expect(prompt).toContain("#99");
    });

    it("includes context filenames for each stage", async () => {
      const prompt = await runAndCapturePrompt({
        stages: ["issue", "planning", "dev"],
        issueNumber: 42,
      });
      expect(prompt).toContain("issue-42.json");
      expect(prompt).toContain("planning-42.json");
      expect(prompt).toContain("dev-42.json");
    });

    it("includes base branch in git diff instruction", async () => {
      const prompt = await runAndCapturePrompt({ baseBranch: "develop" });
      expect(prompt).toContain('base="develop"');
    });

    it("includes batch mode section when batchMode is true", async () => {
      const prompt = await runAndCapturePrompt({
        batchMode: true,
        epicNumber: 100,
        stages: ["issue", "dev"],
      });
      expect(prompt).toContain("Batch Mode");
      expect(prompt).toContain("issue-batch-100.json");
      expect(prompt).toContain("dev-batch-100.json");
    });

    it("does not include batch section when batchMode is false", async () => {
      const prompt = await runAndCapturePrompt({ batchMode: false });
      expect(prompt).not.toContain("Batch Mode");
    });

    it("instructs to output JSON only", async () => {
      const prompt = await runAndCapturePrompt();
      expect(prompt).toContain("Output ONLY the JSON object");
    });

    it("instructs to call git operations", async () => {
      const prompt = await runAndCapturePrompt();
      expect(prompt).toContain("git_diff_summary");
      expect(prompt).toContain("git_log_structured");
      expect(prompt).toContain("git_status_structured");
    });

    it("instructs to use read_context_file for each stage", async () => {
      const prompt = await runAndCapturePrompt();
      expect(prompt).toContain("read_context_file");
    });
  });

  // -------------------------------------------------------------------------
  // Defensive parsing
  // -------------------------------------------------------------------------

  describe("defensive parsing", () => {
    it("defaults contexts to empty object when missing", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          git: {
            diff: { success: true },
            log: { success: true },
            status: { success: true },
          },
        })
      );

      const result = await createGatherer().run();
      expect(result.contexts).toEqual({});
    });

    it("defaults git sections to empty objects when missing", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          contexts: { issue: { schema_version: "1.0" } },
        })
      );

      const result = await createGatherer().run();
      expect(result.git.diff).toEqual({});
      expect(result.git.log).toEqual({});
      expect(result.git.status).toEqual({});
    });

    it("handles array contexts gracefully (defaults to empty)", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          contexts: ["not", "an", "object"],
          git: {
            diff: { success: true },
            log: { success: true },
            status: { success: true },
          },
        })
      );

      const result = await createGatherer().run();
      expect(result.contexts).toEqual({});
    });
  });

  // -------------------------------------------------------------------------
  // Model option
  // -------------------------------------------------------------------------

  describe("model option", () => {
    it("passes the provided model to PTCExecutor", async () => {
      await createGatherer({ model: "claude-opus-4-6" }).run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.model).toBe("claude-opus-4-6");
    });

    it("passes undefined model when not specified", async () => {
      const { model: _model, ...noModel } = BASE_OPTIONS;
      const gatherer = new PTCContextGatherer(noModel);
      await gatherer.run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.model).toBeUndefined();
    });
  });
});
