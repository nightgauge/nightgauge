package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fixedNow dates every record in these tests into one daily JSONL file so the
// raw-line assertions are deterministic.
var fixedNow = time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

// makeRunRec builds a V2RunRecord with the given run_id and one "complete"
// stage per stageName. Zero stageNames yields a skeleton (empty stages map),
// the degraded shape a late finalizer emits.
func makeRunRec(runID string, issue int, startedAt string, stageNames ...string) V2RunRecord {
	stages := map[string]V2StageDetail{}
	for _, s := range stageNames {
		stages[s] = V2StageDetail{Status: "complete"}
	}
	return V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   issue,
		RunID:         runID,
		Repo:          "nightgauge/nightgauge",
		StartedAt:     startedAt,
		CompletedAt:   startedAt,
		Outcome:       "complete",
		Stages:        stages,
		RecordedAt:    startedAt,
	}
}

// rawDailyLines returns the non-empty JSONL lines physically present in the
// daily file — the source of truth for "how many records were actually
// appended", independent of any reader-side de-duplication.
func rawDailyLines(t *testing.T, dir string) []V2RunRecord {
	t.Helper()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", fixedNow.Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read daily file: %v", err)
	}
	var out []V2RunRecord
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec V2RunRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal daily line: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

func readIndexFile(t *testing.T, dir string) V2Index {
	t.Helper()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var idx V2Index
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("unmarshal index (torn/corrupt?): %v\n%s", err, string(data))
	}
	return idx
}

