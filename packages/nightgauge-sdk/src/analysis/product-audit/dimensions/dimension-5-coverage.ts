/**
 * Dimension 5: Test Coverage
 *
 * Identifies files/modules below coverage thresholds and missing test files.
 * Parses lcov.info files; falls back to executing coverage commands.
 *
 * @see Issue #2366 — Implement Dimensions 5-8: Test coverage, security, dependencies, and CI integrity
 */

import * as fs from "fs";
import * as path from "path";
import { execSync } from "child_process";
import { parseLcov, type FileCoverage } from "../utils/coverage-parser.js";

/** Threshold below which a file is flagged as high-risk */
const HIGH_RISK_THRESHOLD = 50;
/** Target coverage threshold (acceptance criteria: 80%) */
const TARGET_THRESHOLD = 80;

export interface CoverageFinding {
  id: string;
  severity: "critical" | "high" | "medium" | "low";
  category: "MISSING_COVERAGE" | "CRITICAL_PATH_UNCOVERED";
  confidence: number;
  repo: string | null;
  dimension: number;
  detail: string;
  auto_fixable: boolean;
  suggested_action: string;
  files: Array<{ path: string; line: null; code_snippet: null }>;
  metadata: {
    detected_at: string;
    detection_method: "coverage_parsing";
    manual_review_required: boolean;
  };
  // TestCoverageFinding fields
  file_or_module: string;
  coverage_percent: number;
  coverage_threshold: number;
  untested_paths: string[];
  critical_path_coverage: "YES" | "NO" | "PARTIAL" | "UNKNOWN";
  test_file_exists: boolean;
}

export interface Dimension5Result {
  findings: CoverageFinding[];
  repos_scanned: string[];
  repos_missing: string[];
  overall_coverage: Record<string, number | null>;
  warnings: string[];
}

interface RepoConfig {
  name: string;
  root: string;
  coverageCommand?: string;
}

/**
 * Determine the coverage command for a repo based on its build tooling.
 */
function inferCoverageCommand(repoRoot: string): string | null {
  if (fs.existsSync(path.join(repoRoot, "package.json"))) {
    return "npm test -- --coverage --coverageReporters=lcov 2>/dev/null";
  }
  if (fs.existsSync(path.join(repoRoot, "go.mod"))) {
    return "go test -cover -coverprofile=coverage.out ./... 2>/dev/null";
  }
  if (fs.existsSync(path.join(repoRoot, "pubspec.yaml"))) {
    return "flutter test --coverage 2>/dev/null";
  }
  return null;
}

/**
 * Locate lcov.info files in a repo (checks common locations).
 */
function findLcovPaths(repoRoot: string): string[] {
  const candidates = ["coverage/lcov.info", "lcov.info", "coverage/lcov.data"];
  const found: string[] = [];

  for (const c of candidates) {
    if (fs.existsSync(path.join(repoRoot, c))) {
      found.push(path.join(repoRoot, c));
    }
  }

  // Monorepo: scan packages/*/coverage/lcov.info
  const pkgsDir = path.join(repoRoot, "packages");
  if (fs.existsSync(pkgsDir)) {
    try {
      for (const pkg of fs.readdirSync(pkgsDir)) {
        const lcov = path.join(pkgsDir, pkg, "coverage", "lcov.info");
        if (fs.existsSync(lcov)) found.push(lcov);
      }
    } catch {
      // Unreadable — skip
    }
  }

  return found;
}

/**
 * Check whether a corresponding test file exists for a source file.
 */
function testFileExists(sourceFile: string, repoRoot: string): boolean {
  const rel = path.relative(repoRoot, sourceFile);
  const ext = path.extname(rel);
  const base = rel.replace(/\.[^/.]+$/, "");

  const candidates = [
    path.join(repoRoot, `${base}.test${ext}`),
    path.join(repoRoot, `${base}.spec${ext}`),
    path.join(repoRoot, "__tests__", `${path.basename(base)}.test${ext}`),
    path.join(repoRoot, "tests", `${base}.test${ext}`),
    path.join(repoRoot, "tests", `${base}.spec${ext}`),
  ];

  return candidates.some((c) => fs.existsSync(c));
}

/**
 * Classify a finding severity based on coverage percentage.
 */
function classifySeverity(coveragePercent: number): "critical" | "high" | "medium" | "low" {
  if (coveragePercent === 0) return "critical";
  if (coveragePercent < 20) return "high";
  if (coveragePercent < HIGH_RISK_THRESHOLD) return "medium";
  return "low";
}

/**
 * Generate a stable finding ID for a coverage gap.
 * Format: coverage-{seq:03d}-{slug}
 */
function makeFindingId(seq: number, filePath: string): string {
  const slug = path
    .basename(filePath, path.extname(filePath))
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .slice(0, 30);
  return `coverage-${String(seq).padStart(3, "0")}-${slug}`;
}

/**
 * Build a CoverageFinding from a FileCoverage entry.
 */
