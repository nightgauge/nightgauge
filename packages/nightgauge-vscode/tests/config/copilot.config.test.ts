/**
 * Tests for Copilot configuration additions from issue #1945
 *
 * Covers CopilotConfigSchema, ExecutionAdapterSchema 'copilot' value, and
 * UICoreConfigSchema copilot field.
 *
 * @see Issue #1945 - Add Copilot to VSCode config schema and adapter switcher
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import {
  CopilotConfigSchema,
  ExecutionAdapterSchema,
  UICoreConfigSchema,
} from "../../src/config/schema";

// ============================================================================
// CopilotConfigSchema
// ============================================================================

describe("CopilotConfigSchema", () => {
  it("accepts empty object (all fields optional)", () => {
    expect(CopilotConfigSchema.safeParse({}).success).toBe(true);
  });

  it('accepts { model: "gpt-4o" }', () => {
    const result = CopilotConfigSchema.safeParse({ model: "gpt-4o" });
    expect(result.success).toBe(true);
  });

  it("accepts any free-form model string", () => {
    const result = CopilotConfigSchema.safeParse({
      model: "claude-3.5-sonnet-20241022",
    });
    expect(result.success).toBe(true);
  });

  it("rejects non-string model", () => {
    const result = CopilotConfigSchema.safeParse({ model: 42 });
    expect(result.success).toBe(false);
  });

  it("rejects non-object input", () => {
    expect(CopilotConfigSchema.safeParse("model").success).toBe(false);
    expect(CopilotConfigSchema.safeParse(42).success).toBe(false);
    expect(CopilotConfigSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// ExecutionAdapterSchema — 'copilot' value
// ============================================================================

describe("ExecutionAdapterSchema", () => {
  it("accepts copilot", () => {
    expect(ExecutionAdapterSchema.safeParse("copilot").success).toBe(true);
  });

  it("accepts claude", () => {
    expect(ExecutionAdapterSchema.safeParse("claude").success).toBe(true);
  });

  it("accepts codex", () => {
    expect(ExecutionAdapterSchema.safeParse("codex").success).toBe(true);
  });

  it("accepts lm-studio", () => {
    expect(ExecutionAdapterSchema.safeParse("lm-studio").success).toBe(true);
  });

  it("accepts gemini", () => {
    expect(ExecutionAdapterSchema.safeParse("gemini").success).toBe(true);
  });

  it("rejects unknown adapter", () => {
    expect(ExecutionAdapterSchema.safeParse("openai").success).toBe(false);
  });

  it("rejects empty string", () => {
    expect(ExecutionAdapterSchema.safeParse("").success).toBe(false);
  });
});

// ============================================================================
// UICoreConfigSchema — copilot field
// ============================================================================

describe("UICoreConfigSchema", () => {
  describe("copilot adapter", () => {
    it("accepts adapter set to copilot", () => {
      const result = UICoreConfigSchema.safeParse({ adapter: "copilot" });
      expect(result.success).toBe(true);
    });

    it("accepts config with copilot model override", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "copilot",
        copilot: { model: "gpt-4o" },
      });
      expect(result.success).toBe(true);
    });

    it("accepts config with empty copilot section (all optional)", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "copilot",
        copilot: {},
      });
      expect(result.success).toBe(true);
    });

    it("accepts config without copilot section (copilot is optional)", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "claude",
        auth_provider: "max",
        default_model: "sonnet",
      });
      expect(result.success).toBe(true);
    });

    it("rejects copilot with non-string model", () => {
      const result = UICoreConfigSchema.safeParse({
        copilot: { model: 123 },
      });
      expect(result.success).toBe(false);
    });
  });
});
