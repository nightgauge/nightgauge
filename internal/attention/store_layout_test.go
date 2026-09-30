package attention

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// The attention store is per-checkout runtime state (ADR-024 § 7): cards land
// in CHECKOUT/attention, never in the working tree.
func TestStoreLivesInCheckout(t *testing.T) {
	root := layouttest.Repo(t)
	s := New(root)
	if err := s.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	if want := layouttest.CheckoutPath(t, root, layout.CheckoutAttention); s.Dir() != want {
		t.Fatalf("Dir = %q, want %q", s.Dir(), want)
	}
	req := validRequest(mustID(t), "k:layout")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), req.ID+".json")); err != nil {
		t.Fatalf("card not in CHECKOUT/attention: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".nightgauge")); !os.IsNotExist(err) {
		t.Fatalf("Raise wrote into the working tree (stat err %v)", err)
	}
}

// Outside a git checkout there is no CHECKOUT: every operation reports the
// resolution error and nothing is written where the store was pointed.
func TestStoreOutsideAGitCheckoutRefuses(t *testing.T) {
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
