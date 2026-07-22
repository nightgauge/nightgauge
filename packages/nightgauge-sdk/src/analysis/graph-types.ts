/**
 * Types and Zod schemas for the source-to-test dependency graph.
 *
 * @see Issue #1970 - Source-to-test dependency graph
 */

import { z } from "zod";

/**
 * Serializable dependency graph mapping source files to their covering test files.
 *
 * Three maps are stored:
 * - `importGraph`: raw import edges (canonical source)
 * - `testToSources`: derived — test file → all source files it covers (transitive)
 * - `sourceToTests`: derived — source file → all test files that cover it (inverted)
 */
export const DependencyGraphSchema = z.object({
  version: z.literal("1.0"),
  generatedAt: z.string().datetime(),
  projectRoot: z.string(),
  packages: z.array(z.string()),
  /** source file path (relative to projectRoot) → test file paths that cover it */
  sourceToTests: z.record(z.string(), z.array(z.string())),
  /** test file path → source file paths it covers (direct + transitive) */
  testToSources: z.record(z.string(), z.array(z.string())),
  /** source file path → directly imported source file paths (raw import graph) */
  importGraph: z.record(z.string(), z.array(z.string())),
});

export type DependencyGraph = z.infer<typeof DependencyGraphSchema>;

export const BuildOptionsSchema = z.object({
  projectRoot: z.string(),
  /** Package directories relative to projectRoot, e.g. ["packages/nightgauge-sdk"] */
  packages: z.array(z.string()),
  /** Path to coverage-final.json for optional enrichment */
  coveragePath: z.string().optional(),
  /** Never include node_modules in the graph */
  includeNodeModules: z.literal(false).default(false),
});

export type BuildOptions = z.infer<typeof BuildOptionsSchema>;

export interface GraphQueryResult {
  /** All test files affected by the changed files */
  affectedTests: string[];
  /** The input changed files */
  changedFiles: string[];
  /** Test files matched by direct import of a changed file */
  directMatches: string[];
  /** Test files matched transitively (not direct importers) */
  transitiveMatches: string[];
}
