package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testStateHome is the machine-state root a test pairs with its fake home: the
// macOS default layout, <home>/.nightgauge/state (layout.StateHomePathFrom).
// A test that runs production code resolving the root from the environment
// sets NIGHTGAUGE_STATE_HOME to it with setTestHome.
func testStateHome(home string) string {
	return filepath.Join(home, ".nightgauge", "state")
}

// setTestHome points HOME at home and NIGHTGAUGE_STATE_HOME at its state
// root, so a run root, a dispatch record or evidence the code under test
// writes lands under home and never in the operator's state directory.
func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("NIGHTGAUGE_STATE_HOME", testStateHome(home))
}

// TestPreflightAndIsolationWriteUnderTheStateHome (#2032): with
// NIGHTGAUGE_STATE_HOME set apart from a fake HOME, the dispatch record, the
// per-run root and the preserved evidence all land under the state root, and
// nothing is written under HOME/.nightgauge. Pointing any of the call sites
// back at the home directory turns this red.
func TestPreflightAndIsolationWriteUnderTheStateHome(t *testing.T) {
	home := preflightEnv(t)
	state := t.TempDir()
	t.Setenv("NIGHTGAUGE_STATE_HOME", state)
	m := openCodeManifestForTest(t)
	fake := installFakeOpenCode(t, fakeOpenCodeBehavior{version: m.MaxTested})
	a := pinnedAdapter(lmStudioSettings(), fake.path)
	run := RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: gitInitTestWorktree(t)}
	if err := a.PreDispatch(context.Background(), run); err != nil {
		t.Fatalf("PreDispatch = %v", err)
	}
	if _, ok, err := ReadOpenCodeDispatchRecord(state); !ok || err != nil {
		t.Errorf("no dispatch record under the state root: %v", err)
	}
	root, err := a.PrepareRunRoot(RunRootRequest{ID: testRunID, MachineConfigDir: filepath.Join(home, "machine"), Run: run})
	if err != nil {
		t.Fatalf("PrepareRunRoot = %v", err)
	}
	if want := filepath.Join(state, "opencode", "runs", testRunID); root.Dir != want {
		t.Errorf("run root = %s, want %s", root.Dir, want)
	}
	for _, dir := range []string{filepath.Join(state, "opencode"), filepath.Join(state, "opencode", "runs"), root.Dir} {
		if info, err := os.Lstat(dir); err != nil || info.Mode().Perm() != 0o700 || !info.IsDir() {
			t.Errorf("%s: %v (%v), want a 0700 directory", dir, info.Mode(), err)
		}
	}
	db := filepath.Join(root.Dir, "data", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("transcript"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, err := PreserveOpenCodeRunEvidence(state, testRunID, time.Now())
	if err != nil || dst != filepath.Join(state, "opencode", "evidence", testRunID) {
		t.Errorf("evidence = %q, %v; want it under the state root", dst, err)
	}
	if err := RemoveOpenCodeRunRoot(state, testRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".nightgauge")); !os.IsNotExist(err) {
		t.Errorf("HOME/.nightgauge exists (%v); nothing may be written there", err)
	}
}

// TestIsolationRefusesASymlinkedStateDir: a link planted as STATE/opencode or
// STATE/opencode/runs is never written through.
func TestIsolationRefusesASymlinkedStateDir(t *testing.T) {
	for _, planted := range []string{"opencode", "opencode/runs"} {
		t.Run(planted, func(t *testing.T) {
			state, elsewhere := t.TempDir(), t.TempDir()
			link := filepath.Join(state, filepath.FromSlash(planted))
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, _, err := EnsureOpenCodeRunRoot(state, t.TempDir(), testRunID, envLookup(nil)); err == nil {
				t.Error("EnsureOpenCodeRunRoot created a run root through a symlink")
			}
			if err := RecordOpenCodeDispatch(state, OpenCodeDispatchRecord{Binary: "/bin/opencode"}); planted == "opencode" && err == nil {
				t.Error("RecordOpenCodeDispatch wrote through a symlink")
			}
			if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
				t.Errorf("the link target gained %d entr(ies)", len(entries))
			}
		})
	}
}
