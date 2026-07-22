package gates

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

func TestDetectAtomicLLMOverrun_DeterministicPath_NoAnomaly(t *testing.T) {
	got := DetectAtomicLLMOverrun(state.StagePRMerge, "deterministic", 5.0, true, 0.01)
	if got != nil {
		t.Fatalf("deterministic path must not produce anomaly; got %#v", got)
	}
}

func TestDetectAtomicLLMOverrun_AtomicLLMAboveFloor_GatePassed_Anomaly(t *testing.T) {
	got := DetectAtomicLLMOverrun(state.StagePRMerge, "llm", 0.50, true, 0.01)
	if got == nil {
		t.Fatalf("expected anomaly for atomic+llm+above-floor+passed")
	}
	if got.Kind != AnomalyAtomicLLMOverrun {
		t.Errorf("Kind = %q, want %q", got.Kind, AnomalyAtomicLLMOverrun)
	}
	if got.Stage != string(state.StagePRMerge) {
		t.Errorf("Stage = %q, want %q", got.Stage, state.StagePRMerge)
	}
	if got.ExecutionPath != "llm" {
		t.Errorf("ExecutionPath = %q, want llm", got.ExecutionPath)
	}
	if got.StageCostUSD != 0.50 {
		t.Errorf("StageCostUSD = %v, want 0.50", got.StageCostUSD)
	}
	if got.DeterministicPredicate == "" {
		t.Errorf("expected non-empty DeterministicPredicate")
	}
	if got.Timestamp == "" {
		t.Errorf("expected non-empty Timestamp")
	}
}

// Failed gate already surfaces the problem on the failure path — no anomaly
// noise on top of it.
func TestDetectAtomicLLMOverrun_GateFailed_NoAnomaly(t *testing.T) {
	got := DetectAtomicLLMOverrun(state.StagePRMerge, "llm", 5.0, false, 0.01)
	if got != nil {
		t.Fatalf("gate failed path must not produce anomaly; got %#v", got)
	}
}

func TestDetectAtomicLLMOverrun_NonAtomicStage_NoAnomaly(t *testing.T) {
	got := DetectAtomicLLMOverrun(state.StageFeatureDev, "llm", 5.0, true, 0.01)
	if got != nil {
		t.Fatalf("non-atomic stage must not produce anomaly; got %#v", got)
	}
}

func TestDetectAtomicLLMOverrun_BelowFloor_NoAnomaly(t *testing.T) {
	got := DetectAtomicLLMOverrun(state.StagePRMerge, "llm", 0.005, true, 0.01)
	if got != nil {
		t.Fatalf("below floor must not produce anomaly; got %#v", got)
	}
}

func TestDetectAtomicLLMOverrun_FloorOverride(t *testing.T) {
	// Above default floor ($0.01) but below override ($1.00) — no anomaly.
	got := DetectAtomicLLMOverrun(state.StagePRMerge, "llm", 0.50, true, 1.00)
	if got != nil {
		t.Fatalf("cost below override floor must not produce anomaly; got %#v", got)
	}
	// Above override.
	got = DetectAtomicLLMOverrun(state.StagePRMerge, "llm", 1.50, true, 1.00)
	if got == nil {
		t.Fatalf("cost above override floor must produce anomaly")
	}
}

func TestDetectAtomicLLMOverrun_ZeroFloorFallsBackToDefault(t *testing.T) {
	// floor=0 → use DefaultAnomalyFloorUSD ($0.01). Cost at $0.005 < $0.01 → no anomaly.
	got := DetectAtomicLLMOverrun(state.StagePRMerge, "llm", 0.005, true, 0)
	if got != nil {
		t.Fatalf("cost below default floor must not produce anomaly when floor=0; got %#v", got)
	}
	// Cost above default.
	got = DetectAtomicLLMOverrun(state.StagePRMerge, "llm", 0.05, true, 0)
	if got == nil {
		t.Fatalf("cost above default floor must produce anomaly when floor=0")
	}
}

func TestIsAtomicEligible(t *testing.T) {
	if !IsAtomicEligible(state.StagePRMerge) {
		t.Error("pr-merge should be atomic-eligible")
	}
	if !IsAtomicEligible(state.StagePRCreate) {
		t.Error("pr-create should be atomic-eligible")
	}
	if IsAtomicEligible(state.StageFeatureDev) {
		t.Error("feature-dev should NOT be atomic-eligible")
	}
	if IsAtomicEligible("not-a-real-stage") {
		t.Error("unknown stage should NOT be atomic-eligible")
	}
}

func TestAtomicEligibleStages_ReturnsCopy(t *testing.T) {
	first := AtomicEligibleStages()
	first[state.PipelineStage("invented")] = "should not leak"
	second := AtomicEligibleStages()
	if _, ok := second[state.PipelineStage("invented")]; ok {
		t.Fatal("AtomicEligibleStages must return a copy, not the source map")
	}
}

func TestAnomaly_ToState(t *testing.T) {
	a := Anomaly{
		Kind:                   AnomalyAtomicLLMOverrun,
		Stage:                  "pr-merge",
		ExecutionPath:          "llm",
		StageCostUSD:           1.23,
		DeterministicPredicate: "deterministic gh pr merge available",
		Timestamp:              "2026-05-09T00:00:00Z",
	}
	got := a.ToState()
	if got.Kind != string(AnomalyAtomicLLMOverrun) {
		t.Errorf("Kind = %q, want %q", got.Kind, AnomalyAtomicLLMOverrun)
	}
	if got.Stage != "pr-merge" || got.ExecutionPath != "llm" || got.StageCostUSD != 1.23 ||
		got.DeterministicPredicate != "deterministic gh pr merge available" ||
		got.Timestamp != "2026-05-09T00:00:00Z" {
		t.Errorf("ToState lost fields: %#v", got)
	}
}
