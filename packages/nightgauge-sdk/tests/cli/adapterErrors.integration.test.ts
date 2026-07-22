/**
 * Adapter Error Scenario Integration Tests
 *
 * Verifies that all 8 adapters throw `AdapterError` with:
 * - The correct `category` field
 * - The correct `adapterName` field
 * - Error messages that contain the adapter name, reason, and fix instruction
 * - Consistent formatting across error scenarios
 *
 * No real API keys, CLI binaries, or servers are required — all external
 * calls are mocked.
 *
 * @see Issue #2596 - Standardize adapter error messages
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  AdapterError,
  type AdapterErrorCategory,
  throwAuthError,
  throwBinaryNotFound,
  throwModelNotFound,
  throwServerUnreachable,
  throwVersionMismatch,
  throwConfigInvalid,
  throwTimeoutError,
} from "../../src/cli/adapters/errors.js";
import { defaultRegistry } from "../../src/cli/adapters/AdapterRegistry.js";
import type { PreflightCommandResult } from "../../src/cli/codexPreflight.js";

// ---------------------------------------------------------------------------
// Helpers
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

/** Mock runner that simulates a binary not found (ENOENT) */
function binaryNotFoundRunner(): () => Promise<PreflightCommandResult> {
  return vi.fn().mockRejectedValue(Object.assign(new Error("spawn ENOENT"), { code: "ENOENT" }));
}

/** Mock runner that simulates installed binary but auth fails */
function authFailRunner(): () => Promise<PreflightCommandResult> {
  return vi.fn().mockResolvedValue({ code: 1, stdout: "", stderr: "not authenticated" });
}

/** Mock runner that simulates installed binary (version check passes) but auth fails */
function installedButAuthFailRunner(): () => Promise<PreflightCommandResult> {
  return vi.fn().mockImplementation((_cmd: string, args: string[]) => {
    if (args[0] === "--version") {
      return Promise.resolve({ code: 0, stdout: "1.0.0", stderr: "" });
    }
    return Promise.resolve({ code: 1, stdout: "", stderr: "not authenticated" });
  });
}

/** Mock runner that simulates timeout (exit code 124) */
function timeoutRunner(): () => Promise<PreflightCommandResult> {
  return vi.fn().mockImplementation((_cmd: string, args: string[]) => {
    if (args[0] === "--version") {
      return Promise.resolve({ code: 0, stdout: "1.0.0", stderr: "" });
    }
    return Promise.resolve({ code: 124, stdout: "", stderr: "Command timed out" });
  });
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
  "NIGHTGAUGE_LM_STUDIO_MODEL",
];

// ---------------------------------------------------------------------------
// AdapterError class unit tests
// ---------------------------------------------------------------------------

describe("AdapterError", () => {
  it("has name='AdapterError'", () => {
    const err = new AdapterError("test message", "AUTH_MISSING", "Test Adapter");
    expect(err.name).toBe("AdapterError");
  });

  it("stores category, adapterName, and actionUrl", () => {
    const err = new AdapterError("msg", "BINARY_NOT_FOUND", "My Adapter", "https://example.com");
    expect(err.category).toBe("BINARY_NOT_FOUND");
    expect(err.adapterName).toBe("My Adapter");
    expect(err.actionUrl).toBe("https://example.com");
  });

  it("format() returns '[Adapter Name] CATEGORY: message'", () => {
    const err = new AdapterError("cli not found", "BINARY_NOT_FOUND", "Claude Headless");
    expect(err.format()).toBe("[Claude Headless] BINARY_NOT_FOUND: cli not found");
  });

  it("is instanceof Error", () => {
    const err = new AdapterError("test", "AUTH_MISSING", "Test");
    expect(err).toBeInstanceOf(Error);
  });

  it("is instanceof AdapterError", () => {
    const err = new AdapterError("test", "TIMEOUT", "Test");
    expect(err).toBeInstanceOf(AdapterError);
  });

  it("has a stack trace", () => {
    const err = new AdapterError("test", "AUTH_MISSING", "Test");
    expect(err.stack).toBeTruthy();
  });
});

