/**
 * FeaturePlanningStageRunner — Stage runner for the feature-planning pipeline stage
 *
 * Validates that the issue-pickup context file exists before delegating
 * to ctx.executeSkill(). No budget size adjustment is applied (feature-planning
 * is not in the POST_PLANNING_STAGES set).
 *
 * @see Issue #2768 — HeadlessOrchestrator decomposition (Part 1)
 * @see Issue #1333 - Planning-aware budget enforcement
 */

import * as fs from "fs";
import { StageRunner } from "./StageRunner";
import type { StageRunContext, StageRunResult } from "./StageRunner";

/**
 * Stage runner for the feature-planning stage.
 *
 * Responsibilities:
 * - Validate prerequisite: issue-{N}.json must exist (written by issue-pickup)
 * - No budget size adjustment (feature-planning uses the default issue size label)
 * - Delegate to ctx.executeSkill() after prerequisite passes
 */
export class FeaturePlanningStageRunner extends StageRunner {
  async run(ctx: StageRunContext): Promise<StageRunResult> {
    const startTime = Date.now();

    // Validate prerequisite: issue-pickup must have written issue-{N}.json
    const prereqPath = ctx.getContextPath("issue", ctx.issueNumber);
    if (!fs.existsSync(prereqPath)) {
      const message =
        `Cannot start feature-planning: required input file ${prereqPath} is missing. ` +
        `issue-pickup must complete and write this file before feature-planning can proceed.`;
      ctx.logger?.error(
        "feature-planning pre-condition failed: prerequisite context file missing",
        {
          stage: "feature-planning",
          prerequisiteStage: "issue-pickup",
          expectedPath: prereqPath,
          issueNumber: ctx.issueNumber,
        }
      );
      return {
        success: false,
        stage: ctx.stage,
        durationMs: Date.now() - startTime,
        error: new Error(message),
      };
    }

    // feature-planning is not in POST_PLANNING_STAGES — no budget adjustment needed.
    return ctx.executeSkill({});
  }
}
