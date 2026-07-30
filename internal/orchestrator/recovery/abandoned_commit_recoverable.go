package recovery

import (
	"context"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/execution"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// AbandonedCommitRecoverable recovers a stage that was killed/crashed after
// committing valid work but before pr-create ran (#191). Unlike
// SkillExitedWithoutCreatingPR (which only fires when pr-create itself is the
// failing stage), this fires for any earlier stage — feature-dev and
// feature-validate — because the gap it closes is upstream of pr-create: a
// killed stage today falls straight through to the generic terminal-failure
// path and a retry re-derives the committed work from scratch instead of
// reusing it.
//
// Execute first tries the shared deterministic PRCreateRunner (free self-heal,
// same as SkillExitedWithoutCreatingPR). When the runner punts — expected when
// the killed stage is feature-dev itself and never wrote dev-{N}.json /
// validate-{N}.json — this action does NOT treat the punt as unrecoverable. It
// sets BacktrackTargetStage to pr-create so the scheduler's existing rewind
// plumbing (already exercised by conflict-recovery-loop) resumes the pipeline
// mid-run on the SAME branch instead of restarting from issue-pickup.
type AbandonedCommitRecoverable struct {
	runner pmstages.PRCreateRunner
}

// NewAbandonedCommitRecoverable wires the shared deterministic runner.
func NewAbandonedCommitRecoverable(runner pmstages.PRCreateRunner) *AbandonedCommitRecoverable {
	return &AbandonedCommitRecoverable{runner: runner}
}

// Name implements RecoveryAction.
func (a *AbandonedCommitRecoverable) Name() string { return "abandoned-commit-recoverable" }

// Description implements RecoveryAction.
func (a *AbandonedCommitRecoverable) Description() string {
	return "A stage upstream of pr-create was killed/crashed after committing valid, unmerged work with a clean tree and no PR — self-heal via the deterministic PRCreateRunner, or resume mid-pipeline at pr-create instead of a full restart."
}

// Matches implements RecoveryAction. Pure: inspects only typed fields.
//
// pr-create and pr-merge have their own, more specific actions for their own
// failure modes — this action is only for the stages upstream of them. A PR
// that already exists means this isn't the "no PR" scenario this action
// exists for. A live workspace is required because Execute must inspect git
// state; without one there is nothing to detect.
func (a *AbandonedCommitRecoverable) Matches(failure StageFailure) bool {
	if failure.Stage == state.StagePRCreate || failure.Stage == state.StagePRMerge {
		return false
	}
	if failure.PRNumber != 0 {
		return false
	}
	return failure.Workspace != ""
}

// Execute implements RecoveryAction.
func (a *AbandonedCommitRecoverable) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	branch := currentBranch(ctx, failure.Workspace)
	if branch == "unknown" || branch == "" {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("could not resolve current branch in workspace %s", failure.Workspace),
		}
	}

	defaultBranch := execution.DetectDefaultBranch(failure.Workspace)
	baseRef, err := execution.ResolveBaseRef(failure.Workspace, defaultBranch)
	if err != nil {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("could not resolve base ref: %s", truncate(err.Error(), 200)),
		}
	}

	info, err := execution.DetectBranchAhead(failure.Workspace, branch, baseRef)
	if err != nil {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("could not inspect branch state: %s", truncate(err.Error(), 200)),
		}
	}
	if !info.AheadOfBase || !info.Clean {
		// No evidence this is the abandoned-commit scenario — a zero-value,
		// non-recovered, non-backtrack result is a plain non-match to the
		// caller (registry falls through unchanged, same as no match).
		return RecoveryResult{Action: a.Name()}
	}

	evidence := []string{
		fmt.Sprintf("branch=%s", branch),
		"ahead_of_base=true",
		"clean_tree=true",
	}

	if a.runner == nil {
		return RecoveryResult{
			Action:               a.Name(),
			Reason:               fmt.Sprintf("branch %s has committed, unmerged work (clean tree) but no deterministic pr-create runner is wired — resuming at pr-create instead of full restart", branch),
			Evidence:             evidence,
			FollowUp:             FollowUpStageCanResume,
			BacktrackTargetStage: string(state.StagePRCreate),
		}
	}

	res, runErr := a.runner.Run(ctx, failure.IssueNumber, failure.Repo, failure.Workspace)
	if runErr != nil {
		return RecoveryResult{
			Action:               a.Name(),
			Reason:               fmt.Sprintf("branch %s has committed, unmerged work (clean tree) but deterministic pr-create errored: %s — resuming at pr-create instead of full restart", branch, truncate(runErr.Error(), 200)),
			Evidence:             evidence,
			FollowUp:             FollowUpStageCanResume,
			BacktrackTargetStage: string(state.StagePRCreate),
		}
	}
	if res.Path == pmstages.CreatePathCreated {
		return RecoveryResult{
			Recovered: true,
			Action:    a.Name(),
			Reason:    fmt.Sprintf("PR #%d created via deterministic runner (%s)", res.PRNumber, res.Reason),
			Evidence: append(evidence,
				fmt.Sprintf("pr=%d", res.PRNumber),
				fmt.Sprintf("runner_reason=%s", res.Reason),
			),
			FollowUp: FollowUpStageCanResume,
		}
	}

	// Runner punted (expected when dev-{N}.json/validate-{N}.json are missing
	// because the killed stage never wrote them) — NOT a failure. Resume at
	// pr-create instead of restarting: this is the mechanism that fixes
	// "retry redoes the work".
	return RecoveryResult{
		Action:               a.Name(),
		Reason:               fmt.Sprintf("branch %s has committed, unmerged work (clean tree) but deterministic pr-create punted: %s — resuming at pr-create instead of full restart", branch, res.Reason),
		Evidence:             append(evidence, fmt.Sprintf("runner_reason=%s", res.Reason)),
		FollowUp:             FollowUpStageCanResume,
		BacktrackTargetStage: string(state.StagePRCreate),
	}
}
