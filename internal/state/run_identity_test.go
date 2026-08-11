package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// ADR-017 step 1 — the state layer.
//
// Fixture provenance rule for this file: every snapshot a test reads back is
// written by the PRODUCTION writer (Persist / PersistExisting / SealAndRemove).
// The one sanctioned class of hand-authored fixture is a CORRUPT or LEGACY
// file — a shape production can no longer produce — and each such fixture says
// so at its construction site.

// ---------------------------------------------------------------------------
// Decision 1 — the identity is a constructor fact, and Persist refuses what it
// cannot name.
// ---------------------------------------------------------------------------

func TestNewRuntimeState_StoresTheIdentityItIsGiven_AndDoesNotValidate(t *testing.T) {
	id := testRunID()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", id)
	if rs.RunID != id {
		t.Errorf("RunID = %q, want %q", rs.RunID, id)
	}

	// The constructor deliberately does NOT validate: identity-less
	// construction stays legal for setPaused's create-on-miss and the
	// scheduler's mint-failure path, both of which are unpersistable by design.
	// The refusal lives in Persist, not here.
	none := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", "")
	if none.RunID != "" {
		t.Errorf("constructor must store %q verbatim, got %q", "", none.RunID)
	}
}

func TestPersist_RefusesAnEmptyIdentity_AndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", "")

	err := rs.Persist(dir)
	if err == nil {
		t.Fatal("Persist with an empty RunID returned nil — a state with no identity has no correct filename and no correct owner (Decision 1)")
	}
	if !errors.Is(err, ErrNoRunIdentity) {
		t.Errorf("error must be ErrNoRunIdentity so callers can distinguish it; got %v", err)
	}
	assertDirEmpty(t, dir, "Persist with an empty RunID")

	// PersistExisting refuses on the same grounds, before it even looks at disk.
	if err := rs.PersistExisting(dir); !errors.Is(err, ErrNoRunIdentity) {
		t.Errorf("PersistExisting with an empty RunID = %v, want ErrNoRunIdentity", err)
	}
	assertDirEmpty(t, dir, "PersistExisting with an empty RunID")
}

