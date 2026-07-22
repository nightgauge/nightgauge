import { existsSync, readdirSync } from "fs";
import { join, resolve } from "path";
import type { AuditDimension, AuditFinding } from "../types.js";
import {
  parseDocumentationEndpoints,
  extractRoutesFromFile,
  endpointExistsInRoutes,
} from "../parsers/documentation-parser.js";
import {
  loadOpenApiSpec,
  compareOpenApiSpecs,
  compareSharedTypesVersions,
} from "../parsers/spec-parser.js";
import { validateReadmeCommands, summarizeValidation } from "../matchers/readme-validator.js";

/** Repos to audit, in discovery order. */
const REPO_NAMES = ["nightgauge", "acme-platform", "acme-dashboard", "acme-mobile"];

/** Statuses in ECOSYSTEM.md that indicate the endpoint should exist. */
const AVAILABLE_STATUSES = ["available", "implemented", "working", "complete"];
/** Statuses that indicate the endpoint is NOT yet implemented. */
const NOT_IMPLEMENTED_STATUSES = ["not implemented", "not-implemented", "planned", "stub", "todo"];

export interface Dimension3Options {
  /** Root workspace directory (default: one level above cwd). */
  workspaceRoot?: string;
  /** Override repo paths (useful for testing). */
  repoPaths?: Record<string, string>;
}

/**
 * Discover which repos are available in the workspace.
 * Returns a map of repo name → absolute path.
 */
function discoverRepos(
  workspaceRoot: string,
  overrides?: Record<string, string>
): Map<string, string> {
  const repos = new Map<string, string>();

  for (const name of REPO_NAMES) {
    const path = overrides?.[name] ?? join(workspaceRoot, name);
    if (existsSync(path)) {
      repos.set(name, path);
    } else {
      console.warn(`[dimension-3] Repo not found, skipping: ${path}`);
    }
  }

  return repos;
}

/**
 * Collect all route files from a Hono/Express platform repo.
 * Looks in common locations: src/routes/, src/api/, routes/.
 */
function collectRouteFiles(platformPath: string): string[] {
  const candidates = [
    join(platformPath, "src", "routes"),
    join(platformPath, "src", "api"),
    join(platformPath, "routes"),
  ];

  const files: string[] = [];
  for (const dir of candidates) {
    if (!existsSync(dir)) continue;
    try {
      const entries = readdirSync(dir, { recursive: true }) as string[];
      for (const entry of entries) {
        if (entry.endsWith(".ts") || entry.endsWith(".js")) {
          files.push(join(dir, entry));
        }
      }
    } catch (err) {
      console.warn(`[dimension-3] Could not read directory ${dir}: ${(err as Error).message}`);
    }
  }

  return files;
}

/**
 * Extract all known routes from the platform repo by scanning route files.
 */
function extractPlatformRoutes(platformPath: string): string[] {
  const routeFiles = collectRouteFiles(platformPath);
  const routes: string[] = [];

  for (const file of routeFiles) {
    routes.push(...extractRoutesFromFile(file));
  }

  return routes;
}

/**
 * Phase 2: Validate endpoints documented in ECOSYSTEM.md against platform routes.
 */
