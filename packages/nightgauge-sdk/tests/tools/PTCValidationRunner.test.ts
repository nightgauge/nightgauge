/**
 * PTCValidationRunner Tests
 *
 * Tests for the validation-specific PTC orchestration runner.
 * PTCExecutor and createValidationHandlers are mocked at module level
 * so no real API calls or shell commands are made.
 *
 * @see Issue #1069 - Refactor feature-validate for PTC
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// ---------------------------------------------------------------------------
// Module-level mocks — must be declared before any imports that use them
// ---------------------------------------------------------------------------

// Shared mock execute fn; exposed via __mockExecute for test access
const mockExecute = vi.fn();

vi.mock("../../src/tools/PTCExecutor.js", () => {
  return {
    PTCExecutor: vi.fn(function () {
      return { execute: mockExecute };
    }),
  };
});

vi.mock("../../src/tools/tool-handlers.js", () => ({
  createValidationHandlers: vi.fn().mockReturnValue(new Map()),
}));

// ---------------------------------------------------------------------------
// Imports (after mocks are registered)
// ---------------------------------------------------------------------------

import { PTCValidationRunner, isPTCAvailable } from "../../src/tools/PTCValidationRunner.js";
import type {
  DevContextInput,
  ValidationResult,
  PTCValidationRunnerOptions,
} from "../../src/tools/PTCValidationRunner.js";
import { PTCExecutor } from "../../src/tools/PTCExecutor.js";
import { createValidationHandlers } from "../../src/tools/tool-handlers.js";
import type { PTCResult } from "../../src/tools/PTCExecutor.js";

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const BASE_DEV_CONTEXT: DevContextInput = {
  issueNumber: 42,
  commitSha: "abc1234def567890",
  filesCreated: ["src/foo.ts"],
  filesModified: ["src/bar.ts"],
  buildAlreadyPassed: false,
  unitTestsPassed: 0,
  unitTestsFailed: 0,
};

const BASE_OPTIONS: PTCValidationRunnerOptions = {
  apiKey: "sk-ant-test-key",
  model: "claude-test-model",
  cwd: "/tmp/test-project",
  devContext: BASE_DEV_CONTEXT,
};

/** Build a minimal successful PTCResult with structured object output */
function makeSuccessPTCResult(outputOverride?: Record<string, unknown>): PTCResult {
  return {
    success: true,
    output: outputOverride ?? {
      build: { ran: true, passed: true, command: "npm run build" },
      lint: { ran: true, passed: true, warning_count: 1, error_count: 0 },
      typecheck: { ran: true, passed: true, error_count: 0 },
      tests: { ran: true, passed: 10, failed: 0, skipped: 2 },
    },
    textOutput: "",
    usage: { inputTokens: 500, outputTokens: 200 },
    turns: 3,
  };
}

/** Build a PTCResult where output is null but textOutput contains JSON */
function makeTextOutputPTCResult(json: Record<string, unknown>): PTCResult {
  return {
    success: true,
    output: null,
    textOutput: JSON.stringify(json),
    usage: { inputTokens: 300, outputTokens: 100 },
    turns: 2,
  };
}

/** Build a failed PTCResult */
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

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

