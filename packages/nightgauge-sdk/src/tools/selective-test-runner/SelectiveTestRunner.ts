/**
 * SelectiveTestRunner — graph-backed selective test selection.
 *
 * Wraps ChangeImpactAnalyzer and SourceToTestGraph to determine which
 * tests need to run for a given set of changed files. Used by the
 * feature-validate pipeline stage to reduce validation cost.
 *
 * @see Issue #1973 - Selective Test Runner
 */

import { existsSync } from "fs";

import { buildSourceToTestGraph, loadGraph } from "../../analysis/SourceToTestGraph.js";
import { analyzeImpact } from "../../analysis/ChangeImpactAnalyzer.js";
import type { DependencyGraph } from "../../analysis/graph-types.js";
import type { DiffEntry } from "../../analysis/change-impact-types.js";
import { buildVitestArgs } from "./VitestFilterBuilder.js";
import type { SelectiveTestRunnerConfig, SelectiveTestResult } from "./types.js";

/**
 * Selective test runner that uses the dependency graph and change impact
 * analysis to determine which tests to run.
 *
 * Usage:
 * ```typescript
 * const runner = new SelectiveTestRunner({ mode: 'auto', projectRoot: '/repo' });
 * const result = await runner.selectTests(['src/foo/bar.ts']);
 * // result.vitestArgs contains the test file paths to pass to Vitest
 * ```
 */
export class SelectiveTestRunner {
  private readonly config: SelectiveTestRunnerConfig;

  constructor(config: SelectiveTestRunnerConfig) {
    this.config = config;
  }

  /**
   * Given a list of changed file paths (from dev-{N}.json files_changed),
   * determine which tests to run.
   *
   * Algorithm:
   * 1. If mode='never', return full suite
   * 2. Load or build DependencyGraph
   * 3. Call analyzeImpact(parsedDiff, graph)
   * 4. If impactLevel='infrastructure', return full suite
   * 5. If mode='auto' and impactLevel='cross-cutting', return full suite
   * 6. Build vitestArgs via VitestFilterBuilder
   * 7. If vitestArgs is empty, return full suite
   * 8. Return selective result
   */
  async selectTests(changedFiles: string[]): Promise<SelectiveTestResult> {
    const projectRoot = this.config.projectRoot ?? process.cwd();

    // Mode 'never' — always full suite
    if (this.config.mode === "never") {
      return this.fullSuiteResult('mode set to "never"', "isolated");
    }

    // Empty changed files — nothing to analyze
    if (changedFiles.length === 0) {
      return this.fullSuiteResult("no changed files provided", "isolated");
    }

    // Load or build dependency graph
    let graph: DependencyGraph;
    try {
      graph = await this.loadOrBuildGraph(projectRoot);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      return this.fullSuiteResult(`graph unavailable: ${msg}`, "isolated");
    }

    // Convert changed file paths to DiffEntry format (assume modified)
    const diffEntries: DiffEntry[] = changedFiles.map((path) => ({
      status: "modified" as const,
      path,
    }));

    // Analyze impact
    const impactResult = analyzeImpact(diffEntries, graph);
    const { impactLevel } = impactResult;

    // Infrastructure changes — always full suite
    if (impactLevel === "infrastructure") {
      return this.fullSuiteResult("infrastructure change detected", impactLevel);
    }

    // Auto mode + cross-cutting — full suite
    if (this.config.mode === "auto" && impactLevel === "cross-cutting") {
      return this.fullSuiteResult("cross-cutting change detected", impactLevel);
    }

    // Build filtered test file list
    const testFiles = buildVitestArgs(
      impactResult,
      { minConfidence: this.config.minConfidence },
      projectRoot
    );

    // No tests found — fall back to full suite
    if (testFiles.length === 0) {
      return this.fullSuiteResult("no affected tests identified", impactLevel);
    }

    // Count total tests in graph for reporting
    const totalTests = Object.keys(graph.testToSources).length;

    return {
      mode: "selective",
      reason: `${testFiles.length} affected test(s) identified via dependency graph`,
      testFiles,
      impactLevel,
      totalTests,
      selectedTests: testFiles.length,
      skippedTests: totalTests > 0 ? totalTests - testFiles.length : null,
      vitestArgs: testFiles,
    };
  }

  /**
   * Load graph from cache or build fresh.
   */
  private async loadOrBuildGraph(projectRoot: string): Promise<DependencyGraph> {
    // Try cached graph first
    if (this.config.graphCachePath && existsSync(this.config.graphCachePath)) {
      return loadGraph(this.config.graphCachePath);
    }

    // Build fresh graph — discover packages by checking common locations
    const packages = await this.discoverPackages(projectRoot);
    return buildSourceToTestGraph({
      projectRoot,
      packages,
      includeNodeModules: false,
    });
  }

  /**
   * Discover package directories within the project root.
   * Checks for common monorepo patterns.
   */
  private async discoverPackages(projectRoot: string): Promise<string[]> {
    const { readdir } = await import("fs/promises");
    const { join } = await import("path");

    // Check for packages/ directory (monorepo pattern)
    const packagesDir = join(projectRoot, "packages");
    if (existsSync(packagesDir)) {
      try {
        const entries = await readdir(packagesDir, { withFileTypes: true });
        return entries.filter((e) => e.isDirectory()).map((e) => `packages/${e.name}`);
      } catch {
        // Fall through to root-level
      }
    }

    // Single-package project: use root
    return ["."];
  }

  /**
   * Construct a full-suite result.
   */
  private fullSuiteResult(reason: string, impactLevel: string): SelectiveTestResult {
    return {
      mode: "full",
      reason,
      testFiles: null,
      impactLevel,
      totalTests: null,
      selectedTests: 0,
      skippedTests: null,
      vitestArgs: [],
    };
  }
}
