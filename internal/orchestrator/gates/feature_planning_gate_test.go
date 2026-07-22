package gates

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFeaturePlanningGate_Pass_AbsolutePath(t *testing.T) {
	ws := t.TempDir()
	planFile := filepath.Join(ws, ".nightgauge", "plans", "42-thing.md")
	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planFile, []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "planning-42.json"), map[string]any{
		"plan_file": planFile,
	})

	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

func TestFeaturePlanningGate_Pass_RelativePath(t *testing.T) {
	ws := t.TempDir()
	planRel := ".nightgauge/plans/42-rel.md"
	planAbs := filepath.Join(ws, planRel)
	if err := os.MkdirAll(filepath.Dir(planAbs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planAbs, []byte("# plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "planning-42.json"), map[string]any{
		"plan_file": planRel,
	})

	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass for relative plan_file; reason=%q", gr.Reason)
	}
}

func TestFeaturePlanningGate_Fail_PlanFileMissing(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "planning-42.json"), map[string]any{
		"plan_file": "no/such/plan.md",
	})
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when plan_file missing")
	}
}

// TestFeaturePlanningGate_SkillSaidSuccessButPlanEmpty covers the canonical
// "skill claimed success but emitted a zero-byte plan" scenario.
func TestFeaturePlanningGate_SkillSaidSuccessButPlanEmpty(t *testing.T) {
	ws := t.TempDir()
	planFile := filepath.Join(ws, ".nightgauge", "plans", "42-empty.md")
	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planFile, []byte{}, 0o644); err != nil {
		t.Fatalf("write empty plan: %v", err)
	}
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "planning-42.json"), map[string]any{
		"plan_file": planFile,
	})

	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when plan_file is zero bytes")
	}
}
