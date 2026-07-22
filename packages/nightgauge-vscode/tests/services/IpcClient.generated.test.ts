/**
 * IpcClient.generated.test.ts — Verifies auto-generated IPC methods
 *
 * Tests a representative sample of generated methods to ensure the codegen
 * tool (`cmd/ipc-codegen`) produces correct TypeScript method bodies:
 *
 *  - Standard methods: forward params and return typed result
 *  - Nullable methods: coerce null → [] for Go nil slices
 *  - Unwrap methods: extract nested field (e.g., { branch: "main" } → "main")
 *  - Void methods: await call without returning
 *  - No-params methods: omit params key
 *  - Optional params: include when provided, omit when undefined
 *
 * @see cmd/ipc-codegen/main.go     — codegen tool
 * @see IpcClient.generated.ts      — file under test (auto-generated)
 * @see internal/ipc/server.go      — Go annotations (source of truth)
 */

import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from "vitest";
import { EventEmitter } from "events";

// ─── Mock setup ──────────────────────────────────────────────────────────────

const mockRl = new EventEmitter() as any;
mockRl.setMaxListeners(50);
mockRl.close = vi.fn();

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
  proc.kill = vi.fn();
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
      cb?.(null, "ghp_test_token\n", "");
    }
  ),
}));

vi.mock("readline", () => ({
  createInterface: vi.fn(() => mockRl),
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
}));

// ─── Import under test ───────────────────────────────────────────────────────

import * as vscode from "vscode";
import { IpcClient } from "../../src/services/IpcClient";

// ─── Test helpers ─────────────────────────────────────────────────────────────

function simulateResponse(response: object): void {
  mockRl.emit("line", JSON.stringify(response));
}

async function flushPromises(depth = 4): Promise<void> {
  for (let i = 0; i < depth; i++) {
    await Promise.resolve();
  }
}

async function startClient(): Promise<void> {
  process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
  await IpcClient.getInstance().start();
}

