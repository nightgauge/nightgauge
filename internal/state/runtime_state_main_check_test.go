package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The run record must carry what main did with the merge (#1249), and it must
// survive the snapshot copy and the on-disk round trip like the breadcrumb it
// sits next to.
func TestSetMainCheckOutcome_PersistsWithTheRunRecord(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1249, "PVTI_1", testRunID())
	rs.SetMergeOutcome("feedface", "2026-09-03T10:00:00Z")
	failing := []string{"e2e", "test"}
	rs.SetMainCheckOutcome("red", failing)
	failing[0] = "mutated" // the state must hold its own copy

	snap := rs.Snapshot()
	if snap.MainCheckVerdict != "red" {
		t.Errorf("Snapshot.MainCheckVerdict = %q, want red", snap.MainCheckVerdict)
	}
	if len(snap.MainCheckFailing) != 2 || snap.MainCheckFailing[0] != "e2e" {
		t.Errorf("Snapshot.MainCheckFailing = %v, want [e2e test]", snap.MainCheckFailing)
	}

	dir := t.TempDir()
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no snapshot written: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if onDisk["mainCheckVerdict"] != "red" {
		t.Errorf("on disk mainCheckVerdict = %v, want red", onDisk["mainCheckVerdict"])
	}
	if got, _ := onDisk["mainCheckFailing"].([]any); len(got) != 2 {
		t.Errorf("on disk mainCheckFailing = %v, want two names", onDisk["mainCheckFailing"])
	}
}

func TestSetMainCheckOutcome_EmptyVerdictDoesNotEraseAnObservation(t *testing.T) {
	rs := NewRuntimeState("o/r", 1, "", testRunID())
	rs.SetMainCheckOutcome("green", nil)
	rs.SetMainCheckOutcome("", []string{"x"})
	if rs.MainCheckVerdict != "green" || len(rs.MainCheckFailing) != 0 {
		t.Errorf("verdict=%q failing=%v after an empty verdict, want green / none", rs.MainCheckVerdict, rs.MainCheckFailing)
	}
	// A later observation with no failures clears the earlier list.
	rs.SetMainCheckOutcome("red", []string{"x"})
	rs.SetMainCheckOutcome("green", nil)
	if rs.MainCheckVerdict != "green" || rs.MainCheckFailing != nil {
		t.Errorf("verdict=%q failing=%v, want green with no failures", rs.MainCheckVerdict, rs.MainCheckFailing)
	}
}
