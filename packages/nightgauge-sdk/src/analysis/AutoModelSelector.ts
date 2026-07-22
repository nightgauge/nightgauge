/**
 * AutoModelSelector - Complexity-based model selection for pipeline stages
 *
 * Determines the optimal AI model (haiku/sonnet/opus) for a pipeline stage
 * based on issue complexity signals (labels, title, size). This is a
 * deterministic, rule-based service — no AI interpretation.
 *
 * ## Selection Logic
 *
 * 1. Extract complexity from size labels (XS/S/M/L/XL)
 * 2. Map complexity × stage to a model tier via per-stage matrix
 * 3. Lightweight stages always use Haiku regardless of complexity
 * 4. Low-confidence selections are upgraded to the next model tier
 * 5. Optional ComplexityModel patterns adjust complexity before mapping
 * 6. Return structured result with confidence scoring
 *
 * @see Issue #730 - AutoModelSelector service
 * @see Issue #732 - Pipeline integration
 * @see docs/CONFIGURATION.md - Model routing section
 */

import type { ComplexityModel, MatchedPattern } from "../context/schemas/complexity-model.js";
import type { CalibrationMode, CalibrationTable } from "../services/CalibrationService.js";
import { CalibrationService } from "../services/CalibrationService.js";
import { DEFAULT_MODEL_COST_RATES } from "./types.js";

/**
 * Supported model tiers in ascending capability order @since Issue #730
 *
 * `fable` (Fable 5) is the premium frontier tier at ~2× Opus. It is a valid
 * tier but automatic selection never returns it — the auto ceiling is `opus`.
 * Fable is reached only via the `frontier` performance mode or an explicit pick.
 */
export type ModelTier = "haiku" | "sonnet" | "opus" | "fable";

/**
 * Model tiers in ascending capability/cost order. The single source of truth
 * for clamping and stepping between tiers. @since Issue #19
 */
export const MODEL_TIER_ORDER: readonly ModelTier[] = ["haiku", "sonnet", "opus", "fable"] as const;

/**
 * A performance-mode routing envelope: the router selects freely within
 * `[floor, ceiling]`. This replaces fixed per-stage model pins — a mode raises
 * the floor and/or lifts the ceiling, and the adaptive router fills in the rest.
 *
 * `ceiling: "fable"` is the only way automatic routing can reach the frontier
 * tier, and even then only for heavy reasoning stages at L/XL (see
 * `selectModel`'s frontier-reasoning escalation). @since Issue #19
 */
export interface ModelEnvelope {
  floor: ModelTier;
  ceiling: ModelTier;
}

/**
 * Default envelope = today's Elevated behavior: haiku floor, Opus ceiling,
 * Fable unreachable by automatic routing. Callers that pass no envelope get
 * exactly the pre-envelope routing. @since Issue #19
 */
export const DEFAULT_MODEL_ENVELOPE: ModelEnvelope = { floor: "haiku", ceiling: "opus" };

function tierIndex(tier: ModelTier): number {
  return MODEL_TIER_ORDER.indexOf(tier);
}

/**
 * Clamp a tier into `[floor, ceiling]`: raise it to the floor, cap it at the
 * ceiling. Used both inside the selector and by the VS Code resolver chain for
 * non-router paths (lightweight/default) so every mode-driven pick respects the
 * envelope. @since Issue #19
 */
export function clampTier(tier: ModelTier, envelope: ModelEnvelope): ModelTier {
  const clamped = Math.min(
    Math.max(tierIndex(tier), tierIndex(envelope.floor)),
    tierIndex(envelope.ceiling)
  );
  return MODEL_TIER_ORDER[clamped];
}

/** Size complexity labels @since Issue #730 */
export type ComplexityLabel = "XS" | "S" | "M" | "L" | "XL";
/** Claude effort levels for reasoning depth @since Issue #934; `xhigh` for the frontier tier (#73) */
export type ClaudeEffort = "low" | "medium" | "high" | "xhigh";

/** Stage categories for the complexity-to-model matrix @since Issue #730, #942, #1593 */
export type StageCategory =
  "classification" | "planning" | "dev" | "validate" | "lightweight" | "merge";

/** Evidence entry for a pattern that influenced model selection @since Issue #1391 */
export interface PatternInfluenceEvidence {
  /** The regex match pattern string */
  match: string;
  /** Complexity category the pattern belongs to */
  category: "high_complexity" | "medium_complexity" | "low_complexity";
  /** Effective confidence after decay: pattern.confidence × decayFactor */
  effectiveConfidence: number;
  /** How this pattern was applied */
  effect: "escalated" | "downgraded" | "complexity-shift" | "skipped-stale";
}

/** Structured result from model selection @since Issue #730 */
export interface ModelSelectionResult {
  /** Selected model tier */
  model: ModelTier;
  /** Confidence in the selection (0.0-1.0) */
  confidence: number;
  /** Human-readable reasoning for the selection */
  reasoning: string;
  /** Detected issue complexity */
  complexity: ComplexityLabel;
  /** Pipeline stage this selection is for */
  stage: string;
  /** Set when cost-health pressure modified the selection @since Issue #1390 */
  costDowngrade?: {
    applied: boolean;
    shift: number;
    consecutiveLowRuns: number;
  };
  /** Set when cost-per-success metric preferred a cheaper model @since Issue #2458 */
  costPerSuccessRouting?: {
    applied: boolean;
    fromModel: ModelTier;
    toModel: ModelTier;
    fromCostPerSuccess: number;
    toCostPerSuccess: number;
    rationale: string;
  };
  /** Set when pattern confidence directly influenced the model tier @since Issue #1391 */
  patternInfluence?: {
    applied: boolean;
    evidence: PatternInfluenceEvidence[];
  };
}

/** Structured result from deterministic effort derivation @since Issue #934 */
export interface EffortDerivationResult {
  /** Selected effort level */
  effort: ClaudeEffort;
  /** Detected issue complexity used for routing */
  complexity: ComplexityLabel;
  /** Pipeline stage this derivation is for */
  stage: string;
  /** Stage category used by the routing rule */
  stageCategory: StageCategory;
  /** Human-readable reasoning for observability */
  reasoning: string;
}

/**
 * Cost-health signals for conservative model routing during high-cost trends.
 * @since Issue #1390
 */
export interface CostHealthContext {
  /**
   * Recent cost-health dimension scores, oldest first. Values 0–100.
   * Typically the last 5–10 pipeline run snapshots.
   */
  recentScores: number[];
}

/**
 * Historical cost and success data for a single (model, stage) combination.
 * Used to compute cost-per-success for cost-aware routing.
 * @since Issue #2458
 */
export interface ModelStageHistory {
  /** Total cost in USD across all recorded executions for this (model, stage) pair */
  totalCostUsd: number;
  /** Number of successful executions */
  successCount: number;
  /** Total execution count (for minimum sample requirements) */
  totalCount: number;
}

