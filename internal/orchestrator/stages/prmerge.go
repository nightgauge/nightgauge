// Package stages contains deterministic stage runners — Go-native pipeline
// stages that bypass the LLM skill path when their work is purely an API
// call. The pr-merge stage is the first such runner; future candidates
// (pr-create has been suggested in epic #3261) follow the same pattern:
// pre-flight via `gh`, decide via a pure function over a typed snapshot,
// execute via a single shell-out, re-verify, and report a Path that the
// scheduler uses to skip or fall through to the existing LLM path.
//
// See docs/PR_MERGE_STAGE.md for the architecture and decision matrix.
package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/codexprovision"
	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/knowledge"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

// PRMergePath is the outcome of a deterministic pr-merge attempt.
//
//   - PathMerged — the PR is merged (either was already, or this runner
//     issued the merge and re-verified MERGED). Scheduler skips the LLM skill.
//   - PathPunt   — the runner declined to merge for a reason that requires
//     human or LLM judgment (real conflict, failed CI, missing review,
//     rate-limit, unexpected error). Scheduler falls through to the existing
//     LLM skill path.
//   - PathRefused — the runner refused the merge for a reason no LLM retry may
//     override (generated steering on the PR head, issue 1675), or a merge
//     queue it enqueued the PR in reported a failure or outlived its budget
//     (#2214). The scheduler fails the stage instead of falling through to the
//     LLM skill, which could otherwise merge the same head or spend tokens
//     babysitting a queue.
type PRMergePath string

const (
	PathMerged  PRMergePath = "merged"
	PathPunt    PRMergePath = "punt"
	PathRefused PRMergePath = "refused"
)

// Reason codes recorded on PRMergeResult.Reason. Free-form strings are also
// allowed (the scheduler logs them verbatim) — these constants name the
// canonical buckets so telemetry queries can group them.
const (
	ReasonAlreadyMerged  = "already-merged"
	ReasonCleanMerged    = "clean-mergeable: merged"
	ReasonNotMergeable   = "not-mergeable"
	ReasonDirtyState     = "dirty-merge-state"
	ReasonFailedChecks   = "failed-ci-checks"
	ReasonReviewMissing  = "review-not-approved"
	ReasonRateLimited    = "rate-limited"
	ReasonECTimeout      = "eventual-consistency-timeout"
	ReasonMergeECTimeout = "merge-ec-timeout" // merge call succeeded but post-merge state never reached MERGED within EC window
	ReasonGHUnavailable  = "gh-unavailable"
	ReasonNoPRContext    = "no-pr-context-file"
	ReasonInvalidPRJSON  = "pr-context-invalid-json"
	ReasonNoPRNumber     = "pr-context-missing-pr-number"
	ReasonMergeFailed    = "merge-call-failed"
	ReasonUnexpected     = "unexpected-error"
	// ReasonCIWaitTimeout is recorded when the runner waited the full bounded CI
	// budget for pending required checks to conclude and they never did (Issue
	// #297). Distinct from ReasonFailedChecks (a check reported FAILURE/ERROR):
	// a timeout means CI was still in-flight when the budget expired.
	ReasonCIWaitTimeout = "ci-wait-timeout"
	// ReasonNoChecksCreated is recorded when the PR was BLOCKED/UNSTABLE with
	// an EMPTY check rollup for the whole no-check grace window (#1027).
	// pr-merge starts seconds after pr-create, before GitHub has created any
	// check run, so the first snapshot is BLOCKED with zero checks — that is
	// "CI has not started yet", not a dirty merge state, and the runner waits
	// the grace window for a run to appear. If none does, the repo most likely
	// has no CI that will ever satisfy the ruleset; the punt names that so the
	// LLM spend is attributable, and `dirty-merge-state: BLOCKED` is reserved
	// for a block the deterministic path actually diagnosed.
	ReasonNoChecksCreated = "no-checks-created"
	// ReasonKnowledgeNonConformant is recorded when an entry in the issue's
	// knowledge directory does not carry the frontmatter contract (a
	// parseable block with a `type`). A contract that is not enforced drifts
	// back into two contracts within a month, and the merge is the only
	// chokepoint every change passes through.
	//
	// The gate is deliberately scoped to THIS issue's directory. The
	// knowledge base is local, gitignored, per-machine state, so a stale
	// entry from an unrelated issue must never block someone's merge; the
	// whole-base sweep is `knowledge validate --conformance`.
	ReasonKnowledgeNonConformant = "knowledge-non-conformant"
	// ReasonMergeabilityUnresolved is recorded when GitHub never finished
	// computing `mergeable` within the bounded wait (#1933).
	//
	// Deliberately NOT `not-mergeable: UNKNOWN`. UNKNOWN is the absence of a
	// verdict, not a negative one, and reading it as a conflict is what punted
	// the deterministic path eleven seconds into every run that lost the race:
	// `Decide` tested `Mergeable != "MERGEABLE"` first, so a value GitHub was
	// still computing outranked every other signal, and `MergeBlockedByPendingCI`
	// — which also demands MERGEABLE — meant the #297 CI wait never got its
	// turn at all. A punt that survives the wait names the absence, so it can
	// never again be read as a conflict that was actually diagnosed.
	ReasonMergeabilityUnresolved = "mergeability-unresolved"
	// ReasonGeneratedSteering is recorded (with PathRefused) when an AGENTS.md
	// at the PR head carries the pipeline's managed Codex steering block —
	// generated, per-stage content that must never reach the default branch
	// (issue 1675). Never merged and never handed to the LLM path.
	ReasonGeneratedSteering = "generated-steering-committed"
	// ReasonMergeQueueMerged is recorded (with PathMerged) when the base
	// branch requires a merge queue and the runner enqueued the PR and then
	// observed the queue merge it (#2214).
	ReasonMergeQueueMerged = "merge-queue: merged"
	// ReasonMergeQueueFailed is recorded (with PathRefused) when the queue
	// removed the PR — its merge group failed, it became unmergeable, or it
	// was closed. The reason names the failing merge-group checks so CI
	// failure handling and retro can act; the LLM skill is never invoked for
	// a queue outcome.
	ReasonMergeQueueFailed = "merge-queue-failed"
	// MergeQueueConflictMarker is the token a queue-failure reason carries
	// when the PR was removed because it conflicts with its base (#2611).
	// Conflict recovery matches on it to rebase instead of failing the run.
	MergeQueueConflictMarker = "merge conflict with"
	// ReasonMergeQueueTimeout is recorded (with PathRefused) when the PR was
	// still queued when the merge-queue wait budget expired.
	ReasonMergeQueueTimeout = "merge-queue-timeout"
	// ReasonMergeQueueEnqueueFailed is recorded (with PathRefused) when the
	// enqueuePullRequest mutation itself failed.
	ReasonMergeQueueEnqueueFailed = "merge-queue-enqueue-failed"
)

// CI-wait budget for the deterministic pr-merge path (Issue #297). When the
// ONLY thing blocking an otherwise-clean, mergeable PR is in-flight CI, the
// runner polls until the merge state clears rather than punting to the LLM
// skill — which historically "won" pr-merge purely by babysitting CI for ~10
// minutes at ~$3–4.44/run. Bounded so a genuinely stuck PR still falls through
// to the LLM path within a predictable budget: github.DefaultCIPollInterval x
// github.DefaultCIPollMax (30 × 30s = 15 min), shared with the merge queue's
// readiness wait (#2584).
const (
	// DefaultCINoCheckGracePolls bounds how many CI-wait polls the runner
	// spends on a BLOCKED/UNSTABLE PR whose check rollup is still EMPTY
	// (#1027): 4 × 30 s = 2 min for GitHub to create the first check run. It
	// is a prefix of the CI-wait budget, not an addition to it, and it is
	// bounded so a repo with no CI at all cannot hold the deterministic path
	// for the full 15 min before punting `no-checks-created`.
	DefaultCINoCheckGracePolls = 4
)

// PRMergeResult is the outcome of a single Run invocation.
type PRMergeResult struct {
	Path       PRMergePath
	PRNumber   int
	PRState    string
	Reason     string
	DurationMs int64
	// HeadRefName is the PR's head branch, populated whenever Path is
	// PathMerged. The merge call itself never deletes it (Issue #589 — see
	// execGhClient.Merge); callers that want remote-branch cleanup after a
	// deterministic merge use this to drive it themselves through a
	// common-dir-aware git service, without shelling to `gh` a second time.
	HeadRefName string
	// PartialDelivery is set when the acceptance-criteria backstop (#2623)
	// rewrote the PR body from `Closes #N` to `Refs #N` before merging, so the
	// merge leaves the issue open. PartialDeliveryReason says why.
	PartialDelivery       bool
	PartialDeliveryReason string
}

