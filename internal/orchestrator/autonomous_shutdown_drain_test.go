package orchestrator

// Shutdown's grace-then-cancel semantics (#489).
//
// The wiring — Server.Run's teardown calling Shutdown at all — is pinned in
// internal/ipc (TestServerRun_DrainsBackgroundOnExit), which necessarily uses
// the production BackgroundDrainGrace. The two ARMS of the grace live here,
// where the duration is a parameter: a 30-second expiry is not something a
// unit test can wait for, and "it eventually cancels" is exactly the half a
// wiring test cannot see.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestShutdown_WaitsForWorkThatFinishesInsideTheGrace pins the wait: work that
// completes before the grace expires must COMPLETE, not be aborted. This is the
// whole reason shutdown has a grace period rather than cancelling outright —
// an aborted MoveStatus leaves an issue stuck "In progress".
func TestShutdown_WaitsForWorkThatFinishesInsideTheGrace(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	entered := make(chan struct{})
	release := make(chan struct{})
	var finishedCleanly bool
	as.goTracked(func(genCtx context.Context) {
		close(entered)
		select {
		case <-release:
			finishedCleanly = true
		case <-genCtx.Done():
		}
	})
	<-entered

	if n := as.BackgroundInFlight(); n != 1 {
		t.Fatalf("BackgroundInFlight() = %d with one op parked, want 1 — the count cannot "+
			"distinguish a shutdown with a tail from a quiet one", n)
	}

	out := withCapturedLog(t)
	done := make(chan struct{})
	go func() { as.Shutdown(10 * time.Second); close(done) }()

	// The op cannot finish until this test releases it, so a Shutdown that has
	// already returned is one that never joined.
	select {
	case <-done:
		t.Error("Shutdown returned while a tracked op was still in flight — it does not join")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown did not return after the tracked op finished")
	}

	// WaitGroup.Wait inside Shutdown is the happens-before edge for this read.
	if !finishedCleanly {
		t.Error("the tracked op was cancelled rather than allowed to finish — Shutdown cancels " +
			"inside its own grace period, which is the board write the grace exists to save")
	}
	if n := as.BackgroundInFlight(); n != 0 {
		t.Errorf("BackgroundInFlight() = %d after Shutdown returned, want 0", n)
	}
	if !strings.Contains(out.String(), "draining 1 background op(s), up to 10s") {
		t.Errorf("no start line naming the tail and the bound:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "drained") {
		t.Errorf("no completion line for a drain that completed:\n%s", out.String())
	}
}

// TestShutdown_CancelsWorkThatOutlastsTheGrace pins the bound. Without it a
// shutdown inherits boardRecoveryTimeout's 80-minute ceiling — the very ceiling
// that makes these ops survive a rate-limit pause — and `nightgauge serve`
// hangs for over an hour on a SIGTERM.
//
// The op here only ever exits on cancellation, so Shutdown returning at all is
// the assertion: under a mutant that waits without cancelling, the 15s bound
// below fires.
func TestShutdown_CancelsWorkThatOutlastsTheGrace(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	entered := make(chan struct{})
	var mu sync.Mutex
	var seen error
	as.goTracked(func(genCtx context.Context) {
		close(entered)
		<-genCtx.Done() // never returns unless the shutdown cancels
		mu.Lock()
		seen = genCtx.Err()
		mu.Unlock()
	})
	<-entered

	out := withCapturedLog(t)
	done := make(chan struct{})
	go func() { as.Shutdown(50 * time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown never returned: the grace period does not cancel, so a serve exit " +
			"waits on boardRecoveryTimeout's 80-minute ceiling")
	}

	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(seen, context.Canceled) {
		t.Errorf("the tracked op saw ctx.Err() = %v, want context.Canceled — Shutdown returned "+
			"without cancelling, abandoning the goroutine instead of joining it", seen)
	}
	if !strings.Contains(out.String(), "cancelled 1 still in flight after 50ms") {
		t.Errorf("the abandoned tail is not named in the log — an operator cannot tell a clean "+
			"drain from a cancelled one:\n%s", out.String())
	}
}

// TestShutdown_OnAQuietSchedulerSaysSo pins the third outcome. A shutdown with
// nothing detached must still leave a line, or "no drain line in the log" is
// ambiguous between a quiet exit and a teardown that never reached the drain.
func TestShutdown_OnAQuietSchedulerSaysSo(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	out := withCapturedLog(t)
	as.Shutdown(BackgroundDrainGrace)
	if !strings.Contains(out.String(), "no background ops in flight") {
		t.Errorf("a quiet shutdown said nothing:\n%s", out.String())
	}
}

// TestStatus_ReportsTheBackgroundTailWithoutPersistingIt pins both halves of the
// operator-visible count: Status must report the live value, and must not write
// it into the state it persists — a count in state.json is stale the instant
// the file is closed, and loadState would resurrect it on the next start.
func TestStatus_ReportsTheBackgroundTailWithoutPersistingIt(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	entered := make(chan struct{})
	release := make(chan struct{})
	as.goTracked(func(context.Context) {
		close(entered)
		<-release
	})
	<-entered

	if got := as.Status().BackgroundInFlight; got != 1 {
		t.Errorf("Status().BackgroundInFlight = %d with one op parked, want 1", got)
	}
	if got := as.state.BackgroundInFlight; got != 0 {
		t.Errorf("the persisted state carries BackgroundInFlight = %d — Status stamped the "+
			"snapshot's count onto as.state, so state.json will record a stale tail", got)
	}

	close(release)
	as.waitBackground()

	if got := as.Status().BackgroundInFlight; got != 0 {
		t.Errorf("Status().BackgroundInFlight = %d after the op returned, want 0 — the count "+
			"never falls, so every status read reports a tail that is gone", got)
	}
}
