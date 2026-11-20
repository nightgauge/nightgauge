package attention

import (
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// Every checkout of one workspace opens the same store (#3453): the manifest
// root, a sibling repository its manifest lists, and a linked worktree of that
// sibling. A card raised in any one is listed from the others (AC 3).
func TestStoreIsSharedAcrossTheWorkspacesCheckouts(t *testing.T) {
	layouttest.StateHome(t)
	repos := layouttest.WorkspaceRepos(t, 2)
	root, sibling := repos[0], repos[1]
	gittest.InitialCommit(t, sibling)
	worktree := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, sibling, "worktree", "add", "-q", worktree)

	stores := map[string]*Store{"root": New(root), "sibling": New(sibling), "worktree": New(worktree)}
	for name, s := range stores {
		if err := s.Err(); err != nil {
			t.Fatalf("%s: Err = %v", name, err)
		}
		if s.Dir() != stores["root"].Dir() {
			t.Fatalf("%s resolved %q, the manifest root %q: one workspace, two stores",
				name, s.Dir(), stores["root"].Dir())
		}
	}

	req := validRequest(mustID(t), "k:workspace")
	if _, _, err := stores["worktree"].Raise(req); err != nil {
		t.Fatalf("Raise from the worktree: %v", err)
	}
	for _, name := range []string{"root", "sibling"} {
		got, err := stores[name].List(ListFilter{})
		if err != nil {
			t.Fatalf("List from %s: %v", name, err)
		}
		if len(got) != 1 || got[0].ID != req.ID {
			t.Fatalf("List from %s = %v, want [%s]", name, ids(got), req.ID)
		}
	}
}

// A different workspace on the same machine opens a different store and never
// sees this workspace's cards: the boundary a daemon's resolve, expiry sweep
// and platform upload rely on (ADR-019).
func TestStoreIsSeparatePerWorkspace(t *testing.T) {
	layouttest.StateHome(t)
	mine := New(layouttest.WorkspaceRepos(t, 2)[1])
	other := New(layouttest.WorkspaceRepos(t, 1)[0])
	loner := New(layouttest.Repo(t))
	if mine.Dir() == other.Dir() || mine.Dir() == loner.Dir() || other.Dir() == loner.Dir() {
		t.Fatalf("separate workspaces share a store: %q %q %q", mine.Dir(), other.Dir(), loner.Dir())
	}
	req := validRequest(mustID(t), "k:mine")
	if _, _, err := mine.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	for name, s := range map[string]*Store{"other": other, "loner": loner} {
		got, err := s.List(ListFilter{IncludeTerminal: true})
		if err != nil {
			t.Fatalf("List from %s: %v", name, err)
		}
		if len(got) != 0 {
			t.Fatalf("the %s workspace lists another workspace's card: %v", name, ids(got))
		}
	}
}
