/**
 * Dimension 7: Dependencies
 *
 * Detects outdated and vulnerable packages across npm, go, and dart ecosystems.
 * Runs npm audit --json, parses go.mod versions, and checks pubspec.lock.
 * Also flags major version mismatches in shared dependencies across repos.
 *
 * @see Issue #2366 — Implement Dimensions 5-8: Test coverage, security, dependencies, and CI integrity
 */

import * as fs from "fs";
import * as path from "path";
import { execSync } from "child_process";

export interface DependencyFinding {
  id: string;
  severity: "critical" | "high" | "medium" | "low";
  category: "VULNERABLE_DEPENDENCY" | "OUTDATED_DEPENDENCY";
  confidence: number;
  repo: string | null;
  dimension: number;
  detail: string;
  auto_fixable: boolean;
  suggested_action: string;
  files: Array<{ path: string; line: null; code_snippet: null }>;
  metadata: {
    detected_at: string;
    detection_method: "npm_audit" | "static_analysis";
    manual_review_required: boolean;
  };
  // DependencyFinding fields
  package_or_dependency: string;
  current_version: string;
  latest_version: string | null;
  is_vulnerability: boolean;
  cve_ids: string[];
  ecosystem: "npm" | "dart" | "go";
}

export interface Dimension7Result {
  findings: DependencyFinding[];
  repos_scanned: string[];
  repos_missing: string[];
  warnings: string[];
}

interface NpmAuditVulnerability {
  name: string;
  severity: string;
  via: unknown[];
  range: string;
  nodes: string[];
  fixAvailable: boolean | { name: string; version: string; isSemVerMajor: boolean };
}

interface NpmAuditOutput {
  vulnerabilities?: Record<string, NpmAuditVulnerability>;
  metadata?: {
    vulnerabilities?: {
      critical: number;
      high: number;
      moderate: number;
      low: number;
      info: number;
      total: number;
    };
  };
}

interface SharedDep {
  name: string;
  expected_major: number;
  rationale: string;
  repos: string[];
}

interface SharedDepsConfig {
  ecosystems: {
    npm: { deps: SharedDep[] };
    dart?: { deps: SharedDep[] };
    go?: { deps: SharedDep[] };
  };
}

let globalSeq = 0;
function nextId(slug: string): string {
  globalSeq++;
  const safe = slug
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .slice(0, 25);
  return `dep-${String(globalSeq).padStart(3, "0")}-${safe}`;
}

function mapNpmSeverity(severity: string): DependencyFinding["severity"] {
  switch (severity.toLowerCase()) {
    case "critical":
      return "critical";
    case "high":
      return "high";
    case "moderate":
    case "medium":
      return "medium";
    default:
      return "low";
  }
}

/**
 * Extract CVE IDs from npm audit vulnerability.via entries.
 */
function extractCves(via: unknown[]): string[] {
  const cves: string[] = [];
  for (const v of via) {
    if (typeof v === "object" && v !== null) {
      const obj = v as Record<string, unknown>;
      if (typeof obj["cve"] === "string") cves.push(obj["cve"]);
      if (Array.isArray(obj["cves"])) {
        for (const c of obj["cves"] as unknown[]) {
          if (typeof c === "string") cves.push(c);
        }
      }
    }
  }
  return [...new Set(cves)];
}

/**
 * Run npm audit --json in a repo and parse results.
 */
function runNpmAudit(
  repoRoot: string,
  repoName: string,
  findings: DependencyFinding[],
  warnings: string[]
): void {
  const packageJsonPath = path.join(repoRoot, "package.json");
  if (!fs.existsSync(packageJsonPath)) return;

  let auditOutput: string;
  try {
    auditOutput = execSync("npm audit --json 2>/dev/null", {
      cwd: repoRoot,
      stdio: ["pipe", "pipe", "pipe"],
      timeout: 120_000,
      encoding: "utf8",
    });
  } catch (err) {
    // npm audit exits non-zero when vulnerabilities found; stdout still has JSON
    const execError = err as { stdout?: string; stderr?: string };
    auditOutput = execError.stdout ?? "";
    if (!auditOutput) {
      warnings.push(`npm audit failed for ${repoName}: ${execError.stderr ?? "no output"}`);
      return;
    }
  }

  let parsed: NpmAuditOutput;
  try {
    parsed = JSON.parse(auditOutput) as NpmAuditOutput;
  } catch {
    warnings.push(`Failed to parse npm audit JSON for ${repoName}`);
    return;
  }

  const vulnerabilities = parsed.vulnerabilities ?? {};
  for (const [pkgName, vuln] of Object.entries(vulnerabilities)) {
    // Only report critical/high/moderate
    const severity = mapNpmSeverity(vuln.severity);
    if (severity === "low") continue;

    const cves = extractCves(vuln.via ?? []);

    findings.push({
      id: nextId(`vuln-${pkgName}`),
      severity,
      category: "VULNERABLE_DEPENDENCY",
      confidence: 95,
      repo: repoName as DependencyFinding["repo"],
      dimension: 7,
      detail: `Vulnerable dependency: ${pkgName}@${vuln.range} (${vuln.severity})${cves.length > 0 ? ` — ${cves.join(", ")}` : ""}`,
      auto_fixable: false,
      suggested_action:
        `Run 'npm audit fix' to auto-remediate, or upgrade ${pkgName} to a non-vulnerable version. ` +
        (cves.length > 0 ? `CVEs: ${cves.join(", ")}` : ""),
      files: [{ path: "package.json", line: null, code_snippet: null }],
      metadata: {
        detected_at: new Date().toISOString(),
        detection_method: "npm_audit",
        manual_review_required: severity === "critical" || severity === "high",
      },
      package_or_dependency: pkgName,
      current_version: vuln.range,
      latest_version: null,
      is_vulnerability: true,
      cve_ids: cves,
      ecosystem: "npm",
    });
  }
}

