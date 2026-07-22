/**
 * Failure Pattern Detectors - Pure Functions
 *
 * Five pure-function detectors that analyze execution history records to
 * identify failure patterns. Each detector examines a specific failure
 * aspect and returns zero or more FailureFinding objects.
 *
 * Detectors:
 * 1. detectFailuresByCategory — Classify and count failures by taxonomy category
 * 2. detectRecurringFailures — Identify failure patterns appearing in 3+ runs
 * 3. correlateRootCauses — Analyze co-occurring factors for each category
 * 4. computeFailureTrends — Determine failure rate direction over time
 * 5. generateRecommendations — Produce actionable recommendations per finding
 *
 * All detectors are pure functions with no side effects.
 *
 * @see docs/ARCHITECTURE.md for architectural context
 */

import type { ExecutionHistoryRecord, CostRates } from "./types.js";
import type {
  FailureCategory,
  FailureFinding,
  FailureTaxonomy,
  TrendDirection,
  RootCauseCorrelation,
} from "./failureTypes.js";

// ── Constants ────────────────────────────────────────────────────────

/** Factor rate must exceed baseline by this multiplier to be flagged as correlated */
const CORRELATION_THRESHOLD_MULTIPLIER = 1.5;

/** Minimum count before a factor is considered for correlation */
const MIN_CORRELATION_COUNT = 2;

/** Slope threshold for linear regression trend classification */
const TREND_SLOPE_THRESHOLD = 0.05;

/** Max sample errors retained as evidence per finding */
const MAX_SAMPLE_ERRORS = 3;

/** Max characters for recurring finding titles */
const MAX_TITLE_LENGTH = 80;

// ── Helpers ──────────────────────────────────────────────────────────

/**
 * Safely extract errorText from a record that may be an extended type.
 * Records may carry errorText if they are ExecutionHistoryRunRecordFlat,
 * but the base type does not declare it.
 */
function getErrorText(record: ExecutionHistoryRecord): string | undefined {
  return (record as unknown as { errorText?: string }).errorText;
}

/** Default cost rates used when none are provided */
const DEFAULT_COST_RATES: CostRates = {
  inputPerMillion: 3.0,
  outputPerMillion: 15.0,
};

/** Map category auto_fixable flag to FailureSeverity */
function severityForCategory(
  category: string,
  taxonomy: FailureTaxonomy
): "auto-fixable" | "manual-fix" | "infrastructure" {
  const pattern = taxonomy.categories.get(category);
  if (!pattern) return "manual-fix";
  if (pattern.autoFixable) return "auto-fixable";
  // Infrastructure categories
  if (category === "timeout-transient") return "infrastructure";
  return "manual-fix";
}

/** Classify a single error text against the taxonomy, returning the matching category */
function classifyError(errorText: string, taxonomy: FailureTaxonomy): FailureCategory {
  for (const [categoryKey, pattern] of taxonomy.categories) {
    for (const regex of pattern.patterns) {
      if (regex.test(errorText)) {
        return categoryKey as FailureCategory;
      }
    }
  }
  return "uncategorized";
}

/** Estimate cost in USD from a record's token counts */
function estimateCostUsd(record: ExecutionHistoryRecord, costRates: CostRates): number {
  const inputCost = (record.inputTokens / 1_000_000) * costRates.inputPerMillion;
  const outputCost = (record.outputTokens / 1_000_000) * costRates.outputPerMillion;
  const cacheReadCost =
    costRates.cacheReadPerMillion && record.cacheReadTokens
      ? (record.cacheReadTokens / 1_000_000) * costRates.cacheReadPerMillion
      : 0;
  const cacheCreateCost =
    costRates.cacheCreationPerMillion && record.cacheCreationTokens
      ? (record.cacheCreationTokens / 1_000_000) * costRates.cacheCreationPerMillion
      : 0;
  return inputCost + outputCost + cacheReadCost + cacheCreateCost;
}

