/**
 * Cross-Model Skill Evaluation Harness — orchestrator.
 *
 * Iterates the (scenario × model) matrix, invokes the injected runner per
 * cell, evaluates assertions, and aggregates an `EvalRunReport`. Pure aside
 * from delegating I/O to the runner: it accepts an injected runner and an
 * injected `now` timestamp, so the matrix logic is fully unit-testable in mock
 * mode with no clock or process dependency.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 */

import type { ModelTier } from "../analysis/AutoModelSelector.js";
import { evaluateAssertions } from "./assertions.js";
import type { EvalModelRunner } from "./modelRunner.js";
import {
  EVAL_SCHEMA_VERSION,
  MODEL_TIER_VERSION_LABELS,
  type EvalCellResult,
  type EvalRunReport,
  type EvalScenario,
  type PIPELINE_SKILLS,
} from "./schemas.js";

type PipelineSkill = (typeof PIPELINE_SKILLS)[number];

export interface SkillEvalHarnessRunOptions {
  /** Scenarios to evaluate (already loaded + validated). */
  scenarios: EvalScenario[];
  /** Model tiers to evaluate. A scenario's own `models` further narrows this. */
  models: ModelTier[];
  /** ISO-8601 timestamp injected by the caller (never generated internally). */
  timestamp: string;
}

/**
 * Runs scenarios across model tiers and produces a structured run report.
 * The runner is injected: `MockModelRunner` for CI/tests, `LiveClaudeModelRunner`
 * for opt-in live evaluation.
 */
export class SkillEvalHarness {
  constructor(private readonly runner: EvalModelRunner) {}

  async run(options: SkillEvalHarnessRunOptions): Promise<EvalRunReport> {
    const { scenarios, models, timestamp } = options;
    const cells: EvalCellResult[] = [];

    for (const scenario of scenarios) {
      // A scenario can restrict which tiers it applies to.
      const tiers = scenario.models ? models.filter((m) => scenario.models!.includes(m)) : models;

      for (const model of tiers) {
        cells.push(await this.runCell(scenario, model));
      }
    }

    const skills = unique(scenarios.map((s) => s.skill)) as PipelineSkill[];
    const summary = {
      total: cells.length,
      passed: cells.filter((c) => c.verdict === "pass").length,
      failed: cells.filter((c) => c.verdict === "fail").length,
      errored: cells.filter((c) => c.verdict === "error").length,
    };

    return {
      schema_version: EVAL_SCHEMA_VERSION,
      timestamp,
      mode: this.runner.mode,
      skills,
      models,
      cells,
      summary,
    };
  }

  private async runCell(scenario: EvalScenario, model: ModelTier): Promise<EvalCellResult> {
    const base = {
      scenario_id: scenario.id,
      skill: scenario.skill,
      model,
      model_version_label: MODEL_TIER_VERSION_LABELS[model],
    };

    let output;
    try {
      output = await this.runner.run(scenario, model);
    } catch (err) {
      return {
        ...base,
        verdict: "error" as const,
        failures: [],
        error: err instanceof Error ? err.message : String(err),
      };
    }

    const evaluation = evaluateAssertions(output, scenario.assertions);
    return {
      ...base,
      verdict: evaluation.passed ? ("pass" as const) : ("fail" as const),
      failures: evaluation.failures,
      exit_code: output.exit_code,
    };
  }
}

function unique<T>(items: T[]): T[] {
  return Array.from(new Set(items));
}
