package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// TestCloneDirResolvesEveryRootFormToTheClassDir pins cloneDir to the
// resolver's absolute answer: an empty, relative or absolute root inside one
// clone names the same directory under the git common dir (ADR-024 § 7).
func TestCloneDirResolvesEveryRootFormToTheClassDir(t *testing.T) {
	repo := layouttest.Repo(t)
	if err := os.MkdirAll(filepath.Join(repo, "sub", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	want := layouttest.PipelineDir(t, repo)
	for _, root := range []string{"", ".", filepath.Join("sub", "pkg"), repo} {
		got, err := cloneDir(layout.PipelineStateDir, root)
		if err != nil {
			t.Fatalf("cloneDir(%q): %v", root, err)
		}
		if got != want {
			t.Errorf("cloneDir(%q) = %q, want %q", root, got, want)
		}
	}
}

// Outside a git repository there is no clone: the resolver's error comes back
// instead of a path somewhere else.
func TestCloneDirOutsideARepositoryIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, root := range []string{"", dir} {
		got, err := cloneDir(layout.PipelineStateDir, root)
		if !errors.Is(err, layout.ErrNotGitRepository) {
			t.Errorf("cloneDir(%q) = %q, %v; want ErrNotGitRepository", root, got, err)
		}
	}
}
