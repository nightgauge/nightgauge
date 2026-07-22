/**
 * Pipeline Budget Ceiling - Pipeline-level total cost enforcement
 *
 * Enforces a configurable ceiling on the total cost of a single pipeline run
 * across all stages. Independent of per-stage BudgetEnforcer limits.
 *
 * Three-phase enforcement:
 * 1. **Warning** at configurable threshold (default 70%) — logs to output
 * 2. **Checkpoint** at configurable threshold (default 85%) — writes signal
 *    file so the running agent can wrap up gracefully
 * 3. **Hard stop** at 100% — pipeline will not start the next stage
 *
 * Pure utility — no vscode imports, fully deterministic and testable.
 *
 * @see Issue #1047 - Configurable token budget ceiling
 * @see budgetEnforcer.ts - Sibling enforcer for per-stage budgets
 */

export interface PipelineCeilingConfig {
  enabled: boolean;
  ceilingUsd: number;
  /**
   * Absolute USD spend at which to emit a warning WITHOUT stopping the stage.
   * Separates "you're spending a lot" (warn) from "stop now" (the ceiling).
   * Optional and additive — when unset or 0, only the percentage-based
   * warning fires, preserving pre-#3542 behavior. @see Issue #3542
   */
  warnThresholdUsd?: number;
  warningThresholdPercent: number;
  checkpointThresholdPercent: number;
  overrideCeilingUsd?: number;
}

export interface CeilingCheckResult {
  /** Hard stop — don't start next stage */
  shouldStop: boolean;
  /** Signal feature-dev to wrap up (write checkpoint file) */
  shouldCheckpoint: boolean;
  /** Emit warning notification */
  shouldWarn: boolean;
  currentCostUsd: number;
  /** Respects override if set */
  effectiveCeilingUsd: number;
  message: string;
}

export const DEFAULT_CEILING_CONFIG: PipelineCeilingConfig = {
  enabled: true,
  // Issue #3542: the old $50 ceiling killed a $61.51 issue-#3365 run that was
  // 97% complete, so #3542 raised it to $150. The maintainer has since set the
  // ceiling to $75 — enough headroom above that $61 run to let near-complete
  // work finish, while keeping per-run costs bounded. The warn-only threshold
  // below stays $50 to surface spend early; per-run `override_ceiling_usd`
  // remains available for legitimately longer runs.
  ceilingUsd: 75,
  warnThresholdUsd: 50,
  warningThresholdPercent: 70,
  checkpointThresholdPercent: 85,
};

/**
 * PipelineBudgetCeiling — pure, deterministic pipeline-level ceiling enforcement.
 *
 * No process management, no vscode imports. Just ceiling math.
 */
export class PipelineBudgetCeiling {
  private readonly config: PipelineCeilingConfig;

  constructor(config?: Partial<PipelineCeilingConfig>) {
    this.config = {
      enabled: config?.enabled ?? DEFAULT_CEILING_CONFIG.enabled,
      ceilingUsd: config?.ceilingUsd ?? DEFAULT_CEILING_CONFIG.ceilingUsd,
      warnThresholdUsd: config?.warnThresholdUsd ?? DEFAULT_CEILING_CONFIG.warnThresholdUsd,
      warningThresholdPercent:
        config?.warningThresholdPercent ?? DEFAULT_CEILING_CONFIG.warningThresholdPercent,
      checkpointThresholdPercent:
        config?.checkpointThresholdPercent ?? DEFAULT_CEILING_CONFIG.checkpointThresholdPercent,
      overrideCeilingUsd: config?.overrideCeilingUsd,
    };
  }

  /**
   * Get the effective ceiling (override if set, else base ceiling).
   */
  getEffectiveCeiling(): number {
    if (!this.config.enabled) return 0;
    return this.config.overrideCeilingUsd ?? this.config.ceilingUsd;
  }

  /**
   * Raise (or set) the override ceiling on a live instance. Used by the
   * orchestrator when the operator confirms "Increase Ceiling & Continue" —
   * before #253 the escalation only muted warnings for the current stage, so
   * the very next stage's fresh ceiling instance stopped the pipeline anyway,
   * one second after the user said to continue.
   */
  setOverrideCeiling(usd: number): void {
    this.config.overrideCeilingUsd = usd;
  }

