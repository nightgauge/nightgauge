package stages

import (
	"context"
	"errors"
	"testing"
)

// stateGh is a direct-merge ghClient that also reads the PR body and the
// issue state (#3151). states drains one value per IssueState call, so a test
// can open the issue at the first read and close it at a later one.
type stateGh struct {
	fakeGh
	prBody     string
	states     []string
	stateErr   error
	stateCalls int
}

func (s *stateGh) PRBody(context.Context, int) (string, error)    { return s.prBody, nil }
func (s *stateGh) IssueBody(context.Context, int) (string, error) { return "", nil }
func (s *stateGh) EditPRBody(context.Context, int, string) error  { return nil }
func (s *stateGh) IssueState(context.Context, int) (string, error) {
	s.stateCalls++
	if s.stateErr != nil {
		return "", s.stateErr
	}
	i := s.stateCalls - 1
	if i >= len(s.states) {
		i = len(s.states) - 1
	}
	return s.states[i], nil
}

func newStateGh(body string, states ...string) *stateGh {
	return &stateGh{
		fakeGh: fakeGh{
			preMerge: PRViewSnapshot{
				State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
				ReviewDecision: "APPROVED",
			},
			postMerge: PRViewSnapshot{State: "MERGED"},
		},
		prBody: body,
		states: states,
	}
}

func TestPRMerge_IssueClosedDuringWait_ClosesBody_Refuses(t *testing.T) {
	gh := newStateGh("Closes #100", "CLOSED")
	res, err := newRunnerWith(gh, 42).Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathRefused || res.Reason != ReasonIssueClosed {
		t.Fatalf("result = %+v, want PathRefused/%s", res, ReasonIssueClosed)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times; a closed issue must not be merged", gh.mergeCalls)
	}
	if gh.stateCalls != 1 {
		t.Errorf("IssueState calls = %d, want 1", gh.stateCalls)
	}
}

func TestPRMerge_IssueClosed_RefsBody_Merges(t *testing.T) {
	gh := newStateGh("Refs #100", "CLOSED")
	res, _ := newRunnerWith(gh, 42).Run(context.Background(), 100, "owner/repo", "/tmp")
	if res.Path != PathMerged || gh.mergeCalls != 1 {
		t.Fatalf("result = %+v, merges = %d; a Refs body must merge", res, gh.mergeCalls)
	}
}

func TestPRMerge_IssueOpen_Merges(t *testing.T) {
	gh := newStateGh("Closes #100", "OPEN")
	res, _ := newRunnerWith(gh, 42).Run(context.Background(), 100, "owner/repo", "/tmp")
	if res.Path != PathMerged || gh.mergeCalls != 1 {
		t.Fatalf("result = %+v, merges = %d; an open issue must merge", res, gh.mergeCalls)
	}
}

func TestPRMerge_IssueStateReadError_FailsOpen(t *testing.T) {
	gh := newStateGh("Closes #100", "CLOSED")
	gh.stateErr = errors.New("boom")
	res, _ := newRunnerWith(gh, 42).Run(context.Background(), 100, "owner/repo", "/tmp")
	if res.Path != PathMerged || gh.mergeCalls != 1 {
		t.Fatalf("result = %+v, merges = %d; a failed read must not refuse", res, gh.mergeCalls)
	}
}
