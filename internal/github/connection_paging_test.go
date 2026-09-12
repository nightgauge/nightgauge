package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/pkg/types"
	"golang.org/x/time/rate"
)

// relationForge is a fake GitHub GraphQL endpoint that serves issue
// relationship connections the way GitHub does: a page holds at most the
// `first:` the query asked for, and pageInfo reports what is left.
//
// It answers only what the query selects. The client's decoder rejects a
// response field the query did not ask for, so a connection gets pageInfo only
// when the query selects it and a board item gets its node id only when the
// query selects that. A reader that asks for one fixed page, as the readers
// did before connection paging, therefore gets exactly that page back, which
// is what makes these tests fail on the old code for the right reason.
type relationForge struct {
	t    *testing.T
	repo string

	mu       sync.Mutex
	byID     map[string]*fakeIssue
	byNumber map[int]*fakeIssue
	board    []int // issue numbers on the project board

	// endless names a connection of issue endlessOn that reports
	// hasNextPage on every page, the way a runaway connection or a
	// misbehaving server would.
	endless   string
	endlessOn int
	// noCursor makes the endless connection omit its endCursor.
	noCursor bool
	// failFollowUps makes every follow-up request fail the given way; see
	// the followUpFailure constants.
	failFollowUps followUpFailure
	// followUps counts the follow-up page reads per connection name, and
	// followUpRequests the requests that carried them.
	followUps        map[string]int
	followUpRequests int
	// rateLimitedFollowUps is how many follow-up requests, from the first,
	// are answered with GitHub's rate-limit errors body instead of pages.
	rateLimitedFollowUps int
	// remaining, when set, is the X-RateLimit-Remaining every response
	// reports, with the reset twenty minutes out.
	remaining string
	// onFollowUp, when set, runs before a follow-up request is answered.
	onFollowUp func(*http.Request)
	// queries records every query the fake answered, in order.
	queries []string
}

// followUpFailure is a way a follow-up page read can fail.
type followUpFailure string

const (
	followUpsServed    followUpFailure = ""
	followUpHTTP502    followUpFailure = "HTTP 502"
	followUpGraphQLErr followUpFailure = "GraphQL errors body"
	followUpNullNode   followUpFailure = "null node"
	followUpNotAnIssue followUpFailure = "node is not an Issue"
	followUpNoPage     followUpFailure = "Issue without the connection"
)

type fakeIssue struct {
	id                           string
	number                       int
	state                        string
	subIssues, blockedBy, blocks []int
}

func newRelationForge(t *testing.T) *relationForge {
	t.Helper()
	return &relationForge{
		t:         t,
		repo:      "acme/widgets",
		byID:      map[string]*fakeIssue{},
		byNumber:  map[int]*fakeIssue{},
		followUps: map[string]int{},
	}
}

// issue adds an issue to the fake and returns it for relationship wiring.
func (f *relationForge) issue(number int, state string) *fakeIssue {
	iss := &fakeIssue{id: fmt.Sprintf("I_%d", number), number: number, state: state}
	f.byID[iss.id] = iss
	f.byNumber[number] = iss
	return iss
}

// numbers returns n consecutive issue numbers starting at from, each added to
// the fake with the given state.
func (f *relationForge) numbers(from, n int, state string) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		f.issue(from+i, state)
		out = append(out, from+i)
	}
	return out
}

// client starts the fake and returns a client aimed at it, with the rate
// limiter lifted so a page-cap walk costs no wall-clock time. All fixture
// setup happens before this call; the handler only reads it.
func (f *relationForge) client() *Client {
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.t.Cleanup(srv.Close)
	c := NewClientWithURL("test-token", srv.URL)
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

var (
	// reFollowUp matches one aliased connection read of a follow-up request:
	// alias index, connection, page size, and the cursor variable's index.
	reFollowUp = regexp.MustCompile(
		`r(\d+): node\(id: \$id(\d+)\) \{ __typename \.\.\. on Issue \{ (subIssues|blockedBy|blocking)\(first: (\d+), after: \$after(\d+)\)`)
	reBatchItem = regexp.MustCompile(`i(\d+): issue\(number: (\d+)\)`)
	reIssueID   = regexp.MustCompile(`\.\.\.\s*on Issue\s*\{\s*id\b`)
	reRelation  = regexp.MustCompile(`\b(subIssues|blockedBy|blocking)\(`)
)

// followUpCount is the number of follow-up pages read for conn so far.
func (f *relationForge) followUpCount(conn string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.followUps[conn]
}

// followUpRequestCount is the number of follow-up requests served so far.
func (f *relationForge) followUpRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.followUpRequests
}

