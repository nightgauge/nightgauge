/**
 * Budget Intelligence — Pre-flight estimation, mid-stage burn rate projection,
 * and diagnostic retro analysis for budget enforcement decisions.
 *
 * Three tiers of budget awareness:
 *
 * 1. **Pre-Flight Budget Gate**: Before any tokens are consumed, estimate the
 *    pipeline cost and compare it to the ceiling. Warn the user if the issue
 *    is likely to exceed the budget.
 *
 * 2. **Budget Retro at Pause**: When budget IS hit, produce a diagnostic
 *    breakdown (per-stage costs, burn rate, compaction, historical comparison)
 *    so the user can make an informed continue-or-stop decision.
 *
 * 3. **Burn Rate Projection**: Track cost-per-second during execution and
 *    project when the ceiling will be hit. Emit early warnings before the
 *    actual threshold is reached.
 *
 * Pure utility — no vscode imports, fully deterministic and testable.
 *
 * @see Issue #1935 - Budget-pause instead of budget-kill
 */

import * as path from "path";
import { resolveMainRepoRoot as resolveMainRepoRootShared } from "./adaptiveBudgetLoader";
import {
  AutoModelSelector,
  StageModelCalibrationService,
  type IssueMetadata,
  type PipelineCostEstimate,
  type Provider,
} from "@nightgauge/sdk";
import type { ClaudeEffort, PipelineStage } from "@nightgauge/sdk";

/**
 * The stages the estimator prices. Mirrors the SDK's PIPELINE_STAGES; declared
 * here so the snapshot can resolve one effort per stage without importing the
 * SDK's internal iteration order.
 */
const ESTIMATOR_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];
import { ExecutionHistoryReader } from "./executionHistoryReader";
import type { NormalizedRunRecord } from "./executionHistoryReader";
import { p75 } from "./adaptiveBudgetLoader";
import { toModelEnvelope } from "./modeProfiles";

/**
 * Resolve the main repository root from a path that may be a worktree.
 *
 * Re-exported from adaptiveBudgetLoader so there is ONE definition (#1017).
 * There were two copies, and both knew about one of the two worktree layouts —
 * see the note on the definition for why that made this estimator calibrate
 * against a near-empty history.
 */
const resolveMainRepoRoot = resolveMainRepoRootShared;

// ============================================================================
// Historical cost calibration (shared by Tier 1 and Tier 2)
// ============================================================================

/**
 * Minimum matching runs before a historical cost is trusted as a calibration
 * input. One sample is an anecdote, and a p75 over two points is just the max.
 */
const MIN_CALIBRATION_SAMPLES = 3;

/**
 * Which cohort of historical runs produced a calibration figure.
 * `"none"` means calibration is OFF — the caller is flying on the static
 * estimate alone.
 */
export type CalibrationSource =
  /** ≥ `MIN_CALIBRATION_SAMPLES` runs of the SAME size — the cohort we want. */
  | "same-size"
  /**
   * Too few same-size runs, so every scored run was used, rescaled from the
   * cohort's typical size into this size's terms by the static table's own
   * size weighting (#1229). Blunter than `same-size`; no longer size-blind.
   */
  | "all-sizes-scaled"
  /**
   * The same widened cohort with NO rescaling, because the caller supplied no
   * size-weighting function. Size-blind: an S issue is projected at the cost of
   * a corpus dominated by L runs. Only ad-hoc callers land here.
   */
  | "all-sizes"
  /** Calibration is OFF — the caller is flying on the static estimate alone. */
  | "none";

/** Historical cost signal for one size bucket, derived from run history. */
export interface HistoricalCalibration {
  /** p75 cost of the matched cohort in USD, or null when nothing matched */
  costUsd: number | null;
  /** Number of historical runs behind `costUsd` */
  sampleCount: number;
  /** Which cohort `costUsd` came from */
  source: CalibrationSource;
}

