package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/shurcooL/graphql"
)

// PRService provides pull request operations.
type PRService struct {
	client *Client
}

// NewPRService creates a PR service.
func NewPRService(client *Client) *PRService {
	return &PRService{client: client}
}

// GetPRState fetches the state of a single pull request ("OPEN", "MERGED", "CLOSED").
// Satisfies hooks.PRVerifier so callers can inject a *PRService directly.
func (s *PRService) GetPRState(ctx context.Context, owner, repo string, number int) (string, error) {
	pr, err := s.GetPR(ctx, owner, repo, number)
	if err != nil {
		return "", err
	}
	return pr.State, nil
}

// GetPRMergeInfo fetches the merge commit SHA and ISO-8601 merge timestamp of a
// merged PR. Both are empty on an un-merged PR. Satisfies hooks.PRMergeInfoFetcher
// so the post-merge hook can capture the ground-truth breadcrumb (#4133).
func (s *PRService) GetPRMergeInfo(ctx context.Context, owner, repo string, number int) (sha, mergedAt string, err error) {
	pr, err := s.GetPR(ctx, owner, repo, number)
	if err != nil {
		return "", "", err
	}
	return pr.MergeCommitSHA, pr.MergedAt, nil
}

// GetPR fetches a single pull request with review and check status.
func (s *PRService) GetPR(ctx context.Context, owner, repo string, number int) (*types.PullRequest, error) {
	var q pullRequestQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphql.Int(number),
	}

	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("fetch PR #%d: %w", number, err)
	}

	pr := q.Repository.PullRequest
	result := &types.PullRequest{
		NodeID:           fmt.Sprintf("%v", pr.ID),
		Number:           int(pr.Number),
		Title:            string(pr.Title),
		Body:             string(pr.Body),
		State:            string(pr.State),
		HeadRef:          string(pr.HeadRefName),
		BaseRef:          string(pr.BaseRefName),
		Repo:             owner + "/" + repo,
		URL:              string(pr.URL),
		Mergeable:        string(pr.Mergeable),
		MergeStateStatus: string(pr.MergeStateStatus),
		ReviewStatus:     string(pr.ReviewDecision),
		IsDraft:          bool(pr.IsDraft),
		Additions:        int(pr.Additions),
		Deletions:        int(pr.Deletions),
		MergedAt:         string(pr.MergedAt),
	}
	if pr.MergeCommit != nil {
		result.MergeCommitSHA = string(pr.MergeCommit.OID)
	}

	for _, l := range pr.Labels.Nodes {
		result.Labels = append(result.Labels, string(l.Name))
	}

	// Get check status from the last commit
	if len(pr.Commits.Nodes) > 0 {
		commit := pr.Commits.Nodes[0].Commit
		if commit.StatusCheckRollup != nil {
			result.CheckStatus = string(commit.StatusCheckRollup.State)
		}
	}

	return result, nil
}

// ListPRs lists pull requests filtered by state and optionally by head ref.
func (s *PRService) ListPRs(ctx context.Context, owner, repo string, state string, headRef string) ([]types.PullRequest, error) {
	stateFilter := []PullRequestState{PullRequestState("OPEN")}
	if state != "" {
		stateFilter = []PullRequestState{PullRequestState(state)}
	}

	var results []types.PullRequest

	if headRef != "" {
		var q pullRequestListQuery
		vars := map[string]interface{}{
			"owner":   graphql.String(owner),
			"name":    graphql.String(repo),
			"first":   graphql.Int(20),
			"states":  stateFilter,
			"headRef": graphql.String(headRef),
		}
		if err := s.client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list PRs: %w", err)
		}
		for _, pr := range q.Repository.PullRequests.Nodes {
			p := types.PullRequest{
				NodeID:    fmt.Sprintf("%v", pr.ID),
				Number:    int(pr.Number),
				Title:     string(pr.Title),
				State:     string(pr.State),
				HeadRef:   string(pr.HeadRefName),
				BaseRef:   string(pr.BaseRefName),
				URL:       string(pr.URL),
				IsDraft:   bool(pr.IsDraft),
				Repo:      owner + "/" + repo,
				CreatedAt: string(pr.CreatedAt),
			}
			for _, l := range pr.Labels.Nodes {
				p.Labels = append(p.Labels, string(l.Name))
			}
			results = append(results, p)
		}
	} else {
		var q pullRequestListByStateQuery
		vars := map[string]interface{}{
			"owner":  graphql.String(owner),
			"name":   graphql.String(repo),
			"first":  graphql.Int(20),
			"states": stateFilter,
		}
		if err := s.client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list PRs: %w", err)
		}
		for _, pr := range q.Repository.PullRequests.Nodes {
			p := types.PullRequest{
				NodeID:    fmt.Sprintf("%v", pr.ID),
				Number:    int(pr.Number),
				Title:     string(pr.Title),
				State:     string(pr.State),
				HeadRef:   string(pr.HeadRefName),
				BaseRef:   string(pr.BaseRefName),
				URL:       string(pr.URL),
				IsDraft:   bool(pr.IsDraft),
				Repo:      owner + "/" + repo,
				CreatedAt: string(pr.CreatedAt),
			}
			for _, l := range pr.Labels.Nodes {
				p.Labels = append(p.Labels, string(l.Name))
			}
			results = append(results, p)
		}
	}

	return results, nil
}

