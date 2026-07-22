/**
 * Stage Resolver — stage-scoped configuration resolvers extracted from incrediConfig.ts
 *
 * Provides utilities for resolving stage execution mode, stage budgets,
 * stage models, stage model overrides, stage models matrix, type overrides,
 * task type stage overrides, and stage effort levels from config/env.
 *
 * @see Issue #2742 - Refactor VSCode extract incrediConfig.ts
 */

import * as fs from "node:fs";
import * as vscode from "vscode";
import { AutoModelSelector, type IssueMetadata } from "@nightgauge/sdk";
import type { PipelineStage } from "@nightgauge/sdk";
import { resolveConfigPathSync, logDeprecationWarning } from "../configPathResolver";
import { readEffectiveConfigTextSync } from "../mergedConfigReader";
import { DEFAULT_SIZE_AWARE_BUDGETS, type SizeLabel } from "../budgetEnforcer";
import { getModelRoutingMode, type DefaultModel } from "./modelResolver";

/**
 * Stage execution mode for single-stage runs
 */
export type StageExecutionMode = "headless" | "interactive";

/**
 * Default stage execution mode
 */
export const DEFAULT_STAGE_EXECUTION_MODE: StageExecutionMode = "headless";

/**
 * Get the default stage execution mode from config or environment.
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_PIPELINE_DEFAULT_MODE
 * 2. Config file: pipeline.default_mode
 * 3. Default: 'headless'
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns The default stage execution mode
 *
 * @see Issue #499 - Mode selection UX
 * @see docs/INTERACTIVE_MODE.md
 */
export function getDefaultStageExecutionMode(workspaceRoot?: string): StageExecutionMode {
  // Check environment variable first
  const envMode = process.env.NIGHTGAUGE_PIPELINE_DEFAULT_MODE;
  if (envMode === "headless" || envMode === "interactive") {
    return envMode;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_STAGE_EXECUTION_MODE;
  }

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_STAGE_EXECUTION_MODE;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read and parse config file (simple line parsing)
    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect pipeline: section
      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      // Exit section on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
        }
      }

      // Parse pipeline config values
      if (inPipeline) {
        const match = trimmed.match(
          /^default_mode:\s*['"]?(headless|interactive)['"]?(?:\s+#.*)?$/
        );
        if (match) {
          return match[1] as StageExecutionMode;
        }
      }
    }

    return DEFAULT_STAGE_EXECUTION_MODE;
  } catch (error) {
    console.error("Failed to read default mode from nightgauge config:", error);
    return DEFAULT_STAGE_EXECUTION_MODE;
  }
}

/**
 * Stage budget configuration.
 */
export interface StageBudget {
  /** Maximum expected cost in USD before warning */
  maxCostUsd: number;
}

/**
 * Get the token budget for a specific pipeline stage.
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_PIPELINE_STAGE_BUDGET_{STAGE_UPPER}
 *    (e.g., NIGHTGAUGE_PIPELINE_STAGE_BUDGET_FEATURE_DEV=5.00)
 * 2. Config file: pipeline.stage_budgets.{stage} (flat number or per-size)
 * 3. Size-aware default from DEFAULT_SIZE_AWARE_BUDGETS
 *
 * @param stage - The pipeline stage to get budget for
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @param sizeLabel - Issue size label for size-aware lookup (optional, defaults to M)
 * @returns The stage budget or undefined if stage has no budget
 *
 * @see Issue #638 - Pipeline token efficiency
 * @see Issue #835 - Enforce hard budget limits
 */
