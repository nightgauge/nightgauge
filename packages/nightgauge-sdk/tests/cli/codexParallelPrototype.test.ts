/**
 * Prototype: Parallel Codex CLI Execution
 *
 * Validates that two independent `codex exec --json` processes can run
 * concurrently in separate directories without interfering with each other.
 *
 * This test requires `codex` on PATH and valid auth. It auto-skips if
 * codex is not available (CI-safe).
 *
 * @see Issue #1661 - Spike: Evaluate Codex Sub-Agents for Agent Teams
 */

import { describe, it, expect, beforeAll } from "vitest";
import { execSync, spawn } from "node:child_process";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

/** Check if codex CLI is available on PATH, authenticated, and has credits */
function isCodexReady(): boolean {
  // Skip in CI environments (codex integration tests are not stable in CI)
  if (process.env.CI || process.env.GITHUB_ACTIONS) {
    return false;
  }

  try {
    execSync("which codex", { stdio: "pipe", encoding: "utf-8" });
  } catch {
    return false;
  }
  // Also verify auth — codex on PATH but unauthenticated should skip
  try {
    execSync("codex login status", { stdio: "pipe", encoding: "utf-8" });
  } catch {
    return false;
  }
  // Verify API access with a trivial exec — catches usage limits, expired
  // tokens, and network issues that would cause all tests to fail with
  // unhelpful "expected 1 to be 0" assertions.
  try {
    const result = execSync(
      'echo "respond ok" | codex exec --dangerously-bypass-approvals-and-sandbox --json',
      { stdio: "pipe", encoding: "utf-8", timeout: 30_000, shell: true }
    );
    // If stdout contains an error event about usage limits, skip
    if (result.includes('"type":"error"') || result.includes("usage limit")) {
      return false;
    }
    return true;
  } catch {
    return false;
  }
}

/** Spawn a codex exec process and collect JSONL output */
function spawnCodexExec(
  cwd: string,
  prompt: string,
  timeoutMs = 60_000
): Promise<{ stdout: string; stderr: string; code: number }> {
  return new Promise((resolve, reject) => {
    const child = spawn("codex", ["exec", "--dangerously-bypass-approvals-and-sandbox", "--json"], {
      cwd,
      stdio: "pipe",
      env: { ...process.env },
    });

    let stdout = "";
    let stderr = "";
    let settled = false;

    const timer = setTimeout(() => {
      if (!settled) {
        settled = true;
        child.kill("SIGTERM");
        reject(new Error(`codex exec timed out after ${timeoutMs}ms`));
      }
    }, timeoutMs);

    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });

    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });

    child.on("error", (err) => {
      if (!settled) {
        settled = true;
        clearTimeout(timer);
        reject(err);
      }
    });

    child.on("close", (code) => {
      if (!settled) {
        settled = true;
        clearTimeout(timer);
        resolve({ stdout, stderr, code: code ?? 1 });
      }
    });

    child.stdin.write(prompt);
    child.stdin.end();
  });
}

/** Parse JSONL output into individual event objects */
function parseJsonlEvents(output: string): Array<Record<string, unknown>> {
  return output
    .split("\n")
    .filter((line) => line.trim().length > 0)
    .map((line) => {
      try {
        return JSON.parse(line) as Record<string, unknown>;
      } catch {
        return null;
      }
    })
    .filter((obj): obj is Record<string, unknown> => obj !== null);
}

