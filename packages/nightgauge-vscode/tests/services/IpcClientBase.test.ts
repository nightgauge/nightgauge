/**
 * IpcClientBase.test.ts — Unit tests for IpcClientBase abstract class.
 *
 * Tests the base class directly via a minimal concrete TestableIpcClient,
 * independent of the IpcClient singleton wrapper used in
 * IpcClient.protocol.test.ts. This covers:
 *
 *  - isConnected getter (false before start, true after, false after dispose/exit)
 *  - onPipelineComplete VSCode event fires when pipeline.complete IPC event received
 *  - start() idempotency: concurrent calls do not spawn multiple processes
 *  - call() auto-start: client starts backend on first call when not connected
 *  - Platform env forwarding: reads .nightgauge/config.yaml for NIGHTGAUGE_*
 *  - dispose() sets disposed flag, preventing auto-restart after process exit
 *  - Multiple independent instances do not share process state
 *
 * @see IpcClient.protocol.test.ts — Protocol-level tests (singleton path)
 * @see src/services/IpcClientBase.ts — Implementation under test
 * @see internal/ipc/protocol.go — Go-side protocol definition
 * @see docs/GO_BINARY.md — IPC architecture
 */

import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from "vitest";
import { EventEmitter } from "events";

// ─── Mock setup ──────────────────────────────────────────────────────────────
//
// vi.mock() factories are hoisted to the top of the file before any
// module-level declarations run. Variables referenced inside those factories
// must be created via vi.hoisted() so they are initialised before the
// hoisted vi.mock() calls execute.

const { execFileMock, mockRl, readFileSyncSpy, existsSyncSpy } = vi.hoisted(() => {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { EventEmitter } = require("events") as typeof import("events");

  // Shared mock readline interface: emit('line', json) simulates Go binary output.
  const rl = new EventEmitter() as any;
  rl.setMaxListeners(50);
  rl.close = vi.fn();

  // execFileMock with util.promisify.custom so promisify(execFile) returns
  // { stdout, stderr } instead of just the first argument.
  const mock = vi.fn();
  const kCustomPromisify = Symbol.for("nodejs.util.promisify.custom");
  (mock as any)[kCustomPromisify] = (file: string, args: string[], opts: object) =>
    new Promise<{ stdout: string; stderr: string }>((resolve, reject) => {
      mock(file, args, opts, (err: Error | null, stdout: string, stderr: string) => {
        if (err) reject(err);
        else resolve({ stdout, stderr });
      });
    });

  // Spy on the real (CJS-cached) fs module so that dynamic require("fs") calls
  // inside IpcClientBase.ts method bodies are intercepted. vi.mock("fs") only
  // intercepts static ESM imports; spying on the real module covers require().
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const realFs = require("fs") as typeof import("fs");
  const readFileSyncSpy = vi.spyOn(realFs, "readFileSync");
  const existsSyncSpy = vi.spyOn(realFs, "existsSync");

  return {
    execFileMock: mock,
    mockRl: rl,
    readFileSyncSpy,
    existsSyncSpy,
  };
});

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
  proc.pid = 5678;
  proc.kill = vi.fn((signal?: string) => {
    proc.killed = true;
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
      cb?.(null, "ghp_test_fallback_token\n", "");
    }
  ),
  execFile: execFileMock,
}));

vi.mock("readline", () => ({
  createInterface: vi.fn(() => mockRl),
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(() => ""),
}));

// Mock incrediConfig so tests can control what getGitHubAuthToken returns without
// relying on the real file system (incrediConfig uses "node:fs" which is a
// separate module specifier from "fs" and is not covered by the vi.mock("fs") above).
vi.mock("../../src/utils/incrediConfig", () => ({
  getGitHubAuthToken: vi.fn(() => null),
  getGitHubAuthTokens: vi.fn(() => ({})),
  // Other functions used by incrediConfig are not needed by IpcClientBase
}));

// ─── Import under test ───────────────────────────────────────────────────────

import * as vscode from "vscode";
import { IpcClientBase } from "../../src/services/IpcClientBase";
import { getGitHubAuthToken, getGitHubAuthTokens } from "../../src/utils/incrediConfig";

// ─── Concrete test subclass ───────────────────────────────────────────────────

/**
 * Minimal concrete subclass for testing IpcClientBase in isolation.
 * Exposes protected members as public getters for assertion without
 * altering production behaviour.
 */
