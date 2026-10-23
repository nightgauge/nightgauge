/**
 * buildHealthInput.test.ts
 *
 * Unit tests for the buildHealthInput utility function.
 * Tests field mapping correctness for each source type.
 *
 * @see Issue #1570 - Connect real HealthAnalysisResult to AdaptivePolicyEngine
 */

import { describe, it, expect } from "vitest";
import { buildHealthInput } from "../../src/utils/buildHealthInput";
import type { AggregatedDataset } from "../../src/types/aggregation";

/** Minimal AggregatedDataset for testing — only fields used by buildHealthInput */
function makeDataset(overrides: Partial<AggregatedDataset> = {}): AggregatedDataset {
  return {
    filter: {
      startDate: new Date("2026-02-25T00:00:00Z"),
      endDate: new Date("2026-03-03T23:59:59Z"),
    },
    executionHistory: [],
    healthScores: [],
    analysisReports: [],
    experimentResults: [],
    healthReports: [],
    summary: {
      totalRuns: 0,
      successfulRuns: 0,
      failedRuns: 0,
      successRate: 0,
      totalCostUsd: 0,
      avgCostPerRun: 0,
      totalInputTokens: 0,
      totalOutputTokens: 0,
      totalCacheReadTokens: 0,
      totalCacheCreationTokens: 0,
      avgDurationMs: 0,
    },
    quality: {
      totalRecords: 0,
      sourcesFound: 0,
      sourcesMissing: 0,
      sources: [],
      dateRangeRequested: {
        start: "2026-02-25T00:00:00Z",
        end: "2026-03-03T23:59:59Z",
      },
      dateRangeCovered: null,
      gapDays: [],
    },
    ...overrides,
  };
}

