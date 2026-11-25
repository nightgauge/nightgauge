package ipc

import (
	"os"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Fixture issue 799: a dead run with no terminal claim is retired, and the reload gate opens.
func TestRetireDeadRun_DeadEntryIsRetiredAndReloadBecomesSafe(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	deadID := newTestRunID()
	otherID := newTestRunID()
	installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 799, "", deadID), now.Add(-2*livenessWindow))
	installRegistryEntry(t, s, state.NewRuntimeState("acme/other", 799, "", otherID), now.Add(-2*livenessWindow))

	got, live := s.RetireDeadRun("acme/platform", 799, "issue closed on forge", now)
	if len(got) != 1 || got[0] != deadID || live != 0 {
		t.Fatalf("retired = %v live = %d, want [%s] 0", got, live, deadID)
	}
	res := s.RunningPipelinesSnapshot(now)
	if res.Count != 1 || res.Runs[0].RunID != otherID {
		t.Fatalf("a different repo's same-numbered run must stay counted: %+v", res)
	}
	if got, _ := s.RetireDeadRun("other", 799, "x", now); len(got) != 1 || got[0] != otherID {
		t.Fatalf("name-only slug: retired = %v, want [%s]", got, otherID)
	}
	if res := s.RunningPipelinesSnapshot(now); res.Count != 0 || !res.ReloadSafe {
		t.Fatalf("after retiring both: Count=%d ReloadSafe=%v, want 0/true", res.Count, res.ReloadSafe)
	}
}

func TestRetireDeadRun_ZeroLeaseIsRetired(t *testing.T) {
	s, _, _ := reconcileServer(t)
	id := newTestRunID()
	installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 5, "", id), time.Time{})
	if got, _ := s.RetireDeadRun("acme/platform", 5, "x", time.Now()); len(got) != 1 {
		t.Fatalf("an administrative install with no lease must be retirable, got %v", got)
	}
}

func TestRetireDeadRun_FreshLeaseIsKept(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 800, "", newTestRunID()), now.Add(-time.Minute))
	if got, live := s.RetireDeadRun("acme/platform", 800, "x", now); len(got) != 0 || live != 1 {
		t.Fatalf("a fresh lease must keep the entry: retired %v live %d", got, live)
	}
	if res := s.RunningPipelinesSnapshot(now); res.Count != 1 || res.ReloadSafe {
		t.Fatalf("fresh entry must stay counted: %+v", res)
	}
}

func TestRetireDeadRun_LiveChildIsKept(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	rt := state.NewRuntimeState("acme/platform", 801, "", newTestRunID())
	rt.SetStageChild(os.Getpid())
	installRegistryEntry(t, s, rt, now.Add(-2*livenessWindow))
	if got, live := s.RetireDeadRun("acme/platform", 801, "x", now); len(got) != 0 || live != 1 {
		t.Fatalf("a live recorded child must keep the entry, retired %v", got)
	}
}

func TestRetireDeadRun_ProgressTouchUnretires(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	e := installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 802, "", newTestRunID()), now.Add(-2*livenessWindow))
	id := e.rs.Snapshot().RunID
	if got, _ := s.RetireDeadRun("acme/platform", 802, "x", now); len(got) != 1 {
		t.Fatalf("want retired, got %v", got)
	}
	s.runtimesMu.Lock()
	e.touchLocked(verbRunProgress)
	retired, abandonedAt := e.retired, e.abandonedAt
	s.runtimesMu.Unlock()
	if retired || !abandonedAt.IsZero() {
		t.Fatalf("touch must clear retired and abandonedAt: %v %v", retired, abandonedAt)
	}
	if res := s.RunningPipelinesSnapshot(time.Now()); res.Count != 1 {
		t.Fatalf("run traffic after retirement must count the run again: %+v", res)
	}
	later := time.Now().Add(2 * livenessWindow)
	if got, _ := s.RetireDeadRun("acme/platform", 802, "x", later); len(got) != 1 || got[0] != id {
		t.Fatalf("a re-aged lease must re-retire: %v", got)
	}
}

