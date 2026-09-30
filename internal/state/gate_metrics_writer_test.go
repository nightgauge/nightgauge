package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func TestAppendGateMetric_RoundTrip(t *testing.T) {
	ws := layouttest.Repo(t)

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
	ws := layouttest.Repo(t)
	if err := AppendGateMetric(ws, 1, "build", "maybe", "", "t"); err == nil {
		t.Error("expected error for invalid result")
	}
	if err := AppendGateMetric(ws, 1, "", "pass", "", "t"); err == nil {
		t.Error("expected error for empty gate name")
	}
}

// The gate-metrics log is per-checkout runtime state: it lives in CHECKOUT,
// never in the working tree, and outside a git checkout it is an error rather
// than a write into the directory the caller named (ADR-024 § 7).
func TestAppendGateMetric_WritesCheckoutNotWorkingTree(t *testing.T) {
	ws := layouttest.Repo(t)
	if err := AppendGateMetric(ws, 7, "build", "pass", "", "2026-06-25T00:00:00Z"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := os.Stat(layouttest.CheckoutPath(t, ws, GateMetricsName)); err != nil {
		t.Fatalf("gate-metrics.jsonl not in CHECKOUT: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".nightgauge")); !os.IsNotExist(err) {
		t.Fatalf("AppendGateMetric wrote into the working tree (stat err %v)", err)
	}

	plain := t.TempDir()
	if err := AppendGateMetric(plain, 7, "build", "pass", "", "t"); err == nil {
		t.Fatal("AppendGateMetric outside a git checkout: want an error")
	}
	if _, err := os.Stat(filepath.Join(plain, ".nightgauge")); !os.IsNotExist(err) {
		t.Fatalf("AppendGateMetric outside a git checkout wrote into it (stat err %v)", err)
	}
}
