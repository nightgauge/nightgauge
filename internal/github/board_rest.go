package github

// REST reads of a ProjectV2 board.
//
// GitHub serves project items over REST (`GET /orgs/{o}/projectsV2/{n}/items`,
// and the /users/ equivalent), under the same `project` scope as GraphQL. The
// reason to prefer it is not the bucket but the conditional request: a GraphQL
// board read is a POST and is billed every time (17 points per 100-item page),
// while a REST page carries an ETag and an unchanged page answers 304 for
// nothing. With the persistent ConditionalStore behind condGet, re-reading an
// unchanged board costs zero — including after a daemon restart.
//
// What REST gives per item, as used here:
//   - `q` filters server-side with the same syntax as the board search
//     (`is:open`, `status:Ready`), so a filtered read stays one page;
//   - `fields[]` selects field values by field id (the ids are per board, read
//     from `/fields`, and a foreign id is a 400);
//   - the embedded issue carries `sub_issues_summary` and
//     `issue_dependencies_summary`: relationship COUNTS, not lists. Verified
//     live: `blocked_by` counts OPEN blockers (3 of 4 open reads blocked_by=3,
//     total_blocked_by=4). Counts are enough to say "blocked" and "epic"; a
//     caller that needs the lists gets them from the per-issue REST list
//     endpoints (restCompleteRelations), one request per non-empty list.
//
// Fallbacks: GitHub Enterprise Server (any API host but api.github.com) and a
// 404 from the projects endpoints go to the GraphQL reads, which remain the
// reference behaviour. A 404 is remembered for the life of the client.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// restItemsPerPage is REST's maximum page size for project items.
const restItemsPerPage = 100

// restRelationConcurrency bounds the per-issue relation-list requests one
// board read may have in flight.
const restRelationConcurrency = 8

// errRESTBoardUnavailable means the REST projects surface cannot serve this
// board and the caller must use the GraphQL read instead.
var errRESTBoardUnavailable = errors.New("REST projects endpoints unavailable")

// restProjectsHost is the only API host whose REST projects surface is used.
const restProjectsHost = "https://api.github.com"