function createRunner(overrides?: Partial<PTCValidationRunnerOptions>): PTCValidationRunner {
  return new PTCValidationRunner({ ...BASE_OPTIONS, ...overrides });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PTCValidationRunner", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Reset mockExecute to an unresolved state by default; individual tests set it up
    mockExecute.mockResolvedValue(makeSuccessPTCResult());
  });

  // -------------------------------------------------------------------------
  // Constructor / dependency wiring
  // -------------------------------------------------------------------------

  describe("constructor", () => {
    it("instantiates PTCExecutor with correct options", async () => {
      const runner = createRunner();
      await runner.run();

      expect(PTCExecutor).toHaveBeenCalledOnce();
      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.apiKey).toBe("sk-ant-test-key");
      expect(ctorCall.model).toBe("claude-test-model");
      expect(ctorCall.cwd).toBe("/tmp/test-project");
      expect(ctorCall.maxTurns).toBe(10);
    });

    it("passes VALIDATION_TOOLS to PTCExecutor", async () => {
      const runner = createRunner();
      await runner.run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(Array.isArray(ctorCall.tools)).toBe(true);
      expect(ctorCall.tools.length).toBeGreaterThan(0);
    });

    it("passes toolHandlers returned by createValidationHandlers to PTCExecutor", async () => {
      const mockHandlerMap = new Map([["run_build", { name: "run_build", execute: vi.fn() }]]);
      vi.mocked(createValidationHandlers).mockReturnValueOnce(
        mockHandlerMap as Map<string, import("../../src/tools/tool-handlers.js").ToolHandler>
      );

      const runner = createRunner();
      await runner.run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.toolHandlers).toBe(mockHandlerMap);
    });

    it("calls createValidationHandlers exactly once per run()", async () => {
      const runner = createRunner();
      await runner.run();

      expect(createValidationHandlers).toHaveBeenCalledOnce();
    });
  });

  // -------------------------------------------------------------------------
  // Successful validation with structured object output
  // -------------------------------------------------------------------------

  describe("run() — successful structured JSON object output", () => {
    it("returns success: true when executor succeeds", async () => {
      mockExecute.mockResolvedValueOnce(makeSuccessPTCResult());
      const result = await createRunner().run();
      expect(result.success).toBe(true);
    });

    it("maps build fields correctly from object output", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: true, passed: true, command: "npm run build" },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();

      expect(result.build.ran).toBe(true);
      expect(result.build.passed).toBe(true);
      expect(result.build.command).toBe("npm run build");
    });

    it("maps lint fields correctly from object output", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: { ran: true, passed: false, warning_count: 3, error_count: 2 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();

      expect(result.lint.ran).toBe(true);
      expect(result.lint.passed).toBe(false);
      expect(result.lint.warningCount).toBe(3);
      expect(result.lint.errorCount).toBe(2);
    });

    it("maps typecheck fields correctly from object output", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: true, passed: false, error_count: 5 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();

      expect(result.typecheck.ran).toBe(true);
      expect(result.typecheck.passed).toBe(false);
      expect(result.typecheck.errorCount).toBe(5);
    });

    it("maps tests fields correctly from object output", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: true, passed: 15, failed: 2, skipped: 1 },
        })
      );

      const result = await createRunner().run();

      expect(result.tests.ran).toBe(true);
      expect(result.tests.passed).toBe(15);
      expect(result.tests.failed).toBe(2);
      expect(result.tests.skipped).toBe(1);
    });

    it("maps optional coverage field when present", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: {
            ran: true,
            passed: 10,
            failed: 0,
            skipped: 0,
            coverage: 87.5,
          },
        })
      );

      const result = await createRunner().run();
      expect(result.tests.coverage).toBe(87.5);
    });

    it("omits coverage field when not present in output", async () => {
      const result = await createRunner().run();
      expect(result.tests.coverage).toBeUndefined();
    });

    it("maps tokenUsage correctly", async () => {
      const ptcResult = makeSuccessPTCResult();
      ptcResult.usage = { inputTokens: 1234, outputTokens: 567 };
      mockExecute.mockResolvedValueOnce(ptcResult);

      const result = await createRunner().run();

      expect(result.tokenUsage.inputTokens).toBe(1234);
      expect(result.tokenUsage.outputTokens).toBe(567);
    });

    it("maps turns correctly", async () => {
      const ptcResult = makeSuccessPTCResult();
      ptcResult.turns = 7;
      mockExecute.mockResolvedValueOnce(ptcResult);

      const result = await createRunner().run();

      expect(result.turns).toBe(7);
    });

    it("does not set error field on success", async () => {
      const result = await createRunner().run();
      expect(result.error).toBeUndefined();
    });
  });

  // -------------------------------------------------------------------------
  // Fallback: parse from textOutput when output is not an object
  // -------------------------------------------------------------------------

  describe("run() — fallback text output parsing", () => {
    it("parses valid JSON from textOutput when output is null", async () => {
      const jsonPayload = {
        build: { ran: true, passed: true, command: "npm run build" },
        lint: { ran: true, passed: true, warning_count: 0, error_count: 0 },
        typecheck: { ran: true, passed: true, error_count: 0 },
        tests: { ran: true, passed: 5, failed: 0, skipped: 0 },
      };
      mockExecute.mockResolvedValueOnce(makeTextOutputPTCResult(jsonPayload));

      const result = await createRunner().run();

      expect(result.success).toBe(true);
      expect(result.build.ran).toBe(true);
      expect(result.build.passed).toBe(true);
      expect(result.tests.passed).toBe(5);
    });

    it("returns error result when textOutput contains non-JSON text", async () => {
      mockExecute.mockResolvedValueOnce({
        success: true,
        output: null,
        textOutput: "This is plain text, not JSON at all.",
        usage: { inputTokens: 100, outputTokens: 50 },
        turns: 1,
      } satisfies PTCResult);

      const result = await createRunner().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("Failed to parse PTC output as JSON");
    });

    it("preserves tokenUsage even when textOutput parse fails", async () => {
      mockExecute.mockResolvedValueOnce({
        success: true,
        output: null,
        textOutput: "unparseable garbage ###",
        usage: { inputTokens: 200, outputTokens: 80 },
        turns: 4,
      } satisfies PTCResult);

      const result = await createRunner().run();

      expect(result.tokenUsage.inputTokens).toBe(200);
      expect(result.tokenUsage.outputTokens).toBe(80);
      expect(result.turns).toBe(4);
    });

    it("returns error result when output is a string (not an object)", async () => {
      mockExecute.mockResolvedValueOnce({
        success: true,
        output: "a bare string",
        textOutput: "a bare string",
        usage: { inputTokens: 10, outputTokens: 5 },
        turns: 1,
      } satisfies PTCResult);

      // output is a string, not null/undefined, but also not an object —
      // the runner will branch to the fallback text parse path.
      // 'a bare string' is not valid JSON, so it should produce a parse error.
      const result = await createRunner().run();

      expect(result.success).toBe(false);
    });

    it("falls back to textOutput parsing when output is an array (not a plain object)", async () => {
      // Arrays are objects in JS but are not Record<string, unknown> with the
      // expected keys — this exercises the fallback via textOutput.
      const jsonPayload = {
        build: { ran: false, passed: false },
        lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
        typecheck: { ran: false, passed: false, error_count: 0 },
        tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
      };
      mockExecute.mockResolvedValueOnce({
        success: true,
        output: ["this", "is", "an", "array"],
        textOutput: JSON.stringify(jsonPayload),
        usage: { inputTokens: 50, outputTokens: 20 },
        turns: 1,
      } satisfies PTCResult);

      // Arrays pass the `typeof === 'object'` check; the runner will attempt
      // to parse them as Record<string,unknown> — casting will succeed but
      // fields like `build` will be undefined → parse helpers return defaults.
      const result = await createRunner().run();

      // success comes from the structured branch (array is truthy object)
      expect(result.success).toBe(true);
      // all sub-fields default to safe zero values when keys are missing
      expect(result.build.ran).toBe(false);
      expect(result.lint.ran).toBe(false);
    });
  });

  // -------------------------------------------------------------------------
  // Error handling when executor throws
  // -------------------------------------------------------------------------

  describe("run() — executor throws", () => {
    it("returns error result when executor.execute() throws an Error", async () => {
      mockExecute.mockRejectedValueOnce(new Error("Network timeout"));

      const result = await createRunner().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("Network timeout");
    });

    it("returns error result when executor.execute() throws a non-Error value", async () => {
      mockExecute.mockRejectedValueOnce("raw string error");

      const result = await createRunner().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("PTC execution failed");
    });

    it("returns zero token usage on executor throw", async () => {
      mockExecute.mockRejectedValueOnce(new Error("API rate limit"));

      const result = await createRunner().run();

      expect(result.tokenUsage.inputTokens).toBe(0);
      expect(result.tokenUsage.outputTokens).toBe(0);
    });

    it("returns 0 turns on executor throw", async () => {
      mockExecute.mockRejectedValueOnce(new Error("API rate limit"));

      const result = await createRunner().run();

      expect(result.turns).toBe(0);
    });

    it("returns error result when PTCResult.success is false", async () => {
      mockExecute.mockResolvedValueOnce(makeFailurePTCResult("Max turns exceeded"));

      const result = await createRunner().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("Max turns exceeded");
    });

    it("uses fallback error message when PTCResult has success: false and no error field", async () => {
      const failResult = makeFailurePTCResult();
      delete failResult.error;
      mockExecute.mockResolvedValueOnce(failResult);

      const result = await createRunner().run();

      expect(result.success).toBe(false);
      expect(result.error).toBe("PTC execution returned failure");
    });

    it("error result has default zero-value sub-fields", async () => {
      mockExecute.mockRejectedValueOnce(new Error("fail"));

      const result = await createRunner().run();

      expect(result.build).toEqual({ ran: false, passed: false });
      expect(result.lint).toEqual({
        ran: false,
        passed: false,
        warningCount: 0,
        errorCount: 0,
      });
      expect(result.typecheck).toEqual({
        ran: false,
        passed: false,
        errorCount: 0,
      });
      expect(result.tests).toEqual({
        ran: false,
        passed: 0,
        failed: 0,
        skipped: 0,
      });
    });
  });

  // -------------------------------------------------------------------------
  // isPTCAvailable()
  // -------------------------------------------------------------------------

  describe("isPTCAvailable()", () => {
    afterEach(() => {
      // Restore the env var to its original state after each test
      delete process.env.ANTHROPIC_API_KEY;
    });

    it("returns true when ANTHROPIC_API_KEY is set to a non-empty string", () => {
      process.env.ANTHROPIC_API_KEY = "sk-ant-real-key";
      expect(isPTCAvailable()).toBe(true);
    });

    it("returns false when ANTHROPIC_API_KEY is not set", () => {
      delete process.env.ANTHROPIC_API_KEY;
      expect(isPTCAvailable()).toBe(false);
    });

    it("returns false when ANTHROPIC_API_KEY is an empty string", () => {
      process.env.ANTHROPIC_API_KEY = "";
      expect(isPTCAvailable()).toBe(false);
    });

    it("returns true regardless of the key value as long as it is non-empty", () => {
      process.env.ANTHROPIC_API_KEY = "any-value-works";
      expect(isPTCAvailable()).toBe(true);
    });
  });

  // -------------------------------------------------------------------------
  // Prompt construction
  // -------------------------------------------------------------------------

  describe("buildPrompt() — prompt construction via executor call arg", () => {
    /** Helper that captures and returns the prompt passed to execute() */
    async function runAndCapturePrompt(context: Partial<DevContextInput> = {}): Promise<string> {
      mockExecute.mockResolvedValueOnce(makeSuccessPTCResult());
      const runner = createRunner({
        devContext: { ...BASE_DEV_CONTEXT, ...context },
      });
      await runner.run();
      expect(mockExecute).toHaveBeenCalledOnce();
      return mockExecute.mock.calls[0][0] as string;
    }

    it("includes the issue number in the prompt", async () => {
      const prompt = await runAndCapturePrompt({ issueNumber: 99 });
      expect(prompt).toContain("#99");
    });

    it("includes the commit SHA in the prompt", async () => {
      const prompt = await runAndCapturePrompt({ commitSha: "deadbeef" });
      expect(prompt).toContain("deadbeef");
    });

    it("includes filesCreated as JSON in the prompt", async () => {
      const prompt = await runAndCapturePrompt({
        filesCreated: ["src/alpha.ts", "src/beta.ts"],
      });
      expect(prompt).toContain(JSON.stringify(["src/alpha.ts", "src/beta.ts"]));
    });

    it("includes filesModified as JSON in the prompt", async () => {
      const prompt = await runAndCapturePrompt({
        filesModified: ["src/gamma.ts"],
      });
      expect(prompt).toContain(JSON.stringify(["src/gamma.ts"]));
    });

    it("instructs to SKIP build when buildAlreadyPassed is true", async () => {
      const prompt = await runAndCapturePrompt({ buildAlreadyPassed: true });
      expect(prompt).toContain("Build already passed in feature-dev stage (SKIP build)");
      expect(prompt).toContain("SKIP run_build (already passed)");
    });

    it("instructs to RUN build when buildAlreadyPassed is false", async () => {
      const prompt = await runAndCapturePrompt({ buildAlreadyPassed: false });
      expect(prompt).toContain("Build has not been verified yet (RUN build)");
      expect(prompt).toContain("Call run_build()");
    });

    it("instructs to SKIP unit tests when all tests passed and none failed", async () => {
      const prompt = await runAndCapturePrompt({
        unitTestsPassed: 10,
        unitTestsFailed: 0,
      });
      expect(prompt).toContain("Unit tests already passed");
      expect(prompt).toContain("SKIP run_tests (already passed in dev)");
    });

    it("instructs to RUN unit tests when unitTestsFailed is non-zero", async () => {
      const prompt = await runAndCapturePrompt({
        unitTestsPassed: 5,
        unitTestsFailed: 2,
      });
      expect(prompt).toContain("Unit tests need re-running");
      expect(prompt).toContain("Call run_tests()");
    });

    it("instructs to RUN unit tests when unitTestsPassed is zero and unitTestsFailed is zero", async () => {
      const prompt = await runAndCapturePrompt({
        unitTestsPassed: 0,
        unitTestsFailed: 0,
      });
      // skipUnitTests = (0 > 0 && 0 === 0) = false
      expect(prompt).toContain("Unit tests need re-running");
    });

    it("always includes typecheck and lint instructions regardless of skip flags", async () => {
      const prompt = await runAndCapturePrompt({
        buildAlreadyPassed: true,
        unitTestsPassed: 10,
        unitTestsFailed: 0,
      });
      expect(prompt).toContain("run_typecheck()");
      expect(prompt).toContain("run_lint()");
    });

    it("prompt instructs Claude to output JSON only", async () => {
      const prompt = await runAndCapturePrompt();
      expect(prompt).toContain("output a single JSON object");
      expect(prompt).toContain("Output ONLY the JSON object");
    });
  });

  // -------------------------------------------------------------------------
  // devContext values passed through
  // -------------------------------------------------------------------------

  describe("devContext value pass-through", () => {
    it("reflects correct issueNumber in prompt for different issues", async () => {
      mockExecute.mockResolvedValue(makeSuccessPTCResult());

      const runner1 = createRunner({
        devContext: { ...BASE_DEV_CONTEXT, issueNumber: 1 },
      });
      await runner1.run();
      const prompt1 = mockExecute.mock.calls[0][0] as string;

      mockExecute.mockClear();

      const runner2 = createRunner({
        devContext: { ...BASE_DEV_CONTEXT, issueNumber: 9999 },
      });
      await runner2.run();
      const prompt2 = mockExecute.mock.calls[0][0] as string;

      expect(prompt1).toContain("#1");
      expect(prompt2).toContain("#9999");
    });

    it("reflects unitTestsPassed count in the skip-tests message", async () => {
      mockExecute.mockResolvedValueOnce(makeSuccessPTCResult());
      const runner = createRunner({
        devContext: {
          ...BASE_DEV_CONTEXT,
          unitTestsPassed: 42,
          unitTestsFailed: 0,
        },
      });
      await runner.run();

      const prompt = mockExecute.mock.calls[0][0] as string;
      expect(prompt).toContain("42 passed");
    });

    it("reflects unitTestsFailed count in the re-run message", async () => {
      mockExecute.mockResolvedValueOnce(makeSuccessPTCResult());
      const runner = createRunner({
        devContext: {
          ...BASE_DEV_CONTEXT,
          unitTestsPassed: 3,
          unitTestsFailed: 7,
        },
      });
      await runner.run();

      const prompt = mockExecute.mock.calls[0][0] as string;
      expect(prompt).toContain("7 failed");
    });
  });

  // -------------------------------------------------------------------------
  // ValidationResult shape
  // -------------------------------------------------------------------------

  describe("ValidationResult shape", () => {
    it("always has a boolean success field", async () => {
      const result = await createRunner().run();
      expect(typeof result.success).toBe("boolean");
    });

    it("build sub-object always has ran and passed booleans", async () => {
      const result = await createRunner().run();
      expect(typeof result.build.ran).toBe("boolean");
      expect(typeof result.build.passed).toBe("boolean");
    });

    it("lint sub-object always has ran, passed, warningCount, errorCount", async () => {
      const result = await createRunner().run();
      expect(typeof result.lint.ran).toBe("boolean");
      expect(typeof result.lint.passed).toBe("boolean");
      expect(typeof result.lint.warningCount).toBe("number");
      expect(typeof result.lint.errorCount).toBe("number");
    });

    it("typecheck sub-object always has ran, passed, errorCount", async () => {
      const result = await createRunner().run();
      expect(typeof result.typecheck.ran).toBe("boolean");
      expect(typeof result.typecheck.passed).toBe("boolean");
      expect(typeof result.typecheck.errorCount).toBe("number");
    });

    it("tests sub-object always has ran, passed, failed, skipped", async () => {
      const result = await createRunner().run();
      expect(typeof result.tests.ran).toBe("boolean");
      expect(typeof result.tests.passed).toBe("number");
      expect(typeof result.tests.failed).toBe("number");
      expect(typeof result.tests.skipped).toBe("number");
    });

    it("tokenUsage always has inputTokens and outputTokens numbers", async () => {
      const result = await createRunner().run();
      expect(typeof result.tokenUsage.inputTokens).toBe("number");
      expect(typeof result.tokenUsage.outputTokens).toBe("number");
    });

    it("turns is always a number", async () => {
      const result = await createRunner().run();
      expect(typeof result.turns).toBe("number");
    });

    it("error is a string when present", async () => {
      mockExecute.mockRejectedValueOnce(new Error("oops"));
      const result = await createRunner().run();
      expect(typeof result.error).toBe("string");
    });

    it("build.skippedReason is a string when present in output", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: {
            ran: false,
            passed: false,
            skipped_reason: "already verified",
          },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();
      expect(typeof result.build.skippedReason).toBe("string");
      expect(result.build.skippedReason).toBe("already verified");
    });

    it("build.command is undefined when not present in output", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();
      expect(result.build.command).toBeUndefined();
    });
  });

  // -------------------------------------------------------------------------
  // Defensive parsing (missing / malformed sub-fields)
  // -------------------------------------------------------------------------

  describe("parseResult() — defensive defaults for malformed sub-fields", () => {
    it('defaults build to { ran: false, passed: false, skippedReason: "no data" } when build is missing', async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          // build intentionally omitted
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();
      expect(result.build).toEqual({
        ran: false,
        passed: false,
        skippedReason: "no data",
      });
    });

    it("defaults lint to zero counts when lint is missing", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          // lint intentionally omitted
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();
      expect(result.lint).toEqual({
        ran: false,
        passed: false,
        warningCount: 0,
        errorCount: 0,
      });
    });

    it("defaults typecheck to zero errorCount when typecheck is missing", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          // typecheck intentionally omitted
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();
      expect(result.typecheck).toEqual({
        ran: false,
        passed: false,
        errorCount: 0,
      });
    });

    it("defaults tests to zero counts when tests is missing", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          // tests intentionally omitted
        })
      );

      const result = await createRunner().run();
      expect(result.tests).toEqual({
        ran: false,
        passed: 0,
        failed: 0,
        skipped: 0,
      });
    });

    it("treats non-boolean ran/passed fields as false", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: {
            ran: "yes" as unknown as boolean,
            passed: 1 as unknown as boolean,
          },
          lint: { ran: false, passed: false, warning_count: 0, error_count: 0 },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();
      // ran: 'yes' === true → false; passed: 1 === true → false
      expect(result.build.ran).toBe(false);
      expect(result.build.passed).toBe(false);
    });

    it("treats non-number warning_count as 0", async () => {
      mockExecute.mockResolvedValueOnce(
        makeSuccessPTCResult({
          build: { ran: false, passed: false },
          lint: {
            ran: true,
            passed: true,
            warning_count: "many" as unknown as number,
            error_count: 0,
          },
          typecheck: { ran: false, passed: false, error_count: 0 },
          tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
        })
      );

      const result = await createRunner().run();
      expect(result.lint.warningCount).toBe(0);
    });
  });

  // -------------------------------------------------------------------------
  // model option defaults
  // -------------------------------------------------------------------------

  describe("model option", () => {
    it("passes the provided model to PTCExecutor", async () => {
      mockExecute.mockResolvedValueOnce(makeSuccessPTCResult());
      const runner = createRunner({ model: "claude-opus-4-6" });
      await runner.run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.model).toBe("claude-opus-4-6");
    });

    it("passes undefined model to PTCExecutor when not specified (executor handles default)", async () => {
      mockExecute.mockResolvedValueOnce(makeSuccessPTCResult());
      const { model: _model, ...noModel } = BASE_OPTIONS;
      const runner = new PTCValidationRunner(noModel);
      await runner.run();

      const ctorCall = vi.mocked(PTCExecutor).mock.calls[0][0];
      expect(ctorCall.model).toBeUndefined();
    });
  });
});