/**
 * Cost-per-success context for cost-aware model routing.
 *
 * Provides historical cost and success data per (model, stage) tuple so the
 * selector can prefer cheaper models when their cost-per-success is comparable
 * to the default selection.
 *
 * ## Key: `"${model}:${stage}"`
 * Example: `"sonnet:feature-planning"`, `"opus:feature-dev"`
 *
 * ## Minimum requirements before applying metric
 * - `totalCount >= minSampleSize` (default 5) — avoids acting on sparse data
 * - `successCount / totalCount >= minSuccessRate` (default 0.70) — avoids
 *   preferring a cheap model that regularly fails
 *
 * @since Issue #2458
 */
export interface CostPerSuccessContext {
  /**
   * Historical data keyed by `"${model}:${stage}"`.
   * Missing keys mean no history is available for that combination.
   */
  history: Record<string, ModelStageHistory>;
  /**
   * Maximum cost-per-success ratio (cheaper / current) below which a cheaper
   * model is preferred. Default: 1.2 (prefer cheaper if its CPS is ≤120% of
   * current model's CPS — i.e., within 20% more expensive per success).
   *
   * A value of 1.0 means exact parity required; higher values allow the
   * cheaper model to be selected even when it costs slightly more per success.
   */
  maxCostRatioThreshold?: number;
  /**
   * Minimum number of executions required before using the metric.
   * Default: 5. Prevents acting on statistically insignificant samples.
   */
  minSampleSize?: number;
  /**
   * Minimum success rate (0.0–1.0) required to use a model for routing.
   * Default: 0.70. Prevents preferring a cheap model that regularly fails.
   */
  minSuccessRate?: number;
}

/** Issue metadata used for complexity assessment @since Issue #730 */
export interface IssueMetadata {
  /** Issue labels (e.g., ['size:M', 'type:feature', 'priority:high']) */
  labels: string[];
  /** Issue title */
  title: string;
  /** Issue description (optional) */
  description?: string;
  /** Pre-computed size label (optional, skips label extraction) */
  size?: ComplexityLabel;
}

/**
 * Issue type labels recognized for type-aware model routing.
 * @since Issue #2400
 */
export type RoutingIssueType = "feature" | "bug" | "docs" | "chore" | "refactor" | "epic";

/**
 * Per-stage model override for a specific issue type.
 * Partial — only specified stages are overridden; others fall through to
 * the complexity×stage matrix.
 * @since Issue #2400
 */
export type TypeStageOverride = Partial<Record<StageCategory, ModelTier>>;

/** Complexity thresholds for model selection @since Issue #730 */
export interface AutoModelSelectorConfig {
  /** Max complexity score for Haiku (0-10, default 3) */
  haikuMax: number;
  /** Max complexity score for Sonnet (0-10, default 6) */
  sonnetMax: number;
  /** Confidence threshold below which model is upgraded one tier (default 0.7) */
  confidenceThreshold: number;
  /** Optional ComplexityModel data for pattern-based adjustments */
  complexityModel?: ComplexityModel;
  /**
   * Maximum sonnetMax shift per cost-pressure cycle (0–10, default 1.0).
   * The per-invocation shift is min(0.5, maxCostDowngradeShift).
   * @since Issue #1390
   */
  maxCostDowngradeShift?: number;
  /**
   * Optional custom stage×complexity→model matrix override.
   * Keys are StageCategory values, values map ComplexityLabel→ModelTier.
   * Missing entries fall back to the built-in STAGE_COMPLEXITY_MATRIX.
   * @since Issue #1590
   */
  stageMatrix?: Partial<Record<StageCategory, Partial<Record<ComplexityLabel, ModelTier>>>>;
  /**
   * Type-aware model overrides. Maps issue type labels to per-stage model
   * selections that take precedence over the complexity×stage matrix.
   *
   * Example: `{ docs: { dev: 'opus' } }` routes type:docs issues to Opus
   * for the feature-dev stage regardless of complexity.
   *
   * Applied after complexity extraction but before cost-health adjustments.
   * Only applies when the issue has a matching `type:*` label.
   * @since Issue #2400
   */
  typeOverrides?: Partial<Record<RoutingIssueType, TypeStageOverride>>;
}

/**
 * Default thresholds.
 *
 * Pricing context (Opus 4.6 era, Feb 2026):
 *   Opus  $5/$25 per MTok  — ~1.7x Sonnet cost
 *   Sonnet $3/$15 per MTok — baseline
 *   Haiku  $1/$5  per MTok — ~0.33x Sonnet cost
 *
 * Sonnet 4.6 is near-Opus quality, so the Sonnet band covers XS through M
 * (scores 1-4). Opus is reserved for L/XL (scores 7-9) where deeper reasoning
 * justifies the premium. The wide sonnet band (sonnetMax 6) ensures M-complexity
 * issues (score 4) stay on Sonnet.
 */
const DEFAULT_THRESHOLDS: AutoModelSelectorConfig = {
  haikuMax: 3,
  sonnetMax: 6,
  confidenceThreshold: 0.7,
};

/** Complexity scores for each size label */
const SIZE_COMPLEXITY_SCORES: Record<ComplexityLabel, number> = {
  XS: 1,
  S: 2,
  M: 4,
  L: 7,
  XL: 9,
};

/** Stages that are lightweight and always use Haiku */
const LIGHTWEIGHT_STAGES = new Set(["pr-create"]);

/**
 * Stages that perform classification/routing decisions and need strong models.
 * Issue-pickup is the decision-making stage — it determines task type, complexity,
 * size label, and model routing for all downstream stages. A $0.10 Sonnet
 * classification that correctly identifies a trivial task saves $4+ by skipping
 * unnecessary stages and using Haiku for execution.
 * @since Issue #1593
 */
const CLASSIFICATION_STAGES = new Set(["issue-pickup"]);

/**
 * Per-stage complexity-to-model mapping matrix.
 *
 * Maps (ComplexityLabel, StageCategory) → ModelTier:
 *
 *                  classif.    planning    dev        validate   lightweight  merge
 *   XS/S           sonnet      sonnet      sonnet     haiku      haiku        haiku
 *   M              sonnet      sonnet      sonnet     sonnet     haiku        haiku
 *   L/XL           sonnet      sonnet      opus       opus       haiku        sonnet
 *
 * Benchmark rationale (Sonnet 4.6 era, Feb–Mar 2026):
 * - Sonnet 4.6 SWE-bench: near-Opus on code generation, sufficient for all planning
 * - Planning is structured document generation; Sonnet handles it at all sizes (#1590)
 * - Classification (issue-pickup) always uses Sonnet: "think expensive, execute cheap".
 *   A $0.10 Sonnet classification that correctly identifies a trivial task saves $4+
 *   by skipping unnecessary stages and using Haiku for execution (#1593)
 * - Haiku validates XS/S adequately: test orchestration is command execution, not reasoning
 * - pr-merge L/XL needs sonnet: complex review feedback requires multi-step reasoning
 *   (GPQA scores show sonnet handles structured analysis at ~95% of opus quality)
 */
