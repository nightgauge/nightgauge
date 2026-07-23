/**
 * Environment Variable Resolver for Nightgauge Configuration
 *
 * Resolves NIGHTGAUGE_* environment variables to a partial config object.
 * Uses algorithmic path→env var transformation with Zod schema introspection
 * for type coercion.
 *
 * @example
 * ```typescript
 * // NIGHTGAUGE_PR_MERGE_STRATEGY=rebase
 * // NIGHTGAUGE_PIPELINE_AUTO_FIX=false
 * // NIGHTGAUGE_BATCH_MAX_ISSUES=5
 *
 * const envConfig = resolveEnvVars();
 * // Returns: { pr: { merge_strategy: 'rebase' }, pipeline: { auto_fix: false }, batch: { max_issues: 5 } }
 * ```
 *
 * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
 * @see docs/CONFIGURATION.md - Environment variable documentation
 */

import { IncrediConfigSchema, type IncrediConfig } from "./schema";
import { z } from "zod";

/**
 * Prefix for all Nightgauge environment variables
 */
export const ENV_VAR_PREFIX = "NIGHTGAUGE_";

/**
 * Result of resolving environment variables
 */
export interface EnvVarResolutionResult {
  /** Partial config from environment variables */
  config: Partial<IncrediConfig>;
  /** List of NIGHTGAUGE_* env vars that were applied */
  appliedVars: string[];
  /** Errors encountered during resolution (non-fatal) */
  errors: EnvVarError[];
}

/**
 * Error during env var resolution
 */
export interface EnvVarError {
  /** Environment variable name */
  envVar: string;
  /** Config path it maps to */
  configPath: string;
  /** Error message */
  message: string;
}

/**
 * Convert a config path to its corresponding NIGHTGAUGE_* env var name
 *
 * @example
 * configPathToEnvVar('pr.merge_strategy') → 'NIGHTGAUGE_PR_MERGE_STRATEGY'
 * configPathToEnvVar('pipeline.retry.max_auto_attempts') → 'NIGHTGAUGE_PIPELINE_RETRY_MAX_AUTO_ATTEMPTS'
 */
export function configPathToEnvVar(configPath: string): string {
  return (
    ENV_VAR_PREFIX +
    configPath
      .replace(/\./g, "_")
      .replace(/([a-z])([A-Z])/g, "$1_$2")
      .toUpperCase()
  );
}

/**
 * Convert a NIGHTGAUGE_* env var name to its config path
 *
 * @example
 * envVarToConfigPath('NIGHTGAUGE_PR_MERGE_STRATEGY') → 'pr.merge_strategy'
 * envVarToConfigPath('NIGHTGAUGE_PIPELINE_RETRY_MAX_AUTO_ATTEMPTS') → 'pipeline.retry.max_auto_attempts'
 */
export function envVarToConfigPath(envVar: string): string | null {
  if (!envVar.startsWith(ENV_VAR_PREFIX)) {
    return null;
  }

  const withoutPrefix = envVar.slice(ENV_VAR_PREFIX.length);
  return withoutPrefix.toLowerCase().replace(/_/g, ".");
}

/**
 * Get the Zod schema type for a config path
 *
 * Uses schema introspection to determine expected type for parsing.
 *
 * @returns 'string' | 'number' | 'boolean' | 'array' | 'object' | null
 */
export function getSchemaType(
  configPath: string
): "string" | "number" | "boolean" | "array" | "object" | null {
  const parts = configPath.split(".");

  // Schema traversal requires the internal Zod v4 base type ($ZodType) because ZodObject.shape
  // returns Record<string, $ZodType>, which does not extend the public ZodType interface.
  // We use unknown + type assertions scoped to the narrowest possible points.
  let schema: unknown = IncrediConfigSchema;

  for (const part of parts) {
    if (!schema) return null;

    // Unwrap optional/nullable
    if (schema instanceof z.ZodOptional || schema instanceof z.ZodNullable) {
      schema = schema.unwrap();
    }

    // Handle ZodObject
    if (schema instanceof z.ZodObject) {
      const shape = schema.shape;
      if (!(part in shape)) {
        // Handle aliases (pr vs pull_request)
        if (part === "pr" && "pull_request" in shape) {
          schema = shape["pull_request"];
        } else if (part === "pull_request" && "pr" in shape) {
          schema = shape["pr"];
        } else {
          return null;
        }
      } else {
        schema = shape[part];
      }
    } else {
      return null;
    }
  }

  if (!schema) return null;

  // Unwrap optional/nullable for final type check
  if (schema instanceof z.ZodOptional || schema instanceof z.ZodNullable) {
    schema = schema.unwrap();
  }

  // Determine type
  if (schema instanceof z.ZodString || schema instanceof z.ZodEnum) {
    return "string";
  }
  if (schema instanceof z.ZodNumber) {
    return "number";
  }
  if (schema instanceof z.ZodBoolean) {
    return "boolean";
  }
  if (schema instanceof z.ZodArray) {
    return "array";
  }
  if (schema instanceof z.ZodObject) {
    return "object";
  }

  return "string"; // Default fallback
}

