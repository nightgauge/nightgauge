/**
 * buildHealthInput - Maps AggregatedDataset to HealthAnalysisInput
 *
 * Pure function that converts the VSCode aggregated dataset to the SDK-native
 * HealthAnalysisInput type, without importing from any service class.
 *
 * The executionHistory mapping is the SDK's `flattenRunRecords`: one flat
 * record per executed stage, carrying that stage's `model` and
 * `selectionSource`. `PipelineHealthRunner` and `PostPipelineAnalyzer` feed
 * the same mapper — there is exactly one run-record → analyzer-record path.
 *
 * @see Issue #1570 - Connect real HealthAnalysisResult to health-gated policies
 * @see Issue #461 - per-stage feeder makes the model-routing dimension reachable
 * @see packages/nightgauge-sdk/src/analysis/health/executionHistoryFeeder.ts
 */

import type { AggregatedDataset } from "../types/aggregation";
import type {
  HealthAnalysisInput,
  HealthScoreEntry,
  SkillEffectivenessEntry,
} from "@nightgauge/sdk";
import type { ExecutionHistoryRecord as SdkExecutionHistoryRecord } from "@nightgauge/sdk";
import { flattenRunRecords } from "@nightgauge/sdk";

/**
 * Map an AggregatedDataset to a HealthAnalysisInput for use by HealthAnalysisEngine.
 *
 * Each source collection is mapped field-by-field to the SDK's types.
 * Missing or empty collections produce empty arrays — the engine handles
 * insufficient-data gracefully.
 *
 * @param dataset - Aggregated telemetry dataset from DataAggregator
 * @returns HealthAnalysisInput ready to pass to HealthAnalysisEngine.analyze()
 */
export function buildHealthInput(dataset: AggregatedDataset): HealthAnalysisInput {
  // One SDK record per EXECUTED STAGE, with `model` / `selectionSource`
  // copied from the stage's `model_selection` block. Routing is a per-stage
  // fact, so the model-routing dimension is only reachable from this shape;
  // the per-run mapper this replaced left `selectionSource` unset (#461).
  const executionHistory: SdkExecutionHistoryRecord[] = flattenRunRecords(dataset.executionHistory);

  // Map HealthScoreSnapshot → HealthScoreEntry (fields align directly)
  const healthScores: HealthScoreEntry[] = dataset.healthScores.map((s) => ({
    timestamp: s.timestamp,
    score: s.score,
    status: s.status,
    components: s.components,
    costUsd: s.costUsd,
    issueNumber: s.issueNumber,
  }));

  // Learning-effectiveness and skill-drift inputs (#1900). `null` (file
  // absent) stays absent so the dimension names the missing input.
  const skillEffectiveness: SkillEffectivenessEntry[] | undefined =
    dataset.skillEffectiveness?.map((r) => ({
      skillFile: r.skill_file,
      stage: r.stage,
      commitHash: r.commit_hash,
      changedAt: r.changed_at,
      beforeWindow: { sampleCount: r.before_sample_count, successRate: r.before_success_rate },
      afterWindow: { sampleCount: r.after_sample_count, successRate: r.after_success_rate },
      delta: r.delta,
      classification: r.classification,
      confidence: r.confidence,
      analyzedAt: r.analyzed_at,
    })) ?? undefined;

  return {
    executionHistory,
    healthScores,
    learningSignals: dataset.learningSignals ?? undefined,
    skillEffectiveness,
  };
}
