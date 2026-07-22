import { describe, it, expect, beforeEach } from "vitest";
import { TokenTracker, type SDKResultMessage } from "../../src/tracking/TokenTracker.js";
import { createMockResult } from "../mocks/agent-sdk.js";

describe("TokenTracker", () => {
  let tracker: TokenTracker;

  beforeEach(() => {
    tracker = new TokenTracker();
  });

  describe("record", () => {
    it("should record usage from SDK result", () => {
      const result = createMockResult();
      tracker.record("issue-pickup", result, 1500);

      const usage = tracker.getStageUsage("issue-pickup");
      expect(usage).toBeDefined();
      expect(usage!.inputTokens).toBe(1000);
      expect(usage!.outputTokens).toBe(500);
      expect(usage!.cacheReadTokens).toBe(200);
      expect(usage!.cacheCreationTokens).toBe(100);
      expect(usage!.costUsd).toBe(0.05);
      expect(usage!.durationMs).toBe(1500);
    });

    it("should handle missing usage data gracefully", () => {
      const result: SDKResultMessage = { type: "result" };
      tracker.record("issue-pickup", result, 1000);

      const usage = tracker.getStageUsage("issue-pickup");
      expect(usage).toBeDefined();
      expect(usage!.inputTokens).toBe(0);
      expect(usage!.outputTokens).toBe(0);
      expect(usage!.costUsd).toBe(0);
    });

    it("should overwrite previous usage for same stage", () => {
      tracker.record("issue-pickup", createMockResult(), 1000);
      tracker.record(
        "issue-pickup",
        createMockResult({
          usage: { input_tokens: 2000, output_tokens: 1000 },
          total_cost_usd: 0.1,
        }),
        2000
      );

      const usage = tracker.getStageUsage("issue-pickup");
      expect(usage!.inputTokens).toBe(2000);
      expect(usage!.costUsd).toBe(0.1);
    });
  });

  describe("getTotalUsage", () => {
    it("should return zeros for empty tracker", () => {
      const totals = tracker.getTotalUsage();
      expect(totals.inputTokens).toBe(0);
      expect(totals.outputTokens).toBe(0);
      expect(totals.costUsd).toBe(0);
      expect(totals.stageCount).toBe(0);
    });

    it("should aggregate across multiple stages", () => {
      tracker.record("issue-pickup", createMockResult(), 1000);
      tracker.record(
        "feature-planning",
        createMockResult({
          usage: { input_tokens: 2000, output_tokens: 1500 },
          total_cost_usd: 0.08,
        }),
        2000
      );

      const totals = tracker.getTotalUsage();
      expect(totals.inputTokens).toBe(3000); // 1000 + 2000
      expect(totals.outputTokens).toBe(2000); // 500 + 1500
      expect(totals.costUsd).toBeCloseTo(0.13); // 0.05 + 0.08
      expect(totals.stageCount).toBe(2);
    });
  });

  describe("hasStage", () => {
    it("should return false for unrecorded stages", () => {
      expect(tracker.hasStage("issue-pickup")).toBe(false);
    });

    it("should return true for recorded stages", () => {
      tracker.record("issue-pickup", createMockResult(), 1000);
      expect(tracker.hasStage("issue-pickup")).toBe(true);
    });
  });

  describe("getRecordedStages", () => {
    it("should return empty array initially", () => {
      expect(tracker.getRecordedStages()).toEqual([]);
    });

    it("should return recorded stages in order", () => {
      tracker.record("issue-pickup", createMockResult(), 1000);
      tracker.record("feature-planning", createMockResult(), 1500);

      const stages = tracker.getRecordedStages();
      expect(stages).toContain("issue-pickup");
      expect(stages).toContain("feature-planning");
    });
  });

  describe("clear", () => {
    it("should remove all recorded usage", () => {
      tracker.record("issue-pickup", createMockResult(), 1000);
      tracker.record("feature-planning", createMockResult(), 1500);

      tracker.clear();

      expect(tracker.getRecordedStages()).toEqual([]);
      expect(tracker.getTotalUsage().stageCount).toBe(0);
    });
  });

  describe("toJSON", () => {
    it("should return record of stage usage", () => {
      tracker.record("issue-pickup", createMockResult(), 1000);

      const json = tracker.toJSON();
      expect(json["issue-pickup"]).toBeDefined();
      expect(json["issue-pickup"].inputTokens).toBe(1000);
    });
  });

  describe("recordPTC", () => {
    it("should record PTC usage for a stage", () => {
      tracker.recordPTC("feature-dev", {
        stage: "feature-dev",
        programmaticCalls: 5,
        directCalls: 3,
        estimatedTokensSaved: 2000,
        codeExecutionCount: 2,
        containerReuseCount: 1,
        inputTokens: 10000,
        outputTokens: 3000,
        estimatedCostUsd: 0.05,
      });

      const usage = tracker.getPTCUsage("feature-dev");
      expect(usage).toBeDefined();
      expect(usage!.programmaticCalls).toBe(5);
      expect(usage!.directCalls).toBe(3);
    });

    it("should return undefined for unrecorded PTC stage", () => {
      expect(tracker.getPTCUsage("feature-dev")).toBeUndefined();
    });

    it("should return all PTC usage entries", () => {
      tracker.recordPTC("feature-dev", {
        stage: "feature-dev",
        programmaticCalls: 5,
        directCalls: 3,
        estimatedTokensSaved: 2000,
        codeExecutionCount: 2,
        containerReuseCount: 1,
        inputTokens: 10000,
        outputTokens: 3000,
        estimatedCostUsd: 0.05,
      });
      tracker.recordPTC("feature-validate", {
        stage: "feature-validate",
        programmaticCalls: 2,
        directCalls: 1,
        estimatedTokensSaved: 500,
        codeExecutionCount: 1,
        containerReuseCount: 0,
        inputTokens: 3000,
        outputTokens: 1000,
        estimatedCostUsd: 0.02,
      });

      const all = tracker.getAllPTCUsage();
      expect(all).toHaveLength(2);
    });

    it("should be cleared by clear()", () => {
      tracker.recordPTC("feature-dev", {
        stage: "feature-dev",
        programmaticCalls: 5,
        directCalls: 3,
        estimatedTokensSaved: 2000,
        codeExecutionCount: 2,
        containerReuseCount: 1,
        inputTokens: 10000,
        outputTokens: 3000,
        estimatedCostUsd: 0.05,
      });

      tracker.clear();

      expect(tracker.getAllPTCUsage()).toHaveLength(0);
    });
  });

  describe("formatSummary", () => {
    it("should format readable summary", () => {
      tracker.record("issue-pickup", createMockResult(), 1500);
      tracker.record("feature-planning", createMockResult(), 2000);

      const summary = tracker.formatSummary();

      expect(summary).toContain("Token Usage Summary");
      expect(summary).toContain("Stages completed: 2");
      expect(summary).toContain("Input tokens:");
      expect(summary).toContain("Total cost: $");
      expect(summary).toContain("issue-pickup");
      expect(summary).toContain("feature-planning");
    });
  });
});
