package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/testexec"
)

func TestGateRecordTestExecution_WritesRecord(t *testing.T) {
	ws := t.TempDir()
	cmd := gateRecordTestExecutionCmd()
	cmd.SetArgs([]string{
		"--issue", "1261", "--workdir", ws,
		"--file", "integration_test/app_e2e/setup_flow_test.dart",
		"--outcome", "pass",
		"--command", "flutter test --tags=app-e2e integration_test/app_e2e/setup_flow_test.dart",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("record-test-execution: %v", err)
	}

	records, err := testexec.ReadRecords(ws, 1261)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(records) != 1 || !records[0].Passed() {
		t.Fatalf("records = %+v, want one passing record", records)
	}
	if records[0].Command == "" {
		t.Error("the record must carry the command that was actually run")
	}
}

func TestGateRecordTestExecution_RejectsUnknownOutcome(t *testing.T) {
	ws := t.TempDir()
	cmd := gateRecordTestExecutionCmd()
	cmd.SetArgs([]string{"--issue", "1", "--workdir", ws, "--file", "a_test.dart", "--outcome", "probably"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unknown outcome")
	}
}

// TestGateCheckTestExecution_UnresolvableBaseIsQuiet — the CLI must exit 0 and
// say nothing when it cannot compute a diff. An inability to tell is not
// evidence of a missing execution.
func TestGateCheckTestExecution_UnresolvableBaseIsQuiet(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "pubspec.yaml"), []byte("name: fixture\n"), 0o644); err != nil {
		t.Fatalf("write pubspec: %v", err)
	}
	cmd := gateCheckTestExecutionCmd()
	cmd.SetArgs([]string{"--issue", "1", "--workdir", ws})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected a quiet exit 0, got %v", err)
	}
}
