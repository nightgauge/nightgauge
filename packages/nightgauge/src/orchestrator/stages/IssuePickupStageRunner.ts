/**
 * IssuePickupStageRunner — Stage runner for the issue-pickup pipeline stage
 *
 * Issue-pickup is the first pipeline stage and has no prerequisites.
 * This runner delegates directly to ctx.executeSkill() with no adjustments.
 *
 * @see Issue #2768 — HeadlessOrchestrator decomposition (Part 1)
 */

import { StageRunner } from "./StageRunner";
import type { StageRunContext, StageRunResult } from "./StageRunner";

/**
 * Stage runner for the issue-pickup stage.
 *
 * Responsibilities:
 * - No prerequisite validation (issue-pickup is the first skill stage)
 * - No budget size adjustment (uses default issue size label)
 * - Delegates fully to ctx.executeSkill()
 */
export class IssuePickupStageRunner extends StageRunner {
  async run(ctx: StageRunContext): Promise<StageRunResult> {
    // issue-pickup is the first skill stage — no prerequisites to validate.
    // Budget size uses the default issue size label (no planning context available yet).
    return ctx.executeSkill({});
  }
}
