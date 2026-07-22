package recovery

import (
	"context"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// SkillExitedWithoutMerging recovers from the canonical "pr-merge skill
// exited 0 but the PR is still OPEN" pattern (Issue #1819 / #3268). The
// stage gate's KindNoOp signal names this case explicitly. Recovery reuses
// the existing deterministic PRMergeRunner — no new merge logic.
type SkillExitedWithoutMerging struct {
	runner pmstages.PRMergeRunner
}

// NewSkillExitedWithoutMerging wires the deterministic runner. nil runner
// is a programming error — Default() pins this to the scheduler's existing
// instance.
func NewSkillExitedWithoutMerging(runner pmstages.PRMergeRunner) *SkillExitedWithoutMerging {
	return &SkillExitedWithoutMerging{runner: runner}
}

// Name implements RecoveryAction.
func (a *SkillExitedWithoutMerging) Name() string { return "skill-exited-without-merging" }

// Description implements RecoveryAction.
func (a *SkillExitedWithoutMerging) Description() string {
	return "pr-merge skill exited 0 but the PR is still OPEN — re-run the deterministic PRMergeRunner."
}

// Matches implements RecoveryAction. Pure: inspects only typed fields.
func (a *SkillExitedWithoutMerging) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge {
		return false
	}
	if failure.GateKind != gates.KindNoOp {
		return false
	}
	// Exclude stall_kill — StallKilledOnPRMerge is the more specific action
	// for that case (registered first in Default()).
	if failure.TerminalKind == "stall_kill" {
		return false
	}
	return failure.PRNumber > 0
}

// Execute implements RecoveryAction. Reuses pmstages.PRMergeRunner.
func (a *SkillExitedWithoutMerging) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
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
			Evidence: []string{fmt.Sprintf("pr=%d", failure.PRNumber)},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	if res.Path == pmstages.PathMerged {
		return RecoveryResult{
			Recovered: true,
			Action:    a.Name(),
			Reason:    fmt.Sprintf("PR #%d merged via deterministic runner (%s)", res.PRNumber, res.Reason),
			Evidence: []string{
				fmt.Sprintf("pr=%d", res.PRNumber),
				fmt.Sprintf("runner_reason=%s", res.Reason),
			},
			FollowUp: FollowUpStageCanResume,
		}
	}
	return RecoveryResult{
		Action:   a.Name(),
		Reason:   fmt.Sprintf("deterministic merge punted: %s", res.Reason),
		Evidence: []string{fmt.Sprintf("pr=%d", failure.PRNumber)},
		FollowUp: FollowUpHumanTriageRequired,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
