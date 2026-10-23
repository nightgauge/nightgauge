/**
 * Config Path Resolver - Utility for resolving .nightgauge/config.yaml path
 *
 * Provides path resolution with backward compatibility fallback to
 * legacy .nightgauge/nightgauge.yaml filename. Supports migration detection
 * and deprecation warnings.
 *
 * @see Issue #433 - Rename config file from nightgauge.yaml to config.yaml
 */

import * as fs from "fs/promises";
import * as path from "path";

/**
 * Primary config file name (new)
 */
export const CONFIG_FILE_NAME = "config.yaml";

/**
 * Legacy config file name (deprecated)
 */
export const LEGACY_CONFIG_FILE_NAME = "nightgauge.yaml";

/**
 * Local config file name (gitignored, developer overrides)
 * @see Issue #435 - Add local config override
 */
export const LOCAL_CONFIG_FILE_NAME = "config.local.yaml";

/**
 * Config directory within workspace
 */
export const CONFIG_DIR = ".nightgauge";

/**
 * Result of resolving config path
 */
export interface ConfigPathResult {
  /** Absolute path to the config file */
  path: string;
  /** Whether this is the legacy config file (nightgauge.yaml) */
  isLegacy: boolean;
  /** Whether the config file exists */
  exists: boolean;
}

/**
 * Config paths for primary, legacy, and local locations
 */
export interface ConfigPaths {
  /** Path to primary config file (.nightgauge/config.yaml) */
  primary: string;
  /** Path to legacy config file (.nightgauge/nightgauge.yaml) */
  legacy: string;
  /** Path to local config file (.nightgauge/config.local.yaml) */
  local: string;
}

/**
 * Get all config paths for a workspace
 *
 * @param workspaceRoot - Absolute path to workspace root
 * @returns Object with primary, legacy, and local paths
 */
export function getConfigPaths(workspaceRoot: string): ConfigPaths {
  return {
    primary: path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME),
    legacy: path.join(workspaceRoot, CONFIG_DIR, LEGACY_CONFIG_FILE_NAME),
    local: path.join(workspaceRoot, CONFIG_DIR, LOCAL_CONFIG_FILE_NAME),
  };
}

/**
 * Check if a path uses the legacy config filename
 *
 * @param configPath - Absolute path to check
 * @returns True if path ends with legacy config filename
 */
export function isLegacyConfigPath(configPath: string): boolean {
  return path.basename(configPath) === LEGACY_CONFIG_FILE_NAME;
}

/**
 * Resolve the config path with fallback to legacy location
 *
 * Priority:
 * 1. .nightgauge/config.yaml (primary) - if exists
 * 2. .nightgauge/nightgauge.yaml (legacy) - if exists
 * 3. .nightgauge/config.yaml (primary) - for creation
 *
 * @param workspaceRoot - Absolute path to workspace root
 * @returns Config path result with path, legacy flag, and existence
 */
export async function resolveConfigPath(workspaceRoot: string): Promise<ConfigPathResult> {
  const paths = getConfigPaths(workspaceRoot);

  // Check primary path first
  try {
    await fs.access(paths.primary);
    return {
      path: paths.primary,
      isLegacy: false,
      exists: true,
    };
  } catch {
    // Primary doesn't exist, check legacy
  }

  // Check legacy path
  try {
    await fs.access(paths.legacy);
    return {
      path: paths.legacy,
      isLegacy: true,
      exists: true,
    };
  } catch {
    // Neither exists, return primary path for creation
  }

  // Return primary path for creation
  return {
    path: paths.primary,
    isLegacy: false,
    exists: false,
  };
}

/**
 * Synchronous version of resolveConfigPath for use in contexts
 * where async is not available (e.g., simple config reads)
 *
 * @param workspaceRoot - Absolute path to workspace root
 * @returns Config path result with path, legacy flag, and existence
 */
export function resolveConfigPathSync(workspaceRoot: string): ConfigPathResult {
  const fsSync = require("fs");
  const paths = getConfigPaths(workspaceRoot);

  // Check primary path first
  if (fsSync.existsSync(paths.primary)) {
    return {
      path: paths.primary,
      isLegacy: false,
      exists: true,
    };
  }

  // Check legacy path
  if (fsSync.existsSync(paths.legacy)) {
    return {
      path: paths.legacy,
      isLegacy: true,
      exists: true,
    };
  }

  // Return primary path for creation
  return {
    path: paths.primary,
    isLegacy: false,
    exists: false,
  };
}

/**
 * Check if migration from legacy to primary config is needed
 *
 * Migration is needed when:
 * - Legacy config exists
 * - Primary config does NOT exist
 *
 * @param workspaceRoot - Absolute path to workspace root
 * @returns True if migration is needed
 */
export async function needsMigration(workspaceRoot: string): Promise<boolean> {
  const paths = getConfigPaths(workspaceRoot);

  try {
    await fs.access(paths.legacy);
    // Legacy exists, check if primary also exists
    try {
      await fs.access(paths.primary);
      // Both exist - no migration needed (user may have manually created primary)
      return false;
    } catch {
      // Legacy exists but primary doesn't - migration needed
      return true;
    }
  } catch {
    // Legacy doesn't exist - no migration needed
    return false;
  }
}

/**
 * Log deprecation warning when using legacy config path
 *
 * @param configPath - Path that was resolved
 */
