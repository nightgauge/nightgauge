/**
 * IpcClient.protocol.test.ts — Phase 3: IPC protocol tests
 *
 * Tests the real JSON-over-stdio wire format and request-routing logic of
 * IpcClient without spawning an actual Go binary. A mock child process with
 * controllable stdin/stdout lets each test send JSON lines and assert on
 * what gets written to stdin.
 *
 * Coverage:
 *  - Request format: newline-terminated JSON with id / method / params
 *  - Request ID increment across calls
 *  - Response routing: correct promise resolved/rejected by ID
 *  - Out-of-order response handling (ID-based routing, not FIFO)
 *  - Error response → rejected promise with "IPC error {code}: {msg}"
 *  - Timeout → rejected promise after configured milliseconds
 *  - Unsolicited event dispatch via on() subscriptions
 *  - dispose() → pending requests rejected, process killed
 *  - GITHUB_TOKEN resolution: env var priority over gh auth token
 *  - Process exit → exponential-backoff restart (up to maxRestartAttempts)
 *
 * @see internal/ipc/protocol.go — Go-side protocol definition
 * @see src/services/IpcClient.ts — implementation under test
 * @see docs/GO_BINARY.md — IPC architecture
 */

import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from "vitest";
import { EventEmitter } from "events";

// ─── Mock setup ──────────────────────────────────────────────────────────────
//
// These mocks are hoisted by vitest before any imports so they intercept the
// static imports inside IpcClient.ts.

// Shared mock readline interface: emit('line', json) simulates Go output.
// Increase maxListeners to suppress warning from many test subscriptions.
const mockRl = new EventEmitter() as any;
mockRl.setMaxListeners(50);
mockRl.close = vi.fn();

// Module-level process ref updated by each spawn() call.
let mockProc: ReturnType<typeof makeMockProcess>;
let capturedStdinWrites: string[];

function makeMockProcess() {
  capturedStdinWrites = [];
  const proc = new EventEmitter() as any;
  proc.stdin = {
    writable: true,
    write: vi.fn((data: string, cb?: (err?: Error | null) => void) => {
      capturedStdinWrites.push(data);
      cb?.();
      return true;
    }),
  };
  proc.stdout = new EventEmitter();
  proc.stderr = new EventEmitter();
  proc.killed = false;
  proc.pid = 1234;
  proc.kill = vi.fn((signal?: string) => {
    proc.killed = true;
    // Defer exit event so dispose() can complete before handlers run.
    setImmediate(() => proc.emit("exit", signal === "SIGTERM" ? 0 : 1));
  });
  return proc;
}

vi.mock("child_process", () => ({
  spawn: vi.fn(() => {
    mockProc = makeMockProcess();
    return mockProc;
  }),
  exec: vi.fn(
    (
      _cmd: string,
      _opts: object,
      cb?: (err: Error | null, stdout: string, stderr: string) => void
    ) => {
      // Default: succeed with a fallback token.
      // Override per-test with vi.mocked(exec).mockImplementation().
      cb?.(null, "ghp_test_fallback_token\n", "");
    }
  ),
}));

vi.mock("readline", () => ({
  createInterface: vi.fn(() => mockRl),
}));

vi.mock("fs", () => ({
  // Return true for all existsSync calls — this test file exercises IPC protocol,
  // not filesystem behavior. The fake binary path (/fake/nightgauge) must pass
  // the existsSync check in resolveBinaryPath() including during auto-restart.
  existsSync: vi.fn(() => true),
}));

// ─── Import under test ───────────────────────────────────────────────────────

import * as vscode from "vscode";
import { IpcClient } from "../../src/services/IpcClient";

// ─── Test helpers ─────────────────────────────────────────────────────────────

/**
 * Emit a JSON line synchronously from the mock readline interface.
 * This triggers IpcClient.handleLine() inline — no timers needed.
 */
function simulateResponse(response: object): void {
  mockRl.emit("line", JSON.stringify(response));
}

/**
 * Flush the microtask queue (Promise callbacks) without advancing fake timers.
 * Use instead of vi.runAllTimersAsync() in tests that don't want to advance time.
 */
async function flushPromises(depth = 4): Promise<void> {
  for (let i = 0; i < depth; i++) {
    await Promise.resolve();
  }
}

/**
 * Start IpcClient with fake binary path. GITHUB_TOKEN must be set before
 * calling (or absent if testing the fallback path).
 */
