/**
 * Token Economics Dimension Analyzer
 *
 * Evaluates token usage efficiency across pipeline stages including
 * cache hit rates, waste detection, and usage trends.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
} from "../types.js";
import { getHealthStatus, DEFAULT_CACHE_THRESHOLD } from "../types.js";
import {
  computePercentile,
  computeTrend,
  mean,
  clamp,
  hasEnoughData,
  buildPeriodComparison,
} from "../statistics.js";

// ── Internal helpers ───────────────────────────────────────────────

/** Total tokens for a single execution record. */
function totalTokens(r: {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
}): number {
  return r.inputTokens + r.outputTokens + (r.cacheReadTokens ?? 0) + (r.cacheCreationTokens ?? 0);
}

/**
 * Cache hit rate for a single record.
 * Defined as cacheReadTokens / (cacheReadTokens + inputTokens).
 * Returns null when no prompt tokens exist (avoids division-by-zero noise).
 */
function cacheHitRate(r: { inputTokens: number; cacheReadTokens?: number }): number | null {
  const cacheRead = r.cacheReadTokens ?? 0;
  const denominator = cacheRead + r.inputTokens;
  if (denominator === 0) return null;
  return cacheRead / denominator;
}

/**
 * Canonical per-stage cache-hit rate (Issue #3804, ADR-003).
 *
 * `cache_read / (cache_read + cache_creation + input)` — the same formula the
 * pipeline-audit skill (SKILL.md:404) and `TokenEfficiencyAnalyzer` use, so the
 * health dimension and the audit surface agree by construction.
 *
 * Sums across the supplied records (stage-grouped). Returns `null` when the
 * denominator is zero (no cacheable input — e.g. a skipped deterministic
 * stage), so such stages are reported as "no data" and never trigger a false
 * low-reuse finding.
 */
function stageCacheHitRate(
  records: Array<{
    inputTokens: number;
    cacheReadTokens?: number;
    cacheCreationTokens?: number;
  }>
): number | null {
  let cacheRead = 0;
  let cacheCreation = 0;
  let input = 0;
  for (const r of records) {
    cacheRead += r.cacheReadTokens ?? 0;
    cacheCreation += r.cacheCreationTokens ?? 0;
    input += r.inputTokens;
  }
  const denominator = cacheRead + cacheCreation + input;
  if (denominator === 0) return null;
  return cacheRead / denominator;
}

/** Resolve the cache-hit threshold for a stage, honoring per-stage overrides. */
function resolveCacheThreshold(config: HealthAnalysisConfig, stage: string): number {
  const cfg = config.cacheThresholds;
  if (!cfg) return DEFAULT_CACHE_THRESHOLD;
  return cfg.byStage?.[stage] ?? cfg.default;
}

// ── Main Analyzer ──────────────────────────────────────────────────

