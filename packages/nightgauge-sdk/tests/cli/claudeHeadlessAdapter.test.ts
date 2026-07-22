import { describe, it, expect } from "vitest";
import { ClaudeHeadlessAdapter } from "../../src/cli/adapters/ClaudeHeadlessAdapter.js";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";

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

describe("ClaudeHeadlessAdapter", () => {
  const adapter = new ClaudeHeadlessAdapter();

  it("should have correct identity fields", () => {
    expect(adapter.name).toBe("claude-headless");
    expect(adapter.displayName).toBe("Claude Headless");
    expect(adapter.cliCommand).toBe("claude");
  });

  it("declares the native-workflow orchestration capability", () => {
    expect(adapter.getOrchestrationCapability()).toBe("native-workflow");
  });

  it("should not require a direct API key", () => {
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });

  it("should return print-mode default args", () => {
    expect(adapter.getDefaultArgs()).toEqual(["--print", "--output-format", "text"]);
  });

  it("should pass auth when CLI is installed and authenticated", async () => {
    const runner = createRunner({
      "claude --version": { code: 0, stdout: "claude 1.0.0\n" },
      "claude auth status": { code: 0, stdout: "Authenticated\n" },
    });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  it("should pass auth without a runner (SDK direct usage)", async () => {
    const result = await adapter.validateAuth({});
    expect(result).toBe("passed");
  });

  it("should fail when CLI is not installed", async () => {
    const runner = createRunner({
      "claude --version": { code: 1, stderr: "command not found" },
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /not installed or not in PATH/
    );
  });

  it("should fail when CLI is installed but not authenticated", async () => {
    const runner = createRunner({
      "claude --version": { code: 0, stdout: "claude 1.0.0\n" },
      "claude auth status": { code: 1, stderr: "Not logged in" },
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /not authenticated/
    );
  });

  it("should fail when auth status times out (exit code 124)", async () => {
    const runner = createRunner({
      "claude --version": { code: 0, stdout: "claude 1.0.0\n" },
      "claude auth status": { code: 124, stderr: "" },
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(/timed out/);
  });
});
