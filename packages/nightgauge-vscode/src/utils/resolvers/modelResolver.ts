/**
 * Model Resolver — model selection, execution adapter, and model routing config.
 *
 * Extracted from nightgaugeConfig.ts as part of the config-module decomposition.
 *
 * @see Issue #2742 - Refactor VSCode nightgaugeConfig.ts into focused domain modules
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import type { z } from "zod";
import { parse as parseYaml } from "yaml";
import type { LocalEndpoint, PipelineStage } from "@nightgauge/sdk";
import {
  CODEX_DEFAULT_BASE_MODEL,
  CODEX_TIER_MODEL_MAP,
  REASONING_EFFORT_ALTERNATION,
  REASONING_EFFORT_LEVELS,
  TIER_BAND_ALTERNATION,
  TIER_BANDS,
  escalationLadder,
  isTierBand,
  retiredAdapterMessage,
  type TierBand,
} from "@nightgauge/sdk";
import { resolveConfigPathSync, logDeprecationWarning } from "../configPathResolver";
import { readEffectiveConfigTextSync } from "../mergedConfigReader";
import { AdapterEnumSchema, ADAPTER_ID_ALTERNATION } from "../../config/schema";

// ============================================================================
// Core model types and selection
// ============================================================================

/**
 * Default model type for pipeline stages — derived from the `TIER_BANDS`
 * authority (#581), never re-spelled (#582).
 */
export type DefaultModel = TierBand;

/**
 * Get the default model from config or environment.
 * Priority: NIGHTGAUGE_UI_CORE_DEFAULT_MODEL env → ui.core.default_model → undefined
 *
 * BOTH sources accept the same four registry bands, and the file matcher below
 * has to spell all four (#340). It listed three — a regex written before Fable
 * existed — while the env branch validated against `validModels`, so
 * `ui.core.default_model: fable` was silently dropped here and honored by the
 * Go mirror (`workspaceDefaultModel`, internal/orchestrator/dispatch_routing.go),
 * which reads all four from both. One config file then dispatched Fable on an
 * autonomous run and Sonnet — resolveModel's Step 4 hardcoded fallback — from
 * the extension, with no log line on either side.
 *
 * @see Issue #626 - Claude CLI headless adapter audit
 */
export function getDefaultModel(workspaceRoot?: string): DefaultModel | undefined {
  const validModels: readonly DefaultModel[] = TIER_BANDS;

  // Check environment variable first
  const envModel = process.env.NIGHTGAUGE_UI_CORE_DEFAULT_MODEL;
  if (envModel && validModels.includes(envModel as DefaultModel)) {
    return envModel as DefaultModel;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inCore = false;
        }
      }

      if (inCore) {
        const match = trimmed.match(
          new RegExp(`^default_model:\\s*['"]?(${TIER_BAND_ALTERNATION})['"]?(?:\\s+#.*)?$`)
        );
        if (match) {
          return match[1] as DefaultModel;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read default model from nightgauge config:", error);
    return undefined;
  }
}

/**
 * Get the fallback model from config or environment.
 * Priority: NIGHTGAUGE_UI_CORE_FALLBACK_MODEL env → ui.core.fallback_model → undefined
 * @see Issue #626 - Claude CLI headless adapter audit
 */
export function getFallbackModel(workspaceRoot?: string): DefaultModel | undefined {
  const validModels: readonly DefaultModel[] = TIER_BANDS;

  // Check environment variable first
  const envModel = process.env.NIGHTGAUGE_UI_CORE_FALLBACK_MODEL;
  if (envModel && validModels.includes(envModel as DefaultModel)) {
    return envModel as DefaultModel;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inCore = false;
        }
      }

      if (inCore) {
        // All four bands, matching this function's own `validModels` guard and
        // the env branch above — the same alternation `default_model` had to
        // grow for #340. A `fallback_model: fable` the env var accepts and the
        // file drops is the identical silent divergence in the adjacent knob.
        const match = trimmed.match(
          new RegExp(`^fallback_model:\\s*['"]?(${TIER_BAND_ALTERNATION})['"]?(?:\\s+#.*)?$`)
        );
        if (match) {
          return match[1] as DefaultModel;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read fallback model from nightgauge config:", error);
    return undefined;
  }
}

/**
 * Get the max_turns setting from config or environment.
 * Priority: NIGHTGAUGE_PIPELINE_MAX_TURNS env → pipeline.max_turns → undefined
 * @see Issue #626 - Claude CLI headless adapter audit
 */
