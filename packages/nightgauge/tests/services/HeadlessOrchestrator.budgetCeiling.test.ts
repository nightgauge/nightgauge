/**
 * Integration-style unit tests for PipelineBudgetCeiling in pipeline scenarios.
 *
 * These tests verify the budget ceiling logic that HeadlessOrchestrator uses
 * between pipeline stages. PipelineBudgetCeiling is a pure utility class with
 * no vscode dependencies, so tests exercise it directly rather than mocking
 * the full orchestrator.
 *
 * @see pipelineBudgetCeiling.ts
 * @see Issue #1047 - Configurable token budget ceiling
 */

import { describe, it, expect, vi } from "vitest";
import { PipelineBudgetCeiling } from "../../src/utils/pipelineBudgetCeiling";
import type { PipelineOutcomeType } from "../../src/services/PipelineStateService";
import { DEFAULT_CONFIG } from "../../src/config/schema";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [],
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(),
      onDidCreate: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  EventEmitter: vi.fn(() => ({
    event: vi.fn(),
    fire: vi.fn(),
    dispose: vi.fn(),
  })),
  RelativePattern: vi.fn(),
  Uri: { file: vi.fn((p: string) => ({ fsPath: p })) },
}));

describe("PipelineBudgetCeiling — pipeline integration scenarios", () => {
  // ==========================================================================
  // 1. Pipeline stops between stages when ceiling exceeded
  // ==========================================================================

  describe("pipeline stops between stages when ceiling exceeded", () => {
    it("returns shouldStop=true when cumulative cost exceeds $10 ceiling", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 10 });
      const result = ceiling.check(11);

      expect(result.shouldStop).toBe(true);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
    });

    it("returns shouldStop=false when cost is exactly at the ceiling", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 10 });
      const result = ceiling.check(10);

      expect(result.shouldStop).toBe(false);
    });

    it("stop message includes current cost and ceiling", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 10 });
      const result = ceiling.check(11);

      expect(result.message).toContain("$11.00");
      expect(result.message).toContain("$10.00");
      expect(result.message).toContain("PIPELINE BUDGET CEILING");
    });

    it("exposes the effective ceiling in the result", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 10 });
      const result = ceiling.check(11);

      expect(result.effectiveCeilingUsd).toBe(10);
      expect(result.currentCostUsd).toBe(11);
    });
  });

  // ==========================================================================
  // 2. Checkpoint signal during feature-dev
  // ==========================================================================

  describe("checkpoint signal at 87% of ceiling", () => {
    it("returns shouldCheckpoint=true at 87% of $100 ceiling (default 85% threshold)", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 100 });
      // 87% of 100 = $87 — above 85% threshold, below 100%
      const result = ceiling.check(87);

      expect(result.shouldCheckpoint).toBe(true);
      expect(result.shouldStop).toBe(false);
      expect(result.shouldWarn).toBe(false);
    });

    it("checkpoint message references the threshold and ceiling", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 100 });
      const result = ceiling.check(87);

      expect(result.message).toContain("PIPELINE BUDGET CHECKPOINT");
      expect(result.message).toContain("$87.00");
      expect(result.message).toContain("$100.00");
    });

    it("returns shouldCheckpoint=false just below the 85% threshold", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 100 });
      // $84 is below 85% threshold ($85)
      const result = ceiling.check(84);

      expect(result.shouldCheckpoint).toBe(false);
    });

    it("uses configurable checkpoint threshold", () => {
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 100,
        checkpointThresholdPercent: 90,
      });
      // $87 is below 90% threshold ($90)
      const belowThreshold = ceiling.check(87);
      expect(belowThreshold.shouldCheckpoint).toBe(false);

      // $91 is above 90% threshold
      const aboveThreshold = ceiling.check(91);
      expect(aboveThreshold.shouldCheckpoint).toBe(true);
    });
  });

  // ==========================================================================
  // 3. Override allows higher spending
  // ==========================================================================

  describe("override ceiling allows higher spending", () => {
    it("returns no signals at $60 when override is $100 (60% < 70% warning threshold)", () => {
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 50, // base ceiling
        warnThresholdUsd: 0, // isolate percentage logic from the absolute warn
        overrideCeilingUsd: 100, // operator override for large tasks
      });
      const result = ceiling.check(60);

      expect(result.shouldStop).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
      expect(result.message).toBe("");
    });

    it("getEffectiveCeiling returns override when set", () => {
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 50,
        overrideCeilingUsd: 100,
      });

      expect(ceiling.getEffectiveCeiling()).toBe(100);
    });

    it("override ceiling triggers stop at cost exceeding $100", () => {
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 50,
        overrideCeilingUsd: 100,
      });
      const result = ceiling.check(101);

      expect(result.shouldStop).toBe(true);
      expect(result.effectiveCeilingUsd).toBe(100);
    });

    it("base ceiling is used when no override is set", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 50 });

      expect(ceiling.getEffectiveCeiling()).toBe(50);
    });
  });

  // ==========================================================================
  // 4. Budget-ceiling outcome type
  // ==========================================================================

  describe("'budget-ceiling' is a valid PipelineOutcomeType", () => {
    it("can assign budget-ceiling as a PipelineOutcomeType", () => {
      // Type-level check — if this compiles the type is valid
      const outcome: PipelineOutcomeType = "budget-ceiling";
      expect(outcome).toBe("budget-ceiling");
    });

    it("budget-ceiling is distinct from other outcome types", () => {
      const outcomes: PipelineOutcomeType[] = ["success", "failure", "cancelled", "budget-ceiling"];
      expect(outcomes).toContain("budget-ceiling");
      expect(new Set(outcomes).size).toBe(outcomes.length);
    });
  });

  // ==========================================================================
  // 5. Warning → checkpoint → stop progression
  // ==========================================================================

  describe("warning → checkpoint → stop progression", () => {
    const ceiling = new PipelineBudgetCeiling({
      ceilingUsd: 100,
      warnThresholdUsd: 0, // isolate the percentage progression from the absolute warn
      warningThresholdPercent: 70,
      checkpointThresholdPercent: 85,
    });

    it("no signals at $50 (50% of $100)", () => {
      const result = ceiling.check(50);
      expect(result.shouldWarn).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldStop).toBe(false);
      expect(result.message).toBe("");
    });

    it("warns at $75 (75% of $100, above 70% threshold)", () => {
      const result = ceiling.check(75);
      expect(result.shouldWarn).toBe(true);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldStop).toBe(false);
      expect(result.message).toContain("PIPELINE BUDGET WARNING");
    });

    it("checkpoints at $90 (90% of $100, above 85% threshold)", () => {
      const result = ceiling.check(90);
      expect(result.shouldCheckpoint).toBe(true);
      expect(result.shouldWarn).toBe(false);
      expect(result.shouldStop).toBe(false);
      expect(result.message).toContain("PIPELINE BUDGET CHECKPOINT");
    });

    it("stops at $101 (above 100% ceiling)", () => {
      const result = ceiling.check(101);
      expect(result.shouldStop).toBe(true);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
      expect(result.message).toContain("PIPELINE BUDGET CEILING");
    });

    it("exactly at warning threshold boundary does not warn", () => {
      // $70 is exactly 70% — not *over* the threshold
      const result = ceiling.check(70);
      expect(result.shouldWarn).toBe(false);
    });

    it("exactly at checkpoint threshold boundary does not checkpoint", () => {
      // $85 is exactly 85% — not *over* the threshold
      const result = ceiling.check(85);
      expect(result.shouldCheckpoint).toBe(false);
    });
  });

  // ==========================================================================
  // 6. Ceiling config from schema defaults
  // ==========================================================================

  describe("DEFAULT_CONFIG includes token_budget_ceiling section", () => {
    it("pipeline.token_budget_ceiling is defined in DEFAULT_CONFIG", () => {
      expect(DEFAULT_CONFIG.pipeline?.token_budget_ceiling).toBeDefined();
    });

    it("default ceiling is enabled", () => {
      expect(DEFAULT_CONFIG.pipeline?.token_budget_ceiling?.enabled).toBe(true);
    });

    it("default ceiling_usd is $75 (maintainer set ceiling to $75)", () => {
      expect(DEFAULT_CONFIG.pipeline?.token_budget_ceiling?.ceiling_usd).toBe(75);
    });

    it("default warn_threshold_usd is $50 (Issue #3542 warn-only threshold)", () => {
      expect(DEFAULT_CONFIG.pipeline?.token_budget_ceiling?.warn_threshold_usd).toBe(50);
    });

    it("default warning_threshold_percent is 70", () => {
      expect(DEFAULT_CONFIG.pipeline?.token_budget_ceiling?.warning_threshold_percent).toBe(70);
    });

    it("default checkpoint_threshold_percent is 85", () => {
      expect(DEFAULT_CONFIG.pipeline?.token_budget_ceiling?.checkpoint_threshold_percent).toBe(85);
    });

    it("schema defaults match PipelineBudgetCeiling DEFAULT_CEILING_CONFIG", () => {
      const schemaCeiling = DEFAULT_CONFIG.pipeline?.token_budget_ceiling;

      // Construct a ceiling with schema defaults and verify behavior matches
      const ceiling = new PipelineBudgetCeiling({
        enabled: schemaCeiling?.enabled ?? true,
        ceilingUsd: schemaCeiling?.ceiling_usd ?? 75,
        warnThresholdUsd: schemaCeiling?.warn_threshold_usd ?? 50,
        warningThresholdPercent: schemaCeiling?.warning_threshold_percent ?? 70,
        checkpointThresholdPercent: schemaCeiling?.checkpoint_threshold_percent ?? 85,
      });

      expect(ceiling.getEffectiveCeiling()).toBe(75);

      // The absolute $50 warn-only threshold fires before the 70% ($52.50) one.
      const warnResult = ceiling.check(51);
      expect(warnResult.shouldWarn).toBe(true);
      expect(warnResult.shouldStop).toBe(false);

      // At ~85% (above $63.75) should checkpoint
      const checkpointResult = ceiling.check(64);
      expect(checkpointResult.shouldCheckpoint).toBe(true);

      // At $76 should stop (the $75 ceiling, NOT the $50 warn threshold)
      const stopResult = ceiling.check(76);
      expect(stopResult.shouldStop).toBe(true);
      // $74 must NOT stop — proves the warn threshold is not a kill threshold.
      expect(ceiling.check(74).shouldStop).toBe(false);
    });
  });

  // ==========================================================================
  // Disabled ceiling
  // ==========================================================================

  describe("disabled ceiling emits no signals", () => {
    it("returns no signals for any cost when disabled", () => {
      const ceiling = new PipelineBudgetCeiling({
        enabled: false,
        ceilingUsd: 10,
      });

      const result = ceiling.check(1000);
      expect(result.shouldStop).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
      expect(result.message).toBe("");
    });

    it("getEffectiveCeiling returns 0 when disabled", () => {
      const ceiling = new PipelineBudgetCeiling({
        enabled: false,
        ceilingUsd: 50,
      });
      expect(ceiling.getEffectiveCeiling()).toBe(0);
    });
  });
});
