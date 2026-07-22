/**
 * ExperimentEvaluator - Automatic A/B experiment evaluation and conclusion
 *
 * Pure SDK class that evaluates whether an experiment has accumulated enough
 * data to conclude and whether the treatment succeeded or failed.
 *
 * Design principles:
 * - `evaluate()` is a pure function — deterministic, no I/O, no side effects
 * - `readConclusion()` / `writeConclusion()` encapsulate all file I/O
 * - Idempotent: conclusion file acts as guard against double-evaluation
 *
 * Success criteria (per Issue #1396 spec):
 * - Both groups must have >= observation_window runs
 * - treatment.success_rate > 0.6 (absolute floor)
 * - success_rate_delta >= min_effect_size
 *
 * @see Issue #1396 - Automatic A/B experiment evaluation and rollback
 * @see ExperimentManager for outcome recording and assignment
 */

import * as fs from "fs";
import * as path from "path";
import type {
  ExperimentConfig,
  ExperimentOutcome,
  ExperimentEvaluationResult,
  ExperimentConclusion,
} from "./experiment-types.js";

const EXPERIMENTS_DIR = ".nightgauge/analysis/experiments";

export class ExperimentEvaluator {
  /**
   * Pure evaluation — no side effects.
   *
   * Determines whether an experiment has enough data and whether the treatment
   * succeeded or failed.
   *
   * Evaluation flow:
   * 1. If `alreadyConcluded` → return `already_concluded`
   * 2. Split outcomes by group; if either group < `observation_window` → `deferred`
   * 3. Compute `success_rate_delta` and `treatment_success_rate`
   * 4. If `success_rate_delta >= min_effect_size` AND `treatment_success_rate > 0.6` → `graduated`
   * 5. Else → `rolled_back`
   */
  static evaluate(
    outcomes: ExperimentOutcome[],
    config: ExperimentConfig,
    alreadyConcluded: boolean
  ): ExperimentEvaluationResult {
    const observationWindow = config.observation_window ?? 10;
    const minEffectSize = config.min_effect_size ?? 0.05;

    if (alreadyConcluded) {
      return {
        status: "already_concluded",
        experiment_name: config.name,
        control_runs: 0,
        treatment_runs: 0,
        success_rate_delta: 0,
        treatment_success_rate: 0,
        min_effect_size: minEffectSize,
        observation_window: observationWindow,
        rationale: "Experiment has already been concluded (conclusion file exists).",
        recommendation: "No action required. Experiment is closed.",
      };
    }

    const controlOutcomes = outcomes.filter((o) => o.group === "control");
    const treatmentOutcomes = outcomes.filter((o) => o.group === "treatment");
    const controlRuns = controlOutcomes.length;
    const treatmentRuns = treatmentOutcomes.length;

    if (controlRuns < observationWindow || treatmentRuns < observationWindow) {
      const controlNeeded = Math.max(observationWindow - controlRuns, 0);
      const treatmentNeeded = Math.max(observationWindow - treatmentRuns, 0);
      return {
        status: "deferred",
        experiment_name: config.name,
        control_runs: controlRuns,
        treatment_runs: treatmentRuns,
        success_rate_delta: 0,
        treatment_success_rate: 0,
        min_effect_size: minEffectSize,
        observation_window: observationWindow,
        rationale:
          `Insufficient data: control has ${controlRuns} run(s), treatment has ${treatmentRuns} run(s). ` +
          `Minimum required: ${observationWindow} per group.`,
        recommendation:
          `Continue running the experiment. ` +
          `${controlNeeded > 0 ? `${controlNeeded} more control run(s)` : "control is ready"}` +
          ` and ` +
          `${treatmentNeeded > 0 ? `${treatmentNeeded} more treatment run(s)` : "treatment is ready"}` +
          ` needed.`,
      };
    }

    const controlSuccesses = controlOutcomes.filter((o) => o.success).length;
    const treatmentSuccesses = treatmentOutcomes.filter((o) => o.success).length;

    const controlSuccessRate = controlSuccesses / controlRuns;
    const treatmentSuccessRate = treatmentSuccesses / treatmentRuns;
    const successRateDelta = treatmentSuccessRate - controlSuccessRate;

    const graduated = successRateDelta >= minEffectSize && treatmentSuccessRate > 0.6;

    if (graduated) {
      return {
        status: "graduated",
        experiment_name: config.name,
        control_runs: controlRuns,
        treatment_runs: treatmentRuns,
        success_rate_delta: round3(successRateDelta),
        treatment_success_rate: round3(treatmentSuccessRate),
        min_effect_size: minEffectSize,
        observation_window: observationWindow,
        rationale:
          `Treatment succeeded: success_rate_delta=${successRateDelta.toFixed(3)} >= min_effect_size=${minEffectSize}, ` +
          `treatment_success_rate=${treatmentSuccessRate.toFixed(3)} > 0.6.`,
        recommendation:
          "Graduated: set model_routing.experiment.active to false. " +
          "Consider updating complexity thresholds to adopt the treatment model configuration (operator action).",
      };
    }

    return {
      status: "rolled_back",
      experiment_name: config.name,
      control_runs: controlRuns,
      treatment_runs: treatmentRuns,
      success_rate_delta: round3(successRateDelta),
      treatment_success_rate: round3(treatmentSuccessRate),
      min_effect_size: minEffectSize,
      observation_window: observationWindow,
      rationale:
        `Treatment did not meet success criteria: ` +
        `success_rate_delta=${successRateDelta.toFixed(3)} (required >= ${minEffectSize}) or ` +
        `treatment_success_rate=${treatmentSuccessRate.toFixed(3)} (required > 0.6).`,
      recommendation:
        "Rolled back: set model_routing.experiment.active to false and retain the control model configuration.",
    };
  }

  /**
   * Read the experiment conclusion file to detect already-concluded state.
   *
   * Returns null if no conclusion file exists (first-run or never concluded).
   * File path: `.nightgauge/analysis/experiments/{experimentName}-conclusion.json`
   */
  static readConclusion(
    workspaceRoot: string,
    experimentName: string
  ): ExperimentConclusion | null {
    const filePath = path.join(workspaceRoot, EXPERIMENTS_DIR, `${experimentName}-conclusion.json`);
    try {
      const content = fs.readFileSync(filePath, "utf-8");
      return JSON.parse(content) as ExperimentConclusion;
    } catch {
      return null;
    }
  }

  /**
   * Write the experiment conclusion state to disk.
   *
   * Creates the experiments directory if needed. This file acts as an
   * idempotency guard: once written, future evaluations return `already_concluded`.
   *
   * File path: `.nightgauge/analysis/experiments/{name}-conclusion.json`
   */
  static writeConclusion(workspaceRoot: string, conclusion: ExperimentConclusion): void {
    const dir = path.join(workspaceRoot, EXPERIMENTS_DIR);
    fs.mkdirSync(dir, { recursive: true });
    const filePath = path.join(dir, `${conclusion.experiment_name}-conclusion.json`);
    fs.writeFileSync(filePath, JSON.stringify(conclusion, null, 2), "utf-8");
  }
}

// ── Helpers ─────────────────────────────────────────────────────

function round3(value: number): number {
  return Math.round(value * 1000) / 1000;
}
