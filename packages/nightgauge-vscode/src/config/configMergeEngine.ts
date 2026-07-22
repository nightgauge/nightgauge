/**
 * Config Merge Engine - 7-Tier Configuration Precedence
 *
 * Merges all configuration tiers into a single effective configuration
 * with source annotations for each value. Implements the precedence chain:
 *
 * 1. defaults (lowest) - Built-in defaults from DEFAULT_CONFIG
 * 2. global - User's global config (~/.nightgauge/config.yaml)
 * 3. project - Project config (.nightgauge/config.yaml)
 * 4. local - Local developer overrides (.nightgauge/config.local.yaml) - gitignored
 * 5. runtime - Runtime memento store (VSCode globalState/workspaceState) - Phase 2 of #3313
 * 6. env - Environment variables (NIGHTGAUGE_*)
 * 7. cli (highest) - CLI flags passed at runtime
 *
 * @example
 * ```typescript
 * const result = mergeConfigs({
 *   defaults: DEFAULT_CONFIG,
 *   global: { pr: { merge_strategy: 'rebase' } },
 *   project: { project: { number: 10 } },
 *   local: { pipeline: { auto_fix: false } },
 *   env: resolveEnvVars(),
 *   cli: { pipeline: { skip: { tests: true } } },
 * });
 *
 * console.log(result.config.pr?.merge_strategy); // 'rebase' (from global)
 * console.log(result.sources['pr.merge_strategy']); // 'global'
 * ```
 *
 * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
 * @see docs/CONFIGURATION.md - Configuration reference
 */

import {
  type IncrediConfig,
  type ConfigSourceMap,
  type ConfigValidationResult,
  type ConfigSource,
  validateConfig,
  mergeWithDefaults,
  trackObjectSources,
  DEFAULT_CONFIG,
} from "./schema";
import { resolveEnvVars, type EnvVarResolutionResult, type EnvVarError } from "./envVarResolver";

// ============================================================================
// Types
// ============================================================================

/**
 * Input for merge engine - all 7 config tiers
 */
export interface ConfigTiers {
  /** Built-in defaults (tier 1 - lowest) */
  defaults?: Partial<IncrediConfig>;
  /** Global user config ~/.nightgauge/config.yaml (tier 2) */
  global?: Partial<IncrediConfig>;
  /** Project config .nightgauge/config.yaml (tier 3) */
  project?: Partial<IncrediConfig>;
  /** Local developer config .nightgauge/config.local.yaml (tier 4) */
  local?: Partial<IncrediConfig>;
  /** Runtime memento store snapshot (tier 5) — VSCode globalState/workspaceState */
  runtime?: Partial<IncrediConfig>;
  /** Environment variables NIGHTGAUGE_* (tier 6) - or pre-resolved config */
  env?: Partial<IncrediConfig>;
  /** CLI flags --config-* (tier 7 - highest) */
  cli?: Partial<IncrediConfig>;
}

/**
 * Metadata about which tiers were present in the merge
 */
export interface TierMetadata {
  hasDefaults: boolean;
  hasGlobal: boolean;
  hasProject: boolean;
  hasLocal: boolean;
  hasRuntime: boolean;
  hasEnv: boolean;
  hasCli: boolean;
}

/**
 * Result of config merge with source annotations
 */
export interface ConfigMergeResult {
  /** Effective configuration after all merges */
  config: IncrediConfig;
  /** Source annotations for each field (dot-notation paths) */
  sources: ConfigSourceMap;
  /** Validation result */
  validation: ConfigValidationResult;
  /** List of environment variables that were applied */
  envVarsApplied: string[];
  /** List of CLI flags that were applied (dot-notation paths) */
  cliOverrides: string[];
  /** Errors during env var resolution */
  envVarErrors: EnvVarError[];
  /** Metadata about loaded tiers */
  tiers: TierMetadata;
  /** Time taken to merge (milliseconds) */
  mergeTimeMs: number;
}

/**
 * Options for merge operation
 */
export interface MergeOptions {
  /** Skip env var resolution (useful when env config is already resolved) */
  skipEnvResolution?: boolean;
  /** Skip final validation (useful for partial merges) */
  skipValidation?: boolean;
  /** Process environment for env var resolution */
  processEnv?: NodeJS.ProcessEnv;
}

