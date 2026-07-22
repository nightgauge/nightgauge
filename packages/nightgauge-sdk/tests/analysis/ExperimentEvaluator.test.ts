/**
 * Tests for ExperimentEvaluator - Automatic A/B experiment evaluation
 *
 * @see Issue #1396 - Automatic A/B experiment evaluation and rollback
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as path from "path";
import { ExperimentEvaluator } from "../../src/analysis/ExperimentEvaluator.js";
import type { ExperimentConfig, ExperimentOutcome } from "../../src/analysis/experiment-types.js";

const WORKSPACE = "/tmp/test-experiment-evaluator";
const EXPERIMENTS_DIR = path.join(WORKSPACE, ".nightgauge/analysis/experiments");

const BASE_CONFIG: ExperimentConfig = {
  name: "haiku-vs-sonnet",
  active: true,
  control: { model: "sonnet" },
  treatment: { model: "haiku", effort: "medium" },
  split_percent: 50,
  min_runs: 20,
  observation_window: 10,
  min_effect_size: 0.05,
};

/**
 * Build N outcomes for a given group with the specified success rate.
 */
function makeOutcomes(
  group: "control" | "treatment",
  n: number,
  successRate: number
): ExperimentOutcome[] {
  return Array.from({ length: n }, (_, i) => ({
    experiment_name: "haiku-vs-sonnet",
    group,
    issue_number: i + 1,
    stage: "feature-dev",
    model: group === "treatment" ? ("haiku" as const) : ("sonnet" as const),
    success: i < Math.round(n * successRate),
    cost_usd: 0.05,
    duration_ms: 30000,
    retry_count: 0,
    recorded_at: new Date().toISOString(),
  }));
}

