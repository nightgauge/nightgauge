package gates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// writeJSON is a test helper for laying out fake skill output JSON.
func writeJSON(t *testing.T, path string, payload any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNoOp_AlwaysPasses(t *testing.T) {
	gr := NoOp{GateName: "x"}.Verify(context.Background(), 1, t.TempDir())
	if !gr.Passed {
		t.Fatalf("NoOp must always pass; got reason=%q", gr.Reason)
	}
	if gr.GateName != "x" {
		t.Fatalf("GateName not propagated; got %q", gr.GateName)
	}
}

func TestNoOp_DefaultName(t *testing.T) {
	if name := (NoOp{}).Name(); name != "noop" {
		t.Fatalf("default Name = %q, want %q", name, "noop")
	}
}

func TestGateResult_ToStageGateResult(t *testing.T) {
	gr := GateResult{
		GateName:   "issue-pickup",
		Passed:     false,
		Reason:     "missing context",
		Evidence:   []string{"foo", "bar"},
		DurationMs: 12,
		Timestamp:  "2026-05-07T00:00:00Z",
		Kind:       KindNoOp,
	}
	got := gr.ToStageGateResult()
	if got.GateName != gr.GateName ||
		got.Passed != gr.Passed ||
		got.Reason != gr.Reason ||
		got.DurationMs != gr.DurationMs ||
		got.Timestamp != gr.Timestamp ||
		got.Kind != string(KindNoOp) ||
		len(got.Evidence) != 2 {
		t.Fatalf("ToStageGateResult = %#v, want fields preserved", got)
	}
	if _, ok := any(got).(state.StageGateResult); !ok {
		t.Fatalf("ToStageGateResult must return state.StageGateResult")
	}
	// Mutating the source after copy must not leak into the returned struct.
	gr.Evidence[0] = "mutated"
	if got.Evidence[0] != "foo" {
		t.Fatalf("Evidence not deep-copied; mutation leaked")
	}
}

func TestDefaultRegistry_Has6Gates(t *testing.T) {
	reg := Default()
	required := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}
	for _, stg := range required {
		if _, ok := reg[stg]; !ok {
			t.Errorf("Default() missing gate for %s", stg)
		}
	}
}

func TestLookupByStageName(t *testing.T) {
	gate, ok := LookupByStageName("issue-pickup")
	if !ok {
		t.Fatalf("LookupByStageName(issue-pickup) returned ok=false")
	}
	if gate.Name() != "issue-pickup" {
		t.Errorf("gate.Name() = %q, want issue-pickup", gate.Name())
	}
	if _, ok := LookupByStageName("not-a-stage"); ok {
		t.Error("expected ok=false for unknown stage")
	}
}

// TestSkillSaidSuccessButGateFailed_AcrossAllGates verifies the canonical
// "skill reported success but gate detected nothing changed" scenario for
// every gate: the skill output context file does not exist, so the gate
// must report passed=false. This is the Issue #3266 contract.
//
// Issue #3267: also asserts that every gate sets Kind=KindNoOp on this
// path so the classifier can emit `skill-no-op` deterministically.
func TestSkillSaidSuccessButGateFailed_AcrossAllGates(t *testing.T) {
	workspace := t.TempDir()
	cases := []struct {
		stage state.PipelineStage
		gate  StageGate
	}{
		{state.StageIssuePickup, IssuePickupGate{}},
		{state.StageFeaturePlanning, FeaturePlanningGate{}},
		{state.StageFeatureDev, FeatureDevGate{}},
		{state.StageFeatureValidate, FeatureValidateGate{}},
		{state.StagePRCreate, PrCreateGate{}},
		{state.StagePRMerge, PrMergeGate{}},
	}
	for _, c := range cases {
		gr := c.gate.Verify(context.Background(), 42, workspace)
		if gr.Passed {
			t.Errorf("%s gate passed when no skill output exists; reason=%q",
				c.stage, gr.Reason)
		}
		if gr.GateName == "" {
			t.Errorf("%s gate did not set GateName", c.stage)
		}
		if gr.Kind != KindNoOp {
			t.Errorf("%s gate Kind = %q, want %q (Issue #3267)",
				c.stage, gr.Kind, KindNoOp)
		}
	}
}

// TestKindOnPass_AcrossAllGates verifies that the timed/timedKind helpers
// produce KindOK on the happy path. Only gates whose pass path doesn't
// require external dependencies (gh, real PR, etc.) are exercised here —
// the others are covered in their per-file tests with mocked dependencies.
func TestKindOnPass_IssuePickup(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "issue-7.json"), map[string]any{
		"issue_number": 7,
		"branch":       "feat/7-x",
	})
	gr := IssuePickupGate{}.Verify(context.Background(), 7, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}
	if gr.Kind != KindOK {
		t.Errorf("Kind = %q, want %q", gr.Kind, KindOK)
	}
}

// TestKindFailOnMalformedJSON verifies that hard-error branches use KindFail
// rather than KindNoOp — distinct semantics for the classifier (no-op means
// the skill produced nothing; fail means it produced something broken).
func TestKindFailOnMalformedJSON(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "issue-9.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gr := IssuePickupGate{}.Verify(context.Background(), 9, ws)
	if gr.Passed {
		t.Fatalf("expected fail on malformed JSON")
	}
	if gr.Kind != KindFail {
		t.Errorf("Kind = %q, want %q (malformed JSON is a hard error, not no-op)",
			gr.Kind, KindFail)
	}
}