const STAGE_COMPLEXITY_MATRIX: Record<StageCategory, Record<ComplexityLabel, ModelTier>> = {
  classification: {
    XS: "sonnet",
    S: "sonnet",
    M: "sonnet",
    L: "sonnet",
    XL: "sonnet",
  },
  planning: {
    XS: "sonnet",
    S: "sonnet",
    M: "sonnet",
    L: "sonnet",
    XL: "sonnet",
  },
  dev: {
    XS: "sonnet",
    S: "sonnet",
    M: "sonnet",
    L: "opus",
    XL: "opus",
  },
  validate: {
    // XS/S raised haiku → sonnet (#197): haiku validation of small issues
    // rubber-stamped dev-stage results ("Trusted from dev stage" — every
    // gate a dev-context pass-through adding no independent signal).
    XS: "sonnet",
    S: "sonnet",
    M: "sonnet",
    L: "opus",
    XL: "opus",
  },
  lightweight: {
    XS: "haiku",
    S: "haiku",
    M: "haiku",
    L: "haiku",
    XL: "haiku",
  },
  // merge is sonnet at every size (#197): the pr-merge LLM path only runs
  // when the deterministic runner punted (blocked merge state, failing
  // checks, dirty state) — a one-line XS diff with a config-blocked merge is
  // exactly the judgment-heavy case. Issue size does not predict punt
  // difficulty, so sizing this row by issue size systematically assigned the
  // weakest model to the hardest instances (bowlsheet#233).
  merge: {
    XS: "sonnet",
    S: "sonnet",
    M: "sonnet",
    L: "sonnet",
    XL: "sonnet",
  },
};

/** Ordered complexity labels for shifting complexity up/down */
const COMPLEXITY_ORDER: ComplexityLabel[] = ["XS", "S", "M", "L", "XL"];

/**
 * Default type-aware model overrides.
 *
 * Rationale:
 * - `docs`: Documentation quality depends heavily on model reasoning ability.
 *   Opus produces significantly better narrative documentation than Sonnet.
 *   Planning also upgraded since doc architecture benefits from deeper reasoning.
 * - `chore`: Mechanical tasks (dependency bumps, config changes, linting fixes)
 *   don't need Sonnet-level reasoning. Haiku handles them adequately.
 * - Other types: no override — fall through to the complexity×stage matrix.
 *
 * @since Issue #2400
 */
const DEFAULT_TYPE_OVERRIDES: Partial<Record<RoutingIssueType, TypeStageOverride>> = {
  docs: {
    planning: "opus",
    dev: "opus",
  },
  chore: {
    dev: "haiku",
    validate: "haiku",
  },
};

/** Per-stage cost estimate @since Issue #948 */
export interface StageCostEstimate {
  stage: string;
  model: ModelTier;
  effort: ClaudeEffort;
  estimatedInputTokens: number;
  estimatedOutputTokens: number;
  estimatedCost: number;
  confidence: number;
  skipped: boolean;
}

/** Full pipeline cost estimate @since Issue #948 */
export interface PipelineCostEstimate {
  stages: StageCostEstimate[];
  totalEstimatedCost: number;
  comparisonAllSonnet: number;
  complexity: ComplexityLabel;
  estimatedAt: string;
  /** Whether calibration data was used instead of static baselines @since calibration integration */
  calibrationUsed?: boolean;
  /** Number of historical samples backing the calibration estimate */
  calibrationSampleCount?: number;
  /** The static baseline estimate (always computed for comparison) */
  baselineEstimatedCost?: number;
}

/**
 * Pipeline stages in execution order.
 * Used by estimatePipelineCost() for per-stage iteration.
 */
const PIPELINE_STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

/**
 * Token baselines per stage × effort level.
 *
 * Calibrated from actual execution history (197 pipeline runs, Feb-Mar 2026).
 * `input` includes all billed input tokens (raw + cache_read + cache_creation).
 * `output` is billed output tokens. These are averages, not aspirational targets.
 *
 * Effort mapping: low → XS/S sizes, medium → M, high → L/XL.
 *
 * @since Issue #948 (originally p90 guesses, recalibrated with real data)
 */
const TOKEN_BASELINES: Record<string, Record<ClaudeEffort, { input: number; output: number }>> = {
  // xhigh rows are UNCALIBRATED extrapolations from the high rows (~1.25×
  // input, ~1.4× output) — the same bootstrap this table started from before
  // real data recalibrated it. xhigh is only reached on router-escalated
  // frontier (Fable) runs (#73); calibration overrides these once
  // MIN_CALIBRATION_SAMPLES real runs exist.
  "issue-pickup": {
    low: { input: 600_000, output: 5_000 },
    medium: { input: 615_000, output: 5_000 },
    high: { input: 670_000, output: 5_100 },
    xhigh: { input: 840_000, output: 7_000 },
  },
  "feature-planning": {
    low: { input: 800_000, output: 6_000 },
    medium: { input: 1_620_000, output: 12_000 },
    high: { input: 1_670_000, output: 11_500 },
    xhigh: { input: 2_100_000, output: 16_000 },
  },
  "feature-dev": {
    low: { input: 1_900_000, output: 10_000 },
    medium: { input: 4_350_000, output: 21_000 },
    high: { input: 5_650_000, output: 30_000 },
    xhigh: { input: 7_000_000, output: 42_000 },
  },
  "feature-validate": {
    low: { input: 750_000, output: 7_000 },
    medium: { input: 875_000, output: 6_500 },
    high: { input: 1_080_000, output: 9_000 },
    xhigh: { input: 1_350_000, output: 12_500 },
  },
  "pr-create": {
    low: { input: 350_000, output: 4_000 },
    medium: { input: 370_000, output: 4_200 },
    high: { input: 400_000, output: 4_600 },
    xhigh: { input: 500_000, output: 6_500 },
  },
  "pr-merge": {
    low: { input: 850_000, output: 5_400 },
    medium: { input: 900_000, output: 5_700 },
    high: { input: 910_000, output: 6_000 },
    xhigh: { input: 1_150_000, output: 8_500 },
  },
};

/**
 * Categorize a pipeline stage into a StageCategory.
 *
 * - `feature-planning` → `planning`
 * - `feature-dev` → `dev`
 * - `feature-validate` → `validate`
 * - `pr-merge` → `merge`
 * - Anything in LIGHTWEIGHT_STAGES → `lightweight`
 * - Unknown stages default to `dev`
 */
function categorizeStage(stage: string): StageCategory {
  if (CLASSIFICATION_STAGES.has(stage)) return "classification";
  if (LIGHTWEIGHT_STAGES.has(stage)) return "lightweight";
  if (stage === "feature-planning") return "planning";
  if (stage === "feature-dev") return "dev";
  if (stage === "feature-validate") return "validate";
  if (stage === "pr-merge") return "merge";
  // Default unknown stages to dev (most capable non-planning category)
  return "dev";
}

/**
 * AutoModelSelector - Deterministic model selection based on issue complexity
 *
 * @example
 * ```typescript
 * const selector = new AutoModelSelector();
 * const result = selector.selectModel('feature-dev', {
 *   labels: ['size:M', 'type:feature'],
 *   title: 'Add user authentication',
 * });
 * // result.model === 'sonnet', result.confidence === 0.9
 * ```
 */
export class AutoModelSelector {
  private readonly config: AutoModelSelectorConfig;

