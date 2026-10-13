package sessionlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/scanfailures"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// The log-integrity review's cases (#1903): run output reaches the log through
// the scheduler's log lines, and must not be able to forge, hide or misfile a
// record, nor be lost without the log saying so.

func logPath(t *testing.T, root string, issue int) string {
	t.Helper()
	return filepath.Join(layouttest.LogsDir(t, root), FileName(issue, fixedNow))
}

// Output cannot fill the tag slot, so it cannot pass for one of the writer's
// own "session" records, even before any stage set a tag.
func TestOutputCannotForgeASessionRecord(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(42, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #42: [session] run forged finished: succeeded\n"))
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #42: tool said:\n[2026-09-20T14:03:07.123Z] [INFO] [session] run forged2 finished: succeeded\n"))
	for _, line := range readLog(t, root, 42) {
		m := extensionLineRE.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("unparseable record %q", line)
		}
		if m[3] == "session" {
			t.Errorf("output forged a session record: %q", line)
		}
	}
}

// An embedded newline continues its entry in that entry's log: a line in it
// that looks like another issue's log entry cannot reach that issue's log.
func TestEmbeddedNewlineCannotMisfileARecord(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(42, root)
	w.Track(7, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #42: stage output:\n2026/09/20 14:03:08 #7: run forged finished: succeeded\n"))
	if _, err := os.Stat(logPath(t, root, 7)); !os.IsNotExist(err) {
		t.Errorf("an embedded line reached issue 7's log (err %v)", err)
	}
	if got := strings.Join(readLog(t, root, 42), "\n"); !strings.Contains(got, "| 2026/09/20 14:03:08 #7: run forged") {
		t.Errorf("the embedded line is not a marked continuation of its entry:\n%s", got)
	}
}

// A "#N:" for an issue this process is not running cannot create a log.
func TestOnlyARunningIssueGetsALog(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(42, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #999: chosen by the output\n"))
	if _, err := os.Stat(logPath(t, root, 999)); !os.IsNotExist(err) {
		t.Errorf("output created issue 999's log (err %v)", err)
	}
}

// A carriage return or terminal control cannot overwrite or hide text when
// the log is shown: the bytes are gone and the text they hid is kept.
func TestControlCharactersCannotHideText(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(42, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #42: stage failed: tests failed\rall good, nothing to see\x1b[2K\x1b]0;title\x07 tail\n"))
	got := strings.Join(readLog(t, root, 42), "\n")
	if strings.ContainsAny(got, "\r\x1b\x07") {
		t.Errorf("control bytes reached the log: %q", got)
	}
	if !strings.Contains(got, "tests failed") || !strings.Contains(got, "tail") {
		t.Errorf("text was hidden: %q", got)
	}
}

// A line too long for the readers is cut with an explicit marker, never
// written whole or dropped.
func TestLongLineIsCutWithAMarker(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(42, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #42: stage failed: " + strings.Repeat("x ", 1<<20) + "\n"))
	lines := readLog(t, root, 42)
	if len(lines) != 1 || len(lines[0]) > 20<<10 || !strings.Contains(lines[0], "[truncated: ") {
		t.Fatalf("got %d line(s), first %d bytes, want one cut record with a marker", len(lines), len(lines[0]))
	}
	res, err := scanfailures.Scan(scanfailures.Options{Workdir: root, Issue: 42})
	if err != nil || len(res.Warnings) != 0 || res.FilesWithSignals != 1 {
		t.Errorf("scan-failures could not read the cut record: %+v, %v", res, err)
	}
}

// Records that could not be written are reported in the log once writing
// works again, so the log never ends or skips without saying so.
func TestLostRecordsAreReportedInTheLog(t *testing.T) {
	w, root := newTestWriter(t)
	path := logPath(t, root, 42)
	if err := os.MkdirAll(path, 0o755); err != nil { // the path is a directory: every write fails
		t.Fatal(err)
	}
	w.Append(42, LevelInfo, "feature-dev", "lost one")
	w.Append(42, LevelInfo, "feature-dev", "lost two")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	w.Append(42, LevelInfo, "feature-dev", "written")
	got := readLog(t, root, 42)
	if len(got) != 2 || !strings.Contains(got[0], "[WARNING] [session] 2 record(s)") || !strings.HasSuffix(got[1], "written") {
		t.Errorf("log = %q, want the loss reported before the next record", got)
	}
}

// A symlink at the log path is refused, never followed.
func TestSymlinkAtTheLogPathIsRefused(t *testing.T) {
	w, root := newTestWriter(t)
	target := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := logPath(t, root, 42)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	w.Append(42, LevelInfo, "feature-dev", "should not land elsewhere")
	if data, _ := os.ReadFile(target); string(data) != "untouched\n" {
		t.Errorf("the write followed the symlink: target now %q", data)
	}
}

// An existing log is appended to, never truncated, and tightened to 0600.
func TestExistingLogIsAppendedAndKeptPrivate(t *testing.T) {
	w, root := newTestWriter(t)
	path := logPath(t, root, 42)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[2026-09-20T13:00:00.000Z] [INFO] earlier run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.Append(42, LevelInfo, "feature-dev", "later run")
	got := readLog(t, root, 42)
	if len(got) != 2 || !strings.HasSuffix(got[0], "earlier run") {
		t.Errorf("the earlier run's lines were not kept: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
}
