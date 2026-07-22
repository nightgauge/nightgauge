/**
 * Failure Category Classifier
 *
 * Classifies pipeline failure outcomes into one of three categories using
 * heuristic error-text pattern matching. Used by analyzeReliability to
 * apply differential weighting so infrastructure and transient failures
 * don't unfairly depress the health score.
 *
 * Categories:
 *   - infrastructure: Pipeline tooling/runtime failures (schema errors, I/O)
 *   - agent: Transient/recoverable AI-side failures (timeouts, rate limits)
 *   - organic: True implementation failures (the default)
 *
 * @see Issue #1260 - Classify infrastructure vs. organic failures
 * @see docs/FAILURE_TAXONOMY.md for full taxonomy documentation
 */

import { MODEL_REGISTRY } from "../../eval/modelRegistry.js";

export type FailureCategory = "infrastructure" | "agent" | "organic";

/**
 * Differential weights for failure categories used in reliability scoring.
 *
 * Infrastructure failures count 5% — they reflect tooling issues, not code quality.
 * Agent failures count 50% — transient but worth tracking.
 * Organic failures count 100% — true implementation failures get full weight.
 */
export const FAILURE_CATEGORY_WEIGHTS: Record<FailureCategory, number> = {
  infrastructure: 0.05,
  agent: 0.5,
  organic: 1.0,
};

/**
 * Classify a pipeline failure by its error text and stage name.
 *
 * Uses case-insensitive substring matching against known error patterns.
 * The default when no pattern matches (or when errorText is absent) is
 * `'organic'` — the conservative choice that penalises unknown failures
 * fully rather than silently excusing them.
 *
 * @param errorText - The error message or stack trace from the failed stage
 * @param _stage    - The pipeline stage name (reserved for future per-stage rules)
 * @returns The failure category
 */
