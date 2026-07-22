/**
 * Pipeline control configuration resolvers extracted from incrediConfig.ts.
 *
 * Covers human-in-the-loop, retry, PR CI check, budget enforcement, output
 * token limits, context budgets, pipeline ceiling, backtracks, escalations,
 * epic merge, concurrent pipelines, and context schema repair.
 *
 * @see Issue #2742 - Refactor VSCode incrediConfig.ts into focused domain modules
 */

import * as fs from "node:fs";
import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { DEFAULT_RETRY_CONFIG, type RetryConfig } from "../retryHelpers";
import { resolveConfigPathSync, logDeprecationWarning } from "../configPathResolver";
import { readEffectiveConfigTextSync } from "../mergedConfigReader";
import {
  DEFAULT_SIZE_AWARE_BUDGETS,
  getBudgetPreset,
  type BudgetMode,
  type BudgetPresetName,
  type SizeAwareBudget,
  type SizeLabel,
} from "../budgetEnforcer";
// PipelineCeilingConfig is owned by pipelineBudgetCeiling.ts (the pure ceiling
// utility). Import it rather than re-declaring so the two never drift — see
// Issue #3542 review.
import type { PipelineCeilingConfig } from "../pipelineBudgetCeiling";

export type { BudgetMode, SizeLabel } from "../budgetEnforcer";
export type { PipelineCeilingConfig };

// ============================================================================
// PR CI Check Configuration (Issue #426)
// ============================================================================

/**
 * PR CI check gate configuration from config.yaml
 *
 * @see Issue #426 - CI check gate and auto-fix retry loop
 */
export interface PrCICheckConfig {
  /** Auto-fix CI failures before merge (default: true) */
  autoFixCI: boolean;
  /**
   * Maximum auto-fix retry attempts before exiting the loop and escalating.
   * Lowered from 3 to 2 in #3108 to bound LLM-driven auto-fix spend after
   * a UI redesign or other mass selector change in pr-merge. The trailing
   * iteration was usually the costliest and rarely produced new progress.
   */
  autoFixMaxAttempts: number;
  /** Timeout for CI checks in seconds (default: 600) */
  ciCheckTimeout: number;
}

/**
 * Default PR CI check configuration
 */
export const DEFAULT_PR_CI_CHECK_CONFIG: PrCICheckConfig = {
  autoFixCI: true,
  autoFixMaxAttempts: 2,
  ciCheckTimeout: 600,
};

// ============================================================================
// Human-in-the-loop Configuration
// ============================================================================

/**
 * Human-in-the-loop configuration from nightgauge.yaml
 */
export interface HumanInTheLoopConfig {
  /** Auto-approve all stage gates */
  autoAcceptStages: boolean;
  /** Auto-accept tool/file permission prompts */
  autoAcceptPermissions: boolean;
  /** Specific stages to auto-accept (overrides autoAcceptStages=false) */
  trustedStages: string[];
}

/**
 * Read human_in_the_loop config from nightgauge.yaml
 *
 * Uses simple line parsing to avoid YAML library dependency.
 * Supports environment variable overrides.
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns Human-in-the-loop configuration
 */
export function getHumanInTheLoopConfig(workspaceRoot?: string): HumanInTheLoopConfig {
  const defaults: HumanInTheLoopConfig = {
    autoAcceptStages: false,
    autoAcceptPermissions: false,
    trustedStages: [],
  };

  // Check environment variable overrides first
  if (process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES === "true") {
    defaults.autoAcceptStages = true;
  }
  if (process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS === "true") {
    defaults.autoAcceptPermissions = true;
  }

  // If env vars are set, return early
  if (defaults.autoAcceptStages && defaults.autoAcceptPermissions) {
    return defaults;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return defaults;
  }

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return defaults;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read and parse config file (simple line parsing)
    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inHumanInTheLoop = false;
    let autoAcceptStages = defaults.autoAcceptStages;
    let autoAcceptPermissions = defaults.autoAcceptPermissions;
    const trustedStages: string[] = [];
    let inTrustedStagesArray = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed.startsWith("human_in_the_loop:")) {
        inHumanInTheLoop = true;
        continue;
      }

      if (inHumanInTheLoop) {
        // Check for next top-level key (end of human_in_the_loop section)
        if (
          trimmed &&
          !trimmed.startsWith("#") &&
          /^[a-z_]+:/.test(trimmed) &&
          !line.startsWith(" ")
        ) {
          inHumanInTheLoop = false;
          inTrustedStagesArray = false;
          continue;
        }

        if (trimmed.includes("auto_accept_stages:")) {
          autoAcceptStages = trimmed.includes("true");
          inTrustedStagesArray = false;
        } else if (trimmed.includes("auto_accept_permissions:")) {
          autoAcceptPermissions = trimmed.includes("true");
          inTrustedStagesArray = false;
        } else if (trimmed.includes("trusted_stages:")) {
          inTrustedStagesArray = true;
          // Handle inline array
          const afterColon = trimmed.split("trusted_stages:")[1];
          if (afterColon && afterColon.trim().startsWith("[")) {
            const match = afterColon.match(/\[(.*)\]/);
            if (match) {
              const items = match[1]
                .split(",")
                .map((s) => s.trim().replace(/['"]/g, ""))
                .filter((s) => s.length > 0);
              trustedStages.push(...items);
            }
            inTrustedStagesArray = false;
          }
        } else if (inTrustedStagesArray && trimmed.startsWith("- ")) {
          // Multiline array item
          const stageName = trimmed.substring(2).trim();
          if (stageName) {
            trustedStages.push(stageName);
          }
        } else if (inTrustedStagesArray && trimmed && !trimmed.startsWith("#")) {
          // End of array
          inTrustedStagesArray = false;
        }
      }
    }

    return {
      autoAcceptStages,
      autoAcceptPermissions,
      trustedStages,
    };
  } catch (error) {
    console.error("Failed to read nightgauge config:", error);
    return defaults;
  }
}

/**
 * Check if a specific stage should be auto-accepted
 *
 * @param stage - Pipeline stage to check
 * @param workspaceRoot - Optional workspace root
 * @returns True if stage should be auto-accepted
 */
export function shouldAutoAcceptStage(stage: PipelineStage, workspaceRoot?: string): boolean {
  const config = getHumanInTheLoopConfig(workspaceRoot);
  return config.autoAcceptStages || config.trustedStages.includes(stage);
}

/**
 * Get the initial execution mode based on config
 *
 * Returns 'automatic' if auto_accept_stages is true,
 * otherwise returns 'manual'.
 *
 * @param workspaceRoot - Optional workspace root
 * @returns 'automatic' | 'manual'
 */
export function getInitialExecutionMode(workspaceRoot?: string): "automatic" | "manual" {
  const config = getHumanInTheLoopConfig(workspaceRoot);
  return config.autoAcceptStages ? "automatic" : "manual";
}

// ============================================================================
// Retry Configuration
// ============================================================================