async function startClient(): Promise<void> {
  process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
  await IpcClient.getInstance().start();
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("IpcClient — IPC protocol", () => {
  beforeEach(async () => {
    IpcClient.resetInstance();
    vi.clearAllMocks();
    mockRl.removeAllListeners();

    // vi.clearAllMocks() resets mock implementations set in vi.mock() factories.
    // Restore existsSync to return true for the fake binary path used in tests.
    const fs = await import("fs");
    vi.mocked(fs.existsSync).mockReturnValue(true);

    // Ensure vscode.workspace.getConfiguration returns sensible defaults.
    (vscode.workspace.getConfiguration as MockInstance).mockReturnValue({
      get: vi.fn(<T>(key: string, defaultValue?: T): T | undefined => {
        if (key === "binaryPath") return "" as unknown as T;
        if (key === "timeoutSeconds") return 30 as unknown as T;
        return defaultValue;
      }),
    });

    // showErrorMessage and showWarningMessage must return a Thenable —
    // IpcClient calls .then() on the results.
    (vscode.window.showErrorMessage as MockInstance).mockResolvedValue(undefined);
    (vscode.window.showWarningMessage as MockInstance).mockResolvedValue(undefined);

    // Default: GITHUB_TOKEN is set so token resolution short-circuits.
    process.env.GITHUB_TOKEN = "test_token";

    vi.useFakeTimers();
  });

  afterEach(() => {
    IpcClient.resetInstance();
    delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
    delete process.env.GITHUB_TOKEN;
    vi.useRealTimers();
  });

  // ── Request format ─────────────────────────────────────────────────────────

  describe("request format", () => {
    it("serialises each request as newline-terminated JSON on stdin", async () => {
      await startClient();

      const callPromise = IpcClient.getInstance().call("board.list", {
        owner: "nightgauge",
        projectNumber: 5,
      });

      // Respond synchronously before the timeout timer fires.
      simulateResponse({ id: 1, result: [] });
      await flushPromises();
      await callPromise;

      expect(capturedStdinWrites).toHaveLength(1);
      const raw = capturedStdinWrites[0];
      expect(raw).toMatch(/\n$/);

      const parsed = JSON.parse(raw.trimEnd());
      expect(parsed).toMatchObject({
        id: expect.any(Number),
        method: "board.list",
        params: { owner: "nightgauge", projectNumber: 5 },
      });
    });

    it("omits the params key when no params are provided", async () => {
      await startClient();

      const callPromise = IpcClient.getInstance().call("execution.list");
      simulateResponse({ id: 1, result: [] });
      await flushPromises();
      await callPromise;

      const parsed = JSON.parse(capturedStdinWrites[0].trimEnd());
      expect(parsed).not.toHaveProperty("params");
    });

    it("increments the request ID for each successive call", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p1 = client.call("execution.list");
      simulateResponse({ id: 1, result: [] });
      await flushPromises();
      await p1;

      const p2 = client.call("execution.list");
      simulateResponse({ id: 2, result: [] });
      await flushPromises();
      await p2;

      const id1 = JSON.parse(capturedStdinWrites[0].trimEnd()).id;
      const id2 = JSON.parse(capturedStdinWrites[1].trimEnd()).id;
      expect(id2).toBe(id1 + 1);
    });
  });

  // ── Response routing ───────────────────────────────────────────────────────

  describe("response routing", () => {
    it("resolves the promise whose id matches the response id", async () => {
      await startClient();
      const callPromise = IpcClient.getInstance().call<{ stage: string }>("pipeline.status", {
        owner: "nightgauge",
        projectNumber: 5,
        itemId: "PVTI_abc",
      });

      simulateResponse({ id: 1, result: { stage: "feature-dev" } });
      await flushPromises();

      const result = await callPromise;
      expect(result).toEqual({ stage: "feature-dev" });
    });

    it("handles out-of-order responses — routes each by ID, not arrival order", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      // Fire two requests without awaiting either.
      const p1 = client.call<string>("test.a");
      const p2 = client.call<string>("test.b");

      // Respond to the SECOND request first (higher ID), then the first.
      simulateResponse({ id: 2, result: "response-b" });
      simulateResponse({ id: 1, result: "response-a" });
      await flushPromises();

      const [r1, r2] = await Promise.all([p1, p2]);
      expect(r1).toBe("response-a");
      expect(r2).toBe("response-b");
    });

    it("ignores responses with an unknown id", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.call<string>("test.known");
      simulateResponse({ id: 999, result: "orphan" }); // no pending request
      simulateResponse({ id: 1, result: "expected" });
      await flushPromises();

      const result = await p;
      expect(result).toBe("expected");
    });

    it("passes through null result values", async () => {
      await startClient();
      const p = IpcClient.getInstance().call("board.updateStatus", {
        owner: "nightgauge",
        projectNumber: 5,
        itemId: "x",
        status: "Done",
      });

      simulateResponse({ id: 1, result: null });
      await flushPromises();

      const result = await p;
      expect(result).toBeNull();
    });
  });

  // ── Error responses ────────────────────────────────────────────────────────

  describe("error responses", () => {
    it('rejects the promise with "IPC error {code}: {message}"', async () => {
      await startClient();
      const p = IpcClient.getInstance().call("issue.view", {
        owner: "nightgauge",
        repo: "nightgauge",
        number: 9999,
      });

      simulateResponse({
        id: 1,
        error: { code: -32603, message: "not found" },
      });
      await flushPromises();

      await expect(p).rejects.toThrow("IPC error -32603: not found");
    });

    it("rejects with ErrMethodNotFound (-32601) for unknown methods", async () => {
      await startClient();
      const p = IpcClient.getInstance().call("method.does.not.exist");

      simulateResponse({
        id: 1,
        error: {
          code: -32601,
          message: "unknown method: method.does.not.exist",
        },
      });
      await flushPromises();

      await expect(p).rejects.toThrow("IPC error -32601:");
    });

    it("rejects with ErrInvalidParams (-32602) when params are malformed", async () => {
      await startClient();
      const p = IpcClient.getInstance().call("board.list", { bad: true });

      simulateResponse({
        id: 1,
        error: { code: -32602, message: "invalid params: missing owner" },
      });
      await flushPromises();

      await expect(p).rejects.toThrow("IPC error -32602:");
    });

    it("does not resolve other pending requests when one receives an error", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const pOk = client.call<string>("test.ok");
      const pErr = client.call<string>("test.err");

      // Respond to pErr (id=2) with an error, then pOk (id=1) with a result.
      simulateResponse({ id: 2, error: { code: -32603, message: "fail" } });
      simulateResponse({ id: 1, result: "ok" });
      await flushPromises();

      const [okResult] = await Promise.all([pOk, expect(pErr).rejects.toThrow()]);
      expect(okResult).toBe("ok");
    });
  });

  // ── Timeout ────────────────────────────────────────────────────────────────

  describe("timeout", () => {
    it("rejects the promise after the configured timeout (default 30s)", async () => {
      await startClient();
      const p = IpcClient.getInstance().call("board.list", {
        owner: "nightgauge",
        projectNumber: 5,
      });

      // Advance past the 30 s default timeout — no response sent.
      vi.advanceTimersByTime(31_000);
      await flushPromises();

      await expect(p).rejects.toThrow(/timed out/);
    });

    it("does not reject the promise before the timeout elapses", async () => {
      await startClient();
      const p = IpcClient.getInstance().call("board.list", {
        owner: "nightgauge",
        projectNumber: 5,
      });

      // Advance to just before the 30 s threshold.
      vi.advanceTimersByTime(29_000);
      await flushPromises();

      // Respond before the remaining 1 s elapses — promise should resolve.
      simulateResponse({ id: 1, result: [] });
      await flushPromises();

      await expect(p).resolves.toEqual([]);
    });

    it("removes the pending request entry after timeout fires", async () => {
      await startClient();
      const p = IpcClient.getInstance().call("board.list", {});

      vi.advanceTimersByTime(31_000);
      await flushPromises();
      await expect(p).rejects.toThrow();

      // A late response for the timed-out id should be silently ignored.
      expect(() => mockRl.emit("line", JSON.stringify({ id: 1, result: [] }))).not.toThrow();
    });
  });

  // ── Event handling ─────────────────────────────────────────────────────────

  describe("event handling", () => {
    it("dispatches unsolicited events to registered handlers", async () => {
      await startClient();
      const handler = vi.fn();
      IpcClient.getInstance().on("stage.complete", handler);

      simulateResponse({ event: "stage.complete", data: { issue: 42 } });
      await flushPromises();

      expect(handler).toHaveBeenCalledOnce();
      expect(handler).toHaveBeenCalledWith({ issue: 42 });
    });

    it("does not invoke handlers for a different event type", async () => {
      await startClient();
      const handler = vi.fn();
      IpcClient.getInstance().on("stage.complete", handler);

      simulateResponse({ event: "pipeline.started", data: {} });
      await flushPromises();

      expect(handler).not.toHaveBeenCalled();
    });

    it("supports multiple handlers for the same event", async () => {
      await startClient();
      const h1 = vi.fn();
      const h2 = vi.fn();
      const client = IpcClient.getInstance();
      client.on("stage.complete", h1);
      client.on("stage.complete", h2);

      simulateResponse({ event: "stage.complete", data: null });
      await flushPromises();

      expect(h1).toHaveBeenCalledOnce();
      expect(h2).toHaveBeenCalledOnce();
    });

    it("allows handlers to be disposed individually", async () => {
      await startClient();
      const handler = vi.fn();
      const sub = IpcClient.getInstance().on("stage.complete", handler);

      sub.dispose();
      simulateResponse({ event: "stage.complete", data: {} });
      await flushPromises();

      expect(handler).not.toHaveBeenCalled();
    });

    it('routes by presence of "event" key — events are not routed as responses', async () => {
      await startClient();
      const p = IpcClient.getInstance().call<string>("test.call");

      // Message has 'event' key → handled as event, not response for id=1.
      simulateResponse({ event: "stage.complete", id: 1, data: {} });
      await flushPromises();

      // The promise for id=1 must still be pending (not resolved by the event).
      let settled = false;
      void p.then(
        () => {
          settled = true;
        },
        () => {
          settled = true;
        }
      );
      await flushPromises();
      expect(settled).toBe(false);

      // Resolve it properly.
      simulateResponse({ id: 1, result: "done" });
      await flushPromises();

      await expect(p).resolves.toBe("done");
    });
  });

  // ── dispose ────────────────────────────────────────────────────────────────

  describe("dispose", () => {
    it('rejects all pending requests with "IPC client disposed"', async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p1 = client.call("board.list", {});
      const p2 = client.call("issue.view", {});

      client.dispose();
      await flushPromises();

      await expect(p1).rejects.toThrow("IPC client disposed");
      await expect(p2).rejects.toThrow("IPC client disposed");
    });

    it("kills the child process on dispose", async () => {
      await startClient();
      const proc = mockProc;
      IpcClient.getInstance().dispose();

      expect(proc.kill).toHaveBeenCalledWith("SIGTERM");
    });

    it("does not restart after disposal", async () => {
      const { spawn } = await import("child_process");
      await startClient();
      IpcClient.getInstance().dispose();

      const spawnCallsBeforeExit = vi.mocked(spawn).mock.calls.length;

      // Advance timers well past the maximum restart backoff.
      vi.advanceTimersByTime(128_000);
      await flushPromises();

      expect(vi.mocked(spawn).mock.calls.length).toBe(spawnCallsBeforeExit);
    });
  });

  // ── Invalid wire input ─────────────────────────────────────────────────────

  describe("invalid wire input", () => {
    it("logs and ignores non-JSON lines from the Go binary", async () => {
      await startClient();
      const p = IpcClient.getInstance().call<string>("test.call");

      mockRl.emit("line", "this is not JSON");
      await flushPromises();

      simulateResponse({ id: 1, result: "ok" });
      await flushPromises();

      await expect(p).resolves.toBe("ok");
    });

    it("ignores empty lines without rejecting pending requests", async () => {
      await startClient();
      const p = IpcClient.getInstance().call<string>("test.call");

      mockRl.emit("line", "");
      await flushPromises();

      simulateResponse({ id: 1, result: "ok" });
      await flushPromises();

      await expect(p).resolves.toBe("ok");
    });
  });

  // ── GITHUB_TOKEN resolution ────────────────────────────────────────────────

  describe("GITHUB_TOKEN resolution", () => {
    it("uses GITHUB_TOKEN env var when set, without calling gh auth token", async () => {
      process.env.GITHUB_TOKEN = "ghp_from_env";
      const { exec } = await import("child_process");

      await startClient();

      const authCalls = vi
        .mocked(exec)
        .mock.calls.filter(([cmd]) => String(cmd).includes("gh auth token"));
      expect(authCalls).toHaveLength(0);
    });

    it("falls back to gh auth token when GITHUB_TOKEN env var is absent", async () => {
      delete process.env.GITHUB_TOKEN;
      const { exec } = await import("child_process");
      vi.mocked(exec).mockImplementation(
        (
          _cmd: string,
          _opts: any,
          cb?: (err: Error | null, stdout: string, stderr: string) => void
        ) => {
          cb?.(null, "ghp_from_gh_auth\n", "");
          return {} as any;
        }
      );

      await startClient();

      const authCall = vi
        .mocked(exec)
        .mock.calls.find(([cmd]) => String(cmd).includes("gh auth token"));
      expect(authCall).toBeDefined();
    });

    it("warns but continues when both GITHUB_TOKEN and gh auth fail", async () => {
      delete process.env.GITHUB_TOKEN;
      const { exec } = await import("child_process");
      vi.mocked(exec).mockImplementation(
        (
          _cmd: string,
          _opts: any,
          cb?: (err: Error | null, stdout: string, stderr: string) => void
        ) => {
          cb?.(new Error("gh not found"), "", "");
          return {} as any;
        }
      );

      await expect(startClient()).resolves.toBeUndefined();
    });
  });

  // ── Process reconnection ───────────────────────────────────────────────────

  describe("process reconnection", () => {
    it("rejects in-flight requests when process exits unexpectedly", async () => {
      await startClient();
      const p = IpcClient.getInstance().call("board.list", {});

      mockProc.emit("exit", 1);
      await flushPromises();

      await expect(p).rejects.toThrow(/exited with code/);
    });

    it("schedules a restart after process exit with 2 s initial delay", async () => {
      const { spawn } = await import("child_process");
      await startClient();

      const spawnCount = vi.mocked(spawn).mock.calls.length;

      mockProc.emit("exit", 1);
      await flushPromises();

      // Not yet — 2 s has not elapsed.
      await vi.advanceTimersByTimeAsync(1999);
      expect(vi.mocked(spawn).mock.calls.length).toBe(spawnCount);

      // Past the 2 s threshold — restart fires.
      // Use advanceTimersByTimeAsync to properly interleave timers and
      // microtasks from the async start() → resolveBinaryPath() chain.
      await vi.advanceTimersByTimeAsync(2);
      expect(vi.mocked(spawn).mock.calls.length).toBeGreaterThan(spawnCount);
    });

    it("uses exponential backoff: second restart waits twice as long", async () => {
      const { spawn } = await import("child_process");
      await startClient();

      // First exit → restart at 2 s.
      mockProc.emit("exit", 1);
      await vi.advanceTimersByTimeAsync(2001);

      const afterFirstRestart = vi.mocked(spawn).mock.calls.length;

      // Second exit → restart should be at 4 s (not 2 s).
      mockProc.emit("exit", 1);

      // At 3999 ms — not yet restarted.
      await vi.advanceTimersByTimeAsync(3999);
      expect(vi.mocked(spawn).mock.calls.length).toBe(afterFirstRestart);

      // At 4001 ms — restart fires.
      await vi.advanceTimersByTimeAsync(2);
      expect(vi.mocked(spawn).mock.calls.length).toBeGreaterThan(afterFirstRestart);
    });

    it("stops restarting after maxRestartAttempts (5) and shows an error", async () => {
      const { spawn } = await import("child_process");
      await startClient();

      for (let i = 0; i < 5; i++) {
        mockProc.emit("exit", 1);
        await vi.advanceTimersByTimeAsync(64_001);
      }

      const spawnCountAfterExhaustion = vi.mocked(spawn).mock.calls.length;

      mockProc.emit("exit", 1);
      await vi.advanceTimersByTimeAsync(64_001);

      expect(vi.mocked(spawn).mock.calls.length).toBe(spawnCountAfterExhaustion);
      expect(vscode.window.showErrorMessage).toHaveBeenCalled();
    });

    it("resets the restart counter after a successful response", async () => {
      const { spawn } = await import("child_process");
      await startClient();

      // Accumulate 2 restart attempts.
      for (let i = 0; i < 2; i++) {
        mockProc.emit("exit", 1);
        await vi.advanceTimersByTimeAsync(64_001);
      }

      // Successful response resets restartAttempts to 0.
      const p = IpcClient.getInstance().call<string>("test");
      simulateResponse({ id: 1, result: "ok" });
      await flushPromises();
      await p;

      const baseSpawnCount = vi.mocked(spawn).mock.calls.length;

      // After reset, next exit should restart at the initial 2 s delay.
      mockProc.emit("exit", 1);

      await vi.advanceTimersByTimeAsync(1999);
      expect(vi.mocked(spawn).mock.calls.length).toBe(baseSpawnCount);

      await vi.advanceTimersByTimeAsync(2);
      expect(vi.mocked(spawn).mock.calls.length).toBeGreaterThan(baseSpawnCount);
    });
  });
});
