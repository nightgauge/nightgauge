import { spawn } from "node:child_process";
import { describe, expect, it, vi } from "vitest";
import { ComplexityModelService } from "@nightgauge/sdk";
import {
  withComplexityModelService,
  type ComplexityModelLockDeps,
} from "../../src/services/ComplexityModelLock";

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

describe("withComplexityModelService", () => {
  it("holds the broker until the transaction finishes and reaps it", async () => {
    const { deps, spawnLock } = depsForScript(
      `let input = "";
       console.log("READY");
       process.stdin.setEncoding("utf8");
       process.stdin.on("data", chunk => input += chunk);
       process.stdin.on("end", () => process.exit(input.includes('schema_version: "1.0"') ? 0 : 4));`
    );

    const workspaceRoot = process.cwd();
    const value = await withComplexityModelService(
      workspaceRoot,
      async (modelService) => {
        expect(modelService.getModelPath()).toBe(
          `${workspaceRoot}/.nightgauge/complexity-model.yaml`
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

    await expect(withComplexityModelService(process.cwd(), async () => 42, deps)).rejects.toThrow(
      "nightgauge binary not found"
    );
  });

  it("rejects when the broker exits before acquiring the lock", async () => {
    const { deps } = depsForScript(`process.stderr.write("lock failed"); process.exit(2);`);

    await expect(withComplexityModelService(process.cwd(), async () => 42, deps)).rejects.toThrow(
      /exited before readiness.*lock failed/
    );
  });

  it("times out and reaps a broker that never reports readiness", async () => {
    const { deps, spawnLock } = depsForScript(`setInterval(() => {}, 1000);`);
    deps.readyTimeoutMs = 25;
    deps.exitTimeoutMs = 25;

    await expect(withComplexityModelService(process.cwd(), async () => 42, deps)).rejects.toThrow(
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
        process.cwd(),
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

    await expect(withComplexityModelService(process.cwd(), async () => 42, deps)).rejects.toThrow(
      /timed out waiting for complexity-model broker to release transaction/
    );
  });

  it("cannot report a committed mutation after the broker loses the lock", async () => {
    const { deps } = depsForScript(`console.log("READY"); setTimeout(() => process.exit(3), 10);`);
    let mutationReported = false;

    await expect(
      withComplexityModelService(
        process.cwd(),
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
});
