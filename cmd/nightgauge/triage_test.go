package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/triage"
)

func writeTriageJSON(t *testing.T, rec triage.Record) string {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "triage.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func groundedRecord() triage.Record {
	return triage.Record{
		V:      triage.SchemaVersion,
		ID:     "e2e-sweep-20260901T120000Z",
		Target: triage.Target{Kind: "check", Value: "E2E Sweep", Repo: "owner/app"},
		History: triage.History{Checked: true, EverPassed: false,
			Detail: "never passed: 34 of 34 examined runs concluded, none successfully"},
		Reproduction: triage.Reproduction{
			Status:   triage.ReproLocal,
			Command:  "flutter test --tags=app-e2e integration_test/app_e2e/setup_flow_test.dart",
			Evidence: "pumpAndSettle timed out after 10m",
		},
		Hypotheses: []triage.Hypothesis{
			{Statement: "the link is never delivered", Verdict: triage.VerdictFalsified,
				Observation: "the mail catcher shows it delivered 1.2s in, and the tap handler fired"},
			{Statement: "an indeterminate progress indicator keeps the frame dirty",
				Verdict: triage.VerdictSupported, Observation: "widget dump during the hang shows value:null"},
		},
		Fix: &triage.Fix{Landed: true, Branch: "fix/settle", PR: "owner/app#380",
			Test: "setup_flow_test.dart::signs in from settings", TestFailsWithoutFix: true},
		TrackingIssue: "owner/app#379",
	}
}

func TestTriageRecord_WritesAndValidates(t *testing.T) {
	ws := t.TempDir()
	cmd := triageRecordCmd()
	cmd.SetArgs([]string{"--file", writeTriageJSON(t, groundedRecord()), "--workdir", ws})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("triage record: %v", err)
	}

	back, err := triage.Read(ws, "e2e-sweep-20260901T120000Z")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(back.Validate()) != 0 {
		t.Fatalf("round-tripped record does not validate: %+v", back.Validate())
	}
	if back.CreatedAt == "" {
		t.Error("CreatedAt should be stamped on write")
	}
}

func TestTriageCheck_ValidRecordPasses(t *testing.T) {
	ws := t.TempDir()
	if _, violations, err := triage.Write(ws, groundedRecord()); err != nil || len(violations) != 0 {
		t.Fatalf("Write: %v %+v", err, violations)
	}
	cmd := triageCheckCmd()
	cmd.SetArgs([]string{"--id", "e2e-sweep-20260901T120000Z", "--workdir", ws})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("triage check: %v", err)
	}
}

func TestTriageList_ReportsWrittenRecords(t *testing.T) {
	ws := t.TempDir()
	if _, _, err := triage.Write(ws, groundedRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	cmd := triageListCmd()
	cmd.SetArgs([]string{"--workdir", ws})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("triage list: %v", err)
	}
}

func TestTriageCheck_MissingRecordErrors(t *testing.T) {
	cmd := triageCheckCmd()
	cmd.SetArgs([]string{"--id", "nope", "--workdir", t.TempDir()})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a record that does not exist")
	}
}