/**
 * Parse an environment variable value to the expected type
 *
 * @param value - Raw string value from environment
 * @param expectedType - Expected type from schema introspection
 * @returns Parsed value or null if parsing fails
 */
export function parseEnvValue(
  value: string,
  expectedType: "string" | "number" | "boolean" | "array" | "object" | null
): unknown {
  if (value === "" || value === undefined) {
    return undefined;
  }

  switch (expectedType) {
    case "boolean":
      return parseBooleanEnv(value);

    case "number":
      return parseNumberEnv(value);

    case "array":
      return parseArrayEnv(value);

    case "object":
      return parseObjectEnv(value);

    case "string":
    default:
      return value;
  }
}

/**
 * Parse boolean from environment variable
 *
 * Accepts: true, false, yes, no, 1, 0, on, off (case-insensitive)
 */
export function parseBooleanEnv(value: string): boolean | undefined {
  const normalized = value.toLowerCase().trim();

  if (["true", "yes", "1", "on"].includes(normalized)) {
    return true;
  }

  if (["false", "no", "0", "off"].includes(normalized)) {
    return false;
  }

  return undefined;
}

/**
 * Parse number from environment variable
 *
 * Supports integers and floats.
 */
export function parseNumberEnv(value: string): number | undefined {
  const trimmed = value.trim();

  // Empty string should return undefined, not 0
  if (trimmed === "") {
    return undefined;
  }

  const parsed = Number(trimmed);

  if (isNaN(parsed)) {
    return undefined;
  }

  return parsed;
}

/**
 * Parse array from environment variable
 *
 * Strategy:
 * 1. If value starts with '[', try JSON parse
 * 2. Otherwise, split by comma and trim each element
 */
export function parseArrayEnv(value: string): string[] | undefined {
  const trimmed = value.trim();

  // JSON array (e.g., ["item1", "item2"])
  if (trimmed.startsWith("[")) {
    try {
      const parsed = JSON.parse(trimmed);
      if (Array.isArray(parsed)) {
        return parsed.map((item) => String(item));
      }
    } catch {
      // Fall through to comma-separated parsing
    }
  }

  // Comma-separated (e.g., "item1,item2,item3")
  if (trimmed === "") {
    return [];
  }

  return trimmed.split(",").map((item) => item.trim());
}

/**
 * Parse object from environment variable
 *
 * Expects JSON format.
 */
export function parseObjectEnv(value: string): Record<string, unknown> | undefined {
  const trimmed = value.trim();

  if (!trimmed.startsWith("{")) {
    return undefined;
  }

  try {
    const parsed = JSON.parse(trimmed);
    if (typeof parsed === "object" && parsed !== null) {
      return parsed;
    }
  } catch {
    return undefined;
  }

  return undefined;
}

/**
 * Set a nested value in an object using dot-notation path
 *
 * @example
 * setNestedValue({}, 'pr.merge_strategy', 'rebase')
 * // Returns: { pr: { merge_strategy: 'rebase' } }
 */
export function setNestedValue(obj: Record<string, unknown>, path: string, value: unknown): void {
  const parts = path.split(".");
  for (const part of parts) {
    if (part === "__proto__" || part === "prototype" || part === "constructor") {
      throw new Error("Unsafe configuration path");
    }
  }
  let current = obj;

  for (let i = 0; i < parts.length - 1; i++) {
    const part = parts[i];
    if (!(part in current) || typeof current[part] !== "object") {
      current[part] = {};
    }
    current = current[part] as Record<string, unknown>;
  }

  const lastPart = parts[parts.length - 1];
  current[lastPart] = value;
}

/**
 * Known config paths for validation
 *
 * This list is used to filter valid NIGHTGAUGE_* env vars.
 * Generated from Zod schema structure.
 */
