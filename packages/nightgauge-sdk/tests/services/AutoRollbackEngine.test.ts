import { describe, it, expect } from "vitest";
import {
  AutoRollbackEngine,
  type RollbackSelfTuningLogEntry,
  type RollbackHealthScoreSnapshot,
} from "../../src/services/AutoRollbackEngine.js";

// ── Test Data Factories ──────────────────────────────────────────

function createAutoTuneEntry(
  overrides?: Partial<RollbackSelfTuningLogEntry>
): RollbackSelfTuningLogEntry {
  return {
    timestamp: "2026-02-20T12:00:00Z",
    action: "auto-tune",
    field: "complexity_thresholds.haiku_max",
    previous_value: 3,
    new_value: 4,
    rationale: "Under-routing detected",
    confidence: "high",
    sample_size: 12,
    issue_number: 100,
    ...overrides,
  };
}

function createRollbackEntry(
  overrides?: Partial<RollbackSelfTuningLogEntry>
): RollbackSelfTuningLogEntry {
  return {
    timestamp: "2026-02-22T12:00:00Z",
    action: "auto-rollback",
    field: "complexity_thresholds.haiku_max",
    previous_value: 4,
    new_value: 3,
    rationale: "Auto-rollback due to health degradation",
    confidence: "high",
    sample_size: 12,
    issue_number: 100,
    ...overrides,
  };
}

function createHealthSnapshot(
  score: number,
  timestamp: string,
  issueNumber?: number
): RollbackHealthScoreSnapshot {
  return { timestamp, score, issueNumber };
}

/**
 * Generate a series of health snapshots with consistent spacing.
 * @param scores - Array of scores in chronological order
 * @param startDate - ISO date string for the first snapshot
 * @param hoursBetween - Hours between snapshots (default: 24)
 */
function createHealthSeries(
  scores: number[],
  startDate: string = "2026-02-15T12:00:00Z",
  hoursBetween: number = 24
): RollbackHealthScoreSnapshot[] {
  const start = new Date(startDate).getTime();
  return scores.map((score, i) => ({
    timestamp: new Date(start + i * hoursBetween * 3600000).toISOString(),
    score,
  }));
}

// ── Tests ────────────────────────────────────────────────────────