// answered returns the queries answered so far.
func (f *relationForge) answered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func (f *relationForge) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	q, vars := req.Query, req.Variables
	f.queries = append(f.queries, q)
	if f.remaining != "" {
		w.Header().Set("X-RateLimit-Remaining", f.remaining)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Unix()+1200, 10))
	}
	var data map[string]interface{}
	switch {
	case reFollowUp.MatchString(q):
		f.followUpRequests++
		if f.onFollowUp != nil {
			f.onFollowUp(r)
		}
		if f.rateLimitedFollowUps > 0 {
			f.rateLimitedFollowUps--
			// GitHub reports an exhausted GraphQL budget as HTTP 200 with
			// a RATE_LIMITED error and no data.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": nil,
				"errors": []interface{}{map[string]interface{}{
					"type":    "RATE_LIMITED",
					"message": "API rate limit already exceeded for user ID 1.",
				}},
			})
			return
		}
		data = map[string]interface{}{}
		for _, m := range reFollowUp.FindAllStringSubmatch(q, -1) {
			alias, idVar, conn, afterVar := "r"+m[1], "id"+m[2], m[3], "after"+m[5]
			first, _ := strconv.Atoi(m[4])
			f.followUps[conn]++
			iss := f.byID[fmt.Sprint(vars[idVar])]
			switch {
			case iss == nil || f.failFollowUps == followUpNullNode:
				data[alias] = nil
			case f.failFollowUps == followUpNotAnIssue:
				data[alias] = map[string]interface{}{"__typename": "PullRequest"}
			case f.failFollowUps == followUpNoPage:
				data[alias] = map[string]interface{}{"__typename": "Issue"}
			default:
				start, _ := strconv.Atoi(strings.TrimPrefix(fmt.Sprint(vars[afterVar]), "after-"))
				data[alias] = map[string]interface{}{
					"__typename": "Issue",
					conn:         f.page(conn, iss, start, first, true),
				}
			}
		}
		switch f.failFollowUps {
		case followUpHTTP502:
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		case followUpGraphQLErr:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data":   nil,
				"errors": []interface{}{map[string]interface{}{"message": "Something went wrong while executing your query."}},
			})
			return
		}
	case strings.Contains(q, "projectV2("):
		nodes := make([]interface{}, 0, len(f.board))
		for _, n := range f.board {
			content := f.issueFields(q, f.byNumber[n])
			content["__typename"] = "Issue"
			content["repository"] = map[string]interface{}{"nameWithOwner": f.repo}
			if !reIssueID.MatchString(q) {
				delete(content, "id")
			}
			nodes = append(nodes, map[string]interface{}{"id": fmt.Sprintf("PVTI_%d", n), "content": content})
		}
		data = map[string]interface{}{"organization": map[string]interface{}{
			"projectV2": map[string]interface{}{
				"id":    "PVT_1",
				"title": "Board",
				"items": map[string]interface{}{
					"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					"nodes":    nodes,
				},
			},
		}}
	case strings.Contains(q, "fragment IssueFields"):
		repo := map[string]interface{}{}
		for _, m := range reBatchItem.FindAllStringSubmatch(q, -1) {
			n, _ := strconv.Atoi(m[2])
			if iss := f.byNumber[n]; iss != nil {
				repo["i"+m[1]] = f.issueFields(q, iss)
			} else {
				repo["i"+m[1]] = nil
			}
		}
		data = map[string]interface{}{"repository": repo}
	case strings.Contains(q, "node(id: $id)"):
		iss := f.byID[fmt.Sprint(vars["id"])]
		fields := f.issueFields(q, iss)
		fields["__typename"] = "Issue"
		fields["repository"] = map[string]interface{}{"nameWithOwner": f.repo}
		data = map[string]interface{}{"node": fields}
	case strings.Contains(q, "issue(number: $number)"):
		iss := f.byNumber[int(vars["number"].(float64))]
		if iss == nil {
			data = map[string]interface{}{"repository": map[string]interface{}{"issue": nil}}
			break
		}
		data = map[string]interface{}{"repository": map[string]interface{}{"issue": f.issueFields(q, iss)}}
	case strings.Contains(q, "rateLimit{"):
		// The client's backoff probe: budget left, so it pauses briefly.
		data = map[string]interface{}{"rateLimit": map[string]interface{}{
			"remaining": 4999,
			"resetAt":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}}
	default:
		f.t.Errorf("relationForge: unrecognised query %q", q)
		http.Error(w, "unrecognised query", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

// issueFields renders an issue with the first page of every relationship
// connection the query selects, at the size the query asked for.
func (f *relationForge) issueFields(q string, iss *fakeIssue) map[string]interface{} {
	out := map[string]interface{}{
		"id":     iss.id,
		"number": iss.number,
		"title":  fmt.Sprintf("Issue %d", iss.number),
		"state":  iss.state,
	}
	for _, conn := range []string{"subIssues", "blockedBy", "blocking"} {
		m := regexp.MustCompile(conn + `\(first: (\d+)\)`).FindStringSubmatch(q)
		if m == nil {
			continue
		}
		first, _ := strconv.Atoi(m[1])
		withPageInfo := regexp.MustCompile(conn + `\([^)]*\)\s*\{\s*pageInfo`).MatchString(q)
		out[conn] = f.page(conn, iss, 0, first, withPageInfo)
	}
	return out
}

// page renders nodes [start, start+first) of one connection.
func (f *relationForge) page(conn string, iss *fakeIssue, start, first int, withPageInfo bool) map[string]interface{} {
	if conn == f.endless && iss.number == f.endlessOn {
		// One fresh node per page, and always more to come.
		n := 90000 + start
		cursor := fmt.Sprintf("after-%d", start+1)
		if f.noCursor {
			cursor = ""
		}
		out := map[string]interface{}{"nodes": []interface{}{f.node(n, "OPEN")}}
		if withPageInfo {
			out["pageInfo"] = map[string]interface{}{"hasNextPage": true, "endCursor": cursor}
		}
		return out
	}
	var all []int
	switch conn {
	case "subIssues":
		all = iss.subIssues
	case "blockedBy":
		all = iss.blockedBy
	case "blocking":
		all = iss.blocks
	}
	end := start + first
	if end > len(all) {
		end = len(all)
	}
	nodes := make([]interface{}, 0, end-start)
	for _, n := range all[start:end] {
		state := "OPEN"
		if rel := f.byNumber[n]; rel != nil {
			state = rel.state
		}
		nodes = append(nodes, f.node(n, state))
	}
	out := map[string]interface{}{"nodes": nodes}
	if withPageInfo {
		out["pageInfo"] = map[string]interface{}{
			"hasNextPage": end < len(all),
			"endCursor":   fmt.Sprintf("after-%d", end),
		}
	}
	return out
}

func (f *relationForge) node(number int, state string) map[string]interface{} {
	return map[string]interface{}{
		"id":         fmt.Sprintf("I_%d", number),
		"number":     number,
		"title":      fmt.Sprintf("Issue %d", number),
		"state":      state,
		"repository": map[string]interface{}{"nameWithOwner": f.repo},
	}
}

func subIssueNumbers(refs []types.SubIssueRef) []int {
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Number)
	}
	return out
}