/**
 * Compute the historical cost signal for a size bucket (#112).
 *
 * **p75, not mean.** The observed cost distribution has a long right tail —
 * one estimate bucket produced actuals spanning $1.66 to $107.02 — so the mean
 * both understates the realistic bad case and lurches on every new outlier.
 * p75 is a stable order statistic that answers the question the budget gate
 * actually asks ("how much does a run like this usually take, worst-case-ish?")
 * and matches the statistic the adaptive stage-budget loader already uses.
 *
 * **Size-null tolerance.** Records written before the size hydration fix carry
 * `size: null`, and runs on issues with no `size:*` label still do. Demanding
 * an exact size match would keep calibration switched off until a full history
 * of labelled records accumulated, so a cohort too small to trust falls back to
 * every scored run instead of to nothing.
 *
 * **The widened cohort is RESCALED, not used raw (#1229).** The old fallback
 * took the p75 of every run whatever its size and projected it onto this issue,
 * justified as "blunter but still better than the uncorrected static estimate".
 * That reasoning is unidirectional: it holds only while the static table
 * under-estimates. It does not hold downward. Observed in a dogfood workspace:
 * an **S** issue was projected at **$29.23** — the all-sizes p75 of a corpus
 * containing a $63.86 L run — against a **$3.42** actual. An 8.5x
 * OVER-estimate, from the correction that exists to fix under-estimates, on a
 * gate whose whole job is to refuse runs it thinks are too expensive.
 *
 * The two inputs are good at different things. The static table is a poor
 * predictor of absolute cost (one bucket produced actuals from $1.66 to
 * $107.02) but it does encode the RELATIVE cost of sizes. The corpus is the
 * opposite: it knows the absolute level and nothing about which size this issue
 * is. So take the level from the corpus and the size weighting from the table —
 * scale the widened p75 by `static(thisSize) / static(cohortTypicalSize)`.
 *
 * Each run is rescaled by its OWN size, so the cohort re-sorts and a large
 * expensive run stops out-ranking the small ones it should sit below — see
 * {@link rescaleCohortToSize} on why one factor applied to the p75 is not the
 * same thing. Unsized runs stay in the cohort, rescaled from the median sized
 * run: dropping them would gut it (32 of 54 scored runs carried `size: null` in
 * the corpus above) and they still carry real level information.
 *
 * Without `staticCostForSize` the scaling cannot be computed and the raw
 * cross-size p75 is returned as before, reported as `"all-sizes"` rather than
 * `"all-sizes-scaled"` so the two are never confused in telemetry.
 */
export function computeHistoricalCalibration(
  runs: Array<{ sizeLabel: string | null; totalCostUsd: number }>,
  sizeLabel: string,
  minSamples: number = MIN_CALIBRATION_SAMPLES,
  staticCostForSize?: (size: string) => number | null
): HistoricalCalibration {
  const scored = runs.filter((c) => c.totalCostUsd > 0);
  const sameSize = scored.filter((c) => c.sizeLabel === sizeLabel);
  const useSameSize = sameSize.length >= minSamples;
  const cohort = useSameSize ? sameSize : scored;

  if (cohort.length < minSamples) {
    return { costUsd: null, sampleCount: cohort.length, source: "none" };
  }

  if (useSameSize) {
    const sorted = cohort.map((c) => c.totalCostUsd).sort((a, b) => a - b);
    return { costUsd: p75(sorted), sampleCount: cohort.length, source: "same-size" };
  }

  // Widened cohort: rescale EACH RUN into this issue's size, then take the p75
  // of the rescaled costs (#1229).
  const rescaled = rescaleCohortToSize(cohort, sizeLabel, staticCostForSize);
  if (rescaled === null) {
    const sorted = cohort.map((c) => c.totalCostUsd).sort((a, b) => a - b);
    return { costUsd: p75(sorted), sampleCount: cohort.length, source: "all-sizes" };
  }
  return {
    costUsd: p75(rescaled.sort((a, b) => a - b)),
    sampleCount: cohort.length,
    source: "all-sizes-scaled",
  };
}

/**
 * Build the size-weighting function `computeHistoricalCalibration` uses to
 * rescale a cross-size cohort (#1229).
 *
 * ONE definition, shared by the pre-flight gate and the budget retro. Those two
 * already carried the comment "same calibration lookup as the pre-flight gate",
 * and they were the same lookup right up until one of them was fixed — which is
 * precisely how a `dual-path-drift` starts. A retro that classifies "anomalous"
 * against a size-blind p75 is wrong in exactly the way the gate was.
 *
 * Every size is priced through the SAME selector under the SAME options,
 * differing only in `size`, so the ratio is a controlled comparison of two
 * static figures rather than two differently-configured ones.
 *
 * `baselineEstimatedCost` — the UNCALIBRATED total by construction — is what a
 * size weighting needs. Reading `totalEstimatedCost` instead would let one
 * calibrated `(stage, model)` cell distort the ratio between two sizes, feeding
 * the calibrated number back into the correction meant to be independent of it.
 */
export function staticSizeWeighting(
  selector: AutoModelSelector,
  snap: EstimatorInputSnapshot,
  skipStages?: string[]
): (size: string) => number | null {
  return (size: string): number | null => {
    try {
      const sized = selector.estimatePipelineCost(
        { ...snap.metadata, size: size as IssueMetadata["size"] },
        {
          skipStages,
          stageModelCalibration: null,
          envelope: toModelEnvelope(snap.mode),
          provider: snap.provider,
          stageEfforts: snap.stageEfforts,
        }
      );
      return sized.unpriced ? null : (sized.baselineEstimatedCost ?? null);
    } catch {
      return null;
    }
  };
}

