/**
 * Codex Session Resume tests
 *
 * Covers Issue #1659: Integrate Codex session resume with git context preservation.
 *
 * Tests:
 * - summarizeCodexJsonOutput extracts session ID from thread.started event
 * - summarizeCodexJsonOutput returns undefined when no thread.started event
 * - createCliQueryFn builds exec resume args when resumeSessionId + env var set
 * - createCliQueryFn builds --last fallback args when no ID but env var set
 * - createCliQueryFn uses standard exec args when env var is not set
 * - Resume command uses --dangerously-bypass-approvals-and-sandbox (not --sandbox)
 * - Fixture file has thread.started event and it is parsed correctly
 * - session_id is propagated in result messages from Codex runs
 */

import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import * as path from "node:path";
import * as fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { summarizeCodexJsonOutput } from "../../src/cli/adapterQuery.js";
import { createCliQueryFn } from "../../src/cli/adapters/cliQueryHelper.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Mock node:child_process at the module level so that createCliQueryFn's
// internal call to runCliCommand (which uses spawn) is intercepted.
// In ESM mode, vi.spyOn on re-exported functions does not intercept
// internal module-scope calls; mocking the underlying dependency does.
vi.mock("node:child_process", () => ({ spawn: vi.fn() }));

const spawnMock = vi.mocked(spawn);

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function threadStartedLine(threadId: string): string {
  return JSON.stringify({ type: "thread.started", thread_id: threadId });
}

function agentMessageLine(text: string): string {
  return JSON.stringify({
    type: "item.completed",
    item: { id: "item_1", type: "agent_message", text },
  });
}

function turnCompletedLine(): string {
  return JSON.stringify({ type: "turn.completed" });
}

/** Create a fake child process that emits stdout data and closes. */
function makeChildProcess(stdout: string, exitCode = 0) {
  const { EventEmitter } = require("node:events");
  const child = new EventEmitter();
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.stdin = { write: vi.fn(), end: vi.fn() };
  process.nextTick(() => {
    child.stdout.emit("data", Buffer.from(stdout));
    child.emit("close", exitCode);
  });
  return child;
}

/** Configure spawnMock to return a successful child process with given stdout. */
function mockSpawnReturning(stdout: string) {
  spawnMock.mockImplementation(() => makeChildProcess(stdout) as any);
}

/** Capture the args passed to spawn from the most recent call. */
function lastSpawnArgs(): string[] {
  const calls = spawnMock.mock.calls;
  if (calls.length === 0) return [];
  return calls[calls.length - 1][1] as string[];
}

// ---------------------------------------------------------------------------
// summarizeCodexJsonOutput — session ID extraction
// ---------------------------------------------------------------------------

