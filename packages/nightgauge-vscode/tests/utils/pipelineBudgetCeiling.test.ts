/**
 * Unit tests for PipelineBudgetCeiling
 *
 * Tests pipeline-level total cost ceiling enforcement including warning,
 * checkpoint, and hard stop thresholds, the absolute warn-only threshold,
 * override ceiling, disabled state, edge cases, and message formatting.
 *
 * @see pipelineBudgetCeiling.ts
 * @see Issue #1047 - Configurable token budget ceiling
 * @see Issue #3542 - ceiling default 50 → 150 + warn-only threshold
 * @see Issue #3727 - maintainer set the ceiling default to $75 (warn stays $50)
 */

import { describe, it, expect } from "vitest";
import {
  PipelineBudgetCeiling,
  DEFAULT_CEILING_CONFIG,
  type PipelineCeilingConfig,
} from "../../src/utils/pipelineBudgetCeiling";

// Percentage-logic fixture: a $50 ceiling with the absolute warn-only
// threshold disabled (warnThresholdUsd: 0). Lets the percentage-threshold
// tests below keep their original $35 / $42.50 / $50 math in isolation from
// the Issue #3542 absolute warn behavior, which has its own describe block.
const PERCENT_ONLY: Partial<PipelineCeilingConfig> = {
  ceilingUsd: 50,
  warnThresholdUsd: 0,
};

