/**
 * Source-to-Test Dependency Graph — build, query, and serialize a graph
 * mapping source files to the test files that exercise them.
 *
 * Uses regex-based import extraction with BFS transitive closure.
 * No TypeScript compiler API — regex covers all static import patterns
 * in this codebase without the overhead.
 *
 * @see Issue #1970 - Source-to-test dependency graph
 */

import { readdir, readFile, stat, writeFile } from "fs/promises";
import { dirname, join, relative, resolve } from "path";

import {
  BuildOptionsSchema,
  DependencyGraphSchema,
  type BuildOptions,
  type DependencyGraph,
  type GraphQueryResult,
} from "./graph-types.js";

// ---------------------------------------------------------------------------
// File Discovery
// ---------------------------------------------------------------------------

const TS_EXTENSIONS = [".ts", ".tsx"];
const TEST_PATTERN = /\.(test|spec)\.(ts|tsx|js|jsx)$/;
const SOURCE_PATTERN = /\.(ts|tsx)$/;

/** Recursively discover TypeScript source and test files in a package directory. */
async function discoverFiles(
  packageDir: string,
  projectRoot: string
): Promise<{ sourceFiles: string[]; testFiles: string[] }> {
  const sourceFiles: string[] = [];
  const testFiles: string[] = [];

  async function walk(dir: string): Promise<void> {
    let entries;
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      return; // directory inaccessible — skip
    }

    for (const entry of entries) {
      const fullPath = join(dir, entry.name);

      if (entry.isDirectory()) {
        if (entry.name === "node_modules" || entry.name === "dist") continue;
        await walk(fullPath);
      } else if (entry.isFile() && SOURCE_PATTERN.test(entry.name)) {
        const relPath = relative(projectRoot, fullPath);
        if (TEST_PATTERN.test(entry.name)) {
          testFiles.push(relPath);
        } else {
          sourceFiles.push(relPath);
        }
      }
    }
  }

  // Walk both src/ and tests/ directories within the package
  const srcDir = join(projectRoot, packageDir, "src");
  const testsDir = join(projectRoot, packageDir, "tests");

  await Promise.all([walk(srcDir), walk(testsDir)]);

  return { sourceFiles, testFiles };
}

// ---------------------------------------------------------------------------
// Import Extraction (regex-based)
// ---------------------------------------------------------------------------

/**
 * Regex patterns for extracting import specifiers from TypeScript source.
 *
 * Matches:
 * 1. import ... from './path'
 * 2. export ... from './path'
 * 3. import('./path')
 * 4. require('./path')
 */
const IMPORT_PATTERNS = [
  // import ... from './path'  or  import './path'
  /(?:import|export)\s+(?:[\s\S]*?\s+from\s+)?['"]([^'"]+)['"]/g,
  // dynamic import('...')
  /import\s*\(\s*['"]([^'"]+)['"]\s*\)/g,
  // require('...')
  /require\s*\(\s*['"]([^'"]+)['"]\s*\)/g,
];

/** Extract relative import paths from TypeScript source content. */
export function extractImports(fileContent: string): string[] {
  const imports = new Set<string>();

  for (const pattern of IMPORT_PATTERNS) {
    // Reset lastIndex for global regexes
    const regex = new RegExp(pattern.source, pattern.flags);
    let match;
    while ((match = regex.exec(fileContent)) !== null) {
      const specifier = match[1];
      // Only keep relative imports (starting with . or ..)
      if (specifier.startsWith(".")) {
        imports.add(specifier);
      }
    }
  }

  return [...imports];
}

// ---------------------------------------------------------------------------
// Import Resolution
// ---------------------------------------------------------------------------

