package ipc

// The GitHub cost of opening a window, measured against a recorded-shape fake.
//
// A daemon serves the VS Code window: on open the Repositories tree reads each
// board's open items (board.listOpen), the status counts (board.counts) and
// the rate-limit reading, and the attention sweep evaluates every repository.
// This file drives those same entry points against a transport that prices
// every request the way GitHub bills it:
//
//   - GraphQL: a ProjectV2 item page 17 points, every other document 1, the
//     rateLimit query 0 (GitHub reports cost 1 and never decrements it);
//   - REST: a 200 (or any non-304) is one counted request against the core
//     limit; a 304 answered to If-None-Match is not counted.
//
// and asserts budgets for a 6-repo and a 20-repo workspace, cold (empty
// conditional store) and warm after a daemon restart (a NEW server and NEW
// clients over the SAME on-disk store).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// costMeter is what the fake GitHub billed.
type costMeter struct {
	mu            sync.Mutex
	graphqlPoints int
	graphqlOps    map[string]int
	restCounted   int
	rest304       int
	restByRoute   map[string]int
}

func newCostMeter() *costMeter {
	return &costMeter{graphqlOps: map[string]int{}, restByRoute: map[string]int{}}
}

func (m *costMeter) snapshot() (points, counted, notModified int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.graphqlPoints, m.restCounted, m.rest304
}

func (m *costMeter) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("graphql=%d pts %v; rest counted=%d (%v), 304=%d", m.graphqlPoints, m.graphqlOps, m.restCounted, m.restByRoute, m.rest304)
}

type fakeRepoState struct {
	name      string
	openPRs   int
	alerts    int
	protected bool
}

type fakeBoardItem struct {
	nodeID  string
	repo    string
	number  int
	status  string
	labels  []string
	blocked int // open blockers
	subs    int // sub-issues
	parent  int
}

// fakeGitHub is a transport answering the endpoints the daemon's window-open
// path uses, with deterministic ETags so a conditional request is answered 304
// exactly when the content is unchanged.
type fakeGitHub struct {
	t       *testing.T
	owner   string
	project int
	repos   []fakeRepoState
	items   []fakeBoardItem
	updated string
	meter   *costMeter
}

func newFakeWorkspace(t *testing.T, repoCount, itemsPerRepo int) *fakeGitHub {
	f := &fakeGitHub{t: t, owner: "acme", project: 3, updated: "2026-09-23T09:00:00Z", meter: newCostMeter()}
	statuses := []string{"Backlog", "Ready", "In progress", "Backlog", "In review"}
	for r := 0; r < repoCount; r++ {
		name := fmt.Sprintf("repo%02d", r)
		f.repos = append(f.repos, fakeRepoState{
			name:      name,
			openPRs:   map[bool]int{true: 2, false: 0}[r%2 == 0], // half the repos have open PRs
			alerts:    map[bool]int{true: 1, false: 0}[r%10 == 3],
			protected: r%3 == 0,
		})
		for i := 1; i <= itemsPerRepo; i++ {
			it := fakeBoardItem{
				nodeID: fmt.Sprintf("PVTI_%s_%d", name, i),
				repo:   "acme/" + name,
				number: i,
				status: statuses[i%len(statuses)],
				labels: []string{"priority:high"},
			}
			switch {
			case i == 1:
				it.labels = append(it.labels, "type:epic")
				it.subs = 3
			case i%7 == 0:
				it.blocked = 1
			case i <= 4:
				it.parent = 1
			}
			f.items = append(f.items, it)
		}
	}
	return f
}

func (f *fakeGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/graphql" {
		return f.graphql(req)
	}
	status, body, link := f.rest(req)
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}
	route := restRoute(req.URL.Path)
	if status == http.StatusOK {
		sum := sha256.Sum256(body)
		etag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
		resp.Header.Set("ETag", etag)
		if link != "" {
			resp.Header.Set("Link", link)
		}
		if req.Header.Get("If-None-Match") == etag {
			f.meter.mu.Lock()
			f.meter.rest304++
			f.meter.mu.Unlock()
			resp.StatusCode = http.StatusNotModified
			resp.Body = io.NopCloser(strings.NewReader(""))
			return resp, nil
		}
	}
	f.meter.mu.Lock()
	f.meter.restCounted++
	f.meter.restByRoute[route]++
	f.meter.mu.Unlock()
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return resp, nil
}

