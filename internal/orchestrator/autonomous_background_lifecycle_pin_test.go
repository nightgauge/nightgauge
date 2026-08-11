package orchestrator

// Regression pins for the background-goroutine lifecycle (#428).
//
// The production change is invisible to assertions: 22 detached spawns routed
// through goTracked, six board-recovery ops re-parented onto the lifecycle
// generation context, and a cancel-then-join drain. Reverting any of it left
// the whole package green under BOTH `go test` and `go test -race` — measured,
// not assumed. These five pins are what makes the mechanism load-bearing, and
// none of them needs the race detector or a sleep:
//
//	P1 source shape   — no untracked spawn can return to autonomous.go
//	P2 the join       — a spawn that is not counted makes the join return early
//	P3 the cancel     — cancellation actually reaches the in-flight `gh` exec
//	P4 the re-arm     — work spawned after a drain is not born cancelled
//	P5 the generation — a drain releases even with a LIVE Run context
//
// Scope note: P1 covers autonomous.go only. scheduler.go (3 spawns),
// wave_orchestrator.go (4), epic.go (1) and merge_lock.go (1) legitimately hold
// bare spawns today; scheduler.go is owned by queued issues #463/#444/#441, and
// widening this pin belongs to whichever change gives those files a lifecycle.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"
	"time"
)

// TestEveryDetachedSpawnInAutonomousGoesThroughGoTracked is the #428
// resurrection pin. autonomous.go may contain exactly ONE detached-spawn
// construct: the one inside goTracked. Any other is a goroutine that outlives
// whatever it reads, and the repo's plain `go test` gate would not notice its
// return — the leak shows up as a ~25-50% flake in some other test, or not at
// all.
//
// The sanctioned construct is goTracked's `wg.Go(...)` (sync.WaitGroup.Go),
// so the pin recognises both spawn shapes — a `go` statement and a `.Go(...)`
// call — which makes it a strict superset of "every `go` statement".
func TestEveryDetachedSpawnInAutonomousGoesThroughGoTracked(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "autonomous.go", nil, 0)
	if err != nil {
		t.Fatalf("parse autonomous.go: %v", err) // positive control: unreadable/moved file fails
	}

	var tracked *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "goTracked" {
			tracked = fn
		}
		return true
	})
	if tracked == nil {
		t.Fatal("no goTracked func in autonomous.go — the background-goroutine " +
			"lifecycle (#428) was removed; restore it, or amend this pin deliberately")
	}

	within := func(n ast.Node) bool {
		return n.Pos() >= tracked.Pos() && n.End() <= tracked.End()
	}

	inside := 0
	var bare []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			if within(node) {
				inside++
				return true
			}
			bare = append(bare, fset.Position(node.Pos()).String()+" (`go` statement)")
		case *ast.CallExpr:
			// sync.WaitGroup.Go — the spawn goTracked actually uses. Counted the
			// same way as a `go` statement: it detaches a goroutine.
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Go" {
				return true
			}
			if within(node) {
				inside++
				return true
			}
			bare = append(bare, fset.Position(node.Pos()).String()+" (`.Go(...)` spawn)")
		}
		return true
	})

	if inside != 1 { // positive control: zero spawn constructs must FAIL, not pass
		t.Fatalf("goTracked contains %d detached-spawn construct(s), want exactly 1 — this pin can "+
			"no longer tell a tracked spawn from an untracked one", inside)
	}
	for _, pos := range bare {
		t.Errorf("untracked detached spawn at %s — route it through as.goTracked so the "+
			"scheduler can cancel and join it (#428). If this spawn genuinely owns its "+
			"own lifecycle (its own WaitGroup, or a documented process-lifetime "+
			"goroutine), amend this pin in the same commit and say which.", pos)
	}
}

// TestWaitBackgroundBlocksUntilTrackedWorkReturns pins the join itself. A
// spawn that is not counted (bare `go` in place of goTracked, or a gutted
// goTracked that spawns without adding to the generation's WaitGroup) makes
// waitBackground return early — which this test observes deterministically,
// with no -race and no sleeps.
func TestWaitBackgroundBlocksUntilTrackedWorkReturns(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	// Belt: a t.Fatal below must not leave the tracked goroutine parked in the
	// stub, or the constructor's cleanup drain would never return. Deferred
	// funcs run on t.Fatal's Goexit, before any t.Cleanup.
	defer releaseAll()

	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		select { // the stub may be called more than once; announce once
		case entered <- struct{}{}:
		default:
		}
		<-release
		return []byte("[]"), nil
	})

	// Constructed AFTER the stub install so t.Cleanup's LIFO order puts the
	// constructor's backstop drain before the stub restore.
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	addRunning(as, "acme/platform", 900, "awaiting architecture approval")
	as.onPipelineComplete("acme/platform", 900, false, false,
		TerminalKindArchitectureApprovalRequired,
		"ARCHITECTURE APPROVAL REQUIRED — a human must approve this decision")

	waited := make(chan struct{})
	go func() { as.waitBackground(); close(waited) }()

	// Synchronization point, not a timing guess: the tracked goroutine is
	// inside the stub and cannot leave until release is closed, and its Add
	// happened before this goroutine was spawned — so "waited" being closed
	// here is impossible unless the spawn was never counted.
	<-entered
	select {
	case <-waited:
		t.Fatal("waitBackground returned while a tracked goroutine was still " +
			"running — the spawn is not counted (bare `go` instead of goTracked, " +
			"or the generation's WaitGroup lost the Add). This is the #428 defect: such " +
			"a goroutine outlives the test and races the next test's stub restore.")
	default:
	}

	releaseAll()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("waitBackground did not return after the tracked goroutine finished")
	}
	as.drainBackground()
}

