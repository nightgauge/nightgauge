/**
 * Budget Enforcer - Hard budget limit enforcement for pipeline stages
 *
 * Encapsulates budget enforcement logic as a pure, testable utility.
 * HeadlessOrchestrator calls checkBudget() and checkOutputTokens() from
 * its onTokenUsage callback.
 *
 * ## Enforcement Modes
 *
 * - **hard**: Terminates stage when cost exceeds budget + grace buffer (default)
 * - **soft**: Warns but never terminates (pre-#835 behavior)
 * - **threshold**: Terminates at configurable N% over budget
 *
 * ## Size-Aware Budgets
 *
 * Default budgets are derived from empirical p90 data (103 pipeline runs,
 * Sonnet 4.6 era pricing). When size is unknown (e.g., issue-pickup),
 * falls back to M (median).
 *
 * @see Issue #947 - Recalibrate budget defaults
 * @see Issue #835 - Enforce hard budget limits
 * @see Issue #638 - Pipeline token efficiency (soft warnings)
 * @see Issue #842 - Cap feature-dev output tokens
 */

import type { PipelineStage } from "@nightgauge/sdk";
import { getCostCapModelScale } from "./resolvers/monitoringResolver";

export type BudgetMode = "hard" | "soft" | "threshold";

/**
 * Optional model/effort info passed to {@link BudgetEnforcer.checkBudget} so
 * the same mode-aware multiplier table that scales the cost-cap kill (#3180,
 * recalibrated post-2026-05-04) also widens the BudgetEnforcer's hard-mode
 * terminate path. Without this, a user in MAXIMUM mode (Opus high-effort)
 * would hit the BudgetEnforcer's pr-create limit ($4.50 effective on size M
 * generous preset) before any real work could land — exactly the failure
 * pattern observed in the 2026-05-04 incident on issue #331.
 */
export interface BudgetModelInfo {
  model?: string;
  effort?: string;
}

export interface BudgetEnforcementDecision {
  shouldTerminate: boolean;
  shouldWarn: boolean;
  /** At wind-down threshold — signal agent to commit work and exit cleanly.
   * @see Issue #2338 - Intelligent budget management */
  shouldWindDown: boolean;
  currentCost: number;
  effectiveLimit: number;
  budgetMode: BudgetMode;
  message: string;
}

/**
 * Enforcement decision for output token limits.
 *
 * @see Issue #842 - Cap feature-dev output tokens
 */
export interface OutputTokenEnforcementDecision {
  shouldTerminate: boolean;
  shouldWarn: boolean;
  currentOutputTokens: number;
  effectiveLimit: number;
  message: string;
}

export type SizeLabel = "XS" | "S" | "M" | "L" | "XL";

export interface SizeAwareBudget {
  XS: number;
  S: number;
  M: number;
  L: number;
  XL: number;
}

/**
 * Ordered size labels for rank comparison.
 */
export const SIZE_RANK: readonly SizeLabel[] = ["XS", "S", "M", "L", "XL"] as const;

/**
 * Planning context data used for budget size resolution.
 * Mirrors the relevant fields from planning-{N}.json.
 *
 * @see Issue #1333 - Planning-aware budget enforcement
 */
export interface PlanningBudgetHint {
  /** Planner's assessed size label (from complexity_assessment.size_label) */
  assessedSize?: SizeLabel | null;
  /** Total file count (files_to_create + files_to_modify) */
  totalFileCount?: number;
}

/**
 * File count thresholds per size level. If the total file count exceeds
 * the threshold for the current effective size, bump up one level.
 *
 * Conservative: only bumps by one level maximum.
 *
 * @see Issue #1333 - Planning-aware budget enforcement
 */
const FILE_COUNT_SIZE_THRESHOLDS: Record<SizeLabel, number> = {
  XS: 3, // >3 files → at least S
  S: 6, // >6 files → at least M
  M: 12, // >12 files → at least L
  L: 25, // >25 files → at least XL
  XL: Infinity, // Can't bump past XL
};

/**
 * Resolve the effective size label for budget enforcement.
 *
 * Takes the GitHub issue label size and optional planning context hints,
 * returns the highest size among all signals. Never downgrades below
 * the issue label.
 *
 * Resolution:
 * 1. Start with GitHub issue label (or 'M' if unknown)
 * 2. If planner assessed a higher size, use that
 * 3. If file count suggests a higher size, bump up one level
 *
 * @param issueSize - Size from GitHub label
 * @param planningHint - Optional planning context data
 * @returns The effective size label for budget enforcement
 *
 * @see Issue #1333 - Planning-aware budget enforcement
 */