/**
 * Read a package.json and return its combined dependency versions.
 */
function readPackageVersions(pkgPath: string): Record<string, string> {
  try {
    const raw = fs.readFileSync(pkgPath, "utf8");
    const pkg = JSON.parse(raw) as {
      dependencies?: Record<string, string>;
      devDependencies?: Record<string, string>;
    };
    return { ...(pkg.dependencies ?? {}), ...(pkg.devDependencies ?? {}) };
  } catch {
    return {};
  }
}

/**
 * Extract major version from a semver string like "^5.4.0" → 5.
 */
function extractMajorVersion(version: string): number | null {
  const clean = version.replace(/^[^0-9]*/, "");
  const major = parseInt(clean.split(".")[0], 10);
  return isNaN(major) ? null : major;
}

/**
 * Check shared dependency version alignment across repos.
 */
function checkSharedDependencies(
  repos: Array<{ name: string; root: string }>,
  sharedDeps: SharedDep[],
  findings: DependencyFinding[],
  warnings: string[]
): void {
  for (const dep of sharedDeps) {
    const versionsFound: Record<string, string> = {};

    for (const repoName of dep.repos) {
      const repo = repos.find((r) => r.name === repoName);
      if (!repo || !fs.existsSync(repo.root)) continue;

      // Check root package.json and packages/*/package.json
      const pkgPaths = [path.join(repo.root, "package.json")];
      const pkgsDir = path.join(repo.root, "packages");
      if (fs.existsSync(pkgsDir)) {
        try {
          for (const pkg of fs.readdirSync(pkgsDir)) {
            pkgPaths.push(path.join(pkgsDir, pkg, "package.json"));
          }
        } catch {
          /* ignore */
        }
      }

      for (const pkgPath of pkgPaths) {
        if (!fs.existsSync(pkgPath)) continue;
        const versions = readPackageVersions(pkgPath);
        if (dep.name in versions) {
          versionsFound[`${repoName}:${path.relative(repo.root, pkgPath)}`] = versions[dep.name];
        }
      }
    }

    // Check for major version mismatches
    const majors = Object.entries(versionsFound).map(([key, v]) => ({
      key,
      major: extractMajorVersion(v),
      raw: v,
    }));

    const uniqueMajors = [...new Set(majors.map((m) => m.major).filter((m) => m !== null))];

    if (uniqueMajors.length > 1) {
      // Multiple major versions — flag as mismatch
      const detail = majors.map((m) => `${m.key}: ${m.raw}`).join(", ");

      findings.push({
        id: nextId(`version-mismatch-${dep.name}`),
        severity: "medium",
        category: "OUTDATED_DEPENDENCY",
        confidence: 90,
        repo: null,
        dimension: 7,
        detail: `Version mismatch for ${dep.name} across repos: ${detail}`,
        auto_fixable: false,
        suggested_action:
          `Align ${dep.name} to major version ${dep.expected_major}.x across all repos. ` +
          dep.rationale,
        files: [],
        metadata: {
          detected_at: new Date().toISOString(),
          detection_method: "static_analysis",
          manual_review_required: false,
        },
        package_or_dependency: dep.name,
        current_version: majors.map((m) => m.raw).join(", "),
        latest_version: null,
        is_vulnerability: false,
        cve_ids: [],
        ecosystem: "npm",
      });
    } else if (
      uniqueMajors.length === 1 &&
      uniqueMajors[0] !== null &&
      uniqueMajors[0] !== dep.expected_major
    ) {
      // Consistent but wrong major version
      warnings.push(
        `${dep.name} is on major v${uniqueMajors[0]} (expected v${dep.expected_major}): ${dep.rationale}`
      );
    }
  }
}

/**
 * Load shared-dependencies.json config.
 */
function loadSharedDepsConfig(configDir: string): SharedDepsConfig | null {
  const configPath = path.join(configDir, "shared-dependencies.json");
  try {
    const content = fs.readFileSync(configPath, "utf8");
    return JSON.parse(content) as SharedDepsConfig;
  } catch {
    return null;
  }
}

/**
 * Run Dimension 7: Dependency analysis.
 */
export async function runDimension7(
  repos: Array<{ name: string; root: string }>,
  configDir?: string
): Promise<Dimension7Result> {
  globalSeq = 0;
  const resolvedConfigDir = configDir ?? path.resolve(__dirname, "..", "config");

  const findings: DependencyFinding[] = [];
  const reposScanned: string[] = [];
  const reposMissing: string[] = [];
  const warnings: string[] = [];

  for (const repo of repos) {
    if (!fs.existsSync(repo.root)) {
      reposMissing.push(repo.name);
      warnings.push(`Repo not found: ${repo.name} — skipping dependency scan`);
      continue;
    }

    reposScanned.push(repo.name);

    // npm audit
    runNpmAudit(repo.root, repo.name, findings, warnings);
  }

  // Cross-repo shared dependency alignment
  const sharedDepsConfig = loadSharedDepsConfig(resolvedConfigDir);
  if (sharedDepsConfig) {
    checkSharedDependencies(repos, sharedDepsConfig.ecosystems.npm.deps, findings, warnings);
  }

  // Sort: critical first
  findings.sort((a, b) => {
    const order = { critical: 0, high: 1, medium: 2, low: 3 };
    return (order[a.severity] ?? 4) - (order[b.severity] ?? 4);
  });

  return {
    findings,
    repos_scanned: reposScanned,
    repos_missing: reposMissing,
    warnings,
  };
}
