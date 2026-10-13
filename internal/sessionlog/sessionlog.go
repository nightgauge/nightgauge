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
//   - Line: [ISO-8601 UTC] [LEVEL] [tag] message, one record per line.
//
// One file per issue per day, not one per run: the extension finds an issue's
// log by the exact suffix _<issue>_session.log and lists logs by
// ^date_issue_session.log$, so a per-run name would be invisible to it. The
// file is only ever appended to (no O_TRUNC), so a retried dispatch never
// overwrites an earlier attempt, and each run's records open and close with
// "session" records naming the run id the scheduler assigned.
//
// Integrity. Run output reaches this log through the scheduler's log lines,
// so it must not be able to forge, hide or misfile a record:
//
//   - The tag slot is always filled by the writer: the stage, "pipeline"
//     before the first stage, or "session" for the writer's own records,
//     which no stage or output can take. Output only ever fills the message.
//   - Terminal escapes and every control character but tab and newline (a
//     carriage return included) are removed, so nothing can overwrite or
//     hide a line. A logger entry's embedded newlines continue that entry
//     ("| "), in that entry's log, and can never start a new one.
//   - The file is chosen only from issues this process is running (Track):
//     a "#N:" naming any other issue cannot create or reach N's log.
//   - A line over maxRecordBytes is cut with an explicit marker, and records
//     that could not be written are counted and reported by a "session"
//     record once writing works again (and on stderr when it first fails).
//   - The file is opened O_APPEND|O_CREATE with O_NOFOLLOW, refused unless it
//     is a regular file, and kept at mode 0600.
//
// Redaction. The bytes written are the bytes the core's credential redactor
// produced: normalised input (escapes and controls removed, a JSON \u escape
// of a token character decoded) is held to line boundaries across writes and
// passed through one credshape.Stream, so a credential or key block split
// across writes is still found, and every formatted buffer passes
// credshape.RedactEgress again immediately before the write. Close flushes
// the held tail through the stream.
package sessionlog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// Levels, as LogFileWriter spells them.
const (
	LevelInfo  = "INFO"
	LevelWarn  = "WARNING"
	LevelError = "ERROR"
)

const (
	// SessionTag tags the writer's own records: a run's start and end, and a
	// report of records that could not be written.
	SessionTag = "session"
	// PipelineTag tags a run's lines before its first stage.
	PipelineTag = "pipeline"
	// ContinuationPrefix opens each line after the first of one entry.
	ContinuationPrefix = "| "
	// maxRecordBytes bounds one record's message; past it the message is cut
	// and says how much was cut. Readers scan lines of up to 1 MiB.
	maxRecordBytes = 16 << 10
	// maxHeldLine bounds the logger output held while waiting for a newline.
	// The standard logger ends every entry with one, so only another writer
	// reaches it; past it the held text is passed on, still through the
	// redacting stream.
	maxHeldLine = 1 << 20
	// entryMark marks where a logger entry begins in the text passed through
	// the redacting stream. normalize removes every NUL from input, so only
	// the writer can put one there.
	entryMark = "\x00"
)

// FileName is the session log's base name for issue on t's UTC day,
// LogFileWriter.generateFilename's form.
func FileName(issue int, t time.Time) string {
	return fmt.Sprintf("%s_%d_session.log", t.UTC().Format("2006-01-02"), issue)
}

// FormatLine renders one record without its newline, LogFileWriter.formatEntry's
// form. An empty tag omits it; the writer itself always passes one.
func FormatLine(t time.Time, level, tag, message string) string {
	if tag != "" {
		tag = "[" + tag + "] "
	}
	return fmt.Sprintf("[%s] [%s] %s%s", t.UTC().Format("2006-01-02T15:04:05.000Z"),
		strings.ToUpper(level), tag, message)
}

// lostRecords counts the records of an issue that could not be written since
// the last successful write, and why.
type lostRecords struct {
	records int
	err     error
}

// Writer appends run output to per-issue session logs. It is also an
// io.Writer for the standard logger: an entry the scheduler logs as "#N: …"
// for an issue this process is running is filed under issue N. Safe for
// concurrent use.
type Writer struct {
	mu          sync.Mutex
	defaultRoot string
	now         func() time.Time
	roots       map[int]string
	stages      map[int]string
	active      map[int]bool
	runs        map[int]string
	lost        map[int]lostRecords

	// held is logger output not yet ended by a newline. stream redacts the
	// logger output across writes, and released is its output not yet ended
	// by a newline. atStart is true when the next logger write begins an
	// entry, nextStart when the next released line does. entry and
	// entryLevel are the issue and level of the entry being read, which its
	// continuation lines take.
	held       []byte
	stream     *credshape.Stream
	released   string
	atStart    bool
	nextStart  bool
	entry      int
	entryLevel string
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
		runs:        map[int]string{},
		lost:        map[int]lostRecords{},
		stream:      credshape.NewStream(),
		atStart:     true,
	}
}