export function resolveEffectiveSize(
  issueSize: SizeLabel,
  planningHint?: PlanningBudgetHint | null
): SizeLabel {
  let effectiveRank = SIZE_RANK.indexOf(issueSize);

  // Use planner's assessment if it's higher than the issue label
  if (planningHint?.assessedSize) {
    const assessedRank = SIZE_RANK.indexOf(planningHint.assessedSize);
    if (assessedRank > effectiveRank) {
      effectiveRank = assessedRank;
    }
  }

  // File count sanity check — if file count exceeds threshold for
  // the current effective size, bump up one level
  if (planningHint?.totalFileCount !== undefined) {
    const currentSize = SIZE_RANK[effectiveRank];
    const threshold = FILE_COUNT_SIZE_THRESHOLDS[currentSize];
    if (planningHint.totalFileCount > threshold && effectiveRank < SIZE_RANK.length - 1) {
      effectiveRank += 1;
    }
  }

  return SIZE_RANK[effectiveRank];
}

/**
 * Default per-stage, per-size budgets (USD).
 *
 * Re-baselined in Issue #3269 (post-deterministic-first era). Values are ≤ 2×
 * observed p90 from the 103-run warm-cache baseline (Issue #947), rounded to
 * 2 decimal places. The prior "generous headroom" multiplier (2–3× p90) that
 * caused the "budget warnings on first run" experience has been replaced by
 * the self-calibration loop (CalibrationTable), which tightens per-repo.
 *
 * pr-merge and pr-create caps apply to the LLM fallback path only — when the
 * deterministic-first runner succeeds (Issue #3264) cost ≈ $0, so no cap fires.
 * The budget-vs-actual dashboard panel (Issue #3269) shows both paths separately.
 *
 * Sample: ≥30 runs, post-deterministic-first era, Sonnet 4.6 pricing.
 *
 * These are "when to ask" thresholds, NOT "when to kill" limits.
 * When exceeded, the user is prompted to increase the budget or save work
 * and stop — work is never destroyed without user consent.
 *
 * @see Issue #1935 - Budget-pause instead of budget-kill
 * @see Issue #947  — Original p90 calibration (103 runs, Sonnet 4.6 era)
 * @see Issue #3269 — Re-baseline caps at ≤ 2× observed p90
 * @see Issue #265  — pr-create/pr-merge re-baseline against LLM-fallback actuals
 */
