import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("child_process", () => ({
  execSync: vi.fn(),
}));

import { execSync } from "child_process";
import {
  RunBuildHandler,
  RunLintHandler,
  RunTestsHandler,
  RunTypecheckHandler,
  createValidationHandlers,
  type ToolHandler,
  type ToolResult,
} from "../../src/tools/tool-handlers.js";

const mockExecSync = vi.mocked(execSync);

const CWD = "/project";

/** Helper: make execSync return stdout as if the command succeeded. */
function mockSuccess(stdout: string): void {
  mockExecSync.mockReturnValueOnce(stdout as unknown as Buffer);
}

/** Helper: make execSync throw as if the command failed. */
function mockFailure(opts: { status?: number; stdout?: string; stderr?: string }): void {
  const err = Object.assign(new Error("command failed"), {
    status: opts.status ?? 1,
    stdout: opts.stdout ?? "",
    stderr: opts.stderr ?? "",
  });
  mockExecSync.mockImplementationOnce(() => {
    throw err;
  });
}

// ---------------------------------------------------------------------------
// Shared structural assertions
// ---------------------------------------------------------------------------

function assertToolResultShape(result: ToolResult, success: boolean): void {
  expect(typeof result.success).toBe("boolean");
  expect(result.success).toBe(success);
  expect(result.output).toBeDefined();
  expect(typeof result.output).toBe("object");
  // output.success must mirror top-level success
  expect(result.output["success"]).toBe(success);
}

// ---------------------------------------------------------------------------
// RunBuildHandler
// ---------------------------------------------------------------------------