/**
 * Rescale every run in a cross-size cohort into `sizeLabel`'s terms (#1229):
 * `cost_i * static(target) / static(size_i)`.
 *
 * **Per run, not one factor on the p75.** Scaling the p75 by a single
 * cohort-level ratio looks equivalent and is not, because size and cost are
 * correlated — which is the entire defect. The expensive runs in a mixed cohort
 * are expensive *because* they are large, so they sit in the right tail and the
 * p75 lands on one of them; a constant factor moves that L-sized outlier down
 * but leaves it the 75th-percentile run. Deflating each run by its OWN size
 * first re-sorts the cohort, so an L run at $63.86 enters an S projection as
 * $22.45 and stops out-ranking the S runs it should sit below.
 *
 * Unsized runs are rescaled from the cohort's median sized run — see
 * {@link computeHistoricalCalibration} on why they stay in rather than being
 * dropped.
 *
 * Returns `null` whenever the rescale cannot be done honestly: no weighting
 * function, no sized run to anchor the unsized ones, or a missing/non-positive
 * static figure for the target size. A null means "project the raw p75 and SAY
 * it is unscaled", never "substitute 1.0 and let it read as scaled".
 */
function rescaleCohortToSize(
  cohort: Array<{ sizeLabel: string | null; totalCostUsd: number }>,
  sizeLabel: string,
  staticCostForSize?: (size: string) => number | null
): number[] | null {
  if (!staticCostForSize) return null;

  const typical = medianSizeLabel(cohort);
  if (typical === null) return null;

  const target = staticCostForSize(sizeLabel);
  if (target === null || !(target > 0)) return null;

  // One lookup per DISTINCT size, not per run: `staticCostForSize` prices a
  // whole pipeline through the selector, and a 100-run cohort spans at most
  // five sizes.
  const anchors = new Map<string, number | null>();
  const anchorFor = (size: string): number | null => {
    if (!anchors.has(size)) anchors.set(size, staticCostForSize(size));
    return anchors.get(size) ?? null;
  };

  const out: number[] = [];
  for (const run of cohort) {
    const raw = run.sizeLabel ? run.sizeLabel.toUpperCase() : typical;
    const from = SIZE_ORDER.includes(raw as SizeLabel) ? raw : typical;
    if (from === sizeLabel) {
      // Identity, short-circuited: `cost * target / target` is not exactly
      // `cost` in floating point, and a rescale that should be a no-op must not
      // perturb the number by 4e-15 and make the two paths look different.
      out.push(run.totalCostUsd);
      continue;
    }
    const anchor = anchorFor(from);
    if (anchor === null || !(anchor > 0)) {
      // One unpriceable size must not silently drop a run and shrink the
      // cohort the caller was told the sample count of — carry it unscaled.
      out.push(run.totalCostUsd);
      continue;
    }
    out.push((run.totalCostUsd * target) / anchor);
  }
  return out;
}

/**
 * Median size label among the cohort's SIZED runs, on the ordinal XS<S<M<L<XL
 * scale. Null when no run in the cohort carries a size.
 *
 * Median rather than mean: the scale is ordinal, so "the middle run's size" is
 * meaningful in a way that an averaged size index is not.
 */
function medianSizeLabel(cohort: Array<{ sizeLabel: string | null }>): string | null {
  const ranked = cohort
    .map((c) => (c.sizeLabel ? SIZE_ORDER.indexOf(c.sizeLabel.toUpperCase() as SizeLabel) : -1))
    .filter((i) => i >= 0)
    .sort((a, b) => a - b);
  if (ranked.length === 0) return null;
  return SIZE_ORDER[ranked[Math.floor((ranked.length - 1) / 2)]];
}

/** Ordinal size scale, smallest first. Mirrors the SDK's `ComplexityLabel`. */
const SIZE_ORDER = ["XS", "S", "M", "L", "XL"] as const;
type SizeLabel = (typeof SIZE_ORDER)[number];

/**
 * Human-readable cohort name for a calibration figure.
 *
 * `all-sizes-scaled` names the rescale in the summary the operator actually
 * reads (#1229). Reporting it as plain "all sizes" would hide that the figure
 * was moved into this issue's size — the difference between $29.23 and a
 * projection an operator can act on.
 */
function calibrationCohortLabel(source: CalibrationSource, sizeLabel: string): string {
  if (source === "same-size") return sizeLabel;
  if (source === "all-sizes-scaled") return `all sizes → ${sizeLabel}`;
  return "all sizes";
}

// ============================================================================
// TIER 1: Pre-Flight Budget Gate
// ============================================================================

/**
 * Pinned estimator inputs for one pipeline run (#198).
 *
 * The estimator's math is deterministic; its INPUTS were not — calibration
 * telemetry was re-loaded from disk on every call (any run finishing in
 * between rewrites the bucket), issue labels were passed live (size labels
 * applied by issue-pickup shift complexity → tier → baselines), and the
 * performance mode was re-read from disk. Two estimates for the same issue
 * seconds apart differed by 83% in the dogfood pr-merge deadlock. Capture
 * once per run and reuse for every estimate, warning threshold, and
 * post-run comparison.
 */
