package orchestrator

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/failure"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/state"
)

// OutcomeTypeBlocked is the run outcome_type for a run that shipped a reviewable
// PR but could not merge because of a required-check / branch-ruleset config
// that no pipeline retry can clear — a human must change repo config. It mirrors
// the TS blockedTerminalState → "blocked" classification (#190) so both
// orchestration paths agree, and it is a first-class, needs-human outcome rather
// than a generic "failure" on the dashboard.
const OutcomeTypeBlocked = "blocked"

// OutcomeTypeDeferred is the run outcome_type for a dispatched issue whose
// blockedBy dependencies were still open when the pipeline picked it up. A
// deferral is NOT a failure — the issue is simply not ready yet — so it books
// as a non-failure "cancelled" run carrying this outcome_type rather than a
// generic failure, keeping the dashboard honest and leaving the issue eligible
// for a later tick once its blocker closes. Issue #305.
const OutcomeTypeDeferred = "deferred"

// Terminal failure kinds — what aborted the pipeline run (Issue #3001).
//
// Independent of state.GateResult / failure_category which classify failures by
// responsibility for weighted reliability scoring. Terminal kind = "what
// stopped the run", failure_category = "who is to blame for reliability".
//
// Mirrors TS TerminalFailureKindSchema in
// packages/nightgauge-vscode/src/schemas/executionHistory.ts.
const (
	TerminalKindStallKill          = "stall_kill"
	TerminalKindBudgetExceeded     = "budget_exceeded"
	TerminalKindValidationError    = "validation_error"
	TerminalKindSubagentCrash      = "subagent_crash"
	TerminalKindOrchestratorCrash  = "orchestrator_crash"
	TerminalKindNetworkUnavailable = "network_unavailable" // Issue #3296
	// TerminalKindStreamIdleTimeout is set when the Anthropic API closes a
	// streaming response mid-flight while the agent is actively producing
	// tokens (Issue #3398). The CLI surfaces this as
	// `API Error: Stream idle timeout - partial response received`. Distinct
	// from stall_kill (where the agent is silent and the harness's idle
	// watchdog fires) and from network_unavailable (where connectivity is
	// lost). The cause is environmental — typically observed when the user's
	// 5-hour rate-limit bucket is depleted with overage rejected
	// (`out_of_credits`). Treat as a transient infra failure: do NOT count
	// toward the lifetime failure cap and use a backoff long enough to clear
	// the rate-limit window.
	TerminalKindStreamIdleTimeout = "stream_idle_timeout"
	// TerminalKindRateLimitQuotaExhausted is the silent sibling of
	// stream_idle_timeout (Issue #3386). Pre-fix, an idle stall fired
	// whenever the agent's next API turn blocked waiting for the Anthropic
	// rate-limit bucket to reset (overage rejected, `out_of_credits`).
	// Visually identical to a true stall_kill, but the agent isn't wedged —
	// the API is. Detected by the TS skillRunner when its idle watchdog
	// fires AND the last observed rate_limit_event indicated quota
	// exhaustion; surfaced in the kill marker as
	// `[rate-limit-quota-exhausted]`. Routes through the same retry policy
	// as stream_idle_timeout: 1-hour backoff, no lifetime-cap increment,
	// preserved worktree.
	TerminalKindRateLimitQuotaExhausted = "rate_limit_quota_exhausted"
	// TerminalKindWorktreeUncommitted is set when a stage fails or is killed
	// but the worktree contained uncommitted work that was successfully
	// auto-recovered into a recovery commit. This is a recoverable condition
	// — the work was preserved; the pipeline can re-run from the next stage.
	// Does NOT increment LifetimeIssueFailures and does NOT revert board
	// status. Issue #3542.
	TerminalKindWorktreeUncommitted = "worktree_uncommitted"
	// TerminalKindBudgetCeiling is set when the USD-based pipeline budget
	// ceiling fires and kills a running stage. Distinct from
	// TerminalKindBudgetExceeded (the token-based safety-rails ceiling). Does
	// NOT increment LifetimeIssueFailures — the cost was real spend, not a
	// code defect — and does NOT revert board status. Issue #3542.
	TerminalKindBudgetCeiling = "budget_ceiling_hit"
	// TerminalKindIssueClosed is set when issue-pickup detects the issue is
	// already CLOSED before any AI stages run. This is a recoverable,
	// non-failure state — the issue was likely closed by the pipeline itself
	// (verify-and-close) and the autonomous re-admit path picked it up due to
	// a GitHub read-after-write race. Does NOT increment LifetimeIssueFailures.
	// Board status moves to Done (not Ready) since the issue is genuinely
	// closed. Does NOT pause autonomous. Issue #3661.
	TerminalKindIssueClosed = "issue_closed"
	// TerminalKindPrMergeUnmerged is set when the pr-merge stage's Claude
	// session exited cleanly (exit_code=0, stage marker says "ok") but the
	// PR was not actually merged — the agent gave up on RALPH or hit a real
	// blocker without surfacing it explicitly. The TS-side post-pipeline
	// diagnostic in HeadlessOrchestrator.diagnosePrMergeBlocker classifies
	// the actual blocker (ci_failures / merge_conflict / review_required /
	// pr_closed_without_merge / agent_gave_up) and stamps the error text
	// with `[pr-merge-unmerged:<blocker>]`. Recoverable: does NOT increment
	// LifetimeIssueFailures (the work product is partially shipped — a PR
	// exists), but DOES pause autonomous with a detailed reason listing the
	// PR URL + failing checks so the operator can act without log
	// archaeology. Issue #3691.
	TerminalKindPrMergeUnmerged = "pr_merge_unmerged"
	// TerminalKindRunawayProgress is set when the progress-based runaway
	// monitor fires: the stage made no new forward-progress signal for the
	// configured window. Same recovery path as stall_kill (30m backoff, no
	// lifetime-cap increment, board→Ready). Issue #3783.
	TerminalKindRunawayProgress = "runaway_progress"
	// TerminalKindApiOverloaded is set when the Anthropic API returns a 529
	// "Overloaded" response — the service was momentarily at capacity. Surfaced
	// by the Claude CLI as a result envelope with `is_error:true` and the
	// message `API Error: Overloaded`. This is a TRANSIENT infrastructure
	// failure: nothing is wrong in our code or the issue, and it clears on its
	// own within minutes. Treated like stream_idle_timeout — short backoff, no
	// lifetime-cap increment, board→Ready, and NO queue pause — so a brief
	// capacity blip can't halt the whole autonomous queue and page the operator.
	// Unlike rate-limit-quota-exhausted it does NOT apply a global cooldown
	// (overload ≠ depleted bucket). Issue #3835 (WS4).
	TerminalKindApiOverloaded = "api_overloaded"
	// TerminalKindGitHubQuotaLow is set when the pipeline-start preflight
	// refuses to launch because the GitHub REST/GraphQL API rate-limit bucket
	// is below the headroom needed for one run. This is the GitHub-API sibling
	// of TerminalKindRateLimitQuotaExhausted (which is the Anthropic bucket):
	// environmental, not a code or issue defect, and the bucket resets within
	// the hour. Emitted by HeadlessOrchestrator.preCheckAuth as
	// `[pipeline-start-failure] github-quota-low (... resetInSec=N)`. Routed
	// like the Anthropic quota path: per-issue backoff, a GLOBAL cooldown until
	// the bucket resets so other Ready items don't keep dispatching into the
	// same exhausted quota, no lifetime-cap increment, board → Ready. Issue #3896.
	TerminalKindGitHubQuotaLow = "github_quota_low"
	// TerminalKindApiConnectionLost is set when the Anthropic API transport
	// drops mid-stage — the Claude CLI surfaces it as a result envelope with
	// `is_error:true` and a message like `API Error: The socket connection
	// was closed unexpectedly` (also "socket hang up" and DNS/conn-reset
	// variants during a local network blip). TRANSIENT infrastructure
	// failure, the transport sibling of TerminalKindApiOverloaded: same
	// recovery — short backoff, no lifetime-cap increment, board → Ready,
	// NO queue pause. A seconds-long Wi-Fi/DNS blip must not halt the
	// autonomous queue and page the operator. Issue #4002.
	TerminalKindApiConnectionLost = "api_connection_lost"
	// TerminalKindGitHubNetworkOutage is set when the pipeline-start
	// preflight cannot reach api.github.com at all (`gh auth status` exits
	// non-zero with a connectivity error — DNS down, no route). The
	// connectivity sibling of TerminalKindGitHubQuotaLow: environmental and
	// transient. Pre-fix this was misreported as github-auth-failed ("Run
	// `gh auth login`") even though auth was fine. Routed with a SHORT
	// GLOBAL dispatch cooldown (an outage affects every repo equally; there
	// is no published reset time), per-issue backoff, no lifetime-cap
	// increment, board → Ready, no pause. Emitted by
	// HeadlessOrchestrator.preCheckAuth as
	// `[pipeline-start-failure] github-network-outage`. Issue #4002.
	TerminalKindGitHubNetworkOutage = "github_network_outage"
	// TerminalKindModelUnavailable is set when the Anthropic API REJECTS the
	// selected model rather than failing transiently (#42): unknown/invalid
	// model ID (HTTP 404 `not_found_error` with message `model: <id>`), a
	// model not offered on the current plan, or a model-specific usage cap
	// (e.g. Fable on Claude Code Max plans, Opus weekly caps). Distinct from
	// api_overloaded / rate_limit_quota_exhausted — those are transient and
	// retried on the SAME model with backoff; this kind means retrying the
	// same model cannot succeed, so it triggers the tier-downgrade fallback
	// instead (RetryEngine.EvaluateDowngrade: fable → opus → sonnet → haiku,
	// sticky for the remainder of the run).
	TerminalKindModelUnavailable = "model_unavailable"
	// TerminalKindPrematureTurnEnd is set when a stage's session exits
	// cleanly (exit_code=0) but its post-condition gate reports
	// gates.KindNoOp — the skill ended its turn without producing the
	// stage's expected output (missing/empty context file, no branch, no
	// state change). This is the "ending a turn on a promise" failure mode
	// (#74): the model narrates intent or asks an implicit question instead
	// of doing the work, then stops. Detected structurally via the
	// stage-gate framework, never by text heuristics on the final message.
	// Exception: pr-merge's no-op keeps the richer
	// TerminalKindPrMergeUnmerged classification (#3691) — its matcher runs
	// first in ClassifyTerminalKind. Agent-class for weighted reliability
	// scoring (the behavior is the agent's, not the environment's).
	TerminalKindPrematureTurnEnd = "premature_turn_end"
	// TerminalKindBlockedDependency is set when the autonomous scheduler
	// dispatches an issue whose blockedBy dependencies are still OPEN — the
	// pipeline defers the run before any AI stages do work. This is a
	// NON-FAILURE deferral, not a crash: the issue simply isn't ready yet.
	// Does NOT increment LifetimeIssueFailures, does NOT feed the cascade
	// circuit breaker, and does NOT pause autonomous. The issue stays ELIGIBLE
	// (board → Ready, modest per-issue backoff to avoid hot-looping) so the
	// blocker-close requeue re-dispatches it once the blocker closes. Booked as
	// a "cancelled" run with outcome_type "deferred" (see OutcomeTypeDeferred).
	// The TS layer stamps the failure text with the `[blocked-dependency]`
	// marker. Issue #305.
	TerminalKindBlockedDependency = "blocked_dependency"
	// TerminalKindAdapterAuthFailed is set when the pipeline-start adapter auth
	// gate refuses to launch: an adapter's `claude auth status` probe either
	// timed out after a retry (transient starvation — several cold probes fired
	// concurrently after an autonomous restart and lost the CPU race, though
	// auth was fine) or came back definitively logged out. Environmental /
	// credential state, NOT a subagent process death — pre-fix this bucketed
	// into subagent_crash, so three burst false-negatives paused the queue and
	// fed the cascade breaker as real crashes. Routed like the other transient
	// infra kinds: short per-issue backoff, board → Ready, NO LifetimeIssueFailures
	// increment, NO cascade feed, NO pause. Emitted by
	// HeadlessOrchestrator's adapter-auth gate with the `[adapter-auth-failed]`
	// marker embedded in the failure text; the timeout vs logged-out distinction
	// is carried in the human-readable reason. Issue #312.
	TerminalKindAdapterAuthFailed = "adapter_auth_failed"
	// TerminalKindNoChangesProduced is set when pr-create's post-condition gate
	// and the deterministic create fallback both agree there is genuinely
	// nothing to open a PR for: the feature branch has zero commits ahead of
	// base and the working tree has no source changes. Pre-fix this bucketed
	// into subagent_crash — a dispatch-eligibility gap (#317) let a human-only
	// issue (labeled `owner-action`: work only an operator can do, e.g.
	// rotating a cloud credential) run the full pipeline through
	// feature-validate, which CORRECTLY produced zero commits, and then failed
	// at pr-create with "nothing to commit". That is not a process crash — the
	// model did exactly the right thing (write no code for a task with no code
	// to write); the defect was dispatching it at all. Emitted by
	// HeadlessOrchestrator's deterministic create-fallback with the
	// `[no-changes-produced]` marker embedded in the failure text (mirrors the
	// `[adapter-auth-failed]` / `[blocked-dependency]` marker pattern — no
	// other stable substring in the gate/fallback text is specific enough:
	// "pr context file missing" also covers a genuine crash-before-write, and
	// "no commits ahead of" also appears in feature-validate's DIFFERENT
	// lost-implementation check, which must stay a real defect). Classified
	// `agent` (0.5 weight, not `infrastructure`'s 0.05): this is a planning/
	// scope failure — the dispatcher's responsibility, analogous to
	// TerminalKindPrematureTurnEnd's "agent ended its turn on a promise"
	// bucket — not a pipeline tooling/runtime defect. Issue #317.
	TerminalKindNoChangesProduced = "no_changes_produced"
	// TerminalKindValidationFailed is set when feature-validate honestly fails
	// its quality gates: the skill exits 0 but writes
	// `validation_status: "failed"` (+ an errorCategory) and deliberately
	// leaves the code uncommitted on disk for retry rather than exiting
	// non-zero, delegating the halt decision to the orchestrator. Pre-fix this
	// had no matcher and fell through to the generic subagent_crash fallback
	// (bowlsheet-infra#164) — hiding an honest, organic quality-gate catch
	// behind a label that implies a process crash. This is exactly the
	// scenario docs/FAILURE_TAXONOMY.md already declared "Feature-validate
	// test failure → organic, weight 1.0" for; the taxonomy was right, the
	// classifier just never implemented it. Emitted by
	// HeadlessOrchestrator.verifyPostValidateState with the
	// `[validation-failed]` marker embedded in the failure text (mirrors the
	// `[adapter-auth-failed]` / `[no-changes-produced]` marker pattern).
	// Classified `organic` (1.0 weight, the classifyFailureCategory default —
	// no dedicated category block needed, same as subagent_crash): this is a
	// true implementation failure caught by the pipeline's own quality gate,
	// not a tooling/runtime defect and not a planning/scope failure. Issue
	// #326.
	TerminalKindValidationFailed = "validation_failed"
)