export function analyzeTokenEconomics(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const records = dataset.executionHistory;
  const sampleSize = records.length;
  const minSamples = config.minimumSampleSizes.basic;

  // Early return — no data at all
  if (sampleSize === 0) {
    return {
      dimension: "token-economics",
      score: 100,
      status: "excellent",
      findings: [],
      metrics: {},
      hasEnoughData: false,
      sampleSize: 0,
    };
  }

  // Early return — insufficient data for meaningful analysis
  if (!hasEnoughData(sampleSize, minSamples)) {
    const avgTotal = mean(records.map(totalTokens));
    return {
      dimension: "token-economics",
      score: 100,
      status: "excellent",
      findings: [],
      metrics: { avgTotalTokensPerRun: avgTotal, sampleSize },
      hasEnoughData: false,
      sampleSize,
    };
  }

  const findings: Finding[] = [];
  let findingIndex = 0;
  let score = 100;

  // ── 1. Cache hit rate ────────────────────────────────────────────

  const cacheRates = records.map(cacheHitRate).filter((r): r is number => r !== null);

  const avgCacheHitRate = cacheRates.length > 0 ? mean(cacheRates) : 0;
  const hasCacheData = cacheRates.length > 0;

  if (hasCacheData && avgCacheHitRate < 0.3) {
    score -= 20;
    findings.push({
      id: `te-${++findingIndex}`,
      dimension: "token-economics",
      severity: avgCacheHitRate < 0.1 ? "high" : "medium",
      title: "Low cache hit rate",
      description: `Average cache hit rate is ${(avgCacheHitRate * 100).toFixed(1)}%, well below the 30% target. Most prompt tokens are not being served from cache.`,
      impact:
        "Higher input token costs and slower stage execution due to repeated context injection.",
      recommendation:
        "Enable or expand prompt caching for frequently repeated context blocks (system prompts, large file reads). Review whether cacheCreationTokens are being populated.",
      evidence: {
        avgCacheHitRate,
        recordsWithCacheData: cacheRates.length,
        totalRecords: sampleSize,
      },
      confidence: cacheRates.length >= minSamples ? "high" : "medium",
    });
  }

  // ── 1b. Per-stage cache hit rate (Issue #3804) ──────────────────
  //
  // Group records by stage and compute the canonical per-stage cache-hit
  // rate (cache_read / (cache_read + cache_creation + input)). Stages below
  // their resolved threshold (with enough samples) emit a low-reuse finding.
  // Stages with zero cacheable input report `null` and never trigger one.
  //
  // In the production VSCode path, runs are mapped to a single "pipeline"
  // stage record (see buildHealthInput.ts), so this collapses to one entry
  // mirroring the global rate. When stage-granular records are supplied
  // (tests, future callers), it produces a true per-stage breakdown.

  const recordsByStage = new Map<string, typeof records>();
  for (const r of records) {
    const existing = recordsByStage.get(r.stage) ?? [];
    existing.push(r);
    recordsByStage.set(r.stage, existing);
  }

  const perStageCacheHitRate: Record<string, number | null> = {};
  for (const [stage, stageRecords] of recordsByStage) {
    const rate = stageCacheHitRate(stageRecords);
    perStageCacheHitRate[stage] = rate;

    // Skip stages with no cacheable input or too few samples for a finding.
    if (rate === null) continue;
    if (stageRecords.length < minSamples) continue;

    const threshold = resolveCacheThreshold(config, stage);
    if (rate >= threshold) continue;

    // Diagnostic finding only — it names *which* stage is under-caching. The
    // score penalty for low cache reuse is already applied once by the global
    // `avgCacheHitRate` check above; deducting again here would double-count
    // the same root cause. The per-stage finding adds granularity, not weight.
    findings.push({
      id: `te-${++findingIndex}`,
      dimension: "token-economics",
      severity: rate < 0.1 ? "high" : "medium",
      title: `Low cache hit rate for ${stage}`,
      description: `Stage ${stage} has a ${(rate * 100).toFixed(1)}% cache hit rate, below the ${(threshold * 100).toFixed(0)}% threshold, across ${stageRecords.length} records.`,
      impact:
        "Repeated context is re-injected as fresh input for this stage instead of being served from cache, inflating input token cost and stage latency.",
      recommendation: `Stabilize the cached prefix for ${stage} (byte-identical system prompt and context structure across runs). Verify cacheCreationTokens are being populated for this stage.`,
      evidence: {
        stage,
        cacheHitRate: rate,
        threshold,
        records: stageRecords.length,
      },
      confidence: stageRecords.length >= config.minimumSampleSizes.significance ? "high" : "medium",
    });
  }

  // ── 2. Token waste detection (P95 outliers per stage) ───────────

  // Group total tokens by stage
  const byStage = new Map<string, number[]>();
  for (const r of records) {
    const existing = byStage.get(r.stage) ?? [];
    existing.push(totalTokens(r));
    byStage.set(r.stage, existing);
  }

  const wasteyStageSummaries: Array<{
    stage: string;
    p95: number;
    median: number;
  }> = [];
  for (const [stage, counts] of byStage) {
    if (counts.length < 3) continue; // skip stages without enough data to compute P95
    const p95 = computePercentile(counts, 95);
    const median = computePercentile(counts, 50);
    // Flag when P95 is more than 3x the median — classic outlier signal
    if (median > 0 && p95 > median * 3) {
      wasteyStageSummaries.push({ stage, p95, median });
    }
  }

  const wasteRatio = wasteyStageSummaries.length / Math.max(byStage.size, 1);

  if (wasteyStageSummaries.length > 0) {
    score -= 15;
    findings.push({
      id: `te-${++findingIndex}`,
      dimension: "token-economics",
      severity: wasteRatio > 0.5 ? "high" : "medium",
      title: "Token waste outliers detected",
      description: `${wasteyStageSummaries.length} stage(s) show P95 token counts more than 3× their median, indicating occasional runaway context injection.`,
      impact:
        "Intermittent high-token runs inflate average cost and can exceed model context windows.",
      recommendation:
        "Add context size guards before large file reads. Review stages with the highest P95/median ratios and cap context injection where possible.",
      evidence: {
        affectedStages: wasteyStageSummaries.map((s) => s.stage),
        stageSummaries: wasteyStageSummaries,
        wasteRatio,
      },
      confidence: "medium",
    });
  }

  // ── 3. Token usage trend (chronological) ────────────────────────

  // Sort by timestamp to get a chronological time series of total tokens per run
  const sorted = [...records].sort(
    (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  );
  const tokenTimeSeries = sorted.map(totalTokens);

  // Normalise slope against mean to make it scale-independent
  const avgTokens = mean(tokenTimeSeries);
  const { slope, direction: tokenTrend } = computeTrend(tokenTimeSeries);
  const normalisedSlope = avgTokens > 0 ? slope / avgTokens : 0;

  if (tokenTrend === "degrading" && normalisedSlope > 0.02) {
    score -= 10;
    findings.push({
      id: `te-${++findingIndex}`,
      dimension: "token-economics",
      severity: normalisedSlope > 0.05 ? "high" : "medium",
      title: "Token usage trending upward",
      description: `Total tokens per run are increasing over time (normalised slope: ${(normalisedSlope * 100).toFixed(2)}% per run).`,
      impact: "Unchecked token growth drives cost increases and risks context-window exhaustion.",
      recommendation:
        "Audit recent context handoff files for size creep. Consider truncating or summarising historical context passed between stages.",
      evidence: {
        slope,
        normalisedSlope,
        avgTokens,
        sampleSize,
      },
      confidence: sampleSize >= config.minimumSampleSizes.trend ? "high" : "medium",
    });
  }

  // ── 4. Input/output token ratio ──────────────────────────────────

  const totalInput = records.reduce((s, r) => s + r.inputTokens, 0);
  const totalOutput = records.reduce((s, r) => s + r.outputTokens, 0);
  const inputOutputRatio = totalOutput > 0 ? totalInput / totalOutput : 0;

  // A ratio > 10 suggests we're injecting far more context than the model uses
  if (inputOutputRatio > 10) {
    score -= 10;
    findings.push({
      id: `te-${findingIndex + 1}`,
      dimension: "token-economics",
      severity: inputOutputRatio > 20 ? "high" : "medium",
      title: "High input-to-output token ratio",
      description: `Input tokens are ${inputOutputRatio.toFixed(1)}× output tokens, suggesting significant context over-injection.`,
      impact:
        "Over-injection wastes tokens on context the model does not utilise and increases per-run cost.",
      recommendation:
        "Trim injected context to only what each stage needs. Use targeted file reads instead of dumping entire files into prompts.",
      evidence: {
        totalInput,
        totalOutput,
        inputOutputRatio,
      },
      confidence: "high",
    });
  }

  // ── 5. Overall efficiency — tokens vs successful outcomes ────────

  const successfulRuns = records.filter((r) => r.success);
  const tokensOnSuccessful = successfulRuns.reduce((s, r) => s + totalTokens(r), 0);
  const tokensOnFailed = records.filter((r) => !r.success).reduce((s, r) => s + totalTokens(r), 0);
  const totalAllTokens = tokensOnSuccessful + tokensOnFailed;
  const successRate = sampleSize > 0 ? successfulRuns.length / sampleSize : 0;
  const wastedFraction = totalAllTokens > 0 ? tokensOnFailed / totalAllTokens : 0;

  const avgTotalTokensPerRun = avgTokens;
  const avgTokensPerSuccess =
    successfulRuns.length > 0 ? tokensOnSuccessful / successfulRuns.length : 0;

  // ── Clamp final score ────────────────────────────────────────────

  score = clamp(score, 0, 100);

  // ── Metrics payload ──────────────────────────────────────────────

  const metrics: Record<string, number> = {
    avgCacheHitRate,
    avgTotalTokensPerRun,
    avgTokensPerSuccess,
    inputOutputRatio,
    wasteyStageFraction: wasteRatio,
    tokenSlope: slope,
    successRate,
    wastedTokenFraction: wastedFraction,
    sampleSize,
  };

  // Per-stage cache-hit rates (Issue #3804). `metrics` is a flat number map, so
  // each stage is surfaced as a `perStageCacheHitRate.<stage>` key. Stages with
  // no cacheable input (rate === null) are omitted rather than reported as 0%.
  let stagesWithCacheData = 0;
  for (const [stage, rate] of Object.entries(perStageCacheHitRate)) {
    if (rate === null) continue;
    metrics[`perStageCacheHitRate.${stage}`] = rate;
    stagesWithCacheData += 1;
  }
  metrics.stagesWithCacheData = stagesWithCacheData;

  // ── Period comparison (baseline) ─────────────────────────────────

  let periodComparison = undefined;
  if (baseline !== undefined && baseline.executionHistory.length > 0) {
    const baselineAvg = mean(baseline.executionHistory.map(totalTokens));
    periodComparison = buildPeriodComparison(
      avgTotalTokensPerRun,
      baselineAvg,
      sampleSize,
      /* lowerIsBetter */ true,
      config.confidenceThreshold
    );
  }

  return {
    dimension: "token-economics",
    score,
    status: getHealthStatus(score),
    findings,
    metrics,
    hasEnoughData: true,
    sampleSize,
    periodComparison,
  };
}