func blockingNumbers(refs []types.BlockingRef) []int {
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Number)
	}
	return out
}

// blockedByOpen is the scheduler's blocker rule (orchestrator isBlocked): an
// issue is blocked while any issue in its blockedBy list is OPEN. The rule is
// only as good as the list it is given, which is what these tests pin.
func blockedByOpen(refs []types.BlockingRef) bool {
	for _, r := range refs {
		if strings.EqualFold(r.State, "OPEN") {
			return true
		}
	}
	return false
}

// boardReader is one BoardService read that returns board items. Each
// completes relationships itself, so each is exercised: the scheduler picks
// from ListItems("Ready"), autonomous mode, the dependency graph and the board
// cache read ListOpenItems, `board list` reads ListItems(""), and RunQueue
// and `project field-get` read GetItem.
type boardReader struct {
	name string
	read func(ctx context.Context, b *BoardService, number int) ([]types.BoardItem, error)
}

var boardReaders = []boardReader{
	{"ListItems all", func(ctx context.Context, b *BoardService, _ int) ([]types.BoardItem, error) {
		return b.ListItems(ctx, "")
	}},
	{"ListItems Ready", func(ctx context.Context, b *BoardService, _ int) ([]types.BoardItem, error) {
		return b.ListItems(ctx, "Ready")
	}},
	{"ListOpenItems", func(ctx context.Context, b *BoardService, _ int) ([]types.BoardItem, error) {
		items, _, err := b.ListOpenItems(ctx)
		return items, err
	}},
	{"GetItem", func(ctx context.Context, b *BoardService, number int) ([]types.BoardItem, error) {
		item, err := b.GetItem(ctx, "acme", "widgets", number)
		if err != nil {
			return nil, err
		}
		return []types.BoardItem{*item}, nil
	}},
}

func assertNumbers(t *testing.T, what string, got, want []int) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: got %d %v, want %d %v", what, len(got), got, len(want), want)
	}
}

