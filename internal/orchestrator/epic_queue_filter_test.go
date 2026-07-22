package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// newEpicFilterFixture builds a scheduler with a mock issue service hosting
// an epic #100 with three open sub-issues (#201, #202, #203). The callers
// vary only the eligibleSubIssues whitelist.
func newEpicFilterFixture(t *testing.T) *Scheduler {
	t.Helper()
	mock := newMockIssueSvc()
	mock.addIssue("Org", "repo", 100, &types.Issue{
		NodeID: "I_epic100",
		Number: 100,
		Title:  "Test Epic",
		State:  "OPEN",
		Repo:   "Org/repo",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_201", Number: 201, Title: "Sub A", State: "OPEN", Repo: "Org/repo"},
			{NodeID: "I_202", Number: 202, Title: "Sub B", State: "OPEN", Repo: "Org/repo"},
			{NodeID: "I_203", Number: 203, Title: "Sub C", State: "OPEN", Repo: "Org/repo"},
		},
	})
	mock.addIssue("Org", "repo", 201, &types.Issue{NodeID: "I_201", Number: 201, Title: "Sub A", State: "OPEN", Repo: "Org/repo"})
	mock.addIssue("Org", "repo", 202, &types.Issue{NodeID: "I_202", Number: 202, Title: "Sub B", State: "OPEN", Repo: "Org/repo"})
	mock.addIssue("Org", "repo", 203, &types.Issue{NodeID: "I_203", Number: 203, Title: "Sub C", State: "OPEN", Repo: "Org/repo"})

	return &Scheduler{
		issueSvc:    mock,
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}
}

// TestEnqueueEpic_NilEligibleSubIssues_QueuesAllOpen verifies the autonomous
// path behaviour: nil whitelist keeps every open sub-issue.
func TestEnqueueEpic_NilEligibleSubIssues_QueuesAllOpen(t *testing.T) {
	s := newEpicFilterFixture(t)

	if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, nil); err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 3 {
		t.Fatalf("queue has %d items, want 3", len(s.queue))
	}
	wanted := map[int]bool{201: true, 202: true, 203: true}
	for _, item := range s.queue {
		if !wanted[item.IssueNumber] {
			t.Errorf("unexpected issue in queue: #%d", item.IssueNumber)
		}
	}
}

// TestEnqueueEpic_UsesClientResolverPerRepo verifies that when a clientResolver
// is wired, EnqueueEpic resolves a client scoped to the epic's (owner, repo)
// rather than relying on the scheduler's startup client. A resolver error
// falls back to the default issueSvc so resolution never blocks enqueue (#3700).
func TestEnqueueEpic_UsesClientResolverPerRepo(t *testing.T) {
	s := newEpicFilterFixture(t)

	var calls int
	var gotOwner, gotRepo string
	s.WithClientResolver(func(_ context.Context, owner, repo string) (*gh.Client, error) {
		calls++
		gotOwner, gotRepo = owner, repo
		// Return an error so issueServiceFor falls back to the mock issueSvc,
		// keeping the test hermetic (no real client construction needed).
		return nil, fmt.Errorf("resolver stub")
	})

	if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, nil); err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if calls == 0 {
		t.Fatal("expected clientResolver to be invoked")
	}
	if gotOwner != "Org" || gotRepo != "repo" {
		t.Errorf("resolver called with %s/%s, want Org/repo", gotOwner, gotRepo)
	}
	// Graceful fallback to the mock issueSvc still queues all three sub-issues.
	if len(s.queue) != 3 {
		t.Fatalf("queue has %d items, want 3", len(s.queue))
	}
}

// TestEnqueueEpic_EmptyEligibleSubIssues_QueuesAllOpen documents that an
// empty slice (not nil) is also treated as "no filter" so callers can pass
// the result of a filter call without guarding against the zero case.
func TestEnqueueEpic_EmptyEligibleSubIssues_QueuesAllOpen(t *testing.T) {
	s := newEpicFilterFixture(t)

	if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, []int{}); err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 3 {
		t.Fatalf("queue has %d items, want 3", len(s.queue))
	}
}

// TestEnqueueEpic_EligibleSubset_OnlyWhitelisted verifies the drag path:
// only sub-issues in the whitelist are enqueued, and epicOrder is sequential
// across the filtered subset.
func TestEnqueueEpic_EligibleSubset_OnlyWhitelisted(t *testing.T) {
	s := newEpicFilterFixture(t)

	if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, []int{201, 203}); err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 2 {
		t.Fatalf("queue has %d items, want 2", len(s.queue))
	}
	if s.queue[0].IssueNumber != 201 || s.queue[1].IssueNumber != 203 {
		t.Fatalf("queue order = [%d, %d], want [201, 203]", s.queue[0].IssueNumber, s.queue[1].IssueNumber)
	}
	// EpicOrder must remain sequential across the filtered subset.
	if s.queue[0].EpicOrder == nil || *s.queue[0].EpicOrder != 0 {
		t.Errorf("queue[0].EpicOrder = %v, want 0", s.queue[0].EpicOrder)
	}
	if s.queue[1].EpicOrder == nil || *s.queue[1].EpicOrder != 1 {
		t.Errorf("queue[1].EpicOrder = %v, want 1", s.queue[1].EpicOrder)
	}
}

