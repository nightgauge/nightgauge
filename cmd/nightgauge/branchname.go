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

// ensureBranchForIssue creates or reuses branchName in svc's checkout of
// owner/repo and checks it out. issue is the issue the branch is for, nil for
// a name that carries no issue number. Its parent epic is the base only when
// the epic lives in owner/repo (#2377); issues is asked for the epic's title
// only when that epic's branch has to be created. The branch itself comes
// from the one implementation shared with the scheduler's deterministic
// issue-pickup runner (#1904).
func ensureBranchForIssue(ctx context.Context, svc *gitpkg.Service, issues func() (issueFetcher, error),
	owner, repo string, issue *types.Issue, branchName string) (gitpkg.IssueBranchResult, error) {
	parentIssue := 0
	var epicTitle func() (string, error)
	if issue != nil {
		// 0 for a parent in another repository, so the title read below is
		// always this repository's #N.
		parentIssue = epicBranchParentFor(issue, owner, repo)
		epicTitle = func() (string, error) {
			fetcher, err := issues()
			if err != nil {
				return "", err
			}
			epic, err := fetcher.GetIssueWithRelations(ctx, owner, repo, parentIssue, gh.NoRelations)
			if err != nil {
				return "", err
			}
			return epic.Title, nil
		}
	}
	return svc.EnsureIssueBranch(branchName, parentIssue, epicTitle)
}

// branchCreatePayload is the `git branch-create --json` report of res.
// parent_issue and epic_branch are null unless the branch is based on an epic
// branch in this repository.
func branchCreatePayload(res gitpkg.IssueBranchResult) map[string]interface{} {
	payload := map[string]interface{}{
		"success":      true,
		"branch":       res.Branch,
		"base_branch":  res.BaseBranch,
		"action":       res.Action,
		"parent_issue": nil,
		"epic_branch":  nil,
	}
	if res.ParentIssue != 0 {
		payload["parent_issue"] = res.ParentIssue
		payload["epic_branch"] = res.EpicBranch
	}
	return payload
}