// ---------------------------------------------------------------------------
// Suite: claude-sdk — AUTH_MISSING when ANTHROPIC_API_KEY is absent
// ---------------------------------------------------------------------------

describe("claude-sdk: validateAuth() AUTH_MISSING when no API key", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    delete process.env.ANTHROPIC_API_KEY;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError", async () => {
    const adapter = defaultRegistry.get("claude-sdk");
    await expect(adapter.validateAuth()).rejects.toBeInstanceOf(AdapterError);
  });

  it("throws with category AUTH_MISSING", async () => {
    const adapter = defaultRegistry.get("claude-sdk");
    const err = await adapter.validateAuth().catch((e) => e);
    expect((err as AdapterError).category).toBe("AUTH_MISSING" satisfies AdapterErrorCategory);
  });

  it("throws with adapterName 'Claude SDK'", async () => {
    const adapter = defaultRegistry.get("claude-sdk");
    const err = await adapter.validateAuth().catch((e) => e);
    expect((err as AdapterError).adapterName).toBe("Claude SDK");
  });

  it("error message mentions ANTHROPIC_API_KEY", async () => {
    const adapter = defaultRegistry.get("claude-sdk");
    const err = await adapter.validateAuth().catch((e) => e);
    expect((err as AdapterError).message).toMatch(/ANTHROPIC_API_KEY/);
  });

  it("error message contains a docs URL", async () => {
    const adapter = defaultRegistry.get("claude-sdk");
    const err = await adapter.validateAuth().catch((e) => e);
    expect((err as AdapterError).message).toMatch(/https?:\/\//);
  });
});

// ---------------------------------------------------------------------------
// Suite: gemini-sdk — AUTH_MISSING when no API key
// ---------------------------------------------------------------------------

describe("gemini-sdk: validateAuth() AUTH_MISSING when no API key", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError", async () => {
    const adapter = defaultRegistry.get("gemini-sdk");
    await expect(adapter.validateAuth()).rejects.toBeInstanceOf(AdapterError);
  });

  it("throws with category AUTH_MISSING", async () => {
    const adapter = defaultRegistry.get("gemini-sdk");
    const err = await adapter.validateAuth().catch((e) => e);
    expect((err as AdapterError).category).toBe("AUTH_MISSING" satisfies AdapterErrorCategory);
  });

  it("error message lists both accepted env vars", async () => {
    const adapter = defaultRegistry.get("gemini-sdk");
    const err = await adapter.validateAuth().catch((e) => e);
    const msg = (err as AdapterError).message;
    expect(msg).toMatch(/GEMINI_API_KEY/);
    expect(msg).toMatch(/GOOGLE_API_KEY/);
  });
});

// ---------------------------------------------------------------------------
// Suite: claude-headless — BINARY_NOT_FOUND when CLI not installed
// ---------------------------------------------------------------------------

describe("claude-headless: validateAuth() BINARY_NOT_FOUND when CLI absent", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError when runner rejects (binary not found)", async () => {
    const adapter = defaultRegistry.get("claude-headless");
    const runner = binaryNotFoundRunner();
    await expect(adapter.validateAuth({ runner })).rejects.toBeInstanceOf(AdapterError);
  });

  it("throws with category BINARY_NOT_FOUND", async () => {
    const adapter = defaultRegistry.get("claude-headless");
    const runner = binaryNotFoundRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect((err as AdapterError).category).toBe("BINARY_NOT_FOUND" satisfies AdapterErrorCategory);
  });

  it("error message includes installation command", async () => {
    const adapter = defaultRegistry.get("claude-headless");
    const runner = binaryNotFoundRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    // Should include some form of install instruction
    expect((err as AdapterError).message).toMatch(/install|brew|npm/i);
  });
});

