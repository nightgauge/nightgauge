/**
 * Adapter resolution and type re-exports.
 *
 * The canonical NightgaugeAdapter type now lives in adapters/ICliAdapter.ts.
 * This module re-exports it and provides the resolution logic.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 */

import { defaultRegistry } from "./adapters/AdapterRegistry.js";
import { AdapterError } from "./adapters/errors.js";
import { readAdapterFileConfig } from "./adapterConfig.js";

// Re-export the canonical type from the adapters module
export type { NightgaugeAdapter } from "./adapters/ICliAdapter.js";
import type { NightgaugeAdapter } from "./adapters/ICliAdapter.js";
import { retiredAdapterMessage } from "./adapters/retiredAdapters.js";

const ADAPTER_ALIASES: Record<string, NightgaugeAdapter> = {
  claude: "claude-sdk",
  "claude-sdk": "claude-sdk",
  "claude-headless": "claude-headless",
  codex: "codex",
  gemini: "gemini",
  "gemini-headless": "gemini",
  "gemini-sdk": "gemini-sdk",
  // Issue #2128 — the generic OpenAI-compatible judge backend
  "openai-compatible": "openai-compatible",
  // Issue #1941 — GitHub Copilot aliases
  copilot: "copilot",
  github: "copilot",
  gh: "copilot",
  grok: "grok",
  "grok-headless": "grok",
  xai: "grok",
  // Issue #1615 — OpenCode (ADR-022). Resolving it is gated: see
  // EXPERIMENTAL_OPENCODE_ENV_VAR.
  opencode: "opencode",
};

/**
 * The OpenCode enable switch, the same name the Go gate reads
 * (`ExperimentalOpenCodeEnvVar` in internal/execution/adapters/opencode.go).
 *
 * Default off, for security (ADR-020 requires the reason beside a default-off
 * setting): an OpenCode dispatch runs without controls every other adapter
 * has. Only the exact value `1` opens it, and it is read from the process
 * environment only, so a committed repository config can never turn it on.
 * There is deliberately no config-file switch. #1643 removes the gate.
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md § The enable gate
 */
export const EXPERIMENTAL_OPENCODE_ENV_VAR = "NIGHTGAUGE_EXPERIMENTAL_OPENCODE";

function isOpenCodeSwitchOn(env: NodeJS.ProcessEnv): boolean {
  return env[EXPERIMENTAL_OPENCODE_ENV_VAR] === "1";
}

function openCodeGateError(): AdapterError {
  return new AdapterError(
    `Adapter "opencode" is experimental and does not dispatch by default. ` +
      `To use it anyway, set ${EXPERIMENTAL_OPENCODE_ENV_VAR}=1 in the environment that runs ` +
      `nightgauge (it is read from the environment only, so no config file can set it); ` +
      `otherwise choose another adapter with --adapter or NIGHTGAUGE_ADAPTER. ` +
      `See docs/decisions/022-opencode-multi-provider-adapter.md`,
    "CONFIG_INVALID",
    "opencode"
  );
}

/** Options for {@link resolveAdapter}'s config-aware rungs (#54). */
export interface ResolveAdapterOptions {
  /** Pipeline stage — enables the per-stage env + pipeline.stage_adapters rungs. */
  stage?: string;
  /** Directory holding .nightgauge/config.yaml. Defaults to process.cwd(). */
  cwd?: string;
}

function aliasOrThrow(value: string, sourceLabel: string): NightgaugeAdapter {
  const retired = retiredAdapterMessage(value, sourceLabel);
  if (retired) {
    throw new AdapterError(retired, "CONFIG_INVALID", value);
  }
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
 *
 * Whichever rung names `opencode`, resolution throws `CONFIG_INVALID` unless
 * {@link EXPERIMENTAL_OPENCODE_ENV_VAR} is exactly `1` (#1615, ADR-022). The
 * refusal never falls through to a lower rung or to a default adapter.
 */
export function resolveAdapter(
  env: NodeJS.ProcessEnv = process.env,
  options: ResolveAdapterOptions = {}
): NightgaugeAdapter {
  const adapter = resolveAdapterUngated(env, options);
  if (adapter === "opencode" && !isOpenCodeSwitchOn(env)) {
    throw openCodeGateError();
  }
  return adapter;
}

function resolveAdapterUngated(
  env: NodeJS.ProcessEnv,
  options: ResolveAdapterOptions
): NightgaugeAdapter {
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
export function requiresDirectApiKey(adapter: NightgaugeAdapter): boolean {
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

export function isCopilotAdapterEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveAdapter(env) === "copilot";
}

export function isGrokAdapterEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveAdapter(env) === "grok";
}

/**
 * True only when {@link EXPERIMENTAL_OPENCODE_ENV_VAR} is exactly `1` and the
 * adapter resolves to `opencode`. The switch is checked first, so with it off
 * this returns false without throwing. No config file can make it true.
 * #1643 removes the gate.
 */
export function isOpenCodeAdapterEnabled(
  env: NodeJS.ProcessEnv = process.env,
  cwd?: string
): boolean {
  return isOpenCodeSwitchOn(env) && resolveAdapter(env, { cwd }) === "opencode";
}