function validateDocumentedEndpoints(repos: Map<string, string>, findings: AuditFinding[]): void {
  const platformPath = repos.get("acme-platform");
  const mainPath = repos.get("nightgauge");

  // Collect route files from platform (if available)
  const platformRoutes = platformPath ? extractPlatformRoutes(platformPath) : [];

  // Check ECOSYSTEM.md in the main nightgauge repo
  if (mainPath) {
    const ecosystemPath = join(mainPath, "docs", "ECOSYSTEM.md");
    const endpoints = parseDocumentationEndpoints(ecosystemPath);

    for (const ep of endpoints) {
      const statusLower = ep.status.toLowerCase();
      const isDocumentedAsAvailable = AVAILABLE_STATUSES.some((s) => statusLower.includes(s));
      const isDocumentedAsNotImplemented = NOT_IMPLEMENTED_STATUSES.some((s) =>
        statusLower.includes(s)
      );

      if (platformRoutes.length > 0) {
        const exists = endpointExistsInRoutes(ep.path, platformRoutes);

        if (isDocumentedAsAvailable && !exists) {
          findings.push({
            severity: "high",
            category: "MISSING_ENDPOINT_DOCUMENTED",
            repo: "nightgauge",
            file: ep.file.replace(mainPath + "/", ""),
            line: ep.line,
            detail: `Endpoint ${ep.method} ${ep.path} is documented as "${ep.status}" but was not found in platform route files.`,
            auto_fixable: false,
          });
        } else if (isDocumentedAsNotImplemented && exists) {
          findings.push({
            severity: "medium",
            category: "STALE_DOC_CLAIM",
            repo: "nightgauge",
            file: ep.file.replace(mainPath + "/", ""),
            line: ep.line,
            detail: `Endpoint ${ep.method} ${ep.path} is documented as "${ep.status}" but exists in platform route files — documentation may be stale.`,
            auto_fixable: true,
          });
        }
      }
    }
  }

  // Also check ECOSYSTEM.md in platform repo if available
  if (platformPath) {
    const ecosystemPath = join(platformPath, "docs", "ECOSYSTEM.md");
    const endpoints = parseDocumentationEndpoints(ecosystemPath);
    const platformRoutes2 = extractPlatformRoutes(platformPath);

    for (const ep of endpoints) {
      const statusLower = ep.status.toLowerCase();
      const isDocumentedAsAvailable = AVAILABLE_STATUSES.some((s) => statusLower.includes(s));

      if (platformRoutes2.length > 0 && isDocumentedAsAvailable) {
        const exists = endpointExistsInRoutes(ep.path, platformRoutes2);
        if (!exists) {
          findings.push({
            severity: "high",
            category: "MISSING_ENDPOINT_DOCUMENTED",
            repo: "acme-platform",
            file: ep.file.replace(platformPath + "/", ""),
            line: ep.line,
            detail: `Endpoint ${ep.method} ${ep.path} is documented as "${ep.status}" but was not found in platform route files.`,
            auto_fixable: false,
          });
        }
      }
    }
  }
}

/**
 * Phase 3: Compare Angular openapi.json against platform API spec.
 */
function validateOpenApiSpecs(repos: Map<string, string>, findings: AuditFinding[]): void {
  const platformPath = repos.get("acme-platform");
  const angularPath = repos.get("acme-dashboard");

  if (!platformPath || !angularPath) {
    if (!platformPath)
      console.warn("[dimension-3] Platform repo not available — skipping OpenAPI comparison");
    if (!angularPath)
      console.warn("[dimension-3] Angular repo not available — skipping OpenAPI comparison");
    return;
  }

  // Look for openapi.json in common locations
  const platformSpecCandidates = [
    join(platformPath, "openapi.json"),
    join(platformPath, "docs", "openapi.json"),
    join(platformPath, "src", "openapi.json"),
  ];
  const angularSpecCandidates = [
    join(angularPath, "openapi.json"),
    join(angularPath, "src", "openapi.json"),
    join(angularPath, "src", "app", "openapi.json"),
  ];

  const platformSpecPath = platformSpecCandidates.find((p) => existsSync(p));
  const angularSpecPath = angularSpecCandidates.find((p) => existsSync(p));

  if (!platformSpecPath) {
    console.warn("[dimension-3] Platform OpenAPI spec not found — skipping comparison");
    return;
  }
  if (!angularSpecPath) {
    console.warn("[dimension-3] Angular OpenAPI spec not found — skipping comparison");
    return;
  }

  const platformSpec = loadOpenApiSpec(platformSpecPath);
  const angularSpec = loadOpenApiSpec(angularSpecPath);

  if (!platformSpec || !angularSpec) return;

  const comparison = compareOpenApiSpecs(platformSpec, angularSpec);

  for (const path of comparison.missingInAngular) {
    findings.push({
      severity: "medium",
      category: "OPENAPI_SPEC_STALE",
      repo: "acme-dashboard",
      file: angularSpecPath.replace(angularPath + "/", ""),
      detail: `Platform endpoint ${path} is missing from Angular openapi.json. Angular client may not have generated types for this endpoint.`,
      auto_fixable: false,
    });
  }

  for (const path of comparison.extraInAngular) {
    findings.push({
      severity: "low",
      category: "OPENAPI_SPEC_EXTRA",
      repo: "acme-dashboard",
      file: angularSpecPath.replace(angularPath + "/", ""),
      detail: `Angular openapi.json contains ${path} which is not in the platform spec. May be stale or from a deprecated endpoint.`,
      auto_fixable: false,
    });
  }
}

/**
 * Phase 4: Check @nightgauge/shared-types version consistency across repos.
 */