// CreatePR creates a new pull request.
func (s *PRService) CreatePR(ctx context.Context, repoID, title, body, headRef, baseRef string) (*types.PullRequest, error) {
	var m createPullRequestMutation
	input := map[string]interface{}{
		"input": CreatePullRequestInput{
			RepositoryID: graphql.ID(repoID),
			Title:        graphql.String(title),
			Body:         graphql.String(body),
			HeadRefName:  graphql.String(headRef),
			BaseRefName:  graphql.String(baseRef),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}

	return &types.PullRequest{
		NodeID:  fmt.Sprintf("%v", m.CreatePullRequest.PullRequest.ID),
		Number:  int(m.CreatePullRequest.PullRequest.Number),
		URL:     string(m.CreatePullRequest.PullRequest.URL),
		Title:   title,
		HeadRef: headRef,
		BaseRef: baseRef,
	}, nil
}

// UpdatePR patches the documented attributes of a pull request identified
// by node ID. Title/Body/BaseBranch flow through the updatePullRequest
// GraphQL mutation; State transitions and Draft toggles are dispatched to
// dedicated mutations.
//
// Forge fields not natively expressed by GitHub (Squash, AllowForcePush,
// ApprovalsBeforeMerge) are silently ignored on this adapter — they are
// merge-time concerns on GitHub, not PR attributes.
func (s *PRService) UpdatePR(ctx context.Context, prID string, opts forge.UpdatePROptions) (*types.PullRequest, error) {
	if prID == "" {
		return nil, fmt.Errorf("update PR: prID is required")
	}

	in := UpdatePullRequestInput{PullRequestID: graphql.ID(prID)}
	hasField := false
	if opts.Title != nil {
		t := graphql.String(*opts.Title)
		in.Title = &t
		hasField = true
	}
	if opts.Body != nil {
		b := graphql.String(*opts.Body)
		in.Body = &b
		hasField = true
	}
	if opts.TargetBranch != nil {
		bb := graphql.String(*opts.TargetBranch)
		in.BaseRefName = &bb
		hasField = true
	}

	var result *types.PullRequest
	if hasField {
		var m updatePullRequestMutation
		input := map[string]interface{}{"input": in}
		if err := s.client.mutate(ctx, &m, input); err != nil {
			return nil, fmt.Errorf("update PR: %w", err)
		}
		result = &types.PullRequest{
			NodeID:  fmt.Sprintf("%v", m.UpdatePullRequest.PullRequest.ID),
			Number:  int(m.UpdatePullRequest.PullRequest.Number),
			Title:   string(m.UpdatePullRequest.PullRequest.Title),
			Body:    string(m.UpdatePullRequest.PullRequest.Body),
			State:   string(m.UpdatePullRequest.PullRequest.State),
			HeadRef: string(m.UpdatePullRequest.PullRequest.HeadRefName),
			BaseRef: string(m.UpdatePullRequest.PullRequest.BaseRefName),
			IsDraft: bool(m.UpdatePullRequest.PullRequest.IsDraft),
		}
	}

	if result == nil {
		result = &types.PullRequest{NodeID: prID}
	}
	return result, nil
}

// ClosePR closes a pull request without merging it.
func (s *PRService) ClosePR(ctx context.Context, prID string) error {
	if prID == "" {
		return fmt.Errorf("close PR: prID is required")
	}
	var m closePullRequestMutation
	input := map[string]interface{}{
		"input": ClosePullRequestInput{PullRequestID: graphql.ID(prID)},
	}
	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("close PR: %w", err)
	}
	return nil
}

// IteratePRs returns an iterator over pull requests matching the given
// state and head ref filters. Slice-backed today (eager fetch via
// ListPRs); cursor-driven streaming will replace it without changing
// the surface.
func (s *PRService) IteratePRs(ctx context.Context, owner, repo, state, headRef string) forge.Iterator[types.PullRequest] {
	prs, err := s.ListPRs(ctx, owner, repo, state, headRef)
	return newSliceIterator(prs, err)
}