// PartialDeliveryACUncovered is the PartialDeliveryReason when the issue has
// unchecked criteria the run never reconciled (#2623).
const PartialDeliveryACUncovered = "acceptance-criteria-uncovered"

// PRMergeRunner is the contract the scheduler uses to invoke the deterministic
// path. The default implementation (NewDeterministicRunner) shells out to
// `gh`; tests substitute a fake.
type PRMergeRunner interface {
	Run(ctx context.Context, issueNumber int, repo, workdir string) (PRMergeResult, error)
}

// PRViewSnapshot is the typed projection of `gh pr view --json …` output that
// the decision matrix operates on. Decoupled from the shell layer so the
// matrix is exhaustively unit-testable without stubbing `gh`.
type PRViewSnapshot struct {
	State             string             // OPEN | MERGED | CLOSED
	Mergeable         string             // MERGEABLE | CONFLICTING | UNKNOWN
	MergeStateStatus  string             // CLEAN | DIRTY | BLOCKED | UNSTABLE | BEHIND | DRAFT | HAS_HOOKS | UNKNOWN
	ReviewDecision    string             // APPROVED | REVIEW_REQUIRED | CHANGES_REQUESTED | "" (no review required)
	StatusCheckRollup []PRStatusCheckRow // CI / status checks
	HeadRefName       string             // the PR's head branch — carried through to PRMergeResult on merge
	// RequiredChecks is the set of required status-check contexts on the PR's
	// base branch (rulesets and, when readable, classic protection), resolved
	// only for an OPEN BLOCKED/UNSTABLE PR (#1891). nil means none are
	// configured or the set could not be read; the predicates then behave
	// exactly as they did before the set was consulted.
	RequiredChecks []string
}

// RequiredCheckGaps reports, for each required context in snap.RequiredChecks,
// whether the rollup lacks it entirely (absent), reports it SKIPPED, or
// reports it FAILURE/ERROR (failing). Names compare case-insensitively, the
// same rule github.MissingRequiredChecks uses. All three are nil when no
// required set is known (#1891).
func RequiredCheckGaps(snap PRViewSnapshot) (absent, skipped, failing []string) {
	if len(snap.RequiredChecks) == 0 {
		return nil, nil, nil
	}
	byName := make(map[string]string, len(snap.StatusCheckRollup))
	for _, c := range snap.StatusCheckRollup {
		key := strings.ToLower(strings.TrimSpace(c.Name))
		// A context can appear more than once (re-runs, matrix legs sharing a
		// name). Keep the worst reading: failing > skipped > anything else.
		prev, seen := byName[key]
		if !seen || checkGapRank(c.Conclusion) > checkGapRank(prev) {
			byName[key] = c.Conclusion
		}
	}
	for _, name := range snap.RequiredChecks {
		conclusion, ok := byName[strings.ToLower(strings.TrimSpace(name))]
		switch {
		case !ok:
			absent = append(absent, name)
		case conclusion == "FAILURE" || conclusion == "ERROR":
			failing = append(failing, name)
		case conclusion == "SKIPPED":
			skipped = append(skipped, name)
		}
	}
	return absent, skipped, failing
}

func checkGapRank(conclusion string) int {
	switch conclusion {
	case "FAILURE", "ERROR":
		return 2
	case "SKIPPED":
		return 1
	}
	return 0
}

// requiredCheckEvidence renders RequiredCheckGaps for a punt reason, so the
// cost of a punt is attributable to what the required set looked like at the
// verdict (#1891). Empty when no required set is known.
func requiredCheckEvidence(snap PRViewSnapshot) string {
	if len(snap.RequiredChecks) == 0 {
		return ""
	}
	absent, skipped, failing := RequiredCheckGaps(snap)
	return fmt.Sprintf("required checks: absent=[%s] skipped=[%s] failing=[%s]",
		strings.Join(absent, ","), strings.Join(skipped, ","), strings.Join(failing, ","))
}

// withRequiredCheckEvidence appends requiredCheckEvidence to a punt reason
// in parentheses, keeping the reason's prefix (which IsBranchProtectionPunt
// and the telemetry buckets match on) intact.
func withRequiredCheckEvidence(reason string, snap PRViewSnapshot) string {
	if ev := requiredCheckEvidence(snap); ev != "" {
		return reason + " (" + ev + ")"
	}
	return reason
}

// PRStatusCheckRow is the projection of one GitHub statusCheckRollup entry.
type PRStatusCheckRow struct {
	Name       string
	Conclusion string // SUCCESS | FAILURE | ERROR | NEUTRAL | SKIPPED | "" (in-flight)
}

// ghClient abstracts the `gh` shell-out so tests can inject deterministic
// snapshots and merge results.
type ghClient interface {
	View(ctx context.Context, prNumber int) (PRViewSnapshot, error)
	Merge(ctx context.Context, prNumber int) error
}

// prBodyClient is implemented by ghClients that can read and edit a PR body
// and read an issue body: the acceptance-criteria backstop (#2623). A client
// without it skips the backstop.
type prBodyClient interface {
	PRBody(ctx context.Context, prNumber int) (string, error)
	EditPRBody(ctx context.Context, prNumber int, body string) error
	IssueBody(ctx context.Context, issueNumber int) (string, error)
}

// mergeQueueClient is implemented by ghClients that can see and drive
// GitHub's merge queue (#2214). A client without it keeps the direct-merge
// path, which is exactly the behaviour on a branch without a queue.
type mergeQueueClient interface {
	// MergeQueueStatus reports whether the PR's base branch requires a merge
	// queue and where the PR stands in it.
	MergeQueueStatus(ctx context.Context, prNumber int) (github.MergeQueueStatus, error)
	// EnqueueAndWait enqueues the PR (unless already queued) and waits for the
	// queue's outcome: nil on MERGED, *github.MergeQueueError otherwise.
	EnqueueAndWait(ctx context.Context, status github.MergeQueueStatus, opts github.MergeQueueWaitOptions) (github.MergeQueueStatus, error)
}

// MergeDecision is the output of the pure decision rule. The shell-out
// populates a snapshot, Decide() evaluates it, and the runner reacts.
type MergeDecision struct {
	ShouldMerge bool
	Punt        bool
	Reason      string
}

// Decide is the pure-function decision matrix. Given a snapshot, decide
// whether to issue the merge, declare already-merged, or punt.
//
// Decision matrix (most specific blocker wins; a failed check or a blocking
// review is reported even when it also makes MergeStateStatus non-CLEAN,
// which is the overwhelming common case in production — see #1927):
//
//	state == MERGED                                       → ShouldMerge=false (already merged; runner returns merged)
//	state == OPEN && MERGEABLE
//	  && no FAILURE/ERROR checks
//	  && review not blocking
//	  && CLEAN                                            → ShouldMerge=true
//	state == OPEN && any failed check                     → Punt (failed CI)
//	state == OPEN && REVIEW_REQUIRED|CHANGES_REQUESTED    → Punt
//	state == OPEN && (CONFLICTING | DIRTY | BLOCKED ...)  → Punt (catch-all,
//	  reached only when nothing more specific applies, e.g. a genuine
//	  BEHIND/DIRTY state)
//	any other (CLOSED, unknown)                           → Punt
//
// "Review not blocking" means ReviewDecision is APPROVED or empty (no
// reviewers required by the branch ruleset). REVIEW_REQUIRED and
// CHANGES_REQUESTED both block.
func Decide(snap PRViewSnapshot) MergeDecision {
	if snap.State == "MERGED" {
		return MergeDecision{Reason: ReasonAlreadyMerged}
	}
	if snap.State != "OPEN" {
		return MergeDecision{Punt: true, Reason: fmt.Sprintf("pr-state-%s", strings.ToLower(snap.State))}
	}

	if snap.Mergeable != "MERGEABLE" {
		return MergeDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonNotMergeable, snap.Mergeable)}
	}

	for _, c := range snap.StatusCheckRollup {
		if c.Conclusion == "FAILURE" || c.Conclusion == "ERROR" {
			return MergeDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonFailedChecks, c.Name)}
		}
	}

	if snap.ReviewDecision == "REVIEW_REQUIRED" || snap.ReviewDecision == "CHANGES_REQUESTED" {
		return MergeDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonReviewMissing, snap.ReviewDecision)}
	}

	if snap.MergeStateStatus != "CLEAN" {
		return MergeDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonDirtyState, snap.MergeStateStatus)}
	}

	return MergeDecision{ShouldMerge: true, Reason: ReasonCleanMerged}
}

