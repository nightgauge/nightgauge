/**
 * ExperimentManager - A/B testing for model routing decisions
 *
 * Provides deterministic experiment assignment, JSONL outcome recording,
 * and report generation for comparing model configurations side-by-side.
 *
 * Assignment is deterministic: `issueNumber % 100 < splitPercent` → treatment.
 * This ensures the same issue always gets the same group across retries.
 *
 * Results persist as JSONL in `.nightgauge/analysis/experiments/`.
 *
 * @see Issue #949 - A/B Testing Framework for Model Routing Decisions
 */

import * as fs from "fs";
import * as path from "path";
import type {
  ExperimentConfig,
  ExperimentAssignment,
  ExperimentOutcome,
  ExperimentReport,
  ExperimentGroup,
  GroupMetrics,
} from "./experiment-types.js";

const EXPERIMENTS_DIR = ".nightgauge/analysis/experiments";

export class ExperimentManager {
  /**
   * Assign an issue to a control or treatment group.
   *
   * Returns null if no active experiment or stage not targeted.
   * Assignment is deterministic: `issueNumber % 100 < splitPercent` → treatment.
   */
  static assign(
    issueNumber: number,
    stage: string,
    config: ExperimentConfig
  ): ExperimentAssignment | null {
    if (!config.active) {
      return null;
    }

    if (
      config.target_stages &&
      config.target_stages.length > 0 &&
      !config.target_stages.includes(stage)
    ) {
      return null;
    }

    const group: ExperimentGroup =
      issueNumber % 100 < config.split_percent ? "treatment" : "control";
    const variant = group === "treatment" ? config.treatment : config.control;

    return {
      experiment_name: config.name,
      group,
      model: variant.model,
      effort: variant.effort,
      issue_number: issueNumber,
      stage,
      assigned_at: new Date().toISOString(),
    };
  }

  /**
   * Record an experiment outcome to JSONL.
   *
   * Appends to `.nightgauge/analysis/experiments/{experiment_name}.jsonl`.
   * Creates directory if needed. Failures are logged as warnings (non-blocking).
   */
  static recordOutcome(workspaceRoot: string, outcome: ExperimentOutcome): void {
    try {
      const dir = path.join(workspaceRoot, EXPERIMENTS_DIR);
      fs.mkdirSync(dir, { recursive: true });

      const filePath = path.join(dir, `${outcome.experiment_name}.jsonl`);
      fs.appendFileSync(filePath, JSON.stringify(outcome) + "\n", "utf-8");
    } catch (error) {
      console.warn(
        `[ExperimentManager] Failed to record outcome: ${error instanceof Error ? error.message : String(error)}`
      );
    }
  }

  /**
   * Generate a comparison report from recorded outcomes.
   *
   * Reads all outcomes from JSONL, computes per-group metrics,
   * and generates a human-readable recommendation.
   */
  static generateReport(
    workspaceRoot: string,
    experimentName: string,
    minRuns?: number
  ): ExperimentReport {
    const outcomes = ExperimentManager.readOutcomes(workspaceRoot, experimentName);

    const controlOutcomes = outcomes.filter((o) => o.group === "control");
    const treatmentOutcomes = outcomes.filter((o) => o.group === "treatment");

    const controlMetrics = ExperimentManager.computeGroupMetrics(controlOutcomes);
    const treatmentMetrics = ExperimentManager.computeGroupMetrics(treatmentOutcomes);

    const effectiveMinRuns = minRuns ?? 20;
    const sufficientData =
      controlMetrics.runs >= effectiveMinRuns && treatmentMetrics.runs >= effectiveMinRuns;

    const costSavingsPercent =
      controlMetrics.avg_cost_usd > 0
        ? ((controlMetrics.avg_cost_usd - treatmentMetrics.avg_cost_usd) /
            controlMetrics.avg_cost_usd) *
          100
        : 0;

    const successRateDelta = treatmentMetrics.success_rate - controlMetrics.success_rate;

    const durationDeltaPercent =
      controlMetrics.avg_duration_ms > 0
        ? ((controlMetrics.avg_duration_ms - treatmentMetrics.avg_duration_ms) /
            controlMetrics.avg_duration_ms) *
          100
        : 0;

    const recommendation = ExperimentManager.generateRecommendation(
      controlMetrics,
      treatmentMetrics,
      sufficientData,
      controlOutcomes[0]?.model ?? "unknown",
      treatmentOutcomes[0]?.model ?? "unknown"
    );

    return {
      experiment_name: experimentName,
      total_runs: {
        control: controlMetrics.runs,
        treatment: treatmentMetrics.runs,
      },
      metrics: {
        control: controlMetrics,
        treatment: treatmentMetrics,
      },
      comparison: {
        cost_savings_percent: Math.round(costSavingsPercent * 100) / 100,
        success_rate_delta: Math.round(successRateDelta * 1000) / 1000,
        duration_delta_percent: Math.round(durationDeltaPercent * 100) / 100,
        recommendation,
      },
      sufficient_data: sufficientData,
      generated_at: new Date().toISOString(),
    };
  }

