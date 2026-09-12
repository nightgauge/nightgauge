package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// committingCodexAdapter impersonates the Codex CLI the way the leak needs: it
// is named "codex", so RunStage provisions the managed steering block into the
// worktree's AGENTS.md, and its "agent" does what a real Codex feature-dev
// stage does — stage everything and commit — while the block is present.
type committingCodexAdapter struct{}

func (committingCodexAdapter) Name() string { return "codex" }
func (committingCodexAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "/bin/sh", []string{"-c", "echo work > feature.txt && git add -A && git commit -qm 'feat: work'"}, nil
}
func (committingCodexAdapter) UsesStdin() bool { return false }
func (committingCodexAdapter) Agentic() bool   { return true }

// TestRunStage_CodexStageCommitNeverPublishesSteering_1675 reproduces the
// observed leak. Before the fix the Go-direct path wrote the managed block and
// never removed it, so the agent's `git add -A && git commit` published it and
// every later commit and push carried it: HEAD:AGENTS.md held the markers after
// the stage. The fix strips the working tree after the stage and repairs the
// commit the agent made, keeping the agent's own work.
func TestRunStage_CodexStageCommitNeverPublishesSteering_1675(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	repoRoot := initTestGitRepo(t, "main")
	const user = "# Project contract\n\nUser-authored rules.\n"
	if err := os.WriteFile(filepath.Join(repoRoot, "AGENTS.md"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repoRoot, "add", "AGENTS.md")
	gittest.Run(t, repoRoot, "commit", "-qm", "docs: contract")

	m := NewManager(repoRoot, committingCodexAdapter{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := m.RunStage(ctx, StageOptions{
		Repo: "nightgauge/nightgauge", IssueNumber: 1675, Stage: "feature-dev", Timeout: 30 * time.Second,
	}); err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	wt := m.worktreePath("nightgauge/nightgauge", 1675)

	show := func(ref string) string {
		out, err := gittest.Command(wt, "show", ref).Output()
		if err != nil {
			t.Fatalf("git show %s: %v", ref, err)
		}
		return string(out)
	}
	if head := show("HEAD:AGENTS.md"); strings.Contains(head, "NIGHTGAUGE MANAGED STEERING") {
		t.Fatalf("generated steering was committed and left at the branch tip:\n%s", head)
	}
	if got := show("HEAD:AGENTS.md"); got != user {
		t.Errorf("the user's AGENTS.md must survive byte-for-byte, got %q", got)
	}
	if got := show("HEAD:feature.txt"); got != "work\n" {
		t.Errorf("the agent's own work must stay committed, got %q", got)
	}
	disk, err := os.ReadFile(filepath.Join(wt, "AGENTS.md"))
	if err != nil || string(disk) != user {
		t.Errorf("working-tree AGENTS.md = %q (err %v), want the user content", disk, err)
	}
	if st, _ := gittest.Command(wt, "status", "--porcelain").Output(); strings.TrimSpace(string(st)) != "" {
		t.Errorf("worktree must be clean after the repair, got:\n%s", st)
	}
}
