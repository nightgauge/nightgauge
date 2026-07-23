/**
 * Adapter Resolver — per-stage execution adapter selection with source attribution.
 *
 * Mirrors the shape of `resolveModel` (model selection) but for adapter dispatch.
 * Returns the adapter the caller should use plus an `AdapterSource` tag the
 * dispatcher / run-history can record.
 *
 * Precedence (highest → lowest):
 *   1. `NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE>` env var → `"env"`
 *   2. `pipeline.stage_adapters.<stage>` from raw YAML       → `"stage-config"`
 *   3. Effective explicit `ui.core.adapter` selection         → `"global-config"`
 *   4. AutoProviderRouter when no adapter was selected        → `"auto-router"`
 *   5. Hardcoded default (`claude`)                           → `"default"`
 *
 * This resolver is pure: it does NOT validate adapter auth (B3 / #3222 owns
 * that) and is NOT yet wired into `skillRunner.ts` (B4 / #3223 will do that).
 *
 * @see Issue #3221 — B2 resolveStageAdapter resolver
 * @see Issue #3212 — Epic: per-stage adapter selection
 * @see Issue #3220 — B1 typed schema (resolver decoupled — reads raw YAML)
 * @see Issue #3222 — B3 validateAdapterAuth (already merged)
 * @see Issue #3223 — B4 SkillRunner dispatcher integration (consumer)
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import { z } from "zod";
import type { ComplexityLabel, PipelineStage } from "@nightgauge/sdk";
import {
  AutoProviderRouter,
  type AutoRouterContext,
  type AutoRouterDecision,
  type AutoRouterMode,
  type AutoRouterWeights,
  type RouterExecutionAdapter,
} from "@nightgauge/sdk";
import {
  type ExecutionAdapter,
  DEFAULT_EXECUTION_ADAPTER,
  VALID_ADAPTERS,
  readAdapterFromFile,
} from "./modelResolver";
import { resolveConfigPathSync, logDeprecationWarning } from "../configPathResolver";
import { readEffectiveConfigTextSync } from "../mergedConfigReader";

// ============================================================================
// Public types
// ============================================================================

/**
 * Source tag explaining which precedence step produced the resolved adapter.
 *
 * Single canonical Zod enum so consumers (history schema, dashboards, telemetry)
 * stay in sync with the resolver — adding a value here propagates everywhere.
 *
 * - `"env"`           — `NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE>` set.
 * - `"stage-config"`  — `pipeline.stage_adapters.<stage>` set in YAML.
 * - `"global-config"` — `ui.core.adapter` configured (env / bridge / file).
 * - `"auto-router"`   — AutoProviderRouter selected an authenticated adapter
 *                       because no explicit adapter selection was configured.
 * - `"fallback"`      — Resolved adapter failed prereq validation, and a
 *                       candidate from `pipeline.adapter_fallback_chain` was
 *                       substituted. Issue #3223.
 * - `"default"`       — Fell through to the hardcoded `claude` default.
 */
export const AdapterSourceSchema = z.enum([
  "env",
  "stage-config",
  "global-config",
  "auto-router",
  "fallback",
  "default",
]);
export type AdapterSource = z.infer<typeof AdapterSourceSchema>;

/** Resolved adapter plus the source step that produced it. */
export interface AdapterDecision {
  adapter: ExecutionAdapter;
  source: AdapterSource;
  /**
   * Router rationale string, populated only when `source === "auto-router"`
   * (Issue #3230). Surfaced by `skillRunner` in the per-stage info log so
   * operators can see why the router chose a particular adapter.
   */
  rationale?: string;
  /**
   * Model the router suggested for this adapter, populated only when
   * `source === "auto-router"` (Issue #3230). Optional because the resolver
   * does not enforce the model — the model resolution chain still runs
   * downstream — but consumers may use it as a hint.
   */
  routerModel?: string;
  /**
   * Every adapter the dispatcher tried at stage start, in order (Issue
   * #3231). Length 1 — no fallback walked (only the primary was checked).
   * Length ≥ 2 — primary failed prereq and candidates were attempted; the
   * winner is the last element when the walk succeeded, or every candidate
   * tried when the walk exhausted the chain (in which case the dispatcher
   * emits `[stage:no-adapter-available]`). Optional so unit tests of the
   * pure precedence chain can construct decisions without populating it.
   */
  adapterFallbackChainUsed?: ExecutionAdapter[];
}