export function classifyFailureCategory(
  errorText: string | undefined,
  _stage: string
): FailureCategory {
  if (!errorText) return "organic";
  const t = errorText.toLowerCase();

  // Premature turn end (#74): the agent ended its turn on a promise with no
  // state change. Agent-class by definition — and matched BEFORE the
  // infrastructure block because the embedded gate reason usually names the
  // missing context file, which would otherwise bucket this as
  // infrastructure (0.05) and hide an agent behavior failure (0.5).
  if (
    t.includes("premature turn end") ||
    t.includes("premature_turn_end") ||
    t.includes("exited 0 but did not write expected output context")
  ) {
    return "agent";
  }

  // Infrastructure: pipeline tooling/runtime failures
  if (
    t.includes("schema validation") ||
    t.includes("pre-condition failed") ||
    t.includes("context file") ||
    t.includes("enoent") ||
    t.includes("eacces") ||
    t.includes("eperm") ||
    t.includes("invalid json") ||
    t.includes("extension lifecycle") ||
    t.includes("failed to read") ||
    t.includes("cannot read") ||
    t.includes("pipeline state") ||
    // Stage cost cap kill (Issue #3002): pipeline guardrail firing, not a
    // code defect. Treated as infrastructure so cap-triggered terminations
    // do not depress the reliability score.
    t.includes("[cost-cap-exceeded]") ||
    t.includes("cost cap exceeded") ||
    // Baseline-CI gate deferral (Issue #3004): the pipeline correctly held
    // an issue because `main`'s recent CI runs of a referenced workflow are
    // failing. Deferral is not a failure — it is a controlled hold. Counted
    // as infrastructure (0.05 weight) so the pause appears in trends without
    // tanking the reliability score.
    t.includes("[baseline-ci-deferred]") ||
    t.includes("baseline ci deferred") ||
    t.includes("baseline-ci red") ||
    // Native blockedBy deferral (Issue #231): issue-pickup correctly held an
    // issue because it has an OPEN native `blockedBy` dependency (blocker's PR
    // not merged). Deferral is not a failure — it is a controlled hold, auto-
    // resumed when the blockers close. Counted as infrastructure (0.05 weight)
    // so the pause appears in trends without tanking the reliability score.
    t.includes("[blocked-dependency]") ||
    t.includes("blocked by open dependency") ||
    // Adapter auth pre-flight failure (Issue #312): the pipeline-start auth
    // gate refused to launch (probe timed out under a burst, or the adapter
    // CLI is logged out). Environmental — a probe starvation or a credential
    // state, not the issue's code — so it counts at the 0.05 infrastructure
    // weight rather than depressing reliability like an organic failure.
    // Matched before the "timeout"/"api error" agent block so a timed-out
    // probe still lands here. Issue #312.
    t.includes("[adapter-auth-failed]") ||
    t.includes("adapter_auth_failed")
  ) {
    return "infrastructure";
  }

  // No changes produced (Issue #317): pr-create's deterministic create
  // fallback confirmed the feature branch has zero commits ahead of base —
  // genuinely nothing to open a PR for (e.g. a human-only `owner-action`
  // issue dispatched before the exclusion existed). This is a planning/scope
  // failure — the dispatcher's responsibility — not a pipeline tooling/
  // runtime defect, so it does NOT belong in the infrastructure bucket
  // (0.05). Weighted `agent` (0.5), the same bucket as premature_turn_end:
  // both are "the run ended without producing state", attributable to the
  // run's own behavior rather than the environment, but not a full-weight
  // organic implementation bug. Matched BEFORE the agent timeout/rate-limit
  // block below (no overlap expected, but keeps ordering consistent with the
  // Go classifier).
  if (t.includes("[no-changes-produced]") || t.includes("no_changes_produced")) {
    return "agent";
  }

  // Agent: transient/recoverable AI-side failures
  if (
    t.includes("timeout") ||
    t.includes("etimedout") ||
    t.includes("rate limit") ||
    t.includes("503") ||
    t.includes("502") ||
    t.includes("504") ||
    t.includes("context exhausted") ||
    t.includes("token limit") ||
    t.includes("maximum context") ||
    t.includes("api error") ||
    t.includes("overloaded") ||
    // Stall-killed: subagent exceeded time threshold (Issue #2871)
    t.includes("stall kill threshold") ||
    t.includes("stalled and killed") ||
    t.includes("heartbeat stall") ||
    // Stall-killed after adaptive retry exhausted (Issue #3005). The first
    // stall already consumed its rewind slot; a second stall is terminal but
    // the underlying cause is still agent-class.
    t.includes("stall-killed-after-retry")
  ) {
    return "agent";
  }

  // Default: organic (implementation failure — full weight). Both
  // subagent_crash and validation_failed (#326) rely on this fallthrough
  // rather than a dedicated block above — neither's marker text collides
  // with the infrastructure/agent buckets, and organic full weight is
  // exactly the taxonomy classification both kinds require.
  return "organic";
}

/**
 * Terminal failure kinds — what aborted the pipeline run (Issue #3001).
 *
 * Independent of `FailureCategory` (which buckets failures by responsibility
 * for weighted reliability scoring). The terminal kind answers "what stopped
 * the run"; the category answers "who/what is to blame".
 *
 * Mirrors the Zod `TerminalFailureKindSchema` in
 * `packages/nightgauge-vscode/src/schemas/executionHistory.ts`.
 */
export type TerminalFailureKind =
  | "stall_kill"
  | "budget_exceeded"
  | "validation_error"
  | "subagent_crash"
  | "orchestrator_crash"
  | "network_unavailable" // Issue #3296
  | "stream_idle_timeout" // Issue #3398
  | "rate_limit_quota_exhausted" // Issue #3386
  | "worktree_uncommitted" // Issue #3542 — failure recovered, work preserved
  | "budget_ceiling_hit" // Issue #3542 — USD pipeline ceiling tripped
  | "issue_closed" // Issue #3661 — issue already closed when pipeline started (non-failure)
  | "api_overloaded" // Issue #3835 — Anthropic 529 "Overloaded"; transient, retried without queue pause
  | "github_quota_low" // Issue #3896 — GitHub API quota below headroom at pipeline-start; transient, cooldown until reset
  | "api_connection_lost" // Issue #4002 — Anthropic API transport drop (socket close / DNS blip); transient, retried without queue pause
  | "github_network_outage" // Issue #4002 — api.github.com unreachable at pipeline-start; transient, short global cooldown
  | "model_unavailable" // Issue #42 — API rejected the selected model (not on plan / unknown / model usage cap); triggers tier-downgrade fallback
  | "premature_turn_end" // Issue #74 — stage exited 0 but its gate reported no state change (agent ended its turn on a promise)
  | "adapter_auth_failed" // Issue #312 — adapter auth pre-flight failed (probe timed out after retry, or definitively logged out); retryable infra
  | "no_changes_produced" // Issue #317 — pr-create's deterministic fallback confirmed zero commits ahead of base; genuinely nothing to open a PR for (e.g. a dispatched human-only issue)
  | "validation_failed"; // Issue #326 — feature-validate honestly failed its quality gates (validation_status="failed"); organic implementation failure, not a subagent crash