// TestPersistAndSealRefuseAPathTraversingIdentity_AndTouchNothingOutsideStateDir
// is the security pin for ADR-017 Decision 1's central claim: the identity is
// VALIDATED BEFORE IT BECOMES A PATH COMPONENT.
//
// Step 1 made RunID a filename component, and `remoteRunId` reaches it from the
// local IPC socket, which ADR-015 documents as UNAUTHENTICATED. With the
// refusal keyed on `RunID == ""` instead of runstate.IsIdentity, a `../`-bearing
// value composed `runtime-42-../../../victim/OWNED.json`: Persist WROTE
// attacker-controlled JSON outside stateDir and SealAndRemove then DELETED that
// same out-of-tree file. Both are proven closed here, at the sink every writer
// goes through.
//
// The escape must be observed, not merely the error: this test asserts on the
// filesystem OUTSIDE stateDir, so a future refactor that reorders the refusal
// behind the write fails on the victim file, not on an error string.
func TestPersistAndSealRefuseAPathTraversingIdentity_AndTouchNothingOutsideStateDir(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "repo", ".nightgauge", "pipeline")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victimDir := filepath.Join(root, "victim")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(victimDir, "OWNED.json")
	const original = `{"original":true}`
	if err := os.WriteFile(victim, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// The composer would happily interpolate this — which is the point: the
	// refusal cannot live in SnapshotFilename, it must live at the sink.
	const traversal = "../../../victim/OWNED"
	if got := SnapshotFilename(370, traversal); !strings.Contains(got, "..") {
		t.Fatalf("probe is not exercising the traversal shape: SnapshotFilename = %q", got)
	}
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", traversal)

	if err := rs.Persist(stateDir); !errors.Is(err, ErrNoRunIdentity) {
		t.Errorf("Persist with a path-traversing RunID = %v, want ErrNoRunIdentity", err)
	}
	if err := rs.PersistExisting(stateDir); !errors.Is(err, ErrNoRunIdentity) {
		t.Errorf("PersistExisting with a path-traversing RunID = %v, want ErrNoRunIdentity", err)
	}
	if err := rs.SealAndRemove(stateDir); !errors.Is(err, ErrNoRunIdentity) {
		t.Errorf("SealAndRemove with a path-traversing RunID = %v, want ErrNoRunIdentity", err)
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("ESCAPED REMOVE: the file outside stateDir is gone: %v", err)
	}
	if string(got) != original {
		t.Fatalf("ESCAPED WRITE: the file outside stateDir was rewritten to %q", string(got))
	}
	assertDirEmpty(t, stateDir, "a path-traversing RunID")

	// Nothing anywhere under the root but the untouched victim: no `.tmp`
	// leftover, no directory conjured by MkdirAll along the escaped path.
	var files []string
	if err := filepath.Walk(root, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !fi.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != filepath.Join("victim", "OWNED.json") {
		t.Fatalf("a refused identity left files behind: %v", files)
	}
}

// TestPersist_RefusesANonCanonicalIdentity_SoNoPhantomSnapshotIsWritten is the
// BENIGN half of the same defect, and needs no attacker at all: a platform that
// assigns a UUIDv4 or a ULID as its run id used to produce a real snapshot
// under a name the discovery regex cannot match. It was then invisible to
// FindPersistedStatesForIssue, the gate seam, orphan reconciliation and
// getState, and nothing ever removed it — a silent phantom run, which is the
// exact defect #44 exists to prevent.
func TestPersist_RefusesANonCanonicalIdentity_SoNoPhantomSnapshotIsWritten(t *testing.T) {
	// This slice and the TypeScript refusal table in
	// packages/nightgauge-sdk/src/__tests__/runIdentity.test.ts are deliberately
	// the SAME SET, case for case (#424 review): "both sides refuse the same
	// strings" is only a claim while one table is a superset of the other, and a
	// row present on one side alone is where a one-sided widening hides.
	for _, id := range []string{
		"3f2504e0-4f89-41d3-9a0c-0305e82c3301", // UUIDv4 — right length, wrong version nibble
		"run_01H8XGJWBWBAQ4ZZY1N1V9PJ0M",       // ULID with a prefix
		"019FE6F3-FCFE-7B6F-8A7C-BE0F444B6610", // canonical shape, UPPERCASE
		"019fe6f3-fcfe-7b6f-8a7c-be0f444b6610 ",
		" 019fe6f3-fcfe-7b6f-8a7c-be0f444b6610",  // leading space — kills a TrimLeft/TrimSpace transform
		"019fe6f3-fcfe-7b6f-8a7c-be0f444b6610\n", // trailing newline
		// Two canonical-shaped lines in one string. RE2 has no `m` flag here, so
		// ^…$ bracket the whole string and this is refused; a per-line reading of
		// the anchors would accept it. Twin of the TS embedded-newline arm.
		"aaaaaaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee\n019fe6f3-fcfe-7b6f-8a7c-be0f444b6610",
		"019fe6f3-fcfe-7b6f-ca7c-be0f444b6610", // wrong variant nibble (c)
	} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", id)
			if err := rs.Persist(dir); !errors.Is(err, ErrNoRunIdentity) {
				t.Errorf("Persist(%q) = %v, want ErrNoRunIdentity", id, err)
			}
			assertDirEmpty(t, dir, "a non-canonical identity")
			// And the writer's refusal agrees with the reader's: LoadPersistedState
			// already refused this string, which is the asymmetry step 1 introduced
			// and this fix removes.
			if _, err := LoadPersistedState(dir, id); err == nil {
				t.Errorf("LoadPersistedState(%q) returned nil error — reader and writer must refuse the same set", id)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Decision 5 — the two-half latch, and the F27 gap it closes.
// ---------------------------------------------------------------------------

func TestMarkTerminal_LatchesTheDurableHalf_AndTravelsIntoTheSnapshot(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())

	if rs.IsTerminal() {
		t.Fatal("a fresh runtime must not be terminal")
	}
	rs.MarkTerminal("complete")
	if !rs.IsTerminal() {
		t.Fatal("MarkTerminal did not latch")
	}

	// The latch is one-way and idempotent: a second call must not move the
	// timestamp or rewrite the outcome.
	first := *rs.Snapshot().TerminalAt
	rs.MarkTerminal("failed")
	snap := rs.Snapshot()
	if !snap.TerminalAt.Equal(first) {
		t.Errorf("MarkTerminal is one-way: TerminalAt moved from %s to %s", first, snap.TerminalAt)
	}
	if snap.TerminalOutcome != "complete" {
		t.Errorf("TerminalOutcome = %q, want the first outcome %q", snap.TerminalOutcome, "complete")
	}

	// A Persist BETWEEN MarkTerminal and the seal is explicitly allowed, and
	// what it writes must carry terminal:true — that is what makes the file it
	// leaves behind one adoption refuses and the reconciler removes without
	// emitting (benign interleaving 2).
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist after MarkTerminal must still succeed (only the SEAL closes writes): %v", err)
	}
	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if !loaded.Terminal || loaded.TerminalAt == nil {
		t.Errorf("the durable half must be marshalled like any other field; got terminal=%v terminalAt=%v",
			loaded.Terminal, loaded.TerminalAt)
	}
}

func TestMarkAbandoned_IsDurable_OneWay_AndDistinctFromTerminal(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	rs.MarkAbandoned(at, "abort deadline")
	// Abandonment is NOT terminality: the dispatch was given up on, the run may
	// still be streaming (7.1).
	if rs.IsTerminal() {
		t.Error("MarkAbandoned must not latch the terminal half")
	}
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("an abandoned run must still persist — it may still be alive: %v", err)
	}

	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if !loaded.Abandoned || loaded.AbandonedAt == nil || !loaded.AbandonedAt.Equal(at) {
		t.Errorf("abandoned marker did not survive the write: %v / %v", loaded.Abandoned, loaded.AbandonedAt)
	}
	if loaded.AbandonedReason != "abort deadline" {
		t.Errorf("AbandonedReason = %q", loaded.AbandonedReason)
	}

	// There is no ClearAbandoned, and a later mutation + Persist must not erase
	// it: a live abandoned run keeps writing, and every byte keeps the marker.
	rs.BeginStage(StagePRCreate)
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	again, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if !again.Abandoned {
		t.Error("a later transition's Persist erased the abandoned marker — there is no clear path by design")
	}
}

