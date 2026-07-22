/**
 * phaseTracker.test.ts - Tests for phase tracking and auto-skip behavior
 *
 * Verifies that:
 * - Phase tracking works correctly for normal phase progression
 * - Untracked phases are auto-marked as skipped when a stage completes (Issue #1232)
 * - All stages show correct phase counts after completion
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { createPhaseTracker } from "../../src/utils/phaseTracker";
import type { PipelineStage } from "@nightgauge/sdk";
import type { ParsedPhaseMarker } from "@nightgauge/sdk";

/**
 * Create a mock PipelineStateService with spied phase methods.
 */
function createMockStateService() {
  return {
    startPhase: vi.fn().mockResolvedValue(undefined),
    completePhase: vi.fn().mockResolvedValue(undefined),
    skipPhase: vi.fn().mockResolvedValue(undefined),
  };
}

describe("createPhaseTracker", () => {
  let mockStateService: ReturnType<typeof createMockStateService>;

  beforeEach(() => {
    mockStateService = createMockStateService();
  });

  it("should complete previous phase when new phase starts", async () => {
    const tracker = createPhaseTracker(mockStateService as any);

    const marker1: ParsedPhaseMarker = {
      name: "read-pr-context",
      index: 0,
      total: 14,
      stage: "pr-merge",
    };
    const marker2: ParsedPhaseMarker = {
      name: "batch-detection",
      index: 1,
      total: 14,
      stage: "pr-merge",
    };

    tracker.onPhaseDetected("pr-merge" as PipelineStage, marker1);
    tracker.onPhaseDetected("pr-merge" as PipelineStage, marker2);

    // Wait for enqueued async work
    await vi.waitFor(() => {
      expect(mockStateService.startPhase).toHaveBeenCalledTimes(2);
    });

    expect(mockStateService.completePhase).toHaveBeenCalledWith("pr-merge", "read-pr-context", 14);
    expect(mockStateService.startPhase).toHaveBeenCalledWith("pr-merge", "batch-detection", 14);
  });

  it("should auto-skip untracked phases when stage completes (Issue #1232)", async () => {
    const tracker = createPhaseTracker(mockStateService as any);

    // Simulate pr-merge with only 6 of 14 phases emitted
    const emittedPhases = [
      "read-pr-context",
      "batch-detection",
      "validate-environment",
      "ci-gate",
      "auto-fix-retry",
      "fetch-reviews",
    ];

    for (let i = 0; i < emittedPhases.length; i++) {
      tracker.onPhaseDetected("pr-merge" as PipelineStage, {
        name: emittedPhases[i],
        index: i,
        total: 14,
        stage: "pr-merge",
      });
    }

    // Complete the stage
    tracker.completeStagePhases("pr-merge" as PipelineStage);

    // Wait for all enqueued work to finish.
    // skipPhase is now called for ALL 14 registry phases (not just the 8
    // untracked ones). It's idempotent in real state.json because
    // skipPhase returns early when a phase already exists; the mock always
    // resolves, so the call count reflects all registry phases.
    await vi.waitFor(() => {
      expect(mockStateService.skipPhase).toHaveBeenCalledTimes(14);
    });

    // The 8 genuinely-untracked phases must still be in the skip list
    const skippedNames = mockStateService.skipPhase.mock.calls.map((call: unknown[]) => call[1]);
    expect(skippedNames).toContain("categorize-issues");
    expect(skippedNames).toContain("address-feedback");
    expect(skippedNames).toContain("freshness-check");
    expect(skippedNames).toContain("merge");
    expect(skippedNames).toContain("post-merge-cleanup");
    expect(skippedNames).toContain("output-summary");
    expect(skippedNames).toContain("self-assessment");
  });

  it("skips all registry phases via skipPhase (idempotent) when all phases are emitted", async () => {
    const tracker = createPhaseTracker(mockStateService as any);

    // Emit all 14 pr-merge phases
    const allPhases = [
      "read-pr-context",
      "batch-detection",
      "validate-environment",
      "ci-gate",
      "auto-fix-retry",
      "fetch-reviews",
      "categorize-issues",
      "address-feedback",
      "freshness-check",
      "merge",
      "post-merge-cleanup",
      "output-summary",
      "self-assessment",
    ];

    for (let i = 0; i < allPhases.length; i++) {
      tracker.onPhaseDetected("pr-merge" as PipelineStage, {
        name: allPhases[i],
        index: i,
        total: 14,
        stage: "pr-merge",
      });
    }

    tracker.completeStagePhases("pr-merge" as PipelineStage);

    // Wait for enqueued work
    await vi.waitFor(() => {
      expect(mockStateService.completePhase).toHaveBeenCalledWith(
        "pr-merge",
        "self-assessment",
        14
      );
    });

    // skipPhase is called for ALL 14 registry phases — in real state.json
    // the phases already exist so skipPhase is a no-op for each one, but
    // the mock always resolves so we see all 14 calls.
    await vi.waitFor(() => {
      expect(mockStateService.skipPhase).toHaveBeenCalledTimes(14);
    });
  });

  it("should handle completeStagePhases for stage with no phases emitted", async () => {
    const tracker = createPhaseTracker(mockStateService as any);

    // Complete stage without any phases emitted — should still auto-skip all
    tracker.completeStagePhases("pr-merge" as PipelineStage);

    // Wait for enqueued work
    await vi.waitFor(() => {
      // All 14 pr-merge phases should be skipped
      expect(mockStateService.skipPhase).toHaveBeenCalledTimes(14);
    });

    expect(mockStateService.completePhase).not.toHaveBeenCalled();
  });

  it("should handle completeAllStages", async () => {
    const tracker = createPhaseTracker(mockStateService as any);

    // Start phases in two stages
    tracker.onPhaseDetected("pr-merge" as PipelineStage, {
      name: "read-pr-context",
      index: 0,
      total: 14,
      stage: "pr-merge",
    });
    tracker.onPhaseDetected("feature-dev" as PipelineStage, {
      name: "validate-environment",
      index: 0,
      total: 18,
      stage: "feature-dev",
    });

    tracker.completeAllStages();

    // Wait for enqueued work — both stages should complete their active phases
    await vi.waitFor(() => {
      expect(mockStateService.completePhase).toHaveBeenCalledWith(
        "pr-merge",
        "read-pr-context",
        14
      );
      expect(mockStateService.completePhase).toHaveBeenCalledWith(
        "feature-dev",
        "validate-environment",
        18
      );
    });
  });
});
