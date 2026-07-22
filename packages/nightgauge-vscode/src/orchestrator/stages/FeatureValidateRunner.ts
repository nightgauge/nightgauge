/**
 * FeatureValidateRunner — Stage runner for the feature-validate pipeline stage
 *
 * Validates that the feature-dev context file exists before delegating
 * to ctx.executeSkill(). No budget size adjustment is applied.
 *
 * @see Issue #2769 — HeadlessOrchestrator decomposition (Part 2)
 * @see Issue #2768 — Part 1 (established the StageRunner pattern)
 */

import * as fs from "fs";
import { StageRunner } from "./StageRunner";
import type { StageRunContext, StageRunResult } from "./StageRunner";

/**
 * Stage runner for the feature-validate stage.
 *
 * Responsibilities:
 * - Validate prerequisite: dev-{N}.json must exist (written by feature-dev)
 * - No budget size adjustment (feature-validate uses the default issue size label)
 * - Delegate to ctx.executeSkill() after prerequisite passes
 */
export class FeatureValidateRunner extends StageRunner {
  async run(ctx: StageRunContext): Promise<StageRunResult> {
    const startTime = Date.now();

    // Validate prerequisite: feature-dev must have written dev-{N}.json
    const prereqPath = ctx.getContextPath("dev", ctx.issueNumber);
    if (!fs.existsSync(prereqPath)) {
      const message =
        `Cannot start feature-validate: required input file ${prereqPath} is missing. ` +
        `feature-dev must complete and write this file before feature-validate can proceed.`;
      ctx.logger?.error(
        "feature-validate pre-condition failed: prerequisite context file missing",
        {
          stage: "feature-validate",
          prerequisiteStage: "feature-dev",
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

    // feature-validate uses the default issue size label — no budget adjustment needed.
    return ctx.executeSkill({});
  }
}
