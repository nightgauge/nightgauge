/**
 * Pipeline Stage Exports
 *
 * This module exports the stage classes that wrap SKILL.md prompts
 * for programmatic pipeline execution.
 */

// Base stage
export {
  BaseStage,
  type StageConfig,
  type StageExecuteOptions,
  type StageExecuteResult,
} from "./base.js";

// Concrete stages
export { IssuePickupStage } from "./IssuePickupStage.js";
export { FeaturePlanningStage } from "./FeaturePlanningStage.js";
export { FeatureDevStage } from "./FeatureDevStage.js";
export { PRCreateStage } from "./PRCreateStage.js";
