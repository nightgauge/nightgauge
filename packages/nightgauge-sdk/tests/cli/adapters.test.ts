/**
 * Adapter Constructor, Interface, and Error Handling Tests
 *
 * Validates that each adapter's constructor produces a valid ICliAdapter,
 * tests error paths for createQueryFunction and validateAuth, and ensures
 * multi-tool support invariants hold across all adapters.
 *
 * @see Issue #2275 - Add CLI adapter tests for multi-tool support validation
 */

import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { ClaudeSdkAdapter } from "../../src/cli/adapters/ClaudeSdkAdapter.js";
import { ClaudeHeadlessAdapter } from "../../src/cli/adapters/ClaudeHeadlessAdapter.js";
import { CodexAdapter } from "../../src/cli/adapters/CodexAdapter.js";
import { GeminiAdapter } from "../../src/cli/adapters/GeminiAdapter.js";
import { GeminiSdkAdapter } from "../../src/cli/adapters/GeminiSdkAdapter.js";
import { LmStudioAdapter } from "../../src/cli/adapters/LmStudioAdapter.js";
import { CopilotCliAdapter } from "../../src/cli/adapters/CopilotCliAdapter.js";
import type { ICliAdapter } from "../../src/cli/adapters/ICliAdapter.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

function allAdapters(): ICliAdapter[] {
  return [
    new ClaudeSdkAdapter(),
    new ClaudeHeadlessAdapter(),
    new CodexAdapter(),
    new GeminiAdapter(),
    new GeminiSdkAdapter(),
    new LmStudioAdapter(),
    new CopilotCliAdapter(),
  ];
}

// ---------------------------------------------------------------------------
// Constructor and identity tests
// ---------------------------------------------------------------------------

describe("adapter constructors", () => {
  it("each adapter can be instantiated without arguments", () => {
    for (const adapter of allAdapters()) {
      expect(adapter).toBeDefined();
      expect(adapter.name).toBeTruthy();
      expect(adapter.displayName).toBeTruthy();
      expect(adapter.cliCommand).toBeTruthy();
    }
  });

  it("each adapter name is a valid IncrediAdapter value", () => {
    const validNames = new Set([
      "claude-sdk",
      "claude-headless",
      "codex",
      "gemini",
      "gemini-sdk",
      "lm-studio",
      "copilot",
    ]);

    for (const adapter of allAdapters()) {
      expect(validNames.has(adapter.name)).toBe(true);
    }
  });

  it("all 7 adapters produce distinct names", () => {
    const adapters = allAdapters();
    const names = new Set(adapters.map((a) => a.name));
    expect(names.size).toBe(7);
  });
});

// ---------------------------------------------------------------------------
// getOrchestrationCapability and getDefaultArgs return type invariants
// ---------------------------------------------------------------------------

describe("adapter capability invariants", () => {
  it("getOrchestrationCapability returns a valid capability for every adapter", () => {
    const valid = ["native-workflow", "sdk-fanout"];

    for (const adapter of allAdapters()) {
      expect(valid).toContain(adapter.getOrchestrationCapability());
    }
  });

  it("getDefaultArgs returns string[] for every adapter", () => {
    for (const adapter of allAdapters()) {
      const args = adapter.getDefaultArgs();
      expect(Array.isArray(args)).toBe(true);
      for (const arg of args) {
        expect(typeof arg).toBe("string");
      }
    }
  });
});

// ---------------------------------------------------------------------------
// validateAuth error handling for CLI-based adapters
// ---------------------------------------------------------------------------

describe("CLI-based adapter validateAuth error handling", () => {
  const cliAdapters: Array<{ adapter: ICliAdapter; versionCmd: string }> = [
    {
      adapter: new ClaudeHeadlessAdapter(),
      versionCmd: "claude --version",
    },
    {
      adapter: new CodexAdapter(),
      versionCmd: "codex --version",
    },
    {
      adapter: new GeminiAdapter(),
      versionCmd: "gemini --version",
    },
    {
      adapter: new CopilotCliAdapter(),
      versionCmd: "copilot --version",
    },
  ];

  afterEach(() => {
    // Clean up env vars that Gemini/Copilot adapters check
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
    delete process.env.GOOGLE_GENAI_USE_VERTEXAI;
    delete process.env.GH_TOKEN;
    delete process.env.GITHUB_TOKEN;
    delete process.env.COPILOT_GITHUB_TOKEN;
  });

  it.each(cliAdapters.map((c) => ({ label: c.adapter.name, ...c })))(
    "$label: throws AdapterError when CLI binary is not installed",
    async ({ adapter, versionCmd }) => {
      const runner = createRunner({
        [versionCmd]: { code: 127, stderr: "command not found" },
      });

      await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    }
  );

  it.each(cliAdapters.map((c) => ({ label: c.adapter.name, ...c })))(
    '$label: returns "passed" without a runner (SDK direct usage)',
    async ({ adapter }) => {
      const result = await adapter.validateAuth({});
      expect(result).toBe("passed");
    }
  );
});