// logPuntSnapshot records the full decision snapshot behind a punt so the
// reason for a punt is provable after the fact instead of inferred from a
// single reason string (#1927 AC1) — also the measurement instrument for
// whether dirty-merge-state stops standing in for a failed check.
func logPuntSnapshot(prNumber int, reason string, snap PRViewSnapshot) {
	checks := make([]string, 0, len(snap.StatusCheckRollup))
	for _, c := range snap.StatusCheckRollup {
		conclusion := c.Conclusion
		if conclusion == "" {
			conclusion = "PENDING"
		}
		checks = append(checks, fmt.Sprintf("%s=%s", c.Name, conclusion))
	}
	log.Printf("pr-merge: punt snapshot pr=%d reason=%q state=%s mergeable=%s mergeStateStatus=%s reviewDecision=%s checks=%d [%s] required=[%s]",
		prNumber, reason, snap.State, snap.Mergeable, snap.MergeStateStatus, snap.ReviewDecision,
		len(snap.StatusCheckRollup), strings.Join(checks, ", "), strings.Join(snap.RequiredChecks, ", "))
}

// The mergeability wait (#1933) is bounded by github.DefaultMergeabilityPolls x
// github.DefaultMergeabilityPollInterval; github.MergeableUnknown is GitHub's
// "not computed yet" answer. They live in internal/github so the merge queue's
// readiness wait shares the budget (#2584).

// DefaultECPolls / DefaultECPollInterval mirror the eventual-consistency
// budget the TS verifyPostMergeState uses. Re-declared here so the Go runner
// matches the existing tolerance without a runtime dependency on the TS path.
const (
	DefaultECPolls        = 4
	DefaultECPollInterval = 2 * time.Second
)

// DeterministicRunner is the default PRMergeRunner implementation: read
// pr-{N}.json, shell out to `gh`, evaluate Decide, optionally merge, and
// re-verify with eventual-consistency polling.
type DeterministicRunner struct {
	gh            ghClient
	prContextRead func(workdir string, issueNumber int) (int, error) // hook for tests; default reads pr-{N}.json
	pollInterval  time.Duration
	pollMax       int
	// ciPollInterval / ciPollMax bound the CI-completion wait (Issue #297) —
	// separate from pollInterval/pollMax, which is the short eventual-consistency
	// budget for post-merge state propagation.
	ciPollInterval time.Duration
	ciPollMax      int
	// ciNoCheckGrace bounds the prefix of the CI wait spent on a PR whose
	// check rollup is still empty (#1027) — see DefaultCINoCheckGracePolls.
	ciNoCheckGrace int
	// mergeabilityPollInterval / mergeabilityPollMax bound the wait for GitHub
	// to compute `mergeable` (#1933) — a third budget, separate from both the
	// EC budget above and the CI budget, because it is waiting on a different
	// thing on a different timescale.
	mergeabilityPollInterval time.Duration
	mergeabilityPollMax      int
	now                      func() time.Time
	// knowledgeConformance checks the issue's knowledge entries against the
	// frontmatter contract. Injectable for tests; nil disables the gate.
	knowledgeConformance func(workdir string, issueNumber int) (*knowledge.ConformanceResult, error)
	// steeringGate inspects the PR head for committed managed steering and
	// repairs it from a local worktree when one exists (issue 1675).
	// Injectable for tests; nil disables the gate.
	steeringGate func(ctx context.Context, workdir, headRef string) (codexprovision.PRHeadVerdict, error)
	// queueWait bounds the merge-queue wait (#2214): sized for a full CI run
	// on the merge-group commit, not for eventual consistency. Configured by
	// pipeline.merge_queue.wait_timeout.
	queueWait github.MergeQueueWaitOptions
}

// SetMergeQueueWaitTimeout sets the merge-queue wait budget (#2214). A
// non-positive value keeps github.DefaultMergeQueueWaitTimeout.
func (r *DeterministicRunner) SetMergeQueueWaitTimeout(d time.Duration) {
	r.queueWait.Timeout = d
}

// SetCIWaitTimeout sizes the CI-completion wait (pr.ci_check_timeout, #2504)
// in whole poll intervals, rounding up. A non-positive value keeps
// github.DefaultCIPollMax.
func (r *DeterministicRunner) SetCIWaitTimeout(d time.Duration) {
	if d <= 0 || r.ciPollInterval <= 0 {
		return
	}
	r.ciPollMax = int((d + r.ciPollInterval - 1) / r.ciPollInterval)
}

// NewDeterministicRunner builds a runner using a real `gh`-backed client.
// The workdir argument to Run is used as the cwd for `gh` invocations so
// the runner respects the orchestrator-managed worktree.
func NewDeterministicRunner() *DeterministicRunner {
	return &DeterministicRunner{
		gh:             &execGhClient{}, // workdir set per Run via withWorkdir
		prContextRead:  readPRContextNumber,
		pollInterval:   DefaultECPollInterval,
		pollMax:        DefaultECPolls,
		ciPollInterval: github.DefaultCIPollInterval,
		ciPollMax:      github.DefaultCIPollMax,
		ciNoCheckGrace: DefaultCINoCheckGracePolls,

		mergeabilityPollInterval: github.DefaultMergeabilityPollInterval,
		mergeabilityPollMax:      github.DefaultMergeabilityPolls,

		now: time.Now,

		knowledgeConformance: knowledge.ValidateConformanceForIssue,
		steeringGate: func(ctx context.Context, workdir, headRef string) (codexprovision.PRHeadVerdict, error) {
			return codexprovision.GuardPRHead(ctx, workdir, "origin", headRef)
		},
	}
}

// NewDeterministicRunnerWithClient builds a runner with a caller-provided
// ghClient (used by tests and by the scheduler when wiring a workdir-bound
// client).
func NewDeterministicRunnerWithClient(client ghClient) *DeterministicRunner {
	r := NewDeterministicRunner()
	r.gh = client
	return r
}