export function getMaxTurns(workspaceRoot?: string): number | undefined {
  // Check environment variable first
  const envMaxTurns = process.env.NIGHTGAUGE_PIPELINE_MAX_TURNS;
  if (envMaxTurns) {
    const parsed = Number.parseInt(envMaxTurns, 10);
    if (!Number.isNaN(parsed) && parsed > 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
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
        const match = trimmed.match(/^max_turns:\s*(\d+)$/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (parsed > 0) {
            return parsed;
          }
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read max_turns from nightgauge config:", error);
    return undefined;
  }
}

/**
 * Get the cost budget from config or environment.
 * Priority: NIGHTGAUGE_BATCH_COST_BUDGET env → batch.resource_limits.cost_budget → undefined
 * @see Issue #626 - Claude CLI headless adapter audit
 */
export function getCostBudget(workspaceRoot?: string): number | undefined {
  // Check environment variable first
  const envBudget = process.env.NIGHTGAUGE_BATCH_COST_BUDGET;
  if (envBudget) {
    const parsed = Number.parseFloat(envBudget);
    if (!Number.isNaN(parsed) && parsed > 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inBatch = false;
    let inResourceLimits = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "batch:") {
        inBatch = true;
        continue;
      }

      if (inBatch && trimmed === "resource_limits:") {
        inResourceLimits = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inBatch = false;
          inResourceLimits = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inResourceLimits = false;
        }
      }

      if (inResourceLimits) {
        const match = trimmed.match(/^cost_budget:\s*([\d.]+)$/);
        if (match) {
          const parsed = Number.parseFloat(match[1]);
          if (!Number.isNaN(parsed) && parsed > 0) {
            return parsed;
          }
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read cost budget from nightgauge config:", error);
    return undefined;
  }
}

// ============================================================================
// Execution adapter
// ============================================================================

/**
 * Execution adapter type for stage orchestration backend.
 *
 * Derived from `AdapterEnumSchema` (schema.ts) — the single canonical
 * source — rather than hand-spelled (#1623).
 */
export type ExecutionAdapter = z.infer<typeof AdapterEnumSchema>;

/** Default execution adapter (Claude CLI). */
export const DEFAULT_EXECUTION_ADAPTER: ExecutionAdapter = "claude";

/**
 * Valid adapter values for runtime validation — the SINGLE SOURCE OF TRUTH is
 * `AdapterEnumSchema`. Deriving from it (rather than a hand-maintained parallel
 * array) prevents the "settings UI offers an adapter the runtime silently drops
 * to claude" drift that the schema comment warns about (#4030).
 */
export const VALID_ADAPTERS: readonly string[] = AdapterEnumSchema.options;

/**
 * Get the execution adapter from config or environment.
 * Priority: NIGHTGAUGE_UI_CORE_ADAPTER env → ConfigBridge → config.local.yaml → config.yaml → 'claude'
 *
 * Source attribution (configured-vs-default) is handled by
 * `getGlobalAdapterWithSource` in `adapterResolver.ts` (Issue #3221) which
 * mirrors this logic and additionally exposes a `configured` flag.
 */
export function getExecutionAdapter(workspaceRoot?: string): ExecutionAdapter {
  // 1. Environment variable takes highest precedence
  const envAdapter = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
  if (VALID_ADAPTERS.includes(envAdapter ?? "")) {
    return envAdapter as ExecutionAdapter;
  }

  // 2. ConfigBridge (available after extension activation, includes local overrides)
  try {
    const { ConfigBridge } = require("../../services/ConfigBridge");
    const bridge = ConfigBridge.getInstance();
    const adapter = bridge?.getUI()?.core?.adapter;
    if (adapter && VALID_ADAPTERS.includes(adapter)) {
      return adapter as ExecutionAdapter;
    }
  } catch {
    // ConfigBridge not yet initialized (early startup) — fall through to file
  }

  // 3. File-based fallback: check config.local.yaml first (personal preference),
  //    then config.yaml (project level, for backward compat)
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    const adapter =
      readAdapterFromFile(path.join(root, ".nightgauge", "config.local.yaml")) ??
      readAdapterFromFile(path.join(root, ".nightgauge", "config.yaml"));
    if (adapter) {
      return adapter;
    }
  }

  // 4. Default
  return DEFAULT_EXECUTION_ADAPTER;
}

/**
 * Parse ui.core.adapter from a YAML config file without full YAML parsing.
 * Returns null if file doesn't exist or adapter isn't set.
 *
 * Exported for reuse by `adapterResolver.ts` (Issue #3221).
 */
/**
 * Throw the #2128 migration error when `value` names a removed adapter
 * (`lm-studio`, `ollama`). `where` names the setting that carried it.
 */
export function assertAdapterNotRetired(value: string | undefined | null, where: string): void {
  const message = retiredAdapterMessage(value, where);
  if (message) {
    throw new RetiredAdapterConfigError(message);
  }
}

/** The error {@link assertAdapterNotRetired} throws; config readers rethrow it. */
export class RetiredAdapterConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "RetiredAdapterConfigError";
  }
}

