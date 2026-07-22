package recovery

import (
	"context"
	"testing"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

func TestAction_StallKilledOnPRMerge_Matches_AndRecovers(t *testing.T) {
	runner := &fakePRMergeRunner{
		res: pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 200, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged},
	}
	a := NewStallKilledOnPRMerge(runner)
	failure := StageFailure{
		Stage:        state.StagePRMerge,
		TerminalKind: "stall_kill",
		PRNumber:     200,
		StageError:   "exceeded stall idle threshold",
	}
	if !a.Matches(failure) {
		t.Fatal("expected match")
	}
	res := a.Execute(context.Background(), failure)
	if !res.Recovered {
		t.Errorf("expected Recovered=true; got %q", res.Reason)
	}
	if res.Action != "stall-killed-on-pr-merge" {
		t.Errorf("Action = %q", res.Action)
	}
}

func TestAction_StallKilledOnPRMerge_NoMatch_FallsThrough(t *testing.T) {
	a := NewStallKilledOnPRMerge(&fakePRMergeRunner{})
	cases := []struct {
		name string
		f    StageFailure
	}{
		{"wrong stage", StageFailure{Stage: state.StageFeatureValidate, TerminalKind: "stall_kill", PRNumber: 1}},
		{"not stall", StageFailure{Stage: state.StagePRMerge, TerminalKind: "subagent_crash", PRNumber: 1}},
		{"cost-cap kill", StageFailure{Stage: state.StagePRMerge, TerminalKind: "stall_kill", PRNumber: 1, StageError: "[cost-cap-exceeded] cap=5.00"}},
		{"no PR", StageFailure{Stage: state.StagePRMerge, TerminalKind: "stall_kill", PRNumber: 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a.Matches(c.f) {
				t.Errorf("expected no match")
			}
		})
	}
}

func TestAction_StallKilledOnPRMerge_RunnerPunt(t *testing.T) {
	a := NewStallKilledOnPRMerge(&fakePRMergeRunner{
		res: pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 200, Reason: pmstages.ReasonNotMergeable},
	})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, TerminalKind: "stall_kill", PRNumber: 200,
	})
	if res.Recovered {
		t.Error("expected no recovery on runner punt")
	}
}
