package ipc

import "testing"

// #2536: an IPC "complete" must never book under the wrong name — not under ""
// when no stage was begun, and not under the running stage's name when the
// notification is for a different stage.
func TestNotifyCompleteWithoutRunningBooksNothing(t *testing.T) {
	s := newAttentionTestServer(t)
	run := newTestRunID()
	notifyStageTransition(t, s, PipelineNotifyStageTransitionParams{
		Repo: "octocat/acme", IssueNumber: 2536, Stage: "feature-dev", Status: "complete",
		CostUsd: 3, RunID: run,
	})

	snap := notifyGuardSnapshot(t, s, run)
	if n := len(snap.CompletedStages); n != 0 {
		t.Fatalf("CompletedStages = %+v, want none booked", snap.CompletedStages)
	}
	if snap.TotalCostUSD != 0 {
		t.Errorf("TotalCostUSD = %v, want 0", snap.TotalCostUSD)
	}
}

func TestNotifyCompleteForOtherStageBooksNothing(t *testing.T) {
	s := newAttentionTestServer(t)
	run := newTestRunID()
	base := PipelineNotifyStageTransitionParams{Repo: "octocat/acme", IssueNumber: 2536, RunID: run}

	running := base
	running.Stage, running.Status = "feature-planning", "running"
	notifyStageTransition(t, s, running)

	complete := base
	complete.Stage, complete.Status, complete.CostUsd = "feature-dev", "complete", 3
	notifyStageTransition(t, s, complete)

	snap := notifyGuardSnapshot(t, s, run)
	if n := len(snap.CompletedStages); n != 0 {
		t.Fatalf("CompletedStages = %+v, want none booked", snap.CompletedStages)
	}

	// The matching completion still books.
	complete.Stage = "feature-planning"
	notifyStageTransition(t, s, complete)
	snap = notifyGuardSnapshot(t, s, run)
	if n := len(snap.CompletedStages); n != 1 || snap.CompletedStages[0].Stage != "feature-planning" {
		t.Fatalf("CompletedStages = %+v, want one feature-planning entry", snap.CompletedStages)
	}
}

func notifyGuardSnapshot(t *testing.T, s *Server, run string) *struct {
	CompletedStages []struct{ Stage string }
	TotalCostUSD    float64
} {
	t.Helper()
	s.runtimesMu.Lock()
	rt, ok := s.activeRuntimes[run]
	s.runtimesMu.Unlock()
	if !ok {
		t.Fatalf("no runtime for run %s", run)
	}
	snap := rt.rs.Snapshot()
	out := &struct {
		CompletedStages []struct{ Stage string }
		TotalCostUSD    float64
	}{TotalCostUSD: snap.TotalCostUSD}
	for _, c := range snap.CompletedStages {
		out.CompletedStages = append(out.CompletedStages, struct{ Stage string }{string(c.Stage)})
	}
	return out
}