  constructor(config?: Partial<AutoModelSelectorConfig>) {
    this.config = {
      haikuMax: config?.haikuMax ?? DEFAULT_THRESHOLDS.haikuMax,
      sonnetMax: config?.sonnetMax ?? DEFAULT_THRESHOLDS.sonnetMax,
      confidenceThreshold: config?.confidenceThreshold ?? DEFAULT_THRESHOLDS.confidenceThreshold,
      complexityModel: config?.complexityModel,
      stageMatrix: config?.stageMatrix,
      typeOverrides: config?.typeOverrides,
    };
  }

  /**
   * Select the optimal model for a pipeline stage given issue metadata.
   *
   * @param stage - The pipeline stage (e.g., 'feature-dev', 'pr-create')
   * @param metadata - Issue metadata containing labels, title, etc.
   * @param costHealth - Optional cost-health context for conservative routing (Issue #1390)
   * @param costPerSuccessContext - Optional historical cost/success data for cost-aware routing (Issue #2458)
   * @param envelope - Optional performance-mode routing envelope (Issue #19). The
   *   selection is clamped to `[floor, ceiling]`, upgrades cap at `ceiling`, and a
   *   `fable` ceiling enables frontier-reasoning escalation. Defaults to
   *   `DEFAULT_MODEL_ENVELOPE` (haiku..opus) — identical to pre-envelope routing.
   * @returns Structured selection result with model, confidence, and reasoning
   */
  selectModel(
    stage: string,
    metadata: IssueMetadata,
    costHealth?: CostHealthContext,
    costPerSuccessContext?: CostPerSuccessContext,
    envelope: ModelEnvelope = DEFAULT_MODEL_ENVELOPE
  ): ModelSelectionResult {
    const ceiling = envelope.ceiling;

    // Lightweight stages always use Haiku regardless of complexity — clamped to
    // the envelope floor (e.g. Maximum's floor would raise it).
    if (LIGHTWEIGHT_STAGES.has(stage)) {
      const complexity = this.extractComplexity(metadata);
      return {
        model: clampTier("haiku", envelope),
        confidence: 1.0,
        reasoning: `Lightweight stage '${stage}' uses haiku (clamped to envelope ${envelope.floor}..${envelope.ceiling})`,
        complexity,
        stage,
      };
    }

    // Extract base complexity from metadata
    let complexity = this.extractComplexity(metadata);
    const reasoningParts: string[] = [];

    // Apply ComplexityModel pattern adjustments if available
    let patternConfidenceAdjustment = 0;
    let patternEvidence: PatternInfluenceEvidence[] = [];
    let proactiveEscalation = false;
    let proactiveDowngrade = false;
    if (this.config.complexityModel) {
      const matchedPatterns = this.findMatchingPatterns(metadata, this.config.complexityModel);
      if (matchedPatterns.length > 0) {
        const decayFactor = this.computeDecayFactor(this.config.complexityModel);
        const adjusted = this.adjustComplexityFromPatterns(
          complexity,
          matchedPatterns,
          decayFactor
        );
        if (adjusted.complexity !== complexity) {
          reasoningParts.push(
            `ComplexityModel patterns adjusted ${complexity}→${adjusted.complexity}`
          );
          complexity = adjusted.complexity;
        }
        patternConfidenceAdjustment = adjusted.confidenceBoost;
        patternEvidence = adjusted.evidence;
        proactiveEscalation = adjusted.proactiveEscalation;
        proactiveDowngrade = adjusted.proactiveDowngrade;
      }
    }

    // Enforce size label as minimum complexity floor (#1138)
    // When an explicit size label exists, patterns can upgrade but never
    // downgrade below the label. Heuristic-inferred complexity has no floor.
    const labelFloor = this.extractLabelFloor(metadata);
    if (labelFloor) {
      const floorIndex = COMPLEXITY_ORDER.indexOf(labelFloor);
      const adjustedIndex = COMPLEXITY_ORDER.indexOf(complexity);
      if (adjustedIndex < floorIndex) {
        reasoningParts.push(
          `Floor enforcement: ${complexity} < label floor ${labelFloor} → ${labelFloor}`
        );
        complexity = labelFloor;
      }
    }

    const score = SIZE_COMPLEXITY_SCORES[complexity];

    // Use per-stage matrix for model selection (config override → built-in default)
    const stageCategory = categorizeStage(stage);
    let model =
      this.config.stageMatrix?.[stageCategory]?.[complexity] ??
      STAGE_COMPLEXITY_MATRIX[stageCategory][complexity];

    // Type-aware model override (Issue #2400)
    // Applied after matrix lookup: if the issue has a type label with a
    // matching override for this stage category, use that model instead.
    const issueType = this.extractIssueType(metadata);
    const typeOverrideMap = this.config.typeOverrides ?? DEFAULT_TYPE_OVERRIDES;
    const typeOverrideModel = issueType ? typeOverrideMap[issueType]?.[stageCategory] : undefined;
    if (typeOverrideModel) {
      reasoningParts.push(
        `Type override: type:${issueType} → ${typeOverrideModel} for ${stageCategory} stage`
      );
      model = typeOverrideModel;
    }

    // Compute confidence based on signal strength
    let confidence = this.computeConfidence(metadata, complexity);
    confidence = Math.min(1.0, confidence + patternConfidenceAdjustment);

    // Build base reasoning
    const hasSizeLabel = metadata.size || metadata.labels.some((l) => /^size:\w+$/i.test(l));
    const source = hasSizeLabel ? "size label" : "inferred signals";
    reasoningParts.unshift(
      `Complexity ${complexity} (score ${score}) from ${source} → ` +
        `${model} for ${stage} stage (${stageCategory} matrix)`
    );

    // Confidence-weighted tier escalation/downgrade (Issue #1391)
    // Applied after matrix lookup, before cost-health downgrade.
    let patternInfluenceApplied = false;

    if (proactiveEscalation) {
      const escalated = this.upgradeModelTier(model, ceiling);
      if (escalated !== model) {
        const trigger = patternEvidence
          .filter((e) => e.category === "high_complexity" && e.effectiveConfidence > 0.8)
          .map((e) => `"${e.match}" (eff_conf=${e.effectiveConfidence.toFixed(2)})`)
          .join(", ");
        reasoningParts.push(`Pattern-confidence escalation: ${model}→${escalated} [${trigger}]`);
        model = escalated;
        patternInfluenceApplied = true;
      }
    }

    // Proactive downgrade is suppressed when an explicit size label exists
    // (labels take precedence over pattern-driven downgrade)
    if (proactiveDowngrade && !labelFloor) {
      const downgraded = this.downgradeModelTier(model);
      if (downgraded !== model) {
        const trigger = patternEvidence
          .filter((e) => e.category === "low_complexity" && e.effectiveConfidence > 0.8)
          .map((e) => `"${e.match}" (eff_conf=${e.effectiveConfidence.toFixed(2)})`)
          .join(", ");
        reasoningParts.push(`Pattern-confidence downgrade: ${model}→${downgraded} [${trigger}]`);
        model = downgraded;
        patternInfluenceApplied = true;
      }
    }

    // Cost-per-success routing (Issue #2458)
    // When historical cost/success data is available and the selected model is
    // not already the cheapest, check whether a cheaper model has comparable
    // cost-per-success and sufficient sample quality to justify switching.
    let costPerSuccessRouting: ModelSelectionResult["costPerSuccessRouting"];
    if (costPerSuccessContext && model !== "haiku") {
      const cpsResult = this.applyCostPerSuccessRouting(model, stage, costPerSuccessContext);
      if (cpsResult.applied) {
        reasoningParts.push(cpsResult.rationale);
        costPerSuccessRouting = cpsResult;
        model = cpsResult.toModel;
      }
    }

    // Cost-aware conservative downgrade (Issue #1390)
    let costDowngrade: ModelSelectionResult["costDowngrade"];
    if (costHealth && costHealth.recentScores.length > 0) {
      const consecutiveLow = this.computeConsecutiveLowRuns(
        costHealth.recentScores,
        /* threshold */ 40
      );
      if (consecutiveLow >= 3) {
        const maxShift = this.config.maxCostDowngradeShift ?? 1.0;
        const shift = Math.min(0.5, maxShift);

        // Safety floor: L/XL always keep Opus regardless of cost pressure
        const isHighComplexity = complexity === "L" || complexity === "XL";
        let downgradeApplied = false;

        if (!isHighComplexity) {
          // (a) Threshold shift: re-evaluate against shifted sonnetMax
          const effectiveSonnetMax = this.config.sonnetMax + shift;
          if (score <= effectiveSonnetMax && model === "opus") {
            reasoningParts.push(
              `Cost-health degraded (${consecutiveLow} consecutive runs < 40): ` +
                `sonnetMax shifted ${this.config.sonnetMax}→${effectiveSonnetMax}, ` +
                `downgrade ${model}→sonnet`
            );
            model = "sonnet";
            downgradeApplied = true;
          }

          // (b) Suppress low-confidence escalation under cost pressure
          if (
            !downgradeApplied &&
            confidence < this.config.confidenceThreshold &&
            model !== "haiku"
          ) {
            reasoningParts.push(
              `Cost-health degraded (${consecutiveLow} consecutive runs < 40): ` +
                `confidence escalation suppressed, keeping ${model} (no opus upgrade)`
            );
            downgradeApplied = true;
          }
        }

        costDowngrade = {
          applied: downgradeApplied,
          shift,
          consecutiveLowRuns: consecutiveLow,
        };
      }
    }

    // Low-confidence fallback: upgrade model tier
    const suppressEscalation =
      costDowngrade?.applied === true && complexity !== "L" && complexity !== "XL";

    if (confidence < this.config.confidenceThreshold && !suppressEscalation) {
      const upgraded = this.upgradeModelTier(model, ceiling);
      if (upgraded !== model) {
        reasoningParts.push(
          `Low confidence (${confidence.toFixed(2)} < ${this.config.confidenceThreshold}) → ` +
            `upgraded ${model}→${upgraded}`
        );
        model = upgraded;
      }
    }

    // Frontier-reasoning escalation (Issue #19): only when the envelope permits
    // Fable AND the stage is heavy generative reasoning (planning/dev) at L/XL.
    // feature-validate is deliberately EXCLUDED — Fable's extended reasoning is
    // counterproductive for test orchestration and empirically caused validation
    // failures on small tasks in dogfooding. Applied last so the deliberate
    // frontier opt-in overrides cost-health nudges.
    if (
      ceiling === "fable" &&
      (stageCategory === "planning" || stageCategory === "dev") &&
      (complexity === "L" || complexity === "XL") &&
      model !== "fable"
    ) {
      reasoningParts.push(
        `Frontier reasoning escalation: ${model}→fable (${complexity} ${stageCategory})`
      );
      model = "fable";
    }

    // Final envelope clamp (Issue #19): raise to floor, cap at ceiling. The last
    // word — every mode-driven pick lands inside its band.
    const clamped = clampTier(model, envelope);
    if (clamped !== model) {
      reasoningParts.push(
        `Envelope clamp ${envelope.floor}..${envelope.ceiling}: ${model}→${clamped}`
      );
      model = clamped;
    }

    const reasoning = reasoningParts.join("; ");

    // Reconcile costPerSuccessRouting.toModel with the final model tier.
    // Cost-health downgrade (above) runs after CPS routing and may further
    // downgrade `model`. When both apply, update toModel so the field
    // accurately reflects the actual outcome, not the CPS-routing intermediate.
    if (costPerSuccessRouting?.applied && costPerSuccessRouting.toModel !== model) {
      costPerSuccessRouting = {
        ...costPerSuccessRouting,
        toModel: model,
        rationale:
          costPerSuccessRouting.rationale +
          `; further downgraded to ${model} by cost-health pressure`,
      };
    }

    return {
      model,
      confidence,
      reasoning,
      complexity,
      stage,
      ...(costDowngrade !== undefined ? { costDowngrade } : {}),
      ...(costPerSuccessRouting !== undefined ? { costPerSuccessRouting } : {}),
      ...(patternEvidence.length > 0
        ? {
            patternInfluence: {
              applied: patternInfluenceApplied,
              evidence: patternEvidence,
            },
          }
        : {}),
    };
  }