export interface EstimatorInputSnapshot {
  /** Issue labels/title as of pipeline start (labels defensively copied) */
  metadata: IssueMetadata;
  /** Per-(stage, model) calibration table as of pipeline start (null when none on disk) */
  stageModelCalibration: Awaited<ReturnType<typeof StageModelCalibrationService.load>>;
  /** Performance mode as of pipeline start (selects the model routing envelope) */
  mode: import("./modeProfiles").PerformanceMode;
  /** ISO timestamp — makes the estimate auditable ("under calibration as-of T") */
  capturedAt: string;
  /**
   * The adapter resolved at pipeline start, and the registry provider it maps
   * to. Pinned for the same reason everything else here is: both are read from
   * config and env, so an estimate taken after a config change would price a
   * run against a provider it never dispatched to (#198, #1213).
   */
  adapter: string;
  provider: Provider;
  /**
   * Per-stage effort resolved from the mode envelope and stage pins — what the
   * run will ACTUALLY dispatch at, not what the size label implies.
   */
  stageEfforts: Partial<Record<string, ClaudeEffort>>;
}

/**
 * Capture the estimator's externally mutable inputs once, at pipeline start.
 */
export async function captureEstimatorInputs(
  metadata: IssueMetadata,
  workspaceRoot: string
): Promise<EstimatorInputSnapshot> {
  const historyRoot = resolveMainRepoRoot(workspaceRoot);
  const calibrationPath = StageModelCalibrationService.getDefaultPath(historyRoot);
  const stageModelCalibration = await StageModelCalibrationService.load(calibrationPath);
  const { getPerformanceMode } = await import("./resolvers/monitoringResolver");
  const mode = getPerformanceMode(historyRoot);

  // Resolved here, in the extension host, rather than inside the SDK: the SDK
  // stays pure and the resolvers are the extension's own config readers.
  const { resolveStageAdapter } = await import("./resolvers/adapterResolver");
  const { resolveStageEffort } = await import("./skillRunner");
  const { providerForAdapter } = await import("@nightgauge/sdk");

  const adapter = resolveStageAdapter("feature-dev" as PipelineStage, workspaceRoot).adapter;
  const stageEfforts: Partial<Record<string, ClaudeEffort>> = {};
  for (const stage of ESTIMATOR_STAGES) {
    const effort = resolveStageEffort(stage, historyRoot, metadata);
    if (effort) stageEfforts[stage] = effort;
  }

  return {
    metadata: { ...metadata, labels: [...(metadata.labels ?? [])] },
    stageModelCalibration,
    mode,
    capturedAt: new Date().toISOString(),
    adapter,
    provider: providerForAdapter(adapter),
    stageEfforts,
  };
}

/**
 * Result of pre-flight budget analysis. Contains the cost estimate,
 * comparison to ceiling, and historical context for similar issues.
 */
/**
 * Which source produced the published `estimatedCost` (#1213).
 *
 * The distinction is the point: before this, `estimatedCost` was ALWAYS the
 * static table's figure, so a reader could not tell a calibrated projection
 * from an uncalibrated one — and the published number never improved with
 * history even though the correction was being computed on every call.
 */
export type BudgetEstimateSource =
  /** The static TOKEN_BASELINES figure — no source qualified. */
  | "static"
  /** p75 of comparable historical runs (≥ 3 samples). */
  | "historical-p75"
  /** Per-(stage, model) calibration cells (≥ 5 samples in a cell). */
  | "stage-model"
  /** The provider serves a band the registry cannot price; there is no number. */
  | "unpriced";

export interface PreFlightBudgetResult {
  /**
   * The PUBLISHED projection — calibrated when a source qualified.
   *
   * This used to be `estimate.totalEstimatedCost` unconditionally, while the
   * p75 that "carries the whole gate" (#112) was computed two lines above and
   * used only for the ceiling warning. Two runs on 2026-08-30: an L/55-file
   * issue estimated at $14.62 landed at $63.86; an S/3-file docs issue
   * estimated at $3.65 landed at $6.74. The direction is always the same
   * because the number never saw the history (#1213).
   */
  estimatedCost: number;
  /** Which source produced `estimatedCost`. @since Issue #1213 */
  estimateSource: BudgetEstimateSource;
  /** The uncalibrated static figure, always retained for comparison. @since Issue #1213 */
  staticEstimatedCost: number;
  /** Effective pipeline ceiling (USD) */
  ceilingUsd: number;
  /** Ratio of estimated cost to ceiling (0.0-1.0+) */
  ceilingRatio: number;
  /** True when estimated cost exceeds the warning threshold (default 80%) */
  shouldWarn: boolean;
  /** Detected issue complexity */
  complexity: string;
  /** Per-stage cost breakdown */
  stages: Array<{
    stage: string;
    cost: number;
    model: string;
    skipped: boolean;
  }>;
  /** p75 cost of comparable issues from history, or null when calibration is off */
  historicalCostUsd: number | null;
  /** Number of historical runs behind `historicalCostUsd` */
  historicalSampleCount: number;
  /** Which cohort produced `historicalCostUsd` ("none" = calibration off) */
  historicalSource: CalibrationSource;
  /** Human-readable summary for the warning notification */
  summary: string;
  /** The pinned inputs this estimate was computed under (#198) */
  snapshot: EstimatorInputSnapshot;
}