describe("AutoRollbackEngine", () => {
  describe("no rollback needed", () => {
    it("returns empty result when health scores are stable after auto-tune", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 5,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-20T12:00:00Z" }),
      ];

      // Scores before change: ~75, scores after: ~75 (stable)
      const healthSnapshots = createHealthSeries(
        [74, 76, 75, 73, 75, 76, 74, 75, 77, 74],
        "2026-02-15T12:00:00Z"
      );

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.evaluated).toBe(1);
      expect(result.rollbacksTriggered).toHaveLength(0);
      expect(result.cooldownsActive).toHaveLength(0);
      expect(result.skippedDueToCooldown).toHaveLength(0);
    });

    it("returns empty result when health scores improve after auto-tune", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 5,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-20T12:00:00Z" }),
      ];

      // Before: ~65, after: ~80 (improved)
      const healthSnapshots = createHealthSeries(
        [64, 66, 65, 63, 65, 80, 82, 79, 81, 78],
        "2026-02-15T12:00:00Z"
      );

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.evaluated).toBe(1);
      expect(result.rollbacksTriggered).toHaveLength(0);
    });
  });

  describe("single rollback", () => {
    it("triggers rollback when health drops > threshold after auto-tune", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 5,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-20T12:00:00Z" }),
      ];

      // Before: ~80, after: ~60 (20-point drop > 10 threshold)
      const healthSnapshots = createHealthSeries(
        [79, 81, 80, 82, 78, 62, 58, 60, 61, 59],
        "2026-02-15T12:00:00Z"
      );

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.evaluated).toBe(1);
      expect(result.rollbacksTriggered).toHaveLength(1);

      const decision = result.rollbacksTriggered[0];
      expect(decision.field).toBe("complexity_thresholds.haiku_max");
      expect(decision.scoreDrop).toBeGreaterThanOrEqual(10);
      expect(decision.preChangeScore).toBeGreaterThan(decision.postChangeScore);
      expect(decision.autoTuneEntry.action).toBe("auto-tune");
    });

    it("includes detailed decision with pre/post scores", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 3,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-18T12:00:00Z" }),
      ];

      // 3 scores before: [80, 80, 80], 3 scores after: [60, 60, 60]
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-15T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-16T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(60, "2026-02-19T12:00:00Z"),
        createHealthSnapshot(60, "2026-02-20T12:00:00Z"),
        createHealthSnapshot(60, "2026-02-21T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.rollbacksTriggered).toHaveLength(1);
      expect(result.rollbacksTriggered[0].preChangeScore).toBe(80);
      expect(result.rollbacksTriggered[0].postChangeScore).toBe(60);
      expect(result.rollbacksTriggered[0].scoreDrop).toBe(20);
    });
  });

  describe("cooldown prevents re-apply", () => {
    it("skips field in cooldown after auto-rollback", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 5,
        cooldownRuns: 10,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        // Original auto-tune
        createAutoTuneEntry({ timestamp: "2026-02-10T12:00:00Z" }),
        // Auto-rollback happened
        createRollbackEntry({ timestamp: "2026-02-15T12:00:00Z" }),
        // Auto-tune re-applied the same change
        createAutoTuneEntry({ timestamp: "2026-02-16T12:00:00Z" }),
      ];

      // Only 3 snapshots since the rollback (< 10 cooldown)
      const healthSnapshots = [
        ...createHealthSeries([80, 80, 80, 80, 80], "2026-02-08T12:00:00Z"),
        createHealthSnapshot(70, "2026-02-16T12:00:00Z"),
        createHealthSnapshot(68, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(65, "2026-02-18T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.skippedDueToCooldown).toContain("complexity_thresholds.haiku_max");
      expect(result.cooldownsActive).toHaveLength(1);
      expect(result.cooldownsActive[0].remainingRuns).toBeGreaterThan(0);
      expect(result.rollbacksTriggered).toHaveLength(0);
    });

    it("allows evaluation after cooldown expires", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 3,
        cooldownRuns: 3,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-10T12:00:00Z" }),
        createRollbackEntry({ timestamp: "2026-02-12T12:00:00Z" }),
        // Re-applied after cooldown
        createAutoTuneEntry({ timestamp: "2026-02-20T12:00:00Z" }),
      ];

      // 5 snapshots since rollback (> 3 cooldown) — cooldown expired
      // Before re-apply: ~80, after: ~60
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-09T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-11T12:00:00Z"),
        createHealthSnapshot(75, "2026-02-13T12:00:00Z"),
        createHealthSnapshot(78, "2026-02-15T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-19T12:00:00Z"),
        createHealthSnapshot(60, "2026-02-21T12:00:00Z"),
        createHealthSnapshot(58, "2026-02-22T12:00:00Z"),
        createHealthSnapshot(55, "2026-02-23T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.skippedDueToCooldown).toHaveLength(0);
      expect(result.evaluated).toBe(1);
      expect(result.rollbacksTriggered).toHaveLength(1);
    });
  });

  describe("multiple changes with partial rollback", () => {
    it("rolls back only the field with degradation", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 3,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        // Field A: auto-tune applied, health stable
        createAutoTuneEntry({
          field: "complexity_thresholds.haiku_max",
          timestamp: "2026-02-18T12:00:00Z",
        }),
        // Field B: auto-tune applied, health drops
        createAutoTuneEntry({
          field: "complexity_thresholds.sonnet_max",
          timestamp: "2026-02-18T12:00:00Z",
          previous_value: 5,
          new_value: 6,
        }),
        // Field C: auto-tune applied, health stable
        createAutoTuneEntry({
          field: "pipeline.budget_ceiling_usd",
          timestamp: "2026-02-18T12:00:00Z",
          previous_value: 2,
          new_value: 1.5,
        }),
      ];

      // Stagger changes with enough healthy scores between them so
      // field A's runWindowSize=3 post-change scores are all healthy.
      tuningLog[0].timestamp = "2026-02-10T12:00:00Z"; // Field A: early
      tuningLog[1].timestamp = "2026-02-20T12:00:00Z"; // Field B: later
      tuningLog[2].timestamp = "2026-02-20T01:00:00Z"; // Field C: same day as B

      const healthSnapshots = [
        // Before field A (Feb 10): stable ~80
        createHealthSnapshot(80, "2026-02-07T12:00:00Z"),
        createHealthSnapshot(82, "2026-02-08T12:00:00Z"),
        createHealthSnapshot(79, "2026-02-09T12:00:00Z"),
        // After field A (Feb 10): still healthy — these are A's post-change window
        createHealthSnapshot(78, "2026-02-11T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-12T12:00:00Z"),
        createHealthSnapshot(81, "2026-02-13T12:00:00Z"),
        // More healthy scores (still before field B)
        createHealthSnapshot(79, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-19T12:00:00Z"),
        // After field B and C (Feb 20): drops
        createHealthSnapshot(60, "2026-02-21T12:00:00Z"),
        createHealthSnapshot(58, "2026-02-22T12:00:00Z"),
        createHealthSnapshot(55, "2026-02-23T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.evaluated).toBe(3);

      // Field A should NOT be rolled back (pre-change: ~80, post-change: ~78 — not a 10pt drop)
      const fieldARollback = result.rollbacksTriggered.find(
        (r) => r.field === "complexity_thresholds.haiku_max"
      );
      expect(fieldARollback).toBeUndefined();

      // Field B SHOULD be rolled back (pre-change: ~80, post-change: ~58 — significant drop)
      const fieldBRollback = result.rollbacksTriggered.find(
        (r) => r.field === "complexity_thresholds.sonnet_max"
      );
      expect(fieldBRollback).toBeDefined();
      expect(fieldBRollback!.scoreDrop).toBeGreaterThanOrEqual(10);
    });
  });

  describe("no auto-tune entries", () => {
    it("returns evaluated:0 with empty tuning log", () => {
      const engine = new AutoRollbackEngine();

      const result = engine.evaluate([], [createHealthSnapshot(80, "2026-02-20T12:00:00Z")]);

      expect(result.evaluated).toBe(0);
      expect(result.rollbacksTriggered).toHaveLength(0);
      expect(result.cooldownsActive).toHaveLength(0);
      expect(result.skippedDueToCooldown).toHaveLength(0);
    });

    it("returns evaluated:0 when only rollback entries exist", () => {
      const engine = new AutoRollbackEngine();

      const tuningLog: RollbackSelfTuningLogEntry[] = [createRollbackEntry({ action: "rollback" })];

      const result = engine.evaluate(tuningLog, [createHealthSnapshot(80, "2026-02-20T12:00:00Z")]);

      expect(result.evaluated).toBe(0);
    });
  });

  describe("insufficient post-change data", () => {
    it("skips evaluation when fewer than 2 scores before change", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 5,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-20T12:00:00Z" }),
      ];

      // Only 1 score before change
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-19T12:00:00Z"),
        createHealthSnapshot(50, "2026-02-21T12:00:00Z"),
        createHealthSnapshot(48, "2026-02-22T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.evaluated).toBe(1);
      expect(result.rollbacksTriggered).toHaveLength(0);
    });

    it("skips evaluation when fewer than 2 scores after change", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 5,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-20T12:00:00Z" }),
      ];

      // Only 1 score after change
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(82, "2026-02-18T12:00:00Z"),
        createHealthSnapshot(79, "2026-02-19T12:00:00Z"),
        createHealthSnapshot(50, "2026-02-21T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.evaluated).toBe(1);
      expect(result.rollbacksTriggered).toHaveLength(0);
    });
  });

  describe("configurable thresholds", () => {
    it("respects custom healthDropThreshold", () => {
      // With threshold=5, a 7-point drop should trigger rollback
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 5,
        runWindowSize: 3,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-18T12:00:00Z" }),
      ];

      // Before: ~80, after: ~73 (7-point drop, > 5 threshold)
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-15T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-16T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(73, "2026-02-19T12:00:00Z"),
        createHealthSnapshot(73, "2026-02-20T12:00:00Z"),
        createHealthSnapshot(73, "2026-02-21T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);
      expect(result.rollbacksTriggered).toHaveLength(1);

      // With threshold=10, same 7-point drop should NOT trigger
      const strictEngine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 3,
      });

      const strictResult = strictEngine.evaluate(tuningLog, healthSnapshots);
      expect(strictResult.rollbacksTriggered).toHaveLength(0);
    });

    it("respects custom runWindowSize", () => {
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        runWindowSize: 2,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-18T12:00:00Z" }),
      ];

      // Before (last 2): [80, 80], after (first 2): [60, 60] → 20pt drop
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-15T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-16T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(60, "2026-02-19T12:00:00Z"),
        createHealthSnapshot(60, "2026-02-20T12:00:00Z"),
        // Extra scores that improve — ignored because runWindowSize=2
        createHealthSnapshot(90, "2026-02-21T12:00:00Z"),
        createHealthSnapshot(95, "2026-02-22T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);
      expect(result.rollbacksTriggered).toHaveLength(1);
      expect(result.rollbacksTriggered[0].scoreDrop).toBe(20);
    });

    it("respects custom cooldownRuns", () => {
      // Short cooldown: 2 runs
      const engine = new AutoRollbackEngine({
        healthDropThreshold: 10,
        cooldownRuns: 2,
      });

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-10T12:00:00Z" }),
        createRollbackEntry({ timestamp: "2026-02-12T12:00:00Z" }),
      ];

      // 3 snapshots since rollback (> 2 cooldown) — cooldown expired
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-09T12:00:00Z"),
        createHealthSnapshot(75, "2026-02-11T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-13T12:00:00Z"),
        createHealthSnapshot(82, "2026-02-14T12:00:00Z"),
        createHealthSnapshot(78, "2026-02-15T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);

      // Cooldown should be expired — field not in skippedDueToCooldown
      expect(result.skippedDueToCooldown).toHaveLength(0);
    });
  });

  describe("disabled engine", () => {
    it("returns empty result when enabled is false", () => {
      const engine = new AutoRollbackEngine({ enabled: false });

      const tuningLog: RollbackSelfTuningLogEntry[] = [createAutoTuneEntry()];
      const healthSnapshots = createHealthSeries(
        [80, 80, 80, 80, 80, 50, 50, 50, 50, 50],
        "2026-02-15T12:00:00Z"
      );

      const result = engine.evaluate(tuningLog, healthSnapshots);

      expect(result.evaluated).toBe(0);
      expect(result.rollbacksTriggered).toHaveLength(0);
    });
  });

  describe("default configuration", () => {
    it("uses sensible defaults", () => {
      const engine = new AutoRollbackEngine();

      const tuningLog: RollbackSelfTuningLogEntry[] = [
        createAutoTuneEntry({ timestamp: "2026-02-18T12:00:00Z" }),
      ];

      // 15-point drop should trigger with default threshold of 10
      const healthSnapshots = [
        createHealthSnapshot(80, "2026-02-14T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-15T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-16T12:00:00Z"),
        createHealthSnapshot(80, "2026-02-17T12:00:00Z"),
        createHealthSnapshot(65, "2026-02-19T12:00:00Z"),
        createHealthSnapshot(65, "2026-02-20T12:00:00Z"),
        createHealthSnapshot(65, "2026-02-21T12:00:00Z"),
      ];

      const result = engine.evaluate(tuningLog, healthSnapshots);
      expect(result.rollbacksTriggered).toHaveLength(1);
    });
  });
});
