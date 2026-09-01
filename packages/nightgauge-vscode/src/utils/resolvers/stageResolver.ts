/**
 * Stage Resolver — stage-scoped configuration resolvers extracted from nightgaugeConfig.ts
 *
 * Provides utilities for resolving stage execution mode, stage budgets,
 * stage models, stage model overrides, stage models matrix, type overrides,
 * task type stage overrides, and stage effort levels from config/env.
 *
 * @see Issue #2742 - Refactor VSCode extract nightgaugeConfig.ts
 */

import * as fs from "node:fs";
import * as vscode from "vscode";
import {
  AutoModelSelector,
  EFFORT_LEVELS,
  TIER_BANDS,
  TIER_BAND_ALTERNATION,
  getModelDescriptor,
  isTierBand,
  resolveModelForAdapter,
  type IssueMetadata,
} from "@nightgauge/sdk";
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
 *
 * `max` is the top tier introduced with Opus 5 (#75). Opus 5 converts extra
 * effort into quality more reliably than earlier models, so the ladder is a
 * real dial rather than a formality — but the guidance is to start at the
 * default (`high`) and move in either direction on eval evidence, not to raise
 * the ceiling reflexively. No stage default changed when `max` was added.
 *
 * Whether a given model accepts a level is NOT encoded here — the registry's
 * `supported_efforts` is authoritative, so a model that lacks a level rejects
 * it loudly rather than being silently downgraded.
 */
export type ClaudeEffort = (typeof EFFORT_LEVELS)[number];

const VALID_CLAUDE_EFFORTS: readonly ClaudeEffort[] = EFFORT_LEVELS;

/**
 * Alternation for the config-file effort regexes, derived from the same array —
 * a hand-written `(low|medium|high|xhigh)` was one of the copies that fell
 * behind when `max` was added (#75, #394).
 */
const EFFORT_ALTERNATION = EFFORT_LEVELS.join("|");

/**
 * The band vocabulary for config-file model regexes and closed-set guards,
 * derived from the SDK's `TIER_BANDS` authority (#581) — the same idiom as
 * `EFFORT_ALTERNATION`. This file alone carried five hand-inlined
 * `["sonnet", "opus", "haiku", "fable"]` copies and two literal band
 * alternations; its own comments record two past incidents where a
 * three-band regex silently dropped `fable`.
 */
const BAND_ALTERNATION = TIER_BAND_ALTERNATION;

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
 * The effort levels a model accepts, read from the model registry — the single
 * authority on whether a model has an effort axis at all (#336).
 *
 * Three answers, all distinguishable, and the difference is load-bearing:
 *
 * - a **non-empty** array — the model takes `--effort`, at exactly these levels;
 * - **`[]`** — the model is registered and DECLARES no effort axis. Haiku has
 *   no extended thinking, so there is no level to request;
 * - **`undefined`** — unknown, which is spelled as descriptor-ABSENCE and
 *   nothing else: the registry has no entry for this model (an unregistered
 *   id, or a user-configured local ollama/lm-studio model whose catalog is
 *   unknowable here by design). The registry's own two states are `[]` and a
 *   ladder — the canonical schema requires `supported_efforts`, so a
 *   descriptor can never be silently uncharacterized.
 *
 * `[]` and `undefined` are not synonyms. The two consumers below deliberately
 * fail in opposite directions on them, so collapsing the states would silently
 * break one of the two.
 *
 * `model` may be a routing band (`"sonnet"`) or a concrete id
 * (`"claude-sonnet-5"`) — the registry resolves both.
 *
 * @see Issue #336 - the registry is the single authority on the effort axis
 */
export function supportedEffortsFor(model: string): readonly ClaudeEffort[] | undefined {
  return getModelDescriptor(model)?.supported_efforts;
}

/**
 * Whether `--effort` may be emitted for this model AT ALL — the coarse gate on
 * the flag, not on any particular level.
 *
 * Registry-derived (#336). This used to be a hardcoded `EFFORT_SUPPORTING_MODELS`
 * band set, which encoded "haiku has no effort axis" a second time, next to a
 * registry that (wrongly) claimed haiku accepted low/medium/high. Two copies of
 * one fact, one of them dead and free to rot; the registry is now the only copy.
 *
 * **Fails CLOSED on an unknown model.** No registry entry means no `--effort`,
 * matching the set this replaced (local models were never members) and keeping
 * a flag off a provider that may reject it outright.
 *
 * Contrast {@link assertEffortSupported}, which fails OPEN on that same missing
 * metadata: emitting a flag speculatively is a provider error, but blocking a
 * stage because the registry is silent is a self-inflicted outage.
 *
 * Fable note (#73): fable declares an effort axis so explicit config and the
 * frontier xhigh escalation actually reach the CLI — but the value passed to
 * Fable is conformed first (floored at Fable's documented `high` default) in
 * `conformEffortForFable`, so a Sonnet-era `medium` default can never downgrade
 * a frontier run below the model's own server-side default.
 *
 * @see Issue #1235 - Per-model effort level configuration
 * @see Issue #336 - registry authority, single source of truth
 */
export function modelSupportsEffort(model: string): boolean {
  const efforts = supportedEffortsFor(model);
  return efforts !== undefined && efforts.length > 0;
}

/**
 * Assert that a model accepts a specific effort LEVEL, per the registry's
 * `supported_efforts` (#75).
 *
 * `modelSupportsEffort` answers the coarser question — does this tier take an
 * `--effort` flag at all. Once the ladder gained `max` (Opus 5 only, at the
 * time of writing) that stopped being sufficient: `opus` supports effort, but
 * an older opus-band model does not support `max`.
 *
 * Deliberately throws rather than downgrading. A silent coercion here would
 * mean an operator who configured `max` gets `xhigh` and never learns —
 * precisely the class of drift that let the opus band run a superseded model
 * for a full release cycle (#74).
 *
 * Fails OPEN, unlike the emission gate above: both `undefined` (no registry
 * entry — local ollama/lm-studio models and unregistered ids) and `[]` (a model
 * that declares no effort axis) skip validation. There is nothing to validate
 * against in either case, and a stage must never be blocked for missing
 * metadata.
 *
 * The caller passes the ladder of the model it is actually DISPATCHING, not of
 * that model's band — a deprecated sibling declares a shorter ladder than the
 * band leader, and validating the leader's is how `--effort max` reached
 * `claude-opus-4-8` (#336). `[]` stays unreachable from the emission site,
 * which declines to pass a flag for a model with no axis at all.
 */
export function assertEffortSupported(
  effort: ClaudeEffort,
  modelId: string,
  supportedEfforts: readonly string[] | undefined,
  stage?: string
): void {
  if (!supportedEfforts || supportedEfforts.length === 0) return;
  if (supportedEfforts.includes(effort)) return;
  const where = stage ? ` for stage "${stage}"` : "";
  throw new Error(
    `Effort "${effort}"${where} is not supported by model "${modelId}" ` +
      `(supports: ${supportedEfforts.join(", ")}). ` +
      `Choose a supported level or route the stage to a model that accepts "${effort}".`
  );
}

/** Result of {@link checkAdapterEffortSupported}. */
export interface AdapterEffortPreflight {
  /** False exactly when the dispatch must fail closed before spawn. */
  ok: boolean;
  /** Set when `!ok` — names the model, the requested effort, and the ladder. */
  reason?: string;
  /**
   * Set when `ok` but unverified: the model has no registry descriptor, or
   * the value is outside the Nightgauge ladder (the adapter's own vocabulary
   * check handles it at dispatch — grok drops the flag, codex rejects the
   * value). Callers log it so the deferral is never silent.
   */
  warning?: string;
  /** The resolved model's declared ladder, when a descriptor was found. */
  supported?: readonly string[];
}

/**
 * Registry effort gate for an ADAPTER dispatch (#569) — the per-level
 * validation {@link assertEffortSupported} performs for the Claude path,
 * generalized to every adapter and to explicit provider-global efforts
 * (`NIGHTGAUGE_GROK_EFFORT`, codex `reasoning_effort`, …).
 *
 * Semantics (#336), enforced against the model the adapter will actually
 * dispatch, resolved the same way the adapter resolves it (band → the
 * provider's registry model, concrete id → itself):
 *
 * - no requested effort → nothing to enforce;
 * - vendor rungs below the Nightgauge ladder (`none`/`minimal`) collapse to
 *   `low` BEFORE the membership check (#523) — enforcement always runs on the
 *   normalized rung;
 * - a value outside the Nightgauge ladder after normalization is not an
 *   error HERE: the adapter's own vocabulary check handles it at dispatch
 *   (the grok adapters drop the flag and let the provider default apply; the
 *   codex adapter rejects the value loudly), and the returned `warning` keeps
 *   the deferral from being silent;
 * - a model with NO registry descriptor passes with a `warning`, never a hard
 *   failure — a stage must not be blocked because the registry is silent;
 * - `supported_efforts: []` is a positive declaration ("no effort axis"), so
 *   an explicit adapter effort against it fails closed. This deliberately
 *   differs from {@link assertEffortSupported}'s fail-open on `[]`: there the
 *   emission gate has already declined to pass a flag, so `[]` is unreachable
 *   with an effort in hand; here the effort is an explicit provider-global
 *   request that WOULD reach the CLI, and dropping it silently is exactly the
 *   downgrade #75 forbids;
 * - a normalized rung missing from the declared ladder fails closed with a
 *   reason naming the model, the requested effort, and the ladder.
 */
export function checkAdapterEffortSupported(
  adapter: string,
  effort: string | undefined,
  modelId: string | undefined,
  stage?: string
): AdapterEffortPreflight {
  const requested = effort?.trim().toLowerCase();
  if (!requested) return { ok: true };

  const normalized =
    requested === "none" || requested === "minimal"
      ? "low"
      : (EFFORT_LEVELS as readonly string[]).includes(requested)
        ? requested
        : undefined;
  if (!normalized) {
    return {
      ok: true,
      warning:
        `effort "${requested}" is not on the Nightgauge ladder ` +
        `(${EFFORT_LEVELS.join("|")}) — deferring to the ${adapter} adapter's own ` +
        `vocabulary check at dispatch (it drops the flag or rejects the value; ` +
        `it is never forwarded unchecked)`,
    };
  }

  const trimmedModel = modelId?.trim();
  const descriptor = trimmedModel ? resolveModelForAdapter(adapter, trimmedModel) : undefined;
  if (!descriptor) {
    return {
      ok: true,
      warning:
        `model "${trimmedModel || "(adapter default)"}" has no registry descriptor — ` +
        `cannot verify effort "${requested}" against supported_efforts; passing through`,
    };
  }

  const ladder = descriptor.supported_efforts;
  const where = stage ? ` for stage "${stage}"` : "";
  if (ladder.length === 0) {
    return {
      ok: false,
      supported: ladder,
      reason:
        `Effort "${requested}"${where} is not supported by model "${descriptor.id}": ` +
        `the model declares no effort axis (supported_efforts: []). ` +
        `Unset the explicit effort or route the stage to a model with an effort ladder.`,
    };
  }
  if (!(ladder as readonly string[]).includes(normalized)) {
    const note = normalized !== requested ? ` (normalized to "${normalized}")` : "";
    return {
      ok: false,
      supported: ladder,
      reason:
        `Effort "${requested}"${note}${where} is not supported by model "${descriptor.id}" ` +
        `(supports: ${ladder.join(", ")}). ` +
        `Choose a supported level or route the stage to a model that accepts "${requested}".`,
    };
  }
  return { ok: true, supported: ladder };
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
 *   source): `high` — the registry's own `effort_default` for the fable band.
 *   This was `xhigh` until #1274. Anthropic's Fable 5.1 guidance is to start
 *   at `high` and move up only on measured gain: at `xhigh`/`max` the model
 *   drafts long deliverables (complete code files) inside thinking and then
 *   writes them again, roughly doubling output tokens for work the router
 *   reaches on every L/XL planning/dev issue. `xhigh` stays reachable by an
 *   explicit pin, which is now the only way to ask for it.
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
    return { effort: "high", coerced: resolvedEffort !== "high" && resolvedEffort !== undefined };
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
/**
 * The per-stage `NIGHTGAUGE_PIPELINE_STAGE_MODEL_{STAGE}` override, or
 * undefined when it is unset or not one of the four registry bands.
 *
 * Split out of `getStageModel` (#340) because this override resolves in a
 * different PLACE than the rest of the explicit chain: it wins in every
 * performance mode — ahead of the Maximum pin, which `resolveModel` Step 0
 * would otherwise return first — while `pipeline.stage_models` sits behind that
 * pin. Its Go pair is `stageEnvModel` (internal/orchestrator/dispatch_routing.go),
 * band validation included: a value one resolver drops and the other dispatches
 * is the drift #340 removed.
 */
export function getStageEnvModel(stage: PipelineStage): DefaultModel | undefined {
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_MODEL_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envModel = process.env[envKey]?.trim();
  return envModel && isTierBand(envModel) ? envModel : undefined;
}

export function getStageModel(
  stage: PipelineStage,
  workspaceRoot?: string
): DefaultModel | undefined {
  // 1. ALWAYS check environment variable first (highest priority, all modes)
  const envModel = getStageEnvModel(stage);
  if (envModel) {
    return envModel;
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
              new RegExp(`^([a-z][-a-z]*):\\s*['"]?(${BAND_ALTERNATION})['"]?(?:\\s+#.*)?$`)
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
    const validModels: readonly string[] = TIER_BANDS; // derived from the SDK band authority (#581)
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
    const validModels: readonly string[] = TIER_BANDS; // derived from the SDK band authority (#581)
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
          new RegExp(`^([a-z][-a-z]*):\\s*['"]?(${EFFORT_ALTERNATION})['"]?(?:\\s+#.*)?$`)
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
          new RegExp(`^default_effort:\\s*['"]?(${EFFORT_ALTERNATION})['"]?(?:\\s+#.*)?$`)
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