/**
 * Run pre-flight budget analysis BEFORE the pipeline consumes any tokens.
 *
 * Uses AutoModelSelector's cost estimation + historical data from past runs
 * to predict whether the issue will exceed the budget ceiling.
 *
 * @param metadata - Issue labels, title, size extracted from gh issue view
 * @param ceilingUsd - The effective pipeline budget ceiling
 * @param workspaceRoot - For reading historical execution data
 * @param skipStages - Stages that will be skipped (from routing)
 * @param warningThreshold - Ratio at which to warn (default 0.8 = 80%)
 */
export async function runPreFlightBudgetCheck(
  metadata: IssueMetadata,
  ceilingUsd: number,
  workspaceRoot: string,
  skipStages?: string[],
  warningThreshold: number = 0.8,
  snapshot?: EstimatorInputSnapshot
): Promise<PreFlightBudgetResult> {
  // Step 1: Get model-based cost estimate under PINNED inputs (#198). The
  // caller captures the snapshot once per run so every estimate, warning
  // threshold, and post-run comparison uses the same calibration table,
  // labels/title, and performance mode; when absent (ad-hoc callers), the
  // inputs are captured here — still one consistent set per call.
  // Issue #3216: the (mode, size) calibration bucket is consulted (with
  // elevated fallback) instead of a flat size lookup.
  const selector = new AutoModelSelector();
  const historyRoot = resolveMainRepoRoot(workspaceRoot);
  const snap = snapshot ?? (await captureEstimatorInputs(metadata, workspaceRoot));
  const estimate: PipelineCostEstimate = selector.estimatePipelineCost(snap.metadata, {
    skipStages,
    stageModelCalibration: snap.stageModelCalibration,
    envelope: toModelEnvelope(snap.mode),
    provider: snap.provider,
    stageEfforts: snap.stageEfforts,
  });

  // Step 2: Calibrate the static estimate against what runs like this have
  // ACTUALLY cost. historyRoot already resolved above (Step 1) from main repo
  // root. This is the correction that carries the whole gate: the static
  // estimate emits ~10 discrete values with essentially no predictive signal
  // (one $2.70 bucket produced actuals from $1.66 to $107.02), so without a
  // historical anchor the projection is noise (#112).
  let calibration: HistoricalCalibration = { costUsd: null, sampleCount: 0, source: "none" };

  try {
    const allCosts = await ExecutionHistoryReader.getCostByIssue(
      historyRoot,
      100 // get enough data for a meaningful percentile
    );
    calibration = computeHistoricalCalibration(
      allCosts,
      estimate.complexity,
      undefined,
      staticSizeWeighting(selector, snap, skipStages)
    );
  } catch (err) {
    console.warn("[Nightgauge] pre-flight calibration lookup failed:", err);
  }

  if (calibration.costUsd === null) {
    // Loud by design. The old code just omitted the historical segment from the
    // summary when nothing matched, so a silently uncalibrated projection was
    // indistinguishable from a calibrated one — which is how a median 3.9x
    // under-estimate survived 112 runs unnoticed (#112).
    console.warn(
      `[Nightgauge] pre-flight cost calibration OFF for ${estimate.complexity}: ` +
        `${calibration.sampleCount} usable historical runs (need ${MIN_CALIBRATION_SAMPLES}). ` +
        `Projecting from the uncorrected static estimate.`
    );
  }

  // Step 3: Pick the source, and PUBLISH what it produced.
  //
  // Order, most-specific first:
  //   unpriced      — the provider serves a band the registry cannot price, so
  //                   there is no number to publish at all.
  //   stage-model   — a (stage, model) cell met its 5-sample threshold; the
  //                   estimate is already the observed p75 per stage.
  //   historical-p75 — ≥ 3 comparable runs, and their p75 exceeds the static
  //                   figure. The static table has never over-estimated in this
  //                   corpus, so a p75 BELOW it means the cohort is not
  //                   comparable rather than that runs got cheap.
  //   static        — nothing qualified.
  //
  // The published number and the ceiling ratio are now the SAME number. They
  // were not: the ratio used the p75 while the published estimate did not, so
  // the gate warned on one figure and the notification rendered another.
  const staticEstimatedCost = estimate.totalEstimatedCost;
  let estimateSource: BudgetEstimateSource;
  let projectedCost: number;
  if (estimate.unpriced) {
    estimateSource = "unpriced";
    projectedCost = staticEstimatedCost;
  } else if (estimate.calibrationUsed) {
    estimateSource = "stage-model";
    projectedCost = staticEstimatedCost;
  } else if (calibration.costUsd !== null && calibration.costUsd > staticEstimatedCost) {
    estimateSource = "historical-p75";
    projectedCost = calibration.costUsd;
  } else {
    estimateSource = "static";
    projectedCost = staticEstimatedCost;
  }

  const ceilingRatio = ceilingUsd > 0 ? projectedCost / ceilingUsd : 0;
  const shouldWarn = ceilingRatio >= warningThreshold;

  // Step 4: Build human-readable summary
  const stages = estimate.stages.map((s) => ({
    stage: s.stage,
    cost: s.estimatedCost,
    model: s.model,
    skipped: s.skipped,
  }));

  let summary =
    estimateSource === "unpriced"
      ? `Estimated cost: UNPRICED (provider ${estimate.provider} has no registry rate for a selected band)`
      : `Estimated cost: $${projectedCost.toFixed(2)} (${estimateSource})`;
  if (calibration.costUsd !== null) {
    const cohort = calibrationCohortLabel(calibration.source, estimate.complexity);
    summary += ` | Historical p75 (${cohort}): $${calibration.costUsd.toFixed(2)} (${calibration.sampleCount} runs)`;
  } else {
    // Never omit the segment — an absent one reads as "calibrated and cheap".
    summary += ` | Historical p75: UNCALIBRATED (${calibration.sampleCount} usable runs)`;
  }
  summary += ` | Ceiling: $${ceilingUsd.toFixed(2)} (${(ceilingRatio * 100).toFixed(0)}%)`;

  if (shouldWarn) {
    summary += `\n\nThis issue is projected to use ${(ceilingRatio * 100).toFixed(0)}% of the budget ceiling.`;
    if (calibration.costUsd && calibration.costUsd > ceilingUsd) {
      const cohort = calibrationCohortLabel(calibration.source, estimate.complexity);
      summary += ` Comparable runs (${cohort}) have historically exceeded the ceiling.`;
    }
    summary += " Consider increasing the ceiling or splitting this issue.";
  }

  return {
    estimatedCost: projectedCost,
    estimateSource,
    staticEstimatedCost,
    ceilingUsd,
    ceilingRatio,
    shouldWarn,
    complexity: estimate.complexity,
    stages,
    historicalCostUsd: calibration.costUsd,
    historicalSampleCount: calibration.sampleCount,
    historicalSource: calibration.source,
    summary,
    snapshot: snap,
  };
}