class TestableIpcClient extends IpcClientBase {
  constructor() {
    super();
  }

  getRestartAttempts(): number {
    return this.restartAttempts;
  }

  isStartingFlag(): boolean {
    return this.starting;
  }

  isDisposedFlag(): boolean {
    return this.disposed;
  }

  getProcess() {
    return this.process;
  }
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

/**
 * Emit a JSON line synchronously from the mock readline interface.
 * Triggers IpcClientBase.handleLine() inline — no timers needed.
 */
function simulateResponse(response: object): void {
  mockRl.emit("line", JSON.stringify(response));
}

/**
 * Flush the microtask queue (Promise callbacks) without advancing fake timers.
 */
async function flushPromises(depth = 6): Promise<void> {
  for (let i = 0; i < depth; i++) {
    await Promise.resolve();
  }
}

/**
 * Start a TestableIpcClient with a fake binary path.
 */
async function startTestClient(client: TestableIpcClient): Promise<void> {
  process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
  await client.start();
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("IpcClientBase", () => {
  let client: TestableIpcClient;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockRl.removeAllListeners();

    // Restore existsSync / readFileSync defaults after clearAllMocks resets call
    // history. The vi.mock("fs") factory intercepts static ESM imports (used by
    // BinaryResolver.ts). The spies cover dynamic require("fs") in IpcClientBase.
    const fs = await import("fs");
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue("");
    // Default: no config files found via require("fs") — prevents the real
    // ~/.nightgauge/config.yaml from bleeding into tests.
    existsSyncSpy.mockReturnValue(false);
    readFileSyncSpy.mockReturnValue("");

    // Restore execFileMock default (returns a per-user token successfully).
    execFileMock.mockImplementation(
      (
        _file: string,
        _args: string[],
        _opts: object,
        cb?: (err: Error | null, stdout: string, stderr: string) => void
      ) => {
        cb?.(null, "ghp_per_user_token\n", "");
        return {};
      }
    );

    (vscode.workspace.getConfiguration as MockInstance).mockReturnValue({
      get: vi.fn(<T>(key: string, defaultValue?: T): T | undefined => {
        if (key === "binaryPath") return "" as unknown as T;
        if (key === "timeoutSeconds") return 30 as unknown as T;
        return defaultValue;
      }),
    });

    (vscode.window.showErrorMessage as MockInstance).mockResolvedValue(undefined);
    (vscode.window.showWarningMessage as MockInstance).mockResolvedValue(undefined);

    process.env.GITHUB_TOKEN = "test_token";
    // Clean up platform env vars that may bleed between tests.
    delete process.env.NIGHTGAUGE_PLATFORM_URL;
    delete process.env.NIGHTGAUGE_LICENSE_KEY;
    delete process.env.NIGHTGAUGE_API_KEY;

    // Default: no config-based token (returns null)
    vi.mocked(getGitHubAuthToken).mockReturnValue(null);
    vi.mocked(getGitHubAuthTokens).mockReturnValue({});

    // Replace the non-functional vscode.EventEmitter stub (from setup.ts) with a
    // working implementation so that client.onPipelineComplete() actually
    // registers listeners and fire() calls them. Must happen BEFORE creating
    // TestableIpcClient, because EventEmitters are created as class properties.
    (vscode as any).EventEmitter = class WorkingEventEmitter<T = unknown> {
      private _listeners: ((e: T) => void)[] = [];
      event = (listener: (e: T) => void) => {
        this._listeners.push(listener);
        return {
          dispose: () => {
            this._listeners = this._listeners.filter((l) => l !== listener);
          },
        };
      };
      fire = (data: T) => {
        for (const l of this._listeners) l(data);
      };
      dispose = () => {
        this._listeners = [];
      };
    };

    vi.useFakeTimers();
    client = new TestableIpcClient();
  });

  afterEach(() => {
    client.dispose();
    delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
    delete process.env.GITHUB_TOKEN;
    delete process.env.NIGHTGAUGE_PLATFORM_URL;
    vi.useRealTimers();
  });

  // ── isConnected ────────────────────────────────────────────────────────────

  describe("isConnected", () => {
    it("returns false before start()", () => {
      expect(client.isConnected).toBe(false);
    });

    it("returns true after a successful start()", async () => {
      await startTestClient(client);
      expect(client.isConnected).toBe(true);
    });

    it("returns false after dispose()", async () => {
      await startTestClient(client);
      client.dispose();
      await flushPromises();
      expect(client.isConnected).toBe(false);
    });

    it("returns false after the child process exits unexpectedly", async () => {
      await startTestClient(client);
      expect(client.isConnected).toBe(true);

      mockProc.emit("exit", 1);
      await flushPromises();

      expect(client.isConnected).toBe(false);
    });
  });

  // ── onPipelineComplete ─────────────────────────────────────────────────────

  describe("onPipelineComplete", () => {
    it("fires when pipeline.complete IPC event is received", async () => {
      await startTestClient(client);

      const handler = vi.fn();
      client.onPipelineComplete(handler);

      const payload = {
        executionId: "exec-abc-123",
        issueNumber: 42,
        success: true,
        totalInputTokens: 1200,
        totalOutputTokens: 600,
        totalCostUSD: 0,
        perStage: [{ stage: "feature-dev", inputTokens: 800, outputTokens: 400 }],
      };

      simulateResponse({ event: "pipeline.complete", data: payload });
      await flushPromises();

      expect(handler).toHaveBeenCalledOnce();
      expect(handler).toHaveBeenCalledWith(payload);
    });

    it("does not fire for other event types", async () => {
      await startTestClient(client);

      const handler = vi.fn();
      client.onPipelineComplete(handler);

      simulateResponse({
        event: "stage.complete",
        data: { stage: "feature-dev" },
      });
      simulateResponse({ event: "queue.changed", data: {} });
      simulateResponse({ event: "ipc.ready", data: { protocolVersion: 1 } });
      await flushPromises();

      expect(handler).not.toHaveBeenCalled();
    });

    it("fires multiple times for consecutive pipeline.complete events", async () => {
      await startTestClient(client);

      const handler = vi.fn();
      client.onPipelineComplete(handler);

      const basePayload = {
        executionId: "exec-1",
        issueNumber: 1,
        success: true,
        totalInputTokens: 100,
        totalOutputTokens: 50,
        totalCostUSD: 0,
        perStage: [],
      };

      simulateResponse({
        event: "pipeline.complete",
        data: { ...basePayload, executionId: "exec-1" },
      });
      simulateResponse({
        event: "pipeline.complete",
        data: { ...basePayload, executionId: "exec-2" },
      });
      await flushPromises();

      expect(handler).toHaveBeenCalledTimes(2);
    });
  });

  // ── start() idempotency ────────────────────────────────────────────────────

  describe("start() idempotency", () => {
    it("does not spawn multiple processes when called concurrently", async () => {
      const { spawn } = await import("child_process");
      process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";

      // Fire two concurrent start() calls — the starting guard must prevent
      // a second spawn while the first is still resolving binary path.
      await Promise.all([client.start(), client.start()]);

      expect(vi.mocked(spawn)).toHaveBeenCalledTimes(1);
    });

    it("does not spawn again when already connected", async () => {
      const { spawn } = await import("child_process");
      await startTestClient(client);

      const spawnCountBefore = vi.mocked(spawn).mock.calls.length;
      await client.start();

      expect(vi.mocked(spawn).mock.calls.length).toBe(spawnCountBefore);
    });
  });

  // ── call() auto-start ─────────────────────────────────────────────────────

  describe("call() auto-start", () => {
    it("starts the backend automatically on the first call() when not connected", async () => {
      const { spawn } = await import("child_process");
      process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";

      expect(client.isConnected).toBe(false);

      const callPromise = client.call<string>("test.method");

      // Allow start() + request write to complete across multiple microtask ticks.
      await flushPromises(8);

      // Respond now that the request is in flight.
      simulateResponse({ id: 1, result: "auto-started" });
      await flushPromises();

      const result = await callPromise;
      expect(result).toBe("auto-started");
      expect(vi.mocked(spawn)).toHaveBeenCalledTimes(1);
    });
  });

  // ── Config-based token resolution (Issue #2670) ───────────────────────────

  describe("config-based token resolution", () => {
    it("uses config token from github_auth.token when present, before checking env", async () => {
      const { spawn } = await import("child_process");

      // Config returns a direct token; no GITHUB_TOKEN env var
      delete process.env.GITHUB_TOKEN;
      vi.mocked(getGitHubAuthToken).mockReturnValue("ghp_config_direct");

      await startTestClient(client);

      const spawnEnv = vi.mocked(spawn).mock.calls[0]?.[2]?.env as
        Record<string, string> | undefined;
      expect(spawnEnv?.GITHUB_TOKEN).toBe("ghp_config_direct");
    });

    it("falls back to GITHUB_TOKEN env var when no config token is present", async () => {
      const { spawn } = await import("child_process");

      // getGitHubAuthToken and getGitHubAuthTokens both return null/empty (default from beforeEach)
      process.env.GITHUB_TOKEN = "ghp_env_fallback";

      await startTestClient(client);

      const spawnEnv = vi.mocked(spawn).mock.calls[0]?.[2]?.env as
        Record<string, string> | undefined;
      expect(spawnEnv?.GITHUB_TOKEN).toBe("ghp_env_fallback");
    });

    it("config token takes priority over GITHUB_TOKEN env var", async () => {
      const { spawn } = await import("child_process");

      // Both config token and env GITHUB_TOKEN are present; config should win
      process.env.GITHUB_TOKEN = "ghp_env_token";
      vi.mocked(getGitHubAuthToken).mockReturnValue("ghp_config_wins");

      await startTestClient(client);

      const spawnEnv = vi.mocked(spawn).mock.calls[0]?.[2]?.env as
        Record<string, string> | undefined;
      expect(spawnEnv?.GITHUB_TOKEN).toBe("ghp_config_wins");
    });

    it("uses per-org token from github_auth.tokens when direct token absent", async () => {
      const { spawn } = await import("child_process");

      delete process.env.GITHUB_TOKEN;
      // No direct token; per-org map has a token for "myorg"
      vi.mocked(getGitHubAuthToken).mockReturnValue(null);
      vi.mocked(getGitHubAuthTokens).mockReturnValue({ myorg: "ghp_org_token" });

      await startTestClient(client);

      const spawnEnv = vi.mocked(spawn).mock.calls[0]?.[2]?.env as
        Record<string, string> | undefined;
      expect(spawnEnv?.GITHUB_TOKEN).toBe("ghp_org_token");
    });

    it("tokenSource getter returns config-source string after config token resolution", async () => {
      delete process.env.GITHUB_TOKEN;
      vi.mocked(getGitHubAuthToken).mockReturnValue("ghp_source_test");

      await startTestClient(client);

      expect(client.tokenSource).toBeTruthy();
      expect(String(client.tokenSource)).toContain("config");
    });

    it("tokenSource getter returns null when no token has been resolved yet", () => {
      // Client not started — tokenSource should be null
      expect(client.tokenSource).toBeNull();
    });
  });

  // ── Platform env forwarding ────────────────────────────────────────────────

  describe("platform env forwarding", () => {
    it("sets NIGHTGAUGE_PLATFORM_URL from platform.api_url in config.yaml", async () => {
      const { spawn } = await import("child_process");

      // Use the real-fs spy (covers require("fs") in IpcClientBase method bodies)
      // so the platform config YAML is returned for the global config path.
      existsSyncSpy.mockReturnValue(true);
      readFileSyncSpy.mockReturnValue("platform:\n  api_url: https://api.nightgauge.test\n");

      await startTestClient(client);

      const spawnEnv = vi.mocked(spawn).mock.calls[0]?.[2]?.env as
        Record<string, string> | undefined;
      expect(spawnEnv?.NIGHTGAUGE_PLATFORM_URL).toBe("https://api.nightgauge.test");
    });

    it("sets NIGHTGAUGE_LICENSE_KEY from SecretStorage, not from config.yaml", async () => {
      const { spawn } = await import("child_process");
      const { SecretStorageService, SECRET_KEYS } =
        await import("../../src/services/SecretStorageService");

      // Simulate SecretStorageService being initialized with a key
      const mockSecrets = {
        get: vi.fn(async (k: string) =>
          k === SECRET_KEYS.platformLicenseKey ? "live_from_keychain" : undefined
        ),
        store: vi.fn(),
        delete: vi.fn(),
        onDidChange: vi.fn(),
      } as unknown as import("vscode").SecretStorage;
      SecretStorageService.resetInstance();
      SecretStorageService.initialize(mockSecrets);

      // YAML has license_key — it must NOT be used
      existsSyncSpy.mockReturnValue(true);
      readFileSyncSpy.mockReturnValue("platform:\n  license_key: live_from_yaml\n");

      await startTestClient(client);

      const spawnEnv = vi.mocked(spawn).mock.calls[0]?.[2]?.env as
        Record<string, string> | undefined;
      // Must use the SecretStorage value, not the YAML value
      expect(spawnEnv?.NIGHTGAUGE_LICENSE_KEY).toBe("live_from_keychain");

      SecretStorageService.resetInstance();
    });

    it("does not override NIGHTGAUGE_PLATFORM_URL already set in process.env", async () => {
      const { spawn } = await import("child_process");

      process.env.NIGHTGAUGE_PLATFORM_URL = "https://already-set.example.com";
      existsSyncSpy.mockReturnValue(true);
      readFileSyncSpy.mockReturnValue("platform:\n  api_url: https://from-config.example.com\n");

      await startTestClient(client);

      const spawnEnv = vi.mocked(spawn).mock.calls[0]?.[2]?.env as
        Record<string, string> | undefined;
      // Pre-existing env var must not be overwritten.
      expect(spawnEnv?.NIGHTGAUGE_PLATFORM_URL).toBe("https://already-set.example.com");
    });
  });

  // ── dispose() ─────────────────────────────────────────────────────────────

  describe("dispose()", () => {
    it("sets the disposed flag to true", async () => {
      await startTestClient(client);
      expect(client.isDisposedFlag()).toBe(false);

      client.dispose();
      expect(client.isDisposedFlag()).toBe(true);
    });

    it("prevents auto-restart after the process exits post-dispose", async () => {
      const { spawn } = await import("child_process");
      await startTestClient(client);

      client.dispose();

      // Explicitly fire the exit event so handleProcessExit() is invoked —
      // this is where the !this.disposed guard lives. Without emitting exit,
      // the guard is never exercised and the test passes vacuously.
      mockProc.emit("exit", 0);
      await flushPromises();

      const spawnCountAfterDispose = vi.mocked(spawn).mock.calls.length;

      // Advance well past all restart backoff timers — no restart should fire.
      vi.advanceTimersByTime(128_000);
      await flushPromises();

      expect(vi.mocked(spawn).mock.calls.length).toBe(spawnCountAfterDispose);
    });

    it('rejects all pending requests with "IPC client disposed"', async () => {
      await startTestClient(client);

      const p1 = client.call("test.alpha");
      const p2 = client.call("test.beta");

      client.dispose();
      await flushPromises();

      await expect(p1).rejects.toThrow("IPC client disposed");
      await expect(p2).rejects.toThrow("IPC client disposed");
    });
  });

  // ── Multiple independent instances ────────────────────────────────────────

  describe("multiple instances", () => {
    it("two instances spawn independent processes — state is not shared", async () => {
      const { spawn } = await import("child_process");
      const client2 = new TestableIpcClient();

      try {
        process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
        await client.start();
        await client2.start();

        // Each instance spawned its own child process.
        expect(vi.mocked(spawn)).toHaveBeenCalledTimes(2);
        expect(client.isConnected).toBe(true);
        expect(client2.isConnected).toBe(true);
      } finally {
        client2.dispose();
      }
    });

    it("disposing one instance does not affect the other", async () => {
      const client2 = new TestableIpcClient();

      try {
        process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
        await client.start();
        await client2.start();

        client.dispose();
        await flushPromises();

        expect(client.isConnected).toBe(false);
        expect(client2.isConnected).toBe(true);
      } finally {
        client2.dispose();
      }
    });

    it("each instance maintains its own request ID counter", async () => {
      const client2 = new TestableIpcClient();

      try {
        process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
        await client.start();
        await client2.start();

        // Both clients make their first call — each should send id=1.
        const p1 = client.call("test.first");
        const p2 = client2.call("test.first");

        await flushPromises();

        // Extract writes from each client's process stdin.
        // capturedStdinWrites tracks the LAST spawned process — check both.
        // Because both use the same mock, we parse what was captured.
        const allWrites = capturedStdinWrites;
        const ids = allWrites.map((w) => JSON.parse(w.trimEnd()).id);
        // Each instance starts its own ID counter at 1.
        expect(ids.filter((id) => id === 1)).toHaveLength(2);

        simulateResponse({ id: 1, result: "ok-1" });
        simulateResponse({ id: 1, result: "ok-2" });
        await flushPromises();

        await Promise.all([expect(p1).resolves.toBeDefined(), expect(p2).resolves.toBeDefined()]);
      } finally {
        client2.dispose();
      }
    });
  });
});
