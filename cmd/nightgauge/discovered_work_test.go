package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
)

func runDiscoveredWork(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := discoveredWorkCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestDiscoveredWork_RecordListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out, err := runDiscoveredWork(t, "record", "--issue", "9999", "--run-dir", dir,
		"--summary", "Waiter ignores ceiling", "--evidence", "a", "--evidence", "b",
		"--source-stage", "producer:post-merge-impact-set", "--suggested-type", "bug",
		"--suggested-priority", "medium", "--suggested-size", "S",
		"--candidate-file", "internal/ci/wait.go", "--run-id", "run-1")
	if err != nil {
		t.Fatalf("record: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	if id != discoveredwork.ID("producer:post-merge-impact-set", "Waiter ignores ceiling") {
		t.Fatalf("printed id %q", id)
	}
	out, err = runDiscoveredWork(t, "list", "--issue", "9999", "--run-dir", dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var c discoveredwork.Capture
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := c.Records[0]
	if len(c.Records) != 1 || r.ID != id || r.Summary != "Waiter ignores ceiling" ||
		len(r.Evidence) != 2 || r.SourceStage != "producer:post-merge-impact-set" ||
		r.SuggestedType != "bug" || r.SuggestedPriority != "medium" || r.SuggestedSize != "S" ||
		len(r.CandidateTargetFiles) != 1 || r.RunID == nil || *r.RunID != "run-1" || r.RecordedAt == "" {
		t.Fatalf("round trip lost fields: %+v", c)
	}
	out, err = runDiscoveredWork(t, "record", "--issue", "9999", "--run-dir", dir,
		"--summary", "Waiter ignores ceiling", "--evidence", "c",
		"--source-stage", "producer:post-merge-impact-set", "--suggested-type", "bug",
		"--suggested-priority", "medium", "--suggested-size", "S")
	if err != nil || !strings.Contains(out, "already recorded") {
		t.Fatalf("re-record: %v %q", err, out)
	}
}

func TestDiscoveredWork_MalformedRecordNamesField(t *testing.T) {
	dir := t.TempDir()
	_, err := runDiscoveredWork(t, "record", "--issue", "9999", "--run-dir", dir,
		"--evidence", "y", "--source-stage", "feature-dev", "--suggested-type", "bug",
		"--suggested-priority", "medium", "--suggested-size", "S")
	if err == nil || !strings.HasPrefix(err.Error(), "summary:") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "discovered-work-9999.json")); !os.IsNotExist(err) {
		t.Fatal("a refused record created the capture file")
	}
}

// Zero-record parity: a run that records nothing leaves no capture file, and
// list/orphans report empty without creating one.
func TestDiscoveredWork_ZeroRecordParity(t *testing.T) {
	dir := t.TempDir()
	if out, err := runDiscoveredWork(t, "list", "--issue", "5", "--run-dir", dir); err != nil || !strings.Contains(out, "no discovered work") {
		t.Fatalf("list: %v %q", err, out)
	}
	if out, err := runDiscoveredWork(t, "orphans", "--run-dir", dir); err != nil || !strings.Contains(out, "no orphaned") {
		t.Fatalf("orphans: %v %q", err, out)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "discovered-work-*"))
	if len(m) != 0 {
		t.Fatalf("files created: %v", m)
	}
}
