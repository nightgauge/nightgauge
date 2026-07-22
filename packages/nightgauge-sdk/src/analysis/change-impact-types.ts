/**
 * Types and Zod schemas for the Change Impact Analyzer.
 *
 * Defines the output structure for impact analysis: impact classification,
 * confidence scoring, and affected source/test file tracking.
 *
 * @see Issue #1971 - Change Impact Analyzer
 */

import { z } from "zod";

/** Three-tier impact classification for a set of changes. */
export type ImpactLevel = "isolated" | "cross-cutting" | "infrastructure";

/** Confidence level for a test file being affected by a change. */
export type ConfidenceLevel = "high" | "medium" | "low";

/** Status of a file change from git diff. */
export type FileChangeStatus = "added" | "modified" | "deleted" | "renamed" | "unknown";

/** Parsed entry from `git diff --name-status` output. */
export interface DiffEntry {
  status: FileChangeStatus;
  /** File path (for renames: the new path). */
  path: string;
  /** For renames: the old path before renaming. */
  oldPath?: string;
}

/** A test file identified as affected by a change, with confidence scoring. */
export interface AffectedTest {
  testFile: string;
  confidence: ConfidenceLevel;
  matchType: "direct" | "transitive" | "heuristic";
  /** Human-readable explanation of why this test is affected. */
  reason: string;
}

/** A source file affected by a change, annotated with graph membership. */
export interface AffectedSource {
  sourceFile: string;
  changeStatus: FileChangeStatus;
  /** False for new/deleted files not yet in the dependency graph. */
  hasGraphEntry: boolean;
}

/** Top-level result of analyzing the impact of a set of changes. */
export interface ImpactAnalysisResult {
  impactLevel: ImpactLevel;
  affectedSources: AffectedSource[];
  affectedTests: AffectedTest[];
  changedFiles: DiffEntry[];
  summary: {
    totalAffectedTests: number;
    highConfidence: number;
    mediumConfidence: number;
    lowConfidence: number;
    impactLevelReason: string;
    /** Set when a regression trigger rule caused a full regression requirement. */
    regressionTrigger?: RegressionTriggerResult;
  };
}

/** Default path patterns that indicate infrastructure changes. */
export const DEFAULT_INFRASTRUCTURE_PATTERNS = [
  "package.json",
  "package-lock.json",
  "tsconfig*",
  ".github/**",
  "vitest.config*",
];

/** Default patterns for dependency file trigger. */
export const DEFAULT_DEPENDENCY_PATTERNS = [
  "package.json",
  "package-lock.json",
  "go.mod",
  "go.sum",
  "yarn.lock",
  "pnpm-lock.yaml",
];

/** Default patterns for build/config infrastructure trigger. */
export const DEFAULT_BUILD_CONFIG_PATTERNS = [
  "tsconfig*",
  "vitest.config*",
  "vite.config*",
  "webpack.config*",
  "rollup.config*",
  "esbuild.config*",
  "babel.config*",
  ".babelrc*",
];

/** Default patterns for shared type definitions trigger. */
export const DEFAULT_SHARED_TYPES_PATTERNS = ["shared-types/**", "**/shared-types/**", "**/*.d.ts"];

/** Default patterns for test infrastructure trigger. */
export const DEFAULT_TEST_INFRASTRUCTURE_PATTERNS = [
  "**/__mocks__/**",
  "**/test-utils/**",
  "**/fixtures/**",
  "**/setup.ts",
  "**/setup.js",
  "**/*.setup.ts",
  "**/*.setup.js",
  "**/vitest.setup*",
  "**/jest.setup*",
];

/** Default patterns for CI configuration trigger. */
export const DEFAULT_CI_CONFIG_PATTERNS = [
  ".github/**",
  ".circleci/**",
  ".gitlab-ci.yml",
  "Jenkinsfile",
  ".travis.yml",
];

// ---------------------------------------------------------------------------
// Regression trigger types
// ---------------------------------------------------------------------------