// ============================================================================
// TIER 2: Budget Retro at Pause
// ============================================================================

/**
 * Diagnostic breakdown produced when a budget limit is hit.
 * Displayed alongside the pause prompt so the user can make an informed decision.
 */
export interface BudgetRetroResult {
  /** Which budget was hit: stage cost, context tokens, or pipeline ceiling */
  budgetType: "stage-cost" | "context-tokens" | "pipeline-ceiling";
  /** Current cost at time of trigger */
  currentCost: number;
  /** The effective limit that was exceeded */
  effectiveLimit: number;
  /** Per-stage cost breakdown (from PipelineStateService) */
  stageCosts: Array<{ stage: string; costUsd: number; percentage: number }>;
  /** The dominant cost stage (highest % of total spend) */
  dominantStage: string;
  /** Dominant stage's share of total cost (0-100) */
  dominantStagePercent: number;
  /** Burn rate in $/minute at the point of budget hit */
  burnRatePerMinute: number;
  /** Whether context compaction was detected during this stage */
  compactionDetected: boolean;
  /** Historical p75 cost for comparable issues, or null when uncalibrated */
  historicalCostUsd: number | null;
  /** How this run's cost compares: 'normal' | 'above-average' | 'anomalous' */
  costAssessment: "normal" | "above-average" | "anomalous";
  /** Human-readable diagnostic for the notification message */
  diagnosticSummary: string;
  /** Actionable recommendation */
  recommendation: string;
}

/**
 * Produce a diagnostic retro analysis when budget is hit.
 *
 * Gathers per-stage cost breakdown, burn rate, compaction status,
 * and historical comparison to explain WHY the budget was exceeded.
 *
 * @param params - Current execution state at the moment of budget hit
 */
