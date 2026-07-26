package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedHistory writes raw records into a workspace's daily history file,
// bypassing the writer so a test can reproduce a corpus that is already
// corrupt — which is the only interesting input for the repair.
func seedHistory(t *testing.T, root string, day string, recs ...V2RunRecord) string {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, day+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range recs {
		b, mErr := json.Marshal(r)
		if mErr != nil {
			t.Fatal(mErr)
		}
		if _, wErr := f.Write(append(b, '\n')); wErr != nil {
			t.Fatal(wErr)
		}
	}
	return path
}

// withTokens returns a copy of rec carrying per-stage token data — the signal
// the repair uses to tell an authoritative record from a skeleton.
func withTokens(rec V2RunRecord, stages ...string) V2RunRecord {
	rec.Tokens.PerStage = map[string]V2StageTokens{}
	for _, s := range stages {
		rec.Tokens.PerStage[s] = V2StageTokens{Input: 1000, Output: 200, CacheRead: 5000, CostUSD: 0.25}
		rec.Tokens.EstimatedCostUSD += 0.25
	}
	return rec
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range splitLines(data) {
		if len(l) > 0 {
			n++
		}
	}
	return n
}

// TestRepairHistory_DryRunReportsButWritesNothing is the safety property that
// matters most: the default mode must be able to describe destructive work
// without doing any of it.
func TestRepairHistory_DryRunReportsButWritesNothing(t *testing.T) {
	root := t.TempDir()
	full := withTokens(makeRunRec("run-1", 7, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev"), "issue-pickup", "feature-dev")
	dup := makeRunRec("run-1", 7, "2026-07-19T09:00:00Z", "issue-pickup")
	skeleton := makeRunRec("run-1", 7, "2026-07-19T09:00:00Z")
	path := seedHistory(t, root, "2026-07-19", full, dup, skeleton)

	before := countLines(t, path)
	report, err := RepairHistory(root, false)
	if err != nil {
		t.Fatal(err)
	}

	if report.Applied {
		t.Error("dry run reported Applied=true")
	}
	if report.RunRecords != 3 || report.DistinctRuns != 1 || report.Duplicates != 2 {
		t.Errorf("run_records=%d distinct=%d duplicates=%d, want 3/1/2",
			report.RunRecords, report.DistinctRuns, report.Duplicates)
	}
	if after := countLines(t, path); after != before {
		t.Errorf("dry run rewrote the file: %d lines before, %d after", before, after)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".nightgauge", "pipeline", "history", "index.json")); statErr == nil {
		t.Error("dry run wrote an index.json — it must touch nothing")
	}
}

// TestRepairHistory_ApplyKeepsRichestRecord: the survivor must be the record
// carrying per-stage token data. Keeping a skeleton would discard the only copy
// of the run's cost, which is worse than leaving the duplicates in place.
func TestRepairHistory_ApplyKeepsRichestRecord(t *testing.T) {
	root := t.TempDir()
	// Deliberately ordered skeleton-first so the repair cannot pass by
	// accidentally keeping the first or last line.
	skeleton := makeRunRec("run-1", 7, "2026-07-19T09:00:00Z")
	full := withTokens(makeRunRec("run-1", 7, "2026-07-19T09:00:00Z", "issue-pickup", "feature-dev"), "issue-pickup", "feature-dev")
	dup := makeRunRec("run-1", 7, "2026-07-19T09:00:00Z", "issue-pickup")
	path := seedHistory(t, root, "2026-07-19", skeleton, full, dup)

	report, err := RepairHistory(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied {
		t.Error("apply run reported Applied=false")
	}
	if n := countLines(t, path); n != 1 {
		t.Fatalf("lines after repair = %d, want 1", n)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept V2RunRecord
	if err := json.Unmarshal(splitLines(data)[0], &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept.Tokens.PerStage) != 2 {
		t.Errorf("survivor has %d per-stage token entries, want 2 (the richest record, not a skeleton)",
			len(kept.Tokens.PerStage))
	}
	if kept.Tokens.EstimatedCostUSD == 0 {
		t.Error("survivor records no cost — the repair discarded the only measured record")
	}

	// The index is a projection of the JSONL and must be rebuilt, or it keeps
	// pointing at records the repair just removed.
	idx := readIndexFile(t, root)
	if idx.TotalRuns != 1 || len(idx.Entries) != 1 {
		t.Errorf("index total_runs=%d entries=%d, want 1/1", idx.TotalRuns, len(idx.Entries))
	}
}

// TestRepairHistory_CollapsesDifferingTimestampFormats: the on-disk duplicates
// carry no run_id and spell the same instant differently. Exact-match dedup
// leaves them all in place, which is why the repair compares instants.
func TestRepairHistory_CollapsesDifferingTimestampFormats(t *testing.T) {
	root := t.TempDir()
	goStyle := withTokens(makeRunRec("", 141, "2026-06-06T14:54:43.559048-06:00", "issue-pickup", "feature-dev"), "issue-pickup")
	tsStyle := makeRunRec("", 141, "2026-06-06T20:54:43.624Z", "issue-pickup", "feature-dev")
	path := seedHistory(t, root, "2026-06-06", goStyle, tsStyle)

	report, err := RepairHistory(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.DistinctRuns != 1 {
		t.Errorf("distinct runs = %d, want 1 (one run, two timestamp spellings)", report.DistinctRuns)
	}
	if n := countLines(t, path); n != 1 {
		t.Fatalf("lines after repair = %d, want 1", n)
	}
}

// TestRepairHistory_PreservesNonRunRecords: outcome records and unparseable
// lines are not the repair's business and must survive it untouched.
func TestRepairHistory_PreservesNonRunRecords(t *testing.T) {
	root := t.TempDir()
	full := withTokens(makeRunRec("run-1", 7, "2026-07-19T09:00:00Z", "issue-pickup"), "issue-pickup")
	dup := makeRunRec("run-1", 7, "2026-07-19T09:00:00Z", "issue-pickup")
	path := seedHistory(t, root, "2026-07-19", full, dup)

	// Append a non-run record and a line this binary cannot parse.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"record_type\":\"outcome\",\"issue_number\":7}\nnot json at all\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	report, err := RepairHistory(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.NonRunRecords != 2 {
		t.Errorf("non-run records = %d, want 2", report.NonRunRecords)
	}
	// 1 surviving run record + 2 passthrough lines.
	if n := countLines(t, path); n != 3 {
		t.Fatalf("lines after repair = %d, want 3 (1 run + 2 preserved)", n)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "not json at all") {
		t.Error("repair discarded an unparseable line instead of preserving it")
	}
}

// TestRepairHistory_ReportsForeignAndUnattributedRecords: a directory holding
// another repository's runs is the contamination symptom. The repair reports it
// and must NOT relocate anything — nothing on an unattributed record says where
// it belongs, so a move would be a guess.
func TestRepairHistory_ReportsForeignAndUnattributedRecords(t *testing.T) {
	root := t.TempDir()
	own := withTokens(makeRunRec("run-own", 1, "2026-07-19T09:00:00Z", "issue-pickup"), "issue-pickup")
	own.Repo = "example/repo-a"
	own2 := withTokens(makeRunRec("run-own-2", 2, "2026-07-19T09:10:00Z", "issue-pickup"), "issue-pickup")
	own2.Repo = "example/repo-a"
	foreign := withTokens(makeRunRec("run-foreign", 3, "2026-07-19T09:20:00Z", "issue-pickup"), "issue-pickup")
	foreign.Repo = "example/repo-b"
	orphan := withTokens(makeRunRec("run-orphan", 4, "2026-07-19T09:30:00Z", "issue-pickup"), "issue-pickup")
	orphan.Repo = ""
	path := seedHistory(t, root, "2026-07-19", own, own2, foreign, orphan)

	report, err := RepairHistory(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Unattributed != 1 {
		t.Errorf("unattributed = %d, want 1", report.Unattributed)
	}
	if report.ForeignRepos["example/repo-b"] != 1 {
		t.Errorf("foreign repos = %v, want example/repo-b:1", report.ForeignRepos)
	}
	// All four are distinct runs — the repair drops nothing and, crucially,
	// moves nothing out of this directory.
	if n := countLines(t, path); n != 4 {
		t.Fatalf("lines after repair = %d, want 4 (repair must not relocate records)", n)
	}
}
