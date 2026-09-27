package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// #2194: a run's knowledge base is the MAIN checkout's, whether the manager
// was launched at the main checkout or inside a pipeline worktree.
func TestRunKnowledgeDir_ResolvesMainCheckoutFromWorktree(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(base, "repo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, main, "init", "-b", "main")
	gittest.Run(t, main, "config", "user.email", "t@t")
	gittest.Run(t, main, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, main, "add", ".")
	gittest.Run(t, main, "commit", "-m", "i")
	wt := filepath.Join(main, ".nightgauge", "worktrees", "issue-1")
	gittest.Run(t, main, "worktree", "add", wt, "-b", "feat/1-x")

	want := filepath.Join(main, ".nightgauge", "knowledge")
	for name, got := range map[string]string{
		"main workspace root":     runKnowledgeDir(main, wt),
		"worktree workspace root": runKnowledgeDir(wt, wt),
		"no workspace root":       runKnowledgeDir("", wt),
	} {
		if got != want {
			t.Errorf("%s: runKnowledgeDir = %q, want %q", name, got, want)
		}
	}
}
