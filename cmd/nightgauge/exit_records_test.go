package main

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
)

// writeTestRecord is a thin shim so tests can scatter records across days
// without re-implementing the daily file path logic.
func writeTestRecord(t *testing.T, root string, ts time.Time, rec diagnostics.StageExitRecord) {
	t.Helper()
	rec.Timestamp = ts.UTC().Format(time.RFC3339Nano)
	if err := diagnostics.WriteStageExitRecord(root, rec); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestTailExitRecords_DefaultLimit_NewestFirst(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()

	// 3 records today, oldest first
	writeTestRecord(t, root, now.Add(-2*time.Minute), diagnostics.StageExitRecord{
		Repo: "r", Issue: 1, Stage: "issue-pickup", Success: true,
	})
	writeTestRecord(t, root, now.Add(-1*time.Minute), diagnostics.StageExitRecord{
		Repo: "r", Issue: 2, Stage: "feature-planning", Success: true,
	})
	writeTestRecord(t, root, now, diagnostics.StageExitRecord{
		Repo: "r", Issue: 3, Stage: "feature-dev", Success: false, TerminalKind: "stall_kill",
	})

	got, err := tailExitRecords(root, 0, 20)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3", len(got))
	}
	// Newest first: issue 3, then 2, then 1.
	if got[0].Issue != 3 || got[1].Issue != 2 || got[2].Issue != 1 {
		t.Errorf("order = %d/%d/%d, want 3/2/1",
			got[0].Issue, got[1].Issue, got[2].Issue)
	}
}

func TestTailExitRecords_RespectsLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		writeTestRecord(t, root, time.Now(), diagnostics.StageExitRecord{
			Repo: "r", Issue: i, Stage: "feature-dev", Success: true,
		})
	}
	got, err := tailExitRecords(root, 0, 3)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d, want 3", len(got))
	}
}

func TestTailExitRecords_FiltersByIssue(t *testing.T) {
	root := t.TempDir()
	writeTestRecord(t, root, time.Now(), diagnostics.StageExitRecord{
		Repo: "r", Issue: 3591, Stage: "feature-planning", Success: false,
	})
	writeTestRecord(t, root, time.Now(), diagnostics.StageExitRecord{
		Repo: "r", Issue: 42, Stage: "feature-dev", Success: true,
	})
	writeTestRecord(t, root, time.Now(), diagnostics.StageExitRecord{
		Repo: "r", Issue: 3591, Stage: "feature-dev", Success: false,
	})

	got, err := tailExitRecords(root, 3591, 20)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (only issue 3591)", len(got))
	}
	for _, rec := range got {
		if rec.Issue != 3591 {
			t.Errorf("filter leak: got issue %d", rec.Issue)
		}
	}
}

func TestTailExitRecords_NoDirectoryReturnsEmpty(t *testing.T) {
	root := t.TempDir() // no exit-records subdir
	got, err := tailExitRecords(root, 0, 20)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d records", len(got))
	}
}

func TestFmtMs(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0"},
		{-100, "0"},
		{120, "120ms"},
		{1_500, "1.5s"},
		{59_999, "60.0s"},
		{60_000, "1m0s"},
		{125_500, "2m5s"},
	}
	for _, c := range cases {
		if got := fmtMs(c.ms); got != c.want {
			t.Errorf("fmtMs(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}
