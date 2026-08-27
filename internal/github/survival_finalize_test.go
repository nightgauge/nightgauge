package github

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// stubDetector reports a fixed observation and counts how many records it was
// asked about, so a test can distinguish "finalized nothing" from "never ran".
// Those look identical from the store, and telling them apart is the whole
// point of #992.
type stubDetector struct {
	obs      survival.Observation
	err      error
	observed int
}

func (s *stubDetector) Observe(context.Context, survival.Record) (survival.Observation, error) {
	s.observed++
	return s.obs, s.err
}

func seedPending(t *testing.T, root, repo string, issue int, mergedAt time.Time) {
	t.Helper()
	rec := survival.NewPending(repo, issue, issue+1000,
		fmt.Sprintf("sha%d", issue), mergedAt.UTC().Format(time.RFC3339), "")
	if _, err := survival.NewStore(root).Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func verdictOf(t *testing.T, root string, issue int) survival.Verdict {
	t.Helper()
	recs, err := survival.NewStore(root).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// The journal is append-only, so the LAST line for an issue is its state.
	var v survival.Verdict
	for _, r := range recs {
		if r.IssueNumber == issue {
			v = r.Verdict
		}
	}
	return v
}

// TestFinalizeDueSurvivalRecords_FinalizesADueRecord is RED-capable guard 1 from
// the issue: drive the non-autonomous entry point with a stub detector and
// assert the record's folded verdict is no longer pending.
func TestFinalizeDueSurvivalRecords_FinalizesADueRecord(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	seedPending(t, root, "o/r", 42, now.AddDate(0, 0, -30))

	det := &stubDetector{} // no revert, no breakage → survived
	res, err := FinalizeDueSurvivalRecordsWith(context.Background(), root, det, now, 7)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if det.observed == 0 {
		t.Fatal("the detector was never asked about the due record — finalization did not run")
	}
	if res.Finalized != 1 {
		t.Errorf("Finalized = %d, want 1", res.Finalized)
	}
	if got := verdictOf(t, root, 42); got == survival.Pending {
		t.Error("the record is still pending after a finalization pass")
	}
}

// TestFinalizeDueSurvivalRecords_LeavesUndueRecordsAlone guards the API budget:
// a record inside its window must cost zero detector calls.
func TestFinalizeDueSurvivalRecords_LeavesUndueRecordsAlone(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	seedPending(t, root, "o/r", 43, now.AddDate(0, 0, -1)) // 1d into a 7d window

	det := &stubDetector{}
	res, err := FinalizeDueSurvivalRecordsWith(context.Background(), root, det, now, 7)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if det.observed != 0 {
		t.Errorf("detector called %d time(s) for a record inside its window", det.observed)
	}
	if res.Finalized != 0 {
		t.Errorf("Finalized = %d, want 0", res.Finalized)
	}
	if got := verdictOf(t, root, 43); got != survival.Pending {
		t.Errorf("verdict = %q, want still pending", got)
	}
}

// TestFinalizeDueSurvivalRecords_EmptyStoreMakesNoCalls is the property that
// makes this safe on the post-merge hook, which runs on EVERY merge.
func TestFinalizeDueSurvivalRecords_EmptyStoreMakesNoCalls(t *testing.T) {
	det := &stubDetector{}
	res, err := FinalizeDueSurvivalRecordsWith(context.Background(), t.TempDir(), det, time.Now(), 7)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if det.observed != 0 {
		t.Errorf("detector called %d time(s) against an empty store", det.observed)
	}
	if res.Finalized != 0 {
		t.Errorf("Finalized = %d, want 0", res.Finalized)
	}
}

// TestFinalizeDueSurvivalRecords_IsIdempotent is the issue's last acceptance
// criterion: running twice over the same store must report finalized=0 the
// second time and append no duplicate terminal line.
func TestFinalizeDueSurvivalRecords_IsIdempotent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	seedPending(t, root, "o/r", 44, now.AddDate(0, 0, -30))

	det := &stubDetector{}
	first, err := FinalizeDueSurvivalRecordsWith(context.Background(), root, det, now, 7)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.Finalized != 1 {
		t.Fatalf("first pass Finalized = %d, want 1", first.Finalized)
	}

	second, err := FinalizeDueSurvivalRecordsWith(context.Background(), root, det, now, 7)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Finalized != 0 {
		t.Errorf("second pass Finalized = %d, want 0 — a completed finalization must not "+
			"be redone, or the hook re-spends detection on every merge forever", second.Finalized)
	}

	// Fold size, not line count: the journal is append-only by design, so the
	// assertion that matters is that the record resolves to ONE state.
	recs, err := survival.NewStore(root).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	terminal := 0
	for _, r := range recs {
		if r.IssueNumber == 44 && r.Verdict != survival.Pending {
			terminal++
		}
	}
	if terminal != 1 {
		t.Errorf("issue #44 has %d terminal line(s), want exactly 1", terminal)
	}
}

// TestFinalizeDueSurvivalRecords_DetectorErrorLeavesRecordPending guards the
// conservative direction: a GitHub failure must not fabricate a verdict.
func TestFinalizeDueSurvivalRecords_DetectorErrorLeavesRecordPending(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	seedPending(t, root, "o/r", 45, now.AddDate(0, 0, -30))

	det := &stubDetector{err: fmt.Errorf("github is down")}
	res, err := FinalizeDueSurvivalRecordsWith(context.Background(), root, det, now, 7)
	if err != nil {
		t.Fatalf("a per-record detection error must not fail the whole pass: %v", err)
	}
	if res.Finalized != 0 {
		t.Errorf("Finalized = %d, want 0 on a detection error", res.Finalized)
	}
	if got := verdictOf(t, root, 45); got != survival.Pending {
		t.Errorf("verdict = %q, want still pending — an unreachable GitHub is not evidence "+
			"that the merge survived", got)
	}
}
