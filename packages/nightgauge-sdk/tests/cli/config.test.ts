import { describe, expect, it } from "vitest";
import { ConfigValidationError, loadConfigFromEnv, validateConfig } from "../../src/cli/config.js";

describe("lm-studio adapter config", () => {
  it("allows lm-studio adapter without ANTHROPIC_API_KEY", () => {
    const config = loadConfigFromEnv({ NIGHTGAUGE_ADAPTER: "lm-studio" });
    expect(config.adapter).toBe("lm-studio");
    expect(config.apiKey).toBe("");
  });

  it("validateConfig skips apiKey enforcement for lm-studio adapter", () => {
    const config = loadConfigFromEnv({ NIGHTGAUGE_ADAPTER: "lm-studio" });
    expect(() => validateConfig(config, { NIGHTGAUGE_ADAPTER: "lm-studio" })).not.toThrow();
  });

  it("does not auto-select lm-studio from env alone (requires explicit NIGHTGAUGE_ADAPTER)", () => {
    const config = loadConfigFromEnv({
      NIGHTGAUGE_LM_STUDIO_MODEL: "llama-3",
    });
    expect(config.adapter).not.toBe("lm-studio");
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
