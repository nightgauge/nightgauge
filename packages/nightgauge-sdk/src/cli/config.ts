/**
 * CLI Configuration - Environment variable and config file handling
 *
 * Loads configuration from environment variables for CI/CD environments.
 * All secrets (API keys) must be provided via environment variables.
 *
 * @see docs/CI_INTEGRATION.md for complete configuration reference
 */

import type { PipelineConfig } from "../orchestrator/PipelineOrchestrator.js";
import { requiresDirectApiKey, resolveAdapter, type IncrediAdapter } from "./adapter.js";
import {
  DEFAULT_ORCHESTRATION_CONFIG,
  DISABLE_WORKFLOWS_ENV,
  type OrchestrationConfig,
} from "./workflow/OrchestrationConfig.js";

/**
 * CLI-specific configuration options
 */
export interface CLIConfig extends PipelineConfig {
  /** Execution adapter */
  adapter: IncrediAdapter;
  /** Skip approval prompts (for CI) */
  autoApprove: boolean;
  /** Output format */
  outputFormat: "text" | "json";
  /** Log level */
  logLevel: "debug" | "info" | "warn" | "error";
  /** Global timeout in milliseconds */
  globalTimeoutMs: number;
  /** Per-stage timeout in milliseconds */
  stageTimeoutMs: number;
  /** Anthropic API key */
  apiKey: string;
}

/**
 * Default configuration values
 */
export const DEFAULT_CONFIG: Omit<CLIConfig, "apiKey"> = {
  adapter: "claude-sdk",
  autoApprove: false,
  outputFormat: "text",
  logLevel: "info",
  globalTimeoutMs: 3600000, // 1 hour
  stageTimeoutMs: 900000, // 15 minutes
  defaultModel: "sonnet",
  // Multi-agent orchestration is off by default (epic #3899, opt-in). The empty
  // block resolves to DEFAULT_ORCHESTRATION_CONFIG (disabled, no native offload,
  // no budget cap) via resolveOrchestrationConfig. @see Issue #3901
  orchestration: {},
};

/**
 * Parse a boolean environment variable
 */
function parseBoolean(value: string | undefined): boolean | undefined {
  if (value === undefined) return undefined;
  const lower = value.toLowerCase();
  if (lower === "true" || lower === "1" || lower === "yes") return true;
  if (lower === "false" || lower === "0" || lower === "no") return false;
  return undefined;
}

/**
 * Parse a numeric environment variable
 */
function parseNumber(value: string | undefined): number | undefined {
  if (value === undefined) return undefined;
  const num = parseInt(value, 10);
  return isNaN(num) ? undefined : num;
}

/**
 * Validate log level
 */
function parseLogLevel(value: string | undefined): CLIConfig["logLevel"] | undefined {
  if (!value) return undefined;
  const valid = ["debug", "info", "warn", "error"];
  return valid.includes(value) ? (value as CLIConfig["logLevel"]) : undefined;
}

/**
 * Validate output format
 */
function parseOutputFormat(value: string | undefined): CLIConfig["outputFormat"] | undefined {
  if (!value) return undefined;
  return value === "json" ? "json" : value === "text" ? "text" : undefined;
}

/**
 * Validate model
 */
function parseModel(value: string | undefined): "sonnet" | "opus" | "haiku" | undefined {
  if (!value) return undefined;
  const valid = ["sonnet", "opus", "haiku"];
  return valid.includes(value) ? (value as "sonnet" | "opus" | "haiku") : undefined;
}

/**
 * Build the orchestration config block from environment variables (epic #3899).
 * Off by default — only env-set knobs are surfaced; everything else resolves to
 * {@link DEFAULT_ORCHESTRATION_CONFIG} at read time via
 * `resolveOrchestrationConfig`. The `CLAUDE_CODE_DISABLE_WORKFLOWS` kill-switch
 * is honored by the resolver, so it does not need duplicating here.
 *
 * @env CLAUDE_CODE_DISABLE_WORKFLOWS — force-disable (handled by the resolver)
 * @env NIGHTGAUGE_ORCHESTRATION_DISABLED — disable flag
 * @env NIGHTGAUGE_ORCHESTRATION_MAX_USD — total USD budget (0 = uncapped)
 * @env NIGHTGAUGE_ORCHESTRATION_MAX_AGENTS — total agent cap (0 = ceiling)
 * @env NIGHTGAUGE_ORCHESTRATION_MAX_CONCURRENCY — concurrent cap (0 = ceiling)
 * @see Issue #3901
 */
