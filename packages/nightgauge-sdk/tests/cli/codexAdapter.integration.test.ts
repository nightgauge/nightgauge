/**
 * Codex CLI Adapter — end-to-end composition tests (mocked subprocess)
 *
 * Validates the full pipeline flow:
 *   validateAuth() + createQueryFunction() → spawn → JSONL parse → session ID propagation
 *
 * All subprocess calls are mocked — no real Codex CLI required in CI.
 *
 * Unit-level concerns (individual method behavior, token parsing) are covered
 * by codexPreflight.test.ts and codexSessionResume.test.ts. These tests focus
 * on end-to-end composition only.
 *
 * @see https://github.com/nightgauge/nightgauge/issues/2589
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { EventEmitter } from "node:events";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";
import { CodexAdapter } from "../../src/cli/adapters/CodexAdapter.js";

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

/**
 * Create a PreflightCommandRunner that returns configured responses per command key.
 * Unrecognized commands return code 1 with a descriptive stderr.
 */
function createRunner(
  responses: Record<string, { code: number; stdout?: string; stderr?: string }>
): PreflightCommandRunner {
  return async (command, args, _cwd) => {
    const key = `${command} ${args.join(" ")}`;
    const response = responses[key];
    if (!response) {
      return { code: 1, stdout: "", stderr: `unexpected command: ${key}` };
    }
    return {
      code: response.code,
      stdout: response.stdout ?? "",
      stderr: response.stderr ?? "",
    };
  };
}

/** Env vars managed across tests. */
const ENV_VARS = [
  "NIGHTGAUGE_CODEX_CLI_COMMAND",
  "NIGHTGAUGE_CODEX_CLI_ARGS",
  "NIGHTGAUGE_CODEX_MODEL",
  "NIGHTGAUGE_CODEX_EPHEMERAL",
  "NIGHTGAUGE_CODEX_EPHEMERAL_STAGES",
  "NIGHTGAUGE_CODEX_RESUME_ENABLED",
];

// ---------------------------------------------------------------------------
// Integration: validateAuth + createQueryFunction composition
// ---------------------------------------------------------------------------