const KNOWN_CONFIG_PATHS: string[] = [
  // Project
  "project.number",
  "project.owner",
  "project.auto_dates",
  // PR / pull_request
  "pr.merge_strategy",
  "pr.delete_branch",
  "pr.reviewers",
  "pr.auto_merge",
  "pr.auto_merge_epic",
  "pr.epic_merge_strategy",
  "pr.auto_fix_ci",
  "pr.auto_fix_max_attempts",
  "pr.ci_check_timeout",
  "pull_request.merge_strategy",
  "pull_request.delete_branch",
  "pull_request.reviewers",
  "pull_request.auto_merge",
  "pull_request.auto_merge_epic",
  "pull_request.epic_merge_strategy",
  "pull_request.auto_fix_ci",
  "pull_request.auto_fix_max_attempts",
  "pull_request.ci_check_timeout",
  // Branch
  "branch.base",
  "branch.protected",
  "branch.suggestions",
  // Issue
  "issue.default_status",
  // Pipeline
  "pipeline.ci_timeout",
  "pipeline.auto_fix",
  "pipeline.architecture_approval.enabled",
  "pipeline.architecture_approval.approval_label",
  "pipeline.skip.tests",
  "pipeline.skip.lint",
  "pipeline.skip.typecheck",
  "pipeline.skip.build",
  "pipeline.skip.format",
  "pipeline.logs.retain",
  "pipeline.logs.dir",
  "pipeline.logs.max_age_days",
  "pipeline.logs.max_count",
  "pipeline.retry.max_auto_attempts",
  "pipeline.retry.backoff_multiplier",
  "pipeline.retry.initial_delay_ms",
  "pipeline.retry.rate_limit_delay_ms",
  // Pipeline - Output Token Limits (Issue #842)
  // Note: output_token_limits is a record type; individual stage overrides
  // are handled via NIGHTGAUGE_PIPELINE_OUTPUT_TOKEN_LIMIT_{STAGE}
  // in incrediConfig.ts, not through the generic resolver.
  // Routing
  "routing.trivial_max_complexity",
  "routing.extensive_min_complexity",
  "routing.force_full_pipeline",
  // Enforcement
  "enforcement.dependencies.enabled",
  "enforcement.dependencies.mode",
  "enforcement.dependencies.check_transitive",
  // Commands
  "commands.test",
  "commands.lint",
  "commands.typecheck",
  "commands.format",
  "commands.build",
  // Validation
  "validation.require_tests",
  "validation.require_changelog",
  "validation.max_files_changed",
  "validation.max_lines_changed",
  // Sanitization
  "sanitization.enabled",
  "sanitization.sanitize_input",
  "sanitization.logging",
  "sanitization.warn_only",
  "sanitization.allowlist",
  "sanitization.blocklist",
  // Human-in-the-loop
  "human_in_the_loop.auto_accept_stages",
  "human_in_the_loop.auto_accept_permissions",
  "human_in_the_loop.trusted_stages",
  // Batch
  "batch.max_issues",
  "batch.pause_between_issues",
  "batch.concurrency",
  "batch.stop_on_error",
  "batch.retry_failed_issues",
  "batch.max_retries",
  "batch.show_summary",
  "batch.notify_on_complete",
  "batch.notify_on_each_issue",
  "batch.show_progress_estimate",
  "batch.resource_limits.token_budget",
  "batch.resource_limits.cost_budget",
  "batch.resource_limits.time_budget",
  "batch.history.save_history",
  "batch.history.history_limit",
  // Ralph Loop
  "ralph_loop.enabled",
  "ralph_loop.build",
  "ralph_loop.tests",
  "ralph_loop.lint",
  "ralph_loop.limits.max_iterations",
  "ralph_loop.limits.token_budget_per_iteration",
  "ralph_loop.limits.total_token_budget",
  "ralph_loop.limits.iteration_timeout_ms",
  "ralph_loop.limits.total_timeout_ms",
  "ralph_loop.abort_patterns",
  // Automations
  "automations.enabled",
  "automations.dry_run",
  "automations.log_file",
  // UI - Core
  "ui.core.adapter",
  "ui.core.auth_provider",
  "ui.core.default_model",
  "ui.core.context_path",
  "ui.core.plans_path",
  // UI - Dashboard
  "ui.dashboard.time_savings.issue_pickup",
  "ui.dashboard.time_savings.feature_planning",
  "ui.dashboard.time_savings.feature_dev",
  "ui.dashboard.time_savings.pr_create",
  "ui.dashboard.time_savings.pr_merge",
  // UI - Output Window
  "ui.output_window.auto_open",
  "ui.output_window.auto_scroll",
  "ui.output_window.verbose_level",
  "ui.output_window.show_token_usage",
  "ui.output_window.word_wrap",
  // UI - Notifications
  "ui.notifications.enabled",
  "ui.notifications.sounds.enabled",
  "ui.notifications.sounds.alert",
  "ui.notifications.sounds.success",
  "ui.notifications.sounds.error",
  "ui.notifications.sounds.volume",
  "ui.notifications.banner_enabled",
  "ui.notifications.dock_bounce_enabled",
  "ui.notifications.respect_do_not_disturb",
  // UI - Ready Items
  "ui.ready_items.auto_refresh",
  "ui.ready_items.refresh_interval",
  "ui.ready_items.sort_by",
  "ui.ready_items.sort_direction",
  "ui.ready_items.filters.priority",
  "ui.ready_items.filters.size",
  "ui.ready_items.filters.component",
  "ui.ready_items.search_text",
  "ui.ready_items.show_dependencies",
  // UI - Sidebar
  "ui.sidebar.hide_empty_sections",
  // UI - Pipeline
  "ui.pipeline.auto_continue",
  "ui.pipeline.auto_continue_delay",
  // UI - Project Board
  "ui.project_board.group_by_epic",
  "ui.project_board.default_epic_collapsed",
  // UI - Warnings
  "ui.warnings.enabled",
  "ui.warnings.warn_on_in_progress",
  "ui.warnings.warn_on_in_review",
  // UI - Plugins
  "ui.plugins.auto_prompt",
  "ui.plugins.marketplace_url",
  // Platform cloud API (Issues #1458, #3718)
  "platform.enabled",
  "platform.api_url",
  "platform.environment",
  "platform.connection_timeout_ms",
  "platform.retry_policy.attempts",
  "platform.retry_policy.backoff_ms",
  "platform.retry_policy.backoff_multiplier",
  "platform.telemetry.enabled",
];