// ============================================================================
// Public API
// ============================================================================

/**
 * Resolve the execution adapter for a specific pipeline stage with source
 * attribution.
 *
 * @param stage         — Pipeline stage name (e.g. `"feature-dev"`).
 * @param workspaceRoot — Workspace root for config-file lookup. Optional;
 *                        falls back to the first VSCode workspace folder.
 * @param env           — Environment override (defaults to `process.env`).
 *                        Exposed for tests and call-site overrides; mirrors
 *                        the shape of `resolveModel`.
 *
 * @see resolveModel in `skillRunner.ts` for the parallel model-resolution shape.
 */
/**
 * Caller-supplied options that drive the AutoProviderRouter step (Issue #3230).
 *
 * The resolver itself stays sync and pure; consumers (skillRunner) build this
 * options bag from VSCode-side services (`validateAdapterPrerequisites`,
 * issue metadata) and pass it in. Omit to bypass the router entirely — useful
 * for unit tests of the existing precedence chain.
 */
export interface AutoRouterOptions {
  /**
   * Returns the list of authenticated, prerequisite-passing adapter ids the
   * router may consider. The resolver invokes this lazily and caches nothing —
   * callers should memoise if the lookup is expensive.
   */
  enumerateAvailableAdapters: () => RouterExecutionAdapter[];
  /** Issue complexity. Required so the router can pick a matching model tier. */
  complexity: ComplexityLabel;
  /** Routing mode (defaults to `automatic` if omitted). */
  mode?: AutoRouterMode;
  /** Optional issue type for capability-matrix bias. */
  issueType?: "feature" | "bug" | "docs" | "chore" | "refactor" | "epic";
  /** Optional remaining-budget signal — drives the cost dimension. */
  remainingBudgetUsd?: number;
  /** Optional stage cost estimate paired with `remainingBudgetUsd`. */
  stageEstimatedCostUsd?: number;
  /** Optional history tail for cost bias. Empty array is allowed. */
  recentHistory?: AutoRouterContext["recent_history"];
  /**
   * Inject a router instance for tests. Production callers may omit; the
   * resolver constructs one with default weights from config.
   */
  router?: AutoProviderRouter;
}

export function resolveStageAdapter(
  stage: PipelineStage,
  workspaceRoot?: string,
  env: NodeJS.ProcessEnv = process.env,
  autoRouterOptions?: AutoRouterOptions
): AdapterDecision {
  // Step 1 — env var: NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE>
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envAdapter = env[envKey];
  if (envAdapter && VALID_ADAPTERS.includes(envAdapter)) {
    return { adapter: envAdapter as ExecutionAdapter, source: "env" };
  }

  // Step 2 — pipeline.stage_adapters.<stage> from raw YAML
  const stageAdapter = getStageAdapterFromYaml(stage, workspaceRoot);
  if (stageAdapter) {
    return { adapter: stageAdapter, source: "stage-config" };
  }

  // Step 3 — an explicitly configured effective adapter is authoritative.
  // ConfigBridge source attribution distinguishes an actual global/project/
  // local selection from the built-in Claude default. A user selecting Codex
  // once must not be silently rerouted to another authenticated provider.
  const configured = getGlobalAdapterWithSource(workspaceRoot);
  if (configured.configured) {
    return { adapter: configured.adapter, source: "global-config" };
  }

  // Step 3.5 — AutoProviderRouter (Issue #3230). Automatic selection is a
  // fallback for users who have not explicitly selected an adapter.
  if (autoRouterOptions) {
    const routerDecision = tryAutoRouter(stage, workspaceRoot, autoRouterOptions);
    if (routerDecision) {
      return {
        adapter: fromRouterAdapter(routerDecision.adapter),
        source: "auto-router",
        rationale: routerDecision.rationale,
        routerModel: routerDecision.model,
      };
    }
  }

  // Step 4 — hardcoded default
  return { adapter: DEFAULT_EXECUTION_ADAPTER, source: "default" };
}

// ============================================================================
// Internal helpers
// ============================================================================

