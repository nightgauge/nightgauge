/**
 * CopilotCliAdapter — integration tests (mocked subprocess)
 *
 * Tests the composed flow: validateAuth() + createQueryFunction() producing
 * a query function that processes output with cost tracking. Each test mocks
 * node:child_process.spawn so no real `copilot` binary is invoked.
 *
 * Unit-level concerns (individual method behavior, token parsing) are covered
 * by copilotAdapter.test.ts. These tests focus on end-to-end composition only.
 *
 * @see Issue #1948 - Document Copilot CLI adapter configuration and add test coverage
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { EventEmitter } from "node:events";
import { CopilotCliAdapter } from "../../src/cli/adapters/CopilotCliAdapter.js";
import { COPILOT_PREMIUM_REQUEST_COST_USD } from "../../src/cli/adapterQuery.js";

// ---------------------------------------------------------------------------
// Mock node:child_process at module level — no real subprocess is ever spawned
// ---------------------------------------------------------------------------

vi.mock("node:child_process", () => ({
  spawn: vi.fn(),
}));

import { spawn } from "node:child_process";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a fake ChildProcess EventEmitter with writable stdin and readable stdout/stderr. */
function createMockProcess(options: {
  exitCode?: number;
  stdout?: string;
  stderr?: string;
}): ReturnType<typeof spawn> {
  const proc = new EventEmitter() as ReturnType<typeof spawn>;

  proc.stdout = new EventEmitter() as ReturnType<typeof spawn>["stdout"];
  proc.stderr = new EventEmitter() as ReturnType<typeof spawn>["stderr"];
  proc.stdin = {
    write: vi.fn(),
    end: vi.fn(),
  } as unknown as ReturnType<typeof spawn>["stdin"];

  // Emit data and close asynchronously so callers can attach listeners first
  setImmediate(() => {
    if (options.stdout) {
      proc.stdout!.emit("data", Buffer.from(options.stdout));
    }
    if (options.stderr) {
      proc.stderr!.emit("data", Buffer.from(options.stderr));
    }
    proc.emit("close", options.exitCode ?? 0);
  });

  return proc;
}

/** Env vars managed across tests. */
const ENV_VARS = [
  "GH_TOKEN",
  "GITHUB_TOKEN",
  "COPILOT_GITHUB_TOKEN",
  "NIGHTGAUGE_COPILOT_CLI_COMMAND",
  "NIGHTGAUGE_COPILOT_CLI_ARGS",
  "NIGHTGAUGE_COPILOT_MODEL",
];

// ---------------------------------------------------------------------------
// Integration: validateAuth + createQueryFunction composition
// ---------------------------------------------------------------------------