function validateSharedTypesVersions(repos: Map<string, string>, findings: AuditFinding[]): void {
  const packageJsonPaths: string[] = [];

  for (const [, repoPath] of repos) {
    const pkgPath = join(repoPath, "package.json");
    if (existsSync(pkgPath)) {
      packageJsonPaths.push(pkgPath);
    }
    // Also check workspace packages
    const packagesDir = join(repoPath, "packages");
    if (existsSync(packagesDir)) {
      try {
        const pkgDirs = readdirSync(packagesDir);
        for (const dir of pkgDirs) {
          const subPkg = join(packagesDir, dir, "package.json");
          if (existsSync(subPkg)) packageJsonPaths.push(subPkg);
        }
      } catch (err) {
        console.warn(
          `[dimension-3] Could not read packages dir ${packagesDir}: ${(err as Error).message}`
        );
      }
    }
  }

  const versions = compareSharedTypesVersions(packageJsonPaths);

  if (versions.size < 2) return; // Not enough repos to compare

  const uniqueVersions = new Set(versions.values());
  if (uniqueVersions.size > 1) {
    const versionList = Array.from(versions.entries())
      .map(([repo, v]) => `${repo}@${v}`)
      .join(", ");

    findings.push({
      severity: "medium",
      category: "SHARED_TYPES_VERSION_MISMATCH",
      detail: `@nightgauge/shared-types version mismatch across repos: ${versionList}. Mismatched versions can cause type incompatibilities.`,
      auto_fixable: false,
    });
  }
}

/**
 * Phase 5: Validate README build/test commands.
 */
function validateReadmes(repos: Map<string, string>, findings: AuditFinding[]): void {
  for (const [repoName, repoPath] of repos) {
    const readmeCandidates = [join(repoPath, "README.md"), join(repoPath, "CONTRIBUTING.md")];

    for (const readmePath of readmeCandidates) {
      if (!existsSync(readmePath)) continue;

      const packageJsonPath = join(repoPath, "package.json");
      const results = validateReadmeCommands(
        readmePath,
        existsSync(packageJsonPath) ? packageJsonPath : undefined
      );
      const summary = summarizeValidation(results);

      for (const invalid of summary.invalidCommands) {
        findings.push({
          severity: "low",
          category: "README_COMMAND_INVALID",
          repo: repoName,
          file: invalid.file.replace(repoPath + "/", ""),
          line: invalid.line,
          detail: `README command \`${invalid.command}\` may be invalid: ${invalid.reason}`,
          auto_fixable: true,
        });
      }
    }
  }
}

/**
 * Compute a 0–100 score based on findings.
 * Deducts points per finding severity: critical=20, high=10, medium=5, low=2
 */
function computeScore(findings: AuditFinding[]): number {
  const deductions = findings.reduce((sum, f) => {
    const d =
      f.severity === "critical" ? 20 : f.severity === "high" ? 10 : f.severity === "medium" ? 5 : 2;
    return sum + d;
  }, 0);

  return Math.max(0, 100 - deductions);
}

/**
 * Run Dimension 3: Documentation Content Accuracy.
 *
 * Validates:
 * - Endpoint tables in ECOSYSTEM.md vs platform routes
 * - Angular openapi.json vs platform spec
 * - @nightgauge/shared-types version consistency
 * - README command syntax
 */
export async function runDimension3(options: Dimension3Options = {}): Promise<AuditDimension> {
  const cwd = process.cwd();
  const workspaceRoot = options.workspaceRoot ?? resolve(cwd, "..");

  const repos = discoverRepos(workspaceRoot, options.repoPaths);
  const findings: AuditFinding[] = [];

  console.log(`[dimension-3] Auditing repos: ${Array.from(repos.keys()).join(", ")}`);

  // Phase 1: Repo discovery (done above)
  // Phase 2: Endpoint validation
  validateDocumentedEndpoints(repos, findings);

  // Phase 3: OpenAPI spec comparison
  validateOpenApiSpecs(repos, findings);

  // Phase 4: Shared-types version check
  validateSharedTypesVersions(repos, findings);

  // Phase 5: README validation
  validateReadmes(repos, findings);

  const score = computeScore(findings);
  const criticalCount = findings.filter((f) => f.severity === "critical").length;
  const highCount = findings.filter((f) => f.severity === "high").length;

  const summary =
    findings.length === 0
      ? `Documentation accuracy is clean. Audited ${repos.size} repos.`
      : `Found ${findings.length} documentation accuracy issue(s) across ${repos.size} repos (${criticalCount} critical, ${highCount} high). Score: ${score}/100.`;

  return {
    schema_version: "1.0",
    dimension: "documentation_accuracy",
    timestamp: new Date().toISOString(),
    score,
    summary,
    findings,
  };
}
