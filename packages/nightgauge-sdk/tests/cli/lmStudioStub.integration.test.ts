/**
 * Stub provider — LmStudioAdapter integration (real subprocess)
 *
 * Spawns the real Go `stub-provider` binary (`go run ./cmd/stub-provider
 * --script tool-edit-stop`), a deterministic, loopback-only, scripted
 * OpenAI-compatible server (see internal/stubprovider). It reads the
 * `base_url` the process prints on stdout, points `LmStudioAdapter` at it via
 * `NIGHTGAUGE_LM_STUDIO_BASE_URL`, and drives one streamed chat completion
 * end to end. This proves the stub works for any base-URL adapter, not just
 * an experimental OpenCode stage.
 *
 * Per the workspace rule on background processes: the child's PID is
 * captured at spawn, killed in afterAll, and its death is verified — never
 * left to `jobs` or an implicit reap.
 *
 * Requires the Go toolchain on PATH; skips (does not fail) when absent.
 *
 * @see Issue #1618 - deterministic OpenAI-compatible stub provider
 */

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { spawn, execSync, type ChildProcessWithoutNullStreams } from "node:child_process";
import net from "node:net";
import path from "node:path";
import { LmStudioAdapter } from "../../src/cli/adapters/LmStudioAdapter.js";

const repoRoot = path.resolve(__dirname, "../../../..");

function isGoAvailable(): boolean {
  try {
    execSync("go version", { stdio: "pipe", cwd: repoRoot });
    return true;
  } catch {
    return false;
  }
}

interface StubProvider {
  child: ChildProcessWithoutNullStreams;
  pid: number;
  baseUrl: string;
}

/**
 * Spawn the stub-provider, capture its PID, and resolve once it prints
 * base_url. `go run` builds a temporary binary and runs it as a child of
 * itself, so the spawned process is detached into its own process group:
 * killing only the direct child's PID leaves that grandchild binary
 * running as an orphan. Cleanup below signals the whole group instead.
 */
function startStubProvider(script: string, timeoutMs = 60_000): Promise<StubProvider> {
  return new Promise((resolve, reject) => {
    const child = spawn(
      "go",
      ["run", "./cmd/stub-provider", "--script", script, "--listen", "127.0.0.1:0"],
      { cwd: repoRoot, stdio: ["ignore", "pipe", "pipe"], detached: true }
    );

    let stdout = "";
    let stderr = "";
    let settled = false;

    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      child.kill("SIGKILL");
      reject(new Error(`stub-provider: no base_url within ${timeoutMs}ms; stderr: ${stderr}`));
    }, timeoutMs);

    child.stdout.on("data", (chunk: Buffer) => {
      stdout += chunk.toString();
      const match = stdout.match(/\{"base_url":"([^"]+)"\}/);
      if (match && !settled) {
        settled = true;
        clearTimeout(timer);
        resolve({ child, pid: child.pid as number, baseUrl: match[1] });
      }
    });
    child.stderr.on("data", (chunk: Buffer) => {
      stderr += chunk.toString();
    });
    child.on("error", (err) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(err);
    });
    child.on("exit", (code) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new Error(`stub-provider: exited early with code ${code}; stderr: ${stderr}`));
    });
  });
}

function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** Sends signal to the whole process group led by pid (negative pid), not just pid itself. */
function killGroup(pid: number, signal: NodeJS.Signals): void {
  try {
    process.kill(-pid, signal);
  } catch {
    // Already gone, or never became a group leader — fall back to direct kill.
    try {
      process.kill(pid, signal);
    } catch {
      /* already dead */
    }
  }
}

async function waitUntilDead(pid: number, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!isProcessAlive(pid)) return true;
    await new Promise((r) => setTimeout(r, 20));
  }
  return !isProcessAlive(pid);
}

/**
 * True once nothing accepts a connection on the stub's port, which proves the
 * real listening process, not just the `go run` wrapper, is gone.
 *
 * This is a bare TCP connect, deliberately not `fetch` (#1929). A poll that
 * connects in the listener's last moments gets a socket the peer resets
 * before the first write. undici's `writeH1` then calls `setTypeOfService` on
 * it, which throws `EINVAL` from inside undici's own write loop. The error
 * bypasses the awaited promise, so it reaches the process as an uncaught
 * exception and fails a green suite. A dedicated keep-alive-free dispatcher
 * does not avoid it, because every fresh connection takes that write path.
 */
function isStubUnreachable(baseUrl: string): Promise<boolean> {
  const { hostname, port } = new URL(baseUrl);
  return new Promise((resolve) => {
    const socket = net.connect({ host: hostname, port: Number(port) });
    const settle = (unreachable: boolean) => {
      socket.destroy();
      resolve(unreachable);
    };
    socket.setTimeout(300);
    socket.once("connect", () => settle(false));
    socket.once("timeout", () => settle(false));
    socket.once("error", () => settle(true));
  });
}

async function waitUntilUnreachable(baseUrl: string, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await isStubUnreachable(baseUrl)) return true;
    await new Promise((r) => setTimeout(r, 20));
  }
  return isStubUnreachable(baseUrl);
}

describe.skipIf(!isGoAvailable())(
  "stub-provider — LmStudioAdapter integration (real subprocess)",
  () => {
    let stub: StubProvider;
    let savedBaseUrl: string | undefined;
    let savedModel: string | undefined;

    beforeAll(async () => {
      savedBaseUrl = process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL;
      savedModel = process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
      stub = await startStubProvider("tool-edit-stop");
    }, 60_000);

    afterAll(async () => {
      if (savedBaseUrl !== undefined) {
        process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL = savedBaseUrl;
      } else {
        delete process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL;
      }
      if (savedModel !== undefined) {
        process.env.NIGHTGAUGE_LM_STUDIO_MODEL = savedModel;
      } else {
        delete process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
      }

      if (!stub) return;
      const { pid, baseUrl } = stub;
      killGroup(pid, "SIGTERM");
      let unreachable = await waitUntilUnreachable(baseUrl, 2_000);
      let dead = await waitUntilDead(pid, 2_000);
      if (!unreachable || !dead) {
        killGroup(pid, "SIGKILL");
        unreachable = await waitUntilUnreachable(baseUrl, 2_000);
        dead = await waitUntilDead(pid, 2_000);
      }
      expect(dead).toBe(true);
      expect(unreachable).toBe(true);
    }, 10_000);

    it("streams a chat completion from the stub and reports non-zero usage tokens", async () => {
      process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL = stub.baseUrl;
      process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "stub/stub-model";

      const adapter = new LmStudioAdapter();
      const queryFn = await adapter.createQueryFunction();

      const messages: Array<Record<string, unknown>> = [];
      for await (const msg of queryFn({ prompt: "edit calc.py" })) {
        messages.push(msg as unknown as Record<string, unknown>);
      }

      const result = messages.find((m) => m.type === "result");
      expect(result).toBeDefined();
      const usage = result!.usage as Record<string, number>;
      expect(usage.input_tokens + usage.output_tokens).toBeGreaterThan(0);
    });
  }
);