// TestEnqueueEpic_SkipsOwnerActionSubIssue verifies #317: a sub-issue labeled
// `owner-action` (human-only work) is never enqueued by epic expansion, even
// though it is open and would otherwise pass every other filter. The other
// two sub-issues still enqueue normally.
func TestEnqueueEpic_SkipsOwnerActionSubIssue(t *testing.T) {
	mock := newMockIssueSvc()
	mock.addIssue("Org", "repo", 100, &types.Issue{
		NodeID: "I_epic100",
		Number: 100,
		Title:  "Test Epic",
		State:  "OPEN",
		Repo:   "Org/repo",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_201", Number: 201, Title: "Sub A", State: "OPEN", Repo: "Org/repo"},
			{NodeID: "I_202", Number: 202, Title: "Rotate the leaked token", State: "OPEN", Repo: "Org/repo", Labels: []string{"owner-action"}},
			{NodeID: "I_203", Number: 203, Title: "Sub C", State: "OPEN", Repo: "Org/repo"},
		},
	})
	mock.addIssue("Org", "repo", 201, &types.Issue{NodeID: "I_201", Number: 201, Title: "Sub A", State: "OPEN", Repo: "Org/repo"})
	mock.addIssue("Org", "repo", 202, &types.Issue{NodeID: "I_202", Number: 202, Title: "Rotate the leaked token", State: "OPEN", Repo: "Org/repo", Labels: []string{"owner-action"}})
	mock.addIssue("Org", "repo", 203, &types.Issue{NodeID: "I_203", Number: 203, Title: "Sub C", State: "OPEN", Repo: "Org/repo"})

	s := &Scheduler{
		issueSvc:      mock,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		excludeLabels: []string{"owner-action"},
	}

	if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, nil); err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 2 {
		t.Fatalf("queue has %d items, want 2 (owner-action sub-issue skipped)", len(s.queue))
	}
	for _, item := range s.queue {
		if item.IssueNumber == 202 {
			t.Errorf("owner-action sub-issue #202 was enqueued; want skipped")
		}
	}
}

// TestEnqueueEpic_ExcludeLabelsConfigOverride verifies a custom exclude-label
// list is honored instead of the "owner-action" default: a sub-issue labeled
// "owner-action" is NOT in the configured list, so it enqueues normally, while
// one labeled "needs-human" (the configured entry) is skipped.
func TestEnqueueEpic_ExcludeLabelsConfigOverride(t *testing.T) {
	mock := newMockIssueSvc()
	mock.addIssue("Org", "repo", 100, &types.Issue{
		NodeID: "I_epic100",
		Number: 100,
		Title:  "Test Epic",
		State:  "OPEN",
		Repo:   "Org/repo",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_201", Number: 201, Title: "Owner action but not configured", State: "OPEN", Repo: "Org/repo", Labels: []string{"owner-action"}},
			{NodeID: "I_202", Number: 202, Title: "Needs a human", State: "OPEN", Repo: "Org/repo", Labels: []string{"needs-human"}},
		},
	})
	mock.addIssue("Org", "repo", 201, &types.Issue{NodeID: "I_201", Number: 201, Title: "Owner action but not configured", State: "OPEN", Repo: "Org/repo", Labels: []string{"owner-action"}})
	mock.addIssue("Org", "repo", 202, &types.Issue{NodeID: "I_202", Number: 202, Title: "Needs a human", State: "OPEN", Repo: "Org/repo", Labels: []string{"needs-human"}})

	s := &Scheduler{
		issueSvc:      mock,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		excludeLabels: []string{"needs-human"},
	}

	if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, nil); err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 1 {
		t.Fatalf("queue has %d items, want 1 (#201 enqueued, #202 skipped)", len(s.queue))
	}
	if s.queue[0].IssueNumber != 201 {
		t.Errorf("queue[0].IssueNumber = %d, want 201", s.queue[0].IssueNumber)
	}
}

// TestEnqueueEpic_EligibleUnknown_NoOp verifies that unknown numbers in the
// whitelist are simply ignored — the epic enqueue succeeds with an empty
// queue rather than erroring. This is the "missing from cache" fallback path.
func TestEnqueueEpic_EligibleUnknown_NoOp(t *testing.T) {
	s := newEpicFilterFixture(t)

	if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, []int{999, 1000}); err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 0 {
		t.Fatalf("queue has %d items, want 0", len(s.queue))
	}
}