// TestSubIssuesPaginatesPast25 serves an epic with 42 children, more than the
// first page of every reader, and requires each reader to return all 42. The
// only open child is the last one, on the second page: epic rollup must see
// it and refuse to call the epic complete.
func TestSubIssuesPaginatesPast25(t *testing.T) {
	ctx := context.Background()
	f := newRelationForge(t)
	epic := f.issue(100, "OPEN")
	epic.subIssues = f.numbers(1001, 42, "CLOSED")
	f.byNumber[1042].state = "OPEN"
	f.board = []int{100}
	big := f.issue(200, "OPEN")
	big.subIssues = f.numbers(2001, 120, "CLOSED")
	f.byNumber[2120].state = "OPEN"
	c := f.client()

	t.Run("GetIssue", func(t *testing.T) {
		got, err := NewIssueService(c).GetIssue(ctx, "acme", "widgets", 100)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		assertNumbers(t, "GetIssue sub-issues", subIssueNumbers(got.SubIssues), epic.subIssues)
	})

	t.Run("GetIssuesByNumbers", func(t *testing.T) {
		got, err := NewIssueService(c).GetIssuesByNumbers(ctx, "acme", "widgets", []int{100})
		if err != nil {
			t.Fatalf("GetIssuesByNumbers: %v", err)
		}
		assertNumbers(t, "batch sub-issues", subIssueNumbers(got[100].SubIssues), epic.subIssues)
	})

	for _, br := range boardReaders {
		t.Run("board scan/"+br.name, func(t *testing.T) {
			items, err := br.read(ctx, NewBoardService(c, "acme", 1), 100)
			if err != nil {
				t.Fatalf("%s: %v", br.name, err)
			}
			if len(items) != 1 {
				t.Fatalf("board items = %d, want 1", len(items))
			}
			assertNumbers(t, "board sub-issues", subIssueNumbers(items[0].SubIssues), epic.subIssues)
		})
	}

	t.Run("epic validate", func(t *testing.T) {
		res, err := NewEpicService(c).Validate(ctx, "acme", "widgets", 100)
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if res.TotalSubIssues != 42 {
			t.Fatalf("epic validate checked %d sub-issues, want 42", res.TotalSubIssues)
		}
	})

	t.Run("epic rollup sees the open child on page two", func(t *testing.T) {
		res, err := NewEpicService(c).CheckCompletion(ctx, "acme", "widgets", 100)
		if err != nil {
			t.Fatalf("CheckCompletion: %v", err)
		}
		if res.Total != 42 || res.Open != 1 || res.Complete {
			t.Fatalf("rollup = total %d, open %d, complete %v; want 42, 1, false", res.Total, res.Open, res.Complete)
		}
		if !res.HasSubIssue("acme/widgets", 1042) {
			t.Fatal("rollup does not list #1042 among the epic's sub-issues")
		}
	})

	t.Run("GetEpicProgress past its first page of 50", func(t *testing.T) {
		got, err := NewIssueService(c).GetEpicProgress(ctx, big.id)
		if err != nil {
			t.Fatalf("GetEpicProgress: %v", err)
		}
		if got.Total != 120 || got.Open != 1 {
			t.Fatalf("GetEpicProgress = total %d, open %d; want 120, 1", got.Total, got.Open)
		}
		assertNumbers(t, "epic progress sub-issues", subIssueNumbers(got.SubIssues), big.subIssues)
	})
}

// TestBlockedByPaginatesPast5 serves an issue with 11 blockers, more than the
// first page of 5, and requires every reader to return all 11. Only the last
// blocker is open, so the issue is blocked only if the reader saw page two.
func TestBlockedByPaginatesPast5(t *testing.T) {
	ctx := context.Background()
	f := newRelationForge(t)
	blocked := f.issue(300, "OPEN")
	blocked.blockedBy = f.numbers(301, 11, "CLOSED")
	f.byNumber[311].state = "OPEN"
	blocked.blocks = f.numbers(401, 7, "OPEN")
	f.board = []int{300}
	epic := f.issue(500, "OPEN")
	epic.subIssues = []int{300}
	c := f.client()

	check := func(t *testing.T, reader string, blockedBy, blocking []types.BlockingRef) {
		t.Helper()
		assertNumbers(t, reader+" blockedBy", blockingNumbers(blockedBy), blocked.blockedBy)
		assertNumbers(t, reader+" blocking", blockingNumbers(blocking), blocked.blocks)
		if !blockedByOpen(blockedBy) {
			t.Fatalf("%s: #300 reads as unblocked while its blocker #311 is open", reader)
		}
	}

	t.Run("GetIssue", func(t *testing.T) {
		got, err := NewIssueService(c).GetIssue(ctx, "acme", "widgets", 300)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		check(t, "GetIssue", got.BlockedBy, got.Blocking)
	})

	t.Run("GetIssuesByNumbers", func(t *testing.T) {
		got, err := NewIssueService(c).GetIssuesByNumbers(ctx, "acme", "widgets", []int{300})
		if err != nil {
			t.Fatalf("GetIssuesByNumbers: %v", err)
		}
		check(t, "GetIssuesByNumbers", got[300].BlockedBy, got[300].Blocking)
	})

	for _, br := range boardReaders {
		t.Run("board scan feeds the scheduler blocker check/"+br.name, func(t *testing.T) {
			items, err := br.read(ctx, NewBoardService(c, "acme", 1), 300)
			if err != nil {
				t.Fatalf("%s: %v", br.name, err)
			}
			if len(items) != 1 {
				t.Fatalf("board items = %d, want 1", len(items))
			}
			check(t, br.name, items[0].BlockedBy, items[0].Blocking)
		})
	}

	t.Run("wave planning orders the issue after its page-two blocker", func(t *testing.T) {
		res, err := NewEpicService(c).PlanWaves(ctx, "acme", "widgets", []int{300, 311})
		if err != nil {
			t.Fatalf("PlanWaves: %v", err)
		}
		wave := map[int]int{}
		for _, w := range res.Waves {
			for _, iss := range w.Issues {
				wave[iss.Number] = w.WaveIndex
			}
		}
		if wave[300] <= wave[311] {
			t.Fatalf("#300 planned in wave %d, not after its blocker #311 in wave %d", wave[300], wave[311])
		}
	})

	t.Run("epic validate sees a closed blocker on page two", func(t *testing.T) {
		res, err := NewEpicService(c).Validate(ctx, "acme", "widgets", 500)
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		stale := map[int]bool{}
		for _, g := range res.Gaps {
			if g.GapType == "stale_blocker" {
				stale[g.BlockerNumber] = true
			}
		}
		for _, n := range []int{306, 307, 308, 309, 310} {
			if !stale[n] {
				t.Fatalf("validate missed stale blocker #%d past the first page; stale = %v", n, stale)
			}
		}
	})
}

