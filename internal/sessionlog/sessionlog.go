// Package sessionlog writes the session log of a run started from the command
// line (#1903). Until it existed a `nightgauge run` wrote none: the extension's
// LogFileWriter was the only writer, so a terminal-started run's output lived
// only in that terminal's scrollback.
//
// It writes the file LogFileWriter writes, in its format, so every existing
// reader takes it unchanged: `nightgauge logs scan-failures`, log retention
// (logretention.IssueOf keys its in-flight guard on the name), and the
// extension's Output window (readEntriesForIssue, listLogs, parseLogLine).
//
//   - Location: the clone's logs directory, layout.CloneLogsDir.
//   - Name: YYYY-MM-DD_<issue>_session.log, the UTC day.
//   - Line: [ISO-8601 UTC] [LEVEL] [stage] message, one entry per line.
//
// One file per issue per day, not one per run: the extension finds an issue's
// log by the exact suffix _<issue>_session.log and lists logs by
// ^date_issue_session.log$, so a per-run name would be invisible to it. Every
// write appends, so a retried dispatch never overwrites an earlier attempt,
// and each run's lines open with the scheduler's "run <id> started" line and
// close with a "run <id> finished" entry, so the record names its run.
//
// Each entry is one append of whole lines, so a run killed mid-stage leaves a
// log whose every complete line still parses. Every message is redacted with
// credshape.RedactEgress before it reaches the file.
package sessionlog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// Levels, as LogFileWriter spells them.
const (
	LevelInfo  = "INFO"
	LevelWarn  = "WARNING"
	LevelError = "ERROR"
)

// FileName is the session log's base name for issue on t's UTC day,
// LogFileWriter.generateFilename's form.
func FileName(issue int, t time.Time) string {
	return fmt.Sprintf("%s_%d_session.log", t.UTC().Format("2006-01-02"), issue)
}

// FormatLine renders one entry without its newline, LogFileWriter.formatEntry's
// form. An empty stage omits the tag.
func FormatLine(t time.Time, level, stage, message string) string {
	tag := ""
	if stage != "" {
		tag = "[" + stage + "] "
	}
	return fmt.Sprintf("[%s] [%s] %s%s", t.UTC().Format("2006-01-02T15:04:05.000Z"),
		strings.ToUpper(level), tag, message)
}

// Writer appends run output to per-issue session logs. It is also an
// io.Writer for the standard logger: a line the scheduler logs as "#N: …" is
// filed under issue N. Safe for concurrent use.
type Writer struct {
	mu          sync.Mutex
	defaultRoot string
	now         func() time.Time
	roots       map[int]string
	stages      map[int]string
	active      map[int]bool
	warned      bool
}

// New returns a Writer whose logs live under defaultRoot's clone logs
// directory unless Track names another root for an issue.
func New(defaultRoot string) *Writer {
	return &Writer{
		defaultRoot: defaultRoot,
		now:         time.Now,
		roots:       map[int]string{},
		stages:      map[int]string{},
		active:      map[int]bool{},
	}
}

// Track records the root an issue's run executes in, whose logs directory
// takes its log, and marks the run active. An empty root keeps the default.
func (w *Writer) Track(issue int, root string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if root != "" {
		w.roots[issue] = root
	}
	w.active[issue] = true
}

// Tracked reports whether Track has named a root for issue.
func (w *Writer) Tracked(issue int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.roots[issue]
	return ok
}

// StageStarted makes stage the tag of issue's following lines.
func (w *Writer) StageStarted(issue int, stage string) {
	w.mu.Lock()
	w.stages[issue] = stage
	w.active[issue] = true
	w.mu.Unlock()
	w.Append(issue, LevelInfo, stage, "stage "+stage+" started")
}

// RunFinished writes the run's closing entry and marks it inactive.
func (w *Writer) RunFinished(issue int, runID string, success bool) {
	if issue <= 0 {
		return
	}
	outcome := "failed"
	level := LevelError
	if success {
		outcome, level = "succeeded", LevelInfo
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Untagged: the entry closes the run, not its last stage.
	w.appendLocked(issue, level, "", fmt.Sprintf("run %s finished: %s", runID, outcome))
	delete(w.active, issue)
	delete(w.stages, issue)
}

// Append writes message as issue's entry, one line per line of message. An
// empty stage tags it with the issue's current stage.
func (w *Writer) Append(issue int, level, stage, message string) {
	if issue <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if stage == "" {
		stage = w.stages[issue]
	}
	w.appendLocked(issue, level, stage, message)
}

func (w *Writer) appendLocked(issue int, level, stage, message string) {
	now := w.now()
	var buf bytes.Buffer
	for _, line := range strings.Split(credshape.RedactEgress(message), "\n") {
		line = strings.TrimRight(line, "\r \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		buf.WriteString(FormatLine(now, level, stage, line))
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return
	}
	root := w.roots[issue]
	if root == "" {
		root = w.defaultRoot
	}
	if err := appendFile(root, FileName(issue, now), buf.Bytes()); err != nil && !w.warned {
		// Never log.Printf here: the standard logger calls Write with its
		// own lock held, so a log line from inside it would deadlock.
		w.warned = true
		fmt.Fprintf(os.Stderr, "warning: the session log for #%d is not being written: %v\n", issue, err)
	}
}

// appendFile appends data to name in root's clone logs directory: one write
// on an O_APPEND descriptor, so concurrent writers never interleave inside a
// line. Mode 0600, as every log there (ADR-024 § 11).
func appendFile(root, name string, data []byte) error {
	dir, err := layout.CloneLogsDir(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// logPrefixRE is the standard logger's date and time prefix, any of its
// precisions.
var logPrefixRE = regexp.MustCompile(`^(?:\d{4}/\d{2}/\d{2} )?(?:\d{2}:\d{2}:\d{2}(?:\.\d+)? )?`)

// issueLineRE is the scheduler's per-issue log line: "#N: message".
var issueLineRE = regexp.MustCompile(`^#(\d+): ?(.*)$`)

// Write files each line of p under the issue its "#N:" prefix names. A line
// without one goes to the only active run, when exactly one is active, and is
// dropped otherwise. It never fails, so it can sit behind io.MultiWriter
// beside the terminal without ever cutting the terminal's copy short.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, raw := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		line := logPrefixRE.ReplaceAllString(raw, "")
		issue := 0
		if m := issueLineRE.FindStringSubmatch(line); m != nil {
			issue, _ = strconv.Atoi(m[1])
			line = m[2]
		} else if len(w.active) == 1 {
			for n := range w.active {
				issue = n
			}
		}
		if issue <= 0 {
			continue
		}
		w.appendLocked(issue, levelOf(line), w.stages[issue], line)
	}
	return len(p), nil
}

// levelOf classifies a scheduler log line the way an operator reads it.
func levelOf(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, " failed") || strings.HasPrefix(lower, "error") || strings.Contains(lower, "error:"):
		return LevelError
	case strings.HasPrefix(lower, "warn") || strings.Contains(lower, "warning"):
		return LevelWarn
	}
	return LevelInfo
}