// restRoute collapses a path to its endpoint shape for the meter's breakdown.
func restRoute(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		if _, err := strconv.Atoi(s); err == nil {
			parts[i] = "{n}"
		}
		if strings.HasPrefix(s, "repo") && len(s) == 6 {
			parts[i] = "{repo}"
		}
	}
	return strings.Join(parts, "/")
}

func (f *fakeGitHub) repoByName(name string) *fakeRepoState {
	for i := range f.repos {
		if f.repos[i].name == name {
			return &f.repos[i]
		}
	}
	return nil
}

func (f *fakeGitHub) rest(req *http.Request) (int, []byte, string) {
	p := req.URL.Path
	q := req.URL.Query()
	js := func(v any) []byte { b, _ := json.Marshal(v); return b }
	orgProjects := "/orgs/" + f.owner + "/projectsV2"
	board := fmt.Sprintf("%s/%d", orgProjects, f.project)
	switch {
	case p == orgProjects:
		return 200, js([]map[string]any{{"number": f.project, "updated_at": f.updated}}), ""
	case p == board+"/fields":
		return 200, js([]map[string]any{
			{"id": 101, "name": "Status"}, {"id": 102, "name": "Priority"}, {"id": 103, "name": "Size"},
			{"id": 104, "name": "Parent issue"}, {"id": 105, "name": "Title"},
		}), ""
	case p == board+"/items":
		return f.itemsPage(req, q)
	}
	if !strings.HasPrefix(p, "/repos/"+f.owner+"/") {
		f.t.Errorf("fake GitHub: unexpected REST %s %s", req.Method, req.URL)
		return 404, js(map[string]string{"message": "Not Found"}), ""
	}
	rest := strings.Split(strings.TrimPrefix(p, "/repos/"+f.owner+"/"), "/")
	r := f.repoByName(rest[0])
	if r == nil {
		return 404, js(map[string]string{"message": "Not Found"}), ""
	}
	sub := strings.Join(rest[1:], "/")
	switch {
	case sub == "":
		return 200, js(map[string]any{"full_name": f.owner + "/" + r.name, "name": r.name, "owner": map[string]string{"login": f.owner}, "default_branch": "main"}), ""
	case sub == "branches/main":
		return 200, js(map[string]any{"name": "main"}), ""
	case sub == "branches/main/protection/required_status_checks":
		if !r.protected {
			return 404, js(map[string]string{"message": "Branch not protected"}), ""
		}
		return 200, js(map[string]any{"contexts": []string{"ci"}}), ""
	case sub == "rules/branches/main":
		return 200, []byte("[]"), ""
	case sub == "commits/main/check-runs":
		return 200, js(map[string]any{"check_runs": []map[string]string{{"name": "ci", "status": "completed", "conclusion": "success"}}}), ""
	case sub == "pulls":
		prs := make([]map[string]int, r.openPRs)
		for i := range prs {
			prs[i] = map[string]int{"number": 100 + i}
		}
		return 200, js(prs), ""
	case sub == "dependabot/alerts":
		alerts := make([]map[string]int, r.alerts)
		for i := range alerts {
			alerts[i] = map[string]int{"number": 1 + i}
		}
		return 200, js(alerts), ""
	case sub == "issues":
		return 200, []byte("[]"), ""
	case strings.HasSuffix(sub, "/dependencies/blocked_by"), strings.HasSuffix(sub, "/dependencies/blocking"), strings.HasSuffix(sub, "/sub_issues"):
		n, _ := strconv.Atoi(strings.Split(sub, "/")[1])
		return 200, js([]map[string]any{{"node_id": fmt.Sprintf("I_%d", n+1000), "number": n + 1000, "title": "rel", "state": "open", "repository_url": "https://api.github.com/repos/" + f.owner + "/" + r.name}}), ""
	}
	f.t.Errorf("fake GitHub: unexpected REST %s %s", req.Method, req.URL)
	return 404, js(map[string]string{"message": "Not Found"}), ""
}