export const DEFAULT_SIZE_AWARE_BUDGETS: Record<string, SizeAwareBudget> = {
  "issue-pickup": { XS: 0.3, S: 0.3, M: 1.5, L: 1.5, XL: 2.0 },
  "feature-planning": { XS: 3.0, S: 4.0, M: 5.0, L: 7.0, XL: 10.0 },
  // feature-dev M re-baselined 16 → 24 (#259): first honest-accounting data
  // (post-#256 — pre-#256 corpora double-counted cumulative result envelopes,
  // so historical p90s are inflated for any stage that emitted more than one)
  // showed a successful M-sized feature-dev at $23.67 REAL, which the old
  // $16 base wound down at $19.20 and killed at $24 with grace. A standard-
  // preset run must be able to finish an observed-successful M stage.
  "feature-dev": { XS: 4.0, S: 8.0, M: 24.0, L: 50.0, XL: 80.0 },
  "feature-validate": { XS: 2.0, S: 4.0, M: 20.0, L: 40.0, XL: 70.0 },
  // pr-create re-baselined again in #265 (post-#259): the #259 ladder
  // (XS/S 0.2, M 0.3, L 1.0, XL 1.5) still undersized the LLM fallback path
  // — the deterministic-first path costs ≈ $0, but on repos where it never
  // engages (observed 0-for-4 on the bowlsheet corpus, tracked separately)
  // every pr-create run pays LLM-fallback cost. Honest-accounting actuals
  // across 4 runs: $1.70 L, $1.97 M, $2.10 M, $2.50 L (the $2.50 run warned
  // against its $2.00 L×generous cap). New STANDARD (1.0×) values give
  // ~30-50% headroom over the worst observed actual per tier:
  //   size:M $3.00 vs $2.10 worst actual = 43% headroom (was $0.30)
  //   size:L $4.00 vs $2.50-3.00 observed range = 33-60% headroom (was $1.00)
  // XS/S/XL have no direct data yet; scaled proportionally to the M/L
  // anchors pending real observations at those tiers.
  "pr-create": { XS: 0.5, S: 1.0, M: 3.0, L: 4.0, XL: 5.5 },
  // pr-merge re-tuned for CI-watching path (#3650 retro of #3646):
  // when CI is flaky or red, pr-merge legitimately watches+investigates+
  // pushes fixes which can easily clear the prior p90 ceiling. The #3646
  // retro showed effectiveLimit = 0.4 × 2.0 (generous) × 1.5 (hard grace)
  // = $1.20 cut off pr-merge mid-investigation at $2.85 actual spend, and
  // the PR ended up stuck open. New ceiling (with generous + grace):
  //   size:S 1.0 × 2.0 × 1.5 = $3.00  (was $1.20)
  //   size:M 1.5 × 2.0 × 1.5 = $4.50  (was $2.40)
  //   size:L 3.0 × 2.0 × 1.5 = $9.00  (was $4.50)
  // These match observed CI-watching spend at p99 across the recent
  // failure corpus while still firing well below the $4.00 stage cost cap
  // for genuine runaways.
  //
  // size:M re-baselined again in #265: bowlsheet #261 (M) hit $4.51 REAL on
  // the CI-watching path, tripping the $4.50 generous+grace ceiling by one
  // cent and forcing an escalation on a healthy run — CI-watching cost
  // scales with CI wall-time, not issue size (compare #240, L, at $4.42 —
  // nearly identical spend at a different size tier). New M generous+grace:
  // 2.0 × 2.0 × 1.5 = $6.00 (was $4.50).
  "pr-merge": { XS: 0.4, S: 1.0, M: 2.0, L: 3.0, XL: 5.0 },
};

/**
 * Named budget presets — convenience multipliers over the standard defaults.
 *
 * @see Issue #947 - Recalibrate budget defaults
 */
export const BUDGET_PRESETS = {
  conservative: {
    multiplier: 0.5,
    description: "Tight budgets for cost-sensitive pipelines",
  },
  standard: {
    multiplier: 1.0,
    description: "Balanced defaults based on p90 empirical data",
  },
  generous: {
    multiplier: 2.0,
    description: "Relaxed budgets for complex or opus-heavy pipelines",
  },
} as const;

export type BudgetPresetName = keyof typeof BUDGET_PRESETS;

/**
 * Returns size-aware budgets scaled by a named preset's multiplier.
 */
export function getBudgetPreset(preset: BudgetPresetName): Record<string, SizeAwareBudget> {
  const { multiplier } = BUDGET_PRESETS[preset];
  const result: Record<string, SizeAwareBudget> = {};
  for (const [stage, budgets] of Object.entries(DEFAULT_SIZE_AWARE_BUDGETS)) {
    result[stage] = {
      XS: +(budgets.XS * multiplier).toFixed(2),
      S: +(budgets.S * multiplier).toFixed(2),
      M: +(budgets.M * multiplier).toFixed(2),
      L: +(budgets.L * multiplier).toFixed(2),
      XL: +(budgets.XL * multiplier).toFixed(2),
    };
  }
  return result;
}

/**
 * Default per-stage, per-size output token limits.
 *
 * Only feature-dev has defaults. Other stages return 0 (no limit).
 * Values based on empirical data: test audit batch (#759, #760, #761)
 * generated 135-150k output tokens each — the limits below cap
 * typical generation while allowing headroom via the grace buffer.
 *
 * @see Issue #842 - Cap feature-dev output tokens
 */
export const DEFAULT_OUTPUT_TOKEN_LIMITS: Record<string, SizeAwareBudget> = {
  "feature-dev": { XS: 15000, S: 25000, M: 50000, L: 100000, XL: 150000 },
};

export const DEFAULT_BUDGET_MODE: BudgetMode = "hard";
export const DEFAULT_GRACE_PERCENT = 50;
/** Default wind-down threshold as % of base budget.
 * @see Issue #2338 - Intelligent budget management */
export const DEFAULT_WINDDOWN_PERCENT = 80;

