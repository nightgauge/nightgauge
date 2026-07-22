/**
 * IpcClient.boardList.test.ts — Regression test for null coercion
 *
 * When the Go binary returns JSON null for an empty board.list result
 * (Go nil slice → JSON null), the TypeScript boardList() method must
 * coerce it to an empty array to prevent "items is not iterable" errors.
 *
 * @see Issue #1888 - board.list null response
 */

import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from "vitest";
import { EventEmitter } from "events";

// ─── Mock setup ──────────────────────────────────────────────────────────────

const mockRl = new EventEmitter() as any;
mockRl.setMaxListeners(50);
mockRl.close = vi.fn();

let mockProc: ReturnType<typeof makeMockProcess>;

function makeMockProcess() {
  const proc = new EventEmitter() as any;
  proc.stdin = {
    writable: true,
    write: vi.fn((data: string, cb?: (err?: Error | null) => void) => {
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
  // Return true — these tests exercise IPC protocol, not filesystem.
  // The fake binary path (/fake/nightgauge) must pass existsSync.
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

describe("IpcClient.boardList — null coercion", () => {
  beforeEach(async () => {
    IpcClient.resetInstance();
    vi.clearAllMocks();
    mockRl.removeAllListeners();

    // vi.clearAllMocks() resets mock implementations from vi.mock() factories.
    // Restore existsSync so the fake binary path passes resolveBinaryPath().
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

  it("returns empty array when Go binary responds with null result (nil slice)", async () => {
    await startClient();
    const client = IpcClient.getInstance();

    const p = client.boardList("nightgauge", 1, "Ready");

    // Go nil slice serializes as JSON null
    simulateResponse({ id: 1, result: null });
    await flushPromises();

    const result = await p;
    expect(result).toEqual([]);
    expect(Array.isArray(result)).toBe(true);
  });

  it("returns the array as-is when Go binary responds with a real array", async () => {
    await startClient();
    const client = IpcClient.getInstance();

    const p = client.boardList("nightgauge", 1, "Ready");

    simulateResponse({
      id: 1,
      result: [{ number: 42, title: "Test", status: "Ready" }],
    });
    await flushPromises();

    const result = await p;
    expect(result).toHaveLength(1);
    expect(result[0].number).toBe(42);
  });

  it("returns empty array when Go binary responds with undefined result", async () => {
    await startClient();
    const client = IpcClient.getInstance();

    const p = client.boardList("nightgauge", 1);

    // result key absent from response — resp.result is undefined
    simulateResponse({ id: 1 });
    await flushPromises();

    const result = await p;
    expect(result).toEqual([]);
    expect(Array.isArray(result)).toBe(true);
  });
});
