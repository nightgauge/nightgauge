package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// twoRepoUnrefinedIssuesServer answers both the REST labels preflight and the
// GraphQL candidate list for two distinct repos from one httptest server,
// routing on the `name` GraphQL variable ListIssuesExcludingLabels sends
// (internal/github/issues.go: `vars["name"] = graphql.String(repo)`).
//
// r1 is given THREE trusted candidates and r2 only one, modeling the issue's
// "a workspace whose first repo has a steady stream of unrefined issues"
// scenario: with a single scheduler-wide slot, r1's first candidate goes on
// a 5-minute per-issue cooldown after cycle 1 dispatches it, but r1 still has
// a second fresh candidate available for cycle 2 — so a fixed scan order
// keeps winning the slot from repos[0] every cycle rather than "naturally"
// rotating away once r1's one candidate is exhausted. A one-candidate-per-repo
// fixture would not distinguish fixed order from rotation, because the
// per-issue cooldown alone would push cycle 2 on to r2 either way.
func twoRepoUnrefinedIssuesServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveRepoLabels(w, r) {
			return
		}
		var req struct {
			Variables struct {
				Name string `json:"name"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		switch req.Variables.Name {
		case "r1":
			_, _ = w.Write([]byte(`{
				"data": {
					"repository": {
						"issues": {
							"pageInfo": {"hasNextPage": false, "endCursor": ""},
							"nodes": [
								{
									"number": 21,
									"title": "r1 candidate A",
									"createdAt": "2026-01-01T00:00:00Z",
									"authorAssociation": "OWNER",
									"labels": {"nodes": []}
								},
								{
									"number": 22,
									"title": "r1 candidate B",
									"createdAt": "2026-01-01T00:00:00Z",
									"authorAssociation": "OWNER",
									"labels": {"nodes": []}
								},
								{
									"number": 23,
									"title": "r1 candidate C",
									"createdAt": "2026-01-01T00:00:00Z",
									"authorAssociation": "OWNER",
									"labels": {"nodes": []}
								}
							]
						}
					}
				}
			}`))
		case "r2":
			_, _ = w.Write([]byte(`{
				"data": {
					"repository": {
						"issues": {
							"pageInfo": {"hasNextPage": false, "endCursor": ""},
							"nodes": [
								{
									"number": 31,
									"title": "r2 candidate",
									"createdAt": "2026-01-01T00:00:00Z",
									"authorAssociation": "OWNER",
									"labels": {"nodes": []}
								}
							]
						}
					}
				}
			}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`))
		}
	}))
}

// TestRunRefinementCycle_RotatesScanStartAcrossCycles pins #502: the
// scheduler-wide refinement semaphore (cap 1 at the default
// refinement_max_concurrent) combined with a FIXED scan order over as.repos
// means repos[0] wins the single slot every cycle for as long as it has
// unrefined candidates — every other repo either short-circuits on the #488
// cycle-exhaustion break or scans and finds the slot already taken. With r1
// holding a steady stream of candidates (so its per-issue cooldown never
// exhausts it) and r2 holding one, and the slot released between cycles,
// consecutive cycles must dispatch from DIFFERENT repos. On unmodified main,
// both cycles dispatch repo "r1" (repos[0] always wins the scan, and its
// second candidate is still cooldown-free), which is the exact starvation
// #502 reports.
func TestRunRefinementCycle_RotatesScanStartAcrossCycles(t *testing.T) {
	srv := twoRepoUnrefinedIssuesServer(t)
	defer srv.Close()

	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 1 // one scheduler-wide slot, two repos each with one candidate

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o1", Name: "r1", Project: 1},
		{Owner: "o2", Name: "r2", Project: 2},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"

	var mu sync.Mutex
	var dispatchedRepo string
	// holdSlot pins the slot for the duration of a cycle, exactly as in
	// TestRunRefinementCycle_RefusedSemaphoreGatesDispatch: OnRefinementDispatch
	// runs synchronously inside refineIssue, before its release defer, so
	// blocking here holds the token until the test lets it go.
	holdSlot := make(chan struct{})
	as.OnRefinementDispatch(func(owner, repo string, _ int) {
		mu.Lock()
		dispatchedRepo = repo
		mu.Unlock()
		<-holdSlot
	})
	observed := func() string {
		mu.Lock()
		defer mu.Unlock()
		return dispatchedRepo
	}

	drainCycle := func() {
		t.Helper()
		drained := make(chan struct{})
		go func() { as.drainBackground(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(15 * time.Second):
			t.Fatalf("drainBackground never returned")
		}
	}

	// Cycle 1: slot is free, both repos hold a candidate.
	as.runRefinementCycle(context.Background())
	close(holdSlot) // release cycle 1's dispatch so drain and cycle 2 can proceed
	drainCycle()
	firstRepo := observed()
	if firstRepo == "" {
		t.Fatalf("cycle 1 dispatched nothing")
	}

	// Cycle 2: slot is free again (released above), both repos still hold
	// their (undispatched, or re-dispatched) candidate. Reset the callback's
	// hold channel so cycle 2's dispatch blocks independently of cycle 1's.
	mu.Lock()
	dispatchedRepo = ""
	mu.Unlock()
	holdSlot = make(chan struct{})
	as.runRefinementCycle(context.Background())
	close(holdSlot)
	drainCycle()
	secondRepo := observed()
	if secondRepo == "" {
		t.Fatalf("cycle 2 dispatched nothing")
	}

	if secondRepo == firstRepo {
		t.Fatalf("cycle 1 and cycle 2 both dispatched repo %q — the scan order is fixed at "+
			"repos[0], so with a scheduler-wide slot of 1 the same repo wins every cycle for as "+
			"long as it has candidates; consecutive cycles must rotate the scan start so slot "+
			"opportunity circulates across repos", firstRepo)
	}
}
