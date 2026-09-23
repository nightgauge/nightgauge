package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// restFake is an api.github.com transport for the REST board reads. Each path
// (query string included) maps to a body; the ETag is the body's hash, so a
// conditional request is answered 304 exactly when the body is unchanged.
type restFake struct {
	t      *testing.T
	mu     sync.Mutex
	bodies map[string]string // "path?query" → body; "path" matches any query
	links  map[string]string // same key → Link header
	status map[string]int    // same key → non-200 status
	gql    string            // GraphQL answer
	seen   []string          // "GET path?query inm=<etag>" per request
	gqlN   int
}

func newRESTFake(t *testing.T) *restFake {
	return &restFake{t: t, bodies: map[string]string{}, links: map[string]string{}, status: map[string]int{}}
}

func (f *restFake) key(req *http.Request) string {
	full := req.URL.Path
	if req.URL.RawQuery != "" {
		full += "?" + req.URL.RawQuery
	}
	if _, ok := f.bodies[full]; ok {
		return full
	}
	if _, ok := f.status[full]; ok {
		return full
	}
	return req.URL.Path
}

func (f *restFake) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"application/json"}}, Request: req}
	if req.URL.Path == "/graphql" {
		f.gqlN++
		f.seen = append(f.seen, "POST /graphql")
		resp.StatusCode = 200
		resp.Body = io.NopCloser(strings.NewReader(f.gql))
		return resp, nil
	}
	k := f.key(req)
	f.seen = append(f.seen, "GET "+req.URL.RequestURI()+" inm="+req.Header.Get("If-None-Match"))
	if st, ok := f.status[k]; ok {
		resp.StatusCode = st
		resp.Body = io.NopCloser(strings.NewReader(`{"message":"Not Found"}`))
		return resp, nil
	}
	body, ok := f.bodies[k]
	if !ok {
		f.t.Errorf("restFake: no fixture for %s", req.URL.RequestURI())
		resp.StatusCode = 418
		resp.Body = io.NopCloser(strings.NewReader(`{}`))
		return resp, nil
	}
	sum := sha256.Sum256([]byte(body))
	etag := `W/"` + hex.EncodeToString(sum[:6]) + `"`
	resp.Header.Set("ETag", etag)
	if l := f.links[k]; l != "" {
		resp.Header.Set("Link", l)
	}
	if req.Header.Get("If-None-Match") == etag {
		resp.StatusCode = http.StatusNotModified
		resp.Body = io.NopCloser(strings.NewReader(""))
		return resp, nil
	}
	resp.StatusCode = 200
	resp.Body = io.NopCloser(strings.NewReader(body))
	return resp, nil
}

func (f *restFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *restFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = nil
	f.gqlN = 0
}

func restClient(f *restFake, store *ConditionalStore, identity string) *Client {
	return NewClientWithHTTPClient(&http.Client{Transport: f}).WithConditionalStore(store, identity)
}