// ClassifyTerminalKind returns the terminal failure kind for the given error
// text. Pure heuristic — same matching style as classifyFailureCategory in the
// SDK so the two stay aligned. Returns "" when no pattern matches; callers
// should fall back to TerminalKindSubagentCrash (the most generic).
//
// @see Issue #3001
func ClassifyTerminalKind(errorText string) string {
	if errorText == "" {
		return ""
	}
	t := strings.ToLower(errorText)

	// Network-unavailable abort (Issue #3296). Set when the TS-side stall
	// watchdog observes ≥ N consecutive connectivity failures and the Go
	// scheduler cancels the active stage with cause ErrNetworkUnavailable.
	// Classified before all other heuristics because the cancellation message
	// surfaces from context.Cause and shouldn't accidentally match a generic
	// "exit" or "stall" pattern below.
	if strings.Contains(t, "network unavailable: extended github connectivity loss") {
		return TerminalKindNetworkUnavailable
	}

	// Stream idle timeout from Anthropic API (Issue #3398). Surfaced by the
	// Claude CLI as a result envelope with `is_error:true` and the message
	// "API Error: Stream idle timeout - partial response received". Match BEFORE
	// the generic stall-kill / network heuristics — the literal "timeout"
	// substring appears in the text and would otherwise bucket into infra.
	if strings.Contains(t, "stream idle timeout") {
		return TerminalKindStreamIdleTimeout
	}

	// API "Overloaded" (Anthropic 529) — a transient capacity blip (#3835 WS4).
	// The Claude CLI surfaces it as a result envelope with `is_error:true` and
	// the message `API Error: Overloaded`. Match BEFORE the generic stall-kill /
	// subagent-crash heuristics so a momentary overload routes to the transient
	// recovery path (short backoff, no pause) instead of being misread as a
	// code crash. "overloaded" is distinctive to this API error in failure text.
	if strings.Contains(t, "overloaded") {
		return TerminalKindApiOverloaded
	}

	// Anthropic API transport drop (#4002). The Claude CLI surfaces a dropped
	// connection as a result envelope whose message carries the raw transport
	// error — canonically `API Error: The socket connection was closed
	// unexpectedly` (observed when a local network/DNS blip kills the stream).
	// Matched BEFORE the generic stall-kill / subagent-crash heuristics so a
	// seconds-long blip routes to the transient recovery path (short backoff,
	// no pause) instead of being misread as a code crash. The bare error-code
	// variants are additionally gated on "api error" so an unrelated stage
	// error that merely mentions ECONNRESET (e.g. a failing integration test)
	// doesn't misclassify.
	if strings.Contains(t, "socket connection was closed") ||
		strings.Contains(t, "socket hang up") ||
		strings.Contains(t, "api_connection_lost") ||
		(strings.Contains(t, "api error") &&
			(strings.Contains(t, "econnreset") ||
				strings.Contains(t, "econnrefused") ||
				strings.Contains(t, "enotfound") ||
				strings.Contains(t, "eai_again") ||
				strings.Contains(t, "getaddrinfo") ||
				strings.Contains(t, "fetch failed") ||
				strings.Contains(t, "connection reset") ||
				strings.Contains(t, "connection refused"))) {
		return TerminalKindApiConnectionLost
	}

	// Rate-limit quota exhausted (Issue #3386). Set by skillRunner when an
	// idle stall fires AND the last rate_limit_event indicated quota
	// exhaustion (status="limited" OR overage rejected for out_of_credits).
	// Marker text: `[rate-limit-quota-exhausted]`. Match BEFORE the generic
	// stall-kill heuristics — the kill reason includes "idle" / "stall idle
	// threshold" substrings and would otherwise bucket into stall_kill.
	if strings.Contains(t, "[rate-limit-quota-exhausted]") ||
		strings.Contains(t, "rate-limit-quota-exhausted") ||
		strings.Contains(t, "rate_limit_quota_exhausted") {
		return TerminalKindRateLimitQuotaExhausted
	}

	// Model rejected by the API (#42). Matched AFTER the explicit TS-stamped
	// quota marker (an explicit signal beats this heuristic) and BEFORE the
	// generic heuristics. Every pattern is gated on a model reference so
	// unrelated failures that merely mention "limit" or "not found" don't
	// misclassify — see isModelUnavailableText for the documented Anthropic
	// error shapes covered.
	if isModelUnavailableText(t) {
		return TerminalKindModelUnavailable
	}

	// Issue-closed non-failure (Issue #3661). Matched BEFORE the generic
	// heuristics so the "exit" substring in the error text doesn't bucket
	// this into subagent_crash. The pipeline emits this as:
	// `[pipeline-start-failure] issue-closed` when issue-pickup detects the
	// issue is already CLOSED before any AI stages run.
	if (strings.Contains(t, "pipeline-start-failure") && strings.Contains(t, "issue-closed")) ||
		strings.Contains(t, "issue_closed") {
		return TerminalKindIssueClosed
	}

	// Blocked-dependency deferral (Issue #305). The autonomous scheduler
	// dispatched an issue whose blockedBy dependencies are still open; the
	// pipeline defers before any AI stages run. NOT a failure. Matched here,
	// before the generic heuristics, so neither the "pipeline-start-failure"
	// wrapper nor the "exit" substring buckets it into subagent_crash. The TS
	// layer stamps the failure text with the `[blocked-dependency]` marker; the
	// underscore form is also matched so the NotifyComplete defense-in-depth
	// reclassify (autonomous.go NotifyComplete) lands on the same kind.
	if strings.Contains(t, "[blocked-dependency]") ||
		strings.Contains(t, "blocked_dependency") {
		return TerminalKindBlockedDependency
	}

	// GitHub API quota too low at the pipeline-start preflight (Issue #3896).
	// Environmental and transient — the REST/GraphQL bucket resets within the
	// hour. Matched here, before the generic heuristics, so neither the
	// "pipeline-start-failure" wrapper nor the "quota"/"limit" words bucket it
	// into an unrelated kind. Emitted by HeadlessOrchestrator.preCheckAuth as
	// `[pipeline-start-failure] github-quota-low`; the legacy descriptive text
	// ("GitHub API quota too low") is also matched for forward/backward compat.
	if strings.Contains(t, "github-quota-low") ||
		strings.Contains(t, "github_quota_low") ||
		(strings.Contains(t, "pipeline-start-failure") && strings.Contains(t, "github api quota too low")) {
		return TerminalKindGitHubQuotaLow
	}

	// GitHub unreachable at the pipeline-start preflight (#4002) — the
	// connectivity sibling of github-quota-low above. Emitted by
	// HeadlessOrchestrator.preCheckAuth as
	// `[pipeline-start-failure] github-network-outage` (with the
	// `[github-network-outage]` token embedded in error.message for the
	// failureDetail fallback path). Matched before the generic heuristics so
	// the "pipeline-start-failure" wrapper doesn't bucket it elsewhere.
	if strings.Contains(t, "github-network-outage") ||
		strings.Contains(t, "github_network_outage") {
		return TerminalKindGitHubNetworkOutage
	}

	// pr-merge "completed but PR not merged" diagnostic (Issue #3691).
	// HeadlessOrchestrator.diagnosePrMergeBlocker classifies the blocker
	// (ci_failures / merge_conflict / review_required / pr_closed_without_merge
	// / agent_gave_up) and stamps the error text with
	// `[pr-merge-unmerged:<blocker>]`. Matched BEFORE the generic budget /
	// stall-kill heuristics so the rich diagnostic isn't accidentally
	// bucketed into an unrelated kind.
	//
	// The post-merge verification gate is a SECOND route to the same state
	// and phrases it "pr-merge reported success but PR #N is not merged
	// (state: OPEN). blocked by …" WITHOUT the stamp. Pre-fix, that route
	// fell through to the generic failure path — each CI-blocked merge
	// incremented LifetimeIssueFailures and re-dispatched into the same
	// red check until the lifetime cap tripped the whole scheduler
	// (bowlsheet #233, 2026-07-11).
	if strings.Contains(t, "[pr-merge-unmerged") ||
		strings.Contains(t, "pr_merge_unmerged") ||
		(strings.Contains(t, "pr-merge reported success") && strings.Contains(t, "is not merged")) {
		return TerminalKindPrMergeUnmerged
	}

	// Premature turn end (#74): the stage exited 0 but produced no state
	// change — the agent ended its turn on a promise instead of doing the
	// work. Two structural producers: the gate hook stamps `premature turn
	// end:` when a post-condition gate reports gates.KindNoOp, and
	// validateStageOutput (#2870) emits `exited 0 but did not write expected
	// output context` when the context file is missing entirely (that check
	// only runs on exit-0 paths, so the phrase always means this failure
	// mode — it previously bucketed into validation_error). Matched AFTER
	// pr-merge-unmerged so pr-merge's richer diagnostic (#3691) keeps
	// winning for its own no-op shape, and BEFORE the generic
	// validation/exit heuristics so an embedded gate reason that mentions
	// context files doesn't bucket into validation_error.
	if strings.Contains(t, "premature turn end") ||
		strings.Contains(t, "premature_turn_end") ||
		strings.Contains(t, "exited 0 but did not write expected output context") {
		return TerminalKindPrematureTurnEnd
	}

	// Worktree-uncommitted recovery (Issue #3542). The scheduler emits the
	// `worktree_uncommitted:` marker after it auto-recovers uncommitted work
	// into a recovery commit; `stop_hook_uncommitted` is a forward-compat
	// alias for any future stop-hook kill marker that carries the same
	// meaning. Matched early so it isn't shadowed by the generic "exit"/stall
	// heuristics below.
	if strings.Contains(t, "worktree_uncommitted") ||
		strings.Contains(t, "stop_hook_uncommitted") {
		return TerminalKindWorktreeUncommitted
	}

	// USD-based pipeline budget ceiling kill (Issue #3542). MUST be matched
	// before the token-based budget heuristic below — "PIPELINE BUDGET
	// CEILING" lowercased contains the substring "budget ceiling" and would
	// otherwise bucket into TerminalKindBudgetExceeded.
	if strings.Contains(t, "budget_ceiling_hit") ||
		strings.Contains(t, "pipeline budget ceiling") {
		return TerminalKindBudgetCeiling
	}

	// Progress-based runaway kill (Issue #3783). Treated as transient stall —
	// identical recovery path to stall-kill (30m backoff, no lifetime-cap increment).
	// MUST be matched before the generic runaway-ceiling and stall-kill heuristics.
	if strings.Contains(t, "[runaway-progress-exceeded]") ||
		strings.Contains(t, "runaway-progress-exceeded") ||
		strings.Contains(t, "exitSignalSource") && strings.Contains(t, "runaway-progress") {
		return TerminalKindRunawayProgress
	}

	// Runaway-ceiling kill (Issue #3508). Treated as transient stall — identical
	// recovery path to stall-kill (30m backoff, no lifetime-cap increment).
	// MUST be matched before the cost-cap-exceeded heuristic below.
	if strings.Contains(t, "[runaway-ceiling-exceeded]") ||
		strings.Contains(t, "runaway-ceiling-exceeded") ||
		strings.Contains(t, "runaway cost ceiling exceeded") {
		return TerminalKindStallKill
	}

	// Cost-cap kills come first — they MUST be classified as budget_exceeded
	// even though the underlying TS skillRunner kill path looks stall-shaped
	// (idle SIGTERM after a polling tick). Match the canonical `[cost-cap-exceeded]`
	// marker emitted by PipelineBridge before falling through to stall-kill
	// heuristics. Kept for backward compat with old extension versions that still
	// emit [cost-cap-exceeded]. (Issue #3002 / #3207)
	if strings.Contains(t, "[cost-cap-exceeded]") ||
		strings.Contains(t, "cost-cap-exceeded") ||
		strings.Contains(t, "cost cap exceeded") {
		return TerminalKindBudgetExceeded
	}

	// Zombie-run guards (#252). `[stale-slot-orphan]` is written by the
	// extension's StaleSlotRecoveryService when a reload sweeps a run whose
	// process died without its close handler running; `[stage-no-output-timeout]`
	// is the first-output watchdog killing a stage that never produced any
	// session output (wedged pre-spawn await or silent session). Both are
	// transient-stall shaped: retry with backoff is the right recovery, and
	// neither should count against lifetime failure caps.
	if strings.Contains(t, "stale-slot-orphan") ||
		strings.Contains(t, "stage-no-output-timeout") {
		return TerminalKindStallKill
	}

	// Stall-kill heuristics — aligned with the SDK "agent" bucket.
	// `[stall-killed]` / `exceeded stall idle threshold` / `exceeded stage_hard_cap`
	// are the canonical markers PipelineBridge emits in the IPC stage result
	// `errorText` field for stall-kill path (Issue #3207). The remaining
	// substrings preserve compatibility with auto-mode error strings and the
	// existing test corpus.
	if strings.Contains(t, "[stall-killed]") ||
		strings.Contains(t, "stall-killed") ||
		strings.Contains(t, "stall kill threshold") ||
		strings.Contains(t, "stalled and killed") ||
		strings.Contains(t, "heartbeat stall") ||
		strings.Contains(t, "exceeded stall idle threshold") ||
		strings.Contains(t, "exceeded stage_hard_cap") ||
		strings.Contains(t, "hard cap") {
		return TerminalKindStallKill
	}

	// Budget-enforcer reasons (see budget_enforcer.go BudgetDecision.Reason).
	if strings.Contains(t, "pipeline_budget_exceeded") ||
		strings.Contains(t, "stage_budget_exceeded") ||
		strings.Contains(t, "budget exceeded") ||
		strings.Contains(t, "budget ceiling") {
		return TerminalKindBudgetExceeded
	}

	// Schema / output-validation failures. (The "did not write expected
	// output context" phrase moved to premature_turn_end above — its only
	// producer, validateStageOutput, runs exclusively on exit-0 paths.)
	if strings.Contains(t, "schema validation") ||
		strings.Contains(t, "invalid json") ||
		strings.Contains(t, "missing prerequisite") {
		return TerminalKindValidationError
	}

	// Adapter auth pre-flight failure (#312). The pipeline-start auth gate
	// refused to launch — an adapter probe timed out after a retry (transient
	// starvation under a concurrent dispatch burst) or the adapter CLI is
	// logged out. Matched on the stable `[adapter-auth-failed]` marker (and the
	// underscore kind) BEFORE the generic subagent-crash fallback, whose
	// "exit " substring would otherwise misbucket it as a process death and
	// feed the cascade breaker with a false crash.
	if strings.Contains(t, "[adapter-auth-failed]") ||
		strings.Contains(t, "adapter-auth-failed") ||
		strings.Contains(t, "adapter_auth_failed") {
		return TerminalKindAdapterAuthFailed
	}

	// No changes produced (#317). pr-create's deterministic create fallback
	// confirmed the feature branch has zero commits ahead of base — there is
	// genuinely nothing to open a PR for (e.g. a human-only `owner-action`
	// issue that was never disqualified from dispatch). Matched on the stable
	// `[no-changes-produced]` marker BEFORE the generic subagent-crash
	// fallback, whose "exit " substring would otherwise misbucket this as a
	// process death. Deliberately NOT matched on bare "no commits ahead of" —
	// that phrase also appears in feature-validate's unrelated
	// lost-implementation check, which must keep its organic classification.
	if strings.Contains(t, "[no-changes-produced]") ||
		strings.Contains(t, "no_changes_produced") {
		return TerminalKindNoChangesProduced
	}

	// Feature-validate honest quality-gate failure (#326). The skill exited 0
	// but wrote validation_status="failed" and left the code uncommitted for
	// retry — a real organic implementation failure caught by the pipeline's
	// own gate, not a subagent process death. Matched on the stable
	// `[validation-failed]` marker BEFORE the generic subagent-crash fallback,
	// whose "exit " substring would otherwise misbucket this as a crash.
	if strings.Contains(t, "[validation-failed]") ||
		strings.Contains(t, "validation_failed") {
		return TerminalKindValidationFailed
	}

	// Subagent process death / non-zero exit fallback.
	if strings.Contains(t, "subagent crash") ||
		strings.Contains(t, "exit ") || // matches "exit 1: …" from SetStageError
		strings.Contains(t, "killed by signal") {
		return TerminalKindSubagentCrash
	}

	return ""
}