export function readAdapterFromFile(filePath: string): ExecutionAdapter | null {
  try {
    const content = fs.readFileSync(filePath, "utf-8");
    const lines = content.split("\n");
    let inUi = false;
    let inCore = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inCore = false;
        }
      }

      if (inCore) {
        const raw = trimmed.match(/^adapter:\s*['"]?([A-Za-z0-9_-]+)['"]?/);
        assertAdapterNotRetired(raw?.[1], `ui.core.adapter in ${filePath}`);
        const match = trimmed.match(
          new RegExp(`^adapter:\\s*['"]?(${ADAPTER_ID_ALTERNATION})['"]?(?:\\s+#.*)?$`)
        );
        if (match) {
          return match[1] as ExecutionAdapter;
        }
      }
    }
  } catch (error) {
    if (error instanceof RetiredAdapterConfigError) throw error;
    // File doesn't exist or can't be read
  }
  return null;
}

// ============================================================================
// Gemini Configuration
// ============================================================================

/** Gemini model type */
export type GeminiModel = "gemini-2.5-pro" | "gemini-2.5-flash" | "gemini-2.0-flash";

/** Gemini auth method type */
export type GeminiAuthMethod = "api-key" | "google-login" | "vertex-ai";

const DEFAULT_GEMINI_MODEL: GeminiModel = "gemini-2.5-flash";
const DEFAULT_GEMINI_AUTH_METHOD: GeminiAuthMethod = "api-key";

const VALID_GEMINI_MODELS: GeminiModel[] = [
  "gemini-2.5-pro",
  "gemini-2.5-flash",
  "gemini-2.0-flash",
];
const VALID_GEMINI_AUTH_METHODS: GeminiAuthMethod[] = ["api-key", "google-login", "vertex-ai"];

/**
 * Get the Gemini model from config or environment.
 * Priority: NIGHTGAUGE_GEMINI_MODEL env → ui.core.gemini.model → 'gemini-2.5-flash'
 * @see Issue #1056 - Gemini VSCode configuration UI
 */
export function getGeminiModel(workspaceRoot?: string): GeminiModel {
  const envModel = process.env.NIGHTGAUGE_GEMINI_MODEL;
  if (envModel && VALID_GEMINI_MODELS.includes(envModel as GeminiModel)) {
    return envModel as GeminiModel;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_GEMINI_MODEL;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_GEMINI_MODEL;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;
    let inGemini = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (inCore && trimmed === "gemini:") {
        inGemini = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inGemini = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inGemini = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inGemini = false;
        }
      }

      if (inGemini) {
        const match = trimmed.match(
          /^model:\s*['"]?(gemini-2\.5-pro|gemini-2\.5-flash|gemini-2\.0-flash)['"]?(?:\s+#.*)?$/
        );
        if (match) {
          return match[1] as GeminiModel;
        }
      }
    }

    return DEFAULT_GEMINI_MODEL;
  } catch (error) {
    console.error("Failed to read Gemini model from nightgauge config:", error);
    return DEFAULT_GEMINI_MODEL;
  }
}

/**
 * Get the Gemini auth method from config or environment.
 * Priority: NIGHTGAUGE_GEMINI_AUTH_METHOD env → ui.core.gemini.auth_method → 'api-key'
 * @see Issue #1056 - Gemini VSCode configuration UI
 */
export function getGeminiAuthMethod(workspaceRoot?: string): GeminiAuthMethod {
  const envMethod = process.env.NIGHTGAUGE_GEMINI_AUTH_METHOD;
  if (envMethod && VALID_GEMINI_AUTH_METHODS.includes(envMethod as GeminiAuthMethod)) {
    return envMethod as GeminiAuthMethod;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_GEMINI_AUTH_METHOD;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_GEMINI_AUTH_METHOD;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;
    let inGemini = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (inCore && trimmed === "gemini:") {
        inGemini = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inGemini = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inGemini = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inGemini = false;
        }
      }

      if (inGemini) {
        const match = trimmed.match(
          /^auth_method:\s*['"]?(api-key|google-login|vertex-ai)['"]?(?:\s+#.*)?$/
        );
        if (match) {
          return match[1] as GeminiAuthMethod;
        }
      }
    }

    return DEFAULT_GEMINI_AUTH_METHOD;
  } catch (error) {
    console.error("Failed to read Gemini auth method from nightgauge config:", error);
    return DEFAULT_GEMINI_AUTH_METHOD;
  }
}

// ============================================================================
// Codex Configuration
// ============================================================================

/** Codex model identifier */
export type CodexModel = string;
/**
 * Derived from the SDK's `REASONING_EFFORT_LEVELS` authority (#435, absorbed
 * by #581) — one of the four hand-listed copies that ladder replaced.
 */
