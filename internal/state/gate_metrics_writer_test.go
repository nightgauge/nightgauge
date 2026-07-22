package state

import (
	"testing"
)

func TestAppendGateMetric_RoundTrip(t *testing.T) {
	ws := t.TempDir()

	if err := AppendGateMetric(ws, 4097, "build", "pass", "", "2026-06-25T00:00:00Z"); err != nil {
		t.Fatalf("append pass: %v", err)
	}
	if err := AppendGateMetric(ws, 4097, "adversarial-review", "catch", "correctness: off-by-one in loop bound", "2026-06-25T00:01:00Z"); err != nil {
		t.Fatalf("append catch: %v", err)
	}
	// A record for a different issue must not leak into the read.
	if err := AppendGateMetric(ws, 9999, "build", "catch", "", "2026-06-25T00:02:00Z"); err != nil {
		t.Fatalf("append other issue: %v", err)
	}

	got, err := ReadGateMetricsForIssue(ws, 4097)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d records for #4097, want 2 (other issue must be filtered)", len(got))
	}

	byGate := map[string]GateResult{}
	for _, r := range got {
		byGate[r.GateName] = r
	}
	if byGate["adversarial-review"].Result != "catch" {
		t.Errorf("adversarial-review result = %q, want catch", byGate["adversarial-review"].Result)
	}
	if byGate["adversarial-review"].ErrorSummary == "" {
		t.Errorf("adversarial-review error_summary should round-trip")
	}
	if byGate["build"].Result != "pass" {
		t.Errorf("build result = %q, want pass", byGate["build"].Result)
	}
}

func TestAppendGateMetric_RejectsBadInput(t *testing.T) {
	ws := t.TempDir()
	if err := AppendGateMetric(ws, 1, "build", "maybe", "", "t"); err == nil {
		t.Error("expected error for invalid result")
	}
	if err := AppendGateMetric(ws, 1, "", "pass", "", "t"); err == nil {
		t.Error("expected error for empty gate name")
	}
}