/** Try to resolve a relative import to an existing file path. */
async function resolveImport(
  importPath: string,
  fromFile: string,
  projectRoot: string
): Promise<string | null> {
  // Resolve relative to the importing file's directory
  const fromDir = dirname(resolve(projectRoot, fromFile));
  const basePath = resolve(fromDir, importPath);

  // Strip .js/.jsx extension if present (ESM imports use .js but files are .ts)
  const stripped = basePath.replace(/\.(js|jsx)$/, "");

  // Candidates: exact, .ts, .tsx, /index.ts, /index.tsx
  const candidates = [
    basePath,
    ...TS_EXTENSIONS.map((ext) => stripped + ext),
    ...TS_EXTENSIONS.map((ext) => join(stripped, "index" + ext)),
  ];

  // Also try the original path with ts extensions directly
  if (!TS_EXTENSIONS.some((ext) => basePath.endsWith(ext))) {
    for (const ext of TS_EXTENSIONS) {
      candidates.push(basePath + ext);
    }
  }

  for (const candidate of candidates) {
    try {
      const s = await stat(candidate);
      if (s.isFile()) {
        return relative(projectRoot, candidate);
      }
    } catch {
      // candidate does not exist — try next
    }
  }

  return null;
}

// ---------------------------------------------------------------------------
// Import Graph Construction
// ---------------------------------------------------------------------------

/**
 * Build the raw import graph: for each file, resolve its imports to
 * concrete file paths within the project.
 */
async function buildImportGraph(
  allFiles: string[],
  projectRoot: string
): Promise<Record<string, string[]>> {
  const graph: Record<string, string[]> = {};
  const allFilesSet = new Set(allFiles);

  await Promise.all(
    allFiles.map(async (file) => {
      const absPath = resolve(projectRoot, file);
      let content: string;
      try {
        content = await readFile(absPath, "utf-8");
      } catch {
        graph[file] = [];
        return;
      }

      const rawImports = extractImports(content);
      const resolved: string[] = [];

      for (const imp of rawImports) {
        const target = await resolveImport(imp, file, projectRoot);
        if (target && allFilesSet.has(target)) {
          resolved.push(target);
        }
      }

      graph[file] = resolved;
    })
  );

  return graph;
}

// ---------------------------------------------------------------------------
// Transitive Closure (BFS)
// ---------------------------------------------------------------------------

const MAX_DEPTH = 20;

/**
 * Compute the transitive closure of all source files reachable from a
 * starting file via the import graph. Uses BFS with cycle detection.
 */
function computeTransitiveClosure(
  startFile: string,
  importGraph: Record<string, string[]>
): Set<string> {
  const visited = new Set<string>();
  const queue: Array<{ file: string; depth: number }> = [{ file: startFile, depth: 0 }];

  while (queue.length > 0) {
    const { file, depth } = queue.shift()!;

    if (visited.has(file) || depth > MAX_DEPTH) continue;
    visited.add(file);

    const imports = importGraph[file];
    if (imports) {
      for (const imp of imports) {
        if (!visited.has(imp)) {
          queue.push({ file: imp, depth: depth + 1 });
        }
      }
    }
  }

  // Remove the start file itself from the set — it's the starting point, not a dependency
  visited.delete(startFile);
  return visited;
}

// ---------------------------------------------------------------------------
// Graph Building (main entry point)
// ---------------------------------------------------------------------------

/**
 * Build a source-to-test dependency graph for the specified packages.
 *
 * Scans TypeScript source files, extracts imports, computes transitive
 * closures per test file, and produces a serializable `DependencyGraph`.
 */
export async function buildSourceToTestGraph(options: BuildOptions): Promise<DependencyGraph> {
  const opts = BuildOptionsSchema.parse(options);
  const { projectRoot, packages } = opts;

  // Phase A: Discover files in all packages
  const discoveryResults = await Promise.all(
    packages.map((pkg) => discoverFiles(pkg, projectRoot))
  );

  const allSourceFiles: string[] = [];
  const allTestFiles: string[] = [];

  for (const result of discoveryResults) {
    allSourceFiles.push(...result.sourceFiles);
    allTestFiles.push(...result.testFiles);
  }

  const allFiles = [...allSourceFiles, ...allTestFiles];

  // Phase B+C: Build import graph
  const importGraph = await buildImportGraph(allFiles, projectRoot);

  // Phase D+E: Compute transitive closure per test file
  const testToSources: Record<string, string[]> = {};
  const sourceToTests: Record<string, string[]> = {};

  // Initialize sourceToTests for all source files
  for (const source of allSourceFiles) {
    sourceToTests[source] = [];
  }

  for (const testFile of allTestFiles) {
    const reachable = computeTransitiveClosure(testFile, importGraph);
    // Filter to only source files (exclude other test files)
    const sources = [...reachable].filter((f) => !TEST_PATTERN.test(f));
    testToSources[testFile] = sources;

    // Invert: record this test file as covering each source
    for (const source of sources) {
      if (!sourceToTests[source]) {
        sourceToTests[source] = [];
      }
      sourceToTests[source].push(testFile);
    }
  }

  let graph: DependencyGraph = {
    version: "1.0",
    generatedAt: new Date().toISOString(),
    projectRoot,
    packages,
    sourceToTests,
    testToSources,
    importGraph,
  };

  // Phase F: Optional coverage enrichment
  if (opts.coveragePath) {
    graph = await mergeWithCoverage(graph, opts.coveragePath, projectRoot);
  }

  return graph;
}

