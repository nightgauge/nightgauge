package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// repoLabelsPageSize mirrors the GraphQL predecessor's `labels(first: 100)`.
// Both the old query and this read return AT MOST one page: a repository with
// more than 100 labels was truncated before the migration and is truncated
// identically after it. That parity is deliberate — quietly gaining pagination
// while moving transports would make a behaviour change indistinguishable from
// the transport change if anything downstream regressed.
const repoLabelsPageSize = 100

// restLabel is the slice of GitHub's REST label object this read uses.
type restLabel struct {
	// NodeID is the GraphQL global ID. REST reports it directly (`node_id`),
	// which is the whole reason a label READ can move while the label
	// MUTATIONS that consume these IDs stay on GraphQL: nothing downstream
	// can tell which transport produced the ID.
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

// listRepoLabels reads a repository's labels over REST (#849).
//
// `GET /repos/{o}/{r}/labels` answers in one call, bills the near-idle `core`
// bucket instead of the `graphql` one, and — unlike a GraphQL POST — is
// conditional-GET-able, so the client's ETag layer serves a repeat read for
// zero points under `nightgauge serve`. See docs/GITHUB_GRAPHQL_SCHEMA.md
// § Transport Classification.
//
// A label carrying no node ID is an error rather than a skipped entry: every
// caller of this read feeds the IDs to a label mutation, so dropping one
// silently would produce a mutation against an empty ID, or a "label not
// found" for a label that plainly exists.
//
// **The ORDER changes, and it was checked rather than assumed.** GraphQL
// returned GitHub's default labels first and custom ones after; REST returns
// them alphabetically by name. Both APIs were run against the same repository
// on 2026-08-26 and returned the identical SET of 30 labels. All three
// consumers match by name — `LabelService.Create`'s idempotency check,
// `LabelService.Rename`'s lookup, and `IssueService.GetRepoLabels`, which
// builds a map — plus the `label list` CLI, which just prints them, so no
// caller can observe the difference except as a nicer listing.
func listRepoLabels(ctx context.Context, c *Client, owner, repo string) ([]restLabel, error) {
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("list labels: owner and repo are required")
	}
	path := fmt.Sprintf("/repos/%s/%s/labels?per_page=%d",
		url.PathEscape(owner), url.PathEscape(repo), repoLabelsPageSize)
	body, status, err := c.restDoStatus(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("list labels for %s/%s: %w", owner, repo, err)
	}
	if status < 200 || status >= 300 {
		// Includes 404 for an unknown or unreadable repository. The GraphQL
		// predecessor also failed there, so an absent repo stays an error
		// rather than becoming an empty label set — an empty map would read
		// as "this repo has no labels" and silently skip every label write.
		return nil, fmt.Errorf("list labels for %s/%s: REST %d: %s",
			owner, repo, status, restErrorSummary(body))
	}
	var labels []restLabel
	if err := json.Unmarshal(body, &labels); err != nil {
		return nil, fmt.Errorf("list labels for %s/%s: decode: %w", owner, repo, err)
	}
	for _, l := range labels {
		if l.NodeID == "" {
			return nil, fmt.Errorf("list labels for %s/%s: label %q carried no node_id",
				owner, repo, l.Name)
		}
	}
	return labels, nil
}
