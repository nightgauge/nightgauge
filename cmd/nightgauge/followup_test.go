package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
)

func runFollowup(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := followupMaterializeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestFollowupMaterialize_RejectsBadIssueNumber(t *testing.T) {
	for _, a := range []string{"0", "-3", "abc"} {
		if _, err := runFollowup(t, a, "--run-dir", t.TempDir()); err == nil {
			t.Errorf("accepted %q", a)
		}
	}
}

func TestFollowupMaterialize_ZeroRecordsIsSilent(t *testing.T) {
	out, err := runFollowup(t, "42", "--run-dir", t.TempDir(), "--workdir", t.TempDir())
	if err != nil || out != "" {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

// With no config the gate fails closed to report-only: no GitHub client is
// constructed (none is configured here), the would-file list is printed, and
// the capture is consumed.
func TestFollowupMaterialize_ReportOnlyConsumesWithoutClient(t *testing.T) {
	runDir := t.TempDir()
	rec := discoveredwork.Record{
		Summary: "Waiter ignores ceiling", Evidence: []string{"x"}, SourceStage: "feature-dev",
		IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "medium", SuggestedSize: "S",
	}
	if _, _, err := discoveredwork.Append(runDir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := runFollowup(t, "42", "--run-dir", runDir, "--workdir", t.TempDir())
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if !strings.Contains(out, "report-only: fail closed") || !strings.Contains(out, "would-file") {
		t.Fatalf("out = %s", out)
	}
	if _, err := os.Stat(filepath.Join(runDir, "discovered-work-42.json")); !os.IsNotExist(err) {
		t.Fatalf("capture not consumed: %v", err)
	}
}

func TestFollowupMaterialize_DryRunKeepsCapture(t *testing.T) {
	runDir := t.TempDir()
	rec := discoveredwork.Record{
		Summary: "Something", Evidence: []string{"x"}, SourceStage: "feature-dev",
		IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "medium", SuggestedSize: "S",
	}
	if _, _, err := discoveredwork.Append(runDir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runFollowup(t, "42", "--run-dir", runDir, "--dry-run", "--workdir", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "discovered-work-42.json")); err != nil {
		t.Fatalf("dry-run consumed the capture: %v", err)
	}
}
