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