// ---------------------------------------------------------------------------
// SDK-based adapter validateAuth (always passes, no CLI required)
// ---------------------------------------------------------------------------

describe("SDK-based adapter validateAuth", () => {
  beforeEach(() => {
    process.env.ANTHROPIC_API_KEY = "sk-test";
    process.env.GEMINI_API_KEY = "test-gemini";
  });

  afterEach(() => {
    delete process.env.ANTHROPIC_API_KEY;
    delete process.env.GEMINI_API_KEY;
  });

  it('ClaudeSdkAdapter.validateAuth always returns "passed"', async () => {
    const adapter = new ClaudeSdkAdapter();
    await expect(adapter.validateAuth()).resolves.toBe("passed");
    await expect(adapter.validateAuth({})).resolves.toBe("passed");
  });

  it('GeminiSdkAdapter.validateAuth always returns "passed"', async () => {
    const adapter = new GeminiSdkAdapter();
    await expect(adapter.validateAuth()).resolves.toBe("passed");
    await expect(adapter.validateAuth({})).resolves.toBe("passed");
  });

  it('LmStudioAdapter.validateAuth always returns "passed"', async () => {
    const adapter = new LmStudioAdapter();
    await expect(adapter.validateAuth()).resolves.toBe("passed");
    await expect(adapter.validateAuth({})).resolves.toBe("passed");
  });
});

// ---------------------------------------------------------------------------
// createQueryFunction basic contract
// ---------------------------------------------------------------------------

describe("createQueryFunction contract", () => {
  it("ClaudeSdkAdapter.createQueryFunction resolves to a function", async () => {
    const adapter = new ClaudeSdkAdapter();
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("GeminiSdkAdapter.createQueryFunction rejects when SDK is not installed", async () => {
    const adapter = new GeminiSdkAdapter();
    // The SDK (@google/genai) is not installed in the test env
    await expect(adapter.createQueryFunction()).rejects.toThrow();
  });

  it("LmStudioAdapter.createQueryFunction rejects when model is not configured", async () => {
    const original = process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
    delete process.env.NIGHTGAUGE_LM_STUDIO_MODEL;

    try {
      await expect(new LmStudioAdapter().createQueryFunction()).rejects.toThrow(
        /NIGHTGAUGE_LM_STUDIO_MODEL/
      );
    } finally {
      if (original !== undefined) {
        process.env.NIGHTGAUGE_LM_STUDIO_MODEL = original;
      }
    }
  });

  it("CLI-based adapters return a callable function from createQueryFunction", async () => {
    // Headless, Codex, Gemini, Copilot all use createCliQueryFn which resolves synchronously
    const cliAdapters = [
      new ClaudeHeadlessAdapter(),
      new CodexAdapter(),
      new GeminiAdapter(),
      new CopilotCliAdapter(),
    ];

    for (const adapter of cliAdapters) {
      const queryFn = await adapter.createQueryFunction();
      expect(typeof queryFn).toBe("function");
    }
  });
});

// ---------------------------------------------------------------------------
// Multi-tool support: requiresDirectApiKey partitioning
// ---------------------------------------------------------------------------

describe("multi-tool requiresDirectApiKey partitioning", () => {
  it("exactly 2 adapters require a direct API key", () => {
    const adapters = allAdapters();
    const apiKeyAdapters = adapters.filter((a) => a.requiresDirectApiKey());
    expect(apiKeyAdapters).toHaveLength(2);
    expect(apiKeyAdapters.map((a) => a.name).sort()).toEqual(["claude-sdk", "gemini-sdk"]);
  });

  it("exactly 5 adapters do NOT require a direct API key", () => {
    const adapters = allAdapters();
    const noApiKeyAdapters = adapters.filter((a) => !a.requiresDirectApiKey());
    expect(noApiKeyAdapters).toHaveLength(5);
    expect(noApiKeyAdapters.map((a) => a.name).sort()).toEqual([
      "claude-headless",
      "codex",
      "copilot",
      "gemini",
      "lm-studio",
    ]);
  });
});