export function getStageBudget(
  stage: PipelineStage,
  workspaceRoot?: string,
  sizeLabel?: SizeLabel
): StageBudget | undefined {
  const size = sizeLabel ?? "M";

  // Check environment variable first (flat override, not size-aware)
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_BUDGET_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envBudget = process.env[envKey];
  if (envBudget) {
    const parsed = Number.parseFloat(envBudget);
    if (!Number.isNaN(parsed) && parsed > 0) {
      return { maxCostUsd: parsed };
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
        let inStageBudgets = false;
        let inTargetStage = false;

        const stageKey = stage;

        for (const line of lines) {
          const trimmed = line.trim();

          if (trimmed === "pipeline:") {
            inPipeline = true;
            continue;
          }

          if (inPipeline && trimmed === "stage_budgets:") {
            inStageBudgets = true;
            continue;
          }

          // Detect section exit
          if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
            if (!line.startsWith(" ")) {
              inPipeline = false;
              inStageBudgets = false;
              inTargetStage = false;
            }
          }

          // Match the target stage key with optional inline value
          if (inStageBudgets) {
            // Flat number: "feature-dev: 10.0"
            const flatMatch = trimmed.match(/^([a-z][-a-z]*):\s*([\d.]+)$/);
            if (flatMatch && flatMatch[1] === stageKey) {
              const parsed = Number.parseFloat(flatMatch[2]);
              if (!Number.isNaN(parsed) && parsed > 0) {
                return { maxCostUsd: parsed };
              }
            }
            // Object form: "feature-dev:"
            const stageMatch = trimmed.match(/^([a-z][-a-z]*):$/);
            if (stageMatch) {
              inTargetStage = stageMatch[1] === stageKey;
              continue;
            }
          }

          // Match size keys or max_cost_usd within the target stage object
          if (inTargetStage) {
            // Size-aware: "M: 12.0"
            const sizeMatch = trimmed.match(/^(XS|S|M|L|XL):\s*([\d.]+)$/);
            if (sizeMatch && sizeMatch[1] === size) {
              const parsed = Number.parseFloat(sizeMatch[2]);
              if (!Number.isNaN(parsed) && parsed > 0) {
                return { maxCostUsd: parsed };
              }
            }
            // Legacy max_cost_usd
            const costMatch = trimmed.match(/^max_cost_usd:\s*([\d.]+)$/);
            if (costMatch) {
              const parsed = Number.parseFloat(costMatch[1]);
              if (!Number.isNaN(parsed) && parsed > 0) {
                return { maxCostUsd: parsed };
              }
            }
          }
        }
      }
    } catch (error) {
      console.error("Failed to read stage budget from nightgauge config:", error);
    }
  }

  // Fall back to size-aware defaults
  const defaults = DEFAULT_SIZE_AWARE_BUDGETS[stage];
  if (defaults) {
    return { maxCostUsd: defaults[size] };
  }
  return undefined;
}

/**
 * Claude effort level type.
 *
 * `xhigh` exists for the frontier tier: Anthropic documents `high` as Fable
 * 5's default and `xhigh` for the most capability-sensitive work (#73). The
 * claude CLI accepts it for Opus too (verified on 2.1.186).
 */
export type ClaudeEffort = "low" | "medium" | "high" | "xhigh";

const VALID_CLAUDE_EFFORTS: ClaudeEffort[] = ["low", "medium", "high", "xhigh"];

/**
 * Default per-stage model overrides — Sonnet 4.6 era cost-optimized strategy.
 *
 * - **Sonnet**: Core reasoning stages (planning, development, validation)
 * - **Haiku**: Lightweight stages with structured, template-driven tasks
 *   (issue extraction, PR template filling, merge flow) — ~67% cost savings
 *   vs Sonnet on these stages
 *
 * null = use the global default model (no override).
 *
 * These are the AUTHORITATIVE per-stage model defaults. The `model:` tier name
 * in a SKILL.md frontmatter is advisory only and is NOT read by the execution
 * layer — resolution is env → config `pipeline.stage_models` → this map →
 * AutoModelSelector, then mapped to a concrete per-adapter model at spawn time
 * via the provider-aware registry (resolveModelForAdapter, #56). A frontmatter value may
 * therefore differ from the effective default here. See docs/SKILL_PORTABILITY.md
 * §2 and docs/CONFIGURATION.md (Issue #4029).
 *
 * @see Issue #638 - Pipeline token efficiency
 * @see Issue #725 - Haiku model routing for lightweight stages
 * @see Issue #944 - Recommended default config for Sonnet 4.6 era
 * @see Issue #4021 - Provider-aware model routing / validation
 */
