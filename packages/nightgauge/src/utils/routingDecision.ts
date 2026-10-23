/**
 * RoutingDecision - Pure functions for pipeline stage routing decisions
 *
 * This module provides deterministic logic for determining which pipeline stages
 * to execute based on the change analysis from ChangeAnalyzer.
 *
 * All functions are pure (no side effects) to enable easy testing and predictable behavior.
 *
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 * @see Issue #216 - Complexity-Based Stage Routing
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { ChangeAnalysis, RoutingPath, SkippableStage, TaskType } from "./changeAnalyzer";

/**
 * Configuration for routing thresholds (from config.yaml)
 */
export interface RoutingConfig {
  /** Max complexity score for trivial path (default: 2) */
  trivialMaxComplexity: number;
  /** Min complexity score for extensive path (default: 5) */
  extensiveMinComplexity: number;
  /** Always run full pipeline regardless of analysis */
  forceFullPipeline: boolean;
  /** Override detected route */
  overrideRoute?: RoutingPath;
}

/**
 * Default routing configuration
 */
export const DEFAULT_ROUTING_CONFIG: RoutingConfig = {
  trivialMaxComplexity: 2,
  extensiveMinComplexity: 5,
  forceFullPipeline: false,
};

/**
 * Routing decision result
 */
export interface RoutingDecision {
  /** The route to take */
  route: RoutingPath;
  /** Stages to skip */
  skipStages: PipelineStage[];
  /** Stages to execute */
  executeStages: PipelineStage[];
  /** Human-readable explanation */
  explanation: string;
  /** Whether the decision was overridden by config/flag */
  wasOverridden: boolean;
  /** Original suggested route (if overridden) */
  originalRoute?: RoutingPath;
}

/**
 * Pickup routing recommendation from issue-pickup context.
 *
 * Issue-pickup writes this to the context file so downstream stages
 * can see explicit skip recommendations with rationale.
 *
 * @see Issue #1593 - Pickup routing recommendations
 */
export interface PickupRoutingRecommendation {
  /** Complexity score determined at pickup */
  complexity: number;
  /** Stages the pipeline should execute */
  recommended_stages: PipelineStage[];
  /** Stages the pipeline should skip */
  skipped_stages: PipelineStage[];
  /** Human-readable explanation of the skip decision */
  skip_rationale: string;
  /** Recommended model for dev stage (null if skipped) */
  dev_model: string;
  /** Recommended model for validate stage (null if skipped) */
  validate_model: string | null;
}

/**
 * Build a pickup routing recommendation from a routing decision.
 *
 * Called after makeRoutingDecision() to generate the recommendation
 * that gets written to the issue context file.
 *
 * @param decision - The routing decision
 * @param complexityScore - The complexity score from change analysis
 * @returns PickupRoutingRecommendation for the context file
 *
 * @see Issue #1593 - Pickup routing recommendations
 */
export function buildPickupRecommendation(
  decision: RoutingDecision,
  complexityScore: number
): PickupRoutingRecommendation {
  // Determine dev model based on complexity
  let devModel = "sonnet";
  if (complexityScore >= 5) {
    devModel = "opus";
  } else if (complexityScore <= 2) {
    devModel = "sonnet";
  }

  // Determine validate model (null if skipped)
  let validateModel: string | null = "sonnet";
  if (decision.skipStages.includes("feature-validate")) {
    validateModel = null;
  } else if (complexityScore <= 2) {
    validateModel = "haiku";
  } else if (complexityScore >= 5) {
    validateModel = "opus";
  }

  return {
    complexity: complexityScore,
    recommended_stages: decision.executeStages,
    skipped_stages: decision.skipStages,
    skip_rationale: decision.explanation,
    dev_model: devModel,
    validate_model: validateModel,
  };
}

/**
 * All pipeline stages in order
 */
const ALL_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

/**
 * Stages that can be skipped based on routing
 *
 * Note: 'issue-pickup' and 'feature-dev' are never skippable:
 * - issue-pickup creates the context file needed by all other stages
 * - feature-dev is where the actual work happens
 *
 * @see Issue #268 - Task-Type Routing (expanded from 2 to 4 stages)
 */