export type CodexReasoningEffort = (typeof REASONING_EFFORT_LEVELS)[number];

const DEFAULT_CODEX_CLI_COMMAND = "codex";
const CODEX_REASONING_EFFORTS: readonly CodexReasoningEffort[] = REASONING_EFFORT_LEVELS;

/**
 * Get the Codex model from config or environment.
 * Priority: NIGHTGAUGE_CODEX_MODEL env → ui.core.codex.model →
 * CODEX_DEFAULT_BASE_MODEL (the canonical sonnet/base Codex tier from the SDK
 * registry — currently gpt-5.4).
 * @see Issue #1656 - GPT-5.4 model routing for Codex adapter
 * @see Issue #4018 - Canonical Codex model registry
 */
export function getCodexModel(workspaceRoot?: string): CodexModel {
  const envModel = process.env.NIGHTGAUGE_CODEX_MODEL;
  if (envModel && envModel.trim()) {
    return envModel.trim();
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return CODEX_DEFAULT_BASE_MODEL;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return CODEX_DEFAULT_BASE_MODEL;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;
    let inCodex = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (inCore && trimmed === "codex:") {
        inCodex = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inCodex = false;
        }
      }

      if (inCodex) {
        const match = trimmed.match(/^model:\s*['"]?([^#"'\n]+?)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1].trim();
        }
      }
    }

    return CODEX_DEFAULT_BASE_MODEL;
  } catch (error) {
    console.error("Failed to read Codex model from nightgauge config:", error);
    return CODEX_DEFAULT_BASE_MODEL;
  }
}

/**
 * Get the default Codex reasoning budget.
 * Priority: NIGHTGAUGE_CODEX_REASONING_EFFORT env →
 * ui.core.codex.reasoning_effort → undefined (Codex provider default).
 */
export function getCodexReasoningEffort(workspaceRoot?: string): CodexReasoningEffort | undefined {
  const envEffort = process.env.NIGHTGAUGE_CODEX_REASONING_EFFORT?.trim();
  if (CODEX_REASONING_EFFORTS.includes(envEffort as CodexReasoningEffort)) {
    return envEffort as CodexReasoningEffort;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return undefined;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return undefined;
    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const lines = readEffectiveConfigTextSync(pathResult).split("\n");
    let inUi = false;
    let inCore = false;
    let inCodex = false;
    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }
      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }
      if (inCore && trimmed === "codex:") {
        inCodex = true;
        continue;
      }
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inCodex = false;
        }
      }
      if (inCodex) {
        // Alternation derived from the #435 authority — the literal
        // `(none|low|medium|high|xhigh|max)` here was the exact regex form
        // stageResolver had already replaced with EFFORT_ALTERNATION.
        const match = trimmed.match(
          new RegExp(
            `^reasoning_effort:\\s*['"]?(${REASONING_EFFORT_ALTERNATION})['"]?(?:\\s+#.*)?$`
          )
        );
        if (match) return match[1] as CodexReasoningEffort;
      }
    }
  } catch (error) {
    console.error("Failed to read Codex reasoning effort from nightgauge config:", error);
  }
  return undefined;
}

/**
 * Resolve a pipeline tier alias (`haiku`/`sonnet`/`opus`) to the concrete
 * Codex/OpenAI model that should run that stage.
 *
 * This preserves the existing Claude-style stage routing contract while
 * letting the Codex adapter use OpenAI-native model identifiers. The concrete
 * ids come from the SDK's canonical `CODEX_TIER_MODEL_MAP` registry:
 *
 * - `haiku`  → `CODEX_TIER_MODEL_MAP.haiku` (cheaper Codex mini tier)
 * - `sonnet` → the configured/default Codex model (preserves current behavior)
 * - `opus`   → `CODEX_TIER_MODEL_MAP.opus` (the strongest Codex tier)
 *
 * Exact Codex model names pass through unchanged.
 *
 * @see Issue #4018 - Canonical Codex model registry
 */
export function resolveCodexPipelineModel(
  model: string | undefined,
  workspaceRoot?: string
): CodexModel {
  if (!model || model === "sonnet") {
    return getCodexModel(workspaceRoot);
  }

  const trimmed = model.trim();

  // Map the remaining tier aliases (haiku/opus/fable) via the canonical
  // CODEX_TIER_MODEL_MAP (#4018). `fable` was previously dropped by a type-guard
  // that omitted it, leaking the literal "fable" to the Codex CLI as an invalid
  // model id. Exact Codex model ids pass through unchanged.
  // `sonnet` already returned above; every remaining band resolves through
  // the canonical map — membership derives from the authority (#582), so a
  // new band can no longer be silently dropped the way `fable` once was.
  if (isTierBand(trimmed)) {
    return CODEX_TIER_MODEL_MAP[trimmed];
  }

  return trimmed;
}

