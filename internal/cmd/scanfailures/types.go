// Package scanfailures implements deterministic scanning of pipeline session
// logs under .nightgauge/logs/ for known failure-signal patterns. It is
// the Go-backed implementation of the inline-Python regex scan previously
// embedded in skills/nightgauge-retro/SKILL.md Phase 2.3.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B29.
package scanfailures

// SchemaVersion is the JSON schema version emitted by Scan. Additive
// evolution only — renames or removed fields require a bump. Mirrors the
// versioning discipline of internal/pipeline/aggregator.go (B2).
const SchemaVersion = 1

// MaxSignalsPerFile caps matches per log file to bound output size. Mirrors
// the [:50] truncation in retro Phase 2.3.
const MaxSignalsPerFile = 50

// AppliedFilters echoes the input flags so consumers can confirm what was
// applied without re-parsing CLI args.
type AppliedFilters struct {
	Issue   int    `json:"issue"`
	Since   string `json:"since"`
	Workdir string `json:"workdir"`
}

// SignalMatch is a single line in a session log that matched a failure
// pattern. Field names mirror retro Phase 2.3's /tmp/retro_logs.json schema.
type SignalMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// LogFileSignals groups SignalMatch results for one session log file.
// IssueNumber is *int because the canonical filename pattern
// `YYYY-MM-DD_session.log` carries no issue number — only
// `YYYY-MM-DD_NNN_session.log` files yield a non-nil value (matches the
// `int(file_issue) if file_issue.isdigit() else None` semantic from Phase 2.3).
type LogFileSignals struct {
	LogFile        string        `json:"log_file"`
	IssueNumber    *int          `json:"issue_number"`
	Date           string        `json:"date"`
	FailureSignals []SignalMatch `json:"failure_signals"`
}

// Result is the stable JSON output schema for `nightgauge logs
// scan-failures`. Schema version 1.
//
// Field-name stability is the contract: retro Phase 3 reads `log_signals` and
// `failure_signals[].text` directly when building the unified failure event
// list.
type Result struct {
	V                int              `json:"v"`
	Filters          AppliedFilters   `json:"filters"`
	LogFilesScanned  int              `json:"log_files_scanned"`
	FilesWithSignals int              `json:"files_with_signals"`
	LogSignals       []LogFileSignals `json:"log_signals"`
	Warnings         []string         `json:"warnings"`
}
