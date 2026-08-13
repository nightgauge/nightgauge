package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/intelligence/actualsize"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
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
		if p.Stage == string(state.StagePRCreate) {
			captureIPCActualSize(srv, root, p)
		}
		rec := buildStageExitRecordFromIPC(p)
		if err := diagnostics.WriteStageExitRecord(root, rec); err != nil {
			return nil, fmt.Errorf("write stage-exit record: %w", err)
		}
		return &RecordStageExitResult{Recorded: true}, nil
	}
}

// captureIPCActualSize mirrors captureSchedulerActualSize for extension-owned
// runs. It resolves only an already-live runtime; a diagnostic must never
// adopt or fabricate a run merely to attach telemetry.
func captureIPCActualSize(srv *Server, repoRoot string, p RecordStageExitParams) {
	runtime := runtimeForStageExit(srv, p)
	if runtime == nil {
		log.Printf("diagnostics.recordStageExit: #%d pr-create exit has no matching live runtime; actualSize remains absent", p.IssueNumber)
		return
	}
	snap := runtime.Snapshot()
	diffRoot := repoRoot
	if snap.WorktreeDir != "" {
		diffRoot = snap.WorktreeDir
	}
	base := actualsize.ResolveBaseBranch(p.IssueNumber, diffRoot, repoRoot)
	lines, err := actualsize.MeasureLines(diffRoot, base)
	if err != nil {
		log.Printf("diagnostics.recordStageExit: #%d pre-merge actual-size measurement unavailable: %v", p.IssueNumber, err)
		return
	}
	runtime.SetActualLinesChanged(lines)
	repo := snap.Repo
	if repo == "" {
		repo = p.Repo
	}
	if stateDir := srv.pipelineStateDir(repo); stateDir != "" {
		if err := runtime.Persist(stateDir); err != nil {
			log.Printf("diagnostics.recordStageExit: #%d captured %d changed lines but runtime persistence failed: %v",
				p.IssueNumber, lines, err)
		}
	}
}

func runtimeForStageExit(srv *Server, p RecordStageExitParams) *state.RuntimeState {
	if p.RunID != "" {
		srv.runtimesMu.Lock()
		entry := srv.activeRuntimes[p.RunID]
		if entry == nil || entry.rs == nil || entry.issue != p.IssueNumber ||
			(p.Repo != "" && entry.repo != "" && entry.repo != p.Repo) || entry.terminal || entry.abandoned {
			srv.runtimesMu.Unlock()
			return nil
		}
		runtime := entry.rs
		srv.runtimesMu.Unlock()
		return runtime
	}
	current, _ := srv.currentRunForIssue(p.Repo, p.IssueNumber)
	if current == nil {
		return nil
	}
	return current.rs
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
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		Repo:         p.Repo,
		Issue:        p.IssueNumber,
		Stage:        p.Stage,
		SessionID:    p.SessionID,
		RunID:        p.RunID,
		Success:      p.Success,
		ExitCode:     p.ExitCode,
		Signal:       p.Signal,
		SignalSource: p.SignalSource,
		// #161: which configured limit fired and what it was set to. Carried
		// verbatim so `jq 'select(.kill_ceiling=="nx-stall-multiple")'` finds
		// the same kills from either dispatch path.
		KillCeiling:      p.KillCeiling,
		KillCeilingValue: p.KillCeilingValue,
		TerminalKind:     p.TerminalKind,
		ElapsedMs:        p.ElapsedMs,
		IdleMsAtExit:     p.IdleMsAtExit,
		LastBashCommand:  p.LastBashCommand,
		LastBashExit:     p.LastBashExit,
		// Bounded here as well as on the Go-scheduler path (#156) so neither
		// dispatch path can write an unbounded command history.
		RecentBash:      diagnostics.BoundRecentBash(p.RecentBash),
		StopHookErrored: p.StopHookErrored,
		StderrTail:      p.StderrTail,
		// #125: the post-condition gate's verdict, when one overrode the
		// skill's self-report. Mirrors what scheduler_exit_record.go copies
		// out of runtime.StageGateResults on the Go-scheduler path, so
		// `jq 'select(.gate_kind=="fail")'` finds gate-caught failures from
		// either dispatch path.
		GateKind:   p.GateKind,
		GateReason: p.GateReason,
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
	// same `terminal_kind` for equivalent failure shapes. Not routed through
	// ResolveTerminalKind (Issue #9): RecordStageExitParams carries GateKind
	// but no structured gate terminal-kind field — the TS dispatch path never
	// has a gates.GateResult in scope to read one from, so there is nothing
	// for ResolveTerminalKind's gate-preference branch to prefer here.
	if rec.TerminalKind == "" && p.ErrorText != "" {
		rec.TerminalKind = orchestrator.ClassifyTerminalKind(p.ErrorText)
	}

	return rec
}
