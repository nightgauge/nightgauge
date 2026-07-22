/**
 * AutoProviderRouter — deterministic cross-adapter routing decision tree.
 *
 * Picks `(adapter, model)` per stage from a set of caller-supplied
 * authenticated adapters. The router is pure — no file system, no env, no
 * clock-dependent logic — so identical inputs always produce identical
 * outputs. Tests assert this directly.
 *
 * ## Pipeline
 * 1. `available_adapters.length === 0` → abstain (confidence 0).
 * 2. Mode `"manual"` → abstain unconditionally — explicit user control.
 * 3. `available_adapters.length === 1` → that adapter wins; delegate model
 *    pick to `AutoModelSelector`; confidence 1.0.
 * 4. Score every candidate via
 *    `cost × w.cost + capability × w.capability + context_window × w.cw`.
 * 5. In `"hybrid"` mode require top to exceed second by
 *    `HYBRID_DOMINANCE_THRESHOLD` (0.15); otherwise abstain.
 * 6. Confidence = `topScore − secondScore` clipped to `[0, 1]`. Below the
 *    confidence threshold (default 0.7) the router abstains so the resolver
 *    falls through to the existing precedence chain.
 * 7. Delegate model pick to `AutoModelSelector`; remap to nearest adapter-
 *    supported tier when the chosen adapter does not natively support the
 *    model (e.g. Codex receiving `"opus"` is remapped to its heavy tier).
 *
 * ## Determinism guarantees
 *
 * Adapters are scored in lexicographic order by id so tie-breaking and
 * argmax are stable. Map iteration is never used as ordering.
 *
 * @see Issue #3230 — AutoProviderRouter: smart adapter selection per stage
 */

import {
  AutoModelSelector,
  type ComplexityLabel,
  type IssueMetadata,
  type ModelTier,
  type RoutingIssueType,
} from "./AutoModelSelector.js";
import {
  DEFAULT_AUTO_ROUTER_CONFIDENCE_THRESHOLD,
  DEFAULT_AUTO_ROUTER_WEIGHTS,
  HYBRID_DOMINANCE_THRESHOLD,
  WORKFLOW_SUBSCORE_WEIGHT,
  type AutoRouterContext,
  type AutoRouterDecision,
  type AutoRouterHistoryEntry,
  type AutoRouterMode,
  type AutoRouterWeights,
  type RouterExecutionAdapter,
  type RouterStageCategory,
} from "./auto-router-types.js";
import { defaultRegistry } from "../cli/adapters/AdapterRegistry.js";
import type { OrchestrationCapability } from "../cli/adapters/ICliAdapter.js";
import { resolveModelForAdapter } from "../eval/modelRegistry.js";

/**
 * Per-adapter context window in tokens, used by the context-window sub-score.
 *
 * Numbers reflect documented 2026-Q1 vendor limits. They are intentionally
 * approximate — the router only cares about relative ordering, not exact
 * token counts. Update when a vendor announces a new context limit.
 */
const ADAPTER_CONTEXT_WINDOW_TOKENS: Record<RouterExecutionAdapter, number> = {
  "claude-sdk": 200_000,
  "claude-headless": 200_000,
  codex: 256_000,
  gemini: 1_000_000,
  "gemini-sdk": 1_000_000,
  "lm-studio": 32_000,
  ollama: 32_000,
  copilot: 64_000,
};

/**
 * Per-stage expected active context size in tokens (i.e. the prompt + working
 * memory the model needs at once, NOT total billed tokens which expand with
 * cache reads). The router uses this to score adapter context windows; a
 * small active context means even Claude's 200K window saturates the score.
 */
const STAGE_EXPECTED_TOKENS: Record<string, number> = {
  "issue-pickup": 60_000,
  "feature-planning": 100_000,
  "feature-dev": 150_000,
  "feature-validate": 80_000,
  "pr-create": 40_000,
  "pr-merge": 60_000,
};

const DEFAULT_STAGE_EXPECTED_TOKENS = 100_000;

