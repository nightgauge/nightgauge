/**
 * The daemon is told which folders the window has open (#2335 review), so its
 * platform agent declares the workspace the extension's own agent declares.
 * The extension resolves that workspace from the window's first folder, and
 * from every folder of a multi-root window with no manifest; `--workspace`
 * is the first folder with a project config, which can be another one.
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
import { IpcClientBase, WINDOW_FOLDERS_ENV_VAR } from "../../src/services/IpcClientBase";

class TestableIpcClient extends IpcClientBase {
  constructor() {
    super();
  }
}

function setWorkspaceFolders(paths: string[] | undefined): void {
  (vscode.workspace as { workspaceFolders?: unknown }).workspaceFolders = paths?.map((p, i) => ({
    uri: { fsPath: p },
    name: p,
    index: i,
  }));
}

function spawnedEnv(): Record<string, string | undefined> {
  const options = vi.mocked(spawn).mock.calls[0]?.[2] as
    { env?: Record<string, string | undefined> } | undefined;
  return options?.env ?? {};
}

describe("IpcClientBase.spawnProcess window folders (#2335)", () => {
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
    delete process.env[WINDOW_FOLDERS_ENV_VAR];
  });

  it("hands the daemon every window folder, in window order", async () => {
    setWorkspaceFolders(["/w/acme", "/w/acme/api", "/w/acme/web"]);

    await client.start();

    expect(WINDOW_FOLDERS_ENV_VAR).toBe("NIGHTGAUGE_WINDOW_FOLDERS");
    expect(JSON.parse(spawnedEnv()[WINDOW_FOLDERS_ENV_VAR] ?? "null")).toEqual([
      "/w/acme",
      "/w/acme/api",
      "/w/acme/web",
    ]);
  });

  it("hands over no folders, not an inherited value, when the window has none", async () => {
    process.env[WINDOW_FOLDERS_ENV_VAR] = '["/somewhere/else"]';
    setWorkspaceFolders(undefined);

    await client.start();

    expect(WINDOW_FOLDERS_ENV_VAR in spawnedEnv()).toBe(false);
  });
});
