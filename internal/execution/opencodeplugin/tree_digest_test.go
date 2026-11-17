package opencodeplugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/shippedfiles"
)

// TestTreeSHA256MatchesTheSource: TreeSHA256 pins plugin/ (#3383).
func TestTreeSHA256MatchesTheSource(t *testing.T) {
	got, err := shippedfiles.Digest("plugin")
	if err != nil {
		t.Fatal(err)
	}
	if got != TreeSHA256 {
		t.Fatalf("TreeSHA256 = %s; plugin/ is %s: update the constant", TreeSHA256, got)
	}
}

// TestFilesRefusesAnEditedOrMissingTree: a tree beside the binary that is
// not the pinned one never becomes the gate, and a miss names its paths.
func TestFilesRefusesAnEditedOrMissingTree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(shippedfiles.EnvDir, dir)
	if _, err := Files(); err == nil || !strings.Contains(err.Error(), filepath.Join(dir, shippedfiles.OpenCodePlugin)) {
		t.Fatalf("missing tree: err = %v, want a miss naming the paths tried", err)
	}
	tree := filepath.Join(dir, shippedfiles.OpenCodePlugin)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, EntryFile), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(t.TempDir()); err == nil || !strings.Contains(err.Error(), TreeSHA256) {
		t.Fatalf("edited tree: err = %v, want a digest refusal", err)
	}
}
