package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// A warn-mode match from a command run in a subdirectory logs to the clone's
// logs directory, never to a .nightgauge/ created under the subdirectory.
func TestLogWarnEventResolvesTheCloneLogsDir(t *testing.T) {
	root := t.TempDir()
	gittest.InitRepo(t, root)
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	logWarnEvent(&PatternMatch{Category: CategoryDestructive, Pattern: "rm -rf"}, "rm -rf /", sub)

	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(resolved, ".nightgauge", "logs", "sanitization.log")); err != nil {
		t.Errorf("expected the log under the clone root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, ".nightgauge")); !os.IsNotExist(err) {
		t.Errorf("a .nightgauge/ was created under the subdirectory (err=%v)", err)
	}
}

// Outside a git work tree there is no clone to log to: nothing is written.
func TestLogWarnEventOutsideARepoWritesNothing(t *testing.T) {
	dir := t.TempDir()
	logWarnEvent(&PatternMatch{Category: CategoryDestructive, Pattern: "rm -rf"}, "rm -rf /", dir)
	if _, err := os.Stat(filepath.Join(dir, ".nightgauge")); !os.IsNotExist(err) {
		t.Errorf("a .nightgauge/ was created outside any repository (err=%v)", err)
	}
}
