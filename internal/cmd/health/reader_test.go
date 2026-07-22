package health

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTrendsFile(t *testing.T, dir string, lines []string) {
	t.Helper()
	healthDir := filepath.Join(dir, ".nightgauge", "health")
	if err := os.MkdirAll(healthDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(healthDir, "trends.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func writeGateFile(t *testing.T, dir string, lines []string) {
	t.Helper()
	healthDir := filepath.Join(dir, ".nightgauge", "health")
	if err := os.MkdirAll(healthDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(healthDir, "gate-metrics.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestReadTrends_Empty(t *testing.T) {
	dir := t.TempDir()
	entries, err := ReadTrends(dir, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestReadTrends_ValidLines(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"schema_version":"1","timestamp":"2026-01-01T00:00:00Z","run_id":"r1","issue_number":1,"overall_score":80,"dimensions":{},"significant_findings":[]}`,
		`{"schema_version":"1","timestamp":"2026-01-02T00:00:00Z","run_id":"r2","issue_number":2,"overall_score":85,"dimensions":{},"significant_findings":[]}`,
		`{"schema_version":"1","timestamp":"2026-01-03T00:00:00Z","run_id":"r3","issue_number":3,"overall_score":90,"dimensions":{},"significant_findings":[]}`,
	}
	writeTrendsFile(t, dir, lines)

	// limit=2 returns last 2 entries
	entries, err := ReadTrends(dir, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].IssueNumber != 2 || entries[1].IssueNumber != 3 {
		t.Fatalf("expected tail entries [2,3], got [%d,%d]", entries[0].IssueNumber, entries[1].IssueNumber)
	}
}

func TestReadTrends_SkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"schema_version":"1","timestamp":"2026-01-01T00:00:00Z","run_id":"r1","issue_number":1,"overall_score":80,"dimensions":{},"significant_findings":[]}`,
		`{not valid json}`,
		`{"schema_version":"1","timestamp":"2026-01-03T00:00:00Z","run_id":"r3","issue_number":3,"overall_score":90,"dimensions":{},"significant_findings":[]}`,
	}
	writeTrendsFile(t, dir, lines)

	entries, err := ReadTrends(dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 valid entries, got %d", len(entries))
	}
}

func TestReadGateMetrics_Empty(t *testing.T) {
	dir := t.TempDir()
	entries, err := ReadGateMetrics(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestAggregateGateMetrics(t *testing.T) {
	entries := []GateMetricsEntry{
		{GateName: "gate-1", Result: "pass", DurationMs: 100},
		{GateName: "gate-1", Result: "pass", DurationMs: 200},
		{GateName: "gate-1", Result: "catch", DurationMs: 150},
		{GateName: "gate-1", Result: "catch", DurationMs: 50},
		{GateName: "gate-1", Result: "catch", DurationMs: 100},
		{GateName: "gate-2", Result: "catch", DurationMs: 80},
	}

	aggs := AggregateGateMetrics(entries)

	if len(aggs) != 2 {
		t.Fatalf("expected 2 aggregates, got %d", len(aggs))
	}
	// sorted by name: gate-1 first
	g1 := aggs[0]
	if g1.GateName != "gate-1" {
		t.Fatalf("expected gate-1 first, got %s", g1.GateName)
	}
	if g1.Invocations != 5 {
		t.Fatalf("expected 5 invocations, got %d", g1.Invocations)
	}
	if g1.Catches != 3 {
		t.Fatalf("expected 3 catches, got %d", g1.Catches)
	}
	// hit_rate = 3 catches / (3 catches + 2 passes) = 0.6
	if g1.HitRate < 0.599 || g1.HitRate > 0.601 {
		t.Fatalf("expected hit rate ~0.6, got %f", g1.HitRate)
	}

	g2 := aggs[1]
	if g2.GateName != "gate-2" {
		t.Fatalf("expected gate-2 second, got %s", g2.GateName)
	}
	if g2.Catches != 1 || g2.HitRate != 1.0 {
		t.Fatalf("expected gate-2 catches=1 hit_rate=1.0, got catches=%d hit_rate=%f", g2.Catches, g2.HitRate)
	}
}
