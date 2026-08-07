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
  //
  // dev_produced_no_changes (#202) rides the same branch: it is a narrower
  // premature turn end (feature-dev delegated the work into a worktree the
  // pipeline never reads) and carries the same `agent` weight. Its marker is
  // listed explicitly rather than relying on the "premature turn end" wrapper,
  // so the classification survives any caller that records only the gate
  // reason.
  if (
    t.includes("premature turn end") ||
    t.includes("premature_turn_end") ||
    t.includes("[dev-produced-no-changes]") ||
    t.includes("dev_produced_no_changes") ||
    t.includes("[dev-handoff-missing]") ||
    t.includes("dev_handoff_missing") ||
    // Leaving the assigned worktree is the stage's own behavior, not the
    // environment and not the issue's code (#230).
    t.includes("[stage:worktree-containment]") ||
    t.includes("containment_breach") ||
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
    t.includes("not valid json") ||
    t.includes("unparseable json") ||
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
    t.includes("adapter_auth_failed") ||
    // Anthropic API transport drop (#4002, wording widened in #227). Matched
    // here for the same reason as adapter-auth above: the bare `api error`
    // pattern in the agent block below would otherwise claim it, scoring an
    // Anthropic socket drop at the 0.5 agent weight instead of 0.05
    // infrastructure. A dropped connection is not the agent's behavior — the
    // run had no say in it — and letting it depress the agent reliability
    // score makes the dogfooding metric describe Anthropic's uptime rather
    // than the pipeline's.
    t.includes("api_connection_lost") ||
    (t.includes("api error") && t.includes("connection closed"))
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

  // Branch forked from its remote (#163). Infrastructure (0.05), not organic:
  // the dominant cause is the pipeline's own kill path leaving a pushed commit
  // on origin that nothing tracks, and the other observed cause is an operator
  // pushing to a pipeline-owned branch. Neither says anything about the quality
  // of the implementation the run produced — charging it full organic weight
  // would depress reliability for a tooling defect. Matched before the agent
  // block so a push rejection is never read as a transient API condition.
  if (
    t.includes("[branch-forked]") ||
    t.includes("branch_forked") ||
    t.includes("non-fast-forward")
  ) {
    return "infrastructure";
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
  // Declared-but-unmatched, mirroring Go: set structurally (a gate override),
  // never derived from error text, so classifyTerminalKind has no matcher.
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
  | "dev_produced_no_changes" // Issue #202 — feature-dev's gate found the stage workspace empty (clean tree, branch level with base) despite a truthful dev context; the work landed where the pipeline never reads
  | "containment_breach" // Issue #230 — the write-containment check (#129) found the stage wrote into a repository it does not own; it exits 0 and reports success, so nothing else marks it failed
  | "dev_handoff_missing" // Issue #223 — the inverse of the above: the dev context is absent or empty and git finds the changed files right there; the stage did the work and ended without writing its handoff, so the work must be preserved rather than re-derived
  | "adapter_auth_failed" // Issue #312 — adapter auth pre-flight failed (probe timed out after retry, or definitively logged out); retryable infra
  | "no_changes_produced" // Issue #317 — pr-create's deterministic fallback confirmed zero commits ahead of base; genuinely nothing to open a PR for (e.g. a dispatched human-only issue)
  | "validation_failed" // Issue #326 — feature-validate honestly failed its quality gates (validation_status="failed"); organic implementation failure, not a subagent crash
  | "branch_forked" // Issue #163 — the run's branch diverged from its remote (killed mid-push, or an operator pushed to it); every push is rejected non-fast-forward and no retry clears it
  | "runaway_progress" // Issue #3783 — the progress-based runaway monitor fired; treated like stall_kill (30m backoff, no lifetime-cap increment)
  | "pr_merge_unmerged" // Issue #3691 — pr-merge's session exited cleanly but the PR was not actually merged
  | "blocked_dependency" // Issue #305 — the autonomous scheduler dispatched an issue whose blockedBy dependencies are still open; a non-failure deferral
  | "architecture_approval_required" // Issue #4098/#4222 — the architecture-approval gate halted the run before feature-dev for a human-owned decision
  | "validation_inconclusive" // Issue #221 — feature-validate's unit-test tier ran but executed zero tests; usually an environmental misconfiguration
  // Declared-but-unmatched, mirroring Go: set by the recovery action from
  // structured evidence, never derived from error text, so
  // classifyTerminalKind has no matcher for it either.
  | "abandoned_commit" // Issue #191 — a stage committed valid, unmerged work but was killed/crashed before pr-create ran
  | "commit_orphaned" // Issue #266 — a killed stage's commit landed on the wrong branch (a stray temp-pre-push-<n> left by a SIGKILL bypassing the pre-push restore-defer) and feature-validate's branch-identity self-heal could not recover it; unrecoverable by retry
  | "permission_denied"; // Issue #289 — the harness denied a tool call outright (commonly a foreground `sleep` wait loop, reported as "User rejected tool use"). A denial is the harness saying "not that way", not a defect: the stage had turns left and could pick another approach. Routed like adapter_auth_failed — short backoff, board → Ready, no lifetime-cap increment, no cascade feed, no pause — but bounded by a max-attempt cap so a stage that keeps reaching for the same denied pattern stops re-dispatching

/**
 * Every `TerminalFailureKind` union member, in declaration order. TS union
 * types have no runtime representation, so this array is the enumerable
 * source the parity test diffs against Go's `TerminalKind*` constants.
 */
export const ALL_TERMINAL_FAILURE_KINDS: readonly TerminalFailureKind[] = [
  "stall_kill",
  "budget_exceeded",
  "validation_error",
  "subagent_crash",
  "orchestrator_crash",
  "network_unavailable",
  "stream_idle_timeout",
  "rate_limit_quota_exhausted",
  "worktree_uncommitted",
  "budget_ceiling_hit",
  "issue_closed",
  "api_overloaded",
  "github_quota_low",
  "api_connection_lost",
  "github_network_outage",
  "model_unavailable",
  "premature_turn_end",
  "dev_produced_no_changes",
  "containment_breach",
  "dev_handoff_missing",
  "adapter_auth_failed",
  "no_changes_produced",
  "validation_failed",
  "branch_forked",
  "runaway_progress",
  "pr_merge_unmerged",
  "blocked_dependency",
  "architecture_approval_required",
  "validation_inconclusive",
  "abandoned_commit",
  "commit_orphaned",
  "permission_denied",
];

/**
/**
 * Classify the *kind* of terminal failure from an error message.
 *
 * MIRROR OF GO. `ClassifyTerminalKind` in
 * `internal/orchestrator/failure_handler.go` is the authoritative classifier —
 * it writes `terminal_kind` into the run record and drives the scheduler's
 * recovery routing. This function reproduces it block for block, IN THE SAME
 * ORDER and with the same literals, because order is the whole contract: many
 * real failure strings match two blocks and the earlier one wins.
 *
 * The two are pinned by a shared corpus rather than by this comment
 * (`internal/orchestrator/testdata/terminal-kind/corpus.json`, #306). Before
 * that corpus existed the ladders had drifted 14 ways, including on the four
 * most common real failures on a live machine: `exceeded stall idle threshold`,
 * `[cost-cap-exceeded]`, a bare `adapter-auth-failed`, and the USD ceiling,
 * which this side ordered below the generic budget block despite a comment
 * saying it must come first.
 *
 * Returns `undefined` when no pattern matches; callers can fall back to
 * `"subagent_crash"` (the most generic kind) or leave the field absent.
 *
 * @param errorText - Error message or stack trace from the failed stage
 * @returns The terminal failure kind, or undefined when unclassifiable
 */
export function classifyTerminalKind(
  errorText: string | undefined
): TerminalFailureKind | undefined {
  if (!errorText) return undefined;
  const t = errorText.toLowerCase();

  // Network-unavailable abort (Issue #3296). Classified before everything else
  // because the message surfaces from the cancellation cause and shouldn't
  // accidentally match a generic "exit"/"stall" pattern below.
  if (t.includes("network unavailable: extended github connectivity loss")) {
    return "network_unavailable";
  }

  // Stream idle timeout from the Anthropic API (Issue #3398). Matched before
  // the stall-kill heuristics — the literal "timeout" substring appears in the
  // text and would otherwise bucket into a stall.
  if (t.includes("stream idle timeout")) {
    return "stream_idle_timeout";
  }

  // API "Overloaded" (Anthropic 529) — a transient capacity blip (#3835 WS4).
  // Matched before the generic stall-kill / crash heuristics so a momentary
  // overload routes to the transient recovery path (short backoff, no pause)
  // instead of being misread as a code fault.
  if (t.includes("overloaded")) {
    return "api_overloaded";
  }

  // Anthropic API transport drop (#4002) — the stream died on a socket close,
  // a DNS failure or a refused connection. Matched before the crash fallback so
  // a seconds-long blip isn't misread as a process death. The bare error-code
  // variants are gated on "api error" so an unrelated stage error that merely
  // mentions ECONNRESET (a failing integration test, say) doesn't misclassify.
  // #227 added "connection closed": the live failure read `API Error:
  // Connection closed mid-response`, which matched none of the socket patterns
  // and fell through to subagent_crash, halting the fleet on a blip.
  if (
    t.includes("socket connection was closed") ||
    t.includes("socket hang up") ||
    t.includes("api_connection_lost") ||
    (t.includes("api error") &&
      (t.includes("econnreset") ||
        t.includes("econnrefused") ||
        t.includes("enotfound") ||
        t.includes("eai_again") ||
        t.includes("getaddrinfo") ||
        t.includes("fetch failed") ||
        t.includes("connection reset") ||
        t.includes("connection refused") ||
        t.includes("connection closed")))
  ) {
    return "api_connection_lost";
  }

  // Rate-limit quota exhausted (Issue #3386). Set by skillRunner when an idle
  // stall coincides with a quota-exhausted rate_limit_event. Matched before the
  // stall-kill heuristics — the kill reason includes "idle" / "stall idle
  // threshold" and would otherwise bucket into stall_kill, retrying in minutes
  // against a bucket that resets in hours.
  if (
    t.includes("[rate-limit-quota-exhausted]") ||
    t.includes("rate-limit-quota-exhausted") ||
    t.includes("rate_limit_quota_exhausted")
  ) {
    return "rate_limit_quota_exhausted";
  }

  // Model rejected by the API (#42). Matched AFTER the explicit quota marker
  // (an explicit signal beats this heuristic) and before the generic blocks.
  // Every pattern is gated on a model reference so unrelated failures that
  // merely mention "limit" or "not found" don't misclassify.
  if (isModelUnavailableText(t)) {
    return "model_unavailable";
  }

  // Issue-closed non-failure (Issue #3661). Matched before the generic
  // heuristics so the "exit" substring in the error text doesn't bucket this
  // into subagent_crash.
  if (
    (t.includes("pipeline-start-failure") && t.includes("issue-closed")) ||
    t.includes("issue_closed")
  ) {
    return "issue_closed";
  }

  // Blocked-dependency deferral (Issue #305). NOT a failure — the scheduler
  // dispatched an issue whose blockedBy dependencies are still open. The
  // underscore form is matched too so Go's NotifyComplete defense-in-depth
  // re-classify lands on the same kind.
  if (t.includes("[blocked-dependency]") || t.includes("blocked_dependency")) {
    return "blocked_dependency";
  }

  // Architecture-approval gate halt (#4098/#4222). NOT a failure — a human must
  // approve a high-impact decision. The sentinel is the human-readable marker
  // text rather than a bracketed token (failureComment.ts and
  // ConcurrentPipelineManager.ts already key on it); the bracketed and
  // underscore forms are matched for the re-classify round trip.
  if (
    t.includes("architecture approval required") ||
    t.includes("[architecture-approval-required]") ||
    t.includes("architecture_approval_required")
  ) {
    return "architecture_approval_required";
  }

  // GitHub API quota too low at the pipeline-start preflight (#3896) —
  // environmental and transient, the bucket resets within the hour.
  if (
    t.includes("github-quota-low") ||
    t.includes("github_quota_low") ||
    (t.includes("pipeline-start-failure") && t.includes("github api quota too low"))
  ) {
    return "github_quota_low";
  }

  // GitHub unreachable at the pipeline-start preflight (#4002) — the
  // connectivity sibling of github_quota_low.
  if (t.includes("github-network-outage") || t.includes("github_network_outage")) {
    return "github_network_outage";
  }

  // pr-merge "completed but PR not merged" (#3691). The stamp carries the
  // blocker classification inside the marker, so the prefix is matched without
  // its closing bracket. The post-merge verification gate is a SECOND route to
  // the same state and phrases it without the stamp — pre-fix that route fell
  // through to the generic failure path and each CI-blocked merge incremented
  // the lifetime failure counter until the cap tripped the whole scheduler.
  // Matched before premature_turn_end, which would otherwise claim the same
  // no-op shape and describe the wrong problem.
  if (
    t.includes("[pr-merge-unmerged") ||
    t.includes("pr_merge_unmerged") ||
    (t.includes("pr-merge reported success") && t.includes("is not merged"))
  ) {
    return "pr_merge_unmerged";
  }

  // Write-containment breach (#129, classified in #230). Matched before the
  // stage-behaviour kinds because a breaching stage usually ALSO exits 0
  // cleanly, and premature_turn_end would otherwise claim it.
  if (t.includes("[stage:worktree-containment]") || t.includes("containment_breach")) {
    return "containment_breach";
  }

  // Dev produced no changes (#202) — a NARROWER premature turn end, so it MUST
  // be matched first: the scheduler wraps every no-op gate reason in a
  // "premature turn end:" string, which means the generic block below would
  // otherwise swallow this kind on every text-classified path while the gate
  // path still reported it. Two classifiers disagreeing about one failure is
  // how a kind stops being trustworthy.
  if (t.includes("[dev-produced-no-changes]") || t.includes("dev_produced_no_changes")) {
    return "dev_produced_no_changes";
  }

  // Dev handoff missing (#223) — the inverse kind, matched here for the same
  // reason. The distinction is what tells a triager whether there is anything
  // on disk worth saving before the next worktree sweep.
  if (t.includes("[dev-handoff-missing]") || t.includes("dev_handoff_missing")) {
    return "dev_handoff_missing";
  }

  // Premature turn end (#74): the stage exited 0 but produced no state change.
  // Two structural producers: the gate hook stamps `premature turn end:` on a
  // no-op gate, and validateStageOutput (#2870) emits `exited 0 but did not
  // write expected output context` (exit-0 paths only, previously bucketed as
  // validation_error). The pr-merge no-op shape already took its richer
  // classification above, so no exclusion is needed here.
  if (
    t.includes("premature turn end") ||
    t.includes("premature_turn_end") ||
    t.includes("exited 0 but did not write expected output context")
  ) {
    return "premature_turn_end";
  }

  // Worktree-uncommitted recovery (Issue #3542) — a failure whose WORK
  // SURVIVED. `stop_hook_uncommitted` is the stop-hook marker carrying the same
  // meaning. Matched early so the generic exit/stall heuristics don't shadow it.
  if (t.includes("worktree_uncommitted") || t.includes("stop_hook_uncommitted")) {
    return "worktree_uncommitted";
  }

  // USD-based pipeline budget ceiling kill (Issue #3542). MUST be matched
  // before the token-based budget heuristic below — "PIPELINE BUDGET CEILING"
  // lowercased contains the substring "budget ceiling" and would otherwise
  // bucket into budget_exceeded. This block previously sat BELOW that one
  // while carrying this very comment (#306).
  if (t.includes("budget_ceiling_hit") || t.includes("pipeline budget ceiling")) {
    return "budget_ceiling_hit";
  }

  // Progress-based runaway kill (Issue #3783). Treated as a transient stall —
  // same recovery as stall_kill (backoff, no lifetime-cap increment) — but kept
  // as its own kind so the monitor's false-positive rate stays measurable.
  // Go carries a second alternative here, the conjunction of `exitSignalSource`
  // and `runaway-progress`; it is deliberately NOT mirrored because Go
  // lowercases its input before matching and that literal carries capitals, so
  // the branch cannot fire for any input. Mirroring dead code would make both
  // sides agree on nothing (#306, corpus row
  // `runaway-progress-exit-signal-source-dead-branch`).
  if (t.includes("[runaway-progress-exceeded]") || t.includes("runaway-progress-exceeded")) {
    return "runaway_progress";
  }

  // Runaway-ceiling kill (Issue #3508) — a safety rail, not the operator's
  // spending decision, so it recovers like a stall. MUST be matched before the
  // cost-cap block below so the two ceilings don't swap recoveries.
  if (
    t.includes("[runaway-ceiling-exceeded]") ||
    t.includes("runaway-ceiling-exceeded") ||
    t.includes("runaway cost ceiling exceeded")
  ) {
    return "stall_kill";
  }

  // Cost-cap kills are budget_exceeded even though the underlying kill path is
  // stall-shaped (an idle SIGTERM on a polling tick), so they must be claimed
  // before the stall heuristics — otherwise a run that deliberately spent its
  // cap is retried as a transient stall and spends it again (#3002/#3207).
  if (
    t.includes("[cost-cap-exceeded]") ||
    t.includes("cost-cap-exceeded") ||
    t.includes("cost cap exceeded")
  ) {
    return "budget_exceeded";
  }

  // Zombie-run guards (#252). `[stale-slot-orphan]` is written when a reload
  // sweeps a run whose process died without its close handler;
  // `[stage-no-output-timeout]` is the first-output watchdog killing a stage
  // that never produced any session output. Both are transient-stall shaped:
  // retry with backoff is the right recovery and neither should count against
  // the lifetime failure cap.
  if (t.includes("stale-slot-orphan") || t.includes("stage-no-output-timeout")) {
    return "stall_kill";
  }

  // Stall-kill heuristics — aligned with classifyFailureCategory's "agent"
  // bucket. `[stall-killed]` / `exceeded stall idle threshold` /
  // `exceeded stage_hard_cap` are the canonical markers the stage runner emits
  // (#3207); the remaining substrings cover the auto-mode wordings. All three
  // real phrasings differ by a word, so a matcher written for one of them
  // leaves the others unclassified — which is exactly what happened here
  // before #306, on the most common failure the machine produces.
  if (
    t.includes("[stall-killed]") ||
    t.includes("stall-killed") ||
    t.includes("stall kill threshold") ||
    t.includes("stalled and killed") ||
    t.includes("heartbeat stall") ||
    t.includes("exceeded stall idle threshold") ||
    t.includes("exceeded stage_hard_cap") ||
    t.includes("hard cap")
  ) {
    return "stall_kill";
  }

  // Budget-enforcer reasons (see internal/orchestrator/budget_enforcer.go
  // BudgetDecision.Reason).
  if (
    t.includes("pipeline_budget_exceeded") ||
    t.includes("stage_budget_exceeded") ||
    t.includes("budget exceeded") ||
    t.includes("budget ceiling")
  ) {
    return "budget_exceeded";
  }

  // Schema / output-validation failures. (The "did not write expected output
  // context" phrase moved to premature_turn_end above — its only producer,
  // validateStageOutput, runs exclusively on exit-0 paths.)
  if (
    t.includes("schema validation") ||
    t.includes("invalid json") ||
    t.includes("not valid json") ||
    t.includes("unparseable json") ||
    t.includes("missing prerequisite")
  ) {
    return "validation_error";
  }

  // Adapter auth pre-flight failure (#312) — a probe timed out after a retry
  // (transient starvation under a concurrent dispatch burst) or the adapter CLI
  // is logged out. Matched before the crash fallback, whose "exit " substring
  // would otherwise feed the cascade breaker a false crash. All three
  // spellings: the wrapped pipeline-start form carries the marker BARE, which
  // this side previously missed on a real, observed failure (#306).
  if (
    t.includes("[adapter-auth-failed]") ||
    t.includes("adapter-auth-failed") ||
    t.includes("adapter_auth_failed")
  ) {
    return "adapter_auth_failed";
  }

  // No changes produced (#317) — pr-create's deterministic fallback confirmed
  // zero commits ahead of base: genuinely nothing to open a PR for. Deliberately
  // NOT matched on bare "no commits ahead of" — that phrase also appears in
  // feature-validate's unrelated lost-implementation check, which must keep its
  // organic classification.
  if (t.includes("[no-changes-produced]") || t.includes("no_changes_produced")) {
    return "no_changes_produced";
  }

  // Feature-validate honest quality-gate failure (#326). The skill exited 0 but
  // wrote validation_status="failed" and left the code uncommitted for retry — a
  // real organic implementation failure caught by the pipeline's own gate, not a
  // process death.
  if (t.includes("[validation-failed]") || t.includes("validation_failed")) {
    return "validation_failed";
  }

  // Feature-validate zero-test run (#221) — the gate learned nothing, usually
  // from an environmental misconfiguration. Kept adjacent to validation_failed:
  // the two validation-status outcomes make opposite claims about the code.
  if (t.includes("[validation-inconclusive]") || t.includes("validation_inconclusive")) {
    return "validation_inconclusive";
  }

  // Branch forked from its remote (#163). Two entry points, one kind: the Go
  // scheduler's pre-stage fork pre-flight stamps `[branch-forked]` before the
  // stage spends a token, and a fork that first surfaces at push time is
  // recognised by the skills' `PUSH REJECTED: non-fast-forward` sentence.
  // Matched before the crash fallback, whose "exit " substring would otherwise
  // read a push rejection as a process death — which is how every retry looked
  // like a fresh crash instead of the same unrecoverable fork. "non-fast-forward"
  // is enough on its own (git emits it for exactly this condition); "rejected"
  // is not, so it must co-occur with a push.
  if (
    t.includes("[branch-forked]") ||
    t.includes("branch_forked") ||
    t.includes("non-fast-forward") ||
    (t.includes("push rejected") && t.includes("fetch first"))
  ) {
    return "branch_forked";
  }

  // Commit stranded on the wrong branch after a SIGKILL bypassed the pre-push
  // restore-defer (#266). Emitted by feature-validate's branch-identity guard
  // when HEAD isn't on the issue's expected feature branch and self-heal can't
  // recover it. Unrecoverable by retry.
  if (t.includes("[commit-orphaned]") || t.includes("commit_orphaned")) {
    return "commit_orphaned";
  }

  // Harness tool-call denial (#289) — most commonly a stage reaching for a
  // forbidden foreground `sleep` wait loop, surfaced as
  // `tool_use_result: "User rejected tool use"`. A denial is the harness saying
  // "not that way", not a defect. Matched before the crash fallback, whose
  // "exit " substring turned one rejected tool call into a permanently killed
  // run and a fleet-wide pause.
  //
  // Deliberately NOT matched on the bare phrase "permission denied": a
  // filesystem `EACCES: permission denied` is an infrastructure fault with the
  // opposite recovery, and matching it here would create a divergence while
  // claiming to close one (#306, corpus row `permission-denied-negative-eacces`).
  if (
    t.includes("[permission-denied]") ||
    t.includes("permission_denied") ||
    t.includes("user rejected tool use") ||
    (t.includes("tool_use_result") && t.includes("rejected"))
  ) {
    return "permission_denied";
  }

  // Subagent process death / non-zero exit fallback. Everything specific is
  // matched above because scheduler.SetStageError prefixes almost every stage
  // error with `exit N: `, so this block can claim any string that reaches it —
  // a rule appended after it is dead code.
  if (t.includes("subagent crash") || t.includes("exit ") || t.includes("killed by signal")) {
    return "subagent_crash";
  }

  return undefined;
}

/**
 * Prefers a gate-sourced structured terminal kind over prose classification
 * of the synthesized error text (Issue #9). Mirrors Go's
 * `ResolveTerminalKind` in `internal/orchestrator/failure_handler.go`. Falls
 * back to `classifyTerminalKind` for non-gate failures and for gate failures
 * that didn't set a structured kind (including all historical records
 * persisted before `terminal_kind` existed on `StageGateResult`).
 */
export function resolveTerminalKind(
  gateRan: boolean,
  gateTerminalKind: string | undefined,
  errorText: string | undefined
): TerminalFailureKind | undefined {
  if (gateRan && gateTerminalKind) {
    return gateTerminalKind as TerminalFailureKind;
  }
  return classifyTerminalKind(errorText);
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