/**
 * Read retry configuration from nightgauge.yaml
 *
 * Reads pipeline.retry section from nightgauge.yaml with environment variable overrides.
 * Returns default configuration if file doesn't exist or section is missing.
 *
 * Environment overrides:
 * - NIGHTGAUGE_RETRY_MAX_AUTO_ATTEMPTS
 * - NIGHTGAUGE_RETRY_BACKOFF_MULTIPLIER
 * - NIGHTGAUGE_RETRY_INITIAL_DELAY_MS
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns Retry configuration
 *
 * @example
 * ```yaml
 * # .nightgauge/nightgauge.yaml
 * pipeline:
 *   retry:
 *     max_auto_attempts: 3
 *     backoff_multiplier: 2
 *     initial_delay_ms: 5000
 *     retryable_api_errors: [500, 502, 503, 504]
 *     rate_limit_delay_ms: 60000
 * ```
 */
export function getRetryConfig(workspaceRoot?: string): RetryConfig {
  const config: RetryConfig = { ...DEFAULT_RETRY_CONFIG };

  // Check environment variable overrides first
  if (process.env.NIGHTGAUGE_RETRY_MAX_AUTO_ATTEMPTS) {
    const parsed = Number.parseInt(process.env.NIGHTGAUGE_RETRY_MAX_AUTO_ATTEMPTS, 10);
    if (!Number.isNaN(parsed) && parsed > 0) {
      config.max_auto_attempts = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_RETRY_BACKOFF_MULTIPLIER) {
    const parsed = Number.parseFloat(process.env.NIGHTGAUGE_RETRY_BACKOFF_MULTIPLIER);
    if (!Number.isNaN(parsed) && parsed >= 1) {
      config.backoff_multiplier = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_RETRY_INITIAL_DELAY_MS) {
    const parsed = Number.parseInt(process.env.NIGHTGAUGE_RETRY_INITIAL_DELAY_MS, 10);
    if (!Number.isNaN(parsed) && parsed > 0) {
      config.initial_delay_ms = parsed;
    }
  }

  // If env vars are set, return early (skip file reading)
  if (
    process.env.NIGHTGAUGE_RETRY_MAX_AUTO_ATTEMPTS ||
    process.env.NIGHTGAUGE_RETRY_BACKOFF_MULTIPLIER ||
    process.env.NIGHTGAUGE_RETRY_INITIAL_DELAY_MS
  ) {
    return config;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return config;
  }

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return config;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read and parse config file (simple line parsing)
    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inRetry = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect pipeline: section
      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      // Detect retry: subsection under pipeline
      if (inPipeline && trimmed === "retry:") {
        inRetry = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inRetry = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          // New pipeline subsection (not retry)
          inRetry = false;
        }
      }

      // Parse retry config values
      if (inRetry) {
        const match = trimmed.match(/^([a-z_]+):\s*(.+)$/);
        if (match) {
          const [, key, value] = match;
          switch (key) {
            case "max_auto_attempts": {
              const parsed = Number.parseInt(value, 10);
              if (!Number.isNaN(parsed) && parsed > 0) {
                config.max_auto_attempts = parsed;
              }
              break;
            }
            case "backoff_multiplier": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 1) {
                config.backoff_multiplier = parsed;
              }
              break;
            }
            case "initial_delay_ms": {
              const parsed = Number.parseInt(value, 10);
              if (!Number.isNaN(parsed) && parsed > 0) {
                config.initial_delay_ms = parsed;
              }
              break;
            }
            case "rate_limit_delay_ms": {
              const parsed = Number.parseInt(value, 10);
              if (!Number.isNaN(parsed) && parsed > 0) {
                config.rate_limit_delay_ms = parsed;
              }
              break;
            }
            case "retryable_api_errors": {
              // Parse array: [500, 502, 503, 504]
              const arrayMatch = value.match(/\[([\d,\s]+)\]/);
              if (arrayMatch) {
                const codes = arrayMatch[1]
                  .split(",")
                  .map((s) => Number.parseInt(s.trim(), 10))
                  .filter((n) => !Number.isNaN(n));
                if (codes.length > 0) {
                  config.retryable_api_errors = codes;
                }
              }
              break;
            }
          }
        }
      }
    }

    return config;
  } catch (error) {
    console.error("Failed to read retry config from nightgauge config:", error);
    return config;
  }
}

// ============================================================================
// PR CI Check Configuration (Issue #426)
// ============================================================================

/**
 * Read PR CI check configuration from nightgauge.yaml
 *
 * Reads pr.auto_fix_ci, pr.auto_fix_max_attempts, and pr.ci_check_timeout
 * from nightgauge.yaml with environment variable overrides.
 * Returns default configuration if file doesn't exist or section is missing.
 *
 * Environment overrides:
 * - NIGHTGAUGE_PR_AUTO_FIX_CI
 * - NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS
 * - NIGHTGAUGE_PR_CI_CHECK_TIMEOUT
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns PR CI check configuration
 *
 * @see Issue #426 - CI check gate and auto-fix retry loop
 *
 * @example
 * ```yaml
 * # .nightgauge/nightgauge.yaml
 * pr:
 *   auto_fix_ci: true
 *   auto_fix_max_attempts: 2
 *   ci_check_timeout: 600
 * ```
 */
export function getPrCICheckConfig(workspaceRoot?: string): PrCICheckConfig {
  const config: PrCICheckConfig = { ...DEFAULT_PR_CI_CHECK_CONFIG };

  // Check environment variable overrides first
  if (process.env.NIGHTGAUGE_PR_AUTO_FIX_CI !== undefined) {
    config.autoFixCI = process.env.NIGHTGAUGE_PR_AUTO_FIX_CI === "true";
  }
  if (process.env.NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS) {
    const parsed = Number.parseInt(process.env.NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS, 10);
    if (!Number.isNaN(parsed) && parsed > 0) {
      config.autoFixMaxAttempts = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_PR_CI_CHECK_TIMEOUT) {
    const parsed = Number.parseInt(process.env.NIGHTGAUGE_PR_CI_CHECK_TIMEOUT, 10);
    if (!Number.isNaN(parsed) && parsed > 0) {
      config.ciCheckTimeout = parsed;
    }
  }

  // If env vars are set, return early (skip file reading)
  if (
    process.env.NIGHTGAUGE_PR_AUTO_FIX_CI !== undefined ||
    process.env.NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS ||
    process.env.NIGHTGAUGE_PR_CI_CHECK_TIMEOUT
  ) {
    return config;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return config;
  }

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return config;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read and parse config file (simple line parsing)
    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPr = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect pr: section
      if (trimmed === "pr:") {
        inPr = true;
        continue;
      }

      // Exit section on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPr = false;
        }
      }

      // Parse pr config values
      if (inPr) {
        const match = trimmed.match(/^([a-z_]+):\s*(.+)$/);
        if (match) {
          const [, key, value] = match;
          switch (key) {
            case "auto_fix_ci": {
              config.autoFixCI = value.trim() === "true";
              break;
            }
            case "auto_fix_max_attempts": {
              const parsed = Number.parseInt(value, 10);
              if (!Number.isNaN(parsed) && parsed > 0) {
                config.autoFixMaxAttempts = parsed;
              }
              break;
            }
            case "ci_check_timeout": {
              const parsed = Number.parseInt(value, 10);
              if (!Number.isNaN(parsed) && parsed > 0) {
                config.ciCheckTimeout = parsed;
              }
              break;
            }
          }
        }
      }
    }

    return config;
  } catch (error) {
    console.error("Failed to read PR CI check config from nightgauge config:", error);
    return config;
  }
}

