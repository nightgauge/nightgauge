package recovery

import (
	"context"
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

type fakePRCreateRunner struct {
	res pmstages.PRCreateResult
	err error
}

func (f *fakePRCreateRunner) Run(_ context.Context, _ int, _, _ string) (pmstages.PRCreateResult, error) {
	return f.res, f.err
}

func TestAction_SkillExitedWithoutCreatingPR_Matches_AndRecovers(t *testing.T) {
	runner := &fakePRCreateRunner{
		res: pmstages.PRCreateResult{Path: pmstages.CreatePathCreated, PRNumber: 999, PRURL: "https://github.com/o/r/pull/999", Reason: pmstages.ReasonRichContext},
	}
	action := NewSkillExitedWithoutCreatingPR(runner)
	failure := StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp, PRNumber: 0}
	if !action.Matches(failure) {
		t.Fatal("expected match")
	}
	res := action.Execute(context.Background(), failure)
	if !res.Recovered {
		t.Errorf("expected Recovered=true; got %q", res.Reason)
	}
	if res.Action != "skill-exited-without-creating-pr" {
		t.Errorf("Action = %q", res.Action)
	}
}

func TestAction_SkillExitedWithoutCreatingPR_NoMatch_FallsThrough(t *testing.T) {
	a := NewSkillExitedWithoutCreatingPR(&fakePRCreateRunner{})
	cases := []struct {
		name string
		f    StageFailure
	}{
		{"wrong stage", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp}},
		{"PR already exists", StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp, PRNumber: 42}},
		{"gate ok", StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindOK, PRNumber: 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a.Matches(c.f) {
				t.Errorf("expected no match")
			}
		})
	}
}

func TestAction_SkillExitedWithoutCreatingPR_RunnerError(t *testing.T) {
	a := NewSkillExitedWithoutCreatingPR(&fakePRCreateRunner{err: errors.New("push failed")})
	res := a.Execute(context.Background(), StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp})
	if res.Recovered {
		t.Error("expected no recovery on runner error")
	}
}

func TestAction_SkillExitedWithoutCreatingPR_NilRunner(t *testing.T) {
	a := NewSkillExitedWithoutCreatingPR(nil)
	res := a.Execute(context.Background(), StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp})
	if res.Recovered {
		t.Error("expected no recovery without runner")
	}
}