const SKIPPABLE_STAGES: PipelineStage[] = [
  "feature-planning",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

/**
 * Stages to execute for each task type
 *
 * This map defines which pipeline stages are fundamentally needed for each
 * task type, independent of complexity. Complexity routing may skip additional
 * stages within these constraints.
 *
 * @see Issue #268 - Task-Type Routing
 */
const TASK_TYPE_STAGES: Record<TaskType, PipelineStage[]> = {
  // Verification tasks: pickup, dev, and PR stages for documenting audit findings
  // Skip planning (no design decisions) and validation (nothing to validate)
  // @see Issue #418 - Verification tasks need PR stages to document audit findings
  verification: ["issue-pickup", "feature-dev", "pr-create", "pr-merge"],

  // Docs-only tasks: skip validation (no code to validate)
  "docs-only": ["issue-pickup", "feature-planning", "feature-dev", "pr-create", "pr-merge"],

  // Feature tasks: full pipeline
  feature: [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ],

  // Bugfix tasks: full pipeline (need validation to verify fix)
  bugfix: [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ],

  // Refactor tasks: full pipeline (need validation to prevent regressions)
  refactor: [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ],

  // Chore tasks: skip planning (mechanical changes), keep validation
  // Rationale: Chores (dependency updates, config changes, tooling updates) can
  // still introduce regressions. While planning is skipped because chores don't
  // need architectural decisions, feature-validate catches accidental breakage.
  // @see Issue #418 - Chore routing rationale documented
  chore: ["issue-pickup", "feature-dev", "feature-validate", "pr-create", "pr-merge"],

  // Spike tasks: research/investigation that produces documentation + follow-up issues.
  // Skip planning AND validation: spikes define their own methodology in acceptance
  // criteria, and their output is documentation rather than testable code.
  // Issue #2614: feature-planning removed — spike cost observation (#2595) showed
  // planning ran 5 min and produced schema mismatches. Spike output structure is
  // deterministic from acceptance criteria.
  // @see Issue #168 - Research spike support
  // @see Issue #2614 - Spike-aware planning routing
  spike: ["issue-pickup", "feature-dev", "pr-create", "pr-merge"],
};

/**
 * Check if a stage can be skipped
 *
 * Note: 'issue-pickup' and 'feature-dev' are never skippable:
 * - issue-pickup creates the context file needed by all other stages
 * - feature-dev is where the actual work happens
 *
 * @param stage - The stage to check
 * @returns True if the stage is skippable
 *
 * @see Issue #268 - Task-Type Routing (expanded from 2 to 4 stages)
 */
export function isSkippableStage(stage: PipelineStage): boolean {
  return SKIPPABLE_STAGES.includes(stage);
}

/**
 * Get the stages to execute for a given task type.
 *
 * Checks for config overrides first, then falls back to built-in defaults.
 * Config overrides allow customizing which stages run per task type without
 * modifying source code.
 *
 * @param taskType - The task type
 * @param configOverrides - Optional task type → stage list overrides from config.yaml
 * @returns Array of stages to execute for this task type
 *
 * @see Issue #268 - Task-Type Routing
 * @see Issue #2402 - Configurable stage profiles
 */
export function getStagesForTaskType(
  taskType: TaskType,
  configOverrides?: Partial<Record<TaskType, PipelineStage[]>>
): PipelineStage[] {
  // Config overrides take precedence (Issue #2402)
  if (configOverrides?.[taskType]) {
    // Validate: issue-pickup and feature-dev are never skippable
    const stages = configOverrides[taskType]!;
    if (!stages.includes("issue-pickup")) stages.unshift("issue-pickup");
    if (!stages.includes("feature-dev")) {
      const devIdx = ALL_STAGES.indexOf("feature-dev");
      const insertIdx = stages.findIndex((s) => ALL_STAGES.indexOf(s) > devIdx);
      if (insertIdx === -1) stages.push("feature-dev");
      else stages.splice(insertIdx, 0, "feature-dev");
    }
    return stages;
  }
  return TASK_TYPE_STAGES[taskType] ?? TASK_TYPE_STAGES.feature;
}

/**
 * Get the built-in default stage profile for a task type.
 * Useful for documentation and comparison against config overrides.
 *
 * @since Issue #2402
 */
export function getDefaultStagesForTaskType(taskType: TaskType): PipelineStage[] {
  return TASK_TYPE_STAGES[taskType] ?? TASK_TYPE_STAGES.feature;
}

/**
 * Get all built-in task type stage profiles.
 * Useful for generating documentation and default config.
 *
 * @since Issue #2402
 */
export function getAllTaskTypeStages(): Record<TaskType, PipelineStage[]> {
  return { ...TASK_TYPE_STAGES };
}

/**
 * Check if a stage should be skipped based on routing
 *
 * This function considers both:
 * 1. Task-type routing: stages not in TASK_TYPE_STAGES for this task type
 * 2. Complexity routing: stages in analysis.skipStages
 *
 * @param stage - The stage to check
 * @param analysis - The change analysis result
 * @param config - Routing configuration
 * @returns True if the stage should be skipped
 *
 * @see Issue #268 - Task-Type Routing
 */
export function shouldSkipStage(
  stage: PipelineStage,
  analysis: ChangeAnalysis,
  config: RoutingConfig = DEFAULT_ROUTING_CONFIG
): boolean {
  // Never skip if force full pipeline
  if (config.forceFullPipeline) {
    return false;
  }

  // Never skip non-skippable stages
  if (!isSkippableStage(stage)) {
    return false;
  }

  // Task-type routing: check if stage is in the allowed stages for this task type
  const taskTypeStages = getStagesForTaskType(analysis.taskType);
  if (!taskTypeStages.includes(stage)) {
    return true;
  }

  // Complexity routing: check if stage is in skipStages
  return analysis.skipStages.includes(stage as SkippableStage);
}

/**
 * Check if a stage should be executed
 *
 * @param stage - The stage to check
 * @param analysis - The change analysis result
 * @param config - Routing configuration
 * @returns True if the stage should be executed
 */
export function shouldExecuteStage(
  stage: PipelineStage,
  analysis: ChangeAnalysis,
  config: RoutingConfig = DEFAULT_ROUTING_CONFIG
): boolean {
  return !shouldSkipStage(stage, analysis, config);
}

/**
 * Get the list of stages to execute based on routing
 *
 * @param analysis - The change analysis result
 * @param config - Routing configuration
 * @returns Array of stages to execute in order
 */
export function getStagesToExecute(
  analysis: ChangeAnalysis,
  config: RoutingConfig = DEFAULT_ROUTING_CONFIG
): PipelineStage[] {
  return ALL_STAGES.filter((stage) => shouldExecuteStage(stage, analysis, config));
}

/**
 * Get the list of stages to skip based on routing
 *
 * @param analysis - The change analysis result
 * @param config - Routing configuration
 * @returns Array of stages to skip
 */
export function getStagesToSkip(
  analysis: ChangeAnalysis,
  config: RoutingConfig = DEFAULT_ROUTING_CONFIG
): PipelineStage[] {
  return ALL_STAGES.filter((stage) => shouldSkipStage(stage, analysis, config));
}

/**
 * Apply config overrides to analysis
 *
 * @param analysis - The change analysis result
 * @param config - Routing configuration
 * @returns Modified analysis with overrides applied
 */
export function applyConfigOverrides(
  analysis: ChangeAnalysis,
  config: RoutingConfig
): {
  analysis: ChangeAnalysis;
  wasOverridden: boolean;
  originalRoute?: RoutingPath;
} {
  let wasOverridden = false;
  let originalRoute: RoutingPath | undefined;

  // Clone analysis to avoid mutation
  const result = { ...analysis, skipStages: [...analysis.skipStages] };

  // Handle force full pipeline
  if (config.forceFullPipeline) {
    if (result.skipStages.length > 0) {
      wasOverridden = true;
      originalRoute = result.suggestedRoute;
      result.skipStages = [];
      result.suggestedRoute = "standard";
      result.rationale = `Full pipeline forced (original: ${originalRoute}). ${result.rationale}`;
    }
  }

  // Handle route override
  if (config.overrideRoute && config.overrideRoute !== result.suggestedRoute) {
    wasOverridden = true;
    originalRoute = originalRoute ?? result.suggestedRoute;

    // Adjust skip stages based on override
    result.suggestedRoute = config.overrideRoute;

    switch (config.overrideRoute) {
      case "trivial":
        // Skip both planning and validation (but keep pr-create/pr-merge)
        if (!result.skipStages.includes("feature-planning")) {
          result.skipStages.push("feature-planning");
        }
        if (!result.skipStages.includes("feature-validate")) {
          result.skipStages.push("feature-validate");
        }
        break;
      case "standard":
        // Only clear complexity-based skips, preserve task-type skips
        // For standard route, we want to run all stages that the task type allows
        result.skipStages = [];
        break;
      case "extensive":
        // No skipping, but use extended docs (handled elsewhere)
        result.skipStages = [];
        break;
    }

    result.rationale = `Route overridden to ${config.overrideRoute} (original: ${originalRoute}). ${result.rationale}`;
  }

  return { analysis: result, wasOverridden, originalRoute };
}

/**
 * Make a routing decision based on change analysis and config
 *
 * This is the main entry point for routing decisions.
 *
 * @param analysis - The change analysis result from ChangeAnalyzer
 * @param config - Routing configuration (optional)
 * @returns Complete routing decision
 */
export function makeRoutingDecision(
  analysis: ChangeAnalysis,
  config: RoutingConfig = DEFAULT_ROUTING_CONFIG
): RoutingDecision {
  // Apply config overrides
  const {
    analysis: effectiveAnalysis,
    wasOverridden,
    originalRoute,
  } = applyConfigOverrides(analysis, config);

  // Get stages to execute and skip
  const executeStages = getStagesToExecute(effectiveAnalysis, config);
  const skipStages = getStagesToSkip(effectiveAnalysis, config);

  // Generate explanation
  let explanation: string;
  if (wasOverridden) {
    explanation = `Using ${effectiveAnalysis.suggestedRoute} path (overridden from ${originalRoute}). `;
  } else {
    explanation = `Using ${effectiveAnalysis.suggestedRoute} path. `;
  }

  if (skipStages.length > 0) {
    explanation += `Skipping: ${skipStages.join(", ")}. `;
  } else {
    explanation += "Executing all stages. ";
  }

  explanation += `Estimated time: ~${effectiveAnalysis.estimatedTimeMinutes} minutes.`;

  return {
    route: effectiveAnalysis.suggestedRoute,
    skipStages,
    executeStages,
    explanation,
    wasOverridden,
    originalRoute,
  };
}

/**
 * Get the next stage to execute after a given stage
 *
 * Respects routing decisions to skip stages.
 *
 * @param currentStage - The current stage
 * @param decision - The routing decision
 * @returns The next stage to execute, or null if complete
 */
export function getNextStage(
  currentStage: PipelineStage,
  decision: RoutingDecision
): PipelineStage | null {
  const currentIndex = decision.executeStages.indexOf(currentStage);

  if (currentIndex === -1 || currentIndex === decision.executeStages.length - 1) {
    return null;
  }

  return decision.executeStages[currentIndex + 1];
}

/**
 * Get the previous stage before a given stage
 *
 * Respects routing decisions to skip stages.
 *
 * @param currentStage - The current stage
 * @param decision - The routing decision
 * @returns The previous stage, or null if at start
 */
export function getPreviousStage(
  currentStage: PipelineStage,
  decision: RoutingDecision
): PipelineStage | null {
  const currentIndex = decision.executeStages.indexOf(currentStage);

  if (currentIndex <= 0) {
    return null;
  }

  return decision.executeStages[currentIndex - 1];
}

/**
 * Check if a stage is the first stage in the routing
 *
 * @param stage - The stage to check
 * @param decision - The routing decision
 * @returns True if this is the first stage
 */
export function isFirstStage(stage: PipelineStage, decision: RoutingDecision): boolean {
  return decision.executeStages[0] === stage;
}

/**
 * Check if a stage is the last stage in the routing
 *
 * @param stage - The stage to check
 * @param decision - The routing decision
 * @returns True if this is the last stage
 */
export function isLastStage(stage: PipelineStage, decision: RoutingDecision): boolean {
  return decision.executeStages[decision.executeStages.length - 1] === stage;
}

/**
 * Get the stage index (0-based) in the execute list
 *
 * @param stage - The stage to check
 * @param decision - The routing decision
 * @returns Stage index, or -1 if not in execute list
 */
export function getStageIndex(stage: PipelineStage, decision: RoutingDecision): number {
  return decision.executeStages.indexOf(stage);
}

/**
 * Get progress information for display
 *
 * @param currentStage - The current stage
 * @param decision - The routing decision
 * @returns Progress object with current, total, and percent
 */
export function getProgressInfo(
  currentStage: PipelineStage,
  decision: RoutingDecision
): { current: number; total: number; percent: number } {
  const current = getStageIndex(currentStage, decision) + 1;
  const total = decision.executeStages.length;
  const percent = Math.round((current / total) * 100);

  return { current, total, percent };
}

/**
 * Format routing decision for logging/display
 *
 * @param decision - The routing decision
 * @returns Formatted string for display
 */
export function formatRoutingDecision(decision: RoutingDecision): string {
  const lines: string[] = [];

  lines.push(`Route: ${decision.route.toUpperCase()}`);
  lines.push(`Stages: ${decision.executeStages.length}`);

  if (decision.skipStages.length > 0) {
    lines.push(`Skipping: ${decision.skipStages.join(", ")}`);
  }

  if (decision.wasOverridden) {
    lines.push(`(Overridden from: ${decision.originalRoute})`);
  }

  return lines.join("\n");
}
