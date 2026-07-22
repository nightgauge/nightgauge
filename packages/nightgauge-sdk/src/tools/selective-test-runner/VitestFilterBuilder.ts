/**
 * VitestFilterBuilder — convert an ImpactAnalysisResult into Vitest
 * positional arguments (file paths).
 *
 * Filters out non-existent files, applies confidence thresholds,
 * and deduplicates results.
 *
 * @see Issue #1973 - Selective Test Runner
 */

import { existsSync } from "fs";
import { resolve } from "path";

import type { ImpactAnalysisResult, ConfidenceLevel } from "../../analysis/change-impact-types.js";
import type { SelectiveTestRunnerConfig } from "./types.js";

/** Confidence levels ordered from lowest to highest. */
const CONFIDENCE_ORDER: ConfidenceLevel[] = ["low", "medium", "high"];

/**
 * Build Vitest positional arguments from an impact analysis result.
 *
 * Returns an array of test file paths that should be passed as
 * positional arguments to the test command. Returns an empty array
 * when the full suite should be run instead.
 *
 * @param result - Impact analysis result from analyzeImpact()
 * @param config - Configuration with minConfidence threshold
 * @param projectRoot - Absolute path to project root for existsSync checks
 * @returns Array of test file paths (empty = run full suite)
 */
export function buildVitestArgs(
  result: ImpactAnalysisResult,
  config: Pick<SelectiveTestRunnerConfig, "minConfidence">,
  projectRoot: string
): string[] {
  const minConfidence = config.minConfidence ?? "low";
  const minIndex = CONFIDENCE_ORDER.indexOf(minConfidence);

  // Filter tests by confidence threshold
  const filteredTests = result.affectedTests.filter((test) => {
    const testIndex = CONFIDENCE_ORDER.indexOf(test.confidence);
    return testIndex >= minIndex;
  });

  // Deduplicate test file paths
  const uniqueFiles = [...new Set(filteredTests.map((t) => t.testFile))];

  // Filter out files that don't exist on disk
  const existingFiles = uniqueFiles.filter((file) => {
    const absPath = resolve(projectRoot, file);
    return existsSync(absPath);
  });

  return existingFiles;
}