export interface BudgetEnforcerConfig {
  mode: BudgetMode;
  gracePercent: number;
  /** % of base budget at which wind-down signal fires (default 80).
   * @see Issue #2338 - Intelligent budget management */
  windDownPercent?: number;
  /** Per-stage per-size budget overrides from config */
  stageOverrides?: Record<string, number | Partial<SizeAwareBudget>>;
  /** Per-stage per-size output token limit overrides from config (Issue #842) */
  outputTokenOverrides?: Record<string, number | Partial<SizeAwareBudget>>;
}

/**
 * BudgetEnforcer — pure, deterministic enforcement logic.
 *
 * No process management, no vscode imports. Just budget math.
 */
export class BudgetEnforcer {
  private readonly mode: BudgetMode;
  private readonly gracePercent: number;
  private readonly windDownPercent: number;
  private readonly stageOverrides?: Record<string, number | Partial<SizeAwareBudget>>;
  private readonly outputTokenOverrides?: Record<string, number | Partial<SizeAwareBudget>>;

  /**
   * Runtime overrides applied via user interaction (e.g., "Increase Budget & Continue").
   * These take highest priority — above config overrides and defaults.
   *
   * @see Issue #1935 - Budget-pause instead of budget-kill
   */
  private runtimeOverrides: Record<string, number> = {};

  constructor(config?: Partial<BudgetEnforcerConfig>) {
    this.mode = config?.mode ?? DEFAULT_BUDGET_MODE;
    this.gracePercent = config?.gracePercent ?? DEFAULT_GRACE_PERCENT;
    this.windDownPercent = config?.windDownPercent ?? DEFAULT_WINDDOWN_PERCENT;
    this.stageOverrides = config?.stageOverrides;
    this.outputTokenOverrides = config?.outputTokenOverrides;
  }

  /**
   * Apply a runtime budget override for a stage. This sets the new effective
   * limit directly (no grace buffer applied on top). Takes highest priority.
   *
   * @see Issue #1935 - Budget-pause instead of budget-kill
   */
  applyRuntimeOverride(stage: string, effectiveLimit: number): void {
    // Store as base budget — getEffectiveLimit will add grace on top.
    // We want effectiveLimit to be the actual ceiling, so back out the grace.
    this.runtimeOverrides[stage] = effectiveLimit / (1 + this.gracePercent / 100);
  }

  /**
   * Get the base budget for a stage + size combination.
   *
   * Resolution: runtime override → config override → size-aware default → M fallback.
   */
  getBaseBudget(stage: PipelineStage | string, sizeLabel?: SizeLabel): number {
    const size = sizeLabel ?? "M";

    // Runtime overrides take highest priority (Issue #1935)
    if (this.runtimeOverrides[stage] !== undefined) {
      return this.runtimeOverrides[stage];
    }

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
    const defaults = DEFAULT_SIZE_AWARE_BUDGETS[stage];
    if (defaults) {
      return defaults[size];
    }

    // Unknown stage — no budget
    return 0;
  }

  /**
   * Resolve the mode-aware multiplier applied on top of the configured base
   * budget. Mirrors the same lookup that scales the cost-cap kill so the two
   * limiters can never disagree on whether a heavier model gets headroom.
   */
  private resolveModelScale(modelInfo?: BudgetModelInfo): number {
    if (!modelInfo?.model) return 1.0;
    return getCostCapModelScale(modelInfo.model, modelInfo.effort);
  }

  /**
   * Get the effective limit (budget × model scale + grace buffer).
   */
  getEffectiveLimit(
    stage: PipelineStage | string,
    sizeLabel?: SizeLabel,
    modelInfo?: BudgetModelInfo
  ): number {
    const base = this.getBaseBudget(stage, sizeLabel);
    if (base <= 0) return 0;
    const scaledBase = base * this.resolveModelScale(modelInfo);
    return scaledBase * (1 + this.gracePercent / 100);
  }