/** Normalize error text to create a grouping signature */
function normalizeErrorSignature(errorText: string): string {
  return errorText
    .replace(/\d+/g, "N") // replace numbers
    .replace(/\/[\w./-]+/g, "<path>") // replace file paths
    .replace(/\d{4}-\d{2}-\d{2}T[\d:.Z]+/g, "<timestamp>") // replace ISO timestamps
    .replace(/\s+/g, " ") // collapse whitespace
    .trim()
    .toLowerCase();
}

/** Get the start of the ISO week for bucketing */
function getWeekBucket(timestamp: string): string {
  const date = new Date(timestamp);
  const day = date.getUTCDay();
  const diff = date.getUTCDate() - day + (day === 0 ? -6 : 1); // Monday
  const monday = new Date(date);
  monday.setUTCDate(diff);
  return monday.toISOString().slice(0, 10); // YYYY-MM-DD
}

// ── Detector 1: detectFailuresByCategory ──────────────────────────

/**
 * Classify and count failures by taxonomy category.
 *
 * Scans all records where `success === false`, matches errorText against
 * taxonomy patterns, and groups findings by category. Computes cost per
 * category using record token data.
 */
export function detectFailuresByCategory(
  records: ExecutionHistoryRecord[],
  taxonomy: FailureTaxonomy,
  costRates: CostRates
): FailureFinding[] {
  const rates = { ...DEFAULT_COST_RATES, ...costRates };
  const failedRecords = records.filter((r) => !r.success);

  if (failedRecords.length === 0) return [];

  // Group by category
  const groups = new Map<
    FailureCategory,
    {
      records: ExecutionHistoryRecord[];
      stages: Set<string>;
      runs: Set<number>;
      costUsd: number;
    }
  >();

  for (const record of failedRecords) {
    const errorText = getErrorText(record);
    const category = errorText ? classifyError(errorText, taxonomy) : "uncategorized";

    if (!groups.has(category)) {
      groups.set(category, {
        records: [],
        stages: new Set(),
        runs: new Set(),
        costUsd: 0,
      });
    }
    const group = groups.get(category)!;
    group.records.push(record);
    group.stages.add(record.stage);
    group.runs.add(record.issueNumber);
    group.costUsd += record.costUsd > 0 ? record.costUsd : estimateCostUsd(record, rates);
  }

  // Build findings
  const findings: FailureFinding[] = [];
  for (const [category, group] of groups) {
    const taxonomyEntry = taxonomy.categories.get(category);
    const displayName = taxonomyEntry?.displayName ?? category.replace(/-/g, " ");

    findings.push({
      category,
      severity: severityForCategory(category, taxonomy),
      title: `${displayName} (${group.records.length} occurrences)`,
      description: `${group.records.length} failures classified as ${displayName.toLowerCase()} across ${group.stages.size} stage(s) and ${group.runs.size} run(s).`,
      occurrenceCount: group.records.length,
      affectedStages: Array.from(group.stages),
      affectedRuns: Array.from(group.runs),
      estimatedCostUsd: group.costUsd,
      rootCauseCorrelation: { correlatedFactors: [] },
      recommendation: "",
      trend: "stable",
      evidence: {
        sampleErrors: group.records
          .slice(0, MAX_SAMPLE_ERRORS)
          .map((r) => getErrorText(r) ?? `stage:${r.stage}`),
      },
    });
  }

  // Sort by occurrence count descending
  findings.sort((a, b) => b.occurrenceCount - a.occurrenceCount);
  return findings;
}

// ── Detector 2: detectRecurringFailures ───────────────────────────

/**
 * Identify failure patterns that recur across multiple runs.
 *
 * Normalizes error text to create signatures, then groups by signature.
 * Patterns appearing in `threshold` or more distinct runs are flagged.
 */
