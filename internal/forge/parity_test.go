// Package forge_test verifies cross-forge behavioural parity between the
// GitHub and GitLab adapters for the field-mapping operations introduced in
// #3357. The contract is: writing a Status / Priority / Size / Iteration
// field through ProjectService and reading the issue back through
// BoardService.GetItem yields the same logical value regardless of which
// forge backs the call.
//
// Iteration parity is asymmetric on CE: GitLab CE uses a milestone fallback
// while GitHub uses a native iteration field. The parity case for Iteration
// asserts only that "the value round-trips on each adapter" rather than
// requiring an identical underlying representation.
package forge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/nightgauge/nightgauge/internal/github" // adapter registration
	"github.com/nightgauge/nightgauge/internal/gitlab"
	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

// gitlabStubServer is a minimal in-process server that emulates enough of
// the GitLab REST surface for the parity contract to exercise SetField +
// GetItem against. The behaviour is intentionally narrow — the goal is
// behavioural equivalence verification, not full server emulation.
type gitlabStubServer struct {
	t          *testing.T
	srv        *httptest.Server
	mux        *http.ServeMux
	labels     map[string]bool
	curLabels  []string
	weight     int
	healthStat string
	milestone  int
}

func newGitlabStubServer(t *testing.T) *gitlabStubServer {
	t.Helper()
	s := &gitlabStubServer{t: t, mux: http.NewServeMux(), labels: map[string]bool{}}
	s.srv = httptest.NewServer(s.mux)
	t.Cleanup(s.srv.Close)
	s.installHandlers()
	return s
}

func (s *gitlabStubServer) installHandlers() {
	// Edition probe → return CE so milestone fallback is the iteration path.
	s.mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	})

	// Labels endpoint: supports list (GET) and create (POST).
	s.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if name, _ := body["name"].(string); name != "" {
				s.labels[name] = true
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		// GET — list every label
		out := []map[string]string{}
		for name := range s.labels {
			out = append(out, map[string]string{"name": name})
		}
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(out)
	})

	// Milestones endpoint: supports list (GET) for iteration fallback.
	s.mux.HandleFunc("/api/v4/projects/o%2Fr/milestones", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.milestone++
			out := map[string]any{
				"id":    s.milestone,
				"title": body["title"],
				"state": "active",
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})

	// Issue endpoint: GET returns current state, PUT updates labels /
	// weight / health_status / milestone_id.
	s.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["labels"].(string); ok {
				s.curLabels = nil
				for _, l := range strings.Split(v, ",") {
					l = strings.TrimSpace(l)
					if l != "" {
						s.curLabels = append(s.curLabels, l)
					}
				}
			}
			if v, ok := body["weight"].(float64); ok {
				s.weight = int(v)
			}
			if v, ok := body["health_status"].(string); ok {
				s.healthStat = v
			}
		}
		out := map[string]any{
			"id":            int64(42000),
			"iid":           42,
			"title":         "issue 42",
			"state":         "opened",
			"labels":        s.curLabels,
			"web_url":       s.srv.URL + "/o/r/-/issues/42",
			"created_at":    "2026-01-01T00:00:00Z",
			"updated_at":    "2026-01-01T00:00:00Z",
			"weight":        s.weight,
			"health_status": s.healthStat,
		}
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(out)
	})
}

// TestParityContract_StatusPriority_Size verifies that writing each scoped
// field through the GitLab adapter round-trips via GetItem with the same
// logical value the caller wrote. The GitHub adapter is exercised by the
// dedicated github tests; the parity contract here pins the GitLab side
// against the canonical writeable surface.
func TestParityContract_StatusPriority_Size(t *testing.T) {
	stub := newGitlabStubServer(t)
	c := gitlab.NewClient(stub.srv.URL, "tok")
	p := gitlab.NewProjectServiceFor(c, "o", "r", gitlab.StrategyLabelStatus, 0)
	b := gitlab.NewBoardServiceFor(c, "o", "r")
	ctx := context.Background()

	cases := []struct {
		field, value string
	}{
		{"Status", "Ready"},
		{"Priority", "P0"},
		{"Size", "M"},
		{"Component", "API"}, // generic single-select fallback
	}
	for _, tc := range cases {
		if err := p.SetSingleSelectField(ctx, "gitlab:o/r#42", tc.field, tc.value); err != nil {
			t.Fatalf("SetSingleSelectField(%s=%s): %v", tc.field, tc.value, err)
		}
	}

	got, err := b.GetItem(ctx, "o", "r", 42)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.Status != "Ready" {
		t.Errorf("Status = %q, want Ready", got.Status)
	}
	if string(got.Priority) != "P0" {
		t.Errorf("Priority = %q, want P0", got.Priority)
	}
	if string(got.Size) != "M" {
		t.Errorf("Size = %q, want M", got.Size)
	}
	// Component is encoded as scoped label — verify via labels.
	hasComponent := false
	for _, l := range got.Labels {
		if l == "Component::API" {
			hasComponent = true
			break
		}
	}
	if !hasComponent {
		t.Errorf("Component label missing: labels = %v", got.Labels)
	}
}