// ============================================================================
// Budget Enforcement Configuration (Issue #835)
// ============================================================================

/**
 * Budget enforcement configuration resolved from config/env.
 * @see Issue #835 - Enforce hard budget limits
 */
export interface BudgetEnforcementConfig {
  mode: BudgetMode;
  gracePercent: number;
  /** % of base budget at which wind-down signal fires (default 80).
   * @see Issue #2338 - Intelligent budget management */
  windDownPercent?: number;
  preset?: BudgetPresetName;
  stageOverrides?: Record<string, Partial<SizeAwareBudget>>;
}

/**
 * Get budget enforcement configuration from config or environment.
 *
 * Priority:
 * 1. Environment variables: NIGHTGAUGE_PIPELINE_BUDGET_MODE,
 *    NIGHTGAUGE_PIPELINE_BUDGET_GRACE_PERCENT
 * 2. Config file: pipeline.budget_mode, pipeline.budget_grace_percent
 * 3. Defaults: { mode: 'hard', gracePercent: 50 }
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Budget enforcement configuration
 *
 * @see Issue #835 - Enforce hard budget limits
 */
export function getBudgetEnforcementConfig(workspaceRoot?: string): BudgetEnforcementConfig {
  const validModes: BudgetMode[] = ["hard", "soft", "threshold"];
  const validPresets: BudgetPresetName[] = ["conservative", "standard", "generous"];
  let mode: BudgetMode = "hard";
  let gracePercent = 50;
  let windDownPercent: number | undefined;
  let preset: BudgetPresetName | undefined;

  // Check environment variables first
  const envMode = process.env.NIGHTGAUGE_PIPELINE_BUDGET_MODE;
  if (envMode && validModes.includes(envMode as BudgetMode)) {
    mode = envMode as BudgetMode;
  }

  const envGrace = process.env.NIGHTGAUGE_PIPELINE_BUDGET_GRACE_PERCENT;
  if (envGrace) {
    const parsed = Number.parseFloat(envGrace);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 500) {
      gracePercent = parsed;
    }
  }

  // If both env vars provided, return early
  if (envMode && envGrace) {
    return { mode, gracePercent };
  }

  // Check config file
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return { mode, gracePercent };
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return { mode, gracePercent };
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
        }
      }

      if (inPipeline) {
        if (!envMode) {
          const modeMatch = trimmed.match(
            /^budget_mode:\s*['"]?(hard|soft|threshold)['"]?(?:\s+#.*)?$/
          );
          if (modeMatch) {
            mode = modeMatch[1] as BudgetMode;
          }
        }

        if (!envGrace) {
          const graceMatch = trimmed.match(/^budget_grace_percent:\s*([\d.]+)$/);
          if (graceMatch) {
            const parsed = Number.parseFloat(graceMatch[1]);
            if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 500) {
              gracePercent = parsed;
            }
          }
        }

        // Wind-down threshold (Issue #2338)
        const windDownMatch = trimmed.match(/^budget_winddown_percent:\s*([\d.]+)$/);
        if (windDownMatch) {
          const parsed = Number.parseFloat(windDownMatch[1]);
          if (!Number.isNaN(parsed) && parsed >= 50 && parsed <= 100) {
            windDownPercent = parsed;
          }
        }

        const presetMatch = trimmed.match(
          /^budget_preset:\s*['"]?(conservative|standard|generous)['"]?(?:\s+#.*)?$/
        );
        if (presetMatch && validPresets.includes(presetMatch[1] as BudgetPresetName)) {
          preset = presetMatch[1] as BudgetPresetName;
        }
      }
    }

    const result: BudgetEnforcementConfig = {
      mode,
      gracePercent,
      windDownPercent,
    };
    if (preset) {
      result.preset = preset;
      result.stageOverrides = getBudgetPreset(preset);
    }

    // Read stage_budget_multipliers and apply to stageOverrides
    const stageMultipliers: Record<string, number> = {};
    let inPipeline2 = false;
    let inMultipliers2 = false;

    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed === "pipeline:") {
        inPipeline2 = true;
        continue;
      }
      if (
        inPipeline2 &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inPipeline2 = false;
      }
      if (inPipeline2 && trimmed === "stage_budget_multipliers:") {
        inMultipliers2 = true;
        continue;
      }
      if (inMultipliers2) {
        if (!line.startsWith("  ") || trimmed === "") {
          inMultipliers2 = false;
        } else {
          const m = trimmed.match(/^([a-z-]+):\s*([\d.]+)$/);
          if (m) stageMultipliers[m[1]] = parseFloat(m[2]);
        }
      }
    }

    if (Object.keys(stageMultipliers).length > 0) {
      result.stageOverrides = result.stageOverrides ?? {};
      for (const [stage, multiplier] of Object.entries(stageMultipliers)) {
        const defaults = DEFAULT_SIZE_AWARE_BUDGETS[stage];
        if (defaults) {
          result.stageOverrides[stage] = {
            XS: parseFloat((defaults.XS * multiplier).toFixed(2)),
            S: parseFloat((defaults.S * multiplier).toFixed(2)),
            M: parseFloat((defaults.M * multiplier).toFixed(2)),
            L: parseFloat((defaults.L * multiplier).toFixed(2)),
            XL: parseFloat((defaults.XL * multiplier).toFixed(2)),
          };
        }
      }
    }

    return result;
  } catch (error) {
    console.error("Failed to read budget enforcement config from nightgauge config:", error);
    return { mode, gracePercent };
  }
}

// ============================================================================
// Output Token Limit Overrides (Issue #842)
// ============================================================================

/**
 * Get output token limit overrides from config file.
 *
 * Reads pipeline.output_token_limits from config.yaml.
 * Returns undefined if no overrides are configured.
 *
 * Environment override per stage:
 * - NIGHTGAUGE_PIPELINE_OUTPUT_TOKEN_LIMIT_{STAGE_UPPER}
 *   (e.g., NIGHTGAUGE_PIPELINE_OUTPUT_TOKEN_LIMIT_FEATURE_DEV=80000)
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Output token limit overrides or undefined
 *
 * @see Issue #842 - Cap feature-dev output tokens
 */
