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
import {
  TERMINAL_KIND_PREDICATE_REF,
  TERMINAL_KIND_TABLE,
  type TerminalKindRule,
} from "./terminalKindTable.generated.js";

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
 * TERMINAL-KIND CLASSIFICATION — the interpreter side (#306).
 *
 * There is no ladder here to keep aligned with Go, because there is no ladder
 * anywhere: the ordered rule table lives in `internal/terminalkind/table.json`,
 * Go embeds it, and `terminalKindTable.generated.ts` is its generated
 * TypeScript view. Everything below reads that table.
 *
 * Before this, the SDK carried a hand-written mirror whose docstring said it
 * reproduced Go "block for block". It did not: it had drifted on 14 of 113
 * inputs, including the four most common real failures on a live machine, and
 * had ordered `budget_ceiling_hit` below `budget_exceeded` while carrying a
 * comment saying it must come first. A mirror is only as good as the last
 * person who remembered it existed.
 */

/**
 * MATCHER_HAS_NO_LITERALS. The two functions below are the whole of terminal-kind
 * matching in TypeScript, and neither contains a single string literal — every
 * marker they compare against comes out of the generated table. That is asserted
 * by failureClassifier.corpusParity.test.ts, and it is the last hole the corpus
 * and the derived stress set cannot see on their own: both are built FROM the
 * table's vocabulary, so a rule invented here for a marker the table has never
 * heard of would be invisible to them. Twelve lines with no literals in them is
 * a claim a reader can check at a glance; a 33-block ladder was not.
 */
const UNKNOWN_PREDICATE =
  "terminal-kind table references a predicate with no TypeScript implementation in failureClassifier.ts: ";

/** A rule matched against an error text, or `undefined` when nothing matched. */
function matchTerminalKindRule(errorText: string | undefined): TerminalKindRule | undefined {
  if (!errorText) return undefined;
  const t = errorText.toLowerCase();
  for (const rule of TERMINAL_KIND_TABLE.rules) {
    for (const clause of rule.clauses) {
      if (clauseSatisfied(t, clause)) return rule;
    }
  }
  return undefined;
}

function clauseSatisfied(lowered: string, clause: string[]): boolean {
  for (const term of clause) {
    if (term.startsWith(TERMINAL_KIND_PREDICATE_REF)) {
      const name = term.slice(TERMINAL_KIND_PREDICATE_REF.length);
      const predicate = TERMINAL_KIND_PREDICATES[name];
      // Fail loudly on an unknown predicate. Silently evaluating to false would
      // disable a rule with no visible symptom — exactly the class of silent
      // divergence the table exists to remove. The message is assembled from
      // constants so this whole matcher stays free of string literals, which is
      // itself asserted (see MATCHER_HAS_NO_LITERALS above).
      if (!predicate) throw new Error(UNKNOWN_PREDICATE + name);
      if (!predicate(lowered)) return false;
      continue;
    }
    if (!lowered.includes(term)) return false;
  }
  return true;
}

/**
 * Named predicates the table may reference as `@name` terms.
 *
 * One entry, deliberately: a predicate exists only for a condition that cannot
 * be written as literal containment. Everything else is a literal in
 * table.json, where review and the generated module can both see it. Each
 * predicate declares probes_true / probes_false in the table, and BOTH
 * languages assert them — that is what keeps the two implementations from
 * answering differently inside a rule that otherwise cannot drift.
 */
const TERMINAL_KIND_PREDICATES: Record<string, (lowered: string) => boolean> = {
  mentions_registry_model: mentionsRegistryModel,
};

/**
 * Classify the *kind* of terminal failure from an error message.
 *
 * Interprets the canonical table, so this returns exactly what Go's
 * `ClassifyTerminalKind` returns for the same input — not by mirroring it, but
 * by reading the same rules in the same order. `terminal_kind` in the run
 * record is Go's answer; this is how an SDK consumer reproduces it.
 *
 * Returns `undefined` when no rule matches; callers can fall back to
 * `"subagent_crash"` (the most generic kind) or leave the field absent.
 *
 * @param errorText - Error message or stack trace from the failed stage
 * @returns The terminal failure kind, or undefined when unclassifiable
 */
export function classifyTerminalKind(
  errorText: string | undefined
): TerminalFailureKind | undefined {
  return matchTerminalKindRule(errorText)?.kind as TerminalFailureKind | undefined;
}

/**
 * The kind a consumer may forward to the Go scheduler as a signal, or
 * `undefined` to defer to Go's own classification.
 *
 * Runs the FULL ladder and answers only when the WINNING rule is declared
 * `signal: true` in the table. That is what makes the signal side incapable of
 * contradicting the record: its answer is either nothing or exactly
 * `classifyTerminalKind`'s answer. Skipping non-signal rules instead would
 * reintroduce disagreement, because a lower-precedence signal rule could then
 * claim text that a higher-precedence non-signal rule owns.
 *
 * The VSCode extension consumes this through
 * `services/terminalKindSignal.ts`; Go's NotifyComplete uses a non-empty
 * answer VERBATIM, which is why the bound has to be structural.
 */
export function signalTerminalKind(errorText: string | undefined): TerminalFailureKind | undefined {
  const rule = matchTerminalKindRule(errorText);
  return rule?.signal ? (rule.kind as TerminalFailureKind) : undefined;
}

/**
 * Prefers a gate-sourced structured terminal kind over prose classification
 * of the synthesized error text (Issue #9). Mirrors Go's
 * `ResolveTerminalKind` in `internal/orchestrator/failure_handler.go` — a
 * two-line precedence rule with no matching in it, which is why it is written
 * out rather than tabulated. Falls back to `classifyTerminalKind` for non-gate
 * failures and for gate failures that didn't set a structured kind (including
 * all historical records persisted before `terminal_kind` existed on
 * `StageGateResult`).
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
 * Registry-derived "names a specific model" gate — IDs, display names, tiers.
 * The Go twin is `mentionsRegistryModel` in internal/terminalkind/predicates.go
 * and iterates `models.All()`; the two registries are the same file
 * (packages/nightgauge-sdk/src/eval/model-registry.json is canonical,
 * internal/models/model-registry.json is a byte copy with a Go parity test).
 */
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

/**
 * Derive a deterministic input set FROM the table — the verbatim TypeScript
 * twin of `StressInputs` in internal/terminalkind/stress.go.
 *
 * Both languages derive the same list and compare their answers against one
 * committed golden (internal/terminalkind/testdata/stress-golden.json), which is
 * how the two interpreters are proved equivalent without a live bridge between
 * them: if the derivations differed the input lists would not match, and if the
 * interpreters differed the answers would not.
 *
 * THE ALGORITHM IS PART OF THE CONTRACT. Changing it here means changing
 * stress.go and regenerating the golden. Order is significant and stable, and
 * duplicates keep their FIRST occurrence.
 */
export function terminalKindStressInputs(): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const add = (s: string): void => {
    if (!seen.has(s)) {
      seen.add(s);
      out.push(s);
    }
  };

  add("");
  add("nothing in this sentence resembles a terminal marker");

  for (const rule of TERMINAL_KIND_TABLE.rules) {
    for (const clause of rule.clauses) {
      const s = sampleClause(clause);
      add(s);
      add("exit 1: " + s);
      add(s.toUpperCase());
      for (const term of clause) add(sampleClause([term]));
    }
  }

  for (const a of TERMINAL_KIND_TABLE.rules) {
    for (const b of TERMINAL_KIND_TABLE.rules) {
      if (a.id === b.id) continue;
      add(sampleClause(a.clauses[0]) + " | " + sampleClause(b.clauses[0]));
    }
  }

  return out;
}

function sampleClause(clause: string[]): string {
  return clause
    .map((term) =>
      term.startsWith(TERMINAL_KIND_PREDICATE_REF)
        ? probeTrue(term.slice(TERMINAL_KIND_PREDICATE_REF.length))
        : term
    )
    .join(" ");
}

function probeTrue(name: string): string {
  const p = TERMINAL_KIND_TABLE.predicates.find((x) => x.name === name);
  if (!p || p.probes_true.length === 0) {
    throw new Error(`terminal-kind predicate "${name}" declares no probes_true`);
  }
  return p.probes_true[0];
}
