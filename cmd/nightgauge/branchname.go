package main

import (
	"context"
	"fmt"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// issueFetcher is the one IssueService call branch-create makes to name a
// branch.
type issueFetcher interface {
	GetIssueWithRelations(ctx context.Context, owner, repo string, number int, rels gh.IssueRelations) (*types.Issue, error)
}

// branchNameForIssue fetches issue number and composes its branch name.
// Naming a branch reads the issue's labels, title and parent, never its
// relationship lists, so none is read whole here.
//
// Both halves refuse an incomplete issue: the fetch rejects a partial response,
// and the composer rejects a title that leaves no slug. Before #1915 an issue
// fetched without its title became `fix/1911-`, and branch-create pushed it.
func branchNameForIssue(ctx context.Context, issues issueFetcher, owner, repo string, number int) (*types.Issue, string, error) {
	fetched, err := issues.GetIssueWithRelations(ctx, owner, repo, number, gh.NoRelations)
	if err != nil {
		return nil, "", err
	}
	name, err := gitpkg.ComposeBranchName(fetched.Labels, number, fetched.Title)
	if err != nil {
		return nil, "", fmt.Errorf("branch-create %s/%s#%d: %w", owner, repo, number, err)
	}
	return fetched, name, nil
}

// epicBranchParentFor is the parent epic number branch-create bases issue's
// branch on: the parent's number when it lives in issue's own repository
// (owner/repo), and 0 when it lives in another. An epic branch epic/<N>-*
// names #N of the repository it is pushed to, so in this one it is the branch
// of this repository's own #N, not of the parent's (#2377).
func epicBranchParentFor(issue *types.Issue, owner, repo string) int {
	issueRepo := issue.Repo
	if issueRepo == "" {
		issueRepo = owner + "/" + repo
	}
	return gitpkg.EpicBranchParent(issueRepo, issue.ParentIssueNumber, issue.ParentIssueRepo)
}
