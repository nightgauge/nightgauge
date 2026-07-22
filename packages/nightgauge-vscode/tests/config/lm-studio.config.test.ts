/**
 * Tests for LM Studio configuration additions from issue #2057
 *
 * Covers LmStudioConfigSchema, LmStudioStreamOptionsSchema,
 * ExecutionAdapterSchema 'lm-studio' value, and top-level lm_studio
 * in IncrediConfigSchema.
 *
 * @see Issue #2057 - Route pipeline stage execution through LM Studio
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import {
  LmStudioConfigSchema,
  LmStudioStreamOptionsSchema,
  ExecutionAdapterSchema,
  UICoreConfigSchema,
  IncrediConfigSchema,
} from "../../src/config/schema";

// ============================================================================
// LmStudioStreamOptionsSchema
// ============================================================================

describe("LmStudioStreamOptionsSchema", () => {
  it("accepts empty object (all fields optional)", () => {
    expect(LmStudioStreamOptionsSchema.safeParse({}).success).toBe(true);
  });

  it("accepts include_usage true", () => {
    const result = LmStudioStreamOptionsSchema.safeParse({
      include_usage: true,
    });
    expect(result.success).toBe(true);
  });

  it("accepts include_usage false", () => {
    const result = LmStudioStreamOptionsSchema.safeParse({
      include_usage: false,
    });
    expect(result.success).toBe(true);
  });

  it("rejects non-boolean include_usage", () => {
    const result = LmStudioStreamOptionsSchema.safeParse({
      include_usage: "yes",
    });
    expect(result.success).toBe(false);
  });

  it("rejects non-object input", () => {
    expect(LmStudioStreamOptionsSchema.safeParse("true").success).toBe(false);
    expect(LmStudioStreamOptionsSchema.safeParse(42).success).toBe(false);
    expect(LmStudioStreamOptionsSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// LmStudioConfigSchema
// ============================================================================

describe("LmStudioConfigSchema", () => {
  it("accepts empty object (all fields optional)", () => {
    expect(LmStudioConfigSchema.safeParse({}).success).toBe(true);
  });

  it("accepts full config with all fields", () => {
    const result = LmStudioConfigSchema.safeParse({
      base_url: "http://localhost:1234/v1",
      model: "lmstudio-community/Meta-Llama-3.1-8B-Instruct-GGUF",
      context_length: 32768,
      api_key: "lm-studio",
      timeout_ms: 180000,
      max_tokens: 8192,
      stream_options: { include_usage: true },
      tool_calling: false,
    });
    expect(result.success).toBe(true);
  });

  it("accepts config with only model", () => {
    const result = LmStudioConfigSchema.safeParse({
      model: "my-local-model",
    });
    expect(result.success).toBe(true);
  });

  it("accepts positive integer context_length", () => {
    const result = LmStudioConfigSchema.safeParse({
      context_length: 16384,
    });
    expect(result.success).toBe(true);
  });

  it("accepts config with custom base_url", () => {
    const result = LmStudioConfigSchema.safeParse({
      base_url: "https://my-remote-server:8080/v1",
    });
    expect(result.success).toBe(true);
  });

  it("rejects invalid base_url (not a URL)", () => {
    const result = LmStudioConfigSchema.safeParse({
      base_url: "not-a-url",
    });
    expect(result.success).toBe(false);
  });

  it("rejects timeout_ms below 1000", () => {
    const result = LmStudioConfigSchema.safeParse({
      timeout_ms: 500,
    });
    expect(result.success).toBe(false);
  });

  it("rejects non-integer timeout_ms", () => {
    const result = LmStudioConfigSchema.safeParse({
      timeout_ms: 1500.5,
    });
    expect(result.success).toBe(false);
  });

  it("rejects max_tokens below 1", () => {
    const result = LmStudioConfigSchema.safeParse({
      max_tokens: 0,
    });
    expect(result.success).toBe(false);
  });

  it("rejects context_length below 1", () => {
    const result = LmStudioConfigSchema.safeParse({
      context_length: 0,
    });
    expect(result.success).toBe(false);
  });

  it("rejects non-object input", () => {
    expect(LmStudioConfigSchema.safeParse("model").success).toBe(false);
    expect(LmStudioConfigSchema.safeParse(42).success).toBe(false);
    expect(LmStudioConfigSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// ExecutionAdapterSchema — 'lm-studio' value
// ============================================================================

describe("ExecutionAdapterSchema", () => {
  it("accepts lm-studio", () => {
    expect(ExecutionAdapterSchema.safeParse("lm-studio").success).toBe(true);
  });

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

  it("rejects lm_studio (underscore instead of hyphen)", () => {
    expect(ExecutionAdapterSchema.safeParse("lm_studio").success).toBe(false);
  });

  it("rejects empty string", () => {
    expect(ExecutionAdapterSchema.safeParse("").success).toBe(false);
  });

  it("rejects null", () => {
    expect(ExecutionAdapterSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// UICoreConfigSchema — adapter accepts 'lm-studio'
// ============================================================================

describe("UICoreConfigSchema", () => {
  describe("lm-studio adapter", () => {
    it("accepts adapter set to lm-studio", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "lm-studio",
      });
      expect(result.success).toBe(true);
    });

    it("accepts config without lm-studio-specific fields (optional)", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "claude",
        auth_provider: "max",
        default_model: "sonnet",
      });
      expect(result.success).toBe(true);
    });
  });
});

// ============================================================================
// IncrediConfigSchema — top-level lm_studio section
// ============================================================================

describe("IncrediConfigSchema", () => {
  describe("lm_studio section", () => {
    it("accepts config with lm_studio section", () => {
      const result = IncrediConfigSchema.safeParse({
        lm_studio: {
          model: "my-model",
          base_url: "http://localhost:1234/v1",
          api_key: "lm-studio",
          timeout_ms: 180000,
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts config with empty lm_studio section", () => {
      const result = IncrediConfigSchema.safeParse({
        lm_studio: {},
      });
      expect(result.success).toBe(true);
    });

    it("accepts config without lm_studio section", () => {
      const result = IncrediConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("rejects lm_studio with invalid base_url", () => {
      const result = IncrediConfigSchema.safeParse({
        lm_studio: {
          base_url: "not-a-url",
        },
      });
      expect(result.success).toBe(false);
    });
  });
});
