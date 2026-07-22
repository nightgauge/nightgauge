/**
 * Adapter Compliance Tests - Table-driven tests validating all 4 adapters
 * implement the ICliAdapter interface correctly.
 *
 * @see Issue #631 - Adapter integration tests
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { ClaudeSdkAdapter } from "../../src/cli/adapters/ClaudeSdkAdapter.js";
import { ClaudeHeadlessAdapter } from "../../src/cli/adapters/ClaudeHeadlessAdapter.js";
import { CodexAdapter } from "../../src/cli/adapters/CodexAdapter.js";
import { GeminiAdapter } from "../../src/cli/adapters/GeminiAdapter.js";
import { GeminiSdkAdapter } from "../../src/cli/adapters/GeminiSdkAdapter.js";
import { LmStudioAdapter } from "../../src/cli/adapters/LmStudioAdapter.js";
import { OllamaAdapter } from "../../src/cli/adapters/OllamaAdapter.js";
import { CopilotCliAdapter } from "../../src/cli/adapters/CopilotCliAdapter.js";
import type {
  ICliAdapter,
  IncrediAdapter,
  OrchestrationCapability,
} from "../../src/cli/adapters/ICliAdapter.js";

// ---------------------------------------------------------------------------
// Table-driven test data
// ---------------------------------------------------------------------------

interface AdapterTestCase {
  adapter: ICliAdapter;
  expected: {
    name: IncrediAdapter;
    displayName: string;
    cliCommand: string;
    requiresDirectApiKey: boolean;
    orchestrationCapability: OrchestrationCapability;
    agentic: boolean;
  };
}

const ADAPTER_TABLE: AdapterTestCase[] = [
  {
    adapter: new ClaudeSdkAdapter(),
    expected: {
      name: "claude-sdk",
      displayName: "Claude SDK",
      cliCommand: "claude",
      requiresDirectApiKey: true,
      agentic: true,
      orchestrationCapability: "native-workflow",
    },
  },
  {
    adapter: new ClaudeHeadlessAdapter(),
    expected: {
      name: "claude-headless",
      displayName: "Claude Headless",
      cliCommand: "claude",
      requiresDirectApiKey: false,
      agentic: true,
      orchestrationCapability: "native-workflow",
    },
  },
  {
    adapter: new CodexAdapter(),
    expected: {
      name: "codex",
      displayName: "Codex",
      cliCommand: "codex",
      requiresDirectApiKey: false,
      agentic: true,
      orchestrationCapability: "sdk-fanout",
    },
  },
  {
    adapter: new GeminiAdapter(),
    expected: {
      name: "gemini",
      displayName: "Gemini",
      cliCommand: "gemini",
      requiresDirectApiKey: false,
      agentic: true,
      orchestrationCapability: "sdk-fanout",
    },
  },
  {
    adapter: new GeminiSdkAdapter(),
    expected: {
      name: "gemini-sdk",
      displayName: "Gemini SDK",
      cliCommand: "gemini",
      requiresDirectApiKey: true,
      agentic: false,
      orchestrationCapability: "sdk-fanout",
    },
  },
  {
    adapter: new LmStudioAdapter(),
    expected: {
      name: "lm-studio",
      displayName: "LM Studio",
      cliCommand: "lm-studio",
      requiresDirectApiKey: false,
      agentic: false,
      orchestrationCapability: "sdk-fanout",
    },
  },
  {
    adapter: new OllamaAdapter(),
    expected: {
      name: "ollama",
      displayName: "Ollama",
      cliCommand: "ollama",
      requiresDirectApiKey: false,
      agentic: false,
      orchestrationCapability: "sdk-fanout",
    },
  },
  {
    adapter: new CopilotCliAdapter(),
    expected: {
      name: "copilot",
      displayName: "GitHub Copilot",
      cliCommand: "copilot",
      requiresDirectApiKey: false,
      agentic: true,
      orchestrationCapability: "sdk-fanout",
    },
  },
];

// ---------------------------------------------------------------------------
// Per-adapter compliance tests (table-driven)
// ---------------------------------------------------------------------------

describe("adapter compliance", () => {
  beforeEach(() => {
    // SDK adapters now require API keys even without a runner
    process.env.ANTHROPIC_API_KEY = "sk-test";
    process.env.GEMINI_API_KEY = "test-gemini";
  });

  afterEach(() => {
    delete process.env.ANTHROPIC_API_KEY;
    delete process.env.GEMINI_API_KEY;
  });

  describe.each(ADAPTER_TABLE.map((tc) => ({ label: tc.expected.name, ...tc })))(
    "$label",
    ({ adapter, expected }) => {
      it("name matches expected IncrediAdapter value", () => {
        expect(adapter.name).toBe(expected.name);
      });

      it("displayName is a non-empty string", () => {
        expect(typeof adapter.displayName).toBe("string");
        expect(adapter.displayName.length).toBeGreaterThan(0);
        expect(adapter.displayName).toBe(expected.displayName);
      });

      it("cliCommand is a non-empty string", () => {
        expect(typeof adapter.cliCommand).toBe("string");
        expect(adapter.cliCommand.length).toBeGreaterThan(0);
        expect(adapter.cliCommand).toBe(expected.cliCommand);
      });

      it("getOrchestrationCapability() returns the expected value", () => {
        const cap = adapter.getOrchestrationCapability();
        expect(["native-workflow", "sdk-fanout"]).toContain(cap);
        expect(cap).toBe(expected.orchestrationCapability);
      });

      it("getDefaultArgs() returns a string array", () => {
        const args = adapter.getDefaultArgs();
        expect(Array.isArray(args)).toBe(true);
        for (const arg of args) {
          expect(typeof arg).toBe("string");
        }
      });

      it("agentic declares the expected tool-loop truth (#57)", () => {
        expect(typeof adapter.agentic).toBe("boolean");
        expect(adapter.agentic).toBe(expected.agentic);
      });

      it("requiresDirectApiKey() returns the expected boolean", () => {
        const result = adapter.requiresDirectApiKey();
        expect(typeof result).toBe("boolean");
        expect(result).toBe(expected.requiresDirectApiKey);
      });

      it("validateAuth() returns a Promise that resolves to passed without a runner", async () => {
        const result = adapter.validateAuth();
        expect(result).toBeInstanceOf(Promise);
        await expect(result).resolves.toBe("passed");
      });

      it("createQueryFunction() returns a Promise", () => {
        // We only verify the method is callable and returns a Promise.
        // We do NOT await it because some adapters (e.g., ClaudeSdkAdapter,
        // GeminiSdkAdapter) attempt to dynamically import an SDK which may
        // not be available in the test environment. Catch expected rejections.
        const result = adapter.createQueryFunction();
        result.catch(() => {});
        expect(result).toBeInstanceOf(Promise);
      });
    }
  );

  // -------------------------------------------------------------------------
  // Cross-adapter invariants
  // -------------------------------------------------------------------------

  describe("cross-adapter invariants", () => {
    it("every adapter name is unique", () => {
      const names = ADAPTER_TABLE.map((tc) => tc.adapter.name);
      const uniqueNames = new Set(names);
      expect(uniqueNames.size).toBe(names.length);
    });

    it("every ICliAdapter method exists and is callable on all adapters", () => {
      const requiredMethods: (keyof ICliAdapter)[] = [
        "validateAuth",
        "createQueryFunction",
        "getDefaultArgs",
        "getOrchestrationCapability",
        "requiresDirectApiKey",
      ];

      const requiredProperties: (keyof ICliAdapter)[] = [
        "name",
        "displayName",
        "cliCommand",
        "agentic",
      ];

      for (const { adapter } of ADAPTER_TABLE) {
        for (const method of requiredMethods) {
          expect(typeof adapter[method], `${adapter.name} is missing method '${method}'`).toBe(
            "function"
          );
        }
        for (const prop of requiredProperties) {
          expect(adapter[prop], `${adapter.name} is missing property '${prop}'`).toBeDefined();
        }
      }
    });

    it("only claude-sdk and gemini-sdk require a direct API key", () => {
      const apiKeyAdapters = new Set(["claude-sdk", "gemini-sdk"]);
      for (const { adapter } of ADAPTER_TABLE) {
        if (apiKeyAdapters.has(adapter.name)) {
          expect(
            adapter.requiresDirectApiKey(),
            `${adapter.name} must require a direct API key`
          ).toBe(true);
        } else {
          expect(
            adapter.requiresDirectApiKey(),
            `${adapter.name} must NOT require a direct API key`
          ).toBe(false);
        }
      }
    });
  });
});

// ---------------------------------------------------------------------------
// Real CLI smoke tests (skipped in CI)
// ---------------------------------------------------------------------------

describe.skipIf(!!process.env.CI)("Real CLI smoke tests", () => {
  it.todo(
    "validates real CLI auth for locally-installed adapters (placeholder for manual verification)"
  );
});