export function detectRecurringFailures(
  records: ExecutionHistoryRecord[],
  taxonomy: FailureTaxonomy,
  costRates: CostRates,
  threshold: number = 3
): FailureFinding[] {
  const rates = { ...DEFAULT_COST_RATES, ...costRates };
  const failedRecords = records.filter((r) => !r.success);

  if (failedRecords.length === 0) return [];

  // Group by normalized error signature
  const groups = new Map<
    string,
    {
      records: ExecutionHistoryRecord[];
      runs: Set<number>;
      stages: Set<string>;
      costUsd: number;
      category: FailureCategory;
      originalErrors: string[];
    }
  >();

  for (const record of failedRecords) {
    const errorText = getErrorText(record) ?? "";
    if (!errorText) continue;

    const signature = normalizeErrorSignature(errorText);
    if (!groups.has(signature)) {
      groups.set(signature, {
        records: [],
        runs: new Set(),
        stages: new Set(),
        costUsd: 0,
        category: classifyError(errorText, taxonomy),
        originalErrors: [],
      });
    }
    const group = groups.get(signature)!;
    group.records.push(record);
    group.runs.add(record.issueNumber);
    group.stages.add(record.stage);
    group.costUsd += record.costUsd > 0 ? record.costUsd : estimateCostUsd(record, rates);
    if (group.originalErrors.length < MAX_SAMPLE_ERRORS) {
      group.originalErrors.push(errorText);
    }
  }

  // Filter to recurring (threshold+ distinct runs)
  const findings: FailureFinding[] = [];
  for (const [signature, group] of groups) {
    if (group.runs.size < threshold) continue;

    findings.push({
      category: group.category,
      severity: severityForCategory(group.category, taxonomy),
      title: `Recurring: ${group.originalErrors[0]?.slice(0, MAX_TITLE_LENGTH) ?? signature}`,
      description: `This error pattern recurred in ${group.runs.size} distinct runs (threshold: ${threshold}). Normalized signature: "${signature.slice(0, 120)}".`,
      occurrenceCount: group.records.length,
      affectedStages: Array.from(group.stages),
      affectedRuns: Array.from(group.runs),
      estimatedCostUsd: group.costUsd,
      rootCauseCorrelation: { correlatedFactors: [] },
      recommendation: "",
      trend: "stable",
      evidence: {
        normalizedSignature: signature,
        sampleErrors: group.originalErrors,
        distinctRuns: group.runs.size,
      },
    });
  }

  findings.sort((a, b) => b.occurrenceCount - a.occurrenceCount);
  return findings;
}

// ── Detector 3: correlateRootCauses ───────────────────────────────

/**
 * Analyze co-occurring factors for each failure category.
 *
 * For each category, examines what factors (complexity score, stage name)
 * appear more frequently in failed runs vs all runs.
 */
