package attention

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// newTestStore is a store over a fresh repository, under a machine-state root
// of the test's own: the store is machine state (#3453), so tests sharing the
// binary's STATE could collide on it.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	layouttest.StateHome(t)
	return New(layouttest.Repo(t))
}

// The attention store is machine state (#3453): cards land in
// STATE/attention/<workspace-key>, never in the checkout's git dir or its
// working tree.
func TestStoreLivesInMachineState(t *testing.T) {
	state := layouttest.StateHome(t)
	root := layouttest.Repo(t)
	s := New(root)
	if err := s.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	if want := filepath.Join(state, layout.StateAttention); filepath.Dir(s.Dir()) != want ||
		len(filepath.Base(s.Dir())) != workspaceKeyLen {
		t.Fatalf("Dir = %q, want %s/<%d-char workspace key>", s.Dir(), want, workspaceKeyLen)
	}
	req := validRequest(mustID(t), "k:layout")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), req.ID+".json")); err != nil {
		t.Fatalf("card not in STATE/attention: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".nightgauge")); !os.IsNotExist(err) {
		t.Fatalf("Raise wrote into the working tree (stat err %v)", err)
	}
	if _, err := os.Stat(layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention)); !os.IsNotExist(err) {
		t.Fatalf("Raise wrote a per-checkout store (stat err %v)", err)
	}
}

// A symlink where the attention parent belongs is refused, never followed.
func TestStoreRefusesASymlinkedAttentionRoot(t *testing.T) {
	state := layouttest.StateHome(t)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(state, layout.StateAttention)); err != nil {
		t.Fatal(err)
	}
	s := New(layouttest.Repo(t))
	if !errors.Is(s.Err(), ErrUnsafeStoreDir) {
		t.Fatalf("Err = %v, want ErrUnsafeStoreDir", s.Err())
	}
}

// Outside a git checkout every operation reports the resolution error and
// nothing is written where the store was pointed.
func TestStoreOutsideAGitCheckoutRefuses(t *testing.T) {
	layouttest.StateHome(t)
	root := t.TempDir()
	s := New(root)
	if !errors.Is(s.Err(), layout.ErrNotGitRepository) {
		t.Fatalf("Err = %v, want ErrNotGitRepository", s.Err())
	}
	if _, _, err := s.Raise(validRequest(mustID(t), "k:nogit")); err == nil {
		t.Fatal("Raise: want an error outside a git checkout")
	}
	if _, err := s.List(ListFilter{}); err == nil {
		t.Fatal("List: want an error outside a git checkout")
	}
	if _, err := s.IncrementStreak("k"); err == nil {
		t.Fatal("IncrementStreak: want an error outside a git checkout")
	}
	if _, err := os.Stat(filepath.Join(root, ".nightgauge")); !os.IsNotExist(err) {
		t.Fatalf("the store wrote into a non-git directory (stat err %v)", err)
	}
}
