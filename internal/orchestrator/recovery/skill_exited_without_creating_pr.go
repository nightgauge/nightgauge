package recovery

import (
	"context"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// SkillExitedWithoutCreatingPR recovers from "pr-create skill exited 0 but
// pr-{N}.json is absent / pr_number == 0" — the pr-create analog of the
// SkillExitedWithoutMerging action. Reuses the existing deterministic
// PRCreateRunner; on CreatePathCreated the stage is marked recovered.
type SkillExitedWithoutCreatingPR struct {
	runner pmstages.PRCreateRunner
}

// NewSkillExitedWithoutCreatingPR wires the deterministic runner.
func NewSkillExitedWithoutCreatingPR(runner pmstages.PRCreateRunner) *SkillExitedWithoutCreatingPR {
	return &SkillExitedWithoutCreatingPR{runner: runner}
}

// Name implements RecoveryAction.
func (a *SkillExitedWithoutCreatingPR) Name() string { return "skill-exited-without-creating-pr" }

// Description implements RecoveryAction.
func (a *SkillExitedWithoutCreatingPR) Description() string {
	return "pr-create skill exited 0 but pr-{N}.json is absent or pr_number == 0 — re-run the deterministic PRCreateRunner."
}

// Matches implements RecoveryAction.
func (a *SkillExitedWithoutCreatingPR) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRCreate {
		return false
	}
	if failure.GateKind != gates.KindNoOp {
		return false
	}
	// PR already exists with a real number — the missing-output is something
	// else; let the LLM path or human handle it.
	return failure.PRNumber == 0
}

// Execute implements RecoveryAction.
func (a *SkillExitedWithoutCreatingPR) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	if a.runner == nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "deterministic pr-create runner not wired",
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
	if res.Path == pmstages.CreatePathCreated {
		return RecoveryResult{
			Recovered: true,
			Action:    a.Name(),
			Reason:    fmt.Sprintf("PR #%d created via deterministic runner (%s)", res.PRNumber, res.Reason),
			Evidence: []string{
				fmt.Sprintf("pr=%d", res.PRNumber),
				fmt.Sprintf("runner_reason=%s", res.Reason),
			},
			FollowUp: FollowUpStageCanResume,
		}
	}
	return RecoveryResult{
		Action:   a.Name(),
		Reason:   fmt.Sprintf("deterministic create punted: %s", res.Reason),
		FollowUp: FollowUpHumanTriageRequired,
	}
}
