package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

// BoardService implements forge.BoardService for GitLab. Board enumeration
// is rooted at /api/v4/projects/:id/issues since GitLab boards display every
// project issue regardless of an explicit "add to board" step.
type BoardService struct {
	client *Client
	owner  string
	repo   string
}

// NewBoardService constructs a GitLab BoardService bound to a given REST
// client. The board's owner/repo are zero-valued by default; the
// ForgeAdapter wires them from forge.Config.Owner via NewBoardServiceFor.
func NewBoardService(client *Client) *BoardService {
	return &BoardService{client: client}
}

// NewBoardServiceFor binds a BoardService to a specific owner/repo so the
// number-keyed methods (GetItem) know which project to target without the
// caller having to thread it through every call.
func NewBoardServiceFor(client *Client, owner, repo string) *BoardService {
	return &BoardService{client: client, owner: owner, repo: repo}
}

// projectPathForBoard resolves the bound owner/repo. When neither is set the
// GitLab adapter has no project context — board operations require one.
func (b *BoardService) projectPathForBoard(owner, repo string) (string, string, string, error) {
	o := owner
	r := repo
	if o == "" {
		o = b.owner
	}
	if r == "" {
		r = b.repo
	}
	if o == "" || r == "" {
		return "", "", "", fmt.Errorf("gitlab board: owner/repo not configured")
	}
	return o, r, projectPath(o, r), nil
}

// rawIssueList is the JSON shape of GET /api/v4/projects/:id/issues. The
// fields tracked here are the ones the BoardItem mapping consumes; unknown
// fields are ignored by the JSON decoder.
type rawIssueList struct {
	ID           int64         `json:"id"`
	IID          int           `json:"iid"`
	Title        string        `json:"title"`
	State        string        `json:"state"`
	Labels       []string      `json:"labels"`
	WebURL       string        `json:"web_url"`
	CreatedAt    string        `json:"created_at"`
	UpdatedAt    string        `json:"updated_at"`
	Weight       *int          `json:"weight,omitempty"`
	HealthStatus string        `json:"health_status,omitempty"`
	Iteration    *rawIteration `json:"iteration,omitempty"`
	Milestone    *rawMilestone `json:"milestone,omitempty"`
}

type rawIteration struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	StartDate string `json:"start_date,omitempty"`
	DueDate   string `json:"due_date,omitempty"`
}

type rawMilestone struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	StartDate string `json:"start_date,omitempty"`
	DueDate   string `json:"due_date,omitempty"`
}

// rawIssueToBoardItem converts a GitLab issue payload into the forge-agnostic
// BoardItem. Status / Priority / Size are derived from scoped labels via
// ParseScopedLabel. Iteration / Weight / Health are not surfaced on
// BoardItem today (no field exists), so they round-trip through GetItem only
// implicitly via the labels payload.
func rawIssueToBoardItem(raw rawIssueList, owner, repo string) *forgetypes.BoardItem {
	item := &forgetypes.BoardItem{
		ID:     fmt.Sprintf("gitlab:%s/%s#%d", owner, repo, raw.IID),
		NodeID: strconv.FormatInt(raw.ID, 10),
		Number: raw.IID,
		Title:  raw.Title,
		State:  raw.State,
		URL:    raw.WebURL,
		Repo:   owner + "/" + repo,
		Labels: append([]string(nil), raw.Labels...),
		IsPR:   false,
	}
	if t, err := time.Parse(time.RFC3339, raw.CreatedAt); err == nil {
		item.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, raw.UpdatedAt); err == nil {
		item.UpdatedAt = t
	}

	if status := scopedLabelValue(item.Labels, "Status"); status != "" {
		item.Status = status
	}
	if priority := scopedLabelValue(item.Labels, "Priority"); priority != "" {
		item.Priority = pkgtypes.Priority(priority)
	} else {
		item.Priority = priorityFromLabels(item.Labels)
	}
	if size := scopedLabelValue(item.Labels, "Size"); size != "" {
		item.Size = pkgtypes.Size(size)
	} else {
		item.Size = sizeFromLabels(item.Labels)
	}

	for _, l := range item.Labels {
		if l == "type:epic" {
			item.IsEpic = true
			break
		}
	}
	return item
}

