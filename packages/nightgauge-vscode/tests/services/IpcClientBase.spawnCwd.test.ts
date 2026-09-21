/**
 * The daemon is started IN the workspace it serves.
 *
 * @see Issue #1913 — `--workspace` is a string the Go side threads through its
 * own subsystems; it never changes the process's working directory. The API
 * ledger resolved its relative path against the cwd and therefore wrote into
 * the extension host's directory (or nowhere). Passing `cwd` removes that
 * whole class of mismatch for anything else inside the daemon that resolves a
 * relative path.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { EventEmitter } from "events";

const mockRl = new EventEmitter() as EventEmitter & { close: () => void };
(mockRl as any).close = vi.fn();

function makeMockProcess() {
  const proc = new EventEmitter() as any;
  proc.stdin = { writable: true, write: vi.fn(() => true) };
  proc.stdout = new EventEmitter();
  proc.stderr = new EventEmitter();
  proc.killed = false;
  proc.pid = 4321;
  proc.kill = vi.fn();
  return proc;
}

vi.mock("child_process", () => ({
  spawn: vi.fn(() => makeMockProcess()),
  exec: vi.fn(
    (_cmd: string, _opts: object, cb?: (e: Error | null, o: string, s: string) => void) => {
      cb?.(null, "ghp_token\n", "");
    }
  ),
  execFile: vi.fn(
    (
      _file: string,
      _args: string[],
      _opts: object,
      cb?: (e: Error | null, o: string, s: string) => void
    ) => {
      cb?.(null, "", "");
    }
  ),
}));

vi.mock("readline", () => ({ createInterface: vi.fn(() => mockRl) }));
vi.mock("fs", () => ({ existsSync: vi.fn(() => true), readFileSync: vi.fn(() => "") }));
vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getGitHubAuthToken: vi.fn(() => null),
  getGitHubAuthTokens: vi.fn(() => ({})),
}));

import * as vscode from "vscode";
import { spawn } from "child_process";
import { IpcClientBase } from "../../src/services/IpcClientBase";

class TestableIpcClient extends IpcClientBase {
  constructor() {
    super();
  }
}

const WORKSPACE = "/workspaces/acme";

function setWorkspaceFolders(paths: string[] | undefined): void {
  (vscode.workspace as { workspaceFolders?: unknown }).workspaceFolders = paths?.map((p) => ({
    uri: { fsPath: p },
    name: p,
    index: 0,
  }));
}

describe("IpcClientBase.spawnProcess cwd (#1913)", () => {
  let client: TestableIpcClient;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
    client = new TestableIpcClient();
  });

  afterEach(() => {
    client.dispose();
    setWorkspaceFolders(undefined);
    delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
  });

  it("spawns the daemon in the workspace it is told to serve", async () => {
    setWorkspaceFolders([WORKSPACE]);

    await client.start();

    const options = vi.mocked(spawn).mock.calls[0]?.[2] as { cwd?: string } | undefined;
    expect(options?.cwd).toBe(WORKSPACE);
    const args = vi.mocked(spawn).mock.calls[0]?.[1] as string[];
    expect(args).toContain("--workspace");
  });

  // With no workspace there is no better directory to name, so the spawn keeps
  // today's inherit-the-parent behaviour rather than inventing one.
  it("omits cwd when no workspace root resolved", async () => {
    setWorkspaceFolders(undefined);

    await client.start();

    const options = vi.mocked(spawn).mock.calls[0]?.[2] as { cwd?: string } | undefined;
    expect(options && "cwd" in options).toBe(false);
  });
});