export function getOutputTokenLimitOverrides(
  workspaceRoot?: string
): Record<string, number | Partial<Record<SizeLabel, number>>> | undefined {
  const overrides: Record<string, number | Partial<Record<SizeLabel, number>>> = {};
  let hasOverrides = false;

  // Check environment variable overrides per stage
  const stages = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ];
  for (const stage of stages) {
    const envKey = `NIGHTGAUGE_PIPELINE_OUTPUT_TOKEN_LIMIT_${stage.toUpperCase().replace(/-/g, "_")}`;
    const envValue = process.env[envKey];
    if (envValue) {
      const parsed = Number.parseInt(envValue, 10);
      if (!Number.isNaN(parsed) && parsed > 0) {
        overrides[stage] = parsed;
        hasOverrides = true;
      }
    }
  }

  // Check config file for per-stage overrides
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    try {
      const pathResult = resolveConfigPathSync(root);
      if (pathResult.exists) {
        if (pathResult.isLegacy) {
          logDeprecationWarning(pathResult.path);
        }

        const configContent = readEffectiveConfigTextSync(pathResult);
        const lines = configContent.split("\n");
        let inPipeline = false;
        let inOutputTokenLimits = false;
        let inTargetStage = "";

        for (const line of lines) {
          const trimmed = line.trim();

          if (trimmed === "pipeline:") {
            inPipeline = true;
            continue;
          }

          if (inPipeline && trimmed === "output_token_limits:") {
            inOutputTokenLimits = true;
            continue;
          }

          // Detect section exit
          if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
            if (!line.startsWith(" ")) {
              inPipeline = false;
              inOutputTokenLimits = false;
              inTargetStage = "";
            }
          }

          if (inOutputTokenLimits) {
            // Flat number: "feature-dev: 80000"
            const flatMatch = trimmed.match(/^([a-z][-a-z]*):\s*(\d+)$/);
            if (flatMatch) {
              const stage = flatMatch[1];
              // Only set if no env var override for this stage
              const envKey = `NIGHTGAUGE_PIPELINE_OUTPUT_TOKEN_LIMIT_${stage.toUpperCase().replace(/-/g, "_")}`;
              if (!process.env[envKey]) {
                const parsed = Number.parseInt(flatMatch[2], 10);
                if (!Number.isNaN(parsed) && parsed > 0) {
                  overrides[stage] = parsed;
                  hasOverrides = true;
                }
              }
              inTargetStage = "";
              continue;
            }
            // Object form: "feature-dev:"
            const stageMatch = trimmed.match(/^([a-z][-a-z]*):$/);
            if (stageMatch) {
              inTargetStage = stageMatch[1];
              continue;
            }
          }

          // Match size keys within a target stage object
          if (inTargetStage) {
            const sizeMatch = trimmed.match(/^(XS|S|M|L|XL):\s*(\d+)$/);
            if (sizeMatch) {
              const envKey = `NIGHTGAUGE_PIPELINE_OUTPUT_TOKEN_LIMIT_${inTargetStage.toUpperCase().replace(/-/g, "_")}`;
              if (!process.env[envKey]) {
                const current = overrides[inTargetStage];
                const sizeObj: Partial<Record<SizeLabel, number>> =
                  typeof current === "object" ? { ...current } : {};
                sizeObj[sizeMatch[1] as SizeLabel] = Number.parseInt(sizeMatch[2], 10);
                overrides[inTargetStage] = sizeObj;
                hasOverrides = true;
              }
            }
          }
        }
      }
    } catch (error) {
      console.error("Failed to read output token limits from nightgauge config:", error);
    }
  }

  return hasOverrides ? overrides : undefined;
}

// ============================================================================
// Context Budget Configuration (Issue #790)
// ============================================================================

/**
 * Context budget configuration returned by getContextBudgetConfig().
 *
 * @see Issue #790 - Per-stage context budgets
 */
export interface ContextBudgetConfig {
  enabled: boolean;
  mode: BudgetMode;
  gracePercent: number;
  stageOverrides?: Record<string, number | Partial<Record<SizeLabel, number>>>;
}

/**
 * Get context budget configuration from config or environment.
 *
 * Reads pipeline.context_budgets from config.yaml.
 * Default: enabled=true, mode='soft', grace_percent=50.
 *
 * Environment overrides:
 * - NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_ENABLED=false
 * - NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_MODE=hard|soft|threshold
 * - NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_GRACE_PERCENT=50
 * - NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_LIMIT_{STAGE_UPPER}=250000
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Context budget configuration
 *
 * @see Issue #790 - Per-stage context budgets
 */
export function getContextBudgetConfig(workspaceRoot?: string): ContextBudgetConfig {
  const validModes: BudgetMode[] = ["hard", "soft", "threshold"];
  let enabled = true;
  let mode: BudgetMode = "soft";
  let gracePercent = 50;
  const stageOverrides: Record<string, number | Partial<Record<SizeLabel, number>>> = {};
  let hasStageOverrides = false;

  // Check environment variables first
  const envEnabled = process.env.NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_ENABLED;
  if (envEnabled === "false") {
    enabled = false;
  }

  const envMode = process.env.NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_MODE;
  if (envMode && validModes.includes(envMode as BudgetMode)) {
    mode = envMode as BudgetMode;
  }

  const envGrace = process.env.NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_GRACE_PERCENT;
  if (envGrace) {
    const parsed = Number.parseFloat(envGrace);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 500) {
      gracePercent = parsed;
    }
  }

  // Per-stage env overrides
  const stages = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ];
  for (const stage of stages) {
    const envKey = `NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_LIMIT_${stage.toUpperCase().replace(/-/g, "_")}`;
    const envValue = process.env[envKey];
    if (envValue) {
      const parsed = Number.parseInt(envValue, 10);
      if (!Number.isNaN(parsed) && parsed > 0) {
        stageOverrides[stage] = parsed;
        hasStageOverrides = true;
      }
    }
  }

  // Check config file
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    try {
      const pathResult = resolveConfigPathSync(root);
      if (pathResult.exists) {
        if (pathResult.isLegacy) {
          logDeprecationWarning(pathResult.path);
        }

        const configContent = readEffectiveConfigTextSync(pathResult);
        const lines = configContent.split("\n");
        let inPipeline = false;
        let inContextBudgets = false;
        let inStageLimits = false;
        let inTargetStage = "";

        for (const line of lines) {
          const trimmed = line.trim();

          if (trimmed === "pipeline:") {
            inPipeline = true;
            inContextBudgets = false;
            inStageLimits = false;
            inTargetStage = "";
            continue;
          }

          // Exit pipeline section on next top-level key
          if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
            if (!line.startsWith(" ")) {
              inPipeline = false;
              inContextBudgets = false;
              inStageLimits = false;
              inTargetStage = "";
            }
          }

          if (inPipeline) {
            if (trimmed === "context_budgets:") {
              inContextBudgets = true;
              inStageLimits = false;
              inTargetStage = "";
              continue;
            }

            // Exit context_budgets on sibling pipeline key
            if (
              inContextBudgets &&
              /^[a-z_]+:/.test(trimmed) &&
              trimmed !== "context_budgets:" &&
              trimmed !== "stage_limits:" &&
              !trimmed.startsWith("enabled:") &&
              !trimmed.startsWith("mode:") &&
              !trimmed.startsWith("grace_percent:")
            ) {
              // Check indentation — if at pipeline level, exit context_budgets
              const indent = line.length - line.trimStart().length;
              if (indent <= 2) {
                inContextBudgets = false;
                inStageLimits = false;
                inTargetStage = "";
              }
            }

            if (inContextBudgets) {
              if (!envEnabled) {
                const enabledMatch = trimmed.match(/^enabled:\s*(true|false)$/);
                if (enabledMatch) {
                  enabled = enabledMatch[1] === "true";
                }
              }

              if (!envMode) {
                const modeMatch = trimmed.match(
                  /^mode:\s*['"]?(hard|soft|threshold)['"]?(?:\s+#.*)?$/
                );
                if (modeMatch) {
                  mode = modeMatch[1] as BudgetMode;
                }
              }

              if (!envGrace) {
                const graceMatch = trimmed.match(/^grace_percent:\s*([\d.]+)$/);
                if (graceMatch) {
                  const parsed = Number.parseFloat(graceMatch[1]);
                  if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 500) {
                    gracePercent = parsed;
                  }
                }
              }

              if (trimmed === "stage_limits:") {
                inStageLimits = true;
                inTargetStage = "";
                continue;
              }

              if (inStageLimits) {
                // Flat form: "feature-dev: 250000"
                const flatMatch = trimmed.match(/^([a-z][-a-z]*):\s*(\d+)$/);
                if (flatMatch && !flatMatch[0].match(/^(XS|S|M|L|XL):/)) {
                  const envKey = `NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_LIMIT_${flatMatch[1].toUpperCase().replace(/-/g, "_")}`;
                  if (!process.env[envKey]) {
                    stageOverrides[flatMatch[1]] = Number.parseInt(flatMatch[2], 10);
                    hasStageOverrides = true;
                  }
                  inTargetStage = "";
                  continue;
                }
                // Object form: "feature-dev:"
                const stageMatch = trimmed.match(/^([a-z][-a-z]*):$/);
                if (stageMatch) {
                  inTargetStage = stageMatch[1];
                  continue;
                }
              }

              // Match size keys within a target stage object
              if (inTargetStage) {
                const sizeMatch = trimmed.match(/^(XS|S|M|L|XL):\s*(\d+)$/);
                if (sizeMatch) {
                  const envKey = `NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_LIMIT_${inTargetStage.toUpperCase().replace(/-/g, "_")}`;
                  if (!process.env[envKey]) {
                    const current = stageOverrides[inTargetStage];
                    const sizeObj: Partial<Record<SizeLabel, number>> =
                      typeof current === "object" ? { ...current } : {};
                    sizeObj[sizeMatch[1] as SizeLabel] = Number.parseInt(sizeMatch[2], 10);
                    stageOverrides[inTargetStage] = sizeObj;
                    hasStageOverrides = true;
                  }
                }
              }
            }
          }
        }
      }
    } catch (error) {
      console.error("Failed to read context budget config from nightgauge config:", error);
    }
  }

  return {
    enabled,
    mode,
    gracePercent,
    stageOverrides: hasStageOverrides ? stageOverrides : undefined,
  };
}