function buildFinding(
  seq: number,
  fileCov: FileCoverage,
  repoName: string,
  repoRoot: string,
  threshold: number
): CoverageFinding {
  const percent = fileCov.linePercent ?? 0;
  const hasTest = testFileExists(fileCov.path, repoRoot);
  const severity = classifySeverity(percent);
  const category: CoverageFinding["category"] =
    percent === 0 ? "CRITICAL_PATH_UNCOVERED" : "MISSING_COVERAGE";

  const detail =
    percent === 0
      ? `${fileCov.path} has 0% line coverage — completely untested`
      : `${fileCov.path} has ${percent.toFixed(1)}% line coverage (threshold: ${threshold}%)`;

  return {
    id: makeFindingId(seq, fileCov.path),
    severity,
    category,
    confidence: 90,
    repo: repoName as CoverageFinding["repo"],
    dimension: 5,
    detail,
    auto_fixable: false,
    suggested_action:
      `Add unit tests for ${path.basename(fileCov.path)} to reach ${threshold}% coverage. ` +
      (hasTest ? "Test file exists — increase test depth." : "No test file found — create one."),
    files: [{ path: fileCov.path, line: null, code_snippet: null }],
    metadata: {
      detected_at: new Date().toISOString(),
      detection_method: "coverage_parsing",
      manual_review_required: false,
    },
    file_or_module: fileCov.path,
    coverage_percent: percent,
    coverage_threshold: threshold,
    untested_paths: [],
    critical_path_coverage: percent === 0 ? "NO" : "PARTIAL",
    test_file_exists: hasTest,
  };
}

/**
 * Run Dimension 5: Test Coverage analysis.
 *
 * @param repos - List of repo configs to scan. Missing repos are skipped gracefully.
 * @param quick - If true, only reads cached lcov.info; never executes coverage commands.
 */
export async function runDimension5(
  repos: RepoConfig[],
  quick: boolean = false
): Promise<Dimension5Result> {
  const findings: CoverageFinding[] = [];
  const reposScanned: string[] = [];
  const reposMissing: string[] = [];
  const overallCoverage: Record<string, number | null> = {};
  const warnings: string[] = [];
  let seq = 0;

  for (const repo of repos) {
    if (!fs.existsSync(repo.root)) {
      reposMissing.push(repo.name);
      warnings.push(`Repo not found: ${repo.name} (${repo.root}) — skipping`);
      continue;
    }

    reposScanned.push(repo.name);
    let lcovPaths = findLcovPaths(repo.root);

    // If no lcov.info found and not in quick mode, try to generate coverage
    if (lcovPaths.length === 0 && !quick) {
      const cmd = repo.coverageCommand ?? inferCoverageCommand(repo.root);
      if (cmd) {
        warnings.push(`No lcov.info in ${repo.name} — running: ${cmd}`);
        try {
          execSync(cmd, {
            cwd: repo.root,
            stdio: "pipe",
            timeout: 300_000,
          });
          lcovPaths = findLcovPaths(repo.root);
        } catch (err) {
          warnings.push(
            `Coverage command failed for ${repo.name}: ${err instanceof Error ? err.message : String(err)}`
          );
        }
      }
    }

    if (lcovPaths.length === 0) {
      warnings.push(`No lcov.info available for ${repo.name} — skipping coverage analysis`);
      overallCoverage[repo.name] = null;
      continue;
    }

    // Parse all lcov files for this repo and aggregate
    const allFiles: FileCoverage[] = [];

    for (const lcovPath of lcovPaths) {
      try {
        const content = fs.readFileSync(lcovPath, "utf8");
        const parsed = parseLcov(content);
        allFiles.push(...parsed.files);
        warnings.push(...parsed.warnings);
      } catch (err) {
        warnings.push(
          `Failed to read ${lcovPath}: ${err instanceof Error ? err.message : String(err)}`
        );
      }
    }

    if (allFiles.length === 0) {
      overallCoverage[repo.name] = null;
      continue;
    }

    // Compute overall coverage for this repo
    const totalFound = allFiles.reduce((s, f) => s + f.linesFound, 0);
    const totalHit = allFiles.reduce((s, f) => s + f.linesHit, 0);
    overallCoverage[repo.name] = totalFound > 0 ? (totalHit / totalFound) * 100 : null;

    // Generate findings for files below threshold
    for (const fileCov of allFiles) {
      const percent = fileCov.linePercent;
      if (percent === null) continue;
      if (percent >= TARGET_THRESHOLD) continue;

      seq++;
      findings.push(buildFinding(seq, fileCov, repo.name, repo.root, TARGET_THRESHOLD));
    }
  }

  // Sort: critical first, then by coverage ascending
  findings.sort((a, b) => {
    const severityOrder = { critical: 0, high: 1, medium: 2, low: 3 };
    const sOrder = (severityOrder[a.severity] ?? 4) - (severityOrder[b.severity] ?? 4);
    if (sOrder !== 0) return sOrder;
    return a.coverage_percent - b.coverage_percent;
  });

  return {
    findings,
    repos_scanned: reposScanned,
    repos_missing: reposMissing,
    overall_coverage: overallCoverage,
    warnings,
  };
}
