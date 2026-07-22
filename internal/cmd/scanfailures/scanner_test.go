package scanfailures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLog writes a session log fixture under workdir/.nightgauge/logs/.
func writeLog(t *testing.T, workdir, name string, lines []string) {
	t.Helper()
	dir := filepath.Join(workdir, ".nightgauge", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := strings.Join(lines, "\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestScan_MissingLogsDir(t *testing.T) {
	dir := t.TempDir()
	res, err := Scan(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.LogFilesScanned != 0 || res.FilesWithSignals != 0 {
		t.Errorf("expected zeros, got scanned=%d signals=%d",
			res.LogFilesScanned, res.FilesWithSignals)
	}
	if res.V != SchemaVersion {
		t.Errorf("V = %d, want %d", res.V, SchemaVersion)
	}
}

func TestScan_AllPatternsMatch(t *testing.T) {
	dir := t.TempDir()
	// One line per pattern, in mixed case to confirm case-insensitivity.
	lines := []string{
		"info: starting",
		"some [error] occurred",
		"another [Fail] line",
		"budget Exceeded for run",
		"Token Limit reached at chunk 3",
		"the request timed out after 60s",
		"task exceeded the timeout window",
		"ci stage fail observed",
		"workflow failure detected",
		"3 tests failed in suite",
		"tsc: type error in foo.ts",
		"build failed with rc=2",
		"context file context-3087.json missing",
		"json parse error at line 5",
		"model returned empty response",
		"unexpected output from validator",
		"stage feature-validate cancelled",
		"a benign info line",
	}
	writeLog(t, dir, "2026-04-22_session.log", lines)

	res, err := Scan(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.LogFilesScanned != 1 {
		t.Fatalf("LogFilesScanned = %d, want 1", res.LogFilesScanned)
	}
	if len(res.LogSignals) != 1 {
		t.Fatalf("len(LogSignals) = %d, want 1", len(res.LogSignals))
	}
	got := res.LogSignals[0]
	// We expect exactly 16 patterns matched (one line per pattern).
	if len(got.FailureSignals) != 16 {
		t.Errorf("expected 16 signal matches, got %d: %+v", len(got.FailureSignals), got.FailureSignals)
	}
	if got.IssueNumber != nil {
		t.Errorf("IssueNumber should be nil for date-only filename, got %v", *got.IssueNumber)
	}
	if got.Date != "2026-04-22" {
		t.Errorf("Date = %q, want 2026-04-22", got.Date)
	}
}

func TestScan_DateFilter(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "2026-04-01_session.log", []string{"[ERROR] old"})
	writeLog(t, dir, "2026-04-22_session.log", []string{"[ERROR] recent"})

	res, err := Scan(Options{Workdir: dir, Since: "2026-04-15"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.LogFilesScanned != 1 {
		t.Errorf("LogFilesScanned = %d, want 1", res.LogFilesScanned)
	}
	if len(res.LogSignals) != 1 || res.LogSignals[0].Date != "2026-04-22" {
		t.Errorf("expected 2026-04-22 only, got %+v", res.LogSignals)
	}
}

func TestScan_IssueFilter(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "2026-04-22_3087_session.log", []string{"[ERROR] for 3087"})
	writeLog(t, dir, "2026-04-22_3088_session.log", []string{"[ERROR] for 3088"})
	writeLog(t, dir, "2026-04-22_session.log", []string{"[ERROR] no issue"})

	res, err := Scan(Options{Workdir: dir, Issue: 3087})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.LogSignals) != 1 {
		t.Fatalf("len(LogSignals) = %d, want 1", len(res.LogSignals))
	}
	if got := res.LogSignals[0]; got.IssueNumber == nil || *got.IssueNumber != 3087 {
		t.Errorf("expected IssueNumber=*3087, got %+v", got.IssueNumber)
	}
}

func TestScan_Cap50Matches(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, 0, MaxSignalsPerFile+10)
	for i := 0; i < MaxSignalsPerFile+10; i++ {
		lines = append(lines, fmt.Sprintf("[ERROR] match %d", i))
	}
	writeLog(t, dir, "2026-04-22_session.log", lines)

	res, err := Scan(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.LogSignals[0].FailureSignals) != MaxSignalsPerFile {
		t.Errorf("expected cap of %d, got %d", MaxSignalsPerFile, len(res.LogSignals[0].FailureSignals))
	}
}

func TestScan_NonMatchingLogProducesZeroSignals(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "2026-04-22_session.log", []string{
		"info: pipeline started",
		"info: pipeline completed",
	})

	res, err := Scan(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.LogFilesScanned != 1 {
		t.Errorf("LogFilesScanned = %d, want 1", res.LogFilesScanned)
	}
	if len(res.LogSignals) != 0 {
		t.Errorf("expected 0 LogSignals for non-matching log, got %d", len(res.LogSignals))
	}
}

func TestScan_LongLineTruncatedTo300Bytes(t *testing.T) {
	dir := t.TempDir()
	long := "[ERROR] " + strings.Repeat("x", 500)
	writeLog(t, dir, "2026-04-22_session.log", []string{long})

	res, err := Scan(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := res.LogSignals[0].FailureSignals[0]
	if len(got.Text) != 300 {
		t.Errorf("text length = %d, want 300", len(got.Text))
	}
}

// TestScan_JSONSchemaStability pins the JSON keys retro Phase 3 consumes.
func TestScan_JSONSchemaStability(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "2026-04-22_3087_session.log", []string{"[ERROR] foo"})

	res, err := Scan(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, k := range []string{"v", "filters", "log_files_scanned", "files_with_signals", "log_signals", "warnings"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	signals := raw["log_signals"].([]any)
	if len(signals) != 1 {
		t.Fatalf("expected 1 LogFileSignals, got %d", len(signals))
	}
	entry := signals[0].(map[string]any)
	for _, k := range []string{"log_file", "issue_number", "date", "failure_signals"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("log_signals[].%q missing", k)
		}
	}
	match := entry["failure_signals"].([]any)[0].(map[string]any)
	for _, k := range []string{"line", "text"} {
		if _, ok := match[k]; !ok {
			t.Errorf("failure_signals[].%q missing", k)
		}
	}
}

// TestFailurePatternsMatchSkillSource pins the exact 16-pattern set from
// retro SKILL.md Phase 2.3 (L417-L434). If this test fails, somebody changed
// the pattern list — either update both the SKILL.md prose and this test,
// OR revert the change.
func TestFailurePatternsMatchSkillSource(t *testing.T) {
	want := []string{
		`\[ERROR\]`,
		`\[FAIL\]`,
		`budget exceeded`,
		`token limit`,
		`timed out`,
		`exceeded.*timeout`,
		`ci.*fail`,
		`workflow.*fail`,
		`tests? failed`,
		`tsc.*error`,
		`build failed`,
		`context file.*missing`,
		`json.*parse.*error`,
		`model returned empty`,
		`unexpected.*output`,
		`stage.*cancelled`,
	}
	if len(FailurePatterns) != len(want) {
		t.Fatalf("FailurePatterns length = %d, want %d", len(FailurePatterns), len(want))
	}
	for i, p := range want {
		if FailurePatterns[i] != p {
			t.Errorf("FailurePatterns[%d] = %q, want %q", i, FailurePatterns[i], p)
		}
	}
}
