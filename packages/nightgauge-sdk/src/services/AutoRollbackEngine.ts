/**
 * AutoRollbackEngine - Detects health degradation from auto-tune changes and decides rollbacks
 *
 * Pure SDK service that receives self-tuning log entries and health score snapshots
 * as parameters, evaluates whether auto-tune changes caused health degradation,
 * and returns rollback decisions. Cooldown tracking prevents oscillation.
 *
 * This is a DETERMINISTIC operation — same inputs always produce the same decisions.
 * The engine is a pure producer with no side effects or file I/O.
 *
 * @see Issue #1388 - Auto-rollback engine
 * @see Issue #1386 - Self-Improving Pipeline Engine (parent epic)
 */

// ── Types ───────────────────────────────────────────────────────

/**
 * Configuration for the auto-rollback engine.
 */
export interface AutoRollbackConfig {
  /** Points of health drop to trigger rollback (default: 10) */
  healthDropThreshold: number;
  /** Number of post-change runs to evaluate (default: 5) */
  runWindowSize: number;
  /** Runs before allowing re-tune of a rolled-back field (default: 10) */
  cooldownRuns: number;
  /** Whether auto-rollback is enabled (default: true when auto_tune is on) */
  enabled: boolean;
}

/**
 * Mirrors the VSCode SelfTuningLogEntry — kept as a plain interface
 * for SDK independence.
 */
export interface RollbackSelfTuningLogEntry {
  timestamp: string;
  action:
    | "auto-tune"
    | "rollback"
    | "auto-rollback"
    | "efficiency-adjustment"
    | "recurring-finding-response"
    | "routing-override"
    | "cost-health-budget-adjust"
    | "retry-policy-adjust"
    | "timeout-adjust";
  field: string;
  previous_value: number | string;
  new_value: number | string;
  rationale: string;
  confidence: string;
  sample_size: number;
  issue_number: number;
}

/**
 * Minimal health score snapshot — only fields needed for rollback evaluation.
 */
export interface RollbackHealthScoreSnapshot {
  timestamp: string;
  score: number;
  issueNumber?: number;
}

/**
 * A single rollback decision for one field.
 */
export interface RollbackDecision {
  field: string;
  reason: string;
  preChangeScore: number;
  postChangeScore: number;
  scoreDrop: number;
  autoTuneEntry: RollbackSelfTuningLogEntry;
}

/**
 * A field in active cooldown after a recent auto-rollback.
 */
export interface CooldownEntry {
  field: string;
  /** Remaining pipeline runs until cooldown expires */
  remainingRuns: number;
  /** ISO 8601 timestamp of the auto-rollback that triggered cooldown */
  triggered_at: string;
}

/**
 * Result of auto-rollback evaluation.
 */
export interface AutoRollbackResult {
  /** Number of auto-tune entries evaluated */
  evaluated: number;
  /** Rollback decisions for fields with detected degradation */
  rollbacksTriggered: RollbackDecision[];
  /** Fields in active cooldown from recent rollbacks */
  cooldownsActive: CooldownEntry[];
  /** Field names skipped due to cooldown */
  skippedDueToCooldown: string[];
}

// ── Default Configuration ───────────────────────────────────────

const DEFAULTS: AutoRollbackConfig = {
  healthDropThreshold: 10,
  runWindowSize: 5,
  cooldownRuns: 10,
  enabled: true,
};

/** Minimum health scores required before and after a change to evaluate */
const MIN_SCORES_REQUIRED = 2;

// ── Engine ──────────────────────────────────────────────────────

export class AutoRollbackEngine {
  private readonly config: AutoRollbackConfig;

  constructor(config?: Partial<AutoRollbackConfig>) {
    this.config = {
      healthDropThreshold: config?.healthDropThreshold ?? DEFAULTS.healthDropThreshold,
      runWindowSize: config?.runWindowSize ?? DEFAULTS.runWindowSize,
      cooldownRuns: config?.cooldownRuns ?? DEFAULTS.cooldownRuns,
      enabled: config?.enabled ?? DEFAULTS.enabled,
    };
  }