  /**
   * Check cumulative cost against pipeline ceiling.
   *
   * Returns enforcement decision with warning, checkpoint, and stop signals.
   */
  check(currentCostUsd: number): CeilingCheckResult {
    const effectiveCeiling = this.getEffectiveCeiling();

    // Disabled or no ceiling configured
    if (!this.config.enabled || effectiveCeiling <= 0) {
      return {
        shouldStop: false,
        shouldCheckpoint: false,
        shouldWarn: false,
        currentCostUsd,
        effectiveCeilingUsd: effectiveCeiling,
        message: "",
      };
    }

    const warningThreshold = effectiveCeiling * (this.config.warningThresholdPercent / 100);
    const checkpointThreshold = effectiveCeiling * (this.config.checkpointThresholdPercent / 100);

    // Issue #3542: the absolute warn-only threshold fires a warning (never a
    // stop) when set. When it is below the percentage-based warning threshold
    // — e.g. $50 vs 70% of the $75 ceiling = $52.50 — it surfaces cost earlier
    // without killing a near-complete run. Unset (0) preserves pre-#3542 behavior.
    const warnThresholdUsd = this.config.warnThresholdUsd ?? 0;
    const effectiveWarningTrigger =
      warnThresholdUsd > 0 ? Math.min(warningThreshold, warnThresholdUsd) : warningThreshold;

    const overCeiling = currentCostUsd > effectiveCeiling;
    const overCheckpoint = currentCostUsd > checkpointThreshold && !overCeiling;
    const overWarning = currentCostUsd > effectiveWarningTrigger && !overCheckpoint && !overCeiling;

    if (overCeiling) {
      return {
        shouldStop: true,
        shouldCheckpoint: false,
        shouldWarn: false,
        currentCostUsd,
        effectiveCeilingUsd: effectiveCeiling,
        message: this.formatStopMessage(currentCostUsd, effectiveCeiling),
      };
    }

    if (overCheckpoint) {
      return {
        shouldStop: false,
        shouldCheckpoint: true,
        shouldWarn: false,
        currentCostUsd,
        effectiveCeilingUsd: effectiveCeiling,
        message: this.formatCheckpointMessage(
          currentCostUsd,
          effectiveCeiling,
          checkpointThreshold
        ),
      };
    }

    if (overWarning) {
      // When the absolute warn-only threshold is what triggered (it sits
      // below the percentage threshold), use a dollar-based message; otherwise
      // fall back to the percentage-based message.
      const triggeredByAbsolute =
        warnThresholdUsd > 0 &&
        warnThresholdUsd < warningThreshold &&
        currentCostUsd <= warningThreshold;
      const message = triggeredByAbsolute
        ? this.formatAbsoluteWarnMessage(currentCostUsd, warnThresholdUsd, effectiveCeiling)
        : this.formatWarningMessage(currentCostUsd, effectiveCeiling, warningThreshold);
      return {
        shouldStop: false,
        shouldCheckpoint: false,
        shouldWarn: true,
        currentCostUsd,
        effectiveCeilingUsd: effectiveCeiling,
        message,
      };
    }

    return {
      shouldStop: false,
      shouldCheckpoint: false,
      shouldWarn: false,
      currentCostUsd,
      effectiveCeilingUsd: effectiveCeiling,
      message: "",
    };
  }

  formatStopMessage(cost: number, ceiling: number): string {
    return (
      `[PIPELINE BUDGET CEILING] Pipeline stopped: ` +
      `$${cost.toFixed(2)} exceeds ceiling of $${ceiling.toFixed(2)}`
    );
  }

  formatCheckpointMessage(cost: number, ceiling: number, threshold: number): string {
    return (
      `[PIPELINE BUDGET CHECKPOINT] Approaching ceiling: ` +
      `$${cost.toFixed(2)} exceeds ${((threshold / ceiling) * 100).toFixed(0)}% threshold ` +
      `of $${ceiling.toFixed(2)} ceiling. Please commit current work and exit.`
    );
  }

  formatWarningMessage(cost: number, ceiling: number, threshold: number): string {
    return (
      `[PIPELINE BUDGET WARNING] Pipeline cost $${cost.toFixed(2)} ` +
      `exceeds ${((threshold / ceiling) * 100).toFixed(0)}% warning threshold ` +
      `of $${ceiling.toFixed(2)} ceiling`
    );
  }

  /**
   * Warning message for the absolute warn-only threshold (Issue #3542).
   * Unlike the percentage-based warning, this one is informational only and
   * explicitly notes that the stage is NOT being stopped.
   */
  formatAbsoluteWarnMessage(cost: number, warnThreshold: number, ceiling: number): string {
    return (
      `[PIPELINE BUDGET WARNING] Pipeline cost $${cost.toFixed(2)} ` +
      `crossed the $${warnThreshold.toFixed(2)} warn-only threshold ` +
      `(ceiling $${ceiling.toFixed(2)}, not stopping)`
    );
  }
}