func TestSealAndRemove_WritesThenRemoves_ThenRefusesEveryFurtherPersist(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	target := filepath.Join(dir, SnapshotFilename(370, rs.RunID))
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("snapshot missing before seal: %v", err)
	}

	rs.MarkTerminal("complete")
	if err := rs.SealAndRemove(dir); err != nil {
		t.Fatalf("SealAndRemove: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("SealAndRemove must remove the snapshot it just wrote; stat = %v", err)
	}
	if !rs.IsSealed() {
		t.Error("SealAndRemove did not latch the in-memory half")
	}

	// This is the F27 fix stated as behaviour: a transition's late Persist can
	// no longer re-create the snapshot the claim just deleted.
	err := rs.Persist(dir)
	if !errors.Is(err, ErrRunSealed) {
		t.Errorf("Persist after the seal = %v, want ErrRunSealed", err)
	}
	assertDirEmpty(t, dir, "Persist after SealAndRemove")

	if err := rs.PersistExisting(dir); !errors.Is(err, ErrRunSealed) {
		t.Errorf("PersistExisting after the seal = %v, want ErrRunSealed", err)
	}
}

func TestSealAndRemove_IsIdempotentWhenTheReconcilerRemovedTheFileFirst(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	rs.MarkTerminal("failed")

	// Nothing on disk at all — the reconciler got there first. Write-then-remove
	// re-creates the file as terminal and takes it away again: net nothing.
	if err := rs.SealAndRemove(dir); err != nil {
		t.Fatalf("SealAndRemove on an already-removed snapshot must be a no-op, got %v", err)
	}
	assertDirEmpty(t, dir, "SealAndRemove with no pre-existing file")
	if !rs.IsSealed() {
		t.Error("SealAndRemove must latch even when there was nothing to remove")
	}
}

