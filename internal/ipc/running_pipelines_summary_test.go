package ipc

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// RunningPipelinesSnapshot answers "is any pipeline running in this window?" —
// the question that decides whether a VS Code reload is safe (#1511).
//
// The failure it exists to prevent is a confident "0 running; safe to reload"
// over live work, so every assertion below is about NOT under-reporting.

func installRun(t *testing.T, s *Server, repo string, issue int, lastSeen time.Time) string {
	t.Helper()
	id := newTestRunID()
	e := newRunEntry(state.NewRuntimeState(repo, issue, "", id), repo, issue)
	e.lastSeen = lastSeen
	s.runtimesMu.Lock()
	s.activeRuntimes[id] = e
	s.runtimesMu.Unlock()
	return id
}

func TestRunningPipelinesSnapshot_EmptyRegistryIsReloadSafe(t *testing.T) {
	s, _, _ := reconcileServer(t)

	res := s.RunningPipelinesSnapshot(time.Now())

	if res.Count != 0 || !res.ReloadSafe {
		t.Errorf("empty registry: count=%d reloadSafe=%v, want 0/true", res.Count, res.ReloadSafe)
	}
}

func TestRunningPipelinesSnapshot_CountsManualRuns(t *testing.T) {
	// The whole reason this reads the run registry rather than the
	// scheduler's `running` list: a manually picked-up issue is invisible to
	// the scheduler and dies in a reload identically.
	s, _, _ := reconcileServer(t)
	now := time.Now()
	installRun(t, s, "acme/web", 42, now)

	res := s.RunningPipelinesSnapshot(now)

	if res.Count != 1 {
		t.Fatalf("count = %d, want 1", res.Count)
	}
	if res.ReloadSafe {
		t.Error("reloadSafe must be false while a run is in flight")
	}
	if res.Runs[0].Source != "manual" {
		t.Errorf("source = %q, want manual — no scheduler dispatched it", res.Runs[0].Source)
	}
	if res.Runs[0].IssueNumber != 42 || res.Runs[0].Repo != "acme/web" {
		t.Errorf("identity lost: %+v", res.Runs[0])
	}
}

func TestRunningPipelinesSnapshot_ReportsStaleRatherThanDroppingIt(t *testing.T) {
	// A run that has gone quiet may be finished, or may be mid-`feature-dev`
	// on a long build. Filtering it out would turn the second case into
	// "safe to reload"; annotating it costs one glance.
	s, _, _ := reconcileServer(t)
	now := time.Now()
	installRun(t, s, "acme/web", 7, now.Add(-2*livenessWindow))

	res := s.RunningPipelinesSnapshot(now)

	if res.Count != 1 {
		t.Fatalf("a stale run must still be counted: count = %d", res.Count)
	}
	if !res.Runs[0].Stale {
		t.Error("the row must be marked stale so the caller can say so")
	}
	if res.ReloadSafe {
		t.Error("a stale run still means a reload is not provably safe")
	}
}

func TestRunningPipelinesSnapshot_ZeroLeaseIsStaleNotFresh(t *testing.T) {
	// An administrative install carries the zero lease and is never evidence
	// that a run is alive (ADR-017 §7.3). It must not read as just-seen.
	s, _, _ := reconcileServer(t)
	installRun(t, s, "acme/web", 8, time.Time{})

	res := s.RunningPipelinesSnapshot(time.Now())

	if res.Count != 1 {
		t.Fatalf("count = %d, want 1", res.Count)
	}
	if !res.Runs[0].Stale {
		t.Error("a zero lease must read as stale")
	}
	if res.Runs[0].LastProgressAt != "" {
		t.Errorf("a zero lease must not be rendered as a timestamp: %q", res.Runs[0].LastProgressAt)
	}
}

func TestRunningPipelinesSnapshot_SkipsTerminalAndAbandoned(t *testing.T) {
	s, _, _ := reconcileServer(t)
	now := time.Now()
	terminalID := installRun(t, s, "acme/web", 10, now)
	abandonedID := installRun(t, s, "acme/web", 11, now)
	installRun(t, s, "acme/web", 12, now)

	s.runtimesMu.Lock()
	s.activeRuntimes[terminalID].terminal = true
	s.activeRuntimes[abandonedID].abandoned = true
	s.runtimesMu.Unlock()

	res := s.RunningPipelinesSnapshot(now)

	if res.Count != 1 {
		t.Fatalf("count = %d, want 1 — a claimed or abandoned run is not in flight", res.Count)
	}
	if res.Runs[0].IssueNumber != 12 {
		t.Errorf("wrong run survived: %+v", res.Runs[0])
	}
}

func TestRunningPipelinesSnapshot_OrderIsStable(t *testing.T) {
	// Two calls a second apart must not reshuffle a list an operator is
	// reading off the screen.
	s, _, _ := reconcileServer(t)
	now := time.Now()
	installRun(t, s, "acme/web", 9, now)
	installRun(t, s, "acme/api", 3, now)
	installRun(t, s, "acme/web", 2, now)

	first := s.RunningPipelinesSnapshot(now)
	second := s.RunningPipelinesSnapshot(now)

	if len(first.Runs) != 3 {
		t.Fatalf("count = %d, want 3", len(first.Runs))
	}
	for i := range first.Runs {
		if first.Runs[i].RunID != second.Runs[i].RunID {
			t.Fatalf("order differs between calls at index %d", i)
		}
	}
	if first.Runs[0].Repo != "acme/api" || first.Runs[1].IssueNumber != 2 {
		t.Errorf("want repo-then-issue ordering, got %+v", first.Runs)
	}
}