// TestConnectionPageCapIsError serves connections that report hasNextPage on
// every page. Each reader must stop at maxRelationPages and fail with
// ErrConnectionTruncated, never loop and never return the pages it read as
// the whole connection; rollup, wave planning and validate must fail too.
func TestConnectionPageCapIsError(t *testing.T) {
	ctx := context.Background()

	// Issue #700 carries the endless connection; #800 is its epic.
	setup := func(t *testing.T, endless string, noCursor bool) (*relationForge, *Client) {
		f := newRelationForge(t)
		f.endless, f.endlessOn, f.noCursor = endless, 700, noCursor
		iss := f.issue(700, "OPEN")
		iss.subIssues = []int{701}
		f.issue(701, "OPEN")
		epic := f.issue(800, "OPEN")
		epic.subIssues = []int{700}
		f.board = []int{700}
		return f, f.client()
	}

	requireCapped := func(t *testing.T, f *relationForge, conn string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("read of an endless %s connection returned no error", conn)
		}
		if !errors.Is(err, ErrConnectionTruncated) {
			t.Fatalf("err = %v, want ErrConnectionTruncated", err)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%d-page cap", maxRelationPages)) {
			t.Fatalf("err = %v, want it to name the %d-page cap", err, maxRelationPages)
		}
		if got := f.followUpCount(conn); got != maxRelationPages-1 {
			t.Fatalf("%s follow-up pages read = %d, want %d (the cap, first page included)", conn, got, maxRelationPages-1)
		}
	}

	for _, conn := range []string{"subIssues", "blockedBy", "blocking"} {
		t.Run("GetIssue/"+conn, func(t *testing.T) {
			f, c := setup(t, conn, false)
			_, err := NewIssueService(c).GetIssue(ctx, "acme", "widgets", 700)
			requireCapped(t, f, conn, err)
		})
	}

	t.Run("GetIssuesByNumbers", func(t *testing.T) {
		f, c := setup(t, "blockedBy", false)
		_, err := NewIssueService(c).GetIssuesByNumbers(ctx, "acme", "widgets", []int{700})
		requireCapped(t, f, "blockedBy", err)
	})

	for _, br := range boardReaders {
		t.Run("board scan/"+br.name, func(t *testing.T) {
			f, c := setup(t, "blockedBy", false)
			items, err := br.read(ctx, NewBoardService(c, "acme", 1), 700)
			requireCapped(t, f, "blockedBy", err)
			if items != nil {
				t.Fatalf("%s returned %d items alongside a truncated read", br.name, len(items))
			}
		})
	}

	t.Run("GetEpicProgress", func(t *testing.T) {
		f, c := setup(t, "subIssues", false)
		_, err := NewIssueService(c).GetEpicProgress(ctx, "I_700")
		requireCapped(t, f, "subIssues", err)
	})

	t.Run("epic rollup", func(t *testing.T) {
		f, c := setup(t, "subIssues", false)
		_, err := NewEpicService(c).CheckCompletion(ctx, "acme", "widgets", 700)
		requireCapped(t, f, "subIssues", err)
	})

	t.Run("wave planning", func(t *testing.T) {
		f, c := setup(t, "blockedBy", false)
		res, err := NewEpicService(c).PlanWaves(ctx, "acme", "widgets", []int{700, 701})
		requireCapped(t, f, "blockedBy", err)
		if res != nil {
			t.Fatalf("PlanWaves returned a plan alongside a truncated read: %+v", res)
		}
	})

	t.Run("epic validate", func(t *testing.T) {
		f, c := setup(t, "blockedBy", false)
		_, err := NewEpicService(c).Validate(ctx, "acme", "widgets", 800)
		requireCapped(t, f, "blockedBy", err)
	})

	t.Run("next page without a cursor", func(t *testing.T) {
		f, c := setup(t, "blockedBy", true)
		_, err := NewIssueService(c).GetIssue(ctx, "acme", "widgets", 700)
		if !errors.Is(err, ErrConnectionTruncated) {
			t.Fatalf("err = %v, want ErrConnectionTruncated", err)
		}
		if got := f.followUpCount("blockedBy"); got != 0 {
			t.Fatalf("follow-up pages read = %d, want 0 with no cursor to continue from", got)
		}
	})
}

