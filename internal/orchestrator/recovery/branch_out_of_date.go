package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// DefaultRebaseCIPolls / DefaultRebaseCIPollInterval bound the post-rebase CI
// wait. The action runs inside the scheduler's per-repo merge lock (held for
// the whole pr-merge stage scope, see scheduler.go getMergeLock), so the budget
// is deliberately tight: a never-green rebased head must not head-of-line block
// the rest of the wave's merges. Mirrors DefaultECPolls / DefaultECPollInterval
// in stages/prmerge.go.
const (
	DefaultRebaseCIPolls        = 10
	DefaultRebaseCIPollInterval = 30 * time.Second
)

// BranchOutOfDate fetches origin/main, rebases the PR branch, and force-pushes
// with --force-with-lease. The action fires when the pr-merge gate's evidence
// names a BEHIND or DIRTY merge state (the second same-wave sibling went stale
// after the first merged). After a successful rebase+push it does NOT merge
// directly: it waits for CI on the rebased head, then re-runs the deterministic
// PRMergeRunner — claiming Recovered=true ONLY on PathMerged, so the rebased PR
// is actually merged rather than skipped while the scheduler advances. A real
// rebase conflict is no longer escalated straight to human triage: the conflict
// context (files + both sides) is captured BEFORE `rebase --abort` and the
// action returns FollowUpStageCanResume so the conflict-recovery loop rewinds to
// feature-dev to resolve it on the same branch (#4072 / epic #4067).
//
// That deferral is claimed only when the capture actually SUCCEEDED. A rebase
// that failed without conflicting anything, and a capture that could not record
// the conflict, are separate outcomes with separate handling — see the switch in
// Execute (#301).
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
	return &BranchOutOfDate{
		runner:       runner,
		pollInterval: DefaultRebaseCIPollInterval,
		pollMax:      DefaultRebaseCIPolls,
	}
}

// Name implements RecoveryAction.
func (a *BranchOutOfDate) Name() string { return "branch-out-of-date" }

// Description implements RecoveryAction.
func (a *BranchOutOfDate) Description() string {
	return "PR's mergeStateStatus is BEHIND/DIRTY — fetch origin/main, rebase, wait for CI, re-run PRMergeRunner."
}

// Matches implements RecoveryAction. The gate's evidence carries the
// mergeStateStatus value; we fire on either a BEHIND (clean fast-forward needed)
// or DIRTY merge state at a pr-merge KindNoOp so a stale sibling PR isn't
// dropped. A clean rebase resolves BEHIND; a DIRTY tree that hits real conflicts
// during rebase defers to the conflict-recovery loop (#4072).
//
// Note: conflict-recovery-loop is registered AHEAD of this action, so a
// pr-merge no-op whose evidence already names a conflict is handled there. This
// action still owns the case where the BEHIND/DIRTY rebase only DISCOVERS the
// conflict at `git rebase` time (the gate evidence said BEHIND, not conflict).
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