func countPrefix(reqs []string, prefix string) (n int) {
	for _, r := range reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// A 304 hands back the payload the earlier 200 was reduced to — and does so
// for a NEW client over the same disk store, which is a daemon restart.
func TestCondGet_NotModifiedServesTheStoredPayloadAcrossARestart(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/repos/o/r"] = `{"full_name":"o/r","name":"r","owner":{"login":"o"},"default_branch":"main","size":1}`
	store := NewConditionalStore(t.TempDir())
	reduce := func(body []byte) (any, error) {
		var v struct {
			FullName string `json:"full_name"`
		}
		err := json.Unmarshal(body, &v)
		return v.FullName, err
	}

	first, err := restClient(f, store, "tok:a").condGet(context.Background(), "/repos/o/r", "test-fullname/v1", reduce)
	if err != nil || first.Status != 200 || first.NotModified || string(first.Payload) != `"o/r"` {
		t.Fatalf("first read = %+v, %v", first, err)
	}
	// Restart: a new client, a new memory mirror, the same directory.
	restarted := restClient(f, NewConditionalStore(store.dir), "tok:a")
	second, err := restarted.condGet(context.Background(), "/repos/o/r", "test-fullname/v1", reduce)
	if err != nil {
		t.Fatal(err)
	}
	if !second.NotModified || string(second.Payload) != `"o/r"` || second.ETag == "" {
		t.Fatalf("second read = %+v, want a 304 serving the stored payload", second)
	}
	reqs := f.requests()
	if !strings.HasSuffix(reqs[1], "inm="+second.ETag) {
		t.Fatalf("the restarted client did not revalidate with the stored ETag: %v", reqs)
	}
}

// Stored answers are keyed by token identity: another token's read of the same
// URL must go to the server, never be served the first token's payload.
func TestConditionalStore_IsKeyedByIdentity(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/repos/o/r/pulls"] = `[{"number":1}]`
	store := NewConditionalStore(t.TempDir())
	if _, _, err := restClient(f, store, "tok:a").openPulls(context.Background(), "o", "r"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restClient(f, store, "tok:b").openPulls(context.Background(), "o", "r"); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 2 || !strings.HasSuffix(reqs[1], "inm=") {
		t.Fatalf("identity b was sent identity a's validator: %v", reqs)
	}
}

// A multi-page list that changed is walked a second time, conditionally, so the
// pages returned all describe one state of the list; an unchanged list is
// walked once.
func TestCondGetAll_RewalksAChangedListAndAcceptsAnUnchangedOne(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/list?page=1"] = `[1,2]`
	f.links["/list?page=1"] = `<https://api.github.com/list?page=2>; rel="next"`
	f.bodies["/list?page=2"] = `[3]`
	c := restClient(f, NewConditionalStore(""), "tok:a")
	reduce := func(b []byte) (any, error) { var v []int; err := json.Unmarshal(b, &v); return v, err }

	pages, changed, err := c.condGetAll(context.Background(), "/list?page=1", "test-ints/v1", reduce)
	if err != nil || !changed || len(pages) != 2 {
		t.Fatalf("cold walk = %v changed=%v err=%v", pages, changed, err)
	}
	if n := len(f.requests()); n != 4 {
		t.Fatalf("cold walk issued %d requests, want 4 (two pages, then a confirming conditional walk): %v", n, f.requests())
	}

	f.reset()
	if _, changed, _ = c.condGetAll(context.Background(), "/list?page=1", "test-ints/v1", reduce); changed {
		t.Fatal("an unchanged list reported a change")
	}
	if n := len(f.requests()); n != 2 {
		t.Fatalf("unchanged walk issued %d requests, want 2 (no re-walk): %v", n, f.requests())
	}

	// Page 2 changes: the walk re-reads it and re-walks once to confirm.
	f.reset()
	f.bodies["/list?page=2"] = `[3,4]`
	pages, changed, err = c.condGetAll(context.Background(), "/list?page=1", "test-ints/v1", reduce)
	if err != nil || !changed || string(pages[1]) != `[3,4]` {
		t.Fatalf("changed walk = %s changed=%v err=%v", pages, changed, err)
	}
	if got := f.requests(); len(got) != 4 || countPrefix(got, "GET /list?page=1") != 2 {
		t.Fatalf("changed walk requests = %v, want page 1 and 2 twice", got)
	}
}

const restBoardFields = `[{"id":1,"name":"Status"},{"id":2,"name":"Priority"},{"id":3,"name":"Size"},{"id":4,"name":"Parent issue"},{"id":5,"name":"Title"}]`

func restIssueItem(nodeID string, number int, status string, labels []string, blockedOpen, blockedTotal, subs int, parent int) string {
	var ls []string
	for _, l := range labels {
		ls = append(ls, fmt.Sprintf(`{"name":%q}`, l))
	}
	parentJSON := "null"
	if parent > 0 {
		parentJSON = fmt.Sprintf(`{"number":%d,"title":"the epic"}`, parent)
	}
	return fmt.Sprintf(`{"node_id":%q,"content_type":"Issue","content":{"number":%d,"title":"t%d","state":"open",
		"html_url":"https://github.com/acme/web/issues/%d","repository":{"full_name":"acme/web"},
		"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","author_association":"MEMBER",
		"labels":[%s],"sub_issues_summary":{"total":%d,"completed":0},
		"issue_dependencies_summary":{"blocked_by":%d,"total_blocked_by":%d,"blocking":0,"total_blocking":0}},
		"fields":[{"name":"Status","value":{"name":{"raw":%q,"html":%q}}},{"name":"Priority","value":null},{"name":"Parent issue","value":%s}]}`,
		nodeID, number, number, number, strings.Join(ls, ","), subs, blockedOpen, blockedTotal, status, status, parentJSON)
}

// The summary read maps what the tree and the sweep decide from: blocked from
// the OPEN blocker count (blocked_by, not total_blocked_by), epic from any
// sub-issue or the type:epic label, the parent from the Parent issue field.
// Draft issues are dropped; pull requests are kept as PRs.
func TestRESTBoard_SummaryMapsBlockedEpicAndParent(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/orgs/acme/projectsV2/3/fields"] = restBoardFields
	f.bodies["/orgs/acme/projectsV2/3/items"] = "[" + strings.Join([]string{
		restIssueItem("PVTI_1", 1, "Ready", []string{"priority:high"}, 3, 4, 0, 9), // blocked: 3 of 4 blockers open
		restIssueItem("PVTI_2", 2, "Ready", nil, 0, 2, 0, 0),                       // blockers all closed: NOT blocked
		restIssueItem("PVTI_3", 3, "Backlog", nil, 0, 0, 5, 0),                     // epic by sub-issues
		restIssueItem("PVTI_4", 4, "Backlog", []string{"type:epic"}, 0, 0, 0, 0),   // epic by label
		`{"node_id":"PVTI_5","content_type":"DraftIssue","content":{"title":"draft"},"fields":[]}`,
		`{"node_id":"PVTI_6","content_type":"PullRequest","content":{"number":6,"title":"pr","state":"open","html_url":"https://github.com/acme/web/pull/6","labels":[]},"fields":[]}`,
	}, ",") + "]"
	b := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg)

	items, raw, err := b.ListOpenItemsSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if raw != 6 || len(items) != 5 {
		t.Fatalf("raw=%d items=%d, want 6 raw and 5 items (the draft dropped)", raw, len(items))
	}
	by := map[int]int{}
	for i, it := range items {
		by[it.Number] = i
	}
	one := items[by[1]]
	if one.RelationSummary == nil || one.RelationSummary.BlockedByOpen != 3 || one.RelationSummary.BlockedByTotal != 4 {
		t.Errorf("#1 relation summary = %+v, want 3 open of 4", one.RelationSummary)
	}
	if one.Status != "Ready" || one.Priority != "P1" || one.ParentNumber != 9 || one.ParentTitle != "the epic" || one.Repo != "acme/web" || one.State != "OPEN" {
		t.Errorf("#1 = %+v", one)
	}
	if len(one.BlockedBy) != 0 {
		t.Error("the summary read filled a relationship list it never read")
	}
	if s := items[by[2]].RelationSummary; s == nil || s.BlockedByOpen != 0 {
		t.Errorf("#2 has only closed blockers and must not read as blocked: %+v", s)
	}
	if !items[by[3]].IsEpic || !items[by[4]].IsEpic || one.IsEpic {
		t.Errorf("epic flags: #3=%v #4=%v #1=%v", items[by[3]].IsEpic, items[by[4]].IsEpic, one.IsEpic)
	}
	if pr := items[by[6]]; !pr.IsPR || pr.RelationSummary != nil {
		t.Errorf("PR item = %+v", pr)
	}
	if reqs := f.requests(); !strings.Contains(reqs[1], "q=is%3Aopen") {
		t.Errorf("items read did not filter server-side with is:open: %v", reqs)
	}
}

// A status read over REST fills every relationship list the summary says is
// non-empty, from the per-issue list endpoints, and only those.
func TestRESTBoard_StatusReadCompletesRelationships(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/orgs/acme/projectsV2/3/fields"] = restBoardFields
	f.bodies["/orgs/acme/projectsV2/3/items"] = "[" + restIssueItem("PVTI_1", 1, "Ready", nil, 1, 2, 0, 0) + "," +
		restIssueItem("PVTI_2", 2, "Ready", nil, 0, 0, 0, 0) + "]"
	f.bodies["/repos/acme/web/issues/1/dependencies/blocked_by"] = `[
		{"node_id":"I_7","number":7,"title":"open blocker","state":"open","repository":{"full_name":"acme/api"}},
		{"node_id":"I_8","number":8,"title":"closed blocker","state":"closed","repository_url":"https://api.github.com/repos/acme/web"}]`
	b := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg)

	items, err := b.ListItems(context.Background(), "Ready")
	if err != nil {
		t.Fatal(err)
	}
	if len(items[0].BlockedBy) != 2 || items[0].BlockedBy[0].State != "OPEN" || items[0].BlockedBy[0].Repo != "acme/api" ||
		items[0].BlockedBy[1].State != "CLOSED" || items[0].BlockedBy[1].Repo != "acme/web" {
		t.Errorf("#1 blockedBy = %+v", items[0].BlockedBy)
	}
	reqs := f.requests()
	if !strings.Contains(reqs[1], "q=status%3A%22Ready%22+is%3Aopen") {
		t.Errorf("status read query = %s", reqs[1])
	}
	if n := countPrefix(reqs, "GET /repos/"); n != 1 {
		t.Errorf("relationship reads = %d, want 1 (only #1 has a non-empty list): %v", n, reqs)
	}
	if f.gqlN != 0 {
		t.Errorf("status read issued %d GraphQL requests", f.gqlN)
	}
}

