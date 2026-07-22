import { describe, expect, it } from "vitest";
import {
  isCodexAdapterEnabled,
  isGeminiAdapterEnabled,
  isGeminiSdkAdapterEnabled,
  isLmStudioAdapterEnabled,
  isCopilotAdapterEnabled,
  requiresDirectApiKey,
  resolveAdapter,
} from "../../src/cli/adapter.js";

describe("adapter resolution", () => {
  it("resolves codex adapter from explicit env", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "codex" })).toBe("codex");
    expect(isCodexAdapterEnabled({ NIGHTGAUGE_ADAPTER: "codex" })).toBe(true);
  });

  it("resolves gemini adapter from explicit env", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "gemini" })).toBe("gemini");
    expect(isGeminiAdapterEnabled({ NIGHTGAUGE_ADAPTER: "gemini" })).toBe(true);
  });

  it("resolves gemini-headless alias to gemini", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "gemini-headless" })).toBe("gemini");
  });

  it("resolves gemini-sdk adapter from explicit env", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "gemini-sdk" })).toBe("gemini-sdk");
    expect(isGeminiSdkAdapterEnabled({ NIGHTGAUGE_ADAPTER: "gemini-sdk" })).toBe(true);
  });

  it("auto-selects gemini-sdk when GEMINI_API_KEY is set", () => {
    expect(resolveAdapter({ GEMINI_API_KEY: "test-key" })).toBe("gemini-sdk");
  });

  it("auto-selects gemini-sdk when GOOGLE_API_KEY is set", () => {
    expect(resolveAdapter({ GOOGLE_API_KEY: "test-key" })).toBe("gemini-sdk");
  });

  it("explicit adapter overrides GEMINI_API_KEY auto-selection", () => {
    expect(
      resolveAdapter({
        NIGHTGAUGE_ADAPTER: "gemini",
        GEMINI_API_KEY: "test-key",
      })
    ).toBe("gemini");
  });

  it("defaults to claude-sdk when ANTHROPIC_API_KEY is present", () => {
    expect(resolveAdapter({ ANTHROPIC_API_KEY: "sk-ant-123" })).toBe("claude-sdk");
  });

  it("defaults to claude-headless when no adapter or API key is configured", () => {
    expect(resolveAdapter({})).toBe("claude-headless");
  });

  it("enforces direct API key for claude-sdk and gemini-sdk adapters", () => {
    expect(requiresDirectApiKey("claude-sdk")).toBe(true);
    expect(requiresDirectApiKey("gemini-sdk")).toBe(true);
    expect(requiresDirectApiKey("claude-headless")).toBe(false);
    expect(requiresDirectApiKey("codex")).toBe(false);
    expect(requiresDirectApiKey("gemini")).toBe(false);
  });

  it("resolves lm-studio adapter from explicit env", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "lm-studio" })).toBe("lm-studio");
    expect(isLmStudioAdapterEnabled({ NIGHTGAUGE_ADAPTER: "lm-studio" })).toBe(true);
  });

  it("resolves lm_studio alias to lm-studio", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "lm_studio" })).toBe("lm-studio");
  });

  // #53: the ollama alias was missing — NIGHTGAUGE_ADAPTER=ollama silently
  // resolved to claude-sdk.
  it("resolves ollama adapter from explicit env", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "ollama" })).toBe("ollama");
  });

  it("throws on an unknown explicit adapter instead of silently running claude-sdk", () => {
    expect(() => resolveAdapter({ NIGHTGAUGE_ADAPTER: "olama" })).toThrow(
      /Unknown adapter 'olama'/
    );
    expect(() => resolveAdapter({ NIGHTGAUGE_ADAPTER: "not-a-real-adapter" })).toThrow(
      /Valid values:/
    );
  });

  it("lm-studio does not require a direct API key", () => {
    expect(requiresDirectApiKey("lm-studio")).toBe(false);
  });

  it("detects gemini adapter mode from env", () => {
    expect(isGeminiAdapterEnabled({ NIGHTGAUGE_ADAPTER: "gemini" })).toBe(true);
    expect(isGeminiAdapterEnabled({ NIGHTGAUGE_ADAPTER: "Gemini" })).toBe(true);
    expect(isGeminiAdapterEnabled({ NIGHTGAUGE_ADAPTER: "codex" })).toBe(false);
    expect(isGeminiAdapterEnabled({})).toBe(false);
  });
});

