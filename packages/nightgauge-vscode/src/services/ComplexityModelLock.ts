import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import * as path from "node:path";
import { ComplexityModelService } from "@nightgauge/sdk";
import { BinaryResolver } from "./BinaryResolver";

const READY_TIMEOUT_MS = 30_000;
const EXIT_TIMEOUT_MS = 5_000;
const STDERR_LIMIT = 4_096;

type CommitComplexityModel = (content: string) => Promise<void>;

interface BrokerExit {
  code: number | null;
  signal: NodeJS.Signals | null;
  error?: Error;
}

export interface ComplexityModelLockDeps {
  resolveBinary: () => Promise<string | null>;
  spawnLock: typeof spawn;
  readyTimeoutMs: number;
  exitTimeoutMs: number;
}

const defaultDeps: ComplexityModelLockDeps = {
  resolveBinary: () => BinaryResolver.fromVSCode().resolve(),
  spawnLock: spawn,
  readyTimeoutMs: READY_TIMEOUT_MS,
  exitTimeoutMs: EXIT_TIMEOUT_MS,
};

function waitForReady(
  child: ChildProcessWithoutNullStreams,
  timeoutMs: number,
  stderr: () => string
): Promise<void> {
  return new Promise((resolve, reject) => {
    let stdout = "";
    let ready = false;
    const timer = setTimeout(() => {
      reject(new Error(`timed out waiting for complexity-model lock${stderr()}`));
    }, timeoutMs);

    const finish = (error?: Error) => {
      clearTimeout(timer);
      if (error) reject(error);
      else resolve();
    };

    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk: string) => {
      stdout += chunk;
      if (!ready && stdout.split(/\r?\n/).includes("READY")) {
        ready = true;
        finish();
      }
    });
    child.once("error", (error) => finish(error));
    child.once("exit", (code, signal) => {
      if (!ready) {
        finish(
          new Error(
            `complexity-model lock broker exited before readiness (code=${code}, signal=${signal})${stderr()}`
          )
        );
      }
    });
  });
}

async function releaseAndReap(
  child: ChildProcessWithoutNullStreams,
  timeoutMs: number
): Promise<void> {
  if (child.pid === undefined || child.exitCode !== null || child.signalCode !== null) return;

  const waitForExit = () =>
    new Promise<boolean>((resolve) => {
      const timer = setTimeout(() => resolve(false), timeoutMs);
      const onExit = () => {
        clearTimeout(timer);
        resolve(true);
      };
      child.once("exit", onExit);
      if (child.exitCode !== null || child.signalCode !== null) {
        child.removeListener("exit", onExit);
        onExit();
      }
    });

  child.stdin.end();
  if (await waitForExit()) return;

  child.kill("SIGTERM");
  if (await waitForExit()) return;

  child.kill("SIGKILL");
  if (!(await waitForExit())) {
    throw new Error(`complexity-model lock broker did not exit after SIGKILL (pid=${child.pid})`);
  }
}

function observeExit(child: ChildProcessWithoutNullStreams): Promise<BrokerExit> {
  return new Promise((resolve) => {
    child.once("error", (error) => resolve({ code: null, signal: null, error }));
    child.once("exit", (code, signal) => resolve({ code, signal }));
  });
}

function assertSuccessfulExit(exit: BrokerExit, operation: string, stderr: () => string): void {
  if (exit.error) {
    throw new Error(`complexity-model broker failed to ${operation}: ${exit.error.message}`);
  }
  if (exit.code !== 0) {
    throw new Error(
      `complexity-model broker failed to ${operation} (code=${exit.code}, signal=${exit.signal})${stderr()}`
    );
  }
}

async function waitForBrokerExit(
  brokerExit: Promise<BrokerExit>,
  timeoutMs: number,
  operation: string
): Promise<BrokerExit> {
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      brokerExit,
      new Promise<never>((_, reject) => {
        timer = setTimeout(
          () => reject(new Error(`timed out waiting for complexity-model broker to ${operation}`)),
          timeoutMs
        );
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/** Run one SDK model transaction through the Go recorder's locked installer. */
async function withComplexityModelLock<T>(
  workspaceRoot: string,
  action: (commit: CommitComplexityModel) => Promise<T>,
  deps: ComplexityModelLockDeps = defaultDeps
): Promise<T> {
  const binary = await deps.resolveBinary();
  if (!binary) {
    throw new Error("nightgauge binary not found; cannot lock the shared complexity model");
  }

  const child = deps.spawnLock(binary, ["outcome", "lock", "--workdir", workspaceRoot], {
    cwd: workspaceRoot,
    stdio: "pipe",
  });
  let stderrText = "";
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk: string) => {
    stderrText = (stderrText + chunk).slice(-STDERR_LIMIT);
  });
  child.stdin.on("error", () => {});
  const stderr = () => (stderrText ? `: ${stderrText.trim()}` : "");
  const brokerExit = observeExit(child);
  let committed = false;

  try {
    await waitForReady(child, deps.readyTimeoutMs, stderr);
    const commit: CommitComplexityModel = async (content) => {
      if (committed || child.stdin.writableEnded) {
        throw new Error("complexity-model transaction already committed");
      }
      child.stdin.end(content, "utf8");
      const exit = await waitForBrokerExit(brokerExit, deps.exitTimeoutMs, "commit transaction");
      assertSuccessfulExit(exit, "commit transaction", stderr);
      committed = true;
    };

    const value = await action(commit);
    if (!committed) {
      child.stdin.end();
      const exit = await waitForBrokerExit(brokerExit, deps.exitTimeoutMs, "release transaction");
      assertSuccessfulExit(exit, "release transaction", stderr);
    }
    return value;
  } finally {
    if (!child.stdin.writableEnded) child.stdin.end();
    await releaseAndReap(child, deps.exitTimeoutMs);
  }
}

/** Run one correctly rooted SDK model transaction through the Go broker. */
export async function withComplexityModelService<T>(
  workspaceRoot: string,
  action: (modelService: ComplexityModelService) => Promise<T>,
  deps: ComplexityModelLockDeps = defaultDeps
): Promise<T> {
  return withComplexityModelLock(
    workspaceRoot,
    async (commit) =>
      action(
        new ComplexityModelService(
          path.join(workspaceRoot, ".nightgauge", "complexity-model.yaml"),
          commit
        )
      ),
    deps
  );
}
