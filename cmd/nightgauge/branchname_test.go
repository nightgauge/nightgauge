package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

type stubIssueFetcher struct {
	issue *types.Issue
	err   error
}

func (s stubIssueFetcher) GetIssueWithRelations(context.Context, string, string, int, gh.IssueRelations) (*types.Issue, error) {
	return s.issue, s.err
}

func TestBranchNameForIssue_ComposesFromLabelsAndTitle(t *testing.T) {
	f := stubIssueFetcher{issue: &types.Issue{Number: 1911, Title: "fix(outcome): model still fails", Labels: []string{"type:bug"}}}
	_, name, err := branchNameForIssue(context.Background(), f, "o", "r", 1911)
	if err != nil {
		t.Fatalf("branchNameForIssue: %v", err)
	}
	if name != "fix/1911-fix-outcome-model-still-fails" {
		t.Errorf("name = %q", name)
	}
}

// The #1915 incident: labels arrived, the title did not, and the branch was
// named `fix/1911-`. branch-create must refuse and name the issue.
func TestBranchNameForIssue_EmptyTitleIsRefused(t *testing.T) {
	f := stubIssueFetcher{issue: &types.Issue{Number: 1911, Labels: []string{"type:bug"}}}
	fetched, name, err := branchNameForIssue(context.Background(), f, "o", "r", 1911)
	if err == nil {
		t.Fatalf("branchNameForIssue returned %q for an issue with no title, want an error", name)
	}
	if name != "" || fetched != nil {
		t.Errorf("a refused name still returned (%q, %v)", name, fetched)
	}
	if !strings.Contains(err.Error(), "o/r#1911") {
		t.Errorf("error does not name the issue: %v", err)
	}
}

func TestBranchNameForIssue_FetchErrorIsReturned(t *testing.T) {
	want := errors.New("fetch issue #1911: incomplete response from GitHub")
	_, _, err := branchNameForIssue(context.Background(), stubIssueFetcher{err: want}, "o", "r", 1911)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestEpicBranchParentFor_CrossRepoParent: branch-create bases a sub-issue
// on its parent's epic branch only when the parent lives in the sub-issue's
// repository. For a parent elsewhere, epic/<N>-* here is the branch of this
// repository's own #N, and the epic's title is not this repository's #N's
// either (#2377).
func TestEpicBranchParentFor_CrossRepoParent(t *testing.T) {
	cases := []struct {
		name  string
		issue types.Issue
		want  int
	}{
		{"parent in another repository",
			types.Issue{Repo: "example-org/app", ParentIssueNumber: 20, ParentIssueRepo: "example-org/platform"}, 0},
		{"parent in the same repository",
			types.Issue{Repo: "example-org/app", ParentIssueNumber: 20, ParentIssueRepo: "example-org/app"}, 20},
		{"issue repository from the flags when the read left it empty",
			types.Issue{ParentIssueNumber: 20, ParentIssueRepo: "example-org/app"}, 20},
		{"no parent", types.Issue{Repo: "example-org/app"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issue := c.issue
			if got := epicBranchParentFor(&issue, "example-org", "app"); got != c.want {
				t.Errorf("epicBranchParentFor = %d, want %d", got, c.want)
			}
		})
	}
}
