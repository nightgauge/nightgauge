/**
 * Failure Pattern Detector
 *
 * Main detector class that orchestrates failure pattern detection across
 * execution history records. Loads the failure taxonomy, runs all five
 * detectors, enriches findings with trends and recommendations, and
 * produces a complete FailureAnalysisResult.
 *
 * @see docs/ARCHITECTURE.md for architectural context
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import * as yaml from "js-yaml";

import type { ExecutionHistoryRecord, CostRates } from "./types.js";
import type {
  FailureAnalysisResult,
  FailureCategory,
  FailureDetectorConfig,
  FailureFinding,
  FailureTaxonomy,
  RawTaxonomy,
} from "./failureTypes.js";
import { DEFAULT_RECURRING_THRESHOLD } from "./failureTypes.js";
import {
  detectFailuresByCategory,
  detectRecurringFailures,
  correlateRootCauses,
  computeFailureTrends,
  generateRecommendations,
} from "./failurePatterns.js";

/** Default cost rates when none provided */
const DEFAULT_COST_RATES: CostRates = {
  inputPerMillion: 3.0,
  outputPerMillion: 15.0,
};

export class FailurePatternDetector {
  private readonly costRates: CostRates;
  private readonly taxonomy: FailureTaxonomy;
  private readonly recurringThreshold: number;

  constructor(config?: FailureDetectorConfig) {
    this.costRates = { ...DEFAULT_COST_RATES, ...config?.costRates };
    this.recurringThreshold = config?.recurringThreshold ?? DEFAULT_RECURRING_THRESHOLD;

    const taxonomyPath = config?.taxonomyPath ?? resolve(__dirname, "failure-taxonomy.yaml");

    try {
      this.taxonomy = loadTaxonomy(taxonomyPath);
    } catch (err) {
      throw new Error(
        `Failed to load failure taxonomy from "${taxonomyPath}": ${err instanceof Error ? err.message : String(err)}`,
        { cause: err }
      );
    }
  }

  /**
   * Analyze execution history records for failure patterns.
   *
   * Filters to failed records, runs all five detectors, enriches findings
   * with trends and root cause data, and returns a complete FailureAnalysisResult.
   */
  analyze(records: ExecutionHistoryRecord[]): FailureAnalysisResult {
    if (records.length === 0) {
      return this.emptyResult(0);
    }

    const failedRecords = records.filter((r) => !r.success);
    if (failedRecords.length === 0) {
      return this.emptyResult(records.length);
    }

    // Run detectors
    const categoryFindings = detectFailuresByCategory(records, this.taxonomy, this.costRates);

    const recurringFindings = detectRecurringFailures(
      records,
      this.taxonomy,
      this.costRates,
      this.recurringThreshold
    );

    const rootCauses = correlateRootCauses(records, this.taxonomy);

    const trends = computeFailureTrends(records, this.taxonomy);

    // Merge category + recurring findings
    const merged = this.mergeFindings(categoryFindings, recurringFindings);

    // Enrich with trend + root cause data
    for (const finding of merged) {
      const categoryTrend = trends.perCategory.get(finding.category);
      if (categoryTrend) {
        finding.trend = categoryTrend;
      }
      const correlation = rootCauses.get(finding.category);
      if (correlation) {
        finding.rootCauseCorrelation = correlation;
      }
    }

    // Generate recommendations
    generateRecommendations(merged, this.taxonomy);

    // Compute summary
    const totalFailureCostUsd = merged.reduce((sum, f) => sum + f.estimatedCostUsd, 0);
    const topCategory = this.findTopCategory(merged);
    const failureRate = failedRecords.length / records.length;

    return {
      analyzedAt: new Date().toISOString(),
      recordsAnalyzed: records.length,
      totalFailures: failedRecords.length,
      findings: merged,
      summary: {
        totalFailureCostUsd,
        topCategory,
        overallTrend: trends.overall,
        failureRate,
      },
    };
  }

  private emptyResult(recordsAnalyzed: number): FailureAnalysisResult {
    return {
      analyzedAt: new Date().toISOString(),
      recordsAnalyzed,
      totalFailures: 0,
      findings: [],
      summary: {
        totalFailureCostUsd: 0,
        topCategory: null,
        overallTrend: "stable",
        failureRate: 0,
      },
    };
  }

  /**
   * Merge category findings and recurring findings, deduplicating
   * by category. Recurring findings that overlap with category findings
   * have their data folded into the category finding.
   */
  private mergeFindings(
    categoryFindings: FailureFinding[],
    recurringFindings: FailureFinding[]
  ): FailureFinding[] {
    const merged = [...categoryFindings];
    const seenCategories = new Set(merged.map((f) => f.category));

    for (const recurring of recurringFindings) {
      if (seenCategories.has(recurring.category)) {
        // Fold recurring data into existing category finding
        const existing = merged.find((f) => f.category === recurring.category);
        if (existing) {
          existing.evidence = {
            ...existing.evidence,
            recurringPatterns: recurring.evidence,
          };
        }
      } else {
        merged.push(recurring);
        seenCategories.add(recurring.category);
      }
    }

    // Sort by occurrence count descending
    merged.sort((a, b) => b.occurrenceCount - a.occurrenceCount);
    return merged;
  }

  private findTopCategory(findings: FailureFinding[]): FailureCategory | null {
    if (findings.length === 0) return null;
    return findings[0].category;
  }
}

// ── Taxonomy Loading ──────────────────────────────────────────────

function loadTaxonomy(path: string): FailureTaxonomy {
  const raw = readFileSync(path, "utf-8");
  const parsed = yaml.load(raw) as RawTaxonomy;

  const categories = new Map<
    string,
    FailureTaxonomy["categories"] extends Map<string, infer V> ? V : never
  >();

  for (const [key, value] of Object.entries(parsed.categories)) {
    categories.set(key, {
      category: key,
      displayName: value.display_name,
      description: value.description,
      patterns: value.patterns.map((p) => new RegExp(p, "i")),
      autoFixable: value.auto_fixable,
      typicalRootCauses: value.typical_root_causes,
    });
  }

  return {
    schemaVersion: parsed.schema_version,
    categories,
  };
}