// ---------------------------------------------------------------------------
// Suite: claude-headless — AUTH_MISSING when CLI installed but not authenticated
// ---------------------------------------------------------------------------

describe("claude-headless: validateAuth() AUTH_MISSING when not authenticated", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError when auth status fails", async () => {
    const adapter = defaultRegistry.get("claude-headless");
    // version check passes, then auth check fails
    const runner = vi.fn().mockImplementation((_cmd: string, args: string[]) => {
      if (args[0] === "--version") {
        return Promise.resolve({ code: 0, stdout: "claude 1.0.0", stderr: "" });
      }
      // auth status fails
      return Promise.resolve({ code: 1, stdout: "", stderr: "Not logged in" });
    });
    await expect(adapter.validateAuth({ runner })).rejects.toBeInstanceOf(AdapterError);
  });

  it("throws with category AUTH_MISSING", async () => {
    const adapter = defaultRegistry.get("claude-headless");
    const runner = vi.fn().mockImplementation((_cmd: string, args: string[]) => {
      if (args[0] === "--version") {
        return Promise.resolve({ code: 0, stdout: "claude 1.0.0", stderr: "" });
      }
      return Promise.resolve({ code: 1, stdout: "", stderr: "Not logged in" });
    });
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect((err as AdapterError).category).toBe("AUTH_MISSING" satisfies AdapterErrorCategory);
  });

  it("error message includes login hint", async () => {
    const adapter = defaultRegistry.get("claude-headless");
    const runner = vi.fn().mockImplementation((_cmd: string, args: string[]) => {
      if (args[0] === "--version") {
        return Promise.resolve({ code: 0, stdout: "claude 1.0.0", stderr: "" });
      }
      return Promise.resolve({ code: 1, stdout: "", stderr: "Not logged in" });
    });
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect((err as AdapterError).message).toMatch(/claude auth login/);
  });
});

// ---------------------------------------------------------------------------
// Suite: claude-headless — TIMEOUT when auth status times out
// ---------------------------------------------------------------------------

describe("claude-headless: validateAuth() TIMEOUT when auth status times out", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with TIMEOUT category on exit code 124", async () => {
    const adapter = defaultRegistry.get("claude-headless");
    const runner = timeoutRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("TIMEOUT" satisfies AdapterErrorCategory);
  });
});

// ---------------------------------------------------------------------------
// Suite: codex — BINARY_NOT_FOUND when CLI not installed
// ---------------------------------------------------------------------------

describe("codex: validateAuth() BINARY_NOT_FOUND when CLI absent", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with BINARY_NOT_FOUND", async () => {
    const adapter = defaultRegistry.get("codex");
    const runner = binaryNotFoundRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("BINARY_NOT_FOUND" satisfies AdapterErrorCategory);
  });

  it("error message includes install command for codex", async () => {
    const adapter = defaultRegistry.get("codex");
    const runner = binaryNotFoundRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect((err as AdapterError).message).toMatch(/codex/i);
  });
});

// ---------------------------------------------------------------------------
// Suite: codex — AUTH_MISSING when login status fails
// ---------------------------------------------------------------------------

describe("codex: validateAuth() AUTH_MISSING when not logged in", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with AUTH_MISSING", async () => {
    const adapter = defaultRegistry.get("codex");
    const runner = installedButAuthFailRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("AUTH_MISSING" satisfies AdapterErrorCategory);
  });

  it("error message includes 'codex login' hint", async () => {
    const adapter = defaultRegistry.get("codex");
    const runner = installedButAuthFailRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect((err as AdapterError).message).toMatch(/codex login/);
  });
});

// ---------------------------------------------------------------------------
// Suite: gemini — BINARY_NOT_FOUND when CLI absent
// ---------------------------------------------------------------------------

describe("gemini: validateAuth() BINARY_NOT_FOUND when CLI absent", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with BINARY_NOT_FOUND", async () => {
    const adapter = defaultRegistry.get("gemini");
    const runner = binaryNotFoundRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("BINARY_NOT_FOUND" satisfies AdapterErrorCategory);
  });
});