describe("copilot adapter resolution", () => {
  it("resolves copilot adapter from explicit env", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "copilot" })).toBe("copilot");
  });

  it("isCopilotAdapterEnabled returns true for explicit copilot", () => {
    expect(isCopilotAdapterEnabled({ NIGHTGAUGE_ADAPTER: "copilot" })).toBe(true);
  });

  it("resolves github alias to copilot", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "github" })).toBe("copilot");
  });

  it("resolves gh alias to copilot", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "gh" })).toBe("copilot");
  });

  it("auto-selects copilot when COPILOT_GITHUB_TOKEN is set", () => {
    expect(resolveAdapter({ COPILOT_GITHUB_TOKEN: "ghp_test" })).toBe("copilot");
  });

  it("ANTHROPIC_API_KEY takes priority over COPILOT_GITHUB_TOKEN", () => {
    expect(
      resolveAdapter({ ANTHROPIC_API_KEY: "sk-ant-test", COPILOT_GITHUB_TOKEN: "ghp_test" })
    ).toBe("claude-sdk");
  });

  it("GEMINI_API_KEY takes priority over COPILOT_GITHUB_TOKEN", () => {
    expect(resolveAdapter({ GEMINI_API_KEY: "test-key", COPILOT_GITHUB_TOKEN: "ghp_test" })).toBe(
      "gemini-sdk"
    );
  });

  it("explicit adapter overrides COPILOT_GITHUB_TOKEN auto-selection", () => {
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "codex", COPILOT_GITHUB_TOKEN: "ghp_test" })).toBe(
      "codex"
    );
  });

  it("copilot does not require a direct API key", () => {
    expect(requiresDirectApiKey("copilot")).toBe(false);
  });

  it("isCopilotAdapterEnabled returns false when a different adapter is active", () => {
    expect(isCopilotAdapterEnabled({ NIGHTGAUGE_ADAPTER: "codex" })).toBe(false);
    expect(isCopilotAdapterEnabled({})).toBe(false);
  });
});

describe("resolveAdapter — canonical config rungs (#54)", () => {
  const { mkdtempSync, mkdirSync, writeFileSync } = require("node:fs") as typeof import("node:fs");
  const { tmpdir } = require("node:os") as typeof import("node:os");
  const { join } = require("node:path") as typeof import("node:path");

  function workspaceWith(configYaml: string, localYaml?: string): string {
    const root = mkdtempSync(join(tmpdir(), "ng-adapter-"));
    mkdirSync(join(root, ".nightgauge"), { recursive: true });
    writeFileSync(join(root, ".nightgauge", "config.yaml"), configYaml);
    if (localYaml !== undefined) {
      writeFileSync(join(root, ".nightgauge", "config.local.yaml"), localYaml);
    }
    return root;
  }

  it("reads pipeline.stage_adapters.<stage> for the given stage", () => {
    const cwd = workspaceWith("pipeline:\n  stage_adapters:\n    feature-dev: gemini\n");
    expect(resolveAdapter({}, { stage: "feature-dev", cwd })).toBe("gemini");
    // Other stages fall through to the default.
    expect(resolveAdapter({}, { stage: "pr-create", cwd })).toBe("claude-headless");
  });

  it("reads ui.core.adapter as the global config default", () => {
    const cwd = workspaceWith("ui:\n  core:\n    adapter: codex\n");
    expect(resolveAdapter({}, { cwd })).toBe("codex");
    expect(resolveAdapter({}, { stage: "feature-dev", cwd })).toBe("codex");
  });

  it("config.local.yaml overlays the project file per key", () => {
    const cwd = workspaceWith(
      "ui:\n  core:\n    adapter: codex\n",
      "ui:\n  core:\n    adapter: gemini\n"
    );
    expect(resolveAdapter({}, { cwd })).toBe("gemini");
  });

  it("config outranks API-key auto-select; env outranks config", () => {
    const cwd = workspaceWith("ui:\n  core:\n    adapter: codex\n");
    // An incidentally exported key must not override the configured adapter.
    expect(resolveAdapter({ ANTHROPIC_API_KEY: "sk-x" }, { cwd })).toBe("codex");
    // NIGHTGAUGE_ADAPTER stays the per-invocation override.
    expect(resolveAdapter({ NIGHTGAUGE_ADAPTER: "copilot" }, { cwd })).toBe("copilot");
  });

  it("per-stage env outranks everything", () => {
    const cwd = workspaceWith("pipeline:\n  stage_adapters:\n    feature-dev: gemini\n");
    expect(
      resolveAdapter(
        { NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV: "codex", NIGHTGAUGE_ADAPTER: "copilot" },
        { stage: "feature-dev", cwd }
      )
    ).toBe("codex");
  });

  it("maps the collapsed 'claude' vocabulary via aliases and throws on unknown config values", () => {
    const claude = workspaceWith("ui:\n  core:\n    adapter: claude\n");
    expect(resolveAdapter({}, { cwd: claude })).toBe("claude-sdk");
    const bogus = workspaceWith("ui:\n  core:\n    adapter: not-an-adapter\n");
    expect(() => resolveAdapter({}, { cwd: bogus })).toThrow(/Unknown adapter/);
  });

  it("treats an unreadable config as absent (falls through to auto-select)", () => {
    const cwd = mkdtempSync(join(tmpdir(), "ng-noconfig-"));
    expect(resolveAdapter({ ANTHROPIC_API_KEY: "sk-x" }, { cwd })).toBe("claude-sdk");
    expect(resolveAdapter({}, { cwd })).toBe("claude-headless");
  });
});