/**
 * Resolve the global `ui.core.adapter` with a `configured` flag.
 *
 * `getExecutionAdapter` collapses configured-claude and unconfigured-default
 * into the same return value, so it cannot tell `"global-config"` from
 * `"default"` for the resolver's source attribution. This helper preserves
 * that distinction by returning `configured: false` only when nothing
 * (env / ConfigBridge / config file) supplied a value.
 *
 * Layers checked, in priority order:
 *   1. `NIGHTGAUGE_UI_CORE_ADAPTER` env var
 *   2. `ConfigBridge` (post-activation, includes local overrides)
 *   3. `.nightgauge/config.local.yaml`, then `config.yaml` (file)
 *
 * Falls through to `{ adapter: DEFAULT_EXECUTION_ADAPTER, configured: false }`.
 */
export function getGlobalAdapterWithSource(workspaceRoot?: string): {
  adapter: ExecutionAdapter;
  configured: boolean;
} {
  // 1. Environment variable
  const envAdapter = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
  if (envAdapter && VALID_ADAPTERS.includes(envAdapter)) {
    return { adapter: envAdapter as ExecutionAdapter, configured: true };
  }

  // 2. ConfigBridge (post-activation, includes local overrides)
  try {
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const { ConfigBridge } = require("../../services/ConfigBridge");
    const bridge = ConfigBridge.getInstance?.();
    const adapter = bridge?.getUI?.()?.core?.adapter;
    if (adapter && VALID_ADAPTERS.includes(adapter)) {
      const source = bridge?.getSource?.("ui.core.adapter");
      return {
        adapter: adapter as ExecutionAdapter,
        configured: source !== undefined && source !== "default",
      };
    }
  } catch {
    // ConfigBridge not yet initialized (early startup) — fall through to file.
  }

  // 3. File-based fallback
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    const fileAdapter =
      readAdapterFromFile(path.join(root, ".nightgauge", "config.local.yaml")) ??
      readAdapterFromFile(path.join(root, ".nightgauge", "config.yaml"));
    if (fileAdapter) {
      return { adapter: fileAdapter, configured: true };
    }
  }

  // 4. Default — nothing configured
  return { adapter: DEFAULT_EXECUTION_ADAPTER, configured: false };
}

/**
 * Scan `.nightgauge/config.yaml` for `pipeline.stage_adapters.<stage>`.
 *
 * Mirrors the line-based YAML scanner used by `getStageModel`
 * (`stageResolver.ts:387-417`) so this resolver remains decoupled from B1
 * (#3220 — typed schema). When B1 lands, no change is required here.
 */
function getStageAdapterFromYaml(
  stage: PipelineStage,
  workspaceRoot?: string
): ExecutionAdapter | undefined {
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
    let inStageAdapters = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "stage_adapters:") {
        inStageAdapters = true;
        continue;
      }

      // Detect section exit on top-level / sibling key.
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inStageAdapters = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inStageAdapters = false;
        }
      }

      if (inStageAdapters) {
        const match = trimmed.match(
          /^([a-z][-a-z]*):\s*['"]?(claude|codex|gemini|gemini-sdk|lm-studio|ollama|copilot)['"]?(?:\s+#.*)?$/
        );
        if (match && match[1] === stage) {
          const adapter = match[2];
          if (VALID_ADAPTERS.includes(adapter)) {
            return adapter as ExecutionAdapter;
          }
          // Matched literal but not in VALID_ADAPTERS (e.g. "ollama") — fall
          // through to the global step rather than returning an invalid value.
          return undefined;
        }
      }
    }

    return undefined;
  } catch (error) {
    console.error("Failed to read stage_adapters from nightgauge config:", error);
    return undefined;
  }
}

// ============================================================================
// Fallback chain (Issue #3223, extended in Issue #3231)
// ============================================================================

/**
 * Built-in default fallback chain applied when neither
 * `pipeline.stage_adapter_fallback.<stage>` nor `pipeline.adapter_fallback_chain`
 * is configured (Issue #3231 / AC #1).
 *
 * Order is intentional: cloud-quality adapters first (claude → codex → gemini),
 * then host-OS-tied (copilot). The walker skips the failed primary, so a
 * `claude` primary failure with the default chain walks
 * `codex → gemini → copilot`. Chat-completion-only adapters (lm-studio,
 * ollama, gemini-sdk) are NOT fallback candidates: the #57 agentic gate
 * rejects them for pipeline dispatch, so a rung would always be dead.
 */
export const DEFAULT_ADAPTER_FALLBACK_CHAIN: ExecutionAdapter[] = [
  "claude",
  "codex",
  "gemini",
  "copilot",
];

