/**
 * IpcClient.protocolMismatch.test.ts — ADR-017 Migration, the protocol hard-fail.
 *
 * `ProtocolVersion` went 1 → 2 because every `pipeline.*` verb now carries the
 * run identity. An extension that predates the bump would keep sending
 * identity-less calls that the new server refuses one by one, producing a live
 * run with zero records, zero learning outcomes and zero telemetry — F16's
 * shape, discovered hours later. So the mismatch is a hard failure, not a
 * warning: the client disconnects, marks itself permanently unusable for this
 * activation, raises a blocking modal naming both versions, and rejects every
 * later `call()` with `protocol_mismatch` WITHOUT touching the socket.
 *
 * Covers the ADR's Testing Strategy row `IpcClient.protocolMismatch.test.ts`.
 *
 * @see docs/decisions/017-runtime-identity-keying.md § Migration
 * @see src/services/IpcClient.ts — implementation under test
 */

import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from "vitest";
import { EventEmitter } from "events";

// ─── Mock setup (mirrors IpcClient.protocol.test.ts) ─────────────────────────

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
  proc.pid = 4242;
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
}));

vi.mock("readline", () => ({
  createInterface: vi.fn(() => mockRl),
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
}));

// ─── Import under test ───────────────────────────────────────────────────────

import * as vscode from "vscode";
import { IpcClient, IPC_PROTOCOL_VERSION } from "../../src/services/IpcClient";

// ─── Helpers ─────────────────────────────────────────────────────────────────

function emitLine(message: object): void {
  mockRl.emit("line", JSON.stringify(message));
}

/** Emit the startup handshake the Go binary sends on `serve` (internal/ipc). */
function emitReady(protocolVersion: number): void {
  emitLine({ event: "ipc.ready", data: { protocolVersion } });
}

async function flushPromises(depth = 8): Promise<void> {
  for (let i = 0; i < depth; i++) {
    await Promise.resolve();
  }
}

async function startClient(): Promise<IpcClient> {
  process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
  await IpcClient.getInstance().start();
  return IpcClient.getInstance();
}

/**
 * Await a `call()` without letting an eventual rejection escape, and report
 * what it did: "pending" when it never settled (today's warn-and-continue
 * writes the request and waits for a response that never comes).
 */
async function settleOf(promise: Promise<unknown>): Promise<string> {
  let outcome = "pending";
  void promise.then(
    () => {
      outcome = "resolved";
    },
    (err: unknown) => {
      outcome = `rejected: ${err instanceof Error ? err.message : String(err)}`;
    }
  );
  await flushPromises();
  return outcome;
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("IpcClient — protocol version hard-fail (ADR-017 Migration)", () => {
  beforeEach(async () => {
    IpcClient.resetInstance();
    vi.clearAllMocks();
    mockRl.removeAllListeners();

    const fs = await import("fs");
    vi.mocked(fs.existsSync).mockReturnValue(true);

    (vscode.workspace.getConfiguration as unknown as MockInstance).mockReturnValue({
      get: vi.fn(<T>(key: string, defaultValue?: T): T | undefined => {
        if (key === "binaryPath") return "" as unknown as T;
        if (key === "timeoutSeconds") return 30 as unknown as T;
        return defaultValue;
      }),
    });

    (vscode.window.showErrorMessage as unknown as MockInstance).mockResolvedValue(undefined);
    (vscode.window.showWarningMessage as unknown as MockInstance).mockResolvedValue(undefined);

    process.env.GITHUB_TOKEN = "test_token";
    vi.useFakeTimers();
  });

  afterEach(() => {
    IpcClient.resetInstance();
    delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
    delete process.env.GITHUB_TOKEN;
    vi.useRealTimers();
  });

  it("disconnects the client and raises a blocking modal naming both versions", async () => {
    const client = await startClient();
    const proc = mockProc;

    emitReady(IPC_PROTOCOL_VERSION + 1);
    await flushPromises();

    // Disconnected — the socket is gone, not merely complained about.
    expect(proc.kill).toHaveBeenCalled();
    expect(client.isConnected).toBe(false);

    // Blocking modal naming binary version, expected version, and the action.
    expect(vscode.window.showErrorMessage).toHaveBeenCalled();
    const [message, options] = (vscode.window.showErrorMessage as unknown as MockInstance).mock
      .calls[0];
    expect(String(message)).toContain(String(IPC_PROTOCOL_VERSION + 1));
    expect(String(message)).toContain(String(IPC_PROTOCOL_VERSION));
    expect(options).toMatchObject({ modal: true });
  });

  it("rejects every subsequent call with protocol_mismatch without touching the socket", async () => {
    const client = await startClient();

    emitReady(IPC_PROTOCOL_VERSION + 1);
    await flushPromises();

    const writesBefore = capturedStdinWrites.length;
    const { spawn } = await import("child_process");
    const spawnsBefore = vi.mocked(spawn).mock.calls.length;

    const first = await settleOf(client.call("board.list", { owner: "nightgauge" }));
    const second = await settleOf(client.pipelineGetState("nightgauge", "nightgauge", 370));

    expect(first).toMatch(/protocol_mismatch/);
    expect(second).toMatch(/protocol_mismatch/);

    // Nothing reached the wire and nothing was respawned to reach it with.
    expect(capturedStdinWrites.length).toBe(writesBefore);
    expect(vi.mocked(spawn).mock.calls.length).toBe(spawnsBefore);
  });

  it("stays down — a protocol mismatch is not a restartable crash", async () => {
    const { spawn } = await import("child_process");
    const client = await startClient();

    emitReady(IPC_PROTOCOL_VERSION + 1);
    await flushPromises();

    const spawnsAfterMismatch = vi.mocked(spawn).mock.calls.length;

    // Well past the whole restart-backoff ladder (2s → 32s).
    await vi.advanceTimersByTimeAsync(128_000);

    expect(vi.mocked(spawn).mock.calls.length).toBe(spawnsAfterMismatch);
    expect(client.isConnected).toBe(false);
  });

  it("leaves a matching protocolVersion connected, silent, and usable", async () => {
    const client = await startClient();

    emitReady(IPC_PROTOCOL_VERSION);
    await flushPromises();

    expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
    expect(client.isConnected).toBe(true);

    const p = client.call<unknown[]>("board.list", { owner: "nightgauge" });
    emitLine({ id: 1, result: [] });
    await flushPromises();

    await expect(p).resolves.toEqual([]);
    expect(capturedStdinWrites.length).toBe(1);
  });
});
