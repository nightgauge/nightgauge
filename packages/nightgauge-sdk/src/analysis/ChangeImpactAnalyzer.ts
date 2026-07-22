/**
 * Change Impact Analyzer — determine which modules and tests are affected
 * by a set of file changes, using the source-to-test dependency graph.
 *
 * Three named exports:
 * - `parseDiff` — parse `git diff --name-status` output
 * - `analyzeImpact` — classify impact and identify affected tests
 * - `analyzeImpactFromDiff` — convenience wrapper combining both
 *
 * @see Issue #1971 - Change Impact Analyzer
 */

import type { DependencyGraph } from "./graph-types.js";
import { getAffectedTests } from "./SourceToTestGraph.js";
import { mapSourceToTestFiles } from "./testFileMapper.js";
import {
  ChangeImpactAnalyzerConfigSchema,
  type AffectedSource,
  type AffectedTest,
  type ChangeImpactAnalyzerConfig,
  type DiffEntry,
  type FileChangeStatus,
  type ImpactAnalysisResult,
  type ImpactLevel,
} from "./change-impact-types.js";
import {
  evaluateLowConfidenceTrigger,
  evaluateRegressionTriggers,
  matchesPattern,
} from "./RegressionTriggerEvaluator.js";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const TEST_FILE_PATTERN = /\.(test|spec)\.(ts|tsx|js|jsx)$/;
const MAX_HEURISTIC_PER_FILE = 10;

// ---------------------------------------------------------------------------
// parseDiff
// ---------------------------------------------------------------------------

/**
 * Parse `git diff --name-status` output into structured DiffEntry records.
 *
 * Supports:
 * - Standard format: `M\tpath/to/file.ts`
 * - Renamed/copied entries: `R100\told/path.ts\tnew/path.ts`
 * - `git diff --name-only` fallback (no status prefix → assumed modified)
 *
 * Blank lines are skipped. Unknown status codes produce `status: 'unknown'`.
 * All paths are normalized to forward slashes.
 */
export function parseDiff(diffOutput: string): DiffEntry[] {
  const entries: DiffEntry[] = [];

  for (const rawLine of diffOutput.split("\n")) {
    const line = rawLine.trim();
    if (!line) continue;

    // Match status-prefixed format: STATUS<whitespace>PATH
    const match = line.match(/^([MADRC]\d{0,3})\s+(.+)$/);

    if (match) {
      const statusCode = match[1];
      const rest = match[2];
      const status = mapStatusCode(statusCode[0]);

      if (statusCode[0] === "R" || statusCode[0] === "C") {
        // Rename/copy: two paths separated by tab (or multiple spaces)
        const tabParts = rest.split("\t");
        if (tabParts.length >= 2) {
          entries.push({
            status,
            path: normalizePath(tabParts[1].trim()),
            oldPath: normalizePath(tabParts[0].trim()),
          });
        } else {
          const spaceParts = rest.split(/\s{2,}/);
          if (spaceParts.length >= 2) {
            entries.push({
              status,
              path: normalizePath(spaceParts[1].trim()),
              oldPath: normalizePath(spaceParts[0].trim()),
            });
          } else {
            entries.push({ status, path: normalizePath(rest) });
          }
        }
      } else {
        entries.push({ status, path: normalizePath(rest) });
      }
    } else {
      // No recognized status prefix — treat as --name-only (assume modified)
      entries.push({ status: "modified", path: normalizePath(line) });
    }
  }

  return entries;
}

// ---------------------------------------------------------------------------
// analyzeImpact
// ---------------------------------------------------------------------------

/**
 * Analyze the impact of a set of file changes against a dependency graph.
 *
 * Steps:
 * 1. Check for infrastructure file changes
 * 2. Build effective paths (including old paths for renames)
 * 3. Query graph for affected tests with direct/transitive classification
 * 4. Heuristic fallback for files not in the graph (new/deleted/renamed)
 * 5. Classify overall impact level
 *
 * @param changedFiles - Parsed diff entries
 * @param graph - Pre-built source-to-test dependency graph
 * @param config - Optional configuration overrides
 */