export function correlateRootCauses(
  records: ExecutionHistoryRecord[],
  taxonomy: FailureTaxonomy
): Map<FailureCategory, RootCauseCorrelation> {
  const result = new Map<FailureCategory, RootCauseCorrelation>();

  if (records.length === 0) return result;

  const failedRecords = records.filter((r) => !r.success);
  if (failedRecords.length === 0) return result;

  // Compute baseline factor rates across all records
  const allStageRates = new Map<string, number>();
  const allComplexityRates = new Map<string, number>();

  for (const record of records) {
    allStageRates.set(record.stage, (allStageRates.get(record.stage) ?? 0) + 1);
    if (record.complexityScore !== undefined) {
      const bucket = complexityBucket(record.complexityScore);
      allComplexityRates.set(bucket, (allComplexityRates.get(bucket) ?? 0) + 1);
    }
  }

  // For each category, compute factor rates in failed records
  const categoryFailed = new Map<FailureCategory, ExecutionHistoryRecord[]>();
  for (const record of failedRecords) {
    const errorText = getErrorText(record) ?? "";
    const category = errorText ? classifyError(errorText, taxonomy) : "uncategorized";
    if (!categoryFailed.has(category)) {
      categoryFailed.set(category, []);
    }
    categoryFailed.get(category)!.push(record);
  }

  for (const [category, catRecords] of categoryFailed) {
    const factors: RootCauseCorrelation["correlatedFactors"] = [];

    // Stage correlation
    const catStageRates = new Map<string, number>();
    for (const r of catRecords) {
      catStageRates.set(r.stage, (catStageRates.get(r.stage) ?? 0) + 1);
    }
    for (const [stage, count] of catStageRates) {
      const failRate = count / catRecords.length;
      const baselineRate = (allStageRates.get(stage) ?? 0) / records.length;
      if (
        failRate > baselineRate * CORRELATION_THRESHOLD_MULTIPLIER &&
        count >= MIN_CORRELATION_COUNT
      ) {
        factors.push({
          factor: `stage:${stage}`,
          occurrenceRate: failRate,
          description: `Stage "${stage}" appears in ${(failRate * 100).toFixed(0)}% of ${category} failures vs ${(baselineRate * 100).toFixed(0)}% baseline.`,
        });
      }
    }

    // Complexity correlation
    const catComplexityRates = new Map<string, number>();
    for (const r of catRecords) {
      if (r.complexityScore !== undefined) {
        const bucket = complexityBucket(r.complexityScore);
        catComplexityRates.set(bucket, (catComplexityRates.get(bucket) ?? 0) + 1);
      }
    }
    for (const [bucket, count] of catComplexityRates) {
      const failRate = count / catRecords.length;
      const baselineRate = (allComplexityRates.get(bucket) ?? 0) / records.length;
      if (
        failRate > baselineRate * CORRELATION_THRESHOLD_MULTIPLIER &&
        count >= MIN_CORRELATION_COUNT
      ) {
        factors.push({
          factor: `complexity:${bucket}`,
          occurrenceRate: failRate,
          description: `Complexity ${bucket} appears in ${(failRate * 100).toFixed(0)}% of ${category} failures vs ${(baselineRate * 100).toFixed(0)}% baseline.`,
        });
      }
    }

    result.set(category, { correlatedFactors: factors });
  }

  return result;
}

/** Map complexity score to a human-readable bucket */
function complexityBucket(score: number): string {
  if (score <= 2) return "low";
  if (score <= 5) return "medium";
  return "high";
}

// ── Detector 4: computeFailureTrends ──────────────────────────────

/**
 * Compute failure rate trends over time using linear regression.
 *
 * Splits records into weekly time buckets, computes the failure rate
 * per bucket, and uses simple linear regression to determine the trend
 * direction (improving, stable, or worsening).
 */
export function computeFailureTrends(
  records: ExecutionHistoryRecord[],
  taxonomy: FailureTaxonomy
): {
  perCategory: Map<FailureCategory, TrendDirection>;
  overall: TrendDirection;
} {
  const perCategory = new Map<FailureCategory, TrendDirection>();

  if (records.length === 0) {
    return { perCategory, overall: "stable" };
  }

  // Bucket all records by week
  const weekBuckets = new Map<string, ExecutionHistoryRecord[]>();
  for (const record of records) {
    const week = getWeekBucket(record.timestamp);
    if (!weekBuckets.has(week)) {
      weekBuckets.set(week, []);
    }
    weekBuckets.get(week)!.push(record);
  }

  // Sort weeks chronologically
  const sortedWeeks = Array.from(weekBuckets.keys()).sort();

  if (sortedWeeks.length < 2) {
    return { perCategory, overall: "stable" };
  }

  // Overall trend: failure rate per week
  const overallRates = sortedWeeks.map((week) => {
    const weekRecords = weekBuckets.get(week)!;
    const failures = weekRecords.filter((r) => !r.success).length;
    return weekRecords.length > 0 ? failures / weekRecords.length : 0;
  });
  const overall = computeLinearTrend(overallRates);

  // Per-category trends
  const categoryWeekData = new Map<
    FailureCategory,
    Map<string, { total: number; failed: number }>
  >();

  for (const record of records) {
    const errorText = getErrorText(record) ?? "";
    const category = !record.success && errorText ? classifyError(errorText, taxonomy) : null;

    if (category) {
      if (!categoryWeekData.has(category)) {
        categoryWeekData.set(category, new Map());
      }
      const weekMap = categoryWeekData.get(category)!;
      const week = getWeekBucket(record.timestamp);
      if (!weekMap.has(week)) {
        weekMap.set(week, { total: 0, failed: 0 });
      }
      const bucket = weekMap.get(week)!;
      bucket.total++;
      bucket.failed++;
    }
  }

  for (const [category, weekMap] of categoryWeekData) {
    const rates = sortedWeeks.map((week) => {
      const data = weekMap.get(week);
      const weekTotal = weekBuckets.get(week)!.length;
      return data && weekTotal > 0 ? data.failed / weekTotal : 0;
    });
    perCategory.set(category, computeLinearTrend(rates));
  }

  return { perCategory, overall };
}