export async function buildBudgetRetro(params: {
  budgetType: "stage-cost" | "context-tokens" | "pipeline-ceiling";
  currentCost: number;
  effectiveLimit: number;
  stage: string;
  issueNumber: number;
  stageStartTime: number;
  compactionDetected: boolean;
  sizeLabel: string;
  workspaceRoot: string;
  /** Per-stage costs from PipelineStateService, if available */
  perStageCosts?: Record<string, number>;
}): Promise<BudgetRetroResult> {
  const {
    budgetType,
    currentCost,
    effectiveLimit,
    stage,
    stageStartTime,
    compactionDetected,
    sizeLabel,
    workspaceRoot,
    perStageCosts,
  } = params;

  // Build per-stage cost breakdown
  const totalCost = currentCost;
  const stageCosts: Array<{
    stage: string;
    costUsd: number;
    percentage: number;
  }> = [];

  if (perStageCosts) {
    for (const [stageName, costUsd] of Object.entries(perStageCosts)) {
      if (costUsd > 0) {
        stageCosts.push({
          stage: stageName,
          costUsd,
          percentage: totalCost > 0 ? (costUsd / totalCost) * 100 : 0,
        });
      }
    }
    stageCosts.sort((a, b) => b.costUsd - a.costUsd);
  }

  // Identify dominant cost stage
  const dominantStage = stageCosts.length > 0 ? stageCosts[0].stage : stage;
  const dominantStagePercent = stageCosts.length > 0 ? stageCosts[0].percentage : 100;

  // Calculate burn rate ($/minute)
  const elapsedMs = Date.now() - stageStartTime;
  const elapsedMinutes = elapsedMs / 60_000;
  const burnRatePerMinute = elapsedMinutes > 0 ? currentCost / elapsedMinutes : 0;

  // Historical comparison — use main repo root, not worktree
  const retroHistoryRoot = resolveMainRepoRoot(workspaceRoot);
  let calibration: HistoricalCalibration = { costUsd: null, sampleCount: 0, source: "none" };
  let costAssessment: "normal" | "above-average" | "anomalous" = "normal";

  try {
    const allCosts = await ExecutionHistoryReader.getCostByIssue(retroHistoryRoot, 100);
    // Same calibration lookup as the pre-flight gate (#112) — this comparison
    // was equally dead while every IPC-written record carried a null size, and
    // it takes the same cross-size rescale (#1229). Classifying this run as
    // "anomalous" against a corpus of a different typical size is the same
    // error the gate was making, one path over.
    const retroSnap = await captureEstimatorInputs(
      { labels: [`size:${sizeLabel}`], title: "" },
      workspaceRoot
    );
    calibration = computeHistoricalCalibration(
      allCosts,
      sizeLabel,
      undefined,
      staticSizeWeighting(new AutoModelSelector(), retroSnap)
    );

    // Classify: above 1.5x the historical p75 = above-average, above 2.5x = anomalous
    if (calibration.costUsd !== null && calibration.costUsd > 0) {
      const ratio = currentCost / calibration.costUsd;
      if (ratio > 2.5) costAssessment = "anomalous";
      else if (ratio > 1.5) costAssessment = "above-average";
    }
  } catch {
    // Non-critical
  }

  // Build diagnostic summary
  const lines: string[] = [];

  // Cost breakdown
  if (stageCosts.length > 0) {
    lines.push("Cost breakdown:");
    for (const sc of stageCosts.slice(0, 4)) {
      lines.push(`  ${sc.stage}: $${sc.costUsd.toFixed(2)} (${sc.percentage.toFixed(0)}%)`);
    }
  }

  // Burn rate
  if (burnRatePerMinute > 0) {
    lines.push(
      `Burn rate: $${burnRatePerMinute.toFixed(2)}/min (${elapsedMinutes.toFixed(1)} min elapsed)`
    );
  }

  // Compaction warning
  if (compactionDetected) {
    lines.push("Context compaction detected — this issue is consuming excessive context");
  }

  // Historical comparison
  if (calibration.costUsd !== null) {
    const ratio = currentCost / calibration.costUsd;
    const cohort = calibrationCohortLabel(calibration.source, sizeLabel);
    lines.push(
      `Historical p75 for ${cohort}: $${calibration.costUsd.toFixed(2)} ` +
        `(${calibration.sampleCount} runs; this run: ${ratio.toFixed(1)}x)`
    );
  } else {
    lines.push(
      `Historical p75: UNCALIBRATED (${calibration.sampleCount} usable runs) — no cost baseline to compare against`
    );
  }

  // Build recommendation
  let recommendation: string;
  if (compactionDetected) {
    recommendation = "Issue is too large for a single run. Split into smaller sub-issues.";
  } else if (costAssessment === "anomalous") {
    recommendation =
      "Cost is anomalously high. The agent may be stuck in a retry loop. Consider stopping and investigating.";
  } else if (costAssessment === "above-average") {
    recommendation =
      "Cost is above average for this size. Increasing the budget should let it complete.";
  } else if (dominantStagePercent > 85) {
    recommendation = `${dominantStage} consumed ${dominantStagePercent.toFixed(0)}% of the budget. This stage may need a higher limit for this issue size.`;
  } else {
    recommendation =
      "Budget threshold may be too conservative for this issue. Increasing should let it complete.";
  }

  return {
    budgetType,
    currentCost,
    effectiveLimit,
    stageCosts,
    dominantStage,
    dominantStagePercent,
    burnRatePerMinute,
    compactionDetected,
    historicalCostUsd: calibration.costUsd,
    costAssessment,
    diagnosticSummary: lines.join("\n"),
    recommendation,
  };
}

// ============================================================================
// TIER 3: Burn Rate Projection
// ============================================================================

/**
 * Tracks cost accumulation over time and projects when the ceiling will be hit.
 *
 * Call `recordSample()` on each onTokenUsage callback. Call `getProjection()`
 * to check if projected cost will exceed the ceiling at current burn rate.
 */
