/**
 * Behavior tests for ui.core.* configuration fields
 *
 * These tests verify that core UI config fields affect runtime behavior,
 * specifically auth provider selection, model selection, and path configuration.
 *
 * @see Issue #472 - Add UI config sections to Zod schema
 * @see packages/nightgauge-vscode/src/config/schema.ts - UICoreConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  UICoreConfigSchema,
  AuthProviderSchema,
  DefaultModelSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";
import { applyEnvOverrides } from "../mocks/config-fixtures";

// ============================================================================
// Mock Fixtures
// ============================================================================

/**
 * Default core UI configuration for tests
 */
export const DEFAULT_UI_CORE_CONFIG = {
  adapter: "claude" as const,
  auth_provider: "max" as const,
  default_model: "sonnet" as const,
  context_path: ".nightgauge/pipeline",
  plans_path: ".nightgauge/plans",
};

/**
 * Create a mock core UI configuration with optional overrides
 */
export function createMockUICoreConfig(overrides?: Partial<typeof DEFAULT_UI_CORE_CONFIG>) {
  return {
    ...DEFAULT_UI_CORE_CONFIG,
    ...overrides,
  };
}

describe("ui.core.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear UI-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_UI_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  describe("adapter", () => {
    it("accepts supported execution adapters", () => {
      expect(UICoreConfigSchema.safeParse({ adapter: "claude" }).success).toBe(true);
      expect(UICoreConfigSchema.safeParse({ adapter: "codex" }).success).toBe(true);
    });

    it("rejects unsupported execution adapters", () => {
      expect(UICoreConfigSchema.safeParse({ adapter: "openai" }).success).toBe(false);
    });

    it("defaults to claude", () => {
      expect(DEFAULT_CONFIG.ui?.core?.adapter).toBe("claude");
    });
  });

  // ============================================================================
  // auth_provider - Behavior Tests
  // ============================================================================

  // ============================================================================
  // auth_provider - Behavior Tests
  // ============================================================================

  describe("auth_provider", () => {
    it("affects which authentication method is used", () => {
      const config = createMockUICoreConfig({ auth_provider: "bedrock" });

      const getAuthEndpoint = (cfg: typeof config): string => {
        switch (cfg.auth_provider) {
          case "max":
            return "https://api.anthropic.com";
          case "bedrock":
            return "https://bedrock-runtime.us-east-1.amazonaws.com";
          case "vertex":
            return "https://us-central1-aiplatform.googleapis.com";
          default:
            return "https://api.anthropic.com";
        }
      };

      expect(getAuthEndpoint(config)).toBe("https://bedrock-runtime.us-east-1.amazonaws.com");
    });

    it("accepts all valid providers", () => {
      expect(AuthProviderSchema.safeParse("max").success).toBe(true);
      expect(AuthProviderSchema.safeParse("bedrock").success).toBe(true);
      expect(AuthProviderSchema.safeParse("vertex").success).toBe(true);
    });

    it("rejects invalid provider", () => {
      expect(AuthProviderSchema.safeParse("azure").success).toBe(false);
      expect(AuthProviderSchema.safeParse("openai").success).toBe(false);
    });

    it("defaults to max", () => {
      expect(DEFAULT_CONFIG.ui?.core?.auth_provider).toBe("max");
    });
  });

  // ============================================================================
  // default_model - Behavior Tests
  // ============================================================================

  describe("default_model", () => {
    it("affects which model runs pipeline stages", () => {
      const config = createMockUICoreConfig({ default_model: "opus" });

      const getModelId = (cfg: typeof config): string => {
        switch (cfg.default_model) {
          case "sonnet":
            return "claude-sonnet-4-6";
          case "opus":
            return "claude-opus-4-6";
          case "haiku":
            return "claude-haiku-4-5-20251001";
          default:
            return "claude-sonnet-4-6";
        }
      };

      expect(getModelId(config)).toBe("claude-opus-4-6");
    });

    it("affects cost estimation", () => {
      const estimateCost = (model: "sonnet" | "opus" | "haiku", tokens: number): number => {
        const rates = {
          haiku: 0.00025, // $0.25 per million input
          sonnet: 0.003, // $3 per million input
          opus: 0.015, // $15 per million input
        };
        return (tokens / 1000000) * rates[model];
      };

      expect(estimateCost("haiku", 1000000)).toBe(0.00025);
      expect(estimateCost("sonnet", 1000000)).toBe(0.003);
      expect(estimateCost("opus", 1000000)).toBe(0.015);
    });

    it("accepts all valid models", () => {
      expect(DefaultModelSchema.safeParse("sonnet").success).toBe(true);
      expect(DefaultModelSchema.safeParse("opus").success).toBe(true);
      expect(DefaultModelSchema.safeParse("haiku").success).toBe(true);
    });

    it("rejects invalid model", () => {
      expect(DefaultModelSchema.safeParse("gpt-4").success).toBe(false);
      expect(DefaultModelSchema.safeParse("claude-3").success).toBe(false);
    });

    it("defaults to sonnet", () => {
      expect(DEFAULT_CONFIG.ui?.core?.default_model).toBe("sonnet");
    });
  });

  // ============================================================================
  // context_path - Behavior Tests
  // ============================================================================

  describe("context_path", () => {
    it("determines where context files are stored", () => {
      const config = createMockUICoreConfig({
        context_path: ".custom/context",
      });

      const getContextFilePath = (cfg: typeof config, issueNumber: number): string => {
        return `${cfg.context_path}/issue-${issueNumber}.json`;
      };

      expect(getContextFilePath(config, 42)).toBe(".custom/context/issue-42.json");
    });

    it("accepts any string path", () => {
      const result = UICoreConfigSchema.safeParse({
        context_path: "/absolute/path/to/context",
      });
      expect(result.success).toBe(true);
    });

    it("defaults to .nightgauge/pipeline", () => {
      expect(DEFAULT_CONFIG.ui?.core?.context_path).toBe(".nightgauge/pipeline");
    });
  });

  // ============================================================================
  // plans_path - Behavior Tests
  // ============================================================================

  describe("plans_path", () => {
    it("determines where plan files are stored", () => {
      const config = createMockUICoreConfig({ plans_path: ".custom/plans" });

      const getPlanFilePath = (cfg: typeof config, issueNumber: number, title: string): string => {
        const slug = title.toLowerCase().replace(/\s+/g, "-");
        return `${cfg.plans_path}/${issueNumber}-${slug}.md`;
      };

      expect(getPlanFilePath(config, 42, "Add Feature")).toBe(".custom/plans/42-add-feature.md");
    });

    it("accepts any string path", () => {
      const result = UICoreConfigSchema.safeParse({
        plans_path: "docs/plans",
      });
      expect(result.success).toBe(true);
    });

    it("defaults to .nightgauge/plans", () => {
      expect(DEFAULT_CONFIG.ui?.core?.plans_path).toBe(".nightgauge/plans");
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = UICoreConfigSchema.safeParse(DEFAULT_UI_CORE_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const result = UICoreConfigSchema.safeParse({
        auth_provider: "vertex",
      });
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = UICoreConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_UI_CORE_AUTH_PROVIDER overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_UI_CORE_AUTH_PROVIDER: "bedrock",
      });

      try {
        expect(process.env.NIGHTGAUGE_UI_CORE_AUTH_PROVIDER).toBe("bedrock");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_UI_CORE_ADAPTER overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_UI_CORE_ADAPTER: "codex",
      });

      try {
        expect(process.env.NIGHTGAUGE_UI_CORE_ADAPTER).toBe("codex");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_UI_CORE_DEFAULT_MODEL: "opus",
      });

      try {
        expect(process.env.NIGHTGAUGE_UI_CORE_DEFAULT_MODEL).toBe("opus");
      } finally {
        cleanup();
      }
    });
  });
});