/**
 * Read `pipeline.adapter_fallback_chain` from raw YAML.
 *
 * Mirrors the line-based scanner pattern used by `getStageAdapterFromYaml` to
 * stay decoupled from the typed config schema (C5 / future). Returns an
 * empty array when the section is absent, malformed, or the file does not
 * exist — callers treat empty as "no fallback configured".
 */
export function readAdapterFallbackChainFromYaml(workspaceRoot?: string): ExecutionAdapter[] {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return [];
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return [];
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    const chain: ExecutionAdapter[] = [];
    let inPipeline = false;
    let inFallbackChain = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "adapter_fallback_chain:") {
        inFallbackChain = true;
        continue;
      }

      // Detect section exit on top-level / sibling key.
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inFallbackChain = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inFallbackChain = false;
        }
      }

      if (inFallbackChain) {
        const match = trimmed.match(
          /^-\s+['"]?(claude|codex|gemini|gemini-sdk|lm-studio|ollama|copilot)['"]?(?:\s+#.*)?$/
        );
        if (match) {
          const adapter = match[1];
          if (VALID_ADAPTERS.includes(adapter)) {
            chain.push(adapter as ExecutionAdapter);
          }
        }
      }
    }

    return chain;
  } catch (error) {
    console.error("Failed to read adapter_fallback_chain from nightgauge config:", error);
    return [];
  }
}

/** Result of a fallback resolution attempt. */
export interface AdapterFallbackResult {
  adapter: ExecutionAdapter;
  source: AdapterSource;
}

/**
 * Read `pipeline.stage_adapter_fallback.<stage>` from raw YAML (Issue #3231).
 *
 * Mirrors the line-based scanner used by `getStageAdapterFromYaml` —
 * decoupled from the typed schema so the resolver works without depending on
 * Zod parsing of the full config. Returns `[]` when the section, the stage
 * key, or the file is absent.
 */
export function readStageAdapterFallbackFromYaml(
  stage: PipelineStage,
  workspaceRoot?: string
): ExecutionAdapter[] {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return [];
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return [];
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    const chain: ExecutionAdapter[] = [];
    let inPipeline = false;
    let inStageFallback = false;
    let inThisStage = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "stage_adapter_fallback:") {
        inStageFallback = true;
        inThisStage = false;
        continue;
      }

      // Section-exit detection. The `[a-z_]+:` regex matches snake_case keys
      // (`pipeline:`, `stage_adapters:`) but NOT hyphenated stage keys
      // (`feature-dev:`) — which is what we want, since stage keys live
      // under stage_adapter_fallback and are handled separately below.
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inStageFallback = false;
          inThisStage = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          // Sibling key under pipeline — leave stage_adapter_fallback.
          inStageFallback = false;
          inThisStage = false;
        }
      }

      // Stage key detection — handled outside the snake_case-only exit
      // regex above because stage names contain hyphens. A stage key lives
      // at exactly 4 spaces of indent under stage_adapter_fallback.
      if (inStageFallback) {
        const stageKeyMatch = line.match(/^ {4}([a-z][-a-z]*):\s*$/);
        if (stageKeyMatch) {
          inThisStage = stageKeyMatch[1] === stage;
          continue;
        }
      }

      if (inThisStage) {
        const match = trimmed.match(
          /^-\s+['"]?(claude|codex|gemini|gemini-sdk|lm-studio|ollama|copilot)['"]?(?:\s+#.*)?$/
        );
        if (match) {
          const adapter = match[1];
          if (VALID_ADAPTERS.includes(adapter)) {
            chain.push(adapter as ExecutionAdapter);
          }
        }
      }
    }

    return chain;
  } catch (error) {
    console.error("Failed to read stage_adapter_fallback from nightgauge config:", error);
    return [];
  }
}

/**
 * Read `pipeline.disable_fallback` from raw YAML (Issue #3231).
 *
 * Returns `true` only when the key is explicitly set to `true` in the file.
 * Absent / malformed → `false` (default behavior: fallback walking enabled).
 */
export function readDisableFallbackFromYaml(workspaceRoot?: string): boolean {
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
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && /^[a-z_]+:/.test(trimmed) && !line.startsWith(" ")) {
        inPipeline = false;
      }

      if (inPipeline) {
        const match = trimmed.match(/^disable_fallback:\s*(true|false)(?:\s+#.*)?$/);
        if (match) {
          return match[1] === "true";
        }
      }
    }

    return false;
  } catch (error) {
    console.error("Failed to read disable_fallback from nightgauge config:", error);
    return false;
  }
}