// itemsPage serves the board's item list, 100 per page, filtered by the `q`
// terms the daemon sends (is:open, status:"X"), with a cursor Link header.
func (f *fakeGitHub) itemsPage(req *http.Request, q url.Values) (int, []byte, string) {
	want := ""
	if s := q.Get("q"); strings.Contains(s, "status:") {
		rest := s[strings.Index(s, "status:")+len("status:"):]
		if strings.HasPrefix(rest, `"`) {
			want = strings.SplitN(rest[1:], `"`, 2)[0]
		} else {
			want = strings.Fields(rest)[0]
		}
	}
	var matched []fakeBoardItem
	for _, it := range f.items {
		if want == "" || it.status == want {
			matched = append(matched, it)
		}
	}
	start, _ := strconv.Atoi(q.Get("after"))
	per, _ := strconv.Atoi(q.Get("per_page"))
	end := start + per
	link := ""
	if end < len(matched) {
		nq := url.Values{}
		for k, v := range q {
			nq[k] = v
		}
		nq.Set("after", strconv.Itoa(end))
		link = fmt.Sprintf(`<https://api.github.com%s?%s>; rel="next"`, req.URL.Path, nq.Encode())
	} else {
		end = len(matched)
	}
	page := []map[string]any{}
	for _, it := range matched[start:end] {
		labels := []map[string]string{}
		for _, l := range it.labels {
			labels = append(labels, map[string]string{"name": l})
		}
		var parent any
		if it.parent > 0 {
			parent = map[string]any{"number": it.parent, "title": "epic"}
		}
		page = append(page, map[string]any{
			"node_id":      it.nodeID,
			"content_type": "Issue",
			"content": map[string]any{
				"number": it.number, "title": fmt.Sprintf("issue %d", it.number), "state": "open",
				"html_url":           fmt.Sprintf("https://github.com/%s/issues/%d", it.repo, it.number),
				"repository":         map[string]string{"full_name": it.repo},
				"created_at":         "2026-09-01T00:00:00Z",
				"updated_at":         "2026-09-02T00:00:00Z",
				"author_association": "MEMBER",
				"labels":             labels,
				"sub_issues_summary": map[string]int{"total": it.subs, "completed": 0},
				"issue_dependencies_summary": map[string]int{
					"blocked_by": it.blocked, "total_blocked_by": it.blocked, "blocking": 0, "total_blocking": 0,
				},
			},
			"fields": []map[string]any{
				{"name": "Status", "value": map[string]any{"name": map[string]string{"raw": it.status}}},
				{"name": "Priority", "value": nil},
				{"name": "Size", "value": nil},
				{"name": "Parent issue", "value": parent},
			},
		})
	}
	b, _ := json.Marshal(page)
	return 200, b, link
}

func (f *fakeGitHub) graphql(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(raw, &body)
	name, _ := body.Variables["name"].(string)
	var (
		op, out string
		cost    = 1
	)
	switch {
	case strings.Contains(body.Query, "rateLimit") && !strings.Contains(body.Query, "repository"):
		op, cost = "rateLimit", 0
		out = `{"data":{"rateLimit":{"remaining":4999,"limit":5000,"resetAt":"2026-09-23T12:00:00Z"}}}`
	case strings.Contains(body.Query, "pullRequests"):
		op = "ListPRs"
		out = `{"data":{"repository":{"pullRequests":{"nodes":[{"number":100,"title":"t","state":"OPEN","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED"}]}}}}`
	case strings.Contains(body.Query, "vulnerabilityAlerts"):
		op = "vulnerabilityAlerts"
		out = `{"data":{"repository":{"url":"https://github.com/acme/` + name + `","hasVulnerabilityAlertsEnabled":true,` +
			`"vulnerabilityAlerts":{"totalCount":1,"pageInfo":{"hasNextPage":false},"nodes":[{"number":1,"createdAt":"2026-09-01T00:00:00Z",` +
			`"vulnerableManifestPath":"go.sum","securityAdvisory":{"ghsaId":"GHSA-x","summary":"s","severity":"HIGH","permalink":"https://github.com/advisories/GHSA-x","identifiers":[]},` +
			`"securityVulnerability":{"severity":"HIGH","package":{"name":"p","ecosystem":"GO"},"vulnerableVersionRange":"< 1","firstPatchedVersion":{"identifier":"1"}},` +
			`"dependabotUpdate":null}]}}}}`
	case strings.Contains(body.Query, "projectV2"):
		op, cost = "projectV2", 17
		f.t.Errorf("fake GitHub: a ProjectV2 GraphQL read on the window-open path: %.120s", body.Query)
		out = `{"data":null,"errors":[{"message":"not served"}]}`
	default:
		op = "other"
		f.t.Errorf("fake GitHub: unexpected GraphQL document: %.160s", body.Query)
		out = `{"data":null,"errors":[{"message":"not served"}]}`
	}
	f.meter.mu.Lock()
	f.meter.graphqlPoints += cost
	f.meter.graphqlOps[op]++
	f.meter.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(out)),
		Request:    req,
	}, nil
}