// Run implements PRMergeRunner.
func (r *DeterministicRunner) Run(ctx context.Context, issueNumber int, _ string, workdir string) (PRMergeResult, error) {
	start := r.now()
	finish := func(res PRMergeResult, err error) (PRMergeResult, error) {
		res.DurationMs = r.now().Sub(start).Milliseconds()
		return res, err
	}

	// If the underlying client supports per-call workdir binding (the real
	// exec-backed client does), inject the workdir for this Run.
	gh := r.gh
	if wb, ok := gh.(workdirBoundClient); ok {
		gh = wb.withWorkdir(workdir)
	}

	ph := newPhaseEmitter(ctx, prMergePhases)

	ph.start("read-pr-context")
	prNumber, err := r.prContextRead(workdir, issueNumber)
	if err != nil {
		ph.supersedeInFlight()
		// No pr-{N}.json or unreadable. Punt — the LLM skill path can decide
		// whether to author the PR or fail the run. Don't silently treat as
		// merged: a missing PR context is structurally unexpected at this
		// stage.
		return finish(PRMergeResult{
			Path:   PathPunt,
			Reason: classifyContextError(err),
		}, nil)
	}

	ph.complete("read-pr-context")

	// Pre-flight + eventual-consistency polling.
	ph.start("ci-gate")
	snap, fetchErr := r.fetchWithPolling(ctx, gh, prNumber)
	if fetchErr != nil {
		ph.supersedeInFlight()
		return finish(PRMergeResult{
			Path:     PathPunt,
			PRNumber: prNumber,
			Reason:   classifyFetchError(fetchErr),
		}, nil)
	}

	// The steering gate runs before any verdict that could punt: a punt hands
	// the stage to the LLM skill, which can merge the same head, so the refusal
	// has to come first to be a gate rather than a suggestion (issue 1675).
	if refused := r.refuseGeneratedSteering(ctx, workdir, prNumber, snap); refused != nil {
		ph.supersedeInFlight()
		return finish(*refused, nil)
	}

	// Acceptance-criteria backstop (#2623): before anything can merge or
	// enqueue, demote `Closes #N` when the issue gained criteria the run never
	// reconciled. Placed ahead of the queue branch so a PR resumed while
	// already queued is covered too.
	partial := r.acBackstop(ctx, gh, workdir, issueNumber, prNumber)
	finishBase := finish
	finish = func(res PRMergeResult, err error) (PRMergeResult, error) {
		if partial {
			res.PartialDelivery = true
			res.PartialDeliveryReason = PartialDeliveryACUncovered
		}
		return finishBase(res, err)
	}

	// Merge-queue detection (#2214), once per Run. A read failure keeps the
	// direct path: a branch without a queue must behave exactly as before.
	mq, queueStatus := r.detectMergeQueue(ctx, gh, prNumber, snap)
	if mq != nil && queueStatus.InQueue {
		// A previous attempt already enqueued this PR: the queue owns the
		// merge now, so resume the wait rather than re-deciding it.
		ph.complete("ci-gate")
		ph.skip("freshness-check")
		ph.start("merge")
		return finish(r.awaitMergeQueue(ctx, ph, mq, prNumber, snap, queueStatus), nil)
	}

	// Bounded mergeability wait (#1933). GitHub computes `mergeable`
	// asynchronously and answers UNKNOWN for the first seconds after a PR is
	// created; pr-merge starts seconds after pr-create, so the first snapshot
	// of a pipeline-authored PR routinely has no verdict yet.
	//
	// This runs BEFORE Decide because UNKNOWN is the absence of a verdict, and
	// every downstream test treats it as a negative one: Decide punts on
	// `Mergeable != "MERGEABLE"` at its first test, and MergeBlockedByPendingCI
	// demands MERGEABLE too, so an unresolved value did not merely mis-answer
	// one question — it denied the #297 CI wait its turn entirely, and the LLM
	// path then babysat CI to green at real per-run cost for work this path
	// does for free once the verdict lands.
	//
	// It runs AFTER the steering gate for the reason that gate documents: a
	// refusal must precede anything that can punt, and waiting here would
	// otherwise delay a refusal the runner can already make.
	if snap.State == "OPEN" && snap.Mergeable == github.MergeableUnknown {
		waited, waitErr := r.waitForMergeability(ctx, gh, prNumber, snap)
		if waitErr != nil {
			ph.supersedeInFlight()
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   classifyFetchError(waitErr),
			}, nil)
		}
		snap = waited
		// Still no verdict when the budget expired. Punt — but name the absence
		// rather than reporting a conflict the runner never observed.
		if snap.State == "OPEN" && snap.Mergeable == github.MergeableUnknown {
			ph.supersedeInFlight()
			logPuntSnapshot(prNumber, ReasonMergeabilityUnresolved, snap)
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   ReasonMergeabilityUnresolved,
			}, nil)
		}
	}

	decision := Decide(snap)

	// Bounded CI wait (Issue #297). When the ONLY thing blocking an otherwise
	// mergeable, conflict-free, review-clean PR is in-flight CI, poll until the
	// merge state clears instead of punting to the LLM path. pr-merge starts
	// immediately after pr-create, so on repos whose CI takes minutes (the
	// dogfood workspace ~10 min) the first snapshot is always BLOCKED/UNSTABLE
	// with pending checks — pre-#297 that punted `dirty-merge-state: BLOCKED`
	// on EVERY run and the LLM skill "won" only by babysitting CI, burning
	// real per-run LLM spend for zero engineering work. On a hard blocker
	// emerging mid-wait (a check fails, a conflict/review appears) or on timeout,
	// re-Decide/return the appropriate punt so the LLM path still gets its turn.
	//
	// The predicate also holds for a BLOCKED/UNSTABLE PR with an EMPTY check
	// rollup (#1027): GitHub has not created a check run yet, which is the
	// shape of the very first snapshot on every run. That case is waited for
	// a shorter grace window (ciNoCheckGrace polls) and punts
	// `no-checks-created` if no run ever appears, so a repo without CI does
	// not hold the deterministic path for the full budget.
	if !decision.ShouldMerge && MergeBlockedByPendingCI(snap) {
		waited, outcome, waitErr := r.waitForCleanMergeState(ctx, gh, prNumber, snap)
		if waitErr != nil {
			ph.supersedeInFlight()
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   classifyFetchError(waitErr),
			}, nil)
		}
		snap = waited
		switch outcome {
		case ciWaitTimedOut:
			ph.supersedeInFlight()
			logPuntSnapshot(prNumber, ReasonCIWaitTimeout, snap)
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   withRequiredCheckEvidence(ReasonCIWaitTimeout, snap),
			}, nil)
		case ciWaitNoChecksCreated:
			ph.supersedeInFlight()
			logPuntSnapshot(prNumber, ReasonNoChecksCreated, snap)
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   ReasonNoChecksCreated,
			}, nil)
		}
		decision = Decide(snap)
	}
	ph.complete("ci-gate")

	// freshness-check is the merge-state verdict itself: whether the snapshot
	// the runner is holding is still clean, unconflicted and review-satisfied
	// after whatever waiting happened above.
	ph.start("freshness-check")
	if !decision.ShouldMerge {
		// Already merged or punt path — no merge call.
		path := PathPunt
		headRef := ""
		if snap.State == "MERGED" {
			path = PathMerged
			headRef = snap.HeadRefName
			// Someone else merged it. The verdict was reached, so
			// freshness-check completed; `merge` is a phase this run decided
			// not to perform, which is what `skipped` means.
			ph.complete("freshness-check")
			ph.skip("merge")
			ph.skipOffPath()
		} else {
			ph.supersedeInFlight()
			logPuntSnapshot(prNumber, decision.Reason, snap)
		}
		reason := decision.Reason
		if decision.Punt {
			reason = withRequiredCheckEvidence(reason, snap)
		}
		return finish(PRMergeResult{
			Path:        path,
			PRNumber:    prNumber,
			PRState:     snap.State,
			Reason:      reason,
			HeadRefName: headRef,
		}, nil)
	}

	// Knowledge conformance is the last gate before the merge itself. This
	// is the deterministic path, which is the DEFAULT path — putting the
	// check only in the pr-merge skill would leave it unenforced on every
	// normal run, because the skill is reached only when this runner punts.
	//
	// A violation punts rather than erroring: the LLM path gets its turn and
	// can repair the entry with `knowledge stamp`, which is a better outcome
	// than failing the run outright. What it must not do is merge.
	ph.complete("freshness-check")

	// The conformance gate maps to no registry phase — the pr-merge skill has
	// no knowledge gate of its own — so it runs between spans rather than
	// being filed under a phase name it does not correspond to.
	if r.knowledgeConformance != nil {
		if conf, confErr := r.knowledgeConformance(workdir, issueNumber); confErr == nil && conf != nil && !conf.Valid {
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   fmt.Sprintf("%s: %s", ReasonKnowledgeNonConformant, conformanceSummary(conf)),
			}, nil)
		}
	}

	// Re-check the head immediately before merging: the CI wait above can
	// last minutes, and a push inside it would otherwise land unexamined.
	if refused := r.refuseGeneratedSteering(ctx, workdir, prNumber, snap); refused != nil {
		return finish(*refused, nil)
	}

	// Queue-protected base (#2214): a direct merge would only enqueue (gh)
	// or be rejected (GraphQL), so enqueue and wait for the queue's verdict.
	if mq != nil {
		ph.start("merge")
		return finish(r.awaitMergeQueue(ctx, ph, mq, prNumber, snap, queueStatus), nil)
	}

	// Issue the merge. Idempotent at the GitHub level: re-issuing on an
	// already-merged PR returns a benign error which we tolerate (the
	// re-poll below confirms MERGED).
	ph.start("merge")
	if mergeErr := gh.Merge(ctx, prNumber); mergeErr != nil {
		ph.supersedeInFlight()
		if isRateLimitErr(mergeErr) {
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   ReasonRateLimited,
			}, nil)
		}
		return finish(PRMergeResult{
			Path:     PathPunt,
			PRNumber: prNumber,
			PRState:  snap.State,
			Reason:   fmt.Sprintf("%s: %s", ReasonMergeFailed, truncateErr(mergeErr, 200)),
		}, nil)
	}

	// Re-poll for MERGED with the same EC budget.
	postSnap, postErr := r.fetchWithPolling(ctx, gh, prNumber)
	if postErr != nil {
		ph.supersedeInFlight()
		// Merge call succeeded but post-verification failed — we cannot OBSERVE
		// MERGED, so we must NOT self-report merged. Punt and let the canonical
		// scheduler gate (verifyPRMerged) be the sole MERGED authority (#4070).
		// Self-reporting PathMerged here was a phantom-success risk: a masked
		// fetch failure would have closed the issue on an unconfirmed merge.
		return finish(PRMergeResult{
			Path:     PathPunt,
			PRNumber: prNumber,
			PRState:  postSnap.State,
			Reason:   fmt.Sprintf("%s: %s", ReasonMergeECTimeout, classifyFetchError(postErr)),
		}, nil)
	}
	if postSnap.State == "MERGED" {
		ph.complete("merge")
		// Only here — a merge this runner issued and then OBSERVED — is the
		// stage terminal on the deterministic path, so only here may the
		// LLM-only phases be recorded as a decision not to run (AC5).
		ph.skipOffPath()
		headRef := postSnap.HeadRefName
		if headRef == "" {
			// Some eventual-consistency windows return a leaner payload on the
			// first post-merge snapshot; fall back to the pre-merge snapshot's
			// head ref, which is the same branch by construction.
			headRef = snap.HeadRefName
		}
		return finish(PRMergeResult{
			Path:        PathMerged,
			PRNumber:    prNumber,
			PRState:     "MERGED",
			Reason:      ReasonCleanMerged,
			HeadRefName: headRef,
		}, nil)
	}

	// EC budget exhausted without observing MERGED — the merge call returned
	// non-error but we cannot confirm the PR reached MERGED state. Punt so
	// the scheduler does not proceed to EvaluatePostMerge and close the issue
	// on an unconfirmed merge. The autonomous scheduler will retry on the next
	// tick when the PR's state becomes observable.
	ph.supersedeInFlight()
	return finish(PRMergeResult{
		Path:     PathPunt,
		PRNumber: prNumber,
		PRState:  postSnap.State,
		Reason:   ReasonMergeECTimeout,
	}, nil)
}