// ---------------------------------------------------------------------------
// Coverage Enrichment (optional)
// ---------------------------------------------------------------------------

/**
 * Enrich the graph with coverage data from a coverage-final.json file.
 *
 * Adds source files that appear in coverage but have no static import mapping.
 * Does NOT overwrite existing static mappings — coverage is additive only.
 */
async function mergeWithCoverage(
  graph: DependencyGraph,
  coveragePath: string,
  projectRoot: string
): Promise<DependencyGraph> {
  let coverageJson: Record<string, unknown>;
  try {
    const raw = await readFile(coveragePath, "utf-8");
    coverageJson = JSON.parse(raw) as Record<string, unknown>;
  } catch {
    // Coverage file missing or invalid — skip silently
    return graph;
  }

  const enriched = { ...graph, sourceToTests: { ...graph.sourceToTests } };

  for (const absFilePath of Object.keys(coverageJson)) {
    const relPath = relative(projectRoot, absFilePath);
    // Only add if not already in sourceToTests
    if (!(relPath in enriched.sourceToTests)) {
      enriched.sourceToTests[relPath] = [];
    }
  }

  return enriched;
}

// ---------------------------------------------------------------------------
// Query API
// ---------------------------------------------------------------------------

/**
 * Given a list of changed files, return the test files affected by those changes.
 *
 * Uses the pre-built dependency graph to find both direct and transitive matches.
 */
export function getAffectedTests(changedFiles: string[], graph: DependencyGraph): GraphQueryResult {
  const directSet = new Set<string>();
  const transitiveSet = new Set<string>();
  const allAffected = new Set<string>();

  for (const changedFile of changedFiles) {
    // If the changed file is itself a test file, include it directly
    if (TEST_PATTERN.test(changedFile)) {
      allAffected.add(changedFile);
      directSet.add(changedFile);
      continue;
    }

    const testFiles = graph.sourceToTests[changedFile];
    if (!testFiles) continue;

    for (const testFile of testFiles) {
      allAffected.add(testFile);

      // Determine if this is a direct or transitive match
      const directImports = graph.importGraph[testFile] ?? [];
      if (directImports.includes(changedFile)) {
        directSet.add(testFile);
      } else {
        transitiveSet.add(testFile);
      }
    }
  }

  return {
    affectedTests: [...allAffected],
    changedFiles,
    directMatches: [...directSet],
    transitiveMatches: [...transitiveSet],
  };
}

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

/** Serialize a DependencyGraph to a JSON string. */
export function serializeGraph(graph: DependencyGraph): string {
  return JSON.stringify(graph, null, 2);
}

/** Deserialize a JSON string to a validated DependencyGraph. */
export function deserializeGraph(json: string): DependencyGraph {
  const parsed = JSON.parse(json);
  return DependencyGraphSchema.parse(parsed);
}

/** Save a DependencyGraph to a file. */
export async function saveGraph(graph: DependencyGraph, path: string): Promise<void> {
  await writeFile(path, serializeGraph(graph), "utf-8");
}

/** Load a DependencyGraph from a file. */
export async function loadGraph(path: string): Promise<DependencyGraph> {
  const raw = await readFile(path, "utf-8");
  return deserializeGraph(raw);
}