const emptyGraphQLBoard = `{"data":{"organization":{"projectV2":{"items":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`

// GitHub Enterprise Server: the REST projects surface is not tried at all.
func TestRESTBoard_GHESUsesGraphQL(t *testing.T) {
	f := newRESTFake(t)
	f.gql = emptyGraphQLBoard
	c := NewClientWithHTTPClient(&http.Client{Transport: f})
	c.graphqlURL = "https://ghes.example.com/api/graphql"
	b := NewBoardService(c, "acme", 3, OwnerTypeOrg)
	if b.SummaryReadAvailable() {
		t.Fatal("SummaryReadAvailable on GHES")
	}
	if _, _, err := b.ListOpenItemsSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reqs := f.requests(); len(reqs) != 1 || reqs[0] != "POST /graphql" {
		t.Fatalf("GHES summary read = %v, want the one GraphQL read", reqs)
	}
}

// A 404 from the REST projects endpoints falls back to GraphQL and is
// remembered: the next read does not ask REST again.
func TestRESTBoard_404FallsBackToGraphQLAndIsRemembered(t *testing.T) {
	f := newRESTFake(t)
	f.gql = emptyGraphQLBoard
	f.status["/orgs/acme/projectsV2/3/fields"] = http.StatusNotFound
	b := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg)
	for i := 0; i < 2; i++ {
		if _, _, err := b.ListOpenItemsSummary(context.Background()); err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
	}
	reqs := f.requests()
	if countPrefix(reqs, "GET ") != 1 || f.gqlN != 2 {
		t.Fatalf("requests = %v, want one REST 404 then GraphQL twice", reqs)
	}
	if b.SummaryReadAvailable() {
		t.Error("SummaryReadAvailable still true after the 404")
	}
}