// OutcomeTypeForTerminalFailure maps a terminal failure's error text to a
// first-class run outcome_type for the platform/dashboard. Today it recognizes
// exactly one non-generic outcome: OutcomeTypeBlocked — a pr-merge blocked by a
// required-check / branch-ruleset config that no pipeline retry can clear (the
// run shipped a reviewable PR; a human must change repo config). Surfacing it as
// "blocked" rather than a generic "failure" keeps the dashboard honest and stops
// a needs-human hold from reading as a pipeline defect. Returns "" for every
// other failure, leaving the run's default failure representation intact. Shares
// its detection with the CatRulesetBlocked failure category via
// failure.IsRulesetBlocked so both classifications stay in lockstep.
func OutcomeTypeForTerminalFailure(errorText string) string {
	if failure.IsRulesetBlocked(errorText) {
		return OutcomeTypeBlocked
	}
	return ""
}

// CurrentRunSidecar is the on-disk record of an in-flight pipeline run.
// Written at stage-start, removed on clean shutdown. A stale sidecar on
// scheduler startup signals an orchestrator crash; loadQueue synthesizes a
// terminal-failure RunRecord and pauses the queue.
//
// @see Issue #3001 ADR-003
type CurrentRunSidecar struct {
	IssueNumber int       `json:"issue_number"`
	Repo        string    `json:"repo"`
	ItemID      string    `json:"item_id,omitempty"`
	Title       string    `json:"title,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	Stage       string    `json:"stage"`
	StageStart  time.Time `json:"stage_started_at"`
	PID         int       `json:"pid,omitempty"`
}

// writeCurrentRunSidecar persists the in-flight run state atomically. Best
// effort: errors are logged but never block the pipeline.
func writeCurrentRunSidecar(workspaceRoot string, sc CurrentRunSidecar) error {
	if workspaceRoot == "" {
		return nil
	}
	p := filepath.Join(workspaceRoot, currentRunSidecarFile)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("create sidecar dir: %w", err)
	}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp sidecar: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename sidecar: %w", err)
	}
	return nil
}

// removeCurrentRunSidecar deletes the in-flight sidecar. No-op when absent.
func removeCurrentRunSidecar(workspaceRoot string) {
	if workspaceRoot == "" {
		return
	}
	p := filepath.Join(workspaceRoot, currentRunSidecarFile)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		log.Printf("failure: failed to remove current-run sidecar: %v", err)
	}
}

// readCurrentRunSidecar reads the sidecar if present. Returns (nil, nil) when
// absent so callers can distinguish missing-file from parse-error.
func readCurrentRunSidecar(workspaceRoot string) (*CurrentRunSidecar, error) {
	if workspaceRoot == "" {
		return nil, nil
	}
	p := filepath.Join(workspaceRoot, currentRunSidecarFile)
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sc CurrentRunSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("parse sidecar: %w", err)
	}
	return &sc, nil
}

// FailedRunID composes the canonical identifier for a failed run, used by
// QueuePausedReason.FailedRunID to correlate paused queue items with the
// failed RunRecord in the daily JSONL.
func FailedRunID(issueNumber int, startedAt time.Time) string {
	return fmt.Sprintf("%d-%s", issueNumber, startedAt.UTC().Format(time.RFC3339))
}

// SynthesizeOrchestratorCrashRecord constructs a V3 RunRecord from a stale
// current-run.json sidecar. Used during scheduler startup when an in-flight
// sidecar is detected — the previous orchestrator process died mid-stage and
// no terminal record was written by the normal failure path.
//
// The synthesized record carries:
//   - outcome:                 "failed"
//   - terminal_failure_kind:   "orchestrator_crash"
//   - is_recovery:             true (analytics excludes recovery runs from
//     cost-trend baselines per Issue #1261)
//   - StageDetail for the in-flight stage marked "failed" with a synthetic
//     error message so `failure_category` lands in the infrastructure bucket
//     when the SDK classifier sees it.
//
// @see Issue #3001 ADR-003
func SynthesizeOrchestratorCrashRecord(sc CurrentRunSidecar, now time.Time) state.V2RunRecord {
	stageName := sc.Stage
	if stageName == "" {
		stageName = "unknown"
	}
	stageStarted := sc.StageStart
	if stageStarted.IsZero() {
		stageStarted = sc.StartedAt
	}
	startedAt := sc.StartedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	totalDurationMs := now.Sub(startedAt).Milliseconds()
	if totalDurationMs < 0 {
		totalDurationMs = 0
	}

	stages := map[string]state.V2StageDetail{
		stageName: {
			Status:          "failed",
			StartedAt:       stageStarted.UTC().Format(time.RFC3339),
			CompletedAt:     now.UTC().Format(time.RFC3339),
			DurationMs:      now.Sub(stageStarted).Milliseconds(),
			Error:           "orchestrator process crashed mid-stage",
			LastOutputLines: "",
		},
	}

	branch := ""
	if sc.IssueNumber > 0 {
		branch = fmt.Sprintf("feat/%d", sc.IssueNumber)
	}

	rec := state.V2RunRecord{
		SchemaVersion:       "3", // V3 record — terminal_failure_kind populated.
		RecordType:          "run",
		IssueNumber:         sc.IssueNumber,
		Title:               sc.Title,
		Branch:              branch,
		BaseBranch:          "main",
		ExecutionMode:       "automatic",
		StartedAt:           startedAt.UTC().Format(time.RFC3339),
		CompletedAt:         now.UTC().Format(time.RFC3339),
		TotalDuration:       totalDurationMs,
		Outcome:             "failed",
		Stages:              stages,
		Tokens:              state.V2Tokens{},
		Files:               state.V2Files{},
		Routing:             state.V2Routing{Path: "standard", SkipStages: []string{}},
		IsRecovery:          true,
		TerminalFailureKind: TerminalKindOrchestratorCrash,
		RecordedAt:          now.UTC().Format(time.RFC3339),
	}
	return rec
}

// PipelineFailureMode is the operator-configurable behavior on terminal failure.
//
//   - FailureModeHalt           — pause queue, require operator action (default)
//   - FailureModeContinueQueue  — keep dispatching the next queued item
//   - FailureModeAutoResume     — single retry of the same item, then halt
//
// @see Issue #3001 ADR-004
const (
	FailureModeHalt          = "halt"
	FailureModeContinueQueue = "continue-queue"
	FailureModeAutoResume    = "auto-resume"
)

// GetPipelineFailureMode reads pipeline.failure_mode from .nightgauge/config.yaml
// with env-var override. Defaults to "halt" — the conservative choice for
// customer onboarding.
//
// Env override: NIGHTGAUGE_PIPELINE_FAILURE_MODE
func GetPipelineFailureMode(workspaceRoot string) string {
	if v := os.Getenv("NIGHTGAUGE_PIPELINE_FAILURE_MODE"); v != "" {
		switch v {
		case FailureModeHalt, FailureModeContinueQueue, FailureModeAutoResume:
			return v
		}
	}
	if workspaceRoot == "" {
		return FailureModeHalt
	}
	configPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return FailureModeHalt
	}
	inPipeline := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "pipeline:" {
			inPipeline = true
			continue
		}
		if inPipeline && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inPipeline = false
		}
		if inPipeline && strings.HasPrefix(trimmed, "failure_mode:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"'`)
				switch val {
				case FailureModeHalt, FailureModeContinueQueue, FailureModeAutoResume:
					return val
				}
			}
		}
	}
	return FailureModeHalt
}

