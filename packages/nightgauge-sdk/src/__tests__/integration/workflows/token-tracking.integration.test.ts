/**
 * Integration tests — Token Usage Tracking & Cost Analysis
 *
 * Workflow 3: TokenTracker.record() → getStageUsage() → getTotalUsage()
 *
 * Tests cumulative tracking logic, cost calculations, model detection,
 * and edge cases (zero tokens, large counts). No external dependencies.
 */

import { describe, it, expect, beforeEach } from "vitest";
import { TokenTracker } from "../../../tracking/TokenTracker.js";
import {
  buildMockResultMessage,
  buildZeroUsageResult,
  buildLargeUsageResult,
} from "../helpers/mocks.js";

describe("Token Tracking Workflow", () => {
  let tracker: TokenTracker;

  beforeEach(() => {
    tracker = new TokenTracker();
  });

  describe("single stage recording", () => {
    it("records stage usage and retrieves it correctly", () => {
      const result = buildMockResultMessage();
      tracker.record("issue-pickup", result, 1500);

      const usage = tracker.getStageUsage("issue-pickup");
      expect(usage).toBeDefined();
      expect(usage?.stage).toBe("issue-pickup");
      expect(usage?.inputTokens).toBe(1000);
      expect(usage?.outputTokens).toBe(500);
      expect(usage?.durationMs).toBe(1500);
    });

    it("records cost from total_cost_usd field", () => {
      const result = buildMockResultMessage({ total_cost_usd: 0.042 });
      tracker.record("feature-dev", result, 2000);

      const usage = tracker.getStageUsage("feature-dev");
      expect(usage?.costUsd).toBe(0.042);
    });

    it("detects model from modelUsage key", () => {
      const result = buildMockResultMessage({
        modelUsage: { "claude-sonnet-4-5": { inputTokens: 1000, outputTokens: 500 } },
        model: undefined,
      });
      tracker.record("feature-planning", result, 3000);

      const usage = tracker.getStageUsage("feature-planning");
      expect(usage?.model).toBe("claude-sonnet-4-5");
    });

    it("marks stage as recorded after record()", () => {
      expect(tracker.hasStage("pr-create")).toBe(false);
      tracker.record("pr-create", buildMockResultMessage(), 500);
      expect(tracker.hasStage("pr-create")).toBe(true);
    });
  });

  describe("cumulative usage across multiple stages", () => {
    it("sums token counts from all recorded stages", () => {
      tracker.record(
        "issue-pickup",
        buildMockResultMessage({
          usage: {
            input_tokens: 500,
            output_tokens: 200,
            cache_read_input_tokens: 0,
            cache_creation_input_tokens: 0,
          },
          total_cost_usd: 0.005,
        }),
        1000
      );
      tracker.record(
        "feature-planning",
        buildMockResultMessage({
          usage: {
            input_tokens: 1000,
            output_tokens: 400,
            cache_read_input_tokens: 100,
            cache_creation_input_tokens: 0,
          },
          total_cost_usd: 0.015,
        }),
        2000
      );
      tracker.record(
        "feature-dev",
        buildMockResultMessage({
          usage: {
            input_tokens: 2000,
            output_tokens: 800,
            cache_read_input_tokens: 200,
            cache_creation_input_tokens: 50,
          },
          total_cost_usd: 0.03,
        }),
        4000
      );

      const total = tracker.getTotalUsage();
      expect(total.inputTokens).toBe(3500);
      expect(total.outputTokens).toBe(1400);
      expect(total.cacheReadTokens).toBe(300);
      expect(total.cacheCreationTokens).toBe(50);
      expect(total.stageCount).toBe(3);
      expect(total.durationMs).toBe(7000);
    });

    it("accumulates cost across all 6 pipeline stages", () => {
      const stages = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ] as const;

      let expectedCost = 0;
      stages.forEach((stage, i) => {
        const cost = (i + 1) * 0.01;
        expectedCost += cost;
        tracker.record(stage, buildMockResultMessage({ total_cost_usd: cost }), 1000);
      });

      const total = tracker.getTotalUsage();
      expect(total.stageCount).toBe(6);
      expect(total.costUsd).toBeCloseTo(expectedCost, 5);
    });
  });

  describe("edge cases", () => {
    it("handles zero token usage (skipped stage)", () => {
      tracker.record("feature-validate", buildZeroUsageResult(), 100);

      const usage = tracker.getStageUsage("feature-validate");
      expect(usage?.inputTokens).toBe(0);
      expect(usage?.outputTokens).toBe(0);
      expect(usage?.costUsd).toBe(0);
      expect(usage?.isLocalModel).toBe(false);
    });

    it("handles very large token counts without overflow", () => {
      tracker.record("feature-dev", buildLargeUsageResult(), 60_000);

      const usage = tracker.getStageUsage("feature-dev");
      expect(usage?.inputTokens).toBe(100_000);
      expect(usage?.outputTokens).toBe(50_000);
      expect(usage?.costUsd).toBe(5.0);
    });

    it("getTotalUsage() returns zero totals when no stages recorded", () => {
      const total = tracker.getTotalUsage();
      expect(total.inputTokens).toBe(0);
      expect(total.outputTokens).toBe(0);
      expect(total.costUsd).toBe(0);
      expect(total.stageCount).toBe(0);
    });

    it("clear() resets all recorded usage", () => {
      tracker.record("issue-pickup", buildMockResultMessage(), 1000);
      tracker.clear();

      const total = tracker.getTotalUsage();
      expect(total.stageCount).toBe(0);
      expect(tracker.hasStage("issue-pickup")).toBe(false);
    });
  });

  describe("recorded stages list", () => {
    it("returns list of all recorded stages", () => {
      tracker.record("issue-pickup", buildMockResultMessage(), 500);
      tracker.record("feature-dev", buildMockResultMessage(), 1000);

      const recorded = tracker.getRecordedStages();
      expect(recorded).toContain("issue-pickup");
      expect(recorded).toContain("feature-dev");
      expect(recorded).toHaveLength(2);
    });
  });
});