// Track records that this process runs issue, in root, whose logs directory
// takes the log, and marks the run active. An empty root keeps the default.
func (w *Writer) Track(issue int, root string) {
	if issue <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if root != "" {
		w.roots[issue] = root
	} else if _, ok := w.roots[issue]; !ok {
		w.roots[issue] = w.defaultRoot
	}
	w.active[issue] = true
}

// Tracked reports whether Track has recorded issue.
func (w *Writer) Tracked(issue int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.roots[issue]
	return ok
}

// StageStarted makes stage the tag of issue's following lines.
func (w *Writer) StageStarted(issue int, stage string) {
	w.mu.Lock()
	w.stages[issue] = normalize(stage)
	w.active[issue] = true
	w.mu.Unlock()
	w.Append(issue, LevelInfo, stage, "stage "+stage+" started")
}

// RunStarted writes the run's opening record the first time runID, the run
// identity the scheduler assigned, is seen for issue.
func (w *Writer) RunStarted(issue int, runID, repo string) {
	if issue <= 0 || runID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.runs[issue] == runID {
		return
	}
	w.runs[issue] = runID
	w.active[issue] = true
	w.messageLocked(issue, LevelInfo, SessionTag, fmt.Sprintf("run %s started for %s", runID, repo))
}

// RunFinished writes the run's closing record and marks it inactive.
func (w *Writer) RunFinished(issue int, runID string, success bool) {
	if issue <= 0 {
		return
	}
	outcome, level := "failed", LevelError
	if success {
		outcome, level = "succeeded", LevelInfo
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messageLocked(issue, level, SessionTag, fmt.Sprintf("run %s finished: %s", runID, outcome))
	delete(w.active, issue)
	delete(w.stages, issue)
	delete(w.runs, issue)
}

// Append writes message as one entry of issue's log, tagged stage, or the
// issue's current stage when stage is empty.
func (w *Writer) Append(issue int, level, stage, message string) {
	if issue <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	tag := normalize(stage)
	if tag == "" || tag == SessionTag {
		tag = w.tagLocked(issue)
	}
	w.messageLocked(issue, level, tag, message)
}

// messageLocked normalises and redacts message whole, so a key block spanning
// its lines is found, and records it as one entry: its first line, then each
// further line as a continuation.
func (w *Writer) messageLocked(issue int, level, tag, message string) {
	var lines []string
	for _, line := range strings.Split(credshape.RedactEgress(normalize(message)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(lines) > 0 {
			line = ContinuationPrefix + line
		}
		lines = append(lines, line)
	}
	w.recordLocked(issue, level, tag, lines)
}

// tagLocked is the tag of issue's next logged line: its stage, or
// PipelineTag before its first.
func (w *Writer) tagLocked(issue int) string {
	if s := w.stages[issue]; s != "" && s != SessionTag {
		return s
	}
	return PipelineTag
}

// recordLocked writes lines, already redacted, as issue's records. A report
// of records lost to an earlier failure goes first; the formatted bytes are
// redacted once more on the way out.
func (w *Writer) recordLocked(issue int, level, tag string, lines []string) {
	if len(lines) == 0 {
		return
	}
	now := w.now()
	var buf bytes.Buffer
	lost := w.lost[issue]
	if lost.records > 0 {
		buf.WriteString(FormatLine(now, LevelWarn, SessionTag, cutRecord(normalize(fmt.Sprintf(
			"%d record(s) of this log could not be written and are missing above this line: %v",
			lost.records, lost.err)))))
		buf.WriteByte('\n')
	}
	for _, line := range lines {
		buf.WriteString(FormatLine(now, level, tag, cutRecord(strings.TrimRight(line, " \t"))))
		buf.WriteByte('\n')
	}
	root := w.roots[issue]
	if root == "" {
		root = w.defaultRoot
	}
	if err := appendFile(root, FileName(issue, now), []byte(credshape.RedactEgress(buf.String()))); err != nil {
		if lost.records == 0 {
			// Never log.Printf here: the standard logger calls Write with
			// its own lock held, so a log line from inside it would deadlock.
			fmt.Fprintf(os.Stderr, "warning: the session log for #%d is not being written: %v\n", issue, err)
		}
		w.lost[issue] = lostRecords{records: lost.records + len(lines), err: err}
		return
	}
	delete(w.lost, issue)
}

// cutRecord bounds a record's message to maxRecordBytes, on a rune boundary,
// and says how much it cut.
func cutRecord(msg string) string {
	if len(msg) <= maxRecordBytes {
		return msg
	}
	cut := maxRecordBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return fmt.Sprintf("%s [truncated: %d more bytes not written]", msg[:cut], len(msg)-cut)
}

// errNotRegular reports a log path that is not a regular file.
var errNotRegular = errors.New("session log path is not a regular file")

// appendFile appends data to name in root's clone logs directory: one write
// on an O_APPEND descriptor, so concurrent writers never interleave inside a
// line. A symlink, FIFO or anything else at the path is refused, and the
// file is kept at mode 0600, as every log there (ADR-024 § 11).
func appendFile(root, name string, data []byte) error {
	dir, err := layout.CloneLogsDir(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", errNotRegular, path)
	}
	f, err := os.OpenFile(path, appendOpenFlags, 0o600)
	if err != nil {
		return err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = fmt.Errorf("%w: %s", errNotRegular, path)
		}
		return err
	}
	_ = f.Chmod(0o600)
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// Write takes logger output. Each write that follows a newline begins an
// entry; text is held to line boundaries, normalised and passed through the
// redacting stream. It never fails, so it can sit behind io.MultiWriter beside
// the terminal without cutting the terminal's copy short.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.held = append(w.held, p...)
	cut := bytes.LastIndexByte(w.held, '\n') + 1
	if cut == 0 && len(w.held) > maxHeldLine {
		cut = len(w.held)
	}
	if cut > 0 {
		w.feedLocked(w.held[:cut])
		w.held = append(w.held[:0], w.held[cut:]...)
	}
	return len(p), nil
}

// Close flushes the held tail of the logger output, redacted, as a final
// line. The Writer stays usable.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.held) > 0 {
		w.feedLocked(w.held)
		w.held = w.held[:0]
	}
	w.releaseLocked(w.stream.Flush())
	if w.released != "" {
		w.lineLocked(w.released, w.nextStart)
		w.released = ""
	}
	w.nextStart = false
	return nil
}