  /**
   * Read outcomes from a JSONL experiment file.
   */
  static readOutcomes(workspaceRoot: string, experimentName: string): ExperimentOutcome[] {
    const filePath = path.join(workspaceRoot, EXPERIMENTS_DIR, `${experimentName}.jsonl`);

    try {
      const content = fs.readFileSync(filePath, "utf-8");
      return content
        .split("\n")
        .filter((line) => line.trim().length > 0)
        .map((line) => JSON.parse(line) as ExperimentOutcome);
    } catch {
      return [];
    }
  }

  private static computeGroupMetrics(outcomes: ExperimentOutcome[]): GroupMetrics {
    const runs = outcomes.length;
    if (runs === 0) {
      return {
        runs: 0,
        successes: 0,
        failures: 0,
        success_rate: 0,
        avg_cost_usd: 0,
        avg_duration_ms: 0,
        avg_retry_count: 0,
        total_cost_usd: 0,
      };
    }

    const successes = outcomes.filter((o) => o.success).length;
    const totalCost = outcomes.reduce((sum, o) => sum + o.cost_usd, 0);
    const totalDuration = outcomes.reduce((sum, o) => sum + o.duration_ms, 0);
    const totalRetries = outcomes.reduce((sum, o) => sum + o.retry_count, 0);

    return {
      runs,
      successes,
      failures: runs - successes,
      success_rate: successes / runs,
      avg_cost_usd: totalCost / runs,
      avg_duration_ms: totalDuration / runs,
      avg_retry_count: totalRetries / runs,
      total_cost_usd: totalCost,
    };
  }

  private static generateRecommendation(
    control: GroupMetrics,
    treatment: GroupMetrics,
    sufficientData: boolean,
    controlModel: string,
    treatmentModel: string
  ): string {
    if (!sufficientData) {
      const totalRuns = control.runs + treatment.runs;
      return `Insufficient data (${totalRuns} total runs). Continue experiment to reach minimum sample size.`;
    }

    const costDiff =
      control.avg_cost_usd > 0
        ? ((control.avg_cost_usd - treatment.avg_cost_usd) / control.avg_cost_usd) * 100
        : 0;

    const successDiff = treatment.success_rate - control.success_rate;

    if (successDiff >= 0 && costDiff > 10) {
      return (
        `Treatment (${treatmentModel}) saved ${Math.abs(costDiff).toFixed(0)}% cost ` +
        `with ${(treatment.success_rate * 100).toFixed(0)}% success rate vs ` +
        `control (${controlModel}) at ${(control.success_rate * 100).toFixed(0)}% success rate. ` +
        `Recommend adopting treatment.`
      );
    }

    if (successDiff < -0.05) {
      return (
        `Treatment (${treatmentModel}) has lower success rate ` +
        `(${(treatment.success_rate * 100).toFixed(0)}% vs ${(control.success_rate * 100).toFixed(0)}%). ` +
        `Recommend keeping control (${controlModel}).`
      );
    }

    if (Math.abs(costDiff) <= 10 && Math.abs(successDiff) <= 0.05) {
      return (
        `No significant difference between control (${controlModel}) and ` +
        `treatment (${treatmentModel}). Consider extending the experiment or ` +
        `testing a more differentiated configuration.`
      );
    }

    return (
      `Treatment (${treatmentModel}): ${(treatment.success_rate * 100).toFixed(0)}% success, ` +
      `$${treatment.avg_cost_usd.toFixed(4)}/run. ` +
      `Control (${controlModel}): ${(control.success_rate * 100).toFixed(0)}% success, ` +
      `$${control.avg_cost_usd.toFixed(4)}/run.`
    );
  }
}
