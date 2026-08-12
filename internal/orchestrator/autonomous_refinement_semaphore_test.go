package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// twoTrustedUnrefinedIssuesServer is the sibling fixture to
// unrefinedIssuesServer (#270): both candidates are authored by OWNER, so the
// author-trust gate passes on BOTH and the only thing left standing between
// candidate #12 and a dispatch is the refinement semaphore. The #270 fixture is
// deliberately left alone — its tests assert on exactly one trusted candidate.
func twoTrustedUnrefinedIssuesServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"repository": {
					"issues": {
						"pageInfo": {"hasNextPage": false, "endCursor": ""},
						"nodes": [
							{
								"number": 11,
								"title": "First trusted issue",
								"createdAt": "2026-01-01T00:00:00Z",
								"authorAssociation": "OWNER",
								"labels": {"nodes": []}
							},
							{
								"number": 12,
								"title": "Second trusted issue",
								"createdAt": "2026-01-01T00:00:00Z",
								"authorAssociation": "OWNER",
								"labels": {"nodes": []}
							}
						]
					}
				}
			}
		}`))
	}))
}

// TestRunRefinementCycle_RefusedSemaphoreGatesDispatch pins #488: a REFUSED
// refinement-slot acquisition must stop the candidate loop, not fall through
// into a dispatch that holds no slot.
//
// The original shape was `select { case sem <- struct{}{}: ; default: log; break }`
// — where `break` binds to the SELECT, not the enclosing for. So a refusal fell
// straight through to RecordRefinementStart, the dispatch log, goTracked
// (refineIssue) and dispatched++, all without a slot held. refineIssue's defer
// releases with a bare `<-as.refinementSem`, so over-dispatching N candidates
// against a cap of 1 leaves N-1 releases parked forever on an empty channel —
// and #428's drainBackground (wg.Wait over the generation) then never returns.
//
// Two failure modes therefore both belong to this test: an over-dispatch count
// and a drain that never comes back. The drain runs bounded, in a goroutine, so
// the second one fails LOUDLY here instead of being swallowed as a package
// timeout.
func TestRunRefinementCycle_RefusedSemaphoreGatesDispatch(t *testing.T) {
	srv := twoTrustedUnrefinedIssuesServer(t)
	defer srv.Close()

	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 1 // one slot, two eligible candidates

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o", Name: "r", Project: 1},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"

	var mu sync.Mutex
	var dispatched []int
	// Registering the IPC dispatch callback is also what makes refinement
	// viable without a CLI adapter, exactly as in the #270 test.
	as.OnRefinementDispatch(func(_, _ string, issueNumber int) {
		mu.Lock()
		dispatched = append(dispatched, issueNumber)
		mu.Unlock()
	})
	observed := func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), dispatched...)
	}

	as.runRefinementCycle(context.Background())

	// Drain in the test BODY, right at the trigger (t.Cleanup is LIFO — see
	// drainBackground's doc), and bound it: an unfixed regression parks a
	// refineIssue release on the empty semaphore forever, so an unbounded join
	// would eat the package timeout with no attribution.
	drained := make(chan struct{})
	go func() { as.drainBackground(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(15 * time.Second):
		t.Fatalf("drainBackground never returned (dispatches observed: %v) — a refineIssue "+
			"goroutine is parked forever on its bare `<-as.refinementSem` release because it "+
			"was dispatched WITHOUT acquiring a slot: the semaphore refusal's `break` binds to "+
			"the select, not the candidate loop", observed())
	}

	if got := observed(); len(got) != 1 || got[0] != 11 {
		t.Fatalf("expected exactly one refinement dispatch (#11) under RefinementMaxConcurrent=1, got %v — "+
			"a refused semaphore acquisition must gate the dispatch, not just log", got)
	}

	// No leaked token and no parked release: every acquisition this cycle made
	// was matched by the release of a goroutine the drain just joined.
	if n := len(as.refinementSem); n != 0 {
		t.Fatalf("refinement semaphore holds %d token(s) after the drain, want 0 — acquisitions "+
			"and releases are no longer paired", n)
	}
}