/** Named trigger types for full regression requirement. */
export type RegressionTriggerType =
  | "dependency-change"
  | "build-config"
  | "shared-types"
  | "test-infrastructure"
  | "ci-config"
  | "low-confidence"
  | "manual-override";

/** Result of trigger evaluation when a rule fired. */
export interface RegressionTriggerResult {
  triggered: true;
  type: RegressionTriggerType;
  /** Human-readable explanation suitable for PR body / logs. */
  reason: string;
  /** The file that matched (for file-based triggers). */
  matchedFile?: string;
}

/** Result of trigger evaluation when no rule fired. */
export interface RegressionNotTriggered {
  triggered: false;
}

/** Union result of evaluating regression trigger rules. */
export type RegressionTriggerEvaluation = RegressionTriggerResult | RegressionNotTriggered;

/** Per-trigger configuration — patterns are additive over built-in defaults. */
export interface RegressionTriggerRuleConfig {
  enabled: boolean;
  /** Additional glob patterns beyond built-in defaults. */
  additionalPatterns?: string[];
}

/** Zod schema for full trigger rules configuration. */
export const RegressionTriggerConfigSchema = z.object({
  dependencyChange: z
    .object({
      enabled: z.boolean().default(true),
      additionalPatterns: z.array(z.string()).default([]),
    })
    .default(() => ({ enabled: true, additionalPatterns: [] })),
  buildConfig: z
    .object({
      enabled: z.boolean().default(true),
      additionalPatterns: z.array(z.string()).default([]),
    })
    .default(() => ({ enabled: true, additionalPatterns: [] })),
  sharedTypes: z
    .object({
      enabled: z.boolean().default(true),
      additionalPatterns: z.array(z.string()).default([]),
    })
    .default(() => ({ enabled: true, additionalPatterns: [] })),
  testInfrastructure: z
    .object({
      enabled: z.boolean().default(true),
      additionalPatterns: z.array(z.string()).default([]),
    })
    .default(() => ({ enabled: true, additionalPatterns: [] })),
  ciConfig: z
    .object({
      enabled: z.boolean().default(true),
      additionalPatterns: z.array(z.string()).default([]),
    })
    .default(() => ({ enabled: true, additionalPatterns: [] })),
  lowConfidence: z
    .object({
      enabled: z.boolean().default(true),
      /**
       * Fraction 0–1: if ratio of low-confidence tests equals or exceeds this
       * threshold, trigger. Default 1.0 (all tests must be low confidence).
       */
      threshold: z.number().min(0).max(1).default(1.0),
    })
    .default(() => ({ enabled: true, threshold: 1.0 })),
  manualOverride: z
    .object({
      enabled: z.boolean().default(true),
    })
    .default(() => ({ enabled: true })),
});

/** Input type for RegressionTriggerConfig (all fields optional). */
export type RegressionTriggerConfig = z.input<typeof RegressionTriggerConfigSchema>;

/** Zod schema for ChangeImpactAnalyzer configuration with sensible defaults. */
export const ChangeImpactAnalyzerConfigSchema = z.object({
  /** Path patterns that indicate infrastructure changes. */
  infrastructurePatterns: z.array(z.string()).default(DEFAULT_INFRASTRUCTURE_PATTERNS),
  /** Min distinct top-level directories for cross-cutting classification. */
  crossCuttingDirThreshold: z.number().int().min(1).default(2),
  /** Min dependent test files for cross-cutting classification. */
  crossCuttingDepThreshold: z.number().int().min(1).default(3),
  /** Per-trigger configuration for full regression rules. */
  regressionTriggers: RegressionTriggerConfigSchema.optional(),
  /** When true, always require full regression regardless of changed files. */
  forceFullRegression: z.boolean().default(false),
});

/** Configuration for ChangeImpactAnalyzer (all fields optional with defaults). */
export type ChangeImpactAnalyzerConfig = z.input<typeof ChangeImpactAnalyzerConfigSchema>;

/** Parsed (output) type of ChangeImpactAnalyzerConfig after schema defaults applied. */
export type ParsedChangeImpactAnalyzerConfig = z.output<typeof ChangeImpactAnalyzerConfigSchema>;
