// Package diagnostics persists per-stage forensic records to disk so
// failure post-mortems have evidence instead of guesswork.
//
// Background (#3605):
//
//	Pipeline failure #3591 was marked `terminalFailureKind=""` ("pipeline
//	failure") and the persisted V3 RunRecord captured nothing about claude
//	CLI exit code, signal source, last bash command, idle time at exit, or
//	concurrent sibling pipelines. Every retro became guesswork because the
//	evidence was already gone by the time anyone read the JSONL. The same
//	pattern recurred across #3365, #3366, #3367, #3368, #3382, #3499, #3544.
//
// Design:
//
//	One JSONL file per UTC day under `.nightgauge/pipeline/exit-records/`.
//	Each line is one StageExitRecord. Records are written for BOTH success
//	and failure exits so healthy runs anchor what "normal" looks like. The
//	on-disk format reuses the existing internal/history.AppendJSONL primitive
//	so byte-equivalent framing carries across all JSONL writers in the binary.
//
// Schema invariants:
//
//	The struct's JSON tags MUST stay stable once shipped. Additive fields are
//	allowed (always with `omitempty`), but renames or removals would break
//	the `nightgauge exit-records tail` reader and any external operator
//	tooling that grep/jq's the daily file. See docs/FAILURE_TAXONOMY.md.
package diagnostics

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/history"
)

// ExitRecordTokens is the per-stage token usage snapshot embedded in the
// daily diagnostic record. Matches the shape the IPC layer already passes
// from the TS SkillRunner so wiring stays straightforward.
type ExitRecordTokens struct {
	Input         int     `json:"input,omitempty"`
	Output        int     `json:"output,omitempty"`
	CacheRead     int     `json:"cache_read,omitempty"`
	CacheCreation int     `json:"cache_creation,omitempty"`
	CostUsd       float64 `json:"cost_usd,omitempty"`
}

