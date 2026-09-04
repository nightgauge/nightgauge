package attention

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// legacySharedTempPath is the staging name #1425 removed: one fixed
// `<target>.tmp` that EVERY writer of that target opened. Reconstructed here,
// literally, so these tests fail the moment a call site goes back to it.
func legacySharedTempPath(target string) string { return target + ".tmp" }

// sentinel is the payload parked at the legacy name. Nothing may touch it.
var sentinel = []byte("a concurrent writer's in-flight bytes — not yours (#1425)\n")

// occupyLegacyName parks the sentinel at target's legacy shared staging path
// and returns a check that the file is still there, byte for byte.
//
// WHY OCCUPY THE NAME RATHER THAN RACE TWO WRITERS. The property #1425's second
// half provides is "no two writers ever share a staging file". Demonstrating it
// by actually racing two writers and waiting for torn bytes is a test that
// passes vacuously whenever the race does not happen to land — the cannot-go-red
// shape docs/FAILURE_TAXONOMY.md warns about, and a poor guard for a defect that
// only ever surfaces under load. Occupying the shared name tests the same
// property deterministically and from the writer's own side: a writer that
// stages at a per-writer path cannot observe or disturb this file, while a
// writer that stages at `<target>.tmp` truncates it and then renames it away.
func occupyLegacyName(t *testing.T, target string) func() {
	t.Helper()
	legacy := legacySharedTempPath(target)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("create dir for %s: %v", legacy, err)
	}
	if err := os.WriteFile(legacy, sentinel, 0o644); err != nil {
		t.Fatalf("park sentinel at %s: %v", legacy, err)
	}
	return func() {
		t.Helper()
		got, err := os.ReadFile(legacy)
		if os.IsNotExist(err) {
			t.Fatalf("the writer consumed the shared staging path %s — it staged at the fixed `<target>.tmp` name, "+
				"so a second writer's in-flight bytes are renamed into place as this writer's card (#1425)", legacy)
		}
		if err != nil {
			t.Fatalf("read sentinel at %s: %v", legacy, err)
		}
		if !bytes.Equal(got, sentinel) {
			t.Fatalf("the writer overwrote the shared staging path %s — it staged at the fixed `<target>.tmp` name, "+
				"so two writers truncate each other mid-write and publish a mix (#1425)", legacy)
		}
	}
}

// TestMaterializedWriteStagesOffTheSharedName pins the per-writer staging path
// at its production call site, writeMaterializedLocked.
//
// TestMaterializedTempPathIsUniquePerWriter exercises the materializedTempPath
// helper in isolation, which pins the helper and nothing else: restoring
// `tmp := path + ".tmp"` at this call site leaves the helper — and that test —
// perfectly happy. This test goes through the public API, so the call site is
// what is under test.
func TestMaterializedWriteStagesOffTheSharedName(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	id := mustID(t)
	if _, _, err := s.Raise(validRequest(id, "cond:staging")); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	card, err := s.pathFor(id)
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}

	// Every mutating write of a materialized card, not just the first: the
	// staging path is chosen once per write, so each transition is its own
	// opportunity to regress.
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"acknowledge", func() error { _, err := s.Acknowledge(id, "operator"); return err }},
		{"resolve", func() error {
			_, err := s.Resolve(context.Background(), id, "go", "operator", "", "", NoopExecutor{})
			return err
		}},
	} {
		t.Run(step.name, func(t *testing.T) {
			check := occupyLegacyName(t, card)
			if err := step.run(); err != nil {
				t.Fatalf("%s: %v", step.name, err)
			}
			check()
			// And the card itself is still whole — the write really happened
			// somewhere, so this is not passing because nothing wrote at all.
			if _, err := readRequest(card); err != nil {
				t.Fatalf("card unreadable after %s: %v", step.name, err)
			}
		})
	}
}

// TestStreakWriteStagesOffTheSharedName pins the same property at the other
// production call site, saveStreaksLocked. The counts file has exactly one
// path for the whole store, so every streak writer in every process targets
// it — the shared-staging-name blast radius is wider here than for cards, not
// narrower.
func TestStreakWriteStagesOffTheSharedName(t *testing.T) {
	s := New(t.TempDir())
	check := occupyLegacyName(t, s.streakPath())

	if n, err := s.IncrementStreak("cond:staging"); err != nil || n != 1 {
		t.Fatalf("IncrementStreak = %d, %v; want 1, nil", n, err)
	}
	check()
	if got := s.StreakCount("cond:staging"); got != 1 {
		t.Fatalf("streak count = %d after the write; want 1 — the counts file did not survive", got)
	}

	check = occupyLegacyName(t, s.streakPath())
	if err := s.ResetStreak("cond:staging"); err != nil {
		t.Fatalf("ResetStreak: %v", err)
	}
	check()
	if got := s.StreakCount("cond:staging"); got != 0 {
		t.Fatalf("streak count = %d after reset; want 0", got)
	}
}

