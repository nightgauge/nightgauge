package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// The pipeline state directory lives under the git common dir (ADR-024 § 7),
// so every root of one clone — the checkout, a Go-manager worktree, a legacy
// in-tree worktree, the VSCode extension's worktree — resolves to the SAME
// issue-{N}.json. The candidate list therefore collapses to that one path
// after de-duplication, whichever worktree layout the run used (#994).

// issueContextFile is the one path every root of root's clone resolves to.
func issueContextFile(t *testing.T, root string) string {
	t.Helper()
	return filepath.Join(layouttest.PipelineDir(t, root), "issue-42.json")
}

// TestIssueContextCandidates_RepoRootResolvesUnderTheGitDir: a run that never
// took a worktree reads the context from the clone's pipeline state directory.
func TestIssueContextCandidates_RepoRootResolvesUnderTheGitDir(t *testing.T) {
	root := layouttest.Repo(t)
	got := IssueContextCandidates(root, "", "acme/widget", 42)
	want := issueContextFile(t, root)
	if len(got) != 1 || got[0] != want {
		t.Errorf("candidates = %v, want exactly [%s]", got, want)
	}
}

// TestIssueContextCandidates_WorktreeSharesTheCloneFile pins the list against
// the function that actually CREATES the worktree: a run in the Go manager's
// worktree reads and writes the same file as the checkout, so the list has
// exactly one entry and it is the clone's.
func TestIssueContextCandidates_WorktreeSharesTheCloneFile(t *testing.T) {
	hermeticConfigHome(t)
	root := initTestGitRepo(t, "main")
	m := &Manager{workspaceRoot: root}
	created := mustWorktreePath(t, m, "acme/widget", 42)
	if err := os.MkdirAll(filepath.Dir(created), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "worktree", "add", "--detach", created)

	want := issueContextFile(t, root)
	if got := issueContextFile(t, created); got != want {
		t.Fatalf("worktree resolves %s, want the clone's %s", got, want)
	}
	for _, wt := range []string{"", created} {
		got := IssueContextCandidates(root, wt, "acme/widget", 42)
		if len(got) != 1 || got[0] != want {
			t.Errorf("worktreeDir=%q: candidates = %v, want exactly [%s]", wt, got, want)
		}
	}
}

// TestIssueContextCandidates_ExplicitWorktreeWins guards ordering: a caller
// that KNOWS the run's worktree has it searched first, even when the repo root
// it was given belongs to another clone.
func TestIssueContextCandidates_ExplicitWorktreeWins(t *testing.T) {
	root := layouttest.Repo(t)
	other := layouttest.Repo(t)
	got := IssueContextCandidates(root, other, "acme/widget", 42)
	want := []string{issueContextFile(t, other), issueContextFile(t, root)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

// TestIssueContextCandidates_DegradesWithoutRepo proves the list still works
// for callers that do not know the repo name.
func TestIssueContextCandidates_DegradesWithoutRepo(t *testing.T) {
	root := layouttest.Repo(t)
	got := IssueContextCandidates(root, "", "", 42)
	want := issueContextFile(t, root)
	if len(got) != 1 || got[0] != want {
		t.Errorf("candidates = %v, want exactly [%s]", got, want)
	}
}

// TestIssueContextCandidates_NotARepositoryHasNoCandidates: a root the
// pipeline state directory cannot be resolved for names no path, rather than
// one in the working tree the data no longer lives in.
func TestIssueContextCandidates_NotARepositoryHasNoCandidates(t *testing.T) {
	dir := t.TempDir()
	if got := IssueContextCandidates(dir, dir, "acme/widget", 42); len(got) != 0 {
		t.Errorf("expected no candidates outside a repository, got %v", got)
	}
}

// TestIssueContextCandidates_EmptyRootsAreSafe guards the degenerate call.
func TestIssueContextCandidates_EmptyRootsAreSafe(t *testing.T) {
	if got := IssueContextCandidates("", "", "", 42); len(got) != 0 {
		t.Errorf("expected no candidates with no roots, got %v", got)
	}
}