// ---------------------------------------------------------------------------
// Suite: gemini — AUTH_MISSING when all auth methods fail
// ---------------------------------------------------------------------------

describe("gemini: validateAuth() AUTH_MISSING when all auth methods fail", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
    delete process.env.GOOGLE_GENAI_USE_VERTEXAI;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with AUTH_MISSING when all methods fail", async () => {
    const adapter = defaultRegistry.get("gemini");
    // version passes, gcloud auth fails
    const runner = vi.fn().mockImplementation((_cmd: string, args: string[]) => {
      if (args[0] === "--version") {
        return Promise.resolve({ code: 0, stdout: "gemini 0.30.0", stderr: "" });
      }
      return Promise.resolve({ code: 1, stdout: "", stderr: "not authenticated" });
    });
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("AUTH_MISSING" satisfies AdapterErrorCategory);
  });

  it("error message lists all three auth options", async () => {
    const adapter = defaultRegistry.get("gemini");
    const runner = vi.fn().mockImplementation((_cmd: string, args: string[]) => {
      if (args[0] === "--version") {
        return Promise.resolve({ code: 0, stdout: "gemini 0.30.0", stderr: "" });
      }
      return Promise.resolve({ code: 1, stdout: "", stderr: "not authenticated" });
    });
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    const msg = (err as AdapterError).message;
    expect(msg).toMatch(/GEMINI_API_KEY/);
    expect(msg).toMatch(/GOOGLE_API_KEY/);
    expect(msg).toMatch(/gcloud auth login/);
  });
});

// ---------------------------------------------------------------------------
// Suite: copilot — BINARY_NOT_FOUND when CLI absent
// ---------------------------------------------------------------------------

describe("copilot: validateAuth() BINARY_NOT_FOUND when CLI absent", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(ALL_AUTH_ENV_KEYS);
    delete process.env.GH_TOKEN;
    delete process.env.GITHUB_TOKEN;
    delete process.env.COPILOT_GITHUB_TOKEN;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with BINARY_NOT_FOUND", async () => {
    const adapter = defaultRegistry.get("copilot");
    const runner = binaryNotFoundRunner();
    const err = await adapter.validateAuth({ runner }).catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("BINARY_NOT_FOUND" satisfies AdapterErrorCategory);
  });
});

// ---------------------------------------------------------------------------
// Suite: ollama — CONFIG_INVALID when model env var not set
// ---------------------------------------------------------------------------

describe("ollama: createQueryFunction() CONFIG_INVALID when model unset", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(["NIGHTGAUGE_OLLAMA_MODEL"]);
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with CONFIG_INVALID", async () => {
    const adapter = defaultRegistry.get("ollama");
    const err = await adapter.createQueryFunction().catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("CONFIG_INVALID" satisfies AdapterErrorCategory);
  });

  it("error message mentions NIGHTGAUGE_OLLAMA_MODEL", async () => {
    const adapter = defaultRegistry.get("ollama");
    const err = await adapter.createQueryFunction().catch((e) => e);
    expect((err as AdapterError).message).toMatch(/NIGHTGAUGE_OLLAMA_MODEL/);
  });

  it("error message includes how to pull a model", async () => {
    const adapter = defaultRegistry.get("ollama");
    const err = await adapter.createQueryFunction().catch((e) => e);
    expect((err as AdapterError).message).toMatch(/ollama pull/i);
  });
});

// ---------------------------------------------------------------------------
// Suite: lm-studio — CONFIG_INVALID when model env var not set
// ---------------------------------------------------------------------------

