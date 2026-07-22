/**
 * Integration test: mixed-adapter cost flow.
 *
 * Drives a synthetic per-stage `PipelineStateInput` through
 * `ExecutionHistoryWriter.buildRunRecord` and asserts that per-stage
 * `cost_usd` and `cost_source` values flow through unchanged for every
 * resolution path: native (Claude), computed (Gemini, Codex, lm-studio
 * zero), and unknown (synthetic adapter+model with no table entry).
 *
 * @see Issue #3228 — Unified `computeStageCost` across all adapters
 */

import { describe, it, expect } from "vitest";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import type { PipelineStateInput } from "../../src/services/PipelineStateService";

function buildState(): PipelineStateInput {
  // Each stage carries a different cost_source label. The writer must spread
  // every label through unchanged into the emitted history record.
  return {
    issue_number: 3228,
    title: "Mixed-adapter cost flow integration",
    branch: "feat/3228-unified-computestagecost",
    base_branch: "main",
    started_at: new Date(Date.now() - 60_000).toISOString(),
    execution_mode: "automatic",
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": {
        status: "complete",
        adapter: "claude",
        adapter_source: "default",
      },
      "feature-planning": {
        status: "complete",
        adapter: "gemini",
        adapter_source: "stage-config",
      },
      "feature-dev": {
        status: "complete",
        adapter: "codex",
        adapter_source: "stage-config",
      },
      "feature-validate": {
        status: "complete",
        adapter: "lm-studio",
        adapter_source: "stage-config",
      },
      "pr-create": {
        status: "complete",
        // Synthetic adapter for the unknown path: a real adapter type but a
        // model string that has no entry in the pricing table.
        adapter: "gemini",
        adapter_source: "stage-config",
      },
      "pr-merge": { status: "complete" },
      "pipeline-finish": { status: "complete" },
    },
    tokens: {
      // Top-level totals are zero so that buildRunRecord falls through to
      // the per-stage sum (#2249 fallback path) and we observe the per-stage
      // contributions cleanly.
      input: 0,
      output: 0,
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
      per_stage: {
        "issue-pickup": {
          input: 1000,
          output: 500,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.012,
          cost_source: "native",
        },
        "feature-planning": {
          input: 100_000,
          output: 50_000,
          cache_read: 0,
          cache_creation: 0,
          // Synthetic value — buildRunRecord passes per-stage cost_usd
          // through unchanged; this test asserts the flow, not the math.
          cost_usd: 0.000155,
          cost_source: "computed",
        },
        "feature-dev": {
          input: 200_000,
          output: 100_000,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0.00125,
          cost_source: "computed",
        },
        "feature-validate": {
          input: 50_000,
          output: 25_000,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0,
          cost_source: "computed",
        },
        "pr-create": {
          input: 1000,
          output: 500,
          cache_read: 0,
          cache_creation: 0,
          cost_usd: 0,
          cost_source: "unknown",
        },
      },
    },
  } as PipelineStateInput;
}

describe("mixed-adapter cost flow", () => {
  it("preserves per-stage cost_usd and cost_source across all resolution paths", () => {
    const record = ExecutionHistoryWriter.buildRunRecord(buildState());
    const perStage = record.tokens.per_stage;
    if (!perStage) throw new Error("expected per_stage in record");

    expect(perStage["issue-pickup"]?.cost_usd).toBe(0.012);
    expect(perStage["issue-pickup"]?.cost_source).toBe("native");

    expect(perStage["feature-planning"]?.cost_usd).toBeCloseTo(0.000155, 6);
    expect(perStage["feature-planning"]?.cost_source).toBe("computed");

    expect(perStage["feature-dev"]?.cost_usd).toBeCloseTo(0.00125, 6);
    expect(perStage["feature-dev"]?.cost_source).toBe("computed");

    expect(perStage["feature-validate"]?.cost_usd).toBe(0);
    expect(perStage["feature-validate"]?.cost_source).toBe("computed");

    expect(perStage["pr-create"]?.cost_usd).toBe(0);
    expect(perStage["pr-create"]?.cost_source).toBe("unknown");
  });

  it("sums per-stage cost_usd into top-level estimated_cost_usd", () => {
    const record = ExecutionHistoryWriter.buildRunRecord(buildState());
    const perStage = record.tokens.per_stage ?? {};
    const sumPerStage = Object.values(perStage).reduce((acc, s) => acc + (s?.cost_usd ?? 0), 0);
    expect(record.tokens.estimated_cost_usd).toBeCloseTo(sumPerStage, 6);
  });

  it("omits cost_source on stages that did not record one (backward compat)", () => {
    const state = buildState();
    // Drop cost_source from one stage to simulate a pre-#3228 record.
    state.tokens!.per_stage!["feature-dev"] = {
      input: 1000,
      output: 500,
      cache_read: 0,
      cache_creation: 0,
      cost_usd: 0.001,
      // no cost_source
    } as never;

    const record = ExecutionHistoryWriter.buildRunRecord(state);
    expect(record.tokens.per_stage?.["feature-dev"]?.cost_usd).toBe(0.001);
    expect(record.tokens.per_stage?.["feature-dev"]?.cost_source).toBeUndefined();
  });
});
