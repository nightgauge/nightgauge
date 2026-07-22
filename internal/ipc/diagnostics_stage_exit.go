package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// makeDiagnosticsRecordStageExitHandler builds the IPC handler for
// `diagnostics.recordStageExit`. Extracted as a factory so:
//   - The inline registration site in server.go stays a one-liner that the
//     IPC codegen scanner can pair with the //ipc:method annotation.
//   - Tests can build a handler against a hand-rolled Server fixture
//     without booting the full method registry.
//
// The closure captures srv so it resolves the run's target-repo root at call
// time via srv.repoRoot(p.Repo) — the same repo-scoping the runtime snapshot
// and history RunRecord use (#215/#232). Previously it wrote to
// srv.workspaceRoot (the IPC launch root), so in a multi-repo workspace an
// interactive run's exit-records landed in the wrong repo. Unregistered/empty
// repos fall back to workspaceRoot inside repoRoot. Best-effort write
// semantics: a failure returns an IPC error but never blocks the pipeline —
// the TS caller treats this as fire-and-forget.
func makeDiagnosticsRecordStageExitHandler(srv *Server) Handler {
	return func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p RecordStageExitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		root := srv.repoRoot(p.Repo)
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		rec := buildStageExitRecordFromIPC(p)
		if err := diagnostics.WriteStageExitRecord(root, rec); err != nil {
			return nil, fmt.Errorf("write stage-exit record: %w", err)
		}
		return &RecordStageExitResult{Recorded: true}, nil
	}
}

// buildStageExitRecordFromIPC translates the TS-side RecordStageExitParams
// payload into a diagnostics.StageExitRecord that's byte-equivalent to what
// the Go-scheduler path writes via scheduler_exit_record.go. Keeping the on-
// disk shape identical means `nightgauge exit-records tail` sees one
// uniform stream regardless of which dispatch path produced the record.
//
// Fields the IPC payload doesn't carry (concurrent sibling pipelines, rate-
// limit remaining at exit) are left zero/empty — the Go-scheduler path fills
// those because it has direct access to the autonomous scheduler's snapshot
// at the moment of stage exit. The TS dispatch path doesn't have that
// visibility, and a partial record is strictly better than no record.
//
// `Timestamp` is always `now()` so daily-file ordering matches actual write
// moment regardless of any clock skew between TS and Go. Fields TS pre-
// computed (TerminalKind, ElapsedMs, etc.) are carried verbatim; the
// orchestrator.ClassifyTerminalKind fallback fires only when TS left
// TerminalKind empty AND included an ErrorText to classify.
func buildStageExitRecordFromIPC(p RecordStageExitParams) diagnostics.StageExitRecord {
	rec := diagnostics.StageExitRecord{
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		Repo:            p.Repo,
		Issue:           p.IssueNumber,
		Stage:           p.Stage,
		SessionID:       p.SessionID,
		RunID:           p.RunID,
		Success:         p.Success,
		ExitCode:        p.ExitCode,
		Signal:          p.Signal,
		SignalSource:    p.SignalSource,
		TerminalKind:    p.TerminalKind,
		ElapsedMs:       p.ElapsedMs,
		IdleMsAtExit:    p.IdleMsAtExit,
		LastBashCommand: p.LastBashCommand,
		LastBashExit:    p.LastBashExit,
		StopHookErrored: p.StopHookErrored,
		StderrTail:      p.StderrTail,
		Tokens: diagnostics.ExitRecordTokens{
			Input:         p.InputTokens,
			Output:        p.OutputTokens,
			CacheRead:     p.CacheReadTokens,
			CacheCreation: p.CacheCreationTokens,
			CostUsd:       p.CostUsd,
		},
		// RateLimitRemainingAtExit and ConcurrentPipelinesAtExit are
		// Go-side-only fields. The TS dispatch path doesn't have visibility
		// into the autonomous scheduler's live state, so they're left at
		// their zero value (omitted from the JSON line via omitempty).
	}

	// Classify terminal kind from error text when TS didn't pre-classify.
	// Mirrors the Go-scheduler fallback so the two write paths produce the
	// same `terminal_kind` for equivalent failure shapes.
	if rec.TerminalKind == "" && p.ErrorText != "" {
		rec.TerminalKind = orchestrator.ClassifyTerminalKind(p.ErrorText)
	}

	return rec
}