describe("RunBuildHandler", () => {
  let handler: RunBuildHandler;

  beforeEach(() => {
    handler = new RunBuildHandler();
    vi.clearAllMocks();
  });

  it('has name "run_build"', () => {
    expect(handler.name).toBe("run_build");
  });

  it("returns success=true when build command exits 0", async () => {
    mockSuccess("Build complete.\n");
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, true);
    expect(result.output["exit_code"]).toBe(0);
    expect(result.output["stdout"]).toBe("Build complete.\n");
    expect(result.output["stderr"]).toBe("");
    expect(typeof result.output["duration_ms"]).toBe("number");
  });

  it("returns success=false when build command fails", async () => {
    mockFailure({ status: 2, stderr: "Error: compilation failed\n" });
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, false);
    expect(result.output["exit_code"]).toBe(2);
    expect(result.output["stderr"]).toBe("Error: compilation failed\n");
  });

  it('uses default command "npm run build" when no input.command provided', async () => {
    mockSuccess("");
    await handler.execute({}, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      "npm run build",
      expect.objectContaining({ cwd: CWD })
    );
  });

  it("uses custom command when input.command is provided", async () => {
    mockSuccess("");
    await handler.execute({ command: "yarn build" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith("yarn build", expect.objectContaining({ cwd: CWD }));
  });

  it("uses input.cwd over the caller-supplied cwd argument", async () => {
    mockSuccess("");
    await handler.execute({ cwd: "/custom/path" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      "npm run build",
      expect.objectContaining({ cwd: "/custom/path" })
    );
  });

  it("ignores non-string input.command and falls back to default", async () => {
    mockSuccess("");
    await handler.execute({ command: 42 }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith("npm run build", expect.any(Object));
  });

  it("includes duration_ms in output", async () => {
    mockSuccess("output");
    const result = await handler.execute({}, CWD);
    expect(result.output["duration_ms"]).toBeGreaterThanOrEqual(0);
  });

  it("captures stdout from failed process", async () => {
    mockFailure({ stdout: "partial build output\n", stderr: "fail\n" });
    const result = await handler.execute({}, CWD);
    expect(result.output["stdout"]).toBe("partial build output\n");
  });

  it("defaults exit_code to 1 when error has no status property", async () => {
    const err = new Error("unexpected crash");
    mockExecSync.mockImplementationOnce(() => {
      throw err;
    });
    const result = await handler.execute({}, CWD);
    expect(result.output["exit_code"]).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// RunLintHandler
// ---------------------------------------------------------------------------

describe("RunLintHandler", () => {
  let handler: RunLintHandler;

  beforeEach(() => {
    handler = new RunLintHandler();
    vi.clearAllMocks();
  });

  it('has name "run_lint"', () => {
    expect(handler.name).toBe("run_lint");
  });

  it("returns success=true when lint exits 0", async () => {
    mockSuccess("No problems found.\n");
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, true);
    expect(result.output["exit_code"]).toBe(0);
  });

  it("returns success=false when lint exits non-zero", async () => {
    mockFailure({ status: 1, stderr: "ESLint found errors\n" });
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, false);
  });

  it('uses default command "npm run lint"', async () => {
    mockSuccess("");
    await handler.execute({}, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      "npm run lint",
      expect.objectContaining({ cwd: CWD })
    );
  });

  it("uses custom command when input.command is provided", async () => {
    mockSuccess("");
    await handler.execute({ command: "eslint src/" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith("eslint src/", expect.any(Object));
  });

  it('appends " -- --fix" when input.fix=true and command does not include --fix', async () => {
    mockSuccess("");
    await handler.execute({ fix: true }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith("npm run lint -- --fix", expect.any(Object));
  });

  it("does NOT double-append --fix when command already contains it", async () => {
    mockSuccess("");
    await handler.execute({ command: "eslint --fix src/", fix: true }, CWD);
    const call = mockExecSync.mock.calls[0][0] as string;
    const fixOccurrences = (call.match(/--fix/g) ?? []).length;
    expect(fixOccurrences).toBe(1);
  });

  it("does NOT append --fix when input.fix is false", async () => {
    mockSuccess("");
    await handler.execute({ fix: false }, CWD);
    const call = mockExecSync.mock.calls[0][0] as string;
    expect(call).not.toContain("--fix");
  });

  it("parses warning_count and error_count from stdout", async () => {
    mockSuccess("3 errors, 7 warnings found.\n");
    const result = await handler.execute({}, CWD);
    // Pattern: /(\d+)\s+error.*?(\d+)\s+warning/i
    expect(result.output["error_count"]).toBe(3);
    expect(result.output["warning_count"]).toBe(7);
  });

  it("returns 0 for warning_count and error_count when stdout has no match", async () => {
    mockSuccess("All good.\n");
    const result = await handler.execute({}, CWD);
    expect(result.output["warning_count"]).toBe(0);
    expect(result.output["error_count"]).toBe(0);
  });

  it("output includes all required schema fields", async () => {
    mockSuccess("");
    const result = await handler.execute({}, CWD);
    const keys = Object.keys(result.output);
    expect(keys).toContain("success");
    expect(keys).toContain("exit_code");
    expect(keys).toContain("stdout");
    expect(keys).toContain("stderr");
    expect(keys).toContain("warning_count");
    expect(keys).toContain("error_count");
    expect(keys).toContain("duration_ms");
  });

  it("uses input.cwd when provided", async () => {
    mockSuccess("");
    await handler.execute({ cwd: "/other" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ cwd: "/other" })
    );
  });
});

// ---------------------------------------------------------------------------
// RunTestsHandler
// ---------------------------------------------------------------------------

describe("RunTestsHandler", () => {
  let handler: RunTestsHandler;

  beforeEach(() => {
    handler = new RunTestsHandler();
    vi.clearAllMocks();
  });

  it('has name "run_tests"', () => {
    expect(handler.name).toBe("run_tests");
  });

  it("returns success=true when tests exit 0", async () => {
    mockSuccess("Tests  5 passed\n");
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, true);
    expect(result.output["exit_code"]).toBe(0);
  });

  it("returns success=false when tests exit non-zero", async () => {
    mockFailure({ status: 1 });
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, false);
  });

  it('uses default command "npm test"', async () => {
    mockSuccess("");
    await handler.execute({}, CWD);
    expect(mockExecSync).toHaveBeenCalledWith("npm test", expect.objectContaining({ cwd: CWD }));
  });

  it("appends pattern when input.pattern is provided", async () => {
    mockSuccess("");
    await handler.execute({ pattern: "src/**/*.test.ts" }, CWD);
    const call = mockExecSync.mock.calls[0][0] as string;
    expect(call).toContain("src/**/*.test.ts");
    expect(call.startsWith("npm test")).toBe(true);
  });

  it("rejects shell metacharacters in test patterns", async () => {
    const result = await handler.execute({ pattern: "tests; touch /tmp/pwned" }, CWD);
    expect(result.success).toBe(false);
    expect(mockExecSync).not.toHaveBeenCalled();
  });

  it('appends " -- --coverage" when input.coverage=true', async () => {
    mockSuccess("");
    await handler.execute({ coverage: true }, CWD);
    const call = mockExecSync.mock.calls[0][0] as string;
    expect(call).toContain("--coverage");
  });

  it("appends both pattern and coverage flags when both are provided", async () => {
    mockSuccess("");
    await handler.execute({ pattern: "foo.test.ts", coverage: true }, CWD);
    const call = mockExecSync.mock.calls[0][0] as string;
    expect(call).toContain("foo.test.ts");
    expect(call).toContain("--coverage");
  });

  it("parses passed and failed counts from Vitest/Jest style output", async () => {
    const vitestOutput = "Tests  12 passed | 3 failed (15)\n Duration  2.50s\n";
    mockSuccess(vitestOutput);
    const result = await handler.execute({}, CWD);
    expect(result.output["passed"]).toBe(12);
    expect(result.output["failed"]).toBe(3);
  });

  it("parses skipped count from stdout", async () => {
    mockSuccess("Tests  5 passed\n2 skipped\n");
    const result = await handler.execute({}, CWD);
    expect(result.output["skipped"]).toBe(2);
  });

  it("parses coverage percentage from coverage table output", async () => {
    const covOutput = "All files          |   87.50 | 100 | 75 | 90\n";
    mockSuccess(covOutput);
    const result = await handler.execute({}, CWD);
    expect(result.output["coverage"]).toBeCloseTo(87.5);
  });

  it("sets coverage to undefined when no coverage line present", async () => {
    mockSuccess("Tests  1 passed\n");
    const result = await handler.execute({}, CWD);
    expect(result.output["coverage"]).toBeUndefined();
  });

  it("defaults passed/failed/skipped to 0 when output has no match", async () => {
    mockSuccess("All done.\n");
    const result = await handler.execute({}, CWD);
    expect(result.output["passed"]).toBe(0);
    expect(result.output["failed"]).toBe(0);
    expect(result.output["skipped"]).toBe(0);
  });

  it("output includes all required schema fields", async () => {
    mockSuccess("");
    const result = await handler.execute({}, CWD);
    const keys = Object.keys(result.output);
    expect(keys).toContain("success");
    expect(keys).toContain("exit_code");
    expect(keys).toContain("passed");
    expect(keys).toContain("failed");
    expect(keys).toContain("skipped");
    expect(keys).toContain("stdout");
    expect(keys).toContain("stderr");
    expect(keys).toContain("duration_ms");
  });

  it("uses custom command when input.command is provided", async () => {
    mockSuccess("");
    await handler.execute({ command: "vitest run" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      expect.stringContaining("vitest run"),
      expect.any(Object)
    );
  });

  it("uses input.cwd when provided", async () => {
    mockSuccess("");
    await handler.execute({ cwd: "/sub/pkg" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ cwd: "/sub/pkg" })
    );
  });
});

// ---------------------------------------------------------------------------
// RunTypecheckHandler
// ---------------------------------------------------------------------------

describe("RunTypecheckHandler", () => {
  let handler: RunTypecheckHandler;

  beforeEach(() => {
    handler = new RunTypecheckHandler();
    vi.clearAllMocks();
  });

  it('has name "run_typecheck"', () => {
    expect(handler.name).toBe("run_typecheck");
  });

  it("returns success=true when tsc exits 0", async () => {
    mockSuccess("");
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, true);
    expect(result.output["exit_code"]).toBe(0);
    expect(result.output["error_count"]).toBe(0);
  });

  it("returns success=false when tsc exits non-zero", async () => {
    mockFailure({ status: 1, stdout: "src/foo.ts(10,5): error TS2345: ...\n" });
    const result = await handler.execute({}, CWD);
    assertToolResultShape(result, false);
  });

  it('uses default command "npx tsc --noEmit"', async () => {
    mockSuccess("");
    await handler.execute({}, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      "npx tsc --noEmit",
      expect.objectContaining({ cwd: CWD })
    );
  });

  it("uses custom command when input.command is provided", async () => {
    mockSuccess("");
    await handler.execute({ command: "tsc -p tsconfig.test.json --noEmit" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      "tsc -p tsconfig.test.json --noEmit",
      expect.any(Object)
    );
  });

  it("counts error TS codes in stdout", async () => {
    const tsOutput = [
      "src/a.ts(1,1): error TS2345: argument type mismatch",
      "src/b.ts(5,3): error TS2304: cannot find name",
      "src/c.ts(9,7): error TS7006: parameter has implicit any",
    ].join("\n");
    mockFailure({ stdout: tsOutput });
    const result = await handler.execute({}, CWD);
    expect(result.output["error_count"]).toBe(3);
  });

  it("returns error_count=0 when stdout has no TS error lines", async () => {
    mockSuccess("Found 0 errors.\n");
    const result = await handler.execute({}, CWD);
    expect(result.output["error_count"]).toBe(0);
  });

  it("output includes all required schema fields", async () => {
    mockSuccess("");
    const result = await handler.execute({}, CWD);
    const keys = Object.keys(result.output);
    expect(keys).toContain("success");
    expect(keys).toContain("exit_code");
    expect(keys).toContain("stdout");
    expect(keys).toContain("stderr");
    expect(keys).toContain("error_count");
    expect(keys).toContain("duration_ms");
  });

  it("uses input.cwd when provided", async () => {
    mockSuccess("");
    await handler.execute({ cwd: "/other/pkg" }, CWD);
    expect(mockExecSync).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ cwd: "/other/pkg" })
    );
  });
});

// ---------------------------------------------------------------------------
// Table-driven: default commands across all handlers
// ---------------------------------------------------------------------------

describe("default command table", () => {
  const cases: Array<{ handler: ToolHandler; expectedCommand: string }> = [
    { handler: new RunBuildHandler(), expectedCommand: "npm run build" },
    { handler: new RunLintHandler(), expectedCommand: "npm run lint" },
    { handler: new RunTestsHandler(), expectedCommand: "npm test" },
    { handler: new RunTypecheckHandler(), expectedCommand: "npx tsc --noEmit" },
  ];

  beforeEach(() => {
    vi.clearAllMocks();
  });

  cases.forEach(({ handler, expectedCommand }) => {
    it(`${handler.name} uses "${expectedCommand}" as default`, async () => {
      mockSuccess("");
      await handler.execute({}, CWD);
      expect(mockExecSync).toHaveBeenCalledWith(expectedCommand, expect.any(Object));
    });
  });
});

// ---------------------------------------------------------------------------
// Table-driven: execSync options are always set correctly
// ---------------------------------------------------------------------------

describe("execSync options", () => {
  const handlers: ToolHandler[] = [
    new RunBuildHandler(),
    new RunLintHandler(),
    new RunTestsHandler(),
    new RunTypecheckHandler(),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
  });

  handlers.forEach((handler) => {
    it(`${handler.name} passes encoding, timeout, stdio, and maxBuffer to execSync`, async () => {
      mockSuccess("");
      await handler.execute({}, CWD);
      expect(mockExecSync).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          encoding: "utf-8",
          timeout: 300_000,
          stdio: ["pipe", "pipe", "pipe"],
          maxBuffer: 10 * 1024 * 1024,
        })
      );
    });
  });
});

