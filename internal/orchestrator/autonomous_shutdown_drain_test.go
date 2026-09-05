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
	if !strings.Contains(out.String(), "draining 1 background op(s) (0 board recovery), up to 10s") {
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
	if !strings.Contains(out.String(), "cancelled 1 still in flight after 50ms — 0 of them mid board write") {
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

// TestShutdown_ReturnsEvenWhenACancelledOpNeverStops pins the SECOND bound.
// The grace only bounds the wait BEFORE the cancel; a cancel is a request, and
// a tracked body that never reads its context never returns. Exactly one such
// body exists in production — fireStatusChangeLocked's emit spawn discards its
// ctx and, wired through SetAutonomousScheduler, calls a listener that blocks
// on a write to stdout — and it is spawned AT teardown, because cancelling the
// run context makes the scheduler complete("cancelled"), which fires the
// status change. Without the post-cancel ceiling, Shutdown parks in
// waitBackground forever and `serve` never exits: the unbounded shutdown the
// grace exists to remove, just relocated past the cancel.
//
// Measured before the fix: Shutdown(50ms) had not returned after 5s (100x the
// grace); the "abandoned" line below did not exist.
func TestShutdown_ReturnsEvenWhenACancelledOpNeverStops(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	entered := make(chan struct{})
	block := make(chan struct{})
	// Deliberately shaped like autonomous.go's emit spawn: the context
	// parameter is discarded, so cancellation is unobservable to this body.
	as.goTracked(func(context.Context) {
		close(entered)
		<-block
	})
	<-entered

	out := withCapturedLog(t)
	done := make(chan struct{})
	go func() { as.Shutdown(50 * time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		// Release before failing, or the parked body outlives the test.
		close(block)
		t.Fatal("Shutdown never returned with one tracked op ignoring its context — the grace " +
			"bounds only the wait before the cancel, so a serve teardown hangs forever on the " +
			"one spawn that discards its ctx")
	}

	if !strings.Contains(out.String(), "abandoned 1 op(s) still parked 50ms after the cancel") {
		t.Errorf("the abandoned op is not named in the log — an operator cannot tell a joined "+
			"shutdown from one that walked away from a goroutine:\n%s", out.String())
	}

	close(block)
	as.waitBackground() // join the released body (and the AfterFunc joiner) before the test ends
}

// TestStatus_ReportsTheBoardTailWithoutPersistingIt pins both halves of the
// operator-visible count: Status must report the live value, and must not write
// it into the state it persists — a count in state.json is stale the instant
// the file is closed, and loadState would resurrect it on the next start.
func TestStatus_ReportsTheBoardTailWithoutPersistingIt(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	entered := make(chan struct{})
	release := make(chan struct{})
	as.goTrackedBoardOp(func(context.Context) {
		close(entered)
		<-release
	})
	<-entered

	if got := as.Status().BoardRecoveryInFlight; got != 1 {
		t.Errorf("Status().BoardRecoveryInFlight = %d with one board op parked, want 1", got)
	}
	if got := as.state.BoardRecoveryInFlight; got != 0 {
		t.Errorf("the persisted state carries BoardRecoveryInFlight = %d — Status stamped the "+
			"snapshot's count onto as.state, so state.json will record a stale tail", got)
	}

	close(release)
	as.waitBackground()

	if got := as.Status().BoardRecoveryInFlight; got != 0 {
		t.Errorf("Status().BoardRecoveryInFlight = %d after the op returned, want 0 — the count "+
			"never falls, so every status read reports a tail that is gone", got)
	}
}

// TestStatus_BoardTailExcludesNonBoardTrackedWork pins the split at the unit
// level: a tracked goroutine that is not a board mutation must not appear in
// the operator's board count, while still being visible to the shutdown that
// has to join it.
func TestStatus_BoardTailExcludesNonBoardTrackedWork(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	entered := make(chan struct{})
	release := make(chan struct{})
	as.goTracked(func(context.Context) {
		close(entered)
		<-release
	})
	<-entered

	if got := as.Status().BoardRecoveryInFlight; got != 0 {
		t.Errorf("Status().BoardRecoveryInFlight = %d with only NON-board tracked work parked, "+
			"want 0 — the operator is told board mutations are pending when none are", got)
	}
	if got := as.BackgroundInFlight(); got != 1 {
		t.Errorf("BackgroundInFlight() = %d, want 1 — the shutdown must still see the op it has "+
			"to join", got)
	}

	close(release)
	as.waitBackground()
}

// TestStatus_BoardTailIsZeroWhileTheRefinementLoopRuns is the finding this
// split exists for, at the production level. RefinementEnabled is the DEFAULT,
// and Run spawns the refinement loop as a tracked goroutine that lives as long
// as the run — so a count taken off the tracked total reports a pending board
// mutation for the entire time autonomous is running, with nothing whatsoever
// pending on the board, and the VSCode line renders it as
// "Board recovery in flight: 1 op(s)".
//
// Measured before the split: the status snapshot reported 1 here.
func TestStatus_BoardTailIsZeroWhileTheRefinementLoopRuns(t *testing.T) {
	cfg := DefaultAutonomousConfig()
	if !cfg.RefinementEnabled {
		t.Fatal("RefinementEnabled is no longer the default — this test measures the default config")
	}
	cfg.RefinementInterval = 10 * time.Millisecond
	// A real (if empty) inner Scheduler: Run() wires its pipeline-complete
	// callback unconditionally, so a nil one panics before the first cycle.
	as := NewAutonomousScheduler(&Scheduler{}, nil, nil, nil, cfg, t.TempDir())
	t.Cleanup(as.drainBackground)
	stubGraphFn(as) // keeps the cycle off the network

	cycled := make(chan struct{}, 1)
	as.onCycleComplete = func() {
		select {
		case cycled <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		_ = as.Run(ctx)
		close(runDone)
	}()

	// Synchronization, not a sleep: Run spawns the refinement loop BEFORE its
	// orphan recovery and initial scan, all on Run's own goroutine — so an
	// observed cycle completion proves the spawn already happened.
	select {
	case <-cycled:
	case <-time.After(15 * time.Second):
		t.Fatal("Run never completed its initial scan cycle — the refinement spawn cannot be assumed to have happened")
	}

	if got := as.BackgroundInFlight(); got == 0 {
		t.Fatal("no tracked goroutine is in flight — the refinement loop was not spawned, so " +
			"this test cannot tell the split from its absence")
	}
	if got := as.Status().BoardRecoveryInFlight; got != 0 {
		t.Errorf("Status().BoardRecoveryInFlight = %d with only the refinement loop running, "+
			"want 0 — the operator-facing count is the tracked TOTAL, so the VSCode report "+
			"says board mutations are still landing whenever autonomous is up", got)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
