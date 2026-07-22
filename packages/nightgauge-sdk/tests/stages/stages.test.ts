import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { TokenTracker } from "../../src/tracking/TokenTracker.js";
import { ContextManager } from "../../src/context/ContextManager.js";
import {
  IssuePickupStage,
  FeaturePlanningStage,
  FeatureDevStage,
  PRCreateStage,
} from "../../src/stages/index.js";

describe("Pipeline Stages", () => {
  let tokenTracker: TokenTracker;
  let contextManager: ContextManager;
  const testContextPath = ".nightgauge/pipeline-test";

  beforeEach(async () => {
    tokenTracker = new TokenTracker();
    contextManager = new ContextManager(testContextPath);
    await fs.mkdir(testContextPath, { recursive: true });
  });

  afterEach(async () => {
    // Clean up test context files
    try {
      await fs.rm(testContextPath, { recursive: true, force: true });
    } catch {
      // Ignore cleanup errors
    }
  });

  // Stage Contract Validation - Tests the essential behavioral contracts
  // Note: Individual stage config tests were removed as trivial (Issue #485 audit)
  // The contract tests below verify the same properties through behavioral assertions

  describe("Stage Contract Validation", () => {
    const STAGE_CONFIGS = [
      {
        Stage: IssuePickupStage,
        name: "issue-pickup",
        input: undefined,
        output: "issue",
        requiresApproval: false,
      },
      {
        Stage: FeaturePlanningStage,
        name: "feature-planning",
        input: "issue",
        output: "planning",
        requiresApproval: true,
      },
      {
        Stage: FeatureDevStage,
        name: "feature-dev",
        input: "planning",
        output: "dev",
        requiresApproval: false,
      },
      {
        Stage: PRCreateStage,
        name: "pr-create",
        input: "dev",
        output: "pr",
        requiresApproval: false,
      },
    ] as const;

    it("all stages should have consistent name from config and getName()", () => {
      STAGE_CONFIGS.forEach(({ Stage, name }) => {
        const stage = new Stage();
        expect(stage.config.name).toBe(name);
        expect(stage.getName()).toBe(name);
      });
    });

    it("all stages should have correct input/output context types", () => {
      STAGE_CONFIGS.forEach(({ Stage, input, output }) => {
        const stage = new Stage();
        expect(stage.config.inputContextType).toBe(input);
        expect(stage.config.outputContextType).toBe(output);
      });
    });

    it("only FeaturePlanningStage should require approval", () => {
      STAGE_CONFIGS.forEach(({ Stage, requiresApproval }) => {
        const stage = new Stage();
        expect(stage.requiresApproval()).toBe(requiresApproval);
      });
    });

    it("stages should form correct input/output chain for pipeline flow", () => {
      // This test ensures the pipeline stages connect properly:
      // issue-pickup (output: issue) -> feature-planning (input: issue)
      // feature-planning (output: planning) -> feature-dev (input: planning)
      // feature-dev (output: dev) -> pr-create (input: dev)
      for (let i = 0; i < STAGE_CONFIGS.length - 1; i++) {
        const currentStage = new STAGE_CONFIGS[i].Stage();
        const nextStage = new STAGE_CONFIGS[i + 1].Stage();

        expect(currentStage.config.outputContextType).toBe(nextStage.config.inputContextType);
      }
    });
  });
});