// TestSealAndRemove_WriteFailureStillSealsAndStillRemoves is the other half of
// F27, and it is the branch that used to leave the hole wide open.
//
// When the terminal-stamped write fails, the file still on disk is the run's
// STALE NON-TERMINAL snapshot — the one shape adoption happily rehydrates.
// Returning early left it there AND left the run unsealed, so a restart adopted
// a dead run, its next call produced a record strictly richer by one stage, and
// the history layer accepted that as an upgrade over the authoritative record
// (R-4). The authoritative record was already durably written in claim step 2,
// so removing the stale file costs at worst R-4's adopt-empty noise.
//
// The write is made to fail deterministically by putting a DIRECTORY where
// AtomicWriteFile wants its temp file: the open fails EISDIR while the target's
// own removal is still perfectly possible, which is exactly the branch under
// test.
func TestSealAndRemove_WriteFailureStillSealsAndStillRemoves(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	target := filepath.Join(dir, SnapshotFilename(370, rs.RunID))
	if err := os.Mkdir(target+".tmp", 0o755); err != nil {
		t.Fatalf("seed the temp-path blocker: %v", err)
	}

	rs.MarkTerminal("complete")
	err := rs.SealAndRemove(dir)
	if err == nil {
		t.Fatal("SealAndRemove reported success while its write failed")
	}
	if !errors.Is(err, ErrSealWriteFailed) {
		t.Errorf("error = %v, want it to wrap ErrSealWriteFailed so the caller can log the branch honestly", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("the STALE NON-TERMINAL snapshot survived a failed seal — a restart rehydrates it (R-4); stat = %v", statErr)
	}
	if !rs.IsSealed() {
		t.Error("a failed write left the run UNSEALED — its next Persist re-creates the snapshot (F27)")
	}
	if perr := rs.Persist(dir); !errors.Is(perr, ErrRunSealed) {
		t.Errorf("Persist after a failed seal = %v, want ErrRunSealed", perr)
	}
}

// TestSealAndRemove_WriteAndRemoveBothFailingStillSeals covers the doubly-bad
// filesystem: a read-only state dir fails the write AND the removal (removing a
// directory entry needs write permission on the directory). The run must still
// latch — a run that reached its terminal claim never re-opens to further
// writes, whatever the filesystem did — and the error must name BOTH failures so
// the operator knows a non-terminal snapshot is still on disk.
func TestSealAndRemove_WriteAndRemoveBothFailingStillSeals(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	// Restore write permission or t.TempDir's cleanup cannot remove the tree.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	rs.MarkTerminal("failed")
	err := rs.SealAndRemove(dir)
	if err == nil {
		t.Fatal("SealAndRemove reported success on a read-only state dir")
	}
	if !errors.Is(err, ErrSealWriteFailed) {
		t.Errorf("error = %v, want it to wrap ErrSealWriteFailed", err)
	}
	if !strings.Contains(err.Error(), "could not be removed") {
		t.Errorf("error = %v, want it to name the removal failure too — the two branches leave different things on disk", err)
	}
	if !rs.IsSealed() {
		t.Error("the seal must latch even when the filesystem refused everything")
	}
}

// TestSealAndRemove_TakesThePersistLockedPath is the F36 property: SealAndRemove
// holds rs.mu for the whole operation and calls the …Locked body, never the
// exported Persist. Re-entering an exported method from inside its own lock on a
// non-reentrant sync.Mutex deadlocks, so a regression here HANGS rather than
// failing — the timeout is the assertion.
func TestSealAndRemove_TakesThePersistLockedPath(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	rs.MarkTerminal("complete")

	done := make(chan error, 1)
	go func() { done <- rs.SealAndRemove(dir) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SealAndRemove: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SealAndRemove deadlocked — it must call persistLocked, not Persist (C16/F36)")
	}
}

func TestPersistExisting_FailsWhenTheTargetIsAbsent(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())

	// The whole point: a read-modify-write must not re-create a file that was
	// removed between the load and the write.
	err := rs.PersistExisting(dir)
	if err == nil {
		t.Fatal("PersistExisting created a snapshot out of nothing")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("PersistExisting on an absent target = %v, want an fs.ErrNotExist", err)
	}
	assertDirEmpty(t, dir, "PersistExisting on an absent target")

	// With the file present it behaves exactly like Persist.
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	rs.SetPrUrl("https://example.invalid/pr/1")
	if err := rs.PersistExisting(dir); err != nil {
		t.Fatalf("PersistExisting on a present target: %v", err)
	}
	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.PrUrl != "https://example.invalid/pr/1" {
		t.Errorf("PersistExisting did not write; PrUrl = %q", loaded.PrUrl)
	}
}

// TestPersist_ConcurrentCallsAreSerialisedByTheRunsOwnMutex is the F27
// regression. Before ADR-017 step 1, Persist snapshotted under rs.mu, UNLOCKED,
// and then marshalled + wrote. AtomicWriteFile uses a single fixed temp path
// (target + ".tmp"), so two concurrent Persist calls on one runtime raced on
// that one file: both O_TRUNC'd it, one renamed it away, and the loser's rename
// failed ENOENT. Captured red on 7b7b0d8b at 225-227 errors per run.
func TestPersist_ConcurrentCallsAreSerialisedByTheRunsOwnMutex(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	// Big enough that marshal+write spans a scheduling quantum.
	rs.RecordStageOutputTail(StageFeatureDev, strings.Repeat("x", 180*1024))

	const goroutines = 8
	const iterations = 40

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*iterations)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Mutate as well as write, so -race also sees the readers and
				// writers of the live object overlap.
				rs.SetStageChild(os.Getpid())
				if err := rs.Persist(dir); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	var persistErrs []string
	for err := range errCh {
		persistErrs = append(persistErrs, err.Error())
	}
	if len(persistErrs) > 0 {
		t.Errorf("concurrent Persist produced %d errors — marshal+write must run under rs.mu (F27); first: %s",
			len(persistErrs), persistErrs[0])
	}

	data, err := os.ReadFile(filepath.Join(dir, SnapshotFilename(370, rs.RunID)))
	if err != nil {
		t.Fatalf("snapshot missing after concurrent Persist: %v", err)
	}
	var out RuntimeState
	if err := json.Unmarshal(data, &out); err != nil {
		t.Errorf("snapshot is not valid JSON after concurrent Persist (%d bytes): %v", len(data), err)
	}
}