/**
 * Classify the *kind* of terminal failure from an error message.
 *
 * Pure heuristic — same matching style as `classifyFailureCategory` so the two
 * stay aligned. Returns `undefined` when no pattern matches; callers can fall
 * back to `"subagent_crash"` (the most generic kind) or leave the field absent.
 *
 * @param errorText - Error message or stack trace from the failed stage
 * @returns The terminal failure kind, or undefined when unclassifiable
 */
export function classifyTerminalKind(
  errorText: string | undefined
): TerminalFailureKind | undefined {
  if (!errorText) return undefined;
  const t = errorText.toLowerCase();

  // Stall-kill: heuristics aligned with classifyFailureCategory's "agent" bucket.
  if (
    t.includes("stall kill threshold") ||
    t.includes("stalled and killed") ||
    t.includes("heartbeat stall") ||
    t.includes("hard cap")
  ) {
    return "stall_kill";
  }

  // Budget enforcer reasons (see internal/orchestrator/budget_enforcer.go).
  if (
    t.includes("pipeline_budget_exceeded") ||
    t.includes("stage_budget_exceeded") ||
    t.includes("budget exceeded") ||
    t.includes("budget ceiling")
  ) {
    return "budget_exceeded";
  }

  // Premature turn end (#74): the stage exited 0 but produced no state
  // change — the agent ended its turn on a promise. Two structural
  // producers: the Go gate hook stamps `premature turn end:` on a no-op
  // gate, and validateStageOutput (#2870) emits `exited 0 but did not
  // write expected output context` (exit-0 paths only, previously bucketed
  // as validation_error). Matched BEFORE validation_error so the embedded
  // gate reason (which often names the missing context file) doesn't
  // bucket there. The pr-merge no-op shape keeps its richer
  // pr_merge_unmerged classification in the Go classifier — mirror that
  // precedence here by excluding it (this classifier has no
  // pr_merge_unmerged matcher).
  if (
    (t.includes("premature turn end") ||
      t.includes("premature_turn_end") ||
      t.includes("exited 0 but did not write expected output context")) &&
    !(t.includes("pr-merge reported success") && t.includes("is not merged"))
  ) {
    return "premature_turn_end";
  }

  // Schema/validation failures — reuse the infrastructure-bucket heuristics.
  // (The "did not write expected output context" phrase moved to
  // premature_turn_end above — its only producer runs on exit-0 paths.)
  if (
    t.includes("schema validation") ||
    t.includes("invalid json") ||
    t.includes("missing prerequisite")
  ) {
    return "validation_error";
  }

  // GitHub API quota too low at the pipeline-start preflight (#3896) —
  // environmental and transient. Match before the subagent_crash fallback so
  // the marker is never mis-bucketed as a process death.
  if (
    t.includes("github-quota-low") ||
    t.includes("github_quota_low") ||
    (t.includes("pipeline-start-failure") && t.includes("github api quota too low"))
  ) {
    return "github_quota_low";
  }

  // GitHub unreachable at the pipeline-start preflight (#4002) — the
  // connectivity sibling of github_quota_low. Matched before the
  // subagent_crash fallback so the marker is never mis-bucketed.
  if (t.includes("github-network-outage") || t.includes("github_network_outage")) {
    return "github_network_outage";
  }

  // Anthropic API transport drop (#4002) — the stream died on a socket
  // close / hang up during a local network blip. Matched before the
  // subagent_crash fallback so a seconds-long blip isn't misread as a
  // process death.
  if (
    t.includes("socket connection was closed") ||
    t.includes("socket hang up") ||
    t.includes("api_connection_lost")
  ) {
    return "api_connection_lost";
  }

  // Model rejected by the API (#42): unknown/invalid model ID, a model not
  // offered on the current plan, or a model-specific usage cap. Mirrors the
  // Go classifier (internal/orchestrator/failure_handler.go) — plan/cap
  // phrases are gated on a registry model reference so account-level limit
  // messages keep routing to the quota path. Matched before the
  // subagent_crash fallback so a rejection isn't misread as a process death.
  if (isModelUnavailableText(t)) {
    return "model_unavailable";
  }

  // Adapter auth pre-flight failure (#312) — the pipeline-start auth gate
  // refused to launch: an adapter probe either timed out after a retry
  // (transient starvation under a concurrent burst) or came back definitively
  // logged out. Either way it is NOT a subagent process death, and the burst
  // false-negative must never feed the cascade breaker as one. Matched on the
  // stable `[adapter-auth-failed]` marker (and the underscore kind) BEFORE the
  // subagent_crash fallback, whose "exit " heuristic would otherwise bucket it.
  if (t.includes("[adapter-auth-failed]") || t.includes("adapter_auth_failed")) {
    return "adapter_auth_failed";
  }

  // No changes produced (#317) — pr-create's deterministic create fallback
  // confirmed the feature branch has zero commits ahead of base and the
  // working tree has no source changes: genuinely nothing to open a PR for.
  // Matched on the stable `[no-changes-produced]` marker BEFORE the
  // subagent_crash fallback, whose "exit " heuristic would otherwise bucket
  // this as a process death. Deliberately NOT matched on bare "no commits
  // ahead of" — that phrase also appears in feature-validate's unrelated
  // lost-implementation check, which must keep its organic classification.
  if (t.includes("[no-changes-produced]") || t.includes("no_changes_produced")) {
    return "no_changes_produced";
  }

  // Feature-validate honest quality-gate failure (#326). The skill exited 0
  // but wrote validation_status="failed" and left the code uncommitted for
  // retry — a real organic implementation failure caught by the pipeline's
  // own gate, not a subagent process death. Matched on the stable
  // `[validation-failed]` marker BEFORE the subagent_crash fallback, whose
  // "exit " heuristic would otherwise bucket this as a process death.
  if (t.includes("[validation-failed]") || t.includes("validation_failed")) {
    return "validation_failed";
  }

  // Process death / non-zero exit fallback.
  if (
    t.includes("subagent crash") ||
    t.includes("exit ") || // matches "exit 1: …" from scheduler.SetStageError
    t.includes("killed by signal")
  ) {
    return "subagent_crash";
  }

  return undefined;
}

