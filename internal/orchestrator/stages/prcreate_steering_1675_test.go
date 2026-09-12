package stages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const leakBlock = "<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->\ngenerated\n<!-- END NIGHTGAUGE MANAGED STEERING -->\n"

// TestExecGitClient_CommitAll_StripsSteering_1675 reproduces the leak at the
// pr-create commit owner: `git add -A` staged the live steering block.
func TestExecGitClient_CommitAll_StripsSteering_1675(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Rules\n\n"+leakBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecGitClient().CommitAll(context.Background(), dir, "feat: x", nil); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if head := gitOut(t, dir, "show", "HEAD:AGENTS.md"); strings.Contains(head, "MANAGED STEERING") {
		t.Fatalf("commit published generated steering:\n%s", head)
	}
}

// TestExecGitClient_PushBranch_RepairsCommittedSteering_1675 covers the commit
// the pipeline did not make: a stage's agent committed the block, and the
// deterministic push is the last point before it becomes the PR's tree.
func TestExecGitClient_PushBranch_RepairsCommittedSteering_1675(t *testing.T) {
	remote := t.TempDir()
	gitOut(t, remote, "init", "-q", "--bare", "-b", "main")
	dir := initRepo(t)
	gitOut(t, dir, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Rules\n\n"+leakBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "feat: agent commit with steering present")

	if err := NewExecGitClient().PushBranch(context.Background(), dir, "fix/1179-x"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	pushed := gitOut(t, remote, "show", "fix/1179-x:AGENTS.md")
	if strings.Contains(pushed, "MANAGED STEERING") || !strings.Contains(pushed, "# Rules") {
		t.Fatalf("pushed tip carries generated steering or lost user content:\n%s", pushed)
	}
}
