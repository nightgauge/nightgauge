// Package github provides repository settings detection and management.
package github

import (
	"context"
	"encoding/json"
	"fmt"
)

// RepositorySettings holds parsed GitHub repository settings relevant to the pipeline.
type RepositorySettings struct {
	AllowAutoMerge      bool   `json:"allow_auto_merge"`
	DeleteBranchOnMerge bool   `json:"delete_branch_on_merge"`
	RepoFullName        string `json:"full_name"`
	Owner               string `json:"owner"`
	Repo                string `json:"repo"`
}

// repoAPIResponse is the relevant subset of the GitHub REST API repository response.
type repoAPIResponse struct {
	FullName            string `json:"full_name"`
	AllowAutoMerge      bool   `json:"allow_auto_merge"`
	DeleteBranchOnMerge bool   `json:"delete_branch_on_merge"`
}

// SettingsService provides repository settings operations via GitHub REST API.
type SettingsService struct {
	client *Client
}

// NewSettingsService creates a SettingsService using the given client.
func NewSettingsService(client *Client) *SettingsService {
	return &SettingsService{client: client}
}

// GetRepositorySettings fetches the allow_auto_merge flag and other settings
// for the repository via GET /repos/{owner}/{repo}.
func (s *SettingsService) GetRepositorySettings(ctx context.Context, owner, repo string) (*RepositorySettings, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	body, err := s.client.restGet(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get repository settings for %s/%s: %w", owner, repo, err)
	}

	var resp repoAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse repository settings response for %s/%s: %w", owner, repo, err)
	}

	return &RepositorySettings{
		AllowAutoMerge:      resp.AllowAutoMerge,
		DeleteBranchOnMerge: resp.DeleteBranchOnMerge,
		RepoFullName:        resp.FullName,
		Owner:               owner,
		Repo:                repo,
	}, nil
}

// DisableAutoMerge disables the allow_auto_merge setting on the repository
// via PATCH /repos/{owner}/{repo}.
func (s *SettingsService) DisableAutoMerge(ctx context.Context, owner, repo string) error {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	payload := map[string]interface{}{"allow_auto_merge": false}
	_, err := s.client.restPatch(ctx, path, payload)
	if err != nil {
		return fmt.Errorf("disable auto-merge for %s/%s: %w", owner, repo, err)
	}
	return nil
}

// EnableDeleteBranchOnMerge turns on the delete_branch_on_merge setting via
// PATCH /repos/{owner}/{repo}, so GitHub deletes a PR's head branch when it
// merges. Set at onboarding so merged remote branches don't accumulate; pairs
// with the pipeline's post-merge worktree + local-branch teardown (#3969) so
// neither remote nor local merged branches pile up.
func (s *SettingsService) EnableDeleteBranchOnMerge(ctx context.Context, owner, repo string) error {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	payload := map[string]interface{}{"delete_branch_on_merge": true}
	_, err := s.client.restPatch(ctx, path, payload)
	if err != nil {
		return fmt.Errorf("enable delete-branch-on-merge for %s/%s: %w", owner, repo, err)
	}
	return nil
}
