package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureKBStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("command: %v", runErr)
	}
	return string(out)
}

// #2194: `knowledge scaffold --json` run from a pipeline worktree reports an
// ABSOLUTE knowledge_path into the main checkout. The deferred-scaffold path
// in feature-planning reads that value from the worktree cwd; a relative one
// named the worktree's own gitignored tree, so writes through it vanished.
func TestKnowledgeScaffoldCmd_JSONPathIsAbsoluteMainCheckout(t *testing.T) {
	root, wt := scaffoldWorktreeFixture(t)
	cmd := knowledgeScaffoldCmd()
	cmd.SetArgs([]string{
		"--issue-number", "2194", "--title", "kb from worktrees",
		"--workdir", wt, "--knowledge-enabled", "true", "--workspace-scoped", "false", "--json",
	})
	out := captureKBStdout(t, cmd.Execute)
	var res struct {
		KnowledgePath string `json:"knowledge_path"`
		PRDPath       string `json:"prd_path"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	want := filepath.Join(root, ".nightgauge", "knowledge", "features")
	if !filepath.IsAbs(res.KnowledgePath) || !strings.HasPrefix(res.KnowledgePath, want+string(filepath.Separator)) {
		t.Fatalf("knowledge_path = %q, want absolute under %s", res.KnowledgePath, want)
	}
	if _, err := os.Stat(res.PRDPath); err != nil {
		t.Errorf("prd_path %q does not resolve from any cwd: %v", res.PRDPath, err)
	}
}

// #2194: a lessons/outcome write (retro's record-outcome) run from the
// worktree lands in the main checkout's knowledge base, so removing the
// worktree loses nothing.
func TestKnowledgeRecordOutcomeCmd_FromWorktreeWritesMainCheckout(t *testing.T) {
	root, wt := scaffoldWorktreeFixture(t)
	scaffold := knowledgeScaffoldCmd()
	scaffold.SetArgs([]string{
		"--issue-number", "2194", "--title", "kb from worktrees",
		"--workdir", wt, "--knowledge-enabled", "true", "--workspace-scoped", "false",
	})
	scaffold.SetOut(io.Discard)
	_ = captureKBStdout(t, scaffold.Execute)

	rec := knowledgeRecordOutcomeCmd()
	rec.SetArgs([]string{
		"--issue", "2194", "--status", "complete", "--workdir", wt,
		"--lessons-learned", "resolve the KB from git's common dir",
	})
	rec.SetOut(io.Discard)
	_ = captureKBStdout(t, rec.Execute)

	dirs, _ := filepath.Glob(filepath.Join(root, ".nightgauge", "knowledge", "features", "2194-*"))
	if len(dirs) != 1 {
		t.Fatalf("main checkout KB dirs = %v, want one", dirs)
	}
	data, err := os.ReadFile(filepath.Join(dirs[0], "decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "resolve the KB from git's common dir") {
		t.Errorf("outcome not in the main checkout's decisions.md:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(wt, ".nightgauge", "knowledge")); err == nil {
		t.Errorf("a knowledge base was written inside the worktree")
	}
}