describe("ExperimentEvaluator", () => {
  beforeEach(() => {
    fs.mkdirSync(EXPERIMENTS_DIR, { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(WORKSPACE, { recursive: true, force: true });
  });

  // ── Core Scenarios ──────────────────────────────────────────────

  describe("evaluate()", () => {
    it("returns already_concluded when alreadyConcluded is true", () => {
      const outcomes = [...makeOutcomes("control", 10, 0.7), ...makeOutcomes("treatment", 10, 0.9)];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, true);

      expect(result.status).toBe("already_concluded");
      expect(result.experiment_name).toBe("haiku-vs-sonnet");
      expect(result.rationale).toContain("already been concluded");
    });

    it("returns deferred when both groups have fewer runs than observation_window", () => {
      const outcomes = [...makeOutcomes("control", 5, 0.7), ...makeOutcomes("treatment", 5, 0.9)];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, false);

      expect(result.status).toBe("deferred");
      expect(result.control_runs).toBe(5);
      expect(result.treatment_runs).toBe(5);
      expect(result.rationale).toContain("Insufficient data");
    });

    it("returns deferred when only control has insufficient data", () => {
      const outcomes = [...makeOutcomes("control", 3, 0.7), ...makeOutcomes("treatment", 15, 0.9)];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, false);

      expect(result.status).toBe("deferred");
      expect(result.control_runs).toBe(3);
      expect(result.treatment_runs).toBe(15);
    });

    it("returns deferred when only treatment has insufficient data", () => {
      const outcomes = [...makeOutcomes("control", 15, 0.7), ...makeOutcomes("treatment", 2, 0.9)];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, false);

      expect(result.status).toBe("deferred");
    });

    it("returns graduated when treatment has good delta and success rate", () => {
      // control: 70% success, treatment: 90% success → delta=0.2, t_sr=0.9
      const outcomes = [...makeOutcomes("control", 10, 0.7), ...makeOutcomes("treatment", 10, 0.9)];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, false);

      expect(result.status).toBe("graduated");
      expect(result.success_rate_delta).toBeGreaterThanOrEqual(0.05);
      expect(result.treatment_success_rate).toBeGreaterThan(0.6);
      expect(result.control_runs).toBe(10);
      expect(result.treatment_runs).toBe(10);
    });

    it("returns rolled_back when treatment delta is below min_effect_size", () => {
      // control: 80%, treatment: 82% → delta=0.02 < 0.05
      const outcomes = [
        ...makeOutcomes("control", 10, 0.8),
        ...makeOutcomes("treatment", 10, 0.82),
      ];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, false);

      expect(result.status).toBe("rolled_back");
    });

    it("returns rolled_back when treatment success rate is at or below 0.6 even with good delta", () => {
      // control: 40%, treatment: 60% → delta=0.2 >= 0.05 but t_sr=0.6 NOT > 0.6
      const controlOutcomes: ExperimentOutcome[] = Array.from({ length: 10 }, (_, i) => ({
        experiment_name: "haiku-vs-sonnet",
        group: "control" as const,
        issue_number: i + 1,
        stage: "feature-dev",
        model: "sonnet" as const,
        success: i < 4, // 40% success rate
        cost_usd: 0.05,
        duration_ms: 30000,
        retry_count: 0,
        recorded_at: new Date().toISOString(),
      }));
      const treatmentOutcomes: ExperimentOutcome[] = Array.from({ length: 10 }, (_, i) => ({
        experiment_name: "haiku-vs-sonnet",
        group: "treatment" as const,
        issue_number: i + 11,
        stage: "feature-dev",
        model: "haiku" as const,
        success: i < 6, // exactly 60% success rate (not > 0.6)
        cost_usd: 0.03,
        duration_ms: 20000,
        retry_count: 0,
        recorded_at: new Date().toISOString(),
      }));
      const result = ExperimentEvaluator.evaluate(
        [...controlOutcomes, ...treatmentOutcomes],
        BASE_CONFIG,
        false
      );

      expect(result.status).toBe("rolled_back");
      expect(result.treatment_success_rate).toBe(0.6);
    });

    // ── Boundary condition ──────────────────────────────────────

    it("evaluates at exactly observation_window runs per group (boundary)", () => {
      // 10 runs each = exactly observation_window → should evaluate, not defer
      const outcomes = [...makeOutcomes("control", 10, 0.7), ...makeOutcomes("treatment", 10, 0.9)];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, false);

      expect(result.status).not.toBe("deferred");
    });

    it("uses custom observation_window and min_effect_size from config", () => {
      const config: ExperimentConfig = {
        ...BASE_CONFIG,
        observation_window: 5,
        min_effect_size: 0.1,
      };

      // Only 5 runs per group — should evaluate with window=5
      const outcomes = [...makeOutcomes("control", 5, 0.6), ...makeOutcomes("treatment", 5, 0.8)];
      const result = ExperimentEvaluator.evaluate(outcomes, config, false);

      // delta = 0.2 >= 0.1, t_sr = 0.8 > 0.6 → graduated
      expect(result.status).toBe("graduated");
      expect(result.observation_window).toBe(5);
      expect(result.min_effect_size).toBe(0.1);
    });

    it("includes correct metric values in result", () => {
      const outcomes = [...makeOutcomes("control", 10, 0.7), ...makeOutcomes("treatment", 10, 0.9)];
      const result = ExperimentEvaluator.evaluate(outcomes, BASE_CONFIG, false);

      expect(result.experiment_name).toBe("haiku-vs-sonnet");
      expect(result.control_runs).toBe(10);
      expect(result.treatment_runs).toBe(10);
      expect(result.min_effect_size).toBe(0.05);
      expect(result.observation_window).toBe(10);
      expect(result.rationale).toBeTruthy();
      expect(result.recommendation).toBeTruthy();
    });

    it("returns deferred with zero outcomes", () => {
      const result = ExperimentEvaluator.evaluate([], BASE_CONFIG, false);

      expect(result.status).toBe("deferred");
      expect(result.control_runs).toBe(0);
      expect(result.treatment_runs).toBe(0);
    });
  });

  // ── Conclusion File I/O ──────────────────────────────────────

  describe("readConclusion()", () => {
    it("returns null when no conclusion file exists", () => {
      const result = ExperimentEvaluator.readConclusion(WORKSPACE, "haiku-vs-sonnet");

      expect(result).toBeNull();
    });

    it("returns parsed conclusion when file exists", () => {
      const conclusion = {
        experiment_name: "haiku-vs-sonnet",
        status: "graduated" as const,
        concluded_at: "2026-02-28T12:00:00Z",
        control_runs: 10,
        treatment_runs: 10,
        success_rate_delta: 0.2,
        treatment_success_rate: 0.9,
        rationale: "Treatment succeeded.",
      };
      fs.writeFileSync(
        path.join(EXPERIMENTS_DIR, "haiku-vs-sonnet-conclusion.json"),
        JSON.stringify(conclusion),
        "utf-8"
      );

      const result = ExperimentEvaluator.readConclusion(WORKSPACE, "haiku-vs-sonnet");

      expect(result).not.toBeNull();
      expect(result!.status).toBe("graduated");
      expect(result!.experiment_name).toBe("haiku-vs-sonnet");
    });
  });

  describe("writeConclusion()", () => {
    it("writes conclusion file to the correct path", () => {
      const conclusion = {
        experiment_name: "haiku-vs-sonnet",
        status: "rolled_back" as const,
        concluded_at: "2026-02-28T12:00:00Z",
        control_runs: 12,
        treatment_runs: 11,
        success_rate_delta: 0.01,
        treatment_success_rate: 0.55,
        rationale: "Treatment did not meet criteria.",
      };

      ExperimentEvaluator.writeConclusion(WORKSPACE, conclusion);

      const filePath = path.join(EXPERIMENTS_DIR, "haiku-vs-sonnet-conclusion.json");
      expect(fs.existsSync(filePath)).toBe(true);

      const written = JSON.parse(fs.readFileSync(filePath, "utf-8"));
      expect(written.status).toBe("rolled_back");
      expect(written.experiment_name).toBe("haiku-vs-sonnet");
    });

    it("creates directory if it does not exist", () => {
      fs.rmSync(WORKSPACE, { recursive: true, force: true });

      const conclusion = {
        experiment_name: "test-exp",
        status: "graduated" as const,
        concluded_at: new Date().toISOString(),
        control_runs: 10,
        treatment_runs: 10,
        success_rate_delta: 0.2,
        treatment_success_rate: 0.9,
        rationale: "Test.",
      };

      ExperimentEvaluator.writeConclusion(WORKSPACE, conclusion);

      const filePath = path.join(
        WORKSPACE,
        ".nightgauge/analysis/experiments/test-exp-conclusion.json"
      );
      expect(fs.existsSync(filePath)).toBe(true);
    });

    it("round-trips readConclusion after writeConclusion", () => {
      const conclusion = {
        experiment_name: "haiku-vs-sonnet",
        status: "graduated" as const,
        concluded_at: "2026-02-28T10:00:00Z",
        control_runs: 15,
        treatment_runs: 15,
        success_rate_delta: 0.15,
        treatment_success_rate: 0.8,
        rationale: "Graduated.",
      };

      ExperimentEvaluator.writeConclusion(WORKSPACE, conclusion);
      const read = ExperimentEvaluator.readConclusion(WORKSPACE, "haiku-vs-sonnet");

      expect(read).not.toBeNull();
      expect(read!.control_runs).toBe(15);
      expect(read!.success_rate_delta).toBe(0.15);
    });
  });
});
