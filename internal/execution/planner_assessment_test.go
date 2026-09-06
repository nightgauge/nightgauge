package execution

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlan(t *testing.T, root string, issue int, body string) {
	t.Helper()
	path := filepath.Join(root, PlanningContextRelPath(issue))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPlannerAssessment_SizeLabel(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, 1429, `{"complexity_assessment":{"size_label":"l","computed_score":5}}`)

	got := LoadPlannerAssessment(root, "", "acme/widget", 1429)
	if got.SizeLabel != "L" {
		t.Errorf("SizeLabel = %q, want \"L\" (normalized)", got.SizeLabel)
	}
	if got.Score != 5 {
		t.Errorf("Score = %d, want 5", got.Score)
	}
	if !got.Assessed() {
		t.Error("Assessed() = false for a plan that assessed a size")
	}
}

// Real plans in this workspace's history spell the score `fibonacci_score`
// while the schema names it `computed_score`. Reading only one spelling would
// report "the planner assessed nothing" for a plan that assessed a size —
// exactly the blindness this loader exists to remove.
func TestLoadPlannerAssessment_AcceptsFibonacciScoreSpelling(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, 149, `{"complexity_assessment":{"size_label":null,"fibonacci_score":3}}`)

	got := LoadPlannerAssessment(root, "", "acme/widget", 149)
	if got.SizeLabel != "" {
		t.Errorf("SizeLabel = %q, want \"\" — the plan wrote null", got.SizeLabel)
	}
	if got.Score != 3 {
		t.Errorf("Score = %d, want 3 from fibonacci_score", got.Score)
	}
}

// The worktree is searched before the repo root, and the two worktree layouts
// are both known — the same rule IssueContextCandidates follows. A reader that
// knew one layout would report "absent" for every run of the other path.
func TestLoadPlannerAssessment_SearchesWorktreeLayouts(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, 5, `{"complexity_assessment":{"size_label":"XS"}}`)

	extWorktree := filepath.Join(root, ".worktrees", "issue-5")
	writePlan(t, extWorktree, 5, `{"complexity_assessment":{"size_label":"XL"}}`)

	if got := LoadPlannerAssessment(root, "", "acme/widget", 5); got.SizeLabel != "XL" {
		t.Errorf("SizeLabel = %q, want \"XL\" — the extension worktree outranks the repo root", got.SizeLabel)
	}

	goWorktree := filepath.Join(root, ".nightgauge", "worktrees", "widget-issue-5")
	writePlan(t, goWorktree, 5, `{"complexity_assessment":{"size_label":"M"}}`)
	if got := LoadPlannerAssessment(root, "", "acme/widget", 5); got.SizeLabel != "M" {
		t.Errorf("SizeLabel = %q, want \"M\" — the Go manager worktree outranks the extension's", got.SizeLabel)
	}

	explicit := t.TempDir()
	writePlan(t, explicit, 5, `{"complexity_assessment":{"size_label":"S"}}`)
	if got := LoadPlannerAssessment(root, explicit, "acme/widget", 5); got.SizeLabel != "S" {
		t.Errorf("SizeLabel = %q, want \"S\" — an explicit worktree is the most specific candidate", got.SizeLabel)
	}
}

// Absence stays absence: a missing or malformed plan must not invent a size,
// because the resolution's next source can only be reached through "".
func TestLoadPlannerAssessment_AbsentAndMalformed(t *testing.T) {
	root := t.TempDir()
	if got := LoadPlannerAssessment(root, "", "acme/widget", 99); got.Assessed() {
		t.Errorf("missing plan yielded %+v, want the zero value", got)
	}
	writePlan(t, root, 98, `{not json`)
	if got := LoadPlannerAssessment(root, "", "acme/widget", 98); got.Assessed() {
		t.Errorf("malformed plan yielded %+v, want the zero value", got)
	}
	writePlan(t, root, 97, `{"approach":"x"}`)
	if got := LoadPlannerAssessment(root, "", "acme/widget", 97); got.Assessed() {
		t.Errorf("plan with no complexity_assessment yielded %+v, want the zero value", got)
	}
}
