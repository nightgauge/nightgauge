package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Options controls a single Fetch call. BaseURL defaults to DefaultBaseURL
// (https://api.github.com); tests inject a httptest.Server URL to run offline.
type Options struct {
	// Source is `owner/repo` (required).
	Source string
	// Since is a semver lower bound (exclusive). Empty = no filter.
	Since string
	// Limit caps the per_page request size and the returned slice.
	// 0 / negative defaults to DefaultLimit.
	Limit int
	// Token is an optional GitHub PAT. When non-empty it is sent as
	// `Authorization: Bearer <token>` to bump the rate limit. Public-repo
	// requests work without a token but at the lower 60/hr ceiling.
	Token string
	// BaseURL overrides the GitHub REST root for tests. Empty =
	// DefaultBaseURL.
	BaseURL string
	// HTTPClient is injectable for tests. When nil, Fetch builds an
	// http.Client with a 10-second timeout.
	HTTPClient *http.Client
}

// Fetch issues GET {BaseURL}/repos/{owner}/{repo}/releases?per_page={limit},
// applies the optional --since semver filter, and returns a FetchResult.
//
// Errors are returned for: malformed source, unparseable since, transport
// failure, non-2xx HTTP status, malformed response body. A successful call
// with zero releases is not an error.
func Fetch(ctx context.Context, opts Options) (FetchResult, error) {
	owner, repo, err := splitSource(opts.Source)
	if err != nil {
		return FetchResult{}, err
	}

	if opts.Since != "" {
		if _, err := parseSemver(opts.Since); err != nil {
			return FetchResult{}, fmt.Errorf("invalid --since version: %w", err)
		}
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	baseURL := strings.TrimRight(opts.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d", baseURL, owner, repo, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return FetchResult{}, fmt.Errorf("GitHub API returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw []Release
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return FetchResult{}, fmt.Errorf("decode releases: %w", err)
	}

	filtered := 0
	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		// Never surface drafts or pre-releases (alphas / nightlies / previews) —
		// the tracker acts on stable releases only. Provider-agnostic (#4056):
		// openai/codex ships `rust-v*-alpha.*`, gemini ships `*-preview/-nightly`.
		if r.Draft || r.Prerelease {
			filtered++
			continue
		}
		if opts.Since != "" && !IsNewer(r.TagName, opts.Since) {
			filtered++
			continue
		}
		out = append(out, r)
	}
	if len(out) > limit {
		out = out[:limit]
	}

	return FetchResult{
		V:         SchemaVersion,
		Source:    opts.Source,
		Since:     opts.Since,
		Limit:     limit,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Filtered:  filtered,
		Releases:  out,
	}, nil
}

// splitSource validates `owner/repo` form and returns the two halves.
func splitSource(source string) (string, string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", "", fmt.Errorf("--source is required (owner/repo)")
	}
	parts := strings.Split(source, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("--source must be owner/repo (got %q)", source)
	}
	return parts[0], parts[1], nil
}