// detectMergeQueue reports whether the PR's base branch requires a merge
// queue (#2214). It returns a nil client when the ghClient cannot drive a
// queue, the PR is not OPEN, detection failed, or no queue is required — all
// of which keep today's direct-merge path.
func (r *DeterministicRunner) detectMergeQueue(ctx context.Context, gh ghClient, prNumber int, snap PRViewSnapshot) (mergeQueueClient, github.MergeQueueStatus) {
	mq, ok := gh.(mergeQueueClient)
	if !ok || snap.State != "OPEN" {
		return nil, github.MergeQueueStatus{}
	}
	st, err := mq.MergeQueueStatus(ctx, prNumber)
	if err != nil {
		log.Printf("pr-merge: merge-queue detection failed for PR #%d, using the direct merge path: %v", prNumber, err)
		return nil, github.MergeQueueStatus{}
	}
	if !st.QueueRequired && !st.InQueue {
		return nil, github.MergeQueueStatus{}
	}
	return mq, st
}

// awaitMergeQueue enqueues the PR (unless already queued) and waits for the
// queue's outcome (#2214). MERGED → PathMerged. Every other outcome is
// PathRefused: a failed merge group is a CI failure on the merge-group
// commit, and a queue wait is never handed to the LLM skill.
func (r *DeterministicRunner) awaitMergeQueue(ctx context.Context, ph *phaseEmitter, mq mergeQueueClient, prNumber int, snap PRViewSnapshot, st github.MergeQueueStatus) PRMergeResult {
	log.Printf("pr-merge: PR #%d targets %s, which requires a merge queue — enqueueing and waiting up to %s",
		prNumber, st.BaseRef, r.queueWaitTimeout())
	final, err := mq.EnqueueAndWait(ctx, st, r.queueWait)
	if err == nil && final.State == "MERGED" {
		ph.complete("merge")
		ph.skipOffPath()
		return PRMergeResult{
			Path:        PathMerged,
			PRNumber:    prNumber,
			PRState:     "MERGED",
			Reason:      ReasonMergeQueueMerged,
			HeadRefName: snap.HeadRefName,
		}
	}
	ph.supersedeInFlight()
	state := final.State
	if state == "" {
		state = snap.State
	}
	res := PRMergeResult{Path: PathRefused, PRNumber: prNumber, PRState: state}
	var qe *github.MergeQueueError
	switch {
	case err == nil:
		res.Reason = fmt.Sprintf("%s: queue wait ended in state %s", ReasonMergeQueueFailed, final.State)
	case isRateLimitErr(err):
		// Rate limits defer, exactly as on the direct path.
		res.Path = PathPunt
		res.Reason = ReasonRateLimited
	case errors.As(err, &qe) && qe.Kind == github.MergeQueueTimeout:
		res.Reason = fmt.Sprintf("%s: %s", ReasonMergeQueueTimeout, truncateErr(err, 300))
	case errors.As(err, &qe) && qe.Kind == github.MergeQueueUnmergeable:
		if base, mergeState, ok := r.queueConflict(ctx, mq, prNumber, st); ok {
			res.Reason = fmt.Sprintf("%s: %s", ReasonMergeQueueFailed, MergeQueueConflictReason(base, prNumber, mergeState))
		} else {
			res.Reason = fmt.Sprintf("%s: %s", ReasonMergeQueueFailed, truncateErr(err, 500))
		}
	case errors.As(err, &qe):
		res.Reason = fmt.Sprintf("%s: %s", ReasonMergeQueueFailed, truncateErr(err, 500))
	default:
		res.Reason = fmt.Sprintf("%s: %s", ReasonMergeQueueEnqueueFailed, truncateErr(err, 300))
	}
	return res
}

// MergeQueueConflictReason names a queue-side conflict (#2611). pr-merge
// emits it and conflict recovery matches on MergeQueueConflictMarker, so
// both sides share this one builder.
func MergeQueueConflictReason(base string, prNumber int, mergeState string) string {
	return fmt.Sprintf("%s %s: PR #%d is CONFLICTING (mergeStateStatus %s)", MergeQueueConflictMarker, base, prNumber, mergeState)
}

// queueConflict re-reads an unmergeable PR's mergeability, polling while
// GitHub reports UNKNOWN within the runner's mergeability budget. It reports
// ok only when the PR reads CONFLICTING; anything else keeps the queue's
// own reason (#2611).
func (r *DeterministicRunner) queueConflict(ctx context.Context, mq mergeQueueClient, prNumber int, prior github.MergeQueueStatus) (base, mergeState string, ok bool) {
	for poll := 0; poll < r.mergeabilityPollMax; poll++ {
		if poll > 0 {
			select {
			case <-ctx.Done():
				return "", "", false
			case <-time.After(r.mergeabilityPollInterval):
			}
		}
		st, err := mq.MergeQueueStatus(ctx, prNumber)
		if err != nil {
			return "", "", false
		}
		if st.Mergeable == github.MergeableUnknown {
			continue
		}
		if st.Mergeable != "CONFLICTING" {
			return "", "", false
		}
		base = st.BaseRef
		if base == "" {
			base = prior.BaseRef
		}
		if base == "" {
			base = "main"
		}
		return base, st.MergeStateStatus, true
	}
	return "", "", false
}

func (r *DeterministicRunner) queueWaitTimeout() time.Duration {
	if r.queueWait.Timeout > 0 {
		return r.queueWait.Timeout
	}
	return github.DefaultMergeQueueWaitTimeout
}

// acBackstop re-reads the issue body and, when the PR body still closes the
// issue while an unchecked criterion is absent from ac-reconcile-{N}.json,
// rewrites the body to `Refs #N` listing those criteria and records
// closes_issue:false in pr-{N}.json (#2623). Returns true when it demoted the
// PR. A client without prBodyClient, or a failed read, leaves the body as
// pr-create wrote it: an unreadable body is no evidence of a missing
// criterion.
func (r *DeterministicRunner) acBackstop(ctx context.Context, gh ghClient, workdir string, issueNumber, prNumber int) bool {
	bc, ok := gh.(prBodyClient)
	if !ok {
		return false
	}
	prBody, err := bc.PRBody(ctx, prNumber)
	if err != nil {
		log.Printf("pr-merge: AC backstop skipped for PR #%d: read PR body: %v", prNumber, err)
		return false
	}
	repo := readPRContextRepo(workdir, issueNumber)
	if repo == "" {
		log.Printf("pr-merge: AC backstop for #%d cannot read the repository from pr-%d.json pr_url; only bare #%d closers count", issueNumber, issueNumber, issueNumber)
	}
	if !BodyClosesIssue(prBody, repo, issueNumber) {
		return false
	}
	issueBody, err := bc.IssueBody(ctx, issueNumber)
	if err != nil {
		log.Printf("pr-merge: AC backstop skipped for #%d: read issue body: %v", issueNumber, err)
		return false
	}
	var rec *preflight.ACReconcileResult
	if data, readErr := readFile(pipelineContextPath(workdir, fmt.Sprintf("ac-reconcile-%d.json", issueNumber))); readErr == nil {
		var parsed preflight.ACReconcileResult
		if json.Unmarshal(data, &parsed) == nil {
			rec = &parsed
		}
	}
	uncovered := preflight.UncoveredACs(issueBody, rec)
	if len(uncovered) == 0 {
		return false
	}
	newBody := AppendUnmetACs(RewriteClosesToRefs(prBody, repo, issueNumber), issueNumber, uncovered)
	if err := bc.EditPRBody(ctx, prNumber, newBody); err != nil {
		log.Printf("pr-merge: AC backstop could not edit PR #%d body: %v", prNumber, err)
		return false
	}
	log.Printf("pr-merge: #%d has %d acceptance criteria this run never reconciled — PR #%d now says Refs #%d",
		issueNumber, len(uncovered), prNumber, issueNumber)
	if err := markPRContextPartial(workdir, issueNumber); err != nil {
		log.Printf("pr-merge: could not record closes_issue:false for #%d: %v", issueNumber, err)
	}
	return true
}

