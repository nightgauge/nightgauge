/**
 * Binary Startup Tests — Real binary spawn verification
 *
 * Tests the real nightgauge Go binary: spawn → ipc.ready event path.
 * All tests are gated on NIGHTGAUGE_GO_BINARY_PATH being set, so they
 * gracefully skip when the binary hasn't been built locally.
 *
 * To run these tests locally:
 *   make build-cli
 *   NIGHTGAUGE_GO_BINARY_PATH=./bin/nightgauge \
 *     npx -w nightgauge-vscode vitest run tests/deployment/binary-startup.test.ts
 *
 * In CI, the binary is built via `make build-cli` and the env var is set
 * before this test suite runs.
 *
 * @see src/services/BinaryResolver.ts — Tier 2 env var resolution
 * @see internal/ipc/server_integration_test.go — Go-side harness (same pattern)
 * @see Issue #1939 — Extension deployment tests: verify binary bundled, resolves, connects
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { spawn, type ChildProcess } from "child_process";
import { mkdirSync, writeFileSync, rmSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { createInterface } from "readline";

// ---------------------------------------------------------------------------
// Gate: skip entire suite unless the binary is built and env var is set
// ---------------------------------------------------------------------------

const BINARY_PATH = process.env.NIGHTGAUGE_GO_BINARY_PATH;

// ---------------------------------------------------------------------------
// Test workspace helpers
// ---------------------------------------------------------------------------

function makeWorkspace(): string {
  const dir = join(
    tmpdir(),
    `binary-startup-test-${Date.now()}-${Math.random().toString(36).slice(2)}`
  );
  const configDir = join(dir, ".nightgauge");
  mkdirSync(configDir, { recursive: true });

  // Minimal config that satisfies the Go binary's startup validation.
  // Matches the pattern used by ipcTestHarness in server_integration_test.go.
  const configYAML = "project:\n  owner: test-org\n  number: 1\n";
  writeFileSync(join(configDir, "config.yaml"), configYAML, "utf8");
  return dir;
}

/**
 * Read the next newline-delimited line from the binary's stdout.
 * Resolves with the line string or rejects on timeout.
 */
function readNextLine(proc: ChildProcess, timeoutMs = 10_000): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const rl = createInterface({ input: proc.stdout! });

    const timer = setTimeout(() => {
      rl.close();
      reject(new Error(`Timed out waiting for binary output after ${timeoutMs}ms`));
    }, timeoutMs);

    rl.once("line", (line) => {
      clearTimeout(timer);
      rl.close();
      resolve(line);
    });
    rl.once("close", () => {
      clearTimeout(timer);
    });
  });
}

/**
 * Drain stdout until we receive an `ipc.ready` event.
 * Handles both newline-delimited JSON and buffered output.
 */