// restProjectsUsable reports whether this client may try the REST projects
// endpoints for owner at all: never on GHES, never after a 404 for the owner.
func (c *Client) restProjectsUsable(owner string) bool {
	if c.restBaseURL() != restProjectsHost {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.restProjects404[strings.ToLower(owner)]
}

func (c *Client) markRESTProjectsUnavailable(owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.restProjects404 == nil {
		c.restProjects404 = map[string]bool{}
	}
	c.restProjects404[strings.ToLower(owner)] = true
}

// restProjectsPath is the REST collection for the owner's projects.
func restProjectsPath(ownerType OwnerType, owner string) string {
	if ownerType.IsUser() {
		return "/users/" + url.PathEscape(owner) + "/projectsV2"
	}
	return "/orgs/" + url.PathEscape(owner) + "/projectsV2"
}

func (b *BoardService) restBase() string {
	return fmt.Sprintf("%s/%d", restProjectsPath(b.ownerType, b.owner), b.projectNumber)
}

// restFallback converts a REST failure into errRESTBoardUnavailable when the
// GraphQL read should answer instead: a 404 (the surface does not exist for
// this owner — remembered), or a 400/422 (the request was refused as shaped,
// e.g. a field id the board no longer has — this call only).
func (b *BoardService) restFallback(err error) error {
	var se *restStatusError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Status {
	case http.StatusNotFound:
		b.client.markRESTProjectsUnavailable(b.owner)
		return fmt.Errorf("%w: %v", errRESTBoardUnavailable, err)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %v", errRESTBoardUnavailable, err)
	}
	return err
}

// restField is one board field as the item reads need it.
type restField struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// restBoardFieldIDs reads the board's field list (conditionally — an
// unchanged schema is a free 304) and returns the ids of the fields the item
// mapping uses, in a stable order.
func (b *BoardService) restBoardFieldIDs(ctx context.Context) ([]int64, error) {
	pages, _, err := b.client.condGetAll(ctx, b.restBase()+"/fields?per_page=100", func(body []byte) (any, error) {
		var raw []restField
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	})
	if err != nil {
		return nil, b.restFallback(err)
	}
	want := map[string]bool{"Status": true, "Priority": true, "Size": true, "Pipeline Stage": true, "Parent issue": true}
	var ids []int64
	for _, p := range pages {
		var fs []restField
		if err := json.Unmarshal(p, &fs); err != nil {
			return nil, err
		}
		for _, f := range fs {
			if want[f.Name] {
				ids = append(ids, f.ID)
			}
		}
	}
	return ids, nil
}

// restItemsURL is the items page-one URL for query q with the given fields.
func (b *BoardService) restItemsURL(q string, fieldIDs []int64) string {
	v := url.Values{}
	v.Set("per_page", fmt.Sprint(restItemsPerPage))
	if q != "" {
		v.Set("q", q)
	}
	for _, id := range fieldIDs {
		v.Add("fields[]", fmt.Sprint(id))
	}
	return b.restBase() + "/items?" + v.Encode()
}

// restItem is the reduced, stored form of one REST project item: exactly the
// fields BoardItem is built from, so a 2.5 MB page is stored as a few KB.
type restItem struct {
	ID                string                `json:"id"`
	Type              string                `json:"type"`
	Number            int                   `json:"number,omitempty"`
	Title             string                `json:"title,omitempty"`
	State             string                `json:"state,omitempty"`
	URL               string                `json:"url,omitempty"`
	Repo              string                `json:"repo,omitempty"`
	CreatedAt         string                `json:"createdAt,omitempty"`
	UpdatedAt         string                `json:"updatedAt,omitempty"`
	AuthorAssociation string                `json:"authorAssociation,omitempty"`
	Labels            []string              `json:"labels,omitempty"`
	Status            string                `json:"status,omitempty"`
	Priority          string                `json:"priority,omitempty"`
	Size              string                `json:"size,omitempty"`
	PipelineStage     string                `json:"pipelineStage,omitempty"`
	ParentNumber      int                   `json:"parentNumber,omitempty"`
	ParentTitle       string                `json:"parentTitle,omitempty"`
	Relations         types.RelationSummary `json:"relations"`
}

// restIssueRepo is the subset of a REST issue / pull request / repository
// reference used to name the owning repository.
type restIssueRepo struct {
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	RepositoryURL string `json:"repository_url"`
	HTMLURL       string `json:"html_url"`
}

// repoName resolves "owner/name" from whichever reference the object carries.
func (r restIssueRepo) repoName() string {
	if r.Repository != nil && r.Repository.FullName != "" {
		return r.Repository.FullName
	}
	if i := strings.Index(r.RepositoryURL, "/repos/"); i >= 0 {
		return r.RepositoryURL[i+len("/repos/"):]
	}
	// https://github.com/<owner>/<name>/(issues|pull)/<n>
	if u, err := url.Parse(r.HTMLURL); err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}

type restProjectItemRaw struct {
	NodeID      string  `json:"node_id"`
	ContentType string  `json:"content_type"`
	ArchivedAt  *string `json:"archived_at"`
	Content     *struct {
		restIssueRepo
		Number            int     `json:"number"`
		Title             string  `json:"title"`
		State             string  `json:"state"`
		MergedAt          *string `json:"merged_at"`
		CreatedAt         string  `json:"created_at"`
		UpdatedAt         string  `json:"updated_at"`
		AuthorAssociation string  `json:"author_association"`
		Labels            []struct {
			Name string `json:"name"`
		} `json:"labels"`
		SubIssuesSummary *struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
		} `json:"sub_issues_summary"`
		IssueDependenciesSummary *struct {
			BlockedBy      int `json:"blocked_by"`
			TotalBlockedBy int `json:"total_blocked_by"`
			Blocking       int `json:"blocking"`
			TotalBlocking  int `json:"total_blocking"`
		} `json:"issue_dependencies_summary"`
	} `json:"content"`
	Fields []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"fields"`
}

// fieldText reads a REST field value as text: a single-select option's name,
// a text field's raw value, or a bare string.
func fieldText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Name json.RawMessage `json:"name"`
		Raw  *string         `json:"raw"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if obj.Raw != nil {
		return *obj.Raw
	}
	if len(obj.Name) > 0 {
		var name string
		if json.Unmarshal(obj.Name, &name) == nil {
			return name
		}
		var rich struct {
			Raw string `json:"raw"`
		}
		if json.Unmarshal(obj.Name, &rich) == nil {
			return rich.Raw
		}
	}
	return ""
}