// Any other REST failure is an error, not a silent fallback.
func TestRESTBoard_OtherFailuresAreErrors(t *testing.T) {
	f := newRESTFake(t)
	f.status["/orgs/acme/projectsV2/3/fields"] = http.StatusInternalServerError
	b := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg)
	_, _, err := b.ListOpenItemsSummary(context.Background())
	if err == nil || errors.Is(err, errRESTBoardUnavailable) || f.gqlN != 0 {
		t.Fatalf("err = %v, GraphQL = %d; want a REST error and no fallback", err, f.gqlN)
	}
}

// The change probe reads the owner's project list: one conditional request
// answers every board of the owner, and an unchanged list is a 304.
func TestProjectUpdatedAt_OneListAnswersEveryBoard(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/orgs/acme/projectsV2"] = `[{"number":3,"updated_at":"2026-09-23T09:07:16Z"},{"number":4,"updated_at":"2026-09-22T10:58:22Z"}]`
	store := NewConditionalStore(t.TempDir())
	c := restClient(f, store, "tok:a")
	for _, n := range []int{3, 4} {
		if _, err := NewBoardService(c, "acme", n, OwnerTypeOrg).ProjectUpdatedAt(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.requests()); n != 1 {
		t.Fatalf("two boards' probes issued %d requests, want 1", n)
	}
	ts, err := NewBoardService(restClient(f, store, "tok:a"), "acme", 3, OwnerTypeOrg).ProjectUpdatedAt(context.Background())
	if err != nil || ts.Format("2006-01-02T15:04:05Z") != "2026-09-23T09:07:16Z" {
		t.Fatalf("restarted probe = %v, %v", ts, err)
	}
	if reqs := f.requests(); !strings.Contains(reqs[1], "inm=W/") {
		t.Fatalf("restarted probe did not revalidate: %v", reqs)
	}
}

// The same underlying issue, read by the GraphQL status read and by the REST
// status read (after relationship completion), is the same BoardItem — the
// only difference being the counts REST also carries.
func TestRESTAndGraphQLStatusReadsAgree(t *testing.T) {
	gql := newRESTFake(t)
	gql.gql = `{"data":{"organization":{"projectV2":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{
		"id":"PVTI_1",
		"content":{"__typename":"Issue","id":"I_1","number":1,"title":"t1","state":"OPEN",
			"url":"https://github.com/acme/web/issues/1","createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-02T00:00:00Z",
			"authorAssociation":"MEMBER","labels":{"totalCount":1,"nodes":[{"name":"priority:high"}]},
			"repository":{"nameWithOwner":"acme/web"},
			"subIssues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},
			"blockedBy":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
				{"id":"I_7","number":7,"title":"open blocker","state":"OPEN","repository":{"nameWithOwner":"acme/api"}},
				{"id":"I_8","number":8,"title":"closed blocker","state":"CLOSED","repository":{"nameWithOwner":"acme/web"}}]},
			"blocking":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},
			"parent":{"number":9,"title":"the epic"}},
		"fieldValues":{"nodes":[{"__typename":"ProjectV2ItemFieldSingleSelectValue","name":"Ready","field":{"name":"Status"}}]}
	}]}}}}}`
	ghes := NewClientWithHTTPClient(&http.Client{Transport: gql})
	ghes.graphqlURL = "https://ghes.example.com/api/graphql"
	fromGraphQL, err := NewBoardService(ghes, "acme", 3, OwnerTypeOrg).ListItems(context.Background(), "Ready")
	if err != nil || len(fromGraphQL) != 1 {
		t.Fatalf("GraphQL read = %v, %v", fromGraphQL, err)
	}

	rest := newRESTFake(t)
	rest.bodies["/orgs/acme/projectsV2/3/fields"] = restBoardFields
	rest.bodies["/orgs/acme/projectsV2/3/items"] = "[" + restIssueItem("PVTI_1", 1, "Ready", []string{"priority:high"}, 1, 2, 0, 9) + "]"
	rest.bodies["/repos/acme/web/issues/1/dependencies/blocked_by"] = `[
		{"node_id":"I_7","number":7,"title":"open blocker","state":"open","repository":{"full_name":"acme/api"}},
		{"node_id":"I_8","number":8,"title":"closed blocker","state":"closed","repository_url":"https://api.github.com/repos/acme/web"}]`
	fromREST, err := NewBoardService(restClient(rest, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg).ListItems(context.Background(), "Ready")
	if err != nil || len(fromREST) != 1 {
		t.Fatalf("REST read = %v, %v", fromREST, err)
	}

	want, got := fromGraphQL[0], fromREST[0]
	if got.RelationSummary == nil || got.RelationSummary.BlockedByOpen != 1 {
		t.Fatalf("REST summary = %+v", got.RelationSummary)
	}
	got.RelationSummary = nil
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("GraphQL and REST disagree:\n graphql %+v\n rest    %+v", want, got)
	}
}

// An issue whose dependency summary is absent is not "zero blockers": its
// lists are read, and its counts derived from them, on the summary read as
// well as the status read.
func TestRESTBoard_MissingSummaryReadsTheLists(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/orgs/acme/projectsV2/3/fields"] = restBoardFields
	f.bodies["/orgs/acme/projectsV2/3/items"] = `[{"node_id":"PVTI_1","content_type":"Issue","content":{"number":1,"title":"t","state":"open",
		"html_url":"https://github.com/acme/web/issues/1","repository":{"full_name":"acme/web"},"labels":[]},
		"fields":[{"name":"Status","value":{"name":{"raw":"Ready"}}}]},
		{"node_id":"PVTI_2","content_type":"Issue","content":null,"fields":[]}]`
	f.bodies["/repos/acme/web/issues/1/dependencies/blocked_by"] = `[{"node_id":"I_7","number":7,"title":"b","state":"open","repository":{"full_name":"acme/web"}}]`
	f.bodies["/repos/acme/web/issues/1/dependencies/blocking"] = `[]`
	f.bodies["/repos/acme/web/issues/1/sub_issues"] = `[]`
	b := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg)

	for name, read := range map[string]func() ([]types.BoardItem, error){
		"summary": func() ([]types.BoardItem, error) {
			items, _, err := b.ListOpenItemsSummary(context.Background())
			return items, err
		},
		"status": func() ([]types.BoardItem, error) { return b.ListItems(context.Background(), "Ready") },
	} {
		items, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(items) != 1 {
			t.Fatalf("%s: %d items, want 1 (the null-content item dropped)", name, len(items))
		}
		it := items[0]
		if len(it.BlockedBy) != 1 || it.RelationSummary == nil || it.RelationSummary.BlockedByOpen != 1 {
			t.Fatalf("%s: blockedBy=%+v summary=%+v, want the open blocker read from the list", name, it.BlockedBy, it.RelationSummary)
		}
	}
}

