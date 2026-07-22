/**
 * WorkTimeFeedback - Pure functions for work-time tracking and feedback persistence
 *
 * This module provides deterministic work-time calculation and feedback loop integration
 * to enable self-improving complexity model accuracy.
 *
 * Key Principles:
 * - Pure functions (no side effects) for testability
 * - Atomic YAML writes (temp file + rename) for crash safety
 * - Only count completed stages (not failed/skipped)
 * - Rolling window of last 50 observations
 *
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 * @see Issue #310 - Add Actual Work Time Feedback Loop
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as yaml from "js-yaml";
import type { PipelineState } from "../services/PipelineStateService";
import type { PipelineStage } from "@nightgauge/sdk";
import type { SizeLabel, TaskType } from "./changeAnalyzer";

/**
 * Work-time observation captured after PR merge
 */
export interface WorkTimeObservation {
  issue_number: number;
  size: SizeLabel;
  priority: string | null;
  task_type: TaskType | null;
  actual_work_minutes: number;
  estimated_minutes: number;
  routing: string;
  stages_completed: PipelineStage[];
  timestamp: string;
}

/**
 * Size-specific average work time
 */
export interface SizeAverage {
  estimated: number;
  actual_average: number;
  observation_count: number;
}

/**
 * Work-time feedback section in complexity-model.yaml
 */
export interface WorkTimeFeedback {
  enabled: boolean;
  observations: WorkTimeObservation[];
  size_averages: Partial<Record<NonNullable<SizeLabel>, SizeAverage>>;
}

/**
 * Calculate actual work time from pipeline state
 *
 * Sums duration_ms for all stages with status === 'complete'.
 * Returns time in minutes (not milliseconds).
 *
 * Edge cases handled:
 * - No completed stages -> returns 0
 * - Missing duration_ms -> skips that stage
 * - Failed/skipped stages -> excluded from sum
 *
 * @param state - Pipeline state with stage durations
 * @returns Work time in minutes
 */
export function calculateWorkTime(state: PipelineState): number {
  const stages: PipelineStage[] = [
    "pipeline-start",
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
    "pipeline-finish",
  ];

  let totalMs = 0;

  for (const stage of stages) {
    const stageState = state.stages[stage];

    // Only count completed stages
    if (stageState.status !== "complete") {
      continue;
    }

    // Skip if duration_ms is missing or invalid
    if (typeof stageState.duration_ms !== "number" || stageState.duration_ms <= 0) {
      continue;
    }

    totalMs += stageState.duration_ms;
  }

  // Convert milliseconds to minutes
  return Math.round(totalMs / 60000);
}

/**
 * Get list of completed stages from pipeline state
 *
 * @param state - Pipeline state
 * @returns Array of stage names that completed successfully
 */
export function getCompletedStages(state: PipelineState): PipelineStage[] {
  const stages: PipelineStage[] = [
    "pipeline-start",
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
    "pipeline-finish",
  ];

  return stages.filter((stage) => state.stages[stage].status === "complete");
}

/**
 * Create work-time observation from pipeline state
 *
 * Extracts relevant fields from state and issue context to build an observation.
 * Does NOT write to YAML - call appendObservationToYAML() separately.
 *
 * @param state - Pipeline state with completed stages
 * @param issueContext - Additional context from issue-pickup (labels, routing, etc.)
 * @returns Observation object ready to append to YAML
 */
export function createObservation(
  state: PipelineState,
  issueContext: {
    size: SizeLabel;
    priority?: string | null;
    task_type?: TaskType | null;
    estimated_minutes?: number;
    routing?: string;
  }
): WorkTimeObservation {
  return {
    issue_number: state.issue_number,
    size: issueContext.size,
    priority: issueContext.priority ?? null,
    task_type: issueContext.task_type ?? null,
    actual_work_minutes: calculateWorkTime(state),
    estimated_minutes: issueContext.estimated_minutes ?? 0,
    routing: issueContext.routing ?? "unknown",
    stages_completed: getCompletedStages(state),
    timestamp: new Date().toISOString(),
  };
}

/**
 * Append observation to complexity-model.yaml with atomic write
 *
 * Reads existing YAML, appends observation, prunes to last 50, recalculates averages,
 * and writes atomically (temp file + rename) for crash safety.
 *
 * Creates file with default structure if it doesn't exist.
 *
 * @param observation - Observation to append
 * @param yamlPath - Path to complexity-model.yaml (absolute or relative to workspace)
 */
