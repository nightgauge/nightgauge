package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
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
	if _, err := os.Stat(filepath.Join(layouttest.LogsDir(t, resolved), "sanitization.log")); err != nil {
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

// The log records flagged commands, so it is private to the user: created
// mode 0600, and a log left at 0644 is tightened on the next append
// (ADR-024 § 11).
func TestLogWarnEventWritesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	root := t.TempDir()
	gittest.InitRepo(t, root)
	match := &PatternMatch{Category: CategoryDestructive, Pattern: "rm -rf"}

	logWarnEvent(match, "rm -rf /", root)
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(layouttest.LogsDir(t, resolved), "sanitization.log")
	assertMode := func(want os.FileMode) {
		t.Helper()
		info, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("sanitization.log mode = %o, want %o", got, want)
		}
	}
	assertMode(0o600)

	if err := os.Chmod(logPath, 0o644); err != nil {
		t.Fatal(err)
	}
	logWarnEvent(match, "rm -rf /", root)
	assertMode(0o600)
}