const DEFAULT_STAGE_MODELS: Partial<Record<PipelineStage, DefaultModel>> = {
  "issue-pickup": "haiku",
  "feature-planning": "sonnet",
  "feature-dev": "sonnet",
  "feature-validate": "sonnet",
  "pr-create": "haiku",
  // sonnet, not haiku (#197): the pr-merge LLM path only runs when the
  // deterministic runner punted — i.e. exclusively on the judgment-heavy
  // instances (blocked merges, failing checks, dirty state). Issue size
  // does not predict punt difficulty.
  "pr-merge": "sonnet",
};

/**
 * Default per-stage effort overrides for Sonnet 4.6 era.
 *
 * - **medium**: Stages requiring thorough analysis (planning, development)
 * - **low**: Stages with structured validation patterns (validate)
 * - **undefined** (omitted): Lightweight stages use Claude default
 *
 * @see Issue #944 - Recommended default config
 */
export const DEFAULT_STAGE_EFFORTS: Partial<Record<PipelineStage, ClaudeEffort>> = {
  "feature-planning": "medium",
  "feature-dev": "medium",
  "feature-validate": "low",
};

/**
 * Models that support the Claude Code `--effort` flag.
 * Haiku does not support extended thinking, so `--effort` is silently skipped.
 * Update this list as new models gain effort support.
 *
 * Fable note (#73): fable is in this set so explicit config and the frontier
 * xhigh escalation actually reach the CLI — but the value passed to Fable is
 * conformed first (floored at Fable's documented `high` default) in
 * `conformEffortForFable`, so a Sonnet-era `medium` default can never
 * downgrade a frontier run below the model's own server-side default.
 *
 * @see Issue #1235 - Per-model effort level configuration
 */
export const EFFORT_SUPPORTING_MODELS: ReadonlySet<DefaultModel> = new Set<DefaultModel>([
  "sonnet",
  "opus",
  "fable",
]);

/**
 * Returns true when the given model supports the `--effort` flag.
 *
 * @see Issue #1235 - Per-model effort level configuration
 */
export function modelSupportsEffort(model: DefaultModel): boolean {
  return EFFORT_SUPPORTING_MODELS.has(model);
}

/**
 * Conform a resolved effort to Fable 5's published guidance (#73).
 *
 * Anthropic documents `high` as Fable's server-side default and `xhigh` for
 * the most capability-sensitive work. Every effort default and derivation in
 * this file predates Fable and is calibrated for Sonnet/Opus, so passing
 * those values through unmodified would actively downgrade a frontier run
 * below the model's own default (e.g. `DEFAULT_STAGE_EFFORTS["feature-dev"]`
 * is `medium`).
 *
 * Rules, in order:
 * - An explicit per-stage effort (env var or `model_routing.stage_efforts`)
 *   is honored, but floored at `high` — an operator's Sonnet-era `medium` is
 *   model-blind config, not a deliberate frontier downgrade. The coercion is
 *   reported via `coerced` so the caller can log it.
 * - No explicit effort + router-selected fable (`auto` / `auto-router`
 *   source): `xhigh`. The router only reaches fable on L/XL planning/dev —
 *   by definition the "most capability-sensitive" case the guidance names.
 * - Otherwise (deliberate fable pin or default with no explicit effort):
 *   `undefined`, which omits `--effort` and lets Fable's own `high` default
 *   apply.
 */
export function conformEffortForFable(
  resolvedEffort: ClaudeEffort | undefined,
  explicitEffort: ClaudeEffort | undefined,
  modelSource: string | undefined
): { effort: ClaudeEffort | undefined; coerced: boolean } {
  if (explicitEffort !== undefined) {
    if (explicitEffort === "low" || explicitEffort === "medium") {
      return { effort: "high", coerced: true };
    }
    return { effort: explicitEffort, coerced: false };
  }
  if (modelSource === "auto" || modelSource === "auto-router") {
    return { effort: "xhigh", coerced: resolvedEffort !== "xhigh" && resolvedEffort !== undefined };
  }
  return { effort: undefined, coerced: false };
}