  /**
   * Check whether a stage's current cost triggers enforcement.
   *
   * `modelInfo` (optional) widens the effective limit for heavier models —
   * e.g. Opus high-effort gets ~5× headroom over the Sonnet-calibrated base
   * so MAXIMUM mode pipelines aren't terminated mid-flight on legitimate
   * work. Omitting it preserves pre-fix behavior (scale = 1.0) for callers
   * that don't yet thread model context.
   */
  checkBudget(
    stage: PipelineStage | string,
    currentCost: number,
    sizeLabel?: SizeLabel,
    modelInfo?: BudgetModelInfo
  ): BudgetEnforcementDecision {
    const rawBaseBudget = this.getBaseBudget(stage, sizeLabel);

    // No budget configured for this stage
    if (rawBaseBudget <= 0) {
      return {
        shouldTerminate: false,
        shouldWarn: false,
        shouldWindDown: false,
        currentCost,
        effectiveLimit: 0,
        budgetMode: this.mode,
        message: "",
      };
    }

    const scale = this.resolveModelScale(modelInfo);
    // Scale the base FIRST, then apply grace + wind-down %s. This keeps the
    // existing semantic of warn/wind-down/terminate phases — a 5× scale moves
    // all three thresholds proportionally rather than collapsing them.
    const baseBudget = rawBaseBudget * scale;
    const effectiveLimit = baseBudget * (1 + this.gracePercent / 100);
    const windDownThreshold = baseBudget * (this.windDownPercent / 100);

    const overWindDown = currentCost > windDownThreshold;
    const overBaseBudget = currentCost > baseBudget;
    const overEffectiveLimit = currentCost > effectiveLimit;

    switch (this.mode) {
      case "soft":
        // Soft mode: warn + wind-down signal, but never terminate (Issue #2338)
        return {
          shouldTerminate: false,
          shouldWarn: overBaseBudget,
          shouldWindDown: overWindDown && !overBaseBudget,
          currentCost,
          effectiveLimit,
          budgetMode: "soft",
          message: overBaseBudget
            ? this.formatWarningMessage(stage, currentCost, baseBudget)
            : overWindDown
              ? this.formatWindDownMessage(stage, currentCost, windDownThreshold)
              : "",
        };

      case "hard":
        // 3-phase: wind-down (80%) → warn (100%) → terminate (150%) (Issue #2338)
        return {
          shouldTerminate: overEffectiveLimit,
          shouldWarn: overBaseBudget && !overEffectiveLimit,
          shouldWindDown: overWindDown && !overBaseBudget,
          currentCost,
          effectiveLimit,
          budgetMode: "hard",
          message: overEffectiveLimit
            ? this.formatTerminationMessage(stage, currentCost, effectiveLimit)
            : overBaseBudget
              ? this.formatWarningMessage(stage, currentCost, baseBudget)
              : overWindDown
                ? this.formatWindDownMessage(stage, currentCost, windDownThreshold)
                : "",
        };

      case "threshold":
        // In threshold mode, gracePercent acts as the termination threshold
        return {
          shouldTerminate: overEffectiveLimit,
          shouldWarn: overBaseBudget && !overEffectiveLimit,
          shouldWindDown: overWindDown && !overBaseBudget,
          currentCost,
          effectiveLimit,
          budgetMode: "threshold",
          message: overEffectiveLimit
            ? this.formatTerminationMessage(stage, currentCost, effectiveLimit)
            : overBaseBudget
              ? this.formatWarningMessage(stage, currentCost, baseBudget)
              : overWindDown
                ? this.formatWindDownMessage(stage, currentCost, windDownThreshold)
                : "",
        };
    }
  }

  /**
   * Get the base output token limit for a stage + size combination.
   *
   * Resolution: config override → default → 0 (no limit).
   *
   * @see Issue #842 - Cap feature-dev output tokens
   */
  getBaseOutputTokenLimit(stage: PipelineStage | string, sizeLabel?: SizeLabel): number {
    const size = sizeLabel ?? "M";

    // Check config overrides first
    if (this.outputTokenOverrides?.[stage] !== undefined) {
      const override = this.outputTokenOverrides[stage];
      if (typeof override === "number") {
        return override;
      }
      const sizeValue = (override as Partial<SizeAwareBudget>)[size];
      if (sizeValue !== undefined) {
        return sizeValue;
      }
      const mValue = (override as Partial<SizeAwareBudget>).M;
      if (mValue !== undefined) {
        return mValue;
      }
    }

    // Default output token limits
    const defaults = DEFAULT_OUTPUT_TOKEN_LIMITS[stage];
    if (defaults) {
      return defaults[size];
    }

    // Unknown stage or no output token limit — no limit
    return 0;
  }

  /**
   * Get the effective output token limit (base + grace buffer).
   *
   * @see Issue #842 - Cap feature-dev output tokens
   */
  getEffectiveOutputTokenLimit(stage: PipelineStage | string, sizeLabel?: SizeLabel): number {
    const base = this.getBaseOutputTokenLimit(stage, sizeLabel);
    if (base <= 0) return 0;
    return Math.round(base * (1 + this.gracePercent / 100));
  }

