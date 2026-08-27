package execution

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestIssueContextCandidates_CoversBothWorktreeLayouts is the guard for the
// defect's root cause: two writers, two worktree layouts, and every reader
// knowing at most one of them (#994).
func TestIssueContextCandidates_CoversBothWorktreeLayouts(t *testing.T) {
	got := IssueContextCandidates("/repo", "", "acme/widget", 42)
	joined := strings.Join(got, "\n")

	// Go manager layout — must match worktreePath's construction exactly, or
	// the search misses every worktree the Go scheduler creates.
	goLayout := filepath.Join("/repo", ".nightgauge", "worktrees", "widget-issue-42",
		".nightgauge", "pipeline", "issue-42.json")
	if !strings.Contains(joined, goLayout) {
		t.Errorf("Go manager worktree layout missing.\nwant a path containing: %s\ngot:\n%s", goLayout, joined)
	}

	// VSCode extension layout.
	vsLayout := filepath.Join("/repo", ".worktrees", "issue-42",
		".nightgauge", "pipeline", "issue-42.json")
	if !strings.Contains(joined, vsLayout) {
		t.Errorf("VSCode worktree layout missing.\nwant a path containing: %s\ngot:\n%s", vsLayout, joined)
	}

	// The plain repo root — a run that never took a worktree.
	rootLayout := filepath.Join("/repo", ".nightgauge", "pipeline", "issue-42.json")
	if !strings.Contains(joined, rootLayout) {
		t.Errorf("plain repo root missing.\nwant: %s\ngot:\n%s", rootLayout, joined)
	}
}

// TestIssueContextCandidates_MatchesWorktreePath pins the shared list against
// the function that actually CREATES the directory. A list that merely looks
// right is worthless: the two must agree byte for byte, or the search misses
// the file on exactly the runs it was added for.
func TestIssueContextCandidates_MatchesWorktreePath(t *testing.T) {
	m := &Manager{workspaceRoot: "/repo"}
	created := m.worktreePath("acme/widget", 42)
	want := filepath.Join(created, IssueContextRelPath(42))

	for _, got := range IssueContextCandidates("/repo", "", "acme/widget", 42) {
		if got == want {
			return
		}
	}
	t.Errorf("worktreePath produces %s, but no candidate matches %s", created, want)
}

// TestIssueContextCandidates_ExplicitWorktreeWins guards ordering: a caller
// that KNOWS the run's worktree must have it searched first, so a stale file at
// the repo root cannot shadow the live one.
func TestIssueContextCandidates_ExplicitWorktreeWins(t *testing.T) {
	got := IssueContextCandidates("/repo", "/explicit/wt", "acme/widget", 42)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	want := filepath.Join("/explicit/wt", ".nightgauge", "pipeline", "issue-42.json")
	if got[0] != want {
		t.Errorf("first candidate = %s, want the explicit worktree %s", got[0], want)
	}
}

// TestIssueContextCandidates_DegradesWithoutRepo proves the list still works
// for callers that do not know the repo name — it simply cannot name the
// Go-manager layout, which is honest rather than wrong.
func TestIssueContextCandidates_DegradesWithoutRepo(t *testing.T) {
	got := IssueContextCandidates("/repo", "", "", 42)
	if len(got) == 0 {
		t.Fatal("no candidates without a repo name")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, filepath.Join("/repo", ".worktrees", "issue-42")) {
		t.Error("VSCode layout should still be enumerated without a repo name")
	}
	if strings.Contains(joined, "-issue-42") {
		t.Error("a Go-manager path was emitted without a repo name to build it from")
	}
}

// TestIssueContextCandidates_NoDuplicates keeps the search from stat-ing the
// same path twice when the explicit worktree equals a derived one.
func TestIssueContextCandidates_NoDuplicates(t *testing.T) {
	wt := filepath.Join("/repo", ".worktrees", "issue-42")
	got := IssueContextCandidates("/repo", wt, "acme/widget", 42)
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("duplicate candidate: %s", p)
		}
		seen[p] = true
	}
}

// TestIssueContextCandidates_EmptyRootsAreSafe guards the degenerate call.
func TestIssueContextCandidates_EmptyRootsAreSafe(t *testing.T) {
	if got := IssueContextCandidates("", "", "", 42); len(got) != 0 {
		t.Errorf("expected no candidates with no roots, got %v", got)
	}
}
