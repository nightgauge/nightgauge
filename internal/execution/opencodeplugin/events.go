// events.go is the Go-side reader for the bounded run-dir events file
// plugin/nightgauge/session.js (#1641) writes: one append-only JSONL file per
// OpenCode run, named "opencode-events-<RUN_ID>.jsonl" beside the run's
// NIGHTGAUGE_OUTPUT_FILE. It is the only producer of compaction telemetry —
// the JSON run stream never carries it — and #1653 reads it back through
// ReadRunEvents and CompactionCount.
//
// The file's own retention contract (security constraint, #1641): it never
// contains transcript, summary, tool output or prompt text, only ids, counts
// and verdict codes, and is capped at 1 MiB, after which the writer appends
// one "truncated" line and stops. ReadRunEvents enforces the reading half of
// that contract defensively — a line whose top level or whose "detail"
// object carries a "text" field is dropped rather than returned, so a buggy
// or tampered writer can never smuggle free text past this reader into
// #1653's telemetry.
//
// There are TWO writers, not one (#1810). session.js writes every event it
// can decide synchronously, inside the opencode process. The one event it
// cannot — "stop_verify", whose verdict is a separate `nightgauge hook
// stop-verify` process's answer — is written by THAT process, through
// AppendRunEvent below. The reason is measured, not stylistic: opencode
// 1.18.30 does NOT await the promise a plugin's `event` hook returns, and a
// one-shot `opencode run` exits within ~10 ms of publishing session.idle
// (probe recorded on #1810: a continuation scheduled 20 ms out never runs at
// all). Neither awaiting the verb inline nor recording it from a .then()
// continuation can survive that exit; a child process can, so the child owns
// the line.
//
// The consequence for READERS — #1653 included — is that the events file
// finalizes shortly AFTER the opencode CLI exits, not before it. Read it
// with WaitForRunEvent, which polls for the terminal "stop_verify" line
// within a bound, rather than with a bare ReadRunEvents the instant the CLI
// returns.
package opencodeplugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// EventKinds are the five event kinds session.js emits during a normal run.
// EventsTruncatedKind is a sixth, terminal kind: the writer's own cap
// sentinel, never counted as one of these five.
var EventKinds = []string{"compaction", "idle", "stop_verify", "permission_ask", "skill"}

// EventsTruncatedKind is the kind the writer appends, at most once per run,
// once the events file reaches its 1 MiB cap; every write after it is
// skipped.
const EventsTruncatedKind = "truncated"

// eventsMaxBytes is the events file's cap. Both writers enforce it
// independently and identically: session.js's own EVENTS_MAX_BYTES inside
// the opencode process, and AppendRunEvent below inside the `nightgauge hook
// stop-verify` child.
const eventsMaxBytes = 1 << 20 // 1 MiB

// detailValueMaxLen bounds a single detail string value's length. This is a
// second, independent line of defence (session.js's own SKILL_ID_RE already
// bounds the one detail value a model can influence, the skill id) against a
// buggy or tampered writer smuggling a long string — a sentence, a command
// line — past the reader under a key that is not literally "text".
const detailValueMaxLen = 256

