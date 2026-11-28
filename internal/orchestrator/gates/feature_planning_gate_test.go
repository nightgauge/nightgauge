package gates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		"plan_file": planFile, "files_to_modify": []string{"a.go"},
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
		"plan_file": "42-rel.md", "files_to_modify": []string{"a.go"},
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
		"plan_file": plan, "files_to_modify": []string{"a.go"},
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
		"plan_file": planFile, "files_to_modify": []string{"a.go"},
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

// publicationWorkspace writes a plan, a boundary manifest, and a stub checker
// that denies every path it is asked about (#2562).
func publicationWorkspace(t *testing.T, ctx map[string]any) string {
	t.Helper()
	ws := t.TempDir()
	planFile := filepath.Join(layouttest.PlansDir(t, ws), "42-thing.md")
	writeFile(t, planFile, "# plan\n")
	writeFile(t, filepath.Join(ws, ".github/publication-boundary.yaml"), "allow: []\n")
	writeFile(t, filepath.Join(ws, "scripts/publication-boundary-check.py"), `import json, sys
paths = [a for a in sys.argv[2:] if a != "--"]
print(json.dumps([{"path": p, "verdict": "deny", "rule": "docs/spikes/**", "class": "PRIVATE"} for p in paths]))
sys.exit(1)
`)
	writeFile(t, filepath.Join(ws, "internal/existing.go"), "package x\n")
	ctx["plan_file"] = planFile
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "planning-42.json"), ctx)
	return ws
}

func TestFeaturePlanningGate_PublicationDeniedPathIsNotPipelineActionable(t *testing.T) {
	ws := publicationWorkspace(t, map[string]any{
		"files_to_create": []string{"docs/spikes/42-x.md"},
	})
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed || gr.TerminalKind != TerminalKindNotPipelineActionable {
		t.Fatalf("want not_pipeline_actionable failure; passed=%v kind=%q reason=%q", gr.Passed, gr.TerminalKind, gr.Reason)
	}
	if len(gr.Evidence) == 0 || !strings.Contains(gr.Evidence[0], "docs/spikes/**") {
		t.Fatalf("evidence must name the deny rule, got %v", gr.Evidence)
	}
}

// An existing file the plan modifies already passed the boundary on main, so
// it is never re-asked; only paths the change would add are checked.
func TestFeaturePlanningGate_PublicationIgnoresExistingModifiedFiles(t *testing.T) {
	ws := publicationWorkspace(t, map[string]any{
		"files_to_modify": []string{"internal/existing.go"},
	})
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

func TestFeaturePlanningGate_PublicationChecksNewModifiedPaths(t *testing.T) {
	ws := publicationWorkspace(t, map[string]any{
		"files_to_modify": []string{"docs/spikes/42-new.md"},
	})
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed || gr.TerminalKind != TerminalKindNotPipelineActionable {
		t.Fatalf("a not-yet-existing modify path must be checked; passed=%v kind=%q", gr.Passed, gr.TerminalKind)
	}
}

// TestFeaturePlanningGate_EmptyPlan (#3354): a plan naming no file fails as
// not_pipeline_actionable quoting its approach, unless it is verify-and-close;
// one planned file passes.
func TestFeaturePlanningGate_EmptyPlan(t *testing.T) {
	ws := t.TempDir()
	planFile := filepath.Join(layouttest.PlansDir(t, ws), "42-thing.md")
	writeFile(t, planFile, "# plan\n")
	ctxPath := filepath.Join(layouttest.PipelineDir(t, ws), "planning-42.json")

	writeJSON(t, ctxPath, map[string]any{
		"plan_file": planFile, "approach": "record via empty commit",
		"files_to_create": []string{}, "files_to_modify": []string{},
	})
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed || gr.TerminalKind != TerminalKindNotPipelineActionable || gr.Reason != emptyPlanReason {
		t.Fatalf("empty plan: passed=%v kind=%q reason=%q", gr.Passed, gr.TerminalKind, gr.Reason)
	}
	// The marker is what the extension's text classifier reads (#3354).
	if !strings.HasPrefix(gr.Reason, "[not-pipeline-actionable] ") {
		t.Fatalf("reason %q lacks the not-pipeline-actionable marker", gr.Reason)
	}
	if len(gr.Evidence) == 0 || gr.Evidence[0] != "approach: record via empty commit" {
		t.Fatalf("evidence %v does not quote the approach", gr.Evidence)
	}

	writeJSON(t, ctxPath, map[string]any{"plan_file": planFile, "approach": "verify-and-close"})
	if gr := (FeaturePlanningGate{}).Verify(context.Background(), 42, ws); !gr.Passed {
		t.Fatalf("verify-and-close failed: %q", gr.Reason)
	}

	writeJSON(t, ctxPath, map[string]any{
		"plan_file": planFile, "approach": "x", "files_to_modify": []string{"a.go"},
	})
	if gr := (FeaturePlanningGate{}).Verify(context.Background(), 42, ws); !gr.Passed {
		t.Fatalf("one-file plan failed: %q %v", gr.Reason, gr.Evidence)
	}
}

// planSizeWorkspace writes a plan listing n files, sized planSize, for an
// issue labelled issueSize, with config as .nightgauge/config.yaml (#3684).
func planSizeWorkspace(t *testing.T, n int, approach, planSize, issueSize, config string) string {
	t.Helper()
	ws := t.TempDir()
	planFile := filepath.Join(layouttest.PlansDir(t, ws), "42-thing.md")
	writeFile(t, planFile, "# plan\n")
	files := make([]string, n)
	for i := range files {
		files[i] = fmt.Sprintf("internal/f%d.go", i)
	}
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "planning-42.json"), map[string]any{
		"plan_file": planFile, "approach": approach, "files_to_modify": files,
		"complexity_assessment": map[string]any{"size_label": planSize},
	})
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "issue-42.json"), map[string]any{
		"labels": []string{"type:feature", "size:" + issueSize},
	})
	if config != "" {
		writeFile(t, filepath.Join(ws, ".nightgauge", "config.yaml"), config)
	}
	return ws
}

