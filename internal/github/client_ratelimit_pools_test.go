package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestPoolsDoNotOverwriteEachOther is the regression for the burn root cause:
// the tracker kept ONE slot per user while GitHub bills REST and GraphQL
// against separate budgets, so whichever response landed last won. Core is
// almost always the healthier pool, so in practice a REST reply repeatedly
// erased the GraphQL exhaustion that the gate existed to see.
func TestPoolsDoNotOverwriteEachOther(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	coreReset := time.Now().Add(40 * time.Minute).Unix()
	gqlReset := time.Now().Add(9 * time.Minute).Unix()

	if _, err := tr.SetFromHeaders("alice", ResourceGraphQL, "4", "5000", itoa(gqlReset)); err != nil {
		t.Fatalf("seed graphql: %v", err)
	}
	// The REST reply that used to clobber it.
	if _, err := tr.SetFromHeaders("alice", ResourceCore, "4900", "5000", itoa(coreReset)); err != nil {
		t.Fatalf("seed core: %v", err)
	}

	gql, _, err := tr.Get("alice", ResourceGraphQL)
	if err != nil {
		t.Fatalf("get graphql: %v", err)
	}
	if gql == nil || gql.Remaining != 4 {
		t.Fatalf("graphql remaining = %v, want 4 — a core reply overwrote the graphql slot", gql)
	}
	core, _, err := tr.Get("alice", ResourceCore)
	if err != nil {
		t.Fatalf("get core: %v", err)
	}
	if core == nil || core.Remaining != 4900 {
		t.Fatalf("core remaining = %v, want 4900", core)
	}
	if gql.ResetAt == core.ResetAt {
		t.Error("the two pools share a reset second; they are independent windows")
	}
}

// TestGateReadsItsOwnPool is the behavioural half: with GraphQL exhausted and
// core healthy, a GraphQL call must gate and a REST call must not.
func TestGateReadsItsOwnPool(t *testing.T) {
	t.Setenv(rateLimitFloorEnv, "100")
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	now := time.Now().Unix()
	writeTrackerFile(t, path, map[string]*SharedTrackerEntry{
		"alice|core":    {Remaining: 4900, Limit: 5000, ResetAt: time.Now().Add(40 * time.Minute).Unix(), CheckedAt: now},
		"alice|graphql": {Remaining: 5, Limit: 5000, ResetAt: time.Now().Add(9 * time.Minute).Unix(), CheckedAt: now},
	})
	silent := func(string, ...interface{}) {}

	if _, gated := (headroomGate{tracker: tr, user: "alice", resource: ResourceGraphQL, logger: silent}).resetWait(); !gated {
		t.Error("a GraphQL call did not gate on an exhausted GraphQL pool")
	}
	if _, gated := (headroomGate{tracker: tr, user: "alice", resource: ResourceCore, logger: silent}).resetWait(); gated {
		t.Error("a REST call gated on the GraphQL pool's exhaustion — the pools are independent")
	}
	// A gh subprocess may issue either, so it takes the lower of the two.
	if _, gated := (headroomGate{tracker: tr, user: "alice", resource: "", logger: silent}).resetWait(); !gated {
		t.Error("the gh-subprocess gate ignored the exhausted pool its child might spend")
	}
}

// TestCrossPoolBudgetIgnoresAnElapsedWindow bounds GetBudgetAcrossPools: a
// pool whose window has already turned over says nothing about the budget now
// and must not win the comparison on a Remaining it can no longer justify.
func TestCrossPoolBudgetIgnoresAnElapsedWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	now := time.Now().Unix()
	writeTrackerFile(t, path, map[string]*SharedTrackerEntry{
		"alice|core":    {Remaining: 4900, Limit: 5000, ResetAt: time.Now().Add(40 * time.Minute).Unix(), CheckedAt: now},
		"alice|graphql": {Remaining: 0, Limit: 5000, ResetAt: time.Now().Add(-time.Minute).Unix(), CheckedAt: now},
	})

	entry, _, err := NewSharedRateLimitTracker(path).GetBudgetAcrossPools("alice")
	if err != nil {
		t.Fatalf("GetBudgetAcrossPools: %v", err)
	}
	if entry == nil || entry.Remaining != 4900 {
		t.Fatalf("governing = %v, want the core reading (4900); an elapsed graphql window must not govern", entry)
	}
}

// TestWriteBackUsesGitHubsOwnResourceHeader proves the classification comes
// from X-RateLimit-Resource rather than a guess about the URL.
func TestWriteBackUsesGitHubsOwnResourceHeader(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Resource", "graphql")
		w.Header().Set("X-RateLimit-Remaining", "7")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", itoa(reset))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	c.gateLogger = func(string, ...interface{}) {}

	var q struct{ Viewer struct{ Login string } }
	_ = c.Query(context.Background(), &q, nil)

	gql, _, err := tr.Get("alice", ResourceGraphQL)
	if err != nil {
		t.Fatalf("get graphql: %v", err)
	}
	if gql == nil || gql.Remaining != 7 {
		t.Fatalf("graphql slot = %v, want Remaining 7 from the response's own X-RateLimit-Resource", gql)
	}
	if core, _, _ := tr.Get("alice", ResourceCore); core != nil {
		t.Errorf("a graphql response wrote the core slot too: %+v", core)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// TestCLIClientsCarryTheMachineTracker is the regression for the hole beneath
// both of the gate fixes: WithRateLimitTracker was called in exactly three
// places, all in internal/ipc, so every one-shot CLI process — the whole
// pipeline — ran with a nil tracker. A nil tracker returns at the first line
// of resetWait, so those processes were never gated and never wrote a reading
// back. Fixing the gate's logic changes nothing where the gate is not
// installed, which is why this test guards the wiring itself.
func TestCLIClientsCarryTheMachineTracker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GITHUB_TOKEN", "test-token")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.mu.Lock()
	tracker := c.tracker
	wait := c.rateLimitWaitOnGate
	c.mu.Unlock()

	if tracker == nil {
		t.Fatal("a CLI client has no rate-limit tracker — the gate is not installed on this path")
	}
	if !wait {
		t.Error("a CLI client should wait out a reset rather than hard-fail an in-flight issue")
	}

	want, err := DefaultSharedTrackerPath()
	if err != nil {
		t.Fatalf("DefaultSharedTrackerPath: %v", err)
	}
	if tracker.path != want {
		t.Errorf("tracker path = %q, want the machine-wide %q", tracker.path, want)
	}
}

// TestCLIClientGatesOnTheMachineBudget drives the wiring end to end: a CLI
// client built against a $HOME whose tracker reports an exhausted GraphQL
// pool must not dispatch.
func TestCLIClientGatesOnTheMachineBudget(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("seed home: %v", err)
	}
	writeTrackerFile(t, filepath.Join(home, ".nightgauge", "rate-limit.json"),
		map[string]*SharedTrackerEntry{
			"default|graphql": {Remaining: 0, Limit: 5000,
				ResetAt:   time.Now().Add(15 * time.Minute).Unix(),
				CheckedAt: time.Now().Add(-2 * time.Hour).Unix()},
		})
	t.Setenv("HOME", home)
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv(rateLimitFloorEnv, "100")
	t.Setenv(rateLimitNoWaitEnv, "1") // fail fast; never sleep out a real window

	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.graphqlURL = srv.URL
	c.gateLogger = func(string, ...interface{}) {}

	if _, err := NewRepoService(c).RepoMetadata(context.Background(), "o", "r"); err == nil {
		t.Fatal("a CLI client dispatched on an exhausted GraphQL budget")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected 0 dispatches when gated, got %d", got)
	}
}
