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
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
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
	// TerminalKindDevProducedNoChanges is set when feature-dev's gate finds the
	// stage workspace empty — clean working tree, branch level with its base —
	// while the dev context reports files changed. A narrower, better-named
	// TerminalKindPrematureTurnEnd: the gate's Kind is still KindNoOp, but the
	// verdict names WHAT was missing instead of only that something was.
	//
	// Distinct from TerminalKindNoChangesProduced, which means the opposite in
	// a way that matters for triage: that kind says the issue genuinely had no
	// code to write (a dispatch-eligibility gap — a human-only task was
	// dispatched, and producing nothing was correct). This kind says the issue
	// had work, the stage did it, and the pipeline cannot see it. Bucketing
	// #202 as no_changes_produced would have read as "nothing to do here" for
	// a run that spent 31 minutes writing five files.
	//
	// Classified `agent` (0.5 weight) alongside premature_turn_end: this is
	// the stage's own turn-ending/delegation behavior, not the environment and
	// not the issue. Issue #202.
	TerminalKindDevProducedNoChanges = "dev_produced_no_changes"
	// TerminalKindDevHandoffMissing is the exact inverse of
	// DevProducedNoChanges: the dev context is absent or reports nothing, and
	// git finds the changed files sitting in the stage workspace. The stage did
	// the work and ended before writing its handoff.
	//
	// Kept distinct because the recovery is opposite. DevProducedNoChanges
	// means there is nothing to salvage and a retry must redo the work.
	// This kind means the work EXISTS: it has to be committed to the branch
	// before anything sweeps the worktree, and a retry must build on it. In
	// #221 the two were conflated — the gate reported "dev context records zero
	// file changes" over a tree holding 206 insertions across 7 files plus a
	// new package, and the halt card told the operator the stage had produced
	// nothing. One worktree sweep from destroying $7.40 of passing code.
	//
	// Classified `agent` (0.5 weight) alongside premature_turn_end: ending a
	// turn without writing the handoff is the stage's own behavior. Issue #223.
	TerminalKindDevHandoffMissing = "dev_handoff_missing"
	// TerminalKindContainmentBreach is set when the write-containment check
	// (#129) finds a stage wrote into a repository it does not own. The stage
	// exits 0 and reports success — it did work, just not where the pipeline
	// can use it — so nothing else in the chain marks it failed.
	//
	// #230: this had no kind at all. The breach forced success=false in
	// skillRunner and stopped there, so the record carried an empty
	// terminal_kind, the Action Center card read `blocker: "unclassified"`,
	// and the operator was never told which repository had been written into —
	// while a manifest on disk named the repo, every escaped path, and a
	// restorable patch. One breach reached an issue's lifetime failure cap and
	// halted the fleet with no actionable reason.
	//
	// Classified `agent` (0.5 weight) alongside premature_turn_end and
	// dev_handoff_missing: leaving the assigned worktree is the stage's own
	// behavior, not the environment and not the issue.
	TerminalKindContainmentBreach = "containment_breach"
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
	// TerminalKindValidationInconclusive is set when feature-validate's unit-
	// test tier RAN but executed zero tests: `validation_status:
	// "inconclusive"` (context-and-board.md Step 6.1, ahead of the
	// failed-tests check so a zero-test run can never fall into `failed` — a
	// suite that ran nothing vacuously "passed" pre-fix). This is usually an
	// environmental misconfiguration (the wrong test command for this repo)
	// rather than a code defect, so unlike TerminalKindValidationFailed it
	// does NOT increment LifetimeIssueFailures and does NOT feed the cascade
	// circuit breaker — same recoverable treatment as
	// TerminalKindWorktreeUncommitted / TerminalKindBudgetCeiling (#3542):
	// fixed backoff, board → Ready so the issue is re-dispatched (a corrected
	// test command, or a subsequent commit that adds tests, resolves it on
	// retry). Emitted by HeadlessOrchestrator.verifyPostValidateState with the
	// `[validation-inconclusive]` marker (mirrors the `[validation-failed]`
	// marker pattern). Issue #221.
	TerminalKindValidationInconclusive = "validation_inconclusive"
	// TerminalKindBranchForked is set when the run's branch and its remote
	// counterpart have diverged: the remote head is not reachable from the
	// local tip, so every push from this worktree is rejected as
	// non-fast-forward. Two causes produce it — a stage killed mid-push that
	// had already pushed, and an operator pushing to a pipeline-owned branch —
	// and neither can be cleared by retrying. Pre-fix it surfaced as a generic
	// stage failure and the run was re-dispatched, regenerating a full
	// implementation (~$25) to reach a guaranteed rejection, once per cycle,
	// until the lifetime failure cap tripped on a condition the pipeline
	// itself created (#163).
	//
	// Emitted by the Go scheduler's pre-stage fork pre-flight (CheckBranchFork)
	// with the `[branch-forked]` marker, and matched on the skills' push-time
	// `PUSH REJECTED: non-fast-forward` sentence so a fork that first surfaces
	// at push time classifies identically. Requires human action (or the
	// post-run orphan reclamation) — the autonomous scheduler neither counts it
	// toward LifetimeIssueFailures nor feeds the cascade breaker with it, and
	// does not re-dispatch: an automatic retry is precisely the loop this kind
	// exists to stop.
	TerminalKindBranchForked = "branch_forked"
	// TerminalKindArchitectureApprovalRequired is set when the architecture-
	// approval gate (#4098/#4222) halts a run BEFORE feature-dev because the
	// issue is a high-impact, human-owned decision: ≥2 trade-off signals or a
	// risk_high routing verdict, with no `approved:architecture` label and no
	// approval file. The gate's own message says "This is NOT a failure" — the
	// pipeline stopped on purpose, exactly as designed.
	//
	// Pre-fix it had no matcher and fell through to the generic
	// subagent_crash fallback, which is wrong three times over: it labelled a
	// deliberate human gate a process death, it counted toward
	// LifetimeIssueFailures, and it reverted the board to Ready so the
	// scheduler re-dispatched into a gate only a human can open. Observed in a
	// production autonomous run: ~$5 and ~13 minutes per attempt to rediscover
	// the same "a human must decide this", with the SECOND attempt due to hit
	// MaxLifetimeFailuresPerIssue and trip the whole scheduler to
	// safety_tripped — a full workspace stop caused by a safety feature
	// working correctly.
	//
	// Routed like TerminalKindPrMergeUnmerged (sideline the issue, not the
	// factory): NO LifetimeIssueFailures increment, NO cascade feed (a human
	// decision point says nothing about the health of the factory), NO pause,
	// and the board moves to "In review" — the truthful state, and what the
	// #4222 handler always claimed happened but never did, because the kind
	// never reached the Go scheduler. Deliberately does NOT revert to Ready:
	// as with TerminalKindBranchForked, an automatic retry is precisely the
	// loop this kind exists to stop. The way back in is human approval (label
	// or approval file), which re-queues the issue.
	TerminalKindArchitectureApprovalRequired = "architecture_approval_required"
	// TerminalKindAbandonedCommit marks a stage killed/crashed after it
	// committed valid, unmerged work but before pr-create ran (#191). Distinct
	// from TerminalKindWorktreeUncommitted (#3542, which covers *uncommitted*
	// work preserved via a recovery commit) — here the work is already a clean
	// commit on the branch; the gap is that nothing created a PR from it. Not
	// derived by ClassifyTerminalKind from error text — it is set by the
	// AbandonedCommitRecoverable recovery action itself when it fires and
	// cannot fully self-heal via the deterministic PRCreateRunner, mirroring
	// how gate-sourced kinds override ResolveTerminalKind.
	TerminalKindAbandonedCommit = "abandoned_commit"
	// TerminalKindCommitOrphaned is the backstop for the specific stranding
	// #266 was filed for: a stage killed (SIGKILL) mid `nightgauge pre-push
	// validate` bypasses pre_push.go's restore-defer (a defer cannot run
	// against SIGKILL), leaving the worktree checked out on a stray
	// `temp-pre-push-<n>` branch. If feature-validate's later commit step then
	// commits without checking which branch it landed on, the commit is valid
	// but sits on a branch nobody expects. Unlike TerminalKindAbandonedCommit
	// (work is on the correct feature branch, just missing a PR), here the
	// commit is on the WRONG branch — self-heal (feedback-and-commit.md Step
	// 5.1) checks out the expected feature branch before committing and is the
	// primary fix; this kind exists for the case self-heal itself cannot
	// recover from (the expected feature branch doesn't exist locally either),
	// where the skill fails loudly rather than silently committing onto the
	// wrong ref. Emitted via the `[commit-orphaned]` marker (mirrors
	// `[branch-forked]` / `[validation-failed]`). Recoverable: does NOT
	// increment LifetimeIssueFailures — the commit is not lost, CleanupWorktree
	// and CleanupLocalBranch's ahead-of-base guard (also added in #266) refuse
	// to delete the branch/worktree holding it, so a human can push it or open
	// a PR by hand.
	TerminalKindCommitOrphaned = "commit_orphaned"
	// TerminalKindPermissionDenied is set when the harness denies a tool call
	// outright — most commonly a stage reaching for a foreground `sleep` wait
	// loop, which the host blocks and reports as `is_error: true,
	// tool_use_result: "User rejected tool use"`. A denial is the harness
	// saying "not that way", not a defect in the work: the stage had turns
	// remaining and could pick a different approach (e.g. a backgrounded
	// command plus its completion notification) on its very next turn. Pre-fix
	// this fell through to the generic subagent_crash fallback, which
	// permanently killed the run, counted toward LifetimeIssueFailures, and
	// tripped haltQueueOnSlotFailure over a single rejected tool call — pausing
	// the whole fleet for a retryable mistake (#289).
	//
	// Routed like TerminalKindAdapterAuthFailed: short backoff, board → Ready,
	// NO LifetimeIssueFailures increment, NO cascade-breaker feed, NO pause —
	// but bounded by permissionDeniedMaxAttempts so a stage that keeps
	// reaching for the same denied pattern eventually stops re-dispatching
	// instead of looping forever. Classified `infrastructure` (harness
	// behavior, not a code or issue defect) in docs/FAILURE_TAXONOMY.md.
	TerminalKindPermissionDenied = "permission_denied"
)

