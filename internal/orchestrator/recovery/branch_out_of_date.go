package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// BranchOutOfDate updates a stale PR branch by merging the PR's base into it
// and pushing with a plain fast-forward push. It never rebases and never
// force-pushes: an existing PR branch keeps its history, so the push cannot be
// rejected as non-fast-forward and no reviewed commit is rewritten (#2699,
// #2752). The base is the PR's baseRefName, not a hard-coded main, so an
// epic-branch PR is updated from its epic branch (#2843).
//
// The action fires when the pr-merge gate's evidence names a BEHIND or DIRTY
// merge state (the second same-wave sibling went stale after the first merged).
// After a successful merge+push it does NOT merge the PR directly: it waits for
// CI on the updated head, then re-runs the deterministic PRMergeRunner —
// claiming Recovered=true ONLY on PathMerged. A real merge conflict is not
// escalated straight to human triage: the conflict context (files + both sides)
// is captured BEFORE `merge --abort` and the action returns
// FollowUpStageCanResume so the conflict-recovery loop rewinds to feature-dev to
// resolve it on the same branch (#4072 / epic #4067).
//
// That deferral is claimed only when the capture actually SUCCEEDED. A merge
// that failed without conflicting anything, and a capture that could not record
// the conflict, are separate outcomes with separate handling — see
// handleMergeFailure (#301).
type BranchOutOfDate struct {
	runner       pmstages.PRMergeRunner
	pollInterval time.Duration
	pollMax      int
}

// NewBranchOutOfDate wires the deterministic runner shared with the scheduler
// (mirrors NewSkillExitedWithoutMerging). A nil runner is a programming error —
// Default() pins this to the scheduler's existing instance; Execute guards it
// with FollowUpHumanTriageRequired.
func NewBranchOutOfDate(runner pmstages.PRMergeRunner) *BranchOutOfDate {
	return (&BranchOutOfDate{runner: runner}).WithCIWait(config.DefaultPRMergeCIWait)
}

// WithCIWait sizes the post-update CI wait to d, polled every
// github.DefaultCIPollInterval. Default() passes config.ResolvePRMergeCIWait so
// the wait matches an ordinary pr-merge CI wait (#2980): core CI takes 15 to 25
// minutes, and a shorter wait booked a resolved conflict as a failure. The merge
// lock is yielded for the wait (StageFailure.YieldMergeLock).
func (a *BranchOutOfDate) WithCIWait(d time.Duration) *BranchOutOfDate {
	return a.withPolling(github.DefaultCIPollInterval, d)
}

// withPolling sets the interval and enough polls to cover d (at least one).
func (a *BranchOutOfDate) withPolling(interval, d time.Duration) *BranchOutOfDate {
	a.pollInterval = interval
	a.pollMax = int((d + interval - 1) / interval)
	if a.pollMax < 1 {
		a.pollMax = 1
	}
	return a
}

// Name implements RecoveryAction.
func (a *BranchOutOfDate) Name() string { return "branch-out-of-date" }

// Description implements RecoveryAction.
func (a *BranchOutOfDate) Description() string {
	return "PR's mergeStateStatus is BEHIND/DIRTY — merge the PR's base into the branch, push (no force), wait for CI, re-run PRMergeRunner."
}

// Matches implements RecoveryAction. The gate's evidence carries the
// mergeStateStatus value; we fire on either a BEHIND (base update needed) or
// DIRTY merge state at a pr-merge KindNoOp so a stale sibling PR isn't dropped.
// A clean merge of the base resolves BEHIND; a DIRTY tree that hits real
// conflicts during the merge defers to the conflict-recovery loop (#4072).
//
// Note: conflict-recovery-loop is registered AHEAD of this action, so a
// pr-merge no-op whose evidence already names a conflict is handled there. This
// action still owns the case where the BEHIND/DIRTY update only DISCOVERS the
// conflict at `git merge` time (the gate evidence said BEHIND, not conflict).
func (a *BranchOutOfDate) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge {
		return false
	}
	if failure.GateKind != gates.KindNoOp {
		return false
	}
	combined := strings.ToLower(failure.Reason + " " + strings.Join(failure.Evidence, " "))
	if !strings.Contains(combined, "behind") &&
		!strings.Contains(combined, "dirty") &&
		!strings.Contains(combined, pmstages.ReasonDirtyState) {
		return false
	}
	return failure.Workspace != ""
}

