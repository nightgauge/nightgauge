package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// These tests pin how the scheduler treats an issue whose relationship
// connections could not be read whole (github.ErrConnectionTruncated). Where
// blockers decide dispatch the read is a hard error; where only an issue's
// state is needed the read skips relationships, so it can never fail on them.

// truncatedRead is the error GetIssue returns when a follow-up page of one of
// the issue's connections could not be read.
func truncatedRead(number int) error {
	return fmt.Errorf("fetch issue #%d: blocking of Org/repo#%d: %w: read page 2: status 502",
		number, number, gh.ErrConnectionTruncated)
}

// TestEnqueueEpic_TruncatedSubIssueReadIsAnError: epic #100 has sub-issues
// #201 and #202, and #202's relationships cannot be read whole. Enqueuing
// #202 anyway gave it no sub-issue blockers, so DequeueIndependent could
// dispatch it beside a blocker that is still open.
func TestEnqueueEpic_TruncatedSubIssueReadIsAnError(t *testing.T) {
	mock := newMockIssueSvc()
	mock.addIssue("Org", "repo", 100, &types.Issue{
		NodeID: "I_100", Number: 100, Title: "Epic", State: "OPEN", Repo: "Org/repo",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_201", Number: 201, Title: "X", State: "OPEN", Repo: "Org/repo"},
			{NodeID: "I_202", Number: 202, Title: "Y", State: "OPEN", Repo: "Org/repo"},
		},
	})
	mock.addIssue("Org", "repo", 201, &types.Issue{NodeID: "I_201", Number: 201, Title: "X", State: "OPEN", Repo: "Org/repo"})
	mock.getErrs = map[string]error{"Org/repo#202": truncatedRead(202)}
	s := &Scheduler{
		issueSvc:    mock,
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}

	err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Epic", nil, nil)
	if !errors.Is(err, gh.ErrConnectionTruncated) {
		t.Fatalf("EnqueueEpic err = %v, want ErrConnectionTruncated", err)
	}
	if len(s.queue) != 0 {
		t.Fatalf("queue = %+v, want nothing enqueued from a truncated read", s.queue)
	}
}

// TestFetchSubIssueDetails_TruncatedSubIssueReadIsAnError: leaving a sub-issue
// out of the wave plan also drops its siblings' edges to it, so they would run
// in an earlier wave than it allows.
func TestFetchSubIssueDetails_TruncatedSubIssueReadIsAnError(t *testing.T) {
	issueSvc := newMockEpicIssueSvc()
	issueSvc.addEpic("Org", "repo", 100, &types.EpicProgress{
		Number: 100, Repo: "Org/repo", Total: 2, Open: 2,
		SubIssues: []types.SubIssueRef{
			{Number: 101, Title: "A", State: "OPEN", Repo: "Org/repo"},
			{Number: 102, Title: "B", State: "OPEN", Repo: "Org/repo"},
		},
	})
	issueSvc.addIssue("Org", "repo", 101, &types.Issue{Number: 101, Title: "A",
		BlockedBy: []types.BlockingRef{{Number: 102, State: "OPEN"}}})
	issueSvc.errs = map[string]error{"Org/repo#102": truncatedRead(102)}
	s := buildWaveTestScheduler(t, t.TempDir(), issueSvc, &trackingStageRunner{behavior: "succeed"})
	wo := newWaveOrchestrator(s, 100, "Org/repo", 4, 0)

	subIssues, details, err := wo.fetchSubIssueDetails(context.Background(), "Org", "repo",
		types.BoardItem{Number: 100, Repo: "Org/repo"})
	if !errors.Is(err, gh.ErrConnectionTruncated) {
		t.Fatalf("fetchSubIssueDetails err = %v, want ErrConnectionTruncated", err)
	}
	if subIssues != nil || details != nil {
		t.Fatalf("returned a plan input (%d sub-issues, %d details) alongside a truncated read", len(subIssues), len(details))
	}
}

// TestEnsureEpicBranchForItem_EpicTitleReadSkipsRelationships: with no board
// ParentTitle, the epic's title is read from GitHub to name its branch. Only
// the title is used, so an epic whose sub-issue list cannot be read whole must
// still get its branch. Reading that list failed the create at "fetch epic
// title" and sent the sub-issue's run on without its epic base branch.
func TestEnsureEpicBranchForItem_EpicTitleReadSkipsRelationships(t *testing.T) {
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "true")
	root := gitWorkspace(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, root, "init", "--bare", remote)
	gitIn(t, root, "remote", "add", "origin", remote)
	gitIn(t, root, "push", "origin", "main")

	mock := newMockIssueSvc()
	mock.addIssue("Org", "repo", 2000, &types.Issue{Number: 2000, Title: "Big Epic", State: "OPEN"})
	mock.truncated = map[string]gh.IssueRelations{"Org/repo#2000": gh.RelationSubIssues}
	s := &Scheduler{issueSvc: mock}

	failure := s.ensureEpicBranchForItem(context.Background(), root,
		types.BoardItem{Number: 2001, ParentNumber: 2000, Repo: "Org/repo"})
	if failure != "" {
		t.Fatalf("epic branch failure = %q, want the branch created from the epic's title", failure)
	}
	out, err := gittest.Command(root, "ls-remote", "--heads", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "refs/heads/epic/2000-big-epic") {
		t.Fatalf("remote heads:\n%s\nwant epic/2000-big-epic, named from the epic's title", out)
	}
}

// TestBlockerStateReadsSkipRelationships: refreshBlockerStates and
// resolveIssueStatesByKey read only each issue's State. Reading it through the
// relationship read meant a blocker's own long relationship list could fail
// the refresh, leaving a closed blocker recorded OPEN, or an OPEN one
// unresolved.
func TestBlockerStateReadsSkipRelationships(t *testing.T) {
	newMock := func() *mockIssueSvc {
		mock := newMockIssueSvc()
		mock.addIssue("test", "repo-a", 50, &types.Issue{Number: 50, State: "CLOSED"})
		mock.addIssue("test", "repo-a", 51, &types.Issue{Number: 51, State: "OPEN"})
		mock.relationReadErr = fmt.Errorf("batch fetch issues: %w", gh.ErrConnectionTruncated)
		return mock
	}

	t.Run("refreshBlockerStates", func(t *testing.T) {
		mock := newMock()
		s := &Scheduler{issueSvc: mock}
		s.queue = []QueueItem{{Repo: "test/repo-a", IssueNumber: 100,
			BlockedBy: []QueueBlockingRef{{Number: 50, State: "OPEN"}, {Number: 51, State: "OPEN"}}}}

		s.refreshBlockerStates(context.Background())

		got := map[int]string{}
		for _, b := range s.queue[0].BlockedBy {
			got[b.Number] = b.State
		}
		if got[50] != "CLOSED" || got[51] != "OPEN" {
			t.Fatalf("blocker states = %v, want #50 CLOSED and #51 OPEN", got)
		}
	})

	t.Run("resolveIssueStatesByKey", func(t *testing.T) {
		resolved := resolveIssueStatesByKey(context.Background(), newMock(),
			[]string{"test/repo-a#50", "test/repo-a#51"})
		if resolved["test/repo-a#50"] != "CLOSED" || resolved["test/repo-a#51"] != "OPEN" {
			t.Fatalf("resolved = %v, want #50 CLOSED and #51 OPEN", resolved)
		}
	})
}
