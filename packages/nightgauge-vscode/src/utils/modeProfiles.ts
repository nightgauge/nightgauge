/**
 * Performance Mode Profiles (Issue #3009)
 *
 * Single source of truth for the mapping between a named performance mode
 * (efficiency / elevated / maximum / frontier) and the per-stage model +
 * effort overrides that mode applies. UI and orchestrator code MUST look up
 * `MODE_PROFILES[mode]` rather than branching on mode names directly so
 * future tuning is a one-file edit.
 *
 * Ordered by capability and cost: efficiency < elevated < maximum < frontier.
 *
 * Modes:
 *   - efficiency: lowers the routing ceiling — Haiku for lightweight stages,
 *     Sonnet for dev/validate. Effort tilts low/medium. Cheaper, faster,
 *     less reasoning headroom.
 *   - elevated:   no overrides — represents today's default routing. Adaptive
 *     policy + AutoModelSelector continue to operate unchanged. The new
 *     default for migrated and first-time users.
 *   - maximum:    raises the floor to Opus + effort=high, raises stall
 *     multiplier 10×, disables the pipeline budget ceiling. Replicates the
 *     legacy Supercharge envelope.
 *   - frontier:   premium opt-in tier above maximum. Routes the reasoning
 *     stages (feature-planning, feature-dev, feature-validate) to Fable 5 —
 *     the frontier model at ~2× Opus cost — and keeps mechanical stages on
 *     Haiku so frontier rates are never paid for git plumbing. Keeps the
 *     budget ceiling ENABLED (unlike maximum) precisely because Fable is the
 *     most expensive tier — the guardrail matters most here. Fable is never
 *     reached by automatic routing; selecting frontier is the deliberate
 *     opt-in.
 *
 * @see docs/PERFORMANCE_MODES.md
 * @see Issue #3009
 */
import type { ModelEnvelope, PipelineStage } from "@nightgauge/sdk";
import { resolveModelForAdapter } from "@nightgauge/sdk";
import type { ClaudeEffort, DefaultModel } from "./incrediConfig";
import type { ExecutionAdapter } from "./resolvers/modelResolver";

export type PerformanceMode = "efficiency" | "elevated" | "maximum" | "frontier";

export const PERFORMANCE_MODES: readonly PerformanceMode[] = [
  "efficiency",
  "elevated",
  "maximum",
  "frontier",
] as const;

export const DEFAULT_PERFORMANCE_MODE: PerformanceMode = "elevated";

/** Per-stage override produced by a mode lookup. */
export interface StageProfile {
  /** Override model for this stage (undefined → fall through to default routing) */
  model?: DefaultModel;
  /** Override effort for this stage */
  effort?: ClaudeEffort;
  /** Optional Codex model override (forwarded as the supercharge-codex equivalent) */
  codexModel?: string;
}

/** Result of translating a registry band for one execution adapter. */
export interface AdapterModelMapping {
  model: string;
  mismatch: boolean;
}

/**
 * Performance-mode routing envelope (Issue #19).
 *
 * A mode expresses its cost/quality posture as a `[floor, ceiling]` band that
 * the adaptive router selects within — instead of pinning a fixed model per
 * stage. `ceiling: "fable"` is the only way automatic routing can reach the
 * frontier tier (and even then only for heavy reasoning stages at L/XL; see
 * `AutoModelSelector.selectModel`). Lightweight/plumbing stages keep their cheap
 * defaults but are still clamped to the band.
 */
export interface ModeEnvelope {
  /** Router never selects below this tier. */
  floor: DefaultModel;
  /** Router never escalates above this tier. */
  ceiling: DefaultModel;
  /** Cap the complexity-derived effort (Efficiency trades reasoning for cost). */
  effortCeiling?: ClaudeEffort;
  /** Raise the complexity-derived effort (Maximum reasons hard everywhere). */
  effortFloor?: ClaudeEffort;
  /**
   * Pin the mode's thinking state, overriding the dispatched rung's declared
   * thinking default (#606, spike #568 §4.1.3: envelopes are `(rung floor,
   * rung ceiling, effort ceiling, thinking policy)`). No mode declares a
   * value today — the axis exists so a mode CAN, with Go⇄TS parity
   * (ModeEnvelope.ThinkingPolicy, performance_mode.go, consumed by the wire
   * thinking resolution; the CLAUDE_CODE_DISABLE_THINKING interlock always
   * outranks a policy).
   */
  thinkingPolicy?: "on" | "off";
}

/** Pipeline-level (mode-wide, non-stage) overrides. */
export interface PipelineProfile {
  /**
   * Multiplier applied to the configured stall threshold before
   * the watchdog kills a stalled stage. `undefined` → use config default.
   */
  stallKillMultiplier?: number;
  /**
   * When true, the pre-flight pipeline-cost ceiling is bypassed for this run.
   * `undefined` → enforcement follows config default.
   */
  disableBudgetCeiling?: boolean;
}

