/**
 * IncrediYamlService - YAML read/write/validate service for .nightgauge/config.yaml
 *
 * Handles parsing, serialization, validation, and file watching for the
 * Nightgauge configuration file. Supports both primary (.nightgauge/config.yaml)
 * and legacy (.nightgauge/nightgauge.yaml) paths with automatic fallback.
 * Preserves YAML structure and comments.
 *
 * Validation now uses Zod schemas from config/schema.ts.
 *
 * @see docs/ARCHITECTURE.md for service patterns
 * @see Issue #433 - Rename config file from nightgauge.yaml to config.yaml
 * @see Issue #432 - Comprehensive Zod Schema for Config Fields
 */

import * as path from "path";
import * as vscode from "vscode";
import { parse as parseYaml, stringify as stringifyYaml, YAMLParseError } from "yaml";
import type { IncrediConfig } from "./types";
import {
  resolveConfigPath,
  getConfigPaths,
  logDeprecationWarning,
  getRelativeConfigPath,
  resolveLocalConfigPath,
  getRelativeLocalConfigPath,
  CONFIG_FILE_NAME,
  LEGACY_CONFIG_FILE_NAME,
  LOCAL_CONFIG_FILE_NAME,
  type ConfigPathResult,
  type LocalConfigPathResult,
} from "../../utils/configPathResolver";
import {
  resolveGlobalConfigPath,
  getGlobalConfigPath,
  type GlobalConfigPathResult,
} from "../../utils/globalConfigResolver";
import {
  validateConfig as zodValidateConfig,
  type ConfigValidationResult,
  type ConfigSourceMap,
  type MergedConfigResult,
  trackObjectSources,
  mergeWithDefaults as zodMergeWithDefaults,
} from "../../config/schema";
import {
  mergeConfigs,
  type ConfigMergeResult,
  type ConfigTiers,
  type MergeOptions,
} from "../../config/configMergeEngine";
import type { RuntimeStateStore } from "../../config/RuntimeStateStore";

// Re-export pure functions from configUtils for convenience
export {
  validateConfig,
  mergeWithDefaults,
  getConfigValue,
  setConfigValue,
  removeUndefined,
} from "./configUtils";
import { validateConfig, removeUndefined } from "./configUtils";

/**
 * Result of reading nightgauge configuration
 */
export interface ReadResult {
  success: boolean;
  config: IncrediConfig | null;
  error?: string;
  /** True if file doesn't exist (vs parse error) */
  notFound?: boolean;
  /** True if using legacy config file (nightgauge.yaml) */
  isLegacy?: boolean;
  /** Validation errors if config is invalid but parseable */
  validationErrors?: Array<{ field: string; message: string }>;
  /** Deprecation warnings for keys present in this config tier */
  validationWarnings?: Array<{ field: string; message: string }>;
}

/**
 * Result of writing nightgauge configuration
 */
export interface WriteResult {
  success: boolean;
  error?: string;
}

/**
 * IncrediYamlService - Service for managing .nightgauge/config.yaml configuration
 *
 * Supports both primary (.nightgauge/config.yaml) and legacy (.nightgauge/nightgauge.yaml)
 * paths with automatic fallback. Watches both paths during the transition period.
 *
 * @example
 * ```typescript
 * const service = new IncrediYamlService('/path/to/workspace');
 *
 * // Read configuration
 * const result = await service.read();
 * if (result.success) {
 *   console.log('Project number:', result.config?.project?.number);
 *   if (result.isLegacy) {
 *     console.log('Using legacy config - consider migrating');
 *   }
 * }
 *
 * // Write configuration
 * const updated = { ...result.config, project: { number: 123 } };
 * await service.write(updated);
 *
 * // Watch for external changes
 * service.onDidChange((config) => {
 *   console.log('Config changed:', config);
 * });
 * ```
 */
export class IncrediYamlService implements vscode.Disposable {
  /** Primary config path (.nightgauge/config.yaml) */
  private readonly primaryConfigPath: string;
  /** Legacy config path (.nightgauge/nightgauge.yaml) */
  private readonly legacyConfigPath: string;
  /** Local config path (.nightgauge/config.local.yaml) */
  private readonly localConfigPath: string;
  /** Currently resolved config path (may be primary or legacy) */
  private resolvedConfigPath: string | null = null;
  /** Whether current config is legacy */
  private isLegacyConfig = false;
  private fileWatcher: vscode.FileSystemWatcher | null = null;
  private legacyFileWatcher: vscode.FileSystemWatcher | null = null;
  private localFileWatcher: vscode.FileSystemWatcher | null = null;
  private globalFileWatcher: vscode.FileSystemWatcher | null = null;
  private readonly _onDidChange = new vscode.EventEmitter<IncrediConfig | null>();
  readonly onDidChange = this._onDidChange.event;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;
  private disposables: vscode.Disposable[] = [];
  private readonly runtimeStore?: RuntimeStateStore;

