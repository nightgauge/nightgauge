package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// threeTrustedUnrefinedIssuesServer is the sibling fixture to
// unrefinedIssuesServer (#270): all three candidates are authored by OWNER, so
// the author-trust gate passes on ALL of them and the only thing left standing
// between candidates #12/#13 and a dispatch is the refinement semaphore. The
// #270 fixture is deliberately left alone — its tests assert on exactly one
// trusted candidate.
//
// Three candidates (not two) is what separates `break` from `continue` at the
// refusal: with two, both shapes dispatch exactly #11 and log exactly one
// refusal, so the arms are indistinguishable. With three, `continue` refuses
// twice.
func threeTrustedUnrefinedIssuesServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveRepoLabels(w, r) {
			return
		}
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
							},
							{
								"number": 13,
								"title": "Third trusted issue",
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
// — where `break` binds to the SELECT, not to the enclosing for. So a refusal fell
// straight through to RecordRefinementStart, the dispatch log, goTracked
// (refineIssue) and dispatched++, all without a slot held. refineIssue's defer
// releases with a bare `<-as.refinementSem`, so over-dispatching N candidates
// against a cap of 1 leaves N-1 releases parked forever on an empty channel —
// and #428's drainBackground (wg.Wait over the generation) then never returns.
//
// Four failure modes belong to this test: an over-dispatch count, a drain that
// never comes back, a refused candidate that still burns a slot of the hourly
// refinement rate limit, and a refusal that `continue`s to the next candidate
// instead of ending this repo's loop. The drain runs bounded, in a goroutine,
// so the second one fails LOUDLY here instead of being swallowed as a package
// timeout.
//
// This test captures the process-global standard logger, so it must NEVER call
// t.Parallel().
func TestRunRefinementCycle_RefusedSemaphoreGatesDispatch(t *testing.T) {
	logs := withCapturedLog(t)

	srv := threeTrustedUnrefinedIssuesServer(t)
	defer srv.Close()

	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 1 // one slot, three eligible candidates

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o", Name: "r", Project: 1},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"

	var mu sync.Mutex
	var dispatched []int
	// holdSlot is what makes the refusal DETERMINISTIC instead of a 2-5 ms
	// race. #11's refineIssue runs in its own goroutine and completes in
	// roughly 2-5 ms against this fixture; if the parent goroutine deschedules
	// for that long between candidate #11 and candidate #12, #11 has already
	// RELEASED its slot and even CORRECT code legitimately dispatches [11 12] —
	// which is the regression signature this test exists to catch. Blocking
	// inside the dispatch callback pins the slot for the whole candidate loop:
	// onRefinementDispatch is invoked synchronously inside refineIssue, BEFORE
	// its release defer runs. Do not "simplify" this channel away.
	holdSlot := make(chan struct{})
	// Registering the IPC dispatch callback is also what makes refinement
	// viable without a CLI adapter, exactly as in the #270 test.
	as.OnRefinementDispatch(func(_, _ string, issueNumber int) {
		mu.Lock()
		dispatched = append(dispatched, issueNumber)
		mu.Unlock()
		<-holdSlot
	})
	observed := func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), dispatched...)
	}

	as.runRefinementCycle(context.Background())
	close(holdSlot) // candidate loop is done; let refineIssue finish and release

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

	if got := strings.Count(logs.String(), "refinement slots occupied"); got != 1 {
		t.Errorf("refusal logged %d times, want exactly 1 — `break` must end this repo's candidate loop, not `continue` to the next candidate", got)
	}

	// No leaked token and no parked release: every acquisition this cycle made
	// was matched by the release of a goroutine the drain just joined.
	if n := len(as.refinementSem); n != 0 {
		t.Fatalf("refinement semaphore holds %d token(s) after the drain, want 0 — acquisitions "+
			"and releases are no longer paired", n)
	}

	if n := as.safetyRails.State().RefinementStartsThisHour; n != 1 {
		t.Errorf("safetyRails counted %d refinement start(s), want 1 — a REFUSED candidate "+
			"must not burn a slot of the hourly refinement rate limit", n)
	}
}

// TestRunRefinementCycle_SaturatedCycleSpendsNoAPIQuota pins the cycle-level
// half of #488's two-gate semantics. The refinement semaphore is
// scheduler-wide, so once it is saturated NO repo in the cycle can dispatch —
// and listing them anyway costs one paginated GraphQL call per repo per 60s
// tick purely to refuse, the GitHub API budget class epic #482 addressed.
//
// The per-candidate gate cannot express this: its `break` only ends the
// current repo's candidate loop, and by then the list call has already been
// paid. This test is therefore a QUOTA pin, not a correctness pin — deleting
// the short-circuit leaves TestRunRefinementCycle_RefusedSemaphoreGatesDispatch
// green and only moves this counter off zero.
func TestRunRefinementCycle_SaturatedCycleSpendsNoAPIQuota(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`))
	}))
	defer srv.Close()

	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 1

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o", Name: "r1", Project: 1},
		{Owner: "o", Name: "r2", Project: 2},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"
	// Registering the IPC callback is what makes refinement viable without a
	// CLI adapter; the cycle must short-circuit before it is ever called.
	as.OnRefinementDispatch(func(_, _ string, _ int) {})

	// Saturate the scheduler-wide semaphore before the cycle starts, exactly
	// as a refinement still running from an earlier cycle would.
	as.refinementSem <- struct{}{}

	as.runRefinementCycle(context.Background())

	drained := make(chan struct{})
	go func() { as.drainBackground(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(15 * time.Second):
		t.Fatalf("drainBackground never returned — a saturated cycle must not dispatch at all")
	}

	if n := atomic.LoadInt64(&hits); n != 0 {
		t.Errorf("a saturated refinement cycle made %d GitHub list call(s) across 2 repos, want 0 — "+
			"the semaphore is scheduler-wide, so an exhausted one must stop the cycle's scanning "+
			"before any repo pays for a list it can only refuse", n)
	}

	<-as.refinementSem // release the token this test injected
}
