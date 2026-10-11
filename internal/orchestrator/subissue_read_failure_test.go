package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/github/githubtest"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// These tests pin that epic enqueue and wave planning fail closed when a
// sub-issue cannot be read at all, as they already did when its blocker list
// was cut short on a later page (relation_truncation_test.go). Both used to
// log a warning and go on without the sub-issue's blockers or without the
// sub-issue.

// TestEnqueueEpic_UnreadableSubIssueIsAnError: epic #100 has sub-issues #201
// and #202, #202 is blocked by #201, and #202 cannot be read. EnqueueEpic
// enqueued #202 with no blockers, so DequeueIndependent could dispatch it
// beside #201.
func TestEnqueueEpic_UnreadableSubIssueIsAnError(t *testing.T) {
	fake := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{201, 202}},
		201: {},
		202: {BlockedBy: []int{201}},
	})
	fake.Unreadable = []int{202}
	s := forgeScheduler(fake)

	err := s.EnqueueEpic(context.Background(), githubtest.Owner, githubtest.Repo, 100, "Epic", nil, nil, "")
	if err == nil || !strings.Contains(err.Error(), "sub-issue acme/widgets#202") {
		t.Fatalf("EnqueueEpic err = %v, want an error naming sub-issue acme/widgets#202", err)
	}
	if len(s.queue) != 0 {
		t.Fatalf("queue = %+v, want nothing enqueued when a sub-issue cannot be read", s.queue)
	}
}

// TestEnqueueEpic_SubIssuesItWillNotEnqueueAreNotRead: a sub-issue the
// enqueue leaves out (outside the drag path's whitelist, or human-only) is
// not read, so failing to read it cannot fail the enqueue of the others.
func TestEnqueueEpic_SubIssuesItWillNotEnqueueAreNotRead(t *testing.T) {
	readFailure := errors.New("status 502")

	t.Run("outside the whitelist", func(t *testing.T) {
		s := newEpicFilterFixture(t)
		mock := s.issueSvc.(*mockIssueSvc)
		mock.getErrs = map[string]error{"Org/repo#202": readFailure, "Org/repo#203": readFailure}

		err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil,
			[]IssueRef{{Number: 201}}, "")
		if err != nil {
			t.Fatalf("EnqueueEpic: %v", err)
		}
		if len(s.queue) != 1 || s.queue[0].IssueNumber != 201 {
			t.Fatalf("queue = %+v, want only #201", s.queue)
		}
	})

	t.Run("human-only", func(t *testing.T) {
		s := newEpicFilterFixture(t)
		s.excludeLabels = []string{"owner-action"}
		mock := s.issueSvc.(*mockIssueSvc)
		mock.issues["Org/repo#100"].SubIssues[1].Labels = []string{"owner-action"}
		mock.getErrs = map[string]error{"Org/repo#202": readFailure}

		if err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, nil, ""); err != nil {
			t.Fatalf("EnqueueEpic: %v", err)
		}
		var got []int
		for _, item := range s.queue {
			got = append(got, item.IssueNumber)
		}
		if len(got) != 2 || got[0] != 201 || got[1] != 203 {
			t.Fatalf("queued %v, want #201 and #203", got)
		}
	})
}

// TestFetchSubIssueDetails_UnreadableSubIssueIsAnError: #102 is blocked by
// #101 and cannot be read. The wave plan left #102 out with a warning, so the
// epic ran without it and reported every wave a success.
func TestFetchSubIssueDetails_UnreadableSubIssueIsAnError(t *testing.T) {
	fake := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{101, 102}},
		101: {},
		102: {BlockedBy: []int{101}},
	})
	fake.Unreadable = []int{102}
	wo := newWaveOrchestrator(forgeScheduler(fake), 100, "acme/widgets", 4, 0)

	subIssues, details, blockedBy, err := wo.fetchSubIssueDetails(context.Background(), githubtest.Owner, githubtest.Repo,
		types.BoardItem{Number: 100, Repo: "acme/widgets"})
	if err == nil || !strings.Contains(err.Error(), "acme/widgets#102") {
		t.Fatalf("fetchSubIssueDetails err = %v, want an error naming acme/widgets#102", err)
	}
	if subIssues != nil || details != nil || blockedBy != nil {
		t.Fatalf("returned a plan input (%d sub-issues, %d details, %d blocker lists) alongside a failed read",
			len(subIssues), len(details), len(blockedBy))
	}
}
