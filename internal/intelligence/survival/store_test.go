package survival

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func TestStore_LoadMissingFileIsEmpty(t *testing.T) {
	s := newTestStore(t)
	recs, err := s.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected empty, got %d records", len(recs))
	}
}

func TestStore_AppendIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	rec := NewPending("nightgauge/nightgauge", 1, 2, "sha-A", testMergedAt, "main")

	added, err := s.Append(rec)
	if err != nil || !added {
		t.Fatalf("first append: added=%v err=%v", added, err)
	}
	added2, err := s.Append(rec)
	if err != nil {
		t.Fatalf("second append err: %v", err)
	}
	if added2 {
		t.Error("expected second append of same SHA to be a no-op (idempotent)")
	}

	all, _ := s.Load()
	if len(all) != 1 {
		t.Errorf("expected 1 folded record, got %d", len(all))
	}
}

func TestStore_AppendRejectsEmptySHA(t *testing.T) {
	s := newTestStore(t)
	rec := NewPending("nightgauge/nightgauge", 1, 2, "", testMergedAt, "main")
	if _, err := s.Append(rec); err == nil {
		t.Error("expected error appending a record with empty merge_commit_sha")
	}
}

func TestStore_FinalizeSupersedesPending(t *testing.T) {
	s := newTestStore(t)
	rec := NewPending("nightgauge/nightgauge", 1, 2, "sha-B", testMergedAt, "main")
	if _, err := s.Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}

	final, _ := DecideVerdict(rec, mergedAtTime(t).Add(8*24*time.Hour), 7, Observation{})
	if final.Verdict != Survived {
		t.Fatalf("precondition: expected survived, got %q", final.Verdict)
	}
	if err := s.Finalize(final); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	all, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 folded record after finalize, got %d", len(all))
	}
	if all[0].Verdict != Survived {
		t.Errorf("folded verdict = %q, want survived (terminal supersedes pending)", all[0].Verdict)
	}

	// Pending() must now be empty.
	pend, _ := s.Pending()
	if len(pend) != 0 {
		t.Errorf("expected 0 pending after finalize, got %d", len(pend))
	}
}

func TestStore_PendingFiltersTerminal(t *testing.T) {
	s := newTestStore(t)
	p := NewPending("nightgauge/nightgauge", 1, 2, "sha-pending", testMergedAt, "main")
	done := NewPending("nightgauge/nightgauge", 3, 4, "sha-done", testMergedAt, "main")
	done.Verdict = Reverted
	if _, err := s.Append(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(done); err != nil {
		t.Fatal(err)
	}

	pend, err := s.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pend) != 1 || pend[0].MergeCommitSHA != "sha-pending" {
		t.Errorf("expected only sha-pending, got %+v", pend)
	}
}

func TestStore_LoadSkipsCorruptLines(t *testing.T) {
	s := newTestStore(t)
	// Seed a file with one good line, one garbage line, one blank line.
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"kind":"survival","merge_commit_sha":"good","verdict":"pending"}
not-json-at-all

{"kind":"survival","merge_commit_sha":"good2","verdict":"pending"}
`
	if err := os.WriteFile(s.Path(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 valid records (corrupt + blank skipped), got %d", len(all))
	}
}

func TestStore_LoadPreservesFirstSeenOrder(t *testing.T) {
	s := newTestStore(t)
	for _, sha := range []string{"z", "a", "m"} {
		if _, err := s.Append(NewPending("nightgauge/r", 1, 1, sha, testMergedAt, "main")); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.Load()
	got := []string{all[0].MergeCommitSHA, all[1].MergeCommitSHA, all[2].MergeCommitSHA}
	want := []string{"z", "a", "m"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