  /**
   * Derive Claude effort for a stage using deterministic complexity rules.
   *
   * Rules:
   * - lightweight stages -> low
   * - planning/dev/validate:
   *   - XS/S -> low
   *   - M -> medium
   *   - L/XL -> high
   *
   * Note: deriveEffort() uses extractComplexity() which returns the label
   * directly (no pattern adjustment), so no floor enforcement is needed here.
   * If pattern adjustments are added to this method in the future, floor
   * enforcement must be applied (see selectModel() for the pattern). (#1138)
   */
  deriveEffort(stage: string, metadata: IssueMetadata): EffortDerivationResult {
    const complexity = this.extractComplexity(metadata);
    const stageCategory = categorizeStage(stage);

    let effort: ClaudeEffort;
    if (stageCategory === "lightweight") {
      effort = "low";
    } else if (complexity === "M") {
      effort = "medium";
    } else if (complexity === "L" || complexity === "XL") {
      effort = "high";
    } else {
      effort = "low";
    }

    return {
      effort,
      complexity,
      stage,
      stageCategory,
      reasoning: `Stage ${stage} (${stageCategory}) with complexity ${complexity} -> effort ${effort}`,
    };
  }

  /**
   * Minimum samples required before calibration data overrides static baselines.
   * Below this threshold, statistical noise makes calibration unreliable.
   */
  private static readonly MIN_CALIBRATION_SAMPLES = 5;

