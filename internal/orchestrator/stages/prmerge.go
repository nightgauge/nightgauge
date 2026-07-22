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
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PRMergePath is the outcome of a deterministic pr-merge attempt.
//
//   - PathMerged — the PR is merged (either was already, or this runner
//     issued the merge and re-verified MERGED). Scheduler skips the LLM skill.
//   - PathPunt   — the runner declined to merge for a reason that requires
//     human or LLM judgment (real conflict, failed CI, missing review,
//     rate-limit, unexpected error). Scheduler falls through to the existing
//     LLM skill path.
type PRMergePath string

const (
	PathMerged PRMergePath = "merged"
	PathPunt   PRMergePath = "punt"
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
)

// CI-wait budget for the deterministic pr-merge path (Issue #297). When the
// ONLY thing blocking an otherwise-clean, mergeable PR is in-flight CI, the
// runner polls until the merge state clears rather than punting to the LLM
// skill — which historically "won" pr-merge purely by babysitting CI for ~10
// minutes at ~$3–4.44/run. Bounded so a genuinely stuck PR still falls through
// to the LLM path within a predictable budget (30 × 30s = 15 min).
const (
	DefaultCIPollInterval = 30 * time.Second
	DefaultCIPollMax      = 30
)

// PRMergeResult is the outcome of a single Run invocation.
type PRMergeResult struct {
	Path       PRMergePath
	PRNumber   int
	PRState    string
	Reason     string
	DurationMs int64
}

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
// Decision matrix:
//
//	state == MERGED                                       → ShouldMerge=false (already merged; runner returns merged)
//	state == OPEN && MERGEABLE && CLEAN
//	  && no FAILURE/ERROR checks
//	  && review not blocking                              → ShouldMerge=true
//	state == OPEN && (CONFLICTING | DIRTY | BLOCKED ...)  → Punt
//	state == OPEN && any failed check                     → Punt (failed CI)
//	state == OPEN && REVIEW_REQUIRED|CHANGES_REQUESTED    → Punt
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

	if snap.MergeStateStatus != "CLEAN" {
		return MergeDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonDirtyState, snap.MergeStateStatus)}
	}

	for _, c := range snap.StatusCheckRollup {
		if c.Conclusion == "FAILURE" || c.Conclusion == "ERROR" {
			return MergeDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonFailedChecks, c.Name)}
		}
	}

	if snap.ReviewDecision == "REVIEW_REQUIRED" || snap.ReviewDecision == "CHANGES_REQUESTED" {
		return MergeDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonReviewMissing, snap.ReviewDecision)}
	}

	return MergeDecision{ShouldMerge: true, Reason: ReasonCleanMerged}
}

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
	now            func() time.Time
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
		ciPollInterval: DefaultCIPollInterval,
		ciPollMax:      DefaultCIPollMax,
		now:            time.Now,
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
	if wb, ok := r.gh.(workdirBoundClient); ok {
		r.gh = wb.withWorkdir(workdir)
	}

	prNumber, err := r.prContextRead(workdir, issueNumber)
	if err != nil {
		// No pr-{N}.json or unreadable. Punt — the LLM skill path can decide
		// whether to author the PR or fail the run. Don't silently treat as
		// merged: a missing PR context is structurally unexpected at this
		// stage.
		return finish(PRMergeResult{
			Path:   PathPunt,
			Reason: classifyContextError(err),
		}, nil)
	}

	// Pre-flight + eventual-consistency polling.
	snap, fetchErr := r.fetchWithPolling(ctx, prNumber)
	if fetchErr != nil {
		return finish(PRMergeResult{
			Path:     PathPunt,
			PRNumber: prNumber,
			Reason:   classifyFetchError(fetchErr),
		}, nil)
	}

	decision := Decide(snap)

	// Bounded CI wait (Issue #297). When the ONLY thing blocking an otherwise
	// mergeable, conflict-free, review-clean PR is in-flight CI, poll until the
	// merge state clears instead of punting to the LLM path. pr-merge starts
	// immediately after pr-create, so on repos whose CI takes minutes (bowlsheet
	// ~10 min) the first snapshot is always BLOCKED/UNSTABLE with pending checks
	// — pre-#297 that punted `dirty-merge-state: BLOCKED` on EVERY run and the
	// LLM skill "won" only by babysitting CI at ~$3–4.44/run. On a hard blocker
	// emerging mid-wait (a check fails, a conflict/review appears) or on timeout,
	// re-Decide/return the appropriate punt so the LLM path still gets its turn.
	if !decision.ShouldMerge && mergeBlockedByPendingCI(snap) {
		waited, timedOut, waitErr := r.waitForCleanMergeState(ctx, prNumber, snap)
		if waitErr != nil {
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   classifyFetchError(waitErr),
			}, nil)
		}
		snap = waited
		if timedOut {
			return finish(PRMergeResult{
				Path:     PathPunt,
				PRNumber: prNumber,
				PRState:  snap.State,
				Reason:   ReasonCIWaitTimeout,
			}, nil)
		}
		decision = Decide(snap)
	}

	if !decision.ShouldMerge {
		// Already merged or punt path — no merge call.
		path := PathPunt
		if snap.State == "MERGED" {
			path = PathMerged
		}
		return finish(PRMergeResult{
			Path:     path,
			PRNumber: prNumber,
			PRState:  snap.State,
			Reason:   decision.Reason,
		}, nil)
	}

	// Issue the merge. Idempotent at the GitHub level: re-issuing on an
	// already-merged PR returns a benign error which we tolerate (the
	// re-poll below confirms MERGED).
	if mergeErr := r.gh.Merge(ctx, prNumber); mergeErr != nil {
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
	postSnap, postErr := r.fetchWithPolling(ctx, prNumber)
	if postErr != nil {
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
		return finish(PRMergeResult{
			Path:     PathMerged,
			PRNumber: prNumber,
			PRState:  "MERGED",
			Reason:   ReasonCleanMerged,
		}, nil)
	}

	// EC budget exhausted without observing MERGED — the merge call returned
	// non-error but we cannot confirm the PR reached MERGED state. Punt so
	// the scheduler does not proceed to EvaluatePostMerge and close the issue
	// on an unconfirmed merge. The autonomous scheduler will retry on the next
	// tick when the PR's state becomes observable.
	return finish(PRMergeResult{
		Path:     PathPunt,
		PRNumber: prNumber,
		PRState:  postSnap.State,
		Reason:   ReasonMergeECTimeout,
	}, nil)
}

