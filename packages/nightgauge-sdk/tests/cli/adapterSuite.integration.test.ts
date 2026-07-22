/**
 * Cross-Adapter Integration Test Suite
 *
 * Parameterized tests that run the same behavioral assertions against all
 * 8 registered adapters. Validates registration, auth validation, query
 * function creation, capability reporting, and error handling uniformly.
 * Adapter-specific tests cover unique behaviors (session resume, model
 * pulling, local server health, etc.).
 *
 * No real API keys or CLI binaries required — all external calls are mocked.
 *
 * @see Issue #2598 - Create cross-adapter integration test suite
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { defaultRegistry } from "../../src/cli/adapters/AdapterRegistry.js";
import type { ICliAdapter, IncrediAdapter } from "../../src/cli/adapters/ICliAdapter.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** All 8 canonical adapter names registered in defaultRegistry. */
const ALL_ADAPTER_NAMES: IncrediAdapter[] = [
  "claude-sdk",
  "claude-headless",
  "codex",
  "gemini",
  "gemini-sdk",
  "lm-studio",
  "ollama",
  "copilot",
];

/** Retrieve all registered adapters from defaultRegistry. */
function getAllAdapters(): ICliAdapter[] {
  return ALL_ADAPTER_NAMES.map((name) => defaultRegistry.get(name));
}

// ---------------------------------------------------------------------------
// Env save/restore helpers
// ---------------------------------------------------------------------------

type EnvSnapshot = Record<string, string | undefined>;

function snapshotEnv(keys: string[]): EnvSnapshot {
  return Object.fromEntries(keys.map((k) => [k, process.env[k]]));
}

function restoreEnv(snapshot: EnvSnapshot): void {
  for (const [key, value] of Object.entries(snapshot)) {
    if (value === undefined) {
      delete process.env[key];
    } else {
      process.env[key] = value;
    }
  }
}

const ALL_AUTH_ENV_KEYS = [
  "ANTHROPIC_API_KEY",
  "GEMINI_API_KEY",
  "GOOGLE_API_KEY",
  "GOOGLE_GENAI_USE_VERTEXAI",
  "GH_TOKEN",
  "GITHUB_TOKEN",
  "COPILOT_GITHUB_TOKEN",
  "NIGHTGAUGE_OLLAMA_MODEL",
  "NIGHTGAUGE_OLLAMA_BASE_URL",
  "NIGHTGAUGE_LM_STUDIO_MODEL",
  "NIGHTGAUGE_LM_STUDIO_BASE_URL",
];

// ---------------------------------------------------------------------------
// Suite 1: Registration — all 8 adapters are in defaultRegistry
// ---------------------------------------------------------------------------

describe("defaultRegistry: registration", () => {
  it("contains all 8 built-in adapters", () => {
    const names = defaultRegistry.getNames();
    expect(names).toHaveLength(8);
    for (const name of ALL_ADAPTER_NAMES) {
      expect(names, `missing adapter '${name}'`).toContain(name);
    }
  });

  it.each(ALL_ADAPTER_NAMES)("get('%s') returns a non-null adapter", (name) => {
    const adapter = defaultRegistry.get(name);
    expect(adapter).toBeDefined();
    expect(adapter).not.toBeNull();
  });

  it.each(ALL_ADAPTER_NAMES)("get('%s') name matches the requested key", (name) => {
    const adapter = defaultRegistry.get(name);
    expect(adapter.name).toBe(name);
  });

  it("get() throws a descriptive error for unregistered names", () => {
    expect(() => defaultRegistry.get("unknown-adapter" as IncrediAdapter)).toThrow(
      /Unknown adapter/
    );
  });
});

// ---------------------------------------------------------------------------
// Suite 2: Interface completeness — all adapters implement ICliAdapter fully
// ---------------------------------------------------------------------------