describe("summarizeCodexJsonOutput — session ID extraction (Issue #1659)", () => {
  it("extracts thread_id from thread.started event", () => {
    const output = [
      threadStartedLine("test-thread-uuid-1234"),
      agentMessageLine("Stage complete."),
      turnCompletedLine(),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.sessionId).toBe("test-thread-uuid-1234");
    expect(summary.hasExplicitFailure).toBe(false);
  });

  it("returns undefined sessionId when no thread.started event is present", () => {
    const output = [
      agentMessageLine("Stage complete with no thread event."),
      turnCompletedLine(),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.sessionId).toBeUndefined();
  });

  it("returns undefined sessionId for empty output", () => {
    expect(summarizeCodexJsonOutput("").sessionId).toBeUndefined();
  });

  it("ignores thread.started events where thread_id is not a string", () => {
    const output = [
      JSON.stringify({ type: "thread.started", thread_id: 42 }),
      agentMessageLine("Stage complete."),
      turnCompletedLine(),
    ].join("\n");

    expect(summarizeCodexJsonOutput(output).sessionId).toBeUndefined();
  });

  it("ignores thread.started events where thread_id is absent", () => {
    const output = [
      JSON.stringify({ type: "thread.started" }),
      agentMessageLine("Stage complete."),
      turnCompletedLine(),
    ].join("\n");

    expect(summarizeCodexJsonOutput(output).sessionId).toBeUndefined();
  });

  it("does not affect hasExplicitFailure or displayText", () => {
    const output = [
      threadStartedLine("some-thread-id"),
      agentMessageLine("All tests passing. Context file written."),
      turnCompletedLine(),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toBe("All tests passing. Context file written.");
    expect(summary.sessionId).toBe("some-thread-id");
  });

  it("correctly parses real Codex 0.98.0 output format (thread.started before items)", () => {
    const output = [
      JSON.stringify({
        type: "thread.started",
        thread_id: "019ce5bb-2787-71d0-b801-f5627ff25e35",
      }),
      JSON.stringify({ type: "turn.started" }),
      JSON.stringify({
        type: "item.completed",
        item: { id: "item_0", type: "agent_message", text: "hi" },
      }),
      JSON.stringify({
        type: "turn.completed",
        usage: {
          input_tokens: 13246,
          cached_input_tokens: 7296,
          output_tokens: 5,
        },
      }),
    ].join("\n");

    const summary = summarizeCodexJsonOutput(output);

    expect(summary.sessionId).toBe("019ce5bb-2787-71d0-b801-f5627ff25e35");
    expect(summary.hasExplicitFailure).toBe(false);
  });

  it("extracts thread_id from success fixture", async () => {
    const fixture = await fs.readFile(
      path.join(__dirname, "fixtures", "codex-jsonl-success.txt"),
      "utf-8"
    );

    const summary = summarizeCodexJsonOutput(fixture);

    expect(summary.sessionId).toBe("019ce5bb-2787-71d0-b801-f5627ff25e35");
    expect(summary.hasExplicitFailure).toBe(false);
    expect(summary.displayText).toContain(
      "All tests passing. Photo upload feature implemented and context file written successfully."
    );
  });
});

// ---------------------------------------------------------------------------
// createCliQueryFn — resume arg construction (via spawn mock)
// ---------------------------------------------------------------------------

describe("createCliQueryFn — resume arg construction (Issue #1659)", () => {
  const STANDARD_ARGS = ["exec", "--dangerously-bypass-approvals-and-sandbox", "--json"];

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  function makeSuccessStdout(threadId?: string): string {
    const lines: string[] = [];
    if (threadId) lines.push(threadStartedLine(threadId));
    lines.push(agentMessageLine("Stage complete."));
    lines.push(turnCompletedLine());
    return lines.join("\n");
  }

  it("builds exec resume args with session ID when env var is set and resumeSessionId provided", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "true");
    mockSpawnReturning(makeSuccessStdout("new-thread-id"));

    const queryFn = createCliQueryFn({
      command: "codex",
      args: STANDARD_ARGS,
      adapter: "codex",
    });
    for await (const _ of queryFn({
      prompt: "test",
      options: { resumeSessionId: "prior-thread-id" },
    })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    expect(args[0]).toBe("exec");
    expect(args[1]).toBe("resume");
    expect(args[2]).toBe("prior-thread-id");
    expect(args[3]).toBe("-"); // stdin marker
    expect(args).not.toContain("--full-auto"); // deprecated flag removed (#4020)
    expect(args).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(args).toContain("--json");
    expect(args).not.toContain("--sandbox");
  });

  it("builds exec resume --last args when env var is set but no resumeSessionId", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "true");
    mockSpawnReturning(makeSuccessStdout());

    const queryFn = createCliQueryFn({
      command: "codex",
      args: STANDARD_ARGS,
      adapter: "codex",
    });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    expect(args[0]).toBe("exec");
    expect(args[1]).toBe("resume");
    expect(args[2]).toBe("--last");
    expect(args[3]).toBe("-"); // stdin marker
    expect(args).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(args).not.toContain("--sandbox");
  });

  it('uses standard exec args when NIGHTGAUGE_CODEX_RESUME_ENABLED is not "true"', async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const queryFn = createCliQueryFn({
      command: "codex",
      args: STANDARD_ARGS,
      adapter: "codex",
    });
    for await (const _ of queryFn({
      prompt: "test",
      options: { resumeSessionId: "some-session-id" },
    })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    // Standard path includes --output-last-message <tmpPath> appended by createCliQueryFn
    expect(args.slice(0, STANDARD_ARGS.length)).toEqual(STANDARD_ARGS);
    expect(args).not.toContain("resume");
  });

  it("propagates session_id in result message when thread.started is in output", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout("captured-thread-id"));

    const queryFn = createCliQueryFn({
      command: "codex",
      args: STANDARD_ARGS,
      adapter: "codex",
    });
    const messages = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      messages.push(msg);
    }

    const resultMsg = messages.find((m) => m.type === "result");
    expect(resultMsg).toBeDefined();
    expect((resultMsg as Record<string, unknown>).session_id).toBe("captured-thread-id");
  });

  it("does not include session_id in result when no thread.started in output", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout()); // no threadId

    const queryFn = createCliQueryFn({
      command: "codex",
      args: STANDARD_ARGS,
      adapter: "codex",
    });
    const messages = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      messages.push(msg);
    }

    const resultMsg = messages.find((m) => m.type === "result");
    expect(resultMsg).toBeDefined();
    expect((resultMsg as Record<string, unknown>).session_id).toBeUndefined();
  });

  it("uses --dangerously-bypass-approvals-and-sandbox and NOT --sandbox on exec resume", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "true");
    mockSpawnReturning(makeSuccessStdout());

    const queryFn = createCliQueryFn({
      command: "codex",
      args: STANDARD_ARGS,
      adapter: "codex",
    });
    for await (const _ of queryFn({
      prompt: "test",
      options: { resumeSessionId: "abc-session" },
    })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    expect(args).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(args).not.toContain("--sandbox");
    expect(args).not.toContain("danger-full-access");
  });
});