/**
 * Resolve the effective fallback chain for a stage (Issue #3231).
 *
 * Precedence (highest → lowest):
 *   1. `pipeline.stage_adapter_fallback.<stage>` — per-stage override
 *   2. `pipeline.adapter_fallback_chain`         — global override
 *   3. `DEFAULT_ADAPTER_FALLBACK_CHAIN`          — built-in default
 *
 * Strict mode: when `pipeline.disable_fallback === true`, the chain is empty
 * regardless of overrides — the dispatcher should fail immediately on primary
 * prereq failure with `[stage:adapter-unavailable]`.
 *
 * Operator opt-out lite: when the stage override is empty AND the global
 * chain is explicitly empty (`adapter_fallback_chain: []`), this returns
 * `[]`. The default chain only kicks in when *no* config is present.
 *
 * The dispatcher walks the returned chain (skipping the failed primary and
 * duplicates) until a candidate's prereq passes, or the chain is exhausted.
 */
export function getEffectiveFallbackChain(
  stage: PipelineStage,
  workspaceRoot?: string
): ExecutionAdapter[] {
  if (readDisableFallbackFromYaml(workspaceRoot)) {
    return [];
  }

  const stageOverride = readStageAdapterFallbackFromYaml(stage, workspaceRoot);
  if (stageOverride.length > 0) {
    return stageOverride;
  }

  const globalChain = readAdapterFallbackChainFromYaml(workspaceRoot);
  if (globalChain.length > 0) {
    return globalChain;
  }

  // Distinguish "no config" (use built-in default) from "explicit empty
  // global chain" (operator opt-out lite). The reader returns [] for both,
  // so we re-scan for the literal `adapter_fallback_chain:` section header.
  if (hasGlobalFallbackChainKey(workspaceRoot)) {
    return [];
  }

  return [...DEFAULT_ADAPTER_FALLBACK_CHAIN];
}

/**
 * @internal Test seam — returns true when the config file has a literal
 * `adapter_fallback_chain:` section header under `pipeline:`, regardless of
 * whether the list is empty. Lets `getEffectiveFallbackChain` distinguish
 * "no config" from "explicit empty list".
 */
function hasGlobalFallbackChainKey(workspaceRoot?: string): boolean {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return false;
  }
  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return false;
    }
    const lines = readEffectiveConfigTextSync(pathResult).split("\n");
    let inPipeline = false;
    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }
      if (inPipeline && /^[a-z_]+:/.test(trimmed) && !line.startsWith(" ")) {
        inPipeline = false;
      }
      if (inPipeline && /^adapter_fallback_chain:\s*(\[\s*\])?\s*$/.test(trimmed)) {
        return true;
      }
    }
    return false;
  } catch {
    return false;
  }
}

/**
 * Multi-hop fallback walker (Issue #3231 / AC #3, #5).
 *
 * Walks the effective fallback chain (`getEffectiveFallbackChain`) for the
 * given stage, skipping the failed primary and any duplicates, calling the
 * injected validator on each candidate. Returns:
 *
 * - `winner`        — the first candidate whose prereq validated, or `null`
 *                     if every candidate failed.
 * - `hopsAttempted` — every adapter validate() was called on, in order,
 *                     **including** the failed primary as element 0. Length
 *                     is always ≥ 1; length 1 means the primary failed and
 *                     the chain was empty (or strict-mode disabled it).
 * - `lastError`     — the last prereq error string the walker observed.
 *                     Useful for the `[stage:no-adapter-available]` envelope.
 *
 * Mirrors `tryAdapterFallback` but exposes the full audit trail. The
 * back-compat `tryAdapterFallback` wrapper below preserves the single-result
 * shape for callers that only care about the winner.
 */
export interface AdapterFallbackWalkResult {
  winner: AdapterFallbackResult | null;
  hopsAttempted: ExecutionAdapter[];
  lastError: string;
}