/**
 * Get the Codex CLI command from config or environment.
 * Priority: NIGHTGAUGE_CODEX_CLI_COMMAND env → ui.core.codex.cli_command → 'codex'
 */
export function getCodexCliCommand(workspaceRoot?: string): string {
  const envCommand = process.env.NIGHTGAUGE_CODEX_CLI_COMMAND;
  if (envCommand && envCommand.trim()) {
    return envCommand.trim();
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_CODEX_CLI_COMMAND;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_CODEX_CLI_COMMAND;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;
    let inCodex = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (inCore && trimmed === "codex:") {
        inCodex = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inCodex = false;
        }
      }

      if (inCodex) {
        const match = trimmed.match(/^cli_command:\s*['"]?([^#"'\n]+?)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1].trim();
        }
      }
    }

    return DEFAULT_CODEX_CLI_COMMAND;
  } catch (error) {
    console.error("Failed to read Codex CLI command from nightgauge config:", error);
    return DEFAULT_CODEX_CLI_COMMAND;
  }
}

/**
 * Get the Codex CLI args override from config or environment.
 * Priority: NIGHTGAUGE_CODEX_CLI_ARGS env → ui.core.codex.cli_args → undefined
 */
export function getCodexCliArgs(workspaceRoot?: string): string | undefined {
  const envArgs = process.env.NIGHTGAUGE_CODEX_CLI_ARGS;
  if (envArgs !== undefined) {
    return envArgs.trim() || undefined;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;
    let inCodex = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (inCore && trimmed === "codex:") {
        inCodex = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inCodex = false;
        }
      }

      if (inCodex) {
        const match = trimmed.match(/^cli_args:\s*['"]?([^#\n]*?)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1].trim() || undefined;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read Codex CLI args from nightgauge config:", error);
    return undefined;
  }
}

/**
 * Get whether Codex session resume is enabled from config or environment.
 * Priority: NIGHTGAUGE_CODEX_RESUME_ENABLED env → ui.core.codex.resume_enabled → false
 */
export function getCodexResumeEnabled(workspaceRoot?: string): boolean {
  const envValue = process.env.NIGHTGAUGE_CODEX_RESUME_ENABLED;
  if (envValue !== undefined) {
    return envValue === "true" || envValue === "1";
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return false;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return false;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;
    let inCodex = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (inCore && trimmed === "codex:") {
        inCodex = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inCodex = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inCodex = false;
        }
      }

      if (inCodex) {
        const match = trimmed.match(/^resume_enabled:\s*(true|false)(?:\s+#.*)?$/);
        if (match) {
          return match[1] === "true";
        }
      }
    }

    return false;
  } catch (error) {
    console.error("Failed to read Codex resume setting from nightgauge config:", error);
    return false;
  }
}

// ============================================================================
// OpenCode Configuration
// ============================================================================

/**
 * Validate a raw `opencode.model` value read from config before it can reach
 * argv. Defence in depth — this is a config-read-time sanity check, not the
 * authoritative gate; #1637 validates the model string again immediately
 * before it reaches the OpenCode CLI. Rejects:
 *   - a leading `-` (would be read as a CLI flag, e.g. `--auto`)
 *   - any whitespace (a single YAML scalar should never contain a space)
 *   - control characters (defends against injection via a crafted config file)
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md § 7
 */
function isValidOpenCodeModelValue(value: string): boolean {
  if (value.startsWith("-")) {
    return false;
  }
  // eslint-disable-next-line no-control-regex -- deliberately matching control chars
  if (/[\s\x00-\x1f\x7f]/.test(value)) {
    return false;
  }
  return true;
}

/**
 * The endpoints the machine-tier `opencode.endpoints[]` declares (#1678), as
 * id, kind and base URL, for deciding locality by endpoint (#2128). An entry
 * missing any of them, or a config that cannot be read, declares nothing.
 * The base URL is used only to classify the endpoint and is never shown.
 */
export function getOpenCodeEndpoints(workspaceRoot?: string): LocalEndpoint[] {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return [];
  }
  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return [];
    }
    const parsed: unknown = parseYaml(readEffectiveConfigTextSync(pathResult));
    const entries = (parsed as { opencode?: { endpoints?: unknown } } | null)?.opencode?.endpoints;
    if (!Array.isArray(entries)) {
      return [];
    }
    return entries.flatMap((e: unknown) => {
      const { id, provider, base_url } = (e ?? {}) as Record<string, unknown>;
      return typeof id === "string" && typeof provider === "string" && typeof base_url === "string"
        ? [{ id, provider, baseUrl: base_url }]
        : [];
    });
  } catch {
    return [];
  }
}

