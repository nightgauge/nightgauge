/**
 * PrCreateRunner — Stage runner for the pr-create pipeline stage
 *
 * Validates that the feature-validate context file exists before delegating
 * to ctx.executeSkill(). No budget size adjustment is applied.
 *
 * Note: HeadlessOrchestrator contains validatePrCreateState() for state
 * reconciliation (recovering from misnamed context files). That logic is
 * NOT duplicated here — this runner only validates the prerequisite.
 *
 * @see Issue #2769 — HeadlessOrchestrator decomposition (Part 2)
 * @see Issue #1608 — pr-create depends on feature-validate, not feature-dev
 */

import * as fs from "fs";
import { StageRunner } from "./StageRunner";
import type { StageRunContext, StageRunResult } from "./StageRunner";

/**
 * Stage runner for the pr-create stage.
 *
 * Responsibilities:
 * - Validate prerequisite: validate-{N}.json must exist (written by feature-validate)
 * - No budget size adjustment (pr-create uses the default issue size label)
 * - Delegate to ctx.executeSkill() after prerequisite passes
 */
export class PrCreateRunner extends StageRunner {
  async run(ctx: StageRunContext): Promise<StageRunResult> {
    const startTime = Date.now();

    // Validate prerequisite: feature-validate must have written validate-{N}.json
    const prereqPath = ctx.getContextPath("validate", ctx.issueNumber);
    if (!fs.existsSync(prereqPath)) {
      const message =
        `Cannot start pr-create: required input file ${prereqPath} is missing. ` +
        `feature-validate must complete and write this file before pr-create can proceed.`;
      ctx.logger?.error("pr-create pre-condition failed: prerequisite context file missing", {
        stage: "pr-create",
        prerequisiteStage: "feature-validate",
        expectedPath: prereqPath,
        issueNumber: ctx.issueNumber,
      });
      return {
        success: false,
        stage: ctx.stage,
        durationMs: Date.now() - startTime,
        error: new Error(message),
      };
    }

    // pr-create uses the default issue size label — no budget adjustment needed.
    return ctx.executeSkill({});
  }
}
