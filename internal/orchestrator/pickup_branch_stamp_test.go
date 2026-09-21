package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// Recovering a branch the pickup skill lost between shells (#1919).
//
// The skill assigns BRANCH_NAME in its branch-creation phase and reads it back
// several phases later, in a DIFFERENT shell. A shell variable does not survive
// between tool calls, so `${BRANCH_NAME:-}` wrote "" and the post-condition gate
// called an otherwise-complete stage a no-op — then the orchestrator escalated
// and paid for the same stage on a larger model, for a branch that existed in
// git the whole time.
//
// The fixture is a REAL linked worktree checked out on the feature branch,
// because "what HEAD names in the run's worktree" is the entire mechanism; two
// plain temp directories cannot tell the fix from the bug.

// branchStampFixture returns (mainCheckout, worktree) with the worktree checked
// out on branchName.
func branchStampFixture(t *testing.T, branchName string) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "init", "-b", "main")
	gittest.Run(t, root, "config", "user.email", "test@test")
	gittest.Run(t, root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-m", "initial")
	wt := filepath.Join(root, ".worktrees", "issue-under-test")
	gittest.Run(t, root, "worktree", "add", wt, "-b", branchName)
	return root, wt
}

// writeContextWithBranch plants issue-N.json inside the worktree carrying the
// given branch value (pass "" for the field the lost variable produces, or
// omit it entirely with absent=true).
func writeContextWithBranch(t *testing.T, worktree string, issue int, branch string, absent bool) string {
	t.Helper()
	dir := filepath.Join(worktree, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "issue-"+strconv.Itoa(issue)+".json")
	body := `{"schema_version":"1.5","issue_number":` + strconv.Itoa(issue) + `,"branch":"` + branch + `"}`
	if absent {
		body = `{"schema_version":"1.5","issue_number":` + strconv.Itoa(issue) + `}`
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readContextBranch(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read context file: %v", err)
	}
	var ctx struct {
		Branch      string `json:"branch"`
		IssueNumber int    `json:"issue_number"`
	}
	if err := json.Unmarshal(raw, &ctx); err != nil {
		t.Fatalf("context file is not valid JSON after the stamp: %v", err)
	}
	if ctx.IssueNumber == 0 {
		t.Error("issue_number was lost by the rewrite — the stamp must preserve every other field")
	}
	return ctx.Branch
}

func TestStampFeatureBranchAtPickup_RecoversEmptyBranchFromHEAD(t *testing.T) {
	root, wt := branchStampFixture(t, "fix/1919-pickup-branch")
	path := writeContextWithBranch(t, wt, 1919, "", false)

	stampFeatureBranchAtPickup(root, wt, types.BoardItem{Number: 1919, Repo: "nightgauge"})

	if got := readContextBranch(t, path); got != "fix/1919-pickup-branch" {
		t.Errorf("branch = %q, want it recovered from the worktree's HEAD", got)
	}
}

func TestStampFeatureBranchAtPickup_RecoversAbsentBranchField(t *testing.T) {
	root, wt := branchStampFixture(t, "feat/1919-pickup-branch")
	path := writeContextWithBranch(t, wt, 1919, "", true)

	stampFeatureBranchAtPickup(root, wt, types.BoardItem{Number: 1919, Repo: "nightgauge"})

	if got := readContextBranch(t, path); got != "feat/1919-pickup-branch" {
		t.Errorf("branch = %q, want the absent field filled from HEAD", got)
	}
}

func TestStampFeatureBranchAtPickup_NeverOverwritesABranchTheSkillWrote(t *testing.T) {
	// Including a wrong-looking one: an empty slug is #1915's defect, and
	// silently rewriting it here would hide that bug rather than fix it.
	root, wt := branchStampFixture(t, "fix/1919-full-slug")
	path := writeContextWithBranch(t, wt, 1919, "fix/1919-", false)

	stampFeatureBranchAtPickup(root, wt, types.BoardItem{Number: 1919, Repo: "nightgauge"})

	if got := readContextBranch(t, path); got != "fix/1919-" {
		t.Errorf("branch = %q, want the skill's own value left untouched", got)
	}
}

func TestStampFeatureBranchAtPickup_StampsNothingWhenHEADIsNotThisIssuesBranch(t *testing.T) {
	// The worktree never left the base branch: no branch was created, so the
	// stage really did produce nothing and the gate must still fail it.
	root, wt := branchStampFixture(t, "chore/4242-unrelated")
	path := writeContextWithBranch(t, wt, 1919, "", false)

	stampFeatureBranchAtPickup(root, wt, types.BoardItem{Number: 1919, Repo: "nightgauge"})

	if got := readContextBranch(t, path); got != "" {
		t.Errorf("branch = %q, want nothing stamped from another issue's branch", got)
	}
}

func TestStampFeatureBranchAtPickup_NoContextFileIsNotAPanic(t *testing.T) {
	root, wt := branchStampFixture(t, "fix/1919-no-context")
	// Deliberately no context file: the skill wrote nothing at all, which is a
	// different gate verdict ("issue context file missing") and not this
	// function's business.
	stampFeatureBranchAtPickup(root, wt, types.BoardItem{Number: 1919, Repo: "nightgauge"})
}
