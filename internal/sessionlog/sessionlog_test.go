package sessionlog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/scanfailures"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/logretention"
)

// extensionLineRE is LogFileWriter.parseLogLine's pattern, the Output
// window's reader of these files.
var extensionLineRE = regexp.MustCompile(`^\[([^\]]+)\] \[([^\]]+)\] (?:\[([^\]]+)\] )?(.+)$`)

var fixedNow = time.Date(2026, 9, 20, 14, 3, 7, 123_000_000, time.UTC)

func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	root := layouttest.Repo(t)
	w := New(root)
	w.now = func() time.Time { return fixedNow }
	return w, root
}

func readLog(t *testing.T, root string, issue int) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(layouttest.LogsDir(t, root), FileName(issue, fixedNow)))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// A CLI run's log is read back by every existing reader: the extension's line
// parser, `nightgauge logs scan-failures`, and log retention's issue key. A
// token the run printed never reaches the file.
func TestCLIRunLogReadsBackThroughTheExistingReaders(t *testing.T) {
	w, root := newTestWriter(t)
	token := "gh" + "p_" + strings.Repeat("A1b2", 9)

	w.Track(1903, root)
	_, _ = w.Write([]byte(`2026/09/20 14:03:07 #1903: run "01a0bea9-1669-745e-8be0-4dc1a7ccb83c" started for nightgauge/nightgauge` + "\n"))
	w.StageStarted(1903, "issue-pickup")
	_, _ = w.Write([]byte("2026/09/20 14:03:07.123456 #1903: push rejected for https://x:" + token + "@github.com/o/r\n"))
	w.Append(1903, LevelError, "issue-pickup", "stage issue-pickup FAILED: exit 1\nsecond line of the reason")
	w.RunFinished(1903, "01a0bea9-1669-745e-8be0-4dc1a7ccb83c", false)

	name := FileName(1903, fixedNow)
	if name != "2026-09-20_1903_session.log" {
		t.Fatalf("file name = %q, want LogFileWriter's YYYY-MM-DD_<issue>_session.log", name)
	}
	lines := readLog(t, root, 1903)
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for i, line := range lines {
		m := extensionLineRE.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line %d does not parse as the extension reads it: %q", i+1, line)
		}
		if _, err := time.Parse(time.RFC3339Nano, m[1]); err != nil {
			t.Errorf("line %d timestamp %q: %v", i+1, m[1], err)
		}
		if strings.Contains(line, token) || strings.Contains(line, "A1b2A1b2") {
			t.Errorf("line %d carries the token: %q", i+1, line)
		}
	}
	if !strings.Contains(lines[0], `run "01a0bea9-1669-745e-8be0-4dc1a7ccb83c" started`) || strings.Contains(lines[0], "2026/09/20") {
		t.Errorf("first line = %q, want the run's start without the logger's own prefix", lines[0])
	}
	if !strings.Contains(lines[2], "[issue-pickup] push rejected") {
		t.Errorf("a logged line is not tagged with the current stage: %q", lines[2])
	}
	if !strings.HasSuffix(lines[5], "[ERROR] run 01a0bea9-1669-745e-8be0-4dc1a7ccb83c finished: failed") {
		t.Errorf("last line = %q, want the run's closing entry", lines[5])
	}

	res, err := scanfailures.Scan(scanfailures.Options{Workdir: root, Issue: 1903})
	if err != nil {
		t.Fatalf("scan-failures: %v", err)
	}
	if res.LogFilesScanned != 1 || len(res.LogSignals) != 1 || res.LogSignals[0].IssueNumber == nil ||
		*res.LogSignals[0].IssueNumber != 1903 || len(res.LogSignals[0].FailureSignals) == 0 {
		t.Fatalf("scan-failures did not read the log back: %+v", res)
	}
	if n, ok := logretention.IssueOf(name); !ok || n != 1903 {
		t.Errorf("retention keys %q to issue %d (%v), want 1903", name, n, ok)
	}
}

// A retried dispatch of the same issue appends: the earlier attempt's record
// stays, and each attempt's lines open with its own run id.
func TestRetriedDispatchAppendsADistinctRecord(t *testing.T) {
	w, root := newTestWriter(t)
	for _, id := range []string{"run-a", "run-b"} {
		w.Track(1901, root)
		_, _ = w.Write([]byte(`#1901: run "` + id + `" started for o/r` + "\n"))
		w.StageStarted(1901, "feature-dev")
		w.RunFinished(1901, id, id == "run-b")
	}
	got := strings.Join(readLog(t, root, 1901), "\n")
	for _, want := range []string{`run "run-a" started`, "run run-a finished: failed", `run "run-b" started`, "run run-b finished: succeeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "run-a finished") > strings.Index(got, `"run-b" started`) {
		t.Errorf("attempts are out of order:\n%s", got)
	}
}

// A line with no "#N:" prefix belongs to the run only when it is the only one
// active; with two, it cannot be attributed and is not guessed.
func TestUnprefixedLinesGoToTheOnlyActiveRun(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(7, root)
	_, _ = w.Write([]byte("[codex-provision] skipped user-defined MCP servers: x\n"))
	w.Track(8, root)
	_, _ = w.Write([]byte("an unattributable line\n"))

	got := strings.Join(readLog(t, root, 7), "\n")
	if !strings.Contains(got, "[codex-provision] skipped") || strings.Contains(got, "unattributable") {
		t.Errorf("issue 7's log = %q", got)
	}
	if _, err := os.Stat(filepath.Join(layouttest.LogsDir(t, root), FileName(8, fixedNow))); !os.IsNotExist(err) {
		t.Errorf("issue 8 got a log from an unattributable line (err %v)", err)
	}
}

// The log is private to the user, as every log in the directory is.
func TestSessionLogIsPrivate(t *testing.T) {
	w, root := newTestWriter(t)
	w.Append(5, LevelInfo, "", "hello")
	info, err := os.Stat(filepath.Join(layouttest.LogsDir(t, root), FileName(5, fixedNow)))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 0600", perm)
	}
}