/**
 * Get the OpenCode model from `.nightgauge/config.yaml` / `config.local.yaml`.
 *
 * Per ADR-022 § 7, the `opencode:` block is machine-tier configuration; this
 * reads it through the same merged-tier YAML text
 * (`readEffectiveConfigTextSync`) the other adapter getters use. The raw
 * value round-trips unchanged, including nested slashes (e.g.
 * `lmstudio/qwen/qwen3.8-27b`) — no path segmentation is applied here.
 *
 * Returns `undefined` (and logs a warning) instead of a sanitized value when
 * the raw string fails `isValidOpenCodeModelValue` — defence in depth; #1637
 * validates again immediately before argv.
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md § 7
 * @see Issue #1623 - VS Code execution-adapter widening
 */
export function getOpenCodeModel(workspaceRoot?: string): string | undefined {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inOpenCode = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "opencode:" && !line.startsWith(" ")) {
        inOpenCode = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inOpenCode = false;
        }
      }

      if (inOpenCode) {
        const match = trimmed.match(/^model:\s*['"]?([^'"#\s]+)['"]?(?:\s+#.*)?$/);
        if (match) {
          const value = match[1];
          if (!isValidOpenCodeModelValue(value)) {
            console.warn(
              "Ignoring invalid opencode.model value in nightgauge config " +
                `(leading '-', whitespace or control characters are rejected): ${JSON.stringify(value)}`
            );
            return undefined;
          }
          return value;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read OpenCode model from nightgauge config:", error);
    return undefined;
  }
}

// ============================================================================
// Copilot Configuration
// ============================================================================

/**
 * Get the Copilot model from env or config.
 * Priority: NIGHTGAUGE_COPILOT_MODEL env → ui.core.copilot.model → ''
 * @see Issue #1946 - Add Copilot CLI execution branch in skillRunner
 */
export function getCopilotModel(workspaceRoot?: string): string {
  const envModel = process.env.NIGHTGAUGE_COPILOT_MODEL;
  if (envModel) return envModel;

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return "";

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return "";

    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;
    let inCopilot = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      if (inCore && trimmed === "copilot:") {
        inCopilot = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
          inCopilot = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inCore = false;
          inCopilot = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inCopilot = false;
        }
      }

      if (inCopilot) {
        const match = trimmed.match(/^model:\s*['"]?([^'"#\s]+)['"]?(?:\s+#.*)?$/);
        if (match) return match[1];
      }
    }

    return "";
  } catch (error) {
    console.error("Failed to read Copilot model from nightgauge config:", error);
    return "";
  }
}

// ============================================================================
// Model routing
// ============================================================================

/**
 * Model routing mode type
 * @see Issue #731 - Model routing configuration modes
 */
export type ModelRoutingMode = "manual" | "automatic" | "hybrid";

/**
 * Complexity thresholds for automatic model selection
 * @see Issue #731 - Model routing configuration modes
 */
export interface ComplexityThresholds {
  /** Max complexity score for Haiku (0-10, default 3) */
  haikuMax: number;
  /** Max complexity score for Sonnet (0-10, default 6) — above this → Opus */
  sonnetMax: number;
}

/**
 * Default complexity thresholds.
 *
 * Sonnet 4.6 is near-Opus quality at ~60% of the cost. The wide Sonnet band
 * (sonnetMax 6) covers XS through M complexity (scores 1-4). Opus is reserved
 * for L/XL (scores 7-9) where deeper reasoning justifies the premium.
 */
export const DEFAULT_COMPLEXITY_THRESHOLDS: ComplexityThresholds = {
  haikuMax: 3,
  sonnetMax: 6,
};

/** Default confidence threshold for automatic model selection */
export const DEFAULT_CONFIDENCE_THRESHOLD = 0.7;

/**
 * Get the model routing mode from config or environment.
 * Priority: NIGHTGAUGE_MODEL_ROUTING_MODE env → model_routing.mode → 'automatic'
 * @see Issue #731 - Model routing configuration modes
 */
export function getModelRoutingMode(workspaceRoot?: string): ModelRoutingMode {
  const validModes: ModelRoutingMode[] = ["manual", "automatic", "hybrid"];

  // Check environment variable first
  const envMode = process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
  if (envMode && validModes.includes(envMode as ModelRoutingMode)) {
    return envMode as ModelRoutingMode;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return "automatic";
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return "automatic";
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

      // Exit section on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inModelRouting = false;
        }
      }

      if (inModelRouting) {
        const match = trimmed.match(/^mode:\s*['"]?(manual|automatic|hybrid)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1] as ModelRoutingMode;
        }
      }
    }

    return "automatic";
  } catch (error) {
    console.error("Failed to read model routing mode from nightgauge config:", error);
    return "automatic";
  }
}