// Archived items are left out, as the GraphQL items connection leaves them.
func TestRESTBoard_DropsArchivedItems(t *testing.T) {
	f := newRESTFake(t)
	f.bodies["/orgs/acme/projectsV2/3/fields"] = restBoardFields
	archived := strings.Replace(restIssueItem("PVTI_2", 2, "Ready", nil, 0, 0, 0, 0), `"node_id":"PVTI_2",`, `"node_id":"PVTI_2","archived_at":"2026-09-01T00:00:00Z",`, 1)
	f.bodies["/orgs/acme/projectsV2/3/items"] = "[" + restIssueItem("PVTI_1", 1, "Ready", nil, 0, 0, 0, 0) + "," + archived + "]"
	items, _, err := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg).ListOpenItemsSummary(context.Background())
	if err != nil || len(items) != 1 || items[0].Number != 1 {
		t.Fatalf("items = %+v, %v; want only #1", items, err)
	}
}

// A 403 that is not a rate limit is a refusal of the REST surface, not of the
// board: this read falls back to GraphQL, and the next one tries REST again.
func TestRESTBoard_Non403RateLimitFallsBackForTheCallOnly(t *testing.T) {
	f := newRESTFake(t)
	f.gql = emptyGraphQLBoard
	f.status["/orgs/acme/projectsV2/3/fields"] = http.StatusForbidden
	b := NewBoardService(restClient(f, NewConditionalStore(""), "tok:a"), "acme", 3, OwnerTypeOrg)
	for i := 0; i < 2; i++ {
		if _, _, err := b.ListOpenItemsSummary(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := countPrefix(f.requests(), "GET "); n != 2 || f.gqlN != 2 {
		t.Fatalf("REST tries = %d, GraphQL = %d; want REST tried each time, GraphQL answering each", n, f.gqlN)
	}
}

// A 404 is remembered for ONE board, and only for a while.
func TestRESTBoard_404IsRememberedPerBoardWithATTL(t *testing.T) {
	c := NewClientWithHTTPClient(&http.Client{Transport: newRESTFake(t)})
	c.markRESTProjectsUnavailable("acme", 3, errors.New("404"))
	if c.restProjectsUsable("acme", 3) {
		t.Fatal("board 3 still usable right after its 404")
	}
	if !c.restProjectsUsable("acme", 4) {
		t.Fatal("board 4 was switched off by board 3's 404")
	}
	c.mu.Lock()
	c.restProjects404[restBoardKey("acme", 3)] = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if !c.restProjectsUsable("acme", 3) {
		t.Fatal("board 3's 404 outlived its TTL")
	}
}