/**
 * Capability score table per (stage category, adapter).
 *
 * The matrix encodes prior knowledge that some adapters are stronger at
 * certain task shapes — Claude is the canonical pick for classification and
 * complex dev tasks; Codex shines on code-focused dev work; Gemini's giant
 * context window helps with planning. Local models score lower on capability
 * to bias against picking them when paid adapters are available.
 *
 * Scores are in `[0, 1]`. Stages outside the table fall back to `dev`.
 */
const CAPABILITY_MATRIX: Record<RouterStageCategory, Record<RouterExecutionAdapter, number>> = {
  classification: {
    "claude-sdk": 0.95,
    "claude-headless": 0.95,
    codex: 0.7,
    gemini: 0.75,
    "gemini-sdk": 0.75,
    "lm-studio": 0.4,
    ollama: 0.4,
    copilot: 0.65,
  },
  planning: {
    "claude-sdk": 0.9,
    "claude-headless": 0.9,
    codex: 0.75,
    gemini: 0.85,
    "gemini-sdk": 0.85,
    "lm-studio": 0.45,
    ollama: 0.45,
    copilot: 0.7,
  },
  dev: {
    "claude-sdk": 0.95,
    "claude-headless": 0.95,
    codex: 0.85,
    gemini: 0.78,
    "gemini-sdk": 0.78,
    "lm-studio": 0.55,
    ollama: 0.55,
    copilot: 0.75,
  },
  validate: {
    "claude-sdk": 0.9,
    "claude-headless": 0.9,
    codex: 0.82,
    gemini: 0.78,
    "gemini-sdk": 0.78,
    "lm-studio": 0.5,
    ollama: 0.5,
    copilot: 0.7,
  },
  lightweight: {
    // For lightweight stages capability matters less — flat scores keep cost
    // as the dominant signal.
    "claude-sdk": 0.7,
    "claude-headless": 0.7,
    codex: 0.7,
    gemini: 0.7,
    "gemini-sdk": 0.7,
    "lm-studio": 0.65,
    ollama: 0.65,
    copilot: 0.7,
  },
  merge: {
    "claude-sdk": 0.88,
    "claude-headless": 0.88,
    codex: 0.78,
    gemini: 0.75,
    "gemini-sdk": 0.75,
    "lm-studio": 0.5,
    ollama: 0.5,
    copilot: 0.7,
  },
};

const CLASSIFICATION_STAGES = new Set<string>(["issue-pickup"]);
const LIGHTWEIGHT_STAGES = new Set<string>(["pr-create"]);

function categorizeStage(stage: string): RouterStageCategory {
  if (CLASSIFICATION_STAGES.has(stage)) return "classification";
  if (LIGHTWEIGHT_STAGES.has(stage)) return "lightweight";
  if (stage === "feature-planning") return "planning";
  if (stage === "feature-dev") return "dev";
  if (stage === "feature-validate") return "validate";
  if (stage === "pr-merge") return "merge";
  return "dev";
}

/**
 * Map a tier alias (`haiku`/`sonnet`/`opus`/`fable`) returned by
 * `AutoModelSelector` to a model identifier the chosen adapter actually
 * understands, via the provider-aware model registry (#56). Claude passes the
 * tier through unchanged (the `claude` CLI accepts tier aliases natively);
 * providers without a fable-equivalent resolve `fable` to their strongest
 * band model. Local adapters (ollama/lm-studio) have no tier hierarchy — the
 * tier alias is returned unchanged and the dispatcher falls back to the
 * configured local model, exactly like the performance-mode mismatch path.
 */
function remapTierForAdapter(adapter: RouterExecutionAdapter, tier: ModelTier): string {
  if (adapter === "claude-sdk" || adapter === "claude-headless") return tier;
  return resolveModelForAdapter(adapter, tier)?.id ?? tier;
}

/**
 * Public router class. Stateless aside from the injected `AutoModelSelector`
 * and weight defaults — instances are safe to share across pipeline runs.
 */
export class AutoProviderRouter {
  private readonly modelSelector: AutoModelSelector;
  private readonly defaultWeights: AutoRouterWeights;