// ============================================================================
// Deep Merge Implementation
// ============================================================================

/**
 * Deep merge two objects with last-writer-wins semantics
 *
 * Merge rules:
 * - Objects: recursively merged (properties combined)
 * - Arrays: replaced (not concatenated)
 * - Scalars: last value wins
 * - undefined: does NOT override existing values
 *
 * @param target - Base object
 * @param source - Object to merge into target (wins on conflict)
 * @returns New merged object
 */
export function deepMerge<T extends Record<string, unknown>>(
  target: T,
  source: Partial<T> | undefined | null
): T {
  if (!source) {
    return { ...target };
  }

  const result = { ...target } as Record<string, unknown>;

  for (const key in source) {
    if (!Object.prototype.hasOwnProperty.call(source, key)) {
      continue;
    }

    const sourceValue = source[key];
    const targetValue = target[key];

    // undefined values do NOT override
    if (sourceValue === undefined) {
      continue;
    }

    // null values DO override (explicit null)
    if (sourceValue === null) {
      result[key] = null;
      continue;
    }

    // Arrays are replaced, not merged
    if (Array.isArray(sourceValue)) {
      result[key] = [...sourceValue];
      continue;
    }

    // Objects are recursively merged
    if (
      typeof sourceValue === "object" &&
      typeof targetValue === "object" &&
      targetValue !== null &&
      !Array.isArray(targetValue)
    ) {
      result[key] = deepMerge(
        targetValue as Record<string, unknown>,
        sourceValue as Record<string, unknown>
      );
      continue;
    }

    // Scalars: source wins
    result[key] = sourceValue;
  }

  return result as T;
}

/**
 * Extract all leaf paths from an object
 *
 * Used for tracking CLI overrides.
 *
 * @example
 * getLeafPaths({ pr: { merge_strategy: 'squash' } })
 * // Returns: ['pr.merge_strategy']
 */
export function getLeafPaths(obj: Record<string, unknown>, prefix = ""): string[] {
  const paths: string[] = [];

  for (const key of Object.keys(obj)) {
    const value = obj[key];
    const path = prefix ? `${prefix}.${key}` : key;

    if (value === undefined) {
      continue;
    }

    if (typeof value === "object" && value !== null && !Array.isArray(value)) {
      // Recurse into nested objects
      paths.push(...getLeafPaths(value as Record<string, unknown>, path));
    } else {
      // Leaf value (including arrays)
      paths.push(path);
    }
  }

  return paths;
}

// ============================================================================
// Core Merge Function
// ============================================================================

/**
 * Merge all config tiers into effective configuration
 *
 * Applies the 6-tier precedence chain:
 * defaults ← global ← project ← local ← env ← cli
 *
 * Each tier's values override lower tiers. Source tracking is maintained
 * throughout, allowing callers to see where each effective value came from.
 *
 * @param tiers - All 6 configuration tiers
 * @param options - Merge options
 * @returns Merged config with source annotations
 */
