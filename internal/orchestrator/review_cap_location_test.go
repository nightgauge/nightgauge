package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

func runCapGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gittest.Run(t, dir, args...)
}

// reviewCapFixture builds an origin with main, a clone, and a worktree on the
// issue branch feat/3059-x holding `commits` commits beyond origin/main.
func reviewCapFixture(t *testing.T, commits int) (checkout, worktree string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	checkout = filepath.Join(root, "clone")
	worktree = filepath.Join(root, "wt")
	runCapGit(t, root, "init", "-q", "--bare", origin)
	runCapGit(t, root, "clone", "-q", origin, checkout)
	runCapGit(t, checkout, "checkout", "-q", "-b", "main")
	runCapGit(t, checkout, "commit", "-q", "--allow-empty", "-m", "base")
	runCapGit(t, checkout, "push", "-q", "origin", "main")
	runCapGit(t, checkout, "remote", "set-head", "origin", "main")
	runCapGit(t, checkout, "worktree", "add", "-q", "-b", "feat/3059-x", worktree)
	for i := 0; i < commits; i++ {
		runCapGit(t, worktree, "commit", "-q", "--allow-empty", "-m", "work")
	}
	return checkout, worktree
}

func renderLocation(loc reviewCapLocation) string {
	var b strings.Builder
	writeReviewCapLocation(&b, loc)
	return b.String()
}

func TestReviewCapLocationWorktreePresentWithUncommitted(t *testing.T) {
	checkout, wt := reviewCapFixture(t, 1)
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(wt, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loc := probeReviewCapLocation(checkout, wt, 3059)
	if loc.Err != nil || !loc.WorktreeExists || loc.Uncommitted != 2 {
		t.Fatalf("probe = %+v", loc)
	}
	body := renderLocation(loc)
	for _, want := range []string{"still exists and holds 2 uncommitted path(s).",
		"Branch `feat/3059-x` holds 1 commit(s) beyond `origin/main`."} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestReviewCapLocationWorktreeRemovedWithCommits(t *testing.T) {
	checkout, wt := reviewCapFixture(t, 3)
	runCapGit(t, checkout, "worktree", "remove", wt)
	loc := probeReviewCapLocation(checkout, wt, 3059)
	if loc.Err != nil || loc.WorktreeExists {
		t.Fatalf("probe = %+v", loc)
	}
	body := renderLocation(loc)
	for _, want := range []string{"no longer exists, so the work is not there.",
		"Branch `feat/3059-x` holds 3 commit(s) beyond `origin/main`."} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "uncommitted") {
		t.Errorf("body claims uncommitted work in a removed worktree:\n%s", body)
	}
}

func TestReviewCapLocationWorktreeRemovedNoCommits(t *testing.T) {
	checkout, wt := reviewCapFixture(t, 0)
	runCapGit(t, checkout, "worktree", "remove", wt)
	body := renderLocation(probeReviewCapLocation(checkout, wt, 3059))
	want := "No work was found: `feat/3059-x` holds no commits beyond `origin/main`."
	if !strings.Contains(body, want) || strings.Contains(body, "uncommitted") {
		t.Errorf("body = %q, want %q and no uncommitted claim", body, want)
	}
}

func TestReviewCapLocationGitFailureClaimsNothing(t *testing.T) {
	notGit := t.TempDir()
	loc := probeReviewCapLocation(notGit, filepath.Join(notGit, "gone"), 3059)
	if loc.Err == nil {
		t.Fatalf("probe of a non-git checkout returned no error: %+v", loc)
	}
	body := renderLocation(loc)
	if !strings.Contains(body, "could not be read") || strings.Contains(body, "uncommitted") ||
		strings.Contains(body, "commit(s)") {
		t.Errorf("git-failure body makes a claim:\n%s", body)
	}
}

func TestReviewCapRemediationPointsToCard(t *testing.T) {
	text := TerminalKindRemediation(TerminalKindReviewCapReached)
	if strings.Contains(text, "uncommitted in") || !strings.Contains(text, "card") {
		t.Errorf("remediation = %q", text)
	}
}

func TestReviewCapLocationDetachedWorktreeStillNamesTheBranch(t *testing.T) {
	checkout, wt := reviewCapFixture(t, 2)
	runCapGit(t, wt, "checkout", "-q", "--detach")
	body := renderLocation(probeReviewCapLocation(checkout, wt, 3059))
	want := "Branch `feat/3059-x` holds 2 commit(s) beyond `origin/main`."
	if !strings.Contains(body, want) || strings.Contains(body, "No local branch") {
		t.Errorf("detached-worktree body = %q, want %q", body, want)
	}
}