  constructor(modelSelector?: AutoModelSelector, weights?: Partial<AutoRouterWeights>) {
    this.modelSelector = modelSelector ?? new AutoModelSelector();
    this.defaultWeights = {
      cost: weights?.cost ?? DEFAULT_AUTO_ROUTER_WEIGHTS.cost,
      capability: weights?.capability ?? DEFAULT_AUTO_ROUTER_WEIGHTS.capability,
      context_window: weights?.context_window ?? DEFAULT_AUTO_ROUTER_WEIGHTS.context_window,
      workflow: weights?.workflow ?? DEFAULT_AUTO_ROUTER_WEIGHTS.workflow,
    };
  }

  /**
   * Pick `(adapter, model)` for a stage, or `null` to abstain.
   *
   * Returns `null` when:
   *  - `available_adapters` is empty
   *  - `mode === "manual"`
   *  - the top candidate's confidence margin falls below the threshold
   *  - `mode === "hybrid"` and the dominance margin is below 0.15
   *
   * Callers should treat `null` as a fall-through signal and let the existing
   * precedence chain run.
   */
  selectForStage(stage: string, ctx: AutoRouterContext): AutoRouterDecision | null {
    if (ctx.mode === "manual") {
      return null;
    }
    if (ctx.available_adapters.length === 0) {
      return null;
    }

    const requiresWorkflow = ctx.requires_workflow === true;
    const weights = this.resolveWeights(ctx.weights, requiresWorkflow);
    const threshold = ctx.confidence_threshold ?? DEFAULT_AUTO_ROUTER_CONFIDENCE_THRESHOLD;

    // Iterate in lexicographic order for deterministic tie-breaking.
    const candidates = dedupeAndSort(ctx.available_adapters);

    if (candidates.length === 1) {
      const adapter = candidates[0];
      const model = this.pickModelForAdapter(stage, adapter, ctx);
      return {
        adapter,
        model,
        rationale: `adapter=${adapter} selected for stage=${stage} (only authenticated adapter); model=${model} from AutoModelSelector`,
        confidence: 1.0,
        scores: { [adapter]: 1.0 } as Partial<Record<RouterExecutionAdapter, number>>,
      };
    }

    const stageCategory = categorizeStage(stage);
    const expectedTokens = STAGE_EXPECTED_TOKENS[stage] ?? DEFAULT_STAGE_EXPECTED_TOKENS;
    const budgetPressure = computeBudgetPressureFactor(
      ctx.remaining_budget_usd,
      ctx.stage_estimated_cost_usd
    );

    const scoreEntries: Array<{
      adapter: RouterExecutionAdapter;
      total: number;
      cost: number;
      capability: number;
      context: number;
      workflow: number;
    }> = [];

    for (const adapter of candidates) {
      const cost = scoreCost(adapter, ctx.recent_history) * budgetPressure;
      const capability = CAPABILITY_MATRIX[stageCategory][adapter] ?? 0.5;
      const context = scoreContextWindow(adapter, expectedTokens);
      // Workflow sub-score is `0` for non-workflow routing, so it contributes
      // nothing when `weights.workflow` is also `0` — the non-workflow path is
      // byte-for-byte identical to the pre-#3912 behaviour.
      const workflow = requiresWorkflow ? scoreWorkflow(adapter) : 0;
      const total =
        cost * weights.cost +
        capability * weights.capability +
        context * weights.context_window +
        workflow * weights.workflow;
      scoreEntries.push({ adapter, total, cost, capability, context, workflow });
    }

    // Stable sort: highest score first, then lexicographic tie-breaker.
    scoreEntries.sort((a, b) => {
      if (b.total !== a.total) return b.total - a.total;
      return a.adapter.localeCompare(b.adapter);
    });

    const top = scoreEntries[0];
    const second = scoreEntries[1];
    const margin = top.total - second.total;
    const confidence = Math.min(1.0, Math.max(0, margin));

    if (ctx.mode === "hybrid" && margin < HYBRID_DOMINANCE_THRESHOLD) {
      return null;
    }

    if (confidence < threshold) {
      return null;
    }

    const model = this.pickModelForAdapter(stage, top.adapter, ctx);
    const workflowFragment = requiresWorkflow ? `workflow=${top.workflow.toFixed(2)} ` : "";
    const rationale =
      `adapter=${top.adapter} selected for stage=${stage} ` +
      `(cost=${top.cost.toFixed(2)} capability=${top.capability.toFixed(2)} ` +
      `context=${top.context.toFixed(2)} ${workflowFragment}total=${top.total.toFixed(3)} ` +
      `margin=${margin.toFixed(3)}); model=${model} from AutoModelSelector`;

    const scores: Partial<Record<RouterExecutionAdapter, number>> = {};
    for (const entry of scoreEntries) {
      scores[entry.adapter] = roundTo(entry.total, 4);
    }

    return {
      adapter: top.adapter,
      model,
      rationale,
      confidence,
      scores,
    };
  }

