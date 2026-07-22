/**
 * Context Budget Enforcer - Per-stage input token budget enforcement
 *
 * Mirrors BudgetEnforcer pattern but operates on input token counts
 * instead of USD cost. Enforces configurable per-stage input token
 * budgets with soft (warn) or hard (terminate) modes.
 *
 * Default budgets are derived from historical p50 usage with generous
 * headroom. Default mode is 'soft' (warn only) to avoid breaking
 * existing pipelines.
 *
 * @see Issue #790 - Per-stage context budgets
 * @see budgetEnforcer.ts - Sibling enforcer for USD cost and output tokens
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { BudgetMode, SizeLabel, SizeAwareBudget } from "./budgetEnforcer";

export interface InputTokenEnforcementDecision {
  shouldTerminate: boolean;
  shouldWarn: boolean;
  currentInputTokens: number;
  effectiveLimit: number;
  budgetMode: BudgetMode;
  message: string;
}

/**
 * Default per-stage, per-size input token budgets.
 *
 * Derived from historical p50 (median) usage with headroom.
 * Grace buffer (default 50%) provides additional room to ~p75+.
 *
 * @see Issue #790 - Per-stage context budgets
 */
export const DEFAULT_INPUT_TOKEN_BUDGETS: Record<string, SizeAwareBudget> = {
  "issue-pickup": { XS: 5000, S: 8000, M: 15000, L: 25000, XL: 40000 },
  "feature-planning": {
    XS: 50000,
    S: 80000,
    M: 120000,
    L: 200000,
    XL: 300000,
  },
  "feature-dev": {
    XS: 100000,
    S: 150000,
    M: 250000,
    L: 400000,
    XL: 600000,
  },
  "feature-validate": {
    XS: 50000,
    S: 80000,
    M: 120000,
    L: 200000,
    XL: 300000,
  },
  "pr-create": { XS: 5000, S: 8000, M: 15000, L: 25000, XL: 40000 },
  "pr-merge": { XS: 10000, S: 15000, M: 25000, L: 40000, XL: 60000 },
};

export const DEFAULT_CONTEXT_BUDGET_MODE: BudgetMode = "soft";
export const DEFAULT_CONTEXT_GRACE_PERCENT = 50;

export interface ContextBudgetEnforcerConfig {
  enabled: boolean;
  mode: BudgetMode;
  gracePercent: number;
  /** Per-stage per-size input token budget overrides from config */
  stageOverrides?: Record<string, number | Partial<SizeAwareBudget>>;
}

/**
 * ContextBudgetEnforcer — pure, deterministic input token budget enforcement.
 *
 * No process management, no vscode imports. Just budget math.
 */
export class ContextBudgetEnforcer {
  private readonly enabled: boolean;
  private readonly mode: BudgetMode;
  private readonly gracePercent: number;
  private readonly stageOverrides?: Record<string, number | Partial<SizeAwareBudget>>;

  constructor(config?: Partial<ContextBudgetEnforcerConfig>) {
    this.enabled = config?.enabled ?? true;
    this.mode = config?.mode ?? DEFAULT_CONTEXT_BUDGET_MODE;
    this.gracePercent = config?.gracePercent ?? DEFAULT_CONTEXT_GRACE_PERCENT;
    this.stageOverrides = config?.stageOverrides;
  }

  /**
   * Get the base context budget for a stage + size combination.
   *
   * Resolution: config override → size-aware default → M fallback.
   */
  getBaseContextBudget(stage: PipelineStage | string, sizeLabel?: SizeLabel): number {
    if (!this.enabled) return 0;

    const size = sizeLabel ?? "M";

    // Check config overrides first
    if (this.stageOverrides?.[stage] !== undefined) {
      const override = this.stageOverrides[stage];
      if (typeof override === "number") {
        return override;
      }
      // Size-aware override object
      const sizeValue = (override as Partial<SizeAwareBudget>)[size];
      if (sizeValue !== undefined) {
        return sizeValue;
      }
      // Fall back to M in the override
      const mValue = (override as Partial<SizeAwareBudget>).M;
      if (mValue !== undefined) {
        return mValue;
      }
    }

    // Size-aware defaults
    const defaults = DEFAULT_INPUT_TOKEN_BUDGETS[stage];
    if (defaults) {
      return defaults[size];
    }

    // Unknown stage — no budget
    return 0;
  }

  /**
   * Get the effective limit (budget + grace buffer).
   */
  getEffectiveContextLimit(stage: PipelineStage | string, sizeLabel?: SizeLabel): number {
    const base = this.getBaseContextBudget(stage, sizeLabel);
    if (base <= 0) return 0;
    return Math.round(base * (1 + this.gracePercent / 100));
  }

  /**
   * Check whether a stage's current input tokens trigger enforcement.
   */
  checkInputTokens(
    stage: PipelineStage | string,
    currentInputTokens: number,
    sizeLabel?: SizeLabel
  ): InputTokenEnforcementDecision {
    const baseBudget = this.getBaseContextBudget(stage, sizeLabel);
    const effectiveLimit = this.getEffectiveContextLimit(stage, sizeLabel);

    // No budget configured or enforcer disabled
    if (baseBudget <= 0) {
      return {
        shouldTerminate: false,
        shouldWarn: false,
        currentInputTokens,
        effectiveLimit: 0,
        budgetMode: this.mode,
        message: "",
      };
    }

    const overBaseBudget = currentInputTokens > baseBudget;
    const overEffectiveLimit = currentInputTokens > effectiveLimit;

    switch (this.mode) {
      case "soft":
        return {
          shouldTerminate: false,
          shouldWarn: overBaseBudget,
          currentInputTokens,
          effectiveLimit,
          budgetMode: "soft",
          message: overBaseBudget
            ? this.formatWarningMessage(stage, currentInputTokens, baseBudget)
            : "",
        };

      case "hard":
        return {
          shouldTerminate: overEffectiveLimit,
          shouldWarn: overBaseBudget && !overEffectiveLimit,
          currentInputTokens,
          effectiveLimit,
          budgetMode: "hard",
          message: overEffectiveLimit
            ? this.formatTerminationMessage(stage, currentInputTokens, effectiveLimit)
            : overBaseBudget
              ? this.formatWarningMessage(stage, currentInputTokens, baseBudget)
              : "",
        };

      case "threshold":
        return {
          shouldTerminate: overEffectiveLimit,
          shouldWarn: overBaseBudget && !overEffectiveLimit,
          currentInputTokens,
          effectiveLimit,
          budgetMode: "threshold",
          message: overEffectiveLimit
            ? this.formatTerminationMessage(stage, currentInputTokens, effectiveLimit)
            : overBaseBudget
              ? this.formatWarningMessage(stage, currentInputTokens, baseBudget)
              : "",
        };
    }
  }

  formatTerminationMessage(stage: string, tokens: number, limit: number): string {
    return (
      `[CONTEXT BUDGET EXCEEDED] Stage ${stage} terminated: ` +
      `${tokens.toLocaleString()} input tokens exceeds hard limit of ${limit.toLocaleString()}`
    );
  }

  formatWarningMessage(stage: string, tokens: number, budget: number): string {
    return (
      `[CONTEXT BUDGET WARNING] Stage ${stage} consumed ${tokens.toLocaleString()} input tokens, ` +
      `exceeding budget of ${budget.toLocaleString()}`
    );
  }
}