  /**
   * Estimate pipeline cost before execution using model selection + token baselines.
   *
   * When a CalibrationTable is provided with sufficient samples (≥5) for the
   * issue's size bucket, the calibration median replaces the static baseline
   * total. Per-stage breakdown is proportionally scaled to match the calibrated
   * total, preserving the relative stage weights from model selection.
   *
   * Without calibration data (or with insufficient samples), falls back to
   * the original TOKEN_BASELINES computation.
   *
   * @param metadata - Issue metadata for complexity assessment
   * @param skipStages - Stages to skip (e.g., from routing.skip_stages)
   * @param calibration - Optional calibration table from execution history
   * @returns Pre-pipeline cost estimate with per-stage breakdown
   * @since Issue #948, calibration integration
   */
  estimatePipelineCost(
    metadata: IssueMetadata,
    skipStages?: string[],
    calibration?: CalibrationTable | null,
    mode: CalibrationMode = "elevated"
  ): PipelineCostEstimate {
    const skipSet = new Set(skipStages ?? []);
    const complexity = this.extractComplexity(metadata);
    const stages: StageCostEstimate[] = [];
    let totalCost = 0;
    let comparisonAllSonnet = 0;

    for (const stage of PIPELINE_STAGES) {
      if (skipSet.has(stage)) {
        stages.push({
          stage,
          model: "sonnet",
          effort: "low",
          estimatedInputTokens: 0,
          estimatedOutputTokens: 0,
          estimatedCost: 0,
          confidence: 1.0,
          skipped: true,
        });
        continue;
      }

      const modelResult = this.selectModel(stage, metadata);
      const effortResult = this.deriveEffort(stage, metadata);
      const baseline = TOKEN_BASELINES[stage][effortResult.effort];
      const rates = DEFAULT_MODEL_COST_RATES[modelResult.model];
      // Input tokens are dominated by cache reads (~95%), so use cache_read
      // rate for the bulk and raw input rate for a small fraction.
      const cacheReadRate = rates.cacheReadPerMillion ?? rates.inputPerMillion;
      const rawInputFraction = 0.05; // ~5% of input is non-cached
      const effectiveInputRate =
        rawInputFraction * rates.inputPerMillion + (1 - rawInputFraction) * cacheReadRate;
      const cost =
        (baseline.input * effectiveInputRate + baseline.output * rates.outputPerMillion) /
        1_000_000;

      stages.push({
        stage,
        model: modelResult.model,
        effort: effortResult.effort,
        estimatedInputTokens: baseline.input,
        estimatedOutputTokens: baseline.output,
        estimatedCost: cost,
        confidence: modelResult.confidence,
        skipped: false,
      });
      totalCost += cost;

      // All-sonnet comparison
      const sonnetRates = DEFAULT_MODEL_COST_RATES["sonnet"];
      const sonnetCacheReadRate = sonnetRates.cacheReadPerMillion ?? sonnetRates.inputPerMillion;
      const effectiveSonnetInputRate =
        rawInputFraction * sonnetRates.inputPerMillion +
        (1 - rawInputFraction) * sonnetCacheReadRate;
      const sonnetCost =
        (baseline.input * effectiveSonnetInputRate +
          baseline.output * sonnetRates.outputPerMillion) /
        1_000_000;
      comparisonAllSonnet += sonnetCost;
    }

    // Check if calibration data should override the static baseline total
    const baselineTotalCost = totalCost;
    let calibrationUsed = false;
    let calibrationSampleCount = 0;

    if (calibration?.buckets && baselineTotalCost > 0) {
      // Look up the (mode, size) cell with elevated fallback. Schema v2
      // (Issue #3216) keys buckets by mode first; the elevated bucket is
      // the natural baseline when the active mode lacks history.
      const { cell: bucket } = CalibrationService.lookupBucket(calibration, mode, complexity);
      if (
        bucket &&
        bucket.sample_count >= AutoModelSelector.MIN_CALIBRATION_SAMPLES &&
        bucket.median_cost_usd > 0
      ) {
        const calibratedTotal = bucket.median_cost_usd;
        calibrationUsed = true;
        calibrationSampleCount = bucket.sample_count;

        // Scale per-stage costs proportionally so they sum to the calibrated total.
        // This preserves the relative stage cost distribution from model selection
        // while adjusting the absolute values to match historical reality.
        const scaleFactor = calibratedTotal / baselineTotalCost;
        for (const stage of stages) {
          if (!stage.skipped) {
            stage.estimatedCost *= scaleFactor;
          }
        }
        totalCost = calibratedTotal;
      }
    }

    return {
      stages,
      totalEstimatedCost: totalCost,
      comparisonAllSonnet,
      complexity,
      estimatedAt: new Date().toISOString(),
      calibrationUsed,
      calibrationSampleCount,
      baselineEstimatedCost: calibrationUsed ? baselineTotalCost : undefined,
    };
  }

  /**
   * Extract complexity label from issue metadata.
   *
   * Priority:
   * 1. Pre-computed size field
   * 2. size:* label
   * 3. Heuristic from title/label analysis
   */
  private extractComplexity(metadata: IssueMetadata): ComplexityLabel {
    // Use pre-computed size if available
    if (metadata.size) {
      return metadata.size;
    }

    // Extract from size labels (normalize in case of {name: string} objects)
    for (const raw of metadata.labels) {
      const label = typeof raw === "string" ? raw : String((raw as { name?: string })?.name ?? "");
      if (!label) continue;
      const match = label.match(/^size:(\w+)$/i);
      if (match) {
        const size = match[1].toUpperCase();
        if (size in SIZE_COMPLEXITY_SCORES) {
          return size as ComplexityLabel;
        }
      }
    }

    // Heuristic fallback: infer from other signals
    return this.inferComplexityFromSignals(metadata);
  }

  /**
   * Extract the explicit size label floor from metadata.
   *
   * Returns the complexity label from `metadata.size` or a `size:*` label,
   * or `null` if no explicit label exists. Heuristic-inferred complexity
   * does not produce a floor — it is already a best guess.
   *
   * @since Issue #1138
   */
  private extractLabelFloor(metadata: IssueMetadata): ComplexityLabel | null {
    if (metadata.size) {
      return metadata.size;
    }

    for (const raw of metadata.labels) {
      const label = typeof raw === "string" ? raw : String((raw as { name?: string })?.name ?? "");
      if (!label) continue;
      const match = label.match(/^size:(\w+)$/i);
      if (match) {
        const size = match[1].toUpperCase();
        if (size in SIZE_COMPLEXITY_SCORES) {
          return size as ComplexityLabel;
        }
      }
    }

    return null;
  }

  /**
   * Extract the issue type from `type:*` labels.
   *
   * Returns the first recognized type, or undefined if no type label is present.
   * Normalizes label format: handles both string labels and {name} objects.
   *
   * @since Issue #2400
   */
  extractIssueType(metadata: IssueMetadata): RoutingIssueType | undefined {
    const validTypes: RoutingIssueType[] = ["feature", "bug", "docs", "chore", "refactor", "epic"];
    for (const raw of metadata.labels) {
      const label = typeof raw === "string" ? raw : String((raw as { name?: string })?.name ?? "");
      if (!label) continue;
      const match = label.match(/^type:(\w+)$/i);
      if (match) {
        const typeName = match[1].toLowerCase();
        if (validTypes.includes(typeName as RoutingIssueType)) {
          return typeName as RoutingIssueType;
        }
      }
    }
    return undefined;
  }