  /**
   * Resolve the effective scoring weights.
   *
   * For non-workflow routing the three classic weights (cost / capability /
   * context-window) are normalised to sum to 1.0 and `workflow` is `0` — the
   * exact pre-#3912 behaviour. For workflow-eligible stages the router reserves
   * {@link WORKFLOW_SUBSCORE_WEIGHT} of the total for the workflow dimension and
   * splits the remaining `1 - WORKFLOW_SUBSCORE_WEIGHT` across the three classic
   * weights in their existing proportions, so all four still sum to 1.0.
   */
  private resolveWeights(
    overrides: Partial<AutoRouterWeights> | undefined,
    requiresWorkflow: boolean
  ): AutoRouterWeights {
    const merged = {
      cost: overrides?.cost ?? this.defaultWeights.cost,
      capability: overrides?.capability ?? this.defaultWeights.capability,
      context_window: overrides?.context_window ?? this.defaultWeights.context_window,
    };
    let classicSum = merged.cost + merged.capability + merged.context_window;
    if (classicSum <= 0) {
      merged.cost = DEFAULT_AUTO_ROUTER_WEIGHTS.cost;
      merged.capability = DEFAULT_AUTO_ROUTER_WEIGHTS.capability;
      merged.context_window = DEFAULT_AUTO_ROUTER_WEIGHTS.context_window;
      classicSum = merged.cost + merged.capability + merged.context_window;
    }

    // `classicShare` is the fraction of the total budget left for the three
    // classic weights: the whole budget for non-workflow routing, the
    // remainder after the reserved workflow share otherwise.
    const classicShare = requiresWorkflow ? 1 - WORKFLOW_SUBSCORE_WEIGHT : 1;
    return {
      cost: (merged.cost / classicSum) * classicShare,
      capability: (merged.capability / classicSum) * classicShare,
      context_window: (merged.context_window / classicSum) * classicShare,
      workflow: requiresWorkflow ? WORKFLOW_SUBSCORE_WEIGHT : 0,
    };
  }

  private pickModelForAdapter(
    stage: string,
    adapter: RouterExecutionAdapter,
    ctx: AutoRouterContext
  ): string {
    const labels: string[] = [`size:${ctx.complexity}`];
    if (ctx.issue_type) labels.push(`type:${ctx.issue_type}`);
    const metadata: IssueMetadata = {
      labels,
      title: `auto-router-stage-${stage}`,
      size: ctx.complexity,
    };
    const result = this.modelSelector.selectModel(stage, metadata);
    return remapTierForAdapter(adapter, result.model);
  }
}

/**
 * Cost sub-score from recent history. Adapters with lower mean cost score
 * higher; adapters absent from history score 0.5 (neutral) so they aren't
 * unfairly penalised on first use.
 */
function scoreCost(adapter: RouterExecutionAdapter, history: AutoRouterHistoryEntry[]): number {
  if (history.length === 0) return 0.5;
  const filtered = history.filter((h) => h.adapter === adapter);
  if (filtered.length === 0) return 0.5;
  const mean = filtered.reduce((sum, h) => sum + h.cost_usd, 0) / filtered.length;
  // Normalise against the highest-cost adapter in the entire history so the
  // most expensive one floors at 0 and the cheapest at 1.
  const allMeans = new Map<RouterExecutionAdapter, number>();
  for (const adapterId of dedupeAndSort(history.map((h) => h.adapter))) {
    const subset = history.filter((h) => h.adapter === adapterId);
    if (subset.length === 0) continue;
    const m = subset.reduce((sum, h) => sum + h.cost_usd, 0) / subset.length;
    allMeans.set(adapterId, m);
  }
  const maxMean = Math.max(...Array.from(allMeans.values()));
  if (maxMean <= 0) return 0.5;
  return Math.max(0, Math.min(1, 1 - mean / maxMean));
}

