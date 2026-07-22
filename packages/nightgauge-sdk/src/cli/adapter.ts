/**
 * Adapter resolution and type re-exports.
 *
 * The canonical IncrediAdapter type now lives in adapters/ICliAdapter.ts.
 * This module re-exports it and provides the resolution logic.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 */

import { defaultRegistry } from "./adapters/AdapterRegistry.js";
import { AdapterError } from "./adapters/errors.js";
import { readAdapterFileConfig } from "./adapterConfig.js";

// Re-export the canonical type from the adapters module
export type { IncrediAdapter } from "./adapters/ICliAdapter.js";
import type { IncrediAdapter } from "./adapters/ICliAdapter.js";

const ADAPTER_ALIASES: Record<string, IncrediAdapter> = {
  claude: "claude-sdk",
  "claude-sdk": "claude-sdk",
  "claude-headless": "claude-headless",
  codex: "codex",
  gemini: "gemini",
  "gemini-headless": "gemini",
  "gemini-sdk": "gemini-sdk",
  "lm-studio": "lm-studio",
  lm_studio: "lm-studio",
  // Issue #2591 — Ollama local LLM inference (alias was missing pre-#53:
  // NIGHTGAUGE_ADAPTER=ollama silently resolved to claude-sdk)
  ollama: "ollama",
  // Issue #1941 — GitHub Copilot aliases
  copilot: "copilot",
  github: "copilot",
  gh: "copilot",
};

/** Options for {@link resolveAdapter}'s config-aware rungs (#54). */
export interface ResolveAdapterOptions {
  /** Pipeline stage — enables the per-stage env + pipeline.stage_adapters rungs. */
  stage?: string;
  /** Directory holding .nightgauge/config.yaml. Defaults to process.cwd(). */
  cwd?: string;
}

function aliasOrThrow(value: string, sourceLabel: string): IncrediAdapter {
  const resolved = ADAPTER_ALIASES[value.trim().toLowerCase()];
  if (!resolved) {
    throw new AdapterError(
      `Unknown adapter '${value}' in ${sourceLabel}.\n` +
        `Valid values: ${[...new Set(Object.keys(ADAPTER_ALIASES))].join(", ")}.`,
      "CONFIG_INVALID",
      value
    );
  }
  return resolved;
}

/**
 * Resolve the execution adapter through the canonical precedence chain
 * shared with the Go binary and the VSCode resolver (#54):
 *
 *   1. NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE> env (when a stage is given)
 *   2. NIGHTGAUGE_ADAPTER env (per-invocation override)
 *   3. pipeline.stage_adapters.<stage> config (when a stage is given)
 *   4. ui.core.adapter config
 *   5. API-key auto-select (SDK-CLI-only legacy rung: Gemini > Claude > Copilot)
 *   6. claude-headless
 *
 * Rung 5 is deliberately kept on this layer (the Go binary deleted its
 * equivalent): headless CI invocations of the SDK CLI rely on
 * key-implies-sdk. Config rungs outrank it, so a configured adapter always
 * wins over an incidentally exported key.
 *
 * An explicit adapter name (env or config) that matches no known alias
 * throws instead of silently falling back — pre-#53 a typo quietly ran
 * claude-sdk, surfacing later as a baffling ANTHROPIC_API_KEY error.
 */
export function resolveAdapter(
  env: NodeJS.ProcessEnv = process.env,
  options: ResolveAdapterOptions = {}
): IncrediAdapter {
  if (options.stage) {
    const stageEnvKey = `NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_${options.stage.toUpperCase().replace(/-/g, "_")}`;
    const stageEnv = (env[stageEnvKey] ?? "").trim();
    if (stageEnv) {
      return aliasOrThrow(stageEnv, stageEnvKey);
    }
  }

  const explicit = (env.NIGHTGAUGE_ADAPTER ?? "").trim().toLowerCase();
  if (explicit) {
    return aliasOrThrow(explicit, "NIGHTGAUGE_ADAPTER");
  }

  const fileConfig = readAdapterFileConfig(options.cwd ?? process.cwd());
  if (options.stage) {
    const stageAdapter = (fileConfig.stageAdapters[options.stage] ?? "").trim();
    if (stageAdapter) {
      return aliasOrThrow(stageAdapter, `pipeline.stage_adapters.${options.stage}`);
    }
  }
  if (fileConfig.globalAdapter) {
    return aliasOrThrow(fileConfig.globalAdapter, "ui.core.adapter");
  }

  // Auto-select gemini-sdk when Gemini API key is present
  if (env.GEMINI_API_KEY || env.GOOGLE_API_KEY) {
    return "gemini-sdk";
  }

  if (env.ANTHROPIC_API_KEY) {
    return "claude-sdk";
  }

  // Auto-select copilot when COPILOT_GITHUB_TOKEN is set (Issue #1941)
  if (env.COPILOT_GITHUB_TOKEN) {
    return "copilot";
  }

  return "claude-headless";
}

/**
 * Check if adapter requires a direct API key.
 * Delegates to the registry for the answer.
 */
export function requiresDirectApiKey(adapter: IncrediAdapter): boolean {
  return defaultRegistry.get(adapter).requiresDirectApiKey();
}

export function isCodexAdapterEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveAdapter(env) === "codex";
}

export function isGeminiAdapterEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveAdapter(env) === "gemini";
}

export function isGeminiSdkAdapterEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveAdapter(env) === "gemini-sdk";
}

export function isLmStudioAdapterEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveAdapter(env) === "lm-studio";
}

export function isCopilotAdapterEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveAdapter(env) === "copilot";
}