// costDaemon is one daemon process: a server whose clients all share one
// conditional store (the disk directory), as `serve` wires them.
func costDaemon(t *testing.T, f *fakeGitHub, storeDir string) *Server {
	t.Helper()
	store := gh.NewConditionalStore(storeDir)
	newClient := func() *gh.Client {
		return gh.NewClientWithHTTPClient(&http.Client{Transport: f}).WithConditionalStore(store, "tok:cost-test")
	}
	boards := boardcache.New(0)
	s := newAttentionTestServer(t)
	s.client = newClient()
	s.boards = boards
	sweepClient := newClient()
	s.forgeClientFn = func(repo string) (forge.ForgeClient, error) {
		return boardcache.WrapClient(boards, gh.NewForgeAdapter(sweepClient, f.owner, f.project, gh.OwnerTypeOrg), f.owner, f.project), nil
	}
	return s
}

func (f *fakeGitHub) repoSpecs() []string {
	out := make([]string, len(f.repos))
	for i, r := range f.repos {
		out[i] = f.owner + "/" + r.name
	}
	return out
}

// openWindow is what the Repositories tree asks on open: the rate-limit
// reading, one board.listOpen per repository row (every row renders
// expanded) and board.counts.
func openWindow(t *testing.T, s *Server, f *fakeGitHub) {
	t.Helper()
	ctx := context.Background()
	call := func(method string, params any) any {
		raw, _ := json.Marshal(params)
		out, err := s.methods[method](ctx, raw)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return out
	}
	call("github.rateLimit", map[string]any{})
	board := map[string]any{"owner": f.owner, "projectNumber": f.project}
	for range f.repos {
		items, _ := call("board.listOpen", board).([]interface{})
		_ = items
	}
	call("board.counts", board)
}

func probeChanged(t *testing.T, s *Server, f *fakeGitHub, since time.Time) bool {
	t.Helper()
	raw, _ := json.Marshal(BoardChangedParams{Repos: f.repoSpecs(), Since: since.UTC().Format(time.RFC3339)})
	out, err := s.handleBoardChanged(context.Background(), raw)
	if err != nil {
		t.Fatalf("board.changed: %v", err)
	}
	return out.(BoardChangedResult).Changed
}