export interface ModeProfile {
  stages: Partial<Record<PipelineStage, StageProfile>>;
  /**
   * Router envelope for this mode (Issue #19). When present, stages without an
   * explicit pin in `stages` flow through the adaptive router clamped to this
   * band. `undefined` falls back to `DEFAULT_MODE_ENVELOPE`.
   */
  envelope?: ModeEnvelope;
  pipeline: PipelineProfile;
  /** One-line description shown in the QuickPick + status bar tooltip. */
  description: string;
  /** Cost direction hint shown alongside the description. */
  costHint: string;
  /** Capitalized label for status-bar / QuickPick rendering. */
  label: string;
}

/**
 * Mode → per-stage profile table.
 *
 * Elevated supplies no overrides — its routing is identical to today's
 * default. Calibration baselines see Elevated runs unchanged.
 *
 * Maximum stage profiles replicate the legacy Supercharge envelope:
 * Opus + effort=high across every stage, 10× stall multiplier, disabled
 * budget ceiling.
 *
 * Efficiency targets cost reduction: Haiku where it suffices, Sonnet for
 * heavier reasoning stages, effort lowered to low/medium. Adaptive policy
 * still picks within this envelope (see docs/SELF_IMPROVEMENT_LOOP.md).
 */
export const MODE_PROFILES: Record<PerformanceMode, ModeProfile> = {
  efficiency: {
    label: "Efficiency",
    description: "Cheap and fast — router capped at Sonnet, Haiku where it suffices.",
    costHint: "≈ baseline ÷ 2",
    // Router-driven within [haiku, sonnet]: no stage ever reaches Opus. Effort
    // capped at medium to keep reasoning cost down.
    stages: {},
    envelope: { floor: "haiku", ceiling: "sonnet", effortCeiling: "medium" },
    pipeline: {},
  },
  elevated: {
    label: "Elevated",
    description: "Balanced default — adaptive routing, Haiku…Opus.",
    costHint: "≈ baseline",
    // The open envelope: exactly today's routing (haiku floor, Opus ceiling,
    // Fable unreachable by automatic routing).
    stages: {},
    envelope: { floor: "haiku", ceiling: "opus" },
    pipeline: {},
  },
  maximum: {
    label: "Maximum",
    description: "Best-effort quality — Opus + effort=high everywhere, no budget ceiling.",
    costHint: "≈ baseline × 4",
    // Deliberate pins: "cost no object" genuinely means pin high on every stage.
    // Kept as explicit pins (not just floor=opus) so plumbing stages are Opus too.
    stages: {
      "issue-pickup": { model: "opus", effort: "high" },
      "feature-planning": { model: "opus", effort: "high" },
      "feature-dev": { model: "opus", effort: "high" },
      "feature-validate": { model: "opus", effort: "high" },
      "pr-create": { model: "opus", effort: "high" },
      "pr-merge": { model: "opus", effort: "high" },
    },
    envelope: { floor: "opus", ceiling: "opus", effortFloor: "high" },
    pipeline: {
      stallKillMultiplier: 10,
      disableBudgetCeiling: true,
    },
  },
  frontier: {
    label: "Frontier",
    description:
      "Premium frontier tier — router may reach Fable 5 on hard (L/XL) planning & dev only.",
    costHint: "≈ maximum, Fable only on hard reasoning",
    // Router-driven within [haiku, fable]. Fable is reached ONLY on L/XL
    // planning/dev (see AutoModelSelector frontier-reasoning escalation); plumbing
    // stays Haiku and feature-validate never exceeds Opus. This replaces the old
    // "Fable pinned on every reasoning stage" behavior that paid frontier rates
    // for trivial work and empirically failed validation in dogfooding.
    stages: {},
    envelope: { floor: "haiku", ceiling: "fable" },
    // Budget ceiling deliberately left ENABLED (no disableBudgetCeiling) — Fable
    // is the most expensive tier, so the guardrail stays on. The stall window is
    // widened because frontier reasoning runs longer.
    pipeline: {
      stallKillMultiplier: 10,
    },
  },
};

/** Fallback envelope when a mode supplies none — today's Elevated band. */
export const DEFAULT_MODE_ENVELOPE: ModeEnvelope = { floor: "haiku", ceiling: "opus" };

/**
 * Resolve the routing envelope for a mode (Issue #19). Modes that still pin
 * every stage (Maximum) also carry an envelope for consistency, but their pins
 * short-circuit the router before the envelope is consulted.
 */
export function getModeEnvelope(mode: PerformanceMode): ModeEnvelope {
  return MODE_PROFILES[mode].envelope ?? DEFAULT_MODE_ENVELOPE;
}

/**
 * Heavy generative reasoning stages — the only ones a `fable` ceiling
 * escalates. Mirrors `AutoModelSelector`'s frontier-reasoning rule
 * (`stageCategory === "planning" || stageCategory === "dev"`) and its Go pair
 * `frontierReasoningStage` (internal/intelligence/routing/performance_mode.go).
 */
function isFrontierReasoningStage(stage: PipelineStage): boolean {
  return stage === "feature-planning" || stage === "feature-dev";
}

