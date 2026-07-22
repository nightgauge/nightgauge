import { readFileSync, existsSync } from "fs";
import type { PackageVersion } from "../types.js";

/** Minimal OpenAPI spec shape (just the parts we validate). */
export interface OpenApiSpec {
  openapi?: string;
  info?: { title?: string; version?: string };
  paths?: Record<string, Record<string, unknown>>;
}

/** Result of comparing two OpenAPI specs. */
export interface SpecComparisonResult {
  missingInAngular: string[];
  extraInAngular: string[];
  pathCount: { platform: number; angular: number };
}

/**
 * Load and parse an OpenAPI JSON spec from disk.
 * Returns null if the file does not exist or cannot be parsed.
 */
export function loadOpenApiSpec(filePath: string): OpenApiSpec | null {
  if (!existsSync(filePath)) return null;

  try {
    const content = readFileSync(filePath, "utf-8");
    return JSON.parse(content) as OpenApiSpec;
  } catch (err) {
    console.warn(
      `[spec-parser] Failed to parse OpenAPI spec at ${filePath}: ${(err as Error).message}`
    );
    return null;
  }
}

/**
 * Compare Angular openapi.json against the platform API spec.
 * Returns paths present in platform but missing in Angular and vice versa.
 */
export function compareOpenApiSpecs(
  platformSpec: OpenApiSpec,
  angularSpec: OpenApiSpec
): SpecComparisonResult {
  const platformPaths = Object.keys(platformSpec.paths ?? {});
  const angularPaths = Object.keys(angularSpec.paths ?? {});

  const platformSet = new Set(platformPaths);
  const angularSet = new Set(angularPaths);

  const missingInAngular = platformPaths.filter((p) => !angularSet.has(p));
  const extraInAngular = angularPaths.filter((p) => !platformSet.has(p));

  return {
    missingInAngular,
    extraInAngular,
    pathCount: { platform: platformPaths.length, angular: angularPaths.length },
  };
}

/**
 * Extract the version of a specific package from a package.json file.
 * Checks "version" (for main package), "dependencies", and "devDependencies".
 */
export function extractPackageVersion(
  packageJsonPath: string,
  packageName: string
): PackageVersion | null {
  if (!existsSync(packageJsonPath)) return null;

  try {
    const content = readFileSync(packageJsonPath, "utf-8");
    const parsed = JSON.parse(content) as Record<string, unknown>;

    // For main package version
    if (packageName === parsed["name"]) {
      return {
        name: packageName,
        version: String(parsed["version"] ?? "unknown"),
        repo: deriveRepoName(packageJsonPath),
        file: packageJsonPath,
      };
    }

    // Check dependencies
    const deps = (parsed["dependencies"] ?? {}) as Record<string, string>;
    const devDeps = (parsed["devDependencies"] ?? {}) as Record<string, string>;

    const version = deps[packageName] ?? devDeps[packageName];
    if (version) {
      return {
        name: packageName,
        version: normalizeVersionRange(version),
        repo: deriveRepoName(packageJsonPath),
        file: packageJsonPath,
      };
    }

    return null;
  } catch (err) {
    console.warn(`[spec-parser] Failed to parse ${packageJsonPath}: ${(err as Error).message}`);
    return null;
  }
}

/**
 * Strip semver range operators (^, ~, >=, <=, >, <) to get a bare version.
 */
function normalizeVersionRange(version: string): string {
  return version.replace(/^[\^~>=<]+/, "").trim();
}

/** Derive a short repo name from a file path (the directory above node_modules or root). */
function deriveRepoName(filePath: string): string {
  const parts = filePath.split("/");
  // Find the segment that looks like a repo root (contains package.json at root level)
  // Use the parent directory name of the package.json as repo name
  const packageJsonIdx = parts.lastIndexOf("package.json");
  if (packageJsonIdx > 0) {
    return parts[packageJsonIdx - 1] ?? "unknown";
  }
  return "unknown";
}

/**
 * Compare shared-types versions across multiple repos.
 * Returns a map of repo → version string.
 */
export function compareSharedTypesVersions(
  packageJsonPaths: string[],
  packageName = "@nightgauge/shared-types"
): Map<string, string> {
  const versions = new Map<string, string>();

  for (const pkgPath of packageJsonPaths) {
    const result = extractPackageVersion(pkgPath, packageName);
    if (result) {
      versions.set(result.repo, result.version);
    }
  }

  return versions;
}
