package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

const githubAPIBase = "https://api.github.com"

// TokenScopeInfo is an alias for the forge-agnostic token-scope shape.
// Field semantics are forge-specific (GitHub OAuth scopes vs GitLab PAT
// scopes); the struct fields are the lowest common denominator.
type TokenScopeInfo = forgetypes.TokenScopeInfo

// requiredScopes are the classic GitHub OAuth scopes needed for repository and
// project pipeline operations. read:org is advisory: it improves discovery of
// private organisation memberships, but must not block an otherwise
// capability-valid repository/project token.
var requiredScopes = []string{"repo", "project"}

// Whoami returns the authenticated actor as a forge-agnostic Actor.
// Mirrors `gh api user --jq .login`. The implementation reuses the same
// GET /user request as CheckTokenScopes; the two could share a body
// fetch, but the cost is one HTTP call so we keep them independent for
// simplicity.
func (c *Client) Whoami(ctx context.Context) (*forgetypes.Actor, error) {
	login, err := c.getCurrentUserLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}
	return &forgetypes.Actor{Login: login}, nil
}

// CheckTokenScopes validates the configured token has sufficient scopes for
// pipeline operations. It uses GET /rate_limit to read the X-OAuth-Scopes
// header without consuming a meaningful API quota slot.
func (c *Client) CheckTokenScopes(ctx context.Context) (*TokenScopeInfo, error) {
	scopes, err := c.getOAuthScopes(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token scopes: %w", err)
	}

	login, err := c.getCurrentUserLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}

	orgs, err := c.getUserOrgLogins(ctx)
	if err != nil {
		// Non-fatal: org memberships are informational.
		orgs = []string{}
	}

	missing := computeMissingScopes(scopes, requiredScopes)

	return &TokenScopeInfo{
		Scopes:         scopes,
		Login:          login,
		OrgMemberships: orgs,
		Resolution:     "env", // NewClient always uses GITHUB_TOKEN env var
		MissingScopes:  missing,
		Valid:          len(missing) == 0,
	}, nil
}

// getOAuthScopes calls GET /rate_limit and returns the X-OAuth-Scopes header
// parsed into individual scope strings.
func (c *Client) getOAuthScopes(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+"/rate_limit", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body) // drain body

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("token is expired or revoked (HTTP 401)")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden {
		return nil, fmt.Errorf("unexpected status %d from /rate_limit", resp.StatusCode)
	}

	raw := resp.Header.Get("X-OAuth-Scopes")
	return parseScopes(raw), nil
}

// getCurrentUserLogin calls GET /user and returns the authenticated user's login.
func (c *Client) getCurrentUserLogin(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("token is expired or revoked (HTTP 401)")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /user returned status %d", resp.StatusCode)
	}

	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", fmt.Errorf("decode /user response: %w", err)
	}
	return u.Login, nil
}

// getUserOrgLogins calls GET /user/orgs and returns the list of org logins.
func (c *Client) getUserOrgLogins(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+"/user/orgs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /user/orgs returned status %d", resp.StatusCode)
	}

	var orgs []struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &orgs); err != nil {
		return nil, fmt.Errorf("decode /user/orgs response: %w", err)
	}

	logins := make([]string, 0, len(orgs))
	for _, o := range orgs {
		logins = append(logins, o.Login)
	}
	return logins, nil
}

// parseScopes splits a comma-separated scope header into individual scope strings.
// Empty input returns an empty (non-nil) slice.
func parseScopes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			result = append(result, s)
		}
	}
	return result
}

// computeMissingScopes returns the elements of required that are not in actual.
func computeMissingScopes(actual, required []string) []string {
	have := make(map[string]bool, len(actual))
	for _, s := range actual {
		have[s] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}
