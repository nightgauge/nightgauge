package survival

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockDetector returns canned observations keyed by merge commit SHA and can be
// told to error on a specific SHA.
type mockDetector struct {
	obs   map[string]Observation
	errOn string
	calls []string
}

func (m *mockDetector) Observe(_ context.Context, rec Record) (Observation, error) {
	m.calls = append(m.calls, rec.MergeCommitSHA)
	if m.errOn != "" && rec.MergeCommitSHA == m.errOn {
		return Observation{}, errors.New("boom")
	}
	return m.obs[rec.MergeCommitSHA], nil
}

func seedPending(t *testing.T, s *Store, sha string, mergedAt string) {
	t.Helper()
	if _, err := s.Append(NewPending("nightgauge/nightgauge", 1, 2, sha, mergedAt, "main")); err != nil {
		t.Fatalf("seed %s: %v", sha, err)
	}
}

func TestSweep_FinalizesDueRecords(t *testing.T) {
	s := newTestStore(t)
	merged := mergedAtTime(t)
	mergedStr := testMergedAt

	seedPending(t, s, "rev", mergedStr)                            // will revert
	seedPending(t, s, "clean", mergedStr)                          // will survive
	seedPending(t, s, "fresh", merged.Add(0).Format(time.RFC3339)) // not due

	det := &mockDetector{obs: map[string]Observation{
		"rev": {RevertFound: true, RevertSHA: "r1"},
	}}

	// now = 8 days after merge: "rev" and "clean" are due; "fresh" (same merge
	// time) is also 8d old, so make it genuinely not-due by overriding its mergedAt.
	now := merged.Add(8 * 24 * time.Hour)

	// Re-seed "fresh" with a recent mergedAt so it is not yet due.
	s2 := newTestStore(t)
	seedPending(t, s2, "rev", mergedStr)
	seedPending(t, s2, "clean", mergedStr)
	seedPending(t, s2, "fresh", now.Add(-1*24*time.Hour).Format(time.RFC3339))

	res, err := Sweep(context.Background(), s2, det, now, 7)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Scanned != 3 {
		t.Errorf("scanned = %d, want 3", res.Scanned)
	}
	if res.Due != 2 {
		t.Errorf("due = %d, want 2 (fresh not due)", res.Due)
	}
	if res.Finalized != 2 {
		t.Errorf("finalized = %d, want 2", res.Finalized)
	}
	if res.ByVerdict[Reverted] != 1 || res.ByVerdict[Survived] != 1 {
		t.Errorf("verdict histogram = %v, want 1 reverted + 1 survived", res.ByVerdict)
	}

	// FinalizedRecords (#4152/#4153) must carry exactly the records that
	// transitioned this sweep, with their terminal verdicts, so a calibration
	// caller can act on them without a second full-journal scan.
	if len(res.FinalizedRecords) != 2 {
		t.Fatalf("FinalizedRecords len = %d, want 2", len(res.FinalizedRecords))
	}
	byVerdict := map[Verdict]int{}
	for _, r := range res.FinalizedRecords {
		byVerdict[r.Verdict]++
		if r.Verdict == Pending {
			t.Errorf("FinalizedRecords must never contain a still-pending record, got %+v", r)
		}
	}
	if byVerdict[Reverted] != 1 || byVerdict[Survived] != 1 {
		t.Errorf("FinalizedRecords verdicts = %v, want 1 reverted + 1 survived", byVerdict)
	}

	// "fresh" must remain pending.
	pend, _ := s2.Pending()
	if len(pend) != 1 || pend[0].MergeCommitSHA != "fresh" {
		t.Errorf("expected only 'fresh' pending, got %+v", pend)
	}
}

func TestSweep_DetectorErrorLeavesPending(t *testing.T) {
	s := newTestStore(t)
	seedPending(t, s, "boom", testMergedAt)
	det := &mockDetector{errOn: "boom"}
	now := mergedAtTime(t).Add(8 * 24 * time.Hour)

	res, err := Sweep(context.Background(), s, det, now, 7)
	if err != nil {
		t.Fatalf("sweep should not hard-error on a detection failure: %v", err)
	}
	if res.Errors != 1 {
		t.Errorf("errors = %d, want 1", res.Errors)
	}
	if res.Finalized != 0 {
		t.Errorf("finalized = %d, want 0", res.Finalized)
	}
	pend, _ := s.Pending()
	if len(pend) != 1 {
		t.Errorf("expected record to remain pending after detection error, got %d pending", len(pend))
	}
}

func TestSweep_SkipsNotDueWithoutCallingDetector(t *testing.T) {
	s := newTestStore(t)
	// merged "now" → nowhere near window.
	recent := mergedAtTime(t).Add(20 * 24 * time.Hour).Format(time.RFC3339)
	seedPending(t, s, "young", recent)
	det := &mockDetector{}
	now := mergedAtTime(t).Add(21 * 24 * time.Hour) // 1 day after merge

	res, err := Sweep(context.Background(), s, det, now, 7)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Due != 0 || res.Finalized != 0 {
		t.Errorf("due=%d finalized=%d, want 0/0", res.Due, res.Finalized)
	}
	if len(det.calls) != 0 {
		t.Errorf("detector should not be called for not-due records, got %d calls", len(det.calls))
	}
}

func TestSweep_EmptyStoreNoOp(t *testing.T) {
	s := newTestStore(t)
	res, err := Sweep(context.Background(), s, &mockDetector{}, time.Now(), 7)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Scanned != 0 || res.Finalized != 0 {
		t.Errorf("expected no-op on empty store, got %+v", res)
	}
}

func TestSweep_BreakageFinalizesBroke(t *testing.T) {
	s := newTestStore(t)
	seedPending(t, s, "brk", testMergedAt)
	det := &mockDetector{obs: map[string]Observation{"brk": {Broke: true, BrokeDetail: "ci"}}}
	now := mergedAtTime(t).Add(8 * 24 * time.Hour)

	res, err := Sweep(context.Background(), s, det, now, 7)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.ByVerdict[Broke] != 1 {
		t.Errorf("expected 1 broke verdict, got %v", res.ByVerdict)
	}
}
