/**
 * Core Settings for Nightgauge
 *
 * Provides typed access to core configuration (auth, model, paths) via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #476 - Refactor tree providers, extension.ts, and settings.ts to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import {
  type UICoreConfig,
  type GeminiAuthMethod,
  type GeminiModel,
  type CopilotConfig,
  type LmStudioConfig,
  DEFAULT_CONFIG,
} from "./schema";

/**
 * Authentication provider options
 */
export type AuthProvider = "max" | "bedrock" | "vertex";

/**
 * Execution adapter options (UI-facing).
 *
 * Maps to SDK's IncrediAdapter:
 * - 'claude' → 'claude-sdk' or 'claude-headless'
 * - 'codex'  → 'codex'
 *
 * @see packages/nightgauge-sdk/src/cli/adapters/ICliAdapter.ts
 * @see Issue #627
 */
export type ExecutionAdapter =
  "claude" | "codex" | "gemini" | "gemini-sdk" | "lm-studio" | "ollama" | "copilot";

/**
 * Model selection options
 */
export type ModelSelection = "sonnet" | "opus" | "haiku" | "fable";

/**
 * Core configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UICoreConfig).
 */
export interface CoreSettings {
  /** Execution adapter for running pipeline stages */
  executionAdapter: ExecutionAdapter;

  /** Authentication provider for Claude API */
  authProvider: AuthProvider;

  /** Default model for pipeline stages */
  defaultModel: ModelSelection;

  /** Path to context files relative to workspace root */
  contextPath: string;

  /** Path to plan files relative to workspace root */
  plansPath: string;

  /** Gemini authentication method (Issue #1056) */
  geminiAuthMethod: GeminiAuthMethod;

  /** Gemini model selection (Issue #1056) */
  geminiModel: GeminiModel;

  /** Copilot model override (Issue #1945) */
  copilotModel: string | undefined;

  /** LM Studio model name (Issue #2058) */
  lmStudioModel: string | undefined;

  /** LM Studio server base URL (Issue #2058) */
  lmStudioBaseUrl: string | undefined;

  /** LM Studio request timeout in ms (Issue #2058) */
  lmStudioTimeoutMs: number | undefined;

  /** LM Studio tool calling enabled (Issue #2058) */
  lmStudioToolCalling: boolean | undefined;

  /** LM Studio max tokens per response (Issue #2058) */
  lmStudioMaxTokens: number | undefined;
}

/**
 * Default core settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.core from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_CORE_SETTINGS: CoreSettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.core,
  DEFAULT_CONFIG.lm_studio
);

/**
 * Map ConfigBridge UICoreConfig to legacy CoreSettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UICoreConfig, lmStudio?: LmStudioConfig): CoreSettings {
  const defaults = DEFAULT_CONFIG.ui!.core!;

  return {
    executionAdapter: config?.adapter ?? defaults.adapter!,
    authProvider: config?.auth_provider ?? defaults.auth_provider!,
    defaultModel: config?.default_model ?? defaults.default_model!,
    contextPath: config?.context_path ?? defaults.context_path!,
    plansPath: config?.plans_path ?? defaults.plans_path!,
    geminiAuthMethod: config?.gemini?.auth_method ?? defaults.gemini!.auth_method!,
    geminiModel: config?.gemini?.model ?? defaults.gemini!.model!,
    copilotModel: config?.copilot?.model,
    lmStudioModel: lmStudio?.model,
    lmStudioBaseUrl: lmStudio?.base_url,
    lmStudioTimeoutMs: lmStudio?.timeout_ms,
    lmStudioToolCalling: lmStudio?.tool_calling,
    lmStudioMaxTokens: lmStudio?.max_tokens,
  };
}

/**
 * Get current core settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getCoreSettings(): CoreSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for core");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.core, DEFAULT_CONFIG.lm_studio);
  }

  const ui = configBridge.getUI();
  const lmStudio = configBridge.getLmStudio();
  return mapToLegacyShape(ui?.core, lmStudio);
}

/**
 * Re-export UICoreConfig and adapter config types for consumers
 */
export type { UICoreConfig, GeminiAuthMethod, GeminiModel, CopilotConfig, LmStudioConfig };
