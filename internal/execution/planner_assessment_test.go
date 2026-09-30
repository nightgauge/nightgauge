package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func writePlan(t *testing.T, root string, issue int, body string) {
	t.Helper()
	path := filepath.Join(layouttest.MkPipelineDir(t, root), planningContextName(issue))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPlannerAssessment_SizeLabel(t *testing.T) {
	root := layouttest.Repo(t)
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
	root := layouttest.Repo(t)
	writePlan(t, root, 149, `{"complexity_assessment":{"size_label":null,"fibonacci_score":3}}`)

	got := LoadPlannerAssessment(root, "", "acme/widget", 149)
	if got.SizeLabel != "" {
		t.Errorf("SizeLabel = %q, want \"\" — the plan wrote null", got.SizeLabel)
	}
	if got.Score != 3 {
		t.Errorf("Score = %d, want 3 from fibonacci_score", got.Score)
	}
}

// The worktree is searched before the repo root — the same rule
// IssueContextCandidates follows. Every worktree of one clone shares the
// clone's pipeline state directory (ADR-024 § 7), so a run in a linked
// worktree reads the plan the checkout's run wrote; an explicit worktree of a
// different clone is the most specific candidate.
func TestLoadPlannerAssessment_SearchesWorktreeLayouts(t *testing.T) {
	root := initTestGitRepo(t, "main")
	writePlan(t, root, 5, `{"complexity_assessment":{"size_label":"XS"}}`)

	extWorktree := filepath.Join(t.TempDir(), "issue-5")
	gittest.Run(t, root, "worktree", "add", "--detach", extWorktree)
	if got := LoadPlannerAssessment(root, extWorktree, "acme/widget", 5); got.SizeLabel != "XS" {
		t.Errorf("SizeLabel = %q, want \"XS\" — a linked worktree shares the clone's plan", got.SizeLabel)
	}
	writePlan(t, extWorktree, 5, `{"complexity_assessment":{"size_label":"XL"}}`)
	if got := LoadPlannerAssessment(root, "", "acme/widget", 5); got.SizeLabel != "XL" {
		t.Errorf("SizeLabel = %q, want \"XL\" — the worktree wrote the clone's one file", got.SizeLabel)
	}

	explicit := layouttest.Repo(t)
	writePlan(t, explicit, 5, `{"complexity_assessment":{"size_label":"S"}}`)
	if got := LoadPlannerAssessment(root, explicit, "acme/widget", 5); got.SizeLabel != "S" {
		t.Errorf("SizeLabel = %q, want \"S\" — an explicit worktree is the most specific candidate", got.SizeLabel)
	}
}

// Absence stays absence: a missing or malformed plan must not invent a size,
// because the resolution's next source can only be reached through "".
func TestLoadPlannerAssessment_AbsentAndMalformed(t *testing.T) {
	root := layouttest.Repo(t)
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
