package telemetry

import (
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/history"
)

// redactedQuerySummary is the sentinel inserted when
// NIGHTGAUGE_TELEMETRY_REDACT_QUERIES=1. Distinct from an empty string so
// downstream aggregators can tell "redacted" from "absent".
const redactedQuerySummary = "<redacted>"

// stageEnvVar is the environment variable execution adapters set to the
// current pipeline stage name (e.g., "feature-dev"). When unset, events
// record "unknown" so the aggregator can surface ungrouped emits.
const stageEnvVar = "NIGHTGAUGE_STAGE"

// redactEnvVar is the opt-in env var for query redaction. Any value other
// than "1" leaves query_summary intact.
const redactEnvVar = "NIGHTGAUGE_TELEMETRY_REDACT_QUERIES"

// IsEnabled returns telemetryEnabled. It exists so call sites have a single
// gate function name even when the resolved value comes from
// KnowledgeConfig.IsTelemetryEnabled() (see internal/config/knowledge.go).
// Decoupling the package from internal/config avoids an import cycle —
// internal/knowledge already imports internal/config, and the aggregator
// reads telemetry without needing the config struct.
func IsEnabled(telemetryEnabled bool) bool { return telemetryEnabled }

// Path returns the absolute JSONL file path where Emit writes events for a
// given workspace root. Exposed for the aggregator and for tests that need
// to read events without rerunning the emit path.
func Path(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history", "knowledge-events.jsonl")
}

// Emit writes one Event to knowledge-events.jsonl under workspaceRoot.
//
// Auto-fills:
//   - Timestamp: RFC3339 UTC at emit time when ev.Timestamp is empty.
//   - Stage: NIGHTGAUGE_STAGE env var; "unknown" when unset/empty.
//   - QuerySummary: truncated to QuerySummaryMaxChars; replaced with
//     "<redacted>" when NIGHTGAUGE_TELEMETRY_REDACT_QUERIES=1.
//
// Path is left untouched (long absolute paths are kept intact by design).
//
// Returns any error from history.AppendJSONL. Callers must NOT propagate
// telemetry errors to the user-facing operation — log to stderr and continue.
func Emit(workspaceRoot string, ev Event) error {
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if ev.Stage == "" {
		stage := os.Getenv(stageEnvVar)
		if stage == "" {
			stage = "unknown"
		}
		ev.Stage = stage
	}
	if ev.QuerySummary != "" {
		if os.Getenv(redactEnvVar) == "1" {
			ev.QuerySummary = redactedQuerySummary
		} else if len(ev.QuerySummary) > QuerySummaryMaxChars {
			ev.QuerySummary = ev.QuerySummary[:QuerySummaryMaxChars]
		}
	}

	return history.AppendJSONL(Path(workspaceRoot), ev)
}