// ── Detector 5: generateRecommendations ───────────────────────────

/**
 * Generate actionable recommendations for each failure finding.
 *
 * Uses taxonomy `typical_root_causes` as a basis and adds context-specific
 * advice based on the finding's data. Mutates the findings in-place by
 * setting the `recommendation` field. Returns the same array for chaining.
 */
export function generateRecommendations(
  findings: FailureFinding[],
  taxonomy: FailureTaxonomy
): FailureFinding[] {
  for (const finding of findings) {
    const pattern = taxonomy.categories.get(finding.category);
    const rootCauses = pattern?.typicalRootCauses ?? [];

    const parts: string[] = [];

    // Add severity-specific advice
    if (finding.severity === "auto-fixable") {
      parts.push(
        "This failure type is typically auto-fixable. Enable auto-fix in pipeline config."
      );
    } else if (finding.severity === "infrastructure") {
      parts.push("This is an infrastructure issue. Consider retry policies and alerting.");
    }

    // Add root cause advice
    if (rootCauses.length > 0) {
      parts.push(`Typical root causes: ${rootCauses.join("; ")}.`);
    }

    // Add frequency-specific advice
    if (finding.occurrenceCount >= 10) {
      parts.push(`High frequency (${finding.occurrenceCount} occurrences) — prioritize this fix.`);
    }

    // Add stage-specific advice
    if (finding.affectedStages.length === 1) {
      parts.push(
        `Isolated to stage "${finding.affectedStages[0]}" — investigate stage-specific configuration.`
      );
    }

    // Add trend-specific advice
    if (finding.trend === "worsening") {
      parts.push("Trend is worsening — address urgently.");
    } else if (finding.trend === "improving") {
      parts.push("Trend is improving — recent changes may be helping.");
    }

    finding.recommendation =
      parts.length > 0
        ? parts.join(" ")
        : `Review ${finding.category} failures and address root causes.`;
  }

  return findings;
}

// ── Helper: computeLinearTrend ────────────────────────────────────

/**
 * Simple linear regression to determine trend direction.
 *
 * Fits a line y = mx + b to the data points and classifies:
 * - slope < -0.05: improving
 * - slope > 0.05: worsening
 * - otherwise: stable
 */
export function computeLinearTrend(values: number[]): TrendDirection {
  if (values.length < 2) return "stable";

  const n = values.length;
  let sumX = 0;
  let sumY = 0;
  let sumXY = 0;
  let sumXX = 0;

  for (let i = 0; i < n; i++) {
    sumX += i;
    sumY += values[i];
    sumXY += i * values[i];
    sumXX += i * i;
  }

  const denominator = n * sumXX - sumX * sumX;
  if (denominator === 0) return "stable";

  const slope = (n * sumXY - sumX * sumY) / denominator;

  if (slope < -TREND_SLOPE_THRESHOLD) return "improving";
  if (slope > TREND_SLOPE_THRESHOLD) return "worsening";
  return "stable";
}
