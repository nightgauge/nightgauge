package state

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// TestCompleteStage_SettlesAPhaseLeftRunningOnSuccess pins the successful-
// boundary half of #1885's fix: a stage that reaches its own CompleteStage
// call (exitCode 0) got all the way to completion, so the last phase it left
// running is settled `complete`, not `abandoned` — `abandoned` is reserved
// for a stage that did NOT finish (see the failure-path sibling below).
//
// Before #1885 this always wrote `abandoned` regardless of exit code, which
// is what made a successful feature-dev/feature-planning/feature-validate
// run render "0/N phases · N abandoned" even though it plainly worked.
func TestCompleteStage_SettlesAPhaseLeftRunningOnSuccess(t *testing.T) {
	rs := NewRuntimeState("o/r", 1009, "item", "01a04662-0000-7000-8000-000000000000")
	rs.Stage = StageFeatureDev
	rs.BeginPhase(StageFeatureDev, "sync-project-status", 15, 18)

	rs.CompleteStage(0, tokens.TokenCounts{}, "sonnet", "claude")

	var p *PhaseRecord
	for i := range rs.PhaseHistory {
		if rs.PhaseHistory[i].Name == "sync-project-status" {
			p = &rs.PhaseHistory[i]
		}
	}
	if p == nil {
		t.Fatal("sync-project-status phase record is missing entirely")
	}
	if p.Status == "running" {
		t.Error("the phase is still \"running\" after its stage completed — that is the " +
			"reading that says a finished run is stuck at phase 15")
	}
	if p.Status != "complete" {
		t.Errorf("Status = %q, want complete — the stage reached its own completion call, "+
			"which is not what \"abandoned\" means (#1885 AC2)", p.Status)
	}
	if p.CompletedAt == nil {
		t.Error("CompletedAt is nil on a terminal phase")
	}
}

// TestCompleteStage_AbandonsAPhaseLeftRunningOnFailure is #1009's original
// case, still reachable and still correct: a stage that ends abnormally
// (non-zero exit) with a phase still open leaves that phase as evidence the
// run got stuck there, not as a claim the phase succeeded (#1885 AC3).
func TestCompleteStage_AbandonsAPhaseLeftRunningOnFailure(t *testing.T) {
	rs := NewRuntimeState("o/r", 1009, "item", "01a04662-0000-7000-8000-000000000002")
	rs.Stage = StageFeatureDev
	rs.BeginPhase(StageFeatureDev, "sync-project-status", 15, 18)

	rs.CompleteStage(1, tokens.TokenCounts{}, "sonnet", "claude")

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d records, want 1 — a failing boundary must not back-fill "+
			"the registry (#1885 AC3: back-fill is a success-only behavior)", len(rs.PhaseHistory))
	}
	p := rs.PhaseHistory[0]
	if p.Status != "abandoned" {
		t.Errorf("Status = %q, want abandoned — the stage ended abnormally with the phase still open", p.Status)
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
	// The registry back-fill (#1885 AC4) must not touch either finished
	// record, and must fill every name neither writer reported.
	if got, want := len(rs.PhaseHistory), len(PhaseRegistry[StageFeatureDev]); got != want {
		t.Errorf("PhaseHistory has %d records, want %d (registry total)", got, want)
	}
	if byName["validate-environment"] != "unreported" {
		t.Errorf("validate-environment = %q, want unreported — the stage said nothing about it", byName["validate-environment"])
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