// pr-merge removed (#197): its LLM path runs only on deterministic punts —
// the judgment-heavy cases — so a blanket low-effort hint was wrong for
// every instance that actually reaches the model.
const LIGHTWEIGHT_EFFORT_STAGES = new Set<PipelineStage>(["issue-pickup", "pr-create"]);

function mapComplexityToEffort(
  stage: PipelineStage,
  complexity: "XS" | "S" | "M" | "L" | "XL"
): ClaudeEffort {
  if (LIGHTWEIGHT_EFFORT_STAGES.has(stage)) {
    return "low";
  }
  if (complexity === "M") {
    return "medium";
  }
  if (complexity === "L" || complexity === "XL") {
    return "high";
  }
  return "low";
}

/**
 * Get the model override for a specific pipeline stage.
 *
 * Behavior depends on the model routing mode:
 * - **manual** (default): env var > config stage_models > DEFAULT_STAGE_MODELS
 * - **automatic**: env var > undefined (defer to AutoModelSelector for all stages)
 * - **hybrid**: env var > config stage_models override > undefined (defer for non-overridden)
 *
 * In all modes, env var overrides take highest priority.
 * Returning undefined signals "use AutoModelSelector" to the caller (skillRunner.ts).
 * In manual mode, undefined is never returned (falls back to DEFAULT_STAGE_MODELS).
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_PIPELINE_STAGE_MODEL_{STAGE_UPPER}
 *    (e.g., NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP=haiku)
 * 2. Config file: pipeline.stage_models.{stage} (manual/hybrid only)
 * 3. Default from DEFAULT_STAGE_MODELS (manual only)
 * 4. undefined (automatic/hybrid: defer to AutoModelSelector)
 *
 * @param stage - The pipeline stage
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns The model to use for this stage, or undefined to defer to AutoModelSelector
 *
 * @see Issue #638 - Pipeline token efficiency
 * @see Issue #731 - Model routing configuration modes
 */
export function getStageModel(
  stage: PipelineStage,
  workspaceRoot?: string
): DefaultModel | undefined {
  const validModels: DefaultModel[] = ["sonnet", "opus", "haiku", "fable"];

  // 1. ALWAYS check environment variable first (highest priority, all modes)
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_MODEL_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envModel = process.env[envKey];
  if (envModel && validModels.includes(envModel as DefaultModel)) {
    return envModel as DefaultModel;
  }

  // 2. Determine routing mode
  const mode = getModelRoutingMode(workspaceRoot);

  // 3. In automatic mode, return undefined for all stages (defer to AutoModelSelector)
  if (mode === "automatic") {
    return undefined;
  }

  // 4. Check config file for explicit per-stage override
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
        let inStageModels = false;

        for (const line of lines) {
          const trimmed = line.trim();

          if (trimmed === "pipeline:") {
            inPipeline = true;
            continue;
          }

          if (inPipeline && trimmed === "stage_models:") {
            inStageModels = true;
            continue;
          }

          // Detect section exit
          if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
            if (!line.startsWith(" ")) {
              inPipeline = false;
              inStageModels = false;
            }
          }

          // Match stage model entries (e.g., "issue-pickup: haiku")
          if (inStageModels) {
            const modelMatch = trimmed.match(
              /^([a-z][-a-z]*):\s*['"]?(sonnet|opus|haiku|fable)['"]?(?:\s+#.*)?$/
            );
            if (modelMatch && modelMatch[1] === stage) {
              return modelMatch[2] as DefaultModel;
            }
          }
        }
      }
    } catch (error) {
      console.error("Failed to read stage model from nightgauge config:", error);
    }
  }

  // 5. In hybrid mode with no explicit override, return undefined (defer to AutoModelSelector)
  if (mode === "hybrid") {
    return undefined;
  }

  // 6. Manual mode: fall back to defaults
  return DEFAULT_STAGE_MODELS[stage];
}

