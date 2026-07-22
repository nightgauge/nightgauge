/**
 * FeatureDevStageRunner — Stage runner for the feature-dev pipeline stage
 *
 * Validates that the feature-planning context file exists, reads planning hints
 * to compute an adjusted budget size label, and delegates to ctx.executeSkill().
 *
 * This runner extracts the POST_PLANNING_STAGES budget-hint logic that previously
 * lived inside HeadlessOrchestrator.runStage(). By moving it here, the budget
 * adjustment is stage-local rather than scattered through a 1200-line method.
 *
 * @see Issue #2768 — HeadlessOrchestrator decomposition (Part 1)
 * @see Issue #1333 - Planning-aware budget enforcement
 */

import * as fs from "fs";
import {
  resolveEffectiveSize,
  type PlanningBudgetHint,
  type SizeLabel,
} from "../../utils/budgetEnforcer";
import { StageRunner } from "./StageRunner";
import type { StageRunContext, StageRunResult } from "./StageRunner";

/**
 * Stage runner for the feature-dev stage.
 *
 * Responsibilities:
 * - Validate prerequisite: planning-{N}.json must exist (written by feature-planning)
 * - Read planning context hints (complexity assessment, file counts)
 * - Compute adjusted budget size label (may be higher than issue label)
 * - Update state service meta with planning complexity data
 * - Delegate to ctx.executeSkill({ sizeLabel }) with the adjusted size
 */
export class FeatureDevStageRunner extends StageRunner {
  async run(ctx: StageRunContext): Promise<StageRunResult> {
    const startTime = Date.now();

    // Validate prerequisite: feature-planning must have written planning-{N}.json
    const prereqPath = ctx.getContextPath("planning", ctx.issueNumber);
    if (!fs.existsSync(prereqPath)) {
      const message =
        `Cannot start feature-dev: required input file ${prereqPath} is missing. ` +
        `feature-planning must complete and write this file before feature-dev can proceed.`;
      ctx.logger?.error("feature-dev pre-condition failed: prerequisite context file missing", {
        stage: "feature-dev",
        prerequisiteStage: "feature-planning",
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

    // Read planning hints and apply budget size adjustment.
    // @see Issue #1333 - Planning-aware budget enforcement
    const planningHint = this.readPlanningHint(ctx);
    let sizeLabel: SizeLabel = ctx.issueSizeLabel;

    if (planningHint) {
      const adjustedSize = resolveEffectiveSize(ctx.issueSizeLabel, planningHint);
      if (adjustedSize !== ctx.issueSizeLabel) {
        ctx.logger?.info("Budget size adjusted from planning context", {
          stage: ctx.stage,
          issueNumber: ctx.issueNumber,
          issueSizeLabel: ctx.issueSizeLabel,
          effectiveSizeLabel: adjustedSize,
          assessedSize: planningHint.assessedSize,
          totalFileCount: planningHint.totalFileCount,
        });
      }
      // Enrich pipeline state with planning metrics for Discord/UI
      ctx.stateService?.setMeta({
        complexity: planningHint.assessedSize ?? ctx.issueSizeLabel,
        file_count: planningHint.totalFileCount,
      });
      sizeLabel = adjustedSize;
    }

    return ctx.executeSkill({ sizeLabel });
  }

  /**
   * Read planning budget hints from the planning context file.
   *
   * Extracts:
   * - complexity_assessment.size_label — planner's assessed complexity size
   * - files_to_create + files_to_modify counts — for file-count-based adjustment
   *
   * Returns null on any read/parse error (non-fatal — budget falls back to issue size).
   */
  private readPlanningHint(ctx: StageRunContext): PlanningBudgetHint | null {
    try {
      const planningPath = ctx.getContextPath("planning", ctx.issueNumber);
      if (!fs.existsSync(planningPath)) {
        return null;
      }

      const content = fs.readFileSync(planningPath, "utf-8");
      const data = JSON.parse(content) as Record<string, unknown>;

      const hint: PlanningBudgetHint = {};

      const assessedSize = (data.complexity_assessment as Record<string, unknown> | undefined)
        ?.size_label;
      if (typeof assessedSize === "string" && ["XS", "S", "M", "L", "XL"].includes(assessedSize)) {
        hint.assessedSize = assessedSize as SizeLabel;
      }

      const filesToCreate = Array.isArray(data.files_to_create) ? data.files_to_create.length : 0;
      const filesToModify = Array.isArray(data.files_to_modify) ? data.files_to_modify.length : 0;
      hint.totalFileCount = filesToCreate + filesToModify;

      return hint;
    } catch {
      ctx.logger?.warn("Failed to read planning context for budget hint", {
        issueNumber: ctx.issueNumber,
      });
      return null;
    }
  }
}
