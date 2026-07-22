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
import type { PipelineStage } from "@nightgauge/sdk";
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
): { model: string; mismatch: boolean } | undefined {
  if (adapter === "claude") return undefined;
  const profile = getModeStageProfile(mode, stage);
  if (!profile?.model) return undefined;
  const resolved = resolveModelForAdapter(adapter, profile.model);
  if (resolved) return { model: resolved.id, mismatch: false };
  return { model: profile.model, mismatch: true };
}