// TestRelationFollowUpFailureIsError fails every follow-up page read, in each
// way a real one fails: a 5xx, a GraphQL errors body, a null node, a node that
// is not an issue, and an issue without the connection. Page two failing is
// how a connection really gets cut short, so every reader and every decision
// built on one must return ErrConnectionTruncated and nothing else: not the
// first page as if it were the list, and not a plan, rollup or validation
// built from it.
func TestRelationFollowUpFailureIsError(t *testing.T) {
	ctx := context.Background()

	// #700 has more sub-issues than any reader's first page (60 > 50) and
	// more blockers than one page (11 > 5); only its last blocker is open.
	// #800 is the epic #700 belongs to.
	setup := func(t *testing.T, mode followUpFailure) *Client {
		f := newRelationForge(t)
		f.failFollowUps = mode
		iss := f.issue(700, "OPEN")
		iss.subIssues = f.numbers(1001, 60, "CLOSED")
		iss.blockedBy = f.numbers(301, 11, "CLOSED")
		f.byNumber[311].state = "OPEN"
		epic := f.issue(800, "OPEN")
		epic.subIssues = []int{700}
		f.board = []int{700}
		return f.client()
	}

	type read struct {
		name string
		run  func(ctx context.Context, c *Client) (result any, err error)
	}
	reads := []read{
		{"GetIssue", func(ctx context.Context, c *Client) (any, error) {
			return NewIssueService(c).GetIssue(ctx, "acme", "widgets", 700)
		}},
		{"GetIssuesByNumbers", func(ctx context.Context, c *Client) (any, error) {
			return NewIssueService(c).GetIssuesByNumbers(ctx, "acme", "widgets", []int{700})
		}},
		{"GetEpicProgress", func(ctx context.Context, c *Client) (any, error) {
			return NewIssueService(c).GetEpicProgress(ctx, "I_700")
		}},
		{"GetEpicProgressByNumber", func(ctx context.Context, c *Client) (any, error) {
			return NewIssueService(c).GetEpicProgressByNumber(ctx, "acme", "widgets", 700)
		}},
		{"epic rollup", func(ctx context.Context, c *Client) (any, error) {
			return NewEpicService(c).CheckCompletion(ctx, "acme", "widgets", 700)
		}},
		{"wave planning", func(ctx context.Context, c *Client) (any, error) {
			return NewEpicService(c).PlanWaves(ctx, "acme", "widgets", []int{700})
		}},
		{"epic validate", func(ctx context.Context, c *Client) (any, error) {
			return NewEpicService(c).Validate(ctx, "acme", "widgets", 800)
		}},
	}
	for _, br := range boardReaders {
		reads = append(reads, read{"board scan/" + br.name, func(ctx context.Context, c *Client) (any, error) {
			return br.read(ctx, NewBoardService(c, "acme", 1), 700)
		}})
	}

	modes := []followUpFailure{followUpHTTP502, followUpGraphQLErr, followUpNullNode, followUpNotAnIssue, followUpNoPage}
	for _, mode := range modes {
		for _, r := range reads {
			t.Run(string(mode)+"/"+r.name, func(t *testing.T) {
				res, err := r.run(ctx, setup(t, mode))
				if !errors.Is(err, ErrConnectionTruncated) {
					t.Fatalf("err = %v, want ErrConnectionTruncated", err)
				}
				if v := reflect.ValueOf(res); v.IsValid() && !v.IsNil() {
					t.Fatalf("returned %+v alongside a truncated read", res)
				}
			})
		}
	}
}

