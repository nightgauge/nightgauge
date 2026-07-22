/**
 * Test File Mapper - Source-to-test file mapping for targeted test selection
 *
 * Maps changed source files to their corresponding test files using naming
 * conventions. Used by the feature-validate stage to run only relevant tests
 * instead of the full suite, reducing validation cost.
 *
 * @see Issue #1046 - Optimize feature-validate stage cost
 */

import { existsSync } from "fs";
import { basename, dirname, join, resolve } from "path";

/**
 * Map source files to candidate test files using naming conventions.
 * Returns only test files that exist on disk.
 *
 * Mapping heuristics (applied to each source file):
 * 1. `src/foo/bar.ts` → `tests/foo/bar.test.ts`
 * 2. `src/foo/bar.ts` → `src/foo/__tests__/bar.test.ts`
 * 3. `src/foo/bar.ts` → `src/__tests__/foo/bar.test.ts`
 * 4. Test files in the changed list pass through directly
 *
 * @param sourceFiles - List of changed source file paths (relative to project root)
 * @param projectRoot - Absolute path to project root
 * @returns Deduplicated list of existing test file paths (relative to project root)
 */
export function mapSourceToTestFiles(sourceFiles: string[], projectRoot: string): string[] {
  const candidates = new Set<string>();

  for (const file of sourceFiles) {
    // Test files pass through directly
    if (isTestFile(file)) {
      const absPath = resolve(projectRoot, file);
      if (existsSync(absPath)) {
        candidates.add(file);
      }
      continue;
    }

    // Skip non-TypeScript/JavaScript files
    if (!isSourceFile(file)) {
      continue;
    }

    const dir = dirname(file);
    const base = basename(file).replace(/\.(ts|tsx|js|jsx)$/, "");
    const ext = file.match(/\.(ts|tsx|js|jsx)$/)?.[0] ?? ".ts";
    const testExt = ext.replace(/^\./, ".test.");

    // Heuristic 1: src/foo/bar.ts → tests/foo/bar.test.ts
    if (dir.startsWith("src/") || dir === "src") {
      const relPath = dir === "src" ? "" : dir.slice(4); // strip 'src/'
      const candidate = join("tests", relPath, `${base}${testExt}`);
      addIfExists(candidate, projectRoot, candidates);
    }

    // Heuristic 2: src/foo/bar.ts → src/foo/__tests__/bar.test.ts
    const siblingTest = join(dir, "__tests__", `${base}${testExt}`);
    addIfExists(siblingTest, projectRoot, candidates);

    // Heuristic 3: src/foo/bar.ts → src/__tests__/foo/bar.test.ts
    if (dir.startsWith("src/")) {
      const relPath = dir.slice(4); // strip 'src/'
      const rootTest = join("src", "__tests__", relPath, `${base}${testExt}`);
      addIfExists(rootTest, projectRoot, candidates);
    }
  }

  return [...candidates];
}

/**
 * Determine if the change set is "cross-cutting" — affecting too many modules
 * to benefit from targeted testing.
 *
 * Cross-cutting when:
 * - sourceFiles.length exceeds the file count threshold (default: 10)
 * - Files span more than 3 top-level directories
 *
 * @param sourceFiles - List of changed source file paths
 * @param threshold - Maximum file count before cross-cutting (default: 10)
 * @returns true when targeted testing should be skipped
 */
export function isCrossCuttingChange(sourceFiles: string[], threshold = 10): boolean {
  if (sourceFiles.length > threshold) {
    return true;
  }

  // Count unique top-level directories
  const topLevelDirs = new Set<string>();
  for (const file of sourceFiles) {
    const parts = file.split("/");
    if (parts.length > 1) {
      topLevelDirs.add(parts[0]);
    }
  }

  return topLevelDirs.size > 3;
}

/** Check if a file path looks like a test file */
function isTestFile(filePath: string): boolean {
  return /\.(test|spec)\.(ts|tsx|js|jsx)$/.test(filePath);
}

/** Check if a file path is a TypeScript/JavaScript source file */
function isSourceFile(filePath: string): boolean {
  return /\.(ts|tsx|js|jsx)$/.test(filePath);
}

/** Add a candidate test file to the set if it exists on disk */
function addIfExists(candidate: string, projectRoot: string, set: Set<string>): void {
  const absPath = resolve(projectRoot, candidate);
  if (existsSync(absPath)) {
    set.add(candidate);
  }
}