describe("PipelineBudgetCeiling", () => {
  // ==========================================================================
  // Default config values
  // ==========================================================================

  describe("DEFAULT_CEILING_CONFIG", () => {
    it("should be enabled by default", () => {
      expect(DEFAULT_CEILING_CONFIG.enabled).toBe(true);
    });

    it("should have a $75 ceiling by default (maintainer set ceiling to $75)", () => {
      expect(DEFAULT_CEILING_CONFIG.ceilingUsd).toBe(75);
    });

    it("should have a $50 warn-only threshold by default (Issue #3542)", () => {
      expect(DEFAULT_CEILING_CONFIG.warnThresholdUsd).toBe(50);
    });

    it("should have a 70% warning threshold by default", () => {
      expect(DEFAULT_CEILING_CONFIG.warningThresholdPercent).toBe(70);
    });

    it("should have an 85% checkpoint threshold by default", () => {
      expect(DEFAULT_CEILING_CONFIG.checkpointThresholdPercent).toBe(85);
    });

    it("should have no override ceiling by default", () => {
      expect(DEFAULT_CEILING_CONFIG.overrideCeilingUsd).toBeUndefined();
    });
  });

  // ==========================================================================
  // getEffectiveCeiling()
  // ==========================================================================

  describe("getEffectiveCeiling()", () => {
    it("should return the base ceiling when no override is set", () => {
      const ceiling = new PipelineBudgetCeiling();
      expect(ceiling.getEffectiveCeiling()).toBe(75);
    });

    it("should return the override ceiling when override is set", () => {
      const ceiling = new PipelineBudgetCeiling({ overrideCeilingUsd: 100 });
      expect(ceiling.getEffectiveCeiling()).toBe(100);
    });

    it("should return the override ceiling even when it is lower than base", () => {
      const ceiling = new PipelineBudgetCeiling({ overrideCeilingUsd: 20 });
      expect(ceiling.getEffectiveCeiling()).toBe(20);
    });

    it("should return 0 when disabled", () => {
      const ceiling = new PipelineBudgetCeiling({ enabled: false });
      expect(ceiling.getEffectiveCeiling()).toBe(0);
    });

    it("should return 0 when disabled, ignoring any override", () => {
      const ceiling = new PipelineBudgetCeiling({
        enabled: false,
        overrideCeilingUsd: 100,
      });
      expect(ceiling.getEffectiveCeiling()).toBe(0);
    });

    it("should return custom base ceiling when set without override", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 75 });
      expect(ceiling.getEffectiveCeiling()).toBe(75);
    });
  });

  // ==========================================================================
  // check() — warning threshold (percentage logic, absolute warn disabled)
  // ==========================================================================

  describe("check() - warning threshold", () => {
    const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY); // $50 ceiling, 70% warning = $35

    it("should emit warning at 70% of ceiling ($35 of $50)", () => {
      const result = ceiling.check(35.01);
      expect(result.shouldWarn).toBe(true);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldStop).toBe(false);
    });

    it("should not warn below the warning threshold", () => {
      const result = ceiling.check(34.99);
      expect(result.shouldWarn).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldStop).toBe(false);
    });

    it("should not warn at exactly $35.00 (boundary is exclusive)", () => {
      const result = ceiling.check(35.0);
      expect(result.shouldWarn).toBe(false);
    });

    it("should include current cost and effective ceiling in warning result", () => {
      const result = ceiling.check(36.0);
      expect(result.currentCostUsd).toBe(36.0);
      expect(result.effectiveCeilingUsd).toBe(50);
    });

    it("should include a non-empty message when warning", () => {
      const result = ceiling.check(36.0);
      expect(result.message).not.toBe("");
    });
  });

  // ==========================================================================
  // check() — checkpoint threshold
  // ==========================================================================

  describe("check() - checkpoint threshold", () => {
    const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY); // $50 ceiling, 85% checkpoint = $42.50

    it("should emit checkpoint at 85% of ceiling ($42.50 of $50)", () => {
      const result = ceiling.check(42.51);
      expect(result.shouldCheckpoint).toBe(true);
      expect(result.shouldWarn).toBe(false);
      expect(result.shouldStop).toBe(false);
    });

    it("should not emit checkpoint below the checkpoint threshold", () => {
      const result = ceiling.check(42.49);
      // May still warn (>$35), but should not checkpoint
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldStop).toBe(false);
    });

    it("should not checkpoint at exactly $42.50 (boundary is exclusive)", () => {
      const result = ceiling.check(42.5);
      expect(result.shouldCheckpoint).toBe(false);
    });

    it("should include current cost and effective ceiling in checkpoint result", () => {
      const result = ceiling.check(43.0);
      expect(result.currentCostUsd).toBe(43.0);
      expect(result.effectiveCeilingUsd).toBe(50);
    });

    it("should include a non-empty message when checkpointing", () => {
      const result = ceiling.check(43.0);
      expect(result.message).not.toBe("");
    });

    it("should not also warn when in checkpoint zone", () => {
      const result = ceiling.check(43.0);
      expect(result.shouldWarn).toBe(false);
    });
  });

  // ==========================================================================
  // check() — hard stop at 100%
  // ==========================================================================

  describe("check() - hard stop at 100%", () => {
    const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY); // $50 ceiling

    it("should stop when cost exceeds the ceiling", () => {
      const result = ceiling.check(50.01);
      expect(result.shouldStop).toBe(true);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
    });

    it("should stop well above the ceiling", () => {
      const result = ceiling.check(100.0);
      expect(result.shouldStop).toBe(true);
    });

    it("should not stop at exactly the ceiling (boundary is exclusive)", () => {
      const result = ceiling.check(50.0);
      expect(result.shouldStop).toBe(false);
    });

    it("should not stop just below the ceiling", () => {
      const result = ceiling.check(49.99);
      expect(result.shouldStop).toBe(false);
    });

    it("should include current cost and effective ceiling in stop result", () => {
      const result = ceiling.check(55.0);
      expect(result.currentCostUsd).toBe(55.0);
      expect(result.effectiveCeilingUsd).toBe(50);
    });

    it("should include a non-empty message when stopping", () => {
      const result = ceiling.check(55.0);
      expect(result.message).not.toBe("");
    });
  });

  // ==========================================================================
  // check() — absolute warn-only threshold (Issue #3542)
  // ==========================================================================

  describe("check() - absolute warn-only threshold (Issue #3542)", () => {
    // Default config: $75 ceiling, $50 warn-only threshold. The percentage
    // warning sits at 70% = $52.50, so the absolute $50 threshold fires earlier.
    const ceiling = new PipelineBudgetCeiling();

    it("should warn once cost crosses the absolute $50 threshold", () => {
      const result = ceiling.check(50.01);
      expect(result.shouldWarn).toBe(true);
      expect(result.shouldStop).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
    });

    it("should NOT stop at the absolute warn threshold — it is warn-only", () => {
      const result = ceiling.check(60.0);
      expect(result.shouldStop).toBe(false);
      expect(result.shouldWarn).toBe(true);
    });

    it("should not warn below the absolute threshold", () => {
      const result = ceiling.check(49.99);
      expect(result.shouldWarn).toBe(false);
    });

    it("absolute-warn message references the dollar threshold, not a percentage", () => {
      // $51 is above the absolute $50 warn but at or below 70% of $75 ($52.50),
      // so the dollar-based absolute-warn message fires (not the percentage one).
      const result = ceiling.check(51.0);
      expect(result.message).toContain("$50.00");
      expect(result.message).toContain("not stopping");
    });

    it("still hard-stops at the $75 ceiling, not at the warn threshold", () => {
      expect(ceiling.check(74.99).shouldStop).toBe(false);
      expect(ceiling.check(75.01).shouldStop).toBe(true);
    });

    it("still emits the percentage checkpoint at 85% of $75 ($63.75)", () => {
      const result = ceiling.check(64.0);
      expect(result.shouldCheckpoint).toBe(true);
      expect(result.shouldStop).toBe(false);
    });

    it("a warnThresholdUsd of 0 disables the absolute warn (pre-#3542 behavior)", () => {
      const noAbsolute = new PipelineBudgetCeiling({ ceilingUsd: 75, warnThresholdUsd: 0 });
      // $40 is below 70% of $75 ($52.50) and the absolute warn is off → no warn.
      expect(noAbsolute.check(40.0).shouldWarn).toBe(false);
    });
  });

  // ==========================================================================
  // check() — override ceiling takes precedence
  // ==========================================================================

  describe("check() - override ceiling takes precedence", () => {
    const ceiling = new PipelineBudgetCeiling({
      ceilingUsd: 50,
      warnThresholdUsd: 0,
      overrideCeilingUsd: 100,
      // default thresholds: 70% warning = $70, 85% checkpoint = $85
    });

    it("should warn at 70% of override ceiling ($70 of $100)", () => {
      const result = ceiling.check(70.01);
      expect(result.shouldWarn).toBe(true);
      expect(result.effectiveCeilingUsd).toBe(100);
    });

    it("should not warn at 70% of base ceiling ($35) when override is $100", () => {
      const result = ceiling.check(35.0);
      expect(result.shouldWarn).toBe(false);
    });

    it("should checkpoint at 85% of override ceiling ($85 of $100)", () => {
      const result = ceiling.check(85.01);
      expect(result.shouldCheckpoint).toBe(true);
      expect(result.effectiveCeilingUsd).toBe(100);
    });

    it("should stop when cost exceeds override ceiling ($100)", () => {
      const result = ceiling.check(100.01);
      expect(result.shouldStop).toBe(true);
      expect(result.effectiveCeilingUsd).toBe(100);
    });

    it("should not stop at the base ceiling ($50) when override is $100", () => {
      const result = ceiling.check(50.01);
      expect(result.shouldStop).toBe(false);
    });
  });

  // ==========================================================================
  // check() — disabled returns no enforcement
  // ==========================================================================

  describe("check() - disabled returns no enforcement signals", () => {
    const ceiling = new PipelineBudgetCeiling({ enabled: false });

    it("should not warn even when cost is very high", () => {
      const result = ceiling.check(1000.0);
      expect(result.shouldWarn).toBe(false);
    });

    it("should not checkpoint even when cost is very high", () => {
      const result = ceiling.check(1000.0);
      expect(result.shouldCheckpoint).toBe(false);
    });

    it("should not stop even when cost is very high", () => {
      const result = ceiling.check(1000.0);
      expect(result.shouldStop).toBe(false);
    });

    it("should return empty message when disabled", () => {
      const result = ceiling.check(1000.0);
      expect(result.message).toBe("");
    });

    it("should return 0 for effective ceiling when disabled", () => {
      const result = ceiling.check(1000.0);
      expect(result.effectiveCeilingUsd).toBe(0);
    });

    it("should still reflect the actual current cost in the result", () => {
      const result = ceiling.check(42.0);
      expect(result.currentCostUsd).toBe(42.0);
    });
  });

  // ==========================================================================
  // check() — edge cases
  // ==========================================================================

  describe("check() - edge cases", () => {
    it("should return no enforcement signals when ceiling is 0", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 0 });
      const result = ceiling.check(100.0);
      expect(result.shouldStop).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
      expect(result.message).toBe("");
    });

    it("should handle $0 cost without error", () => {
      const ceiling = new PipelineBudgetCeiling();
      const result = ceiling.check(0);
      expect(result.shouldStop).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
    });

    it("should not warn at exactly the warning threshold boundary", () => {
      // 70% of $50 = $35.00 exactly — not strictly greater, so no warning
      const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY);
      const result = ceiling.check(35.0);
      expect(result.shouldWarn).toBe(false);
    });

    it("should warn just above the warning threshold boundary", () => {
      const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY);
      const result = ceiling.check(35.000001);
      expect(result.shouldWarn).toBe(true);
    });

    it("should not checkpoint at exactly the checkpoint threshold boundary", () => {
      // 85% of $50 = $42.50 exactly — not strictly greater, so no checkpoint
      const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY);
      const result = ceiling.check(42.5);
      expect(result.shouldCheckpoint).toBe(false);
    });

    it("should checkpoint just above the checkpoint threshold boundary", () => {
      const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY);
      const result = ceiling.check(42.500001);
      expect(result.shouldCheckpoint).toBe(true);
    });

    it("should not stop at exactly the ceiling boundary", () => {
      // $50.00 exactly — not strictly greater, so no stop
      const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY);
      const result = ceiling.check(50.0);
      expect(result.shouldStop).toBe(false);
    });

    it("should stop just above the ceiling boundary", () => {
      const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY);
      const result = ceiling.check(50.000001);
      expect(result.shouldStop).toBe(true);
    });

    it("should produce a clean result with no signals below all thresholds", () => {
      const ceiling = new PipelineBudgetCeiling(PERCENT_ONLY);
      const result = ceiling.check(10.0);
      expect(result.shouldStop).toBe(false);
      expect(result.shouldCheckpoint).toBe(false);
      expect(result.shouldWarn).toBe(false);
      expect(result.message).toBe("");
      expect(result.currentCostUsd).toBe(10.0);
      expect(result.effectiveCeilingUsd).toBe(50);
    });
  });

  // ==========================================================================
  // check() — custom thresholds
  // ==========================================================================

  describe("check() - custom thresholds", () => {
    it("should apply custom warning threshold percentage", () => {
      // 50% warning on $50 ceiling = $25 threshold
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 50,
        warnThresholdUsd: 0,
        warningThresholdPercent: 50,
        checkpointThresholdPercent: 85,
      });

      expect(ceiling.check(24.99).shouldWarn).toBe(false);
      expect(ceiling.check(25.01).shouldWarn).toBe(true);
    });

    it("should apply custom checkpoint threshold percentage", () => {
      // 90% checkpoint on $50 ceiling = $45 threshold
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 50,
        warnThresholdUsd: 0,
        warningThresholdPercent: 70,
        checkpointThresholdPercent: 90,
      });

      expect(ceiling.check(44.99).shouldCheckpoint).toBe(false);
      expect(ceiling.check(45.01).shouldCheckpoint).toBe(true);
    });

    it("should use custom thresholds together correctly", () => {
      // 60% warning, 80% checkpoint on $100 ceiling
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 100,
        warnThresholdUsd: 0,
        warningThresholdPercent: 60,
        checkpointThresholdPercent: 80,
      });

      // Below warning ($60)
      expect(ceiling.check(59.99).shouldWarn).toBe(false);

      // In warning zone ($60–$80)
      const warnResult = ceiling.check(65.0);
      expect(warnResult.shouldWarn).toBe(true);
      expect(warnResult.shouldCheckpoint).toBe(false);
      expect(warnResult.shouldStop).toBe(false);

      // In checkpoint zone ($80–$100)
      const checkpointResult = ceiling.check(85.0);
      expect(checkpointResult.shouldCheckpoint).toBe(true);
      expect(checkpointResult.shouldWarn).toBe(false);
      expect(checkpointResult.shouldStop).toBe(false);

      // Over ceiling ($100+)
      const stopResult = ceiling.check(101.0);
      expect(stopResult.shouldStop).toBe(true);
      expect(stopResult.shouldCheckpoint).toBe(false);
      expect(stopResult.shouldWarn).toBe(false);
    });

    it("should correctly reflect custom thresholds with override ceiling", () => {
      // 50% warning, 75% checkpoint on $200 override ceiling
      const ceiling = new PipelineBudgetCeiling({
        ceilingUsd: 50,
        warnThresholdUsd: 0,
        overrideCeilingUsd: 200,
        warningThresholdPercent: 50, // $100
        checkpointThresholdPercent: 75, // $150
      });

      expect(ceiling.check(99.0).shouldWarn).toBe(false);
      expect(ceiling.check(101.0).shouldWarn).toBe(true);
      expect(ceiling.check(151.0).shouldCheckpoint).toBe(true);
      expect(ceiling.check(201.0).shouldStop).toBe(true);
    });
  });

  // ==========================================================================
  // Message formatting
  // ==========================================================================

  describe("message formatting", () => {
    const ceiling = new PipelineBudgetCeiling();

    describe("formatStopMessage()", () => {
      it("should contain PIPELINE BUDGET CEILING prefix", () => {
        const msg = ceiling.formatStopMessage(55.0, 50.0);
        expect(msg).toContain("PIPELINE BUDGET CEILING");
      });

      it('should contain the word "stopped"', () => {
        const msg = ceiling.formatStopMessage(55.0, 50.0);
        expect(msg.toLowerCase()).toContain("stopped");
      });

      it("should contain the formatted cost", () => {
        const msg = ceiling.formatStopMessage(55.5, 50.0);
        expect(msg).toContain("$55.50");
      });

      it("should contain the formatted ceiling", () => {
        const msg = ceiling.formatStopMessage(55.0, 50.0);
        expect(msg).toContain("$50.00");
      });

      it("should format costs to 2 decimal places", () => {
        const msg = ceiling.formatStopMessage(55.1, 50.0);
        expect(msg).toContain("$55.10");
        expect(msg).toContain("$50.00");
      });
    });

    describe("formatCheckpointMessage()", () => {
      it("should contain PIPELINE BUDGET CHECKPOINT prefix", () => {
        const msg = ceiling.formatCheckpointMessage(43.0, 50.0, 42.5);
        expect(msg).toContain("PIPELINE BUDGET CHECKPOINT");
      });

      it("should contain the formatted cost", () => {
        const msg = ceiling.formatCheckpointMessage(43.5, 50.0, 42.5);
        expect(msg).toContain("$43.50");
      });

      it("should contain the formatted ceiling", () => {
        const msg = ceiling.formatCheckpointMessage(43.0, 50.0, 42.5);
        expect(msg).toContain("$50.00");
      });

      it("should contain the threshold percentage", () => {
        // threshold = $42.50, ceiling = $50.00 → 85%
        const msg = ceiling.formatCheckpointMessage(43.0, 50.0, 42.5);
        expect(msg).toContain("85%");
      });

      it("should contain guidance to commit and exit", () => {
        const msg = ceiling.formatCheckpointMessage(43.0, 50.0, 42.5);
        expect(msg.toLowerCase()).toContain("commit");
        expect(msg.toLowerCase()).toContain("exit");
      });
    });

    describe("formatWarningMessage()", () => {
      it("should contain PIPELINE BUDGET WARNING prefix", () => {
        const msg = ceiling.formatWarningMessage(36.0, 50.0, 35.0);
        expect(msg).toContain("PIPELINE BUDGET WARNING");
      });

      it("should contain the formatted cost", () => {
        const msg = ceiling.formatWarningMessage(36.5, 50.0, 35.0);
        expect(msg).toContain("$36.50");
      });

      it("should contain the formatted ceiling", () => {
        const msg = ceiling.formatWarningMessage(36.0, 50.0, 35.0);
        expect(msg).toContain("$50.00");
      });

      it("should contain the threshold percentage", () => {
        // threshold = $35.00, ceiling = $50.00 → 70%
        const msg = ceiling.formatWarningMessage(36.0, 50.0, 35.0);
        expect(msg).toContain("70%");
      });

      it('should contain the word "warning" or "WARNING"', () => {
        const msg = ceiling.formatWarningMessage(36.0, 50.0, 35.0);
        expect(msg.toUpperCase()).toContain("WARNING");
      });
    });

    describe("formatAbsoluteWarnMessage() (Issue #3542)", () => {
      it("should contain PIPELINE BUDGET WARNING prefix", () => {
        const msg = ceiling.formatAbsoluteWarnMessage(55.0, 50.0, 75.0);
        expect(msg).toContain("PIPELINE BUDGET WARNING");
      });

      it("should contain the formatted cost and the warn threshold", () => {
        const msg = ceiling.formatAbsoluteWarnMessage(55.5, 50.0, 75.0);
        expect(msg).toContain("$55.50");
        expect(msg).toContain("$50.00");
      });

      it("should make clear the stage is NOT being stopped", () => {
        const msg = ceiling.formatAbsoluteWarnMessage(55.0, 50.0, 75.0);
        expect(msg.toLowerCase()).toContain("not stopping");
      });
    });

    describe("message content via check()", () => {
      const percentCeiling = new PipelineBudgetCeiling(PERCENT_ONLY);

      it("check() stop message should match formatStopMessage() output", () => {
        const result = percentCeiling.check(55.0);
        const expected = percentCeiling.formatStopMessage(55.0, 50.0);
        expect(result.message).toBe(expected);
      });

      it("check() checkpoint message should match formatCheckpointMessage() output", () => {
        const checkpointThreshold = 50.0 * (85 / 100); // $42.50
        const result = percentCeiling.check(43.0);
        const expected = percentCeiling.formatCheckpointMessage(43.0, 50.0, checkpointThreshold);
        expect(result.message).toBe(expected);
      });

      it("check() warning message should match formatWarningMessage() output", () => {
        const warningThreshold = 50.0 * (70 / 100); // $35.00
        const result = percentCeiling.check(36.0);
        const expected = percentCeiling.formatWarningMessage(36.0, 50.0, warningThreshold);
        expect(result.message).toBe(expected);
      });

      it("check() absolute-warn message matches formatAbsoluteWarnMessage() output", () => {
        // Default config: $75 ceiling, $50 warn-only. $51 triggers the absolute
        // warn ($50) before the percentage warn (70% = $52.50).
        const defaultCeiling = new PipelineBudgetCeiling();
        const result = defaultCeiling.check(51.0);
        const expected = defaultCeiling.formatAbsoluteWarnMessage(51.0, 50.0, 75.0);
        expect(result.message).toBe(expected);
      });

      it("check() below all thresholds returns empty message", () => {
        const result = percentCeiling.check(10.0);
        expect(result.message).toBe("");
      });
    });
  });

  // ==========================================================================
  // Constructor config merging
  // ==========================================================================

  describe("constructor config merging", () => {
    it("should use all defaults when no config is provided", () => {
      const ceiling = new PipelineBudgetCeiling();
      expect(ceiling.getEffectiveCeiling()).toBe(DEFAULT_CEILING_CONFIG.ceilingUsd);
    });

    it("should override only specified fields, keeping defaults for the rest", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 200, warnThresholdUsd: 0 });
      // Only ceiling changed; thresholds remain default
      const warnResult = ceiling.check(200 * 0.7 + 0.01); // just over 70%
      expect(warnResult.shouldWarn).toBe(true);

      const belowWarn = ceiling.check(200 * 0.7 - 0.01); // just under 70%
      expect(belowWarn.shouldWarn).toBe(false);
    });

    it("should handle a fully custom config", () => {
      const config: PipelineCeilingConfig = {
        enabled: true,
        ceilingUsd: 30,
        warnThresholdUsd: 0,
        warningThresholdPercent: 60, // $18
        checkpointThresholdPercent: 80, // $24
        overrideCeilingUsd: undefined,
      };
      const ceiling = new PipelineBudgetCeiling(config);

      expect(ceiling.getEffectiveCeiling()).toBe(30);
      expect(ceiling.check(18.01).shouldWarn).toBe(true);
      expect(ceiling.check(24.01).shouldCheckpoint).toBe(true);
      expect(ceiling.check(30.01).shouldStop).toBe(true);
    });
  });

  // #253: a confirmed "Increase Ceiling & Continue" must survive on the live
  // instance — before the fix the escalation only muted warnings and the very
  // next check stopped the pipeline anyway.
  describe("setOverrideCeiling", () => {
    it("raises the effective ceiling on a live instance", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 75, warnThresholdUsd: 0 });
      expect(ceiling.check(107.02).shouldStop).toBe(true);

      ceiling.setOverrideCeiling(150);
      expect(ceiling.getEffectiveCeiling()).toBe(150);
      const after = ceiling.check(107.02);
      expect(after.shouldStop).toBe(false);
      expect(after.effectiveCeilingUsd).toBe(150);
    });

    it("still stops when cost exceeds the raised ceiling", () => {
      const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 75, warnThresholdUsd: 0 });
      ceiling.setOverrideCeiling(150);
      expect(ceiling.check(150.01).shouldStop).toBe(true);
    });
  });
});