// StageExitRecord is the structured forensic payload written at every stage
// exit (success OR failure). Optional fields are `omitempty` so a record from
// a healthy run stays terse, and a partial capture (e.g. signal never raised)
// doesn't pollute the line with empty placeholders.
type StageExitRecord struct {
	// Timestamp is RFC3339Nano in UTC, captured at WriteStageExitRecord
	// time so concurrent stage exits keep monotonic ordering inside a
	// single day file.
	Timestamp string `json:"ts"`
	// Repo is the canonical "owner/name" identifier of the repository that
	// owns the issue this stage was running for.
	Repo string `json:"repo"`
	// Issue is the GitHub issue number this run is dispatched against.
	Issue int `json:"issue"`
	// Stage is the canonical pipeline stage name (issue-pickup,
	// feature-planning, feature-dev, feature-validate, pr-create, pr-merge).
	Stage string `json:"stage"`
	// SessionID is the claude CLI conversation id when one was captured
	// before exit; empty when the subprocess never reached `result` framing
	// (the most common pathology this record was added to debug).
	SessionID string `json:"session_id,omitempty"`
	// RunID is the UUID v7 from runstate, threading this record to its
	// matching V3 RunRecord row in the daily history JSONL. (#3557)
	RunID string `json:"run_id,omitempty"`
	// Success mirrors the scheduler's success flag. Healthy runs carry
	// `success=true`; the record is still written so the file anchors what
	// "normal" looks like for ratio-based health analysis.
	Success bool `json:"success"`
	// ExitCode is the subprocess exit code, when the subprocess actually
	// forked. Zero pointer means no fork (spawn-time failure) — distinct
	// from a real exit-0.
	ExitCode *int `json:"exit_code,omitempty"`
	// Signal is the POSIX signal name (SIGTERM, SIGKILL, ...) when the
	// subprocess was signalled. Empty when the process exited naturally.
	Signal string `json:"signal,omitempty"`
	// SignalSource names who delivered the signal:
	//   "stall-kill"         — TS idle-stall kill path
	//   "hard-cap"           — TS stage_hard_cap kill path
	//   "quota-fast-fail"    — TS quota-exhausted fast-fail kill path
	//   "processTree-reaper" — orphan reaper killed a survivor (#3605 cross-pipeline forensic)
	//   "external"           — signal arrived from outside the pipeline
	SignalSource string `json:"signal_source,omitempty"`
	// TerminalKind is the post-classification terminal failure category
	// (see internal/orchestrator.TerminalKind*). Empty for success records
	// and for generic failures that fell through every classifier.
	TerminalKind string `json:"terminal_kind,omitempty"`
	// ElapsedMs is total wall time from stage start to exit.
	ElapsedMs int64 `json:"elapsed_ms,omitempty"`
	// IdleMsAtExit is milliseconds since the last subprocess output chunk
	// at the moment of exit. Distinguishes "genuinely wedged" (large
	// IdleMs) from "killed mid-activity" (near-zero IdleMs).
	IdleMsAtExit int64 `json:"idle_ms_at_exit,omitempty"`
	// Tokens is the per-stage token/cost snapshot at exit.
	Tokens ExitRecordTokens `json:"tokens,omitempty"`
	// LastBashCommand is the most recent `Bash` tool_use command observed
	// in the stream, truncated to 500 chars. Common forensic anchor —
	// many silent kills happen mid-Bash.
	LastBashCommand string `json:"last_bash_command,omitempty"`
	// LastBashExit is the exit code of the matching Bash tool_result, when
	// it landed before the stage exited. Pointer so 0 is distinguishable
	// from "never observed."
	LastBashExit *int `json:"last_bash_exit,omitempty"`
	// StopHookErrored is true when the stream included a
	// `notification.key == "stop-hook-error"` event before exit.
	StopHookErrored bool `json:"stop_hook_errored,omitempty"`
	// StderrTail is the last 4 KB of stderr captured by the TS SkillRunner
	// ring buffer. Includes the `[skillRunner] ...` kill markers so retro
	// can reconstruct the chosen kill path from a single line.
	StderrTail string `json:"stderr_tail,omitempty"`
	// RateLimitRemainingAtExit is the GraphQL bucket reading at stage end
	// (REST/GraphQL share a tracker on the Go side). -1 means the value
	// was unavailable; 0+ is a real reading.
	RateLimitRemainingAtExit int `json:"rate_limit_remaining_at_exit,omitempty"`
	// ConcurrentPipelinesAtExit lists `owner/repo#number` keys of other
	// pipelines that were running at the moment of this exit. Empty when
	// no siblings ran. Used for cross-pipeline forensics — a SIGKILL with
	// SignalSource="processTree-reaper" plus a non-empty sibling list is
	// a smoking gun for accidental cross-pipeline reaping.
	ConcurrentPipelinesAtExit []string `json:"concurrent_pipelines_at_exit,omitempty"`
	// PRStateAtExit is the GitHub PR state captured at stage exit for pr-merge
	// stages. Values: "MERGED" | "OPEN" | "CLOSED" | "" (unknown/not applicable).
	// Populated by the deterministic path; empty for LLM-path exits.
	PRStateAtExit string `json:"pr_state_at_exit,omitempty"`
	// SizeLabel is the effective issue size label (XS/S/M/L/XL) resolved at
	// pipeline start from the issue context complexity score. Empty for records
	// written before Issue #3667.
	SizeLabel string `json:"size_label,omitempty"`
	// GateKind is the post-condition gate outcome shape for this stage when a
	// gate ran: "ok" | "no_op" | "fail" (see gates.Kind). Empty when no gate
	// ran for the stage. A "no_op" here is the forensic signal that the skill
	// exited cleanly but produced no state change (e.g. pr-merge reported
	// success but the PR never merged / pr_number is null) — distinct from a
	// hard failure, so retros can `jq` the daily file for cancelled-at-gate
	// exits without joining the V3 record. (Issue #3863)
	GateKind string `json:"gate_kind,omitempty"`
	// GateReason is the short human-readable reason from the gate that ran,
	// mirroring StageGateResult.Reason. Empty when no gate ran. (Issue #3863)
	GateReason string `json:"gate_reason,omitempty"`
}

// exitRecordsSubdir is the project-relative directory the daily JSONL files
// live in. Exported as a package constant so the CLI reader uses the same
// path without re-deriving it.
const exitRecordsSubdir = ".nightgauge/pipeline/exit-records"

// ExitRecordsDir returns the absolute path to the per-project exit-records
// directory. The directory itself is not created — WriteStageExitRecord
// creates it on first append.
func ExitRecordsDir(rootDir string) string {
	return filepath.Join(rootDir, ".nightgauge", "pipeline", "exit-records")
}

// DailyFilePath returns the absolute path of the daily JSONL file for the
// given UTC date. The on-disk filename is always YYYY-MM-DD.jsonl so
// glob/sort lexicographically equals chronologically.
func DailyFilePath(rootDir string, day time.Time) string {
	stamp := day.UTC().Format("2006-01-02")
	return filepath.Join(ExitRecordsDir(rootDir), stamp+".jsonl")
}

// WriteStageExitRecord appends one StageExitRecord to today's daily file.
// Atomic single-line semantics are inherited from internal/history.AppendJSONL
// (mutex-serialized in-process; O_APPEND across processes). Best-effort:
// returns a wrapped error so callers can log, but callers MUST NOT block
// pipeline progress on a write failure.
//
// rootDir is the workspace root (e.g. `as.workspaceRoot`). An empty rootDir
// returns an error rather than silently writing to CWD.
func WriteStageExitRecord(rootDir string, rec StageExitRecord) error {
	if rootDir == "" {
		return fmt.Errorf("diagnostics: WriteStageExitRecord requires a non-empty rootDir")
	}
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	path := DailyFilePath(rootDir, time.Now())
	return history.AppendJSONL(path, rec)
}
