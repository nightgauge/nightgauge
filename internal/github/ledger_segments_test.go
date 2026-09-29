package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The default-path ledger writes one segment per UTC day (#2029), so log
// retention can bound it by deleting whole old days.
func TestAPILedgerWritesDatedSegmentsAndRollsAtMidnight(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "github-api.jsonl")
	clock := time.Date(2026, 9, 28, 23, 59, 0, 0, time.UTC)
	l := &apiLedger{path: base, segmented: true, now: func() time.Time { return clock }, prev: map[string]int{}}
	if err := l.open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.close)

	l.record(APILedgerRecord{TS: clock.Format(time.RFC3339Nano), Kind: "graphql", Caller: "before"}, "")
	clock = clock.Add(2 * time.Minute)
	l.record(APILedgerRecord{TS: clock.Format(time.RFC3339Nano), Kind: "graphql", Caller: "after"}, "")

	for name, want := range map[string]string{
		"github-api-2026-09-28.jsonl": `"before"`,
		"github-api-2026-09-29.jsonl": `"after"`,
	} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("segment %s: %v", name, err)
		}
		if !strings.Contains(string(raw), want) || strings.Count(string(raw), "\n") != 1 {
			t.Errorf("%s = %q, want exactly the %s record", name, raw, want)
		}
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Errorf("the base path was written: %v", err)
	}
}

// Size rotation still bounds a single busy day.
func TestAPILedgerSegmentRotatesOnSize(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "github-api.jsonl")
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	l := &apiLedger{path: base, segmented: true, now: func() time.Time { return clock }, prev: map[string]int{}}
	if err := l.open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.close)
	seg := filepath.Join(dir, "github-api-2026-09-29.jsonl")
	if err := os.WriteFile(seg, make([]byte, ledgerMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	l.record(APILedgerRecord{TS: clock.Format(time.RFC3339Nano), Kind: "graphql"}, "")
	if _, err := os.Stat(seg + ".1"); err != nil {
		t.Fatalf("no size backup of the day's segment: %v", err)
	}
}

// Readers see the pre-segment files first, then every segment in date order
// with its size backup, and a since bound drops whole earlier days.
func TestLedgerFilesListsLegacyThenSegments(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "github-api.jsonl")
	for _, n := range []string{
		"github-api.jsonl.1", "github-api.jsonl",
		"github-api-2026-09-27.jsonl", "github-api-2026-09-29.jsonl.1", "github-api-2026-09-29.jsonl",
		"github-api-notadate.jsonl", "other.jsonl",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names := func(paths []string) string {
		var out []string
		for _, p := range paths {
			out = append(out, filepath.Base(p))
		}
		return strings.Join(out, ",")
	}
	want := "github-api.jsonl.1,github-api.jsonl,github-api-2026-09-27.jsonl,github-api-2026-09-29.jsonl.1,github-api-2026-09-29.jsonl"
	if got := names(LedgerFiles(base)); got != want {
		t.Errorf("LedgerFiles = %s\nwant %s", got, want)
	}
	since := time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	want = "github-api.jsonl.1,github-api.jsonl,github-api-2026-09-29.jsonl.1,github-api-2026-09-29.jsonl"
	if got := names(LedgerFilesSince(base, since)); got != want {
		t.Errorf("LedgerFilesSince = %s\nwant %s", got, want)
	}
}

// A window reaching past pruned days is absent, not an error; with no ledger
// at all the reader still says so.
func TestReadLedgerSinceTreatsPrunedDaysAsAbsent(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "github-api.jsonl")
	if _, err := ReadLedgerSince(base, time.Time{}); err == nil {
		t.Fatal("no ledger at all read as a clean empty window")
	}
	old := filepath.Join(dir, "github-api-2020-01-01.jsonl")
	if err := os.WriteFile(old, []byte(`{"ts":"2020-01-01T00:00:00Z","kind":"graphql"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadLedgerSince(base, time.Now().Add(-time.Hour))
	if err != nil || len(recs) != 0 {
		t.Fatalf("recs=%v err=%v, want an empty window and no error", recs, err)
	}
}

func TestLedgerSegmentNameIsUTC(t *testing.T) {
	loc := time.FixedZone("UTC-7", -7*3600)
	if got := LedgerSegmentName(time.Date(2026, 9, 29, 20, 0, 0, 0, loc)); got != "github-api-2026-09-30.jsonl" {
		t.Errorf("LedgerSegmentName = %s, want the UTC day github-api-2026-09-30.jsonl", got)
	}
}
