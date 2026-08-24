/**
 * ipcRejection.classification.test.ts
 *
 * ADR-017 Decision 3 — the twelve bare `catch {}` blocks on the extension's
 * notify surface become classified log lines.
 *
 * "An error rejects the promise, so every existing try/catch at minimum logs
 * it" was FALSE and load-bearing: `PipelineStateService`'s stage-transition
 * catches were bare, non-logging blocks that fabricated local state and fired
 * `_onStateChanged`, so the UI showed a healthy run while the server refused
 * everything. That is how #304's corpus stayed empty for the life of the
 * product.
 *
 * REAL REJECTIONS, NOT HAND-AUTHORED SHAPES (#166). Every error under test is
 * produced by writing a JSON-RPC error frame — shaped exactly as
 * `Server.sendError` in internal/ipc/server.go writes it — into the client's
 * own line parser and catching what `call()` rejects with. A test that built
 * `new Error("IPC error -32602: run_closed")` by hand would pass even if
 * `IpcClientBase` stopped formatting errors that way, which is precisely the
 * coupling the classifier depends on.
 *
 * The `run_*` codes are INERT until step 4 (no verb refuses anything today);
 * these arms exist so the flip lands into a classifier that already knows
 * what each code means.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decision 3
 * @see src/services/ipcRejection.ts — implementation under test
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { EventEmitter } from "events";

const { execFileMock, mockRl } = vi.hoisted(() => {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { EventEmitter } = require("events") as typeof import("events");
  const rl = new EventEmitter() as any;
  rl.setMaxListeners(50);
  rl.close = vi.fn();
  const mock = vi.fn();
  const kCustomPromisify = Symbol.for("nodejs.util.promisify.custom");
  (mock as any)[kCustomPromisify] = (file: string, args: string[], opts: object) =>
    new Promise<{ stdout: string; stderr: string }>((resolve, reject) => {
      mock(file, args, opts, (err: Error | null, stdout: string, stderr: string) => {
        if (err) reject(err);
        else resolve({ stdout, stderr });
      });
    });
  return { execFileMock: mock, mockRl: rl };
});

let mockProc: any;

function makeMockProcess() {
  const proc = new EventEmitter() as any;
  proc.stdin = {
    writable: true,
    write: vi.fn((_data: string, cb?: (err?: Error | null) => void) => {
      cb?.();
      return true;
    }),
  };
  proc.stdout = new EventEmitter();
  proc.stderr = new EventEmitter();
  proc.killed = false;
  proc.pid = 4242;
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

vi.mock("../../src/utils/incrediConfig", () => ({
  getGitHubAuthToken: vi.fn(() => null),
  getGitHubAuthTokens: vi.fn(() => ({})),
}));

import * as vscode from "vscode";
import { IpcClientBase } from "../../src/services/IpcClientBase";
import {
  handleIpcRejection,
  type IpcRejectionClass,
  type RejectionLogger,
} from "../../src/services/ipcRejection";

class TestableIpcClient extends IpcClientBase {
  // IpcClientBase's constructor is protected to enforce the singleton; a test
  // double has to widen it to be instantiable directly.
  public constructor() {
    super();
  }
}

function makeLogger(): RejectionLogger & {
  lines: Array<{ level: string; message: string; meta?: Record<string, unknown> }>;
} {
  const lines: Array<{ level: string; message: string; meta?: Record<string, unknown> }> = [];
  return {
    lines,
    debug: (message, meta) => lines.push({ level: "debug", message, meta }),
    info: (message, meta) => lines.push({ level: "info", message, meta }),
    warn: (message, meta) => lines.push({ level: "warn", message, meta }),
  };
}

/**
 * Drive one `pipeline.*` call through the real client, answer it with a real
 * JSON-RPC error frame, and return whatever the promise rejected with.
 *
 * `code` and `message` are the two fields `Server.sendError` writes
 * (`Response{ID, Error: &RPCError{Code, Message}}`); nothing else is
 * synthesised, and the Error object handed back is constructed by
 * `IpcClientBase` itself.
 */
async function rejectionFromServerFrame(
  client: TestableIpcClient,
  code: number,
  message: string
): Promise<unknown> {
  const pending = client.call("pipeline.notifyStageTransition", { stage: "feature-dev" });
  const caught = pending.then(
    () => {
      throw new Error("expected the call to reject");
    },
    (err: unknown) => err
  );
  // The server answers request id 1 (the client numbers from 1 per instance).
  mockRl.emit("line", JSON.stringify({ id: 1, error: { code, message } }));
  return caught;
}