// markPRContextPartial sets closes_issue:false in pr-{N}.json, keeping every
// other field as written.
func markPRContextPartial(workdir string, issueNumber int) error {
	p := pipelineContextPath(workdir, fmt.Sprintf("pr-%d.json", issueNumber))
	data, err := readFile(p)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	doc["closes_issue"] = false
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(out, '\n'), 0o644)
}

// refuseGeneratedSteering is the steering gate (issue 1675). It returns a
// PathRefused result when an AGENTS.md at the PR head carries the managed
// steering block, nil otherwise. With a local worktree on the head branch the
// block is repaired and the repair pushed first, so the next attempt can merge
// once required checks re-run on the new head; without one the operator is
// told the exact command. A head that cannot be inspected is logged and
// passed: the pr-create push and the post-stage repair are the other layers.
func (r *DeterministicRunner) refuseGeneratedSteering(ctx context.Context, workdir string, prNumber int, snap PRViewSnapshot) *PRMergeResult {
	if r.steeringGate == nil || snap.State != "OPEN" || snap.HeadRefName == "" {
		return nil
	}
	v, err := r.steeringGate(ctx, workdir, snap.HeadRefName)
	if err != nil {
		log.Printf("pr-merge: could not inspect PR #%d head %s for generated steering (not blocking): %v",
			prNumber, snap.HeadRefName, err)
		return nil
	}
	if len(v.Leaked) == 0 {
		return nil
	}
	head := codexprovision.ShortSHA(v.HeadSHA)
	files := strings.Join(v.Leaked, ", ")
	var detail string
	switch {
	case v.Pushed:
		detail = fmt.Sprintf("PR head %s carries generated Nightgauge steering in %s; the repair %s was pushed to %s, so required checks must re-run before the merge is retried",
			head, files, codexprovision.ShortSHA(v.RepairSHA), snap.HeadRefName)
	case v.Worktree != "":
		detail = fmt.Sprintf("PR head %s carries generated Nightgauge steering in %s and the repair could not be published from %s (%v); run 'nightgauge preflight managed-steering --fix' there, commit and push, then retry",
			head, files, v.Worktree, v.PushErr)
	default:
		detail = fmt.Sprintf("PR head %s carries generated Nightgauge steering in %s and no local worktree is on %s; run 'nightgauge preflight managed-steering --fix' on that branch, commit and push, then retry",
			head, files, snap.HeadRefName)
	}
	return &PRMergeResult{
		Path:     PathRefused,
		PRNumber: prNumber,
		PRState:  snap.State,
		Reason:   ReasonGeneratedSteering + ": " + detail,
	}
}

// conformanceSummary renders the offending entries compactly enough for a
// punt reason, naming at most three so the log line stays readable.
func conformanceSummary(conf *knowledge.ConformanceResult) string {
	const maxNamed = 3
	names := make([]string, 0, maxNamed)
	for i, v := range conf.Violations {
		if i == maxNamed {
			names = append(names, fmt.Sprintf("+%d more", len(conf.Violations)-maxNamed))
			break
		}
		names = append(names, fmt.Sprintf("%s (%s)", v.Path, v.Reason))
	}
	return strings.Join(names, ", ")
}

// fetchWithPolling calls gh.View up to pollMax times with pollInterval between
// attempts, returning early once state is MERGED.
func (r *DeterministicRunner) fetchWithPolling(ctx context.Context, gh ghClient, prNumber int) (PRViewSnapshot, error) {
	var last PRViewSnapshot
	var lastErr error
	for poll := 0; poll < r.pollMax; poll++ {
		snap, err := gh.View(ctx, prNumber)
		if err == nil {
			last = snap
			if snap.State == "MERGED" {
				return snap, nil
			}
		} else {
			lastErr = err
			// Rate-limit errors are not retryable inside the deterministic path
			// (#3020 / ADR-004) — surface immediately.
			if isRateLimitErr(err) {
				return PRViewSnapshot{}, err
			}
		}
		if poll == r.pollMax-1 {
			break
		}
		select {
		case <-ctx.Done():
			return PRViewSnapshot{}, ctx.Err()
		case <-time.After(r.pollInterval):
		}
	}
	if lastErr != nil && last.State == "" {
		return PRViewSnapshot{}, lastErr
	}
	return last, nil
}

// waitForMergeability polls until GitHub has computed the PR's `mergeable`
// field, or the bounded budget expires (#1933).
//
// Returns the last snapshot seen either way; the caller decides what an
// unresolved verdict means. It stops early on any resolution — including the
// PR leaving OPEN — because a MERGED or CLOSED PR has a verdict of its own that
// Decide already handles, and continuing to poll would only delay it.
//
// A transient fetch error keeps the wait alive: the budget, not the first bad
// response, is what bounds this. Rate limits are the exception, surfaced
// immediately, matching fetchWithPolling and waitForCleanMergeState
// (#3020 / ADR-004) — retrying a rate limit inside the deterministic path is
// how the path spends its budget earning nothing.
func (r *DeterministicRunner) waitForMergeability(ctx context.Context, gh ghClient, prNumber int, initial PRViewSnapshot) (PRViewSnapshot, error) {
	last := initial
	for poll := 0; poll < r.mergeabilityPollMax; poll++ {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(r.mergeabilityPollInterval):
		}
		snap, err := gh.View(ctx, prNumber)
		if err != nil {
			if isRateLimitErr(err) {
				return last, err
			}
			continue
		}
		last = snap
		if snap.State != "OPEN" || snap.Mergeable != github.MergeableUnknown {
			return snap, nil
		}
	}
	return last, nil
}

// MergeBlockedByPendingCI reports whether the ONLY thing preventing a clean
// merge is CI that has not finished — or not started — yet (Issue #297,
// #1027): the PR is OPEN and MERGEABLE (no conflict), no check has reported
// FAILURE/ERROR, review is not blocking, the merge state is BLOCKED or UNSTABLE
// (required/optional checks not yet green), and either at least one check is
// still pending, NO check run has been created at all, or a known required
// context is absent from the rollup or reported SKIPPED (#1891). Such a PR is expected
// to become CLEAN once CI finishes, so the runner waits rather than punting.
// Hard blockers (conflict → DIRTY/CONFLICTING, review required, a failed check,
// BEHIND/DRAFT, or BLOCKED with every listed check concluded and every known
// required context reported) return false:
// those will not self-resolve by waiting, so the LLM path should get its turn
// immediately.
//
// Zero checks is "pending", not "structural" (#1027): pr-merge starts seconds
// after pr-create, before GitHub has created any check run, so the first
// snapshot of nearly every run is BLOCKED with an empty rollup. Reading that
// as `dirty-merge-state: BLOCKED` punted the deterministic path on the common
// case and — through attention.raise — carded in-flight CI as a
// branch-protection block. The predicate cannot tell "not created yet" from
// "no CI will ever run" on one snapshot; that distinction is temporal, and
// DeterministicRunner bounds it with a grace window (ciNoCheckGrace) that
// punts ReasonNoChecksCreated when no run appears.
//
// EXPORTED for the attention.raise verb (#305). Decide() alone cannot see
// pending CI — the interception lives OUT here, in DeterministicRunner.Run,
// which is why a caller that classifies with bare Decide() reads a
// still-running required check as `dirty-merge-state: BLOCKED` and cards it as
// branch protection. Any surface that asks "is this block human-needed?" must
// gate on THIS predicate over the same snapshot, not re-derive a third copy of
// the matrix.
func MergeBlockedByPendingCI(snap PRViewSnapshot) bool {
	if snap.State != "OPEN" {
		return false
	}
	if snap.Mergeable != "MERGEABLE" {
		return false
	}
	// Already clean — nothing to wait for; Decide() will merge.
	if snap.MergeStateStatus == "CLEAN" {
		return false
	}
	// Only BLOCKED/UNSTABLE are "waiting on checks". DIRTY (conflict), BEHIND,
	// DRAFT, HAS_HOOKS, UNKNOWN are structural and will not clear via CI.
	if snap.MergeStateStatus != "BLOCKED" && snap.MergeStateStatus != "UNSTABLE" {
		return false
	}
	// A blocking review will not resolve by waiting on CI.
	if snap.ReviewDecision == "REVIEW_REQUIRED" || snap.ReviewDecision == "CHANGES_REQUESTED" {
		return false
	}
	// No check run created yet — CI has not started. Waiting is the only
	// thing that can tell this apart from a repo with no CI, and the runner
	// bounds that wait (#1027).
	if len(snap.StatusCheckRollup) == 0 {
		return true
	}
	// Any already-failed check is a hard blocker — do not wait.
	sawPending := false
	for _, c := range snap.StatusCheckRollup {
		switch c.Conclusion {
		case "FAILURE", "ERROR":
			return false
		case "", "PENDING":
			sawPending = true
		}
	}
	if sawPending {
		return true
	}
	// Nothing listed is in flight. Before #1891 that alone read as a non-CI
	// block ("every listed check concluded green and the ruleset still says
	// BLOCKED") and punted at once — but a required context that has not
	// reported yet (absent from the rollup) or reported SKIPPED keeps the PR
	// BLOCKED with nothing pending, and that does clear on its own: on the
	// #1889 run the LLM arm merged it 33 minutes later with no intervention,
	// at $6.74 of waiting. When the required set is known and has such a gap,
	// keep waiting; the runner's budget bounds it and punts ci-wait-timeout
	// if it never clears. With no gap (or no known required set) the block is
	// not CI's, so punt immediately as before.
	absent, skipped, _ := RequiredCheckGaps(snap)
	return len(absent) > 0 || len(skipped) > 0
}

