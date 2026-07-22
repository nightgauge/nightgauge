package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// IssueService implements forge.IssueService for GitLab.
type IssueService struct {
	client *Client
}

// NewIssueService constructs a GitLab IssueService bound to the given REST
// client. Callers typically reach this via ForgeAdapter.Issues().
func NewIssueService(client *Client) *IssueService {
	return &IssueService{client: client}
}

// rawGitLabIssue is the JSON shape returned by /projects/:id/issues/:iid.
// Fields are kept loose (interface{}) for forge-irrelevant attributes so
// the decoder doesn't reject responses that grow new properties.
type rawGitLabIssue struct {
	ID          int64               `json:"id"`
	IID         int                 `json:"iid"`
	ProjectID   int64               `json:"project_id"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	State       string              `json:"state"`
	Labels      []string            `json:"labels"`
	WebURL      string              `json:"web_url"`
	Assignees   []rawGitLabUser     `json:"assignees"`
	Milestone   *rawGitLabMilestone `json:"milestone"`
}

type rawGitLabUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type rawGitLabMilestone struct {
	Title string `json:"title"`
}

// toForgeIssue translates a raw GitLab issue into the forge-agnostic
// Issue shape, applying the iid-as-Number convention documented in
// doc.go.
func (r *rawGitLabIssue) toForgeIssue(owner, repo string) *forgetypes.Issue {
	if r == nil {
		return nil
	}
	out := &forgetypes.Issue{
		NodeID: strconv.FormatInt(r.ID, 10),
		Number: r.IID,
		Title:  r.Title,
		Body:   r.Description,
		State:  r.State,
		Repo:   owner + "/" + repo,
		URL:    r.WebURL,
		Labels: append([]string(nil), r.Labels...),
	}
	for _, a := range r.Assignees {
		if a.Username != "" {
			out.Assignees = append(out.Assignees, a.Username)
		}
	}
	if r.Milestone != nil {
		out.Milestone = r.Milestone.Title
	}
	return out
}

// GetIssue fetches a single issue by iid and enriches it with sub-issue and
// blocking link arrays via a single /issues/:iid/links call. Link fetch
// failures are non-fatal — the issue is returned with empty link slices and
// the underlying error is discarded so consumers behave the same way as the
// GitHub adapter when GraphQL link queries return no rows.
func (s *IssueService) GetIssue(ctx context.Context, owner, repo string, number int) (*forgetypes.Issue, error) {
	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), number)
	full := s.client.buildURL(path, nil)

	var raw rawGitLabIssue
	if _, err := s.client.do(ctx, "GET", full, nil, &raw, fmt.Sprintf("get issue #%d", number)); err != nil {
		return nil, err
	}
	issue := raw.toForgeIssue(owner, repo)
	if issue == nil {
		return nil, nil
	}
	if links, err := listIssueLinks(ctx, s.client, owner, repo, number); err == nil {
		subIssues, blockedBy, blocking := classifyLinks(owner, repo, links)
		issue.SubIssues = subIssues
		issue.BlockedBy = blockedBy
		issue.Blocking = blocking
		if len(subIssues) > 0 {
			issue.IsEpic = true
		}
	}
	return issue, nil
}

// issueFieldsFragment is the GraphQL fragment used in aliased batch queries.
const issueFieldsFragment = `
fragment IssueFields on Issue {
  iid
  title
  description
  state
  webUrl
  labels { nodes { title } }
  assignees { nodes { username } }
}
`

// rawGraphQLIssue is the shape returned by the aliased GraphQL query.
type rawGraphQLIssue struct {
	IID         string `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	WebURL      string `json:"webUrl"`
	Labels      struct {
		Nodes []struct {
			Title string `json:"title"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []struct {
			Username string `json:"username"`
		} `json:"nodes"`
	} `json:"assignees"`
}

// toForgeIssueFromGraphQL converts a rawGraphQLIssue to forge Issue.
func (r *rawGraphQLIssue) toForgeIssueFromGraphQL(owner, repo string) *forgetypes.Issue {
	if r == nil {
		return nil
	}
	iid, _ := strconv.Atoi(r.IID)
	out := &forgetypes.Issue{
		// NodeID is left empty: the GitLab GraphQL aliased query returns iid-scoped
		// fields, not global GraphQL IDs. Callers that mutate via nodeID must use
		// the REST-path GetIssue instead. TODO: add `id` to IssueFields once callers need it.
		Number: iid,
		Title:  r.Title,
		Body:   r.Description,
		State:  r.State,
		Repo:   owner + "/" + repo,
		URL:    r.WebURL,
	}
	for _, l := range r.Labels.Nodes {
		if l.Title != "" {
			out.Labels = append(out.Labels, l.Title)
		}
	}
	for _, a := range r.Assignees.Nodes {
		if a.Username != "" {
			out.Assignees = append(out.Assignees, a.Username)
		}
	}
	return out
}

// GetIssuesByNumbers fetches the listed iids using a single GraphQL aliased
// batch request. Falls back to serial REST when the alias query fails (e.g.,
// on GitLab CE <16.x that doesn't support the alias syntax).
func (s *IssueService) GetIssuesByNumbers(ctx context.Context, owner, repo string, numbers []int) (map[int]*forgetypes.Issue, error) {
	// Dedup and filter non-positive iids.
	seen := make(map[int]struct{}, len(numbers))
	unique := make([]int, 0, len(numbers))
	for _, n := range numbers {
		if n <= 0 {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		unique = append(unique, n)
	}
	if len(unique) == 0 {
		return make(map[int]*forgetypes.Issue), nil
	}
	sort.Ints(unique)

	out, err := s.getIssuesByAliasedBatch(ctx, owner, repo, unique)
	if err != nil {
		log.Printf("gitlab: aliased batch failed (%v) — falling back to serial REST", err)
		return s.getIssuesByNumbersFallback(ctx, owner, repo, unique)
	}
	return out, nil
}

// getIssuesByAliasedBatch fetches all iids in a single GraphQL request using
// aliases: iid_N: issue(iid: "N") { ...IssueFields }.
func (s *IssueService) getIssuesByAliasedBatch(ctx context.Context, owner, repo string, iids []int) (map[int]*forgetypes.Issue, error) {
	// Build aliased query body.
	var sb strings.Builder
	sb.WriteString(`query($fullPath: ID!) { project(fullPath: $fullPath) {`)
	for _, iid := range iids {
		fmt.Fprintf(&sb, ` iid_%d: issue(iid: "%d") { ...IssueFields }`, iid, iid)
	}
	sb.WriteString(` } }`)
	sb.WriteString(issueFieldsFragment)

	variables := map[string]interface{}{
		"fullPath": owner + "/" + repo,
	}

	data, err := s.client.doGraphQL(ctx, sb.String(), variables)
	if err != nil {
		return nil, err
	}

	// Decode the aliased response.
	var resp struct {
		Project map[string]json.RawMessage `json:"project"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode aliased batch: %w", err)
	}

	out := make(map[int]*forgetypes.Issue, len(iids))
	for _, iid := range iids {
		aliasKey := fmt.Sprintf("iid_%d", iid)
		raw, ok := resp.Project[aliasKey]
		if !ok {
			continue
		}
		// null alias means issue doesn't exist — skip silently.
		if string(raw) == "null" {
			continue
		}
		var gqlIssue rawGraphQLIssue
		if err := json.Unmarshal(raw, &gqlIssue); err != nil {
			return nil, fmt.Errorf("decode alias %s: %w", aliasKey, err)
		}
		if issue := gqlIssue.toForgeIssueFromGraphQL(owner, repo); issue != nil {
			out[iid] = issue
		}
	}
	return out, nil
}

// getIssuesByNumbersFallback is the serial REST fallback used when the aliased
// GraphQL batch fails. The nodes(ids:[...]) true-batch is not feasible because
// GitLab REST responses don't return global GraphQL IDs.
func (s *IssueService) getIssuesByNumbersFallback(ctx context.Context, owner, repo string, iids []int) (map[int]*forgetypes.Issue, error) {
	out := make(map[int]*forgetypes.Issue, len(iids))
	for _, n := range iids {
		issue, err := s.GetIssue(ctx, owner, repo, n)
		if err != nil {
			if errors.Is(err, forge.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out[n] = issue
	}
	return out, nil
}

// listIssuesQuery returns the query parameters for the /issues collection
// endpoint matching the (labels, state) filter. Empty labels disables
// label filtering; empty state defaults to "opened".
func listIssuesQuery(labels []string, state string) url.Values {
	q := url.Values{}
	if state == "" {
		state = "opened"
	}
	q.Set("state", state)
	q.Set("per_page", "100")
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	return q
}

// ListIssues fetches all open issues with the given labels, walking
// link-header pagination until exhausted.
func (s *IssueService) ListIssues(ctx context.Context, owner, repo string, labels []string) ([]forgetypes.Issue, error) {
	path := fmt.Sprintf("/projects/%s/issues", projectPath(owner, repo))
	full := s.client.buildURL(path, listIssuesQuery(labels, "opened"))

	var all []forgetypes.Issue
	for full != "" {
		var page []rawGitLabIssue
		resp, err := s.client.do(ctx, "GET", full, nil, &page, "list issues")
		if err != nil {
			return nil, err
		}
		for _, raw := range page {
			if issue := raw.toForgeIssue(owner, repo); issue != nil {
				all = append(all, *issue)
			}
		}
		links := parseLinkHeader(resp.Header.Get("Link"))
		if links.Next == nil {
			break
		}
		full = links.Next.String()
	}
	return all, nil
}

// IterateIssues returns a streaming iterator that walks GitLab pagination
// without buffering the full result set.
func (s *IssueService) IterateIssues(ctx context.Context, owner, repo string, labels []string) forge.Iterator[forgetypes.Issue] {
	path := fmt.Sprintf("/projects/%s/issues", projectPath(owner, repo))
	startURL := s.client.buildURL(path, listIssuesQuery(labels, "opened"))
	return newPageIterator(s.client, startURL, "list issues", owner, repo, decodeIssuePage)
}

// decodeIssuePage decodes a page body into forge issues, used by the
// generic page iterator.
func decodeIssuePage(body []byte, owner, repo string) ([]forgetypes.Issue, error) {
	var raws []rawGitLabIssue
	if err := json.Unmarshal(body, &raws); err != nil {
		return nil, fmt.Errorf("decode issues page: %w", err)
	}
	out := make([]forgetypes.Issue, 0, len(raws))
	for i := range raws {
		if v := raws[i].toForgeIssue(owner, repo); v != nil {
			out = append(out, *v)
		}
	}
	return out, nil
}

// CreateIssue creates a new issue against /projects/:id/issues. repoID
// must be "owner/repo" so the adapter can build the project path; this
// matches GitHub's pattern of accepting an opaque repo handle.
func (s *IssueService) CreateIssue(ctx context.Context, repoID, title, body string, labelIDs []string) (*forgetypes.Issue, error) {
	owner, repo := splitProjectID(repoID)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("gitlab create issue: repoID %q must be owner/repo", repoID)
	}
	path := fmt.Sprintf("/projects/%s/issues", projectPath(owner, repo))
	full := s.client.buildURL(path, nil)

	payload := map[string]any{
		"title":       title,
		"description": body,
	}
	if len(labelIDs) > 0 {
		// GitLab uses label *names* on this endpoint, not numeric IDs;
		// the labelIDs argument carries names by convention to keep the
		// forge interface uniform across forges.
		payload["labels"] = strings.Join(labelIDs, ",")
	}

	var raw rawGitLabIssue
	if _, err := s.client.do(ctx, "POST", full, payload, &raw, "create issue"); err != nil {
		return nil, err
	}
	return raw.toForgeIssue(owner, repo), nil
}

// UpdateIssue patches an issue identified by its (project, iid) pair. The
// nodeID parameter encodes "owner/repo#iid" so the adapter knows which
// project to target — mirroring the way GitHub uses an opaque GraphQL
// node ID.
func (s *IssueService) UpdateIssue(ctx context.Context, nodeID string, opts forge.UpdateIssueOptions) (*forgetypes.Issue, error) {
	owner, repo, iid, err := parseIssueRef(nodeID)
	if err != nil {
		return nil, fmt.Errorf("gitlab update issue: %w", err)
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), iid)
	full := s.client.buildURL(path, nil)

	payload := map[string]any{}
	if opts.Title != nil {
		payload["title"] = *opts.Title
	}
	if opts.Body != nil {
		payload["description"] = *opts.Body
	}
	if opts.Labels != nil {
		payload["labels"] = strings.Join(*opts.Labels, ",")
	}
	if opts.Assignees != nil {
		// GitLab takes assignee IDs as integers; adapter accepts the
		// numeric IDs passed as strings so the forge contract stays
		// uniform. Empty / non-numeric entries are skipped.
		ids := make([]int64, 0, len(*opts.Assignees))
		for _, a := range *opts.Assignees {
			if id, err := strconv.ParseInt(a, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		payload["assignee_ids"] = ids
	}
	if opts.Milestone != nil {
		if id, err := strconv.ParseInt(*opts.Milestone, 10, 64); err == nil {
			payload["milestone_id"] = id
		}
	}
	if opts.State != nil {
		switch strings.ToLower(*opts.State) {
		case "opened", "open":
			payload["state_event"] = "reopen"
		case "closed", "close":
			payload["state_event"] = "close"
		default:
			return nil, fmt.Errorf("gitlab update issue: unknown state %q", *opts.State)
		}
	}

	if len(payload) == 0 {
		return s.GetIssue(ctx, owner, repo, iid)
	}

	var raw rawGitLabIssue
	if _, err := s.client.do(ctx, "PUT", full, payload, &raw, "update issue"); err != nil {
		return nil, err
	}
	return raw.toForgeIssue(owner, repo), nil
}

// CloseIssue closes an issue identified by "owner/repo#iid".
func (s *IssueService) CloseIssue(ctx context.Context, issueID string) error {
	owner, repo, iid, err := parseIssueRef(issueID)
	if err != nil {
		return fmt.Errorf("gitlab close issue: %w", err)
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), iid)
	full := s.client.buildURL(path, nil)
	_, err = s.client.do(ctx, "PUT", full, map[string]any{"state_event": "close"}, nil, "close issue")
	return err
}

// ReopenIssue reopens a closed issue identified by "owner/repo#iid".
func (s *IssueService) ReopenIssue(ctx context.Context, issueID string) error {
	owner, repo, iid, err := parseIssueRef(issueID)
	if err != nil {
		return fmt.Errorf("gitlab reopen issue: %w", err)
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), iid)
	full := s.client.buildURL(path, nil)
	_, err = s.client.do(ctx, "PUT", full, map[string]any{"state_event": "reopen"}, nil, "reopen issue")
	return err
}

// EditIssue updates only the description; back-compat with the
// IssueService.EditIssue shape exposed by the GitHub adapter.
func (s *IssueService) EditIssue(ctx context.Context, nodeID, body string) (*forgetypes.Issue, error) {
	return s.UpdateIssue(ctx, nodeID, forge.UpdateIssueOptions{Body: &body})
}

// parseIssueRef extracts (owner, repo, iid) from "owner/repo#iid". Used by
// the mutate methods to rebuild the project path from a single opaque
// reference.
func parseIssueRef(ref string) (string, string, int, error) {
	hash := strings.LastIndex(ref, "#")
	if hash < 0 {
		return "", "", 0, fmt.Errorf("ref %q must be owner/repo#iid", ref)
	}
	repoPart := ref[:hash]
	iidPart := ref[hash+1:]
	owner, repo := splitProjectID(repoPart)
	if owner == "" || repo == "" {
		return "", "", 0, fmt.Errorf("ref %q has malformed owner/repo", ref)
	}
	iid, err := strconv.Atoi(iidPart)
	if err != nil {
		return "", "", 0, fmt.Errorf("ref %q has malformed iid: %w", ref, err)
	}
	return owner, repo, iid, nil
}

// splitProjectID splits "owner/repo" into its parts. Returns empty
// strings when the input is malformed.
func splitProjectID(full string) (string, string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// --- Stubs for IssueService methods not implemented in W2.4 (#3356) ---

func (s *IssueService) SearchIssues(ctx context.Context, owner, repo, query string, limit int) ([]forgetypes.Issue, error) {
	return nil, fmt.Errorf("gitlab.IssueService.SearchIssues: %w (tracked: future)", forge.ErrUnsupported)
}

func (s *IssueService) HasLabel(ctx context.Context, owner, repo string, number int, label string) (bool, error) {
	issue, err := s.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return false, err
	}
	for _, l := range issue.Labels {
		if l == label {
			return true, nil
		}
	}
	return false, nil
}

func (s *IssueService) GetRepoLabels(ctx context.Context, owner, repo string) (map[string]string, error) {
	return nil, fmt.Errorf("gitlab.IssueService.GetRepoLabels: %w (tracked: #3358)", forge.ErrUnsupported)
}

func (s *IssueService) AddComment(ctx context.Context, subjectID, body string) error {
	return fmt.Errorf("gitlab.IssueService.AddComment: %w (tracked: future)", forge.ErrUnsupported)
}

// AddSubIssue creates a parent→child relates_to link. On EE installs, the
// adapter additionally writes the native parent_id field as a best-effort
// secondary action; failure of that secondary write is non-fatal because
// the canonical readback is the relates_to link.
func (s *IssueService) AddSubIssue(ctx context.Context, parentID, childID string) error {
	pOwner, pRepo, pIid, err := parseIssueRef(parentID)
	if err != nil {
		return fmt.Errorf("gitlab AddSubIssue parent: %w", err)
	}
	cOwner, cRepo, cIid, err := parseIssueRef(childID)
	if err != nil {
		return fmt.Errorf("gitlab AddSubIssue child: %w", err)
	}
	if err := addIssueLink(ctx, s.client, pOwner, pRepo, pIid, cOwner, cRepo, cIid, linkTypeRelatesTo); err != nil {
		return err
	}
	if s.client.Edition(ctx) == EditionEE {
		_ = tryWriteParentID(ctx, s.client, cOwner, cRepo, cIid, pIid)
	}
	return nil
}

// RemoveSubIssue removes the parent→child relates_to link. Returns nil
// when no such link exists so the operation is idempotent.
func (s *IssueService) RemoveSubIssue(ctx context.Context, parentID, childID string) error {
	pOwner, pRepo, pIid, err := parseIssueRef(parentID)
	if err != nil {
		return fmt.Errorf("gitlab RemoveSubIssue parent: %w", err)
	}
	_, _, cIid, err := parseIssueRef(childID)
	if err != nil {
		return fmt.Errorf("gitlab RemoveSubIssue child: %w", err)
	}
	linkID, err := findLinkID(ctx, s.client, pOwner, pRepo, pIid, cIid, linkTypeRelatesTo)
	if err != nil {
		return err
	}
	if linkID == 0 {
		return nil
	}
	return deleteIssueLink(ctx, s.client, pOwner, pRepo, pIid, linkID)
}

// LinkSubIssue is a convenience that resolves number-keyed identifiers into
// "owner/repo#iid" refs and delegates to AddSubIssue.
func (s *IssueService) LinkSubIssue(ctx context.Context, owner, repo string, parentNumber, childNumber int) error {
	parentRef := fmt.Sprintf("%s/%s#%d", owner, repo, parentNumber)
	childRef := fmt.Sprintf("%s/%s#%d", owner, repo, childNumber)
	return s.AddSubIssue(ctx, parentRef, childRef)
}

// AddBlockedBy creates an is_blocked_by link from blockedID → blockerID.
// GitLab materialises the inverse blocks link on the blocker automatically.
func (s *IssueService) AddBlockedBy(ctx context.Context, blockedID, blockerID string) error {
	bOwner, bRepo, bIid, err := parseIssueRef(blockedID)
	if err != nil {
		return fmt.Errorf("gitlab AddBlockedBy blocked: %w", err)
	}
	rOwner, rRepo, rIid, err := parseIssueRef(blockerID)
	if err != nil {
		return fmt.Errorf("gitlab AddBlockedBy blocker: %w", err)
	}
	return addIssueLink(ctx, s.client, bOwner, bRepo, bIid, rOwner, rRepo, rIid, linkTypeIsBlockedBy)
}

// RemoveBlockedBy deletes the is_blocked_by link from blockedID → blockerID.
// GitLab removes the inverse blocks link on the blocker automatically.
// Returns nil when no such link exists.
func (s *IssueService) RemoveBlockedBy(ctx context.Context, blockedID, blockerID string) error {
	bOwner, bRepo, bIid, err := parseIssueRef(blockedID)
	if err != nil {
		return fmt.Errorf("gitlab RemoveBlockedBy blocked: %w", err)
	}
	_, _, rIid, err := parseIssueRef(blockerID)
	if err != nil {
		return fmt.Errorf("gitlab RemoveBlockedBy blocker: %w", err)
	}
	linkID, err := findLinkID(ctx, s.client, bOwner, bRepo, bIid, rIid, linkTypeIsBlockedBy)
	if err != nil {
		return err
	}
	if linkID == 0 {
		return nil
	}
	return deleteIssueLink(ctx, s.client, bOwner, bRepo, bIid, linkID)
}

func (s *IssueService) AddLabels(ctx context.Context, issueID string, labelIDs []string) error {
	return fmt.Errorf("gitlab.IssueService.AddLabels: %w (tracked: #3358)", forge.ErrUnsupported)
}

func (s *IssueService) RemoveLabels(ctx context.Context, issueID string, labelIDs []string) error {
	return fmt.Errorf("gitlab.IssueService.RemoveLabels: %w (tracked: #3358)", forge.ErrUnsupported)
}

func (s *IssueService) SyncStatusLabel(ctx context.Context, owner, repo string, number int, newStatus string) error {
	return fmt.Errorf("gitlab.IssueService.SyncStatusLabel: %w (tracked: #3357)", forge.ErrUnsupported)
}

func (s *IssueService) MarkRefined(ctx context.Context, owner, repo string, number int) error {
	return fmt.Errorf("gitlab.IssueService.MarkRefined: %w (tracked: #3358)", forge.ErrUnsupported)
}

func (s *IssueService) GetEpicProgress(ctx context.Context, epicNodeID string) (*forgetypes.EpicProgress, error) {
	return nil, fmt.Errorf("gitlab.IssueService.GetEpicProgress: %w (tracked: #3358)", forge.ErrUnsupported)
}

func (s *IssueService) GetEpicProgressByNumber(ctx context.Context, owner, repo string, number int) (*forgetypes.EpicProgress, error) {
	return nil, fmt.Errorf("gitlab.IssueService.GetEpicProgressByNumber: %w (tracked: #3358)", forge.ErrUnsupported)
}

// --- Page iterator helpers (shared with merge-request iteration) ---

// pageIterator walks a paginated GitLab REST endpoint via link-header
// "next" links, decoding each page through a caller-provided decoder.
// Generic over the decoded value type T.
type pageIterator[T any] struct {
	mu        sync.Mutex
	client    *Client
	nextURL   string
	op        string
	owner     string
	repo      string
	decoder   func(body []byte, owner, repo string) ([]T, error)
	queue     []T
	idx       int
	exhausted bool
	err       error
	closed    bool
}

func newPageIterator[T any](client *Client, startURL, op, owner, repo string,
	decoder func(body []byte, owner, repo string) ([]T, error),
) *pageIterator[T] {
	return &pageIterator[T]{
		client:  client,
		nextURL: startURL,
		op:      op,
		owner:   owner,
		repo:    repo,
		decoder: decoder,
	}
}

// Next returns the next decoded value, or io.EOF when the stream is
// exhausted. Errors from page fetches stop iteration and are returned
// from the next Next call (then sticky-cleared so callers don't see the
// same error twice).
func (it *pageIterator[T]) Next(ctx context.Context) (*T, error) {
	it.mu.Lock()
	defer it.mu.Unlock()

	if it.closed {
		return nil, io.EOF
	}

	for it.idx >= len(it.queue) {
		if it.exhausted {
			return nil, io.EOF
		}
		if err := it.fetchPageLocked(ctx); err != nil {
			it.err = nil
			return nil, err
		}
	}
	v := it.queue[it.idx]
	it.idx++
	return &v, nil
}

// fetchPageLocked fetches the next page of results into the queue. The
// caller must hold it.mu.
func (it *pageIterator[T]) fetchPageLocked(ctx context.Context) error {
	if it.nextURL == "" {
		it.exhausted = true
		return nil
	}
	body, headers, err := it.client.doRaw(ctx, "GET", it.nextURL, nil, it.op)
	if err != nil {
		it.exhausted = true
		return err
	}
	values, err := it.decoder(body, it.owner, it.repo)
	if err != nil {
		it.exhausted = true
		return err
	}
	it.queue = values
	it.idx = 0

	links := parseLinkHeader(headers.Get("Link"))
	if links.Next == nil {
		it.exhausted = true
		it.nextURL = ""
	} else {
		it.nextURL = links.Next.String()
	}
	return nil
}

// Close marks the iterator as exhausted. Idempotent.
func (it *pageIterator[T]) Close() error {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.closed = true
	return nil
}