/**
 * Mirror of the Go matcher in internal/orchestrator/failure_handler.go (#42).
 * Anthropic shapes covered: 404 `not_found_error` naming the model, invalid /
 * unknown model wording, plan restrictions, and model-specific usage caps.
 */
function isModelUnavailableText(t: string): boolean {
  if (t.includes("not_found_error") && t.includes("model")) return true;
  if (t.includes("model not found") || t.includes("invalid model") || t.includes("unknown model")) {
    return true;
  }
  const planPhrase =
    t.includes("not available on your") ||
    t.includes("not included in your") ||
    t.includes("not offered on your") ||
    t.includes("not supported on your");
  const capPhrase =
    t.includes("usage limit") || t.includes("usage cap") || t.includes("weekly limit");
  return (planPhrase || capPhrase) && mentionsRegistryModel(t);
}

/** Registry-derived "names a specific model" gate — IDs, display names, tiers. */
function mentionsRegistryModel(t: string): boolean {
  const tiers = new Set<string>();
  for (const m of MODEL_REGISTRY) {
    if (m.id && t.includes(m.id.toLowerCase())) return true;
    if (m.display_name && t.includes(m.display_name.toLowerCase())) return true;
    for (const tier of m.tiers ?? []) tiers.add(tier.toLowerCase());
  }
  for (const tier of tiers) {
    if (t.includes(tier)) return true;
  }
  return false;
}
