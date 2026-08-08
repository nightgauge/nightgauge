package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// TestAppendStageGateResultToDisk verifies the CLI/IPC persistence seam
// (Issue #210): `nightgauge gate verify --record` uses this to append a gate
// result onto the run record for the TypeScript HeadlessOrchestrator path,
// which never runs in-process alongside the Go scheduler and so cannot call
// RuntimeState.AppendStageGateResult directly.
func TestAppendStageGateResultToDisk(t *testing.T) {
	stateDir := t.TempDir()

	if err := AppendStageGateResultToDisk(stateDir, 210, StageFeatureDev, StageGateResult{
		GateName: "feature-dev",
		Passed:   true,
		Reason:   "workspace has changes",
		Kind:     "ok",
	}); err != nil {
		t.Fatalf("AppendStageGateResultToDisk (first write): %v", err)
	}

	rs, err := LoadPersistedState(stateDir, 210)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	got := rs.StageGateResultsFor(StageFeatureDev)
	if len(got) != 1 || !got[0].Passed || got[0].GateName != "feature-dev" {
		t.Fatalf("StageGateResultsFor(feature-dev) = %#v", got)
	}

	// A second append for a different stage on the same run must accumulate
	// rather than clobber the first — this is the multi-call-site path all
	// six HeadlessOrchestrator.ts gate invocations exercise across one run.
	if err := AppendStageGateResultToDisk(stateDir, 210, StageIssuePickup, StageGateResult{
		GateName: "issue-pickup",
		Passed:   false,
		Reason:   "no branch created",
		Kind:     "no_op",
	}); err != nil {
		t.Fatalf("AppendStageGateResultToDisk (second write): %v", err)
	}

	rs2, err := LoadPersistedState(stateDir, 210)
	if err != nil {
		t.Fatalf("LoadPersistedState (after second write): %v", err)
	}
	if rs2.IssueNumber != 210 {
		t.Errorf("IssueNumber = %d, want 210", rs2.IssueNumber)
	}
	devResults := rs2.StageGateResultsFor(StageFeatureDev)
	if len(devResults) != 1 || !devResults[0].Passed {
		t.Fatalf("feature-dev result lost after second write: %#v", devResults)
	}
	pickupResults := rs2.StageGateResultsFor(StageIssuePickup)
	if len(pickupResults) != 1 || pickupResults[0].Passed || pickupResults[0].Kind != "no_op" {
		t.Fatalf("issue-pickup result wrong: %#v", pickupResults)
	}
}

// TestRuntimeState_AppendStageGateResult verifies the per-stage append/read
// path used by the orchestrator scheduler (Issue #3266).
func TestRuntimeState_AppendStageGateResult(t *testing.T) {
	rs := NewRuntimeState("o/r", 42, "item-1")
	rs.AppendStageGateResult(StageIssuePickup, StageGateResult{
		GateName: "issue-pickup",
		Passed:   true,
		Reason:   "context exists",
	})
	rs.AppendStageGateResult(StagePRMerge, StageGateResult{
		GateName: "pr-merge",
		Passed:   false,
		Reason:   "PR not merged",
	})

	got := rs.StageGateResultsFor(StageIssuePickup)
	if len(got) != 1 || !got[0].Passed {
		t.Fatalf("StageGateResultsFor(issue-pickup) = %#v", got)
	}

	got = rs.StageGateResultsFor(StagePRMerge)
	if len(got) != 1 || got[0].Passed {
		t.Fatalf("StageGateResultsFor(pr-merge) = %#v", got)
	}

	// Returned slice must be a copy — mutating it should not affect state.
	got[0].Reason = "mutated"
	again := rs.StageGateResultsFor(StagePRMerge)
	if again[0].Reason == "mutated" {
		t.Errorf("StageGateResultsFor must return a copy")
	}
}

// TestRuntimeState_Snapshot_DeepCopiesGateResults verifies that snapshots
// (used by the V2 writer) cannot mutate the live state.
func TestRuntimeState_Snapshot_DeepCopiesGateResults(t *testing.T) {
	rs := NewRuntimeState("o/r", 42, "item-1")
	rs.AppendStageGateResult(StageIssuePickup, StageGateResult{
		GateName: "issue-pickup",
		Passed:   true,
	})

	snap := rs.Snapshot()
	if len(snap.StageGateResults) != 1 {
		t.Fatalf("snapshot missing stage gate results: %#v", snap.StageGateResults)
	}
	snap.StageGateResults[string(StageIssuePickup)][0].Passed = false
	got := rs.StageGateResultsFor(StageIssuePickup)
	if !got[0].Passed {
		t.Errorf("snapshot mutation leaked into live state")
	}
}

