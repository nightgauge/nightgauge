/**
 * Repository Model
 *
 * Represents a single repository within a Nightgauge workspace. Provides
 * lazy-loading of nightgauge configuration and a consistent interface
 * for accessing repository metadata. Supports both primary (.nightgauge/config.yaml)
 * and legacy (.nightgauge/nightgauge.yaml) config paths.
 *
 * @see Issue #324 - WorkspaceManager service and repository loading
 * @see Issue #433 - config.yaml (formerly nightgauge.yaml)
 * @see docs/ARCHITECTURE.md - Multi-Repository Workspace Support
 */

import * as fs from "fs/promises";
import * as path from "path";
import * as yaml from "yaml";
import {
  resolveConfigPath,
  logDeprecationWarning,
  getRelativeConfigPath,
} from "../utils/configPathResolver";

/**
 * GitHub repository information extracted from config.yaml
 */
export interface GitHubConfig {
  owner: string;
  repo: string;
  project_number?: number;
}

/**
 * Nightgauge configuration from .nightgauge/config.yaml
 */
export interface IncrediConfig {
  github?: GitHubConfig;
  /** Flat-config form: top-level `owner:` instead of nested under `github:`. */
  owner?: string;
  /** Flat-config form: top-level `repo:` instead of nested under `github:`. */
  repo?: string;
  pipeline?: {
    auto_accept_stages?: boolean;
    logs?: {
      enabled?: boolean;
      path?: string;
      max_size_mb?: number;
    };
  };
  project?: {
    number?: number;
  };
  [key: string]: unknown;
}

/**
 * Repository model class
 *
 * Represents a single repository in the workspace with lazy-loaded configuration.
 *
 * @example
 * ```typescript
 * const repo = new Repository('frontend', '/path/to/repo', 'primary');
 *
 * // Get cached config or load on first access
 * const config = await repo.loadConfig();
 *
 * // Check if config is already loaded
 * if (repo.incrediConfig) {
 *   console.log(`GitHub: ${repo.incrediConfig.github?.owner}/${repo.incrediConfig.github?.repo}`);
 * }
 * ```
 */
export class Repository {
  /** Repository name (unique within workspace) */
  readonly name: string;

  /** Absolute path to repository root */
  readonly path: string;

  /** Role classification for routing decisions */
  readonly role: "primary" | "secondary" | "shared" | undefined;

  /** Cached nightgauge configuration (null = not loaded, undefined = load failed) */
  private _incrediConfig: IncrediConfig | null | undefined = null;

  /** Loading promise for deduplication */
  private _loadingPromise: Promise<IncrediConfig | null> | null = null;

  /**
   * Project number seeded from the workspace manifest (WorkspaceRepository.project_number).
   * Surfaced via the `github` getter when no config.yaml is loaded yet, so the
   * Repositories view can show the project badge without a config load round-trip.
   */
  _workspaceProjectNumber: number | undefined = undefined;

  /**
   * Create a new Repository instance
   *
   * @param name - Repository name (must be unique within workspace)
   * @param repoPath - Absolute path to repository root
   * @param role - Optional role classification
   */
  constructor(name: string, repoPath: string, role?: "primary" | "secondary" | "shared") {
    this.name = name;
    this.path = repoPath;
    this.role = role;
  }

  /**
   * Get cached nightgauge configuration
   *
   * Returns the cached configuration if already loaded.
   * Returns null if not yet loaded.
   * Returns undefined if loading failed.
   */
  get incrediConfig(): IncrediConfig | null | undefined {
    return this._incrediConfig;
  }

  /**
   * Get GitHub configuration if available.
   *
   * Supports both nested form (`github: { owner, repo }`) and flat form
   * (top-level `owner:` / `repo:`, as used by the nightgauge repo's own
   * config).
   */
  get github(): GitHubConfig | undefined {
    const cfg = this._incrediConfig;
    if (!cfg) return undefined;
    if (cfg.github) {
      // Merge workspace-manifest project_number when config doesn't already supply it.
      if (this._workspaceProjectNumber !== undefined && cfg.github.project_number === undefined) {
        return { ...cfg.github, project_number: this._workspaceProjectNumber };
      }
      return cfg.github;
    }
    if (typeof cfg.owner === "string" && typeof cfg.repo === "string") {
      const projectNum = cfg.project?.number ?? this._workspaceProjectNumber;
      return { owner: cfg.owner, repo: cfg.repo, project_number: projectNum };
    }
    return undefined;
  }