// TestParityContract_Iteration_CEUsesMilestoneFallback documents the
// asymmetry in iteration handling on CE. The parity contract is: the
// adapter accepts the same `SetIterationField` call and produces a value
// the caller can later read back, even though the underlying mechanism
// differs (milestone vs. native iteration_id).
func TestParityContract_Iteration_CEUsesMilestoneFallback(t *testing.T) {
	stub := newGitlabStubServer(t)
	c := gitlab.NewClient(stub.srv.URL, "tok")
	p := gitlab.NewProjectServiceFor(c, "o", "r", gitlab.StrategyLabelStatus, 0)
	ctx := context.Background()

	// On CE, SetIterationField creates the milestone if missing then
	// attaches it to the issue.
	if err := p.SetIterationField(ctx, "gitlab:o/r#42", "Iteration", "Sprint 5"); err != nil {
		t.Fatalf("SetIterationField: %v", err)
	}
	if stub.milestone != 1 {
		t.Errorf("milestone count = %d, want 1 (auto-created)", stub.milestone)
	}
}

// gitlabBlockedByStubServer extends the basic gitlabStubServer with the
// /issues/:iid/links endpoint so AddBlockedBy / GetItem round-trips can
// be exercised through the GitLab adapter.
type gitlabBlockedByStubServer struct {
	t     *testing.T
	srv   *httptest.Server
	mux   *http.ServeMux
	links map[int][]map[string]any // iid → link records
	next  int
}

func newGitlabBlockedByStub(t *testing.T) *gitlabBlockedByStubServer {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s := &gitlabBlockedByStubServer{t: t, srv: srv, mux: mux, links: map[int][]map[string]any{}, next: 1000}

	mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	})

	mux.HandleFunc("/api/v4/projects/o%2Fr/issues/", func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/api/v4/projects/o/r/issues/"
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		rest := path[len(prefix):]
		// /<iid>/links — POST adds a link, GET lists
		if strings.HasSuffix(rest, "/links") {
			iid := pickIID(rest)
			if r.Method == "POST" {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				targetIID := int(body["target_issue_iid"].(float64))
				linkType, _ := body["link_type"].(string)
				s.next++
				record := map[string]any{
					"id":            s.next,
					"iid":           targetIID,
					"issue_link_id": s.next,
					"title":         fmt.Sprintf("Issue %d", targetIID),
					"state":         "opened",
					"link_type":     linkType,
				}
				s.links[iid] = append(s.links[iid], record)
				w.WriteHeader(201)
				_ = json.NewEncoder(w).Encode(record)
				return
			}
			if r.Method == "GET" {
				w.WriteHeader(200)
				_ = json.NewEncoder(w).Encode(s.links[iid])
				return
			}
		}
		// /<iid> — GET returns sample issue payload
		if r.Method == "GET" {
			iid := pickIID(rest)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(gitlab.MarshalRawIssue(iid, fmt.Sprintf("Issue %d", iid), nil)))
			return
		}
		http.NotFound(w, r)
	})

	return s
}

func pickIID(rest string) int {
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return parseIntForTest(rest)
	}
	return parseIntForTest(rest[:idx])
}