// ---------------------------------------------------------------------------
// createValidationHandlers()
// ---------------------------------------------------------------------------

describe("createValidationHandlers()", () => {
  it("returns a Map", () => {
    const handlers = createValidationHandlers();
    expect(handlers).toBeInstanceOf(Map);
  });

  it("contains exactly 4 handlers", () => {
    const handlers = createValidationHandlers();
    expect(handlers.size).toBe(4);
  });

  it("contains run_build handler", () => {
    const handlers = createValidationHandlers();
    expect(handlers.has("run_build")).toBe(true);
    expect(handlers.get("run_build")).toBeInstanceOf(RunBuildHandler);
  });

  it("contains run_lint handler", () => {
    const handlers = createValidationHandlers();
    expect(handlers.has("run_lint")).toBe(true);
    expect(handlers.get("run_lint")).toBeInstanceOf(RunLintHandler);
  });

  it("contains run_tests handler", () => {
    const handlers = createValidationHandlers();
    expect(handlers.has("run_tests")).toBe(true);
    expect(handlers.get("run_tests")).toBeInstanceOf(RunTestsHandler);
  });

  it("contains run_typecheck handler", () => {
    const handlers = createValidationHandlers();
    expect(handlers.has("run_typecheck")).toBe(true);
    expect(handlers.get("run_typecheck")).toBeInstanceOf(RunTypecheckHandler);
  });

  it("returns a new Map on each call", () => {
    const a = createValidationHandlers();
    const b = createValidationHandlers();
    expect(a).not.toBe(b);
  });

  it("each handler name matches its map key", () => {
    const handlers = createValidationHandlers();
    for (const [key, handler] of handlers) {
      expect(handler.name).toBe(key);
    }
  });

  it("every handler implements the ToolHandler interface (has name and execute)", () => {
    const handlers = createValidationHandlers();
    for (const handler of handlers.values()) {
      expect(typeof handler.name).toBe("string");
      expect(typeof handler.execute).toBe("function");
    }
  });

  it("handlers retrieved from map are callable and return ToolResult shape", async () => {
    vi.clearAllMocks();
    mockSuccess("");
    mockSuccess("");
    mockSuccess("");
    mockSuccess("");

    const handlers = createValidationHandlers();
    for (const handler of handlers.values()) {
      const result = await handler.execute({}, CWD);
      expect(typeof result.success).toBe("boolean");
      expect(typeof result.output).toBe("object");
    }
  });
});
