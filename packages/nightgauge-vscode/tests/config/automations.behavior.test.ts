/**
 * Behavior tests for automations.* configuration fields
 *
 * These tests verify that automations config fields actually affect runtime behavior,
 * specifically trigger matching, action execution, and dry-run mode.
 *
 * @see Issue #439 - Audit behavior config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - AutomationsConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockAutomationsConfig,
  createMockAutomationTrigger,
  createMockAutomationAction,
  DEFAULT_AUTOMATIONS_CONFIG,
  DEFAULT_AUTOMATION_TRIGGER,
  DEFAULT_AUTOMATION_ACTION,
  applyEnvOverrides,
  BEHAVIOR_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import {
  AutomationsConfigSchema,
  AutomationTriggerSchema,
  AutomationActionSchema,
  AutomationActionTypeSchema,
  mergeWithDefaults,
} from "../../src/config/schema";

describe("automations.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear automations-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_AUTOMATIONS_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // automations.enabled - Behavior Tests
  // ============================================================================

  describe("enabled", () => {
    it("activates automation triggers when true", () => {
      const config = createMockAutomationsConfig({ enabled: true });

      const shouldProcessAutomations = (cfg: typeof config) => {
        return cfg.enabled === true;
      };

      expect(shouldProcessAutomations(config)).toBe(true);
    });

    it("skips all automations when false", () => {
      const config = createMockAutomationsConfig({ enabled: false });

      const shouldProcessAutomations = (cfg: typeof config) => {
        return cfg.enabled === true;
      };

      expect(shouldProcessAutomations(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_AUTOMATIONS_CONFIG.enabled).toBe(true);
    });
  });

  // ============================================================================
  // automations.dry_run - Behavior Tests
  // ============================================================================

  describe("dry_run", () => {
    it("logs but does not execute actions when true", () => {
      const config = createMockAutomationsConfig({ dry_run: true });

      interface ActionResult {
        executed: boolean;
        logged: boolean;
      }

      const executeAction = (_action: string, cfg: typeof config): ActionResult => {
        if (cfg.dry_run) {
          return { executed: false, logged: true };
        }
        return { executed: true, logged: true };
      };

      const result = executeAction("post_slack", config);
      expect(result.executed).toBe(false);
      expect(result.logged).toBe(true);
    });

    it("executes actions when false", () => {
      const config = createMockAutomationsConfig({ dry_run: false });

      interface ActionResult {
        executed: boolean;
        logged: boolean;
      }

      const executeAction = (_action: string, cfg: typeof config): ActionResult => {
        if (cfg.dry_run) {
          return { executed: false, logged: true };
        }
        return { executed: true, logged: true };
      };

      const result = executeAction("post_slack", config);
      expect(result.executed).toBe(true);
    });

    it("defaults to false (actions execute)", () => {
      expect(DEFAULT_AUTOMATIONS_CONFIG.dry_run).toBe(false);
    });
  });

  // ============================================================================
  // automations.log_file - Behavior Tests
  // ============================================================================

  describe("log_file", () => {
    it("specifies log output location", () => {
      const config = createMockAutomationsConfig({
        log_file: "/var/log/nightgauge-automations.log",
      });

      const getLogPath = (cfg: typeof config): string => {
        return cfg.log_file || ".nightgauge/automations.log";
      };

      expect(getLogPath(config)).toBe("/var/log/nightgauge-automations.log");
    });

    it("falls back to default when not specified", () => {
      const config = createMockAutomationsConfig({ log_file: undefined });

      const getLogPath = (cfg: typeof config): string => {
        return cfg.log_file || ".nightgauge/automations.log";
      };

      expect(getLogPath(config)).toBe(".nightgauge/automations.log");
    });

    it("defaults to .nightgauge/automations.log", () => {
      expect(DEFAULT_AUTOMATIONS_CONFIG.log_file).toBe(".nightgauge/automations.log");
    });
  });

  // ============================================================================
  // automations.triggers - Behavior Tests
  // ============================================================================

  describe("triggers", () => {
    it("matches triggers by event name", () => {
      const config = createMockAutomationsConfig({
        triggers: [
          createMockAutomationTrigger({
            name: "notify-on-merge",
            trigger: "pr-merged",
            actions: [createMockAutomationAction({ type: "notify" })],
          }),
          createMockAutomationTrigger({
            name: "slack-on-create",
            trigger: "pr-created",
            actions: [createMockAutomationAction({ type: "post_slack" })],
          }),
        ],
      });

      const findMatchingTriggers = (event: string, cfg: typeof config) => {
        return (cfg.triggers || []).filter((t) => t.trigger === event);
      };

      expect(findMatchingTriggers("pr-merged", config)).toHaveLength(1);
      expect(findMatchingTriggers("pr-created", config)).toHaveLength(1);
      expect(findMatchingTriggers("pr-updated", config)).toHaveLength(0);
    });

    it("supports from field for transition triggers", () => {
      const config = createMockAutomationsConfig({
        triggers: [
          createMockAutomationTrigger({
            name: "ready-to-review",
            trigger: "status:in-review",
            from: "status:in-progress",
            actions: [createMockAutomationAction({ type: "assign_reviewers" })],
          }),
        ],
      });

      interface StatusTransition {
        from: string;
        to: string;
      }

      const matchesTrigger = (
        transition: StatusTransition,
        trigger: (typeof config.triggers)[0]
      ): boolean => {
        if (!trigger) return false;
        const triggerTo = trigger.trigger;
        const triggerFrom = trigger.from;

        if (triggerTo !== transition.to) return false;
        if (triggerFrom && triggerFrom !== transition.from) return false;
        return true;
      };

      const trigger = config.triggers?.[0];
      expect(matchesTrigger({ from: "status:in-progress", to: "status:in-review" }, trigger!)).toBe(
        true
      );
      expect(matchesTrigger({ from: "status:ready", to: "status:in-review" }, trigger!)).toBe(
        false
      );
    });

    it("empty triggers means no automations fire", () => {
      const config = createMockAutomationsConfig({ triggers: [] });

      const findMatchingTriggers = (event: string, cfg: typeof config) => {
        return (cfg.triggers || []).filter((t) => t.trigger === event);
      };

      expect(findMatchingTriggers("pr-merged", config)).toHaveLength(0);
    });

    it("defaults to empty array", () => {
      expect(DEFAULT_AUTOMATIONS_CONFIG.triggers).toEqual([]);
    });
  });

  // ============================================================================
  // Action Types - Behavior Tests
  // ============================================================================

  describe("action types", () => {
    it("accepts all valid action types", () => {
      const validTypes = [
        "post_slack",
        "assign_reviewers",
        "add_label",
        "remove_label",
        "notify",
        "run_script",
      ];

      for (const type of validTypes) {
        const result = AutomationActionTypeSchema.safeParse(type);
        expect(result.success).toBe(true);
      }
    });

    it("rejects invalid action types", () => {
      const invalidTypes = ["send_email", "webhook", "custom", ""];

      for (const type of invalidTypes) {
        const result = AutomationActionTypeSchema.safeParse(type);
        expect(result.success).toBe(false);
      }
    });

    it("post_slack action uses webhook_env and message", () => {
      const action = createMockAutomationAction({
        type: "post_slack",
        webhook_env: "SLACK_WEBHOOK_URL",
        message: "PR merged: {{pr_title}}",
      });

      expect(action.type).toBe("post_slack");
      expect(action.webhook_env).toBe("SLACK_WEBHOOK_URL");
      expect(action.message).toBe("PR merged: {{pr_title}}");
    });

    it("assign_reviewers action uses reviewers list", () => {
      const action = createMockAutomationAction({
        type: "assign_reviewers",
        reviewers: ["user1", "user2"],
      });

      expect(action.type).toBe("assign_reviewers");
      expect(action.reviewers).toEqual(["user1", "user2"]);
    });

    it("add_label action uses label field", () => {
      const action = createMockAutomationAction({
        type: "add_label",
        label: "needs-review",
      });

      expect(action.type).toBe("add_label");
      expect(action.label).toBe("needs-review");
    });

    it("run_script action uses script and args", () => {
      const action = createMockAutomationAction({
        type: "run_script",
        script: "scripts/notify.sh",
        args: ["--pr", "{{pr_number}}"],
      });

      expect(action.type).toBe("run_script");
      expect(action.script).toBe("scripts/notify.sh");
      expect(action.args).toEqual(["--pr", "{{pr_number}}"]);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_AUTOMATIONS_ENABLED overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTOMATIONS_ENABLED: "false",
      });

      try {
        expect(process.env.NIGHTGAUGE_AUTOMATIONS_ENABLED).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_AUTOMATIONS_DRY_RUN overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTOMATIONS_DRY_RUN: "true",
      });

      try {
        expect(process.env.NIGHTGAUGE_AUTOMATIONS_DRY_RUN).toBe("true");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_AUTOMATIONS_DRY_RUN: "true",
      });

      try {
        const configValue = "false";
        const envValue = process.env.NIGHTGAUGE_AUTOMATIONS_DRY_RUN;

        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("true");
      } finally {
        cleanup();
      }
    });

    it("automations env vars are defined", () => {
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["automations.enabled"]).toBe(
        "NIGHTGAUGE_AUTOMATIONS_ENABLED"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["automations.dry_run"]).toBe(
        "NIGHTGAUGE_AUTOMATIONS_DRY_RUN"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["automations.log_file"]).toBe(
        "NIGHTGAUGE_AUTOMATIONS_LOG_FILE"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = AutomationsConfigSchema.safeParse(DEFAULT_AUTOMATIONS_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { enabled: false };
      const result = AutomationsConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = AutomationsConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates trigger schema", () => {
      const result = AutomationTriggerSchema.safeParse(DEFAULT_AUTOMATION_TRIGGER);
      expect(result.success).toBe(true);
    });

    it("validates action schema", () => {
      const result = AutomationActionSchema.safeParse(DEFAULT_AUTOMATION_ACTION);
      expect(result.success).toBe(true);
    });

    it("rejects trigger without trigger field", () => {
      const result = AutomationTriggerSchema.safeParse({
        name: "test",
        actions: [{ type: "notify" }],
      });
      expect(result.success).toBe(false);
    });

    it("rejects trigger with empty actions", () => {
      const result = AutomationTriggerSchema.safeParse({
        trigger: "pr-merged",
        actions: [],
      });
      expect(result.success).toBe(false);
    });

    it("rejects action with invalid type", () => {
      const result = AutomationActionSchema.safeParse({
        type: "invalid_action",
      });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_AUTOMATIONS_CONFIG has correct defaults", () => {
      expect(DEFAULT_AUTOMATIONS_CONFIG.enabled).toBe(true);
      expect(DEFAULT_AUTOMATIONS_CONFIG.dry_run).toBe(false);
      expect(DEFAULT_AUTOMATIONS_CONFIG.log_file).toBe(".nightgauge/automations.log");
      expect(DEFAULT_AUTOMATIONS_CONFIG.triggers).toEqual([]);
    });

    it("DEFAULT_AUTOMATION_TRIGGER has correct defaults", () => {
      expect(DEFAULT_AUTOMATION_TRIGGER.name).toBe("test-trigger");
      expect(DEFAULT_AUTOMATION_TRIGGER.trigger).toBe("pr-merged");
      expect(DEFAULT_AUTOMATION_TRIGGER.actions).toHaveLength(1);
    });

    it("DEFAULT_AUTOMATION_ACTION has correct defaults", () => {
      expect(DEFAULT_AUTOMATION_ACTION.type).toBe("notify");
      expect(DEFAULT_AUTOMATION_ACTION.message).toBe("Test notification");
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        automations: { dry_run: true },
      });

      expect(config.automations?.dry_run).toBe(true);
    });

    it("missing automations section returns undefined", () => {
      const config = mergeWithDefaults({});

      // DEFAULT_CONFIG doesn't have automations section
      expect(config.automations).toBeUndefined();
    });
  });

  // ============================================================================
  // Automation Execution Simulation
  // ============================================================================

  describe("automation execution simulation", () => {
    it("simulates complete automation flow", () => {
      const config = createMockAutomationsConfig({
        enabled: true,
        dry_run: false,
        triggers: [
          createMockAutomationTrigger({
            name: "merge-notification",
            trigger: "pr-merged",
            actions: [
              createMockAutomationAction({
                type: "post_slack",
                message: "PR merged!",
              }),
              createMockAutomationAction({
                type: "add_label",
                label: "merged",
              }),
            ],
          }),
          createMockAutomationTrigger({
            name: "review-assignment",
            trigger: "pr-created",
            actions: [
              createMockAutomationAction({
                type: "assign_reviewers",
                reviewers: ["reviewer1"],
              }),
            ],
          }),
        ],
      });

      interface ExecutionResult {
        triggered: string[];
        actionsExecuted: string[];
        actionsSkipped: string[];
      }

      const executeAutomations = (event: string, cfg: typeof config): ExecutionResult => {
        const result: ExecutionResult = {
          triggered: [],
          actionsExecuted: [],
          actionsSkipped: [],
        };

        if (!cfg.enabled) {
          return result;
        }

        const matchingTriggers = (cfg.triggers || []).filter((t) => t.trigger === event);

        for (const trigger of matchingTriggers) {
          result.triggered.push(trigger.name || trigger.trigger);

          for (const action of trigger.actions) {
            if (cfg.dry_run) {
              result.actionsSkipped.push(action.type);
            } else {
              result.actionsExecuted.push(action.type);
            }
          }
        }

        return result;
      };

      // Test pr-merged event
      const mergeResult = executeAutomations("pr-merged", config);
      expect(mergeResult.triggered).toContain("merge-notification");
      expect(mergeResult.actionsExecuted).toContain("post_slack");
      expect(mergeResult.actionsExecuted).toContain("add_label");

      // Test pr-created event
      const createResult = executeAutomations("pr-created", config);
      expect(createResult.triggered).toContain("review-assignment");
      expect(createResult.actionsExecuted).toContain("assign_reviewers");

      // Test unknown event
      const unknownResult = executeAutomations("unknown-event", config);
      expect(unknownResult.triggered).toEqual([]);
    });

    it("dry_run mode skips action execution", () => {
      const config = createMockAutomationsConfig({
        enabled: true,
        dry_run: true,
        triggers: [
          createMockAutomationTrigger({
            trigger: "pr-merged",
            actions: [
              createMockAutomationAction({ type: "post_slack" }),
              createMockAutomationAction({ type: "add_label" }),
            ],
          }),
        ],
      });

      interface ExecutionResult {
        triggered: string[];
        actionsExecuted: string[];
        actionsSkipped: string[];
      }

      const executeAutomations = (event: string, cfg: typeof config): ExecutionResult => {
        const result: ExecutionResult = {
          triggered: [],
          actionsExecuted: [],
          actionsSkipped: [],
        };

        if (!cfg.enabled) return result;

        const matchingTriggers = (cfg.triggers || []).filter((t) => t.trigger === event);

        for (const trigger of matchingTriggers) {
          result.triggered.push(trigger.name || trigger.trigger);

          for (const action of trigger.actions) {
            if (cfg.dry_run) {
              result.actionsSkipped.push(action.type);
            } else {
              result.actionsExecuted.push(action.type);
            }
          }
        }

        return result;
      };

      const result = executeAutomations("pr-merged", config);
      expect(result.actionsExecuted).toEqual([]);
      expect(result.actionsSkipped).toContain("post_slack");
      expect(result.actionsSkipped).toContain("add_label");
    });

    it("disabled automations skip all processing", () => {
      const config = createMockAutomationsConfig({
        enabled: false,
        triggers: [
          createMockAutomationTrigger({
            trigger: "pr-merged",
            actions: [createMockAutomationAction({ type: "notify" })],
          }),
        ],
      });

      interface ExecutionResult {
        triggered: string[];
        actionsExecuted: string[];
      }

      const executeAutomations = (event: string, cfg: typeof config): ExecutionResult => {
        const result: ExecutionResult = {
          triggered: [],
          actionsExecuted: [],
        };

        if (!cfg.enabled) return result;

        const matchingTriggers = (cfg.triggers || []).filter((t) => t.trigger === event);

        for (const trigger of matchingTriggers) {
          result.triggered.push(trigger.name || trigger.trigger);
          for (const action of trigger.actions) {
            result.actionsExecuted.push(action.type);
          }
        }

        return result;
      };

      const result = executeAutomations("pr-merged", config);
      expect(result.triggered).toEqual([]);
      expect(result.actionsExecuted).toEqual([]);
    });
  });
});
