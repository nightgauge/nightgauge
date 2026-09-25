/**
 * Core Settings for Nightgauge
 *
 * Provides typed access to core configuration (auth, model, paths) via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #476 - Refactor tree providers, extension.ts, and settings.ts to use ConfigBridge
 */

import type { TierBand } from "@nightgauge/sdk";
import { ConfigBridge } from "../services/ConfigBridge";
import {
  type UICoreConfig,
  type GeminiAuthMethod,
  type GeminiModel,
  type CopilotConfig,
  type AdapterEnumSchema,
  DEFAULT_CONFIG,
} from "./schema";
import type { z } from "zod";

/**
 * Authentication provider options
 */
export type AuthProvider = "max" | "bedrock" | "vertex";

/**
 * Execution adapter options (UI-facing).
 *
 * Derived from `AdapterEnumSchema` (the single canonical source in
 * `schema.ts`) rather than hand-spelled — a fifth copy of this union used to
 * drift independently of the other four (#1623).
 *
 * Maps to SDK's NightgaugeAdapter:
 * - 'claude' → 'claude-sdk' or 'claude-headless'
 * - 'codex'  → 'codex'
 * - 'opencode' → 'opencode'
 *
 * @see packages/nightgauge-sdk/src/cli/adapters/ICliAdapter.ts
 * @see Issue #627
 * @see Issue #1623 - Collapse the four hand-spelled adapter enums into one
 */
export type ExecutionAdapter = z.infer<typeof AdapterEnumSchema>;

/**
 * Model selection options — derived from the `TIER_BANDS` authority (#581),
 * never re-spelled (#582).
 */
export type ModelSelection = TierBand;

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
}

/**
 * Default core settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.core from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_CORE_SETTINGS: CoreSettings = mapToLegacyShape(DEFAULT_CONFIG.ui?.core);

/**
 * Map ConfigBridge UICoreConfig to legacy CoreSettings shape
 *
 * Handles the snake_case → camelCase transformations.
 */
function mapToLegacyShape(config?: UICoreConfig): CoreSettings {
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
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.core);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.core);
}

/**
 * Re-export UICoreConfig and adapter config types for consumers
 */
export type { UICoreConfig, GeminiAuthMethod, GeminiModel, CopilotConfig };