async function awaitReady(
  proc: ChildProcess,
  timeoutMs = 30_000
): Promise<{ event: string; data: { protocolVersion: number } }> {
  let stdoutData = "";
  let stderrData = "";

  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(
        new Error(
          `Timed out waiting for ipc.ready after ${timeoutMs}ms. stdout: ${stdoutData}. stderr: ${stderrData}`
        )
      );
    }, timeoutMs);

    // Listen to stdout data directly
    const onStdoutData = (chunk: Buffer) => {
      stdoutData += chunk.toString();
      // Check for ipc.ready in the accumulated data
      // Split by newlines and check each line
      const lines = stdoutData.split("\n");
      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const parsed = JSON.parse(line);
          if (parsed?.event === "ipc.ready") {
            clearTimeout(timer);
            proc.stdout?.removeListener("data", onStdoutData);
            proc.stderr?.removeListener("data", onStderrData);
            resolve(parsed);
            return;
          }
        } catch {
          // not JSON — skip (could be log output)
        }
      }
    };

    const onStderrData = (chunk: Buffer) => {
      stderrData += chunk.toString();
    };

    const onProcessExit = () => {
      clearTimeout(timer);
      reject(
        new Error(
          `Binary exited before emitting ipc.ready. stdout: ${stdoutData}. stderr: ${stderrData}`
        )
      );
    };

    proc.stdout?.on("data", onStdoutData);
    proc.stderr?.on("data", onStderrData);
    proc.on("exit", onProcessExit);
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe.skipIf(!BINARY_PATH)("binary startup — real binary", () => {
  let workDir: string;
  let proc: ChildProcess | null = null;

  beforeEach(() => {
    workDir = makeWorkspace();
    proc = null;
  });

  afterEach(async () => {
    // Terminate the binary if it is still running
    if (proc && !proc.killed) {
      try {
        proc.kill("SIGTERM");
        // Give it a moment to exit cleanly
        await new Promise<void>((resolve) => {
          proc!.once("exit", () => resolve());
          setTimeout(() => {
            if (proc && !proc.killed) proc.kill("SIGKILL");
            resolve();
          }, 2000);
        });
      } catch {
        // ignore cleanup errors
      }
    }
    proc = null;

    // Remove temp workspace
    try {
      rmSync(workDir, { recursive: true, force: true });
    } catch {
      // ignore
    }
  });

  // -------------------------------------------------------------------------
  // Version request (health check — no serve mode needed)
  // -------------------------------------------------------------------------

  it("binary responds to version request with exit code 0", async () => {
    await new Promise<void>((resolve, reject) => {
      const child = spawn(BINARY_PATH!, ["version"], {
        env: { ...process.env, GITHUB_TOKEN: "fake-token-for-test" },
      });

      let stderr = "";
      child.stderr?.on("data", (chunk: Buffer) => {
        stderr += chunk.toString();
      });

      const timer = setTimeout(() => {
        child.kill();
        reject(new Error("version command timed out after 5s"));
      }, 5000);

      child.on("exit", (code) => {
        clearTimeout(timer);
        if (code === 0) {
          resolve();
        } else {
          reject(new Error(`binary version exited with code ${code}. stderr: ${stderr}`));
        }
      });

      child.on("error", (err) => {
        clearTimeout(timer);
        reject(new Error(`Failed to spawn binary: ${err.message}`));
      });
    });
  });

  // -------------------------------------------------------------------------
  // Serve mode: ipc.ready event
  // -------------------------------------------------------------------------

  it("binary starts serve mode and emits ipc.ready event", async () => {
    proc = spawn(BINARY_PATH!, ["serve", "--workspace", workDir], {
      stdio: ["pipe", "pipe", "pipe"],
      env: {
        ...process.env,
        GITHUB_TOKEN: "fake-token-for-integration-test",
      },
    });

    // Track if process exits unexpectedly
    let exitCode: number | null = null;
    let exitSignal: string | null = null;
    proc.on("exit", (code, signal) => {
      exitCode = code;
      exitSignal = signal;
    });

    const ready = await awaitReady(proc);

    expect(ready.event).toBe("ipc.ready");
    expect(ready.data).toBeDefined();
    expect(typeof ready.data.protocolVersion).toBe("number");
    // Protocol version should be a positive integer (e.g. 1, 2, etc)
    expect(ready.data.protocolVersion).toBeGreaterThan(0);
  });

  // -------------------------------------------------------------------------
  // Serve mode: unknown method returns JSON error response
  // -------------------------------------------------------------------------

  it("binary handles unknown method with structured error response", async () => {
    proc = spawn(BINARY_PATH!, ["serve", "--workspace", workDir], {
      stdio: ["pipe", "pipe", "pipe"],
      env: {
        ...process.env,
        GITHUB_TOKEN: "fake-token-for-integration-test",
      },
    });

    // Wait for binary to be ready before sending requests
    await awaitReady(proc);

    // Send an unknown method request
    const requestId = 42;
    const request =
      JSON.stringify({
        id: requestId,
        method: "unknown.method.does.not.exist",
        params: {},
      }) + "\n";

    proc.stdin!.write(request);

    // Read the response line — unknown methods return an error response
    const responseLine = await readNextLine(proc);
    const response = JSON.parse(responseLine);

    expect(response.id).toBe(requestId);
    expect(response.error).toBeDefined();
    expect(typeof response.error.code).toBe("number");
    expect(typeof response.error.message).toBe("string");
  });
});
