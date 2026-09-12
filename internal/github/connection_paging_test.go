package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

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
	// followUps counts the follow-up page reads per connection name.
	followUps map[string]int
}

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
	reFollowUp  = regexp.MustCompile(`(subIssues|blockedBy|blocking)\(first: \$first, after: \$after\)`)
	reBatchItem = regexp.MustCompile(`i(\d+): issue\(number: (\d+)\)`)
	reIssueID   = regexp.MustCompile(`\.\.\.\s*on Issue\s*\{\s*id\b`)
)

// followUpCount is the number of follow-up pages read for conn so far.
func (f *relationForge) followUpCount(conn string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.followUps[conn]
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
	var data map[string]interface{}
	switch {
	case reFollowUp.MatchString(q):
		conn := reFollowUp.FindStringSubmatch(q)[1]
		f.followUps[conn]++
		iss := f.byID[fmt.Sprint(vars["id"])]
		if iss == nil {
			data = map[string]interface{}{"node": nil}
			break
		}
		first := int(vars["first"].(float64))
		start, _ := strconv.Atoi(strings.TrimPrefix(fmt.Sprint(vars["after"]), "after-"))
		data = map[string]interface{}{"node": map[string]interface{}{
			"__typename": "Issue",
			conn:         f.page(conn, iss, start, first, true),
		}}
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

	t.Run("board scan", func(t *testing.T) {
		items, err := NewBoardService(c, "acme", 1).ListItems(ctx, "")
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("board items = %d, want 1", len(items))
		}
		assertNumbers(t, "board sub-issues", subIssueNumbers(items[0].SubIssues), epic.subIssues)
	})

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

	t.Run("board scan feeds the scheduler blocker check", func(t *testing.T) {
		items, err := NewBoardService(c, "acme", 1).ListItems(ctx, "")
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("board items = %d, want 1", len(items))
		}
		check(t, "board scan", items[0].BlockedBy, items[0].Blocking)
	})

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

	t.Run("board scan", func(t *testing.T) {
		f, c := setup(t, "blockedBy", false)
		_, err := NewBoardService(c, "acme", 1).ListItems(ctx, "")
		requireCapped(t, f, "blockedBy", err)
	})

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