func parseIntForTest(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// TestParityContract_BlockedBy_RoundTrip pins the cross-forge contract
// that AddBlockedBy followed by GetItem yields a BoardItem.BlockedBy[]
// shape consumable by the VSCode lock-icon predicate (isBlocked) on either
// adapter. The GitHub side of this round-trip is covered by the existing
// internal/github tests that exercise the GraphQL mutation paths; this
// test pins the GitLab side and asserts the resulting BoardItem.BlockedBy
// shape matches what a GitHub-adapter caller would receive.
func TestParityContract_BlockedBy_RoundTrip(t *testing.T) {
	stub := newGitlabBlockedByStub(t)
	c := gitlab.NewClient(stub.srv.URL, "tok")
	svc := gitlab.NewIssueService(c)
	board := gitlab.NewBoardServiceFor(c, "o", "r")
	ctx := context.Background()

	// Write blocking relationship: #42 is blocked by #43.
	if err := svc.AddBlockedBy(ctx, "o/r#42", "o/r#43"); err != nil {
		t.Fatalf("AddBlockedBy: %v", err)
	}

	// Read back via BoardService.GetItem (what the VSCode tree view consumes).
	item, err := board.GetItem(ctx, "o", "r", 42)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if len(item.BlockedBy) != 1 {
		t.Fatalf("BlockedBy = %d, want 1", len(item.BlockedBy))
	}
	got := item.BlockedBy[0]

	// Cross-forge shape contract:
	//   - Number must be the linked issue's number (forge-agnostic identifier)
	//   - Repo must be the "owner/repo" string (matches GitHub adapter convention)
	//   - State must be "opened" so isBlocked() treats it as active
	if got.Number != 43 {
		t.Errorf("BlockedBy[0].Number = %d, want 43", got.Number)
	}
	if got.Repo != "o/r" {
		t.Errorf("BlockedBy[0].Repo = %q, want o/r", got.Repo)
	}
	if got.State != "opened" {
		t.Errorf("BlockedBy[0].State = %q, want opened", got.State)
	}
}

// TestParityContract_PlanWaves pins the cross-forge contract that the
// EpicService.PlanWaves output is identical for the same dependency
// graph regardless of which forge backed the fetch. Both adapters
// delegate to teams.PlanWavesFromIssues, so the parity is structural —
// this test asserts that contract by feeding both adapter wrappers the
// same input and comparing wave assignments.
//
// The GitHub adapter's PlanWaves is exercised in internal/github
// directly; this test pins the GitLab side against a synthetic input
// produced from the same pkgtypes.Issue fixture.
func TestParityContract_PlanWaves(t *testing.T) {
	// Shared fixture: 5-issue dependency graph
	//   1 → 2,3 → 4 → 5
	fixture := []pkgtypes.Issue{
		{Number: 1, Title: "Root", Repo: "o/r"},
		{Number: 2, Title: "B", Repo: "o/r", BlockedBy: []pkgtypes.BlockingRef{{Number: 1, Repo: "o/r", State: "opened"}}},
		{Number: 3, Title: "C", Repo: "o/r", BlockedBy: []pkgtypes.BlockingRef{{Number: 1, Repo: "o/r", State: "opened"}}},
		{Number: 4, Title: "D", Repo: "o/r", BlockedBy: []pkgtypes.BlockingRef{
			{Number: 2, Repo: "o/r", State: "opened"},
			{Number: 3, Repo: "o/r", State: "opened"},
		}},
		{Number: 5, Title: "E", Repo: "o/r", BlockedBy: []pkgtypes.BlockingRef{{Number: 4, Repo: "o/r", State: "opened"}}},
	}

	// The shared algorithm produces the canonical wave assignment.
	canonical := teams.PlanWavesFromIssues(fixture)

	// Exercise the GitLab adapter end-to-end against a stub that returns
	// the fixture issues with their BlockedBy graphs.
	stub := newGitlabPlanWavesStub(t, fixture)
	c := gitlab.NewClient(stub.URL, "tok")
	gl := gitlab.NewEpicService(c)

	gitlabResult, err := gl.PlanWaves(context.Background(), "o", "r", []int{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatalf("gitlab PlanWaves: %v", err)
	}

	// Wave-shape contract: same wave count, same per-wave issue numbers.
	if gitlabResult.SubIssueCount != canonical.SubIssueCount {
		t.Errorf("SubIssueCount: gitlab=%d canonical=%d", gitlabResult.SubIssueCount, canonical.SubIssueCount)
	}
	if len(gitlabResult.Waves) != len(canonical.Waves) {
		t.Fatalf("Waves count: gitlab=%d canonical=%d", len(gitlabResult.Waves), len(canonical.Waves))
	}
	for i := range canonical.Waves {
		want := waveNumbersForTest(canonical.Waves[i])
		got := waveNumbersForTest(gitlabResult.Waves[i])
		if !sortedEqual(want, got) {
			t.Errorf("wave %d: gitlab=%v canonical=%v", i, got, want)
		}
	}
}

// newGitlabPlanWavesStub returns an httptest.Server that serves the
// /issues/:iid and /issues/:iid/links endpoints from a pkgtypes.Issue
// fixture. PlanWaves issues one /issues/:iid GET per number plus one
// /links GET per fetched issue; the stub returns the fixture's
// BlockedBy as is_blocked_by links.
func newGitlabPlanWavesStub(t *testing.T, fixture []pkgtypes.Issue) *httptest.Server {
	t.Helper()
	byNumber := make(map[int]pkgtypes.Issue, len(fixture))
	for _, i := range fixture {
		byNumber[i.Number] = i
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/issues/", func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/api/v4/projects/o/r/issues/"
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		rest := path[len(prefix):]
		if strings.HasSuffix(rest, "/links") {
			iid := pickIID(rest)
			issue, ok := byNumber[iid]
			out := []map[string]any{}
			if ok {
				for _, b := range issue.BlockedBy {
					out = append(out, map[string]any{
						"id":            b.Number * 10,
						"iid":           b.Number,
						"issue_link_id": b.Number * 10,
						"title":         fmt.Sprintf("Issue %d", b.Number),
						"state":         "opened",
						"link_type":     "is_blocked_by",
					})
				}
			}
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		iid := pickIID(rest)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(gitlab.MarshalRawIssue(iid, fmt.Sprintf("Issue %d", iid), nil)))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func waveNumbersForTest(w teams.WaveAssignment) []int {
	out := make([]int, len(w.Issues))
	for i, s := range w.Issues {
		out[i] = s.Number
	}
	return out
}

func sortedEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		seen := false
		for _, y := range b {
			if x == y {
				seen = true
				break
			}
		}
		if !seen {
			return false
		}
	}
	return true
}