/**
 * Get the adaptive policy routing override for a specific stage.
 *
 * Reads model_routing.stage_overrides.<stage> from config.yaml.
 * Returns undefined if no override is set for the stage.
 *
 * Called by resolveModel() before experiment/AutoModelSelector to apply
 * long-horizon policy engine decisions (Issue #1571).
 *
 * @param stage - The pipeline stage
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns The override model for this stage, or undefined if none is set
 *
 * @see Issue #1571 - Handle routing-override decisions in applyPolicyDecisions()
 */
export function getStageOverrideModel(
  stage: PipelineStage,
  workspaceRoot?: string
): DefaultModel | undefined {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return undefined;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return undefined;
    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    const validModels: DefaultModel[] = ["sonnet", "opus", "haiku", "fable"];
    let inModelRouting = false;
    let inStageOverrides = false;

    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }
      if (
        inModelRouting &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inModelRouting = false;
        inStageOverrides = false;
      }
      if (inModelRouting && trimmed === "stage_overrides:") {
        inStageOverrides = true;
        continue;
      }
      if (inStageOverrides) {
        if (!line.startsWith("  ") || trimmed === "") {
          inStageOverrides = false;
          continue;
        }
        const match = trimmed.match(/^([a-z][-a-z]*):\s*['"]?(sonnet|opus|haiku|fable)['"]?/);
        if (match && match[1] === stage && validModels.includes(match[2] as DefaultModel)) {
          return match[2] as DefaultModel;
        }
      }
    }
  } catch {
    // Non-critical — return undefined
  }
  return undefined;
}

/**
 * Get the stage_models_matrix configuration for AutoModelSelector.
 *
 * Reads model_routing.stage_models_matrix from config.yaml and returns
 * it in the shape expected by AutoModelSelectorConfig.stageMatrix.
 *
 * @returns Partial matrix or undefined if not configured
 * @see Issue #1590 - Configurable stage × size model routing
 */
