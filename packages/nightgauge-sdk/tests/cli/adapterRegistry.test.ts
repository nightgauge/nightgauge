import { describe, it, expect, vi } from "vitest";
import { AdapterRegistry, defaultRegistry } from "../../src/cli/adapters/AdapterRegistry.js";
import type { ICliAdapter, IncrediAdapter } from "../../src/cli/adapters/ICliAdapter.js";
import { ClaudeSdkAdapter } from "../../src/cli/adapters/ClaudeSdkAdapter.js";
import { ClaudeHeadlessAdapter } from "../../src/cli/adapters/ClaudeHeadlessAdapter.js";
import { CodexAdapter } from "../../src/cli/adapters/CodexAdapter.js";
import { GeminiAdapter } from "../../src/cli/adapters/GeminiAdapter.js";
import { GeminiSdkAdapter } from "../../src/cli/adapters/GeminiSdkAdapter.js";
import { LmStudioAdapter } from "../../src/cli/adapters/LmStudioAdapter.js";
import { OllamaAdapter } from "../../src/cli/adapters/OllamaAdapter.js";
import { CopilotCliAdapter } from "../../src/cli/adapters/CopilotCliAdapter.js";

function createMockAdapter(name: IncrediAdapter, displayName = "Mock"): ICliAdapter {
  return {
    name,
    displayName,
    cliCommand: "mock-cli",
    validateAuth: vi.fn(async () => "passed" as const),
    createQueryFunction: vi.fn(async () => vi.fn()),
    getDefaultArgs: vi.fn(() => []),
    getOrchestrationCapability: vi.fn(() => "sdk-fanout" as const),
    requiresDirectApiKey: vi.fn(() => false),
  };
}

describe("AdapterRegistry", () => {
  it("should roundtrip register() and get()", () => {
    const registry = new AdapterRegistry();
    const adapter = createMockAdapter("claude-sdk");

    registry.register(adapter);

    expect(registry.get("claude-sdk")).toBe(adapter);
  });

  it("should throw a descriptive error for an unknown adapter name", () => {
    const registry = new AdapterRegistry();
    const adapter = createMockAdapter("codex");
    registry.register(adapter);

    expect(() => registry.get("gemini")).toThrow(/Unknown adapter 'gemini'/);
    expect(() => registry.get("gemini")).toThrow(/Registered adapters:.*codex/);
  });

  it("should return true from has() for registered adapters and false for unknown", () => {
    const registry = new AdapterRegistry();
    registry.register(createMockAdapter("claude-headless"));

    expect(registry.has("claude-headless")).toBe(true);
    expect(registry.has("gemini")).toBe(false);
  });

  it("should return all registered adapters from getAll()", () => {
    const registry = new AdapterRegistry();
    const a = createMockAdapter("claude-sdk");
    const b = createMockAdapter("codex");
    registry.register(a);
    registry.register(b);

    const all = registry.getAll();

    expect(all).toHaveLength(2);
    expect(all).toContain(a);
    expect(all).toContain(b);
  });

  it("should return all canonical adapter names from getNames()", () => {
    const registry = new AdapterRegistry();
    registry.register(createMockAdapter("claude-sdk"));
    registry.register(createMockAdapter("gemini"));
    registry.register(createMockAdapter("codex"));

    const names = registry.getNames();

    expect(names).toHaveLength(3);
    expect(names).toContain("claude-sdk");
    expect(names).toContain("gemini");
    expect(names).toContain("codex");
  });

  it("should overwrite the previous entry on duplicate register()", () => {
    const registry = new AdapterRegistry();
    const first = createMockAdapter("claude-sdk", "First");
    const second = createMockAdapter("claude-sdk", "Second");

    registry.register(first);
    registry.register(second);

    expect(registry.get("claude-sdk")).toBe(second);
    expect(registry.getAll()).toHaveLength(1);
  });
});

describe("defaultRegistry", () => {
  const expectedNames: IncrediAdapter[] = [
    "claude-sdk",
    "claude-headless",
    "codex",
    "gemini",
    "gemini-sdk",
    "lm-studio",
    "ollama",
    "copilot",
  ];

  it("should contain all 8 built-in adapters", () => {
    const names = defaultRegistry.getNames();

    expect(names).toHaveLength(8);
    for (const name of expectedNames) {
      expect(names).toContain(name);
    }
  });

  it("should return the correct implementation type for each adapter name", () => {
    expect(defaultRegistry.get("claude-sdk")).toBeInstanceOf(ClaudeSdkAdapter);
    expect(defaultRegistry.get("claude-headless")).toBeInstanceOf(ClaudeHeadlessAdapter);
    expect(defaultRegistry.get("codex")).toBeInstanceOf(CodexAdapter);
    expect(defaultRegistry.get("gemini")).toBeInstanceOf(GeminiAdapter);
    expect(defaultRegistry.get("gemini-sdk")).toBeInstanceOf(GeminiSdkAdapter);
    expect(defaultRegistry.get("lm-studio")).toBeInstanceOf(LmStudioAdapter);
    expect(defaultRegistry.get("ollama")).toBeInstanceOf(OllamaAdapter);
    expect(defaultRegistry.get("copilot")).toBeInstanceOf(CopilotCliAdapter);
  });
});