/**
 * Read a boolean flag under `model_routing:` from config, with an env override
 * and a default. Priority: `NIGHTGAUGE_MODEL_ROUTING_{envSuffix}` env →
 * `model_routing.{key}` → `defaultValue`. Fail-open to the default.
 *
 * @param key - the config key under `model_routing:` (e.g. "auto_tune")
 * @param envSuffix - the env-var suffix (e.g. "AUTO_TUNE")
 * @since Issue #21
 */
export function getModelRoutingBoolean(
  key: string,
  envSuffix: string,
  defaultValue: boolean,
  workspaceRoot?: string
): boolean {
  const envVal = process.env[`NIGHTGAUGE_MODEL_ROUTING_${envSuffix}`];
  if (envVal !== undefined) {
    const v = envVal.trim().toLowerCase();
    if (v === "true" || v === "1" || v === "yes") return true;
    if (v === "false" || v === "0" || v === "no") return false;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return defaultValue;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return defaultValue;
    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const lines = readEffectiveConfigTextSync(pathResult).split("\n");
    let inModelRouting = false;
    const re = new RegExp(`^${key}:\\s*['"]?(true|false)['"]?(?:\\s+#.*)?$`);
    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }
      if (
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inModelRouting = false;
      }
      if (inModelRouting) {
        const m = trimmed.match(re);
        if (m) return m[1] === "true";
      }
    }
    return defaultValue;
  } catch {
    return defaultValue;
  }
}

/**
 * Whether routing may consult the eval advisor's materialized advice file
 * (`.nightgauge/model-evals/routing-advice.json`, #581 / spike #568 §4.2).
 *
 * Priority: `NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS` env →
 * `model_routing.use_eval_recommendations` → **false** — the conservative
 * rollout default: with the key off (or no advice file, or no advisable
 * evidence) the axis query alone decides, which reproduces pre-advice
 * behavior exactly. Go pair: `useEvalRecommendations`
 * (internal/orchestrator/dispatch_routing.go).
 */
export function isEvalRecommendationsEnabled(workspaceRoot?: string): boolean {
  return getModelRoutingBoolean(
    "use_eval_recommendations",
    "USE_EVAL_RECOMMENDATIONS",
    false,
    workspaceRoot
  );
}

/**
 * Get complexity thresholds from config or environment.
 * Priority: NIGHTGAUGE_MODEL_ROUTING_{HAIKU,SONNET}_MAX env → model_routing.complexity_thresholds → defaults
 * @see Issue #731 - Model routing configuration modes
 */
export function getComplexityThresholds(workspaceRoot?: string): ComplexityThresholds {
  const thresholds: ComplexityThresholds = {
    ...DEFAULT_COMPLEXITY_THRESHOLDS,
  };

  // Check environment variables
  const envHaikuMax = process.env.NIGHTGAUGE_MODEL_ROUTING_HAIKU_MAX;
  if (envHaikuMax) {
    const parsed = Number.parseInt(envHaikuMax, 10);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 10) {
      thresholds.haikuMax = parsed;
    }
  }

  const envSonnetMax = process.env.NIGHTGAUGE_MODEL_ROUTING_SONNET_MAX;
  if (envSonnetMax) {
    const parsed = Number.parseInt(envSonnetMax, 10);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 10) {
      thresholds.sonnetMax = parsed;
    }
  }

  // If env vars provided both, return early
  if (envHaikuMax && envSonnetMax) {
    return thresholds;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return thresholds;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return thresholds;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inModelRouting = false;
    let inComplexityThresholds = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }

      if (inModelRouting && trimmed === "complexity_thresholds:") {
        inComplexityThresholds = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inModelRouting = false;
          inComplexityThresholds = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inComplexityThresholds = false;
        }
      }

      if (inComplexityThresholds) {
        const match = trimmed.match(/^([a-z_]+):\s*(\d+)$/);
        if (match) {
          const [, key, value] = match;
          const parsed = Number.parseInt(value, 10);
          if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 10) {
            if (key === "haiku_max" && !envHaikuMax) {
              thresholds.haikuMax = parsed;
            } else if (key === "sonnet_max" && !envSonnetMax) {
              thresholds.sonnetMax = parsed;
            }
          }
        }
      }
    }

    return thresholds;
  } catch (error) {
    console.error("Failed to read complexity thresholds from nightgauge config:", error);
    return thresholds;
  }
}

/**
 * Get the minimum model for a specific pipeline stage.
 * The minimum model acts as a floor — AutoModelSelector cannot assign a lighter model.
 * Priority: NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_{STAGE} env → model_routing.minimum_model.{stage} → undefined
 * @see Issue #731 - Model routing configuration modes
 */
