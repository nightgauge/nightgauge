package health

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

func writeTrendsFile(t *testing.T, dir string, lines []string) {
	t.Helper()
	healthDir := layouttest.MkCheckoutSubdir(t, dir, "health")
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
	f, err := os.Create(filepath.Join(layouttest.MkLogsDir(t, dir), state.GateMetricsHistoryName))
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
	dir := layouttest.Repo(t)
	entries, err := ReadTrends(dir, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestReadTrends_ValidLines(t *testing.T) {
	dir := layouttest.Repo(t)
	lines := []string{
		`{"schema_version":"2","timestamp":"2026-01-01T00:00:00Z","run_id":"r1","issue_number":1,"overall_score":80,"status":"good","dimensions":{},"significant_findings":[]}`,
		`{"schema_version":"2","timestamp":"2026-01-02T00:00:00Z","run_id":"r2","issue_number":2,"overall_score":85,"status":"good","dimensions":{},"significant_findings":[]}`,
		`{"schema_version":"2","timestamp":"2026-01-03T00:00:00Z","run_id":"r3","issue_number":3,"overall_score":90,"status":"good","dimensions":{},"significant_findings":[]}`,
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
	dir := layouttest.Repo(t)
	lines := []string{
		`{"schema_version":"2","timestamp":"2026-01-01T00:00:00Z","run_id":"r1","issue_number":1,"overall_score":80,"status":"good","dimensions":{},"significant_findings":[]}`,
		`{not valid json}`,
		`{"schema_version":"2","timestamp":"2026-01-03T00:00:00Z","run_id":"r3","issue_number":3,"overall_score":90,"status":"good","dimensions":{},"significant_findings":[]}`,
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
	dir := layouttest.Repo(t)
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

// One series, one schema (#1898): a null score stays null, and a line of any
// other schema is not read as a health entry.
func TestReadTrends_NullScoreStaysNullAndOtherSchemasAreSkipped(t *testing.T) {
	dir := layouttest.Repo(t)
	writeTrendsFile(t, dir, []string{
		`{"schema_version":"2","timestamp":"2026-01-01T00:00:00Z","run_id":"r1","issue_number":1,"overall_score":null,"status":"no-data","dimensions":{},"excluded_dimensions":{"reliability":"no runs"},"significant_findings":[]}`,
		`{"schema_version":"2","timestamp":"2026-01-02T00:00:00Z","run_id":"r2","issue_number":2,"overall_score":81,"status":"good","dimensions":{"reliability":81},"excluded_dimensions":{},"significant_findings":[]}`,
		`{"schema_version":"1","timestamp":"2026-01-03T00:00:00Z","score":75,"status":"good"}`,
	})
	entries, err := ReadTrends(dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 schema-2 entries, got %d", len(entries))
	}
	if entries[0].OverallScore != nil {
		t.Fatalf("null overall_score decoded as %v, want nil", *entries[0].OverallScore)
	}
	if entries[0].ExcludedDimensions["reliability"] != "no runs" {
		t.Fatalf("excluded_dimensions not read: %v", entries[0].ExcludedDimensions)
	}
	if entries[1].OverallScore == nil || *entries[1].OverallScore != 81 {
		t.Fatalf("scored entry lost its score: %v", entries[1].OverallScore)
	}
}

// Legacy pretty-printed records span several lines; each line fails to parse,
// and the reader must report them once, not once per line (#2535).
func TestReadGateMetrics_MalformedLinesGiveOneSummaryWarning(t *testing.T) {
	dir := layouttest.Repo(t)
	writeGateFile(t, dir, []string{
		`{"schema_version":"1.0","issue_number":1,"gate_name":"build","result":"pass"}`,
		`{`,
		`  "gate_name": "legacy",`,
		`}`,
		`{"schema_version":"1.0","issue_number":2,"gate_name":"build","result":"catch"}`,
	})
	var warn bytes.Buffer
	entries, err := readGateMetrics(dir, &warn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 valid entries, got %d", len(entries))
	}
	out := strings.TrimSpace(warn.String())
	if strings.Count(out, "\n") != 0 || !strings.Contains(out, "skipped 3 malformed line(s)") {
		t.Fatalf("want one summary warning for 3 lines, got %q", out)
	}
}

// End to end: a record written from a linked worktree is read by
// `health gate-metrics` from the main checkout after the worktree is removed.
func TestReadGateMetrics_RecordFromRemovedWorktree(t *testing.T) {
	repo := layouttest.Repo(t)
	gittest.Run(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, repo, "worktree", "add", "-q", "--detach", linked)
	if err := state.AppendGateMetric(linked, 7, "verify-ui", "pass", "", "2026-10-25T00:00:00Z"); err != nil {
		t.Fatalf("append: %v", err)
	}
	gittest.Run(t, repo, "worktree", "remove", "--force", linked)

	entries, err := ReadGateMetrics(repo)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 1 || entries[0].GateName != "verify-ui" {
		t.Fatalf("want the worktree's verify-ui record, got %+v", entries)
	}
}
