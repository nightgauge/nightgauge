import { spawn } from "node:child_process";
import { EventEmitter } from "node:events";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { PassThrough } from "node:stream";
import { afterAll, describe, expect, it, vi } from "vitest";
import { ComplexityModelService } from "@nightgauge/sdk";
import {
  withComplexityModelService,
  type ComplexityModelLockDeps,
} from "../../src/services/ComplexityModelLock";
import { fakeCloneLayout } from "../helpers/cloneLayout";

// A temp root with a fixed layout, so nothing resolves against the real
// checkout. The model is per checkout (ADR-024 § 7).
const ROOT = fs.mkdtempSync(path.join(os.tmpdir(), "ng-complexity-lock-"));
const LAYOUT = fakeCloneLayout(ROOT);
afterAll(() => fs.rmSync(ROOT, { recursive: true, force: true }));

function depsForScript(script: string): {
  deps: ComplexityModelLockDeps;
  spawnLock: ReturnType<typeof vi.fn>;
} {
  const spawnLock = vi.fn((_binary, _args, options) => spawn("node", ["-e", script], options));
  return {
    deps: {
      resolveBinary: async () => "/fake/nightgauge",
      spawnLock: spawnLock as unknown as typeof spawn,
      readyTimeoutMs: 1_000,
      exitTimeoutMs: 1_000,
    },
    spawnLock,
  };
}

/**
 * A broker that reports readiness, then ignores EOF and SIGTERM. Its exit
 * after SIGKILL is observed `exitDelayMs` later, or never when null: the delay
 * Node takes to see a killed process exit grows under load (#2356).
 */
function brokerSlowToReap(exitDelayMs: number | null) {
  const child = Object.assign(new EventEmitter(), {
    stdin: new PassThrough(),
    stdout: new PassThrough(),
    stderr: new PassThrough(),
    pid: 4242,
    exitCode: null as number | null,
    signalCode: null as NodeJS.Signals | null,
    kill: vi.fn((signal: NodeJS.Signals) => {
      if (signal === "SIGKILL" && exitDelayMs !== null) {
        setTimeout(() => {
          child.signalCode = "SIGKILL";
          child.emit("exit", null, "SIGKILL");
        }, exitDelayMs);
      }
      return true;
    }),
  });
  setImmediate(() => child.stdout.write("READY\n"));
  return child;
}

function depsForChild(child: ReturnType<typeof brokerSlowToReap>): ComplexityModelLockDeps {
  return {
    resolveBinary: async () => "/fake/nightgauge",
    spawnLock: vi.fn(() => child) as unknown as typeof spawn,
    readyTimeoutMs: 1_000,
    exitTimeoutMs: 25,
  };
}

describe("withComplexityModelService", () => {
  it("holds the broker until the transaction finishes and reaps it", async () => {
    const { deps, spawnLock } = depsForScript(
      `let input = "";
       console.log("READY");
       process.stdin.setEncoding("utf8");
       process.stdin.on("data", chunk => input += chunk);
       process.stdin.on("end", () => process.exit(input.includes('schema_version: "1.0"') ? 0 : 4));`
    );

    const workspaceRoot = ROOT;
    const value = await withComplexityModelService(
      workspaceRoot,
      async (modelService) => {
        expect(modelService.getModelPath()).toBe(
          path.join(LAYOUT.checkout, "complexity-model.yaml")
        );
        await modelService.save(ComplexityModelService.createBootstrapModel());
        return 42;
      },
      deps
    );

    expect(value).toBe(42);
    expect(spawnLock).toHaveBeenCalledWith(
      "/fake/nightgauge",
      ["outcome", "lock", "--workdir", workspaceRoot],
      { cwd: workspaceRoot, stdio: "pipe" }
    );
  });

  it("fails closed when no Go binary can provide the shared lock", async () => {
    const { deps } = depsForScript("");
    deps.resolveBinary = async () => null;

    await expect(withComplexityModelService(ROOT, async () => 42, deps)).rejects.toThrow(
      "nightgauge binary not found"
    );
  });

  it("rejects when the broker exits before acquiring the lock", async () => {
    const { deps } = depsForScript(`process.stderr.write("lock failed"); process.exit(2);`);

    await expect(withComplexityModelService(ROOT, async () => 42, deps)).rejects.toThrow(
      /exited before readiness.*lock failed/
    );
  });

  it("times out and reaps a broker that never reports readiness", async () => {
    const { deps, spawnLock } = depsForScript(`setInterval(() => {}, 1000);`);
    deps.readyTimeoutMs = 25;
    deps.exitTimeoutMs = 25;

    await expect(withComplexityModelService(ROOT, async () => 42, deps)).rejects.toThrow(
      /timed out waiting for complexity-model lock/
    );
    const child = spawnLock.mock.results[0].value;
    expect(child.exitCode !== null || child.signalCode !== null).toBe(true);
  });

  it("releases and reaps the broker when model computation fails", async () => {
    const { deps } = depsForScript(
      `console.log("READY"); process.stdin.resume(); process.stdin.on("end", () => process.exit(0));`
    );

    await expect(
      withComplexityModelService(
        ROOT,
        async () => {
          throw new Error("model computation failed");
        },
        deps
      )
    ).rejects.toThrow("model computation failed");
  });

  it("times out and reaps a broker that ignores EOF after readiness", async () => {
    const { deps } = depsForScript(
      `console.log("READY"); process.stdin.resume(); process.stdin.on("end", () => setInterval(() => {}, 1000));`
    );
    deps.exitTimeoutMs = 25;

    await expect(withComplexityModelService(ROOT, async () => 42, deps)).rejects.toThrow(
      /timed out waiting for complexity-model broker to release transaction/
    );
  });

  it("cannot report a committed mutation after the broker loses the lock", async () => {
    const { deps } = depsForScript(`console.log("READY"); setTimeout(() => process.exit(3), 10);`);
    let mutationReported = false;

    await expect(
      withComplexityModelService(
        ROOT,
        async (modelService) => {
          await new Promise((resolve) => setTimeout(resolve, 30));
          await modelService.save(ComplexityModelService.createBootstrapModel());
          mutationReported = true;
        },
        deps
      )
    ).rejects.toThrow(/failed to commit transaction.*code=3/);
    expect(mutationReported).toBe(false);
  });

  // #2356: the wait after SIGKILL does not shrink with exitTimeoutMs, and a
  // reap failure never replaces the transaction's own error.
  it("keeps the transaction's error when the killed broker's exit is observed late", async () => {
    const child = brokerSlowToReap(80);

    await expect(
      withComplexityModelService(ROOT, async () => 42, depsForChild(child))
    ).rejects.toThrow(/timed out waiting for complexity-model broker to release transaction/);
    expect(child.kill.mock.calls.map(([signal]) => signal)).toEqual(["SIGTERM", "SIGKILL"]);
    expect(child.signalCode).toBe("SIGKILL");
  });

  it("never replaces the transaction's error with a broker that outlives SIGKILL", async () => {
    const child = brokerSlowToReap(null);
    const deps = { ...depsForChild(child), killExitTimeoutMs: 30 };

    await expect(withComplexityModelService(ROOT, async () => 42, deps)).rejects.toThrow(
      /timed out waiting for complexity-model broker to release transaction/
    );
    expect(child.kill).toHaveBeenCalledWith("SIGKILL");
  });
});