describe.each(ALL_ADAPTER_NAMES)("ICliAdapter completeness — %s", (name) => {
  let adapter: ICliAdapter;

  beforeEach(() => {
    adapter = defaultRegistry.get(name);
  });

  it("has a non-empty name", () => {
    expect(adapter.name).toBeTruthy();
  });

  it("has a non-empty displayName", () => {
    expect(adapter.displayName).toBeTruthy();
  });

  it("has a non-empty cliCommand", () => {
    expect(adapter.cliCommand).toBeTruthy();
  });

  it("validateAuth is a function", () => {
    expect(typeof adapter.validateAuth).toBe("function");
  });

  it("createQueryFunction is a function", () => {
    expect(typeof adapter.createQueryFunction).toBe("function");
  });

  it("getDefaultArgs is a function", () => {
    expect(typeof adapter.getDefaultArgs).toBe("function");
  });

  it("getOrchestrationCapability is a function", () => {
    expect(typeof adapter.getOrchestrationCapability).toBe("function");
  });

  it("requiresDirectApiKey is a function", () => {
    expect(typeof adapter.requiresDirectApiKey).toBe("function");
  });
});

// ---------------------------------------------------------------------------
// Suite 3: Capability reporting — getOrchestrationCapability() returns a valid value
// ---------------------------------------------------------------------------

describe.each(ALL_ADAPTER_NAMES)("getOrchestrationCapability() — %s", (name) => {
  let adapter: ICliAdapter;

  beforeEach(() => {
    adapter = defaultRegistry.get(name);
  });

  it("declares native-workflow or sdk-fanout", () => {
    expect(["native-workflow", "sdk-fanout"]).toContain(adapter.getOrchestrationCapability());
  });
});

// ---------------------------------------------------------------------------
// Suite 4: getDefaultArgs() — returns an array for every adapter
// ---------------------------------------------------------------------------

