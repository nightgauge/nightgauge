/**
 * AutoProviderRouter — type definitions
 *
 * Pure data shapes for the cross-adapter routing decision tree. Lives in the
 * SDK so the router stays free of cyclic dependencies on the VSCode extension
 * (which owns the canonical `ExecutionAdapter` union and the auth-validation
 * surface). The caller injects auth-validated adapter ids and recent history
 * via `AutoRouterContext`.
 *
 * @see Issue #3230 — AutoProviderRouter: smart adapter selection per stage
 */

import type { ComplexityLabel, RoutingIssueType } from "./AutoModelSelector.js";
import type { IncrediAdapter } from "../cli/adapters/ICliAdapter.js";

/**
 * Adapter identifiers supported by the router.
 *
 * Derived from the canonical {@link IncrediAdapter} — single source of truth.
 * This disambiguates the two Claude backends (`claude-sdk` vs
 * `claude-headless`) that the old bare-`"claude"` collapse hid, so "route to
 * the native runner" is no longer ambiguous between them (#3912). The capability
 * scoring tables in `AutoProviderRouter` therefore key off the same names the
 * adapter registry uses, and the router can call each adapter's
 * `getOrchestrationCapability()` directly.
 */
export type RouterExecutionAdapter = IncrediAdapter;

/**
 * Routing mode that governs how aggressive the router should be.
 *
 * - `"manual"` — router never picks; always abstains so the resolver falls
 *                through to the existing precedence chain.
 * - `"hybrid"` — router only picks when one candidate dominates by
 *                ≥`HYBRID_DOMINANCE_THRESHOLD`. Otherwise abstains.
 * - `"automatic"` — highest score wins, even narrowly.
 */
export type AutoRouterMode = "manual" | "automatic" | "hybrid";

/**
 * One historical execution of a stage on a specific adapter, used for
 * cost-bias scoring. Caller (VSCode-side) tails recent records via
 * `ExecutionHistoryReader` and converts them to this shape.
 */
export interface AutoRouterHistoryEntry {
  adapter: RouterExecutionAdapter;
  model: string;
  cost_usd: number;
  success: boolean;
  duration_ms?: number;
}

/**
 * Optional weight overrides for the scoring sub-functions. All weights live in
 * `[0, 1]` and the router normalises them to sum to 1.0 when scoring.
 *
 * The `workflow` dimension is special: it stays at `0` for ordinary routing and
 * is only mixed in when `AutoRouterContext.requires_workflow` is set, so adding
 * it never perturbs the non-workflow scoring path (#3912). When workflow routing
 * is active the router reserves {@link WORKFLOW_SUBSCORE_WEIGHT} of the total for
 * this dimension and renormalises the other three to share the remainder.
 */
export interface AutoRouterWeights {
  /** Cost-pressure weight (default 0.4) */
  cost: number;
  /** Capability weight (default 0.4) */
  capability: number;
  /** Context-window weight (default 0.2) */
  context_window: number;
  /**
   * Orchestration-capability weight. Default `0` — contributes nothing unless
   * `requires_workflow` is set, at which point the router substitutes
   * {@link WORKFLOW_SUBSCORE_WEIGHT}. @see Issue #3912
   */
  workflow: number;
}

/**
 * Default scoring weights. `cost` + `capability` + `context_window` sum to 1.0;
 * `workflow` is `0` by default so ordinary (non-workflow) routing is byte-for-
 * byte unchanged from the pre-#3912 behaviour.
 *
 * Tuned to match the existing `AutoModelSelector` bias: cost and capability
 * carry equal weight; context-window contributes a quarter as much.
 */
export const DEFAULT_AUTO_ROUTER_WEIGHTS: AutoRouterWeights = {
  cost: 0.4,
  capability: 0.4,
  context_window: 0.2,
  workflow: 0,
};