func TestFeaturePlanningGate_OversizedPlanNeedsDecomposition(t *testing.T) {
	cases := []struct {
		name      string
		files     int
		approach  string
		planSize  string
		issueSize string
		config    string
		pass      bool
	}{
		{"over default threshold", 26, "x", "M", "M", "", false},
		{"at default threshold", 25, "x", "M", "M", "", true},
		{"size mismatch", 3, "x", "L", "M", "", false},
		{"max_planned_files 0", 500, "x", "M", "M",
			"pipeline:\n  size_gate:\n    thresholds:\n      max_planned_files: 0\n", true},
		{"size_gate disabled", 500, "x", "XL", "S", "pipeline:\n  size_gate:\n    enabled: false\n", true},
		{"verify-and-close", 0, "verify-and-close", "XL", "S", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := planSizeWorkspace(t, tc.files, tc.approach, tc.planSize, tc.issueSize, tc.config)
			gr := FeaturePlanningGate{}.Verify(context.Background(), 42, ws)
			if gr.Passed != tc.pass {
				t.Fatalf("passed=%v, want %v; reason=%q evidence=%v", gr.Passed, tc.pass, gr.Reason, gr.Evidence)
			}
			if tc.pass {
				return
			}
			if gr.TerminalKind != TerminalKindNeedsDecomposition {
				t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindNeedsDecomposition)
			}
			if !strings.HasPrefix(gr.Reason, NeedsDecompositionMarker+" ") {
				t.Errorf("reason %q lacks the %s marker", gr.Reason, NeedsDecompositionMarker)
			}
			want := []string{
				fmt.Sprintf("planned_files=%d", tc.files), "max_planned_files=25",
				"plan_size=" + tc.planSize, "issue_size=" + tc.issueSize,
			}
			if strings.Join(gr.Evidence, "|") != strings.Join(want, "|") {
				t.Errorf("evidence = %v, want %v", gr.Evidence, want)
			}
		})
	}
}

// A configured threshold reaches both the verdict and the evidence.
func TestFeaturePlanningGate_ConfiguredPlannedFilesThreshold(t *testing.T) {
	cfg := "pipeline:\n  size_gate:\n    thresholds:\n      max_planned_files: 10\n"
	gr := FeaturePlanningGate{}.Verify(context.Background(), 42, planSizeWorkspace(t, 11, "x", "M", "M", cfg))
	if gr.Passed || gr.TerminalKind != TerminalKindNeedsDecomposition {
		t.Fatalf("11 files over 10: passed=%v kind=%q", gr.Passed, gr.TerminalKind)
	}
	if !strings.Contains(strings.Join(gr.Evidence, "|"), "max_planned_files=10") {
		t.Errorf("evidence %v lacks the configured threshold", gr.Evidence)
	}
	if gr := (FeaturePlanningGate{}).Verify(context.Background(), 42, planSizeWorkspace(t, 10, "x", "M", "M", cfg)); !gr.Passed {
		t.Errorf("10 files at 10 failed: %q", gr.Reason)
	}
}

func TestIssueSizeLabel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue-42.json")
	if got := issueSizeLabel(path); got != "" {
		t.Errorf("missing file: %q, want empty", got)
	}
	writeFile(t, path, "{not json")
	if got := issueSizeLabel(path); got != "" {
		t.Errorf("malformed file: %q, want empty", got)
	}
	writeJSON(t, path, map[string]any{"labels": []map[string]string{{"name": "type:feature"}, {"name": "size:S"}}})
	if got := issueSizeLabel(path); got != "S" {
		t.Errorf("object labels: %q, want S", got)
	}
	writeJSON(t, path, map[string]any{"labels": []string{"size:M"}})
	if got := issueSizeLabel(path); got != "M" {
		t.Errorf("string labels: %q, want M", got)
	}
}

// A missing issue context only disables the mismatch arm; the count arm
// still stops an oversized plan.
func TestFeaturePlanningGate_MissingIssueContext(t *testing.T) {
	ws := planSizeWorkspace(t, 3, "x", "XL", "S", "")
	if err := os.Remove(filepath.Join(layouttest.PipelineDir(t, ws), "issue-42.json")); err != nil {
		t.Fatal(err)
	}
	if gr := (FeaturePlanningGate{}).Verify(context.Background(), 42, ws); !gr.Passed {
		t.Errorf("no issue size must not fire the mismatch arm: %q", gr.Reason)
	}
}
