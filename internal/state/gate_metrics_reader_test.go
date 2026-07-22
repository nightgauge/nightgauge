package state

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGateMetricsFile(t *testing.T, dir string, content string) string {
	t.Helper()
	healthDir := filepath.Join(dir, ".nightgauge", "health")
	if err := os.MkdirAll(healthDir, 0755); err != nil {
		t.Fatalf("create health dir: %v", err)
	}
	filePath := filepath.Join(healthDir, "gate-metrics.jsonl")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("write gate-metrics.jsonl: %v", err)
	}
	return dir
}

func TestReadGateMetricsForIssue(t *testing.T) {
	t.Run("file not found returns nil nil", func(t *testing.T) {
		dir := t.TempDir()
		got, err := ReadGateMetricsForIssue(dir, 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil slice, got %v", got)
		}
	})

	t.Run("empty file returns empty slice", func(t *testing.T) {
		dir := t.TempDir()
		writeGateMetricsFile(t, dir, "")
		got, err := ReadGateMetricsForIssue(dir, 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %v", got)
		}
	})

	t.Run("valid JSONL with matching issue returns records", func(t *testing.T) {
		dir := t.TempDir()
		content := `{"schema_version":"1","timestamp":"2026-03-16T10:00:00Z","issue_number":42,"gate_name":"build","result":"pass","duration_ms":1234}
{"schema_version":"1","timestamp":"2026-03-16T10:01:00Z","issue_number":42,"gate_name":"unit-tests","result":"catch","duration_ms":567,"error_summary":"2 tests failed"}
`
		writeGateMetricsFile(t, dir, content)
		got, err := ReadGateMetricsForIssue(dir, 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 records, got %d: %v", len(got), got)
		}
		if got[0].GateName != "build" || got[0].Result != "pass" || got[0].DurationMs != 1234 {
			t.Errorf("unexpected record[0]: %+v", got[0])
		}
		if got[1].GateName != "unit-tests" || got[1].Result != "catch" || got[1].ErrorSummary != "2 tests failed" {
			t.Errorf("unexpected record[1]: %+v", got[1])
		}
	})

	t.Run("mixed issues returns only matching records", func(t *testing.T) {
		dir := t.TempDir()
		content := `{"schema_version":"1","timestamp":"2026-03-16T10:00:00Z","issue_number":99,"gate_name":"build","result":"pass"}
{"schema_version":"1","timestamp":"2026-03-16T10:01:00Z","issue_number":42,"gate_name":"lint","result":"pass"}
{"schema_version":"1","timestamp":"2026-03-16T10:02:00Z","issue_number":100,"gate_name":"type-check","result":"catch"}
`
		writeGateMetricsFile(t, dir, content)
		got, err := ReadGateMetricsForIssue(dir, 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 record, got %d: %v", len(got), got)
		}
		if got[0].GateName != "lint" {
			t.Errorf("unexpected gate_name: %q", got[0].GateName)
		}
	})

	t.Run("malformed lines are skipped", func(t *testing.T) {
		dir := t.TempDir()
		content := `{"schema_version":"1","timestamp":"2026-03-16T10:00:00Z","issue_number":42,"gate_name":"build","result":"pass"}
not valid json
{"schema_version":"1","timestamp":"2026-03-16T10:01:00Z","issue_number":42,"gate_name":"lint","result":"pass"}
`
		writeGateMetricsFile(t, dir, content)
		got, err := ReadGateMetricsForIssue(dir, 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 records (skipping malformed), got %d: %v", len(got), got)
		}
	})

	t.Run("optional duration and error_summary fields", func(t *testing.T) {
		dir := t.TempDir()
		// Record without duration_ms or error_summary
		content := `{"schema_version":"1","timestamp":"2026-03-16T10:00:00Z","issue_number":42,"gate_name":"build","result":"pass"}
`
		writeGateMetricsFile(t, dir, content)
		got, err := ReadGateMetricsForIssue(dir, 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 record, got %d", len(got))
		}
		if got[0].DurationMs != 0 {
			t.Errorf("DurationMs should be 0 when absent, got %d", got[0].DurationMs)
		}
		if got[0].ErrorSummary != "" {
			t.Errorf("ErrorSummary should be empty when absent, got %q", got[0].ErrorSummary)
		}
	})

	t.Run("timestamp is preserved", func(t *testing.T) {
		dir := t.TempDir()
		content := `{"schema_version":"1","timestamp":"2026-03-16T10:00:00Z","issue_number":42,"gate_name":"build","result":"pass"}
`
		writeGateMetricsFile(t, dir, content)
		got, err := ReadGateMetricsForIssue(dir, 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got[0].Timestamp != "2026-03-16T10:00:00Z" {
			t.Errorf("Timestamp = %q, want %q", got[0].Timestamp, "2026-03-16T10:00:00Z")
		}
	})
}