// scopedLabelValue returns the value of a "<prefix>::<value>" scoped label
// when present in labels, or empty string when no matching label exists. The
// last match wins so callers that pre-applied a write read it back even if
// older Status::* labels still linger (defensive against partial writes).
func scopedLabelValue(labels []string, prefix string) string {
	want := prefix + "::"
	out := ""
	for _, l := range labels {
		if strings.HasPrefix(l, want) {
			out = strings.TrimPrefix(l, want)
		}
	}
	return out
}

// priorityFromLabels lifts the legacy "priority:high" non-scoped convention
// onto the typed Priority enum. Mirrors github.priorityFromLabels so the
// fallback for repos that haven't migrated to Priority::P0 still works.
func priorityFromLabels(labels []string) pkgtypes.Priority {
	for _, l := range labels {
		switch l {
		case "priority:critical":
			return pkgtypes.PriorityP0
		case "priority:high":
			return pkgtypes.PriorityP1
		case "priority:medium":
			return pkgtypes.PriorityP2
		case "priority:low":
			return pkgtypes.PriorityP3
		}
	}
	return ""
}

// sizeFromLabels lifts the legacy "size:M" non-scoped convention onto the
// typed Size enum. Mirrors github.sizeFromLabels.
func sizeFromLabels(labels []string) pkgtypes.Size {
	for _, l := range labels {
		switch l {
		case "size:XS":
			return pkgtypes.SizeXS
		case "size:S":
			return pkgtypes.SizeS
		case "size:M":
			return pkgtypes.SizeM
		case "size:L":
			return pkgtypes.SizeL
		case "size:XL":
			return pkgtypes.SizeXL
		}
	}
	return ""
}

// listItemsQuery returns the query parameters for /issues with a given state
// filter and an optional Status::<value> scoped-label filter for server-side
// status filtering.
func listItemsQuery(state, statusFilter string) url.Values {
	q := url.Values{}
	if state == "" {
		state = "all"
	}
	q.Set("state", state)
	q.Set("per_page", "100")
	if statusFilter != "" {
		q.Set("labels", "Status::"+statusFilter)
	}
	return q
}

// ListItems fetches all issues on the bound project, optionally filtered to
// a single Status::<value> scoped label. Paginates link-header until
// exhausted.
func (b *BoardService) ListItems(ctx context.Context, statusFilter string) ([]forgetypes.BoardItem, error) {
	owner, repo, _, err := b.projectPathForBoard("", "")
	if err != nil {
		return nil, err
	}
	return b.listItems(ctx, owner, repo, "all", statusFilter)
}