// reduceItemsPage decodes one REST items page into its stored form.
func reduceItemsPage(body []byte) (any, error) {
	var raw []restProjectItemRaw
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]restItem, 0, len(raw))
	for _, r := range raw {
		if r.ArchivedAt != nil && *r.ArchivedAt != "" {
			// The GraphQL items connection does not list archived items;
			// neither does this read.
			continue
		}
		it := restItem{ID: r.NodeID, Type: r.ContentType}
		if c := r.Content; c != nil && (r.ContentType == "Issue" || r.ContentType == "PullRequest") {
			it.Number = c.Number
			it.Title = c.Title
			it.State = strings.ToUpper(c.State)
			if r.ContentType == "PullRequest" && c.MergedAt != nil && *c.MergedAt != "" {
				it.State = "MERGED"
			}
			it.URL = c.HTMLURL
			it.Repo = c.repoName()
			it.CreatedAt = c.CreatedAt
			it.UpdatedAt = c.UpdatedAt
			if r.ContentType == "Issue" {
				it.AuthorAssociation = c.AuthorAssociation
			}
			for _, l := range c.Labels {
				it.Labels = append(it.Labels, l.Name)
			}
			if s := c.SubIssuesSummary; s != nil {
				it.Relations.SubIssuesTotal = s.Total
				it.Relations.SubIssuesCompleted = s.Completed
			}
			if d := c.IssueDependenciesSummary; d != nil {
				it.Relations.BlockedByOpen = d.BlockedBy
				it.Relations.BlockedByTotal = d.TotalBlockedBy
				it.Relations.BlockingOpen = d.Blocking
				it.Relations.BlockingTotal = d.TotalBlocking
			}
		}
		for _, f := range r.Fields {
			switch f.Name {
			case "Status":
				it.Status = fieldText(f.Value)
			case "Priority":
				it.Priority = fieldText(f.Value)
			case "Size":
				it.Size = fieldText(f.Value)
			case "Pipeline Stage":
				it.PipelineStage = fieldText(f.Value)
			case "Parent issue":
				if len(f.Value) > 0 && string(f.Value) != "null" {
					var p struct {
						Number int    `json:"number"`
						Title  string `json:"title"`
					}
					if json.Unmarshal(f.Value, &p) == nil {
						it.ParentNumber = p.Number
						it.ParentTitle = p.Title
					}
				}
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// toBoardItem maps a stored REST item to a BoardItem the way nodeToItem maps
// a GraphQL node: same fields, same label fallbacks for Priority and Size,
// same epic rule (any sub-issue, or the type:epic label). Relationship lists
// are left empty — RelationSummary says what they hold. Items that are
// neither issues nor pull requests (draft issues) are dropped, as nodeToItem
// drops them.
func (it restItem) toBoardItem() (types.BoardItem, bool) {
	if it.Type != "Issue" && it.Type != "PullRequest" {
		return types.BoardItem{}, false
	}
	item := types.BoardItem{
		ID:                it.ID,
		Number:            it.Number,
		Title:             it.Title,
		State:             it.State,
		URL:               it.URL,
		Repo:              it.Repo,
		IsPR:              it.Type == "PullRequest",
		AuthorAssociation: it.AuthorAssociation,
		Labels:            append([]string(nil), it.Labels...),
		Status:            it.Status,
		Priority:          types.Priority(it.Priority),
		Size:              types.Size(it.Size),
		PipelineStage:     it.PipelineStage,
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339, it.CreatedAt)
	item.UpdatedAt, _ = time.Parse(time.RFC3339, it.UpdatedAt)
	if !item.IsPR {
		item.IsEpic = it.Relations.SubIssuesTotal > 0 || hasTypeEpicLabel(item.Labels)
		item.ParentNumber = it.ParentNumber
		item.ParentTitle = it.ParentTitle
		rel := it.Relations
		item.RelationSummary = &rel
	}
	if item.Priority == "" {
		item.Priority = priorityFromLabels(item.Labels)
	}
	if item.Size == "" {
		item.Size = sizeFromLabels(item.Labels)
	}
	return item, true
}

// restListItems reads every item matching q over REST, returning the mapped
// items (relationship lists empty) and the raw item count.
func (b *BoardService) restListItems(ctx context.Context, q string) ([]types.BoardItem, int, error) {
	if !b.client.restProjectsUsable(b.owner) {
		return nil, 0, errRESTBoardUnavailable
	}
	fieldIDs, err := b.restBoardFieldIDs(ctx)
	if err != nil {
		return nil, 0, err
	}
	pages, _, err := b.client.condGetAll(ctx, b.restItemsURL(q, fieldIDs), reduceItemsPage)
	if err != nil {
		return nil, 0, b.restFallback(err)
	}
	var items []types.BoardItem
	raw := 0
	seen := map[string]bool{}
	for _, p := range pages {
		var page []restItem
		if err := json.Unmarshal(p, &page); err != nil {
			return nil, 0, fmt.Errorf("decode stored board page: %w", err)
		}
		for _, it := range page {
			// An item that moved between pages while the walk ran can appear
			// twice; the board holds it once.
			if it.ID != "" && seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			raw++
			if item, ok := it.toBoardItem(); ok {
				items = append(items, item)
			}
		}
	}
	return items, raw, nil
}

// ListOpenItemsSummary returns the board's open items with relationship
// COUNTS (BoardItem.RelationSummary) instead of lists. It is the read for
// every consumer that only asks "is it blocked / an epic / in which status" —
// the Repositories tree, board counts, and the attention sweep — and on
// github.com it is served by REST pages, each revalidated with its stored
// ETag, so an unchanged board costs no rate limit at all.
//
// Falls back to the GraphQL ListOpenItems (and derives the counts from its
// lists) on GHES or when the REST endpoints answer 404.
func (b *BoardService) ListOpenItemsSummary(ctx context.Context) ([]types.BoardItem, int, error) {
	items, raw, err := b.restListItems(ctx, "is:open")
	if err == nil {
		return items, raw, nil
	}
	if !errors.Is(err, errRESTBoardUnavailable) {
		return nil, 0, fmt.Errorf("fetch board items (open, REST): %w", err)
	}
	items, raw, err = b.ListOpenItems(ctx)
	if err != nil {
		return nil, 0, err
	}
	return withDerivedSummaries(items), raw, nil
}

// withDerivedSummaries sets RelationSummary on items read with their lists.
func withDerivedSummaries(items []types.BoardItem) []types.BoardItem {
	for i := range items {
		if items[i].IsPR {
			continue
		}
		s := types.SummarizeRelations(items[i])
		items[i].RelationSummary = &s
	}
	return items
}

// restListItemsWithRelations is restListItems plus every issue's relationship
// lists read whole over REST (restCompleteRelations).
func (b *BoardService) restListItemsWithRelations(ctx context.Context, q string) ([]types.BoardItem, error) {
	items, _, err := b.restListItems(ctx, q)
	if err != nil {
		return nil, err
	}
	if err := b.client.restCompleteRelations(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

// restRef is the stored form of one related issue.
type restRef struct {
	NodeID string `json:"nodeId"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Repo   string `json:"repo"`
}

func reduceRefsPage(body []byte) (any, error) {
	var raw []struct {
		restIssueRepo
		NodeID string `json:"node_id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]restRef, 0, len(raw))
	for _, r := range raw {
		out = append(out, restRef{NodeID: r.NodeID, Number: r.Number, Title: r.Title, State: strings.ToUpper(r.State), Repo: r.repoName()})
	}
	return out, nil
}

// restRefs reads one relationship list whole (all pages).
func (c *Client) restRefs(ctx context.Context, path string) ([]restRef, error) {
	pages, _, err := c.condGetAll(ctx, path, reduceRefsPage)
	if err != nil {
		return nil, err
	}
	var out []restRef
	for _, p := range pages {
		var refs []restRef
		if err := json.Unmarshal(p, &refs); err != nil {
			return nil, err
		}
		out = append(out, refs...)
	}
	return out, nil
}

// restCompleteRelations fills each issue's SubIssues, BlockedBy and Blocking
// lists from the per-issue REST list endpoints, asking only for the lists the
// item's summary says are non-empty. Every list is read whole or the read
// fails — a short list would read as "not blocked", which is the answer
// callers act on. Each list is conditional, so an unchanged one is free.
func (c *Client) restCompleteRelations(ctx context.Context, items []types.BoardItem) error {
	type job struct {
		idx  int
		kind int // 0 sub-issues, 1 blocked-by, 2 blocking
		path string
	}
	var jobs []job
	for i := range items {
		it := &items[i]
		if it.IsPR || it.RelationSummary == nil || it.Repo == "" || it.Number == 0 {
			continue
		}
		base := fmt.Sprintf("/repos/%s/issues/%d", it.Repo, it.Number)
		if it.RelationSummary.SubIssuesTotal > 0 {
			jobs = append(jobs, job{i, 0, base + "/sub_issues?per_page=100"})
		}
		if it.RelationSummary.BlockedByTotal > 0 {
			jobs = append(jobs, job{i, 1, base + "/dependencies/blocked_by?per_page=100"})
		}
		if it.RelationSummary.BlockingTotal > 0 {
			jobs = append(jobs, job{i, 2, base + "/dependencies/blocking?per_page=100"})
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	results := make([][]restRef, len(jobs))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		sem      = make(chan struct{}, restRelationConcurrency)
	)
	for j := range jobs {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			refs, err := c.restRefs(ctx, jobs[j].path)
			if err != nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("read relationship list %s: %w", jobs[j].path, err); cancel() })
				return
			}
			results[j] = refs
		}(j)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	for j, jb := range jobs {
		it := &items[jb.idx]
		for _, r := range results[j] {
			switch jb.kind {
			case 0:
				it.SubIssues = append(it.SubIssues, types.SubIssueRef{NodeID: r.NodeID, Number: r.Number, Title: r.Title, State: r.State, Repo: r.Repo})
			case 1:
				it.BlockedBy = append(it.BlockedBy, types.BlockingRef{NodeID: r.NodeID, Number: r.Number, Title: r.Title, State: r.State, Repo: r.Repo})
			case 2:
				it.Blocking = append(it.Blocking, types.BlockingRef{NodeID: r.NodeID, Number: r.Number, Title: r.Title, State: r.State, Repo: r.Repo})
			}
		}
	}
	return nil
}

// restProject is the stored form of one entry of the owner's project list.
type restProject struct {
	Number    int    `json:"number"`
	UpdatedAt string `json:"updatedAt"`
}

// projectListMemo holds one owner's project list for projectListTTL, so the
// probes of several boards in one burst share one request.
type projectListMemo struct {
	at       time.Time
	projects map[int]string
}

// projectListTTL is how long one project-list answer serves every board of
// that owner. The list is conditional, so this only saves round trips.
const projectListTTL = 10 * time.Second

// restProjectUpdatedAt reads the board's updated_at from the owner's project
// list: ONE conditional request answers for every board the owner has, and an
// unchanged list is a free 304.
func (b *BoardService) restProjectUpdatedAt(ctx context.Context) (time.Time, error) {
	if !b.client.restProjectsUsable(b.owner) {
		return time.Time{}, errRESTBoardUnavailable
	}
	key := string(b.ownerType) + "|" + strings.ToLower(b.owner)
	b.client.mu.Lock()
	memo, ok := b.client.projectLists[key]
	b.client.mu.Unlock()
	if !ok || time.Since(memo.at) >= projectListTTL {
		pages, _, err := b.client.condGetAll(ctx, restProjectsPath(b.ownerType, b.owner)+"?per_page=100", func(body []byte) (any, error) {
			var raw []struct {
				Number    int    `json:"number"`
				UpdatedAt string `json:"updated_at"`
			}
			if err := json.Unmarshal(body, &raw); err != nil {
				return nil, err
			}
			out := make([]restProject, 0, len(raw))
			for _, r := range raw {
				out = append(out, restProject{Number: r.Number, UpdatedAt: r.UpdatedAt})
			}
			return out, nil
		})
		if err != nil {
			return time.Time{}, b.restFallback(err)
		}
		memo = projectListMemo{at: time.Now(), projects: map[int]string{}}
		for _, p := range pages {
			var ps []restProject
			if err := json.Unmarshal(p, &ps); err != nil {
				return time.Time{}, err
			}
			for _, pr := range ps {
				memo.projects[pr.Number] = pr.UpdatedAt
			}
		}
		b.client.mu.Lock()
		if b.client.projectLists == nil {
			b.client.projectLists = map[string]projectListMemo{}
		}
		b.client.projectLists[key] = memo
		b.client.mu.Unlock()
	}
	raw, ok := memo.projects[b.projectNumber]
	if !ok {
		// Not in the list (a closed project is not listed): the GraphQL probe
		// answers for it rather than this guessing.
		return time.Time{}, errRESTBoardUnavailable
	}
	if raw == "" {
		return time.Time{}, fmt.Errorf("probe board updatedAt: empty timestamp")
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("probe board updatedAt: parse %q: %w", raw, err)
	}
	return ts, nil
}

// CacheIdentity reports the token identity this board's reads are made with
// (boardcache.IdentityReporter), so the snapshot cache keys by it.
func (b *BoardService) CacheIdentity() string { return b.client.CacheIdentity() }

// SummaryReadAvailable reports whether ListOpenItemsSummary is served by the
// REST summary read right now (boardcache.SummaryAvailability). False on
// GHES and after a 404 for the owner, where it would answer from the GraphQL
// open read.
func (b *BoardService) SummaryReadAvailable() bool { return b.client.restProjectsUsable(b.owner) }