export function analyzeImpact(
  changedFiles: DiffEntry[],
  graph: DependencyGraph,
  config?: ChangeImpactAnalyzerConfig
): ImpactAnalysisResult {
  if (changedFiles.length === 0) {
    return {
      impactLevel: "isolated",
      affectedSources: [],
      affectedTests: [],
      changedFiles: [],
      summary: {
        totalAffectedTests: 0,
        highConfidence: 0,
        mediumConfidence: 0,
        lowConfidence: 0,
        impactLevelReason: "No files changed",
      },
    };
  }

  const cfg = ChangeImpactAnalyzerConfigSchema.parse(config ?? {});

  // Step 1: Pre-graph regression trigger evaluation
  // Checks manual-override first, then file-based triggers (dependency-change,
  // build-config, shared-types, test-infrastructure, ci-config).
  // Short-circuits immediately if any trigger fires.
  const preTrigger = evaluateRegressionTriggers(changedFiles, cfg);

  if (preTrigger.triggered) {
    // Build minimal affectedSources without graph lookup (short-circuit path)
    const minimalSources: AffectedSource[] = changedFiles.map((entry) => ({
      sourceFile: entry.path,
      changeStatus: entry.status,
      hasGraphEntry: false,
    }));

    return {
      impactLevel: "infrastructure",
      affectedSources: minimalSources,
      affectedTests: [],
      changedFiles,
      summary: {
        totalAffectedTests: 0,
        highConfidence: 0,
        mediumConfidence: 0,
        lowConfidence: 0,
        impactLevelReason: preTrigger.reason,
        regressionTrigger: preTrigger,
      },
    };
  }

  // Step 2: Build effective paths for graph lookup
  // For renames: query both old and new paths
  // For deletes: query the deleted path (may still be in graph)
  const effectivePaths: string[] = [];
  for (const entry of changedFiles) {
    effectivePaths.push(entry.path);
    if (entry.oldPath) {
      effectivePaths.push(entry.oldPath);
    }
  }

  // Step 3: Query the dependency graph
  const queryResult = getAffectedTests(effectivePaths, graph);

  // Step 4: Build AffectedTest map with confidence scoring
  // Direct imports → high confidence, transitive → medium
  const affectedTestMap = new Map<string, AffectedTest>();

  for (const testFile of queryResult.directMatches) {
    affectedTestMap.set(testFile, {
      testFile,
      confidence: "high",
      matchType: "direct",
      reason: "Direct import of changed file",
    });
  }

  for (const testFile of queryResult.transitiveMatches) {
    if (!affectedTestMap.has(testFile)) {
      affectedTestMap.set(testFile, {
        testFile,
        confidence: "medium",
        matchType: "transitive",
        reason: "Transitive dependency on changed file",
      });
    }
  }

  // Step 5: Heuristic fallback for files not in the graph
  // Applies to new/deleted/renamed files that have no graph entry
  const uncoveredFiles = effectivePaths.filter(
    (p) => !TEST_FILE_PATTERN.test(p) && graph.sourceToTests[p] === undefined
  );

  for (const uncoveredFile of uncoveredFiles) {
    const heuristicTests = mapSourceToTestFiles([uncoveredFile], graph.projectRoot);
    for (const testFile of heuristicTests.slice(0, MAX_HEURISTIC_PER_FILE)) {
      if (!affectedTestMap.has(testFile)) {
        affectedTestMap.set(testFile, {
          testFile,
          confidence: "low",
          matchType: "heuristic",
          reason: `Heuristic match for ${uncoveredFile} (not in dependency graph)`,
        });
      }
    }
  }

  // Step 6: Build affectedSources (include both paths for renames)
  const affectedSources: AffectedSource[] = [];
  for (const entry of changedFiles) {
    affectedSources.push({
      sourceFile: entry.path,
      changeStatus: entry.status,
      hasGraphEntry: graph.sourceToTests[entry.path] !== undefined,
    });
    if (entry.oldPath) {
      affectedSources.push({
        sourceFile: entry.oldPath,
        changeStatus: entry.status,
        hasGraphEntry: graph.sourceToTests[entry.oldPath] !== undefined,
      });
    }
  }

  const affectedTests = [...affectedTestMap.values()];

  // Step 7: Post-graph low-confidence trigger check
  const partialResult = {
    affectedTests,
    summary: {
      totalAffectedTests: affectedTests.length,
      highConfidence: 0,
      mediumConfidence: 0,
      lowConfidence: 0,
      impactLevelReason: "",
    },
  };
  const lowConfTrigger = evaluateLowConfidenceTrigger(partialResult, cfg);

  if (lowConfTrigger.triggered) {
    return {
      impactLevel: "infrastructure",
      affectedSources,
      affectedTests,
      changedFiles,
      summary: {
        totalAffectedTests: affectedTests.length,
        highConfidence: affectedTests.filter((t) => t.confidence === "high").length,
        mediumConfidence: affectedTests.filter((t) => t.confidence === "medium").length,
        lowConfidence: affectedTests.filter((t) => t.confidence === "low").length,
        impactLevelReason: lowConfTrigger.reason,
        regressionTrigger: lowConfTrigger,
      },
    };
  }

  // Step 8: Backwards-compat infrastructure check via cfg.infrastructurePatterns.
  // The new trigger system (Steps 1 and 7) is the canonical path for infrastructure
  // detection. This check preserves behaviour for callers that set
  // cfg.infrastructurePatterns directly. Unlike the trigger short-circuit, this
  // runs post-graph so affected tests from the graph walk are still included.
  const hasLegacyInfrastructure = changedFiles.some((entry) =>
    matchesPattern(entry.path, cfg.infrastructurePatterns)
  );

  if (hasLegacyInfrastructure) {
    return {
      impactLevel: "infrastructure",
      affectedSources,
      affectedTests,
      changedFiles,
      summary: {
        totalAffectedTests: affectedTests.length,
        highConfidence: affectedTests.filter((t) => t.confidence === "high").length,
        mediumConfidence: affectedTests.filter((t) => t.confidence === "medium").length,
        lowConfidence: affectedTests.filter((t) => t.confidence === "low").length,
        impactLevelReason: "Infrastructure file changed — full regression recommended",
      },
    };
  }

  // Step 9: Classify impact level (only reached when no trigger fired)
  let impactLevel: ImpactLevel;
  let impactLevelReason: string;

  const topDirs = new Set<string>();
  for (const entry of changedFiles) {
    if (entry.path.includes("/")) {
      topDirs.add(entry.path.split("/")[0]);
    }
  }

  const dependentCount = affectedTestMap.size;

  if (
    topDirs.size >= cfg.crossCuttingDirThreshold ||
    dependentCount >= cfg.crossCuttingDepThreshold
  ) {
    impactLevel = "cross-cutting";
    impactLevelReason = `Affects ${topDirs.size} top-level directories and ${dependentCount} test files`;
  } else {
    impactLevel = "isolated";
    impactLevelReason = `Change isolated to ${topDirs.size || 1} directory`;
  }

  return {
    impactLevel,
    affectedSources,
    affectedTests,
    changedFiles,
    summary: {
      totalAffectedTests: affectedTests.length,
      highConfidence: affectedTests.filter((t) => t.confidence === "high").length,
      mediumConfidence: affectedTests.filter((t) => t.confidence === "medium").length,
      lowConfidence: affectedTests.filter((t) => t.confidence === "low").length,
      impactLevelReason,
    },
  };
}

// ---------------------------------------------------------------------------
// analyzeImpactFromDiff
// ---------------------------------------------------------------------------

/**
 * Convenience wrapper: parse a raw git diff string and analyze impact.
 *
 * Equivalent to `analyzeImpact(parseDiff(diffOutput), graph, config)`.
 */
export function analyzeImpactFromDiff(
  diffOutput: string,
  graph: DependencyGraph,
  config?: ChangeImpactAnalyzerConfig
): ImpactAnalysisResult {
  return analyzeImpact(parseDiff(diffOutput), graph, config);
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function mapStatusCode(code: string): FileChangeStatus {
  switch (code) {
    case "M":
      return "modified";
    case "A":
      return "added";
    case "D":
      return "deleted";
    case "R":
      return "renamed";
    case "C":
      return "added"; // copy creates a new file; original remains
    default:
      return "unknown";
  }
}

function normalizePath(p: string): string {
  return p.replace(/\\/g, "/");
}