// Execute implements RecoveryAction. Rebase → wait for CI on the rebased head →
// re-run the deterministic runner. Recovered=true only on PathMerged.
func (a *BranchOutOfDate) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	if a.runner == nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "deterministic merge runner not wired",
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// Rebase base is origin/main. Wave sub-issue PRs target main (the scheduler
	// pins BaseBranch="main" when authoring them, scheduler.go), so a hardcoded
	// base is correct for the in-scope wave merge-train (#4071 / epic #142).
	// StageFailure does not carry the PR's baseRef; deriving an epic-branch base
	// would require an extra `gh pr view --json baseRefName` shell-out and is
	// deferred until epic-branch wave PRs are actually in scope.
	steps := []struct {
		label string
		args  []string
	}{
		{"fetch", []string{"fetch", "origin", "main"}},
		{"rebase", []string{"rebase", "origin/main"}},
		{"push", []string{"push", "--force-with-lease"}},
	}

	// Resolve the PR branch BEFORE the steps run. `git rebase` detaches HEAD for
	// its whole duration, so asking from inside the failure handler below reads
	// the literal "HEAD" and degrades to the unknownBranch sentinel — which
	// feature-dev's conflict intake refuses to check out, silently discarding the
	// same-branch guarantee this hand-off exists to provide (#301). HEAD is still
	// attached here.
	branch := currentBranch(ctx, failure.Workspace)

	evidence := []string{fmt.Sprintf("pr=%d", failure.PRNumber)}
	for _, step := range steps {
		out, err := execGit(ctx, failure.Workspace, step.args...)
		if err != nil {
			if step.label == "rebase" {
				// A rebase failure. If it is a genuine content conflict, defer to
				// the conflict-recovery loop rather than escalating outright:
				// capture the conflicting files + both sides into
				// conflict-context-{N}.json and emit CONFLICT_RESOLUTION_NEEDED
				// BEFORE aborting (the blobs vanish after `git rebase --abort`), so
				// the scheduler rewinds to feature-dev on the SAME branch (#4072).
				//
				// But "the rebase failed" is not the same claim as "we captured a
				// conflict", and neither is "the capture ran". Each of the capture's
				// three outcomes gets its own handling below — the bug this replaces
				// treated all three as success (#301).
				if branch == unknownBranch {
					// Second chance: git records the branch it is rebasing in
					// rebase-merge/head-name even though HEAD is detached.
					if b := rebaseBranch(ctx, failure.Workspace); b != "" {
						branch = b
					}
				}
				capture := captureConflictContextFromIndex(ctx, failure.Workspace,
					failure.IssueNumber, failure.PRNumber, branch, "main")
				rebaseErr := truncate(err.Error(), 200)
				conflictEvidence := append(evidence, "step=rebase", fmt.Sprintf("branch=%s", branch))

				switch capture.Outcome {
				case captureCaptured:
					// The evidence is on disk and the signal is written; the index
					// has done its job. Abort to leave a clean tree for the dev
					// re-dispatch.
					_, _ = execGit(ctx, failure.Workspace, "rebase", "--abort")
					return RecoveryResult{
						Action: a.Name(),
						Reason: fmt.Sprintf("rebase conflict — deferring to conflict-recovery (re-dispatch feature-dev on %q, %d file(s))", capture.Branch, len(capture.Files)),
						Evidence: append(append(conflictEvidence, "capture=captured"),
							prefixed("conflicting_file=", capture.Files)...),
						FollowUp: FollowUpStageCanResume,
					}

				case captureNoConflictState:
					// The rebase failed but nothing is conflicted — a dirty index, a
					// pre-existing rebase, an unborn base. There is nothing for
					// feature-dev to resolve, so emitting CONFLICT_RESOLUTION_NEEDED
					// here would spend the entire max_dev_redispatch budget
					// re-running the dev stage against a context naming zero files
					// and terminate in triage anyway. Escalate now, with the real
					// rebase error. The abort is a harmless no-op when no rebase
					// started, and cleans up when one did.
					_, _ = execGit(ctx, failure.Workspace, "rebase", "--abort")
					return RecoveryResult{
						Action: a.Name(),
						Reason: fmt.Sprintf("git rebase failed with no conflicted paths — not a content conflict: %s", rebaseErr),
						Evidence: append(conflictEvidence,
							"capture=no-conflict-state",
							fmt.Sprintf("rebase_error=%s", rebaseErr),
							fmt.Sprintf("output=%s", truncate(string(out), 200)),
						),
						FollowUp: FollowUpHumanTriageRequired,
					}

				default: // captureFailed
					// A conflict may well exist and is recorded NOWHERE.
					// `git rebase --abort` permanently destroys the :2:/:3: index
					// blobs, so deliberately leave the rebase in progress: the index
					// is the only surviving copy of the evidence. Pair that strictly
					// with human triage — never StageCanResume — because a dev stage
					// must not be dispatched into a conflicted index.
					captureErr := "unspecified"
					if capture.Err != nil {
						captureErr = truncate(capture.Err.Error(), 200)
					}
					return RecoveryResult{
						Action: a.Name(),
						Reason: fmt.Sprintf("rebase conflict could not be captured (%s) — leaving the rebase in progress so the conflicted index survives for triage", captureErr),
						Evidence: append(append(conflictEvidence,
							"capture=failed",
							fmt.Sprintf("capture_error=%s", captureErr),
							fmt.Sprintf("rebase_error=%s", rebaseErr),
							"rebase_left_in_progress=true"),
							prefixed("conflicting_file=", capture.Files)...),
						FollowUp: FollowUpHumanTriageRequired,
					}
				}
			}
			return RecoveryResult{
				Action: a.Name(),
				Reason: fmt.Sprintf("git %s failed: %s", step.label, truncate(err.Error(), 200)),
				Evidence: append(evidence,
					fmt.Sprintf("step=%s", step.label),
					fmt.Sprintf("output=%s", truncate(string(out), 200)),
				),
				FollowUp: FollowUpHumanTriageRequired,
			}
		}
	}

	// Rebase+push succeeded but the PR is NOT yet merged. Wait for CI to
	// re-pass on the rebased commits before re-validating — pre-rebase checks
	// are stale.
	if ciRes, ok := a.waitForCI(ctx, failure, evidence); !ok {
		return ciRes
	}

	// CI is green on the rebased head — re-run the deterministic runner. It
	// re-fetches the PR snapshot and Decide() now finds CLEAN, so the merge is
	// issued. Recovered=true only on PathMerged: the PR is actually merged, not
	// skipped.
	res, err := a.runner.Run(ctx, failure.IssueNumber, failure.Repo, failure.Workspace)
	if err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("runner error after rebase: %s", truncate(err.Error(), 200)),
			Evidence: evidence,
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	if res.Path == pmstages.PathMerged {
		return RecoveryResult{
			Recovered: true,
			Action:    a.Name(),
			Reason:    fmt.Sprintf("rebased onto origin/main, CI re-passed, PR #%d merged via deterministic runner (%s)", res.PRNumber, res.Reason),
			Evidence: append(evidence,
				fmt.Sprintf("runner_reason=%s", res.Reason),
			),
			FollowUp: FollowUpStageCanResume,
		}
	}
	return RecoveryResult{
		Action:   a.Name(),
		Reason:   fmt.Sprintf("rebased and CI green but deterministic merge punted: %s", res.Reason),
		Evidence: append(evidence, fmt.Sprintf("runner_reason=%s", res.Reason)),
		FollowUp: FollowUpHumanTriageRequired,
	}
}

