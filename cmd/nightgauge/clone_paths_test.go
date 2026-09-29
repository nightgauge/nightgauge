package main

import (
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// TestCloneDirKeepsTheRootsForm pins cloneDir to the paths the hand-joins it
// replaced produced: relative in, relative out; absolute in, absolute out.
func TestCloneDirKeepsTheRootsForm(t *testing.T) {
	abs := t.TempDir()
	for _, root := range []string{"", ".", "sub/repo", filepath.Join("..", "repo"), abs} {
		got, err := cloneDir(layout.PipelineStateDir, root)
		if err != nil {
			t.Fatalf("cloneDir(%q): %v", root, err)
		}
		if want := filepath.Join(root, ".nightgauge", "pipeline"); got != want {
			t.Errorf("cloneDir(%q) = %q, want %q", root, got, want)
		}
	}
}
