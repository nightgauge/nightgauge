/**
 * PrMergeRunner — Stage runner for the pr-merge pipeline stage
 *
 * Validates that the pr-create context file exists before delegating
 * to ctx.executeSkill(). No budget size adjustment is applied.
 *
 * pr-merge is a bookend stage (may be deferred) but still validates
 * its prerequisite context file when it does run.
 *
 * @see Issue #2769 — HeadlessOrchestrator decomposition (Part 2)
 */

import * as fs from "fs";
import { StageRunner } from "./StageRunner";
import type { StageRunContext, StageRunResult } from "./StageRunner";

/**
 * Stage runner for the pr-merge stage.
 *
 * Responsibilities:
 * - Validate prerequisite: pr-{N}.json must exist (written by pr-create)
 * - No budget size adjustment (pr-merge uses the default issue size label)
 * - Delegate to ctx.executeSkill() after prerequisite passes
 */
export class PrMergeRunner extends StageRunner {
  async run(ctx: StageRunContext): Promise<StageRunResult> {
    const startTime = Date.now();

    // Validate prerequisite: pr-create must have written pr-{N}.json
    const prereqPath = ctx.getContextPath("pr", ctx.issueNumber);
    if (!fs.existsSync(prereqPath)) {
      const message =
        `Cannot start pr-merge: required input file ${prereqPath} is missing. ` +
        `pr-create must complete and write this file before pr-merge can proceed.`;
      ctx.logger?.error("pr-merge pre-condition failed: prerequisite context file missing", {
        stage: "pr-merge",
        prerequisiteStage: "pr-create",
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

    // pr-merge uses the default issue size label — no budget adjustment needed.
    return ctx.executeSkill({});
  }
}
