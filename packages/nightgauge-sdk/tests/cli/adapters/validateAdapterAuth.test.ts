import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  validateAdapterAuth,
  DEFAULT_AUTH_TIMEOUT_MS,
} from "../../../src/cli/adapters/validateAdapterAuth.js";
import { AdapterError } from "../../../src/cli/adapters/errors.js";
import type { ICliAdapter, IncrediAdapter } from "../../../src/cli/adapters/ICliAdapter.js";

function fakeAdapter(name: IncrediAdapter, validateImpl: ICliAdapter["validateAuth"]): ICliAdapter {
  return {
    name,
    displayName: name,
    cliCommand: name,
    validateAuth: validateImpl,
    createQueryFunction: vi.fn() as unknown as ICliAdapter["createQueryFunction"],
    getDefaultArgs: () => [],
    getOrchestrationCapability: () => "sdk-fanout" as const,
    requiresDirectApiKey: () => false,
  };
}

function fakeRegistry(adapters: Record<string, ICliAdapter>) {
  return {
    get(name: IncrediAdapter): ICliAdapter {
      const a = adapters[name];
      if (!a) {
        throw new Error(
          `Unknown adapter '${name}'. Registered: ${Object.keys(adapters).join(", ")}`
        );
      }
      return a;
    },
    has(name: IncrediAdapter): boolean {
      return Boolean(adapters[name]);
    },
  };
}

describe("validateAdapterAuth", () => {
  it("returns { ok: true } when adapter.validateAuth resolves", async () => {
    const adapter = fakeAdapter("claude-sdk", async () => "passed");
    const registry = fakeRegistry({ "claude-sdk": adapter });

    const result = await validateAdapterAuth("claude-sdk", { registry });

    expect(result).toEqual({ ok: true });
  });

  it("returns { ok: false } with AUTH_MISSING when adapter throws AdapterError", async () => {
    const adapter = fakeAdapter("claude-headless", async () => {
      throw new AdapterError(
        "claude CLI is not authenticated.\nFix: claude auth login",
        "AUTH_MISSING",
        "Claude Headless"
      );
    });
    const registry = fakeRegistry({ "claude-headless": adapter });

    const result = await validateAdapterAuth("claude-headless", { registry });

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.category).toBe("AUTH_MISSING");
      expect(result.reason).toContain("Claude Headless");
      expect(result.reason).toContain("AUTH_MISSING");
    }
  });

  it("returns { ok: false } with BINARY_NOT_FOUND when binary missing", async () => {
    const adapter = fakeAdapter("codex", async () => {
      throw new AdapterError(
        "codex CLI is not installed.\nFix: npm install -g @openai/codex",
        "BINARY_NOT_FOUND",
        "Codex"
      );
    });
    const registry = fakeRegistry({ codex: adapter });

    const result = await validateAdapterAuth("codex", { registry });

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.category).toBe("BINARY_NOT_FOUND");
      expect(result.reason).toContain("not installed");
    }
  });

  it("returns { ok: false } CONFIG_INVALID when adapter is not in registry", async () => {
    const registry = fakeRegistry({});

    const result = await validateAdapterAuth("gemini-sdk", { registry });

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.category).toBe("CONFIG_INVALID");
      expect(result.reason).toMatch(/gemini-sdk|Unknown adapter/);
    }
  });

  it("returns TIMEOUT when validateAuth never resolves within timeoutMs", async () => {
    vi.useFakeTimers();
    try {
      const adapter = fakeAdapter(
        "claude-sdk",
        () => new Promise<"passed">(() => {}) // never resolves
      );
      const registry = fakeRegistry({ "claude-sdk": adapter });

      const promise = validateAdapterAuth("claude-sdk", {
        registry,
        timeoutMs: 5_000,
      });

      await vi.advanceTimersByTimeAsync(5_001);
      const result = await promise;

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.category).toBe("TIMEOUT");
        expect(result.reason).toContain("timed out after 5s");
      }
    } finally {
      vi.useRealTimers();
    }
  });

  it("forwards runner and cwd into adapter.validateAuth", async () => {
    const validateAuth = vi.fn(async () => "passed" as const);
    const adapter = fakeAdapter("codex", validateAuth);
    const registry = fakeRegistry({ codex: adapter });
    const runner = vi.fn();

    await validateAdapterAuth("codex", {
      registry,
      runner: runner as unknown as Parameters<ICliAdapter["validateAuth"]>[0] extends infer T
        ? T extends { runner?: infer R }
          ? R
          : never
        : never,
      cwd: "/tmp/work",
    });

    expect(validateAuth).toHaveBeenCalledWith({ runner, cwd: "/tmp/work" });
  });

  it("default timeout is 5_000ms", () => {
    expect(DEFAULT_AUTH_TIMEOUT_MS).toBe(5_000);
  });
});