// blockingExecutor is a verb that waits for its context and reports what it
// saw. It is the honest shape of every remote verb the daemon registers: work
// that ends when the context says so.
type blockingExecutor struct {
	hadDeadline bool
	ctxDone     bool
	elapsed     time.Duration
}

func (e *blockingExecutor) ExecuteVerb(ctx context.Context, _ *DecisionRequest, _ Option) error {
	start := time.Now()
	_, e.hadDeadline = ctx.Deadline()
	select {
	case <-ctx.Done():
		e.ctxDone = true
	case <-time.After(5 * time.Second):
		// The escape hatch exists so a regression fails the test instead of
		// hanging the package until the go test timeout.
	}
	e.elapsed = time.Since(start)
	return nil
}

// TestResolveBoundsTheVerbInsideTheLock is the #1425 follow-up regression.
//
// Resolve holds the cross-process flock across the verb, and the wait for that
// flock is bounded (flockTimeout). Before this fix the HOLD was not bounded at
// all: the daemon's issue.close verb runs on the server-lifetime context and
// the GitHub client sleeps through a fully exhausted rate limit for up to 75
// minutes. Every other producer's wait then expires, each takes the fail-open
// branch, and the interleave TestStoreSerialisesMutationsAcrossProcesses guards
// is back — during a GitHub outage, which is when the Action Center is busiest.
func TestResolveBoundsTheVerbInsideTheLock(t *testing.T) {
	prev := verbTimeout
	verbTimeout = 150 * time.Millisecond
	t.Cleanup(func() { verbTimeout = prev })

	s := New(t.TempDir())
	id := mustID(t)
	if _, _, err := s.Raise(validRequest(id, "cond:verbbound")); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	exec := &blockingExecutor{}
	if _, err := s.Resolve(context.Background(), id, "go", "operator", "", "", exec); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !exec.hadDeadline {
		t.Fatalf("the verb ran on a context with no deadline — its duration is the store's cross-process " +
			"hold time, so an unbounded verb pushes every other producer past flockTimeout and back onto " +
			"the fail-open branch (#1425)")
	}
	if !exec.ctxDone {
		t.Fatalf("the verb was never cut off (ran %v) — the deadline is not actually reaching it", exec.elapsed)
	}
	if exec.elapsed > time.Second {
		t.Fatalf("the verb ran %v under a %v ceiling — the hold is not bounded by verbTimeout", exec.elapsed, verbTimeout)
	}
}

// TestVerbTimeoutKeepsTheHoldBelowTheWait pins the relationship the two
// constants exist to express. A bounded wait over an unbounded (or merely
// longer) hold is not a lock: raise the verb ceiling to or past flockTimeout
// and a single legitimately slow resolve makes every other producer's wait
// expire, which is the fail-open path, not the "wedged holder" the flockTimeout
// comment promises expiry means.
func TestVerbTimeoutKeepsTheHoldBelowTheWait(t *testing.T) {
	if verbTimeout >= flockTimeout {
		t.Fatalf("verbTimeout %v >= flockTimeout %v: a resolve may legitimately hold the cross-process lock "+
			"for longer than another process is willing to wait for it, so waiters take the fail-open branch "+
			"during normal operation and the #1425 serialisation guarantee lapses", verbTimeout, flockTimeout)
	}
}

// TestSweepRunsVerbsOutsideTheLock guards the other shape of the same rule.
// SweepExpired collects its transitions under the lock and executes default
// verbs after releasing, so its verbs need no ceiling — and must not acquire
// one by being moved back inside the section.
func TestSweepRunsVerbsOutsideTheLock(t *testing.T) {
	s := New(t.TempDir())
	id := mustID(t)
	req := validRequest(id, "cond:sweepbound")
	req.ExpiresAt = time.Now().UTC().Add(-time.Hour).Format(tsLayout)
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	// If the sweep held the lock across its verb, this acquire would block for
	// the whole verb; because it does not, the verb observes a free store.
	var lockWasFree bool
	exec := verbFunc(func(context.Context, *DecisionRequest, Option) error {
		done := make(chan struct{})
		go func() {
			release := acquireDir(s.dir)
			release()
			close(done)
		}()
		select {
		case <-done:
			lockWasFree = true
		case <-time.After(2 * time.Second):
		}
		return nil
	})
	if _, err := s.SweepExpired(context.Background(), exec); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if !lockWasFree {
		t.Fatalf("SweepExpired executed its default verb while still holding the store lock — sweep verbs are " +
			"unbounded by design and belong outside the critical section (#1425)")
	}
}

// verbFunc adapts a func to VerbExecutor.
type verbFunc func(context.Context, *DecisionRequest, Option) error

func (f verbFunc) ExecuteVerb(ctx context.Context, req *DecisionRequest, opt Option) error {
	return f(ctx, req, opt)
}
