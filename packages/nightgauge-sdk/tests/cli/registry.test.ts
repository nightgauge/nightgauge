/**
 * Registry Tests - Adapter lookup, registration edge cases, and
 * default registry completeness for multi-tool support.
 *
 * Complements adapterRegistry.test.ts with additional multi-tool validation
 * scenarios: concurrent registration, getAll() ordering, and cross-validation
 * between the registry and the IncrediAdapter type union.
 *
 * @see Issue #2275 - Add CLI adapter tests for multi-tool support validation
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AdapterRegistry, defaultRegistry } from "../../src/cli/adapters/AdapterRegistry.js";
import type { ICliAdapter, IncrediAdapter } from "../../src/cli/adapters/ICliAdapter.js";

// ---------------------------------------------------------------------------
// Helper: mock adapter factory
// ---------------------------------------------------------------------------

function createMockAdapter(name: IncrediAdapter, overrides?: Partial<ICliAdapter>): ICliAdapter {
  return {
    name,
    displayName: overrides?.displayName ?? `Mock ${name}`,
    cliCommand: overrides?.cliCommand ?? "mock-cli",
    validateAuth: vi.fn(async () => "passed" as const),
    createQueryFunction: vi.fn(async () => vi.fn()),
    getDefaultArgs: vi.fn(() => []),
    getOrchestrationCapability: vi.fn(() => "sdk-fanout" as const),
    requiresDirectApiKey: vi.fn(() => false),
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// AdapterRegistry: registration and lookup
// ---------------------------------------------------------------------------

describe("AdapterRegistry registration", () => {
  it("should support registering and retrieving all 8 adapter names", () => {
    const registry = new AdapterRegistry();
    const names: IncrediAdapter[] = [
      "claude-sdk",
      "claude-headless",
      "codex",
      "gemini",
      "gemini-sdk",
      "lm-studio",
      "ollama",
      "copilot",
    ];

    for (const name of names) {
      registry.register(createMockAdapter(name));
    }

    expect(registry.getNames()).toHaveLength(8);
    for (const name of names) {
      expect(registry.has(name)).toBe(true);
      expect(registry.get(name).name).toBe(name);
    }
  });

  it("getAll() returns adapters in registration order", () => {
    const registry = new AdapterRegistry();
    const order: IncrediAdapter[] = ["gemini", "codex", "claude-sdk"];

    for (const name of order) {
      registry.register(createMockAdapter(name));
    }

    const names = registry.getAll().map((a) => a.name);
    expect(names).toEqual(order);
  });

  it("last registration wins for duplicate names", () => {
    const registry = new AdapterRegistry();
    const first = createMockAdapter("codex", { displayName: "First" });
    const second = createMockAdapter("codex", { displayName: "Second" });

    registry.register(first);
    registry.register(second);

    expect(registry.get("codex").displayName).toBe("Second");
    // Map replaces value but retains original key order
    expect(registry.getAll()).toHaveLength(1);
  });

  it("getNames() returns empty array for empty registry", () => {
    const registry = new AdapterRegistry();
    expect(registry.getNames()).toEqual([]);
  });

  it("getAll() returns empty array for empty registry", () => {
    const registry = new AdapterRegistry();
    expect(registry.getAll()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// AdapterRegistry: error messages on unknown adapter
// ---------------------------------------------------------------------------

describe("AdapterRegistry error messages", () => {
  it("error message includes the unknown adapter name", () => {
    const registry = new AdapterRegistry();
    registry.register(createMockAdapter("codex"));

    expect(() => registry.get("gemini")).toThrow("Unknown adapter 'gemini'");
  });

  it("error message lists all registered adapters", () => {
    const registry = new AdapterRegistry();
    registry.register(createMockAdapter("claude-sdk"));
    registry.register(createMockAdapter("codex"));
    registry.register(createMockAdapter("copilot"));

    try {
      registry.get("gemini");
      expect.fail("should have thrown");
    } catch (err) {
      const message = (err as Error).message;
      expect(message).toContain("claude-sdk");
      expect(message).toContain("codex");
      expect(message).toContain("copilot");
    }
  });

  it("error message for empty registry lists no adapters", () => {
    const registry = new AdapterRegistry();

    expect(() => registry.get("codex")).toThrow(/Registered adapters:\s*$/);
  });
});

// ---------------------------------------------------------------------------
// defaultRegistry: completeness and consistency
// ---------------------------------------------------------------------------

describe("defaultRegistry completeness", () => {
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

  it("contains exactly 8 adapters", () => {
    expect(defaultRegistry.getAll()).toHaveLength(8);
  });

  it("has() returns true for every IncrediAdapter name", () => {
    for (const name of ALL_ADAPTER_NAMES) {
      expect(defaultRegistry.has(name)).toBe(true);
    }
  });

  it("get() returns an adapter for every IncrediAdapter name", () => {
    for (const name of ALL_ADAPTER_NAMES) {
      const adapter = defaultRegistry.get(name);
      expect(adapter.name).toBe(name);
    }
  });

  it("every adapter in defaultRegistry has a unique cliCommand or unique name", () => {
    const adapters = defaultRegistry.getAll();
    const nameSet = new Set(adapters.map((a) => a.name));
    // All names must be unique
    expect(nameSet.size).toBe(adapters.length);
  });

  it("every adapter displayName is non-empty and distinct", () => {
    const adapters = defaultRegistry.getAll();
    const displayNames = adapters.map((a) => a.displayName);

    for (const name of displayNames) {
      expect(name.length).toBeGreaterThan(0);
    }

    const unique = new Set(displayNames);
    expect(unique.size).toBe(displayNames.length);
  });

  it("getNames() and getAll() return consistent results", () => {
    const names = defaultRegistry.getNames();
    const allAdapters = defaultRegistry.getAll();

    expect(names).toHaveLength(allAdapters.length);
    for (const adapter of allAdapters) {
      expect(names).toContain(adapter.name);
    }
  });
});

// ---------------------------------------------------------------------------
// Multi-tool lookup simulation
// ---------------------------------------------------------------------------

describe("multi-tool adapter lookup", () => {
  beforeEach(() => {
    // SDK adapters (claude-sdk, gemini-sdk) now require API keys
    process.env.ANTHROPIC_API_KEY = "sk-test";
    process.env.GEMINI_API_KEY = "test-gemini";
  });

  afterEach(() => {
    delete process.env.ANTHROPIC_API_KEY;
    delete process.env.GEMINI_API_KEY;
  });

  it("can look up and validate auth for each adapter in sequence", async () => {
    const names = defaultRegistry.getNames();

    for (const name of names) {
      const adapter = defaultRegistry.get(name);
      // All adapters should resolve validateAuth without a runner
      const result = await adapter.validateAuth();
      expect(result).toBe("passed");
    }
  });

  it("can look up the orchestration capability for all adapters", () => {
    const matrix: Record<string, string> = {};

    for (const adapter of defaultRegistry.getAll()) {
      matrix[adapter.name] = adapter.getOrchestrationCapability();
    }

    // Verify the matrix has entries for all adapters
    expect(Object.keys(matrix)).toHaveLength(8);

    // Claude adapters offload to native workflows; everything else fans out.
    expect(matrix["claude-sdk"]).toBe("native-workflow");
    expect(matrix["claude-headless"]).toBe("native-workflow");
    expect(matrix["codex"]).toBe("sdk-fanout");
    expect(matrix["lm-studio"]).toBe("sdk-fanout");
  });
});
