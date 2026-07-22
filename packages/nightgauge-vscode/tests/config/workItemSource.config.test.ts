/**
 * Tests for WorkItemSource configuration schema from issue #2571
 *
 * Covers WorkItemSourceModeSchema, WorkItemSourceConfigSchema,
 * IncrediConfigSchema.work_item_source field, and DEFAULT_CONFIG defaults.
 *
 * @see Issue #2571 - Add work item source configuration and provider selection wiring
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import {
  WorkItemSourceModeSchema,
  WorkItemSourceConfigSchema,
  IncrediConfigSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

// ============================================================================
// WorkItemSourceModeSchema
// ============================================================================

describe("WorkItemSourceModeSchema", () => {
  describe("valid values", () => {
    it("accepts 'github'", () => {
      expect(WorkItemSourceModeSchema.safeParse("github").success).toBe(true);
    });

    it("accepts 'repo'", () => {
      expect(WorkItemSourceModeSchema.safeParse("repo").success).toBe(true);
    });

    it("accepts 'composite'", () => {
      expect(WorkItemSourceModeSchema.safeParse("composite").success).toBe(true);
    });
  });

  describe("invalid values", () => {
    it("rejects 'jira'", () => {
      expect(WorkItemSourceModeSchema.safeParse("jira").success).toBe(false);
    });

    it("rejects 'linear'", () => {
      expect(WorkItemSourceModeSchema.safeParse("linear").success).toBe(false);
    });

    it("rejects 'unknown'", () => {
      expect(WorkItemSourceModeSchema.safeParse("unknown").success).toBe(false);
    });

    it("rejects empty string", () => {
      expect(WorkItemSourceModeSchema.safeParse("").success).toBe(false);
    });
  });
});

// ============================================================================
// WorkItemSourceConfigSchema
// ============================================================================

describe("WorkItemSourceConfigSchema", () => {
  it("accepts empty object (all fields optional)", () => {
    expect(WorkItemSourceConfigSchema.safeParse({}).success).toBe(true);
  });

  it("accepts valid mode 'github'", () => {
    expect(WorkItemSourceConfigSchema.safeParse({ mode: "github" }).success).toBe(true);
  });

  it("accepts valid mode 'repo'", () => {
    expect(WorkItemSourceConfigSchema.safeParse({ mode: "repo" }).success).toBe(true);
  });

  it("accepts valid mode 'composite'", () => {
    expect(WorkItemSourceConfigSchema.safeParse({ mode: "composite" }).success).toBe(true);
  });

  it("rejects invalid mode value", () => {
    expect(WorkItemSourceConfigSchema.safeParse({ mode: "jira" }).success).toBe(false);
  });

  it("accepts provider_options as key-value object", () => {
    const result = WorkItemSourceConfigSchema.safeParse({
      mode: "github",
      provider_options: { url: "https://example.com", project_key: "PROJ" },
    });
    expect(result.success).toBe(true);
  });

  it("accepts undefined provider_options (backward compat)", () => {
    const result = WorkItemSourceConfigSchema.safeParse({ mode: "github" });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.provider_options).toBeUndefined();
    }
  });

  it("rejects non-object input", () => {
    expect(WorkItemSourceConfigSchema.safeParse("github").success).toBe(false);
  });

  it("rejects null input", () => {
    expect(WorkItemSourceConfigSchema.safeParse(null).success).toBe(false);
  });
});

// ============================================================================
// IncrediConfigSchema.work_item_source
// ============================================================================

describe("IncrediConfigSchema work_item_source field", () => {
  it("allows config without work_item_source (backward compat)", () => {
    const result = IncrediConfigSchema.safeParse({});
    expect(result.success).toBe(true);
  });

  it("accepts valid work_item_source config", () => {
    const result = IncrediConfigSchema.safeParse({
      work_item_source: { mode: "github" },
    });
    expect(result.success).toBe(true);
  });

  it("rejects invalid mode in work_item_source", () => {
    const result = IncrediConfigSchema.safeParse({
      work_item_source: { mode: "invalid-mode" },
    });
    expect(result.success).toBe(false);
  });
});

// ============================================================================
// DEFAULT_CONFIG.work_item_source
// ============================================================================

describe("DEFAULT_CONFIG.work_item_source", () => {
  it("has work_item_source defined", () => {
    expect(DEFAULT_CONFIG.work_item_source).toBeDefined();
  });

  it("defaults mode to 'github'", () => {
    expect(DEFAULT_CONFIG.work_item_source?.mode).toBe("github");
  });

  it("does not set provider_options by default", () => {
    expect(DEFAULT_CONFIG.work_item_source?.provider_options).toBeUndefined();
  });
});
