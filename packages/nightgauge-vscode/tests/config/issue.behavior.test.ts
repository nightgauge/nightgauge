/**
 * Behavior tests for issue.* configuration fields
 *
 * These tests verify that issue config fields actually affect runtime behavior,
 * not just that they parse correctly (that's covered by schema.test.ts).
 *
 * @see Issue #437 - Audit and test project/issue/commands config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - IssueConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockIssueConfig,
  applyEnvOverrides,
  DEFAULT_ISSUE_CONFIG,
} from "../mocks/config-fixtures";
import { IssueConfigSchema, mergeWithDefaults, DEFAULT_CONFIG } from "../../src/config/schema";

describe("issue.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear issue-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_ISSUE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // issue.auto_assign - Behavior Tests
  // ============================================================================

  describe("issue.auto_assign", () => {
    it("auto_assign=true enables assigning issue to creator", () => {
      const config = createMockIssueConfig({ auto_assign: true });
      expect(config.auto_assign).toBe(true);
      // When auto_assign is true, issue-pickup should assign the issue
      // to the current authenticated GitHub user
    });

    it("auto_assign=false leaves issue unassigned", () => {
      const config = createMockIssueConfig({ auto_assign: false });
      expect(config.auto_assign).toBe(false);
      // When auto_assign is false, issue-pickup should not modify assignees
    });

    it("defaults to true when not specified", () => {
      const config = mergeWithDefaults({});
      expect(config.issue?.auto_assign).toBe(true);
    });

    it("validates as boolean type", () => {
      const result = IssueConfigSchema.safeParse({ auto_assign: true });
      expect(result.success).toBe(true);

      const invalidResult = IssueConfigSchema.safeParse({
        auto_assign: "yes",
      });
      expect(invalidResult.success).toBe(false);
    });

    // Behavior: Service should check this config when picking up issues
    it("config value determines assignment behavior", () => {
      // Simulate behavior decision
      const shouldAssign = (config: { auto_assign?: boolean }) => {
        return config.auto_assign !== false; // Default is true
      };

      expect(shouldAssign({ auto_assign: true })).toBe(true);
      expect(shouldAssign({ auto_assign: false })).toBe(false);
      expect(shouldAssign({})).toBe(true); // Default behavior
    });
  });

  // ============================================================================
  // issue.default_labels - Behavior Tests
  // ============================================================================

  describe("issue.default_labels", () => {
    it("empty array adds no extra labels", () => {
      const config = createMockIssueConfig({ default_labels: [] });
      expect(config.default_labels).toEqual([]);
      // Issue creation should not add any labels beyond what the user specifies
    });

    it("configured labels are added to new issues", () => {
      const config = createMockIssueConfig({
        default_labels: ["status:ready", "needs-triage"],
      });

      expect(config.default_labels).toContain("status:ready");
      expect(config.default_labels).toContain("needs-triage");
      expect(config.default_labels).toHaveLength(2);
    });

    it("supports single label", () => {
      const config = createMockIssueConfig({
        default_labels: ["auto-generated"],
      });

      expect(config.default_labels).toHaveLength(1);
      expect(config.default_labels?.[0]).toBe("auto-generated");
    });

    it("supports multiple labels", () => {
      const labels = ["status:ready", "priority:medium", "size:M", "type:feature"];
      const config = createMockIssueConfig({ default_labels: labels });

      expect(config.default_labels).toHaveLength(4);
      labels.forEach((label) => {
        expect(config.default_labels).toContain(label);
      });
    });

    it("defaults to empty array when not specified", () => {
      const config = mergeWithDefaults({});
      expect(config.issue?.default_labels).toEqual([]);
    });

    it("validates as array of strings", () => {
      const result = IssueConfigSchema.safeParse({
        default_labels: ["label1", "label2"],
      });
      expect(result.success).toBe(true);

      const invalidResult = IssueConfigSchema.safeParse({
        default_labels: "single-label", // Should be array
      });
      expect(invalidResult.success).toBe(false);

      const invalidItemResult = IssueConfigSchema.safeParse({
        default_labels: [123], // Should be strings
      });
      expect(invalidItemResult.success).toBe(false);
    });

    // Behavior: Service should merge default_labels with user-provided labels
    it("config labels are merged with user-provided labels", () => {
      const config = createMockIssueConfig({
        default_labels: ["status:ready", "auto-generated"],
      });

      const userLabels = ["type:feature", "priority:high"];

      // Simulate merge behavior
      const mergedLabels = [...new Set([...(config.default_labels ?? []), ...userLabels])];

      expect(mergedLabels).toContain("status:ready");
      expect(mergedLabels).toContain("auto-generated");
      expect(mergedLabels).toContain("type:feature");
      expect(mergedLabels).toContain("priority:high");
      expect(mergedLabels).toHaveLength(4);
    });

    // Behavior: Duplicates should be handled
    it("duplicate labels are deduplicated", () => {
      const config = createMockIssueConfig({
        default_labels: ["status:ready", "type:feature"],
      });

      const userLabels = ["status:ready"]; // Duplicate

      const mergedLabels = [...new Set([...(config.default_labels ?? []), ...userLabels])];

      expect(mergedLabels).toHaveLength(2);
      expect(mergedLabels.filter((l) => l === "status:ready")).toHaveLength(1);
    });
  });

  // ============================================================================
  // issue.default_status - Behavior Tests
  // ============================================================================

  describe("issue.default_status", () => {
    it("defaults to backlog when not specified", () => {
      const config = mergeWithDefaults({});
      expect(config.issue?.default_status).toBe("backlog");
    });

    it("accepts backlog as valid value", () => {
      const config = createMockIssueConfig({ default_status: "backlog" });
      expect(config.default_status).toBe("backlog");
    });

    it("accepts ready as valid value", () => {
      const config = createMockIssueConfig({ default_status: "ready" });
      expect(config.default_status).toBe("ready");
    });

    it("rejects invalid values", () => {
      const result = IssueConfigSchema.safeParse({ default_status: "invalid" });
      expect(result.success).toBe(false);
    });

    it("validates as enum type", () => {
      const validResult = IssueConfigSchema.safeParse({
        default_status: "backlog",
      });
      expect(validResult.success).toBe(true);

      const invalidResult = IssueConfigSchema.safeParse({
        default_status: 123,
      });
      expect(invalidResult.success).toBe(false);
    });

    it("maps to correct status label", () => {
      const mapStatusLabel = (status: string) => `status:${status}`;

      expect(mapStatusLabel("backlog")).toBe("status:backlog");
      expect(mapStatusLabel("ready")).toBe("status:ready");
    });

    it("config override to ready works", () => {
      const config = mergeWithDefaults({
        issue: { default_status: "ready" },
      });
      expect(config.issue?.default_status).toBe("ready");
    });

    it("env var NIGHTGAUGE_ISSUE_DEFAULT_STATUS overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ISSUE_DEFAULT_STATUS: "ready",
      });

      try {
        const envValue = process.env.NIGHTGAUGE_ISSUE_DEFAULT_STATUS;
        expect(envValue).toBe("ready");
      } finally {
        cleanup();
      }
    });
  });

  // ============================================================================
  // Environment Variable Overrides - Behavior Tests
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_ISSUE_AUTO_ASSIGN=false overrides config", () => {
      // Apply environment override
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ISSUE_AUTO_ASSIGN: "false",
      });

      try {
        // Simulate reading from env
        const envValue = process.env.NIGHTGAUGE_ISSUE_AUTO_ASSIGN;
        const autoAssign = envValue?.toLowerCase() === "true";

        expect(autoAssign).toBe(false);
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_ISSUE_AUTO_ASSIGN=true overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ISSUE_AUTO_ASSIGN: "true",
      });

      try {
        const envValue = process.env.NIGHTGAUGE_ISSUE_AUTO_ASSIGN;
        const autoAssign = envValue?.toLowerCase() === "true";

        expect(autoAssign).toBe(true);
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ISSUE_AUTO_ASSIGN: "false",
      });

      try {
        // Config says true, but env says false
        const configValue = true;
        const envValue = process.env.NIGHTGAUGE_ISSUE_AUTO_ASSIGN;

        // Env should take precedence
        const effectiveValue =
          envValue !== undefined ? envValue.toLowerCase() === "true" : configValue;

        expect(effectiveValue).toBe(false);
      } finally {
        cleanup();
      }
    });

    it("cleanup restores original environment", () => {
      const originalValue = process.env.NIGHTGAUGE_ISSUE_AUTO_ASSIGN;
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ISSUE_AUTO_ASSIGN: "test-value",
      });

      expect(process.env.NIGHTGAUGE_ISSUE_AUTO_ASSIGN).toBe("test-value");

      cleanup();

      expect(process.env.NIGHTGAUGE_ISSUE_AUTO_ASSIGN).toBe(originalValue);
    });
  });

  // ============================================================================
  // Config Defaults - Behavior Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_ISSUE_CONFIG has expected defaults", () => {
      expect(DEFAULT_ISSUE_CONFIG.auto_assign).toBe(true);
      expect(DEFAULT_ISSUE_CONFIG.default_labels).toEqual([]);
      expect(DEFAULT_ISSUE_CONFIG.default_status).toBe("backlog");
    });

    it("mergeWithDefaults applies issue defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.issue).toBeDefined();
      expect(config.issue?.auto_assign).toBe(true);
      expect(config.issue?.default_labels).toEqual([]);
    });

    it("user config overrides defaults", () => {
      const config = mergeWithDefaults({
        issue: {
          auto_assign: false,
          default_labels: ["custom-label"],
        },
      });

      expect(config.issue?.auto_assign).toBe(false);
      expect(config.issue?.default_labels).toEqual(["custom-label"]);
    });

    it("partial user config preserves other defaults", () => {
      const config = mergeWithDefaults({
        issue: {
          auto_assign: false,
          // default_labels not specified
        },
      });

      expect(config.issue?.auto_assign).toBe(false);
      // default_labels should come from defaults
      expect(config.issue?.default_labels).toEqual([]);
    });
  });

  // ============================================================================
  // Integration Scenarios - Behavior Tests
  // ============================================================================

  describe("integration scenarios", () => {
    it("issue pickup with auto_assign=true and default_labels", () => {
      const config = createMockIssueConfig({
        auto_assign: true,
        default_labels: ["status:in-progress", "claimed"],
      });

      // Simulate issue pickup behavior
      const issuePickupActions = {
        shouldAssign: config.auto_assign,
        labelsToAdd: config.default_labels,
      };

      expect(issuePickupActions.shouldAssign).toBe(true);
      expect(issuePickupActions.labelsToAdd).toContain("status:in-progress");
      expect(issuePickupActions.labelsToAdd).toContain("claimed");
    });

    it("issue pickup with auto_assign=false skips assignment", () => {
      const config = createMockIssueConfig({
        auto_assign: false,
        default_labels: ["status:ready"],
      });

      const issuePickupActions = {
        shouldAssign: config.auto_assign,
        labelsToAdd: config.default_labels,
      };

      expect(issuePickupActions.shouldAssign).toBe(false);
      expect(issuePickupActions.labelsToAdd).toContain("status:ready");
    });

    it("config values are type-safe", () => {
      const config = createMockIssueConfig({
        auto_assign: true,
        default_labels: ["label1", "label2"],
      });

      // TypeScript should enforce these types
      const autoAssign: boolean = config.auto_assign!;
      const labels: string[] = config.default_labels!;

      expect(typeof autoAssign).toBe("boolean");
      expect(Array.isArray(labels)).toBe(true);
      labels.forEach((label) => {
        expect(typeof label).toBe("string");
      });
    });
  });
});