// TestRelationFollowUpsArePacedLikeFirstPages holds a follow-up page read to
// the rate-limit handling of the first page it extends (Client.query): it
// waits on the rate-limit floor gate, it retries a rate-limit error in the
// response body, and the error it returns keeps its cause, so a caller can
// still tell a gated or cancelled read from a broken connection.
func TestRelationFollowUpsArePacedLikeFirstPages(t *testing.T) {
	// #700 has more sub-issues than any reader's first page (60 > 50) and
	// more blockers than one page (11 > 5); only its last blocker is open.
	setup := func(t *testing.T) *relationForge {
		f := newRelationForge(t)
		iss := f.issue(700, "OPEN")
		iss.subIssues = f.numbers(1001, 60, "CLOSED")
		iss.blockedBy = f.numbers(301, 11, "CLOSED")
		f.byNumber[311].state = "OPEN"
		f.board = []int{700}
		return f
	}

	// Each of these reads is one first-page query and then its follow-ups,
	// so the follow-up is the first request made after the first page.
	type read struct {
		name string
		run  func(ctx context.Context, c *Client) (result any, err error)
	}
	reads := []read{
		{"GetIssue", func(ctx context.Context, c *Client) (any, error) {
			return NewIssueService(c).GetIssue(ctx, "acme", "widgets", 700)
		}},
		{"GetIssuesByNumbers", func(ctx context.Context, c *Client) (any, error) {
			return NewIssueService(c).GetIssuesByNumbers(ctx, "acme", "widgets", []int{700})
		}},
		{"GetEpicProgress", func(ctx context.Context, c *Client) (any, error) {
			return NewIssueService(c).GetEpicProgress(ctx, "I_700")
		}},
	}
	for _, br := range boardReaders {
		reads = append(reads, read{"board scan/" + br.name, func(ctx context.Context, c *Client) (any, error) {
			return br.read(ctx, NewBoardService(c, "acme", 1), 700)
		}})
	}

	for _, r := range reads {
		t.Run("below the rate-limit floor/"+r.name, func(t *testing.T) {
			// The first page reports 5 points left, under the floor of 100.
			// A fail-fast client must not send the follow-up.
			t.Setenv(rateLimitFloorEnv, "100")
			f := setup(t)
			f.remaining = "5"
			tr := NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
			c := f.client().WithRateLimitTracker(tr, "alice")
			res, err := r.run(context.Background(), c)
			if !errors.Is(err, ErrRateLimitGated) || !errors.Is(err, ErrConnectionTruncated) {
				t.Fatalf("err = %v, want ErrRateLimitGated and ErrConnectionTruncated", err)
			}
			if v := reflect.ValueOf(res); v.IsValid() && !v.IsNil() {
				t.Fatalf("returned %+v alongside a truncated read", res)
			}
			if got := f.followUpRequestCount(); got != 0 {
				t.Fatalf("follow-up requests = %d, want 0 below the floor", got)
			}
		})
	}

	t.Run("rate-limit error body is retried", func(t *testing.T) {
		f := setup(t)
		f.rateLimitedFollowUps = 1
		got, err := NewIssueService(f.client()).GetIssue(context.Background(), "acme", "widgets", 700)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if len(got.SubIssues) != 60 || len(got.BlockedBy) != 11 {
			t.Fatalf("got %d sub-issues and %d blockers, want 60 and 11", len(got.SubIssues), len(got.BlockedBy))
		}
		if !blockedByOpen(got.BlockedBy) {
			t.Fatal("issue reads unblocked although its 11th blocker is open")
		}
		if got := f.followUpRequestCount(); got != 2 {
			t.Fatalf("follow-up requests = %d, want 2 (one rate-limited, one retry)", got)
		}
	})

	t.Run("rate-limit error body past the retries is truncation", func(t *testing.T) {
		f := setup(t)
		f.rateLimitedFollowUps = maxRetries + 1
		got, err := NewIssueService(f.client()).GetIssue(context.Background(), "acme", "widgets", 700)
		if !errors.Is(err, ErrConnectionTruncated) || !strings.Contains(fmt.Sprint(err), "rate limit") {
			t.Fatalf("err = %v, want ErrConnectionTruncated naming the rate limit", err)
		}
		if got != nil {
			t.Fatalf("returned %+v alongside a truncated read", got)
		}
		if got := f.followUpRequestCount(); got != maxRetries+1 {
			t.Fatalf("follow-up requests = %d, want %d", got, maxRetries+1)
		}
	})

	t.Run("cancellation keeps its cause", func(t *testing.T) {
		f := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.onFollowUp = func(r *http.Request) {
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
		_, err := NewIssueService(f.client()).GetIssue(ctx, "acme", "widgets", 700)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrConnectionTruncated) {
			t.Fatalf("err = %v, want context.Canceled and ErrConnectionTruncated", err)
		}
	})
}

// TestRelationFollowUpsShareRequests pins the cost of completing connections.
// The client allows about one request a second, so the follow-up pages of
// every oversized connection in one read go out together: one aliased request
// per round, split only past relationFollowUpsPerRequest connections.
func TestRelationFollowUpsShareRequests(t *testing.T) {
	ctx := context.Background()

	// oversized adds n issues from first, each with every connection larger
	// than any first page, and returns their numbers.
	oversized := func(f *relationForge, first, n int) []int {
		children := f.numbers(5001, 42, "CLOSED")
		blockers := f.numbers(6001, 11, "CLOSED")
		blocks := f.numbers(7001, 7, "OPEN")
		var out []int
		for i := 0; i < n; i++ {
			iss := f.issue(first+i, "OPEN")
			iss.subIssues, iss.blockedBy, iss.blocks = children, blockers, blocks
			out = append(out, first+i)
		}
		return out
	}
	whole := func(t *testing.T, what string, subs []types.SubIssueRef, by, blocking []types.BlockingRef) {
		t.Helper()
		if len(subs) != 42 || len(by) != 11 || len(blocking) != 7 {
			t.Fatalf("%s: got %d sub-issues, %d blockers, %d blocking; want 42, 11, 7",
				what, len(subs), len(by), len(blocking))
		}
	}

	t.Run("board scan of three oversized items", func(t *testing.T) {
		f := newRelationForge(t)
		f.board = oversized(f, 100, 3)
		items, _, err := NewBoardService(f.client(), "acme", 1).ListOpenItems(ctx)
		if err != nil {
			t.Fatalf("ListOpenItems: %v", err)
		}
		for _, it := range items {
			whole(t, fmt.Sprintf("board item #%d", it.Number), it.SubIssues, it.BlockedBy, it.Blocking)
		}
		if got := f.followUpRequestCount(); got != 1 {
			t.Fatalf("follow-up requests = %d, want 1 for 9 connections", got)
		}
	})

	t.Run("GetIssuesByNumbers of three oversized issues", func(t *testing.T) {
		f := newRelationForge(t)
		nums := oversized(f, 100, 3)
		got, err := NewIssueService(f.client()).GetIssuesByNumbers(ctx, "acme", "widgets", nums)
		if err != nil {
			t.Fatalf("GetIssuesByNumbers: %v", err)
		}
		for _, n := range nums {
			whole(t, fmt.Sprintf("issue #%d", n), got[n].SubIssues, got[n].BlockedBy, got[n].Blocking)
		}
		if got := f.followUpRequestCount(); got != 1 {
			t.Fatalf("follow-up requests = %d, want 1 for 9 connections", got)
		}
	})

	t.Run("GetIssue with every connection oversized", func(t *testing.T) {
		f := newRelationForge(t)
		oversized(f, 100, 1)
		got, err := NewIssueService(f.client()).GetIssue(ctx, "acme", "widgets", 100)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		whole(t, "issue #100", got.SubIssues, got.BlockedBy, got.Blocking)
		if got := f.followUpRequestCount(); got != 1 {
			t.Fatalf("follow-up requests = %d, want 1 for 3 connections", got)
		}
	})

	t.Run("more connections than one request carries", func(t *testing.T) {
		f := newRelationForge(t)
		f.board = oversized(f, 100, 20)
		items, _, err := NewBoardService(f.client(), "acme", 1).ListOpenItems(ctx)
		if err != nil {
			t.Fatalf("ListOpenItems: %v", err)
		}
		if len(items) != 20 {
			t.Fatalf("board items = %d, want 20", len(items))
		}
		for _, it := range items {
			whole(t, fmt.Sprintf("board item #%d", it.Number), it.SubIssues, it.BlockedBy, it.Blocking)
		}
		want := (60 + relationFollowUpsPerRequest - 1) / relationFollowUpsPerRequest
		if got := f.followUpRequestCount(); got != want {
			t.Fatalf("follow-up requests = %d, want %d for 60 connections", got, want)
		}
	})

	t.Run("a connection several pages long", func(t *testing.T) {
		f := newRelationForge(t)
		epic := f.issue(100, "OPEN")
		epic.subIssues = f.numbers(1001, 230, "CLOSED")
		got, err := NewIssueService(f.client()).GetIssue(ctx, "acme", "widgets", 100)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		assertNumbers(t, "sub-issues", subIssueNumbers(got.SubIssues), epic.subIssues)
		// 25 on the first page, then 100, 100 and 5.
		if got := f.followUpRequestCount(); got != 3 {
			t.Fatalf("follow-up requests = %d, want 3", got)
		}
	})
}

