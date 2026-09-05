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

// TestRunRefinementCycle_ReposShrinkMidScanDoesNotPanic pins the adversarial
// review finding on #502's rotation rewrite: `numRepos := len(as.repos)` was
// read once, but `as.repos[(scanStart+scanIdx)%numRepos]` re-read as.repos on
// every iteration. ReplaceRepos and FilterRepos reassign as.repos from
// goroutines (the manifest poller, IPC workspace-update handlers) that run
// concurrently with this ticker and take no lock of their own — `range
// as.repos` was immune to that because it evaluates the slice header once,
// but the rewritten indexed loop was not: a repo set that shrinks mid-scan
// makes the stale numRepos bound index past the new, shorter slice.
//
// This drives the real race: repo r1's candidate query blocks on a channel,
// the repo set is narrowed from 3 repos to 1 while the scan is parked inside
// that request, and the request is then released. The scheduler's own
// refinementSem release runs bare with no recover (autonomous.go's
// goTracked/wg.Go), so an unrecovered panic here models a crashed daemon, not
// just a failed assertion.
func TestRunRefinementCycle_ReposShrinkMidScanDoesNotPanic(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var enteredOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveRepoLabels(w, r) {
			return
		}
		var req struct {
			Variables struct {
				Name string `json:"name"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Variables.Name == "r1" {
			enteredOnce.Do(func() { close(entered) })
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`))
	}))
	defer srv.Close()

	cfg := DefaultAutonomousConfig()
	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o", Name: "r1", Project: 1},
		{Owner: "o", Name: "r2", Project: 2},
		{Owner: "o", Name: "r3", Project: 3},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"
	as.OnRefinementDispatch(func(_, _ string, _ int) {})

	panicked := make(chan any, 1)
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicked <- r
			}
			close(done)
		}()
		as.runRefinementCycle(context.Background())
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("scan never reached repo r1's candidate query")
	}

	// Shrink the repo set to 1 while the scan is blocked mid-cycle on r1 —
	// exactly the manifest-poller / IPC workspace-update race the review
	// identified: ReplaceRepos takes no lock and reassigns as.repos while the
	// refinement ticker's scan is still working through it.
	if !as.ReplaceRepos([]depgraph.RepoConfig{{Owner: "o", Name: "r1", Project: 1}}) {
		t.Fatalf("ReplaceRepos reported no change — the fixture must actually narrow the repo set")
	}

	close(release)

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("runRefinementCycle never returned")
	}

	select {
	case p := <-panicked:
		t.Fatalf("runRefinementCycle panicked when the repo set shrank mid-scan: %v — as.repos "+
			"must be snapshotted once under as.mu before indexing, the way effectiveAvailableSlots "+
			"already does, not re-read per iteration against a stale numRepos bound", p)
	default:
	}
}

// TestRunRefinementCycle_OffsetDoesNotAdvanceOnSaturatedCycle pins the second
// adversarial review finding: the round-robin offset used to advance on
// EVERY cycle, including a cycle that broke immediately because the
// scheduler-wide semaphore was still saturated from the previous cycle's
// dispatch. With RefinementMaxConcurrent's default of 1 and a refinement that
// outlives one tick, cycles alternate free/busy/free/busy; advancing the
// offset on the busy cycle too means two ticks (one free, one busy) net back
// to the SAME starting repo with exactly two repos — reproducing the very
// starvation #502 was filed to fix, just on a period of 2 instead of 1.
//
// This drives that alternation directly: cycle 1 dispatches r1 and holds the
// only slot (modelling a refinement that spans the tick boundary), cycle 2
// runs while the slot is still held and must scan nothing, and cycle 3 —
// once the slot is released — must land on r2, not r1 again.
func TestRunRefinementCycle_OffsetDoesNotAdvanceOnSaturatedCycle(t *testing.T) {
	srv := twoRepoUnrefinedIssuesServer(t)
	defer srv.Close()

	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 1

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o1", Name: "r1", Project: 1},
		{Owner: "o2", Name: "r2", Project: 2},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"

	var mu sync.Mutex
	var dispatchedRepo string
	holdSlot := make(chan struct{})
	started := make(chan struct{})
	as.OnRefinementDispatch(func(_, repo string, _ int) {
		mu.Lock()
		dispatchedRepo = repo
		mu.Unlock()
		close(started) // signal BEFORE blocking, so a waiter never races the assignment above
		<-holdSlot
	})
	observed := func() string {
		mu.Lock()
		defer mu.Unlock()
		return dispatchedRepo
	}
	awaitStarted := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("dispatch callback never ran")
		}
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

	// Cycle 1 (free): dispatches r1 and holds its slot, modelling a
	// refinement that has not finished by the next tick. The dispatch itself
	// runs in a goroutine the cycle call does not wait for, so the test must
	// wait on `started` rather than reading dispatchedRepo the instant
	// runRefinementCycle returns.
	as.runRefinementCycle(context.Background())
	awaitStarted()
	firstRepo := observed()
	if firstRepo == "" {
		t.Fatalf("cycle 1 dispatched nothing")
	}

	// Cycle 2 (busy): the slot from cycle 1 is still held, so this cycle must
	// break immediately without dispatching anything — synchronously, since a
	// saturated cycle spawns no goroutine at all.
	mu.Lock()
	dispatchedRepo = ""
	mu.Unlock()
	as.runRefinementCycle(context.Background())
	if got := observed(); got != "" {
		t.Fatalf("cycle 2 dispatched %q while the slot from cycle 1 was still held — the semaphore "+
			"exhaustion check should have stopped scanning entirely", got)
	}

	// Release cycle 1's slot now that the busy cycle has been observed.
	close(holdSlot)
	drainCycle()

	// Cycle 3 (free again): must land on the OTHER repo. If the offset had
	// advanced on cycle 2's no-op scan as well as cycle 1's real dispatch, the
	// two advances would net back to r1 with only two repos in play.
	mu.Lock()
	dispatchedRepo = ""
	mu.Unlock()
	holdSlot = make(chan struct{})
	started = make(chan struct{})
	as.runRefinementCycle(context.Background())
	awaitStarted()
	close(holdSlot)
	drainCycle()
	thirdRepo := observed()
	if thirdRepo == "" {
		t.Fatalf("cycle 3 dispatched nothing")
	}
	if thirdRepo == firstRepo {
		t.Fatalf("cycle 3 dispatched repo %q again (same as cycle 1) — the intervening saturated "+
			"cycle 2 must not have consumed a rotation step, or with two repos the two advances "+
			"alias back to the same starting repo and the slot never circulates", thirdRepo)
	}
}