/** Parse the last stdin write to extract method and params sent to Go. */
function lastRequest(): { method: string; params?: Record<string, unknown> } {
  const raw = capturedStdinWrites[capturedStdinWrites.length - 1];
  return JSON.parse(raw.trimEnd());
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("IpcClient.generated — auto-generated methods", () => {
  beforeEach(async () => {
    IpcClient.resetInstance();
    vi.clearAllMocks();
    mockRl.removeAllListeners();

    const fs = await import("fs");
    vi.mocked(fs.existsSync).mockReturnValue(true);

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
    vi.useFakeTimers();
  });

  afterEach(() => {
    IpcClient.resetInstance();
    delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
    delete process.env.GITHUB_TOKEN;
    vi.useRealTimers();
  });

  // ── Standard methods (pass-through params → typed result) ─────────────────

  describe("standard methods", () => {
    it("issueView sends correct method and params, returns typed result", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.issueView("nightgauge", "nightgauge", 42);
      simulateResponse({
        id: 1,
        result: { number: 42, title: "Test Issue", state: "open" },
      });
      await flushPromises();

      const result = await p;
      const req = lastRequest();
      expect(req.method).toBe("issue.view");
      expect(req.params).toEqual({
        owner: "nightgauge",
        repo: "nightgauge",
        number: 42,
      });
      expect(result.number).toBe(42);
      expect(result.title).toBe("Test Issue");
    });

    it("epicProgress sends correct method and params", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.epicProgress("nightgauge", "nightgauge", 100);
      simulateResponse({
        id: 1,
        result: { total: 5, closed: 3, percent: 60 },
      });
      await flushPromises();

      const result = await p;
      const req = lastRequest();
      expect(req.method).toBe("epic.progress");
      expect(req.params).toEqual({
        owner: "nightgauge",
        repo: "nightgauge",
        number: 100,
      });
      expect(result.total).toBe(5);
    });

    it("pipelineStatus sends correct method and params", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.pipelineStatus("nightgauge", 5, "PVTI_abc");
      simulateResponse({
        id: 1,
        result: { stage: "feature-dev", status: "running" },
      });
      await flushPromises();

      const result = await p;
      const req = lastRequest();
      expect(req.method).toBe("pipeline.status");
      expect(req.params).toEqual({
        owner: "nightgauge",
        projectNumber: 5,
        itemId: "PVTI_abc",
      });
      expect(result.stage).toBe("feature-dev");
    });
  });

  // ── Nullable methods (null → empty array coercion) ────────────────────────

  describe("nullable methods", () => {
    it("boardList coerces null result to empty array", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.boardList("nightgauge", 5, "Ready");
      simulateResponse({ id: 1, result: null });
      await flushPromises();

      const result = await p;
      expect(result).toEqual([]);
      expect(Array.isArray(result)).toBe(true);
    });

    it("boardList passes through non-null arrays", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.boardList("nightgauge", 5);
      simulateResponse({
        id: 1,
        result: [{ number: 1, title: "Item" }],
      });
      await flushPromises();

      const result = await p;
      expect(result).toHaveLength(1);
      expect(result[0].number).toBe(1);
    });

    it("boardList sends optional status param when provided", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.boardList("nightgauge", 5, "Ready");
      simulateResponse({ id: 1, result: [] });
      await flushPromises();
      await p;

      const req = lastRequest();
      expect(req.method).toBe("board.list");
      expect(req.params).toEqual({
        owner: "nightgauge",
        projectNumber: 5,
        status: "Ready",
      });
    });
  });

  // ── Unwrap methods (extract nested field from Go response) ────────────────

  describe("unwrap methods", () => {
    it("gitCurrentBranch unwraps { branch } to return a string", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitCurrentBranch("/workspace");
      simulateResponse({ id: 1, result: { branch: "feat/test" } });
      await flushPromises();

      const result = await p;
      expect(result).toBe("feat/test");
      expect(typeof result).toBe("string");
    });

    it("gitRoot unwraps { root } to return a string", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitRoot("/workspace");
      simulateResponse({ id: 1, result: { root: "/home/user/project" } });
      await flushPromises();

      const result = await p;
      expect(result).toBe("/home/user/project");
    });

    it("gitDiff unwraps { diff } to return a string", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitDiff("/workspace");
      simulateResponse({ id: 1, result: { diff: "--- a/file\n+++ b/file" } });
      await flushPromises();

      const result = await p;
      expect(result).toBe("--- a/file\n+++ b/file");
    });

    it("gitListRemoteBranches unwraps and coerces null branches to []", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitListRemoteBranches("/workspace");
      simulateResponse({ id: 1, result: { branches: null } });
      await flushPromises();

      const result = await p;
      expect(result).toEqual([]);
      expect(Array.isArray(result)).toBe(true);
    });

    it("gitListRemoteBranches unwraps real branch list", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitListRemoteBranches();
      simulateResponse({
        id: 1,
        result: { branches: ["main", "feat/test"] },
      });
      await flushPromises();

      const result = await p;
      expect(result).toEqual(["main", "feat/test"]);
    });
  });

  // ── Void methods (no return value) ────────────────────────────────────────

  describe("void methods", () => {
    it("boardUpdateStatus sends params and resolves void", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.boardUpdateStatus("nightgauge", 5, "PVTI_abc", "Done");
      simulateResponse({ id: 1, result: null });
      await flushPromises();

      await expect(p).resolves.toBeUndefined();

      const req = lastRequest();
      expect(req.method).toBe("board.updateStatus");
      expect(req.params).toEqual({
        owner: "nightgauge",
        projectNumber: 5,
        itemId: "PVTI_abc",
        status: "Done",
      });
    });

    it("pipelineStop sends executionId and resolves void", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.pipelineStop("exec-123");
      simulateResponse({ id: 1, result: null });
      await flushPromises();

      await expect(p).resolves.toBeUndefined();

      const req = lastRequest();
      expect(req.method).toBe("pipeline.stop");
      expect(req.params).toEqual({ executionId: "exec-123" });
    });

    it("gitCheckout sends branch and optional workDir", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitCheckout("feat/test", "/workspace");
      simulateResponse({ id: 1, result: null });
      await flushPromises();

      await expect(p).resolves.toBeUndefined();

      const req = lastRequest();
      expect(req.method).toBe("git.checkout");
      expect(req.params).toEqual({
        branch: "feat/test",
        workDir: "/workspace",
      });
    });
  });

  // ── No-params methods ─────────────────────────────────────────────────────

  describe("no-params methods", () => {
    it("executionList sends no params", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.executionList();
      simulateResponse({ id: 1, result: [] });
      await flushPromises();

      const result = await p;
      expect(result).toEqual([]);

      const req = lastRequest();
      expect(req.method).toBe("execution.list");
      expect(req.params).toBeUndefined();
    });

    it("platformStatus sends no params", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.platformStatus();
      simulateResponse({ id: 1, result: { connected: true } });
      await flushPromises();

      const result = await p;
      expect(result.connected).toBe(true);

      const req = lastRequest();
      expect(req.method).toBe("platform.status");
      expect(req.params).toBeUndefined();
    });

    it("queueClear sends no params and resolves void", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.queueClear();
      simulateResponse({ id: 1, result: null });
      await flushPromises();

      await expect(p).resolves.toBeUndefined();

      const req = lastRequest();
      expect(req.method).toBe("queue.clear");
    });
  });

  // ── Optional params ───────────────────────────────────────────────────────

  describe("optional params", () => {
    it("gitCurrentBranch omits workDir when not provided", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitCurrentBranch();
      simulateResponse({ id: 1, result: { branch: "main" } });
      await flushPromises();
      await p;

      const req = lastRequest();
      expect(req.method).toBe("git.currentBranch");
      expect(req.params).toEqual({ workDir: undefined });
    });

    it("gitLog sends limit when provided", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.gitLog(10, "/workspace");
      simulateResponse({ id: 1, result: [{ hash: "abc123" }] });
      await flushPromises();

      const result = await p;
      expect(result).toHaveLength(1);

      const req = lastRequest();
      expect(req.method).toBe("git.log");
      expect(req.params).toEqual({ limit: 10, workDir: "/workspace" });
    });

    it("queueAdd includes optional fields when provided", async () => {
      await startClient();
      const client = IpcClient.getInstance();

      const p = client.queueAdd("nightgauge", "nightgauge", 42, "Test", ["bug"], "high");
      simulateResponse({ id: 1, result: null });
      await flushPromises();

      await expect(p).resolves.toBeUndefined();

      const req = lastRequest();
      expect(req.method).toBe("queue.add");
      expect(req.params).toEqual({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 42,
        title: "Test",
        labels: ["bug"],
        priority: "high",
      });
    });
  });

  // ── Protocol version export ───────────────────────────────────────────────

  describe("protocol version", () => {
    it("exports IPC_PROTOCOL_VERSION matching Go ProtocolVersion", async () => {
      const { IPC_PROTOCOL_VERSION } = await import("../../src/services/IpcClient.generated");
      expect(IPC_PROTOCOL_VERSION).toBe(1);
      expect(typeof IPC_PROTOCOL_VERSION).toBe("number");
    });
  });

  // ── Inheritance chain ─────────────────────────────────────────────────────

  describe("class hierarchy", () => {
    it("IpcClient extends IpcClientGenerated extends IpcClientBase", async () => {
      const { IpcClientGenerated } = await import("../../src/services/IpcClient.generated");
      const { IpcClientBase } = await import("../../src/services/IpcClientBase");

      await startClient();
      const client = IpcClient.getInstance();

      expect(client).toBeInstanceOf(IpcClientGenerated);
      expect(client).toBeInstanceOf(IpcClientBase);
    });
  });
});