// ciWaitOutcome is how waitForCleanMergeState ended.
type ciWaitOutcome int

const (
	// ciWaitResolved — the merge state resolved (CLEAN and mergeable, or a hard
	// blocker/merge emerged); the caller re-runs Decide.
	ciWaitResolved ciWaitOutcome = iota
	// ciWaitTimedOut — the budget was exhausted with CI still pending (caller
	// punts ReasonCIWaitTimeout so the LLM path runs).
	ciWaitTimedOut
	// ciWaitNoChecksCreated — the no-check grace window expired and GitHub
	// still had not created a single check run (caller punts
	// ReasonNoChecksCreated) (#1027).
	ciWaitNoChecksCreated
)

// waitForCleanMergeState polls the PR up to ciPollMax times (ciPollInterval
// apart) while it remains blocked solely by pending CI (Issue #297). It returns
// the last observed snapshot, how the wait ended, and a non-nil error only for
// a non-retryable fetch error (e.g. rate limit).
//
// While the check rollup is EMPTY the wait is capped at ciNoCheckGrace polls
// (#1027): a check run appearing within the window promotes the wait to the
// full budget; none appearing ends it with ciWaitNoChecksCreated. The grace is
// a prefix of ciPollMax, so a grace larger than the budget degrades to the
// plain timeout.
//
// The initial snapshot is passed so a runner configured with ciPollMax==0 (or a
// PR that resolves on the first re-poll) degrades gracefully.
func (r *DeterministicRunner) waitForCleanMergeState(ctx context.Context, gh ghClient, prNumber int, initial PRViewSnapshot) (PRViewSnapshot, ciWaitOutcome, error) {
	last := initial
	// Polls that observed an empty rollup, counted only while no check has
	// been seen yet. Once a run exists the ordinary budget governs.
	emptyPolls := 0
	sawCheck := len(initial.StatusCheckRollup) > 0
	for poll := 0; poll < r.ciPollMax; poll++ {
		if !sawCheck && emptyPolls >= r.ciNoCheckGrace {
			return last, ciWaitNoChecksCreated, nil
		}
		select {
		case <-ctx.Done():
			return last, ciWaitResolved, ctx.Err()
		case <-time.After(r.ciPollInterval):
		}
		snap, err := gh.View(ctx, prNumber)
		if err != nil {
			// Rate-limit errors are not retryable inside the deterministic path
			// (#3020 / ADR-004) — surface immediately.
			if isRateLimitErr(err) {
				return last, ciWaitResolved, err
			}
			// Transient fetch error — keep waiting until the budget expires.
			continue
		}
		last = snap
		// Resolved one way or another: either now mergeable/clean or a hard
		// blocker (failed check, conflict, review, merged) has appeared.
		if !MergeBlockedByPendingCI(snap) {
			return snap, ciWaitResolved, nil
		}
		if len(snap.StatusCheckRollup) > 0 {
			sawCheck = true
		} else {
			emptyPolls++
		}
	}
	if !sawCheck && emptyPolls >= r.ciNoCheckGrace {
		return last, ciWaitNoChecksCreated, nil
	}
	return last, ciWaitTimedOut, nil
}

// readPRContextNumber reads .git/nightgauge/pipeline/pr-{N}.json and returns
// pr_number. Default ctxReader for production runs.
func readPRContextNumber(workdir string, issueNumber int) (int, error) {
	prContextPath := pipelineContextPath(workdir, fmt.Sprintf("pr-%d.json", issueNumber))
	data, err := readFile(prContextPath)
	if err != nil {
		return 0, fmt.Errorf("read pr context: %w", err)
	}
	var ctx struct {
		PRNumber int `json:"pr_number"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return 0, fmt.Errorf("parse pr context: %w", err)
	}
	if ctx.PRNumber <= 0 {
		return 0, errPRNumberMissing
	}
	return ctx.PRNumber, nil
}

var errPRNumberMissing = errors.New("pr context: pr_number is missing or zero")

// prURLRepoRE captures the "owner/repo" of a pull request URL.
var prURLRepoRE = regexp.MustCompile(`^https?://[^\s/]+/([\w.-]+/[\w.-]+)/pull/\d+`)

// readPRContextRepo returns the "owner/repo" of pr_url in pr-{N}.json, the
// repository the PR merges into, or "" when it cannot be read. The AC
// backstop scopes closing keywords to it: `Closes other/repo#N` does not
// close this repository's #N (#2687).
func readPRContextRepo(workdir string, issueNumber int) string {
	data, err := readFile(pipelineContextPath(workdir, fmt.Sprintf("pr-%d.json", issueNumber)))
	if err != nil {
		return ""
	}
	var ctx struct {
		PRURL string `json:"pr_url"`
	}
	if json.Unmarshal(data, &ctx) != nil {
		return ""
	}
	if m := prURLRepoRE.FindStringSubmatch(ctx.PRURL); m != nil {
		return m[1]
	}
	return ""
}

// classifyContextError maps a pr-context read error to a Reason constant.
func classifyContextError(err error) string {
	if err == nil {
		return ReasonNoPRContext
	}
	msg := err.Error()
	switch {
	case errors.Is(err, errPRNumberMissing):
		return ReasonNoPRNumber
	case strings.Contains(msg, "parse pr context"):
		return ReasonInvalidPRJSON
	default:
		return ReasonNoPRContext
	}
}

// classifyFetchError maps a `gh pr view` error to a Reason constant.
func classifyFetchError(err error) string {
	if err == nil {
		return ""
	}
	if isRateLimitErr(err) {
		return ReasonRateLimited
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "gh: command not found") || strings.Contains(msg, "executable file not found") {
		return ReasonGHUnavailable
	}
	return fmt.Sprintf("%s: %s", ReasonUnexpected, truncateErr(err, 200))
}

// isRateLimitErr matches the canonical rate-limit phrases used by gh CLI and
// the GitHub REST API. Mirrors internal/intelligence/failure/taxonomy.go's
// detection to keep classifications consistent.
func isRateLimitErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "secondary rate") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "quota exceeded")
}

