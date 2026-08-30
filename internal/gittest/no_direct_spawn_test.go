package gittest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// directSpawnNeedle is the banned construction — a bare git subprocess built
// outside this package. It is assembled at run time from two pieces so that
// this file, which is scanned like every other test file in the module, does
// not contain the literal it searches for and report itself as an offender.
var directSpawnNeedle = `exec.Command(` + `"git"`

// exemptFromDirectSpawnBan lists the test files allowed to build a bare git
// subprocess. There is exactly one, and it is this package's own demonstration
// of the background-gc mechanism the rest of the tree exists to avoid: it must
// spawn git WITHOUT the disarming in order to show what the disarming
// prevents (see TestBackgroundGitProcessCanOutliveItsParent).
var exemptFromDirectSpawnBan = map[string]bool{
	filepath.Join("internal", "gittest", "gittest_test.go"): true,
}

// TestNoDirectGitSpawnsInTests is the ratchet for #680/#688/#1160. Migrating
// the tree's test files onto this package fixes the files that exist today;
// nothing stops the next test from copy-pasting a bare git spawn back in, and
// the failure that reintroduces is a ~1-in-250 red check on a diff that cannot
// have caused it — the most expensive kind of regression to diagnose and the
// least likely to be caught in review.
//
// The check is deliberately textual rather than AST-based: the property being
// pinned is "no test file constructs a git subprocess outside this package",
// and a grep-shaped rule is the same rule #688's acceptance criteria state, so
// the guard and the spec cannot drift apart.
func TestNoDirectGitSpawnsInTests(t *testing.T) {
	root := moduleRoot(t)

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", ".worktrees", "dist", "out":
				return filepath.SkipDir
			}
			// A nested checkout (an agent worktree under .claude/worktrees, a
			// vendored clone) is not this module's source; walking into it
			// would make the guard's verdict depend on what happens to be
			// lying around the machine — the #851 shape.
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if exemptFromDirectSpawnBan[rel] {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(src), directSpawnNeedle) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("%d test file(s) spawn git directly instead of through internal/gittest:\n  %s\n\n"+
			"A bare %s, ...) leaves git's background housekeeping armed (gc.auto fires from "+
			"ordinary porcelain, gc.autoDetach defaults true so the child outlives its parent) "+
			"and inherits the operator's /etc/gitconfig, which CI does not sanitise. The detached "+
			"child races t.TempDir()'s cleanup and the half-built .git/worktrees/<id>/ that "+
			"`git worktree add` needs. Use gittest.Run / gittest.Command / gittest.InitRepo.",
			len(offenders), strings.Join(offenders, "\n  "), directSpawnNeedle)
	}
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod. Tests run with the package directory as cwd, so this is the only way
// to reach the whole module without hard-coding a relative depth that breaks
// the moment the package moves.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
