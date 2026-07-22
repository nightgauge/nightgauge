/**
 * OrchestrationConfig — the config knobs that steer the provider-neutral
 * `WorkflowEngine` (epic #3899). Orchestration is **off by default**: the engine
 * is opt-in while the epic lands, so an unset config resolves to "disabled, no
 * native offload, no budget cap".
 *
 * The selection point (`selectExecutor`, #3912) and the `WorkflowExecutor`
 * (#3908) read the *resolved* config — never the raw optional — so an unset knob
 * is a documented default, never `undefined`. Resolve once via
 * `resolveOrchestrationConfig` and surface the single resolved value.
 *
 * Maps onto `WorkflowSpec`: `preferNativeOffload` ← per-stage
 * `prefer_native_offload`; `budgetUsd` ← `max_usd`; `ceiling` ← `max_agents` /
 * `max_concurrency` (each tightens the provider ceiling, never raises it).
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md § Configuration knobs
 * @see Issue #3901
 */

import type { PipelineStage } from "../../events/EventBus.js";

/**
 * Pipeline stages that may be fanned out. `pipeline-start` / `pipeline-finish`
 * are lifecycle markers and `pr-create` / `pr-merge` are single-agent
 * deterministic phases by design (never fanned out) — so neither is a key.
 */
export type OrchestrationStage = Exclude<PipelineStage, "pipeline-start" | "pipeline-finish">;

/** Per-stage `prefer_native_offload` map. A missing stage resolves to `false`. */
export type PreferNativeOffloadMap = Partial<Record<OrchestrationStage, boolean>>;

/**
 * Raw orchestration config as it appears in the SDK / VSCode / manifest schemas.
 * Every field is optional — `resolveOrchestrationConfig` fills the defaults.
 */
export interface OrchestrationConfig {
  /**
   * Disable the orchestration engine entirely. Default `true` (off by default).
   * The env var `CLAUDE_CODE_DISABLE_WORKFLOWS` forces this to `true` regardless
   * of config, so a kill-switch is always available.
   */
  disabled?: boolean;
  /**
   * Prefer an adapter's native `runWorkflow?()` offload over the portable
   * `SdkFanoutRunner` floor when the resolved adapter declares
   * `native-workflow`. Per-stage; a missing stage means "use the floor".
   */
  prefer_native_offload?: PreferNativeOffloadMap;
  /**
   * Total USD budget for a single orchestrated run. `0` means uncapped.
   * Maps to `WorkflowSpec.budgetUsd`.
   */
  max_usd?: number;
  /**
   * Max agents spawned over a whole run. `0` means "use the provider ceiling".
   * When > 0 it tightens `WorkflowSpec.ceiling.maxTotal` — it can only lower the
   * provider safety ceiling, never raise it.
   */
  max_agents?: number;
  /**
   * Max agents running at once. `0` means "use the provider ceiling". When > 0
   * it tightens `WorkflowSpec.ceiling.maxConcurrent` — it can only lower the
   * provider safety ceiling, never raise it.
   */
  max_concurrency?: number;
}

/**
 * Fully-resolved orchestration config. Every field is present, so consumers read
 * a single concrete value and never branch on `undefined`.
 */
export interface ResolvedOrchestrationConfig {
  disabled: boolean;
  prefer_native_offload: PreferNativeOffloadMap;
  max_usd: number;
  max_agents: number;
  max_concurrency: number;
}

/** Env var kill-switch — when truthy, orchestration is disabled regardless of config. */
export const DISABLE_WORKFLOWS_ENV = "CLAUDE_CODE_DISABLE_WORKFLOWS";

/**
 * The single source of truth for orchestration defaults. Off by default, no
 * native offload, no budget/agent/concurrency cap.
 */
export const DEFAULT_ORCHESTRATION_CONFIG: ResolvedOrchestrationConfig = {
  disabled: true,
  prefer_native_offload: {},
  max_usd: 0,
  max_agents: 0,
  max_concurrency: 0,
};

function isEnvDisabled(env: NodeJS.ProcessEnv): boolean {
  const raw = env[DISABLE_WORKFLOWS_ENV];
  if (raw === undefined) return false;
  const lower = raw.trim().toLowerCase();
  return lower === "true" || lower === "1" || lower === "yes";
}

function nonNegative(value: number | undefined, fallback: number): number {
  if (value === undefined || !Number.isFinite(value) || value < 0) return fallback;
  return value;
}

/**
 * Resolve a raw (possibly `undefined`) orchestration config to a concrete value.
 * Unset knobs fall back to {@link DEFAULT_ORCHESTRATION_CONFIG}; the
 * `CLAUDE_CODE_DISABLE_WORKFLOWS` env var forces `disabled: true`.
 *
 * @param config raw config block, or `undefined` when none is configured
 * @param env process env (override for tests)
 */
export function resolveOrchestrationConfig(
  config?: OrchestrationConfig,
  env: NodeJS.ProcessEnv = process.env
): ResolvedOrchestrationConfig {
  const envDisabled = isEnvDisabled(env);
  return {
    disabled: envDisabled || (config?.disabled ?? DEFAULT_ORCHESTRATION_CONFIG.disabled),
    prefer_native_offload: {
      ...DEFAULT_ORCHESTRATION_CONFIG.prefer_native_offload,
      ...config?.prefer_native_offload,
    },
    max_usd: nonNegative(config?.max_usd, DEFAULT_ORCHESTRATION_CONFIG.max_usd),
    max_agents: nonNegative(config?.max_agents, DEFAULT_ORCHESTRATION_CONFIG.max_agents),
    max_concurrency: nonNegative(
      config?.max_concurrency,
      DEFAULT_ORCHESTRATION_CONFIG.max_concurrency
    ),
  };
}

/**
 * Whether the engine should fan out a given stage with native offload. Reads the
 * resolved config so it never returns `undefined`: a stage with no entry — or any
 * stage while orchestration is disabled — resolves to `false` (portable floor).
 */
export function prefersNativeOffload(
  config: ResolvedOrchestrationConfig,
  stage: OrchestrationStage
): boolean {
  if (config.disabled) return false;
  return config.prefer_native_offload[stage] ?? false;
}