describe("buildHealthInput", () => {
  // =========================================================================
  // Empty dataset
  // =========================================================================

  it("returns empty arrays for an empty dataset without throwing", () => {
    const input = buildHealthInput(makeDataset());
    expect(input.executionHistory).toEqual([]);
    expect(input.healthScores).toEqual([]);
    expect(input.experimentResults).toEqual([]);
    expect(input.healthReports).toEqual([]);
  });

  // =========================================================================
  // executionHistory mapping
  // =========================================================================

  describe("executionHistory mapping", () => {
    it("maps each executed stage to a flat SDK record carrying model and selectionSource (#461)", () => {
      const dataset = makeDataset({
        executionHistory: [
          {
            record_type: "run",
            issue_number: 42,
            outcome: "complete",
            started_at: "2026-03-01T11:00:00Z",
            stages: {
              "feature-dev": {
                status: "complete",
                started_at: "2026-03-01T12:00:00Z",
                duration_ms: 120000,
                auto_retry_count: 1,
                model_selection: {
                  model: "claude-sonnet-4-5",
                  source: "scheduler",
                  mode: "automatic",
                  confidence: 0.8,
                  complexity: "M",
                  adapter: "claude",
                },
              },
              "pr-create": {
                status: "complete",
                duration_ms: 3000,
                model_selection: { model: "claude-haiku-4-5", source: "scheduler" },
              },
              "feature-validate": { status: "skipped", skip_reason: "docs-only" },
            },
            tokens: {
              total_input: 10000,
              total_output: 2000,
              estimated_cost_usd: 0.15,
              per_stage: {
                "feature-dev": {
                  input: 10000,
                  output: 2000,
                  cache_read: 500,
                  cache_creation: 200,
                  cost_usd: 0.15,
                },
              },
            },
          } as any,
        ],
      });

      const input = buildHealthInput(dataset);
      // Two executed stages; the skipped one dispatched no model and is absent.
      expect(input.executionHistory.map((r) => r.stage)).toEqual(["feature-dev", "pr-create"]);

      const dev = input.executionHistory[0];
      expect(dev.issueNumber).toBe(42);
      expect(dev.success).toBe(true);
      expect(dev.retries).toBe(1);
      expect(dev.model).toBe("claude-sonnet-4-5");
      expect(dev.selectionSource).toBe("scheduler");
      expect(dev.modelSelectionMode).toBe("automatic");
      expect(dev.autoSelectorConfidence).toBe(0.8);
      expect(dev.autoSelectorComplexity).toBe("M");
      expect(dev.adapter).toBe("claude");
      expect(dev.inputTokens).toBe(10000);
      expect(dev.outputTokens).toBe(2000);
      expect(dev.cacheReadTokens).toBe(500);
      expect(dev.cacheCreationTokens).toBe(200);
      expect(dev.costUsd).toBe(0.15);
      expect(dev.durationMs).toBe(120000);
      expect(dev.timestamp).toBe("2026-03-01T12:00:00Z");
      expect(dev.isLocalModel).toBe(false);

      const pr = input.executionHistory[1];
      expect(pr.model).toBe("claude-haiku-4-5");
      expect(pr.selectionSource).toBe("scheduler");
      // No stage timestamp → the run's start; no per-stage tokens → zeros.
      expect(pr.timestamp).toBe("2026-03-01T11:00:00Z");
      expect(pr.costUsd).toBe(0);
    });

    it("sets success=false for a failed stage and carries its failure_category", () => {
      const dataset = makeDataset({
        executionHistory: [
          {
            record_type: "run",
            issue_number: 99,
            outcome: "failed",
            started_at: "2026-03-01T10:00:00Z",
            stages: {
              "feature-dev": {
                status: "failed",
                failure_category: "agent",
                model_selection: { model: "claude-sonnet-4-5", source: "escalation" },
              },
            },
          } as any,
        ],
      });

      const input = buildHealthInput(dataset);
      expect(input.executionHistory).toHaveLength(1);
      expect(input.executionHistory[0].success).toBe(false);
      expect(input.executionHistory[0].failure_category).toBe("agent");
      expect(input.executionHistory[0].selectionSource).toBe("escalation");
    });

    it("skips non-run records (outcome records)", () => {
      const dataset = makeDataset({
        executionHistory: [
          {
            record_type: "outcome",
            issue_number: 10,
            pr_number: 5,
            outcome: "merged",
            recorded_at: "2026-03-01T15:00:00Z",
          } as any,
        ],
      });

      const input = buildHealthInput(dataset);
      expect(input.executionHistory).toHaveLength(0);
    });

    it("produces no records for a run without a stages map", () => {
      const dataset = makeDataset({
        executionHistory: [
          {
            record_type: "run",
            recorded_at: "2026-03-01T12:00:00Z",
          } as any,
        ],
      });

      const input = buildHealthInput(dataset);
      expect(input.executionHistory).toHaveLength(0);
    });

    it("flags a zero-cost stage that moved tokens as local inference, unless the zero is unstamped", () => {
      const stage = (cost: Record<string, unknown>) => ({
        record_type: "run",
        issue_number: 7,
        started_at: "2026-03-01T10:00:00Z",
        stages: { "feature-dev": { status: "complete" } },
        tokens: { per_stage: { "feature-dev": { input: 100, output: 10, cost_usd: 0, ...cost } } },
      });
      const local = buildHealthInput(makeDataset({ executionHistory: [stage({}) as any] }));
      expect(local.executionHistory[0].isLocalModel).toBe(true);
      expect(local.executionHistory[0].model).toBeUndefined();

      const unstamped = buildHealthInput(
        makeDataset({ executionHistory: [stage({ cost_unstamped: true }) as any] })
      );
      expect(unstamped.executionHistory[0].isLocalModel).toBe(false);
    });
  });

  // =========================================================================
  // healthScores mapping
  // =========================================================================

  describe("healthScores mapping", () => {
    it("maps HealthScoreSnapshot fields correctly", () => {
      const dataset = makeDataset({
        healthScores: [
          {
            schema_version: "1",
            timestamp: "2026-03-01T12:00:00Z",
            score: 72,
            status: "good",
            components: { reliability: 80, cost: 65 },
            cacheHitRate: 0.45,
            costUsd: 0.32,
            issueNumber: 55,
          },
        ],
      });

      const input = buildHealthInput(dataset);
      expect(input.healthScores).toHaveLength(1);
      const entry = input.healthScores[0];
      expect(entry.timestamp).toBe("2026-03-01T12:00:00Z");
      expect(entry.score).toBe(72);
      expect(entry.status).toBe("good");
      expect(entry.components).toEqual({ reliability: 80, cost: 65 });
      expect(entry.costUsd).toBe(0.32);
      expect(entry.issueNumber).toBe(55);
    });
  });

  // =========================================================================
  // experimentResults mapping
  // =========================================================================

  describe("experimentResults mapping", () => {
    it("maps ExperimentOutcome snake_case fields to ExperimentEntry camelCase", () => {
      const dataset = makeDataset({
        experimentResults: [
          {
            experiment_name: "haiku-expansion-v1",
            group: "treatment",
            issue_number: 1400,
            stage: "feature-dev",
            model: "haiku",
            success: true,
            cost_usd: 0.08,
            duration_ms: 90000,
            retry_count: 0,
            recorded_at: "2026-03-02T14:00:00Z",
          },
        ],
      });

      const input = buildHealthInput(dataset);
      expect(input.experimentResults).toHaveLength(1);
      const entry = input.experimentResults[0];
      expect(entry.experimentName).toBe("haiku-expansion-v1");
      expect(entry.group).toBe("treatment");
      expect(entry.issueNumber).toBe(1400);
      expect(entry.stage).toBe("feature-dev");
      expect(entry.success).toBe(true);
      expect(entry.costUsd).toBe(0.08);
      expect(entry.durationMs).toBe(90000);
      expect(entry.recordedAt).toBe("2026-03-02T14:00:00Z");
    });
  });

  // =========================================================================
  // healthReports mapping
  // =========================================================================

  describe("healthReports mapping", () => {
    it("maps HealthReport snake_case summary fields to HealthReportEntry camelCase", () => {
      const dataset = makeDataset({
        healthReports: [
          {
            schema_version: "1",
            analysis_period: {
              from: "2026-02-24T00:00:00Z",
              to: "2026-03-03T23:59:59Z",
              period_days: 7,
              data_sources_found: 4,
              data_sources_missing: 2,
            },
            summary: {
              total_cost_usd: 12.5,
              avg_cost_per_run: 0.42,
              total_runs: 30,
              success_rate: 0.9,
              avg_duration_minutes: 15.2,
              total_tokens: 3000000,
              cache_hit_rate: 0.55,
            },
            findings: [
              {
                id: "f1",
                dimension: "cost-health",
                severity: "high",
                title: "Cost spike",
                description: "Cost above threshold",
              },
            ],
            recommendations: [
              {
                id: "r1",
                priority: "high",
                title: "Reduce model tier",
                description: "Switch haiku_max to 3",
              },
              {
                id: "r2",
                priority: "medium",
                title: "Improve caching",
                description: "Enable cache warming",
              },
            ],
            created_at: "2026-03-03T10:00:00Z",
          },
        ],
      });

      const input = buildHealthInput(dataset);
      expect(input.healthReports).toHaveLength(1);
      const entry = input.healthReports[0];
      expect(entry.createdAt).toBe("2026-03-03T10:00:00Z");
      expect(entry.periodDays).toBe(7);
      expect(entry.summary.totalCostUsd).toBe(12.5);
      expect(entry.summary.avgCostPerRun).toBe(0.42);
      expect(entry.summary.totalRuns).toBe(30);
      expect(entry.summary.successRate).toBe(0.9);
      expect(entry.summary.avgDurationMinutes).toBe(15.2);
      expect(entry.summary.totalTokens).toBe(3000000);
      expect(entry.summary.cacheHitRate).toBe(0.55);
      expect(entry.findingCount).toBe(1);
      expect(entry.recommendationCount).toBe(2);
    });
  });
});
