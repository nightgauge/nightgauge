import { describe, expect, it } from "vitest";
import { ConfigValidationError, loadConfigFromEnv, validateConfig } from "../../src/cli/config.js";

describe("openai-compatible adapter config", () => {
  it("allows the openai-compatible adapter without ANTHROPIC_API_KEY", () => {
    const config = loadConfigFromEnv({ NIGHTGAUGE_ADAPTER: "openai-compatible" });
    expect(config.adapter).toBe("openai-compatible");
    expect(config.apiKey).toBe("");
  });

  it("refuses the removed lm-studio adapter with the migration (#2128)", () => {
    expect(() => loadConfigFromEnv({ NIGHTGAUGE_ADAPTER: "lm-studio" })).toThrow(
      /removed \(#2128\)/
    );
  });
});

describe("cli config auth contract", () => {
  it("allows realistic coding stages by default while retaining a one-hour bound", () => {
    const config = loadConfigFromEnv({ NIGHTGAUGE_ADAPTER: "codex" });

    expect(config.stageTimeoutMs).toBe(3_600_000);
    expect(config.stageTimeoutMs).toBe(config.globalTimeoutMs);
  });

  it("honors an explicit per-stage timeout override", () => {
    const config = loadConfigFromEnv({
      NIGHTGAUGE_ADAPTER: "codex",
      NIGHTGAUGE_STAGE_TIMEOUT: "1800000",
    });

    expect(config.stageTimeoutMs).toBe(1_800_000);
  });

  it("defaults to claude-headless adapter when no explicit adapter or API key is set", () => {
    const config = loadConfigFromEnv({});

    expect(config.adapter).toBe("claude-headless");
    expect(config.apiKey).toBe("");
  });

  it("allows codex adapter mode without ANTHROPIC_API_KEY", () => {
    const config = loadConfigFromEnv({
      NIGHTGAUGE_ADAPTER: "codex",
      NIGHTGAUGE_OUTPUT_FORMAT: "json",
      NIGHTGAUGE_LOG_LEVEL: "debug",
    });

    expect(config.apiKey).toBe("");
    expect(config.adapter).toBe("codex");
    expect(config.outputFormat).toBe("json");
    expect(config.logLevel).toBe("debug");
  });

  it("requires ANTHROPIC_API_KEY when claude-sdk adapter is explicitly selected", () => {
    expect(() =>
      loadConfigFromEnv({
        NIGHTGAUGE_ADAPTER: "claude-sdk",
      })
    ).toThrow(/ANTHROPIC_API_KEY/);
  });

  it("validateConfig skips apiKey enforcement in codex adapter mode", () => {
    const config = loadConfigFromEnv({
      NIGHTGAUGE_ADAPTER: "codex",
    });

    expect(() =>
      validateConfig(config, {
        NIGHTGAUGE_ADAPTER: "codex",
      })
    ).not.toThrow();
  });

  it("validateConfig enforces apiKey outside codex adapter mode", () => {
    const config = loadConfigFromEnv({
      NIGHTGAUGE_ADAPTER: "codex",
    });

    expect(() =>
      validateConfig(config, {
        NIGHTGAUGE_ADAPTER: "claude-sdk",
      })
    ).toThrow(ConfigValidationError);
  });
});
