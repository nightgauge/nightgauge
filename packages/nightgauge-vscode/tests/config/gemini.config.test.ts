/**
 * Tests for Gemini configuration additions from issue #1056
 *
 * Covers GeminiAuthMethodSchema, GeminiModelSchema, GeminiConfigSchema,
 * UICoreConfigSchema.gemini field, ExecutionAdapterSchema 'gemini-sdk' value,
 * and DEFAULT_CONFIG gemini defaults.
 *
 * @see Issue #1056 - Gemini VSCode configuration UI
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import {
  GeminiAuthMethodSchema,
  GeminiModelSchema,
  GeminiConfigSchema,
  UICoreConfigSchema,
  ExecutionAdapterSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

// ============================================================================
// GeminiAuthMethodSchema
// ============================================================================

describe("GeminiAuthMethodSchema", () => {
  describe("valid values", () => {
    it("accepts api-key", () => {
      expect(GeminiAuthMethodSchema.safeParse("api-key").success).toBe(true);
    });

    it("accepts google-login", () => {
      expect(GeminiAuthMethodSchema.safeParse("google-login").success).toBe(true);
    });

    it("accepts vertex-ai", () => {
      expect(GeminiAuthMethodSchema.safeParse("vertex-ai").success).toBe(true);
    });
  });

  describe("invalid values", () => {
    it("rejects empty string", () => {
      expect(GeminiAuthMethodSchema.safeParse("").success).toBe(false);
    });

    it("rejects unknown auth method", () => {
      expect(GeminiAuthMethodSchema.safeParse("oauth").success).toBe(false);
    });

    it("rejects service-account", () => {
      expect(GeminiAuthMethodSchema.safeParse("service-account").success).toBe(false);
    });

    it("rejects null", () => {
      expect(GeminiAuthMethodSchema.safeParse(null).success).toBe(false);
    });

    it("rejects undefined", () => {
      expect(GeminiAuthMethodSchema.safeParse(undefined).success).toBe(false);
    });

    it("rejects numeric value", () => {
      expect(GeminiAuthMethodSchema.safeParse(1).success).toBe(false);
    });
  });
});

// ============================================================================
// GeminiModelSchema
// ============================================================================

describe("GeminiModelSchema", () => {
  describe("valid values", () => {
    it("accepts gemini-2.5-pro", () => {
      expect(GeminiModelSchema.safeParse("gemini-2.5-pro").success).toBe(true);
    });

    it("accepts gemini-2.5-flash", () => {
      expect(GeminiModelSchema.safeParse("gemini-2.5-flash").success).toBe(true);
    });

    it("accepts gemini-2.0-flash", () => {
      expect(GeminiModelSchema.safeParse("gemini-2.0-flash").success).toBe(true);
    });
  });

  describe("invalid values", () => {
    it("rejects empty string", () => {
      expect(GeminiModelSchema.safeParse("").success).toBe(false);
    });

    it("rejects unknown model", () => {
      expect(GeminiModelSchema.safeParse("gemini-1.5-pro").success).toBe(false);
    });

    it("rejects claude model name", () => {
      expect(GeminiModelSchema.safeParse("claude-sonnet-4-6").success).toBe(false);
    });

    it("rejects null", () => {
      expect(GeminiModelSchema.safeParse(null).success).toBe(false);
    });

    it("rejects undefined", () => {
      expect(GeminiModelSchema.safeParse(undefined).success).toBe(false);
    });

    it("rejects numeric value", () => {
      expect(GeminiModelSchema.safeParse(2).success).toBe(false);
    });
  });
});

// ============================================================================
// GeminiConfigSchema
// ============================================================================

describe("GeminiConfigSchema", () => {
  it("accepts empty object (all fields optional)", () => {
    expect(GeminiConfigSchema.safeParse({}).success).toBe(true);
  });

  it("accepts full nested config with valid auth_method and model", () => {
    const result = GeminiConfigSchema.safeParse({
      auth_method: "api-key",
      model: "gemini-2.5-pro",
    });
    expect(result.success).toBe(true);
  });

  it("accepts partial config with only auth_method", () => {
    const result = GeminiConfigSchema.safeParse({
      auth_method: "google-login",
    });
    expect(result.success).toBe(true);
  });

  it("accepts partial config with only model", () => {
    const result = GeminiConfigSchema.safeParse({
      model: "gemini-2.5-flash",
    });
    expect(result.success).toBe(true);
  });

  it("accepts vertex-ai auth_method with gemini-2.0-flash model", () => {
    const result = GeminiConfigSchema.safeParse({
      auth_method: "vertex-ai",
      model: "gemini-2.0-flash",
    });
    expect(result.success).toBe(true);
  });

  it("rejects invalid auth_method", () => {
    const result = GeminiConfigSchema.safeParse({
      auth_method: "invalid-method",
      model: "gemini-2.5-pro",
    });
    expect(result.success).toBe(false);
  });

  it("rejects invalid model", () => {
    const result = GeminiConfigSchema.safeParse({
      auth_method: "api-key",
      model: "gemini-1.0-pro",
    });
    expect(result.success).toBe(false);
  });

  it("rejects non-object input", () => {
    expect(GeminiConfigSchema.safeParse("api-key").success).toBe(false);
    expect(GeminiConfigSchema.safeParse(42).success).toBe(false);
    expect(GeminiConfigSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// UICoreConfigSchema — gemini field
// ============================================================================

describe("UICoreConfigSchema", () => {
  describe("gemini field", () => {
    it("accepts gemini section with valid auth_method and model", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "gemini-sdk",
        gemini: {
          auth_method: "api-key",
          model: "gemini-2.5-flash",
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts gemini section with google-login", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "gemini",
        gemini: {
          auth_method: "google-login",
          model: "gemini-2.5-pro",
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts gemini section with vertex-ai", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "gemini-sdk",
        gemini: {
          auth_method: "vertex-ai",
          model: "gemini-2.0-flash",
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts config without gemini field (gemini is optional)", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "claude",
        auth_provider: "max",
        default_model: "sonnet",
      });
      expect(result.success).toBe(true);
    });

    it("accepts empty gemini object", () => {
      const result = UICoreConfigSchema.safeParse({
        gemini: {},
      });
      expect(result.success).toBe(true);
    });

    it("rejects gemini section with invalid auth_method", () => {
      const result = UICoreConfigSchema.safeParse({
        gemini: {
          auth_method: "unknown-auth",
        },
      });
      expect(result.success).toBe(false);
    });

    it("rejects gemini section with invalid model", () => {
      const result = UICoreConfigSchema.safeParse({
        gemini: {
          model: "gemini-1.5-ultra",
        },
      });
      expect(result.success).toBe(false);
    });
  });
});

// ============================================================================
// ExecutionAdapterSchema — 'gemini-sdk' value
// ============================================================================

describe("ExecutionAdapterSchema", () => {
  it("accepts claude", () => {
    expect(ExecutionAdapterSchema.safeParse("claude").success).toBe(true);
  });

  it("accepts codex", () => {
    expect(ExecutionAdapterSchema.safeParse("codex").success).toBe(true);
  });

  it("accepts gemini", () => {
    expect(ExecutionAdapterSchema.safeParse("gemini").success).toBe(true);
  });

  it("accepts gemini-sdk", () => {
    expect(ExecutionAdapterSchema.safeParse("gemini-sdk").success).toBe(true);
  });

  it("rejects unknown adapter", () => {
    expect(ExecutionAdapterSchema.safeParse("openai").success).toBe(false);
  });

  it("rejects empty string", () => {
    expect(ExecutionAdapterSchema.safeParse("").success).toBe(false);
  });

  it("rejects null", () => {
    expect(ExecutionAdapterSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// DEFAULT_CONFIG — gemini defaults
// ============================================================================

describe("DEFAULT_CONFIG gemini defaults", () => {
  it("includes gemini section under ui.core", () => {
    expect(DEFAULT_CONFIG.ui?.core?.gemini).toBeDefined();
  });

  it("defaults auth_method to api-key", () => {
    expect(DEFAULT_CONFIG.ui?.core?.gemini?.auth_method).toBe("api-key");
  });

  it("defaults model to gemini-2.5-flash", () => {
    expect(DEFAULT_CONFIG.ui?.core?.gemini?.model).toBe("gemini-2.5-flash");
  });

  it("default gemini config passes GeminiConfigSchema validation", () => {
    const result = GeminiConfigSchema.safeParse(DEFAULT_CONFIG.ui?.core?.gemini);
    expect(result.success).toBe(true);
  });
});
