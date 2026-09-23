package ipc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// TestClearBlockedFindingRefusesUnresolvedRoot: with no absolute repository
// root, the finding's path cannot be resolved, so clearing must fail rather
// than report "already gone" and resolve a card whose hold may still be on
// disk (#2033).
func TestClearBlockedFindingRefusesUnresolvedRoot(t *testing.T) {
	for _, root := range []string{"", "relative/repo"} {
		c := blockedFindingClearer{server: &Server{workspaceRoot: root}}
		if err := c.ClearBlockedFinding(context.Background(), "o/r", 7); err == nil {
			t.Errorf("root %q: ClearBlockedFinding = nil, want an error", root)
		}
	}
}

// TestClearBlockedFindingRemovesAndToleratesAbsent: with an absolute root the
// file is removed, and a second clear of the now-absent file still succeeds.
func TestClearBlockedFindingRemovesAndToleratesAbsent(t *testing.T) {
	root := t.TempDir()
	path := orchestrator.BlockedFindingPath(root, 7)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := blockedFindingClearer{server: &Server{workspaceRoot: root}}
	for i := 0; i < 2; i++ {
		if err := c.ClearBlockedFinding(context.Background(), "o/r", 7); err != nil {
			t.Fatalf("clear #%d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("finding still present after clear: %v", err)
	}
}