  /**
   * Detect whether an issue is a foundation/scaffolding task.
   *
   * Foundation tasks are greenfield setup issues (e.g., "Initialize npm
   * workspaces", "Configure vitest") that should skip planning and use
   * relaxed validation because no existing patterns or tests exist yet.
   *
   * @returns true if the issue matches foundation task patterns
   * @see Issue #1318 - Foundation task type routing
   */
  isFoundationTask(metadata: IssueMetadata): boolean {
    const titleLower = metadata.title.toLowerCase();
    const labels = metadata.labels.map((l) => l.toLowerCase());
    const isChore = labels.some((l) => l.includes("chore"));

    const foundationKeywords = [
      "scaffold",
      "foundation",
      "setup",
      "bootstrap",
      "initialize",
      "configure",
      "init monorepo",
      "init workspace",
    ];
    const hasFoundationTitle = foundationKeywords.some((k) => titleLower.includes(k));

    // type:chore + foundation keyword is a strong signal
    if (isChore && hasFoundationTitle) return true;

    // Strong foundation phrases even without chore label
    const strongFoundation = [
      "initialize monorepo",
      "initialize npm",
      "setup typescript",
      "setup vitest",
      "setup eslint",
      "scaffold project",
      "bootstrap project",
      "configure ci",
      "configure github actions",
    ];
    return strongFoundation.some((k) => titleLower.includes(k));
  }

  /**
   * Infer complexity when no explicit size label is present.
   * Uses title keywords and label signals.
   */
  private inferComplexityFromSignals(metadata: IssueMetadata): ComplexityLabel {
    const titleLower = metadata.title.toLowerCase();
    const labels = metadata.labels.map((l) => l.toLowerCase());

    // Foundation tasks route to S (skip planning, relaxed validation) (#1318)
    if (this.isFoundationTask(metadata)) return "S";

    // Priority signals
    const hasCritical = labels.some((l) => l.includes("critical"));
    const hasHigh = labels.some((l) => l.includes("priority:high"));

    // Type signals
    const isBug = labels.some((l) => l.includes("bug"));
    const isDocs = labels.some((l) => l.includes("docs") || l.includes("documentation"));
    const isChore = labels.some((l) => l.includes("chore"));
    const isRefactor = labels.some((l) => l.includes("refactor"));

    // Title complexity signals
    const complexKeywords = ["refactor", "redesign", "overhaul", "migrate", "architect"];
    const simpleKeywords = ["fix", "typo", "rename", "update", "bump", "minor"];

    const hasComplexTitle = complexKeywords.some((k) => titleLower.includes(k));
    const hasSimpleTitle = simpleKeywords.some((k) => titleLower.includes(k));

    if (isDocs || isChore) return "S";
    if (isBug && hasSimpleTitle) return "S";
    if (hasCritical || hasComplexTitle) return "L";
    if (isRefactor) return "M";
    if (hasHigh) return "M";
    if (hasSimpleTitle) return "S";

    // Default: S (small) — execution data shows 90%+ of unlabeled issues
    // land as XS by actual lines changed (avg ~59 lines). S is conservative
    // and within adjacent-size tolerance of both XS and M.
    return "S";
  }

  /**
   * Compute cost-per-success for a (model, stage) pair from historical data.
   *
   * Returns null when:
   * - No history entry exists for this combination
   * - totalCount < minSampleSize (insufficient data)
   * - successCount === 0 (no successes → CPS is infinite)
   * - success rate < minSuccessRate (model is too unreliable)
   *
   * @since Issue #2458
   */
  private computeCostPerSuccess(
    model: ModelTier,
    stage: string,
    context: CostPerSuccessContext,
    minSampleSize: number,
    minSuccessRate: number
  ): number | null {
    const key = `${model}:${stage}`;
    const entry = context.history[key];
    if (!entry) return null;
    if (entry.totalCount < minSampleSize) return null;
    if (entry.successCount === 0) return null;
    const successRate = entry.successCount / entry.totalCount;
    if (successRate < minSuccessRate) return null;
    return entry.totalCostUsd / entry.successCount;
  }

  /**
   * Apply cost-per-success routing to potentially switch to a cheaper model.
   *
   * Checks cheaper model tiers in order (opus→sonnet→haiku) to find the
   * cheapest model whose cost-per-success is within the threshold of the
   * current model's CPS. Returns the routing result including whether a
   * switch was applied and the cost delta rationale.
   *
   * @since Issue #2458
   */
  private applyCostPerSuccessRouting(
    currentModel: ModelTier,
    stage: string,
    context: CostPerSuccessContext
  ): NonNullable<ModelSelectionResult["costPerSuccessRouting"]> {
    const maxRatio = context.maxCostRatioThreshold ?? 1.2;
    const minSample = context.minSampleSize ?? 5;
    const minRate = context.minSuccessRate ?? 0.7;

    const noOp: NonNullable<ModelSelectionResult["costPerSuccessRouting"]> = {
      applied: false,
      fromModel: currentModel,
      toModel: currentModel,
      fromCostPerSuccess: 0,
      toCostPerSuccess: 0,
      rationale: "",
    };

    const currentCps = this.computeCostPerSuccess(currentModel, stage, context, minSample, minRate);
    if (currentCps === null) return noOp;

    // Candidate cheaper tiers ordered from most to least capable
    const cheaperTiers: ModelTier[] = currentModel === "opus" ? ["sonnet", "haiku"] : ["haiku"];

    for (const candidate of cheaperTiers) {
      const candidateCps = this.computeCostPerSuccess(
        candidate,
        stage,
        context,
        minSample,
        minRate
      );
      if (candidateCps === null) continue;

      const ratio = candidateCps / currentCps;
      if (ratio <= maxRatio) {
        // Compute cost delta description — candidate may be cheaper or slightly
        // more expensive (allowed up to maxRatio). Use direction-aware label.
        const diff = Math.abs(currentCps - candidateCps);
        const pct = Math.abs(((currentCps - candidateCps) / currentCps) * 100);
        const costLabel =
          candidateCps <= currentCps
            ? `$${diff.toFixed(4)} (${pct.toFixed(1)}%) cheaper per success`
            : `$${diff.toFixed(4)} (${pct.toFixed(1)}%) more expensive per success but within threshold`;
        return {
          applied: true,
          fromModel: currentModel,
          toModel: candidate,
          fromCostPerSuccess: currentCps,
          toCostPerSuccess: candidateCps,
          rationale:
            `Cost-per-success: selected ${candidate} ($${candidateCps.toFixed(4)}/success) ` +
            `over ${currentModel} ($${currentCps.toFixed(4)}/success) ` +
            `— ${costLabel} (ratio ${ratio.toFixed(2)} ≤ ${maxRatio})`,
        };
      }
    }

    return noOp;
  }

  /**
   * Count consecutive tail values below threshold (newest first, from end of array).
   * Returns 0 if recentScores is empty.
   */
  private computeConsecutiveLowRuns(scores: number[], threshold: number): number {
    let count = 0;
    for (let i = scores.length - 1; i >= 0; i--) {
      if (scores[i] < threshold) count++;
      else break;
    }
    return count;
  }

  /**
   * Map a complexity score to a model tier using thresholds.
   */
  private scoreToModel(score: number): ModelTier {
    if (score <= this.config.haikuMax) return "haiku";
    if (score <= this.config.sonnetMax) return "sonnet";
    return "opus";
  }