/**
 * Fraction of the total score reserved for the workflow sub-score when
 * `requires_workflow` is set. The remaining `1 - WORKFLOW_SUBSCORE_WEIGHT` is
 * split across cost/capability/context-window in their existing proportions, so
 * a workflow-capable adapter is decisively preferred for workflow-eligible
 * stages while a non-workflow adapter (e.g. Codex via the engine) stays routable
 * when no `native-workflow` adapter is authenticated. @see Issue #3912
 */
export const WORKFLOW_SUBSCORE_WEIGHT = 0.45;

/**
 * Default confidence threshold below which the router abstains.
 *
 * Mirrors `AutoModelSelector`'s confidence threshold (0.7). When the top
 * candidate's `(topScore - secondScore)` margin falls below this value the
 * router returns `null` so the resolver falls through to the existing
 * precedence chain rather than producing a low-quality auto pick.
 */
export const DEFAULT_AUTO_ROUTER_CONFIDENCE_THRESHOLD = 0.7;

/**
 * Hybrid-mode dominance threshold. The router only picks in hybrid mode when
 * the top score exceeds the second by at least this margin.
 */
export const HYBRID_DOMINANCE_THRESHOLD = 0.15;

/**
 * Inputs for `AutoProviderRouter.selectForStage`. The caller is responsible
 * for sourcing every field — the router never reaches into the file system,
 * environment, or any VSCode-side service.
 */
export interface AutoRouterContext {
  /** Stage being routed (e.g. `"feature-dev"`). */
  stage: string;
  /** Active routing mode from config. */
  mode: AutoRouterMode;
  /** Issue complexity from `AutoModelSelector.extractComplexity`. */
  complexity: ComplexityLabel;
  /** Issue type label, when known. */
  issue_type?: RoutingIssueType;
  /** Remaining budget in USD; when set, downweights cost when the budget is comfortable. */
  remaining_budget_usd?: number;
  /** Per-stage cost estimate in USD; used together with `remaining_budget_usd`. */
  stage_estimated_cost_usd?: number;
  /** Tail of stage runs, oldest first. Empty array is allowed. */
  recent_history: AutoRouterHistoryEntry[];
  /** Adapters that passed auth pre-flight. Empty → router abstains. */
  available_adapters: RouterExecutionAdapter[];
  /**
   * Does this stage need multi-agent orchestration (fan-out / judge tree)?
   * When `true` the router mixes in the workflow sub-score so adapters that
   * declare `getOrchestrationCapability() === "native-workflow"` are strongly
   * preferred, while `sdk-fanout` participants (e.g. Codex) stay routable when
   * no native adapter is authenticated. When `false`/omitted the workflow
   * dimension is inert and routing is identical to the pre-#3912 behaviour.
   * @see Issue #3912
   */
  requires_workflow?: boolean;
  /** Optional weight overrides; defaults applied when omitted. */
  weights?: Partial<AutoRouterWeights>;
  /** Optional confidence threshold; default `DEFAULT_AUTO_ROUTER_CONFIDENCE_THRESHOLD`. */
  confidence_threshold?: number;
}

/**
 * Output of the router. Returned to the VSCode-side resolver as the basis for
 * `AdapterDecision { source: "auto-router" }` when the pick is confident.
 */
export interface AutoRouterDecision {
  /** Adapter the router chose. */
  adapter: RouterExecutionAdapter;
  /** Concrete model identifier compatible with the chosen adapter. */
  model: string;
  /** Human-readable reasoning string for logs and history. */
  rationale: string;
  /** Margin between top and second score, clipped to `[0, 1]`. */
  confidence: number;
  /** Per-adapter scores, exposed for debug and dashboards. */
  scores?: Partial<Record<RouterExecutionAdapter, number>>;
}

/**
 * Stage categories used by the capability scoring matrix. Mirrors
 * `AutoModelSelector.StageCategory` so router and selector stay aligned when
 * a new category is introduced.
 */
export type RouterStageCategory =
  "classification" | "planning" | "dev" | "validate" | "lightweight" | "merge";