// waitForCI polls the rebased PR's aggregate check rollup to completion. It
// returns ok=true once every check is green; otherwise it returns a terminal
// RecoveryResult (ok=false) describing why recovery is declined: a failing
// check, context cancellation, or budget exhaustion while still in flight. The
// budget (DefaultRebaseCIPolls × DefaultRebaseCIPollInterval) is bounded so a
// never-green head cannot hang the scheduler under the per-repo merge lock.
func (a *BranchOutOfDate) waitForCI(ctx context.Context, failure StageFailure, evidence []string) (RecoveryResult, bool) {
	for poll := 0; poll < a.pollMax; poll++ {
		select {
		case <-ctx.Done():
			return RecoveryResult{
				Action:   a.Name(),
				Reason:   "context cancelled while waiting for CI on rebased head",
				Evidence: evidence,
				FollowUp: FollowUpNoAction,
			}, false
		default:
		}

		out, err := execGh(ctx, "pr", "view", fmt.Sprint(failure.PRNumber), "--json", "statusCheckRollup")
		if err == nil {
			var snap prChecksSnapshot
			if json.Unmarshal(out, &snap) == nil {
				state := summarizeChecks(snap.StatusCheckRollup)
				switch state {
				case checksGreen:
					return RecoveryResult{}, true
				case checksFailed:
					return RecoveryResult{
						Action:   a.Name(),
						Reason:   "CI failed on rebased head — not recovering; PR left for triage",
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
				Reason:   "context cancelled while waiting for CI on rebased head",
				Evidence: evidence,
				FollowUp: FollowUpNoAction,
			}, false
		case <-time.After(a.pollInterval):
		}
	}

	return RecoveryResult{
		Action:   a.Name(),
		Reason:   "CI still in flight on rebased head after polling budget — declining to claim recovery",
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

// summarizeChecks reduces the rollup to a single state: any FAILURE/ERROR is
// terminal-failed; any in-flight (empty/PENDING/QUEUED/IN_PROGRESS) keeps the
// wait pending; otherwise all checks are complete-and-green.
func summarizeChecks(rows []statusCheckRollupEntry) checksState {
	// An empty rollup right after a force-push means the old runs were dropped and
	// the rebased head's runs have not registered yet — treat it as PENDING (not
	// green) so waitForCI keeps polling for the rebased commits' CI, matching the
	// canonical CI-wait convention (internal/github/ci.go). For a repo with no CI
	// the bounded poll budget expires and waitForCI declines rather than merging
	// on a never-validated head (#4071 review).
	if len(rows) == 0 {
		return checksPending
	}
	pending := false
	for _, c := range rows {
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
			return checksFailed
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			// terminal-green; keep scanning.
		default:
			// "" / PENDING / QUEUED / IN_PROGRESS / EXPECTED / WAITING — in flight.
			pending = true
		}
	}
	if pending {
		return checksPending
	}
	return checksGreen
}