// Event is one line of the run's events file.
type Event struct {
	V         int            `json:"v"`
	TS        string         `json:"ts"`
	Kind      string         `json:"kind"`
	SessionID string         `json:"session_id,omitempty"`
	Child     bool           `json:"child,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// EventsFileName is the writer's own filename formula for one run.
func EventsFileName(runID string) string {
	return fmt.Sprintf("opencode-events-%s.jsonl", runID)
}

// EventsPath mirrors the writer's own path formula (session.js's
// eventsPath): the events file lives beside outputFile, named
// EventsFileName(runID). ok is false — the file is disabled, exactly as the
// writer disables itself — when outputFile is not an absolute path, when it
// carries a literal ".." path segment, or when either argument is empty.
func EventsPath(outputFile, runID string) (path string, ok bool) {
	if outputFile == "" || runID == "" {
		return "", false
	}
	if !filepath.IsAbs(outputFile) {
		return "", false
	}
	for _, part := range strings.Split(filepath.ToSlash(outputFile), "/") {
		if part == ".." {
			return "", false
		}
	}
	return filepath.Join(filepath.Dir(outputFile), EventsFileName(runID)), true
}

// ReadRunEvents parses path's JSONL events. A line that is not valid JSON,
// or whose top level or "detail" object carries a "text" key, is dropped
// rather than returned or treated as a read failure: the retention contract
// is enforced by exclusion, not by failing the whole read over one bad line.
// A missing file is not an error: ([]Event)(nil), nil.
func ReadRunEvents(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opencodeplugin: reading run events %s: %w", path, err)
	}

	var events []Event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &top); err != nil {
			continue // not JSON: drop rather than fail the whole read
		}
		if _, hasText := top["text"]; hasText {
			continue // the file must never carry free text
		}

		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if _, hasText := ev.Detail["text"]; hasText {
			continue
		}
		if !detailHoldsOnlyScalars(ev.Detail) {
			continue
		}
		events = append(events, ev)
	}
	return events, nil
}

// detailHoldsOnlyScalars reports whether every value in detail is a scalar
// (string, number, bool or null) within detailValueMaxLen — never a nested
// object or array. detail must hold only ids, counts and verdict codes (this
// file's own retention contract); a nested object (e.g. a skill id the model
// passed as {"text": "..."}, which carries no top-level or detail-level
// "text" key to catch) or an over-length string is exactly the shape a
// smuggled transcript fragment would take, so the line is dropped rather
// than returned.
func detailHoldsOnlyScalars(detail map[string]any) bool {
	for _, v := range detail {
		switch val := v.(type) {
		case nil, bool, float64:
			// scalar: fine.
		case string:
			if len(val) > detailValueMaxLen {
				return false
			}
		default:
			return false // object, array, or any other non-scalar shape
		}
	}
	return true
}

// RunOutputFileEnvVar and RunIDEnvVar are the two variables every adapter
// already exports on a dispatch, and the only inputs the events file's path
// has. A `nightgauge hook` verb spawned by the plugin inherits both from the
// opencode process, so the child resolves the SAME file the plugin does
// without any new plumbing.
const (
	RunOutputFileEnvVar = "NIGHTGAUGE_OUTPUT_FILE"
	RunIDEnvVar         = "NIGHTGAUGE_RUN_ID"
)

// RunEventsPathFromEnv resolves this process's run events file from the
// environment, applying exactly EventsPath's rules. ok is false — the events
// file is disabled, not an error — whenever this process was not spawned
// inside a Nightgauge run, or the run's output file is not an absolute,
// ".."-free path.
func RunEventsPathFromEnv() (path string, ok bool) {
	return EventsPath(os.Getenv(RunOutputFileEnvVar), os.Getenv(RunIDEnvVar))
}

// eventIDRe bounds what AppendRunEvent accepts as a session id: an opaque
// identifier, never a sentence. It is the Go twin of session.js's own
// SKILL_ID_RE reasoning — the session id reaches a `hook stop-verify` child
// through argv, and although opencode mints it (no model ever authors one),
// this reader-side shape check means a malformed or injected value is
// dropped rather than recorded.
var eventIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// SanitizeEventID returns id when it is an opaque identifier, and "" when it
// is anything else.
func SanitizeEventID(id string) string {
	if eventIDRe.MatchString(id) {
		return id
	}
	return ""
}

// AppendRunEvent appends one event line to path, honouring the same cap and
// "truncated" sentinel session.js's own appendEvent enforces: at or past
// eventsMaxBytes it appends one truncated line — and only one, whatever the
// process boundary, since it re-reads the file's last line rather than
// trusting in-process state the way the JS side's module-level flag can —
// and writes nothing further.
//
// It refuses to write a detail that violates the retention contract
// (detailHoldsOnlyScalars), so the two independent writers cannot disagree
// about what may be recorded. An empty path is a no-op, not an error: the
// events file is simply disabled for this run.
func AppendRunEvent(path string, ev Event) error {
	if path == "" {
		return nil
	}
	if _, hasText := ev.Detail["text"]; hasText || !detailHoldsOnlyScalars(ev.Detail) {
		return fmt.Errorf("opencodeplugin: refusing to write a %q event whose detail violates the events file's retention contract", ev.Kind)
	}

	size := int64(0)
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	if size >= eventsMaxBytes {
		if lastEventKind(path) == EventsTruncatedKind {
			return nil
		}
		return appendEventLine(path, Event{V: 1, TS: eventTimestamp(), Kind: EventsTruncatedKind})
	}

	if ev.V == 0 {
		ev.V = 1
	}
	if ev.TS == "" {
		ev.TS = eventTimestamp()
	}
	return appendEventLine(path, ev)
}

// eventTimestamp is the writer's timestamp format: the same ISO-8601 UTC
// string with milliseconds JavaScript's Date.toISOString() produces, so both
// writers' lines are indistinguishable to a reader.
func eventTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func appendEventLine(path string, ev Event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("opencodeplugin: encoding a %q event: %w", ev.Kind, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opencodeplugin: opening run events %s: %w", path, err)
	}
	defer f.Close()
	// One Write of one whole line: O_APPEND makes a single write of this size
	// atomic against the plugin's own concurrent appends, so the two writers
	// can never interleave a partial line.
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("opencodeplugin: appending to run events %s: %w", path, err)
	}
	return nil
}

// lastEventKind reports the "kind" of path's last non-empty line, or "" when
// the file is missing, empty or unreadable.
func lastEventKind(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return ""
		}
		return ev.Kind
	}
	return ""
}

// WaitForRunEvent reads path until it holds at least one event of kind, or
// bound expires, and returns whatever it last read either way. It exists
// because the events file's terminal "stop_verify" line is written by the
// `nightgauge hook stop-verify` child, which by design outlives the opencode
// CLI that spawned it (see this file's own header): a reader that runs the
// instant the CLI exits is racing that child, not observing a lost event. A
// bound expiring is not an error — the caller sees the events it did get.
func WaitForRunEvent(path, kind string, bound time.Duration) ([]Event, error) {
	deadline := time.Now().Add(bound)
	for {
		events, err := ReadRunEvents(path)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.Kind == kind {
				return events, nil
			}
		}
		if !time.Now().Before(deadline) {
			return events, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// CompactionCount reports how many "compaction" events path holds.
func CompactionCount(path string) (int, error) {
	events, err := ReadRunEvents(path)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range events {
		if e.Kind == "compaction" {
			n++
		}
	}
	return n, nil
}
