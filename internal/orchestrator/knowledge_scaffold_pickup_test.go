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

// Knowledge scaffolding at pickup (#1205).
//
// The scaffold used to be ~100 lines of bash in the issue-pickup skill that
// called `nightgauge knowledge scaffold` from the stage's cwd. On the scheduler
// path that cwd is the run's WORKTREE, so the PRD and decisions landed in
// <worktree>/.nightgauge/knowledge/ — gitignored, and deleted with the worktree
// at reclamation. Every later reader found nothing, and this workspace's root
// knowledge base still ended at 390- after hundreds of runs.
//
// The distinction between the main checkout and a linked worktree IS the bug,
// so the fixture builds a real one rather than two unrelated temp directories:
// against two plain directories the broken and fixed code are indistinguishable.

// worktreeFixture returns (mainCheckout, linkedWorktree) for a real repo.
func worktreeFixture(t *testing.T) (string, string) {
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
	wt := filepath.Join(root, ".worktrees", "issue-1205")
	gittest.Run(t, root, "worktree", "add", wt, "-b", "fix/1205-work")
	return root, wt
}

// writeKnowledgeConfig writes .nightgauge/config.yaml with the given flags.
func writeKnowledgeConfig(t *testing.T, root string, enabled, autoScaffold bool) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// owner/repo are required by config.Load — a knowledge-only stanza does
	// not parse, and a nil cfg would make every assertion below vacuous.
	body := "owner: nightgauge\nrepo: nightgauge\n"
	body += "knowledge:\n"
	body += "  enabled: " + boolLit(enabled) + "\n"
	body += "  auto_scaffold: " + boolLit(autoScaffold) + "\n"
	body += "  workspace_scoped: false\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// writeWorktreeContextFile plants issue-N.json INSIDE the worktree — where the