// TestDrainBackground_CancelReachesInFlightGhExec pins the CANCEL half of
// cancel-then-join, all the way down to the `gh` subprocess.
//
// It is the one pin that fails if any of three separate things regress: the six
// board-recovery ops re-parenting onto the generation context they were spawned
// under (revert them to context.Background() and the stub's ctx never closes),
// drainBackground calling cancel at all (drop it and the same), and goTracked
// handing the body its generation's context rather than the body re-fetching
// one. Under the fix the drain returns in microseconds; under any of those
// reverts the 5s bound fires. Without it, reverting all six derivations leaked
// 17 real `gh pr list` invocations with the whole package still green.
func TestDrainBackground_CancelReachesInFlightGhExec(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var seen error
	stubReconcileGh(t, func(ctx context.Context, _ ...string) ([]byte, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done() // never returns unless cancellation reaches the exec
		mu.Lock()
		seen = ctx.Err()
		mu.Unlock()
		return nil, ctx.Err()
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.goTracked(func(genCtx context.Context) {
		as.sidelineHalt(genCtx, "O/app", 900, "architecture approval required")
	})
	<-entered

	done := make(chan struct{})
	go func() { as.drainBackground(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainBackground did not cancel the in-flight gh exec — board-recovery " +
			"ops are not deriving from the lifecycle context handed to their tracked " +
			"closure, or the drain no longer cancels before it joins")
	}

	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(seen, context.Canceled) {
		t.Fatalf("gh exec saw ctx.Err() = %v, want context.Canceled", seen)
	}
}

// TestDrainBackground_ReArmsLifecycleContext pins the re-arm: a drain retires
// its generation, so the NEXT spawn must get a live successor rather than the
// cancelled context it just abandoned. Drop the retire and every post-drain
// board-recovery op is born cancelled — invisible to every other test in the
// package, because each op bails early on "no project config" whether its
// context is alive or dead.
//
// The join here is waitBackground, not a second drainBackground: a drain
// cancels before it waits, so it would cancel the very context the spawned body
// is reading and this pin would race itself. waitBackground is the join-only
// counterpart, and WaitGroup.Wait is the happens-before edge that makes the
// write below safe to read.
//
// The first generation is forced into existence deliberately. On a virgin
// scheduler the lifecycle is lazy — bgCtx is nil — so a drain has nothing to
// retire and a mutant that never retires anything is indistinguishable from the
// fix. Measured: without this line the no-re-arm mutant SURVIVES.
func TestDrainBackground_ReArmsLifecycleContext(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	if err := as.backgroundContext().Err(); err != nil {
		t.Fatalf("a freshly created lifecycle generation is already cancelled: %v", err)
	}
	as.drainBackground() // retires (and cancels) generation 1

	var err error
	as.goTracked(func(genCtx context.Context) { err = genCtx.Err() })
	as.waitBackground()

	if err != nil {
		t.Fatalf("work spawned after a drain was born cancelled: %v — drainBackground must "+
			"retire the cancelled generation so its successor is live", err)
	}
	as.drainBackground()
}

// TestRun_RefinementLoopReleasesDrain pins the generation contract on the one
// tracked body that loops instead of running to completion. The refinement loop
// carries the RUN context, which a drain cannot reach; its lifecycle arm is the
// generation context goTracked counted it under. Remove that arm and the loop
// watches only the (deliberately still-live) run context and stopRefinementCh,
// so the drain's join never returns.
//
// The run context staying LIVE across the drain is the whole point of the
// shape: cancelling it first would make the test pass with no arm at all.
func TestRun_RefinementLoopReleasesDrain(t *testing.T) {
	cfg := DefaultAutonomousConfig()
	cfg.RefinementEnabled = true
	cfg.RefinementInterval = 10 * time.Millisecond
	// A real (if empty) inner Scheduler: Run() wires its pipeline-complete
	// callback unconditionally, so a nil one panics before the first cycle.
	as := NewAutonomousScheduler(&Scheduler{}, nil, nil, nil, cfg, t.TempDir())
	t.Cleanup(as.drainBackground) // backstop for the successor generation
	stubGraphFn(as)               // keeps the cycle off the network

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

	drained := make(chan struct{})
	go func() { as.drainBackground(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("drainBackground blocked on the refinement loop — it needs a lifecycle-cancel " +
			"arm on the generation context it was spawned under (a Run-context-only loop is " +
			"unreachable by cancelBackground)")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