export function walkAdapterFallback(
  failedAdapter: ExecutionAdapter,
  primaryError: string,
  validate: (adapter: ExecutionAdapter) => string | null,
  workspaceRoot?: string,
  stage?: PipelineStage
): AdapterFallbackWalkResult {
  // Without a stage we cannot consult the per-stage override path; fall
  // through to the global chain only. Existing #3223 callers that did not
  // pass a stage stay on the global path with no behavior change.
  const chain = stage
    ? getEffectiveFallbackChain(stage, workspaceRoot)
    : readAdapterFallbackChainFromYaml(workspaceRoot);

  const hopsAttempted: ExecutionAdapter[] = [failedAdapter];
  let lastError = primaryError;

  if (chain.length === 0) {
    return { winner: null, hopsAttempted, lastError };
  }

  const tried = new Set<ExecutionAdapter>([failedAdapter]);
  for (const candidate of chain) {
    if (tried.has(candidate)) {
      continue;
    }
    tried.add(candidate);
    hopsAttempted.push(candidate);
    const err = validate(candidate);
    if (err === null) {
      return {
        winner: { adapter: candidate, source: "fallback" },
        hopsAttempted,
        lastError,
      };
    }
    lastError = err;
  }

  return { winner: null, hopsAttempted, lastError };
}

/**
 * Resolve a fallback adapter when the primary adapter's prereq check fails.
 *
 * Back-compat wrapper around `walkAdapterFallback` that exposes only the
 * winner (or `null`). Existing callers and unit tests against the
 * pre-#3231 single-result shape continue to work unchanged. New callers in
 * `skillRunner.ts` should use `walkAdapterFallback` to capture the full
 * `hopsAttempted` audit trail.
 *
 * @param failedAdapter — The primary adapter that just failed prereq check.
 * @param validate      — Prereq validator (returns null on success, error
 *                        message string on failure).
 * @param workspaceRoot — Workspace root for raw YAML lookup.
 * @returns First candidate that passed validation, or `null` when chain is
 *          empty or every candidate fails.
 */
export function tryAdapterFallback(
  failedAdapter: ExecutionAdapter,
  validate: (adapter: ExecutionAdapter) => string | null,
  workspaceRoot?: string
): AdapterFallbackResult | null {
  // Preserve the pre-#3231 contract: this wrapper consults only the global
  // chain (`pipeline.adapter_fallback_chain`) — no per-stage override, no
  // built-in default. Callers that want the full Issue-#3231 precedence
  // chain should call `walkAdapterFallback` with a `stage` argument.
  const result = walkAdapterFallback(failedAdapter, "", validate, workspaceRoot);
  return result.winner;
}

// ============================================================================
// AutoProviderRouter (Issue #3230)
// ============================================================================

/**
 * Read `pipeline.auto_router` from raw YAML.
 *
 * Returns `{ enabled, weights }` where missing keys are left undefined. Mirrors
 * the line-based scanner pattern used elsewhere in this file so the resolver
 * stays decoupled from the typed config schema.
 */
export function readAutoRouterConfigFromYaml(workspaceRoot?: string): {
  enabled?: boolean;
  weights?: Partial<AutoRouterWeights>;
} {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return {};
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return {};
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inAutoRouter = false;
    let inWeights = false;
    const result: { enabled?: boolean; weights?: Partial<AutoRouterWeights> } = {};

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "auto_router:") {
        inAutoRouter = true;
        continue;
      }

      if (inAutoRouter && trimmed === "weights:") {
        inWeights = true;
        continue;
      }

      // Detect section exit.
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inAutoRouter = false;
          inWeights = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          inAutoRouter = false;
          inWeights = false;
        } else if (line.match(/^ {4}[a-z_]+:/) && !line.match(/^ {6}/)) {
          inWeights = false;
        }
      }

      if (inAutoRouter && !inWeights) {
        const enabledMatch = trimmed.match(/^enabled:\s*(true|false)(?:\s+#.*)?$/);
        if (enabledMatch) {
          result.enabled = enabledMatch[1] === "true";
        }
      }

      if (inWeights) {
        const weightMatch = trimmed.match(
          /^(cost|capability|context_window):\s*([\d.]+)(?:\s+#.*)?$/
        );
        if (weightMatch) {
          const value = Number.parseFloat(weightMatch[2]);
          if (!Number.isNaN(value) && value >= 0 && value <= 1) {
            result.weights ??= {};
            const key = weightMatch[1] as "cost" | "capability" | "context_window";
            result.weights[key] = value;
          }
        }
      }
    }

    return result;
  } catch (error) {
    console.error("Failed to read pipeline.auto_router from nightgauge config:", error);
    return {};
  }
}