// TestExportedMutatorsDoNotDeadlockUnderConcurrency exercises every symbol that
// gained a …Locked twin through its EXPORTED form, concurrently. Each exported
// method is a Lock/defer-Unlock wrapper over its …Locked body; a wrapper that
// accidentally called another exported method from inside the hold would
// deadlock here rather than fail (C16, F36).
func TestExportedMutatorsDoNotDeadlockUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					rs.RecordExecutionPath(StagePRMerge, "deterministic")
					rs.RecordStagePuntReason(StagePRMerge, "dirty-merge-state")
					rs.SetStageChild(1000 + g)
					_ = rs.Snapshot()
					_ = rs.Persist(dir)
					rs.MarkAbandoned(time.Now(), "concurrency probe")
					rs.MarkTerminal("complete")
				}
			}(g)
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("an exported mutator deadlocked — every exported form must wrap its …Locked body exactly once (C16)")
	}
}

// ---------------------------------------------------------------------------
// Decisions 1 + 8 — the composer and the discovery regex are pinned to each
// other, and to the shared identity constant.
// ---------------------------------------------------------------------------

func TestSnapshotFilename_RoundTripsThroughTheDiscoveryRegex(t *testing.T) {
	for _, issue := range []int{1, 42, 370, 99999} {
		id := testRunID()
		if !runstate.IsIdentity(id) {
			t.Fatalf("runstate.NewRunID produced %q, which the shared identity pattern rejects", id)
		}
		name := SnapshotFilename(issue, id)
		gotIssue, gotID, ok := ParseSnapshotFilename(name)
		if !ok {
			t.Fatalf("the discovery regex does not match a name the composer produced: %s", name)
		}
		if gotIssue != issue || gotID != id {
			t.Errorf("round-trip of %s = (%d, %s), want (%d, %s)", name, gotIssue, gotID, issue, id)
		}
	}
}

func TestParseSnapshotFilename_RejectsEverythingThatIsNotACanonicalSnapshot(t *testing.T) {
	id := testRunID()
	for _, name := range []string{
		"runtime-370.json",                             // legacy scheme — no migration, pre-customer
		"runtime--" + id + ".json",                     // no issue number
		"runtime-370-.json",                            // the empty-identity filename Persist refuses to write
		"runtime-370-not-a-uuid.json",                  // wrong shape
		"runtime-370-" + strings.ToUpper(id) + ".json", // uppercase: the identity is canonical LOWERCASE
		"resuming-370-" + id + "." + id + ".json",      // the pause-restore claim artifact, a different file class
		"current-run.json",
		"runtime-370-" + id + ".json.tmp", // an in-flight atomic write
	} {
		if _, _, ok := ParseSnapshotFilename(name); ok {
			t.Errorf("discovery regex accepted %q", name)
		}
	}
}

// TestResumingArtifactPattern_IsBuiltFromTheSameIdentity pins Decision 9's claim
// artifact to the same shared constant. It is INERT until step 8 creates the
// first such file — inert, not wrong, and the pinning is what keeps it that way.
func TestResumingArtifactPattern_IsBuiltFromTheSameIdentity(t *testing.T) {
	runID, claimToken := testRunID(), testRunID()
	name := "resuming-370-" + runID + "." + claimToken + ".json"

	issue, gotRun, gotToken, ok := runstate.ParseResumingArtifactName(name)
	if !ok {
		t.Fatalf("claim-artifact regex does not match %s", name)
	}
	if issue != "370" || gotRun != runID || gotToken != claimToken {
		t.Errorf("parsed (%s, %s, %s), want (370, %s, %s)", issue, gotRun, gotToken, runID, claimToken)
	}

	// A token that is not an identity is not a claim artifact, and Decision 9
	// requires such a file be left alone rather than guessed at.
	if _, _, _, ok := runstate.ParseResumingArtifactName("resuming-370-" + runID + ".whenever.json"); ok {
		t.Error("claim-artifact regex accepted a non-identity claim token")
	}
	// A canonical snapshot is not a claim artifact and vice versa.
	if _, _, _, ok := runstate.ParseResumingArtifactName(SnapshotFilename(370, runID)); ok {
		t.Error("claim-artifact regex matched a canonical snapshot name")
	}
	if _, _, ok := ParseSnapshotFilename(name); ok {
		t.Error("snapshot discovery regex matched a claim artifact")
	}
}

