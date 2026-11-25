package ipc

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// AC 2/3: SetAutonomousScheduler wires the retire hook and the live-issues
// provider to the registry. A stale entry is retired and reload becomes safe;
// a fresh entry stays counted (#3567).
func TestSetAutonomousScheduler_WiresRunRetire(t *testing.T) {
	s, root, _ := reconcileServer(t)
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil,
		orchestrator.DefaultAutonomousConfig(), root)
	t.Cleanup(func() { as.Shutdown(time.Second) })
	s.SetAutonomousScheduler(as)

	now := time.Now()
	installRegistryEntry(t, s, state.NewRuntimeState("acme/app", 830, "", newTestRunID()),
		now.Add(-2*livenessWindow))
	installRegistryEntry(t, s, state.NewRuntimeState("acme/app", 831, "", newTestRunID()), now)

	if got := as.LiveRunIssues(); len(got) != 2 {
		t.Fatalf("provider not wired: %+v", got)
	}
	hook := as.RunNotInFlightHook()
	if hook == nil {
		t.Fatal("retire hook not wired")
	}
	if !hook("acme/app", 830, "x") {
		t.Fatal("a retired dead run must answer settled")
	}
	if hook("acme/app", 831, "x") {
		t.Fatal("a fresh run must answer unsettled")
	}
	res := s.RunningPipelinesSnapshot(time.Now())
	if res.Count != 1 || res.Runs[0].IssueNumber != 831 || res.ReloadSafe {
		t.Fatalf("want only #831 counted: %+v", res)
	}
}
