package attention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// legacyStore is a store over an older build's per-checkout directory, for
// seeding the cards the migration must move.
func legacyStore(dir string) *Store { return &Store{dir: dir, now: time.Now} }

func raiseInto(t *testing.T, s *Store, key string) string {
	t.Helper()
	req := validRequest(mustID(t), key)
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise %s: %v", key, err)
	}
	return req.ID
}

func ids(reqs []DecisionRequest) map[string]bool {
	out := make(map[string]bool, len(reqs))
	for _, r := range reqs {
		out[r.ID] = true
	}
	return out
}

// An older build's per-checkout cards, journal and streak counts move into the
// workspace store on first use, and the emptied directory is removed (#3453
// AC 2: nothing stays stranded in a checkout).
func TestMigrationMovesCheckoutCardsIntoTheWorkspaceStore(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	checkoutStore := layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention)

	old := legacyStore(checkoutStore)
	a := raiseInto(t, old, "k:a")
	b := raiseInto(t, old, "k:b")
	for i := 0; i < 2; i++ {
		if _, err := old.IncrementStreak("k:streak"); err != nil {
			t.Fatalf("IncrementStreak: %v", err)
		}
	}

	s := New(root)
	if err := s.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	got, err := s.List(ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if have := ids(got); len(got) != 2 || !have[a] || !have[b] {
		t.Fatalf("workspace store holds %v, want the two legacy cards %s and %s", have, a, b)
	}
	if _, err := os.Stat(checkoutStore); !os.IsNotExist(err) {
		t.Errorf("legacy store %s still exists after the migration (stat err %v)", checkoutStore, err)
	}
	journal, err := s.ReadJournal()
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	created := map[string]bool{}
	for _, e := range journal {
		if e.Action == ActionCreated {
			created[e.ID] = true
		}
	}
	if !created[a] || !created[b] {
		t.Errorf("machine journal is missing the legacy created entries: %v", created)
	}
	if n := s.StreakCount("k:streak"); n != 2 {
		t.Errorf("StreakCount after migration = %d, want 2", n)
	}
}

// A legacy open card for a condition the workspace store already has open is
// the same condition: it is dropped, not shown twice. A terminal card with the
// same key is history and is kept.
func TestMigrationDropsAnOpenDuplicateAndKeepsHistory(t *testing.T) {
	layouttest.StateHome(t)
	repos := layouttest.WorkspaceRepos(t, 2)
	daemon := New(repos[0])
	live := raiseInto(t, daemon, "k:dup")

	other := repos[1]
	old := legacyStore(layouttest.CheckoutPath(t, other, layout.LegacyCheckoutAttention))
	// A resolved predecessor of the same key, then the open duplicate.
	resolved := raiseInto(t, old, "k:dup")
	path := filepath.Join(old.dir, resolved+".json")
	req, err := readRequest(path)
	if err != nil {
		t.Fatal(err)
	}
	req.Lifecycle.State = StateResolved
	if err := old.writeMaterializedLocked(path, req); err != nil {
		t.Fatal(err)
	}
	dup := raiseInto(t, old, "k:dup")

	s := New(other)
	if err := s.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	open, err := s.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if have := ids(open); len(open) != 1 || !have[live] {
		t.Fatalf("open cards = %v, want only the workspace store's %s (legacy duplicate %s dropped)", have, live, dup)
	}
	all, err := s.List(ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ids(all)[resolved] {
		t.Errorf("the resolved legacy card %s was not migrated", resolved)
	}
	if _, err := os.Stat(old.dir); !os.IsNotExist(err) {
		t.Errorf("legacy store still exists (stat err %v)", err)
	}
}

// A card a previous, interrupted migration already copied is not copied
// twice, and running the migration again is a no-op. The card is terminal, so
// only the id check (not the open-key dedup) can keep the copy in dst.
func TestMigrationIsIdempotent(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	src := layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention)
	old := legacyStore(src)
	id := raiseInto(t, old, "k:once")
	srcPath := filepath.Join(src, id+".json")
	req, err := readRequest(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	req.Lifecycle.State = StateResolved
	if err := old.writeMaterializedLocked(srcPath, req); err != nil {
		t.Fatal(err)
	}

	dst, err := workspaceStoreDir(root)
	if err != nil {
		t.Fatal(err)
	}
	// Interrupted after the copy, before the source file was removed; the
	// copy in dst has since moved on.
	req.Title = "already in the workspace store"
	if err := (&Store{dir: dst, now: time.Now}).writeMaterializedLocked(filepath.Join(dst, id+".json"), req); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		migratedSources.Delete(src)
		if err := migrateCheckoutStore(root, dst); err != nil {
			t.Fatalf("migration run %d: %v", i+1, err)
		}
	}
	all, err := New(root).List(ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != id {
		t.Fatalf("workspace store = %v, want exactly %s", ids(all), id)
	}
	if all[0].Title != "already in the workspace store" {
		t.Errorf("the workspace store's copy was overwritten: title %q", all[0].Title)
	}
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Errorf("the source copy was not removed (stat err %v)", err)
	}
}