// feedLocked passes raw logger output through the redacting stream, marking
// where an entry begins.
func (w *Writer) feedLocked(raw []byte) {
	text := normalize(string(raw))
	if w.atStart {
		text = entryMark + text
	}
	w.atStart = bytes.HasSuffix(raw, []byte{'\n'})
	w.releaseLocked(w.stream.Write(text))
}

// releaseLocked files each complete line of the stream's redacted output. An
// entry mark ends the line before it and begins an entry.
func (w *Writer) releaseLocked(redacted string) {
	w.released += redacted
	for {
		i := strings.IndexAny(w.released, "\n"+entryMark)
		if i < 0 {
			return
		}
		line, sep := w.released[:i], w.released[i]
		w.released = w.released[i+1:]
		if line != "" || sep == '\n' {
			w.lineLocked(line, w.nextStart)
			w.nextStart = false
		}
		if sep == entryMark[0] {
			w.nextStart = true
		}
	}
}

// logPrefixRE is the standard logger's date and time prefix.
var logPrefixRE = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)? `)

// issueLineRE is the scheduler's per-issue entry: "#N: message".
var issueLineRE = regexp.MustCompile(`^#(\d+): ?(.*)$`)

// lineLocked files one redacted line of logger output. An entry names its
// issue with "#N:" when this process runs N; one that names no such issue
// goes to the only active run, when exactly one is active, and is dropped
// otherwise. Its continuation lines follow it.
func (w *Writer) lineLocked(line string, start bool) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if start {
		line = logPrefixRE.ReplaceAllString(line, "")
		w.entry = 0
		if m := issueLineRE.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && w.runningLocked(n) {
				w.entry, line = n, m[2]
			}
		}
		if w.entry == 0 && len(w.active) == 1 {
			for n := range w.active {
				w.entry = n
			}
		}
		w.entryLevel = levelOf(line)
	} else {
		line = ContinuationPrefix + line
	}
	if w.entry <= 0 {
		return
	}
	w.recordLocked(w.entry, w.entryLevel, w.tagLocked(w.entry), []string{line})
}

// runningLocked reports whether this process runs issue.
func (w *Writer) runningLocked(issue int) bool {
	_, tracked := w.roots[issue]
	return tracked || w.active[issue]
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

// escapeSeqRE matches complete terminal escape sequences: CSI (colours,
// cursor moves), OSC (titles, hyperlinks) ended by BEL or ST, and two-byte
// escapes. An incomplete one keeps its text; only its ESC is removed, by
// normalize's control strip, so nothing after it is hidden.
var escapeSeqRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b\n]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// jsonEscapeRE matches a JSON \u escape of an ASCII character.
var jsonEscapeRE = regexp.MustCompile(`\\u00[0-7][0-9a-fA-F]`)

// normalize removes what could split a credential out of its shape, or hide
// or overwrite text, while a reader still joins or shows it: terminal escape
// sequences and every control character but tab and newline (carriage
// return and NUL included), and a JSON \u escape of a character a credential
// is made of, which is decoded.
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
