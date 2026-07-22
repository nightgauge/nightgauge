/**
 * Tests for ExperimentManager - A/B testing for model routing decisions
 *
 * @see Issue #949 - A/B Testing Framework for Model Routing Decisions
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as path from "path";
import { ExperimentManager } from "../../src/analysis/ExperimentManager.js";
import type { ExperimentConfig, ExperimentOutcome } from "../../src/analysis/experiment-types.js";

const WORKSPACE = "/tmp/test-experiment-manager";
const EXPERIMENTS_DIR = path.join(WORKSPACE, ".nightgauge/analysis/experiments");

const BASE_CONFIG: ExperimentConfig = {
  name: "haiku-vs-sonnet-planning",
  active: true,
  control: { model: "sonnet" },
  treatment: { model: "haiku", effort: "medium" },
  split_percent: 50,
  min_runs: 20,
};

function makeOutcome(overrides: Partial<ExperimentOutcome> = {}): ExperimentOutcome {
  return {
    experiment_name: "haiku-vs-sonnet-planning",
    group: "control",
    issue_number: 1,
    stage: "feature-dev",
    model: "sonnet",
    success: true,
    cost_usd: 0.05,
    duration_ms: 30000,
    retry_count: 0,
    recorded_at: new Date().toISOString(),
    ...overrides,
  };
}

describe("ExperimentManager", () => {
  beforeEach(() => {
    fs.mkdirSync(EXPERIMENTS_DIR, { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(WORKSPACE, { recursive: true, force: true });
  });

  describe("assign", () => {
    it("returns null when experiment is not active", () => {
      const result = ExperimentManager.assign(42, "feature-dev", {
        ...BASE_CONFIG,
        active: false,
      });
      expect(result).toBeNull();
    });

    it("assigns deterministically based on issue number", () => {
      const result1 = ExperimentManager.assign(42, "feature-dev", BASE_CONFIG);
      const result2 = ExperimentManager.assign(42, "feature-dev", BASE_CONFIG);
      expect(result1).not.toBeNull();
      expect(result1!.group).toBe(result2!.group);
      expect(result1!.model).toBe(result2!.model);
    });

    it("assigns treatment when issueNumber % 100 < split_percent", () => {
      // split_percent = 50, so issue 10 → 10 % 100 = 10 < 50 → treatment
      const result = ExperimentManager.assign(10, "feature-dev", BASE_CONFIG);
      expect(result!.group).toBe("treatment");
      expect(result!.model).toBe("haiku");
      expect(result!.effort).toBe("medium");
    });

    it("assigns control when issueNumber % 100 >= split_percent", () => {
      // split_percent = 50, so issue 75 → 75 % 100 = 75 >= 50 → control
      const result = ExperimentManager.assign(75, "feature-dev", BASE_CONFIG);
      expect(result!.group).toBe("control");
      expect(result!.model).toBe("sonnet");
    });

    it("distributes approximately 50/50 across 100 issues", () => {
      let treatment = 0;
      for (let i = 0; i < 100; i++) {
        const result = ExperimentManager.assign(i, "feature-dev", BASE_CONFIG);
        if (result!.group === "treatment") treatment++;
      }
      expect(treatment).toBe(50);
    });

    it("returns null when stage is not in target_stages", () => {
      const config: ExperimentConfig = {
        ...BASE_CONFIG,
        target_stages: ["feature-planning"],
      };
      const result = ExperimentManager.assign(42, "feature-dev", config);
      expect(result).toBeNull();
    });

    it("assigns when stage is in target_stages", () => {
      const config: ExperimentConfig = {
        ...BASE_CONFIG,
        target_stages: ["feature-dev", "feature-planning"],
      };
      const result = ExperimentManager.assign(42, "feature-dev", config);
      expect(result).not.toBeNull();
    });

    it("assigns all issues to treatment at 100% split", () => {
      const config: ExperimentConfig = { ...BASE_CONFIG, split_percent: 100 };
      for (let i = 0; i < 10; i++) {
        const result = ExperimentManager.assign(i, "feature-dev", config);
        expect(result!.group).toBe("treatment");
      }
    });

    it("assigns all issues to control at 0% split", () => {
      const config: ExperimentConfig = { ...BASE_CONFIG, split_percent: 0 };
      for (let i = 0; i < 10; i++) {
        const result = ExperimentManager.assign(i, "feature-dev", config);
        expect(result!.group).toBe("control");
      }
    });

    it("includes experiment metadata in assignment", () => {
      const result = ExperimentManager.assign(42, "feature-dev", BASE_CONFIG);
      expect(result!.experiment_name).toBe("haiku-vs-sonnet-planning");
      expect(result!.issue_number).toBe(42);
      expect(result!.stage).toBe("feature-dev");
      expect(result!.assigned_at).toBeTruthy();
    });
  });

  describe("recordOutcome", () => {
    it("creates directory and writes JSONL", () => {
      const outcome = makeOutcome();
      ExperimentManager.recordOutcome(WORKSPACE, outcome);

      const filePath = path.join(EXPERIMENTS_DIR, "haiku-vs-sonnet-planning.jsonl");
      expect(fs.existsSync(filePath)).toBe(true);

      const content = fs.readFileSync(filePath, "utf-8").trim();
      const parsed = JSON.parse(content);
      expect(parsed.experiment_name).toBe("haiku-vs-sonnet-planning");
      expect(parsed.success).toBe(true);
    });

    it("appends multiple outcomes", () => {
      ExperimentManager.recordOutcome(WORKSPACE, makeOutcome({ issue_number: 1 }));
      ExperimentManager.recordOutcome(WORKSPACE, makeOutcome({ issue_number: 2 }));

      const filePath = path.join(EXPERIMENTS_DIR, "haiku-vs-sonnet-planning.jsonl");
      const lines = fs.readFileSync(filePath, "utf-8").trim().split("\n");
      expect(lines).toHaveLength(2);
    });

    it("handles write failures gracefully", () => {
      const consoleSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      // Point recordOutcome at a *file* so its internal fs.mkdirSync fails fast
      // with ENOTDIR on every platform. The previous "/proc/nonexistent" path
      // made fs.mkdirSync({ recursive: true }) spin at 100% CPU forever on Linux
      // (/proc is a real read-only virtual filesystem there, unlike macOS where
      // it simply doesn't exist), which hung CI uninterruptibly.
      const notADir = path.join(WORKSPACE, "blocker");
      fs.writeFileSync(notADir, "x");
      ExperimentManager.recordOutcome(notADir, makeOutcome());
      expect(consoleSpy).toHaveBeenCalled();
      consoleSpy.mockRestore();
    });
  });

  describe("readOutcomes", () => {
    it("returns empty array for missing file", () => {
      const outcomes = ExperimentManager.readOutcomes(WORKSPACE, "nonexistent");
      expect(outcomes).toEqual([]);
    });

    it("parses JSONL file correctly", () => {
      const filePath = path.join(EXPERIMENTS_DIR, "test-experiment.jsonl");
      fs.writeFileSync(
        filePath,
        [
          JSON.stringify(makeOutcome({ group: "control" })),
          JSON.stringify(makeOutcome({ group: "treatment" })),
        ].join("\n") + "\n",
        "utf-8"
      );

      const outcomes = ExperimentManager.readOutcomes(WORKSPACE, "test-experiment");
      expect(outcomes).toHaveLength(2);
      expect(outcomes[0].group).toBe("control");
      expect(outcomes[1].group).toBe("treatment");
    });
  });

  describe("generateReport", () => {
    function seedOutcomes(
      controlCount: number,
      treatmentCount: number,
      options?: {
        controlSuccess?: number;
        treatmentSuccess?: number;
        controlCost?: number;
        treatmentCost?: number;
      }
    ) {
      const controlSuccessRate = options?.controlSuccess ?? 1.0;
      const treatmentSuccessRate = options?.treatmentSuccess ?? 1.0;
      const controlCost = options?.controlCost ?? 0.05;
      const treatmentCost = options?.treatmentCost ?? 0.02;

      for (let i = 0; i < controlCount; i++) {
        ExperimentManager.recordOutcome(
          WORKSPACE,
          makeOutcome({
            group: "control",
            model: "sonnet",
            success: Math.random() < controlSuccessRate,
            cost_usd: controlCost,
            duration_ms: 30000,
            issue_number: 100 + i,
          })
        );
      }
      for (let i = 0; i < treatmentCount; i++) {
        ExperimentManager.recordOutcome(
          WORKSPACE,
          makeOutcome({
            group: "treatment",
            model: "haiku",
            success: Math.random() < treatmentSuccessRate,
            cost_usd: treatmentCost,
            duration_ms: 20000,
            issue_number: 200 + i,
          })
        );
      }
    }

    it("reports insufficient data when below min_runs", () => {
      seedOutcomes(5, 5);
      const report = ExperimentManager.generateReport(WORKSPACE, "haiku-vs-sonnet-planning", 20);

      expect(report.sufficient_data).toBe(false);
      expect(report.total_runs.control).toBe(5);
      expect(report.total_runs.treatment).toBe(5);
      expect(report.comparison.recommendation).toContain("Insufficient data");
    });

    it("generates comparison with sufficient data", () => {
      seedOutcomes(25, 25, {
        controlSuccess: 1.0,
        treatmentSuccess: 1.0,
        controlCost: 0.05,
        treatmentCost: 0.02,
      });

      const report = ExperimentManager.generateReport(WORKSPACE, "haiku-vs-sonnet-planning", 20);

      expect(report.sufficient_data).toBe(true);
      expect(report.metrics.control.runs).toBe(25);
      expect(report.metrics.treatment.runs).toBe(25);
      expect(report.comparison.cost_savings_percent).toBeGreaterThan(0);
    });

    it("computes correct cost savings", () => {
      seedOutcomes(25, 25, {
        controlSuccess: 1.0,
        treatmentSuccess: 1.0,
        controlCost: 0.1,
        treatmentCost: 0.05,
      });

      const report = ExperimentManager.generateReport(WORKSPACE, "haiku-vs-sonnet-planning", 20);

      // Treatment costs 50% less
      expect(report.comparison.cost_savings_percent).toBe(50);
    });

    it("returns empty report for nonexistent experiment", () => {
      const report = ExperimentManager.generateReport(WORKSPACE, "nonexistent", 20);

      expect(report.total_runs.control).toBe(0);
      expect(report.total_runs.treatment).toBe(0);
      expect(report.sufficient_data).toBe(false);
    });

    it("includes group metrics", () => {
      seedOutcomes(25, 25, {
        controlSuccess: 1.0,
        treatmentSuccess: 1.0,
        controlCost: 0.05,
        treatmentCost: 0.02,
      });

      const report = ExperimentManager.generateReport(WORKSPACE, "haiku-vs-sonnet-planning", 20);

      expect(report.metrics.control.avg_cost_usd).toBeCloseTo(0.05);
      expect(report.metrics.treatment.avg_cost_usd).toBeCloseTo(0.02);
      expect(report.metrics.control.success_rate).toBe(1.0);
    });
  });
});
