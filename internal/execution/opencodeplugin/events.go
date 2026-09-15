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
package opencodeplugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EventKinds are the five event kinds session.js emits during a normal run.
// EventsTruncatedKind is a sixth, terminal kind: the writer's own cap
// sentinel, never counted as one of these five.
var EventKinds = []string{"compaction", "idle", "stop_verify", "permission_ask", "skill"}

// EventsTruncatedKind is the kind the writer appends, at most once per run,
// once the events file reaches its 1 MiB cap; every write after it is
// skipped.
const EventsTruncatedKind = "truncated"

// eventsMaxBytes is the writer's own cap, mirrored here only for doc
// purposes; the JS side (session.js) is what enforces it.
const eventsMaxBytes = 1 << 20 // 1 MiB

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
		events = append(events, ev)
	}
	return events, nil
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