// ============================================================================
// Pipeline Budget Ceiling Configuration (Issue #1047)
// ============================================================================

const DEFAULT_PIPELINE_CEILING_CONFIG: PipelineCeilingConfig = {
  enabled: true,
  // Issue #3542 raised 50 → 150; the maintainer has since set the ceiling to
  // $75. $50 remains the warn-only threshold below.
  ceilingUsd: 75,
  warnThresholdUsd: 50,
  warningThresholdPercent: 70,
  checkpointThresholdPercent: 85,
};

/**
 * Get pipeline budget ceiling configuration.
 *
 * Reads from env vars → config.yaml → defaults.
 *
 * @see Issue #1047 - Configurable token budget ceiling
 */
export function getPipelineCeilingConfig(workspaceRoot?: string): PipelineCeilingConfig {
  const config: PipelineCeilingConfig = { ...DEFAULT_PIPELINE_CEILING_CONFIG };

  // Environment variable overrides
  if (process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_ENABLED !== undefined) {
    config.enabled = process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_ENABLED !== "false";
  }
  if (process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD) {
    const parsed = Number.parseFloat(
      process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD
    );
    if (!Number.isNaN(parsed) && parsed >= 0) {
      config.ceilingUsd = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_WARNING_THRESHOLD_PERCENT) {
    const parsed = Number.parseFloat(
      process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_WARNING_THRESHOLD_PERCENT
    );
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 100) {
      config.warningThresholdPercent = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CHECKPOINT_THRESHOLD_PERCENT) {
    const parsed = Number.parseFloat(
      process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CHECKPOINT_THRESHOLD_PERCENT
    );
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 100) {
      config.checkpointThresholdPercent = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_OVERRIDE_CEILING_USD) {
    const parsed = Number.parseFloat(
      process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_OVERRIDE_CEILING_USD
    );
    if (!Number.isNaN(parsed) && parsed >= 0) {
      config.overrideCeilingUsd = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_WARN_THRESHOLD_USD) {
    const parsed = Number.parseFloat(
      process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_WARN_THRESHOLD_USD
    );
    if (!Number.isNaN(parsed) && parsed >= 0) {
      config.warnThresholdUsd = parsed;
    }
  }

  // If any env vars set, return early
  if (
    process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_ENABLED !== undefined ||
    process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD ||
    process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_OVERRIDE_CEILING_USD ||
    process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_WARN_THRESHOLD_USD
  ) {
    return config;
  }

  // Read from config.yaml
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return config;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return config;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inCeiling = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "token_budget_ceiling:") {
        inCeiling = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inCeiling = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && trimmed !== "token_budget_ceiling:") {
          inCeiling = false;
        }
      }

      if (inCeiling) {
        const match = trimmed.match(/^([a-z_]+):\s*(.+)$/);
        if (match) {
          const [, key, value] = match;
          switch (key) {
            case "enabled":
              config.enabled = value === "true";
              break;
            case "ceiling_usd": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0) {
                config.ceilingUsd = parsed;
              }
              break;
            }
            case "warn_threshold_usd": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0) {
                config.warnThresholdUsd = parsed;
              }
              break;
            }
            case "warning_threshold_percent": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 100) {
                config.warningThresholdPercent = parsed;
              }
              break;
            }
            case "checkpoint_threshold_percent": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 100) {
                config.checkpointThresholdPercent = parsed;
              }
              break;
            }
            case "override_ceiling_usd": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0) {
                config.overrideCeilingUsd = parsed;
              }
              break;
            }
          }
        }
      }
    }
  } catch {
    // Non-critical: fall through to defaults
  }

  return config;
}

// ============================================================================
// Max Backtracks (Issue #1342)
// ============================================================================

/**
 * Get the maximum number of backtracks allowed per pipeline run.
 *
 * Reads from:
 * 1. Environment variable: NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS
 * 2. Config file: pipeline.max_backtracks
 * 3. Default: 1
 *
 * Set to 0 to completely disable backtracking.
 *
 * @param workspaceRoot - Optional workspace root
 * @returns Maximum backtracks (0-5)
 *
 * @see Issue #1342 - Orchestrator Backtrack Engine
 */
