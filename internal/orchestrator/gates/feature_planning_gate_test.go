package gates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func TestFeaturePlanningGate_Pass_AbsolutePath(t *testing.T) {
	ws := t.TempDir()
	planFile := filepath.Join(layouttest.PlansDir(t, ws), "42-thing.md")
	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planFile, []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "planning-42.json"), map[string]any{
		"plan_file": planFile,
	})

	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

// A relative plan_file is taken from the clone's plans directory, where
// feature-planning writes the plan (ADR-024 § 7).
func TestFeaturePlanningGate_Pass_RelativePath(t *testing.T) {
	ws := layouttest.Repo(t)
	planAbs := filepath.Join(layouttest.MkPlansDir(t, ws), "42-rel.md")
	if err := os.WriteFile(planAbs, []byte("# plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "planning-42.json"), map[string]any{
		"plan_file": "42-rel.md",
	})

	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass for relative plan_file; reason=%q", gr.Reason)
	}
}

// A worktree-isolated run's plans directory is the main clone's, outside the
// REAL linked worktree the stage ran in: the gate accepts a plan there, and
// fails one written into the worktree, which feature-dev would refuse to read.
func TestFeaturePlanningGate_LinkedWorktree(t *testing.T) {
	root := gitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "--detach", wt)

	plan := filepath.Join(layouttest.MkPlansDir(t, wt), "42-plan.md")
	writeFile(t, plan, "# plan\n")
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, wt), "planning-42.json"), map[string]any{
		"plan_file": plan,
	})
	if gr := (FeaturePlanningGate{}).Verify(context.Background(), 42, wt); !gr.Passed {
		t.Fatalf("plan in the plans directory failed from a linked worktree: reason=%q evidence=%v",
			gr.Reason, gr.Evidence)
	}

	inTree := filepath.Join(wt, "PLAN.md")
	writeFile(t, inTree, "# plan\n")
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, wt), "planning-42.json"), map[string]any{
		"plan_file": inTree,
	})
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, wt)
	if gr.Passed || gr.TerminalKind != TerminalKindValidationError {
		t.Fatalf("plan in the worktree: passed=%v kind=%q, want a validation_error failure (reason=%q)",
			gr.Passed, gr.TerminalKind, gr.Reason)
	}
}

// ResolvePlanFile accepts only a regular file inside the plans directory,
// after symlink evaluation, resolved from a REAL linked worktree whose plans
// directory lies outside it.
func TestResolvePlanFile_LinkedWorktree(t *testing.T) {
	root := gitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "--detach", wt)
	plans := layouttest.MkPlansDir(t, wt)

	plan := filepath.Join(plans, "7-plan.md")
	writeFile(t, plan, "- [ ] One\n")
	for _, planFile := range []string{plan, "7-plan.md"} {
		got, err := ResolvePlanFile(wt, planFile)
		if err != nil {
			t.Fatalf("%s refused: %v", planFile, err)
		}
		if want, _ := filepath.EvalSymlinks(plan); got != want {
			t.Errorf("%s resolved to %q, want %q", planFile, got, want)
		}
	}

	inTree := filepath.Join(wt, "PLAN.md")
	writeFile(t, inTree, "- [ ] One\n")
	outside := filepath.Join(t.TempDir(), "plan.md")
	writeFile(t, outside, "- [ ] One\n")
	escape := filepath.Join(plans, "escape.md")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	dotdot, _ := filepath.Rel(plans, outside)
	for name, planFile := range map[string]string{
		"in the worktree":  inTree,
		"outside both":     outside,
		"symlink escaping": escape,
		"dot-dot escape":   dotdot,
		"the directory":    plans,
		"missing":          "none.md",
	} {
		if _, err := ResolvePlanFile(wt, planFile); !errors.Is(err, ErrPlanNotContained) {
			t.Errorf("%s: err = %v, want ErrPlanNotContained", name, err)
		}
	}
	if _, err := ResolvePlanFile("", plan); !errors.Is(err, ErrPlanNotContained) {
		t.Errorf("empty workspace: err = %v, want ErrPlanNotContained", err)
	}
}

func TestFeaturePlanningGate_Fail_PlanFileMissing(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "planning-42.json"), map[string]any{
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
	planFile := filepath.Join(layouttest.PlansDir(t, ws), "42-empty.md")
	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planFile, []byte{}, 0o644); err != nil {
		t.Fatalf("write empty plan: %v", err)
	}
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "planning-42.json"), map[string]any{
		"plan_file": planFile,
	})

	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when plan_file is zero bytes")
	}
	if gr.TerminalKind != "" {
		t.Errorf("TerminalKind = %q, want empty (KindNoOp falls back to prose)", gr.TerminalKind)
	}
}

func TestFeaturePlanningGate_Fail_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	dir := layouttest.PipelineDir(t, ws)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planning-42.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail on malformed JSON")
	}
	if gr.TerminalKind != TerminalKindValidationError {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindValidationError)
	}
}