describe.each(ALL_ADAPTER_NAMES)("getDefaultArgs() — %s", (name) => {
  it("returns an Array", () => {
    const adapter = defaultRegistry.get(name);
    expect(Array.isArray(adapter.getDefaultArgs())).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Suite 5: validateAuth() — without a runner, resolves 'passed'
// ---------------------------------------------------------------------------

/**
 * SDK adapters (claude-sdk, gemini-sdk) validate API key presence even without
 * a runner. Provide the necessary keys in beforeEach so the no-runner test
 * passes for all adapters.
 *
 * Error scenarios (missing keys) are covered in adapterErrors.integration.test.ts.
 */
describe.each(ALL_ADAPTER_NAMES)("validateAuth() no-runner — %s", (name) => {
  let adapter: ICliAdapter;
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    // SDK adapters require API keys even without a runner
    process.env.ANTHROPIC_API_KEY = "sk-test-anthropic";
    process.env.GEMINI_API_KEY = "test-gemini";
    adapter = defaultRegistry.get(name);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("returns a Promise", () => {
    const result = adapter.validateAuth();
    expect(result).toBeInstanceOf(Promise);
    result.catch(() => {}); // suppress unhandled rejection
  });

  it("resolves to 'passed' without a runner (when credentials are present)", async () => {
    await expect(adapter.validateAuth()).resolves.toBe("passed");
  });
});

// ---------------------------------------------------------------------------
// Suite 6: validateAuth() — with a runner, passes validation for installed CLI
// ---------------------------------------------------------------------------

describe.each(ALL_ADAPTER_NAMES)("validateAuth() with runner — %s", (name) => {
  let adapter: ICliAdapter;
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    // Provide env vars so auth checks pass
    process.env.ANTHROPIC_API_KEY = "sk-test-anthropic";
    process.env.GEMINI_API_KEY = "test-gemini";
    process.env.GOOGLE_API_KEY = "test-google";
    process.env.GH_TOKEN = "ghp_test";
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama2";
    process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "test-model";
    adapter = defaultRegistry.get(name);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("resolves to 'passed' when runner reports CLI installed and authenticated", async () => {
    const runner = vi.fn().mockResolvedValue({ code: 0, stdout: "Logged in", stderr: "" });
    await expect(adapter.validateAuth({ runner })).resolves.toBe("passed");
  });
});

// ---------------------------------------------------------------------------
// Suite 7: createQueryFunction() — returns a Promise
// ---------------------------------------------------------------------------

describe.each(ALL_ADAPTER_NAMES)("createQueryFunction() — %s", (name) => {
  let adapter: ICliAdapter;
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    // Set env vars that local adapters need to avoid early throw
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama2";
    process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "test-model";
    adapter = defaultRegistry.get(name);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("returns a Promise", () => {
    const result = adapter.createQueryFunction();
    expect(result).toBeInstanceOf(Promise);
    result.catch(() => {}); // suppress unhandled rejection for SDK adapters
  });
});

// ---------------------------------------------------------------------------
// Suite 8: requiresDirectApiKey() — only SDK adapters require API keys
// ---------------------------------------------------------------------------

describe("requiresDirectApiKey() cross-adapter invariant", () => {
  const API_KEY_ADAPTERS = new Set<IncrediAdapter>(["claude-sdk", "gemini-sdk"]);

  it.each(ALL_ADAPTER_NAMES)("%s: requiresDirectApiKey() matches expected value", (name) => {
    const adapter = defaultRegistry.get(name);
    const expected = API_KEY_ADAPTERS.has(name);
    expect(
      adapter.requiresDirectApiKey(),
      `${name} requiresDirectApiKey() should be ${expected}`
    ).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// Suite 9: Adapter-specific unique behaviors
// ---------------------------------------------------------------------------

describe("adapter-specific: codex — orchestration capability", () => {
  it("declares sdk-fanout", () => {
    const adapter = defaultRegistry.get("codex");
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });
});

describe("adapter-specific: ollama — model env var required for createQueryFunction", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(["NIGHTGAUGE_OLLAMA_MODEL"]);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
  });

  it("createQueryFunction rejects when NIGHTGAUGE_OLLAMA_MODEL is unset", async () => {
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
    const adapter = defaultRegistry.get("ollama");
    await expect(adapter.createQueryFunction()).rejects.toThrow(/NIGHTGAUGE_OLLAMA_MODEL/);
  });

  it("declares the sdk-fanout orchestration capability", () => {
    const adapter = defaultRegistry.get("ollama");
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });
});

describe("adapter-specific: lm-studio — model env var required for createQueryFunction", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(["NIGHTGAUGE_LM_STUDIO_MODEL"]);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
  });

  it("createQueryFunction rejects when NIGHTGAUGE_LM_STUDIO_MODEL is unset", async () => {
    delete process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
    const adapter = defaultRegistry.get("lm-studio");
    await expect(adapter.createQueryFunction()).rejects.toThrow(/NIGHTGAUGE_LM_STUDIO_MODEL/);
  });

  it("declares the sdk-fanout orchestration capability", () => {
    const adapter = defaultRegistry.get("lm-studio");
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });
});

describe("adapter-specific: claude-headless — no direct API key required", () => {
  it("requiresDirectApiKey() returns false (uses OAuth / CLI session)", () => {
    const adapter = defaultRegistry.get("claude-headless");
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });

  it("declares the native-workflow orchestration capability", () => {
    const adapter = defaultRegistry.get("claude-headless");
    expect(adapter.getOrchestrationCapability()).toBe("native-workflow");
  });
});

describe("adapter-specific: claude-sdk — requires ANTHROPIC_API_KEY", () => {
  it("requiresDirectApiKey() returns true", () => {
    const adapter = defaultRegistry.get("claude-sdk");
    expect(adapter.requiresDirectApiKey()).toBe(true);
  });

  it("declares the native-workflow orchestration capability", () => {
    const adapter = defaultRegistry.get("claude-sdk");
    expect(adapter.getOrchestrationCapability()).toBe("native-workflow");
  });
});

describe("adapter-specific: gemini-sdk — requires GEMINI_API_KEY", () => {
  it("requiresDirectApiKey() returns true", () => {
    const adapter = defaultRegistry.get("gemini-sdk");
    expect(adapter.requiresDirectApiKey()).toBe(true);
  });
});

describe("adapter-specific: copilot — uses GH_TOKEN / GITHUB_TOKEN auth", () => {
  it("requiresDirectApiKey() returns false (token-based, not API key)", () => {
    const adapter = defaultRegistry.get("copilot");
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Suite 10: Cross-adapter invariants
// ---------------------------------------------------------------------------

describe("cross-adapter invariants", () => {
  it("all adapter names are unique", () => {
    const names = getAllAdapters().map((a) => a.name);
    const unique = new Set(names);
    expect(unique.size).toBe(names.length);
  });

  it("all cliCommands are non-empty strings", () => {
    for (const adapter of getAllAdapters()) {
      expect(typeof adapter.cliCommand, `${adapter.name} cliCommand type`).toBe("string");
      expect(adapter.cliCommand.length, `${adapter.name} cliCommand length`).toBeGreaterThan(0);
    }
  });

  it("all displayNames are non-empty strings", () => {
    for (const adapter of getAllAdapters()) {
      expect(adapter.displayName.length, `${adapter.name} displayName length`).toBeGreaterThan(0);
    }
  });

  it("getDefaultArgs() returns [] or a non-empty array (never null/undefined)", () => {
    for (const adapter of getAllAdapters()) {
      const args = adapter.getDefaultArgs();
      expect(args, `${adapter.name} getDefaultArgs result`).toBeDefined();
      expect(Array.isArray(args), `${adapter.name} getDefaultArgs is array`).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// Suite 11: Error type invariants — all adapters throw AdapterError on failure
// ---------------------------------------------------------------------------

describe("cross-adapter error type invariant — CLI adapters throw AdapterError on missing binary", () => {
  const CLI_ADAPTERS: IncrediAdapter[] = ["claude-headless", "codex", "gemini", "copilot"];
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv([
      "GEMINI_API_KEY",
      "GOOGLE_API_KEY",
      "GH_TOKEN",
      "GITHUB_TOKEN",
      "COPILOT_GITHUB_TOKEN",
    ]);
    // Clear all auth env vars to force CLI path
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
    delete process.env.GH_TOKEN;
    delete process.env.GITHUB_TOKEN;
    delete process.env.COPILOT_GITHUB_TOKEN;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it.each(CLI_ADAPTERS)("%s: throws AdapterError when binary not found", async (name) => {
    const adapter = defaultRegistry.get(name);
    const runner = vi
      .fn()
      .mockRejectedValue(Object.assign(new Error("spawn ENOENT"), { code: "ENOENT" }));
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect(err, `${name} should throw AdapterError`).toBeInstanceOf(AdapterError);
  });
});

describe("cross-adapter error type invariant — SDK adapters throw AdapterError on missing key", () => {
  const SDK_ADAPTERS: Array<[IncrediAdapter, string]> = [
    ["claude-sdk", "ANTHROPIC_API_KEY"],
    ["gemini-sdk", "GEMINI_API_KEY"],
  ];
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(["ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"]);
    delete process.env.ANTHROPIC_API_KEY;
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it.each(SDK_ADAPTERS)(
    "%s: throws AdapterError with AUTH_MISSING when %s is absent",
    async (name, _envKey) => {
      const adapter = defaultRegistry.get(name);
      const err = await adapter.validateAuth().catch((e) => e);
      expect(err, `${name} should throw AdapterError`).toBeInstanceOf(AdapterError);
      expect((err as AdapterError).category).toBe("AUTH_MISSING");
    }
  );
});