export function getMaxBacktracks(workspaceRoot?: string): number {
  const DEFAULT_MAX_BACKTRACKS = 1;

  // Check environment variable override first
  if (process.env.NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS) {
    const parsed = Number.parseInt(process.env.NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS, 10);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 5) {
      return parsed;
    }
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_MAX_BACKTRACKS;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_MAX_BACKTRACKS;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      // Exit pipeline section on new top-level key
      if (
        inPipeline &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inPipeline = false;
        continue;
      }

      if (inPipeline) {
        const match = trimmed.match(/^max_backtracks:\s*(\d+)/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 5) {
            return parsed;
          }
        }
      }
    }
  } catch {
    // Non-critical: fall through to default
  }

  return DEFAULT_MAX_BACKTRACKS;
}

// ============================================================================
// Max Escalations Per Stage (Issue #1343)
// ============================================================================

/**
 * Get the maximum number of model escalations allowed per stage per pipeline run.
 *
 * Resolution order:
 * 1. Env var: NIGHTGAUGE_PIPELINE_MAX_ESCALATIONS_PER_STAGE
 * 2. Config file: model_routing.max_escalations_per_stage
 * 3. Default: 1
 *
 * @see Issue #1343 - Dynamic Model Escalation Engine
 */
export function getMaxEscalationsPerStage(workspaceRoot?: string): number {
  const DEFAULT_MAX_ESCALATIONS = 1;

  // Check environment variable override first
  if (process.env.NIGHTGAUGE_PIPELINE_MAX_ESCALATIONS_PER_STAGE) {
    const parsed = Number.parseInt(process.env.NIGHTGAUGE_PIPELINE_MAX_ESCALATIONS_PER_STAGE, 10);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 3) {
      return parsed;
    }
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_MAX_ESCALATIONS;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_MAX_ESCALATIONS;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inModelRouting = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }

      // Exit model_routing section on new top-level key
      if (
        inModelRouting &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inModelRouting = false;
        continue;
      }

      if (inModelRouting) {
        const match = trimmed.match(/^max_escalations_per_stage:\s*(\d+)/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 3) {
            return parsed;
          }
        }
      }
    }
  } catch {
    // Non-critical: fall through to default
  }

  return DEFAULT_MAX_ESCALATIONS;
}

// ============================================================================
// Epic Merge Configuration (Issue #1617)
// ============================================================================

/**
 * Epic PR auto-merge configuration
 *
 * @see Issue #1617 - Auto-merge epic PR to main when all sub-issues complete
 */
export interface EpicMergeConfig {
  autoMergeEpic: boolean;
  mergeStrategy: "squash" | "merge" | "rebase";
  deleteBranch: boolean;
}

export const DEFAULT_EPIC_MERGE_CONFIG: EpicMergeConfig = {
  autoMergeEpic: true,
  mergeStrategy: "merge",
  deleteBranch: true,
};

export function getEpicMergeConfig(workspaceRoot?: string): EpicMergeConfig {
  const config: EpicMergeConfig = { ...DEFAULT_EPIC_MERGE_CONFIG };

  // Check environment variable override
  if (process.env.NIGHTGAUGE_PR_AUTO_MERGE_EPIC !== undefined) {
    const val = process.env.NIGHTGAUGE_PR_AUTO_MERGE_EPIC.toLowerCase();
    if (["true", "yes", "1", "on"].includes(val)) config.autoMergeEpic = true;
    if (["false", "no", "0", "off"].includes(val)) config.autoMergeEpic = false;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return config;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return config;

    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const content = readEffectiveConfigTextSync(pathResult);
    const lines = content.split("\n");
    let inPr = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect pr: or pull_request: section
      if (/^(pr|pull_request):/.test(trimmed)) {
        inPr = true;
        continue;
      }

      // Exit pr section on next top-level key
      if (
        inPr &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inPr = false;
        continue;
      }

      if (inPr) {
        if (trimmed.includes("auto_merge_epic:")) {
          config.autoMergeEpic = trimmed.includes("true");
        } else if (trimmed.includes("epic_merge_strategy:")) {
          const val = trimmed.split(":")[1]?.trim().replace(/['"]/g, "");
          if (val === "squash" || val === "merge" || val === "rebase") {
            config.mergeStrategy = val;
          }
        } else if (trimmed.includes("delete_branch:")) {
          config.deleteBranch = trimmed.includes("true");
        }
      }
    }
  } catch {
    // Config read failure is non-fatal — use defaults
  }

  return config;
}

// ============================================================================
// Concurrent Pipeline Configuration (Issue #1621)
// ============================================================================

/**
 * Concurrent pipeline execution configuration
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */
export interface ConcurrentPipelineConfig {
  maxConcurrent: number;
  worktreeBase: string;
}

/**
 * Parse `concurrency.workspace_max` (the canonical workspace-wide ceiling,
 * #3781) from raw config YAML. Returns the integer when present and in
 * [min,max], else undefined. Intentionally a small line scanner (no YAML dep)
 * to match the existing parseMaxConcurrentBlocks approach.
 */
export function parseConcurrencyWorkspaceMax(
  content: string,
  min: number,
  max: number
): number | undefined {
  const lines = content.split("\n");
  let inBlock = false;
  for (const line of lines) {
    const trimmed = line.trim();
    if (trimmed === "concurrency:") {
      inBlock = true;
      continue;
    }
    if (inBlock) {
      // Left the block at the next top-level (non-indented) key.
      if (trimmed && !line.startsWith(" ") && !line.startsWith("\t")) break;
      const m = trimmed.match(/^workspace_max:\s*(\d+)/);
      if (m) {
        const n = Number.parseInt(m[1], 10);
        if (!Number.isNaN(n) && n >= min && n <= max) return n;
        return undefined;
      }
    }
  }
  return undefined;
}

/**
 * Get concurrent pipeline execution config from config or environment.
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_PIPELINE_MAX_CONCURRENT
 * 2. Config file: `pipeline.max_concurrent` (the unified source of truth)
 * 3. Config file: `autonomous.max_concurrent` (deprecated legacy fallback —
 *    logs a one-time warning when used)
 * 4. Default: 3
 *
 * The legacy fallback exists so configs predating PR #3187 keep working
 * unchanged; the startup migration in `extension.ts` prompts the user to
 * consolidate. See Issue #3195.
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 * @see Issue #3195 - Unify pipeline.max_concurrent as single source of truth
 */
export function getConcurrentPipelineConfig(workspaceRoot?: string): ConcurrentPipelineConfig {
  const DEFAULT_MAX_CONCURRENT = 3;
  const DEFAULT_WORKTREE_BASE = ".worktrees";
  const RANGE_MIN = 1;
  const RANGE_MAX = 10;

  let maxConcurrent = DEFAULT_MAX_CONCURRENT;
  let worktreeBase = DEFAULT_WORKTREE_BASE;

  // Environment variable override — wins over both YAML blocks. An invalid
  // env value (non-numeric, out-of-range) is treated as if the env var were
  // unset, so the config file still gets a chance to set the value.
  const envRaw = process.env.NIGHTGAUGE_PIPELINE_MAX_CONCURRENT;
  let envClaimed = false;
  if (envRaw) {
    const parsed = Number.parseInt(envRaw, 10);
    if (!Number.isNaN(parsed) && parsed >= RANGE_MIN && parsed <= RANGE_MAX) {
      maxConcurrent = parsed;
      envClaimed = true;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return { maxConcurrent, worktreeBase };
  }

  try {
    const resolved = resolveConfigPathSync(root);
    if (!resolved) {
      return { maxConcurrent, worktreeBase };
    }
    const content = readEffectiveConfigTextSync(resolved);
    const parsed = parseMaxConcurrentBlocks(content, RANGE_MIN, RANGE_MAX);
    worktreeBase = parsed.worktreeBase ?? worktreeBase;

    // concurrency.workspace_max is the canonical workspace-wide ceiling
    // (#3781). It is the single source of truth; nothing else is consulted.
    if (!envClaimed) {
      const wsMax = parseConcurrencyWorkspaceMax(content, RANGE_MIN, RANGE_MAX);
      if (wsMax !== undefined) {
        maxConcurrent = wsMax;
      }
    }
  } catch {
    // Config read failure is non-fatal
  }

  return { maxConcurrent, worktreeBase };
}

/**
 * Single-pass YAML scan that extracts the top-level `max_concurrent` value
 * from both the `pipeline:` and `autonomous:` blocks (plus `worktree_base`
 * from `pipeline:`). Hand-rolled instead of routing through a YAML parser
 * because this lives on the synchronous extension-startup path and the
 * project intentionally keeps config reads dependency-free.
 *
 * Exported for direct unit testing.
 */
export function parseMaxConcurrentBlocks(
  content: string,
  rangeMin: number,
  rangeMax: number
): {
  pipelineMaxConcurrent: number | undefined;
  autonomousMaxConcurrent: number | undefined;
  worktreeBase: string | undefined;
} {
  let pipelineMaxConcurrent: number | undefined;
  let autonomousMaxConcurrent: number | undefined;
  let worktreeBase: string | undefined;

  const lines = content.split("\n");
  let block: "pipeline" | "autonomous" | null = null;

  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) {
      continue;
    }

    // Block headers (top-level keys, no leading whitespace).
    if (!line.startsWith(" ") && !line.startsWith("\t")) {
      if (trimmed === "pipeline:") {
        block = "pipeline";
        continue;
      }
      if (trimmed === "autonomous:") {
        block = "autonomous";
        continue;
      }
      block = null;
      continue;
    }

    if (block === null) continue;

    // Only direct children of the block — not nested keys.
    const indent = line.length - line.trimStart().length;
    if (indent !== 2) continue;

    const concurrentMatch = trimmed.match(/^max_concurrent:\s*(\d+)/);
    if (concurrentMatch) {
      const parsed = Number.parseInt(concurrentMatch[1], 10);
      if (!Number.isNaN(parsed) && parsed >= rangeMin && parsed <= rangeMax) {
        if (block === "pipeline") {
          pipelineMaxConcurrent = parsed;
        } else if (block === "autonomous") {
          autonomousMaxConcurrent = parsed;
        }
      }
      continue;
    }

    if (block === "pipeline") {
      const baseMatch = trimmed.match(/^worktree_base:\s*['"]?([^'"]+)['"]?/);
      if (baseMatch) {
        worktreeBase = baseMatch[1].trim();
      }
    }
  }

  return { pipelineMaxConcurrent, autonomousMaxConcurrent, worktreeBase };
}