export function logDeprecationWarning(configPath: string): void {
  if (isLegacyConfigPath(configPath)) {
    console.warn(
      `[Nightgauge] Deprecation warning: Using legacy config file '${LEGACY_CONFIG_FILE_NAME}'. ` +
        `Please rename to '${CONFIG_FILE_NAME}' for future compatibility. ` +
        `Run 'Nightgauge: Migrate Config File' command to migrate.`
    );
  }
}

/**
 * Get the relative config path (for display purposes)
 *
 * @param isLegacy - Whether to return legacy path
 * @returns Relative path string
 */
export function getRelativeConfigPath(isLegacy: boolean): string {
  return isLegacy
    ? `${CONFIG_DIR}/${LEGACY_CONFIG_FILE_NAME}`
    : `${CONFIG_DIR}/${CONFIG_FILE_NAME}`;
}

// ============================================================================
// Local Config Support (Issue #435)
// ============================================================================

/**
 * Result of resolving local config path
 */
export interface LocalConfigPathResult {
  /** Absolute path to the local config file */
  path: string;
  /** Whether the local config file exists */
  exists: boolean;
}

/**
 * Resolve the local config path
 *
 * Local config (.nightgauge/config.local.yaml) is:
 * - Optional (exists=false when not present)
 * - Gitignored (for developer-specific overrides)
 * - Higher precedence than project config, lower than env vars
 *
 * @param workspaceRoot - Absolute path to workspace root
 * @returns Local config path result with path and existence
 */
export async function resolveLocalConfigPath(
  workspaceRoot: string
): Promise<LocalConfigPathResult> {
  const paths = getConfigPaths(workspaceRoot);

  try {
    await fs.access(paths.local);
    return {
      path: paths.local,
      exists: true,
    };
  } catch {
    // Local config doesn't exist, which is fine (it's optional)
    return {
      path: paths.local,
      exists: false,
    };
  }
}

/**
 * Synchronous version of resolveLocalConfigPath
 *
 * @param workspaceRoot - Absolute path to workspace root
 * @returns Local config path result with path and existence
 */
export function resolveLocalConfigPathSync(workspaceRoot: string): LocalConfigPathResult {
  const fsSync = require("fs");
  const paths = getConfigPaths(workspaceRoot);

  return {
    path: paths.local,
    exists: fsSync.existsSync(paths.local),
  };
}

/**
 * Get the relative local config path (for display purposes)
 *
 * @returns Relative path string for local config
 */
export function getRelativeLocalConfigPath(): string {
  return `${CONFIG_DIR}/${LOCAL_CONFIG_FILE_NAME}`;
}

/**
 * Check if a path is the local config file
 *
 * @param configPath - Absolute path to check
 * @returns True if path ends with local config filename
 */
export function isLocalConfigPath(configPath: string): boolean {
  return path.basename(configPath) === LOCAL_CONFIG_FILE_NAME;
}

// ============================================================================
// Repo Identity (Issue #1561)
// ============================================================================

/**
 * Repository identity (owner and repo name) from config
 */
export interface RepoIdentity {
  owner: string;
  repo: string;
}

/**
 * Read repository owner and name from .nightgauge/config.yaml.
 *
 * Config layout supports two shapes:
 * - Top-level: `owner: "nightgauge"` + `repo: "nightgauge"`
 * - Legacy nested: `github: { owner, repo }`
 *
 * @param workspaceRoot - Absolute path to workspace root
 * @returns RepoIdentity or null if config is missing or incomplete
 */
export async function getRepoIdentity(workspaceRoot: string): Promise<RepoIdentity | null> {
  const pathResult = await resolveConfigPath(workspaceRoot);
  if (!pathResult.exists) {
    return null;
  }

  try {
    const content = await fs.readFile(pathResult.path, "utf-8");

    // Simple line-based parsing (no yaml dependency needed)
    const ownerMatch = content.match(/^\s*owner:\s*["']?([^"'\s#]+)["']?/m);
    const repoMatch = content.match(/^\s*repo:\s*["']?([^"'\s#]+)["']?/m);

    // Try github block fallback
    const githubOwnerMatch = content.match(/github:\s*\n\s+owner:\s*["']?([^"'\s#]+)["']?/m);
    const githubRepoMatch = content.match(
      /github:\s*\n(?:\s+\w+:.*\n)*?\s+repo:\s*["']?([^"'\s#]+)["']?/m
    );

    const owner = ownerMatch?.[1] || githubOwnerMatch?.[1] || null;
    const repo = repoMatch?.[1] || githubRepoMatch?.[1] || null;

    if (owner && repo) {
      return { owner, repo };
    }

    // Fall back to git remote origin URL when config is incomplete
    return getRepoIdentityFromGit(workspaceRoot);
  } catch {
    return getRepoIdentityFromGit(workspaceRoot);
  }
}

/**
 * Derive owner/repo from git remote origin URL as a last-resort fallback.
 */
async function getRepoIdentityFromGit(workspaceRoot: string): Promise<RepoIdentity | null> {
  try {
    const { execFile } = await import("child_process");
    const { promisify } = await import("util");
    const execFileAsync = promisify(execFile);
    const { stdout } = await execFileAsync("git", ["remote", "get-url", "origin"], {
      cwd: workspaceRoot,
    });
    // Match github.com:owner/repo.git or github.com/owner/repo(.git)
    const match = stdout.trim().match(/github\.com[:/]([^/]+)\/([^/.]+?)(?:\.git)?$/);
    if (match) {
      return { owner: match[1], repo: match[2] };
    }
  } catch {
    // git not available or no remote — give up
  }
  return null;
}