  /**
   * Returns the effective project number for this repository.
   * Prefers config.yaml, falls back to workspace manifest, then undefined.
   */
  get effectiveProjectNumber(): number | undefined {
    return this.github?.project_number ?? this._workspaceProjectNumber;
  }

  /**
   * Check if nightgauge configuration has been loaded
   */
  get isConfigLoaded(): boolean {
    return this._incrediConfig !== null;
  }

  /**
   * Load nightgauge configuration from .nightgauge/config.yaml
   *
   * Uses lazy loading with caching - subsequent calls return cached config.
   * If config file doesn't exist or is invalid, returns null and caches the result.
   *
   * @returns Parsed nightgauge configuration, or null if not found/invalid
   */
  async loadConfig(): Promise<IncrediConfig | null> {
    // Return cached config if already loaded
    if (this._incrediConfig !== null) {
      return this._incrediConfig === undefined ? null : this._incrediConfig;
    }

    // Deduplicate concurrent loads
    if (this._loadingPromise) {
      return this._loadingPromise;
    }

    this._loadingPromise = this._doLoadConfig();

    try {
      const config = await this._loadingPromise;
      this._incrediConfig = config === null ? undefined : config;
      return config;
    } finally {
      this._loadingPromise = null;
    }
  }

  /**
   * Internal config loading implementation
   */
  private async _doLoadConfig(): Promise<IncrediConfig | null> {
    // Resolve config path with fallback to legacy
    const pathResult = await resolveConfigPath(this.path);

    if (!pathResult.exists) {
      // File doesn't exist - not an error, just no config
      return null;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    try {
      // Read and parse YAML
      const fileContent = await fs.readFile(pathResult.path, "utf-8");
      const parsed = yaml.parse(fileContent);

      // Validate basic structure
      if (typeof parsed !== "object" || parsed === null) {
        console.warn(`[Nightgauge] Invalid config in ${this.name}: not an object`);
        return null;
      }

      return parsed as IncrediConfig;
    } catch (error) {
      if (error instanceof yaml.YAMLParseError) {
        console.warn(`[Nightgauge] Failed to parse config in ${this.name}: ${error.message}`);
      } else {
        console.warn(`[Nightgauge] Error loading config in ${this.name}: ${error}`);
      }
      return null;
    }
  }

  /**
   * Reload configuration (invalidates cache)
   *
   * Forces a fresh read of the config.yaml file.
   *
   * @returns Freshly parsed nightgauge configuration
   */
  async reloadConfig(): Promise<IncrediConfig | null> {
    this._incrediConfig = null;
    this._loadingPromise = null;
    return this.loadConfig();
  }

  /**
   * Clear cached configuration
   *
   * Next call to loadConfig() will read from disk.
   */
  clearCache(): void {
    this._incrediConfig = null;
    this._loadingPromise = null;
  }

  /**
   * Check if this repository has an nightgauge configuration file
   *
   * Performs a quick file existence check without loading the full config.
   * Checks both primary (.nightgauge/config.yaml) and legacy (.nightgauge/nightgauge.yaml, now renamed) paths.
   *
   * @returns true if nightgauge config file exists
   */
  async hasIncrediConfig(): Promise<boolean> {
    const pathResult = await resolveConfigPath(this.path);
    return pathResult.exists;
  }

  /**
   * Get repository display name with role indicator
   *
   * @returns Display string like "frontend (primary)" or "utils"
   */
  getDisplayName(): string {
    return this.role ? `${this.name} (${this.role})` : this.name;
  }

  /**
   * Create a Repository from workspace configuration
   *
   * @param config - Repository configuration from nightgauge-workspace.yaml
   * @param workspaceRoot - Absolute path to workspace root
   * @returns Repository instance
   */
  static fromWorkspaceConfig(
    config: {
      name: string;
      path: string;
      role?: "primary" | "secondary" | "shared";
      project_number?: number;
    },
    workspaceRoot: string
  ): Repository {
    // Resolve relative path to absolute
    const absolutePath = path.isAbsolute(config.path)
      ? config.path
      : path.resolve(workspaceRoot, config.path);

    const repo = new Repository(config.name, absolutePath, config.role);

    // Pre-seed github.project_number from the workspace manifest when present
    // so the Repositories view can show the project without loading config.yaml.
    if (config.project_number !== undefined) {
      repo._workspaceProjectNumber = config.project_number;
    }

    return repo;
  }
}
