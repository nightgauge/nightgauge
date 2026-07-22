/**
 * IpcClient.forge.test.ts — Tests for the forge selection IPC wiring.
 *
 * Pins the contract that the TypeScript IpcClient correctly serialises
 * `workspace.configureForgeInstance` calls to the Go binary and surfaces the
 * response, including the case where an unknown forge kind is rejected by the
 * server.
 *
 * Pattern mirrors IpcClient.boardList.test.ts — a real EventEmitter-backed
 * mock of child_process so the round-trip exercises the request/response
 * envelope rather than mocking IpcClient itself.
 *
 * @see Issue #3365 — Unit Test Corpus + GitHub↔GitLab Parity Contract Tests
 */

import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from "vitest";
import { EventEmitter } from "events";

// ─── Mock setup (mirrors IpcClient.boardList.test.ts) ────────────────────────

const mockRl = new EventEmitter() as any;
mockRl.setMaxListeners(50);
mockRl.close = vi.fn();

let mockProc: ReturnType<typeof makeMockProcess>;
let lastWritten: string[] = [];

function makeMockProcess() {
  const proc = new EventEmitter() as any;
  proc.stdin = {
    writable: true,
    write: vi.fn((data: string, cb?: (err?: Error | null) => void) => {
      lastWritten.push(data);
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

import * as vscode from "vscode";
import { IpcClient } from "../../src/services/IpcClient";

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

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("IpcClient.workspaceConfigureForgeInstance", () => {
  beforeEach(async () => {
    IpcClient.resetInstance();
    vi.clearAllMocks();
    mockRl.removeAllListeners();
    lastWritten = [];

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

  it("serialises a workspace.configureForgeInstance call and surfaces the result", async () => {
    await startClient();
    const client = IpcClient.getInstance();

    const p = client.workspaceConfigureForgeInstance(
      "nightgauge",
      "nightgauge",
      "github",
      "github.com"
    );

    // Inspect the request frame written to the binary's stdin: it must be
    // newline-delimited JSON containing the method name and the params shape.
    expect(lastWritten.length).toBeGreaterThan(0);
    const lastFrame = lastWritten[lastWritten.length - 1];
    const req = JSON.parse(lastFrame);
    expect(req.method).toBe("workspace.configureForgeInstance");
    expect(req.params.owner).toBe("nightgauge");
    expect(req.params.repo).toBe("nightgauge");
    expect(req.params.kind).toBe("github");
    expect(req.params.host).toBe("github.com");

    // Stub the success response.
    simulateResponse({ id: req.id, result: { ok: true, kind: "github" } });
    await flushPromises();

    const result = await p;
    expect(result.ok).toBe(true);
    expect(result.kind).toBe("github");
  });

  it("accepts kind=gitlab", async () => {
    await startClient();
    const client = IpcClient.getInstance();

    const p = client.workspaceConfigureForgeInstance("acme", "platform", "gitlab", "gitlab.com");

    const lastFrame = lastWritten[lastWritten.length - 1];
    const req = JSON.parse(lastFrame);
    expect(req.params.kind).toBe("gitlab");

    simulateResponse({ id: req.id, result: { ok: true, kind: "gitlab" } });
    await flushPromises();

    const result = await p;
    expect(result.kind).toBe("gitlab");
  });

  it("propagates the server-side error when an unknown kind is rejected", async () => {
    await startClient();
    const client = IpcClient.getInstance();

    const p = client.workspaceConfigureForgeInstance("o", "r", "bitbucket");

    const lastFrame = lastWritten[lastWritten.length - 1];
    const req = JSON.parse(lastFrame);

    // Server-side rejection — the Go handler returns an RPC error rather
    // than a result. The TS layer must reject the promise with the message.
    simulateResponse({
      id: req.id,
      error: {
        code: -32603,
        message: 'kind must be "github" or "gitlab", got "bitbucket"',
      },
    });
    await flushPromises();

    await expect(p).rejects.toThrow(/github.*gitlab/);
  });

  it("token parameter is forwarded when provided", async () => {
    await startClient();
    const client = IpcClient.getInstance();

    const p = client.workspaceConfigureForgeInstance(
      "o",
      "r",
      "github",
      "github.com",
      "ghp_secret"
    );

    const lastFrame = lastWritten[lastWritten.length - 1];
    const req = JSON.parse(lastFrame);
    expect(req.params.token).toBe("ghp_secret");

    simulateResponse({ id: req.id, result: { ok: true, kind: "github" } });
    await flushPromises();
    await p;
  });
});
