package recovery

import (
	"context"
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// fakePRMergeRunner stubs pmstages.PRMergeRunner for action tests.
type fakePRMergeRunner struct {
	res pmstages.PRMergeResult
	err error
}

func (f *fakePRMergeRunner) Run(_ context.Context, _ int, _, _ string) (pmstages.PRMergeResult, error) {
	return f.res, f.err
}

func TestAction_SkillExitedWithoutMerging_Matches_AndRecovers(t *testing.T) {
	runner := &fakePRMergeRunner{
		res: pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 100, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged},
	}
	action := NewSkillExitedWithoutMerging(runner)

	failure := StageFailure{
		Stage:    state.StagePRMerge,
		GateKind: gates.KindNoOp,
		PRNumber: 100,
	}
	if !action.Matches(failure) {
		t.Fatalf("expected Matches=true for pr-merge KindNoOp + PRNumber>0")
	}

	res := action.Execute(context.Background(), failure)
	if !res.Recovered {
		t.Errorf("expected Recovered=true; got reason=%q", res.Reason)
	}
	if res.Action != "skill-exited-without-merging" {
		t.Errorf("Action = %q, want skill-exited-without-merging", res.Action)
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpStageCanResume)
	}
}

func TestAction_SkillExitedWithoutMerging_NoMatch_FallsThrough(t *testing.T) {
	action := NewSkillExitedWithoutMerging(&fakePRMergeRunner{})

	cases := []struct {
		name string
		f    StageFailure
	}{
		{"wrong stage", StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp, PRNumber: 1}},
		{"gate is OK (skill exit was not no-op)", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindOK, PRNumber: 1}},
		{"missing PR number", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 0}},
		{"stall-kill belongs to stall-killed action", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, TerminalKind: "stall_kill"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if action.Matches(c.f) {
				t.Errorf("expected no match for %s", c.name)
			}
		})
	}
}

// TestAction_SkillExitedWithoutMerging_RunnerPunt records a non-recovery
// when the deterministic runner declines. The matched-but-declined path is
// part of the registry contract.
func TestAction_SkillExitedWithoutMerging_RunnerPunt(t *testing.T) {
	runner := &fakePRMergeRunner{
		res: pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 100, Reason: pmstages.ReasonNotMergeable + ": CONFLICTING"},
	}
	action := NewSkillExitedWithoutMerging(runner)
	res := action.Execute(context.Background(), StageFailure{
		Stage:    state.StagePRMerge,
		GateKind: gates.KindNoOp,
		PRNumber: 100,
	})
	if res.Recovered {
		t.Errorf("expected Recovered=false on runner punt")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
}

func TestAction_SkillExitedWithoutMerging_RunnerError(t *testing.T) {
	runner := &fakePRMergeRunner{err: errors.New("gh exploded")}
	action := NewSkillExitedWithoutMerging(runner)
	res := action.Execute(context.Background(), StageFailure{
		Stage:    state.StagePRMerge,
		GateKind: gates.KindNoOp,
		PRNumber: 100,
	})
	if res.Recovered {
		t.Errorf("expected Recovered=false on runner error")
	}
}

func TestAction_SkillExitedWithoutMerging_NilRunner(t *testing.T) {
	action := NewSkillExitedWithoutMerging(nil)
	res := action.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1,
	})
	if res.Recovered {
		t.Error("nil runner cannot recover")
	}
}