/**
 * Build a reverse lookup from env var names to config paths
 */
const ENV_VAR_TO_PATH_MAP: Map<string, string> = new Map(
  KNOWN_CONFIG_PATHS.map((path) => [configPathToEnvVar(path), path])
);

/**
 * Resolve environment variables to a partial config object
 *
 * Scans process.env for NIGHTGAUGE_* variables and converts them to
 * a partial config object using schema-aware type coercion.
 *
 * @param env - Process environment (defaults to process.env)
 * @returns Resolution result with config, applied vars, and any errors
 */
export function resolveEnvVars(env: NodeJS.ProcessEnv = process.env): EnvVarResolutionResult {
  const config: Record<string, unknown> = {};
  const appliedVars: string[] = [];
  const errors: EnvVarError[] = [];

  // Find all NIGHTGAUGE_* env vars
  const incrediVars = Object.keys(env).filter((key) => key.startsWith(ENV_VAR_PREFIX));

  for (const envVar of incrediVars) {
    const value = env[envVar];
    if (value === undefined || value === "") {
      continue;
    }

    // Look up the config path
    const configPath = ENV_VAR_TO_PATH_MAP.get(envVar);

    if (!configPath) {
      // Unknown env var - skip silently (user may have custom NIGHTGAUGE_ vars)
      continue;
    }

    // Get expected type from schema
    const expectedType = getSchemaType(configPath);

    // Parse value
    const parsedValue = parseEnvValue(value, expectedType);

    if (parsedValue === undefined) {
      errors.push({
        envVar,
        configPath,
        message: `Failed to parse "${value}" as ${expectedType ?? "string"}`,
      });
      continue;
    }

    // Set the value in the config object
    setNestedValue(config, configPath, parsedValue);
    appliedVars.push(envVar);
  }

  return {
    config: config as Partial<IncrediConfig>,
    appliedVars,
    errors,
  };
}

/**
 * Get a single config value from environment variable
 *
 * Convenience function for checking a specific env var.
 *
 * @param configPath - Dot-notation config path
 * @param env - Process environment (defaults to process.env)
 * @returns Parsed value or undefined
 */
export function getEnvConfigValue(
  configPath: string,
  env: NodeJS.ProcessEnv = process.env
): unknown {
  const envVar = configPathToEnvVar(configPath);
  const value = env[envVar];

  if (value === undefined || value === "") {
    return undefined;
  }

  const expectedType = getSchemaType(configPath);
  return parseEnvValue(value, expectedType);
}

/**
 * Check if an environment variable override exists for a config path
 *
 * @param configPath - Dot-notation config path
 * @param env - Process environment (defaults to process.env)
 * @returns True if env var exists and has a value
 */
export function hasEnvOverride(configPath: string, env: NodeJS.ProcessEnv = process.env): boolean {
  const envVar = configPathToEnvVar(configPath);
  const value = env[envVar];
  return value !== undefined && value !== "";
}

/**
 * Get the env var name for a config path (for documentation/display)
 *
 * @param configPath - Dot-notation config path
 * @returns Environment variable name
 */
export function getEnvVarName(configPath: string): string {
  return configPathToEnvVar(configPath);
}

/**
 * Get all known environment variable names
 *
 * Useful for documentation and auto-completion.
 */
export function getAllKnownEnvVars(): string[] {
  return Array.from(ENV_VAR_TO_PATH_MAP.keys()).sort();
}
