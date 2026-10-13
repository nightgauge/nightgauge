package sessionlog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/scanfailures"
	"github.com/nightgauge/nightgauge/internal/credshape"
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
	w.RunStarted(1903, "01a0bea9-1669-745e-8be0-4dc1a7ccb83c", "nightgauge/nightgauge")
	w.StageStarted(1903, "issue-pickup")
	_, _ = w.Write([]byte("2026/09/20 14:03:07.123456 #1903: push rejected for https://x:" + token + "@github.com/o/r\n"))
	w.Append(1903, LevelError, "issue-pickup", "stage issue-pickup FAILED: exit 1\nsecond line of the reason")
	w.RunFinished(1903, "01a0bea9-1669-745e-8be0-4dc1a7ccb83c", false)

	name := FileName(1903, fixedNow)
	if name != "2026-09-20_1903_session.log" {
		t.Fatalf("file name = %q, want LogFileWriter's YYYY-MM-DD_<issue>_session.log", name)
	}
	lines := readLog(t, root, 1903)
	want := []string{
		"[2026-09-20T14:03:07.123Z] [INFO] [session] run 01a0bea9-1669-745e-8be0-4dc1a7ccb83c started for nightgauge/nightgauge",
		"[2026-09-20T14:03:07.123Z] [INFO] [issue-pickup] stage issue-pickup started",
		"[2026-09-20T14:03:07.123Z] [INFO] [issue-pickup] push rejected for https://x:" + credshape.Marker + "@github.com/o/r",
		"[2026-09-20T14:03:07.123Z] [ERROR] [issue-pickup] stage issue-pickup FAILED: exit 1",
		"[2026-09-20T14:03:07.123Z] [ERROR] [issue-pickup] | second line of the reason",
		"[2026-09-20T14:03:07.123Z] [ERROR] [session] run 01a0bea9-1669-745e-8be0-4dc1a7ccb83c finished: failed",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("session log =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for i, line := range lines {
		m := extensionLineRE.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line %d does not parse as the extension reads it: %q", i+1, line)
		}
		if _, err := time.Parse(time.RFC3339Nano, m[1]); err != nil {
			t.Errorf("line %d timestamp %q: %v", i+1, m[1], err)
		}
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
		w.RunStarted(1901, id, "o/r")
		w.RunStarted(1901, id, "o/r") // every state change reports it; one record
		w.StageStarted(1901, "feature-dev")
		w.RunFinished(1901, id, id == "run-b")
	}
	got := strings.Join(readLog(t, root, 1901), "\n")
	if n := strings.Count(got, "run run-a started"); n != 1 {
		t.Errorf("run-a announced %d times, want once", n)
	}
	for _, want := range []string{"[session] run run-a started", "run run-a finished: failed", "[session] run run-b started", "run run-b finished: succeeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "run-a finished") > strings.Index(got, "run run-b started") {
		t.Errorf("attempts are out of order:\n%s", got)
	}
}

// A line with no "#N:" prefix belongs to the run only when it is the only one
// active; with two, it cannot be attributed and is not guessed.
func TestUnprefixedLinesGoToTheOnlyActiveRun(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(7, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 [codex-provision] skipped user-defined MCP servers: x\n"))
	w.Track(8, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:08 an unattributable line\n"))

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

// A log entry's continuation lines belong to the entry's issue even with
// several runs active.
func TestContinuationLinesFollowTheirEntry(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(41, root)
	w.Track(42, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #42: stage tail:\nline two\n\tline three\n"))
	got := strings.Join(readLog(t, root, 42), "\n")
	for _, want := range []string{"stage tail:", "line two", "line three"} {
		if !strings.Contains(got, want) {
			t.Errorf("issue 42's log lacks %q:\n%s", want, got)
		}
	}
}

func testToken() string { return "gh" + "p_" + strings.Repeat("A1b2", 9) }

func pemKey() string {
	label := "RSA " + "PRIVATE KEY"
	return "-----BEGIN " + label + "-----\n" + strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n", 4) + "-----END " + label + "-----"
}

// assertRedacted fails when the log holds any piece of the token or the key
// body, or any byte the redactor did not see (an escape character).
func assertRedacted(t *testing.T, got string) {
	t.Helper()
	for _, leak := range []string{"A1b2A1b2", "p_A1b2", "MIIEvQIB", "\x1b"} {
		if strings.Contains(got, leak) {
			t.Errorf("session log leaks %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, credshape.Marker) {
		t.Errorf("session log shows no redaction marker:\n%s", got)
	}
}

// The security review's cases (#1903): a credential the redactor would find in
// one piece must be found however the text reaches the writer.
func TestRedactionSeesTheBytesItWrites(t *testing.T) {
	token := testToken()
	cases := []struct {
		name   string
		writes []string
		close  bool
	}{
		{"token split across two writes",
			[]string{"2026/09/20 14:03:07 #42: push with " + token[:10], token[10:] + " failed\n"}, false},
		{"token in the final unterminated line",
			[]string{"2026/09/20 14:03:07 #42: done\n", "2026/09/20 14:03:08 #42: token " + token}, true},
		{"token inside an escaped JSON string",
			[]string{`2026/09/20 14:03:07 #42: event {"content":"export GH_TOKEN=\"` +
				strings.Replace(token, "A", `\u0041`, -1) + `\"\n"}` + "\n"}, false},
		{"key block inside an escaped JSON string",
			[]string{`2026/09/20 14:03:07 #42: event {"content":"` + strings.ReplaceAll(pemKey(), "\n", `\n`) + `"}` + "\n"}, false},
		{"token wrapped in and split by terminal escapes",
			[]string{"2026/09/20 14:03:07 #42: \x1b[31m" + token[:12] + "\x1b[0m" + token[12:] + "\x1b[0m\n"}, false},
		{"token split by a carriage return",
			[]string{"2026/09/20 14:03:07 #42: " + token[:12] + "\r" + token[12:] + "\n"}, false},
		{"key block split across two writes",
			[]string{"2026/09/20 14:03:07 #42: key " + pemKey()[:60], pemKey()[60:] + "\n"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, root := newTestWriter(t)
			w.Track(42, root)
			for _, piece := range tc.writes {
				_, _ = w.Write([]byte(piece))
			}
			if tc.close {
				_ = w.Close()
			}
			assertRedacted(t, strings.Join(readLog(t, root, 42), "\n"))
		})
	}
}

// A message handed to Append is redacted whole: a key block spanning its
// lines is found, and terminal escapes cannot split a token.
func TestAppendRedactsTheWholeMessage(t *testing.T) {
	w, root := newTestWriter(t)
	tok := testToken()
	w.Append(42, LevelError, "feature-dev", "stage failed:\n"+pemKey()+"\nwith \x1b[1m"+tok[:8]+"\x1b[0m"+tok[8:])
	assertRedacted(t, strings.Join(readLog(t, root, 42), "\n"))
}

// Close writes the held tail even when it holds no secret.
func TestCloseFlushesTheHeldTail(t *testing.T) {
	w, root := newTestWriter(t)
	w.Track(42, root)
	_, _ = w.Write([]byte("2026/09/20 14:03:07 #42: the last words"))
	if _, err := os.Stat(filepath.Join(layouttest.LogsDir(t, root), FileName(42, fixedNow))); !os.IsNotExist(err) {
		t.Fatalf("a partial line was written before its newline (err %v)", err)
	}
	_ = w.Close()
	if got := readLog(t, root, 42); len(got) != 1 || !strings.HasSuffix(got[0], "the last words") {
		t.Errorf("tail = %q", got)
	}
}