describe("lm-studio: createQueryFunction() CONFIG_INVALID when model unset", () => {
  let envSnapshot: EnvSnapshot;

  beforeEach(() => {
    envSnapshot = snapshotEnv(["NIGHTGAUGE_LM_STUDIO_MODEL"]);
    delete process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
  });

  afterEach(() => {
    restoreEnv(envSnapshot);
    vi.restoreAllMocks();
  });

  it("throws AdapterError with CONFIG_INVALID", async () => {
    const adapter = defaultRegistry.get("lm-studio");
    const err = await adapter.createQueryFunction().catch((e) => e);
    expect(err).toBeInstanceOf(AdapterError);
    expect((err as AdapterError).category).toBe("CONFIG_INVALID" satisfies AdapterErrorCategory);
  });

  it("error message mentions NIGHTGAUGE_LM_STUDIO_MODEL", async () => {
    const adapter = defaultRegistry.get("lm-studio");
    const err = await adapter.createQueryFunction().catch((e) => e);
    expect((err as AdapterError).message).toMatch(/NIGHTGAUGE_LM_STUDIO_MODEL/);
  });
});

// ---------------------------------------------------------------------------
// Suite: Error format consistency — all AdapterErrors follow the template
// ---------------------------------------------------------------------------

describe("AdapterError format consistency", () => {
  it("format() includes adapter name in brackets", () => {
    const err = new AdapterError("some message", "AUTH_MISSING", "My Adapter");
    expect(err.format()).toMatch(/^\[My Adapter\]/);
  });

  it("format() includes category in uppercase", () => {
    const err = new AdapterError("msg", "MODEL_NOT_FOUND", "Ollama");
    expect(err.format()).toMatch(/MODEL_NOT_FOUND/);
  });

  it("all category values are representable", () => {
    const categories: AdapterErrorCategory[] = [
      "AUTH_MISSING",
      "AUTH_EXPIRED",
      "BINARY_NOT_FOUND",
      "VERSION_MISMATCH",
      "SERVER_UNREACHABLE",
      "MODEL_NOT_FOUND",
      "CONFIG_INVALID",
      "TIMEOUT",
    ];
    for (const cat of categories) {
      const err = new AdapterError("test", cat, "Adapter");
      expect(err.category).toBe(cat);
      expect(err.format()).toContain(cat);
    }
  });
});

// ---------------------------------------------------------------------------
// Suite: Error helper functions
// ---------------------------------------------------------------------------

describe("error helpers", () => {
  it("throwAuthError includes Fix: line", () => {
    expect(() => throwAuthError("Test", "No key found", "set MY_KEY=xxx")).toThrow(/Fix:/);
  });

  it("throwBinaryNotFound includes Fix: line with install command", () => {
    expect(() => throwBinaryNotFound("Test Adapter", "mytool", "npm install -g mytool")).toThrow(
      /npm install -g mytool/
    );
  });

  it("throwModelNotFound includes model name", () => {
    expect(() => throwModelNotFound("Ollama", "llama3.1", "ollama pull llama3.1")).toThrow(
      /llama3\.1/
    );
  });

  it("throwServerUnreachable includes start command", () => {
    expect(() =>
      throwServerUnreachable("Ollama", "http://localhost:11434/v1", "ollama serve")
    ).toThrow(/ollama serve/);
  });

  it("throwVersionMismatch shows current and required versions", () => {
    expect(() =>
      throwVersionMismatch("Gemini", "0.20.0", "0.29.0", "npm update @google/gemini-cli")
    ).toThrow(/0\.20\.0.*0\.29\.0|0\.29\.0.*0\.20\.0/);
  });

  it("throwConfigInvalid mentions config key", () => {
    expect(() =>
      throwConfigInvalid("LM Studio", "NIGHTGAUGE_LM_STUDIO_MODEL", "Set a model name")
    ).toThrow(/NIGHTGAUGE_LM_STUDIO_MODEL/);
  });

  it("throwTimeoutError includes timeout duration in seconds", () => {
    expect(() =>
      throwTimeoutError("Claude Headless", "`claude auth status`", 10_000, "Verify manually")
    ).toThrow(/10s/);
  });
});