// ============================================================================
// Epic Queue Filter Configuration (Issue #2992)
// ============================================================================

/**
 * Drag-to-queue epic sub-issue filter configuration.
 *
 * Only applies to the drag path in `IssueDragAndDropController`. Autonomous
 * scheduling already respects board status via
 * `ProjectV2.items(query: "status:...")`, so the filter is not applied there.
 *
 * @see Issue #2992
 */
export interface EpicQueueFilterConfig {
  /** Statuses that remain pickup-eligible when an epic is dragged. Default `["Ready"]`. */
  eligibleStatuses: string[];
  /** Skip sub-issues that already have an open PR. Default `true`. */
  skipIssuesWithOpenPR: boolean;
}

export const DEFAULT_EPIC_QUEUE_FILTER_CONFIG: EpicQueueFilterConfig = {
  eligibleStatuses: ["Ready"],
  skipIssuesWithOpenPR: true,
};

/**
 * Read `pipeline.epic_queue_filter` from `.nightgauge/config.yaml`.
 *
 * This is a small, read-only parser — matching the deliberately lightweight
 * pattern used by `getConcurrentPipelineConfig` above. Parsing failures fall
 * back to the defaults so a malformed config never blocks drag-to-queue.
 */
export function getEpicQueueFilterConfig(workspaceRoot?: string): EpicQueueFilterConfig {
  const config: EpicQueueFilterConfig = {
    eligibleStatuses: [...DEFAULT_EPIC_QUEUE_FILTER_CONFIG.eligibleStatuses],
    skipIssuesWithOpenPR: DEFAULT_EPIC_QUEUE_FILTER_CONFIG.skipIssuesWithOpenPR,
  };

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return config;

  try {
    const resolved = resolveConfigPathSync(root);
    if (!resolved) return config;
    const content = readEffectiveConfigTextSync(resolved);
    const lines = content.split("\n");

    let inPipeline = false;
    let inFilter = false;
    let inEligibleList = false;
    const eligible: string[] = [];

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        inFilter = false;
        inEligibleList = false;
        continue;
      }

      // Leave the pipeline block on any top-level key.
      if (inPipeline && trimmed && !trimmed.startsWith("#") && !line.startsWith(" ")) {
        inPipeline = false;
        inFilter = false;
        inEligibleList = false;
      }

      if (!inPipeline) continue;

      if (trimmed === "epic_queue_filter:") {
        inFilter = true;
        inEligibleList = false;
        continue;
      }

      // Leave the filter block when a non-indented-enough key appears.
      if (inFilter && trimmed && !line.startsWith("    ")) {
        inFilter = false;
        inEligibleList = false;
      }

      if (!inFilter) continue;

      if (trimmed.startsWith("eligible_statuses:")) {
        const inline = trimmed.match(/^eligible_statuses:\s*\[(.*)\]/);
        if (inline) {
          const items = inline[1]
            .split(",")
            .map((s) => s.trim().replace(/^['"]|['"]$/g, ""))
            .filter(Boolean);
          if (items.length > 0) config.eligibleStatuses = items;
          inEligibleList = false;
        } else {
          inEligibleList = true;
          eligible.length = 0;
        }
        continue;
      }

      if (inEligibleList) {
        const itemMatch = trimmed.match(/^-\s*['"]?([^'"#]+?)['"]?\s*$/);
        if (itemMatch) {
          eligible.push(itemMatch[1].trim());
          continue;
        }
        // Any non-list line ends the list
        if (eligible.length > 0) {
          config.eligibleStatuses = eligible.slice();
        }
        inEligibleList = false;
      }

      const skipMatch = trimmed.match(/^skip_issues_with_open_pr:\s*(true|false)/i);
      if (skipMatch) {
        config.skipIssuesWithOpenPR = skipMatch[1].toLowerCase() === "true";
      }
    }

    if (inEligibleList && eligible.length > 0) {
      config.eligibleStatuses = eligible.slice();
    }
  } catch {
    // Malformed config — fall back to defaults.
  }

  return config;
}

// ============================================================================
// Context Schema Repair Configuration (Issue #2552)
// ============================================================================

/**
 * Context schema repair configuration.
 *
 * When enabled, the pipeline will attempt to re-invoke a stage with
 * Zod error details appended to the prompt when context file validation
 * fails. If the repair attempt produces a valid context file, the pipeline
 * continues normally. If repair fails, the pipeline falls back to the
 * existing warn-and-continue behavior.
 *
 * @see Issue #2552 - Pipeline context schema self-correction
 */
export interface ContextSchemaRepairConfig {
  /** Whether context schema repair is enabled (default: false) */
  enabled: boolean;
  /** Maximum repair attempts per stage per pipeline run (default: 1, range: 0-5) */
  max_attempts: number;
}

/**
 * Default context schema repair configuration.
 * Repair is disabled by default to avoid unexpected token costs.
 */
export const DEFAULT_CONTEXT_SCHEMA_REPAIR_CONFIG: ContextSchemaRepairConfig = {
  enabled: false,
  max_attempts: 1,
};

/**
 * Read context schema repair configuration from .nightgauge/config.yaml.
 *
 * Parses `pipeline.context_schema_repair` block:
 * ```yaml
 * pipeline:
 *   context_schema_repair:
 *     enabled: false
 *     max_attempts: 1
 * ```
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Context schema repair configuration
 *
 * @see Issue #2552 - Pipeline context schema self-correction
 */
export function getContextSchemaRepairConfig(workspaceRoot?: string): ContextSchemaRepairConfig {
  const config: ContextSchemaRepairConfig = { ...DEFAULT_CONTEXT_SCHEMA_REPAIR_CONFIG };

  // Environment variable override
  if (process.env.NIGHTGAUGE_CONTEXT_SCHEMA_REPAIR_ENABLED === "true") {
    config.enabled = true;
  } else if (process.env.NIGHTGAUGE_CONTEXT_SCHEMA_REPAIR_ENABLED === "false") {
    config.enabled = false;
  }
  if (process.env.NIGHTGAUGE_CONTEXT_SCHEMA_REPAIR_MAX_ATTEMPTS) {
    const parsed = Number.parseInt(process.env.NIGHTGAUGE_CONTEXT_SCHEMA_REPAIR_MAX_ATTEMPTS, 10);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 5) {
      config.max_attempts = parsed;
    }
  }

  // Read from config.yaml
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return config;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return config;

    const content = readEffectiveConfigTextSync(pathResult);
    const lines = content.split("\n");
    let inPipeline = false;
    let inRepair = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect top-level `pipeline:` block
      if (trimmed === "pipeline:" || trimmed.startsWith("pipeline:")) {
        if (!line.startsWith(" ")) {
          inPipeline = true;
          inRepair = false;
          continue;
        }
      }

      // Detect end of pipeline block (next top-level key)
      if (
        inPipeline &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inPipeline = false;
        inRepair = false;
        continue;
      }

      if (inPipeline) {
        if (trimmed === "context_schema_repair:" || trimmed.startsWith("context_schema_repair:")) {
          inRepair = true;
          continue;
        }

        // Detect end of repair sub-block (next pipeline sub-key at same indent level)
        if (inRepair && trimmed && !trimmed.startsWith("#")) {
          // Check if this is a sibling key (indented at pipeline level, not repair level)
          const indent = line.length - line.trimStart().length;
          if (indent <= 2 && /^[a-z_]+:/.test(trimmed)) {
            inRepair = false;
            // Don't continue — might be another pipeline key we need to process
          }
        }

        if (inRepair) {
          if (trimmed.startsWith("enabled:")) {
            // env var override takes precedence
            if (!process.env.NIGHTGAUGE_CONTEXT_SCHEMA_REPAIR_ENABLED) {
              config.enabled = trimmed.includes("true");
            }
          } else if (trimmed.startsWith("max_attempts:")) {
            if (!process.env.NIGHTGAUGE_CONTEXT_SCHEMA_REPAIR_MAX_ATTEMPTS) {
              const val = trimmed.split(":")[1]?.trim();
              if (val) {
                const parsed = Number.parseInt(val, 10);
                if (!Number.isNaN(parsed)) {
                  // Clamp to 0-5 range
                  config.max_attempts = Math.max(0, Math.min(5, parsed));
                }
              }
            }
          }
        }
      }
    }
  } catch {
    // Config read failure is non-fatal — return defaults
  }

  return config;
}

