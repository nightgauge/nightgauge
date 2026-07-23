/**
 * Tests for Codex configuration additions from issue #1656
 *
 * Covers CodexModelSchema, CodexConfigSchema,
 * UICoreConfigSchema.codex field, and DEFAULT_CONFIG codex defaults.
 *
 * @see Issue #1656 - Codex model routing for Codex adapter
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import { CODEX_DEFAULT_BASE_MODEL } from "@nightgauge/sdk";
import {
  CodexModelSchema,
  CodexConfigSchema,
  UICoreConfigSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

// ============================================================================
// CodexModelSchema
// ============================================================================

describe("CodexModelSchema", () => {
  describe("valid values", () => {
    it("accepts gpt-5.4", () => {
      expect(CodexModelSchema.safeParse("gpt-5.4").success).toBe(true);
    });

    it("accepts gpt-5.4-mini", () => {
      expect(CodexModelSchema.safeParse("gpt-5.4-mini").success).toBe(true);
    });

    it("accepts gpt-5.5", () => {
      expect(CodexModelSchema.safeParse("gpt-5.5").success).toBe(true);
    });

    it("accepts an arbitrary future Codex id (schema is free-form)", () => {
      // CodexModelSchema is z.string() — it intentionally accepts any non-empty
      // string so new Codex ids never require a schema bump. Registry-level
      // validity is enforced separately (see isValidCodexModel).
      expect(CodexModelSchema.safeParse("gpt-6-codex-preview").success).toBe(true);
    });

    it("accepts codex-mini-latest", () => {
      expect(CodexModelSchema.safeParse("codex-mini-latest").success).toBe(true);
    });
  });

  describe("invalid values", () => {
    it("rejects empty string", () => {
      expect(CodexModelSchema.safeParse("").success).toBe(false);
    });

    it("rejects null", () => {
      expect(CodexModelSchema.safeParse(null).success).toBe(false);
    });

    it("rejects undefined", () => {
      expect(CodexModelSchema.safeParse(undefined).success).toBe(false);
    });

    it("rejects numeric value", () => {
      expect(CodexModelSchema.safeParse(5).success).toBe(false);
    });
  });
});

describe("Codex reasoning effort", () => {
  it.each(["none", "low", "medium", "high", "xhigh", "max"])("accepts %s", (reasoning_effort) => {
    expect(CodexConfigSchema.safeParse({ reasoning_effort }).success).toBe(true);
  });

  it("rejects unsupported effort values", () => {
    expect(CodexConfigSchema.safeParse({ reasoning_effort: "ultra" }).success).toBe(false);
  });
});

// ============================================================================
// CodexConfigSchema
// ============================================================================

describe("CodexConfigSchema", () => {
  it("accepts empty object (all fields optional)", () => {
    expect(CodexConfigSchema.safeParse({}).success).toBe(true);
  });

  it("accepts config with model gpt-5.4", () => {
    const result = CodexConfigSchema.safeParse({ model: "gpt-5.4" });
    expect(result.success).toBe(true);
  });

  it("accepts config with CLI command and args overrides", () => {
    const result = CodexConfigSchema.safeParse({
      cli_command: "/opt/homebrew/bin/codex",
      cli_args: "exec --full-auto --sandbox danger-full-access --json",
      resume_enabled: true,
    });
    expect(result.success).toBe(true);
  });

  it("rejects empty CLI command", () => {
    const result = CodexConfigSchema.safeParse({ cli_command: "" });
    expect(result.success).toBe(false);
  });

  it("rejects non-object input", () => {
    expect(CodexConfigSchema.safeParse("gpt-5.4").success).toBe(false);
    expect(CodexConfigSchema.safeParse(42).success).toBe(false);
    expect(CodexConfigSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// UICoreConfigSchema — codex field
// ============================================================================

describe("UICoreConfigSchema", () => {
  describe("codex field", () => {
    it("accepts codex section with valid model", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "codex",
        codex: {
          model: "gpt-5.5",
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts codex section with current GPT-5.4 model", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "codex",
        codex: {
          model: "gpt-5.4",
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts codex section with the lightweight Codex model", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "codex",
        codex: {
          model: "gpt-5.4-mini",
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts codex section with CLI command overrides", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "codex",
        codex: {
          model: "codex-mini-latest",
          cli_command: "codex-nightly",
          cli_args: "exec --full-auto --json",
          resume_enabled: true,
        },
      });
      expect(result.success).toBe(true);
    });

    it("accepts config without codex field (codex is optional)", () => {
      const result = UICoreConfigSchema.safeParse({
        adapter: "claude",
        auth_provider: "max",
        default_model: "sonnet",
      });
      expect(result.success).toBe(true);
    });

    it("accepts empty codex object", () => {
      const result = UICoreConfigSchema.safeParse({
        codex: {},
      });
      expect(result.success).toBe(true);
    });

    it("rejects codex section with invalid CLI command", () => {
      const result = UICoreConfigSchema.safeParse({
        codex: {
          cli_command: "",
        },
      });
      expect(result.success).toBe(false);
    });
  });
});

// ============================================================================
// DEFAULT_CONFIG — codex defaults
// ============================================================================

describe("DEFAULT_CONFIG codex defaults", () => {
  it("includes codex section under ui.core", () => {
    expect(DEFAULT_CONFIG.ui?.core?.codex).toBeDefined();
  });

  it("defaults to the current balanced Codex model and medium effort", () => {
    expect(DEFAULT_CONFIG.ui?.core?.codex?.model).toBe("gpt-5.6-terra");
    expect(DEFAULT_CONFIG.ui?.core?.codex?.reasoning_effort).toBe("medium");
  });

  it("default model tracks the canonical CODEX_DEFAULT_BASE_MODEL", () => {
    expect(DEFAULT_CONFIG.ui?.core?.codex?.model).toBe(CODEX_DEFAULT_BASE_MODEL);
  });

  it("defaults CLI command to codex", () => {
    expect(DEFAULT_CONFIG.ui?.core?.codex?.cli_command).toBe("codex");
  });

  it("defaults session resume to disabled", () => {
    expect(DEFAULT_CONFIG.ui?.core?.codex?.resume_enabled).toBe(false);
  });

  it("default codex config passes CodexConfigSchema validation", () => {
    const result = CodexConfigSchema.safeParse(DEFAULT_CONFIG.ui?.core?.codex);
    expect(result.success).toBe(true);
  });
});