// TestBuildV2Record_PopulatesGateResultsPerStage verifies that the V2
// writer projects RuntimeState.StageGateResults onto V2StageDetail.GateResults
// for matching stages.
func TestBuildV2Record_PopulatesGateResultsPerStage(t *testing.T) {
	rs := NewRuntimeState("o/r", 42, "item-1")
	rs.StartedAt = time.Now()
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 200}, "claude-sonnet-4-6")
	rs.AppendStageGateResult(StageIssuePickup, StageGateResult{
		GateName:  "issue-pickup",
		Passed:    true,
		Reason:    "context exists",
		Timestamp: "2026-05-07T00:00:00Z",
	})

	hw := NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs.Snapshot(), true, "", V2RunInput{}, time.Now())
	stage, ok := rec.Stages["issue-pickup"]
	if !ok {
		t.Fatalf("issue-pickup stage missing from record")
	}
	if len(stage.GateResults) != 1 {
		t.Fatalf("expected 1 gate result, got %d", len(stage.GateResults))
	}
	if stage.GateResults[0].GateName != "issue-pickup" || !stage.GateResults[0].Passed {
		t.Errorf("gate result not preserved: %#v", stage.GateResults[0])
	}
}

// TestV2StageDetail_GateResults_BackwardsCompatRead verifies the additive
// field default — old records (no gate_results key) still parse cleanly
// and read with a nil/empty slice. ADR-002 V1∪V2∪V3 union convention.
func TestV2StageDetail_GateResults_BackwardsCompatRead(t *testing.T) {
	// Old V2 record without gate_results — emitted before #3266.
	old := []byte(`{
        "schema_version": "2",
        "record_type": "run",
        "issue_number": 42,
        "title": "old",
        "branch": "feat/42",
        "base_branch": "main",
        "execution_mode": "automatic",
        "started_at": "2026-05-07T00:00:00Z",
        "completed_at": "2026-05-07T00:01:00Z",
        "outcome": "complete",
        "stages": {
            "issue-pickup": {
                "status": "complete",
                "duration_ms": 1234
            }
        },
        "tokens": {
            "total_input": 0,
            "total_output": 0,
            "total_cache_read": 0,
            "total_cache_creation": 0,
            "estimated_cost_usd": 0
        },
        "files": {"read_count": 0, "written_count": 0},
        "routing": {"complexity_score": 0, "path": "standard", "skip_stages": []}
    }`)
	var rec V2RunRecord
	if err := json.Unmarshal(old, &rec); err != nil {
		t.Fatalf("old record failed to parse: %v", err)
	}
	stage := rec.Stages["issue-pickup"]
	if stage.GateResults != nil {
		t.Errorf("expected nil GateResults on legacy record, got %#v", stage.GateResults)
	}
}

// TestStageGateResult_TerminalKind_BackwardsCompatRead verifies the Issue #9
// additive field: an old persisted StageGateResult with no `terminal_kind`
// key decodes cleanly to an empty string, never an error, and a record that
// does carry it round-trips the value unchanged.
func TestStageGateResult_TerminalKind_BackwardsCompatRead(t *testing.T) {
	old := []byte(`{
        "gate_name": "issue-pickup",
        "passed": false,
        "reason": "issue context file is not valid JSON",
        "timestamp": "2026-05-07T00:00:00Z",
        "kind": "fail"
    }`)
	var gr StageGateResult
	if err := json.Unmarshal(old, &gr); err != nil {
		t.Fatalf("legacy StageGateResult failed to parse: %v", err)
	}
	if gr.TerminalKind != "" {
		t.Errorf("expected empty TerminalKind on legacy record, got %q", gr.TerminalKind)
	}

	withKind := []byte(`{
        "gate_name": "issue-pickup",
        "passed": false,
        "reason": "issue context file is not valid JSON",
        "timestamp": "2026-05-07T00:00:00Z",
        "kind": "fail",
        "terminal_kind": "validation_error"
    }`)
	var gr2 StageGateResult
	if err := json.Unmarshal(withKind, &gr2); err != nil {
		t.Fatalf("record with terminal_kind failed to parse: %v", err)
	}
	if gr2.TerminalKind != "validation_error" {
		t.Errorf("TerminalKind = %q, want %q", gr2.TerminalKind, "validation_error")
	}

	// Round-trip: marshal then unmarshal preserves the value, and an empty
	// TerminalKind is omitted from the JSON (omitempty).
	data, err := json.Marshal(gr2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"terminal_kind":"validation_error"`) {
		t.Errorf("marshaled record missing terminal_kind: %s", data)
	}
	emptyData, err := json.Marshal(gr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(emptyData), "terminal_kind") {
		t.Errorf("empty TerminalKind should be omitted via omitempty: %s", emptyData)
	}
}