/**
 * Read `model_routing.max_model` — the operator cap on the strongest tier
 * AUTOMATIC routing may reach, below the performance mode's own ceiling
 * (#1201). Returns undefined when unset, which means "no cap" and reproduces
 * the mode's envelope exactly.
 *
 * Go pair: `routing.resolveMaxModel`. Both fail open on an unreadable value —
 * a cap that cannot be parsed must not silently reroute anything.
 *
 * This is a scalar, not a per-stage map: a per-stage cap is what
 * `pipeline.stage_models` already is, and two per-stage mechanisms that clamp
 * differently is how the drift this key was filed alongside started.
 */
export function getMaxModel(workspaceRoot?: string): DefaultModel | undefined {
  const validModels: readonly DefaultModel[] = TIER_BANDS;

  const envModel = process.env.NIGHTGAUGE_MODEL_ROUTING_MAX_MODEL;
  if (envModel && validModels.includes(envModel as DefaultModel)) {
    return envModel as DefaultModel;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const lines = readEffectiveConfigTextSync(pathResult).split("\n");
    let inModelRouting = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }
      // A new TOP-LEVEL key ends the section. Nested keys (two-space indented)
      // stay inside it — max_model is one of them.
      if (inModelRouting && trimmed && !trimmed.startsWith("#") && !line.startsWith(" ")) {
        inModelRouting = false;
      }

      if (inModelRouting) {
        const m = trimmed.match(
          new RegExp(`^max_model:\\s*['"]?(${validModels.join("|")})['"]?(?:\\s+#.*)?$`)
        );
        if (m) {
          return m[1] as DefaultModel;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read max model from nightgauge config:", error);
    return undefined;
  }
}

export function getMinimumModel(
  stage: PipelineStage,
  workspaceRoot?: string
): DefaultModel | undefined {
  const validModels: readonly DefaultModel[] = TIER_BANDS;

  // Check environment variable first
  const envKey = `NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envModel = process.env[envKey];
  if (envModel && validModels.includes(envModel as DefaultModel)) {
    return envModel as DefaultModel;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inModelRouting = false;
    let inMinimumModel = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }

      if (inModelRouting && trimmed === "minimum_model:") {
        inMinimumModel = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inModelRouting = false;
          inMinimumModel = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inMinimumModel = false;
        }
      }

      if (inMinimumModel) {
        // Alternation is built from validModels so every routing tier —
        // including fable — is accepted; a hardcoded list here once silently
        // dropped `minimum_model.<stage>: fable` (#56).
        const modelMatch = trimmed.match(
          new RegExp(`^([a-z][-a-z]*):\\s*['"]?(${validModels.join("|")})['"]?(?:\\s+#.*)?$`)
        );
        if (modelMatch && modelMatch[1] === stage) {
          return modelMatch[2] as DefaultModel;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read minimum model from nightgauge config:", error);
    return undefined;
  }
}

/**
 * Get the confidence threshold for automatic model selection.
 * When AutoModelSelector's confidence is below this value, it falls back to the default model.
 * Priority: NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD env → model_routing.confidence_threshold → 0.7
 * @see Issue #731 - Model routing configuration modes
 */
export function getConfidenceThreshold(workspaceRoot?: string): number {
  // Check environment variable first
  const envThreshold = process.env.NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD;
  if (envThreshold) {
    const parsed = Number.parseFloat(envThreshold);
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 1) {
      return parsed;
    }
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_CONFIDENCE_THRESHOLD;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_CONFIDENCE_THRESHOLD;
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

      // Exit section on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inModelRouting = false;
        }
      }

      if (inModelRouting) {
        const match = trimmed.match(/^confidence_threshold:\s*([\d.]+)$/);
        if (match) {
          const parsed = Number.parseFloat(match[1]);
          if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 1) {
            return parsed;
          }
        }
      }
    }

    return DEFAULT_CONFIDENCE_THRESHOLD;
  } catch (error) {
    console.error("Failed to read confidence threshold from nightgauge config:", error);
    return DEFAULT_CONFIDENCE_THRESHOLD;
  }
}

// ============================================================================
// Escalation
// ============================================================================

/**
 * The escalation path (haiku → sonnet → opus with today's registry), derived
 * from the selection query (#581): membership from the registry, order from
 * the band ladder, and the frontier exclusion from the declared escalation
 * ceiling — never a hand-inlined triplet. Go pair: routing.EscalationLadder
 * feeding RetryConfig.ModelLadder.
 * @see Issue #1343 - Dynamic Model Escalation Engine
 */
const ESCALATION_PATH: readonly DefaultModel[] = escalationLadder("anthropic");

export function getEscalatedModel(currentModel: DefaultModel): DefaultModel | null {
  const idx = ESCALATION_PATH.indexOf(currentModel);
  if (idx === -1 || idx === ESCALATION_PATH.length - 1) return null; // at ceiling or unknown
  return ESCALATION_PATH[idx + 1];
}
