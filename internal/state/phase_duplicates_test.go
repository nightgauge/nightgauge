package state

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// TestCompleteStage_ClosesAPhaseLeftRunning pins the third defect: a phase sat
// in status "running" for twenty-four minutes and the stage then finished, so
// the record still claimed the run was stuck there.
func TestCompleteStage_ClosesAPhaseLeftRunning(t *testing.T) {
	rs := NewRuntimeState("o/r", 1009, "item", "01a04662-0000-7000-8000-000000000000")
	rs.Stage = StageFeatureDev
	rs.BeginPhase(StageFeatureDev, "sync-project-status", 15, 18)

	rs.CompleteStage(0, tokens.TokenCounts{}, "sonnet", "claude")

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d records, want 1", len(rs.PhaseHistory))
	}
	p := rs.PhaseHistory[0]
	if p.Status == "running" {
		t.Error("the phase is still \"running\" after its stage completed — that is the " +
			"reading that says a finished run is stuck at phase 15")
	}
	if p.Status != "abandoned" {
		t.Errorf("Status = %q, want abandoned — the stage got past it without it ever completing", p.Status)
	}
	if p.CompletedAt == nil {
		t.Error("CompletedAt is nil on a terminal phase")
	}
}

// TestCompleteStage_LeavesFinishedPhasesAlone is the control: the sweep must
// not rewrite phases that ended properly.
func TestCompleteStage_LeavesFinishedPhasesAlone(t *testing.T) {
	rs := NewRuntimeState("o/r", 1009, "item", "01a04662-0000-7000-8000-000000000001")
	rs.Stage = StageFeatureDev
	rs.BeginPhase(StageFeatureDev, "implementation", 8, 18)
	rs.CompletePhase(StageFeatureDev, "implementation")
	rs.SkipPhase(StageFeatureDev, "e2e-testing", 10, 18)

	rs.CompleteStage(0, tokens.TokenCounts{}, "sonnet", "claude")

	byName := map[string]string{}
	for _, p := range rs.PhaseHistory {
		byName[p.Name] = p.Status
	}
	if byName["implementation"] != "complete" {
		t.Errorf("implementation = %q, want complete", byName["implementation"])
	}
	if byName["e2e-testing"] != "skipped" {
		t.Errorf("e2e-testing = %q, want skipped", byName["e2e-testing"])
	}
}

// TestCloseRunningPhases_IsScopedToItsStage stops one stage's completion
// terminating another's in-flight phases.
func TestCloseRunningPhases_IsScopedToItsStage(t *testing.T) {
	rs := &RuntimeState{}
	rs.BeginPhase(StageFeatureDev, "implementation", 8, 18)
	rs.BeginPhase(StageFeatureValidate, "run-tests", 3, 23)

	closed := rs.CloseRunningPhases(StageFeatureDev)

	if closed != 1 {
		t.Errorf("closed %d phases, want 1", closed)
	}
	for _, p := range rs.PhaseHistory {
		if p.Stage == StageFeatureValidate && p.Status != "running" {
			t.Errorf("a phase of another stage was closed: %+v", p)
		}
	}
}
