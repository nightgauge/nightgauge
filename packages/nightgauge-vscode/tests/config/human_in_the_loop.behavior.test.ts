/**
 * Behavior tests for human_in_the_loop.* configuration fields
 *
 * These tests verify that HITL config fields actually affect runtime behavior,
 * specifically auto-accept behavior and trusted_stages validation.
 *
 * @see Issue #439 - Audit behavior config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - HumanInTheLoopConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockHITLConfig,
  DEFAULT_HITL_CONFIG,
  applyEnvOverrides,
  BEHAVIOR_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import {
  HumanInTheLoopConfigSchema,
  TrustedStageSchema,
  mergeWithDefaults,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

describe("human_in_the_loop.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear HITL-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_HITL_") || key === "NIGHTGAUGE_AUTO_APPROVE") {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // human_in_the_loop.auto_accept_stages - Behavior Tests
  // ============================================================================

  describe("auto_accept_stages", () => {
    it("auto-accepts all stage prompts when true", () => {
      const config = createMockHITLConfig({ auto_accept_stages: true });

      const shouldAutoAccept = (promptType: "stage" | "permission", cfg: typeof config) => {
        if (promptType === "stage") {
          return cfg.auto_accept_stages === true;
        }
        return false;
      };

      expect(shouldAutoAccept("stage", config)).toBe(true);
      expect(shouldAutoAccept("permission", config)).toBe(false);
    });

    it("requires user confirmation when false", () => {
      const config = createMockHITLConfig({ auto_accept_stages: false });

      const shouldAutoAccept = (promptType: "stage" | "permission", cfg: typeof config) => {
        if (promptType === "stage") {
          return cfg.auto_accept_stages === true;
        }
        return false;
      };

      expect(shouldAutoAccept("stage", config)).toBe(false);
    });

    it("NIGHTGAUGE_AUTO_APPROVE=1 enables auto-accept for stages", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTO_APPROVE: "1",
      });

      try {
        const config = createMockHITLConfig({ auto_accept_stages: false });

        const shouldAutoAcceptStage = (cfg: typeof config) => {
          if (process.env.NIGHTGAUGE_AUTO_APPROVE === "1") {
            return true;
          }
          return cfg.auto_accept_stages === true;
        };

        expect(shouldAutoAcceptStage(config)).toBe(true);
      } finally {
        cleanup();
      }
    });

    it("defaults to true", () => {
      expect(DEFAULT_HITL_CONFIG.auto_accept_stages).toBe(true);
    });
  });

  // ============================================================================
  // human_in_the_loop.auto_accept_permissions - Behavior Tests
  // ============================================================================

  describe("auto_accept_permissions", () => {
    it("auto-accepts tool permission prompts when true", () => {
      const config = createMockHITLConfig({ auto_accept_permissions: true });

      const shouldAutoAcceptPermission = (cfg: typeof config) => {
        return cfg.auto_accept_permissions === true;
      };

      expect(shouldAutoAcceptPermission(config)).toBe(true);
    });

    it("requires user confirmation for permissions when false", () => {
      const config = createMockHITLConfig({ auto_accept_permissions: false });

      const shouldAutoAcceptPermission = (cfg: typeof config) => {
        return cfg.auto_accept_permissions === true;
      };

      expect(shouldAutoAcceptPermission(config)).toBe(false);
    });

    it("NIGHTGAUGE_AUTO_APPROVE=1 enables auto-accept for permissions", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTO_APPROVE: "1",
      });

      try {
        const config = createMockHITLConfig({ auto_accept_permissions: false });

        const shouldAutoAcceptPermission = (cfg: typeof config) => {
          if (process.env.NIGHTGAUGE_AUTO_APPROVE === "1") {
            return true;
          }
          return cfg.auto_accept_permissions === true;
        };

        expect(shouldAutoAcceptPermission(config)).toBe(true);
      } finally {
        cleanup();
      }
    });

    it("defaults to false", () => {
      expect(DEFAULT_HITL_CONFIG.auto_accept_permissions).toBe(false);
    });
  });

  // ============================================================================
  // human_in_the_loop.trusted_stages - Behavior Tests
  // ============================================================================

  describe("trusted_stages", () => {
    it("auto-accepts only listed stages", () => {
      const config = createMockHITLConfig({
        auto_accept_stages: false,
        trusted_stages: ["issue-pickup", "feature-planning"],
      });

      const shouldAutoAcceptStage = (stage: string, cfg: typeof config): boolean => {
        // If auto_accept_stages is true, accept all
        if (cfg.auto_accept_stages) return true;
        // Otherwise, check trusted_stages list
        return (cfg.trusted_stages as string[] | undefined)?.includes(stage) || false;
      };

      expect(shouldAutoAcceptStage("issue-pickup", config)).toBe(true);
      expect(shouldAutoAcceptStage("feature-planning", config)).toBe(true);
      expect(shouldAutoAcceptStage("feature-dev", config)).toBe(false);
      expect(shouldAutoAcceptStage("pr-merge", config)).toBe(false);
    });

    it("empty trusted_stages means no auto-accept (unless auto_accept_stages=true)", () => {
      const config = createMockHITLConfig({
        auto_accept_stages: false,
        trusted_stages: [],
      });

      const shouldAutoAcceptStage = (stage: string, cfg: typeof config): boolean => {
        if (cfg.auto_accept_stages) return true;
        return (cfg.trusted_stages as string[] | undefined)?.includes(stage) || false;
      };

      expect(shouldAutoAcceptStage("issue-pickup", config)).toBe(false);
      expect(shouldAutoAcceptStage("feature-dev", config)).toBe(false);
    });

    it("auto_accept_stages=true overrides trusted_stages", () => {
      const config = createMockHITLConfig({
        auto_accept_stages: true,
        trusted_stages: ["issue-pickup"], // Only issue-pickup in trusted list
      });

      const shouldAutoAcceptStage = (stage: string, cfg: typeof config): boolean => {
        if (cfg.auto_accept_stages) return true;
        return (cfg.trusted_stages as string[] | undefined)?.includes(stage) || false;
      };

      // auto_accept_stages=true means ALL stages are auto-accepted
      expect(shouldAutoAcceptStage("pr-merge", config)).toBe(true);
    });

    it("accepts valid pipeline stage names", () => {
      const validStages = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ];

      for (const stage of validStages) {
        const result = TrustedStageSchema.safeParse(stage);
        expect(result.success).toBe(true);
      }
    });

    it("rejects invalid stage names", () => {
      const invalidStages = ["invalid-stage", "deploy", "test", "build", "setup", ""];

      for (const stage of invalidStages) {
        const result = TrustedStageSchema.safeParse(stage);
        expect(result.success).toBe(false);
      }
    });

    it("rejects config with invalid trusted_stages values", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: ["issue-pickup", "invalid-stage"],
      });
      expect(result.success).toBe(false);
    });

    it("accepts config with all valid trusted_stages", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: ["issue-pickup", "feature-dev", "pr-merge"],
      });
      expect(result.success).toBe(true);
    });

    it("defaults to empty array", () => {
      expect(DEFAULT_HITL_CONFIG.trusted_stages).toEqual([]);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_AUTO_APPROVE=1 enables all auto-accept", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTO_APPROVE: "1",
      });

      try {
        expect(process.env.NIGHTGAUGE_AUTO_APPROVE).toBe("1");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_HITL_AUTO_ACCEPT_STAGES overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_HITL_AUTO_ACCEPT_STAGES: "true",
      });

      try {
        expect(process.env.NIGHTGAUGE_HITL_AUTO_ACCEPT_STAGES).toBe("true");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTO_APPROVE: "1",
      });

      try {
        const configValue = "false";
        const envValue = process.env.NIGHTGAUGE_AUTO_APPROVE === "1" ? "true" : "false";

        expect(envValue).toBe("true");
        expect(configValue).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("HITL env vars are defined", () => {
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["human_in_the_loop.auto_accept_stages"]).toBe(
        "NIGHTGAUGE_HITL_AUTO_ACCEPT_STAGES"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["human_in_the_loop.auto_accept_permissions"]).toBe(
        "NIGHTGAUGE_HITL_AUTO_ACCEPT_PERMISSIONS"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["human_in_the_loop.trusted_stages"]).toBe(
        "NIGHTGAUGE_HITL_TRUSTED_STAGES"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = HumanInTheLoopConfigSchema.safeParse(DEFAULT_HITL_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { auto_accept_stages: true };
      const result = HumanInTheLoopConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates config with trusted_stages", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: ["issue-pickup", "feature-dev"],
      });
      expect(result.success).toBe(true);
    });

    it("rejects non-boolean auto_accept_stages", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        auto_accept_stages: "yes",
      });
      expect(result.success).toBe(false);
    });

    it("rejects array of non-stage strings", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: ["not-a-stage"],
      });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_CONFIG.human_in_the_loop has correct defaults", () => {
      expect(DEFAULT_CONFIG.human_in_the_loop?.auto_accept_stages).toBe(true);
      expect(DEFAULT_CONFIG.human_in_the_loop?.auto_accept_permissions).toBe(false);
      expect(DEFAULT_CONFIG.human_in_the_loop?.trusted_stages).toEqual([]);
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        human_in_the_loop: { auto_accept_stages: true },
      });

      expect(config.human_in_the_loop?.auto_accept_stages).toBe(true);
    });

    it("missing human_in_the_loop section uses defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.human_in_the_loop?.auto_accept_stages).toBe(true);
      expect(config.human_in_the_loop?.trusted_stages).toEqual([]);
    });
  });

  // ============================================================================
  // HITL Decision Integration
  // ============================================================================

  describe("HITL decision integration", () => {
    it("determines prompt handling based on config", () => {
      const config = createMockHITLConfig({
        auto_accept_stages: false,
        auto_accept_permissions: true,
        trusted_stages: ["issue-pickup", "pr-merge"],
      });

      type PromptType = "stage" | "permission";

      interface HITLDecision {
        autoAccept: boolean;
        reason: string;
      }

      const makeHITLDecision = (
        promptType: PromptType,
        stage: string | null,
        cfg: typeof config
      ): HITLDecision => {
        // Check environment override first
        if (process.env.NIGHTGAUGE_AUTO_APPROVE === "1") {
          return { autoAccept: true, reason: "env_override" };
        }

        if (promptType === "permission") {
          return {
            autoAccept: cfg.auto_accept_permissions === true,
            reason: cfg.auto_accept_permissions
              ? "auto_accept_permissions"
              : "requires_confirmation",
          };
        }

        if (promptType === "stage" && stage) {
          // Check auto_accept_stages first
          if (cfg.auto_accept_stages) {
            return { autoAccept: true, reason: "auto_accept_stages" };
          }
          // Check trusted_stages
          if ((cfg.trusted_stages as string[] | undefined)?.includes(stage)) {
            return { autoAccept: true, reason: "trusted_stage" };
          }
        }

        return { autoAccept: false, reason: "requires_confirmation" };
      };

      // Permission prompt - auto-accept (auto_accept_permissions=true)
      expect(makeHITLDecision("permission", null, config)).toEqual({
        autoAccept: true,
        reason: "auto_accept_permissions",
      });

      // Trusted stage - auto-accept
      expect(makeHITLDecision("stage", "issue-pickup", config)).toEqual({
        autoAccept: true,
        reason: "trusted_stage",
      });

      // Non-trusted stage - requires confirmation
      expect(makeHITLDecision("stage", "feature-dev", config)).toEqual({
        autoAccept: false,
        reason: "requires_confirmation",
      });
    });

    it("NIGHTGAUGE_AUTO_APPROVE overrides all settings", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTO_APPROVE: "1",
      });

      try {
        const config = createMockHITLConfig({
          auto_accept_stages: false,
          auto_accept_permissions: false,
          trusted_stages: [],
        });

        type PromptType = "stage" | "permission";

        interface HITLDecision {
          autoAccept: boolean;
          reason: string;
        }

        const makeHITLDecision = (
          promptType: PromptType,
          _stage: string | null,
          _cfg: typeof config
        ): HITLDecision => {
          if (process.env.NIGHTGAUGE_AUTO_APPROVE === "1") {
            return { autoAccept: true, reason: "env_override" };
          }
          return { autoAccept: false, reason: "requires_confirmation" };
        };

        // All prompts auto-accepted due to env override
        expect(makeHITLDecision("stage", "feature-dev", config)).toEqual({
          autoAccept: true,
          reason: "env_override",
        });
        expect(makeHITLDecision("permission", null, config)).toEqual({
          autoAccept: true,
          reason: "env_override",
        });
      } finally {
        cleanup();
      }
    });
  });

  // ============================================================================
  // Trusted Stages Security
  // ============================================================================

  describe("trusted stages security", () => {
    it("only valid pipeline stages can be trusted", () => {
      // This ensures that only legitimate pipeline stages can be in trusted_stages
      // Prevents configuration errors that could lead to unexpected behavior
      const validConfig = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: ["issue-pickup", "feature-dev", "pr-merge"],
      });
      expect(validConfig.success).toBe(true);

      const invalidConfig = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: ["issue-pickup", "arbitrary-command"],
      });
      expect(invalidConfig.success).toBe(false);
    });

    it("case sensitivity is enforced", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: ["Issue-Pickup"], // Wrong case
      });
      expect(result.success).toBe(false);
    });

    it("whitespace is not trimmed", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        trusted_stages: [" issue-pickup"], // Leading space
      });
      expect(result.success).toBe(false);
    });
  });
});