export function getStageModelsMatrix(
  workspaceRoot?: string
): Record<string, Record<string, string>> | undefined {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return undefined;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return undefined;
    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const configContent = readEffectiveConfigTextSync(pathResult);
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const yaml = require("yaml") as { parse: (s: string) => unknown };
    const parsed = yaml.parse(configContent) as Record<string, unknown> | null;
    if (!parsed) return undefined;

    const modelRouting = parsed.model_routing as Record<string, unknown> | undefined;
    if (!modelRouting?.stage_models_matrix) return undefined;

    const raw = modelRouting.stage_models_matrix as Record<string, Record<string, string>>;
    const validModels = ["sonnet", "opus", "haiku", "fable"];
    const validCategories = ["planning", "dev", "validate", "lightweight", "merge"];
    const validSizes = ["XS", "S", "M", "L", "XL"];

    const result: Record<string, Record<string, string>> = {};
    for (const [category, sizes] of Object.entries(raw)) {
      if (!validCategories.includes(category) || typeof sizes !== "object") continue;
      const sizeMap: Record<string, string> = {};
      for (const [size, model] of Object.entries(sizes)) {
        if (validSizes.includes(size) && validModels.includes(String(model))) {
          sizeMap[size] = String(model);
        }
      }
      if (Object.keys(sizeMap).length > 0) {
        result[category] = sizeMap;
      }
    }
    return Object.keys(result).length > 0 ? result : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Get type-aware model overrides from config.yaml.
 *
 * Reads model_routing.type_overrides from config.yaml and returns it in the
 * shape expected by AutoModelSelectorConfig.typeOverrides.
 *
 * Config format:
 * ```yaml
 * model_routing:
 *   type_overrides:
 *     docs:
 *       planning: opus
 *       dev: opus
 *     chore:
 *       dev: haiku
 *       validate: haiku
 * ```
 *
 * @returns Partial type override map, or undefined if not configured
 * @since Issue #2400
 */
export function getTypeOverrides(
  workspaceRoot?: string
): Record<string, Record<string, string>> | undefined {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return undefined;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return undefined;
    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const configContent = readEffectiveConfigTextSync(pathResult);
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const yaml = require("yaml") as { parse: (s: string) => unknown };
    const parsed = yaml.parse(configContent) as Record<string, unknown> | null;
    if (!parsed) return undefined;

    const modelRouting = parsed.model_routing as Record<string, unknown> | undefined;
    if (!modelRouting?.type_overrides) return undefined;

    const raw = modelRouting.type_overrides as Record<string, Record<string, string>>;
    const validModels = ["sonnet", "opus", "haiku", "fable"];
    const validTypes = ["feature", "bug", "docs", "chore", "refactor", "epic"];
    const validCategories = [
      "classification",
      "planning",
      "dev",
      "validate",
      "lightweight",
      "merge",
    ];

    const result: Record<string, Record<string, string>> = {};
    for (const [issueType, stages] of Object.entries(raw)) {
      if (!validTypes.includes(issueType) || typeof stages !== "object") continue;
      const stageMap: Record<string, string> = {};
      for (const [stage, model] of Object.entries(stages)) {
        if (validCategories.includes(stage) && validModels.includes(String(model))) {
          stageMap[stage] = String(model);
        }
      }
      if (Object.keys(stageMap).length > 0) {
        result[issueType] = stageMap;
      }
    }
    return Object.keys(result).length > 0 ? result : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Get task type → stage profile overrides from config.yaml.
 *
 * Reads routing.task_type_stages from config.yaml. Each entry maps a task type
 * to the list of pipeline stages that should execute for that type.
 *
 * Config format:
 * ```yaml
 * routing:
 *   task_type_stages:
 *     docs-only:
 *       - issue-pickup
 *       - feature-dev
 *       - pr-create
 *       - pr-merge
 *     chore:
 *       - issue-pickup
 *       - feature-dev
 *       - feature-validate
 *       - pr-create
 *       - pr-merge
 * ```
 *
 * @returns Partial task type → stage list map, or undefined if not configured
 * @since Issue #2402
 */
export function getTaskTypeStageOverrides(
  workspaceRoot?: string
): Record<string, string[]> | undefined {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return undefined;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return undefined;
    if (pathResult.isLegacy) logDeprecationWarning(pathResult.path);

    const configContent = readEffectiveConfigTextSync(pathResult);
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const yaml = require("yaml") as { parse: (s: string) => unknown };
    const parsed = yaml.parse(configContent) as Record<string, unknown> | null;
    if (!parsed) return undefined;

    const routing = parsed.routing as Record<string, unknown> | undefined;
    if (!routing?.task_type_stages) return undefined;

    const raw = routing.task_type_stages as Record<string, string[]>;
    const validStages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];
    const validTypes = [
      "feature",
      "bugfix",
      "docs-only",
      "chore",
      "refactor",
      "verification",
      "spike",
    ];

    const result: Record<string, string[]> = {};
    for (const [taskType, stages] of Object.entries(raw)) {
      if (!validTypes.includes(taskType) || !Array.isArray(stages)) continue;
      const validatedStages = stages.filter((s) => validStages.includes(String(s)));
      if (validatedStages.length > 0) {
        result[taskType] = validatedStages;
      }
    }
    return Object.keys(result).length > 0 ? result : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Check whether automatic effort derivation is enabled.
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_MODEL_ROUTING_EFFORT_AUTO
 * 2. Config file: model_routing.effort_auto
 * 3. Default: false
 */
function isEffortAutoEnabled(workspaceRoot?: string): boolean {
  const envValue = process.env.NIGHTGAUGE_MODEL_ROUTING_EFFORT_AUTO;
  if (envValue === "true") {
    return true;
  }
  if (envValue === "false") {
    return false;
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
    let inModelRouting = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inModelRouting = false;
        }
      }

      if (inModelRouting) {
        const match = trimmed.match(/^effort_auto:\s*(true|false)$/);
        if (match) {
          return match[1] === "true";
        }
      }
    }

    return false;
  } catch (error) {
    console.error("Failed to read effort_auto from nightgauge config:", error);
    return false;
  }
}

/**
 * Get the explicit effort override for a specific pipeline stage.
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_PIPELINE_STAGE_EFFORT_{STAGE_UPPER}
 * 2. Config file: model_routing.stage_efforts.{stage}
 * 3. Default: undefined
 */
