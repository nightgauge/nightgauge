package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

func gitCleanupRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gittest.Command(dir, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func initCleanupWireRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCleanupRun(t, dir, "init", "-b", "main")
	gitCleanupRun(t, dir, "config", "user.email", "test@test")
	gitCleanupRun(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCleanupRun(t, dir, "add", ".")
	gitCleanupRun(t, dir, "commit", "-m", "initial")
	return dir
}

// TestGitCleanupMergedBranches_DoesNotNeedAnExecutionManager pins the defect
// #1013 fixed.
//
// The verb used to reach CleanupMergedBranches through Server.execMgr — a field
// written by exactly one function, WithExecutionManager, which has ZERO callers.
// The serve daemon never passes it. So this verb answered "execution manager not
// initialized" on every invocation the extension ever made, and the extension's
// caller logged and continued: cleanup was silently skipped on every activation.
//
// A server built the way production builds one — no execution manager — must now
// answer this verb.
func TestGitCleanupMergedBranches_DoesNotNeedAnExecutionManager(t *testing.T) {
	repo := initCleanupWireRepo(t)
	// Exactly how cmd/nightgauge builds it: no WithExecutionManager.
	s := NewServer(nil, WithWorkspaceRoot(repo))

	if s.execMgr != nil {
		t.Fatal("this test is meaningless if the server HAS an execution manager")
	}

	handler, ok := s.methods["git.cleanupMergedBranches"]
	if !ok {
		t.Fatal("git.cleanupMergedBranches is not registered")
	}

	res, err := handler(t.Context(), nil)
	if err != nil {
		t.Fatalf("the verb failed on a server with no execution manager: %v\n"+
			"That is the #1013 defect: cleanup needs a git checkout, not a stage runner.", err)
	}
	out, ok := res.(GitCleanupMergedBranchesResult)
	if !ok {
		t.Fatalf("result type = %T, want GitCleanupMergedBranchesResult", res)
	}
	// A fresh repo has no gone branches — the point is that it SCANNED and
	// answered, not that it deleted anything.
	if out.Count != len(out.Deleted) {
		t.Errorf("Count=%d disagrees with Deleted=%v", out.Count, out.Deleted)
	}
}

// TestGitCleanupMergedBranches_HonoursWorkDir pins the parameter the old
// implementation declared and ignored.
func TestGitCleanupMergedBranches_HonoursWorkDir(t *testing.T) {
	// The server's own root is a NON-repo, so a verb that ignores WorkDir and
	// falls back to the workspace root cannot succeed.
	s := NewServer(nil, WithWorkspaceRoot(t.TempDir()))
	repo := initCleanupWireRepo(t)

	handler := s.methods["git.cleanupMergedBranches"]
	res, err := handler(t.Context(), []byte(`{"workDir":"`+repo+`"}`))
	if err != nil {
		t.Fatalf("the verb ignored workDir and fell back to a non-repo root: %v", err)
	}
	if _, ok := res.(GitCleanupMergedBranchesResult); !ok {
		t.Fatalf("result type = %T", res)
	}
}