// MergePR merges a pull request using squash merge by default.
func (s *PRService) MergePR(ctx context.Context, prID string) error {
	_, err := s.MergePRWithStrategy(ctx, prID, "SQUASH")
	return err
}

// MergePRWithStrategy merges a pull request with the specified merge method.
// Valid strategies: "SQUASH", "MERGE", "REBASE".
// Returns the merge commit SHA (empty string if unavailable) and any error.
func (s *PRService) MergePRWithStrategy(ctx context.Context, prID string, strategy string) (string, error) {
	var m mergePullRequestMutation
	input := map[string]interface{}{
		"input": MergePullRequestInput{
			PullRequestID: graphql.ID(prID),
			MergeMethod:   graphql.String(strategy),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return "", fmt.Errorf("merge PR (%s): %w", strategy, err)
	}
	sha := ""
	if m.MergePullRequest.PullRequest.MergeCommit != nil {
		sha = string(m.MergePullRequest.PullRequest.MergeCommit.OID)
	}
	return sha, nil
}

// DeleteBranch deletes a remote branch via the GitHub GraphQL deleteRef mutation.
func (s *PRService) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	// First, get the ref's node ID
	var q repositoryRefQuery
	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(repo),
		"ref":   graphql.String("refs/heads/" + branch),
	}
	if err := s.client.query(ctx, &q, vars); err != nil {
		return fmt.Errorf("lookup ref %s: %w", branch, err)
	}
	if q.Repository.Ref == nil {
		return nil // Branch already deleted — idempotent
	}

	// Delete the ref
	var m deleteRefMutation
	input := map[string]interface{}{
		"input": DeleteRefInput{
			RefID: q.Repository.Ref.ID,
		},
	}
	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("delete ref %s: %w", branch, err)
	}
	return nil
}

// EpicPRResult is an alias for the forge-agnostic epic-PR-result shape.
type EpicPRResult = forgetypes.EpicPRResult

// CreateEpicPR creates a PR to merge an epic branch into the base branch (usually main).
// Returns "already_exists" if an open PR already exists, "already_merged" if previously merged.
func (s *PRService) CreateEpicPR(ctx context.Context, owner, repo string, epicNumber int, epicTitle, epicBranch, baseBranch string) (*EpicPRResult, error) {
	// Check for existing PR from this branch
	existing, err := s.ListPRs(ctx, owner, repo, "", epicBranch)
	if err != nil {
		return nil, fmt.Errorf("check existing PRs: %w", err)
	}

	for _, pr := range existing {
		if pr.State == "OPEN" {
			return &EpicPRResult{
				Action:   "already_exists",
				PRNumber: pr.Number,
				PRURL:    pr.URL,
				PRNodeID: pr.NodeID,
			}, nil
		}
		if pr.State == "MERGED" {
			return &EpicPRResult{
				Action:   "already_merged",
				PRNumber: pr.Number,
				PRURL:    pr.URL,
			}, nil
		}
	}

	// Get repo node ID for CreatePR
	repoID, err := s.client.GetRepositoryID(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("get repo ID: %w", err)
	}

	title := fmt.Sprintf("epic(#%d): %s", epicNumber, epicTitle)
	body := fmt.Sprintf("## Epic #%d: %s\n\nAll sub-issues have been completed and merged to the `%s` branch.\n\nThis PR merges the epic branch into `%s`.\n\n---\n*Auto-created by Nightgauge pipeline on epic completion.*",
		epicNumber, epicTitle, epicBranch, baseBranch)

	pr, err := s.CreatePR(ctx, repoID, title, body, epicBranch, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("create epic PR: %w", err)
	}

	return &EpicPRResult{
		Action:   "created",
		PRNumber: pr.Number,
		PRURL:    pr.URL,
		PRNodeID: pr.NodeID,
	}, nil
}

// MergeEpicPR merges an epic PR using MERGE strategy (preserves sub-issue commit history)
// and deletes the epic branch afterward.
func (s *PRService) MergeEpicPR(ctx context.Context, owner, repo string, prNodeID, epicBranch string) error {
	// Epic PRs use MERGE (not SQUASH) to preserve sub-issue commit history
	if _, err := s.MergePRWithStrategy(ctx, prNodeID, "MERGE"); err != nil {
		return fmt.Errorf("merge epic PR: %w", err)
	}

	// Delete the epic branch via GitHub API (authoritative remote deletion)
	if err := s.DeleteBranch(ctx, owner, repo, epicBranch); err != nil {
		// Non-fatal: branch may have been auto-deleted by GitHub settings
		_ = err
	}

	return nil
}

// splitPROwnerRepo splits "owner/repo" into parts.
func splitPROwnerRepo(full string) (string, string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", full
	}
	return parts[0], parts[1]
}