// pickup skill actually writes it on a worktree-isolated run (#994) — and
// nothing at the root, so a root-only stamp cannot pass by accident.
func writeWorktreeContextFile(t *testing.T, worktree string, issue int) string {
	t.Helper()
	dir := filepath.Join(worktree, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "issue-"+strconv.Itoa(issue)+".json")
	body := `{"schema_version":"1.5","issue_number":` + strconv.Itoa(issue) + `,"knowledge_path":null}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScaffoldKnowledgeAtPickup_LandsAtCanonicalRootNotTheWorktree(t *testing.T) {
	root, wt := worktreeFixture(t)
	writeKnowledgeConfig(t, root, true, true)
	contextPath := writeWorktreeContextFile(t, wt, 1205)

	s := &Scheduler{}
	item := types.BoardItem{Number: 1205, Title: "scaffold lands in the run worktree", Repo: "nightgauge"}
	// workspaceRoot is deliberately the WORKTREE: that is what the scheduler
	// passes on a worktree-isolated run, and passing the root here would
	// exercise a path the bug never took.
	s.scaffoldKnowledgeAtPickup(wt, wt, item)

	rootDirs, _ := filepath.Glob(filepath.Join(root, ".nightgauge", "knowledge", "features", "1205-*"))
	if len(rootDirs) != 1 {
		t.Fatalf("knowledge base at the canonical root = %v, want exactly one 1205-* directory", rootDirs)
	}
	for _, name := range []string{"PRD.md", "decisions.md"} {
		if _, err := os.Stat(filepath.Join(rootDirs[0], name)); err != nil {
			t.Errorf("%s missing from the scaffold: %v", name, err)
		}
	}

	// Nothing in the worktree. This is the assertion that goes red when the
	// MainCheckoutRoot call is removed.
	wtDirs, _ := filepath.Glob(filepath.Join(wt, ".nightgauge", "knowledge", "features", "1205-*"))
	if len(wtDirs) != 0 {
		t.Errorf("scaffold landed in the worktree at %v — it is reclaimed with the "+
			"worktree after merge, which is exactly #1205", wtDirs)
	}

	// knowledge_path must be stamped into the WORKTREE-resident context file,
	// and must be absolute: the stages read it from the worktree while the
	// sidebar and retro read it from the root, and only an absolute path is
	// correct from both.
	raw, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read context file: %v", err)
	}
	var ctxData map[string]interface{}
	if err := json.Unmarshal(raw, &ctxData); err != nil {
		t.Fatalf("parse context file: %v", err)
	}
	kp, _ := ctxData["knowledge_path"].(string)
	if kp == "" {
		t.Fatal("knowledge_path was not stamped into the context file")
	}
	if !filepath.IsAbs(kp) {
		t.Errorf("knowledge_path = %q, want an absolute path", kp)
	}
	if kp != rootDirs[0] {
		t.Errorf("knowledge_path = %q, want the root-anchored scaffold %q", kp, rootDirs[0])
	}
	if _, err := os.Stat(filepath.Join(kp, "PRD.md")); err != nil {
		t.Errorf("knowledge_path does not resolve to a readable PRD: %v", err)
	}
	// The context file must still be valid JSON with its other fields intact.
	if got, _ := ctxData["schema_version"].(string); got != "1.5" {
		t.Errorf("schema_version = %q after stamping, want 1.5 — the rewrite "+
			"must patch one field, not replace the document", got)
	}
}

func TestScaffoldKnowledgeAtPickup_AutoScaffoldFalseDoesNothing(t *testing.T) {
	root, wt := worktreeFixture(t)
	writeKnowledgeConfig(t, root, true, false)
	contextPath := writeWorktreeContextFile(t, wt, 1205)

	s := &Scheduler{}
	s.scaffoldKnowledgeAtPickup(wt, wt, types.BoardItem{Number: 1205, Title: "t", Repo: "nightgauge"})

	if dirs, _ := filepath.Glob(filepath.Join(root, ".nightgauge", "knowledge", "features", "1205-*")); len(dirs) != 0 {
		t.Errorf("auto_scaffold=false still scaffolded %v", dirs)
	}
	raw, _ := os.ReadFile(contextPath)
	var ctxData map[string]interface{}
	_ = json.Unmarshal(raw, &ctxData)
	if kp, _ := ctxData["knowledge_path"].(string); kp != "" {
		t.Errorf("auto_scaffold=false stamped knowledge_path = %q, want none", kp)
	}
}

func TestScaffoldKnowledgeAtPickup_KnowledgeDisabledDoesNothing(t *testing.T) {
	root, wt := worktreeFixture(t)
	// enabled=false with auto_scaffold=true: the gate is the parent flag, so a
	// project that opted out gets nothing written into its tree.
	writeKnowledgeConfig(t, root, false, true)
	writeWorktreeContextFile(t, wt, 1205)

	s := &Scheduler{}
	s.scaffoldKnowledgeAtPickup(wt, wt, types.BoardItem{Number: 1205, Title: "t", Repo: "nightgauge"})

	if dirs, _ := filepath.Glob(filepath.Join(root, ".nightgauge", "knowledge", "features", "1205-*")); len(dirs) != 0 {
		t.Errorf("knowledge.enabled=false still scaffolded %v", dirs)
	}
}

// A second pickup of the same issue (a retry, a re-dispatch) must not fail or
// duplicate — and must still stamp the path, since the first run's context file
// is gone with its worktree.
func TestScaffoldKnowledgeAtPickup_IsIdempotent(t *testing.T) {
	root, wt := worktreeFixture(t)
	writeKnowledgeConfig(t, root, true, true)
	contextPath := writeWorktreeContextFile(t, wt, 1205)

	s := &Scheduler{}
	item := types.BoardItem{Number: 1205, Title: "scaffold lands in the run worktree", Repo: "nightgauge"}
	s.scaffoldKnowledgeAtPickup(wt, wt, item)
	s.scaffoldKnowledgeAtPickup(wt, wt, item)

	dirs, _ := filepath.Glob(filepath.Join(root, ".nightgauge", "knowledge", "features", "1205-*"))
	if len(dirs) != 1 {
		t.Fatalf("after two pickups: %v, want exactly one directory", dirs)
	}
	raw, _ := os.ReadFile(contextPath)
	var ctxData map[string]interface{}
	_ = json.Unmarshal(raw, &ctxData)
	if kp, _ := ctxData["knowledge_path"].(string); kp != dirs[0] {
		t.Errorf("knowledge_path = %q after a rerun, want %q", kp, dirs[0])
	}
}

// No context file (the pickup skill has not written it yet, or the run was
// interrupted) must not lose the scaffold or panic — it is an enrichment.
func TestScaffoldKnowledgeAtPickup_MissingContextFileIsNonFatal(t *testing.T) {
	root, wt := worktreeFixture(t)
	writeKnowledgeConfig(t, root, true, true)

	s := &Scheduler{}
	s.scaffoldKnowledgeAtPickup(wt, wt, types.BoardItem{Number: 1205, Title: "t", Repo: "nightgauge"})

	if dirs, _ := filepath.Glob(filepath.Join(root, ".nightgauge", "knowledge", "features", "1205-*")); len(dirs) != 1 {
		t.Errorf("scaffold = %v with no context file to stamp, want it created anyway", dirs)
	}
}
