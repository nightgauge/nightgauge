package state

import (
	"testing"
	"time"
)

// ── #91 served-model attribution ─────────────────────────────────────────
// The claude CLI silently retries safety-refused turns on a fallback model
// (its internal model_refusal_fallback event) and still exits 0. These tests
// pin the RuntimeState record of that swap and its projection onto the V2
// history record. See docs/spikes/fable-5-behavior-porting.md §8.3.

func TestRecordModelRefusalFallback(t *testing.T) {
	rs := NewRuntimeState("o/r", 91, "item-1")

	if got := rs.LastRefusalServedModel(); got != "" {
		t.Errorf("empty state: LastRefusalServedModel = %q, want empty", got)
	}

	// Empty fallback model is ignored (omitempty wire contract).
	rs.RecordModelRefusalFallback(StageFeatureDev, "claude-fable-5", "", "reasoning_extraction")
	if len(rs.ModelRefusalFallbacks) != 0 {
		t.Fatalf("empty fallback recorded: %#v", rs.ModelRefusalFallbacks)
	}

	rs.RecordModelRefusalFallback(StageFeatureDev, "claude-fable-5", "claude-opus-4-8", "reasoning_extraction")
	if got := rs.LastRefusalServedModel(); got != "claude-opus-4-8" {
		t.Errorf("LastRefusalServedModel = %q, want claude-opus-4-8", got)
	}

	// Append-only; the most recent fallback wins for run-level attribution.
	rs.RecordModelRefusalFallback(StagePRCreate, "claude-fable-5", "claude-sonnet-4-6", "")
	if len(rs.ModelRefusalFallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks, got %d", len(rs.ModelRefusalFallbacks))
	}
	if got := rs.LastRefusalServedModel(); got != "claude-sonnet-4-6" {
		t.Errorf("LastRefusalServedModel = %q, want claude-sonnet-4-6 (last wins)", got)
	}

	fb := rs.ModelRefusalFallbacks[0]
	if fb.Stage != string(StageFeatureDev) || fb.OriginalModel != "claude-fable-5" ||
		fb.FallbackModel != "claude-opus-4-8" || fb.RefusalCategory != "reasoning_extraction" {
		t.Errorf("first fallback not preserved: %#v", fb)
	}
}

func TestSnapshotCopiesModelRefusalFallbacks(t *testing.T) {
	rs := NewRuntimeState("o/r", 91, "item-1")
	rs.RecordModelRefusalFallback(StageFeatureDev, "claude-fable-5", "claude-opus-4-8", "reasoning_extraction")

	snap := rs.Snapshot()
	if len(snap.ModelRefusalFallbacks) != 1 {
		t.Fatalf("snapshot lost fallbacks: %#v", snap.ModelRefusalFallbacks)
	}
	// Deep copy — mutating the snapshot must not touch the source.
	snap.ModelRefusalFallbacks[0].FallbackModel = "mutated"
	if rs.ModelRefusalFallbacks[0].FallbackModel != "claude-opus-4-8" {
		t.Error("snapshot shares backing array with source")
	}
}

// TestBuildV2Record_RefusalFallbackModelSelection is the #91 history-record
// regression: when the scheduler re-records StageModels with the served model
// after a CLI refusal fallback, the V2 per-stage ModelSelection must carry
// the SERVED model with source "cli-refusal-fallback" so consumers can flag
// the substitution without diffing against the predicted model.
func TestBuildV2Record_RefusalFallbackModelSelection(t *testing.T) {
	rs := NewRuntimeState("o/r", 91, "item-1")
	rs.StartedAt = time.Now()
	rs.BeginStage(StageFeatureDev)
	// Dispatch-time record (requested), then the post-run re-record with the
	// served model — mirroring the scheduler's #91 attribution flow.
	rs.RecordStageModel(StageFeatureDev, "claude-fable-5")
	rs.RecordModelRefusalFallback(StageFeatureDev, "claude-fable-5", "claude-opus-4-8", "reasoning_extraction")
	rs.RecordStageModel(StageFeatureDev, "claude-opus-4-8")
	rs.CompleteStage(0, 100, 200, "claude-opus-4-8")

	hw := NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs.Snapshot(), true, "", V2RunInput{}, time.Now())
	stage, ok := rec.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev stage missing from record")
	}
	if stage.ModelSelection == nil {
		t.Fatal("ModelSelection missing")
	}
	if stage.ModelSelection.Model != "claude-opus-4-8" {
		t.Errorf("ModelSelection.Model = %q, want the served model claude-opus-4-8", stage.ModelSelection.Model)
	}
	if stage.ModelSelection.Source != "cli-refusal-fallback" {
		t.Errorf("ModelSelection.Source = %q, want cli-refusal-fallback", stage.ModelSelection.Source)
	}
}

// A stage without a refusal fallback keeps the plain scheduler source — the
// #91 marker must never leak onto unaffected stages.
func TestBuildV2Record_RefusalFallbackDoesNotLeakAcrossStages(t *testing.T) {
	rs := NewRuntimeState("o/r", 91, "item-1")
	rs.StartedAt = time.Now()
	rs.BeginStage(StageIssuePickup)
	rs.RecordStageModel(StageIssuePickup, "claude-sonnet-4-6")
	rs.CompleteStage(0, 10, 20, "claude-sonnet-4-6")
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "claude-opus-4-8")
	rs.RecordModelRefusalFallback(StageFeatureDev, "claude-fable-5", "claude-opus-4-8", "reasoning_extraction")
	rs.CompleteStage(0, 100, 200, "claude-opus-4-8")

	hw := NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs.Snapshot(), true, "", V2RunInput{}, time.Now())
	pickup := rec.Stages[string(StageIssuePickup)]
	if pickup.ModelSelection == nil || pickup.ModelSelection.Source != "scheduler" {
		t.Errorf("issue-pickup ModelSelection = %#v, want source scheduler", pickup.ModelSelection)
	}
	dev := rec.Stages[string(StageFeatureDev)]
	if dev.ModelSelection == nil || dev.ModelSelection.Source != "cli-refusal-fallback" {
		t.Errorf("feature-dev ModelSelection = %#v, want source cli-refusal-fallback", dev.ModelSelection)
	}
}