describe("CodexAdapter — end-to-end composition (mocked subprocess)", () => {
  const adapter = new CodexAdapter();
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

  // -------------------------------------------------------------------------
  // Test 1: Smoke test — adapter validates auth and creates query function
  // -------------------------------------------------------------------------

  it("smoke test: adapter validates auth and creates query function", async () => {
    const runner = createRunner({
      "codex --version": { code: 0, stdout: "codex 0.111.0" },
      "codex login status": { code: 0, stdout: "Logged in" },
    });

    await expect(adapter.validateAuth({ runner })).resolves.toBe("passed");
    expect(typeof adapter.createQueryFunction).toBe("function");
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  // -------------------------------------------------------------------------
  // Test 2: Worktree build — adapter identity signals SDK CLI build needed
  // -------------------------------------------------------------------------

  it("integration: worktree setup — adapter identity signals SDK CLI build is needed", () => {
    // WorktreeManager checks adapter.name !== 'claude' to decide whether to
    // build the SDK CLI in the worktree before executing pipeline stages.
    // This test verifies the identity and capability signals are correct.
    expect(adapter.name).toBe("codex");
    expect(adapter.displayName).toBe("Codex");
    expect(adapter.cliCommand).toBe("codex");

    // Codex participates as a fan-out provider driven by the engine.
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");

    // No direct API key required — Codex uses CLI auth (codex login)
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });

  // -------------------------------------------------------------------------
  // Test 3: Integration — executes a mock stage and parses JSONL output
  // -------------------------------------------------------------------------

  it("integration: codex adapter executes a mock stage and parses JSONL output", async () => {
    const mockJsonlOutput =
      [
        JSON.stringify({ type: "thread.started", thread_id: "thread_abc123" }),
        JSON.stringify({
          type: "item.completed",
          item: { id: "item_0", type: "agent_message", text: "Plan written." },
        }),
        JSON.stringify({
          type: "turn.completed",
          usage: { input_tokens: 500, cached_input_tokens: 120, output_tokens: 150 },
        }),
      ].join("\n") + "\n";

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: mockJsonlOutput }));

    const queryFn = await adapter.createQueryFunction({ stage: "feature-planning" });
    const messages: Array<{ type: string; [key: string]: unknown }> = [];

    for await (const msg of queryFn({ prompt: "Write a plan" })) {
      messages.push(msg);
    }

    // Should emit an assistant text message with parsed agent_message text
    const assistantMsg = messages.find((m) => m.type === "assistant");
    expect(assistantMsg).toBeDefined();
    expect(typeof assistantMsg!.text).toBe("string");

    // session_id must be propagated from thread.started for downstream resume
    const resultMsg = messages.find((m) => m.type === "result");
    expect(resultMsg).toBeDefined();
    expect(resultMsg!.session_id).toBe("thread_abc123");

    // Real Codex token usage flows through the result message (#4027). Codex's
    // input_tokens (500) is cache-inclusive; the cached subset (120) is stored as
    // cache_read_input_tokens and subtracted out of input_tokens (500-120=380) so
    // the pools are disjoint — no longer zeros / estimated.
    expect(resultMsg!.usage).toEqual({
      input_tokens: 380,
      output_tokens: 150,
      cache_read_input_tokens: 120,
      cache_creation_input_tokens: 0,
    });
  });

  // -------------------------------------------------------------------------
  // Test 3b: Sandbox scoping from allowed-tools (#4026)
  // -------------------------------------------------------------------------

  it("scopes the sandbox to read-only for analysis-only allowed-tools (#4026)", async () => {
    spawnMock.mockReturnValue(
      createMockProcess({
        exitCode: 0,
        stdout: '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}\n',
      })
    );

    const queryFn = await adapter.createQueryFunction({ stage: "feature-validate" });
    for await (const _ of queryFn({
      prompt: "analyze",
      options: { allowedTools: ["Read", "Grep", "Glob"] },
    })) {
      // drain
    }

    const args = spawnMock.mock.calls[0][1] as string[];
    expect(args).toContain("--sandbox");
    expect(args).toContain("read-only");
    expect(args).toContain("--ask-for-approval");
    expect(args).toContain("never");
    expect(args).not.toContain("--dangerously-bypass-approvals-and-sandbox");
  });

  it("keeps full access (bypass flag) when allowed-tools include Bash (#4026)", async () => {
    spawnMock.mockReturnValue(
      createMockProcess({
        exitCode: 0,
        stdout: '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}\n',
      })
    );

    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({
      prompt: "build",
      options: { allowedTools: ["Read", "Edit", "Bash"] },
    })) {
      // drain
    }

    const args = spawnMock.mock.calls[0][1] as string[];
    expect(args).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(args).not.toContain("--sandbox");
  });

  // -------------------------------------------------------------------------
  // Test 4: Integration — session resume uses exec resume <threadId> args
  // -------------------------------------------------------------------------

  it("integration: session resume uses exec resume <threadId> args when env var is set", async () => {
    process.env.NIGHTGAUGE_CODEX_RESUME_ENABLED = "true";

    const mockJsonlOutput =
      [
        JSON.stringify({ type: "thread.started", thread_id: "thread_resume_456" }),
        JSON.stringify({
          type: "item.completed",
          item: { id: "item_0", type: "agent_message", text: "Done." },
        }),
        JSON.stringify({
          type: "turn.completed",
          usage: { input_tokens: 300, output_tokens: 100 },
        }),
      ].join("\n") + "\n";

    spawnMock.mockReturnValue(createMockProcess({ exitCode: 0, stdout: mockJsonlOutput }));

    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });

    for await (const _ of queryFn({
      prompt: "Implement feature",
      options: { resumeSessionId: "thread_resume_456" },
    })) {
      // drain
    }

    const spawnCall = spawnMock.mock.calls[0];
    const args = spawnCall[1] as string[];
    expect(args[0]).toBe("exec");
    expect(args[1]).toBe("resume");
    expect(args[2]).toBe("thread_resume_456");
    expect(args[3]).toBe("-"); // stdin marker
    expect(args).not.toContain("--full-auto"); // deprecated flag removed (#4020)
    expect(args).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(args).toContain("--json");
    // --sandbox should NOT appear on resume path
    expect(args).not.toContain("--sandbox");
    expect(args).not.toContain("danger-full-access");
  });

  // -------------------------------------------------------------------------
  // Test 5: Error path — missing codex binary gives clear error
  // -------------------------------------------------------------------------

  it("error path: missing codex binary throws with recognizable error message", async () => {
    const runner = createRunner({
      "codex --version": { code: 127, stderr: "codex: command not found" },
    });

    await expect(adapter.validateAuth({ runner })).rejects.toThrow(/codex/i);
  });

  // -------------------------------------------------------------------------
  // Test 6: Error path — spawn exits non-zero (SDK CLI build failure scenario)
  // -------------------------------------------------------------------------

  it("error path: SDK CLI build failure — spawn exits non-zero gives clear error", async () => {
    spawnMock.mockReturnValue(
      createMockProcess({
        exitCode: 1,
        stderr: "npm ERR! Build failed with errors",
      })
    );

    const queryFn = await adapter.createQueryFunction({ stage: "feature-planning" });

    await expect(async () => {
      for await (const _ of queryFn({ prompt: "test" })) {
        // drain
      }
    }).rejects.toThrow(/codex runner command failed/i);
  });

  // -------------------------------------------------------------------------
  // Test 7: Error path — Codex CLI exits non-zero, error is parsed and reported
  // -------------------------------------------------------------------------

  it("error path: codex CLI exits non-zero — error is parsed and reported", async () => {
    spawnMock.mockReturnValue(
      createMockProcess({
        exitCode: 1,
        stderr: "Error: request failed with status 401 Unauthorized",
      })
    );

    const queryFn = await adapter.createQueryFunction({ stage: "feature-planning" });

    await expect(async () => {
      for await (const _ of queryFn({ prompt: "test" })) {
        // drain
      }
    }).rejects.toThrow(/codex runner command failed/i);
  });

  // -------------------------------------------------------------------------
  // Test 8: NIGHTGAUGE_CODEX_CLI_COMMAND override
  // -------------------------------------------------------------------------

  it("uses NIGHTGAUGE_CODEX_CLI_COMMAND override as spawned command", async () => {
    process.env.NIGHTGAUGE_CODEX_CLI_COMMAND = "codex-beta";

    spawnMock.mockReturnValue(
      createMockProcess({
        exitCode: 0,
        stdout: JSON.stringify({ type: "turn.completed" }) + "\n",
      })
    );

    const queryFn = await adapter.createQueryFunction({ stage: "feature-planning" });

    for await (const _ of queryFn({ prompt: "test" })) {
      // drain
    }

    expect(spawnMock).toHaveBeenCalledWith("codex-beta", expect.any(Array), expect.any(Object));
  });
});
