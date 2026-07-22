/**
 * WorkflowQuotaGate — consults the Go-bridged ratelimit/cooldown quota state
 * before a large fan-out so the `WorkflowExecutor` (#3908) distinguishes a
 * genuine quota exhaustion (defer/throttle) from a transient status=allowed
 * stall (proceed). Epic #3899, Issue #3909.
 *
 * SINGLE SOURCE OF TRUTH: every quota number and the `exhausted` decision are
 * computed in Go (`internal/ipc` `workflow.quotaState`, backed by the shared
 * rate-limit tracker + the autonomous dispatch cooldown). This module performs
 * NO quota arithmetic of its own — it only decides whether the bridged signal
 * should gate a fan-out of a given size. The Go state reaches the SDK through an
 * injected `QuotaStateProvider` (the VSCode IpcClient's typed
 * `workflowQuotaState()` method); tests inject a fake provider.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see internal/ipc/server.go (workflow.quotaState)
 */

import { plannedAgentCount, type WorkflowSpec } from "./WorkflowSpec.js";

/**
 * The bridged quota/cooldown snapshot — the exact shape the Go
 * `workflow.quotaState` IPC method returns (and the VSCode
 * `WorkflowQuotaStateResult`). Mirrored here so the SDK gate has a typed
 * contract without depending on the VSCode package.
 */
export interface WorkflowQuotaState {
  /** GitHub API requests left in the bucket; -1 when no tracker reading. */
  remaining: number;
  /** GitHub API bucket size; -1 when no tracker reading. */
  limit: number;
  /** Unix-seconds at which the GitHub bucket refills; 0 when unknown. */
  resetsAt: number;
  /** ISO-8601 deadline of an active dispatch cooldown, if any. */
  cooldownUntil?: string;
  /** Human-readable reason for the cooldown, if any. */
  cooldownReason?: string;
  /** Binding constraint when exhausted (e.g. "github-rest", "github-quota"). */
  bucket?: string;
  /** Single derived gate signal computed in Go. */
  exhausted: boolean;
}

/**
 * Injected seam that fetches the current bridged quota state. In the VSCode
 * extension this is `() => ipcClient.workflowQuotaState(githubUser)`; in tests
 * it is a fake returning a fixed snapshot.
 */
export type QuotaStateProvider = () => Promise<WorkflowQuotaState>;

/** What the gate decided and why. */
export type QuotaGateAction = "proceed" | "defer";

/** Outcome of evaluating the quota gate against a planned fan-out. */
export interface QuotaGateDecision {
  action: QuotaGateAction;
  /** True when the bridged signal indicates the fan-out must NOT dispatch now. */
  deferred: boolean;
  /** Whether this fan-out counted as "large" (>= threshold) and was gated. */
  large: boolean;
  /** Planned agent+judge count this decision was made against. */
  plannedAgents: number;
  /** ISO-8601 / Unix-derived hint for when to retry, when deferred. */
  retryAfter?: string;
  /** Human-readable explanation (carries the Go cooldown reason when present). */
  reason: string;
}

/**
 * Below this planned-agent count a fan-out is "small" and is NOT gated against
 * quota — a handful of agents won't meaningfully deepen an exhausted bucket and
 * gating them would needlessly stall trivial runs. At or above it, the fan-out
 * is "large" and the bridged `exhausted` signal blocks dispatch.
 */
export const DEFAULT_LARGE_FANOUT_THRESHOLD = 16;

/**
 * Pure decision: given a bridged quota snapshot and a planned fan-out size,
 * decide whether to proceed or defer. The Go side already decided `exhausted`;
 * this only applies the "large fan-out only" policy and surfaces a retry hint.
 *
 * - Small fan-out (< threshold): always `proceed` — quota is not gated.
 * - Large fan-out + `exhausted`: `defer` with a retry hint from the cooldown
 *   deadline (preferred) or the GitHub bucket `resetsAt`.
 * - Large fan-out + not exhausted: `proceed` (the status=allowed stall case).
 */
export function evaluateQuotaGate(
  state: WorkflowQuotaState,
  plannedAgents: number,
  largeFanoutThreshold: number = DEFAULT_LARGE_FANOUT_THRESHOLD
): QuotaGateDecision {
  const large = plannedAgents >= largeFanoutThreshold;

  if (!large || !state.exhausted) {
    return {
      action: "proceed",
      deferred: false,
      large,
      plannedAgents,
      reason: large
        ? "quota not exhausted — proceeding with large fan-out"
        : `fan-out of ${plannedAgents} below large-fan-out threshold ${largeFanoutThreshold} — not gated`,
    };
  }

  // Exhausted + large: defer. Prefer the cooldown deadline as the retry hint;
  // fall back to the GitHub bucket reset when only the bucket is depleted.
  let retryAfter: string | undefined;
  if (state.cooldownUntil) {
    retryAfter = state.cooldownUntil;
  } else if (state.resetsAt > 0) {
    retryAfter = new Date(state.resetsAt * 1000).toISOString();
  }

  const bucket = state.bucket ?? "unknown";
  const detail = state.cooldownReason ?? `${bucket} bucket exhausted`;
  return {
    action: "defer",
    deferred: true,
    large,
    plannedAgents,
    retryAfter,
    reason: `quota exhausted (${bucket}) — deferring fan-out of ${plannedAgents} agents: ${detail}`,
  };
}

/**
 * Async convenience the `WorkflowExecutor` calls before fanning out a spec:
 * fetches the bridged state via `provider`, sizes the fan-out from the spec, and
 * returns the gate decision. When the provider itself fails (IPC unavailable),
 * the gate fails OPEN (proceed) so a transient bridge outage never wedges the
 * executor — the hard concurrency ceiling in `runSdkFanout` remains the
 * unconditional safety control.
 */
export async function gateWorkflowFanout(
  spec: WorkflowSpec,
  provider: QuotaStateProvider,
  largeFanoutThreshold: number = DEFAULT_LARGE_FANOUT_THRESHOLD
): Promise<QuotaGateDecision> {
  const plannedAgents = plannedAgentCount(spec);
  let state: WorkflowQuotaState;
  try {
    state = await provider();
  } catch (err) {
    return {
      action: "proceed",
      deferred: false,
      large: plannedAgents >= largeFanoutThreshold,
      plannedAgents,
      reason: `quota provider unavailable (${err instanceof Error ? err.message : String(err)}) — gate failing open`,
    };
  }
  return evaluateQuotaGate(state, plannedAgents, largeFanoutThreshold);
}