// ClassifyTerminalKind returns the terminal failure kind for the given error
// text, or "" when nothing matches; callers fall back to
// TerminalKindSubagentCrash (the most generic).
//
// THE LADDER IS DATA (#306). The ordered rule table lives in
// internal/terminalkind/table.json and this is a delegation to its interpreter.
// It is not a mirror of anything and nothing mirrors it: the SDK's
// classifyTerminalKind and the extension's signal ladder interpret the SAME
// table, the SDK's copy generated from it with a byte-equality drift check. The
// three used to be hand-written ladders held aligned by comments, and they
// disagreed on 19 of 98 real and synthetic inputs.
//
// @see Issue #3001, #306
func ClassifyTerminalKind(errorText string) string {
	return terminalkind.Classify(errorText)
}

// ResolveTerminalKind prefers a gate-sourced structured terminal kind over
// prose classification of the synthesized error text (Issue #9). Falls
// back to ClassifyTerminalKind for non-gate failures and for gate
// failures that didn't set a structured kind (including all historical
// records persisted before this field existed).
func ResolveTerminalKind(gateRan bool, gateTerminalKind string, errorText string) string {
	if gateRan && gateTerminalKind != "" {
		return gateTerminalKind
	}
	return ClassifyTerminalKind(errorText)
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