func TestGitHubCost_WindowOpen(t *testing.T) {
	for _, tc := range []struct {
		repos, itemsPerRepo int
	}{
		{6, 40},  // 240 open items: three REST pages, like the live board
		{20, 40}, // 800 open items: eight pages
	} {
		t.Run(fmt.Sprintf("%d-repos", tc.repos), func(t *testing.T) {
			f := newFakeWorkspace(t, tc.repos, tc.itemsPerRepo)
			storeDir := t.TempDir()
			reposWithPRs, reposWithAlerts := 0, 0
			for _, r := range f.repos {
				if r.openPRs > 0 {
					reposWithPRs++
				}
				if r.alerts > 0 {
					reposWithAlerts++
				}
			}

			// --- Cold: an empty store, a fresh daemon, the window opens and the
			// activation sweep runs.
			cold := costDaemon(t, f, storeDir)
			openWindow(t, cold, f)
			// The extension consults the change probe before an activation
			// sweep when it holds a previous sweep time; a first-ever window
			// holds none and sweeps, and either way the probe has been asked
			// by the time a later window opens.
			probeChanged(t, cold, f, time.Now().Add(-time.Hour))
			res := runSweep(t, cold, f.repoSpecs()...)
			for _, r := range res.Repos {
				if r.Error != "" || r.Skipped || len(r.Failed) > 0 {
					t.Fatalf("cold sweep did not evaluate %s cleanly: %+v", r.Repo, r)
				}
			}
			sweptAt := time.Now()
			points, counted, _ := f.meter.snapshot()
			t.Logf("cold %d repos: %s", tc.repos, f.meter)
			// GraphQL only where REST cannot answer: the review/merge/check
			// rollup of repositories that HAVE open PRs, and the remediation
			// fact of repositories that HAVE open alerts. No board read.
			if want := reposWithPRs + reposWithAlerts; points != want {
				t.Errorf("cold GraphQL = %d points, want %d (one per repo with PRs + one per repo with alerts)", points, want)
			}
			if points > tc.repos {
				t.Errorf("cold GraphQL = %d points, over the ~1 per repo budget (%d repos)", points, tc.repos)
			}
			pages := (tc.repos*tc.itemsPerRepo + 99) / 100
			// Per repo: repo + default branch, protection + rules + check runs,
			// open alerts, open PRs = 7. Per board: fields + item pages, and the
			// project list for the probe.
			if limit := 7*tc.repos + pages + 2; counted > limit {
				t.Errorf("cold REST = %d counted requests, want <= %d", counted, limit)
			}

			// --- Warm: the daemon restarts (window reload). A new server and new
			// clients over the same on-disk store; the tree opens again and the
			// extension asks the change probe whether to sweep.
			f.meter = newCostMeter()
			warm := costDaemon(t, f, storeDir)
			openWindow(t, warm, f)
			if probeChanged(t, warm, f, sweptAt) {
				t.Error("board.changed reported a change on an unchanged workspace — the extension would sweep")
			}
			points, counted, notModified := f.meter.snapshot()
			t.Logf("warm %d repos: %s", tc.repos, f.meter)
			if points != 0 || counted != 0 {
				t.Errorf("warm re-open cost %d GraphQL points and %d counted REST requests, want 0 and 0", points, counted)
			}
			if notModified == 0 {
				t.Error("warm re-open issued no conditional requests at all — the test is not exercising revalidation")
			}

			// --- A board moves: only the probe and the changed read pay.
			f.meter = newCostMeter()
			if f.items[0].status == "Ready" {
				f.items[0].status = "Backlog"
			} else {
				f.items[0].status = "Ready"
			}
			f.updated = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
			moved := costDaemon(t, f, storeDir)
			if !probeChanged(t, moved, f, sweptAt) {
				t.Error("board.changed missed a board that moved")
			}
			openWindow(t, moved, f)
			points, counted, _ = f.meter.snapshot()
			t.Logf("after one item moved, %d repos: %s", tc.repos, f.meter)
			if points != 0 {
				t.Errorf("re-reading a moved board cost %d GraphQL points, want 0", points)
			}
			// The project list, and the one item page that changed. Later pages
			// are confirmed by 304s.
			if counted > 2 {
				t.Errorf("re-reading a moved board cost %d counted REST requests, want <= 2", counted)
			}
		})
	}
}

// A sweep forced after a restart (the timer, or a manual refresh) with nothing
// changed pays GraphQL only for the PR rollup of repositories with open PRs:
// the board, repository and alert reads all revalidate for free.
func TestGitHubCost_WarmSweepAfterRestart(t *testing.T) {
	f := newFakeWorkspace(t, 6, 40)
	storeDir := t.TempDir()
	runSweep(t, costDaemon(t, f, storeDir), f.repoSpecs()...)

	f.meter = newCostMeter()
	res := runSweep(t, costDaemon(t, f, storeDir), f.repoSpecs()...)
	for _, r := range res.Repos {
		if r.Error != "" || r.Skipped || len(r.Failed) > 0 {
			t.Fatalf("warm sweep did not evaluate %s cleanly: %+v", r.Repo, r)
		}
	}
	t.Logf("warm sweep, 6 repos: %s", f.meter)
	points, counted, _ := f.meter.snapshot()
	withPRs := 0
	for _, r := range f.repos {
		if r.openPRs > 0 {
			withPRs++
		}
	}
	if points != withPRs {
		t.Errorf("warm sweep GraphQL = %d points, want %d (ListPRs for repos with open PRs only)", points, withPRs)
	}
	// What still counts: an unprotected branch's protection read answers 404,
	// and a 404 carries no validator, so it is billed every time. Every 200
	// (the CI reads included, through the transport's persisted layer)
	// revalidates for free.
	unprotected := 0
	for _, r := range f.repos {
		if !r.protected {
			unprotected++
		}
	}
	if counted != unprotected {
		t.Errorf("warm sweep REST = %d counted, want %d (the unprotected branches' 404s only)", counted, unprotected)
	}
	if n := f.meter.restByRoute["repos/acme/{repo}/dependabot/alerts"] + f.meter.restByRoute["orgs/acme/projectsV2/{n}/items"]; n != 0 {
		t.Errorf("warm sweep re-billed %d alert/board reads that had not changed", n)
	}
}