// TestGetIssuesByNumbersWithoutRelations covers the read for callers that use
// only an issue's state or body (the scheduler's blocker-state refresh and
// dangling-edge check, the dependency graph's body fetch). It selects no
// relationship connection, so it reads no follow-up page and cannot fail on
// one, even for an issue whose connections could not be completed.
func TestGetIssuesByNumbersWithoutRelations(t *testing.T) {
	ctx := context.Background()
	f := newRelationForge(t)
	f.failFollowUps = followUpHTTP502
	iss := f.issue(700, "OPEN")
	iss.subIssues = f.numbers(1001, 60, "CLOSED")
	iss.blockedBy = f.numbers(301, 11, "CLOSED")
	iss.blocks = f.numbers(401, 7, "OPEN")
	f.issue(701, "CLOSED")
	svc := NewIssueService(f.client())

	got, err := svc.GetIssuesByNumbersWithoutRelations(ctx, "acme", "widgets", []int{700, 701})
	if err != nil {
		t.Fatalf("GetIssuesByNumbersWithoutRelations: %v", err)
	}
	if got[700] == nil || got[700].State != "OPEN" || got[701] == nil || got[701].State != "CLOSED" {
		t.Fatalf("states = %+v, want #700 OPEN and #701 CLOSED", got)
	}
	if n := len(got[700].SubIssues) + len(got[700].BlockedBy) + len(got[700].Blocking); n != 0 || got[700].IsEpic {
		t.Fatalf("#700 carries %d relationships (IsEpic %v); the read selects none", n, got[700].IsEpic)
	}
	if n := f.followUpRequestCount(); n != 0 {
		t.Fatalf("follow-up requests = %d, want 0", n)
	}
	for _, q := range f.answered() {
		if m := reRelation.FindString(q); m != "" {
			t.Fatalf("query selects the %s connection:\n%s", strings.TrimSuffix(m, "("), q)
		}
	}

	// The same issue through the relationship read cannot be completed here.
	if _, err := svc.GetIssuesByNumbers(ctx, "acme", "widgets", []int{700}); !errors.Is(err, ErrConnectionTruncated) {
		t.Fatalf("GetIssuesByNumbers err = %v, want ErrConnectionTruncated", err)
	}
}

// TestRelationPageSelectionsMatchStructs pins the follow-up selections to the
// structs the first page decodes into: the GraphQL client renders
// subIssuePage and blockingPage exactly as the constants read, so a field
// added to subIssueNode or blockingNode reaches follow-up pages too.
func TestRelationPageSelectionsMatchStructs(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req.Query)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"p":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClientWithURL("test-token", srv.URL)
	c.limiter = rate.NewLimiter(rate.Inf, 1)

	var sub struct {
		P subIssuePage `graphql:"p"`
	}
	var blk struct {
		P blockingPage `graphql:"p"`
	}
	for _, q := range []interface{}{&sub, &blk} {
		if err := c.query(context.Background(), q, nil); err != nil {
			t.Fatalf("query: %v", err)
		}
	}
	want := []string{"{p" + subIssuePageSelection + "}", "{p" + blockingPageSelection + "}"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("client renders\n  %q\nfollow-up selections read\n  %q", got, want)
	}
}
