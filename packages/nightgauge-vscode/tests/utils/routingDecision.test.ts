/**
 * Unit tests for RoutingDecision
 *
 * @see routingDecision.ts
 * @see Issue #216 - Complexity-Based Stage Routing
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { ChangeAnalysis } from "../../src/utils/changeAnalyzer";
import {
  DEFAULT_ROUTING_CONFIG,
  isSkippableStage,
  shouldSkipStage,
  shouldExecuteStage,
  getStagesToExecute,
  getStagesToSkip,
  getStagesForTaskType,
  applyConfigOverrides,
  makeRoutingDecision,
  buildPickupRecommendation,
  getNextStage,
  getPreviousStage,
  isFirstStage,
  isLastStage,
  getStageIndex,
  getProgressInfo,
  formatRoutingDecision,
  type RoutingConfig,
} from "../../src/utils/routingDecision";

// Helper to create a basic analysis for testing
function createMockAnalysis(overrides: Partial<ChangeAnalysis> = {}): ChangeAnalysis {
  return {
    changeType: "code",
    taskType: "feature",
    sizeLabel: "M",
    typeLabel: "feature",
    priorityLabel: "medium",
    complexityScore: 3,
    suggestedRoute: "standard",
    skipStages: [],
    rationale: "Standard path selected",
    estimatedTimeMinutes: 30,
    ...overrides,
  };
}

describe("isSkippableStage", () => {
  it("should identify feature-planning as skippable", () => {
    expect(isSkippableStage("feature-planning")).toBe(true);
  });

  it("should identify feature-validate as skippable", () => {
    expect(isSkippableStage("feature-validate")).toBe(true);
  });

  it("should identify pr-create as skippable (Issue #268)", () => {
    expect(isSkippableStage("pr-create")).toBe(true);
  });

  it("should identify pr-merge as skippable (Issue #268)", () => {
    expect(isSkippableStage("pr-merge")).toBe(true);
  });

  it("should not identify issue-pickup as skippable", () => {
    expect(isSkippableStage("issue-pickup")).toBe(false);
  });

  it("should not identify feature-dev as skippable", () => {
    expect(isSkippableStage("feature-dev")).toBe(false);
  });
});

describe("getStagesForTaskType", () => {
  it("should return 4 stages for verification tasks (Issue #418 - includes PR stages)", () => {
    const stages = getStagesForTaskType("verification");

    // Verification tasks include PR stages to document audit findings
    // Skip planning (no design decisions) and validation (nothing to validate)
    expect(stages).toEqual(["issue-pickup", "feature-dev", "pr-create", "pr-merge"]);
    expect(stages.length).toBe(4);
  });

  it("should return all 6 stages for feature tasks", () => {
    const stages = getStagesForTaskType("feature");

    expect(stages).toEqual([
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]);
    expect(stages.length).toBe(6);
  });

  it("should return all 6 stages for bugfix tasks", () => {
    const stages = getStagesForTaskType("bugfix");

    expect(stages).toEqual([
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]);
  });

  it("should skip feature-validate for docs-only tasks", () => {
    const stages = getStagesForTaskType("docs-only");

    expect(stages).not.toContain("feature-validate");
    expect(stages.length).toBe(5);
  });

  it("should skip feature-planning for chore tasks", () => {
    const stages = getStagesForTaskType("chore");

    expect(stages).not.toContain("feature-planning");
    expect(stages.length).toBe(5);
  });

  it("should return all 6 stages for refactor tasks", () => {
    const stages = getStagesForTaskType("refactor");

    expect(stages).toEqual([
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]);
  });
});

describe("shouldSkipStage", () => {
  it("should skip stage if in analysis skipStages", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-planning"],
    });

    expect(shouldSkipStage("feature-planning", analysis)).toBe(true);
  });

  it("should not skip stage if not in analysis skipStages", () => {
    const analysis = createMockAnalysis({
      skipStages: [],
    });

    expect(shouldSkipStage("feature-planning", analysis)).toBe(false);
  });

  it("should never skip non-skippable stages", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-planning", "feature-validate"],
    });

    expect(shouldSkipStage("issue-pickup", analysis)).toBe(false);
    expect(shouldSkipStage("feature-dev", analysis)).toBe(false);
  });

  it("should not skip any stage when forceFullPipeline is true", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-planning", "feature-validate"],
    });
    const config: RoutingConfig = {
      ...DEFAULT_ROUTING_CONFIG,
      forceFullPipeline: true,
    };

    expect(shouldSkipStage("feature-planning", analysis, config)).toBe(false);
    expect(shouldSkipStage("feature-validate", analysis, config)).toBe(false);
  });

  it("should NOT skip pr-create for verification tasks (Issue #418 - needs PR for audit findings)", () => {
    const analysis = createMockAnalysis({
      taskType: "verification",
      skipStages: [],
    });

    // Verification tasks now include PR stages to document audit findings
    expect(shouldSkipStage("pr-create", analysis)).toBe(false);
  });

  it("should NOT skip pr-merge for verification tasks (Issue #418 - needs PR for audit findings)", () => {
    const analysis = createMockAnalysis({
      taskType: "verification",
      skipStages: [],
    });

    // Verification tasks now include PR stages to document audit findings
    expect(shouldSkipStage("pr-merge", analysis)).toBe(false);
  });

  it("should skip feature-planning for verification tasks (Issue #268)", () => {
    const analysis = createMockAnalysis({
      taskType: "verification",
      skipStages: [],
    });

    expect(shouldSkipStage("feature-planning", analysis)).toBe(true);
  });

  it("should skip feature-validate for verification tasks (Issue #268)", () => {
    const analysis = createMockAnalysis({
      taskType: "verification",
      skipStages: [],
    });

    expect(shouldSkipStage("feature-validate", analysis)).toBe(true);
  });

  it("should not skip pr-create for feature tasks", () => {
    const analysis = createMockAnalysis({
      taskType: "feature",
      skipStages: [],
    });

    expect(shouldSkipStage("pr-create", analysis)).toBe(false);
  });

  it("should skip feature-validate for docs-only tasks (Issue #268)", () => {
    const analysis = createMockAnalysis({
      taskType: "docs-only",
      skipStages: [],
    });

    expect(shouldSkipStage("feature-validate", analysis)).toBe(true);
  });
});

describe("shouldExecuteStage", () => {
  it("should execute stage not in skipStages", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-validate"],
    });

    expect(shouldExecuteStage("feature-planning", analysis)).toBe(true);
  });

  it("should not execute stage in skipStages", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-validate"],
    });

    expect(shouldExecuteStage("feature-validate", analysis)).toBe(false);
  });

  it("should always execute non-skippable stages", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-planning", "feature-validate"],
    });

    expect(shouldExecuteStage("issue-pickup", analysis)).toBe(true);
    expect(shouldExecuteStage("feature-dev", analysis)).toBe(true);
    expect(shouldExecuteStage("pr-create", analysis)).toBe(true);
    expect(shouldExecuteStage("pr-merge", analysis)).toBe(true);
  });
});

describe("getStagesToExecute", () => {
  it("should return all stages for standard route", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "standard",
      skipStages: [],
    });

    const result = getStagesToExecute(analysis);

    expect(result).toEqual([
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]);
  });

  it("should exclude skipped stages for trivial route", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
    });

    const result = getStagesToExecute(analysis);

    expect(result).toEqual(["issue-pickup", "feature-dev", "pr-create", "pr-merge"]);
  });

  it("should return all stages when forceFullPipeline is true", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
    });
    const config: RoutingConfig = {
      ...DEFAULT_ROUTING_CONFIG,
      forceFullPipeline: true,
    };

    const result = getStagesToExecute(analysis, config);

    expect(result).toEqual([
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]);
  });

  it("should return 4 stages for verification tasks (Issue #418 - includes PR stages)", () => {
    const analysis = createMockAnalysis({
      taskType: "verification",
      suggestedRoute: "standard",
      skipStages: [],
    });

    const result = getStagesToExecute(analysis);

    // Verification tasks include PR stages to document audit findings
    // Skip planning (no design decisions) and validation (nothing to validate)
    expect(result).toEqual(["issue-pickup", "feature-dev", "pr-create", "pr-merge"]);
    expect(result.length).toBe(4);
  });

  it("should return 5 stages for docs-only tasks (Issue #268)", () => {
    const analysis = createMockAnalysis({
      taskType: "docs-only",
      suggestedRoute: "standard",
      skipStages: [],
    });

    const result = getStagesToExecute(analysis);

    expect(result).not.toContain("feature-validate");
    expect(result.length).toBe(5);
  });

  it("should return 5 stages for chore tasks (Issue #268)", () => {
    const analysis = createMockAnalysis({
      taskType: "chore",
      suggestedRoute: "standard",
      skipStages: [],
    });

    const result = getStagesToExecute(analysis);

    expect(result).not.toContain("feature-planning");
    expect(result.length).toBe(5);
  });

  it("should return all 6 stages for bugfix tasks", () => {
    const analysis = createMockAnalysis({
      taskType: "bugfix",
      suggestedRoute: "standard",
      skipStages: [],
    });

    const result = getStagesToExecute(analysis);

    expect(result.length).toBe(6);
  });
});

describe("getStagesToSkip", () => {
  it("should return empty array for standard route", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "standard",
      skipStages: [],
    });

    const result = getStagesToSkip(analysis);

    expect(result).toEqual([]);
  });

  it("should return skipped stages for trivial route", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
    });

    const result = getStagesToSkip(analysis);

    expect(result).toEqual(["feature-planning", "feature-validate"]);
  });
});

describe("applyConfigOverrides", () => {
  it("should not modify analysis when no overrides", () => {
    const analysis = createMockAnalysis();

    const { analysis: result, wasOverridden } = applyConfigOverrides(
      analysis,
      DEFAULT_ROUTING_CONFIG
    );

    expect(result.suggestedRoute).toBe("standard");
    expect(result.skipStages).toEqual([]);
    expect(wasOverridden).toBe(false);
  });

  it("should clear skipStages when forceFullPipeline is true", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
    });
    const config: RoutingConfig = {
      ...DEFAULT_ROUTING_CONFIG,
      forceFullPipeline: true,
    };

    const {
      analysis: result,
      wasOverridden,
      originalRoute,
    } = applyConfigOverrides(analysis, config);

    expect(result.skipStages).toEqual([]);
    expect(result.suggestedRoute).toBe("standard");
    expect(wasOverridden).toBe(true);
    expect(originalRoute).toBe("trivial");
  });

  it("should apply overrideRoute when provided", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "standard",
      skipStages: [],
    });
    const config: RoutingConfig = {
      ...DEFAULT_ROUTING_CONFIG,
      overrideRoute: "trivial",
    };

    const {
      analysis: result,
      wasOverridden,
      originalRoute,
    } = applyConfigOverrides(analysis, config);

    expect(result.suggestedRoute).toBe("trivial");
    expect(result.skipStages).toContain("feature-planning");
    expect(result.skipStages).toContain("feature-validate");
    expect(wasOverridden).toBe(true);
    expect(originalRoute).toBe("standard");
  });

  it("should not duplicate skipStages when overriding", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "standard",
      skipStages: ["feature-validate"],
    });
    const config: RoutingConfig = {
      ...DEFAULT_ROUTING_CONFIG,
      overrideRoute: "trivial",
    };

    const { analysis: result } = applyConfigOverrides(analysis, config);

    // Should not have duplicate feature-validate
    expect(result.skipStages.filter((s) => s === "feature-validate").length).toBe(1);
  });
});

describe("makeRoutingDecision", () => {
  it("should make standard routing decision", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "standard",
      skipStages: [],
    });

    const decision = makeRoutingDecision(analysis);

    expect(decision.route).toBe("standard");
    expect(decision.skipStages).toEqual([]);
    expect(decision.executeStages.length).toBe(6);
    expect(decision.wasOverridden).toBe(false);
    expect(decision.explanation).toContain("standard");
    expect(decision.explanation).toContain("30 minutes");
  });

  it("should make trivial routing decision", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
      estimatedTimeMinutes: 6,
    });

    const decision = makeRoutingDecision(analysis);

    expect(decision.route).toBe("trivial");
    expect(decision.skipStages).toContain("feature-planning");
    expect(decision.skipStages).toContain("feature-validate");
    expect(decision.executeStages.length).toBe(4);
    expect(decision.explanation).toContain("Skipping");
  });

  it("should indicate when overridden", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
    });
    const config: RoutingConfig = {
      ...DEFAULT_ROUTING_CONFIG,
      forceFullPipeline: true,
    };

    const decision = makeRoutingDecision(analysis, config);

    expect(decision.wasOverridden).toBe(true);
    expect(decision.originalRoute).toBe("trivial");
    expect(decision.explanation).toContain("overridden");
  });
});

describe("getNextStage", () => {
  it("should return next stage in execute list", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(getNextStage("issue-pickup", decision)).toBe("feature-planning");
    expect(getNextStage("feature-planning", decision)).toBe("feature-dev");
    expect(getNextStage("feature-dev", decision)).toBe("feature-validate");
  });

  it("should return null for last stage", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(getNextStage("pr-merge", decision)).toBeNull();
  });

  it("should skip stages not in execute list", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-planning"],
    });
    const decision = makeRoutingDecision(analysis);

    // issue-pickup should go straight to feature-dev
    expect(getNextStage("issue-pickup", decision)).toBe("feature-dev");
  });
});

describe("getPreviousStage", () => {
  it("should return previous stage in execute list", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(getPreviousStage("pr-merge", decision)).toBe("pr-create");
    expect(getPreviousStage("feature-dev", decision)).toBe("feature-planning");
  });

  it("should return null for first stage", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(getPreviousStage("issue-pickup", decision)).toBeNull();
  });
});

describe("isFirstStage and isLastStage", () => {
  it("should correctly identify first stage", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(isFirstStage("issue-pickup", decision)).toBe(true);
    expect(isFirstStage("feature-planning", decision)).toBe(false);
  });

  it("should correctly identify last stage", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(isLastStage("pr-merge", decision)).toBe(true);
    expect(isLastStage("pr-create", decision)).toBe(false);
  });
});

describe("getStageIndex", () => {
  it("should return correct index for stages", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(getStageIndex("issue-pickup", decision)).toBe(0);
    expect(getStageIndex("feature-planning", decision)).toBe(1);
    expect(getStageIndex("pr-merge", decision)).toBe(5);
  });

  it("should return -1 for skipped stages", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-planning"],
    });
    const decision = makeRoutingDecision(analysis);

    expect(getStageIndex("feature-planning", decision)).toBe(-1);
  });
});

describe("getProgressInfo", () => {
  it("should calculate progress correctly", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));

    expect(getProgressInfo("issue-pickup", decision)).toEqual({
      current: 1,
      total: 6,
      percent: 17,
    });

    expect(getProgressInfo("feature-dev", decision)).toEqual({
      current: 3,
      total: 6,
      percent: 50,
    });

    expect(getProgressInfo("pr-merge", decision)).toEqual({
      current: 6,
      total: 6,
      percent: 100,
    });
  });

  it("should adjust total for skipped stages", () => {
    const analysis = createMockAnalysis({
      skipStages: ["feature-planning", "feature-validate"],
    });
    const decision = makeRoutingDecision(analysis);

    expect(getProgressInfo("issue-pickup", decision)).toEqual({
      current: 1,
      total: 4,
      percent: 25,
    });

    expect(getProgressInfo("feature-dev", decision)).toEqual({
      current: 2,
      total: 4,
      percent: 50,
    });
  });
});

describe("formatRoutingDecision", () => {
  it("should format standard decision", () => {
    const decision = makeRoutingDecision(createMockAnalysis({ skipStages: [] }));
    const formatted = formatRoutingDecision(decision);

    expect(formatted).toContain("STANDARD");
    expect(formatted).toContain("Stages: 6");
  });

  it("should include skipped stages in format", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
    });
    const decision = makeRoutingDecision(analysis);
    const formatted = formatRoutingDecision(decision);

    expect(formatted).toContain("TRIVIAL");
    expect(formatted).toContain("Skipping");
  });

  it("should indicate override in format", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning"],
    });
    const config: RoutingConfig = {
      ...DEFAULT_ROUTING_CONFIG,
      forceFullPipeline: true,
    };
    const decision = makeRoutingDecision(analysis, config);
    const formatted = formatRoutingDecision(decision);

    expect(formatted).toContain("Overridden");
  });
});

describe("buildPickupRecommendation (Issue #1593)", () => {
  it("should build trivial recommendation with skipped stages", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
      complexityScore: 2,
    });
    const decision = makeRoutingDecision(analysis);
    const recommendation = buildPickupRecommendation(decision, 2);

    expect(recommendation.complexity).toBe(2);
    expect(recommendation.recommended_stages).toEqual([
      "issue-pickup",
      "feature-dev",
      "pr-create",
      "pr-merge",
    ]);
    expect(recommendation.skipped_stages).toContain("feature-planning");
    expect(recommendation.skipped_stages).toContain("feature-validate");
    expect(recommendation.dev_model).toBe("sonnet");
    expect(recommendation.validate_model).toBeNull();
  });

  it("should build standard recommendation with all stages", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "standard",
      skipStages: [],
      complexityScore: 3,
    });
    const decision = makeRoutingDecision(analysis);
    const recommendation = buildPickupRecommendation(decision, 3);

    expect(recommendation.complexity).toBe(3);
    expect(recommendation.recommended_stages.length).toBe(6);
    expect(recommendation.skipped_stages).toEqual([]);
    expect(recommendation.dev_model).toBe("sonnet");
    expect(recommendation.validate_model).toBe("sonnet");
  });

  it("should recommend opus for dev on complex issues", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "extensive",
      skipStages: [],
      complexityScore: 5,
    });
    const decision = makeRoutingDecision(analysis);
    const recommendation = buildPickupRecommendation(decision, 5);

    expect(recommendation.complexity).toBe(5);
    expect(recommendation.dev_model).toBe("opus");
    expect(recommendation.validate_model).toBe("opus");
  });

  it("should recommend haiku for validate on trivial complexity", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: [],
      complexityScore: 2,
    });
    const decision = makeRoutingDecision(analysis);
    const recommendation = buildPickupRecommendation(decision, 2);

    // validate not skipped → haiku for trivial complexity
    expect(recommendation.validate_model).toBe("haiku");
  });

  it("should set validate_model to null when validate is skipped", () => {
    const analysis = createMockAnalysis({
      suggestedRoute: "trivial",
      skipStages: ["feature-planning", "feature-validate"],
      complexityScore: 1,
    });
    const decision = makeRoutingDecision(analysis);
    const recommendation = buildPickupRecommendation(decision, 1);

    expect(recommendation.validate_model).toBeNull();
    expect(recommendation.skip_rationale).toBeTruthy();
  });
});

describe("spike task routing", () => {
  it("should return 4 stages for spike tasks (skips planning and validate, Issue #2614)", () => {
    const stages = getStagesForTaskType("spike");

    // Spike tasks skip feature-planning (Issue #2614) and feature-validate
    expect(stages).toEqual(["issue-pickup", "feature-dev", "pr-create", "pr-merge"]);
    expect(stages.length).toBe(4);
  });

  it("should skip feature-validate for spike tasks", () => {
    const analysis = createMockAnalysis({
      taskType: "spike",
      skipStages: [],
    });

    expect(shouldSkipStage("feature-validate", analysis)).toBe(true);
  });

  it("should skip feature-planning for spike tasks (Issue #2614)", () => {
    const analysis = createMockAnalysis({
      taskType: "spike",
      skipStages: [],
    });

    // Issue #2614: feature-planning removed from spike stages — spikes define
    // their own methodology in acceptance criteria
    expect(shouldSkipStage("feature-planning", analysis)).toBe(true);
  });

  it("should execute correct 4 stages for spike (Issue #2614)", () => {
    const analysis = createMockAnalysis({
      taskType: "spike",
      suggestedRoute: "standard",
      skipStages: [],
    });

    const result = getStagesToExecute(analysis);

    // Issue #2614: spike stages no longer include feature-planning
    expect(result).toEqual(["issue-pickup", "feature-dev", "pr-create", "pr-merge"]);
    expect(result.length).toBe(4);
  });

  it("should NOT skip pr-create for spike tasks", () => {
    const analysis = createMockAnalysis({
      taskType: "spike",
      skipStages: [],
    });

    expect(shouldSkipStage("pr-create", analysis)).toBe(false);
  });

  it("should include feature-planning in spike skipStages (Issue #2614)", () => {
    const analysis = createMockAnalysis({
      taskType: "spike",
      skipStages: [],
    });

    const skipped = getStagesToSkip(analysis);
    expect(skipped).toContain("feature-planning");
    expect(skipped).toContain("feature-validate");
  });
});
