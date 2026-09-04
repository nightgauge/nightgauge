package ipc

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// staysRunning asserts the dispatch loop is still alive after a settle window.
// The negative of waitRunning: coming up is not the property under test here,
// staying up is.
func staysRunning(t *testing.T, isRunning func() bool, window time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if !isRunning() {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestResumeVerbSurvivesTheVerbScopedDeadline is the #1425 follow-up wire pin.
//
// Store.Resolve now runs a verb under its own deadline, because the verb
// executes inside the store's cross-process critical section and an unbounded
// hold defeats the bounded wait (attention.verbTimeout). That deadline is
// request-scoped, and `context.WithTimeout`'s cancel runs the instant the verb
// returns — so anything the verb SPAWNS and leaves running must not be tied to
// the context the verb was handed.
//
// The autonomous resume verb spawns exactly that: the fleet dispatch loop. It
// used to hand the loop the verb's own context, which was harmless only
// because that context happened to be the server-lifetime one. Under the
// store's ceiling the same line kills the loop microseconds after the operator
// clicks Resume — the fleet reporting "running" while nothing dispatches,
// which is the silent dead state #3303 and #405 exist to prevent, reintroduced
// through a context lifetime instead of a status flag.
//
// The test cancels the verb context exactly where Store.Resolve does: right
// after ExecuteVerb returns.
func TestResumeVerbSurvivesTheVerbScopedDeadline(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())

	// The store's shape: a bounded, cancelled-on-return context per verb.
	verbCtx, cancelVerb := context.WithTimeout(ctx, 20*time.Second)
	err := server.ExecuteVerb(verbCtx, &attention.DecisionRequest{},
		attention.Option{ID: "resume", Verb: attention.VerbAutonomousResume})
	cancelVerb()
	if err != nil {
		t.Fatalf("ExecuteVerb(resume): %v", err)
	}

	waitRunning(t, as)
	staysRunning(t, as.IsRunning, 300*time.Millisecond,
		"the dispatch loop died with the verb's context — a card Resume leaves the fleet reporting "+
			"running while nothing dispatches (#1425 follow-up; the spawn must detach, see detachedRunCtx)")
}

// TestRepoResumeVerbSurvivesTheVerbScopedDeadline covers the repo-scoped twin
// (#1148), which spawns the same loop from a different arm. Two call sites,
// two pins: fixing one and not the other is the failure mode a single test
// would miss.
func TestRepoResumeVerbSurvivesTheVerbScopedDeadline(t *testing.T) {
	state := haltedState()
	state.Status = "running"
	state.MachineHalt = nil
	state.PausedRepos = map[string]*orchestrator.RepoPauseRecord{
		"octocat/acme": {Repo: "octocat/acme", TriggeredBy: "haltQueueOnSlotFailure", Issue: 1148},
	}
	server, as, ctx := newHaltedServer(t, state)

	req := &attention.DecisionRequest{Context: attention.Context{Repo: "octocat/acme", Issue: 1148}}
	opt := attention.Option{
		ID:   "retry",
		Verb: attention.VerbAutonomousClearIssueFailures,
		Args: map[string]any{"key": "octocat/acme#1148", "then": "autonomous.resumeRepo"},
	}
	verbCtx, cancelVerb := context.WithTimeout(ctx, 20*time.Second)
	err := server.ExecuteVerb(verbCtx, req, opt)
	cancelVerb()
	if err != nil {
		t.Fatalf("ExecuteVerb(resumeRepo): %v", err)
	}

	waitRunning(t, as)
	staysRunning(t, as.IsRunning, 300*time.Millisecond,
		"the repo-scoped resume's dispatch loop died with the verb's context (#1425 follow-up)")
}

// TestDetachedRunCtxShedsCancellationAndKeepsValues pins the helper's contract
// directly, so a regression in it is named at the helper rather than only
// showing up as a mysteriously dead fleet two tests above.
func TestDetachedRunCtxShedsCancellationAndKeepsValues(t *testing.T) {
	type ctxKey string
	const key ctxKey = "workspace"

	parent, cancel := context.WithCancel(context.WithValue(context.Background(), key, "acme"))
	detached := detachedRunCtx(parent)
	cancel()

	select {
	case <-detached.Done():
		t.Fatal("detachedRunCtx propagated the caller's cancellation — a long-lived spawn would die with the request that started it")
	case <-time.After(50 * time.Millisecond):
	}
	if got := detached.Value(key); got != "acme" {
		t.Fatalf("detachedRunCtx dropped context values: %v — the spawn loses request-scoped configuration", got)
	}
}
