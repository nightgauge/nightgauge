// Package telemetry emits one JSONL event per knowledge-base operation to
// .nightgauge/pipeline/history/knowledge-events.jsonl. The package is
// deliberately small: it owns the event schema and the Emit entrypoint that
// every knowledge subcommand calls at its success path.
//
// The package is decoupled from internal/config to avoid an import cycle —
// the resolver method `KnowledgeConfig.IsTelemetryEnabled()` lives on the
// config type itself, and callers pass the result into IsEnabled(bool) before
// invoking Emit. Telemetry emit failures must never fail the user-facing
// operation; callers are expected to log and swallow errors.
package telemetry

// EventType is the typed string enum for knowledge events. Each event type
// corresponds to a distinct knowledge-base lifecycle operation. See
// docs/GO_BINARY.md for when each type fires.
type EventType string

const (
	EventScaffold  EventType = "scaffold"
	EventRead      EventType = "read"
	EventWrite     EventType = "write"
	EventRecall    EventType = "recall"
	EventRecallHit EventType = "recall_hit"
	EventGraduate  EventType = "graduate"
	EventPrune     EventType = "prune"
	EventIndex     EventType = "index"
	EventValidate  EventType = "validate"
	EventStats     EventType = "stats"
)

// allEventTypes is the canonical, ordered list of valid event types. The
// `knowledge telemetry record --type` flag validates against this set.
var allEventTypes = []EventType{
	EventScaffold,
	EventRead,
	EventWrite,
	EventRecall,
	EventRecallHit,
	EventGraduate,
	EventPrune,
	EventIndex,
	EventValidate,
	EventStats,
}

// AllEventTypes returns a copy of the valid event type set. Used by the CLI
// validation path and by help text generators.
func AllEventTypes() []string {
	out := make([]string, len(allEventTypes))
	for i, t := range allEventTypes {
		out[i] = string(t)
	}
	return out
}

// IsValidEventType reports whether t is one of the declared event constants.
func IsValidEventType(t EventType) bool {
	for _, candidate := range allEventTypes {
		if candidate == t {
			return true
		}
	}
	return false
}

// Event is the on-disk schema for one knowledge-events.jsonl line. All
// optional fields use the omitempty json tag so a minimally-populated event
// produces a compact record. The field ordering here also matches the JSONL
// output ordering Go's encoder produces in struct-declaration order.
type Event struct {
	Timestamp    string    `json:"timestamp"`
	Type         EventType `json:"type"`
	Stage        string    `json:"stage"`
	Scope        string    `json:"scope,omitempty"`
	IssueNumber  int       `json:"issue_number,omitempty"`
	Path         string    `json:"path,omitempty"`
	QuerySummary string    `json:"query_summary,omitempty"`
	RecallID     string    `json:"recall_id,omitempty"`
	HitIndex     *int      `json:"hit_index,omitempty"`
	ResultCount  *int      `json:"result_count,omitempty"`
	DurationMs   int64     `json:"duration_ms,omitempty"`
	Status       string    `json:"status,omitempty"`
	ErrorKind    string    `json:"error_kind,omitempty"`
	// Mode distinguishes operational modes for events that have more than one
	// (e.g. graduate's "manual" vs "auto"). Empty in historical records and in
	// events whose command has a single mode — aggregators treat an empty
	// value as the legacy/manual default.
	Mode string `json:"mode,omitempty"`
}

// QuerySummaryMaxChars caps query_summary at this many characters to keep
// individual JSONL lines small. Longer query strings are truncated to this
// length with no marker — aggregators treat them as opaque blobs.
const QuerySummaryMaxChars = 200
