import { describe, it, expect } from "vitest";
import { TokenTracker } from "../../tracking/TokenTracker.js";
import type { SDKResultMessage } from "../../tracking/TokenTracker.js";

describe("TokenTracker", () => {
  describe("record() — model resolution", () => {
    it("uses top-level model field when present (LM Studio pattern)", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 100, output_tokens: 50 },
        total_cost_usd: 0,
        model: "mistral",
      };
      tracker.record("feature-dev", result, 1000);
      const usage = tracker.getStageUsage("feature-dev");
      expect(usage?.model).toBe("mistral");
    });

    it("falls back to modelUsage key when top-level model is absent (Anthropic pattern)", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 5000, output_tokens: 2000 },
        total_cost_usd: 0.05,
        modelUsage: {
          "claude-sonnet-4-5": { inputTokens: 5000, outputTokens: 2000 },
        },
      };
      tracker.record("feature-dev", result, 2000);
      const usage = tracker.getStageUsage("feature-dev");
      expect(usage?.model).toBe("claude-sonnet-4-5");
    });

    it('falls back to "unknown" when neither model field nor modelUsage is present', () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 0, output_tokens: 0 },
        total_cost_usd: 0,
      };
      tracker.record("feature-planning", result, 500);
      const usage = tracker.getStageUsage("feature-planning");
      expect(usage?.model).toBe("unknown");
    });

    it("prefers top-level model over modelUsage key when both are present", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 100, output_tokens: 50 },
        total_cost_usd: 0,
        model: "llama-3.2",
        modelUsage: { "claude-haiku": { inputTokens: 100, outputTokens: 50 } },
      };
      tracker.record("pr-create", result, 800);
      const usage = tracker.getStageUsage("pr-create");
      expect(usage?.model).toBe("llama-3.2");
    });
  });

  describe("record() — isLocalModel detection", () => {
    it("sets isLocalModel=true for LM Studio result (cost=0, tokens>0)", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 1200, output_tokens: 600 },
        total_cost_usd: 0,
        model: "mistral",
      };
      tracker.record("feature-dev", result, 1500);
      const usage = tracker.getStageUsage("feature-dev");
      expect(usage?.isLocalModel).toBe(true);
    });

    it("sets isLocalModel=false for Anthropic result (cost>0)", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 5000, output_tokens: 2000 },
        total_cost_usd: 0.045,
        modelUsage: {
          "claude-sonnet-4-5": { inputTokens: 5000, outputTokens: 2000 },
        },
      };
      tracker.record("feature-dev", result, 3000);
      const usage = tracker.getStageUsage("feature-dev");
      expect(usage?.isLocalModel).toBe(false);
    });

    it("sets isLocalModel=false when cost=0 but tokens=0 (skipped/errored stage)", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 0, output_tokens: 0 },
        total_cost_usd: 0,
      };
      tracker.record("pr-merge", result, 100);
      const usage = tracker.getStageUsage("pr-merge");
      expect(usage?.isLocalModel).toBe(false);
    });

    it("sets isLocalModel=false when total_cost_usd is absent and tokens=0", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: {},
      };
      tracker.record("issue-pickup", result, 200);
      const usage = tracker.getStageUsage("issue-pickup");
      expect(usage?.isLocalModel).toBe(false);
    });
  });

  describe("getWorkflowUsage() — canonical SubAgentNode.usage projection (#3914)", () => {
    it("projects recorded tokens/costUsd onto the workflow usage shape (estimated=false for real cost)", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: {
          input_tokens: 1200,
          output_tokens: 400,
          cache_read_input_tokens: 100,
          cache_creation_input_tokens: 25,
        },
        total_cost_usd: 0.015,
        modelUsage: { "claude-sonnet-4-5": { inputTokens: 1200, outputTokens: 400 } },
      };
      tracker.record("feature-dev", result, 300);

      expect(tracker.getWorkflowUsage("feature-dev")).toEqual({
        inputTokens: 1200,
        outputTokens: 400,
        cacheReadTokens: 100,
        cacheCreationTokens: 25,
        costUsd: 0.015,
        estimated: false,
      });
    });

    it("marks a local-inference stage (tokens>0, cost=0) as estimated", () => {
      const tracker = new TokenTracker();
      const result: SDKResultMessage = {
        type: "result",
        usage: { input_tokens: 800, output_tokens: 200 },
        total_cost_usd: 0,
        model: "lmstudio-local",
      };
      tracker.record("feature-dev", result, 300);

      const usage = tracker.getWorkflowUsage("feature-dev");
      expect(usage.inputTokens).toBe(800);
      expect(usage.costUsd).toBe(0);
      expect(usage.estimated).toBe(true);
    });

    it("returns a zeroed (non-estimated) record for an unrecorded stage — REQUIRED, never blank", () => {
      const tracker = new TokenTracker();
      expect(tracker.getWorkflowUsage("pr-merge")).toEqual({
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
        estimated: false,
      });
    });
  });
});