  /**
   * Compute confidence based on signal strength.
   *
   * High confidence when explicit size label is present.
   * Lower confidence when inferring from other signals.
   */
  private computeConfidence(metadata: IssueMetadata, _complexity: ComplexityLabel): number {
    // Explicit size label or pre-computed → high confidence
    if (metadata.size) return 0.95;

    const hasSizeLabel = metadata.labels.some((l) => /^size:\w+$/i.test(l));
    if (hasSizeLabel) return 0.9;

    // Priority label present → moderate confidence
    const hasPriority = metadata.labels.some((l) => l.startsWith("priority:"));
    if (hasPriority) return 0.7;

    // Type label only → lower confidence
    const hasType = metadata.labels.some((l) => l.startsWith("type:"));
    if (hasType) return 0.6;

    // No useful labels → low confidence
    return 0.4;
  }

  /**
   * Compute the exponential decay factor for a ComplexityModel.
   *
   * Returns a value in (0, 1] where 1.0 means no decay (either decay is
   * disabled or last_updated is in the future). Uses a half-life formula:
   *   factor = 0.5 ^ (days_since_updated / half_life_days)
   *
   * @since Issue #1391
   */
  private computeDecayFactor(model: ComplexityModel): number {
    if (!model.decay.enabled) return 1.0;
    const now = new Date();
    const lastUpdated = new Date(model.last_updated);
    const daysSince = (now.getTime() - lastUpdated.getTime()) / (1000 * 60 * 60 * 24);
    // Cap at 1.0 to handle future-dated last_updated gracefully
    return Math.min(1.0, Math.pow(0.5, daysSince / model.decay.half_life_days));
  }

  /**
   * Downgrade a model tier by one level.
   * fable -> opus, opus -> sonnet, sonnet -> haiku, haiku -> haiku (floor)
   *
   * @since Issue #1391
   */
  private downgradeModelTier(model: ModelTier): ModelTier {
    switch (model) {
      case "fable":
        return "opus";
      case "opus":
        return "sonnet";
      case "sonnet":
        return "haiku";
      case "haiku":
        return "haiku";
    }
  }

  /**
   * Upgrade a model tier by one level, capped at `ceiling`.
   *
   * The ceiling defaults to `opus`: with no envelope, the auto-selector still
   * never escalates opus -> fable, because Fable is the premium frontier tier
   * (~2× Opus) and must only be entered via an explicit opt-in. A caller that
   * passes `ceiling: "fable"` (the `frontier` envelope) lifts that cap so
   * escalation can reach Fable. An already-at-or-above-ceiling tier is returned
   * unchanged, keeping the function total. @since Issue #19 (added ceiling)
   */
  private upgradeModelTier(model: ModelTier, ceiling: ModelTier = "opus"): ModelTier {
    const next = Math.min(tierIndex(model) + 1, tierIndex(ceiling));
    return MODEL_TIER_ORDER[Math.max(tierIndex(model), next)];
  }

  /**
   * Find matching patterns from a ComplexityModel against issue text.
   *
   * Checks regex patterns from high_complexity, medium_complexity, and
   * low_complexity categories against the concatenated title + description.
   */
  private findMatchingPatterns(
    metadata: IssueMetadata,
    complexityModel: ComplexityModel
  ): MatchedPattern[] {
    const text = `${metadata.title} ${metadata.description ?? ""}`.toLowerCase();
    const matched: MatchedPattern[] = [];

    const categories = ["high_complexity", "medium_complexity", "low_complexity"] as const;

    for (const category of categories) {
      const patterns = complexityModel.patterns[category];
      for (const pattern of patterns) {
        try {
          const regex = new RegExp(pattern.match, "i");
          const regexMatch = text.match(regex);
          if (regexMatch) {
            matched.push({
              pattern,
              category,
              matched_text: regexMatch[0],
            });
          }
        } catch {
          // Skip invalid regex patterns silently
        }
      }
    }

    return matched;
  }

  /**
   * Adjust complexity label based on matched ComplexityModel patterns.
   *
   * - high_complexity matches -> boost complexity toward L/XL
   * - low_complexity matches -> reduce complexity toward XS/S
   * - medium_complexity matches -> no shift (but may boost confidence)
   * - Stale patterns (effectiveConfidence < 0.3) are skipped entirely
   *
   * Returns adjusted complexity, confidence boost, proactive tier flags, and
   * per-pattern evidence for reasoning and debugging.
   *
   * @param decayFactor - Model-level decay factor (0, 1]; 1.0 = no decay
   * @since Issue #1391 — added decayFactor, proactive escalation/downgrade, evidence
   */
  private adjustComplexityFromPatterns(
    baseComplexity: ComplexityLabel,
    matchedPatterns: MatchedPattern[],
    decayFactor: number
  ): {
    complexity: ComplexityLabel;
    confidenceBoost: number;
    proactiveEscalation: boolean;
    proactiveDowngrade: boolean;
    evidence: PatternInfluenceEvidence[];
  } {
    let currentIndex = COMPLEXITY_ORDER.indexOf(baseComplexity);
    let totalEffectiveConfidence = 0;
    let patternCount = 0;
    let proactiveEscalation = false;
    let proactiveDowngrade = false;
    const evidence: PatternInfluenceEvidence[] = [];

    for (const mp of matchedPatterns) {
      const effectiveConfidence = mp.pattern.confidence * decayFactor;

      // Skip stale patterns (Issue #1391)
      if (effectiveConfidence < 0.3) {
        evidence.push({
          match: mp.pattern.match,
          category: mp.category,
          effectiveConfidence,
          effect: "skipped-stale",
        });
        continue;
      }

      patternCount++;
      totalEffectiveConfidence += effectiveConfidence;

      if (mp.category === "high_complexity") {
        // Shift complexity up by one level per high_complexity match
        currentIndex = Math.min(currentIndex + 1, COMPLEXITY_ORDER.length - 1);
        // High-confidence high_complexity → proactive tier escalation
        if (effectiveConfidence > 0.8) {
          proactiveEscalation = true;
        }
      } else if (mp.category === "low_complexity") {
        // Shift complexity down by one level per low_complexity match
        currentIndex = Math.max(currentIndex - 1, 0);
        // High-confidence low_complexity → proactive tier downgrade
        if (effectiveConfidence > 0.8) {
          proactiveDowngrade = true;
        }
      }
      // medium_complexity: no shift, but contributes to confidence

      evidence.push({
        match: mp.pattern.match,
        category: mp.category,
        effectiveConfidence,
        effect: "complexity-shift",
      });
    }

    // Average effective pattern confidence provides a small confidence boost
    const avgEffectiveConfidence = patternCount > 0 ? totalEffectiveConfidence / patternCount : 0;
    const confidenceBoost = avgEffectiveConfidence * 0.1; // Max +0.1 from patterns

    return {
      complexity: COMPLEXITY_ORDER[currentIndex],
      confidenceBoost,
      proactiveEscalation,
      proactiveDowngrade,
      evidence,
    };
  }
}
