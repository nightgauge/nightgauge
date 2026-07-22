package state

import (
	"encoding/json"
	"testing"
	"time"
)

// TestBuildV2RecordPopulatesExecutionPathAndPuntReason is the observability
// regression for Issue #297. The V3 history stage record must carry BOTH the
// execution path AND, when the deterministic-first hook punted, the
// machine-readable reason it fell through to the LLM — so pipeline-health /
// retro can answer WHY the expensive path ran without the forensic session-log
// archaeology #288 required.
func TestBuildV2RecordPopulatesExecutionPathAndPuntReason(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 297, "item-297")

	// pr-create ran deterministically (no punt reason).
	rs.BeginStage(StagePRCreate)
	rs.CompleteStage(0, 100, 50, "")
	rs.RecordExecutionPath(StagePRCreate, "deterministic")

	// pr-merge punted to the LLM with a machine-readable reason.
	rs.BeginStage(StagePRMerge)
	rs.CompleteStage(0, 200, 90, "claude-sonnet-5")
	rs.RecordExecutionPath(StagePRMerge, "llm")
	rs.RecordStagePuntReason(StagePRMerge, "dirty-merge-state: BLOCKED")

	rec := hw.BuildV2Record(rs.Snapshot(), true, "", V2RunInput{Title: "obs", Branch: "feat/297"}, time.Now())

	create := rec.Stages[string(StagePRCreate)]
	if create.ExecutionPath != "deterministic" {
		t.Errorf("pr-create execution_path = %q, want deterministic", create.ExecutionPath)
	}
	if create.PuntReason != "" {
		t.Errorf("pr-create punt_reason = %q, want empty (deterministic path did not punt)", create.PuntReason)
	}

	merge := rec.Stages[string(StagePRMerge)]
	if merge.ExecutionPath != "llm" {
		t.Errorf("pr-merge execution_path = %q, want llm", merge.ExecutionPath)
	}
	if merge.PuntReason != "dirty-merge-state: BLOCKED" {
		t.Errorf("pr-merge punt_reason = %q, want %q", merge.PuntReason, "dirty-merge-state: BLOCKED")
	}

	// The field must serialize under the documented wire key `punt_reason` and
	// stay omitempty for stages that did not punt.
	blob, err := json.Marshal(rec.Stages)
	if err != nil {
		t.Fatalf("marshal stages: %v", err)
	}
	var decoded map[string]map[string]json.RawMessage
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal stages: %v", err)
	}
	if _, ok := decoded[string(StagePRMerge)]["punt_reason"]; !ok {
		t.Errorf("pr-merge stage JSON missing punt_reason key: %s", blob)
	}
	if _, ok := decoded[string(StagePRCreate)]["punt_reason"]; ok {
		t.Errorf("pr-create stage JSON should omit punt_reason (omitempty): %s", blob)
	}
}