// isModelUnavailableText reports whether the lowercased failure text is a
// model-rejection API response (#42). Documented Anthropic shapes covered —
// per the issue's AC, if observed responses differ from these, update this
// list with the actual text before extending the matcher:
//
//   - HTTP 404 `{"type":"error","error":{"type":"not_found_error","message":"model: <id>"}}`
//     — unknown model ID, or a model the current API key/plan cannot access.
//   - `invalid_request_error` / CLI wording naming an invalid or unknown model.
//   - Plan-restriction wording ("<model> is not available on your plan").
//   - Model-specific usage caps ("claude-fable-5 usage limit reached",
//     "Opus weekly limit reached" on Claude Code Max plans).
//
// Plan/cap phrases are additionally gated on a registry model reference so
// account-level limit messages (no model named) keep routing to the generic
// quota path instead of triggering a model downgrade.
func isModelUnavailableText(t string) bool {
	if strings.Contains(t, "not_found_error") && strings.Contains(t, "model") {
		return true
	}
	if strings.Contains(t, "model not found") ||
		strings.Contains(t, "invalid model") ||
		strings.Contains(t, "unknown model") {
		return true
	}
	planPhrase := strings.Contains(t, "not available on your") ||
		strings.Contains(t, "not included in your") ||
		strings.Contains(t, "not offered on your") ||
		strings.Contains(t, "not supported on your")
	capPhrase := strings.Contains(t, "usage limit") ||
		strings.Contains(t, "usage cap") ||
		strings.Contains(t, "weekly limit")
	if (planPhrase || capPhrase) && mentionsRegistryModel(t) {
		return true
	}
	return false
}

// mentionsRegistryModel reports whether the text names a model from the model
// registry — by concrete ID ("claude-fable-5"), display name ("fable 5"), or
// tier ("fable"/"opus"/…). Registry-derived rather than hardcoded so new
// models are covered as the registry evolves (#42 AC).
func mentionsRegistryModel(t string) bool {
	seenTiers := map[string]bool{}
	for _, m := range models.All() {
		if m.ID != "" && strings.Contains(t, strings.ToLower(m.ID)) {
			return true
		}
		if m.DisplayName != "" && strings.Contains(t, strings.ToLower(m.DisplayName)) {
			return true
		}
		for _, tier := range m.Tiers {
			seenTiers[strings.ToLower(tier)] = true
		}
	}
	for tier := range seenTiers {
		if strings.Contains(t, tier) {
			return true
		}
	}
	return false
}