/**
 * `getModeEnvelope` narrowed to the band a PIPELINE-CHOSEN tier may land in for
 * one stage (#340). Go pair: `routing.RoutedTierEnvelope`.
 *
 * One rule differs from the raw mode band, and it is the rule
 * `MODE_PROFILES.frontier`'s own comment states: "plumbing stays Haiku and
 * feature-validate never exceeds Opus". `AutoModelSelector` already applies it
 * to its own pick, but the selector is not the only thing that chooses a tier —
 * the `model_routing.minimum_model` floor and (on the Go path) a
 * `run.retryWithEscalation` forced tier arrive after it. Clamping those against
 * the raw `fable` ceiling put `feature-validate` back on Fable, which is the
 * behavior #19 deleted for having "empirically failed validation in
 * dogfooding" — reached through a different door.
 *
 * An explicit per-stage model is NOT clamped by this (or any) envelope; that is
 * the operator overriding the mode, not the pipeline choosing within it.
 */
export function getRoutedTierEnvelope(mode: PerformanceMode, stage: PipelineStage): ModeEnvelope {
  const envelope = getModeEnvelope(mode);
  if (envelope.ceiling === "fable" && !isFrontierReasoningStage(stage)) {
    return { ...envelope, ceiling: "opus" };
  }
  return envelope;
}

/**
 * Convert a mode's `ModeEnvelope` (vscode-side) to the SDK's `ModelEnvelope`
 * shape consumed by `AutoModelSelector.selectModel()` /
 * `estimatePipelineCost()`. Tier names line up 1:1 (`DefaultModel` /
 * `ModelTier` share the haiku < sonnet < opus < fable ordering), so this is a
 * pure field pick — no new mapping table.
 *
 * @since Issue #142 - threads the run's actual performance mode into the
 *   pre-run cost estimator so the estimated model tier matches the tier the
 *   run will actually serve.
 */
export function toModelEnvelope(mode: PerformanceMode): ModelEnvelope {
  const { floor, ceiling } = getModeEnvelope(mode);
  return { floor: floor as ModelEnvelope["floor"], ceiling: ceiling as ModelEnvelope["ceiling"] };
}

/**
 * Resolve the per-stage profile for a given mode and stage.
 * Returns `undefined` when the mode supplies no override for the stage —
 * caller should fall through to the existing routing chain.
 */
export function getModeStageProfile(
  mode: PerformanceMode,
  stage: PipelineStage
): StageProfile | undefined {
  return MODE_PROFILES[mode].stages[stage];
}

/** Type guard for parsing strings read from disk / env. */
export function isPerformanceMode(value: unknown): value is PerformanceMode {
  return (
    value === "efficiency" || value === "elevated" || value === "maximum" || value === "frontier"
  );
}

/**
 * Resolve the adapter-specific model id for a given mode + stage + adapter,
 * via the SDK's provider-aware model registry (#56 — this replaced the
 * hand-maintained per-adapter `ADAPTER_MODEL_TABLES`).
 *
 * Returns `undefined` when:
 *   - `adapter === "claude"` (Claude consumes the canonical alias verbatim).
 *   - The mode supplies no override for the stage (e.g. `elevated`).
 *
 * Returns `{ model, mismatch: true }` when the mode profile names a tier but
 * the adapter's provider has no registry band for it — by design every tier
 * for `lm-studio` / `ollama`, whose user-configured local model serves all
 * tiers. In that case `model` echoes the canonical alias so callers can
 * include it in the warning log; the dispatcher MUST fall back to the
 * adapter's configured default and demote `modelDecision.source` to
 * `"config"` so run history does not falsely advertise `"performance-mode"`
 * (AC #3).
 *
 * Returns `{ model, mismatch: false }` when the registry resolves the tier;
 * the dispatcher uses this id directly.
 *
 * @see docs/PERFORMANCE_MODES.md
 * @see Issue #3214
 */
export function getModeStageAdapterModel(
  mode: PerformanceMode,
  stage: PipelineStage,
  adapter: ExecutionAdapter
): AdapterModelMapping | undefined {
  if (adapter === "claude") return undefined;
  const profile = getModeStageProfile(mode, stage);
  if (!profile?.model) return undefined;
  return getAdapterModelForBand(profile.model, adapter);
}

/**
 * Translate an authoritative registry band to the concrete model id an
 * adapter launches. This is the shared last-mile mapping for dispatched bands
 * and performance-mode stage pins.
 *
 * Local adapters deliberately have no registry hierarchy. They return a
 * mismatch so the dispatcher can retain the configured local model instead of
 * leaking a tier alias to the process.
 */
export function getAdapterModelForBand(
  band: DefaultModel,
  adapter: ExecutionAdapter
): AdapterModelMapping | undefined {
  if (adapter === "claude") return undefined;
  const resolved = resolveModelForAdapter(adapter, band);
  if (resolved) return { model: resolved.id, mismatch: false };
  return { model: band, mismatch: true };
}
