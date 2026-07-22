package recovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// stubExecGh swaps the package-level execGh for the duration of the test.
// Mirrors the gates package pattern.
func stubExecGh(t *testing.T, fn func(ctx context.Context, args ...string) ([]byte, error)) {
	t.Helper()
	prev := execGh
	execGh = fn
	t.Cleanup(func() { execGh = prev })
}

func TestAction_CICheckTransientlyFailed_Matches_AndRecovers(t *testing.T) {
	views := 0
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "pr view"):
			return []byte(`{"statusCheckRollup":[{"name":"unit","conclusion":"FAILURE","workflowRun":{"databaseId":4242}}]}`), nil
		case strings.HasPrefix(joined, "run rerun"):
			return []byte(``), nil
		case strings.HasPrefix(joined, "run view"):
			views++
			// First view: still running. Second: completed/SUCCESS.
			if views == 1 {
				return []byte(`{"status":"in_progress","conclusion":""}`), nil
			}
			return []byte(`{"status":"completed","conclusion":"SUCCESS"}`), nil
		}
		return nil, nil
	})

	a := &CICheckTransientlyFailed{pollInterval: time.Millisecond, pollMax: 5}
	failure := StageFailure{
		Stage:          state.StagePRMerge,
		GateKind:       gates.KindNoOp,
		PRNumber:       100,
		Reason:         "failed-ci-checks: unit",
		AttemptOrdinal: 1,
	}
	if !a.Matches(failure) {
		t.Fatalf("expected Matches=true; reason=%q", failure.Reason)
	}
	res := a.Execute(context.Background(), failure)
	if !res.Recovered {
		t.Fatalf("expected Recovered=true; got reason=%q", res.Reason)
	}
	if res.Action != "ci-check-transiently-failed" {
		t.Errorf("Action = %q", res.Action)
	}
}

func TestAction_CICheckTransientlyFailed_NoMatch_FallsThrough(t *testing.T) {
	a := NewCICheckTransientlyFailed()
	cases := []struct {
		name string
		f    StageFailure
	}{
		{"wrong stage", StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp, PRNumber: 1, Reason: "failed-ci-checks"}},
		{"no PR", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 0, Reason: "failed-ci-checks"}},
		{"already attempted", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, Reason: "failed-ci-checks", AttemptOrdinal: 2}},
		{"reason mentions OPEN, not CI", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, Reason: "PR is not MERGED (state=OPEN)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a.Matches(c.f) {
				t.Errorf("expected no match")
			}
		})
	}
}

func TestAction_CICheckTransientlyFailed_MultipleFailuresDecline(t *testing.T) {
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[
			{"name":"unit","conclusion":"FAILURE","workflowRun":{"databaseId":1}},
			{"name":"lint","conclusion":"ERROR","workflowRun":{"databaseId":2}}
		]}`), nil
	})
	a := &CICheckTransientlyFailed{pollInterval: time.Millisecond, pollMax: 1}
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 100, Reason: "failed-ci-checks", AttemptOrdinal: 1,
	})
	if res.Recovered {
		t.Errorf("expected no recovery on multi-fail")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage", res.FollowUp)
	}
}

func TestAction_CICheckTransientlyFailed_RerunFailsHard(t *testing.T) {
	views := 0
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "pr view"):
			return []byte(`{"statusCheckRollup":[{"name":"unit","conclusion":"FAILURE","workflowRun":{"databaseId":4242}}]}`), nil
		case strings.HasPrefix(joined, "run rerun"):
			return []byte(``), nil
		case strings.HasPrefix(joined, "run view"):
			views++
			return []byte(`{"status":"completed","conclusion":"FAILURE"}`), nil
		}
		return nil, nil
	})
	a := &CICheckTransientlyFailed{pollInterval: time.Millisecond, pollMax: 2}
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 100, Reason: "failed-ci-checks", AttemptOrdinal: 1,
	})
	if res.Recovered {
		t.Errorf("expected no recovery when rerun also fails")
	}
}