function parseOrchestration(env: NodeJS.ProcessEnv): OrchestrationConfig {
  const config: OrchestrationConfig = {};

  const disabled =
    parseBoolean(env[DISABLE_WORKFLOWS_ENV]) ?? parseBoolean(env.NIGHTGAUGE_ORCHESTRATION_DISABLED);
  if (disabled !== undefined) config.disabled = disabled;

  const maxUsd = parseNumber(env.NIGHTGAUGE_ORCHESTRATION_MAX_USD);
  if (maxUsd !== undefined) config.max_usd = maxUsd;

  const maxAgents = parseNumber(env.NIGHTGAUGE_ORCHESTRATION_MAX_AGENTS);
  if (maxAgents !== undefined) config.max_agents = maxAgents;

  const maxConcurrency = parseNumber(env.NIGHTGAUGE_ORCHESTRATION_MAX_CONCURRENCY);
  if (maxConcurrency !== undefined) config.max_concurrency = maxConcurrency;

  return config;
}

/**
 * Load configuration from environment variables
 *
 * @returns Merged configuration with defaults
 * @throws Error if ANTHROPIC_API_KEY is missing when claude-sdk adapter is used
 */
export function loadConfigFromEnv(env: NodeJS.ProcessEnv = process.env): CLIConfig {
  const adapter = resolveAdapter(env);
  const apiKey = env.ANTHROPIC_API_KEY ?? "";

  if (requiresDirectApiKey(adapter) && !apiKey) {
    throw new Error(
      "ANTHROPIC_API_KEY environment variable is required.\n" +
        "Set it in your CI environment or export it locally:\n" +
        "  export ANTHROPIC_API_KEY=your-api-key"
    );
  }

  return {
    ...DEFAULT_CONFIG,
    adapter,
    apiKey,
    autoApprove: parseBoolean(env.NIGHTGAUGE_AUTO_APPROVE) ?? DEFAULT_CONFIG.autoApprove,
    outputFormat: parseOutputFormat(env.NIGHTGAUGE_OUTPUT_FORMAT) ?? DEFAULT_CONFIG.outputFormat,
    logLevel: parseLogLevel(env.NIGHTGAUGE_LOG_LEVEL) ?? DEFAULT_CONFIG.logLevel,
    globalTimeoutMs: parseNumber(env.NIGHTGAUGE_TIMEOUT) ?? DEFAULT_CONFIG.globalTimeoutMs,
    stageTimeoutMs: parseNumber(env.NIGHTGAUGE_STAGE_TIMEOUT) ?? DEFAULT_CONFIG.stageTimeoutMs,
    defaultModel: parseModel(env.NIGHTGAUGE_MODEL) ?? DEFAULT_CONFIG.defaultModel,
    orchestration: parseOrchestration(env),
  };
}

/**
 * Merge CLI arguments with environment config
 *
 * CLI arguments take precedence over environment variables.
 */
export function mergeConfig(envConfig: CLIConfig, cliArgs: Partial<CLIConfig>): CLIConfig {
  return {
    ...envConfig,
    ...Object.fromEntries(Object.entries(cliArgs).filter(([_, v]) => v !== undefined)),
  } as CLIConfig;
}

/**
 * Configuration validation errors
 */
export class ConfigValidationError extends Error {
  constructor(
    message: string,
    public readonly field: string
  ) {
    super(message);
    this.name = "ConfigValidationError";
  }
}

/**
 * Validate the complete configuration
 *
 * @throws ConfigValidationError if configuration is invalid
 */
export function validateConfig(config: CLIConfig, env: NodeJS.ProcessEnv = process.env): void {
  if (requiresDirectApiKey(resolveAdapter(env)) && !config.apiKey) {
    throw new ConfigValidationError("API key is required", "apiKey");
  }

  if (config.globalTimeoutMs < 0) {
    throw new ConfigValidationError("Global timeout must be non-negative", "globalTimeoutMs");
  }

  if (config.stageTimeoutMs < 0) {
    throw new ConfigValidationError("Stage timeout must be non-negative", "stageTimeoutMs");
  }

  if (config.stageTimeoutMs > config.globalTimeoutMs) {
    throw new ConfigValidationError("Stage timeout cannot exceed global timeout", "stageTimeoutMs");
  }

  validateOrchestrationConfig(config.orchestration);
}

/**
 * Validate the orchestration knobs (epic #3899). Budget/agent/concurrency caps
 * must be non-negative; `0` is the documented "uncapped / use provider ceiling"
 * sentinel. @see Issue #3901
 */
function validateOrchestrationConfig(orchestration: OrchestrationConfig | undefined): void {
  if (!orchestration) return;

  const numericKnobs: Array<[keyof OrchestrationConfig, number | undefined]> = [
    ["max_usd", orchestration.max_usd],
    ["max_agents", orchestration.max_agents],
    ["max_concurrency", orchestration.max_concurrency],
  ];
  for (const [field, value] of numericKnobs) {
    if (value !== undefined && (!Number.isFinite(value) || value < 0)) {
      throw new ConfigValidationError(
        `orchestration.${field} must be a non-negative finite number (0 = uncapped)`,
        `orchestration.${field}`
      );
    }
  }
}

// Surface the resolved default so callers can introspect the off-by-default
// baseline without re-importing the workflow module.
export { DEFAULT_ORCHESTRATION_CONFIG };
