package scanfailures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/logretention"
)

// writeLog writes a session log fixture in workdir's clone logs directory.
func writeLog(t *testing.T, workdir, name string, lines []string) {
	t.Helper()
	dir := layouttest.MkLogsDir(t, workdir)
	body := strings.Join(lines, "\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestScan_OutsideGitRepositoryIsAnError(t *testing.T) {
	_, err := Scan(Options{Workdir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("Scan outside a repository: err = %v, want not a git repository", err)
	}
}

func TestScan_MissingLogsDir(t *testing.T) {
	dir := layouttest.Repo(t)
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
	dir := layouttest.Repo(t)
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
	dir := layouttest.Repo(t)
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
	dir := layouttest.Repo(t)
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
	dir := layouttest.Repo(t)
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
	dir := layouttest.Repo(t)
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
	dir := layouttest.Repo(t)
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
	dir := layouttest.Repo(t)
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

// TestScan_AfterRetentionPrune (#2029): after log retention deletes old
// session logs, a --since range reaching back past them is absent, not an
// error: no warning, the surviving logs still scan, and OldestLogDate names
// the earliest log left.
func TestScan_AfterRetentionPrune(t *testing.T) {
	dir := layouttest.Repo(t)
	logs := layouttest.LogsDir(t, dir)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, f := range []struct {
		name string
		age  time.Duration
	}{
		{"2026-07-01_11_session.log", 90 * 24 * time.Hour},
		{"2026-08-01_12_session.log", 59 * 24 * time.Hour},
		{"2026-09-20_13_session.log", 9 * 24 * time.Hour},
	} {
		writeLog(t, dir, f.name, []string{"build failed with rc=2"})
		mt := now.Add(-f.age)
		if err := os.Chtimes(filepath.Join(logs, f.name), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	res, err := logretention.Prune(logs, logretention.Options{
		Policy:   logretention.Policy{MaxAge: 30 * 24 * time.Hour},
		Now:      now,
		InFlight: func(int) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 2 {
		t.Fatalf("prune deleted %d files, want 2", len(res.Deleted))
	}

	got, err := Scan(Options{Workdir: dir, Since: "2026-06-01"})
	if err != nil {
		t.Fatalf("Scan after prune: %v", err)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings after prune = %v, want none", got.Warnings)
	}
	if got.LogFilesScanned != 1 || got.FilesWithSignals != 1 {
		t.Errorf("scanned=%d signals=%d, want 1 and 1", got.LogFilesScanned, got.FilesWithSignals)
	}
	if got.OldestLogDate != "2026-09-20" {
		t.Errorf("OldestLogDate = %q, want 2026-09-20", got.OldestLogDate)
	}
}

// TestScan_LogVanishedMidScan: a log deleted between the listing and the
// open (a dangling entry stands in for the race) is skipped silently.
func TestScan_LogVanishedMidScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := layouttest.Repo(t)
	writeLog(t, dir, "2026-09-20_13_session.log", []string{"ok"})
	logs := layouttest.LogsDir(t, dir)
	if err := os.Symlink(filepath.Join(logs, "gone"), filepath.Join(logs, "2026-09-01_9_session.log")); err != nil {
		t.Fatal(err)
	}
	got, err := Scan(Options{Workdir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 0 || got.LogFilesScanned != 1 {
		t.Errorf("warnings=%v scanned=%d, want none and 1", got.Warnings, got.LogFilesScanned)
	}
}