func TestRetireDeadRun_DurableAbandonmentIsNotUndone(t *testing.T) {
	s, _, _ := reconcileServer(t)
	e := installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 803, "", newTestRunID()), time.Now())
	s.runtimesMu.Lock()
	e.abandoned = true
	e.touchLocked(verbRunProgress)
	ab := e.abandoned
	s.runtimesMu.Unlock()
	if !ab {
		t.Fatal("touchLocked must not clear an abandonment RetireDeadRun did not latch")
	}
}

func TestSameRepo(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"acme/x", "ACME/X", true},
		{"acme/x", "x", true},
		{"acme/x", "other/x", false},
		{"acme/x", "y", false},
		{"", "acme/x", true},
		{"x", "y", false},
		{"x", "X", true},
		{"x", "acme/x", true},
	}
	for _, c := range cases {
		if got := sameRepo(c.a, c.b); got != c.want {
			t.Errorf("sameRepo(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

// #3567 review: a closure first reported inside the lease window is refused;
// re-asked after the lease ages out, the same entry is retired.
func TestRetireDeadRun_RefusedOnFreshLeaseRetiresOnceAged(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	id := newTestRunID()
	installRegistryEntry(t, s, state.NewRuntimeState("acme/platform", 805, "", id), now.Add(-time.Minute))
	if got, _ := s.RetireDeadRun("acme/platform", 805, "issue closed on forge", now); len(got) != 0 {
		t.Fatalf("fresh lease must refuse, retired %v", got)
	}
	later := now.Add(2 * livenessWindow)
	got, _ := s.RetireDeadRun("acme/platform", 805, "issue closed on forge", later)
	if len(got) != 1 || got[0] != id {
		t.Fatalf("aged lease: retired = %v, want [%s]", got, id)
	}
	if res := s.RunningPipelinesSnapshot(later); res.Count != 0 || !res.ReloadSafe {
		t.Fatalf("after retire: %+v", res)
	}
}

// An entry with no repo of its own falls back to its snapshot's repo.
func TestRetireDeadRun_EmptyEntryRepoUsesSnapshotRepo(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	rt := state.NewRuntimeState("acme/snap", 806, "", newTestRunID())
	e := newRunEntry(rt, "", 806)
	e.lastSeen = now.Add(-2 * livenessWindow)
	s.runtimesMu.Lock()
	s.activeRuntimes[rt.RunID] = e
	s.runtimesMu.Unlock()
	if got, _ := s.RetireDeadRun("acme/other", 806, "x", now); len(got) != 0 {
		t.Fatalf("a mismatched repo must skip, retired %v", got)
	}
	if got, _ := s.RetireDeadRun("acme/snap", 806, "x", now); len(got) != 1 {
		t.Fatalf("the snapshot repo must match, retired %v", got)
	}
}

func TestLiveRunIssues_ExcludesTerminalAndRetired(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	installRegistryEntry(t, s, state.NewRuntimeState("acme/app", 1, "", newTestRunID()), now)
	installRegistryEntry(t, s, state.NewRuntimeState("acme/app", 1, "", newTestRunID()), now)
	term := installRegistryEntry(t, s, state.NewRuntimeState("acme/app", 2, "", newTestRunID()), now)
	installRegistryEntry(t, s, state.NewRuntimeState("acme/app", 3, "", newTestRunID()), time.Time{})
	s.runtimesMu.Lock()
	term.terminal = true
	s.runtimesMu.Unlock()
	if got, _ := s.RetireDeadRun("acme/app", 3, "x", now); len(got) != 1 {
		t.Fatalf("setup: retire #3: %v", got)
	}
	got := s.LiveRunIssues()
	if len(got) != 1 || got[0] != (orchestrator.RunIssue{Repo: "acme/app", Number: 1}) {
		t.Fatalf("LiveRunIssues = %+v, want only acme/app#1 once", got)
	}
}