/**
 * Singleton router instance — stateless aside from default weights, safe to
 * share across pipeline runs. The resolver constructs it lazily so unit tests
 * that never trigger Step 2.5 don't pay the construction cost.
 */
let _sharedRouter: AutoProviderRouter | null = null;

function getOrCreateRouter(weights: Partial<AutoRouterWeights> | undefined): AutoProviderRouter {
  // Reuse the singleton when no per-call weight overrides are supplied. Custom
  // weights produce a fresh instance so we never accidentally cache them.
  if (!weights || Object.keys(weights).length === 0) {
    if (!_sharedRouter) {
      _sharedRouter = new AutoProviderRouter();
    }
    return _sharedRouter;
  }
  return new AutoProviderRouter(undefined, weights);
}

/**
 * Invoke the AutoProviderRouter for a stage.
 *
 * Returns `null` when the router is disabled in config, when no adapters are
 * authenticated, or when the router itself abstains. Callers treat null as a
 * fall-through signal and let the existing precedence chain run.
 */
export function tryAutoRouter(
  stage: PipelineStage,
  workspaceRoot: string | undefined,
  options: AutoRouterOptions
): AutoRouterDecision | null {
  const config = readAutoRouterConfigFromYaml(workspaceRoot);
  if (config.enabled === false) {
    return null;
  }

  const available = options.enumerateAvailableAdapters();
  if (available.length === 0) {
    return null;
  }

  const router = options.router ?? getOrCreateRouter(config.weights);
  const ctx: AutoRouterContext = {
    stage,
    mode: options.mode ?? "automatic",
    complexity: options.complexity,
    issue_type: options.issueType,
    available_adapters: available,
    recent_history: options.recentHistory ?? [],
    remaining_budget_usd: options.remainingBudgetUsd,
    stage_estimated_cost_usd: options.stageEstimatedCostUsd,
  };

  return router.selectForStage(stage, ctx);
}

/**
 * Map the UI-facing `ExecutionAdapter` to the SDK's canonical
 * `RouterExecutionAdapter` (= `IncrediAdapter`). The Marketplace extension
 * deliberately routes bare `"claude"` through the user's external Claude CLI.
 * It does not embed or auto-select the optional Claude Agent SDK based on an
 * ambient API key. Every other UI adapter id is identical to its SDK id.
 */
function toRouterAdapter(adapter: ExecutionAdapter): RouterExecutionAdapter {
  if (adapter === "claude") {
    return "claude-headless";
  }
  return adapter;
}

/**
 * Inverse of {@link toRouterAdapter}: collapse the SDK's two Claude backends
 * back to the UI's bare `"claude"` so a router pick threads cleanly through the
 * `ExecutionAdapter`-typed precedence chain. Every other id passes through.
 */
export function fromRouterAdapter(adapter: RouterExecutionAdapter): ExecutionAdapter {
  if (adapter === "claude-sdk" || adapter === "claude-headless") {
    return "claude";
  }
  return adapter;
}

/**
 * Enumerate authenticated, prerequisite-passing adapters in lexicographic
 * order. Used by skillRunner to build the AutoRouterOptions bag without
 * importing `validateAdapterPrerequisites` here — the validator is injected
 * to avoid cyclic SDK→VSCode imports (mirrors `tryAdapterFallback`).
 *
 * The UI-facing `"claude"` candidate is disambiguated to its canonical SDK
 * backend (`claude-sdk` / `claude-headless`) via {@link toRouterAdapter} before
 * being handed to the router (#3912).
 */
export function enumerateAvailableAdapters(
  validate: (adapter: ExecutionAdapter) => string | null
): RouterExecutionAdapter[] {
  // Derive candidates from the canonical adapter set (VALID_ADAPTERS ==
  // AdapterEnumSchema.options) so the auto-router considers every selectable
  // adapter — incl. ollama — rather than a stale hand-maintained subset (#4030).
  // Each is still gated by `validate`.
  const candidates = VALID_ADAPTERS as readonly ExecutionAdapter[];
  const passing = candidates.filter((adapter) => validate(adapter) === null).map(toRouterAdapter);
  return passing.sort((a, b) => a.localeCompare(b));
}

/** @internal Test-only — reset the shared router instance between tests. */
export function _resetAutoRouterForTests(): void {
  _sharedRouter = null;
}
