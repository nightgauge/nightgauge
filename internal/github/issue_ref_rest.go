package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// issueRef is the minimum an issue-linking write needs about one issue: the
// database id the REST link endpoints take, the node ID the remaining
// ProjectV2 mutations take, and the parent epic's number for the
// circular-dependency guard. Deliberately NOT a types.Issue — nothing on this
// path reads a title, a label or a body, and returning the full object would
// invite a caller to depend on fields this cheap read does not fetch.
type issueRef struct {
	// NodeID is the GraphQL global ID. REST reports it directly (`node_id`),
	// which is why resolving it no longer costs a GraphQL point.
	NodeID string
	// DatabaseID is the integer id the sub-issue and dependency REST endpoints
	// take. It arrives in the same response as NodeID, at no extra cost —
	// which is the whole reason the link mutations could move transport: the
	// id source did not have to move, it was already here.
	DatabaseID int64
	// ParentNumber is the parent epic's issue number, or 0 when the issue has
	// no parent. Derived from REST's `parent_issue_url`.
	ParentNumber int
}

// issueRefResponse is the slice of GitHub's REST issue object this read uses.
type issueRefResponse struct {
	Number int `json:"number"`
	// ID is the database id — the parameter every sub-issue and dependency
	// REST endpoint takes. GraphQL never exposed it, which is why the link
	// mutations were classified `coupled` until this read carried it.
	ID     int64  `json:"id"`
	NodeID string `json:"node_id"`
	// ParentIssueURL is null for an issue with no parent, and an API URL
	// ending in /issues/<number> otherwise.
	ParentIssueURL string `json:"parent_issue_url"`
	// PullRequest is present ONLY when the number names a pull request. Its
	// presence is the discriminator — see resolveIssueRef.
	PullRequest *struct{} `json:"pull_request"`
}

// resolveIssueRef reads one issue's node ID and parent number over REST.
//
// This replaces a full `IssueService.GetIssue` GraphQL document that was
// issued purely to reach two fields. `GET /repos/{o}/{r}/issues/{n}` answers
// both in one call, bills the near-idle `core` bucket instead of the `graphql`
// one, and — unlike a GraphQL POST — is conditional-GET-able, so the client's
// ETag layer serves a repeat read for zero points under `nightgauge serve`.
// See docs/GITHUB_GRAPHQL_SCHEMA.md § Transport Classification for why the
// node ID is not a reason to keep this read on GraphQL.
//
// **A pull request number is rejected, and that is not defensive coding.**
// The two APIs genuinely disagree here: GraphQL's `repository.issue(number:)`
// errors NOT_FOUND for a PR number, while REST's `/issues/{n}` cheerfully
// returns the pull request (verified live 2026-08-26). Without this check the
// migration would silently widen the contract — `nightgauge issue
// add-blocked-by 925 …` would start succeeding against a PR where it used to
// fail loudly. REST marks the difference with a `pull_request` key, so the
// discriminator is the object itself rather than a second call.
func resolveIssueRef(ctx context.Context, c *Client, owner, repo string, number int) (*issueRef, error) {
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("resolve issue #%d: owner and repo are required", number)
	}
	if number <= 0 {
		return nil, fmt.Errorf("resolve issue: number must be positive, got %d", number)
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d",
		url.PathEscape(owner), url.PathEscape(repo), number)
	body, status, err := c.restDoStatus(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve issue %s/%s#%d: %w", owner, repo, number, err)
	}
	if status < 200 || status >= 300 {
		// Includes 404. The GraphQL predecessor also failed on an unknown
		// number, so an absent issue stays an error rather than becoming a
		// silent no-op — see docs/TESTING.md on fail-open vs fail-silent.
		return nil, fmt.Errorf("resolve issue %s/%s#%d: REST %d: %s",
			owner, repo, number, status, restErrorSummary(body))
	}
	var resp issueRefResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("resolve issue %s/%s#%d: decode: %w", owner, repo, number, err)
	}
	if resp.PullRequest != nil {
		return nil, fmt.Errorf("resolve issue %s/%s#%d: number names a pull request, not an issue",
			owner, repo, number)
	}
	if resp.NodeID == "" {
		// A 200 with no node ID is not a shape this API produces; treating it
		// as success would hand an empty ID to a mutation.
		return nil, fmt.Errorf("resolve issue %s/%s#%d: response carried no node_id", owner, repo, number)
	}
	if resp.ID == 0 {
		// Same reasoning as the node_id guard, for the other id. A zero
		// database id would reach a link endpoint as `{"sub_issue_id": 0}`,
		// which GitHub answers 404 — indistinguishable from "the issue is
		// gone" at the call site. Fail here, where the cause is still known.
		return nil, fmt.Errorf("resolve issue %s/%s#%d: response carried no id", owner, repo, number)
	}
	return &issueRef{
		NodeID:       resp.NodeID,
		DatabaseID:   resp.ID,
		ParentNumber: parentIssueNumber(resp.ParentIssueURL),
	}, nil
}

// parentIssueNumber extracts the issue number from REST's parent_issue_url.
// Returns 0 for an empty URL (no parent) or any shape whose trailing segment
// is not a positive integer — an unparseable URL must read as "no parent
// known" rather than as some arbitrary number, because the caller uses this
// to REJECT a link.
func parentIssueNumber(raw string) int {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "/"))
	if raw == "" {
		return 0
	}
	idx := strings.LastIndex(raw, "/")
	if idx < 0 || idx == len(raw)-1 {
		return 0
	}
	n, err := strconv.Atoi(raw[idx+1:])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