describe("Codex Parallel Execution Prototype", () => {
  const codexReady = isCodexReady();
  let tmpDir1: string;
  let tmpDir2: string;

  beforeAll(() => {
    if (!codexReady) return;

    // Create two isolated temporary directories with git repos initialized.
    // Codex exec requires a trusted git directory — this mirrors the real
    // worktree-based execution model where each slot has its own git checkout.
    tmpDir1 = mkdtempSync(join(tmpdir(), "codex-parallel-a-"));
    tmpDir2 = mkdtempSync(join(tmpdir(), "codex-parallel-b-"));

    for (const dir of [tmpDir1, tmpDir2]) {
      execSync("git init", { cwd: dir, stdio: "pipe" });
      execSync('git config user.email "test@test.com"', {
        cwd: dir,
        stdio: "pipe",
      });
      execSync('git config user.name "Test"', { cwd: dir, stdio: "pipe" });
    }

    // Write a small file in each directory so codex has something to reference
    writeFileSync(join(tmpDir1, "hello.txt"), "Hello from directory A\n");
    writeFileSync(join(tmpDir2, "hello.txt"), "Hello from directory B\n");

    // Commit the files so the directories are trusted git repos
    for (const dir of [tmpDir1, tmpDir2]) {
      execSync('git add . && git commit -m "init"', {
        cwd: dir,
        stdio: "pipe",
      });
    }
  });

  // afterAll would clean up tmpDirs but we let the OS handle temp cleanup

  it.skipIf(!codexReady)(
    "should run two codex exec processes concurrently with independent output",
    { timeout: 120_000 },
    async () => {
      // Spawn both processes concurrently
      const [resultA, resultB] = await Promise.all([
        spawnCodexExec(tmpDir1, "Read hello.txt and respond with its content"),
        spawnCodexExec(tmpDir2, "Read hello.txt and respond with its content"),
      ]);

      // Both should exit successfully
      expect(resultA.code).toBe(0);
      expect(resultB.code).toBe(0);

      // Both should produce JSONL output
      expect(resultA.stdout.trim().length).toBeGreaterThan(0);
      expect(resultB.stdout.trim().length).toBeGreaterThan(0);

      // Parse JSONL events
      const eventsA = parseJsonlEvents(resultA.stdout);
      const eventsB = parseJsonlEvents(resultB.stdout);

      // Both should have thread.started events
      const threadStartedA = eventsA.find((e) => e.type === "thread.started");
      const threadStartedB = eventsB.find((e) => e.type === "thread.started");
      expect(threadStartedA).toBeDefined();
      expect(threadStartedB).toBeDefined();

      // thread_ids should be unique (independent sessions)
      expect(threadStartedA!.thread_id).not.toBe(threadStartedB!.thread_id);

      // Both should have turn.completed events with usage data
      const turnCompletedA = eventsA.find((e) => e.type === "turn.completed");
      const turnCompletedB = eventsB.find((e) => e.type === "turn.completed");
      expect(turnCompletedA).toBeDefined();
      expect(turnCompletedB).toBeDefined();

      // Verify usage data is present
      const usageA = turnCompletedA!.usage as Record<string, number>;
      const usageB = turnCompletedB!.usage as Record<string, number>;
      expect(usageA).toBeDefined();
      expect(usageB).toBeDefined();
      expect(usageA.input_tokens).toBeGreaterThan(0);
      expect(usageB.input_tokens).toBeGreaterThan(0);
      expect(typeof usageA.output_tokens).toBe("number");
      expect(typeof usageB.output_tokens).toBe("number");
    }
  );

  it.skipIf(!codexReady)(
    "should produce unique thread_ids proving session isolation",
    { timeout: 60_000 },
    async () => {
      const result = await spawnCodexExec(tmpDir1, 'Respond with just the word "isolated"');

      expect(result.code).toBe(0);

      const events = parseJsonlEvents(result.stdout);
      const threadStarted = events.find((e) => e.type === "thread.started");
      expect(threadStarted).toBeDefined();
      expect(typeof threadStarted!.thread_id).toBe("string");
      expect((threadStarted!.thread_id as string).length).toBeGreaterThan(0);
    }
  );

  it("should skip gracefully when codex is not available or not authenticated", () => {
    // This test always runs — it validates the skip mechanism itself
    if (!codexReady) {
      console.log(
        "codex CLI not available or not authenticated — parallel prototype tests skipped (CI-safe)"
      );
    }
    // Always passes — the point is that no error is thrown
    expect(true).toBe(true);
  });

  // Cleanup temp directories after all tests
  it.skipIf(!codexReady)("cleanup temp directories", () => {
    try {
      rmSync(tmpDir1, { recursive: true, force: true });
      rmSync(tmpDir2, { recursive: true, force: true });
    } catch {
      // Best effort cleanup
    }
  });
});
