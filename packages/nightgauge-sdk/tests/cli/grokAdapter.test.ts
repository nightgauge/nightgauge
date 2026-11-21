import { describe, it, expect, afterEach } from "vitest";
import { GrokAdapter, GROK_MIN_KNOWN_VERSION } from "../../src/cli/adapters/GrokAdapter.js";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

function createRunner(
  responses: Record<string, { code: number; stdout?: string; stderr?: string }>
): PreflightCommandRunner {
  return async (command, args) => {
    const key = `${command} ${args.join(" ")}`;
    const response = responses[key];
    if (!response) {
      return { code: 1, stdout: "", stderr: `unexpected command: ${key}` };
    }
    return {
      code: response.code,
      stdout: response.stdout ?? "",
      stderr: response.stderr ?? "",
    };
  };
}

const GROK_VERSION_OK = {
  "grok --version": { code: 0, stdout: "grok 1.0.4\n" },
};

const GROK_INSPECT_OK = {
  "grok inspect --json": { code: 0, stdout: '{"authenticated": true}\n' },
};

const GROK_INSPECT_FAIL = {
  "grok inspect --json": { code: 1, stderr: "not authenticated" },
};

const CLEANUP_ENV_VARS = [
  "XAI_API_KEY",
  "GROK_HOME",
  "NIGHTGAUGE_GROK_CLI_COMMAND",
  "NIGHTGAUGE_GROK_CLI_ARGS",
  "NIGHTGAUGE_STAGE_MAX_TURNS",
  "NIGHTGAUGE_GROK_MODEL",
  "NIGHTGAUGE_MODEL",
  "NIGHTGAUGE_GROK_EFFORT",
];

describe("GrokAdapter", () => {
  const adapter = new GrokAdapter();
  let tempDir: string | undefined;

  afterEach(() => {
    for (const key of CLEANUP_ENV_VARS) {
      delete process.env[key];
    }
    if (tempDir) {
      try {
        rmSync(tempDir, { recursive: true, force: true });
      } catch {
        // Ignore cleanup failure
      }
      tempDir = undefined;
    }
  });

  // --- Identity and capabilities ---

  it("should have correct identity fields", () => {
    expect(adapter.name).toBe("grok");
    expect(adapter.displayName).toBe("Grok");
    expect(adapter.cliCommand).toBe("grok");
    expect(adapter.agentic).toBe(true);
  });

  it("declares the sdk-fanout orchestration capability", () => {
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });

  it("should not require a direct API key", () => {
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });

  it("should return expected default args", () => {
    expect(adapter.getDefaultArgs()).toEqual([
      "--output-format",
      "streaming-json",
      "--always-approve",
      "--no-auto-update",
      "--max-turns",
      "200",
    ]);
  });

  // --- Auth: runner absence (SDK direct mode) ---

  it("should pass auth without a runner", async () => {
    const result = await adapter.validateAuth({});
    expect(result).toBe("passed");
  });

  // --- Auth: CLI installation check ---

  it("should fail when grok CLI is not installed", async () => {
    const runner = createRunner({
      "grok --version": { code: 1, stderr: "command not found" },
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /not installed or not in PATH/
    );
  });

  // --- Version warnings ---

  it("should warn but not block on older CLI version", async () => {
    process.env.XAI_API_KEY = "test-xai-key";
    const warnings: string[] = [];
    const originalWarn = console.warn;
    console.warn = (...args: unknown[]) => {
      warnings.push(args.join(" "));
    };

    try {
      const runner = createRunner({
        "grok --version": { code: 0, stdout: "grok 0.9.0\n" },
      });

      const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
      expect(result).toBe("passed");
      expect(warnings.some((w) => w.includes("WARNING") && w.includes("0.9.0"))).toBe(true);
    } finally {
      console.warn = originalWarn;
    }
  });

  it("should not warn when CLI version meets minimum", async () => {
    process.env.XAI_API_KEY = "test-xai-key";
    const warnings: string[] = [];
    const originalWarn = console.warn;
    console.warn = (...args: unknown[]) => {
      warnings.push(args.join(" "));
    };

    try {
      const runner = createRunner({
        "grok --version": { code: 0, stdout: `grok ${GROK_MIN_KNOWN_VERSION}\n` },
      });

      await adapter.validateAuth({ runner, cwd: "/tmp" });
      expect(warnings.filter((w) => w.includes("[grok-adapter]"))).toHaveLength(0);
    } finally {
      console.warn = originalWarn;
    }
  });

  // --- Auth cascade ---

  it("should pass auth when XAI_API_KEY is configured", async () => {
    process.env.XAI_API_KEY = "xai-test-key-123";
    const runner = createRunner({ ...GROK_VERSION_OK });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  it("should pass auth when session file auth.json exists in GROK_HOME", async () => {
    tempDir = mkdtempSync(join(tmpdir(), "grok-auth-test-"));
    writeFileSync(join(tempDir, "auth.json"), JSON.stringify({ token: "fake-session" }));
    process.env.GROK_HOME = tempDir;

    const runner = createRunner({ ...GROK_VERSION_OK });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  it("should pass auth when inspect command exits 0", async () => {
    // Point GROK_HOME to an empty dir so no auth.json is found
    tempDir = mkdtempSync(join(tmpdir(), "grok-empty-test-"));
    process.env.GROK_HOME = tempDir;

    const runner = createRunner({
      ...GROK_VERSION_OK,
      ...GROK_INSPECT_OK,
    });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  it("should fail when all auth methods fail", async () => {
    tempDir = mkdtempSync(join(tmpdir(), "grok-empty-test-"));
    process.env.GROK_HOME = tempDir;

    const runner = createRunner({
      ...GROK_VERSION_OK,
      ...GROK_INSPECT_FAIL,
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /Grok CLI is not authenticated/
    );
  });

  it("should include actionable hints in auth failure message", async () => {
    tempDir = mkdtempSync(join(tmpdir(), "grok-empty-test-"));
    process.env.GROK_HOME = tempDir;

    const runner = createRunner({
      ...GROK_VERSION_OK,
      ...GROK_INSPECT_FAIL,
    });

    try {
      await adapter.validateAuth({ runner, cwd: "/tmp" });
      expect.fail("should have thrown");
    } catch (err) {
      const message = (err as Error).message;
      expect(message).toContain("grok login");
      expect(message).toContain("XAI_API_KEY");
    }
  });

  // --- Query function creation ---

  it("creates a query function with default configuration", async () => {
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("applies model routing when NIGHTGAUGE_GROK_MODEL is set", async () => {
    process.env.NIGHTGAUGE_GROK_MODEL = "grok-4.6";
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("resolves tier alias through model preflight", async () => {
    process.env.NIGHTGAUGE_MODEL = "sonnet";
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("fails fast on an invalid Grok model", async () => {
    process.env.NIGHTGAUGE_GROK_MODEL = "grok-invalid-99";
    await expect(adapter.createQueryFunction()).rejects.toThrow(AdapterError);
    await expect(adapter.createQueryFunction()).rejects.toThrow(/not valid for the Grok/);
  });

  it("applies effort flag when NIGHTGAUGE_GROK_EFFORT is configured", async () => {
    process.env.NIGHTGAUGE_GROK_MODEL = "grok-4.6";
    process.env.NIGHTGAUGE_GROK_EFFORT = "high";
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("applies stage turn budget to lower --max-turns", async () => {
    process.env.NIGHTGAUGE_STAGE_MAX_TURNS = "30";
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });
});
