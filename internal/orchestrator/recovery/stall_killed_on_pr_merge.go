package recovery

import (
	"context"
	"fmt"
	"strings"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// StallKilledOnPRMerge recovers from a stall-killed pr-merge stage when the
// PR is otherwise clean+mergeable. The stall-rewind path (see
// orchestrator/stall_recovery.go) does not apply here because pr-merge is
// not a CanRewindFromStage stage. This action runs the deterministic
// PRMergeRunner (the same action SkillExitedWithoutMerging uses) but matches
// on a different signal — the stall-kill terminal kind.
type StallKilledOnPRMerge struct {
	runner pmstages.PRMergeRunner
}

// NewStallKilledOnPRMerge wires the runner.
func NewStallKilledOnPRMerge(runner pmstages.PRMergeRunner) *StallKilledOnPRMerge {
	return &StallKilledOnPRMerge{runner: runner}
}

// Name implements RecoveryAction.
func (a *StallKilledOnPRMerge) Name() string { return "stall-killed-on-pr-merge" }

// Description implements RecoveryAction.
func (a *StallKilledOnPRMerge) Description() string {
	return "pr-merge stalled and was killed but the PR exists and is mergeable — re-run the deterministic PRMergeRunner."
}

// Matches implements RecoveryAction.
//
// Stall-rewind does not apply to pr-merge (CanRewindFromStage rejects it).
// Cost-cap kills are excluded — the per-stage cost cap contract takes
// precedence and is never auto-recovered.
func (a *StallKilledOnPRMerge) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge {
		return false
	}
	if failure.TerminalKind != "stall_kill" {
		return false
	}
	if hasCostCapMarker(failure.StageError) {
		return false
	}
	return failure.PRNumber > 0
}

// Execute implements RecoveryAction.
func (a *StallKilledOnPRMerge) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	if a.runner == nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "deterministic merge runner not wired",
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	res, err := a.runner.Run(ctx, failure.IssueNumber, failure.Repo, failure.Workspace)
	if err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("runner error: %s", truncate(err.Error(), 200)),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	if res.Path == pmstages.PathMerged {
		return RecoveryResult{
			Recovered: true,
			Action:    a.Name(),
			Reason:    fmt.Sprintf("PR #%d merged after stall (%s)", res.PRNumber, res.Reason),
			Evidence: []string{
				fmt.Sprintf("pr=%d", res.PRNumber),
				fmt.Sprintf("runner_reason=%s", res.Reason),
			},
			FollowUp: FollowUpStageCanResume,
		}
	}
	return RecoveryResult{
		Action:   a.Name(),
		Reason:   fmt.Sprintf("deterministic merge punted after stall: %s", res.Reason),
		FollowUp: FollowUpHumanTriageRequired,
	}
}

// hasCostCapMarker mirrors orchestrator.HasCostCapKillMarker — duplicated
// here to keep the recovery package free of a reverse import on orchestrator.
func hasCostCapMarker(s string) bool {
	if s == "" {
		return false
	}
	t := strings.ToLower(s)
	return strings.Contains(t, "[cost-cap-exceeded]") ||
		strings.Contains(t, "cost-cap-exceeded") ||
		strings.Contains(t, "cost cap exceeded")
}
