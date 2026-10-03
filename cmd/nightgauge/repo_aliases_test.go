package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

func writeAliasRepoConfig(t *testing.T, root, owner, repo string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "owner: " + owner + "\ndefaultRepo: " + repo + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorkspaceRepoAliases_DiscoversSiblingsFromALinkedWorktree covers where
// the pickup gate actually runs (#2349): in a pipeline worktree under
// .nightgauge/worktrees/. Discovering the workspace from the worktree itself
// finds only other worktrees as its siblings, so "Blocked by widget-api #12"
// would again resolve to nothing; the main checkout's siblings are the
// workspace.
func TestWorkspaceRepoAliases_DiscoversSiblingsFromALinkedWorktree(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(base, "app")
	widget := filepath.Join(base, "widget-api")
	writeAliasRepoConfig(t, app, "example-org", "app")
	writeAliasRepoConfig(t, widget, "example-org", "widget-api")

	gittest.Run(t, app, "init", "-b", "main")
	gittest.Run(t, app, "config", "user.email", "test@test")
	gittest.Run(t, app, "config", "user.name", "test")
	gittest.Run(t, app, "add", ".")
	gittest.Run(t, app, "commit", "-m", "initial")
	worktree := filepath.Join(app, ".nightgauge", "worktrees", "feat-1-work")
	gittest.Run(t, app, "worktree", "add", worktree, "-b", "feat/1-work")

	for _, root := range []string{app, worktree} {
		aliases := workspaceRepoAliases(root, nil)
		for short, want := range map[string]string{
			"app":        "example-org/app",
			"widget-api": "example-org/widget-api",
		} {
			if got := aliases[short]; got != want {
				t.Errorf("from %s: %q -> %q, want %q (aliases %v)", root, short, got, want, aliases)
			}
		}
	}
}

// The scheduler's repo set and the slugs a caller already holds count even
// where nothing can be discovered on disk.
func TestWorkspaceRepoAliases_IncludesTheRepoSetAndExtraSlugs(t *testing.T) {
	aliases := workspaceRepoAliases("",
		[]depgraph.RepoConfig{{Owner: "example-org", Name: "platform"}, {Name: "no-owner"}},
		"example-org/store")
	for short, want := range map[string]string{
		"platform": "example-org/platform",
		"store":    "example-org/store",
	} {
		if got := aliases[short]; got != want {
			t.Errorf("%q -> %q, want %q (aliases %v)", short, got, want, aliases)
		}
	}
	if got, ok := aliases["no-owner"]; ok {
		t.Errorf("a repo config without an owner named %q; it must be skipped", got)
	}
}