// truncateErr returns at most max bytes of err.Error() so a long stderr
// payload doesn't blow up the run record.
func truncateErr(err error, max int) string {
	s := err.Error()
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// `gh` shell-out client.
// ---------------------------------------------------------------------------

// workdirBoundClient is implemented by ghClients that produce a copy bound to
// a specific working directory. The exec-backed client supports this; tests
// typically don't need to.
type workdirBoundClient interface {
	withWorkdir(dir string) ghClient
}

// execGhClient calls `gh` via os/exec.
type execGhClient struct {
	workdir string
}

func (c *execGhClient) withWorkdir(dir string) ghClient {
	return &execGhClient{workdir: dir}
}

func (c *execGhClient) View(ctx context.Context, prNumber int) (PRViewSnapshot, error) {
	stdout, err := github.RunGhSubprocess(ctx, c.workdir,
		"pr", "view", fmt.Sprintf("%d", prNumber),
		"--json", "state,statusCheckRollup,mergeable,mergeStateStatus,reviewDecision,headRefName,baseRefName")
	if err != nil {
		return PRViewSnapshot{}, normalizeGhError(err)
	}
	var raw struct {
		State             string `json:"state"`
		Mergeable         string `json:"mergeable"`
		MergeStateStatus  string `json:"mergeStateStatus"`
		ReviewDecision    string `json:"reviewDecision"`
		HeadRefName       string `json:"headRefName"`
		BaseRefName       string `json:"baseRefName"`
		StatusCheckRollup []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return PRViewSnapshot{}, fmt.Errorf("parse gh pr view JSON: %w", err)
	}
	snap := PRViewSnapshot{
		State:            raw.State,
		Mergeable:        raw.Mergeable,
		MergeStateStatus: raw.MergeStateStatus,
		ReviewDecision:   raw.ReviewDecision,
		HeadRefName:      raw.HeadRefName,
	}
	for _, row := range raw.StatusCheckRollup {
		snap.StatusCheckRollup = append(snap.StatusCheckRollup, PRStatusCheckRow{
			Name:       row.Name,
			Conclusion: row.Conclusion,
		})
	}
	// The required set is read only when it can change the verdict: an OPEN
	// PR that GitHub still holds BLOCKED/UNSTABLE (#1891). A read failure
	// leaves it nil, which is the pre-#1891 behaviour (punt immediately on
	// "nothing pending"), so an unreadable ruleset can only cost LLM spend,
	// never a merge.
	if snap.State == "OPEN" && raw.BaseRefName != "" &&
		(snap.MergeStateStatus == "BLOCKED" || snap.MergeStateStatus == "UNSTABLE") {
		required, rerr := c.requiredChecks(ctx, raw.BaseRefName)
		if rerr != nil {
			log.Printf("pr-merge: could not read required checks for base %q: %v", raw.BaseRefName, rerr)
		}
		snap.RequiredChecks = required
	}
	return snap, nil
}

// requiredChecks returns the required status-check contexts on base: the
// rulesets' required_status_checks rules, plus classic branch protection when
// the token can read it (that endpoint needs admin, so its failure contributes
// nothing — the same union github.CIService.GetRequiredCheckNames computes).
// `{owner}/{repo}` is resolved by gh from the working directory's remote.
func (c *execGhClient) requiredChecks(ctx context.Context, base string) ([]string, error) {
	escaped := url.PathEscape(base)
	out, err := github.RunGhSubprocess(ctx, c.workdir, "api", "--paginate",
		"repos/{owner}/{repo}/rules/branches/"+escaped,
		"--jq", `.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context`)
	if err != nil {
		return nil, normalizeGhError(err)
	}
	names := splitContexts(out, nil)
	if classic, cerr := github.RunGhSubprocess(ctx, c.workdir, "api",
		"repos/{owner}/{repo}/branches/"+escaped+"/protection/required_status_checks",
		"--jq", `.contexts[]`); cerr == nil {
		names = splitContexts(classic, names)
	}
	return names, nil
}

// splitContexts appends the non-empty, not-yet-seen lines of out to into.
func splitContexts(out []byte, into []string) []string {
	seen := make(map[string]bool, len(into))
	for _, n := range into {
		seen[n] = true
	}
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		into = append(into, name)
	}
	return into
}

// Merge issues a server-side squash merge via `gh pr merge --squash`.
//
// Deliberately NOT `--delete-branch` (Issue #589). Every pipeline run
// executes from a linked worktree checked out on the PR's own head branch.
// `gh pr merge --delete-branch` merges via the API (fine, no local git
// needed) and THEN, because it can't delete the branch out from under the
// caller's own checkout, switches the LOCAL repo to the base branch first —
// i.e. it shells `git checkout main` in cwd. In a linked worktree that
// collides with the primary checkout, which already holds `main`
// (`fatal: 'main' is already used by worktree at '<primary checkout>'`), and
// gh exits non-zero even though the merge itself already landed. That
// turned every worktree-mode merge into a punt to the LLM path, which then
// discovered the PR was already merged and did nothing but spend tokens
// confirming it.
//
// Branch cleanup is not lost by dropping the flag — it just moves to callers
// that can do it without a checkout: DeterministicRunner reports the merged
// PR's HeadRefName so the orchestrator can delete the remote branch through
// the common-dir-aware git service (a plain push-based ref deletion, which
// never needs any branch checked out), and the LOCAL branch/worktree is
// reclaimed by the post-merge worktree sweep once the worktree is no longer
// active.
func (c *execGhClient) Merge(ctx context.Context, prNumber int) error {
	_, err := github.RunGhSubprocess(ctx, c.workdir,
		"pr", "merge", fmt.Sprintf("%d", prNumber), "--squash")
	if err != nil {
		return normalizeGhError(err)
	}
	return nil
}

// PRBody implements prBodyClient.
func (c *execGhClient) PRBody(ctx context.Context, prNumber int) (string, error) {
	return c.viewBody(ctx, "pr", prNumber)
}

// IssueBody implements prBodyClient.
func (c *execGhClient) IssueBody(ctx context.Context, issueNumber int) (string, error) {
	return c.viewBody(ctx, "issue", issueNumber)
}

func (c *execGhClient) viewBody(ctx context.Context, kind string, number int) (string, error) {
	stdout, err := github.RunGhSubprocess(ctx, c.workdir, kind, "view", strconv.Itoa(number), "--json", "body")
	if err != nil {
		return "", normalizeGhError(err)
	}
	var raw struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return "", fmt.Errorf("parse gh %s view JSON: %w", kind, err)
	}
	return raw.Body, nil
}

// EditPRBody implements prBodyClient. The body goes through a temp file so
// its size and content never touch the argument list.
func (c *execGhClient) EditPRBody(ctx context.Context, prNumber int, body string) error {
	f, err := os.CreateTemp("", "nightgauge-pr-body-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := github.RunGhSubprocess(ctx, c.workdir, "pr", "edit", strconv.Itoa(prNumber), "--body-file", f.Name()); err != nil {
		return normalizeGhError(err)
	}
	return nil
}

// MergeQueueStatus implements mergeQueueClient over `gh api graphql`, with
// the repository resolved by gh from the working directory.
func (c *execGhClient) MergeQueueStatus(ctx context.Context, prNumber int) (github.MergeQueueStatus, error) {
	return github.NewMergeQueue(c.graphQL).StatusByNumber(ctx, "{owner}", "{repo}", prNumber)
}

// EnqueueAndWait implements mergeQueueClient: the GraphQL enqueuePullRequest
// mutation, then a bounded poll of the PR's queue entry (#2214).
func (c *execGhClient) EnqueueAndWait(ctx context.Context, st github.MergeQueueStatus, opts github.MergeQueueWaitOptions) (github.MergeQueueStatus, error) {
	return github.NewMergeQueue(c.graphQL).EnqueueAndWait(ctx, st, opts)
}

// graphQL is the github.GraphQLDoer for `gh api graphql`. String variables
// travel as -f (raw) except gh's {owner}/{repo} placeholders, which need -F
// to be substituted; integers travel as -F so they stay typed.
func (c *execGhClient) graphQL(ctx context.Context, query string, vars map[string]any) ([]byte, error) {
	args := []string{"api", "graphql", "-f", "query=" + query}
	for k, v := range vars {
		switch val := v.(type) {
		case int:
			args = append(args, "-F", fmt.Sprintf("%s=%d", k, val))
		case string:
			if val == "{owner}" || val == "{repo}" {
				args = append(args, "-F", k+"="+val)
			} else {
				args = append(args, "-f", k+"="+val)
			}
		default:
			args = append(args, "-f", fmt.Sprintf("%s=%v", k, val))
		}
	}
	out, err := github.RunGhSubprocess(ctx, c.workdir, args...)
	if err != nil {
		// gh prints the GraphQL error body on stdout; keep it for classification.
		if len(out) > 0 {
			return nil, fmt.Errorf("%w: %s", normalizeGhError(err), strings.TrimSpace(string(out)))
		}
		return nil, normalizeGhError(err)
	}
	return out, nil
}

// normalizeGhError attaches captured stderr (when present) to the returned
// error so the caller's truncateErr / isRateLimitErr can inspect it.
func normalizeGhError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

// readFile is a package-level indirection so tests can stub the file read.
var readFile = osReadFile