// ---------------------------------------------------------------------------
// Decision 8 — LoadPersistedState by identity, FindPersistedStatesForIssue by
// index, and the standard pick.
// ---------------------------------------------------------------------------

func TestLoadPersistedState_AddressesByIdentity_NotByIssue(t *testing.T) {
	dir := t.TempDir()
	a := NewRuntimeState("nightgauge/nightgauge", 370, "item-a", testRunID())
	a.SetPrUrl("https://example.invalid/pr/a")
	b := NewRuntimeState("nightgauge/nightgauge", 370, "item-b", testRunID())
	b.SetPrUrl("https://example.invalid/pr/b")
	for _, rs := range []*RuntimeState{a, b} {
		if err := rs.Persist(dir); err != nil {
			t.Fatalf("Persist: %v", err)
		}
	}

	// Two concurrent dispatches of ONE issue coexist; only the identity picks
	// one out, which is the whole point of the re-key.
	loaded, err := LoadPersistedState(dir, b.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.PrUrl != "https://example.invalid/pr/b" {
		t.Errorf("loaded the wrong run: PrUrl = %q", loaded.PrUrl)
	}

	// A string that is not an identity never becomes a filename component.
	if _, err := LoadPersistedState(dir, "../../etc/passwd"); err == nil {
		t.Error("LoadPersistedState accepted a non-identity as an address")
	}
}

// LoadSnapshotByIdentity is the exact-path reader the reconciler uses: it
// already holds both components, and paying a full os.ReadDir per entry of a
// directory it just listed is quadratic in snapshot count — the direction the
// one-file-per-RUN scheme makes worse.
func TestLoadSnapshotByIdentity_ReadsTheOnePathTheComponentsCompose(t *testing.T) {
	dir := t.TempDir()
	a := NewRuntimeState("nightgauge/nightgauge", 370, "item-a", testRunID())
	a.SetPrUrl("https://example.invalid/pr/a")
	b := NewRuntimeState("nightgauge/nightgauge", 370, "item-b", testRunID())
	for _, rs := range []*RuntimeState{a, b} {
		if err := rs.Persist(dir); err != nil {
			t.Fatalf("Persist: %v", err)
		}
	}

	loaded, err := LoadSnapshotByIdentity(dir, 370, a.RunID)
	if err != nil {
		t.Fatalf("LoadSnapshotByIdentity: %v", err)
	}
	if loaded.RunID != a.RunID || loaded.PrUrl != "https://example.invalid/pr/a" {
		t.Errorf("loaded run %q (%q), want %q", loaded.RunID, loaded.PrUrl, a.RunID)
	}

	// The issue number is part of the ADDRESS here, not a filter: a wrong pair
	// names a file that does not exist rather than the run's snapshot.
	if _, err := LoadSnapshotByIdentity(dir, 371, a.RunID); err == nil {
		t.Error("a mismatched (issue, runId) pair must not resolve")
	}
	// Same sink discipline as LoadPersistedState: the identity becomes a path
	// component, so it is validated before it is interpolated.
	if _, err := LoadSnapshotByIdentity(dir, 370, "../../etc/passwd"); err == nil {
		t.Error("LoadSnapshotByIdentity accepted a non-identity as an address")
	}
}

func TestFindPersistedStatesForIssue_ReturnsEveryCandidateNewestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	var want []string
	for i := 0; i < 3; i++ {
		rs := NewRuntimeState("nightgauge/nightgauge", 370, "item", testRunID())
		rs.StartedAt = base.Add(time.Duration(i) * time.Hour)
		if err := rs.Persist(dir); err != nil {
			t.Fatalf("Persist: %v", err)
		}
		want = append([]string{rs.RunID}, want...) // newest first
	}
	// A different issue in the same dir must not leak in.
	other := NewRuntimeState("nightgauge/nightgauge", 371, "item", testRunID())
	if err := other.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	got, err := FindPersistedStatesForIssue(dir, 370)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	for i, rs := range got {
		if rs.RunID != want[i] {
			t.Errorf("candidate %d = %s, want %s (order is newest StartedAt first)", i, rs.RunID, want[i])
		}
	}
}

