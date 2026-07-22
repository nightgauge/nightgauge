/**
 * executionHistoryWriter.model-selection.test.ts
 *
 * Unit tests validating that model selection metadata flows through
 * buildRunRecord() correctly.
 *
 * @see Issue #734 - Learning Feedback Loop Model Routing Report
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";

// Mock node:fs/promises
vi.mock("node:fs/promises");

describe("ExecutionHistoryWriter — model_selection in buildRunRecord()", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should include model_selection when present in stage data", () => {
    const state = createMockPipelineState({
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": {
          status: "complete",
          model_selection: {
            model: "sonnet",
            source: "auto",
            confidence: 0.85,
            complexity: "M",
            mode: "automatic",
          },
        },
        "feature-dev": { status: "complete" },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
        "pipeline-finish": { status: "complete" },
      },
    });

    const record = ExecutionHistoryWriter.buildRunRecord(state);

    expect(record.stages["feature-planning"]?.model_selection).toBeDefined();
    expect(record.stages["feature-planning"]?.model_selection?.model).toBe("sonnet");
    expect(record.stages["feature-planning"]?.model_selection?.source).toBe("auto");
    expect(record.stages["feature-planning"]?.model_selection?.confidence).toBe(0.85);
    expect(record.stages["feature-planning"]?.model_selection?.complexity).toBe("M");
    expect(record.stages["feature-planning"]?.model_selection?.mode).toBe("automatic");
  });

  it("should omit model_selection when not present (backward compat)", () => {
    const state = createMockPipelineState();
    const record = ExecutionHistoryWriter.buildRunRecord(state);

    // No stage in the default mock includes model_selection
    for (const [, stageData] of Object.entries(record.stages)) {
      expect(stageData?.model_selection).toBeUndefined();
    }
  });

  it("should handle model_selection with all fields populated", () => {
    const fullModelSelection = {
      model: "opus",
      source: "config" as const,
      confidence: 0.95,
      complexity: "XL",
      mode: "hybrid" as const,
    };

    const state = createMockPipelineState({
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": {
          status: "complete",
          model_selection: fullModelSelection,
        },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
        "pipeline-finish": { status: "complete" },
      },
    });

    const record = ExecutionHistoryWriter.buildRunRecord(state);
    const ms = record.stages["feature-dev"]?.model_selection;

    expect(ms).toBeDefined();
    expect(ms?.model).toBe("opus");
    expect(ms?.source).toBe("config");
    expect(ms?.confidence).toBe(0.95);
    expect(ms?.complexity).toBe("XL");
    expect(ms?.mode).toBe("hybrid");
  });

  it("should handle model_selection with only required fields (model, source)", () => {
    const minimalModelSelection = {
      model: "haiku",
      source: "default" as const,
    };

    const state = createMockPipelineState({
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "complete" },
        "feature-validate": {
          status: "complete",
          model_selection: minimalModelSelection,
        },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
        "pipeline-finish": { status: "complete" },
      },
    });

    const record = ExecutionHistoryWriter.buildRunRecord(state);
    const ms = record.stages["feature-validate"]?.model_selection;

    expect(ms).toBeDefined();
    expect(ms?.model).toBe("haiku");
    expect(ms?.source).toBe("default");
    expect(ms?.confidence).toBeUndefined();
    expect(ms?.complexity).toBeUndefined();
    expect(ms?.mode).toBeUndefined();
  });

  it("should support multiple stages with different model_selection data", () => {
    const state = createMockPipelineState({
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": {
          status: "complete",
          model_selection: {
            model: "haiku",
            source: "default" as const,
          },
        },
        "feature-planning": {
          status: "complete",
          model_selection: {
            model: "sonnet",
            source: "auto" as const,
            confidence: 0.8,
            complexity: "M",
            mode: "automatic" as const,
          },
        },
        "feature-dev": {
          status: "complete",
          model_selection: {
            model: "opus",
            source: "env" as const,
            confidence: 0.92,
            complexity: "XL",
            mode: "manual" as const,
          },
        },
        "feature-validate": {
          status: "complete",
          model_selection: {
            model: "sonnet",
            source: "config" as const,
            confidence: 0.75,
            complexity: "S",
          },
        },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
        "pipeline-finish": { status: "complete" },
      },
    });

    const record = ExecutionHistoryWriter.buildRunRecord(state);

    // issue-pickup: haiku / default
    const issueMs = record.stages["issue-pickup"]?.model_selection;
    expect(issueMs?.model).toBe("haiku");
    expect(issueMs?.source).toBe("default");
    expect(issueMs?.confidence).toBeUndefined();

    // feature-planning: sonnet / auto
    const planMs = record.stages["feature-planning"]?.model_selection;
    expect(planMs?.model).toBe("sonnet");
    expect(planMs?.source).toBe("auto");
    expect(planMs?.confidence).toBe(0.8);
    expect(planMs?.complexity).toBe("M");
    expect(planMs?.mode).toBe("automatic");

    // feature-dev: opus / env
    const devMs = record.stages["feature-dev"]?.model_selection;
    expect(devMs?.model).toBe("opus");
    expect(devMs?.source).toBe("env");
    expect(devMs?.confidence).toBe(0.92);
    expect(devMs?.complexity).toBe("XL");
    expect(devMs?.mode).toBe("manual");

    // feature-validate: sonnet / config
    const valMs = record.stages["feature-validate"]?.model_selection;
    expect(valMs?.model).toBe("sonnet");
    expect(valMs?.source).toBe("config");
    expect(valMs?.confidence).toBe(0.75);
    expect(valMs?.complexity).toBe("S");
    expect(valMs?.mode).toBeUndefined();

    // pr-create: no model_selection
    expect(record.stages["pr-create"]?.model_selection).toBeUndefined();

    // pr-merge: no model_selection
    expect(record.stages["pr-merge"]?.model_selection).toBeUndefined();
  });

  it("should propagate model info into per_stage token records (Issue #1006)", () => {
    const state = createMockPipelineState({
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": {
          status: "complete",
          model_selection: {
            model: "sonnet",
            source: "auto",
            confidence: 0.85,
            complexity: "M",
            mode: "automatic",
          },
        },
        "feature-dev": {
          status: "complete",
          model_selection: {
            model: "opus",
            source: "env",
          },
        },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
        "pipeline-finish": { status: "complete" },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.1,
        per_stage: {
          "feature-planning": {
            input: 3000,
            output: 1500,
            cache_read: 500,
            cache_creation: 200,
            cost_usd: 0.03,
          },
          "feature-dev": {
            input: 5000,
            output: 2500,
            cache_read: 1000,
            cache_creation: 500,
            cost_usd: 0.05,
          },
          "feature-validate": {
            input: 2000,
            output: 1000,
            cache_read: 500,
            cache_creation: 300,
            cost_usd: 0.02,
          },
        },
      },
    });

    const record = ExecutionHistoryWriter.buildRunRecord(state);

    // feature-planning: has model_selection → model info in tokens
    const planTokens = record.tokens.per_stage?.["feature-planning"] as Record<string, unknown>;
    expect(planTokens?.model).toBe("sonnet");
    expect(planTokens?.model_source).toBe("auto");

    // feature-dev: has model_selection → model info in tokens
    const devTokens = record.tokens.per_stage?.["feature-dev"] as Record<string, unknown>;
    expect(devTokens?.model).toBe("opus");
    expect(devTokens?.model_source).toBe("env");

    // feature-validate: no model_selection → no model info
    const valTokens = record.tokens.per_stage?.["feature-validate"] as Record<string, unknown>;
    expect(valTokens?.model).toBeUndefined();
    expect(valTokens?.model_source).toBeUndefined();
  });

  it("should not add model fields to per_stage when per_stage is absent (Issue #1006)", () => {
    const state = createMockPipelineState({
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": {
          status: "complete",
          model_selection: { model: "sonnet", source: "auto" },
        },
        "feature-dev": { status: "complete" },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
        "pipeline-finish": { status: "complete" },
      },
    });

    const record = ExecutionHistoryWriter.buildRunRecord(state);
    expect(record.tokens.per_stage).toBeUndefined();
  });
});

// ============================================================================
// Test Helpers
// ============================================================================

function createMockPipelineState(overrides?: Record<string, unknown>) {
  return {
    schema_version: "1.0",
    issue_number: 42,
    title: "Test issue",
    branch: "feat/42-test",
    base_branch: "main",
    started_at: new Date(Date.now() - 60000).toISOString(),
    updated_at: new Date().toISOString(),
    execution_mode: "automatic",
    paused: false,
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "complete" },
      "feature-validate": { status: "complete" },
      "pr-create": { status: "complete" },
      "pr-merge": { status: "complete" },
      "pipeline-finish": { status: "complete" },
    },
    tokens: {
      total_input: 10000,
      total_output: 5000,
      total_cache_read: 2000,
      total_cache_creation: 1000,
      estimated_cost_usd: 0.1,
    },
    ...overrides,
  };
}