  /**
   * Evaluate auto-tune entries against health score history.
   *
   * For each field with an active (un-rolled-back) auto-tune entry:
   * 1. Check cooldown from prior auto-rollback
   * 2. Compare pre-change and post-change health score averages
   * 3. If health dropped by >= threshold, mark for rollback
   *
   * @param tuningLog - All self-tuning log entries (chronological)
   * @param healthSnapshots - All health score snapshots
   * @returns Evaluation result with rollback decisions and cooldown state
   */
  evaluate(
    tuningLog: RollbackSelfTuningLogEntry[],
    healthSnapshots: RollbackHealthScoreSnapshot[]
  ): AutoRollbackResult {
    const result: AutoRollbackResult = {
      evaluated: 0,
      rollbacksTriggered: [],
      cooldownsActive: [],
      skippedDueToCooldown: [],
    };

    if (!this.config.enabled || tuningLog.length === 0) {
      return result;
    }

    // Group entries by field
    const byField = new Map<string, RollbackSelfTuningLogEntry[]>();
    for (const entry of tuningLog) {
      const entries = byField.get(entry.field) ?? [];
      entries.push(entry);
      byField.set(entry.field, entries);
    }

    // Sort health snapshots chronologically
    const sortedSnapshots = [...healthSnapshots].sort(
      (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
    );

    for (const [field, entries] of byField) {
      // Sort entries chronologically
      const sorted = [...entries].sort(
        (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
      );

      // Find the most recent auto-tune entry for this field
      const lastAutoTune = findLastByAction(sorted, "auto-tune");
      if (!lastAutoTune) continue;

      // Check if this auto-tune was already rolled back
      // (a rollback/auto-rollback entry exists AFTER the auto-tune)
      const lastEntry = sorted[sorted.length - 1];
      if (lastEntry.action === "rollback" || lastEntry.action === "auto-rollback") {
        // Already rolled back — check if cooldown is still active
        this.checkCooldown(field, lastEntry, sortedSnapshots, result);
        continue;
      }

      // Check if field has a prior auto-rollback within cooldown window
      // (auto-tune was re-applied after a rollback)
      const lastAutoRollback = findLastByAction(sorted, "auto-rollback");
      if (lastAutoRollback) {
        const inCooldown = this.checkCooldown(field, lastAutoRollback, sortedSnapshots, result);
        if (inCooldown) continue;
      }

      // Evaluate health degradation for this active auto-tune entry
      result.evaluated++;

      const decision = this.evaluateDegradation(field, lastAutoTune, sortedSnapshots);
      if (decision) {
        result.rollbacksTriggered.push(decision);
      }
    }

    return result;
  }

  /**
   * Check if a field is in cooldown from a prior auto-rollback.
   * Returns true if the field is in cooldown and was added to result.
   */
  private checkCooldown(
    field: string,
    rollbackEntry: RollbackSelfTuningLogEntry,
    sortedSnapshots: RollbackHealthScoreSnapshot[],
    result: AutoRollbackResult
  ): boolean {
    if (rollbackEntry.action !== "auto-rollback") return false;

    const rollbackTime = new Date(rollbackEntry.timestamp).getTime();
    const runsSinceRollback = sortedSnapshots.filter(
      (s) => new Date(s.timestamp).getTime() > rollbackTime
    ).length;

    if (runsSinceRollback < this.config.cooldownRuns) {
      result.cooldownsActive.push({
        field,
        remainingRuns: this.config.cooldownRuns - runsSinceRollback,
        triggered_at: rollbackEntry.timestamp,
      });
      result.skippedDueToCooldown.push(field);
      return true;
    }

    return false;
  }

  /**
   * Evaluate whether an auto-tune change caused health degradation.
   * Returns a RollbackDecision if degradation is detected, null otherwise.
   */
  private evaluateDegradation(
    field: string,
    autoTuneEntry: RollbackSelfTuningLogEntry,
    sortedSnapshots: RollbackHealthScoreSnapshot[]
  ): RollbackDecision | null {
    const changeTime = new Date(autoTuneEntry.timestamp).getTime();

    const scoresBefore = sortedSnapshots.filter(
      (s) => new Date(s.timestamp).getTime() < changeTime
    );
    const scoresAfter = sortedSnapshots.filter(
      (s) => new Date(s.timestamp).getTime() >= changeTime
    );

    // Require minimum scores before and after to avoid false positives
    if (scoresBefore.length < MIN_SCORES_REQUIRED || scoresAfter.length < MIN_SCORES_REQUIRED) {
      return null;
    }

    // Use the last runWindowSize scores before and first runWindowSize after
    const preScores = scoresBefore.slice(-this.config.runWindowSize);
    const postScores = scoresAfter.slice(0, this.config.runWindowSize);

    const preAvg = average(preScores.map((s) => s.score));
    const postAvg = average(postScores.map((s) => s.score));
    const drop = preAvg - postAvg;

    if (drop >= this.config.healthDropThreshold) {
      return {
        field,
        reason: `Health score dropped ${drop.toFixed(1)} points after auto-tune change (threshold: ${this.config.healthDropThreshold})`,
        preChangeScore: Math.round(preAvg * 10) / 10,
        postChangeScore: Math.round(postAvg * 10) / 10,
        scoreDrop: Math.round(drop * 10) / 10,
        autoTuneEntry,
      };
    }

    return null;
  }
}

// ── Helpers ─────────────────────────────────────────────────────

function average(values: number[]): number {
  if (values.length === 0) return 0;
  return values.reduce((sum, v) => sum + v, 0) / values.length;
}

function findLastByAction(
  entries: RollbackSelfTuningLogEntry[],
  action: string
): RollbackSelfTuningLogEntry | undefined {
  for (let i = entries.length - 1; i >= 0; i--) {
    if (entries[i].action === action) return entries[i];
  }
  return undefined;
}
