package state

import "testing"

// TestSkipPhase_IsRecorded pins the value the record has advertised since it
// was written and never produced (#1026).
//
// PhaseRecord.Status documented "skipped" from the start. No writer emitted
// one, because the extension's skipPhase updated its own in-memory state, fired
// a view event, and sent no IPC — so the live GUI knew about a skipped phase
// and the durable record did not. They could not agree, by construction.
func TestSkipPhase_IsRecorded(t *testing.T) {
	rs := &RuntimeState{}
	rs.SkipPhase(StageFeatureDev, "run-tests", 3, 18)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d record(s), want 1", len(rs.PhaseHistory))
	}
	p := rs.PhaseHistory[0]
	if p.Status != "skipped" {
		t.Errorf("Status = %q, want skipped", p.Status)
	}
	if p.Name != "run-tests" || p.Index != 3 || p.Total != 18 {
		t.Errorf("record = %+v, want name=run-tests index=3 total=18", p)
	}
	// A skip is terminal on arrival — nothing will ever close it, so it must
	// not be left looking like work still in progress.
	if p.CompletedAt == nil {
		t.Error("CompletedAt is nil — a skipped phase is terminal, not running")
	}
}

// TestSkipPhase_IsIdempotent stops a re-notification double-counting.
func TestSkipPhase_IsIdempotent(t *testing.T) {
	rs := &RuntimeState{}
	rs.SkipPhase(StageFeatureDev, "run-tests", 3, 18)
	rs.SkipPhase(StageFeatureDev, "run-tests", 3, 18)

	if len(rs.PhaseHistory) != 1 {
		t.Errorf("PhaseHistory has %d record(s) after two skips, want 1", len(rs.PhaseHistory))
	}
}

// TestFailPhase_ClosesTheRunningRecord is the defect the live run exhibited:
// feature-dev left sync-project-status in status "running" for twenty-four
// minutes because failPhase was an empty body with a live caller. A failing
// phase was indistinguishable from one still in progress.
func TestFailPhase_ClosesTheRunningRecord(t *testing.T) {
	rs := &RuntimeState{}
	rs.BeginPhase(StageFeatureDev, "sync-project-status", 2, 18)
	rs.FailPhase(StageFeatureDev, "sync-project-status", 2, 18)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d record(s), want 1 — fail must AMEND the running record, not append a second",
			len(rs.PhaseHistory))
	}
	p := rs.PhaseHistory[0]
	if p.Status != "failed" {
		t.Errorf("Status = %q, want failed — a phase left \"running\" reads as in progress forever", p.Status)
	}
	if p.CompletedAt == nil {
		t.Error("CompletedAt is nil on a failed phase")
	}
}

// TestFailPhase_RecordsAPhaseThatNeverStarted covers the phase that dies before
// emitting its start marker — the case where there is nothing to amend.
func TestFailPhase_RecordsAPhaseThatNeverStarted(t *testing.T) {
	rs := &RuntimeState{}
	rs.FailPhase(StageFeatureDev, "never-began", 7, 18)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d record(s), want 1", len(rs.PhaseHistory))
	}
	if rs.PhaseHistory[0].Status != "failed" {
		t.Errorf("Status = %q, want failed", rs.PhaseHistory[0].Status)
	}
}

// TestCompletePhase_StillOnlyClosesRunning is the control: the new writers must
// not disturb the existing one.
func TestCompletePhase_StillOnlyClosesRunning(t *testing.T) {
	rs := &RuntimeState{}
	rs.BeginPhase(StageFeatureDev, "build", 1, 18)
	rs.SkipPhase(StageFeatureDev, "lint", 2, 18)
	rs.CompletePhase(StageFeatureDev, "build")

	if len(rs.PhaseHistory) != 2 {
		t.Fatalf("PhaseHistory has %d record(s), want 2", len(rs.PhaseHistory))
	}
	for _, p := range rs.PhaseHistory {
		switch p.Name {
		case "build":
			if p.Status != "complete" {
				t.Errorf("build = %q, want complete", p.Status)
			}
		case "lint":
			if p.Status != "skipped" {
				t.Errorf("lint = %q, want skipped — CompletePhase must not touch a skipped record", p.Status)
			}
		}
	}
}