export function getExplicitStageEffort(
  stage: PipelineStage,
  workspaceRoot?: string
): ClaudeEffort | undefined {
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_EFFORT_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envEffort = process.env[envKey];
  if (envEffort && VALID_CLAUDE_EFFORTS.includes(envEffort as ClaudeEffort)) {
    return envEffort as ClaudeEffort;
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
    let inModelRouting = false;
    let inStageEfforts = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }

      if (inModelRouting && trimmed === "stage_efforts:") {
        inStageEfforts = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inModelRouting = false;
          inStageEfforts = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inStageEfforts = false;
        }
      }

      if (inStageEfforts) {
        const effortMatch = trimmed.match(
          /^([a-z][-a-z]*):\s*['"]?(low|medium|high|xhigh)['"]?(?:\s+#.*)?$/
        );
        if (effortMatch && effortMatch[1] === stage) {
          return effortMatch[2] as ClaudeEffort;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read stage effort from nightgauge config:", error);
    return undefined;
  }
}

/**
 * Read `model_routing.default_effort` from env or config file.
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT
 * 2. Config file: model_routing.default_effort
 * 3. undefined
 *
 * @see Issue #1235 - Per-model effort level configuration
 */
export function getModelDefaultEffort(workspaceRoot?: string): ClaudeEffort | undefined {
  const envEffort = process.env.NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT;
  if (envEffort && VALID_CLAUDE_EFFORTS.includes(envEffort as ClaudeEffort)) {
    return envEffort as ClaudeEffort;
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
    let inModelRouting = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "model_routing:") {
        inModelRouting = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inModelRouting = false;
        }
      }

      if (inModelRouting) {
        const match = trimmed.match(
          /^default_effort:\s*['"]?(low|medium|high|xhigh)['"]?(?:\s+#.*)?$/
        );
        if (match) {
          return match[1] as ClaudeEffort;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read model_routing.default_effort from nightgauge config:", error);
    return undefined;
  }
}

/**
 * Resolve Claude effort for a stage.
 *
 * Resolution order:
 * 1. Explicit per-stage env/config override (stage_efforts)
 * 2. Per-model default effort (model_routing.default_effort)
 * 3. Manual mode: DEFAULT_STAGE_EFFORTS fallback
 * 4. Deterministic auto-derivation (automatic/hybrid + effort_auto=true)
 * 5. undefined (omit --effort)
 *
 * @see Issue #1235 - Per-model effort level configuration
 */
export function getStageEffort(
  stage: PipelineStage,
  workspaceRoot?: string,
  issueMetadata?: IssueMetadata
): ClaudeEffort | undefined {
  const explicitEffort = getExplicitStageEffort(stage, workspaceRoot);
  if (explicitEffort !== undefined) {
    return explicitEffort;
  }

  const modelDefaultEffort = getModelDefaultEffort(workspaceRoot);
  if (modelDefaultEffort !== undefined) {
    return modelDefaultEffort;
  }

  const mode = getModelRoutingMode(workspaceRoot);
  if (mode === "manual") {
    return DEFAULT_STAGE_EFFORTS[stage];
  }
  if (!isEffortAutoEnabled(workspaceRoot) || !issueMetadata) {
    return undefined;
  }

  try {
    const matrixConfig = getStageModelsMatrix(workspaceRoot);
    const selector = new AutoModelSelector(
      matrixConfig
        ? {
            stageMatrix: matrixConfig as Partial<Record<string, Partial<Record<string, string>>>>,
          }
        : undefined
    );
    const selectorWithEffort = selector as AutoModelSelector & {
      deriveEffort?: (stageName: string, metadata: IssueMetadata) => { effort: ClaudeEffort };
    };
    if (typeof selectorWithEffort.deriveEffort === "function") {
      return selectorWithEffort.deriveEffort(stage, issueMetadata).effort;
    }
    // Backward-compatible fallback when running against an older SDK runtime.
    const complexity = selector.selectModel(stage, issueMetadata).complexity;
    return mapComplexityToEffort(stage, complexity);
  } catch (error) {
    console.error(`Failed to auto-derive stage effort for ${stage} from issue metadata:`, error);
    return undefined;
  }
}