  constructor(
    private readonly workspaceRoot: string,
    runtimeStore?: RuntimeStateStore
  ) {
    const paths = getConfigPaths(workspaceRoot);
    this.primaryConfigPath = paths.primary;
    this.legacyConfigPath = paths.legacy;
    this.localConfigPath = paths.local;
    this.runtimeStore = runtimeStore;
    this.setupFileWatcher();

    // Re-emit runtime change events through the existing debounce path so
    // consumers (ConfigBridge, SettingsPanel) see one onDidChange per burst.
    if (runtimeStore) {
      this.disposables.push(runtimeStore.onDidChange(() => this.handleFileChange()));
    }
  }

  /**
   * Get the config path (for backward compatibility)
   * @deprecated Use getResolvedConfigPath() for accurate path
   */
  private get configPath(): string {
    return this.resolvedConfigPath || this.primaryConfigPath;
  }

  /**
   * Check if config file exists (checks both primary and legacy paths)
   */
  private async configExists(): Promise<ConfigPathResult> {
    return resolveConfigPath(this.workspaceRoot);
  }

  /**
   * Set up file watcher for external changes
   *
   * Watches both primary (.nightgauge/config.yaml) and legacy (.nightgauge/nightgauge.yaml)
   * paths during the transition period. This ensures config changes are detected
   * regardless of which file the user edits.
   */
  private setupFileWatcher(): void {
    // Watch primary config path
    const primaryPattern = new vscode.RelativePattern(
      this.workspaceRoot,
      `.nightgauge/${CONFIG_FILE_NAME}`
    );
    const primaryWatcher = vscode.workspace.createFileSystemWatcher(primaryPattern);

    primaryWatcher.onDidChange(this.handleFileChange.bind(this));
    primaryWatcher.onDidCreate(this.handleFileChange.bind(this));
    primaryWatcher.onDidDelete(this.handleFileChange.bind(this));

    this.disposables.push(primaryWatcher);
    this.fileWatcher = primaryWatcher;

    // Watch legacy config path for transition period
    const legacyPattern = new vscode.RelativePattern(
      this.workspaceRoot,
      `.nightgauge/${LEGACY_CONFIG_FILE_NAME}`
    );
    const legacyWatcher = vscode.workspace.createFileSystemWatcher(legacyPattern);

    legacyWatcher.onDidChange(this.handleFileChange.bind(this));
    legacyWatcher.onDidCreate(this.handleFileChange.bind(this));
    legacyWatcher.onDidDelete(this.handleFileChange.bind(this));

    this.disposables.push(legacyWatcher);
    this.legacyFileWatcher = legacyWatcher;

    // Watch local config path for developer overrides (Issue #435)
    const localPattern = new vscode.RelativePattern(
      this.workspaceRoot,
      `.nightgauge/${LOCAL_CONFIG_FILE_NAME}`
    );
    const localWatcher = vscode.workspace.createFileSystemWatcher(localPattern);

    localWatcher.onDidChange(this.handleFileChange.bind(this));
    localWatcher.onDidCreate(this.handleFileChange.bind(this));
    localWatcher.onDidDelete(this.handleFileChange.bind(this));

    this.disposables.push(localWatcher);
    this.localFileWatcher = localWatcher;

    // Watch global config path (~/.nightgauge/config.yaml)
    // This ensures changes to the global config (e.g., enabling Discord
    // notifications machine-wide) are picked up without requiring a reload.
    const globalConfigPath = getGlobalConfigPath();
    const globalConfigDir = path.dirname(globalConfigPath);
    const globalConfigFile = path.basename(globalConfigPath);
    const globalPattern = new vscode.RelativePattern(
      vscode.Uri.file(globalConfigDir),
      globalConfigFile
    );
    const globalWatcher = vscode.workspace.createFileSystemWatcher(globalPattern);

    globalWatcher.onDidChange(this.handleFileChange.bind(this));
    globalWatcher.onDidCreate(this.handleFileChange.bind(this));
    globalWatcher.onDidDelete(this.handleFileChange.bind(this));

    this.disposables.push(globalWatcher);
    this.globalFileWatcher = globalWatcher;
  }

  /**
   * Handle file change with debouncing
   */
  private handleFileChange(): void {
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }

    this.debounceTimer = setTimeout(async () => {
      const result = await this.read();
      this._onDidChange.fire(result.config);
    }, 100);
  }

  /**
   * Read and parse nightgauge config file
   *
   * Automatically resolves between primary (.nightgauge/config.yaml) and
   * legacy (.nightgauge/nightgauge.yaml) paths. Logs deprecation warning when
   * using legacy path.
   *
   * Now uses Zod schema validation for comprehensive type checking.
   */
  async read(): Promise<ReadResult> {
    try {
      const pathResult = await this.configExists();

      if (!pathResult.exists) {
        return {
          success: false,
          config: null,
          notFound: true,
          error: `Configuration file not found at ${getRelativeConfigPath(false)}`,
        };
      }

      // Update resolved path state
      this.resolvedConfigPath = pathResult.path;
      this.isLegacyConfig = pathResult.isLegacy;

      // Log deprecation warning when using legacy path
      if (pathResult.isLegacy) {
        logDeprecationWarning(pathResult.path);
      }

      const uri = vscode.Uri.file(pathResult.path);
      const content = await vscode.workspace.fs.readFile(uri);
      const text = Buffer.from(content).toString("utf-8");

      if (!text.trim()) {
        return {
          success: true,
          config: {},
          isLegacy: pathResult.isLegacy,
        };
      }

      const rawConfig = parseYaml(text);

      // Validate using Zod schema
      const validation: ConfigValidationResult = zodValidateConfig(rawConfig);

      if (!validation.valid) {
        // Return config but with validation errors for informational purposes
        // This allows partial configs to still be loaded with warnings
        return {
          success: true,
          config: rawConfig as IncrediConfig,
          isLegacy: pathResult.isLegacy,
          validationErrors: validation.errors.map((e) => ({
            field: e.field,
            message: e.message,
          })),
          ...(validation.warnings.length > 0 && {
            validationWarnings: validation.warnings.map((w) => ({
              field: w.field,
              message: w.message,
            })),
          }),
        };
      }

      return {
        success: true,
        config: validation.config ?? (rawConfig as IncrediConfig),
        isLegacy: pathResult.isLegacy,
        ...(validation.warnings.length > 0 && {
          validationWarnings: validation.warnings.map((w) => ({
            field: w.field,
            message: w.message,
          })),
        }),
      };
    } catch (error) {
      if (error instanceof vscode.FileSystemError && error.code === "FileNotFound") {
        return {
          success: false,
          config: null,
          notFound: true,
          error: `Configuration file not found at ${getRelativeConfigPath(false)}`,
        };
      }

      if (error instanceof YAMLParseError) {
        return {
          success: false,
          config: null,
          error: `YAML syntax error: ${error.message}`,
        };
      }

      const message = error instanceof Error ? error.message : "Unknown error reading file";
      return {
        success: false,
        config: null,
        error: message,
      };
    }
  }

  /**
   * Write configuration to nightgauge config file
   *
   * Always writes to the primary path (.nightgauge/config.yaml).
   * Preserves YAML formatting with 2-space indentation.
   * Uses Zod schema validation before writing.
   *
   * The `tier` parameter is required and must be `"project"` — this is a
   * compile-time guard ensuring callers explicitly declare they are writing
   * to the project tier. Use `writeLocal()` or `writeGlobal()` for other tiers.
   *
   * @see Issue #3337 — Phase 4: machine-tier routing
   */
  async write(config: IncrediConfig, tier: "project"): Promise<WriteResult> {
    try {
      const mergedConfig = await this.mergeWithExistingFile(this.primaryConfigPath, config);

      // Validate before writing using Zod schema
      const validation = validateConfig(mergedConfig);
      if (!validation.valid) {
        return {
          success: false,
          error: `Validation failed: ${validation.errors.map((e) => `${e.field}: ${e.message}`).join(", ")}`,
        };
      }

      // Remove undefined values for cleaner YAML
      const cleanConfig = removeUndefined(mergedConfig);

      const yaml = stringifyYaml(cleanConfig, {
        indent: 2,
        lineWidth: 100,
        nullStr: "",
      });

      // Always write to primary path
      const uri = vscode.Uri.file(this.primaryConfigPath);
      await vscode.workspace.fs.writeFile(uri, Buffer.from(yaml, "utf-8"));

      // Update resolved path to primary
      this.resolvedConfigPath = this.primaryConfigPath;
      this.isLegacyConfig = false;

      return { success: true };
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error writing file";
      return {
        success: false,
        error: message,
      };
    }
  }

  /**
   * Create .nightgauge/config.yaml with minimal defaults
   *
   * Always creates the primary config file (config.yaml), not the legacy path.
   */
  async create(projectNumber?: number): Promise<WriteResult> {
    // Ensure .nightgauge directory exists
    const incrediDir = vscode.Uri.file(`${this.workspaceRoot}/.nightgauge`);
    try {
      await vscode.workspace.fs.createDirectory(incrediDir);
    } catch {
      // Directory may already exist, which is fine
    }

    const config: IncrediConfig = {
      project: {
        number: projectNumber,
      },
    };

    return this.write(config, "project");
  }

  /**
   * Check if config file exists (primary or legacy)
   */
  async exists(): Promise<boolean> {
    const result = await this.configExists();
    return result.exists;
  }

  /**
   * Get the full path to the resolved config file
   *
   * Returns the path that was resolved during the last read() call,
   * or the primary path if read() hasn't been called yet.
   */
  getConfigPath(): string {
    return this.resolvedConfigPath || this.primaryConfigPath;
  }

  /**
   * Get the primary config path (always .nightgauge/config.yaml)
   */
  getPrimaryConfigPath(): string {
    return this.primaryConfigPath;
  }

  /**
   * Get the legacy config path (.nightgauge/nightgauge.yaml)
   */
  getLegacyConfigPath(): string {
    return this.legacyConfigPath;
  }

  /**
   * Check if currently using legacy config
   */
  isUsingLegacyConfig(): boolean {
    return this.isLegacyConfig;
  }

  /**
   * Get the local config path (.nightgauge/config.local.yaml)
   */
  getLocalConfigPath(): string {
    return this.localConfigPath;
  }

  // ============================================================================
  // Local Config Support (Issue #435)
  // ============================================================================

  /**
   * Read the local config file (.nightgauge/config.local.yaml)
   *
   * Returns null config if file doesn't exist (local config is optional).
   * Local config is gitignored and allows developers to override project settings.
   *
   * Precedence: Env > Local > Project > Global > Defaults
   */
  async readLocal(): Promise<ReadResult> {
    try {
      const pathResult = await resolveLocalConfigPath(this.workspaceRoot);

      if (!pathResult.exists) {
        return {
          success: true,
          config: null,
          notFound: true,
        };
      }

      const uri = vscode.Uri.file(pathResult.path);
      const content = await vscode.workspace.fs.readFile(uri);
      const text = Buffer.from(content).toString("utf-8");

      if (!text.trim()) {
        return {
          success: true,
          config: {},
        };
      }

      const rawConfig = parseYaml(text);

      // Validate using Zod schema
      const validation: ConfigValidationResult = zodValidateConfig(rawConfig);

      if (!validation.valid) {
        return {
          success: true,
          config: rawConfig as IncrediConfig,
          validationErrors: validation.errors.map((e) => ({
            field: e.field,
            message: e.message,
          })),
          ...(validation.warnings.length > 0 && {
            validationWarnings: validation.warnings.map((w) => ({
              field: w.field,
              message: w.message,
            })),
          }),
        };
      }

      return {
        success: true,
        config: validation.config ?? (rawConfig as IncrediConfig),
        ...(validation.warnings.length > 0 && {
          validationWarnings: validation.warnings.map((w) => ({
            field: w.field,
            message: w.message,
          })),
        }),
      };
    } catch (error) {
      if (error instanceof YAMLParseError) {
        return {
          success: false,
          config: null,
          error: `Local config YAML syntax error: ${error.message}`,
        };
      }

      const message = error instanceof Error ? error.message : "Unknown error reading local config";
      return {
        success: false,
        config: null,
        error: message,
      };
    }
  }

  /**
   * Check if local config exists
   */
  async localConfigExists(): Promise<boolean> {
    const result = await resolveLocalConfigPath(this.workspaceRoot);
    return result.exists;
  }

  /**
   * Write configuration to local config file (.nightgauge/config.local.yaml)
   *
   * Creates the file if it doesn't exist. Local config is gitignored
   * and allows developers to override project settings.
   *
   * @param config - Configuration to write
   * @returns Write result
   *
   * @see Issue #435 - Add local config override
   * @see Issue #440 - Multi-tier config GUI support
   */
  async writeLocal(config: IncrediConfig): Promise<WriteResult> {
    try {
      const mergedConfig = await this.mergeWithExistingFile(this.localConfigPath, config);

      // Validate before writing using Zod schema
      const validation = validateConfig(mergedConfig);
      if (!validation.valid) {
        return {
          success: false,
          error: `Validation failed: ${validation.errors.map((e) => `${e.field}: ${e.message}`).join(", ")}`,
        };
      }

      // Remove undefined values for cleaner YAML
      const cleanConfig = removeUndefined(mergedConfig);

      // If config is empty, delete the file instead
      if (Object.keys(cleanConfig).length === 0) {
        try {
          const uri = vscode.Uri.file(this.localConfigPath);
          await vscode.workspace.fs.delete(uri);
          return { success: true };
        } catch {
          // File might not exist, which is fine
          return { success: true };
        }
      }

      const yaml = stringifyYaml(cleanConfig, {
        indent: 2,
        lineWidth: 100,
        nullStr: "",
      });

      // Ensure .nightgauge directory exists
      const incrediDir = vscode.Uri.file(`${this.workspaceRoot}/.nightgauge`);
      try {
        await vscode.workspace.fs.createDirectory(incrediDir);
      } catch {
        // Directory may already exist
      }

      const uri = vscode.Uri.file(this.localConfigPath);
      await vscode.workspace.fs.writeFile(uri, Buffer.from(yaml, "utf-8"));

      return { success: true };
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error writing local config";
      return {
        success: false,
        error: message,
      };
    }
  }

  /**
   * Replace only the repository's project-routing fields in one file tier.
   *
   * Unlike the general settings writers, this deliberately removes the legacy
   * `project.number` key so deleting the final `projects[]` assignment does not
   * unexpectedly reactivate an older scalar value. Other project settings and
   * unknown YAML keys are preserved.
   */
  async writeProjectAssignments(
    projects: NonNullable<IncrediConfig["projects"]>,
    tier: "project" | "local"
  ): Promise<WriteResult> {
    try {
      const filePath = tier === "project" ? this.primaryConfigPath : this.localConfigPath;
      const existing = ((await this.readRawConfigFile(filePath)) ?? {}) as IncrediConfig;
      if (existing.project) {
        delete existing.project.number;
        if (Object.keys(existing.project).length === 0) delete existing.project;
      }
      existing.projects = projects;
      const validation = validateConfig(existing);
      if (!validation.valid) {
        return {
          success: false,
          error: `Validation failed: ${validation.errors.map((e) => `${e.field}: ${e.message}`).join(", ")}`,
        };
      }
      const cleanConfig = removeUndefined(existing);
      const directory = vscode.Uri.file(`${this.workspaceRoot}/.nightgauge`);
      await vscode.workspace.fs.createDirectory(directory);
      const yaml = stringifyYaml(cleanConfig, { indent: 2, lineWidth: 100, nullStr: "" });
      await vscode.workspace.fs.writeFile(vscode.Uri.file(filePath), Buffer.from(yaml, "utf-8"));
      if (tier === "project") {
        this.resolvedConfigPath = this.primaryConfigPath;
        this.isLegacyConfig = false;
      }
      return { success: true };
    } catch (error) {
      return {
        success: false,
        error: error instanceof Error ? error.message : "Unknown project-routing write error",
      };
    }
  }

  // ============================================================================
  // Global Config Support (Issue #434)
  // ============================================================================

  /**
   * Read the global config file (~/.nightgauge/config.yaml)
   *
   * Returns null config if file doesn't exist (global config is optional).
   * Uses platform-specific path resolution.
   */
  async readGlobal(): Promise<ReadResult> {
    try {
      const pathResult = await resolveGlobalConfigPath();

      if (!pathResult.exists) {
        return {
          success: true,
          config: null,
          notFound: true,
        };
      }

      const uri = vscode.Uri.file(pathResult.path);
      const content = await vscode.workspace.fs.readFile(uri);
      const text = Buffer.from(content).toString("utf-8");

      if (!text.trim()) {
        return {
          success: true,
          config: {},
        };
      }

      const rawConfig = parseYaml(text);

      // Validate using Zod schema
      const validation: ConfigValidationResult = zodValidateConfig(rawConfig);

      if (!validation.valid) {
        return {
          success: true,
          config: rawConfig as IncrediConfig,
          validationErrors: validation.errors.map((e) => ({
            field: e.field,
            message: e.message,
          })),
          ...(validation.warnings.length > 0 && {
            validationWarnings: validation.warnings.map((w) => ({
              field: w.field,
              message: w.message,
            })),
          }),
        };
      }

      return {
        success: true,
        config: validation.config ?? (rawConfig as IncrediConfig),
        ...(validation.warnings.length > 0 && {
          validationWarnings: validation.warnings.map((w) => ({
            field: w.field,
            message: w.message,
          })),
        }),
      };
    } catch (error) {
      if (error instanceof YAMLParseError) {
        return {
          success: false,
          config: null,
          error: `Global config YAML syntax error: ${error.message}`,
        };
      }

      const message =
        error instanceof Error ? error.message : "Unknown error reading global config";
      return {
        success: false,
        config: null,
        error: message,
      };
    }
  }

  /**
   * Read merged configuration with source annotations
   *
   * Merges configurations in precedence order:
   * 1. DEFAULT_CONFIG (hardcoded defaults)
   * 2. Global config (~/.nightgauge/config.yaml)
   * 3. Project config (.nightgauge/config.yaml)
   * 4. Local config (.nightgauge/config.local.yaml) - wins on conflict
   *
   * Environment variable overrides are NOT applied here - those are handled
   * at runtime by individual skills/commands.
   *
   * @see Issue #435 - Add local config override
   * @returns Merged config with source annotations
   */

  /**
   * Comment header prepended to a newly created global config file.
   * Only written once, when the file does not yet exist.
   *
   * @see Issue #3337 — Phase 4: machine-tier routing
   */
  private static readonly GLOBAL_CONFIG_HEADER = `\
# ~/.nightgauge/config.yaml — Machine-tier configuration
#
# This file stores your personal Nightgauge preferences.
# It applies to ALL your repositories and is NOT committed to git.
# Do NOT add this file to version control.
#
# To edit your team/project config instead, use:
#   Nightgauge: Edit Team Config (command palette)
#   or open .nightgauge/config.yaml in your project.
#

`;

  /**
   * Write to the global config file (~/.nightgauge/config.yaml).
   *
   * Deep-merges with any existing global config so callers can write partial
   * updates without clobbering unrelated keys. Follows the same approach as
   * `writeLocal()` but targets the machine-tier path.
   *
   * When creating the file for the first time, prepends `GLOBAL_CONFIG_HEADER`
   * to explain the file's purpose and warn against committing it.
   *
   * @see Issue #3337 — Phase 4: machine-tier routing
   * @see Issue #3338 — Phase 5 legacy-key migration writes machine-tier keys
   */
  async writeGlobal(config: Partial<IncrediConfig>): Promise<WriteResult> {
    try {
      const globalPath = getGlobalConfigPath();
      const globalDir = path.dirname(globalPath);

      // Check existence before the merge so we know whether to add the header.
      const previouslyExisted = await this.globalConfigExists();

      const mergedConfig = await this.mergeWithExistingFile(globalPath, config as IncrediConfig);

      const validation = validateConfig(mergedConfig);
      if (!validation.valid) {
        return {
          success: false,
          error: `Validation failed: ${validation.errors.map((e) => `${e.field}: ${e.message}`).join(", ")}`,
        };
      }

      const cleanConfig = removeUndefined(mergedConfig);

      if (Object.keys(cleanConfig).length === 0) {
        try {
          const uri = vscode.Uri.file(globalPath);
          await vscode.workspace.fs.delete(uri);
          return { success: true };
        } catch {
          return { success: true };
        }
      }

      const yaml = stringifyYaml(cleanConfig, {
        indent: 2,
        lineWidth: 100,
        nullStr: "",
      });

      // Ensure ~/.nightgauge directory exists
      try {
        await vscode.workspace.fs.createDirectory(vscode.Uri.file(globalDir));
      } catch {
        // Directory may already exist
      }

      const finalYaml = previouslyExisted ? yaml : IncrediYamlService.GLOBAL_CONFIG_HEADER + yaml;

      const uri = vscode.Uri.file(globalPath);
      await vscode.workspace.fs.writeFile(uri, Buffer.from(finalYaml, "utf-8"));

      return { success: true };
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "Unknown error writing global config";
      return {
        success: false,
        error: message,
      };
    }
  }

  async readMerged(): Promise<MergedConfigResult> {
    const sources: ConfigSourceMap = {};

    // 1. Start with defaults
    const defaultConfig = zodMergeWithDefaults({});
    trackObjectSources(sources, defaultConfig as Record<string, unknown>, "", "default");

    // 2. Read global config
    const globalResult = await this.readGlobal();
    const globalConfig = globalResult.success && globalResult.config ? globalResult.config : {};
    const globalPathResult = await resolveGlobalConfigPath();

    if (globalResult.success && globalResult.config && !globalResult.notFound) {
      trackObjectSources(sources, globalConfig as Record<string, unknown>, "", "global");
    }

    // 3. Read project config
    const projectResult = await this.read();
    const projectConfig = projectResult.success && projectResult.config ? projectResult.config : {};

    if (projectResult.success && projectResult.config && !projectResult.notFound) {
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");
    }

    // 4. Read local config (developer overrides, gitignored)
    const localResult = await this.readLocal();
    const localConfig = localResult.success && localResult.config ? localResult.config : {};
    const localPathResult = await resolveLocalConfigPath(this.workspaceRoot);

    if (localResult.success && localResult.config && !localResult.notFound) {
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      // Warn about critical settings being overridden locally
      this.warnAboutCriticalLocalOverrides(localConfig);
    }

    // 5. Deep merge: defaults <- global <- project <- local
    const mergedConfig = this.deepMergeConfigs(
      defaultConfig,
      globalConfig,
      projectConfig,
      localConfig
    );

    return {
      config: mergedConfig,
      sources,
      hasGlobalConfig:
        globalResult.success && !globalResult.notFound && globalResult.config !== null,
      globalConfigPath: globalPathResult.exists ? globalPathResult.path : undefined,
      hasProjectConfig:
        projectResult.success && !projectResult.notFound && projectResult.config !== null,
      projectConfigPath: projectResult.success ? this.getConfigPath() : undefined,
      isLegacyProjectConfig: projectResult.isLegacy,
      hasLocalConfig: localResult.success && !localResult.notFound && localResult.config !== null,
      localConfigPath: localPathResult.exists ? localPathResult.path : undefined,
    };
  }

  /**
   * Read effective configuration using the 6-tier merge engine
   *
   * This method uses the full ConfigMergeEngine which supports all 6 tiers:
   * 1. defaults - Built-in defaults
   * 2. global - User's global config (~/.nightgauge/config.yaml)
   * 3. project - Project config (.nightgauge/config.yaml)
   * 4. local - Local developer overrides (.nightgauge/config.local.yaml)
   * 5. env - Environment variables (NIGHTGAUGE_*)
   * 6. cli - CLI flags (passed as parameter)
   *
   * Unlike readMerged(), this method:
   * - Automatically resolves NIGHTGAUGE_* environment variables
   * - Supports CLI flag overrides
   * - Provides detailed source tracking for all tiers
   * - Returns validation errors if config is invalid
   *
   * @param cliOverrides - Optional CLI flag overrides (highest priority)
   * @param options - Merge options
   * @returns Full merge result with config, sources, and metadata
   *
   * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
   */
  async readEffective(
    cliOverrides?: Partial<IncrediConfig>,
    options: MergeOptions = {}
  ): Promise<ConfigMergeResult> {
    // Read all file-based configs
    const [globalResult, projectResult, localResult] = await Promise.all([
      this.readGlobal(),
      this.read(),
      this.readLocal(),
    ]);

    // Build config tiers
    const runtimeSnapshot = this.runtimeStore?.snapshot();
    const tiers: ConfigTiers = {
      global:
        globalResult.success && globalResult.config && !globalResult.notFound
          ? globalResult.config
          : undefined,
      project:
        projectResult.success && projectResult.config && !projectResult.notFound
          ? projectResult.config
          : undefined,
      local:
        localResult.success && localResult.config && !localResult.notFound
          ? localResult.config
          : undefined,
      runtime:
        runtimeSnapshot && Object.keys(runtimeSnapshot).length > 0 ? runtimeSnapshot : undefined,
      cli: cliOverrides,
    };

    // Warn about critical settings being overridden locally
    if (tiers.local) {
      this.warnAboutCriticalLocalOverrides(tiers.local);
    }

    // Merge using the engine
    return mergeConfigs(tiers, options);
  }

  /**
   * Deep merge multiple configs (local wins on conflict)
   *
   * Merge order: defaults <- global <- project <- local
   *
   * @param defaultConfig - Built-in defaults
   * @param globalConfig - User's global config
   * @param projectConfig - Project-specific config
   * @param localConfig - Local developer overrides (optional)
   * @returns Merged configuration
   */
  private deepMergeConfigs(
    defaultConfig: IncrediConfig,
    globalConfig: IncrediConfig,
    projectConfig: IncrediConfig,
    localConfig: IncrediConfig = {}
  ): IncrediConfig {
    // Use the same deep merge logic as zodMergeWithDefaults
    // Merge order: defaults <- global <- project <- local
    return this.deepMerge(
      this.deepMerge(this.deepMerge(defaultConfig, globalConfig), projectConfig),
      localConfig
    );
  }

  /**
   * Deep merge two objects (source wins on conflict)
   */
  private deepMerge<T extends Record<string, unknown>>(target: T, source: Partial<T>): T {
    const result = { ...target } as Record<string, unknown>;

    for (const key in source) {
      const sourceValue = source[key];
      const targetValue = target[key];

      if (sourceValue === undefined) {
        continue;
      }

      if (
        typeof sourceValue === "object" &&
        sourceValue !== null &&
        !Array.isArray(sourceValue) &&
        typeof targetValue === "object" &&
        targetValue !== null &&
        !Array.isArray(targetValue)
      ) {
        result[key] = this.deepMerge(
          targetValue as Record<string, unknown>,
          sourceValue as Record<string, unknown>
        );
      } else {
        result[key] = sourceValue;
      }
    }

    return result as T;
  }

  /**
   * Merge updated settings onto the existing file contents so settings saves do
   * not discard unsupported or not-yet-modeled config keys.
   */
  private async mergeWithExistingFile(
    filePath: string,
    updates: IncrediConfig
  ): Promise<IncrediConfig> {
    const existing = await this.readRawConfigFile(filePath);
    if (!existing) {
      return updates;
    }

    return this.deepMerge(existing as IncrediConfig, updates);
  }

  /**
   * Read a YAML config file without schema normalization so unknown keys are preserved.
   */
  private async readRawConfigFile(filePath: string): Promise<Record<string, unknown> | null> {
    try {
      const uri = vscode.Uri.file(filePath);
      const content = await vscode.workspace.fs.readFile(uri);
      const text = Buffer.from(content).toString("utf-8").trim();

      if (!text) {
        return null;
      }

      const parsed = parseYaml(text);
      return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
        ? (parsed as Record<string, unknown>)
        : null;
    } catch (error) {
      if (error instanceof vscode.FileSystemError && error.code === "FileNotFound") {
        return null;
      }
      if (error instanceof YAMLParseError) {
        return null;
      }
      return null;
    }
  }

  // ============================================================================
  // Critical Settings Warnings (Issue #435)
  // ============================================================================

  /**
   * Settings that are typically project-wide and shouldn't be overridden locally
   *
   * These settings affect team coordination and project board sync.
   * Warn (but don't block) when local config overrides them.
   */
  private static readonly CRITICAL_SETTINGS = ["project.number", "project.owner", "project.id"];

  /**
   * Warn about critical settings being overridden in local config
   *
   * Logs a warning when local config sets project-critical settings
   * that are typically team-wide. Does not block - just informs.
   *
   * @param localConfig - The local configuration object
   */
  private warnAboutCriticalLocalOverrides(localConfig: IncrediConfig): void {
    const criticalOverrides: string[] = [];

    if (localConfig.project?.number !== undefined) {
      criticalOverrides.push("project.number");
    }
    if (localConfig.project?.owner !== undefined) {
      criticalOverrides.push("project.owner");
    }
    // Check for project.id if it exists in config
    if (
      localConfig.project &&
      "id" in localConfig.project &&
      (localConfig.project as Record<string, unknown>).id !== undefined
    ) {
      criticalOverrides.push("project.id");
    }

    if (criticalOverrides.length > 0) {
      console.warn(
        `[Nightgauge] Local config (${getRelativeLocalConfigPath()}) overrides ` +
          `critical project settings: ${criticalOverrides.join(", ")}. ` +
          `These settings are typically project-wide. ` +
          `This may cause issues with team coordination.`
      );
    }
  }

  /**
   * Add a pattern to sanitization.allowlist in config.yaml
   *
   * Reads current config, appends the pattern (with deduplication),
   * validates with Zod, and writes back atomically.
   *
   * @see Issue #786 - Firewall Learning Mode
   */
  async addToSanitizationAllowlist(pattern: string): Promise<WriteResult> {
    const readResult = await this.read();
    if (!readResult.success || !readResult.config) {
      return {
        success: false,
        error: readResult.error || "Failed to read config",
      };
    }

    const config = readResult.config;
    if (!config.sanitization) {
      config.sanitization = {};
    }
    if (!config.sanitization.allowlist) {
      config.sanitization.allowlist = [];
    }

    // Deduplication check
    if (config.sanitization.allowlist.includes(pattern)) {
      return { success: true }; // Already present
    }

    config.sanitization.allowlist.push(pattern);
    return this.write(config, "project");
  }

  /**
   * Add a directory to sanitization.safe_directories in config.yaml
   *
   * Reads current config, appends the directory (with deduplication),
   * validates with Zod, and writes back atomically.
   *
   * @see Issue #786 - Firewall Learning Mode
   */
  async addToSanitizationSafeDirectories(directory: string): Promise<WriteResult> {
    const readResult = await this.read();
    if (!readResult.success || !readResult.config) {
      return {
        success: false,
        error: readResult.error || "Failed to read config",
      };
    }

    const config = readResult.config;
    if (!config.sanitization) {
      config.sanitization = {};
    }
    if (!config.sanitization.safe_directories) {
      config.sanitization.safe_directories = [];
    }

    // Deduplication check
    if (config.sanitization.safe_directories.includes(directory)) {
      return { success: true }; // Already present
    }

    config.sanitization.safe_directories.push(directory);
    return this.write(config, "project");
  }

  /**
   * Get the global config path
   *
   * Returns the platform-specific path where global config is expected.
   */
  getGlobalConfigPath(): string {
    return getGlobalConfigPath();
  }

  /**
   * Check if global config exists
   */
  async globalConfigExists(): Promise<boolean> {
    const result = await resolveGlobalConfigPath();
    return result.exists;
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }
    this._onDidChange.dispose();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}
