package adapters

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// skillsKBFixture is a main checkout with a pipeline worktree under
// .nightgauge/worktrees/, the shape every stage runs in (#2191, #2194).
func skillsKBFixture(t *testing.T) (main, wt string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main = filepath.Join(base, "repo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, main, "init", "-b", "main")
	gittest.Run(t, main, "config", "user.email", "t@t")
	gittest.Run(t, main, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(main, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, main, "add", ".")
	gittest.Run(t, main, "commit", "-m", "init")
	wt = filepath.Join(main, ".nightgauge", "worktrees", "issue-7")
	gittest.Run(t, main, "worktree", "add", wt, "-b", "feat/7-x")
	return main, wt
}

func writeSkill(t *testing.T, root, dir string) string {
	t.Helper()
	p := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: x\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func relEdit(t *testing.T, wt, file string) string {
	t.Helper()
	rel, err := filepath.Rel(wt, file)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// The skills root a render resolved is readable, never writable, and the
// knowledge base is readable and writable, for both a bundle skills root
// (a consumer repo: <prefix>/skills beside <prefix>/bin/nightgauge, outside
// every worktree) and a workspace skills root (the core repo's main-checkout
// skills/).
func TestOpenCodePermissionMap_SkillsRootReadOnlyAndKnowledgeBaseWritable(t *testing.T) {
	main, wt := skillsKBFixture(t)
	bundle := filepath.Join(filepath.Dir(main), "nightgauge-dogfood", "skills")
	kb := config.KnowledgeBaseDir(wt)
	if want := filepath.Join(main, ".nightgauge", "knowledge"); kb != want {
		t.Fatalf("KnowledgeBaseDir(worktree) = %q, want the main checkout's %q", kb, want)
	}

	for _, tc := range []struct {
		name, skillsRoot string
	}{
		{"bundle", bundle},
		{"workspace", filepath.Join(main, "skills")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skill := writeSkill(t, tc.skillsRoot, "nightgauge-feature-planning")
			shared := filepath.Join(tc.skillsRoot, "_shared", "KNOWLEDGE.md")
			opts := RunOptions{
				SkillPath:    skill,
				WorktreeDir:  wt,
				KnowledgeDir: kb,
				AllowedTools: []string{"Read", "Edit", "Write", "Bash"},
			}
			perm := openCodePermissionMap(opts, "")

			// #2191: the rendered prompt's _shared read is admitted.
			if got := openCodePatternAction(perm.ExternalDirectory, filepath.Dir(shared)+"/*"); got != openCodeAllow {
				t.Errorf("external_directory for %s = %q, want allow", shared, got)
			}
			// ...read-only: an edit into the skills tree is denied.
			if got := openCodePatternAction(perm.Edit, relEdit(t, wt, shared)); got != openCodeDeny {
				t.Errorf("edit %s = %q, want deny", shared, got)
			}

			// #2194: the knowledge base is read AND write.
			prd := filepath.Join(kb, "features", "7-x", "PRD.md")
			if got := openCodePatternAction(perm.ExternalDirectory, filepath.Dir(prd)+"/*"); got != openCodeAllow {
				t.Errorf("external_directory for %s = %q, want allow", prd, got)
			}
			if got := openCodePatternAction(perm.Edit, relEdit(t, wt, prd)); got != openCodeAllow {
				t.Errorf("edit %s = %q, want allow", prd, got)
			}

			// Nothing else in the main checkout is admitted.
			other := filepath.Join(main, "internal", "x.go")
			if got := openCodePatternAction(perm.ExternalDirectory, filepath.Dir(other)+"/*"); got != openCodeDeny {
				t.Errorf("external_directory for %s = %q, want deny", other, got)
			}
		})
	}
}

// A skills tree inside the worktree is the repository's own source: no
// external entry and no edit deny (the core repo's feature-dev edits skills/).
func TestOpenCodePermissionMap_SkillsRootInsideWorktreeStaysEditable(t *testing.T) {
	_, wt := skillsKBFixture(t)
	skill := writeSkill(t, filepath.Join(wt, "skills"), "nightgauge-feature-dev")
	perm := openCodePermissionMap(RunOptions{SkillPath: skill, WorktreeDir: wt, AllowedTools: []string{"Read", "Edit"}}, "")
	other := filepath.Join(wt, "skills", "nightgauge-pr-create", "SKILL.md")
	if got := openCodePatternAction(perm.Edit, relEdit(t, wt, other)); got != openCodeAllow {
		t.Errorf("edit %s = %q, want allow", other, got)
	}
}

// Claude adapter parity: the knowledge base is added as a working directory,
// and with no skill path the skills flags stay off the command.
func TestClaudeAdapters_AddKnowledgeDir(t *testing.T) {
	opts := RunOptions{KnowledgeDir: "/main/.nightgauge/knowledge", Model: "m"}
	for name, a := range map[string]interface {
		BuildCommand(RunOptions) (string, []string, map[string]string)
	}{"claude": NewClaudeAdapter(), "claude-sdk": NewClaudeSdkAdapter()} {
		_, args, _ := a.BuildCommand(opts)
		if got := flagValues(args, "--add-dir"); !slices.Equal(got, []string{opts.KnowledgeDir}) {
			t.Errorf("%s: --add-dir = %v, want only the knowledge dir", name, got)
		}
		_, args, _ = a.BuildCommand(RunOptions{})
		if got := flagValues(args, "--add-dir"); len(got) != 0 {
			t.Errorf("%s: --add-dir with no knowledge dir: %v", name, got)
		}
	}
}

// TestClaudeAdapters_SkillsTreeIsReadableAndDenied pins #2718: the resolved tree
// is added, not every search root, and the same argv denies file-tool writes.
func TestClaudeAdapters_SkillsTreeIsReadableAndDenied(t *testing.T) {
	opts := RunOptions{
		KnowledgeDir: "/main/.nightgauge/knowledge",
		SkillPath:    "/bundle/dist/skills/nightgauge-feature-dev/SKILL.md",
		WorktreeDir:  "/worktrees/issue-1",
	}
	for name, a := range map[string]interface {
		BuildCommand(RunOptions) (string, []string, map[string]string)
	}{"claude": NewClaudeAdapter(), "claude-sdk": NewClaudeSdkAdapter()} {
		_, args, _ := a.BuildCommand(opts)
		want := []string{opts.KnowledgeDir, "/bundle/dist/skills"}
		if got := flagValues(args, "--add-dir"); !slices.Equal(got, want) {
			t.Errorf("%s: --add-dir = %v, want %v", name, got, want)
		}
		deny := "Edit(//bundle/dist/skills/**),Write(//bundle/dist/skills/**)"
		if got := flagValues(args, "--disallowedTools"); !slices.Equal(got, []string{deny}) {
			t.Errorf("%s: --disallowedTools = %v, want %q", name, got, deny)
		}
	}
}

// TestClaudeSkillsDirArgs_SkipsATreeInsideTheWorktree: a dev-host or override
// render can resolve the worktree's own skills/. Denying edits there would stop
// a stage changing the repository it was dispatched to change.
func TestClaudeSkillsDirArgs_SkipsATreeInsideTheWorktree(t *testing.T) {
	worktree := t.TempDir()
	tree := filepath.Join(worktree, "skills")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := RunOptions{
		SkillPath:   filepath.Join(tree, "nightgauge-feature-dev", "SKILL.md"),
		WorktreeDir: worktree,
	}
	if got := claudeSkillsDirArgs(inside); got != nil {
		t.Errorf("tree inside the worktree: args = %v, want none", got)
	}

	sibling := RunOptions{
		SkillPath:   filepath.Join(worktree+"-other", "skills", "nightgauge-feature-dev", "SKILL.md"),
		WorktreeDir: worktree,
	}
	if got := claudeSkillsDirArgs(sibling); len(got) == 0 {
		t.Error("a sibling whose name extends the worktree's must still be added")
	}
}

func flagValues(args []string, flag string) []string {
	var values []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	return values
}
