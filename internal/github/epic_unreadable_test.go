package github_test

import (
	"context"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// TestPlanWaves_UnreadableIssueFailsThePlan: #2 blocks #3, and #2 cannot be
// read at all. PlanWaves warned on stderr, left #2 out and planned #3 into the
// first wave, as if nothing blocked it. A read that fails on its first page
// fails the plan as one cut short on a later page does.
func TestPlanWaves_UnreadableIssueFailsThePlan(t *testing.T) {
	fake := githubtest.New(t, map[int]githubtest.Issue{
		1: {},
		2: {BlockedBy: []int{1}},
		3: {BlockedBy: []int{2}},
	})
	fake.Unreadable = []int{2}

	res, err := gh.NewEpicService(fake.Client()).PlanWaves(context.Background(), githubtest.Owner, githubtest.Repo, []int{1, 2, 3})
	if err == nil || !strings.Contains(err.Error(), "#2") {
		t.Fatalf("PlanWaves err = %v (plan %+v), want an error naming #2", err, res)
	}
	if res != nil {
		t.Fatalf("PlanWaves returned a plan alongside a failed read: %+v", res)
	}
}

// TestValidate_ReportsTheSubIssuesItCouldNotCheck: epic #100 lists #101, #102
// and #103, and #102 cannot be read. Validate warned on stderr that #102 was
// missing from the batch, checked the other two and reported the epic valid,
// "3 sub-issues checked". It now counts the two it checked and the one it
// could not, names #102, and does not call the epic valid.
func TestValidate_ReportsTheSubIssuesItCouldNotCheck(t *testing.T) {
	fake := githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{101, 102, 103}},
		101: {},
		102: {},
		103: {},
	})
	fake.Unreadable = []int{102}

	res, err := gh.NewEpicService(fake.Client()).Validate(context.Background(), githubtest.Owner, githubtest.Repo, 100)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.Valid {
		t.Fatalf("result = %+v, want not valid with #102 unchecked", res)
	}
	if res.TotalSubIssues != 3 || res.CheckedSubIssues != 2 || res.FailedSubIssues != 1 {
		t.Fatalf("listed %d, checked %d, failed %d; want 3, 2, 1", res.TotalSubIssues, res.CheckedSubIssues, res.FailedSubIssues)
	}
	if len(res.Failures) != 1 || res.Failures[0].SubIssueNumber != 102 || res.Failures[0].SubIssueRepo != "acme/widgets" {
		t.Fatalf("failures = %+v, want acme/widgets#102", res.Failures)
	}
	if res.Failures[0].Error == "" {
		t.Errorf("failure for #102 names no reason")
	}
	if len(res.Gaps) != 0 {
		t.Errorf("gaps = %+v, want none among the sub-issues checked", res.Gaps)
	}
}
