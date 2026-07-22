package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/history"
)

// traceSubdir is the project-relative directory per-run trace files live in.
const traceSubdir = ".nightgauge/pipeline/trace"

// tsLayout formats event timestamps as fixed-width RFC3339 with exactly three
// fractional (millisecond) digits and a literal Z, e.g. 2026-07-17T10:00:49.000Z.
//
// WHY fixed-width (ADR 013 total order; flaky #226): SortEvents — and every
// trace consumer — orders events by comparing Ts as strings, so the string
// order must equal chronological order. time.RFC3339Nano TRIMS trailing zeros,
// so a whole-second stamp ("…49Z") sorted AFTER a fractional one in the same
// second ("…49.5Z") because 'Z'(0x5A) > '.'(0x2E) — defeating chronological
// order AND the seq tiebreaker, which intermittently failed
// TestWriterSeqResumesFromExistingFile. A constant three-digit fraction makes
// lexicographic order == chronological order and matches the SDK producer
// (traceRecorder.ts: new Date().toISOString(), also fixed 3-digit ms), so a
// cross-producer same-millisecond tie correctly breaks by (producer, seq).
const tsLayout = "2006-01-02T15:04:05.000Z07:00"

// runIDPattern guards FilePath against path traversal: run ids are UUID v7
// strings (hex + dashes); anything else is rejected before touching the
// filesystem. Kept slightly loose (word chars) so remote run ids from the
// platform remain writable.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// Dir returns the absolute per-project trace directory. Not created here —
// the writer creates it on first append.
func Dir(rootDir string) string {
	return filepath.Join(rootDir, ".nightgauge", "pipeline", "trace")
}

// FilePath returns the absolute path of the per-run trace JSONL for runID,
// or an error when the run id fails the path-safety pattern.
func FilePath(rootDir, runID string) (string, error) {
	if !runIDPattern.MatchString(runID) {
		return "", fmt.Errorf("trace: invalid run id %q", runID)
	}
	return filepath.Join(Dir(rootDir), runID+".jsonl"), nil
}

// Writer appends events for one run. Nil-safe: every method no-ops on a nil
// receiver so call sites emit unconditionally. Fail-open: append errors are
// logged, never returned to the pipeline path.
type Writer struct {
	rootDir  string
	runID    string
	repo     string
	issue    int
	producer string
	path     string

	mu  sync.Mutex
	seq int64
}

// NewWriter creates a trace writer for one run rooted at the workspace root.
// Returns nil (a valid no-op writer) when rootDir or runID is empty or the
// run id is unsafe — callers do not need to guard.
//
// Seq resumes from the existing file's line count so a crash-restarted
// process keeps per-producer monotonicity (gaps are allowed by ADR 013;
// regressions are not).
func NewWriter(rootDir, runID, repo string, issue int) *Writer {
	if rootDir == "" || runID == "" {
		return nil
	}
	path, err := FilePath(rootDir, runID)
	if err != nil {
		log.Printf("trace: disabled for run: %v", err)
		return nil
	}
	return &Writer{
		rootDir:  rootDir,
		runID:    runID,
		repo:     repo,
		issue:    issue,
		producer: ProducerGo,
		path:     path,
		seq:      countLines(path),
	}
}

// RunID returns the run id this writer appends for ("" on a nil writer).
func (w *Writer) RunID() string {
	if w == nil {
		return ""
	}
	return w.runID
}

// Emit appends one event of the given kind. stage may be empty for
// run-scoped events. payload is one of the typed payload structs in
// events.go (or nil). Fail-open: errors are logged and swallowed.
func (w *Writer) Emit(kind Kind, stage string, payload any) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.seq++
	seq := w.seq
	w.mu.Unlock()

	ev := Event{
		SchemaVersion: SchemaVersion,
		RunID:         w.runID,
		Repo:          w.repo,
		Issue:         w.issue,
		Seq:           seq,
		Ts:            time.Now().UTC().Format(tsLayout),
		Stage:         stage,
		Kind:          kind,
		Producer:      w.producer,
		Payload:       payload,
	}
	if err := history.AppendJSONL(w.path, ev); err != nil {
		log.Printf("trace: append %s event failed (fail-open): %v", kind, err)
	}
}

// countLines returns the number of newline-terminated lines in the file at
// path, or 0 when the file does not exist / cannot be read. Used only to
// seed seq on writer creation.
func countLines(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var n int64
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		n++
	}
	return n
}

// ReadRun reads every event in a run's trace file, ordered by
// (ts, producer, seq) per ADR 013. Malformed lines are skipped with a
// warning on stderr rather than failing the read. A missing file returns
// (nil, nil) — "no trace" is a valid state for runs pre-dating capture.
func ReadRun(rootDir, runID string) ([]Event, error) {
	path, err := FilePath(rootDir, runID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("trace: open %s: %w", path, err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping malformed trace line in %s: %v\n", path, err)
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("trace: scan %s: %w", path, err)
	}
	SortEvents(events)
	return events, nil
}

// SortEvents orders events by (ts, producer, seq) — the ADR 013 total order.
// Stable so byte order breaks any residual ties deterministically.
func SortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Ts != events[j].Ts {
			return events[i].Ts < events[j].Ts
		}
		if events[i].Producer != events[j].Producer {
			return events[i].Producer < events[j].Producer
		}
		return events[i].Seq < events[j].Seq
	})
}

// ListRunIDs returns run ids with a trace file under rootDir, newest first
// by file modification time.
func ListRunIDs(rootDir string) ([]string, error) {
	entries, err := os.ReadDir(Dir(rootDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("trace: read dir: %w", err)
	}
	type fileInfo struct {
		runID string
		mod   time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{runID: name[:len(name)-len(".jsonl")], mod: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	out := make([]string, len(files))
	for i, fi := range files {
		out[i] = fi.runID
	}
	return out, nil
}

// FindLatestRunIDForIssue resolves the most recent run id whose trace events
// carry the given issue number. Returns "" (no error) when no trace matches.
func FindLatestRunIDForIssue(rootDir string, issue int) (string, error) {
	runIDs, err := ListRunIDs(rootDir)
	if err != nil {
		return "", err
	}
	for _, runID := range runIDs {
		path, err := FilePath(rootDir, runID)
		if err != nil {
			continue
		}
		if firstEventIssue(path) == issue {
			return runID, nil
		}
	}
	return "", nil
}

// firstEventIssue reads the first parseable event of a trace file and
// returns its issue number (0 when unreadable).
func firstEventIssue(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		return ev.Issue
	}
	return 0
}
