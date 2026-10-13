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
// Redaction. A session log is a new place for a credential to land, so the
// bytes written are the bytes the core's credential redactor produced, never a
// form it did not see:
//
//  1. Input is normalised first: terminal escape sequences and other control
//     characters are removed and a JSON \u escape of a token character is
//     decoded. Each of those could split a credential into pieces that match
//     no shape while a terminal, a JSON reader or the eye still joins them.
//  2. The logger's output is held to line boundaries across writes, then
//     passed through one credshape.Stream, which redacts text arriving in
//     pieces: a credential or a PEM key block split across writes is still
//     found. Close flushes the held tail through it.
//  3. Every formatted buffer passes credshape.RedactEgress once more
//     immediately before the write.
//
// Each append is one write of whole lines, so a run killed mid-stage leaves a
// log whose every complete line still parses.
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
	"unicode"

	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// Levels, as LogFileWriter spells them.
const (
	LevelInfo  = "INFO"
	LevelWarn  = "WARNING"
	LevelError = "ERROR"
)

// maxHeldLine bounds the logger output held while waiting for a newline. The
// standard logger ends every entry with one, so only another writer reaches
// it; past it the held text is passed on, still through the redacting stream.
const maxHeldLine = 1 << 20

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
// io.Writer for the standard logger: an entry the scheduler logs as "#N: …"
// is filed under issue N. Safe for concurrent use.
type Writer struct {
	mu          sync.Mutex
	defaultRoot string
	now         func() time.Time
	roots       map[int]string
	stages      map[int]string
	active      map[int]bool
	warned      bool

	// held is logger output not yet ended by a newline; stream redacts the
	// logger output across writes; released is redacted output not yet ended
	// by a newline; entry is the issue of the logger entry being read, which
	// its continuation lines belong to.
	held     []byte
	stream   *credshape.Stream
	released string
	entry    int
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
		stream:      credshape.NewStream(),
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
	w.appendLocked(issue, level, "", credshape.RedactEgress(normalize(fmt.Sprintf("run %s finished: %s", runID, outcome))))
	delete(w.active, issue)
	delete(w.stages, issue)
}

// Append writes message as issue's entry, one line per line of message. An
// empty stage tags it with the issue's current stage. The message is redacted
// whole, so a key block spanning its lines is found.
func (w *Writer) Append(issue int, level, stage, message string) {
	if issue <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if stage == "" {
		stage = w.stages[issue]
	}
	w.appendLocked(issue, level, stage, credshape.RedactEgress(normalize(message)))
}

// appendLocked formats redacted, already-redacted text, as issue's entry and
// writes it, redacting the formatted bytes once more on the way out.
func (w *Writer) appendLocked(issue int, level, stage, redacted string) {
	now := w.now()
	stage = normalize(stage)
	var buf bytes.Buffer
	for _, line := range strings.Split(redacted, "\n") {
		line = strings.TrimRight(line, " \t")
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
	out := []byte(credshape.RedactEgress(buf.String()))
	if err := appendFile(root, FileName(issue, now), out); err != nil && !w.warned {
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

// Write takes logger output. Complete lines are normalised and passed through
// the redacting stream; a trailing partial line is held for the next write or
// Close. It never fails, so it can sit behind io.MultiWriter beside the
// terminal without cutting the terminal's copy short.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.held = append(w.held, p...)
	cut := bytes.LastIndexByte(w.held, '\n') + 1
	if cut == 0 && len(w.held) > maxHeldLine {
		cut = len(w.held)
	}
	if cut > 0 {
		w.releaseLocked(w.stream.Write(normalize(string(w.held[:cut]))))
		w.held = append(w.held[:0], w.held[cut:]...)
	}
	return len(p), nil
}

// Close flushes the held tail of the logger output, redacted, as a final
// line. The Writer stays usable.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	tail := ""
	if len(w.held) > 0 {
		tail = w.stream.Write(normalize(string(w.held)))
		w.held = w.held[:0]
	}
	w.releaseLocked(tail + w.stream.Flush())
	if w.released != "" {
		w.routeLocked(w.released)
		w.released = ""
	}
	return nil
}

// releaseLocked files each complete line of the stream's redacted output.
func (w *Writer) releaseLocked(redacted string) {
	w.released += redacted
	for {
		i := strings.IndexByte(w.released, '\n')
		if i < 0 {
			return
		}
		line := w.released[:i]
		w.released = w.released[i+1:]
		w.routeLocked(line)
	}
}

// logPrefixRE is the standard logger's date and time prefix, which opens
// every entry; a line without one continues the entry before it.
var logPrefixRE = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)? `)

// issueLineRE is the scheduler's per-issue entry: "#N: message".
var issueLineRE = regexp.MustCompile(`^#(\d+): ?(.*)$`)

// routeLocked files one redacted line of logger output. An entry that names
// no issue goes to the only active run, when exactly one is active, and is
// dropped otherwise; its continuation lines follow it.
func (w *Writer) routeLocked(line string) {
	prefixed := logPrefixRE.MatchString(line)
	line = logPrefixRE.ReplaceAllString(line, "")
	if m := issueLineRE.FindStringSubmatch(line); m != nil {
		w.entry, _ = strconv.Atoi(m[1])
		line = m[2]
	} else if prefixed || w.entry <= 0 {
		w.entry = 0
		if len(w.active) == 1 {
			for n := range w.active {
				w.entry = n
			}
		}
	}
	if w.entry <= 0 {
		return
	}
	w.appendLocked(w.entry, levelOf(line), w.stages[w.entry], line)
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

// escapeSeqRE matches terminal escape sequences: CSI (colours, cursor moves),
// OSC (titles, hyperlinks) ended by BEL or ST, and two-byte escapes.
var escapeSeqRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[@-Z\\-_]`)

// jsonEscapeRE matches a JSON \u escape of an ASCII character.
var jsonEscapeRE = regexp.MustCompile(`\\u00[0-7][0-9a-fA-F]`)

// normalize removes what could split a credential out of its shape while a
// reader still joins it: terminal escape sequences and every other control
// character but tab and newline (a carriage return included), and a JSON \u
// escape of a character a credential is made of, which is decoded.
func normalize(s string) string {
	s = escapeSeqRE.ReplaceAllString(s, "")
	s = jsonEscapeRE.ReplaceAllStringFunc(s, func(esc string) string {
		n, err := strconv.ParseUint(esc[2:], 16, 8)
		if err != nil {
			return esc
		}
		if r := rune(n); r == '_' || r == '-' || r == '.' || r == '/' || r == '+' || r == '=' ||
			('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			return string(r)
		}
		return esc
	})
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}
