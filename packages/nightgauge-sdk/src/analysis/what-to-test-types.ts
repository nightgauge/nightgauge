/**
 * Types for the "What to Test" PR section generator.
 *
 * Defines the options and output shape for `generateWhatToTestSection`.
 *
 * @see Issue #1972 - "What to Test" PR Section Generator
 */

export interface WhatToTestOptions {
  /** Max high-confidence tests to list explicitly. Default: 10 */
  maxHighConfidence?: number;
  /** Max medium-confidence tests to list explicitly. Default: 5 */
  maxMediumConfidence?: number;
  /** Whether to include low-confidence heuristic matches. Default: false */
  includeLowConfidence?: boolean;
  /** Project root path (for displaying relative paths). Default: process.cwd() */
  projectRoot?: string;
}

export interface WhatToTestSection {
  /** Generated Markdown string for the section, or empty string if skipped */
  markdown: string;
  /** Whether the section was generated with actual test data (false = no tests / placeholder only) */
  generated: boolean;
  /** Summary stats for logging */
  stats: {
    impactLevel: string;
    testsListed: number;
    testsOmitted: number;
  };
  /**
   * Snapshot of selected test file paths for post-merge escaped defect correlation.
   * Populated from `ImpactAnalysisResult.affectedTests` when the section is generated.
   * Optional — absent when section is not generated or tests cannot be enumerated.
   */
  selectedTestFiles?: string[];
}