export async function appendObservationToYAML(
  observation: WorkTimeObservation,
  yamlPath: string
): Promise<void> {
  // Read existing or create default structure
  let feedback: WorkTimeFeedback;

  try {
    const content = await fs.readFile(yamlPath, "utf-8");
    const parsed = yaml.load(content) as Record<string, unknown>;

    feedback = (parsed.work_time_feedback as WorkTimeFeedback | undefined) ?? {
      enabled: true,
      observations: [],
      size_averages: {},
    };
  } catch (error: unknown) {
    // File doesn't exist or is corrupted - create default
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      feedback = {
        enabled: true,
        observations: [],
        size_averages: {},
      };
    } else {
      throw new Error(
        `Failed to read complexity-model.yaml: ${error instanceof Error ? error.message : String(error)}`,
        { cause: error }
      );
    }
  }

  // Append observation
  feedback.observations.push(observation);

  // Prune to last 50 observations
  feedback.observations = pruneOldObservations(feedback.observations, 50);

  // Recalculate size averages
  feedback.size_averages = calculateSizeAverages(feedback.observations);

  // Atomic write: temp file + rename
  const tempPath = `${yamlPath}.tmp`;
  const yamlContent = yaml.dump(
    { work_time_feedback: feedback },
    {
      indent: 2,
      lineWidth: -1, // No line wrapping
    }
  );

  await fs.writeFile(tempPath, yamlContent, "utf-8");
  await fs.rename(tempPath, yamlPath);
}

/**
 * Prune observations to keep only the last N entries
 *
 * @param observations - Array of observations
 * @param maxCount - Maximum number to keep (default: 50)
 * @returns Pruned observations (last N entries)
 */
export function pruneOldObservations(
  observations: WorkTimeObservation[],
  maxCount: number = 50
): WorkTimeObservation[] {
  if (observations.length <= maxCount) {
    return observations;
  }

  // Keep last N observations (most recent)
  return observations.slice(-maxCount);
}

/**
 * Calculate average work time per size from observations
 *
 * Computes actual_average_minutes for each size label based on all observations
 * with that size. Excludes observations with zero work time (likely errors).
 *
 * Edge cases handled:
 * - No observations for size -> not included in result
 * - Division by zero -> prevented by checking observation_count
 * - Null size in observation -> skipped
 *
 * @param observations - Array of observations to analyze
 * @returns Size averages with observation counts
 */
export function calculateSizeAverages(
  observations: WorkTimeObservation[]
): Partial<Record<NonNullable<SizeLabel>, SizeAverage>> {
  const sizeGroups: Partial<Record<NonNullable<SizeLabel>, WorkTimeObservation[]>> = {};

  // Group observations by size
  for (const obs of observations) {
    if (!obs.size) {
      continue; // Skip observations with null size
    }

    if (!sizeGroups[obs.size]) {
      sizeGroups[obs.size] = [];
    }

    // Only include observations with non-zero work time
    if (obs.actual_work_minutes > 0) {
      sizeGroups[obs.size]!.push(obs);
    }
  }

  // Calculate averages per size
  const averages: Partial<Record<NonNullable<SizeLabel>, SizeAverage>> = {};

  for (const [size, group] of Object.entries(sizeGroups)) {
    if (group.length === 0) {
      continue; // No valid observations for this size
    }

    const totalActual = group.reduce((sum, obs) => sum + obs.actual_work_minutes, 0);
    const totalEstimated = group.reduce((sum, obs) => sum + obs.estimated_minutes, 0);

    averages[size as NonNullable<SizeLabel>] = {
      estimated: Math.round(totalEstimated / group.length),
      actual_average: Math.round(totalActual / group.length),
      observation_count: group.length,
    };
  }

  return averages;
}

/**
 * Read work-time feedback from complexity-model.yaml
 *
 * Returns null if file doesn't exist or work_time_feedback section is missing.
 * Gracefully handles corrupted YAML (logs error, returns null).
 *
 * @param yamlPath - Path to complexity-model.yaml
 * @returns Work-time feedback data or null
 */
export async function readWorkTimeFeedback(yamlPath: string): Promise<WorkTimeFeedback | null> {
  try {
    const content = await fs.readFile(yamlPath, "utf-8");
    const parsed = yaml.load(content) as Record<string, unknown>;

    return (parsed.work_time_feedback as WorkTimeFeedback | undefined) ?? null;
  } catch (error: unknown) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return null; // File doesn't exist
    }

    // Corrupted YAML - log error and return null
    console.error(
      `Failed to read complexity-model.yaml: ${error instanceof Error ? error.message : String(error)}`
    );
    return null;
  }
}