  /**
   * Check whether a stage's current output tokens trigger a warning.
   *
   * Output token limits are warn-only — they never terminate a stage.
   * The post-hoc hard limit was removed in Issue #1609 because it destroyed
   * completed work without preventing cost overruns (tokens are already
   * consumed and billed before the check runs). Cost budget enforcement
   * (`checkBudget()`) remains as the safety net for runaway spending.
   *
   * @see Issue #1609 - Remove post-hoc output token hard limit
   * @see Issue #842 - Original cap (now warn-only)
   */
  checkOutputTokens(
    stage: PipelineStage | string,
    currentOutputTokens: number,
    sizeLabel?: SizeLabel
  ): OutputTokenEnforcementDecision {
    const baseLimit = this.getBaseOutputTokenLimit(stage, sizeLabel);
    const effectiveLimit = this.getEffectiveOutputTokenLimit(stage, sizeLabel);

    // No output token limit configured for this stage
    if (baseLimit <= 0) {
      return {
        shouldTerminate: false,
        shouldWarn: false,
        currentOutputTokens,
        effectiveLimit: 0,
        message: "",
      };
    }

    const overBaseLimit = currentOutputTokens > baseLimit;

    return {
      shouldTerminate: false,
      shouldWarn: overBaseLimit,
      currentOutputTokens,
      effectiveLimit,
      message: overBaseLimit
        ? this.formatOutputTokenWarningMessage(stage, currentOutputTokens, baseLimit)
        : "",
    };
  }

  formatOutputTokenTerminationMessage(stage: string, tokens: number, limit: number): string {
    return (
      `[OUTPUT TOKEN LIMIT EXCEEDED] Stage ${stage} terminated: ` +
      `${tokens.toLocaleString()} output tokens exceeds hard limit of ${limit.toLocaleString()}`
    );
  }

  formatOutputTokenWarningMessage(stage: string, tokens: number, limit: number): string {
    return (
      `[OUTPUT TOKEN WARNING] Stage ${stage} generated ${tokens.toLocaleString()} output tokens, ` +
      `exceeding limit of ${limit.toLocaleString()}`
    );
  }

  formatTerminationMessage(stage: string, cost: number, limit: number): string {
    return (
      `[BUDGET EXCEEDED] Stage ${stage} terminated: ` +
      `$${cost.toFixed(2)} exceeds hard limit of $${limit.toFixed(2)}`
    );
  }

  formatWarningMessage(stage: string, cost: number, budget: number): string {
    return (
      `[BUDGET WARNING] Stage ${stage} cost $${cost.toFixed(2)} ` +
      `exceeds budget of $${budget.toFixed(2)}`
    );
  }

  /**
   * Format wind-down message — agent should commit work and exit cleanly.
   * @see Issue #2338 - Intelligent budget management
   */
  formatWindDownMessage(stage: string, cost: number, threshold: number): string {
    return (
      `[BUDGET WIND-DOWN] Stage ${stage} cost $${cost.toFixed(2)} ` +
      `exceeds ${this.windDownPercent}% wind-down threshold ($${threshold.toFixed(2)}). ` +
      `Please commit current work and exit cleanly.`
    );
  }
}

/**
 * Resolve the effective stage cost at the moment of stage-budget termination.
 *
 * Preference order — never fall back to pipeline-total, which is a different
 * scope and would mislead the failure report, the budget-overrun retry context,
 * and the calibration signal:
 *
 * 1. `usage.costUsd` from the streamed Claude CLI message (when > 0)
 * 2. `state.tokens.per_stage[stage].cost_usd` accumulated by `PipelineStateService`
 * 3. 0 — unknown is preferable to lying with a different scope's number
 *
 * @see Issue #3120 - stage-budget overrun reported pipeline-total cost
 * @see Issue #2777 - earlier fix that introduced the conflation
 */
export function resolveStageCostUsd(
  streamedStageCostUsd: number,
  perStageCosts: Record<string, number> | undefined,
  stage: string
): number {
  if (streamedStageCostUsd > 0) return streamedStageCostUsd;
  const fromState = perStageCosts?.[stage];
  if (typeof fromState === "number" && fromState > 0) return fromState;
  return 0;
}