func TestFindPersistedStatesForIssue_SkipsCorruptAndLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	good := NewRuntimeState("nightgauge/nightgauge", 370, "item", testRunID())
	if err := good.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// HAND-AUTHORED FIXTURES, deliberately: these are shapes the production
	// writer can no longer produce, which is exactly why they must be written
	// by hand. A legacy-named file (pre-ADR-017) and a new-scheme file with
	// unparseable content.
	if err := os.WriteFile(filepath.Join(dir, "runtime-370.json"),
		[]byte(`{"issueNumber":370,"repo":"nightgauge/nightgauge"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SnapshotFilename(370, testRunID())),
		[]byte(`{ this is not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FindPersistedStatesForIssue(dir, 370)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(got) != 1 || got[0].RunID != good.RunID {
		t.Fatalf("scan must survive a corrupt neighbour and ignore the legacy scheme; got %d candidates", len(got))
	}
}

func TestPickPersistedStateForIssue_PrefersNonTerminalThenNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	// Newest of all, but terminal — it must lose to the older live run.
	dead := NewRuntimeState("nightgauge/nightgauge", 370, "item-dead", testRunID())
	dead.StartedAt = base.Add(3 * time.Hour)
	dead.MarkTerminal("complete")
	oldLive := NewRuntimeState("nightgauge/nightgauge", 370, "item-old", testRunID())
	oldLive.StartedAt = base
	newLive := NewRuntimeState("nightgauge/nightgauge", 370, "item-new", testRunID())
	newLive.StartedAt = base.Add(time.Hour)
	for _, rs := range []*RuntimeState{dead, oldLive, newLive} {
		if err := rs.Persist(dir); err != nil {
			t.Fatalf("Persist: %v", err)
		}
	}

	got, err := PickPersistedStateForIssue(dir, 370)
	if err != nil {
		t.Fatalf("PickPersistedStateForIssue: %v", err)
	}
	if got.RunID != newLive.RunID {
		t.Errorf("picked %s, want the newest NON-TERMINAL run %s", got.RunID, newLive.RunID)
	}

	// All terminal → newest terminal, never nothing.
	allDead := t.TempDir()
	for i := 0; i < 2; i++ {
		rs := NewRuntimeState("nightgauge/nightgauge", 370, "item", testRunID())
		rs.StartedAt = base.Add(time.Duration(i) * time.Hour)
		rs.MarkTerminal("complete")
		if err := rs.Persist(allDead); err != nil {
			t.Fatalf("Persist: %v", err)
		}
		if i == 1 {
			defer func(want string) {
				got, err := PickPersistedStateForIssue(allDead, 370)
				if err != nil || got.RunID != want {
					t.Errorf("all-terminal pick = %v / %v, want %s", got, err, want)
				}
			}(rs.RunID)
		}
	}

	if _, err := PickPersistedStateForIssue(t.TempDir(), 370); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an issue with no snapshot must report fs.ErrNotExist; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Decision 5 (cross-process half) — the gate CLI seam is load-or-skip.
// ---------------------------------------------------------------------------

func TestAppendStageGateResultToDisk_ZeroCandidatesSkipsLoudlyAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()

	err := AppendStageGateResultToDisk(dir, 210, StageFeatureDev, StageGateResult{GateName: "feature-dev", Passed: true})
	if err == nil {
		t.Fatal("with no snapshot on disk the record must be SKIPPED with a loud error, not created")
	}
	// The contract the error text has to carry: the verdict already ran and was
	// returned; only the --record write is skipped.
	if !strings.Contains(err.Error(), "verdict still returned") {
		t.Errorf("error must name the contract (verdict returned, record skipped); got %q", err)
	}
	assertDirEmpty(t, dir, "gate seam with zero candidates")
}

func TestAppendStageGateResultToDisk_RefusesATerminalSnapshot(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 210, "item-210", testRunID())
	rs.MarkTerminal("complete")
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// The durable marker is exactly the cross-process latch the in-memory one
	// cannot be: a gate record must not land on a run that already ended.
	if err := AppendStageGateResultToDisk(dir, 210, StageFeatureDev, StageGateResult{GateName: "feature-dev"}); err == nil {
		t.Fatal("gate seam wrote into a terminal snapshot")
	}
	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if len(loaded.StageGateResultsFor(StageFeatureDev)) != 0 {
		t.Error("a terminal snapshot was mutated by the gate seam")
	}
}