// defaultBaseRef is the merge base used when the PR's baseRefName cannot be
// read.
const defaultBaseRef = "main"

// prBaseRef returns the PR's baseRefName, or defaultBaseRef when the lookup
// fails or names nothing.
func prBaseRef(ctx context.Context, pr int) string {
	out, err := execGh(ctx, "pr", "view", fmt.Sprint(pr), "--json", "baseRefName")
	if err != nil {
		return defaultBaseRef
	}
	var v struct {
		BaseRefName string `json:"baseRefName"`
	}
	if json.Unmarshal(out, &v) != nil || strings.TrimSpace(v.BaseRefName) == "" {
		return defaultBaseRef
	}
	return strings.TrimSpace(v.BaseRefName)
}

// Execute implements RecoveryAction. Merge the base → push → wait for CI on the
// updated head → re-run the deterministic runner. Recovered=true only on
// PathMerged.
func (a *BranchOutOfDate) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	if a.runner == nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "deterministic merge runner not wired",
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// Refuse outright when a rebase or a merge is ALREADY in progress.
	// Everything below assumes any merge state it meets is state this
	// invocation created, and `git merge --abort` acts on that assumption — but
	// a worktree paused in `git rebase -i` at an `edit` step, or part-way
	// through an operator's conflicted merge, is the operator's, and aborting it
	// throws away their in-progress work with no record that anything was
	// destroyed (#301 review reproduced exactly that: staged `wip.txt`, gone).
	// We cannot tell whose operation it is, so we do not touch it: no fetch, no
	// merge, no abort, straight to triage.
	//
	// Probed here rather than in the failure handler because this is the only
	// point at which the answer is unambiguous — after `git merge` runs,
	// MERGE_HEAD could be either.
	if dir := rebaseStateDir(ctx, failure.Workspace); dir != "" {
		return RecoveryResult{
			Action: a.Name(),
			Reason: "a rebase is already in progress in this worktree — refusing to merge over, or abort, state this run did not create",
			Evidence: []string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				"preexisting_rebase=true",
				fmt.Sprintf("rebase_state=%s", dir),
			},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	if gitPathExists(ctx, failure.Workspace, "MERGE_HEAD") {
		return RecoveryResult{
			Action: a.Name(),
			Reason: "a merge is already in progress in this worktree — refusing to merge over, or abort, state this run did not create",
			Evidence: []string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				"preexisting_merge=true",
			},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// The PR's own base, not a hard-coded main: an epic-branch PR is updated
	// from its epic branch (#2843).
	base := prBaseRef(ctx, failure.PRNumber)
	branch := currentBranch(ctx, failure.Workspace)
	evidence := []string{fmt.Sprintf("pr=%d", failure.PRNumber), fmt.Sprintf("base=%s", base)}

	if out, err := execGit(ctx, failure.Workspace, "fetch", "origin", base); err != nil {
		return a.gitStepFailed("fetch", evidence, out, err)
	}

	if out, err := execGit(ctx, failure.Workspace, "merge", "--no-edit", "origin/"+base); err != nil {
		return a.handleMergeFailure(ctx, failure, branch, base, evidence, out, err)
	}

	// A detached HEAD names no branch to push. The merge was clean, but pushing
	// HEAD to a guessed ref could land on the wrong branch.
	if branch == unknownBranch {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "merged the base but HEAD is detached — no PR branch to push to",
			Evidence: append(evidence, "step=push", fmt.Sprintf("branch=%s", branch)),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// A plain push: the merge only adds commits on top of the remote branch, so
	// it fast-forwards. Never --force*.
	if out, err := execGit(ctx, failure.Workspace, "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return a.gitStepFailed("push", evidence, out, err)
	}

	return a.afterPush(ctx, failure, base, evidence)
}

// gitStepFailed is the triage result for a git step that failed outright.
func (a *BranchOutOfDate) gitStepFailed(label string, evidence []string, out []byte, err error) RecoveryResult {
	return RecoveryResult{
		Action: a.Name(),
		Reason: fmt.Sprintf("git %s failed: %s", label, truncate(gitErrDetail(err), 300)),
		Evidence: append(evidence,
			fmt.Sprintf("step=%s", label),
			fmt.Sprintf("output=%s", truncate(string(out), 200)),
		),
		FollowUp: FollowUpHumanTriageRequired,
	}
}

// handleMergeFailure handles a failed `git merge` of the base. If it is a
// genuine content conflict, defer to the conflict-recovery loop rather than
// escalating outright: capture the conflicting files + both sides into
// conflict-context-{N}.json and emit CONFLICT_RESOLUTION_NEEDED BEFORE aborting
// (the index stages vanish after `git merge --abort`), so the scheduler rewinds
// to feature-dev on the SAME branch (#4072). HEAD stays attached during a
// merge, so the branch resolved before the merge is the branch in conflict.
//
// But "the merge failed" is not the same claim as "we captured a conflict", and
// neither is "the capture ran". Each of the capture's three outcomes gets its
// own handling below (#301).
func (a *BranchOutOfDate) handleMergeFailure(ctx context.Context, failure StageFailure, branch, base string, evidence []string, out []byte, err error) RecoveryResult {
	capture := captureConflictContextFromIndex(ctx, failure.Workspace,
		failure.IssueNumber, failure.PRNumber, branch, base)
	// gitErrDetail, not err.Error(): git writes its whole diagnosis to stderr and
	// execGit returns stdout, so the bare error is the contentless string
	// "exit status 1" (#301 round-2 advisory).
	mergeErr := truncate(gitErrDetail(err), 300)
	conflictEvidence := append(evidence, "step=merge", fmt.Sprintf("branch=%s", branch))

	switch capture.Outcome {
	case captureCaptured:
		// The evidence is on disk and the signal is written; the index has done
		// its job. Abort to leave a clean tree for the dev re-dispatch.
		//
		// This abort's exit status is CHECKED, unlike the two below, because this
		// is the only branch that returns FollowUpStageCanResume. An abort that
		// failed (index.lock, permissions) leaves a live merge and an unmerged
		// index while telling the scheduler to rewind — dispatching a dev stage
		// into a conflicted index is the one thing this path exists to prevent
		// (#301 round-2 advisory).
		if _, aerr := execGit(ctx, failure.Workspace, "merge", "--abort"); aerr != nil {
			return RecoveryResult{
				Action: a.Name(),
				Reason: fmt.Sprintf("merge conflict captured for %q, but `git merge --abort` failed (%s) — refusing to resume a stage into a live merge with an unmerged index",
					capture.Branch, truncate(gitErrDetail(aerr), 200)),
				Evidence: append(append(conflictEvidence,
					"capture=captured",
					"abort_failed=true",
					"merge_left_in_progress=true",
					fmt.Sprintf("abort_error=%s", truncate(gitErrDetail(aerr), 200))),
					prefixed("conflicting_file=", capture.Files)...),
				FollowUp: FollowUpHumanTriageRequired,
			}
		}
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("merge conflict with origin/%s — deferring to conflict-recovery (re-dispatch feature-dev on %q, %d file(s))", base, capture.Branch, len(capture.Files)),
			Evidence: append(append(conflictEvidence, "capture=captured"),
				prefixed("conflicting_file=", capture.Files)...),
			FollowUp: FollowUpStageCanResume,
		}

	case captureNoConflictState:
		// The merge failed but nothing is conflicted — a dirty index or an
		// unknown base. There is nothing for feature-dev to resolve, so emitting
		// CONFLICT_RESOLUTION_NEEDED here would spend the whole
		// max_dev_redispatch budget re-running the dev stage against a context
		// naming zero files and terminate in triage anyway. Escalate now, with
		// the real merge error.
		//
		// The abort is safe ONLY because the pre-flight guard established that
		// no merge was in progress when we started: any merge state now is ours.
		_, _ = execGit(ctx, failure.Workspace, "merge", "--abort")
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("git merge failed with no conflicted paths — not a content conflict: %s", mergeErr),
			Evidence: append(conflictEvidence,
				"capture=no-conflict-state",
				fmt.Sprintf("merge_error=%s", mergeErr),
				fmt.Sprintf("output=%s", truncate(string(out), 200)),
			),
			FollowUp: FollowUpHumanTriageRequired,
		}

	default: // captureFailed
		// The conflict could not be turned into something feature-dev can act
		// on. What licenses the abort here is NOT the outcome but whether the raw
		// index was copied out first: `git merge --abort` destroys the :2:/:3:
		// stages permanently. Either way this is human triage, never
		// StageCanResume — a dev stage must not be dispatched against a conflict
		// nobody could record.
		captureErr := "unspecified"
		if capture.Err != nil {
			captureErr = truncate(capture.Err.Error(), 200)
		}
		if capture.EvidenceDir != "" {
			// The raw stages are on disk under conflict-evidence-{N}/. Abort so
			// the operator inherits a usable worktree — leaving it mid-merge does
			// not protect the index: the scheduler's terminal defer `git add -A`s
			// it away seconds later (#301 review).
			_, _ = execGit(ctx, failure.Workspace, "merge", "--abort")
			return RecoveryResult{
				Action: a.Name(),
				Reason: fmt.Sprintf("merge conflict could not be captured (%s) — raw index stages preserved to %s for triage", captureErr, capture.EvidenceDir),
				Evidence: append(append(conflictEvidence,
					"capture=failed",
					"evidence_preserved=true",
					fmt.Sprintf("evidence_dir=%s", capture.EvidenceDir),
					fmt.Sprintf("capture_error=%s", captureErr),
					fmt.Sprintf("merge_error=%s", mergeErr)),
					prefixed("conflicting_file=", capture.Files)...),
				FollowUp: FollowUpHumanTriageRequired,
			}
		}
		// Last resort: the capture failed AND the raw index could not be copied
		// out. Aborting now would leave zero record of the conflict, so do not —
		// the in-index stages are all that is left.
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("merge conflict could not be captured or preserved (%s) — leaving the merge in progress so the conflicted index survives for triage. To clear it: copy each conflicting path's sides out with `git show :2:<path>` / `git show :3:<path>` (a submodule pointer's sides are the commit ids in `git ls-files -u`), then `git merge --abort`; nothing in the pipeline reclaims a mid-merge worktree on its own", captureErr),
			Evidence: append(append(conflictEvidence,
				"capture=failed",
				"evidence_preserved=false",
				fmt.Sprintf("capture_error=%s", captureErr),
				fmt.Sprintf("merge_error=%s", mergeErr),
				"merge_left_in_progress=true"),
				prefixed("conflicting_file=", capture.Files)...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
}

// afterPush waits for CI on the updated head and re-runs the deterministic
// runner.
func (a *BranchOutOfDate) afterPush(ctx context.Context, failure StageFailure, base string, evidence []string) RecoveryResult {
	// Merge+push succeeded but the PR is NOT yet merged. Wait for CI to re-pass
	// on the updated head before re-validating — the earlier checks are stale.
	if ciRes, ok := a.waitForCI(ctx, failure, evidence); !ok {
		return ciRes
	}

	// CI is green on the updated head — re-run the deterministic runner. It
	// re-fetches the PR snapshot and Decide() now finds CLEAN, so the merge is
	// issued. Recovered=true only on PathMerged: the PR is actually merged, not
	// skipped.
	res, err := a.runner.Run(ctx, failure.IssueNumber, failure.Repo, failure.Workspace)
	if err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("runner error after base merge: %s", truncate(err.Error(), 200)),
			Evidence: evidence,
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	if res.Path == pmstages.PathMerged {
		return RecoveryResult{
			Recovered: true,
			Action:    a.Name(),
			Reason:    fmt.Sprintf("merged origin/%s, CI re-passed, PR #%d merged via deterministic runner (%s)", base, res.PRNumber, res.Reason),
			Evidence: append(evidence,
				fmt.Sprintf("runner_reason=%s", res.Reason),
			),
			FollowUp: FollowUpStageCanResume,
		}
	}
	return RecoveryResult{
		Action:   a.Name(),
		Reason:   fmt.Sprintf("base merged and CI green but deterministic merge punted: %s", res.Reason),
		Evidence: append(evidence, fmt.Sprintf("runner_reason=%s", res.Reason)),
		FollowUp: FollowUpHumanTriageRequired,
	}
}

// waitForCI polls the updated PR's aggregate check rollup to completion. It
// returns ok=true once every check is green; otherwise it returns a terminal
// RecoveryResult (ok=false) describing why recovery is declined: a failing
// check, context cancellation, or budget exhaustion while still in flight. The
// budget (see WithCIWait) matches an ordinary pr-merge CI wait. The merge lock
// is yielded for the wait and re-taken before returning, so the repository's
// other merges proceed meanwhile (#2980); failure and cancellation return at once.
func (a *BranchOutOfDate) waitForCI(ctx context.Context, failure StageFailure, evidence []string) (RecoveryResult, bool) {
	if failure.YieldMergeLock != nil {
		relock := failure.YieldMergeLock()
		defer relock()
	}
	lastPending := "rollup unreadable"
	for poll := 0; poll < a.pollMax; poll++ {
		select {
		case <-ctx.Done():
			return RecoveryResult{
				Action:   a.Name(),
				Reason:   "context cancelled while waiting for CI on updated head",
				Evidence: evidence,
				FollowUp: FollowUpNoAction,
			}, false
		default:
		}

		out, err := execGh(ctx, "pr", "view", fmt.Sprint(failure.PRNumber), "--json", "statusCheckRollup")
		lastPending = "rollup unreadable"
		if err == nil {
			var snap prChecksSnapshot
			if json.Unmarshal(out, &snap) == nil {
				state, pendingNames := summarizeChecks(snap.StatusCheckRollup)
				lastPending = formatPending(pendingNames)
				switch state {
				case checksGreen:
					return RecoveryResult{}, true
				case checksFailed:
					return RecoveryResult{
						Action:   a.Name(),
						Reason:   "CI failed on updated head — not recovering; PR left for triage",
						Evidence: evidence,
						FollowUp: FollowUpHumanTriageRequired,
					}, false
				}
				// checksPending → keep polling.
			}
		}

		if poll == a.pollMax-1 {
			break
		}
		select {
		case <-ctx.Done():
			return RecoveryResult{
				Action:   a.Name(),
				Reason:   "context cancelled while waiting for CI on updated head",
				Evidence: evidence,
				FollowUp: FollowUpNoAction,
			}, false
		case <-time.After(a.pollInterval):
		}
	}

	return RecoveryResult{
		Action:   a.Name(),
		Reason:   "CI still in flight on updated head after polling budget — declining to claim recovery; " + lastPending,
		Evidence: evidence,
		FollowUp: FollowUpNoAction,
	}, false
}

// checksState classifies the aggregate of a PR's status check rollup.
type checksState int

const (
	checksPending checksState = iota
	checksGreen
	checksFailed
)

// maxPendingNames caps how many pending checks a give-up message lists.
const maxPendingNames = 10

// formatPending renders the pending check names for the give-up message.
func formatPending(names []string) string {
	if len(names) == 0 {
		return "still pending: no checks registered"
	}
	shown := names
	more := ""
	if len(names) > maxPendingNames {
		shown = names[:maxPendingNames]
		more = fmt.Sprintf(" +%d more", len(names)-maxPendingNames)
	}
	return "still pending: " + strings.Join(shown, ", ") + more
}

// summarizeChecks reduces the rollup to a single state: any failed row is
// terminal-failed; any in-flight row keeps the wait pending, and its label is
// returned; otherwise all checks are complete-and-green. Each row is judged by
// verdict(), so a commit status is read by its state (#3026).
func summarizeChecks(rows []statusCheckRollupEntry) (checksState, []string) {
	// An empty rollup right after a push means the new head's runs have not
	// registered yet — treat it as PENDING (not
	// green) so waitForCI keeps polling for the new head's CI, matching the
	// canonical CI-wait convention (internal/github/ci.go). For a repo with no CI
	// the bounded poll budget expires and waitForCI declines rather than merging
	// on a never-validated head (#4071 review).
	if len(rows) == 0 {
		return checksPending, nil
	}
	var pending []string
	for _, c := range rows {
		switch c.verdict() {
		case checksFailed:
			return checksFailed, nil
		case checksPending:
			pending = append(pending, c.label())
		}
	}
	if len(pending) > 0 {
		return checksPending, pending
	}
	return checksGreen, nil
}
