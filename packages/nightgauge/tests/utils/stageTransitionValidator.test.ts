import { describe, it, expect } from "vitest";
import {
  getStageIndex,
  isBackwardTransition,
  getHighestCompletedStage,
  validateTransition,
  STAGE_ORDER,
  MAX_STAGE_RETRIES,
  type ExtendedStageState,
} from "../../src/utils/stageTransitionValidator";
import type { PipelineStage } from "@nightgauge/sdk";

/**
 * Helper to create a stages record with specified statuses
 */
function createStages(
  overrides: Partial<Record<PipelineStage, Partial<ExtendedStageState>>> = {}
): Record<PipelineStage, ExtendedStageState> {
  const stages = {} as Record<PipelineStage, ExtendedStageState>;
  for (const stage of STAGE_ORDER) {
    stages[stage] = {
      status: "pending",
      retry_count: 0,
      ...overrides[stage],
    };
  }
  return stages;
}

describe("stageTransitionValidator", () => {
  describe("getStageIndex", () => {
    it("should return correct index for each stage", () => {
      // Bookend stages added at beginning and end
      expect(getStageIndex("pipeline-start")).toBe(0);
      expect(getStageIndex("issue-pickup")).toBe(1);
      expect(getStageIndex("feature-planning")).toBe(2);
      expect(getStageIndex("feature-dev")).toBe(3);
      expect(getStageIndex("feature-validate")).toBe(4);
      expect(getStageIndex("pr-create")).toBe(5);
      expect(getStageIndex("pr-merge")).toBe(6);
      expect(getStageIndex("pipeline-finish")).toBe(7);
    });

    it("should return -1 for unknown stage", () => {
      expect(getStageIndex("unknown" as PipelineStage)).toBe(-1);
    });
  });

  describe("isBackwardTransition", () => {
    it("should return false when no current stage", () => {
      expect(isBackwardTransition(null, "issue-pickup")).toBe(false);
    });

    it("should return false for forward transition", () => {
      expect(isBackwardTransition("issue-pickup", "feature-planning")).toBe(false);
      expect(isBackwardTransition("feature-dev", "pr-create")).toBe(false);
    });

    it("should return false for same stage (retry)", () => {
      expect(isBackwardTransition("feature-dev", "feature-dev")).toBe(false);
    });

    it("should return true for backward transition", () => {
      expect(isBackwardTransition("pr-create", "feature-dev")).toBe(true);
      expect(isBackwardTransition("pr-merge", "issue-pickup")).toBe(true);
      expect(isBackwardTransition("feature-planning", "issue-pickup")).toBe(true);
    });
  });

  describe("getHighestCompletedStage", () => {
    it("should return null when no stages completed", () => {
      const stages = createStages();
      expect(getHighestCompletedStage(stages)).toBeNull();
    });

    it("should return the highest completed stage", () => {
      const stages = createStages({
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "running" },
      });
      expect(getHighestCompletedStage(stages)).toBe("feature-planning");
    });

    it("should handle all stages completed", () => {
      const stages = createStages();
      for (const stage of STAGE_ORDER) {
        stages[stage].status = "complete";
      }
      // pipeline-finish is now the last stage
      expect(getHighestCompletedStage(stages)).toBe("pipeline-finish");
    });

    it("should ignore failed stages", () => {
      const stages = createStages({
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "failed" },
      });
      expect(getHighestCompletedStage(stages)).toBe("issue-pickup");
    });
  });

  describe("validateTransition", () => {
    describe("issue number locking", () => {
      it("should allow transition when issue numbers match", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
        });
        const result = validateTransition(stages, "feature-planning", 42, 42);
        expect(result.allowed).toBe(true);
      });

      it("should block transition when issue numbers differ", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
        });
        const result = validateTransition(stages, "feature-planning", 42, 99);
        expect(result.allowed).toBe(false);
        expect(result.error).toContain("#42");
        expect(result.error).toContain("#99");
      });

      it("should allow transition when locked issue is null", () => {
        const stages = createStages();
        // pipeline-start is the first stage when no pipeline exists
        const result = validateTransition(stages, "pipeline-start", null, 42);
        expect(result.allowed).toBe(true);
      });

      it("should allow transition when requested issue is null", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
        });
        const result = validateTransition(stages, "feature-planning", 42, null);
        expect(result.allowed).toBe(true);
      });
    });

    describe("retry count circuit breaker", () => {
      it("should allow retry when under limit", () => {
        const stages = createStages({
          "feature-dev": { status: "failed", retry_count: 1 },
        });
        const result = validateTransition(stages, "feature-dev", 42, 42);
        expect(result.allowed).toBe(true);
        expect(result.retryCount).toBe(1);
      });

      it("should block retry when at limit", () => {
        const stages = createStages({
          "feature-dev": { status: "failed", retry_count: MAX_STAGE_RETRIES },
        });
        const result = validateTransition(stages, "feature-dev", 42, 42);
        expect(result.allowed).toBe(false);
        expect(result.error).toContain(`${MAX_STAGE_RETRIES}`);
        expect(result.retryCount).toBe(MAX_STAGE_RETRIES);
        expect(result.maxRetries).toBe(MAX_STAGE_RETRIES);
      });

      it("should block retry when over limit", () => {
        const stages = createStages({
          "feature-dev": {
            status: "failed",
            retry_count: MAX_STAGE_RETRIES + 1,
          },
        });
        const result = validateTransition(stages, "feature-dev", 42, 42);
        expect(result.allowed).toBe(false);
      });
    });

    describe("backward transition detection", () => {
      it("should require confirmation for backward transition", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "complete" },
        });
        const result = validateTransition(stages, "feature-planning", 42, 42);
        expect(result.allowed).toBe(false);
        expect(result.requiresConfirmation).toBe(true);
        expect(result.confirmationMessage).toContain("feature-planning");
        expect(result.confirmationMessage).toContain("feature-dev");
      });

      it("should allow forward transition without confirmation", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
        });
        const result = validateTransition(stages, "feature-dev", 42, 42);
        expect(result.allowed).toBe(true);
        expect(result.requiresConfirmation).toBeFalsy();
      });

      it("should allow same-stage retry without confirmation", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "failed", retry_count: 1 },
        });
        const result = validateTransition(stages, "feature-planning", 42, 42);
        expect(result.allowed).toBe(true);
        expect(result.requiresConfirmation).toBeFalsy();
      });

      it("should allow skipping stages forward", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
        });
        const result = validateTransition(stages, "feature-dev", 42, 42);
        expect(result.allowed).toBe(true);
      });
    });

    describe("combined scenarios", () => {
      it("should prioritize issue number mismatch over retry count", () => {
        const stages = createStages({
          "feature-dev": { status: "failed", retry_count: 1 },
        });
        const result = validateTransition(stages, "feature-dev", 42, 99);
        // Issue mismatch should be checked first
        expect(result.allowed).toBe(false);
        expect(result.error).toContain("Issue number mismatch");
      });

      it("should prioritize issue number mismatch over backward transition", () => {
        const stages = createStages({
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
        });
        const result = validateTransition(stages, "issue-pickup", 42, 99);
        expect(result.allowed).toBe(false);
        expect(result.error).toContain("Issue number mismatch");
        expect(result.requiresConfirmation).toBeFalsy();
      });

      it("should prioritize retry count over backward transition", () => {
        const stages = createStages({
          "issue-pickup": {
            status: "failed",
            retry_count: MAX_STAGE_RETRIES,
          },
          "feature-planning": { status: "complete" },
        });
        const result = validateTransition(stages, "issue-pickup", 42, 42);
        expect(result.allowed).toBe(false);
        expect(result.error).toContain("Maximum retries");
        expect(result.requiresConfirmation).toBeFalsy();
      });
    });

    describe("edge cases", () => {
      it("should handle undefined retry_count as 0", () => {
        const stages = createStages({
          "feature-dev": { status: "pending" },
        });
        // Remove retry_count to simulate legacy state
        delete (stages["feature-dev"] as Partial<ExtendedStageState>).retry_count;

        const result = validateTransition(stages, "feature-dev", 42, 42);
        expect(result.allowed).toBe(true);
        expect(result.retryCount).toBe(0);
      });

      it("should handle all stages pending", () => {
        const stages = createStages();
        // pipeline-start is the first stage when starting a new pipeline
        const result = validateTransition(stages, "pipeline-start", null, 42);
        expect(result.allowed).toBe(true);
      });

      it("should handle transitioning from last stage back to first", () => {
        const stages = createStages();
        for (const stage of STAGE_ORDER) {
          stages[stage].status = "complete";
        }
        const result = validateTransition(stages, "issue-pickup", 42, 42);
        expect(result.allowed).toBe(false);
        expect(result.requiresConfirmation).toBe(true);
      });
    });
  });

  describe("STAGE_ORDER", () => {
    it("should have 8 stages (including bookend stages)", () => {
      expect(STAGE_ORDER).toHaveLength(8);
    });

    it("should start with pipeline-start and end with pipeline-finish", () => {
      expect(STAGE_ORDER[0]).toBe("pipeline-start");
      expect(STAGE_ORDER[STAGE_ORDER.length - 1]).toBe("pipeline-finish");
    });

    it("should have skill stages in correct order between bookends", () => {
      expect(STAGE_ORDER[1]).toBe("issue-pickup");
      expect(STAGE_ORDER[2]).toBe("feature-planning");
      expect(STAGE_ORDER[3]).toBe("feature-dev");
      expect(STAGE_ORDER[4]).toBe("feature-validate");
      expect(STAGE_ORDER[5]).toBe("pr-create");
      expect(STAGE_ORDER[6]).toBe("pr-merge");
    });
  });

  describe("bookend stage transitions", () => {
    it("should allow pipeline-start to issue-pickup transition", () => {
      const stages = createStages({
        "pipeline-start": { status: "complete" },
      });
      const result = validateTransition(stages, "issue-pickup", 42, 42);
      expect(result.allowed).toBe(true);
    });

    it("should allow pr-merge to pipeline-finish transition", () => {
      const stages = createStages({
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "complete" },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
      });
      const result = validateTransition(stages, "pipeline-finish", 42, 42);
      expect(result.allowed).toBe(true);
    });

    it("should require confirmation for pipeline-finish to pipeline-start backward transition", () => {
      const stages = createStages();
      for (const stage of STAGE_ORDER) {
        stages[stage].status = "complete";
      }
      const result = validateTransition(stages, "pipeline-start", 42, 42);
      expect(result.allowed).toBe(false);
      expect(result.requiresConfirmation).toBe(true);
    });
  });

  describe("MAX_STAGE_RETRIES", () => {
    it("should be 3", () => {
      expect(MAX_STAGE_RETRIES).toBe(3);
    });
  });
});