describe("CopilotCliAdapter — end-to-end composition (mocked subprocess)", () => {
  const adapter = new CopilotCliAdapter();
  const spawnMock = vi.mocked(spawn);

  beforeEach(() => {
    vi.clearAllMocks();
    for (const key of ENV_VARS) {
      delete process.env[key];
    }
  });

  afterEach(() => {
    for (const key of ENV_VARS) {
      delete process.env[key];
    }
  });

  it("composes validateAuth + createQueryFunction for a full stage run with a stats footer", async () => {
    // Auth passes via GH_TOKEN (no CLI subprocess needed for auth)
    process.env.GH_TOKEN = "ghp_test-token";

    const copilotOutput = `Stage completed successfully.

Session ID: 221b5571-3998-47e1-b57a-552cf9078947
Duration: 42s
Usage: Total usage est: 3 Premium requests
Total code changes: 8 lines added, 2 lines removed`;

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: copilotOutput }));

    const queryFn = await adapter.createQueryFunction();
    const messages: Array<{ type: string; [key: string]: unknown }> = [];

    for await (const msg of queryFn({ prompt: "Run feature-dev stage" })) {
      messages.push(msg);
    }

    // Should emit an assistant text message and a result message
    const assistantMsg = messages.find((m) => m.type === "assistant");
    const resultMsg = messages.find((m) => m.type === "result");

    expect(assistantMsg).toBeDefined();
    // Footer stripped — only the agent response text is surfaced.
    expect(assistantMsg!.text).toBe("Stage completed successfully.");

    expect(resultMsg).toBeDefined();
    const usage = resultMsg!.usage as Record<string, unknown>;
    expect(usage).toBeDefined();
    expect(usage.premium_requests).toBe(3);
    // Copilot reports no token counts — honest zeros.
    expect(usage.input_tokens).toBe(0);
    expect(usage.output_tokens).toBe(0);
    // Real accounting: 3 premium requests, not a flat 1-per-invocation guess.
    expect(resultMsg!.total_cost_usd).toBeCloseTo(3 * COPILOT_PREMIUM_REQUEST_COST_USD, 10);
    // Session id propagated for resume/attribution.
    expect(resultMsg!.session_id).toBe("221b5571-3998-47e1-b57a-552cf9078947");
  });

  it("attributes the requested NIGHTGAUGE_COPILOT_MODEL as the served model", async () => {
    process.env.GH_TOKEN = "ghp_test-token";
    process.env.NIGHTGAUGE_COPILOT_MODEL = "claude-sonnet-4.5";

    const copilotOutput = `Task complete.

Session ID: abc-123
Usage: Total usage est: 1 Premium requests`;

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: copilotOutput }));

    const queryFn = await adapter.createQueryFunction();
    const messages: Array<{ type: string; [key: string]: unknown }> = [];

    for await (const msg of queryFn({ prompt: "Run feature-planning stage" })) {
      messages.push(msg);
    }

    const resultMsg = messages.find((m) => m.type === "result");
    expect(resultMsg).toBeDefined();
    const usage = resultMsg!.usage as Record<string, unknown>;
    expect(usage.premium_requests).toBe(1);
    expect(usage.model).toBe("claude-sonnet-4.5");
    expect(resultMsg!.model).toBe("claude-sonnet-4.5");
  });

  it("reports zero cost when the output carries no stats footer (unobserved)", async () => {
    process.env.GH_TOKEN = "ghp_test-token";

    // No footer → usage undefined → cost 0 (no fabricated premium request).
    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: "Done." }));

    const queryFn = await adapter.createQueryFunction();
    const messages: Array<{ type: string; [key: string]: unknown }> = [];

    for await (const msg of queryFn({ prompt: "Minimal prompt" })) {
      messages.push(msg);
    }

    const resultMsg = messages.find((m) => m.type === "result");
    expect(resultMsg).toBeDefined();
    expect(resultMsg!.total_cost_usd).toBe(0);
  });

  it("throws on auth failure propagated from CLI subprocess exit code", async () => {
    process.env.GH_TOKEN = "ghp_test-token";

    spawnMock.mockReturnValue(
      createMockProcess({
        exitCode: 1,
        stderr: "copilot: authentication required",
      })
    );

    const queryFn = await adapter.createQueryFunction();

    await expect(
      (async () => {
        for await (const _ of queryFn({ prompt: "test" })) {
          // drain
        }
      })()
    ).rejects.toThrow(/copilot runner command failed/);
  });

  it("throws on stage failure signal in output (exit code 0 but explicit failure)", async () => {
    process.env.GH_TOKEN = "ghp_test-token";

    spawnMock.mockReturnValue(
      createMockProcess({
        exitCode: 0,
        stdout: "execution halted: cannot proceed with missing context",
      })
    );

    const queryFn = await adapter.createQueryFunction();

    await expect(
      (async () => {
        for await (const _ of queryFn({ prompt: "test" })) {
          // drain
        }
      })()
    ).rejects.toThrow(/copilot runner reported stage failure/);
  });

  it("uses NIGHTGAUGE_COPILOT_CLI_COMMAND override as spawned command", async () => {
    process.env.GH_TOKEN = "ghp_test-token";
    process.env.NIGHTGAUGE_COPILOT_CLI_COMMAND = "gh-copilot";

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: "Done." }));

    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test" })) {
      // drain
    }

    expect(spawnMock).toHaveBeenCalledWith("gh-copilot", expect.any(Array), expect.any(Object));
  });

  it('uses default "copilot" command when NIGHTGAUGE_COPILOT_CLI_COMMAND is not set', async () => {
    process.env.GH_TOKEN = "ghp_test-token";

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: "Done." }));

    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test" })) {
      // drain
    }

    expect(spawnMock).toHaveBeenCalledWith("copilot", expect.any(Array), expect.any(Object));
  });

  it("includes --allow-all-tools in default args passed to spawned process", async () => {
    process.env.GH_TOKEN = "ghp_test-token";

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: "Done." }));

    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test" })) {
      // drain
    }

    expect(spawnMock).toHaveBeenCalledWith(
      expect.any(String),
      expect.arrayContaining(["--allow-all-tools"]),
      expect.any(Object)
    );
  });

  it("forwards a resolved --model when NIGHTGAUGE_COPILOT_MODEL is set (#52)", async () => {
    process.env.GH_TOKEN = "ghp_test-token";
    // A bare routing tier must resolve to a concrete copilot-hosted id.
    process.env.NIGHTGAUGE_COPILOT_MODEL = "sonnet";

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: "Done." }));

    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test" })) {
      // drain
    }

    const spawnArgs = spawnMock.mock.calls[0][1] as string[];
    const modelIdx = spawnArgs.indexOf("--model");
    expect(modelIdx).toBeGreaterThanOrEqual(0);
    expect(spawnArgs[modelIdx + 1]).toBe("gpt-4o"); // registry copilot sonnet band
    expect(spawnArgs).not.toContain("sonnet"); // raw tier never reaches --model
  });

  it("omits --model when NIGHTGAUGE_COPILOT_MODEL is unset (CLI default)", async () => {
    process.env.GH_TOKEN = "ghp_test-token";

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: "Done." }));

    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test" })) {
      // drain
    }

    const spawnArgs = spawnMock.mock.calls[0][1] as string[];
    expect(spawnArgs).not.toContain("--model");
  });
});