// TestAppendStageGateResultToDisk_ManyCandidatesRecordsIntoTheNewestLiveRun
// pins the correction that makes the seam survive a RE-RUN.
//
// The seam originally refused whenever more than one non-terminal snapshot
// existed. Under per-run filenames that is not the rare concurrency it looks
// like: nothing sets the durable terminal marker before ADR-017 step 4, so a
// second dispatch of an issue leaves TWO live snapshots and the refusal fires
// permanently — every gate record for every future run of that issue silently
// dropped, and logGateNotInvoked then reporting `[gate-not-invoked]` for gates
// that all ran. (Before step 1 the shared runtime-{issue}.json was overwritten,
// so there was always exactly one candidate and the record always landed.)
//
// The newest non-terminal snapshot is the CURRENT run, because the gate CLI is
// spawned by that run; the older one is an orphan of a prior dispatch.
func TestAppendStageGateResultToDisk_ManyCandidatesRecordsIntoTheNewestLiveRun(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	// Both written by the PRODUCTION writer, as the re-run population is.
	older := NewRuntimeState("nightgauge/nightgauge", 210, "item-older", testRunID())
	older.StartedAt = base
	newer := NewRuntimeState("nightgauge/nightgauge", 210, "item-newer", testRunID())
	newer.StartedAt = base.Add(time.Hour)
	for _, rs := range []*RuntimeState{older, newer} {
		if err := rs.Persist(dir); err != nil {
			t.Fatalf("Persist: %v", err)
		}
	}

	if err := AppendStageGateResultToDisk(dir, 210, StageFeatureDev, StageGateResult{
		GateName: "feature-dev", Passed: true,
	}); err != nil {
		t.Fatalf("a re-run issue must still record its gate result: %v", err)
	}

	loadedNewer, err := LoadPersistedState(dir, newer.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState(newer): %v", err)
	}
	if got := loadedNewer.StageGateResultsFor(StageFeatureDev); len(got) != 1 || !got[0].Passed {
		t.Errorf("gate result did not land on the NEWEST live run: %#v", got)
	}
	loadedOlder, err := LoadPersistedState(dir, older.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState(older): %v", err)
	}
	if len(loadedOlder.StageGateResultsFor(StageFeatureDev)) != 0 {
		t.Error("the orphaned prior run was written to — the pick must be newest-live, not any-live")
	}
}

// TestAppendStageGateResultToDisk_IgnoresTerminalSiblingsWhenExactlyOneIsLive
// pins the ONE case that still records: a finished earlier run of the same issue
// does not make the live one ambiguous.
func TestAppendStageGateResultToDisk_IgnoresTerminalSiblingsWhenExactlyOneIsLive(t *testing.T) {
	dir := t.TempDir()
	dead := NewRuntimeState("nightgauge/nightgauge", 210, "item-dead", testRunID())
	dead.MarkTerminal("complete")
	live := NewRuntimeState("nightgauge/nightgauge", 210, "item-live", testRunID())
	for _, rs := range []*RuntimeState{dead, live} {
		if err := rs.Persist(dir); err != nil {
			t.Fatalf("Persist: %v", err)
		}
	}

	if err := AppendStageGateResultToDisk(dir, 210, StageFeatureDev, StageGateResult{
		GateName: "feature-dev", Passed: true,
	}); err != nil {
		t.Fatalf("exactly one non-terminal candidate must record: %v", err)
	}
	loaded, err := LoadPersistedState(dir, live.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if got := loaded.StageGateResultsFor(StageFeatureDev); len(got) != 1 || !got[0].Passed {
		t.Errorf("gate result did not land on the live run: %#v", got)
	}
}

// ---------------------------------------------------------------------------
// Decision 3 (step 1) — the RunID JSON key is always present.
// ---------------------------------------------------------------------------

func TestPersistedSnapshotAlwaysCarriesTheRunIdKey(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 370, "item-370", testRunID())
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, SnapshotFilename(370, rs.RunID)))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// omitempty is off on purpose: with the key ALWAYS present, a legacy file
	// (key absent) stays distinguishable from a corrupt one (key empty).
	if _, ok := raw["runId"]; !ok {
		t.Error("persisted snapshot has no runId key — the tag must not carry omitempty")
	}
}

func assertDirEmpty(t *testing.T, dir, what string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Errorf("%s wrote %v — it must write nothing", what, names)
}