export function mergeConfigs(tiers: ConfigTiers, options: MergeOptions = {}): ConfigMergeResult {
  const startTime = performance.now();

  const sources: ConfigSourceMap = {};
  let envVarsApplied: string[] = [];
  let envVarErrors: EnvVarError[] = [];

  // 1. Start with defaults
  const defaults = tiers.defaults ?? DEFAULT_CONFIG;
  let merged = { ...defaults } as IncrediConfig;
  trackObjectSources(sources, defaults as Record<string, unknown>, "", "default");

  // 2. Merge global config
  if (tiers.global && Object.keys(tiers.global).length > 0) {
    merged = deepMerge(merged, tiers.global);
    trackObjectSources(sources, tiers.global as Record<string, unknown>, "", "global");
  }

  // 3. Merge project config
  if (tiers.project && Object.keys(tiers.project).length > 0) {
    merged = deepMerge(merged, tiers.project);
    trackObjectSources(sources, tiers.project as Record<string, unknown>, "", "project");
  }

  // 4. Merge local config
  if (tiers.local && Object.keys(tiers.local).length > 0) {
    merged = deepMerge(merged, tiers.local);
    trackObjectSources(sources, tiers.local as Record<string, unknown>, "", "local");
  }

  // 4.5. Merge runtime tier (VSCode memento snapshot — Issue #3335)
  if (tiers.runtime && Object.keys(tiers.runtime).length > 0) {
    merged = deepMerge(merged, tiers.runtime);
    trackObjectSources(sources, tiers.runtime as Record<string, unknown>, "", "runtime");
  }

  // 5. Merge env config
  let envConfig = tiers.env;
  if (!options.skipEnvResolution && !envConfig) {
    // Resolve from process.env if not provided
    const envResult: EnvVarResolutionResult = resolveEnvVars(options.processEnv ?? process.env);
    envConfig = envResult.config;
    envVarsApplied = envResult.appliedVars;
    envVarErrors = envResult.errors;
  }

  if (envConfig && Object.keys(envConfig).length > 0) {
    merged = deepMerge(merged, envConfig);
    trackObjectSources(sources, envConfig as Record<string, unknown>, "", "env");
  }

  // 6. Merge CLI config (highest priority)
  let cliOverrides: string[] = [];
  if (tiers.cli && Object.keys(tiers.cli).length > 0) {
    merged = deepMerge(merged, tiers.cli);
    trackObjectSources(sources, tiers.cli as Record<string, unknown>, "", "cli");
    cliOverrides = getLeafPaths(tiers.cli as Record<string, unknown>);
  }

  // 7. Validate final config
  let validation: ConfigValidationResult;
  if (options.skipValidation) {
    validation = { valid: true, errors: [], warnings: [], config: merged };
  } else {
    validation = validateConfig(merged);
    if (validation.valid && validation.config) {
      merged = validation.config;
    }
  }

  // 8. Build tier metadata
  const tierMetadata: TierMetadata = {
    hasDefaults: true, // Always true
    hasGlobal: !!(tiers.global && Object.keys(tiers.global).length > 0),
    hasProject: !!(tiers.project && Object.keys(tiers.project).length > 0),
    hasLocal: !!(tiers.local && Object.keys(tiers.local).length > 0),
    hasRuntime: !!(tiers.runtime && Object.keys(tiers.runtime).length > 0),
    hasEnv: !!(envConfig && Object.keys(envConfig).length > 0),
    hasCli: !!(tiers.cli && Object.keys(tiers.cli).length > 0),
  };

  const endTime = performance.now();

  return {
    config: merged,
    sources,
    validation,
    envVarsApplied,
    envVarErrors,
    cliOverrides,
    tiers: tierMetadata,
    mergeTimeMs: endTime - startTime,
  };
}

// ============================================================================
// Display Utilities
// ============================================================================

/**
 * Source display colors (for terminal/UI)
 */
export const SOURCE_COLORS: Record<ConfigSource | "cli", string> = {
  default: "gray",
  global: "blue",
  project: "green",
  local: "yellow",
  runtime: "cyan",
  env: "magenta",
  cli: "white",
};

/**
 * Source display labels
 */
export const SOURCE_LABELS: Record<ConfigSource | "cli", string> = {
  default: "Default",
  global: "Global (~/.nightgauge/config.yaml)",
  project: "Project (.nightgauge/config.yaml)",
  local: "Local (.nightgauge/config.local.yaml)",
  runtime: "Runtime (memento)",
  env: "Environment",
  cli: "CLI Flag",
};

/**
 * Get value at a dot-notation path
 */
export function getValueAtPath(obj: Record<string, unknown>, path: string): unknown {
  const parts = path.split(".");
  let current: unknown = obj;

  for (const part of parts) {
    if (current === null || current === undefined) {
      return undefined;
    }
    if (typeof current !== "object") {
      return undefined;
    }
    current = (current as Record<string, unknown>)[part];
  }

  return current;
}

/**
 * Format a single config entry for display
 */
export interface FormattedConfigEntry {
  path: string;
  value: unknown;
  source: ConfigSource | "cli";
  sourceLabel: string;
}

/**
 * Get formatted config entries for display
 *
 * Returns a flat list of all config values with their sources.
 */
