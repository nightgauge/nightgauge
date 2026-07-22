package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// GitLab issue link types. Reference:
// https://docs.gitlab.com/api/issue_links/
//
//   - linkTypeRelatesTo: bidirectional, untyped relationship. Used as the
//     CE-compatible substrate for the parent-child sub-issue convention
//     (outgoing relates_to from issue X are X's children).
//   - linkTypeBlocks / linkTypeIsBlockedBy: bidirectional pair — POSTing
//     either materialises the inverse on the linked issue automatically.
const (
	linkTypeRelatesTo   = "relates_to"
	linkTypeBlocks      = "blocks"
	linkTypeIsBlockedBy = "is_blocked_by"
)

// rawIssueLink is the JSON shape of an element in
// GET /api/v4/projects/:id/issues/:iid/links. The fields tracked here are
// what the classifier and the BlockedBy open-state filter need; unknown
// fields are ignored by the JSON decoder.
//
// Numeric link identity (`id`, `issue_link_id`) varies between CE versions.
// Older GitLab installs return only one of the two; we read both and use
// whichever is non-zero. `link_id` is the legacy spelling some installs use
// for the same value.
type rawIssueLink struct {
	ID          int64  `json:"id"`
	IID         int    `json:"iid"`
	IssueLinkID int64  `json:"issue_link_id"`
	LinkID      int64  `json:"link_id"`
	ProjectID   int64  `json:"project_id"`
	Title       string `json:"title"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	LinkType    string `json:"link_type"`
}

// linkID returns the identifier used for DELETE /links/:id. Falls back
// across the historical field names so the helper works on older self-hosted
// installs that still emit only one of them.
func (r *rawIssueLink) linkID() int64 {
	if r.IssueLinkID > 0 {
		return r.IssueLinkID
	}
	if r.LinkID > 0 {
		return r.LinkID
	}
	return r.ID
}

// listIssueLinks fetches every issue link on (owner/repo)#iid, paginating
// via the standard GitLab Link header.
func listIssueLinks(ctx context.Context, c *Client, owner, repo string, iid int) ([]rawIssueLink, error) {
	path := fmt.Sprintf("/projects/%s/issues/%d/links", projectPath(owner, repo), iid)
	full := c.buildURL(path, url.Values{"per_page": []string{"100"}})

	var all []rawIssueLink
	for full != "" {
		var page []rawIssueLink
		resp, err := c.do(ctx, http.MethodGet, full, nil, &page, fmt.Sprintf("list issue links #%d", iid))
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		links := parseLinkHeader(resp.Header.Get("Link"))
		if links.Next == nil {
			break
		}
		full = links.Next.String()
	}
	return all, nil
}

// addIssueLink creates a link of linkType from (ownerA/repoA)#iidA →
// (ownerB/repoB)#iidB. GitLab returns 409 on duplicate links; the helper
// treats 409 as a successful no-op so callers get idempotent semantics.
func addIssueLink(ctx context.Context, c *Client, ownerA, repoA string, iidA int, ownerB, repoB string, iidB int, linkType string) error {
	path := fmt.Sprintf("/projects/%s/issues/%d/links", projectPath(ownerA, repoA), iidA)
	full := c.buildURL(path, nil)

	payload := map[string]any{
		"target_project_id": ownerB + "/" + repoB,
		"target_issue_iid":  iidB,
		"link_type":         linkType,
	}

	resp, err := c.do(ctx, http.MethodPost, full, payload, nil, fmt.Sprintf("add issue link (%s)", linkType))
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusConflict {
			return nil
		}
		return err
	}
	return nil
}

// deleteIssueLink removes the link with the given linkID from
// (owner/repo)#iid. GitLab removes the inverse side automatically for
// blocks / is_blocked_by, so the caller only needs to delete from one side.
func deleteIssueLink(ctx context.Context, c *Client, owner, repo string, iid int, linkID int64) error {
	path := fmt.Sprintf("/projects/%s/issues/%d/links/%d", projectPath(owner, repo), iid, linkID)
	full := c.buildURL(path, nil)
	_, err := c.do(ctx, http.MethodDelete, full, nil, nil, fmt.Sprintf("delete issue link %d", linkID))
	return err
}

// findLinkID resolves the GitLab link_id for the (linkType, target_iid)
// tuple by listing all links on (owner/repo)#iid and matching client-side.
// Returns 0 with a nil error when no matching link exists — callers treat
// "no such link" as an idempotent remove success.
func findLinkID(ctx context.Context, c *Client, owner, repo string, iid int, targetIID int, linkType string) (int64, error) {
	links, err := listIssueLinks(ctx, c, owner, repo, iid)
	if err != nil {
		return 0, err
	}
	for _, l := range links {
		if l.IID == targetIID && l.LinkType == linkType {
			return l.linkID(), nil
		}
	}
	return 0, nil
}

// classifyLinks partitions a link list into the three forge-typed slices
// SubIssues / BlockedBy / Blocking. The "open" filter for BlockedBy drops
// links to closed issues, matching the GitHub adapter's isBlocked
// semantics (a closed blocker does not count as blocking).
//
// The convention for sub-issues on CE is documented in
// docs/SELF_HOSTED_GITLAB_FEATURES.md: outgoing relates_to from the listed
// issue are treated as that issue's children. EE installs that wrote a
// native parent_id alongside the relates_to link keep the canonical
// disambiguation through that field — readers of this slice receive both
// "merely related" and sub-issue links indistinguishably on CE.
func classifyLinks(owner, repo string, links []rawIssueLink) (subIssues []forgetypes.SubIssueRef, blockedBy, blocking []forgetypes.BlockingRef) {
	repoStr := owner + "/" + repo
	for _, l := range links {
		switch l.LinkType {
		case linkTypeRelatesTo:
			subIssues = append(subIssues, forgetypes.SubIssueRef{
				NodeID: strconv.FormatInt(l.ID, 10),
				Number: l.IID,
				Title:  l.Title,
				State:  l.State,
				Repo:   repoStr,
			})
		case linkTypeIsBlockedBy:
			if l.State != "opened" {
				continue
			}
			blockedBy = append(blockedBy, forgetypes.BlockingRef{
				NodeID: strconv.FormatInt(l.ID, 10),
				Number: l.IID,
				Title:  l.Title,
				State:  l.State,
				Repo:   repoStr,
			})
		case linkTypeBlocks:
			blocking = append(blocking, forgetypes.BlockingRef{
				NodeID: strconv.FormatInt(l.ID, 10),
				Number: l.IID,
				Title:  l.Title,
				State:  l.State,
				Repo:   repoStr,
			})
		}
	}
	return subIssues, blockedBy, blocking
}

// tryWriteParentID is the EE-only best-effort write of the native
// parent_id field on a sub-issue. Failures are intentionally swallowed by
// the caller — the canonical write path is the relates_to link, which has
// already succeeded by the time this is invoked.
func tryWriteParentID(ctx context.Context, c *Client, owner, repo string, iid, parentIID int) error {
	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), iid)
	full := c.buildURL(path, nil)
	_, err := c.do(ctx, http.MethodPut, full, map[string]any{"parent_id": parentIID}, nil, "write parent_id")
	return err
}
