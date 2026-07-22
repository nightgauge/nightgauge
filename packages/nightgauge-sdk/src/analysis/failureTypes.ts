/**
 * Failure Pattern Detection - Type Definitions
 *
 * Defines TypeScript interfaces for the failure pattern detection module.
 * These types model failure categories, detection findings, and analysis
 * results.
 *
 * @see docs/ARCHITECTURE.md for architectural context
 */
import type { CostRates } from "./types.js";

/** Failure categories defined in the YAML taxonomy */
export type FailureCategory =
  | "build-failure"
  | "test-failure"
  | "lint-format-failure"
  | "environment-failure"
  | "timeout-transient"
  | "context-exhaustion"
  | "stage-cost-cap-exceeded"
  | "uncategorized";

/** Severity classification for failure findings */
export type FailureSeverity = "auto-fixable" | "manual-fix" | "infrastructure";

/** A single failure pattern parsed from the YAML taxonomy */
export interface FailurePattern {
  category: string;
  displayName: string;
  description: string;
  patterns: RegExp[];
  autoFixable: boolean;
  typicalRootCauses: string[];
}

/** The complete loaded failure taxonomy */
export interface FailureTaxonomy {
  schemaVersion: string;
  categories: Map<string, FailurePattern>;
}

/** Raw YAML taxonomy category before regex compilation */
export interface RawTaxonomyCategory {
  display_name: string;
  description: string;
  patterns: string[];
  auto_fixable: boolean;
  typical_root_causes: string[];
}

/** Raw YAML taxonomy structure before processing */
export interface RawTaxonomy {
  schema_version: string;
  categories: Record<string, RawTaxonomyCategory>;
}

/** Root cause correlation data for a failure category */
export interface RootCauseCorrelation {
  correlatedFactors: Array<{
    factor: string;
    occurrenceRate: number;
    description: string;
  }>;
}

/** Trend direction for a failure category */
export type TrendDirection = "improving" | "stable" | "worsening";

/** Individual failure finding produced by the detector */
export interface FailureFinding {
  category: FailureCategory;
  severity: FailureSeverity;
  title: string;
  description: string;
  occurrenceCount: number;
  affectedStages: string[];
  affectedRuns: number[];
  estimatedCostUsd: number;
  rootCauseCorrelation: RootCauseCorrelation;
  recommendation: string;
  trend: TrendDirection;
  evidence: Record<string, unknown>;
}

/** Complete failure analysis result returned by the detector */
export interface FailureAnalysisResult {
  analyzedAt: string;
  recordsAnalyzed: number;
  totalFailures: number;
  findings: FailureFinding[];
  summary: {
    totalFailureCostUsd: number;
    topCategory: FailureCategory | null;
    overallTrend: TrendDirection;
    failureRate: number;
  };
}

/** Configuration for the FailurePatternDetector */
export interface FailureDetectorConfig {
  costRates?: Partial<CostRates>;
  taxonomyPath?: string;
  recurringThreshold?: number;
}

/** Default recurring threshold: a failure must appear in 3+ runs to be "recurring" */
export const DEFAULT_RECURRING_THRESHOLD = 3;