export function getFormattedEntries(result: ConfigMergeResult): FormattedConfigEntry[] {
  const entries: FormattedConfigEntry[] = [];

  function traverse(obj: unknown, prefix: string): void {
    if (typeof obj !== "object" || obj === null) {
      return;
    }

    for (const key of Object.keys(obj as Record<string, unknown>)) {
      const path = prefix ? `${prefix}.${key}` : key;
      const value = (obj as Record<string, unknown>)[key];

      if (value === undefined) {
        continue;
      }

      // Get source, defaulting to 'default'
      let source = result.sources[path] as ConfigSource | "cli";
      if (!source) {
        source = "default";
      }

      if (typeof value === "object" && value !== null && !Array.isArray(value)) {
        // Recurse into nested objects
        traverse(value, path);
      } else {
        // Leaf value
        entries.push({
          path,
          value,
          source,
          sourceLabel: SOURCE_LABELS[source] || source,
        });
      }
    }
  }

  traverse(result.config, "");
  return entries.sort((a, b) => a.path.localeCompare(b.path));
}

/**
 * Format config for console/terminal display
 *
 * @param result - Merge result
 * @param options - Display options
 * @returns Formatted string
 */
export function formatConfigDisplay(
  result: ConfigMergeResult,
  options: { json?: boolean; color?: boolean; showAll?: boolean } = {}
): string {
  if (options.json) {
    return JSON.stringify(
      {
        config: result.config,
        sources: result.sources,
        tiers: result.tiers,
        envVarsApplied: result.envVarsApplied,
        cliOverrides: result.cliOverrides,
      },
      null,
      2
    );
  }

  const entries = getFormattedEntries(result);
  const lines: string[] = [];

  lines.push("Effective Configuration");
  lines.push("═".repeat(60));

  for (const entry of entries) {
    // Skip undefined values unless showAll
    if (entry.value === undefined && !options.showAll) {
      continue;
    }

    const valueStr =
      typeof entry.value === "object" ? JSON.stringify(entry.value) : String(entry.value);

    const sourceBadge = `[${entry.source}]`;

    lines.push(`${entry.path}: ${valueStr} ${sourceBadge}`);
  }

  lines.push("");
  lines.push(`Merge time: ${result.mergeTimeMs.toFixed(2)}ms`);

  if (result.envVarsApplied.length > 0) {
    lines.push(`Env vars applied: ${result.envVarsApplied.join(", ")}`);
  }

  if (result.cliOverrides.length > 0) {
    lines.push(`CLI overrides: ${result.cliOverrides.join(", ")}`);
  }

  if (!result.validation.valid) {
    lines.push("");
    lines.push("⚠ Validation Errors:");
    for (const err of result.validation.errors) {
      lines.push(`  - ${err.field}: ${err.message}`);
    }
  }

  return lines.join("\n");
}

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Create a simple merged config from file-based tiers only
 *
 * Convenience function that skips CLI tier and uses default env resolution.
 * This is what IncrediYamlService.readMerged() uses internally.
 */
export function mergeFileConfigs(
  globalConfig: Partial<IncrediConfig> | null | undefined,
  projectConfig: Partial<IncrediConfig> | null | undefined,
  localConfig: Partial<IncrediConfig> | null | undefined,
  options: MergeOptions = {}
): ConfigMergeResult {
  return mergeConfigs(
    {
      defaults: DEFAULT_CONFIG,
      global: globalConfig ?? undefined,
      project: projectConfig ?? undefined,
      local: localConfig ?? undefined,
    },
    options
  );
}

/**
 * Check if a config path was overridden by a higher tier
 *
 * @param result - Merge result
 * @param path - Config path to check
 * @param belowSource - Check if overridden by something higher than this
 */
export function wasOverridden(
  result: ConfigMergeResult,
  path: string,
  belowSource: ConfigSource
): boolean {
  const source = result.sources[path];
  if (!source) return false;

  const precedence: ConfigSource[] = ["default", "global", "project", "local", "runtime", "env"];
  const sourceIndex = precedence.indexOf(source as ConfigSource);
  const belowIndex = precedence.indexOf(belowSource);

  return sourceIndex > belowIndex;
}

/**
 * Get all paths that came from a specific source
 */
export function getPathsFromSource(
  result: ConfigMergeResult,
  source: ConfigSource | "cli"
): string[] {
  return Object.entries(result.sources)
    .filter(([, s]) => s === source)
    .map(([path]) => path);
}
