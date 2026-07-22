/**
 * StageRunnerRegistry — Maps pipeline stages to their StageRunner implementations
 *
 * Provides runtime selection of the correct runner for each pipeline stage.
 * All six pipeline stages have registered runners — no fallback is needed.
 *
 * Registered runners:
 *   issue-pickup       → IssuePickupStageRunner      (Part 1 — Issue #2768)
 *   feature-planning   → FeaturePlanningStageRunner   (Part 1 — Issue #2768)
 *   feature-dev        → FeatureDevStageRunner        (Part 1 — Issue #2768)
 *   feature-validate   → FeatureValidateRunner        (Part 2 — Issue #2769)
 *   pr-create          → PrCreateRunner               (Part 2 — Issue #2769)
 *   pr-merge           → PrMergeRunner                (Part 2 — Issue #2769)
 *
 * @see Issue #2768 — HeadlessOrchestrator decomposition (Part 1)
 * @see Issue #2769 — HeadlessOrchestrator decomposition (Part 2)
 */

import type { PipelineStage } from "@nightgauge/sdk";
import { StageRunner } from "./StageRunner";
import { IssuePickupStageRunner } from "./IssuePickupStageRunner";
import { FeaturePlanningStageRunner } from "./FeaturePlanningStageRunner";
import { FeatureDevStageRunner } from "./FeatureDevStageRunner";
import { FeatureValidateRunner } from "./FeatureValidateRunner";
import { PrCreateRunner } from "./PrCreateRunner";
import { PrMergeRunner } from "./PrMergeRunner";

/**
 * Registry that maps pipeline stage names to their StageRunner instances.
 *
 * Uses a static singleton registry for zero runtime allocation.
 */
export class StageRunnerRegistry {
  private static readonly registry = new Map<string, StageRunner>([
    ["issue-pickup", new IssuePickupStageRunner()],
    ["feature-planning", new FeaturePlanningStageRunner()],
    ["feature-dev", new FeatureDevStageRunner()],
    ["feature-validate", new FeatureValidateRunner()],
    ["pr-create", new PrCreateRunner()],
    ["pr-merge", new PrMergeRunner()],
  ]);

  /**
   * Get the registered runner for a pipeline stage.
   *
   * @param stage - The pipeline stage name
   * @throws {Error} If no runner is registered for the given stage
   */
  static getRunner(stage: PipelineStage): StageRunner {
    const runner = StageRunnerRegistry.registry.get(stage);
    if (!runner) {
      throw new Error(`No runner registered for stage: ${stage}`);
    }
    return runner;
  }
}