// A symlink where a legacy store sat is refused, never followed: layout
// refuses a symlinked CHECKOUT entry before the migration reads it.
func TestMigrationRefusesASymlinkedLegacyStore(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	outside := t.TempDir()
	raiseInto(t, legacyStore(outside), "k:outside")
	if err := os.Symlink(outside, layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention)); err != nil {
		t.Fatal(err)
	}
	// The refusal is reported, but it does not close the workspace store.
	if err := migrateCheckoutStore(root, t.TempDir()); !errors.Is(err, layout.ErrUnsafeCloneDir) {
		t.Fatalf("migrateCheckoutStore = %v, want ErrUnsafeCloneDir", err)
	}
	if err := New(root).Err(); err != nil {
		t.Fatalf("New: Err = %v, want the store open despite the refused migration", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the migration emptied the symlink's target: %v %v", entries, err)
	}
}

// The pre-ADR-024 working-tree .nightgauge/attention is repository content:
// a cloned repository must not be able to plant cards in the shared store.
func TestMigrationIgnoresTheWorkingTreeStore(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	planted := filepath.Join(root, ".nightgauge", "attention")
	id := raiseInto(t, legacyStore(planted), "k:planted")

	got, err := New(root).List(ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	if ids(got)[id] {
		t.Fatalf("a card committed to the working tree reached the workspace store")
	}
	if _, err := os.Stat(filepath.Join(planted, id+".json")); err != nil {
		t.Errorf("the working-tree file was touched: %v", err)
	}
}

// A file that does not parse, or whose id does not name it, is dropped rather
// than copied into the shared store.
func TestMigrationDropsMalformedAndMisnamedCards(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	src := layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention)
	good := raiseInto(t, legacyStore(src), "k:good")
	if err := os.WriteFile(filepath.Join(src, "dr_0000000000-garbage.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(src, good+".json"))
	if err != nil {
		t.Fatal(err)
	}
	misnamed := "dr_00000000-0000-7000-8000-000000000000.json"
	if err := os.WriteFile(filepath.Join(src, misnamed), data, 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(root)
	if err := s.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	var cards []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			cards = append(cards, e.Name())
		}
	}
	if len(cards) != 1 || cards[0] != good+".json" {
		t.Fatalf("workspace store files = %v, want only %s.json", cards, good)
	}
}

// Two openers can race to migrate the same checkout after an upgrade (the
// daemon and a CLI command). The loser finds the source gone and succeeds.
func TestMigrationToleratesAConcurrentOpener(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	src := layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention)
	raiseInto(t, legacyStore(src), "k:race")
	dst, err := workspaceStoreDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := migrateOneStore(src, dst); err != nil {
			t.Fatalf("migrateOneStore run %d: %v", i+1, err)
		}
	}

	root2 := layouttest.Repo(t)
	src2 := layouttest.CheckoutPath(t, root2, layout.LegacyCheckoutAttention)
	race2 := raiseInto(t, legacyStore(src2), "k:race2")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = New(root2).Err()
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
	}
	// New reports a migration failure on stderr, not through Err, so check the
	// outcome: the card moved exactly once and the source is gone.
	all, err := New(root2).List(ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != race2 {
		t.Fatalf("workspace store after the race = %v, want exactly %s", ids(all), race2)
	}
	if _, err := os.Stat(src2); !os.IsNotExist(err) {
		t.Errorf("legacy store %s still exists after the race (stat err %v)", src2, err)
	}
}