export class BurnRateProjector {
  private samples: Array<{ timestampMs: number; costUsd: number }> = [];
  private readonly ceilingUsd: number;
  /** Ratio of projected cost at which to emit early warning (default 0.7 = 70%) */
  private readonly earlyWarningRatio: number;
  /** Minimum samples before making projections (avoids noisy early data) */
  private readonly minSamples: number;

  constructor(ceilingUsd: number, earlyWarningRatio: number = 0.7, minSamples: number = 5) {
    this.ceilingUsd = ceilingUsd;
    this.earlyWarningRatio = earlyWarningRatio;
    this.minSamples = minSamples;
  }

  /**
   * Record a cost sample.
   *
   * @param costUsd - Cumulative cost so far for the series being tracked
   * @param timestampMs - When the sample was observed. Defaults to now, which
   *   is what the live mid-stage caller (`HeadlessOrchestrator`) wants: it
   *   samples an `onTokenUsage` callback as it fires. A caller replaying
   *   *already-recorded* observations must pass each sample's own timestamp —
   *   the dashboard usage panel (Issue #661) replays run history through this
   *   projector, and stamping every historical run `Date.now()` would collapse
   *   the elapsed time the rate is divided by to ~0.
   */
  recordSample(costUsd: number, timestampMs: number = Date.now()): void {
    this.samples.push({ timestampMs, costUsd });
  }

  /**
   * Reset for a new stage.
   */
  reset(): void {
    this.samples = [];
  }

  /**
   * Get current burn rate in $/minute using the last N samples.
   * Uses a sliding window for stability.
   */
  getBurnRatePerMinute(): number {
    if (this.samples.length < 2) return 0;

    // Use last 10 samples for recent burn rate (more responsive to changes)
    const windowSize = Math.min(10, this.samples.length);
    const recent = this.samples.slice(-windowSize);
    const first = recent[0];
    const last = recent[recent.length - 1];

    const elapsedMs = last.timestampMs - first.timestampMs;
    if (elapsedMs <= 0) return 0;

    const costDelta = last.costUsd - first.costUsd;
    return (costDelta / elapsedMs) * 60_000; // Convert to $/minute
  }

  /**
   * Project whether the current burn rate will cause the stage (or pipeline)
   * to exceed the ceiling. Returns an early warning signal.
   *
   * @param currentTotalPipelineCost - Total pipeline cost so far (all stages)
   * @returns Projection result, or null if insufficient data
   */
  getProjection(currentTotalPipelineCost: number): BurnRateProjection | null {
    if (this.samples.length < this.minSamples || this.ceilingUsd <= 0) {
      return null;
    }

    const burnRate = this.getBurnRatePerMinute();
    if (burnRate <= 0) return null;

    const remainingBudget = this.ceilingUsd - currentTotalPipelineCost;
    if (remainingBudget <= 0) {
      // Already exceeded
      return {
        burnRatePerMinute: burnRate,
        projectedMinutesRemaining: 0,
        projectedFinalCost: currentTotalPipelineCost,
        ceilingRatio: currentTotalPipelineCost / this.ceilingUsd,
        shouldWarnEarly: true,
        message: `Budget already exceeded ($${currentTotalPipelineCost.toFixed(2)} / $${this.ceilingUsd.toFixed(2)})`,
      };
    }

    const minutesRemaining = remainingBudget / burnRate;
    const currentStageCost =
      this.samples.length > 0 ? this.samples[this.samples.length - 1].costUsd : 0;
    // Project where total cost will be if current stage continues at this rate for 10 more minutes
    const projectedAdditionalCost = burnRate * 10;
    const projectedFinalCost = currentTotalPipelineCost + projectedAdditionalCost;
    const ceilingRatio = projectedFinalCost / this.ceilingUsd;

    const shouldWarnEarly =
      currentTotalPipelineCost / this.ceilingUsd >= this.earlyWarningRatio || minutesRemaining < 2;

    let message = "";
    if (shouldWarnEarly) {
      message =
        `At current burn rate ($${burnRate.toFixed(2)}/min), ` +
        `budget ceiling will be hit in ~${minutesRemaining.toFixed(1)} minutes. ` +
        `Current: $${currentTotalPipelineCost.toFixed(2)} / $${this.ceilingUsd.toFixed(2)}`;
    }

    return {
      burnRatePerMinute: burnRate,
      projectedMinutesRemaining: minutesRemaining,
      projectedFinalCost,
      ceilingRatio,
      shouldWarnEarly,
      message,
    };
  }
}

export interface BurnRateProjection {
  /** Current burn rate in $/minute */
  burnRatePerMinute: number;
  /** Projected minutes until ceiling is hit at current rate */
  projectedMinutesRemaining: number;
  /** Projected final cost if stage continues at current rate for ~10 more min */
  projectedFinalCost: number;
  /** Ratio of projected cost to ceiling */
  ceilingRatio: number;
  /** Whether to emit an early warning notification */
  shouldWarnEarly: boolean;
  /** Human-readable message (empty if no warning needed) */
  message: string;
}