describe("handleIpcRejection — classification of real JSON-RPC refusals", () => {
  let client: TestableIpcClient;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockRl.removeAllListeners();
    const fs = await import("fs");
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue("");
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
    (vscode.workspace.getConfiguration as any).mockReturnValue({
      get: vi.fn(<T>(key: string, defaultValue?: T): T | undefined => {
        if (key === "binaryPath") return "" as unknown as T;
        if (key === "timeoutSeconds") return 30 as unknown as T;
        return defaultValue;
      }),
    });
    (vscode as any).EventEmitter = class WorkingEventEmitter<T = unknown> {
      private _listeners: ((e: T) => void)[] = [];
      event = (listener: (e: T) => void) => {
        this._listeners.push(listener);
        return { dispose: () => {} };
      };
      fire = (data: T) => {
        for (const l of this._listeners) l(data);
      };
      dispose = () => {};
    };
    process.env.NIGHTGAUGE_GO_BINARY_PATH = "/fake/nightgauge";
    client = new TestableIpcClient();
    await client.start();
  });

  afterEach(() => {
    client.dispose();
  });

  const cases: Array<{
    code: string;
    rpcCode: number;
    expected: IpcRejectionClass;
    level: string;
  }> = [
    { code: "run_closed", rpcCode: -32602, expected: "terminal-refusal", level: "debug" },
    { code: "run_not_found", rpcCode: -32602, expected: "ownership", level: "warn" },
    { code: "run_wrong_owner", rpcCode: -32602, expected: "ownership", level: "warn" },
    { code: "run_id_required", rpcCode: -32602, expected: "identity", level: "warn" },
    { code: "run_id_invalid", rpcCode: -32602, expected: "identity", level: "warn" },
  ];

  for (const c of cases) {
    it(`classifies ${c.code} as ${c.expected} and logs at ${c.level}`, async () => {
      const err = await rejectionFromServerFrame(
        client,
        c.rpcCode,
        `${c.code}: run 0195b2c3-d4e5-7f60-8a1b-2c3d4e5f6071 for issue #370`
      );
      const logger = makeLogger();

      const record = handleIpcRejection({
        method: "pipeline.notifyStageTransition",
        stage: "feature-dev",
        runId: "0195b2c3-d4e5-7f60-8a1b-2c3d4e5f6071",
        err,
        logger,
      });

      expect(record.classification).toBe(c.expected);
      expect(record.code).toBe(c.code);
      expect(record.rpcCode).toBe(c.rpcCode);
      expect(logger.lines).toHaveLength(1);
      expect(logger.lines[0].level).toBe(c.level);
      // Every line carries method + stage + runId + code, unconditionally.
      expect(logger.lines[0].meta).toMatchObject({
        method: "pipeline.notifyStageTransition",
        stage: "feature-dev",
        runId: "0195b2c3-d4e5-7f60-8a1b-2c3d4e5f6071",
        code: c.code,
        rpcCode: c.rpcCode,
      });
    });
  }

  it("classifies an ordinary server error as a transport failure", async () => {
    const err = await rejectionFromServerFrame(client, -32603, "runtime not found for issue 370");
    const logger = makeLogger();

    const record = handleIpcRejection({
      method: "pipeline.notifyComplete",
      runId: null,
      err,
      logger,
    });

    expect(record.classification).toBe("transport");
    expect(record.code).toBeNull();
    expect(record.rpcCode).toBe(-32603);
    expect(logger.lines[0].level).toBe("warn");
    expect(logger.lines[0].message).toContain("state not sent to Go");
  });

  it("classifies a dead-backend rejection (no JSON-RPC frame at all) as transport", async () => {
    const pending = client.call("pipeline.notifyStageProgress", {});
    const caught = pending.then(
      () => {
        throw new Error("expected the call to reject");
      },
      (err: unknown) => err
    );
    // Not a refusal: the process died with the request in flight, so
    // IpcClientBase rejects with its own message and there is no code to read.
    mockProc.emit("exit", 1);
    const err = await caught;
    const logger = makeLogger();

    const record = handleIpcRejection({
      method: "pipeline.notifyStageProgress",
      stage: "pr-merge",
      runId: null,
      err,
      logger,
    });

    expect(record.classification).toBe("transport");
    expect(record.rpcCode).toBeNull();
    expect(record.code).toBeNull();
    expect(logger.lines[0].level).toBe("warn");
  });

  it("never throws, whatever it is handed", () => {
    const logger = makeLogger();
    for (const value of [undefined, null, "a string", 42, { nope: true }]) {
      expect(() =>
        handleIpcRejection({ method: "pipeline.setPaused", runId: null, err: value, logger })
      ).not.toThrow();
    }
    expect(logger.lines).toHaveLength(5);
  });
});