// ============================================================================
// Adapter Auth Pre-Flight Skip Flag (Issue #3222)
// ============================================================================

/**
 * Read `pipeline.skip_auth_preflight` from `.nightgauge/config.yaml`.
 *
 * Defaults to `false` — the pre-flight runs unless explicitly disabled.
 * Returns `true` only when the YAML key resolves to a literal `true`.
 *
 * @see Issue #3222 - validateAdapterAuth pre-flight checker per adapter
 */
export function getSkipAuthPreflight(workspaceRoot?: string): boolean {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return false;

  try {
    const resolved = resolveConfigPathSync(root);
    if (!resolved) return false;
    const content = readEffectiveConfigTextSync(resolved);
    const lines = content.split("\n");

    let inPipeline = false;
    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed && !trimmed.startsWith("#") && !line.startsWith(" ")) {
        inPipeline = false;
      }

      if (!inPipeline) continue;

      const match = trimmed.match(/^skip_auth_preflight:\s*(true|false)\b/i);
      if (match) {
        return match[1].toLowerCase() === "true";
      }
    }
  } catch {
    // Malformed config — fall back to default (probe enabled).
  }

  return false;
}

/**
 * Read `pipeline.adaptive_budget` from `.nightgauge/config.yaml`.
 *
 * Defaults to `true` — adaptive budgets are enabled unless explicitly set to
 * `false`. Returns `false` only when the YAML key resolves to a literal `false`.
 *
 * @env NIGHTGAUGE_PIPELINE_ADAPTIVE_BUDGET
 * @see Issue #3667 — Adaptive per-repo stage budgets
 */
export function isAdaptiveBudgetEnabled(workspaceRoot?: string): boolean {
  // Environment variable override takes highest priority.
  const envVal = process.env.NIGHTGAUGE_PIPELINE_ADAPTIVE_BUDGET;
  if (envVal === "false") return false;
  if (envVal === "true") return true;

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return true; // default: enabled

  try {
    const resolved = resolveConfigPathSync(root);
    if (!resolved?.exists) return true;

    if (resolved.isLegacy) {
      logDeprecationWarning(resolved.path);
    }

    const content = readEffectiveConfigTextSync(resolved);
    const lines = content.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }
      if (inPipeline) {
        if (trimmed && !line.startsWith(" ") && !line.startsWith("\t")) {
          inPipeline = false;
          continue;
        }
        const match = trimmed.match(/^adaptive_budget:\s*(true|false)\b/i);
        if (match) {
          return match[1].toLowerCase() !== "false";
        }
      }
    }
  } catch {
    // Malformed config — fall back to default (enabled).
  }

  return true;
}
