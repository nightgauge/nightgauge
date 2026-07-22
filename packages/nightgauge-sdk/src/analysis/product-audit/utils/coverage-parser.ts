/**
 * Coverage Parser — lcov.info format reader
 *
 * Parses lcov.info files produced by npm test --coverage, go test -cover, etc.
 * Returns a per-file coverage map with line and branch coverage percentages.
 */

export interface FileCoverage {
  /** Relative path to source file */
  path: string;
  /** Line coverage percentage (0-100), null if not recorded */
  linePercent: number | null;
  /** Branch coverage percentage (0-100), null if not recorded */
  branchPercent: number | null;
  /** Lines found (total executable lines) */
  linesFound: number;
  /** Lines hit (executed lines) */
  linesHit: number;
  /** Branches found */
  branchesFound: number;
  /** Branches hit */
  branchesHit: number;
}

export interface CoverageParseResult {
  files: FileCoverage[];
  /** Overall line coverage % across all files */
  overallLinePercent: number | null;
  /** Overall branch coverage % across all files */
  overallBranchPercent: number | null;
  /** Parse warnings (e.g., malformed lines) */
  warnings: string[];
}

/**
 * Parse lcov.info content into structured coverage data.
 *
 * lcov.info format per section:
 *   SF:<source file path>
 *   DA:<line number>,<hit count>[,<branch data>]
 *   BRH:<branches hit>
 *   BRF:<branches found>
 *   LH:<lines hit>
 *   LF:<lines found>
 *   end_of_record
 */
export function parseLcov(content: string): CoverageParseResult {
  const files: FileCoverage[] = [];
  const warnings: string[] = [];

  // Split into per-file sections by "end_of_record"
  const sections = content.split(/^end_of_record\s*$/m);

  for (const section of sections) {
    const lines = section.trim().split("\n");
    if (lines.length === 0 || lines.every((l) => l.trim() === "")) {
      continue;
    }

    let path = "";
    let linesFound = 0;
    let linesHit = 0;
    let branchesFound = 0;
    let branchesHit = 0;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed.startsWith("SF:")) {
        path = trimmed.slice(3).trim();
      } else if (trimmed.startsWith("LF:")) {
        const val = parseInt(trimmed.slice(3), 10);
        if (!isNaN(val)) linesFound = val;
      } else if (trimmed.startsWith("LH:")) {
        const val = parseInt(trimmed.slice(3), 10);
        if (!isNaN(val)) linesHit = val;
      } else if (trimmed.startsWith("BRF:")) {
        const val = parseInt(trimmed.slice(4), 10);
        if (!isNaN(val)) branchesFound = val;
      } else if (trimmed.startsWith("BRH:")) {
        const val = parseInt(trimmed.slice(4), 10);
        if (!isNaN(val)) branchesHit = val;
      }
    }

    if (!path) {
      warnings.push("Found lcov section without SF: line");
      continue;
    }

    const linePercent = linesFound > 0 ? (linesHit / linesFound) * 100 : null;
    const branchPercent = branchesFound > 0 ? (branchesHit / branchesFound) * 100 : null;

    files.push({
      path,
      linePercent,
      branchPercent,
      linesFound,
      linesHit,
      branchesFound,
      branchesHit,
    });
  }

  // Compute overall stats
  const totalLinesFound = files.reduce((s, f) => s + f.linesFound, 0);
  const totalLinesHit = files.reduce((s, f) => s + f.linesHit, 0);
  const totalBranchesFound = files.reduce((s, f) => s + f.branchesFound, 0);
  const totalBranchesHit = files.reduce((s, f) => s + f.branchesHit, 0);

  const overallLinePercent = totalLinesFound > 0 ? (totalLinesHit / totalLinesFound) * 100 : null;
  const overallBranchPercent =
    totalBranchesFound > 0 ? (totalBranchesHit / totalBranchesFound) * 100 : null;

  return { files, overallLinePercent, overallBranchPercent, warnings };
}

/**
 * Find all lcov.info files within a directory tree.
 * Returns relative paths from the given root.
 */
export function findLcovFiles(
  repoRoot: string,
  fs: {
    readdirSync: (dir: string) => string[];
    existsSync: (p: string) => boolean;
  }
): string[] {
  const candidates = ["coverage/lcov.info", "lcov.info", "coverage/lcov.data"];

  // Check each package workspace too
  const found: string[] = [];
  for (const candidate of candidates) {
    const full = `${repoRoot}/${candidate}`;
    if (fs.existsSync(full)) {
      found.push(candidate);
    }
  }

  // Also scan packages/* for monorepo
  const packagesDir = `${repoRoot}/packages`;
  if (fs.existsSync(packagesDir)) {
    try {
      const pkgs = fs.readdirSync(packagesDir);
      for (const pkg of pkgs) {
        const lcovPath = `packages/${pkg}/coverage/lcov.info`;
        if (fs.existsSync(`${repoRoot}/${lcovPath}`)) {
          found.push(lcovPath);
        }
      }
    } catch {
      // Directory not readable — skip
    }
  }

  return found;
}