// fetchWithPolling calls gh.View up to pollMax times with pollInterval between
// attempts, returning early once state is MERGED.
func (r *DeterministicRunner) fetchWithPolling(ctx context.Context, prNumber int) (PRViewSnapshot, error) {
	var last PRViewSnapshot
	var lastErr error
	for poll := 0; poll < r.pollMax; poll++ {
		snap, err := r.gh.View(ctx, prNumber)
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

// mergeBlockedByPendingCI reports whether the ONLY thing preventing a clean
// merge is in-flight CI (Issue #297): the PR is OPEN and MERGEABLE (no
// conflict), no check has reported FAILURE/ERROR, review is not blocking, the
// merge state is BLOCKED or UNSTABLE (required/optional checks not yet green),
// and at least one check is still pending. Such a PR is expected to become
// CLEAN once CI finishes, so the runner waits rather than punting. Hard blockers
// (conflict → DIRTY/CONFLICTING, review required, a failed check, BEHIND/DRAFT)
// return false: those will not self-resolve by waiting, so the LLM path should
// get its turn immediately.
func mergeBlockedByPendingCI(snap PRViewSnapshot) bool {
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
	// Require at least one in-flight check so we don't spin on a PR that is
	// BLOCKED for a non-CI reason (e.g. a required check that will never run).
	return sawPending
}

// waitForCleanMergeState polls the PR up to ciPollMax times (ciPollInterval
// apart) while it remains blocked solely by pending CI (Issue #297). It returns
// the last observed snapshot and:
//   - timedOut=true  → the budget was exhausted with CI still pending (caller
//     punts ReasonCIWaitTimeout so the LLM path runs).
//   - timedOut=false, err=nil → the merge state resolved (either CLEAN and
//     mergeable, or a hard blocker/merge emerged); the caller re-runs Decide.
//   - err != nil → a non-retryable fetch error (e.g. rate limit) surfaced.
//
// The initial snapshot is passed so a runner configured with ciPollMax==0 (or a
// PR that resolves on the first re-poll) degrades gracefully.
func (r *DeterministicRunner) waitForCleanMergeState(ctx context.Context, prNumber int, initial PRViewSnapshot) (PRViewSnapshot, bool, error) {
	last := initial
	for poll := 0; poll < r.ciPollMax; poll++ {
		select {
		case <-ctx.Done():
			return last, false, ctx.Err()
		case <-time.After(r.ciPollInterval):
		}
		snap, err := r.gh.View(ctx, prNumber)
		if err != nil {
			// Rate-limit errors are not retryable inside the deterministic path
			// (#3020 / ADR-004) — surface immediately.
			if isRateLimitErr(err) {
				return last, false, err
			}
			// Transient fetch error — keep waiting until the budget expires.
			continue
		}
		last = snap
		// Resolved one way or another: either now mergeable/clean or a hard
		// blocker (failed check, conflict, review, merged) has appeared.
		if !mergeBlockedByPendingCI(snap) {
			return snap, false, nil
		}
	}
	return last, true, nil
}

// readPRContextNumber reads .nightgauge/pipeline/pr-{N}.json and returns
// pr_number. Default ctxReader for production runs.
func readPRContextNumber(workdir string, issueNumber int) (int, error) {
	prContextPath := filepath.Join(workdir, ".nightgauge", "pipeline",
		fmt.Sprintf("pr-%d.json", issueNumber))
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
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", fmt.Sprintf("%d", prNumber),
		"--json", "state,statusCheckRollup,mergeable,mergeStateStatus,reviewDecision")
	if c.workdir != "" {
		cmd.Dir = c.workdir
	}
	stdout, err := cmd.Output()
	if err != nil {
		return PRViewSnapshot{}, normalizeGhError(err)
	}
	var raw struct {
		State             string `json:"state"`
		Mergeable         string `json:"mergeable"`
		MergeStateStatus  string `json:"mergeStateStatus"`
		ReviewDecision    string `json:"reviewDecision"`
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
	}
	for _, row := range raw.StatusCheckRollup {
		snap.StatusCheckRollup = append(snap.StatusCheckRollup, PRStatusCheckRow{
			Name:       row.Name,
			Conclusion: row.Conclusion,
		})
	}
	return snap, nil
}

func (c *execGhClient) Merge(ctx context.Context, prNumber int) error {
	cmd := exec.CommandContext(ctx, "gh", "pr", "merge", fmt.Sprintf("%d", prNumber),
		"--squash", "--delete-branch")
	if c.workdir != "" {
		cmd.Dir = c.workdir
	}
	_, err := cmd.Output()
	if err != nil {
		return normalizeGhError(err)
	}
	return nil
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