/**
 * Per-`OrchestrationCapability` workflow sub-score. `native-workflow` adapters
 * can offload the fan-out to a provider-native Dynamic Workflow and score the
 * ceiling; `sdk-fanout` participants are still first-class — driven through the
 * portable `SdkFanoutRunner` floor — and score a solid 0.55 so they remain
 * routable (e.g. Codex via the engine) when no native adapter is authenticated,
 * yet sit clearly below a native adapter when one is.
 */
const WORKFLOW_CAPABILITY_SCORE: Record<OrchestrationCapability, number> = {
  "native-workflow": 1.0,
  "sdk-fanout": 0.55,
};

/**
 * Workflow sub-score for an adapter, sourced from the adapter's declared
 * `getOrchestrationCapability()`. This is the first production consumer of that
 * capability hook (#3902 / #3912): the router asks the canonical adapter
 * registry — the same one the engine drives — so the score can never drift from
 * the adapter's real orchestration backend. The registry lookup is a pure
 * in-memory map access, so the router stays deterministic and side-effect-free.
 */
function scoreWorkflow(adapter: RouterExecutionAdapter): number {
  const capability = defaultRegistry.get(adapter).getOrchestrationCapability();
  return WORKFLOW_CAPABILITY_SCORE[capability];
}

/**
 * Context-window sub-score: 1.0 once the adapter's window fits the expected
 * stage volume (ratio ≥ 1), linear penalty below. Saturates intentionally so
 * giant windows (Gemini 1M) do not dominate the score for stages that already
 * fit comfortably in 200K.
 */
function scoreContextWindow(adapter: RouterExecutionAdapter, expectedTokens: number): number {
  const window = ADAPTER_CONTEXT_WINDOW_TOKENS[adapter];
  if (window <= 0 || expectedTokens <= 0) return 0;
  const ratio = window / expectedTokens;
  if (ratio >= 1) return 1.0;
  return Math.max(0, ratio);
}

/**
 * Budget-pressure factor: full weight (1.0) under normal conditions,
 * downweighted when the budget has comfortable headroom so capability and
 * context-window dominate.
 */
function computeBudgetPressureFactor(
  remainingBudgetUsd: number | undefined,
  stageEstimatedCostUsd: number | undefined
): number {
  if (remainingBudgetUsd === undefined || stageEstimatedCostUsd === undefined) return 1.0;
  if (stageEstimatedCostUsd <= 0) return 1.0;
  const ratio = remainingBudgetUsd / stageEstimatedCostUsd;
  // When budget headroom is comfortable, neutralise the cost dimension so
  // capability and context window dominate. Stays at 1.0 only when the budget
  // is genuinely tight relative to the stage's expected cost.
  if (ratio >= 10) return 0;
  if (ratio >= 5) return 0.4;
  return 1.0;
}

function dedupeAndSort(items: RouterExecutionAdapter[]): RouterExecutionAdapter[] {
  const set = new Set<RouterExecutionAdapter>(items);
  return Array.from(set).sort((a, b) => a.localeCompare(b));
}

function roundTo(value: number, decimals: number): number {
  const factor = 10 ** decimals;
  return Math.round(value * factor) / factor;
}

export type {
  AutoRouterContext,
  AutoRouterDecision,
  AutoRouterHistoryEntry,
  AutoRouterMode,
  AutoRouterWeights,
  RouterExecutionAdapter,
  RouterStageCategory,
};

export {
  DEFAULT_AUTO_ROUTER_CONFIDENCE_THRESHOLD,
  DEFAULT_AUTO_ROUTER_WEIGHTS,
  HYBRID_DOMINANCE_THRESHOLD,
  WORKFLOW_SUBSCORE_WEIGHT,
};

export type { ComplexityLabel, ModelTier, RoutingIssueType };
