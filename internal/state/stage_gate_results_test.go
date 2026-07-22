package state

import (
	"encoding/json"
	"testing"
	"time"
)

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
	rs.CompleteStage(0, 100, 200, "claude-sonnet-4-6")
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
