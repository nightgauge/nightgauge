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
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// These tests pin how the scheduler treats an issue whose relationship
// connections could not be read whole (github.ErrConnectionTruncated). Where
// blockers decide dispatch the blocker list is read whole and a truncated one
// is a hard error; a list the scheduler does not use is never read, so it can
// never fail the read.

// truncatedRead is the error the real read returns when a follow-up page of
// one of the issue's connections could not be read.
func truncatedRead(number int) error {
	return fmt.Errorf("fetch issue #%d: a relationship list of Org/repo#%d: %w: read page 2: status 502",
		number, number, gh.ErrConnectionTruncated)
}

// forgeScheduler returns a scheduler whose issue reads go through the real
// IssueService to forge, which fails every relationship page after the first.
func forgeScheduler(forge *githubtest.Forge) *Scheduler {
	return &Scheduler{
		issueSvc:    gh.NewIssueService(forge.Client()),
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}
}

// TestEnqueueEpic_TruncatedBlockerListIsAnError: epic #100 has sub-issues #201
// and #202, and #202 has 11 blockers, more than the first page holds, whose
// later page cannot be read. Enqueuing #202 with only the blockers on the
// first page would let DequeueIndependent dispatch it beside one that is still
// open.
func TestEnqueueEpic_TruncatedBlockerListIsAnError(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{201, 202}},
		201: {},
		202: {BlockedBy: githubtest.Numbers(300, 11)},
	})
	s := forgeScheduler(forge)

	err := s.EnqueueEpic(context.Background(), githubtest.Owner, githubtest.Repo, 100, "Epic", nil, nil)
	if !errors.Is(err, gh.ErrConnectionTruncated) {
		t.Fatalf("EnqueueEpic err = %v, want ErrConnectionTruncated", err)
	}
	if !strings.Contains(err.Error(), "blockedBy of acme/widgets#202") {
		t.Errorf("err = %v, want it to name #202's blocker list", err)
	}
	if len(s.queue) != 0 {
		t.Fatalf("queue = %+v, want nothing enqueued from a truncated read", s.queue)
	}
}

// TestEnqueueEpic_LongListsItDoesNotUseCannotAbortIt: the epic and both of its
// sub-issues each block 7 issues, more than the first page holds, and every
// later page fails. Enqueuing uses only the epic's sub-issue and blocker lists
// and each sub-issue's blocker list, all of which fit their first page, so it
// must read no later page and enqueue both sub-issues with their blockers.
// When it read every list, one failed page of a blocking list enqueued
// nothing and reported a blocker problem on a sub-issue whose blockers were
// complete.
func TestEnqueueEpic_LongListsItDoesNotUseCannotAbortIt(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{201, 202}, Blocking: githubtest.Numbers(900, 7)},
		201: {Blocking: githubtest.Numbers(910, 7)},
		202: {BlockedBy: []int{201, 301, 302}, Blocking: githubtest.Numbers(920, 7)},
	})
	s := forgeScheduler(forge)

	if err := s.EnqueueEpic(context.Background(), githubtest.Owner, githubtest.Repo, 100, "Epic", nil, nil); err != nil {
		t.Fatalf("EnqueueEpic: %v", err)
	}
	if len(s.queue) != 2 || s.queue[0].IssueNumber != 201 || s.queue[1].IssueNumber != 202 {
		t.Fatalf("queue = %+v, want #201 and #202 enqueued", s.queue)
	}
	var blockers []int
	for _, b := range s.queue[1].BlockedBy {
		blockers = append(blockers, b.Number)
	}
	if fmt.Sprint(blockers) != "[201 301 302]" {
		t.Errorf("#202 blockers = %v, want [201 301 302]", blockers)
	}
	if n := forge.FollowUps(); n != 0 {
		t.Errorf("EnqueueEpic read %d later page(s) of lists it does not use", n)
	}
}

// TestFetchSubIssueDetails_TruncatedBlockerListIsAnError: a sub-issue's
// blocker list read only in part would plan it into an earlier wave than its
// unseen blockers allow, and leaving the sub-issue out of the plan would drop
// its siblings' edges to it as well.
func TestFetchSubIssueDetails_TruncatedBlockerListIsAnError(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{101, 102}},
		101: {BlockedBy: []int{102}},
		102: {BlockedBy: githubtest.Numbers(300, 11)},
	})
	wo := newWaveOrchestrator(forgeScheduler(forge), 100, "acme/widgets", 4, 0)

	subIssues, details, err := wo.fetchSubIssueDetails(context.Background(), githubtest.Owner, githubtest.Repo,
		types.BoardItem{Number: 100, Repo: "acme/widgets"})
	if !errors.Is(err, gh.ErrConnectionTruncated) {
		t.Fatalf("fetchSubIssueDetails err = %v, want ErrConnectionTruncated", err)
	}
	if subIssues != nil || details != nil {
		t.Fatalf("returned a plan input (%d sub-issues, %d details) alongside a truncated read", len(subIssues), len(details))
	}
}

// TestFetchSubIssueDetails_LongListsItDoesNotUseCannotFailIt: both sub-issues
// block 7 issues and every later page fails. The wave plan uses each
// sub-issue's body, labels and blocker list, so the blocking lists must not be
// read and cannot fail the plan.
func TestFetchSubIssueDetails_LongListsItDoesNotUseCannotFailIt(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{101, 102}},
		101: {BlockedBy: []int{102}, Blocking: githubtest.Numbers(900, 7)},
		102: {Blocking: githubtest.Numbers(910, 7)},
	})
	wo := newWaveOrchestrator(forgeScheduler(forge), 100, "acme/widgets", 4, 0)

	subIssues, details, err := wo.fetchSubIssueDetails(context.Background(), githubtest.Owner, githubtest.Repo,
		types.BoardItem{Number: 100, Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("fetchSubIssueDetails: %v", err)
	}
	if len(subIssues) != 2 || len(details) != 2 {
		t.Fatalf("plan input = %d sub-issues, %d details; want 2 and 2", len(subIssues), len(details))
	}
	if fmt.Sprint(details[0].BlockedBy) != "[102]" {
		t.Errorf("#101 blockers = %v, want [102]", details[0].BlockedBy)
	}
	if n := forge.FollowUps(); n != 0 {
		t.Errorf("the plan read %d later page(s) of lists it does not use", n)
	}
}

// TestFindReadySubIssues_LongListsItDoesNotUseCannotHideAReadySubIssue: #101
// is open with no blockers and blocks 7 issues; #102 is blocked by #101. Only
// each sub-issue's blocker list decides readiness, so #101's blocking list
// must not be read: when it was, its failed later page skipped #101 and the
// epic reported no ready sub-issue.
func TestFindReadySubIssues_LongListsItDoesNotUseCannotHideAReadySubIssue(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{101, 102}},
		101: {Blocking: githubtest.Numbers(900, 7)},
		102: {BlockedBy: []int{101}},
	})

	ready, err := forgeScheduler(forge).FindReadySubIssues(context.Background(), githubtest.Owner, githubtest.Repo, 100)
	if err != nil {
		t.Fatalf("FindReadySubIssues: %v", err)
	}
	if len(ready) != 1 || ready[0].Number != 101 {
		t.Fatalf("ready = %+v, want only #101", ready)
	}
	if n := forge.FollowUps(); n != 0 {
		t.Errorf("FindReadySubIssues read %d later page(s) of lists it does not use", n)
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
