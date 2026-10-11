package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgauge/nightgauge/internal/github/githubtest"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestIssueViewMany_ReadsNoRelationshipList: issue.viewMany's one consumer,
// the epic dashboard's per-repository progress, reads number, title, labels
// and state. Against a forge that fails every relationship page after the
// first, #700's sub-issue, blocker and blocked lists, each longer than its
// first page, must not fail the batch and empty that repository's progress
// (#1707): the call succeeds, asks for no later page, and returns no list.
func TestIssueViewMany_ReadsNoRelationshipList(t *testing.T) {
	f := githubtest.New(t, map[int]githubtest.Issue{
		700: {
			Labels:    []string{"size:M"},
			SubIssues: githubtest.Numbers(1001, 60),
			BlockedBy: githubtest.Numbers(301, 11),
			Blocking:  githubtest.Numbers(401, 7),
		},
		701: {State: "CLOSED"},
	})
	s := newTestServer(&bytes.Buffer{})
	s.client = f.Client()
	s.resolver = NewClientResolver(s.client, true)
	s.registerMethods()

	params, _ := json.Marshal(IssueViewManyParams{
		Owner: githubtest.Owner, Repo: githubtest.Repo, Numbers: []int{701, 700},
	})
	res, err := s.methods["issue.viewMany"](context.Background(), params)
	if err != nil {
		t.Fatalf("issue.viewMany: %v", err)
	}
	issues, ok := res.([]*types.Issue)
	if !ok || len(issues) != 2 {
		t.Fatalf("result = %#v, want #701 and #700", res)
	}
	if issues[0].Number != 701 || issues[0].State != "CLOSED" {
		t.Errorf("first = %+v, want #701 CLOSED, in input order", issues[0])
	}
	got := issues[1]
	if got.Number != 700 || got.Title != "Issue 700" || got.State != "OPEN" || len(got.Labels) != 1 || got.Labels[0] != "size:M" {
		t.Errorf("#700 = %+v, want its number, title, state and size:M label", got)
	}
	if n := len(got.SubIssues) + len(got.BlockedBy) + len(got.Blocking); n != 0 || got.IsEpic {
		t.Errorf("#700 carries %d relationships (IsEpic %v); the read selects none", n, got.IsEpic)
	}
	if n := f.FollowUps(); n != 0 {
		t.Errorf("relationship pages requested = %d, want 0", n)
	}
}
