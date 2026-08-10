package ipc

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// Registry reaping (ADR-017 7.2, step-4's accepted advisory). Adoption has no
// expiry of its own, so an adopt-empty entry holds its key for the life of the
// process — unbounded growth in a long-lived server, and every one of those
// entries answers ladder arm 1 for a run nobody is running.

func TestRunIdentity_StaleRegistryEntriesAreReaped(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()

	staleID := newTestRunID()
	freshID := newTestRunID()
	terminalID := newTestRunID()

	installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 600, "", staleID), now.Add(-2*livenessWindow))
	installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 601, "", freshID), now.Add(-time.Minute))
	terminal := installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 602, "", terminalID), now.Add(-2*livenessWindow))
	s.runtimesMu.Lock()
	terminal.terminal = true
	s.runtimesMu.Unlock()

	s.reapStaleRunEntries(now)

	s.runtimesMu.Lock()
	_, staleLeft := s.activeRuntimes[staleID]
	_, freshLeft := s.activeRuntimes[freshID]
	_, terminalLeft := s.activeRuntimes[terminalID]
	// A reaped id is NOT closed: the ring means "a terminal claim has run", and
	// a run that later proves alive must be able to re-adopt from its snapshot
	// and book honestly (C13's bias).
	closed := s.closedRuns.hasLocked(staleID)
	s.runtimesMu.Unlock()

	if staleLeft {
		t.Error("an entry with no lease, no scheduler and no live process must be reaped")
	}
	if !freshLeft {
		t.Error("an entry whose lease is inside the window must survive")
	}
	if !terminalLeft {
		t.Error("a terminal-latched entry is the claim's business, not the reaper's")
	}
	if closed {
		t.Error("a reaped id must not enter closedRuns — that would refuse the run's own honest re-adoption")
	}
}

// An adopt-empty entry carries the ZERO lastSeen deliberately (an administrative
// verb may state a run's state, never that the run is alive), so the reaper
// falls back to firstSeen — otherwise every freshly installed administrative
// entry would be reaped on the very next pass.
func TestRunIdentity_ReapingFallsBackToFirstSeen(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()

	justInstalled := newTestRunID()
	e := newRunEntry(state.NewRuntimeState("acme/platform", 610, "", justInstalled), "acme/platform", 610)
	e.firstSeen = now.Add(-time.Minute)
	e.lastSeen = time.Time{} // administrative install
	s.runtimesMu.Lock()
	s.activeRuntimes[justInstalled] = e
	s.runtimesMu.Unlock()

	s.reapStaleRunEntries(now)

	s.runtimesMu.Lock()
	_, left := s.activeRuntimes[justInstalled]
	s.runtimesMu.Unlock()
	if !left {
		t.Fatal("an entry installed a minute ago must not be reaped for having a zero lease")
	}
}

// The two arms that outrank a dead lease, each on its own.
func TestRunIdentity_ReapingHonoursTheSchedulerAndTheProcess(t *testing.T) {
	now := time.Now()

	t.Run("scheduler registry", func(t *testing.T) {
		s, _, _ := reconcileServer(t)
		runID := newTestRunID()
		rt := state.NewRuntimeState("acme/platform", 620, "", runID)
		installRegistryEntry(t, s, rt, now.Add(-2*livenessWindow))
		sched := newFakeSchedulerRuns()
		sched.register(620, rt)
		s.schedulerRuns = sched

		s.reapStaleRunEntries(now)

		s.runtimesMu.Lock()
		_, left := s.activeRuntimes[runID]
		s.runtimesMu.Unlock()
		if !left {
			t.Fatal("a run the scheduler is executing must not be reaped out of the IPC registry")
		}
	})

	t.Run("live stage child", func(t *testing.T) {
		s, _, _ := reconcileServer(t)
		runID := newTestRunID()
		rt := state.NewRuntimeState("acme/platform", 621, "", runID)
		rt.SetStageChild(os.Getpid())
		installRegistryEntry(t, s, rt, now.Add(-2*livenessWindow))

		s.reapStaleRunEntries(now)

		s.runtimesMu.Lock()
		_, left := s.activeRuntimes[runID]
		s.runtimesMu.Unlock()
		if !left {
			t.Fatal("a run whose stage child is alive must not be reaped")
		}
	})
}

// J.5: workspace.setRoot writes s.workspaceRoot from a handler goroutine while
// the deferred sweep reads it from the timer's. The reconcile was synchronous in
// the same handler until step 5, so this race did not exist; the goroutine-borne
// sweep introduces it, and -race is what proves the fix. Run this file with
// -race or it asserts nothing.
func TestOrphanReconcile_WorkspaceRootIsSafeUnderAConcurrentSweep(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(rootA))
	setRoot := s.methods["workspace.setRoot"]

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			root := rootA
			if i%2 == 0 {
				root = rootB
			}
			if _, err := setRoot(t.Context(), []byte(`{"root":"`+root+`"}`)); err != nil {
				t.Errorf("workspace.setRoot: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			s.reconcileOrphanedRuns()
			_ = s.pipelineStateScanRoots()
		}
	}()
	wg.Wait()
}
