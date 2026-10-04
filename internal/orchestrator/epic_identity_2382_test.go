package orchestrator

import (
	"context"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// #2382: an issue number names an issue only within one repository, so the
// drag whitelist and queue removal name an issue by repository and number.

// newCrossRepoEpicFixture hosts epic example-org/platform#20 with sub-issues
// example-org/platform#21 and example-org/app#21: the same number in two
// repositories.
func newCrossRepoEpicFixture(t *testing.T) *Scheduler {
	t.Helper()
	mock := newMockIssueSvc()
	mock.addIssue("example-org", "platform", 20, &types.Issue{
		NodeID: "I_epic20", Number: 20, Title: "Platform Epic", State: "OPEN",
		Repo: "example-org/platform",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_p21", Number: 21, Title: "Platform 21", State: "OPEN", Repo: "example-org/platform"},
			{NodeID: "I_a21", Number: 21, Title: "App 21", State: "OPEN", Repo: "example-org/app"},
		},
	})
	mock.addIssue("example-org", "platform", 21, &types.Issue{NodeID: "I_p21", Number: 21, Title: "Platform 21", State: "OPEN", Repo: "example-org/platform"})
	mock.addIssue("example-org", "app", 21, &types.Issue{NodeID: "I_a21", Number: 21, Title: "App 21", State: "OPEN", Repo: "example-org/app"})
	return &Scheduler{
		issueSvc:    mock,
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}
}

func TestEnqueueEpic_EligibleNamesRepository_2382(t *testing.T) {
	cases := []struct {
		name     string
		eligible []IssueRef
		wantRepo string
	}{
		{"the epic's own repository", []IssueRef{{Repo: "example-org/platform", Number: 21}}, "example-org/platform"},
		{"an empty repository is the epic's", []IssueRef{{Number: 21}}, "example-org/platform"},
		{"another repository, case-insensitively", []IssueRef{{Repo: "Example-Org/App", Number: 21}}, "example-org/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCrossRepoEpicFixture(t)
			if err := s.EnqueueEpic(context.Background(), "example-org", "platform", 20, "Platform Epic", nil, tc.eligible); err != nil {
				t.Fatalf("EnqueueEpic: %v", err)
			}
			if len(s.queue) != 1 {
				t.Fatalf("queue has %d items, want 1: %+v", len(s.queue), s.queue)
			}
			if got := s.queue[0]; got.Repo != tc.wantRepo || got.IssueNumber != 21 {
				t.Fatalf("queued %s#%d, want %s#21", got.Repo, got.IssueNumber, tc.wantRepo)
			}
		})
	}
}

func TestQueueRemove_ScopedToRepository_2382(t *testing.T) {
	newQueue := func() *Scheduler {
		s := &Scheduler{repoRunning: make(map[string]int), mergeLocks: make(map[string]*sync.Mutex)}
		s.queue = []QueueItem{
			{Repo: "example-org/platform", IssueNumber: 21, Status: "pending"},
			{Repo: "example-org/app", IssueNumber: 21, Status: "pending"},
			{Repo: "example-org/app", IssueNumber: 22, Status: "pending"},
		}
		return s
	}

	s := newQueue()
	s.QueueRemove("Example-Org/Platform", 21)
	if len(s.queue) != 2 {
		t.Fatalf("queue has %d items after a scoped remove, want 2", len(s.queue))
	}
	for _, it := range s.queue {
		if it.Repo == "example-org/platform" {
			t.Fatalf("the named repository's item is still queued: %+v", it)
		}
	}
	if s.queue[0].Repo != "example-org/app" || s.queue[0].IssueNumber != 21 {
		t.Fatalf("the other repository's #21 was removed: %+v", s.queue)
	}

	// An empty repository keeps the CLI's by-number form.
	s = newQueue()
	s.QueueRemove("", 21)
	if len(s.queue) != 1 || s.queue[0].IssueNumber != 22 {
		t.Fatalf("by-number remove left %+v, want only #22", s.queue)
	}
}
