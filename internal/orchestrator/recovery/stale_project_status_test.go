package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func stubExecNightgauge(t *testing.T, fn func(ctx context.Context, args ...string) ([]byte, error)) {
	t.Helper()
	prev := execNightgauge
	execNightgauge = fn
	t.Cleanup(func() { execNightgauge = prev })
}

func TestAction_StaleProjectStatus_Matches_AndRecovers(t *testing.T) {
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "issue view") {
			return []byte(`{"state":"CLOSED"}`), nil
		}
		return nil, nil
	})
	syncCalled := 0
	stubExecNightgauge(t, func(_ context.Context, args ...string) ([]byte, error) {
		syncCalled++
		want := []string{"project", "move-status", "42", "done"}
		for i, w := range want {
			if i >= len(args) || args[i] != w {
				t.Errorf("arg[%d]=%q, want %q", i, args[i], w)
			}
		}
		return nil, nil
	})

	a := NewStaleProjectStatus()
	failure := StageFailure{IssueNumber: 42, Reason: "stale-project-status"}
	if !a.Matches(failure) {
		t.Fatal("expected match")
	}
	res := a.Execute(context.Background(), failure)
	if !res.Recovered {
		t.Fatalf("expected Recovered=true; got %q", res.Reason)
	}
	if syncCalled != 1 {
		t.Errorf("project move-status calls = %d, want 1", syncCalled)
	}
}

func TestAction_StaleProjectStatus_NoMatch_FallsThrough(t *testing.T) {
	a := NewStaleProjectStatus()
	cases := []struct {
		name string
		f    StageFailure
	}{
		{"no issue number", StageFailure{Reason: "stale-project-status"}},
		{"reason unrelated", StageFailure{IssueNumber: 1, Reason: "something else"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a.Matches(c.f) {
				t.Errorf("expected no match")
			}
		})
	}
}

func TestAction_StaleProjectStatus_IssueStillOpen(t *testing.T) {
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"OPEN"}`), nil
	})
	a := NewStaleProjectStatus()
	res := a.Execute(context.Background(), StageFailure{IssueNumber: 42, Reason: "stale-project-status"})
	if res.Recovered {
		t.Error("expected no recovery when issue is still OPEN")
	}
}

func TestAction_StaleProjectStatus_GhFailure(t *testing.T) {
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("gh down")
	})
	a := NewStaleProjectStatus()
	res := a.Execute(context.Background(), StageFailure{IssueNumber: 42, Reason: "stale-project-status"})
	if res.Recovered {
		t.Error("expected no recovery on gh failure")
	}
}