// ListOpenItems fetches only open issues from the bound project. Returns the
// items, the raw count from GitLab (currently equal to len(items)), and any
// error. The raw count exists for parity with GitHub's BoardService.
func (b *BoardService) ListOpenItems(ctx context.Context) ([]forgetypes.BoardItem, int, error) {
	owner, repo, _, err := b.projectPathForBoard("", "")
	if err != nil {
		return nil, 0, err
	}
	items, err := b.listItems(ctx, owner, repo, "opened", "")
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

// listItems is the shared pagination helper for ListItems and ListOpenItems.
func (b *BoardService) listItems(ctx context.Context, owner, repo, state, statusFilter string) ([]forgetypes.BoardItem, error) {
	path := fmt.Sprintf("/projects/%s/issues", projectPath(owner, repo))
	full := b.client.buildURL(path, listItemsQuery(state, statusFilter))

	all := make([]forgetypes.BoardItem, 0)
	for full != "" {
		var page []rawIssueList
		resp, err := b.client.do(ctx, "GET", full, nil, &page, "list board items")
		if err != nil {
			return nil, err
		}
		for _, raw := range page {
			if item := rawIssueToBoardItem(raw, owner, repo); item != nil {
				all = append(all, *item)
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

// CountsByStatus returns per-status item counts. Implemented as five
// concurrent HEAD-equivalent requests (per_page=1) that read X-Total from
// the response header. Each request only filters by Status::<value>; the
// "Done" bucket includes closed issues (matching the GitHub adapter's
// CountsByStatus behaviour).
func (b *BoardService) CountsByStatus(ctx context.Context) (*forgetypes.StatusCounts, error) {
	owner, repo, _, err := b.projectPathForBoard("", "")
	if err != nil {
		return nil, err
	}

	type bucket struct {
		name  string
		state string
		count int
		err   error
	}
	buckets := []*bucket{
		{name: "Ready", state: "opened"},
		{name: "In progress", state: "opened"},
		{name: "In review", state: "opened"},
		{name: "Done", state: "all"}, // include closed
		{name: "Backlog", state: "opened"},
	}

	done := make(chan struct{}, len(buckets))
	for _, bk := range buckets {
		bk := bk
		go func() {
			defer func() { done <- struct{}{} }()
			bk.count, bk.err = b.countForStatus(ctx, owner, repo, bk.name, bk.state)
		}()
	}
	for range buckets {
		<-done
	}
	for _, bk := range buckets {
		if bk.err != nil {
			return nil, fmt.Errorf("count %s: %w", bk.name, bk.err)
		}
	}

	out := &forgetypes.StatusCounts{}
	for _, bk := range buckets {
		switch bk.name {
		case "Ready":
			out.Ready = bk.count
		case "In progress":
			out.InProgress = bk.count
		case "In review":
			out.InReview = bk.count
		case "Done":
			out.Done = bk.count
		case "Backlog":
			out.Backlog = bk.count
		}
	}
	return out, nil
}

// countForStatus fetches the bucket count for a single Status::<value>
// label. GitLab returns the total via the X-Total header for paginated
// endpoints; reading per_page=1 is the cheapest representation.
func (b *BoardService) countForStatus(ctx context.Context, owner, repo, status, state string) (int, error) {
	q := url.Values{}
	q.Set("state", state)
	q.Set("per_page", "1")
	q.Set("labels", "Status::"+status)
	full := b.client.buildURL(
		fmt.Sprintf("/projects/%s/issues", projectPath(owner, repo)), q,
	)
	_, headers, err := b.client.doRaw(ctx, "GET", full, nil, "count "+status)
	if err != nil {
		return 0, err
	}
	totalStr := headers.Get("X-Total")
	if totalStr == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(totalStr)
	if err != nil {
		return 0, fmt.Errorf("decode X-Total %q: %w", totalStr, err)
	}
	return n, nil
}

// GetItem fetches a single board item identified by issue number and
// enriches it with sub-issue + blocking link arrays via a single
// /issues/:iid/links call. Returns forge.ErrNotFound when the issue does
// not exist on the project. Link fetch failures are non-fatal — the item
// is returned with empty link slices so consumers behave the same way as
// they do for GitHub-backed projects with no sub-issues.
func (b *BoardService) GetItem(ctx context.Context, owner, repo string, issueNumber int) (*forgetypes.BoardItem, error) {
	o, r, projPath, err := b.projectPathForBoard(owner, repo)
	if err != nil {
		return nil, err
	}
	full := b.client.buildURL(
		fmt.Sprintf("/projects/%s/issues/%d", projPath, issueNumber), nil,
	)
	var raw rawIssueList
	if _, err := b.client.do(ctx, "GET", full, nil, &raw, fmt.Sprintf("get board item #%d", issueNumber)); err != nil {
		return nil, err
	}
	if raw.IID == 0 {
		return nil, fmt.Errorf("get board item #%d: %w", issueNumber, forge.ErrNotFound)
	}
	item := rawIssueToBoardItem(raw, o, r)
	if item == nil {
		return nil, nil
	}
	if links, err := listIssueLinks(ctx, b.client, o, r, issueNumber); err == nil {
		subIssues, blockedBy, blocking := classifyLinks(o, r, links)
		item.SubIssues = subIssues
		item.BlockedBy = blockedBy
		item.Blocking = blocking
		if len(subIssues) > 0 {
			item.IsEpic = true
		}
	}
	return item, nil
}

// MarshalRawIssue is exposed for tests in sibling files (project_test.go,
// parity tests) that need to construct a stub server response without
// re-deriving the JSON shape. Encoding errors return an empty payload —
// tests treat that as a programming bug.
func MarshalRawIssue(iid int, title string, labels []string) string {
	raw := rawIssueList{
		ID:        int64(iid * 1000),
		IID:       iid,
		Title:     title,
		State:     "opened",
		Labels:    labels,
		WebURL:    fmt.Sprintf("https://gitlab.example.com/o/r/-/issues/%d", iid),
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
	b, _ := json.Marshal(raw)
	return string(b)
}
