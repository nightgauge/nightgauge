package state

import "testing"

// A passed phase is a weaker claim than a completed one, so it must never
// displace a record that carries real evidence (#1924) — the same rule
// UnreportedPhase follows.
func TestPassPhaseNeverOverwritesARecordWithEvidence(t *testing.T) {
	rs := &RuntimeState{}
	rs.BeginPhase(StageFeatureDev, "testing", 9, 18)
	rs.CompletePhase(StageFeatureDev, "testing")

	rs.PassPhase(StageFeatureDev, "testing", 9, 18)

	var seen int
	for _, p := range rs.PhaseHistory {
		if p.Name != "testing" {
			continue
		}
		seen++
		if p.Status == "passed" {
			t.Errorf("an observed phase was overwritten as passed; status = %q", p.Status)
		}
	}
	if seen != 1 {
		t.Errorf("phase recorded %d times, want 1 — PassPhase must be idempotent on stage+name", seen)
	}
}

func TestPassPhaseIsTerminalOnArrival(t *testing.T) {
	rs := &RuntimeState{}
	rs.PassPhase(StageFeaturePlanning, "load-context", 1, 14)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d records, want 1", len(rs.PhaseHistory))
	}
	rec := rs.PhaseHistory[0]
	if rec.Status != "passed" {
		t.Errorf("Status = %q, want passed", rec.Status)
	}
	if rec.CompletedAt == nil {
		t.Error("a passed phase must arrive terminal — CompletedAt is nil, so it would render as still running")
	}
}

// PassPhase must not be mistaken for a completion by anything counting them.
func TestPassedIsDistinctFromComplete(t *testing.T) {
	rs := &RuntimeState{}
	rs.PassPhase(StageFeatureDev, "implementation", 8, 18)
	if rs.PhaseHistory[0].Status == "complete" {
		t.Fatal("a phase that was never observed must not be recorded as complete")
	}
}
