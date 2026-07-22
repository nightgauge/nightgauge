/**
 * Model Resolver — model selection, execution adapter, and model routing config.
 *
 * Extracted from incrediConfig.ts as part of the config-module decomposition.
 *
 * @see Issue #2742 - Refactor VSCode incrediConfig.ts into focused domain modules
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { CODEX_DEFAULT_BASE_MODEL, CODEX_TIER_MODEL_MAP } from "@nightgauge/sdk";
import { resolveConfigPathSync, logDeprecationWarning } from "../configPathResolver";
import { readEffectiveConfigTextSync } from "../mergedConfigReader";
import { AdapterEnumSchema } from "../../config/schema";

// ============================================================================
// Core model types and selection
// ============================================================================

/** Default model type for pipeline stages */
export type DefaultModel = "sonnet" | "opus" | "haiku" | "fable";

/**
 * Get the default model from config or environment.
 * Priority: NIGHTGAUGE_UI_CORE_DEFAULT_MODEL env → ui.core.default_model → undefined
 * @see Issue #626 - Claude CLI headless adapter audit
 */
export function getDefaultModel(workspaceRoot?: string): DefaultModel | undefined {
  const validModels: DefaultModel[] = ["sonnet", "opus", "haiku", "fable"];

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
        const match = trimmed.match(/^default_model:\s*['"]?(sonnet|opus|haiku)['"]?(?:\s+#.*)?$/);
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
  const validModels: DefaultModel[] = ["sonnet", "opus", "haiku", "fable"];

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
        const match = trimmed.match(/^fallback_model:\s*['"]?(sonnet|opus|haiku)['"]?(?:\s+#.*)?$/);
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

/** Execution adapter type for stage orchestration backend. */
export type ExecutionAdapter =
  "claude" | "codex" | "gemini" | "gemini-sdk" | "lm-studio" | "ollama" | "copilot";

/** Default execution adapter (Claude CLI). */
export const DEFAULT_EXECUTION_ADAPTER: ExecutionAdapter = "claude";

/**
 * Valid adapter values for runtime validation — the SINGLE SOURCE OF TRUTH is
 * `AdapterEnumSchema`. Deriving from it (rather than a hand-maintained parallel
 * array) prevents the "settings UI offers an adapter the runtime silently drops
 * to claude" drift that the schema comment warns about — e.g. `ollama`, which is
 * a fully-wired adapter and selectable in the settings UI (#4030).
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
    const { ConfigBridge } = require("../services/ConfigBridge");
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
        const match = trimmed.match(
          /^adapter:\s*['"]?(claude|codex|gemini|gemini-sdk|lm-studio|copilot)['"]?(?:\s+#.*)?$/
        );
        if (match) {
          return match[1] as ExecutionAdapter;
        }
      }
    }
  } catch {
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

const DEFAULT_CODEX_CLI_COMMAND = "codex";

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
  if (trimmed === "haiku" || trimmed === "opus" || trimmed === "fable") {
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
// LM Studio Configuration
// ============================================================================

/**
 * Get the LM Studio model from env or config.
 * Priority: NIGHTGAUGE_LM_STUDIO_MODEL env → lm_studio.model → ''
 * @see Issue #2057 - Route pipeline stage execution through LM Studio
 */
export function getLmStudioModel(workspaceRoot?: string): string {
  const envModel = process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
  if (envModel) return envModel;

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return "";
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return "";
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inLmStudio = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "lm_studio:" && !line.startsWith(" ")) {
        inLmStudio = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inLmStudio = false;
        }
      }

      if (inLmStudio) {
        const match = trimmed.match(/^model:\s*['"]?([^'"#\s]+)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1];
        }
      }
    }

    return "";
  } catch (error) {
    console.error("Failed to read LM Studio model from nightgauge config:", error);
    return "";
  }
}

/**
 * Get the LM Studio base URL from env or config.
 * Priority: NIGHTGAUGE_LM_STUDIO_BASE_URL env → lm_studio.base_url → 'http://127.0.0.1:1234/v1'
 * @see Issue #2057 - Route pipeline stage execution through LM Studio
 */
export function getLmStudioBaseUrl(workspaceRoot?: string): string {
  const envUrl = process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL;
  if (envUrl) return envUrl;

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return "http://127.0.0.1:1234/v1";
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return "http://127.0.0.1:1234/v1";
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inLmStudio = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "lm_studio:" && !line.startsWith(" ")) {
        inLmStudio = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inLmStudio = false;
        }
      }

      if (inLmStudio) {
        const match = trimmed.match(/^base_url:\s*['"]?([^'"#\s]+)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1];
        }
      }
    }

    return "http://127.0.0.1:1234/v1";
  } catch (error) {
    console.error("Failed to read LM Studio base URL from nightgauge config:", error);
    return "http://127.0.0.1:1234/v1";
  }
}

/**
 * Get the LM Studio API key from env or config.
 * Priority: NIGHTGAUGE_LM_STUDIO_API_KEY env → lm_studio.api_key → 'lm-studio'
 * @see Issue #2057 - Route pipeline stage execution through LM Studio
 */
export function getLmStudioApiKey(workspaceRoot?: string): string {
  const envKey = process.env.NIGHTGAUGE_LM_STUDIO_API_KEY;
  if (envKey) return envKey;

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return "lm-studio";
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return "lm-studio";
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inLmStudio = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "lm_studio:" && !line.startsWith(" ")) {
        inLmStudio = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inLmStudio = false;
        }
      }

      if (inLmStudio) {
        const match = trimmed.match(/^api_key:\s*['"]?([^'"#\s]+)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1];
        }
      }
    }

    return "lm-studio";
  } catch (error) {
    console.error("Failed to read LM Studio API key from nightgauge config:", error);
    return "lm-studio";
  }
}

/**
 * Get the LM Studio request timeout in milliseconds from env or config.
 * Priority: NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS env → lm_studio.timeout_ms → 180000
 * @see Issue #2057 - Route pipeline stage execution through LM Studio
 */
export function getLmStudioTimeoutMs(workspaceRoot?: string): number {
  const envMs = process.env.NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS;
  if (envMs) {
    const parsed = parseInt(envMs, 10);
    if (!isNaN(parsed)) return parsed;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return 180_000;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return 180_000;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inLmStudio = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "lm_studio:" && !line.startsWith(" ")) {
        inLmStudio = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inLmStudio = false;
        }
      }

      if (inLmStudio) {
        const match = trimmed.match(/^timeout_ms:\s*(\d+)(?:\s+#.*)?$/);
        if (match) {
          return parseInt(match[1], 10);
        }
      }
    }

    return 180_000;
  } catch (error) {
    console.error("Failed to read LM Studio timeout from nightgauge config:", error);
    return 180_000;
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
export function getMinimumModel(
  stage: PipelineStage,
  workspaceRoot?: string
): DefaultModel | undefined {
  const validModels: DefaultModel[] = ["sonnet", "opus", "haiku", "fable"];

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
 * The fixed escalation path: haiku → sonnet → opus.
 * Returns the next more-capable model, or null if already at the ceiling.
 * @see Issue #1343 - Dynamic Model Escalation Engine
 */
const ESCALATION_PATH: DefaultModel[] = ["haiku", "sonnet", "opus"];

export function getEscalatedModel(currentModel: DefaultModel): DefaultModel | null {
  const idx = ESCALATION_PATH.indexOf(currentModel);
  if (idx === -1 || idx === ESCALATION_PATH.length - 1) return null; // at ceiling or unknown
  return ESCALATION_PATH[idx + 1];
}