// A linked worktree's CHECKOUT is inside its own git dir, so it held its own
// legacy store. New(worktree) moves those cards into the workspace store that
// the main checkout opens too.
func TestMigrationMovesAWorktreesCardsIntoTheWorkspaceStore(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	gittest.InitialCommit(t, root)
	worktree := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, root, "worktree", "add", "-q", worktree)
	src := layouttest.CheckoutPath(t, worktree, layout.LegacyCheckoutAttention)
	id := raiseInto(t, legacyStore(src), "k:worktree")

	if err := New(worktree).Err(); err != nil {
		t.Fatalf("New(worktree): %v", err)
	}
	got, err := New(root).List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !ids(got)[id] {
		t.Fatalf("the worktree's legacy card %s is not in the workspace store: %v", id, ids(got))
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("the worktree's legacy store %s still exists (stat err %v)", src, err)
	}
}

// Streak counts for a key both stores hold merge to the higher count, in
// either direction.
func TestMigrationMergesStreaksByHigherCount(t *testing.T) {
	layouttest.StateHome(t)
	repos := layouttest.WorkspaceRepos(t, 2)
	daemon := New(repos[0])
	bump := func(s *Store, key string, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := s.IncrementStreak(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	bump(daemon, "k:src-higher", 1)
	bump(daemon, "k:dst-higher", 4)

	root := repos[1]
	old := legacyStore(layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention))
	bump(old, "k:src-higher", 3)
	bump(old, "k:dst-higher", 2)

	s := New(root)
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if n := s.StreakCount("k:src-higher"); n != 3 {
		t.Errorf("k:src-higher = %d, want 3", n)
	}
	if n := s.StreakCount("k:dst-higher"); n != 4 {
		t.Errorf("k:dst-higher = %d, want 4", n)
	}
}

// NewAt refuses a directory that is not absolute.
func TestNewAtRefusesARelativeDirectory(t *testing.T) {
	for _, dir := range []string{"", "rel/dir"} {
		if err := NewAt(dir).Err(); err == nil {
			t.Errorf("NewAt(%q).Err() = nil, want an error", dir)
		}
	}
}

// A migration that cannot take the workspace store's lock does not close the
// store: New opens it, leaves the legacy cards where they are and does not mark
// the source done, so the next New migrates them (#3453: the documented
// failure-and-retry contract).
func TestMigrationRetriesAfterABusyStore(t *testing.T) {
	layouttest.StateHome(t)
	root := layouttest.Repo(t)
	checkoutStore := layouttest.CheckoutPath(t, root, layout.LegacyCheckoutAttention)
	id := raiseInto(t, legacyStore(checkoutStore), "k:busy")
	card := filepath.Join(checkoutStore, id+".json")

	dst, err := workspaceStoreDir(root)
	if err != nil {
		t.Fatal(err)
	}
	prev := migrateLockWait
	migrateLockWait = 50 * time.Millisecond
	t.Cleanup(func() { migrateLockWait = prev })

	release, err := acquireDirCtx(context.Background(), dst)
	if err != nil {
		t.Fatalf("acquireDirCtx: %v", err)
	}
	if err := migrateCheckoutStore(root, dst); !errors.Is(err, ErrStoreBusy) {
		release()
		t.Fatalf("migration while the store is locked = %v, want ErrStoreBusy", err)
	}
	if busy := New(root); busy.Err() != nil || busy.Dir() != dst {
		release()
		t.Fatalf("New while the store is locked: Err = %v, Dir = %q; want the store open at %s",
			busy.Err(), busy.Dir(), dst)
	}
	if _, err := os.Stat(card); err != nil {
		release()
		t.Fatalf("legacy card %s did not survive the failed migration: %v", card, err)
	}
	if _, done := migratedSources.Load(checkoutStore); done {
		release()
		t.Fatalf("the failed migration marked %s done, so no later New would retry it", checkoutStore)
	}
	release()

	s := New(root)
	if err := s.Err(); err != nil {
		t.Fatalf("New after the lock was released: %v", err)
	}
	got, err := s.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !ids(got)[id] {
		t.Fatalf("the retry did not move %s into the workspace store: %v", id, ids(got))
	}
	if _, err := os.Stat(checkoutStore); !os.IsNotExist(err) {
		t.Errorf("legacy store %s still exists after the retry (stat err %v)", checkoutStore, err)
	}
}