// TestIdempotency_DuplicateRunIDDropped: a second write for the same run_id is
// dropped — exactly one JSONL line and one index entry survive.
func TestIdempotency_DuplicateRunIDDropped(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)
	rec := makeRunRec("run-abc", 313, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev", "pr-merge")

	for i := 0; i < 4; i++ {
		if err := hw.WriteV2Record(rec, fixedNow); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	if lines := rawDailyLines(t, dir); len(lines) != 1 {
		t.Fatalf("daily JSONL lines = %d, want 1 (later duplicates must be dropped)", len(lines))
	}
	idx := readIndexFile(t, dir)
	if idx.TotalRuns != 1 || len(idx.Entries) != 1 {
		t.Fatalf("index total_runs=%d entries=%d, want 1/1", idx.TotalRuns, len(idx.Entries))
	}
	if idx.Entries[0].RunID != "run-abc" {
		t.Errorf("index entry run_id = %q, want run-abc", idx.Entries[0].RunID)
	}
}

// TestIdempotency_SkeletonAfterFullDropped: once a full record exists, a
// later skeleton (empty stages) for the same run never appends or overwrites.
func TestIdempotency_SkeletonAfterFullDropped(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)
	full := makeRunRec("run-xyz", 163, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev")
	skeleton := makeRunRec("run-xyz", 163, "2026-07-19T09:00:00Z") // no stages

	if err := hw.WriteV2Record(full, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := hw.WriteV2Record(skeleton, fixedNow); err != nil {
		t.Fatal(err)
	}

	lines := rawDailyLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("daily JSONL lines = %d, want 1 (skeleton must be dropped)", len(lines))
	}
	if len(lines[0].Stages) != 2 {
		t.Errorf("surviving record stage count = %d, want 2 (full, not skeleton)", len(lines[0].Stages))
	}
	// The freshest reader-visible record is the full one, never the skeleton.
	recs, err := hw.ReadRecentV2(0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || len(recs[len(recs)-1].Stages) != 2 {
		t.Fatalf("reader returned degraded/duplicate record: %+v", recs)
	}
}

// TestIdempotency_FullAfterSkeletonUpgrades documents the inverse ordering:
// when a skeleton lands FIRST and a full record follows, the full record is
// appended as an upgrade (the JSONL is append-only), the index entry is
// replaced with the full one, and reader de-duplication surfaces only the full
// record. This ordering does not occur once the skeleton emitter is removed at
// source, but the writer stays correct if it ever does.
func TestIdempotency_FullAfterSkeletonUpgrades(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)
	skeleton := makeRunRec("run-up", 42, "2026-07-19T09:00:00Z")
	full := makeRunRec("run-up", 42, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev", "pr-merge")

	if err := hw.WriteV2Record(skeleton, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := hw.WriteV2Record(full, fixedNow); err != nil {
		t.Fatal(err)
	}

	// Append-only: both lines are on disk (skeleton, then the richer upgrade).
	if lines := rawDailyLines(t, dir); len(lines) != 2 {
		t.Fatalf("daily JSONL lines = %d, want 2 (append-only upgrade)", len(lines))
	}
	// Index holds exactly ONE entry — the full one.
	idx := readIndexFile(t, dir)
	if idx.TotalRuns != 1 || len(idx.Entries) != 1 {
		t.Fatalf("index total_runs=%d entries=%d, want 1/1", idx.TotalRuns, len(idx.Entries))
	}
	if idx.Entries[0].StageCount != 3 {
		t.Errorf("index stage_count = %d, want 3 (upgraded to full)", idx.Entries[0].StageCount)
	}
	// Reader collapses to the single richest record.
	recs, err := hw.ReadRecentV2(0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("reader records = %d, want 1 (deduped)", len(recs))
	}
	if len(recs[0].Stages) != 3 {
		t.Errorf("reader record stage count = %d, want 3 (full)", len(recs[0].Stages))
	}
}

// TestIdempotency_SeedsFromIndexAcrossProcess simulates a process restart: the
// in-memory ledger is dropped, but a subsequent writer seeds it from the
// on-disk index and still drops a duplicate/degraded record for a run that a
// previous process already recorded.
func TestIdempotency_SeedsFromIndexAcrossProcess(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)
	full := makeRunRec("run-seed", 99, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev")
	if err := hw.WriteV2Record(full, fixedNow); err != nil {
		t.Fatal(err)
	}

	// Simulate a fresh process: forget the in-memory coordinator for this dir.
	dirCoordinatorsMu.Lock()
	delete(dirCoordinators, hw.dir)
	dirCoordinatorsMu.Unlock()

	// A duplicate arriving in the "new process" must still be dropped (seeded
	// from the persisted index).
	dup := makeRunRec("run-seed", 99, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev")
	hw2 := NewHistoryWriter(dir)
	if err := hw2.WriteV2Record(dup, fixedNow); err != nil {
		t.Fatal(err)
	}
	if lines := rawDailyLines(t, dir); len(lines) != 1 {
		t.Fatalf("daily JSONL lines = %d, want 1 (seeded ledger drops cross-process dup)", len(lines))
	}
}

// TestUpdateIndex_ConcurrentAppendsNoTear writes many DISTINCT runs
// concurrently. Under -race this asserts the per-directory serialization keeps
// the index.json read-modify-write from interleaving: the file stays parseable
// and every run is present exactly once.
func TestUpdateIndex_ConcurrentAppendsNoTear(t *testing.T) {
	dir := t.TempDir()

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := makeRunRec(
				fmt.Sprintf("run-%03d", i),
				1000+i,
				fmt.Sprintf("2026-07-19T09:%02d:00Z", i%60),
				"issue-pickup", "feature-dev",
			)
			// A fresh writer per goroutine, as the real racing callers do.
			w := NewHistoryWriter(dir)
			if err := w.WriteV2Record(rec, fixedNow); err != nil {
				t.Errorf("concurrent write %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	idx := readIndexFile(t, dir) // fails the test if index.json is torn
	if idx.TotalRuns != n || len(idx.Entries) != n {
		t.Fatalf("index total_runs=%d entries=%d, want %d/%d", idx.TotalRuns, len(idx.Entries), n, n)
	}
	seen := map[string]bool{}
	for _, e := range idx.Entries {
		if seen[e.RunID] {
			t.Errorf("duplicate index entry for run %q", e.RunID)
		}
		seen[e.RunID] = true
	}
	if lines := rawDailyLines(t, dir); len(lines) != n {
		t.Fatalf("daily JSONL lines = %d, want %d", len(lines), n)
	}
}

// TestUpdateIndex_ConcurrentSameRunSingleEntry: many writers racing on ONE run
// (the reported #313 shape) collapse to a single JSONL line and index entry.
func TestUpdateIndex_ConcurrentSameRunSingleEntry(t *testing.T) {
	dir := t.TempDir()
	rec := makeRunRec("run-solo", 313, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev", "pr-merge")

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := NewHistoryWriter(dir)
			if err := w.WriteV2Record(rec, fixedNow); err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	wg.Wait()

	if lines := rawDailyLines(t, dir); len(lines) != 1 {
		t.Fatalf("daily JSONL lines = %d, want 1 (one run, one record)", len(lines))
	}
	idx := readIndexFile(t, dir)
	if idx.TotalRuns != 1 || len(idx.Entries) != 1 {
		t.Fatalf("index total_runs=%d entries=%d, want 1/1", idx.TotalRuns, len(idx.Entries))
	}
}

// TestUpdateIndex_RebuildsFromJSONLWhenCorrupt: an unparseable index.json is
// rebuilt from the JSONL source of truth on the next write, not silently
// discarded — automating the operator's manual rebuild.
func TestUpdateIndex_RebuildsFromJSONLWhenCorrupt(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	// Record three distinct runs — index now has three entries.
	for i := 0; i < 3; i++ {
		rec := makeRunRec(fmt.Sprintf("run-%d", i), 200+i, fmt.Sprintf("2026-07-19T09:0%d:00Z", i),
			"issue-pickup", "feature-dev")
		if err := hw.WriteV2Record(rec, fixedNow); err != nil {
			t.Fatal(err)
		}
	}

	// Corrupt the index (the failure mode #313 reports: torn/garbage index).
	indexPath := filepath.Join(dir, ".nightgauge", "pipeline", "history", "index.json")
	if err := os.WriteFile(indexPath, []byte("{ this is not valid json "), 0644); err != nil {
		t.Fatal(err)
	}

	// The next write must rebuild from JSONL, recovering the earlier entries
	// instead of starting fresh with just the new one.
	newRec := makeRunRec("run-new", 999, "2026-07-19T09:30:00Z", "issue-pickup", "feature-dev")
	if err := hw.WriteV2Record(newRec, fixedNow); err != nil {
		t.Fatal(err)
	}

	idx := readIndexFile(t, dir)
	if idx.TotalRuns != 4 || len(idx.Entries) != 4 {
		t.Fatalf("index total_runs=%d entries=%d, want 4/4 (rebuilt from JSONL, not reset)",
			idx.TotalRuns, len(idx.Entries))
	}
	runIDs := map[string]bool{}
	for _, e := range idx.Entries {
		runIDs[e.RunID] = true
	}
	for _, want := range []string{"run-0", "run-1", "run-2", "run-new"} {
		if !runIDs[want] {
			t.Errorf("rebuilt index missing run %q", want)
		}
	}
}

// TestUpdateIndex_RebuildDedupesLegacyDuplicates: rebuilding from a JSONL file
// that already contains legacy duplicate/skeleton lines for one run yields a
// single index entry — the richest one.
func TestUpdateIndex_RebuildDedupesLegacyDuplicates(t *testing.T) {
	dir := t.TempDir()
	histDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(histDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Hand-write a daily file mirroring the reported incident: cancelled(full),
	// complete(full), complete(full), complete(skeleton) — all one run.
	daily := filepath.Join(histDir, fixedNow.Format("2006-01-02")+".jsonl")
	f, err := os.Create(daily)
	if err != nil {
		t.Fatal(err)
	}
	writeRec := func(rec V2RunRecord) {
		b, _ := json.Marshal(rec)
		f.Write(append(b, '\n'))
	}
	bad := makeRunRec("run-dupe", 163, "2026-07-19T09:00:00Z", "a", "b", "c")
	bad.Outcome = "cancelled"
	writeRec(bad)
	writeRec(makeRunRec("run-dupe", 163, "2026-07-19T09:00:00Z", "a", "b", "c"))
	writeRec(makeRunRec("run-dupe", 163, "2026-07-19T09:00:00Z", "a", "b", "c"))
	writeRec(makeRunRec("run-dupe", 163, "2026-07-19T09:00:00Z")) // skeleton, run_id present
	f.Close()

	// A new write for a DIFFERENT run triggers a rebuild path only if the index
	// is corrupt; here there is no index yet, so force a rebuild directly.
	hw := NewHistoryWriter(dir)
	entries := hw.rebuildIndexEntriesFromJSONL()
	if len(entries) != 1 {
		t.Fatalf("rebuilt entries = %d, want 1 (deduped)", len(entries))
	}
	if entries[0].StageCount != 3 {
		t.Errorf("deduped entry stage_count = %d, want 3 (richest, not skeleton)", entries[0].StageCount)
	}

	// The reader collapses the same duplicates for consumers.
	recs, err := hw.ReadRecentV2(0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("reader records = %d, want 1 (deduped)", len(recs))
	}
	if len(recs[0].Stages) != 3 {
		t.Errorf("reader record stage count = %d, want 3 (richest)", len(recs[0].Stages))
	}
}
