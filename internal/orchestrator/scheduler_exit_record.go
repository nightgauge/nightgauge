package orchestrator

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// writeStageExitRecord persists one StageExitRecord per stage to the
// per-project daily JSONL (`.nightgauge/pipeline/exit-records/<day>.jsonl`).
//
// The record is written for both success and failure exits so the daily file
// also anchors what "healthy" looks like for ratio-based health analysis
// (e.g., "p95 idle_ms at exit went from 1.2s last week to 18.4s today").
// Best-effort: a write failure logs at INFO and never blocks pipeline progress.
//
// Field population is layered:
//
//   - Always populated by Go (deterministic): timestamp, repo, issue, stage,
//     success, exit_code, elapsed_ms, tokens, run_id.
//
//   - Populated by Go when a provider fn is attached: rate_limit_remaining
//     (via SetRateLimitRemainingFn) and concurrent_pipelines (via
//     SetRunningSiblingsFn). Both fns are wired by NewServer in the IPC
//     bootstrap so the autonomous scheduler's full sibling list (including
//     cross-repo) reaches every diagnostic write.
//
//   - Populated by Go when set on the runtime / err: terminal_kind (via
//     ClassifyTerminalKind for failures), with a fallback to the runtime
//     StageErrors map for IPC-mode failures where stageErr is nil but the
//     skill set the stage error text directly (#3207).
//
//   - Forwarded verbatim from TS SkillRunner via StageResultParams /
//     StageRunResult: session_id, signal, signal_source, idle_ms_at_exit,
//     last_bash_command, last_bash_exit, stop_hook_errored, stderr_tail.
//     These are zero when the TS SkillRunner pre-dates the #3605 update —
//     in that case the record is still valid, just terser.
//
// See docs/STAGE_EXIT_DIAGNOSTIC.md for the full schema + lifecycle.
func (s *Scheduler) writeStageExitRecord(
	item types.BoardItem,
	stage state.PipelineStage,
	runtime *state.RuntimeState,
	result *StageRunResult,
	exitCode int,
	stageErr error,
	actualCostUsd float64,
	model string,
	inputTokens, outputTokens, cacheReadTokens int,
	stageStartedAt time.Time,
	workspaceRoot string,
	prStateAtExit string,
	sizeLabel string,
) {
	if workspaceRoot == "" {
		// Tests sometimes drive the scheduler without a workspace root.
		// Skip the write rather than scribbling into CWD.
		return
	}

	success := stageErr == nil && exitCode == 0

	// Pull the runtime snapshot once (cheap copy) so we can read RunID and
	// the StageErrors map without holding a long lock.
	var snap *state.RuntimeState
	if runtime != nil {
		snap = runtime.Snapshot()
	}
	runID := ""
	if snap != nil {
		runID = snap.RunID
	}

	rec := diagnostics.StageExitRecord{
		Timestamp:                 time.Now().UTC().Format(time.RFC3339Nano),
		Repo:                      item.Repo,
		Issue:                     item.Number,
		Stage:                     string(stage),
		Success:                   success,
		ExitCode:                  intPtr(exitCode),
		ElapsedMs:                 time.Since(stageStartedAt).Milliseconds(),
		Tokens:                    buildExitRecordTokens(inputTokens, outputTokens, cacheReadTokens, actualCostUsd, model),
		RateLimitRemainingAtExit:  s.rateLimitRemainingAtExit(),
		ConcurrentPipelinesAtExit: s.snapshotConcurrentPipelines(item.Repo, item.Number),
		RunID:                     runID,
		PRStateAtExit:             prStateAtExit,
		SizeLabel:                 sizeLabel,
	}

	// Classify terminal kind from whichever error text we have. stageErr is
	// the most reliable source (it's what the orchestrator already classifies
	// against for the V3 record). Fall back to the runtime's per-stage error
	// map for IPC-mode failures where stageErr was nil but the SkillRunner
	// surfaced the kill marker via SetStageError (#3207).
	if !success {
		errText := ""
		if stageErr != nil {
			errText = stageErr.Error()
		}
		if errText == "" && snap != nil && snap.StageErrors != nil {
			errText = snap.StageErrors[string(stage)]
		}
		if errText != "" {
			rec.TerminalKind = ClassifyTerminalKind(errText)
		}
	}

	// Snapshot the stage's post-condition gate outcome (Issue #3863). The
	// scheduler appends the gate result to runtime.StageGateResults before this
	// write runs (both the success-path gate and the #3835 terminal-stage
	// reconcile path), so the latest entry for this stage is the one that
	// decided the exit. Recording its Kind here makes a "no_op" gate — the skill
	// exited 0 but produced no state change (e.g. pr-merge that never merged /
	// pr_number null) — a distinct, greppable forensic signal in the daily file,
	// separate from a generic failure.
	if snap != nil && snap.StageGateResults != nil {
		if grs := snap.StageGateResults[string(stage)]; len(grs) > 0 {
			latest := grs[len(grs)-1]
			rec.GateKind = latest.Kind
			rec.GateReason = latest.Reason
		}
	}

	// Forward TS-side diagnostic fields when the SkillRunner has been updated
	// to populate them. Empty values are dropped by `omitempty` on the JSON
	// tags so pre-update runs produce a terser (still valid) record.
	if result != nil {
		rec.SessionID = result.SessionID
		rec.Signal = result.Signal
		rec.SignalSource = result.SignalSource
		rec.IdleMsAtExit = result.IdleMsAtExit
		rec.LastBashCommand = truncExitRecordBashCommand(result.LastBashCommand)
		rec.LastBashExit = result.LastBashExit
		rec.StopHookErrored = result.StopHookErrored
		rec.StderrTail = truncExitRecordStderrTail(result.StderrTail)
		// Prefer the TS-provided elapsed over the Go-side fallback when present —
		// TS captures the actual subprocess wall time (Go's stageStartedAt is
		// slightly earlier because it brackets the deterministic-merge fast path).
		if result.ElapsedMs > 0 {
			rec.ElapsedMs = result.ElapsedMs
		}
		if result.CacheCreationTokens > 0 {
			rec.Tokens.CacheCreation = result.CacheCreationTokens
		}
	}

	if err := diagnostics.WriteStageExitRecord(workspaceRoot, rec); err != nil {
		log.Printf("#%d: failed to write stage-exit diagnostic record for %s: %v",
			item.Number, stage, err)
	}
}

// rateLimitRemainingAtExit reads the latest GraphQL/REST rate-limit
// remaining-quota value. Prefers an externally injected provider fn (set by
// the IPC server via SetRateLimitRemainingFn — typically wired to the
// autonomous scheduler's RateLimitRemaining method) and falls back to the
// scheduler's own github client tracker. Returns -1 when no reading is
// available — distinct from a real zero, which would indicate the bucket is
// genuinely empty at exit (a strong forensic signal for #3368-class failures).
func (s *Scheduler) rateLimitRemainingAtExit() int {
	if s.rateLimitRemainingFn != nil {
		if v := s.rateLimitRemainingFn(); v >= 0 {
			return v
		}
	}
	if s.client == nil {
		return -1
	}
	tracker := s.client.RateLimitTracker()
	if tracker == nil {
		return -1
	}
	user := s.client.RateLimitTrackerUser()
	if user == "" {
		return -1
	}
	entry, ok, err := tracker.Get(user)
	if err != nil || !ok || entry == nil {
		return -1
	}
	return entry.Remaining
}

// snapshotConcurrentPipelines returns "owner/repo#number" keys for every
// other pipeline that was running at exit. Prefers the externally injected
// running-siblings fn (set by the IPC server via SetRunningSiblingsFn —
// typically wired to the autonomous scheduler's RunningSiblings method,
// which has full repo+number visibility across the workspace). Falls back
// to the local activeStages map which only carries issue numbers (no repo),
// so the sibling key is "?/?#NUM" in fallback mode — still strong evidence
// for cross-pipeline interference (the signal operators care about is
// "ran alongside another issue", not "which repo").
func (s *Scheduler) snapshotConcurrentPipelines(selfRepo string, selfNumber int) []string {
	if s.runningSiblingsFn != nil {
		return s.runningSiblingsFn(selfRepo, selfNumber)
	}
	s.activeStagesMu.Lock()
	defer s.activeStagesMu.Unlock()
	if len(s.activeStages) == 0 {
		return nil
	}
	siblings := make([]string, 0, len(s.activeStages))
	for issueNumber := range s.activeStages {
		if issueNumber == selfNumber {
			continue
		}
		siblings = append(siblings, fmt.Sprintf("?#%d", issueNumber))
	}
	if len(siblings) == 0 {
		return nil
	}
	return siblings
}

// buildExitRecordTokens is a tiny adapter to keep the call-site readable.
// The cost passed in is the actual cost from Claude CLI when available; when
// zero the caller fell back to calculated cost, which we do NOT use for the
// exit record because the diagnostic record is for forensic actuals only.
func buildExitRecordTokens(input, output, cacheRead int, actualCostUsd float64, _ string) diagnostics.ExitRecordTokens {
	return diagnostics.ExitRecordTokens{
		Input:     input,
		Output:    output,
		CacheRead: cacheRead,
		CostUsd:   actualCostUsd,
	}
}

// intPtr returns a pointer to v. Used so StageExitRecord.ExitCode can
// distinguish "real exit 0" from "never observed" via pointer-shape semantics.
// intPtr returns a pointer to v. Used so StageExitRecord.ExitCode can
// distinguish "real exit 0" from "never observed" via pointer-shape semantics —
// `omitempty` would otherwise drop a successful exit-0 from the daily JSONL.
func intPtr(v int) *int { return &v }

// exitRecordBashCommandMaxRunes caps the persisted last_bash_command at 500
// runes. The TS side already truncates; this is belt-and-braces defense for
// older clients and for any future caller that bypasses TS.
const exitRecordBashCommandMaxRunes = 500

// exitRecordStderrTailMaxBytes caps the persisted stderr_tail at 4 KB. Same
// rationale as exitRecordBashCommandMaxRunes.
const exitRecordStderrTailMaxBytes = 4 * 1024

// truncExitRecordBashCommand truncates s to at most exitRecordBashCommandMaxRunes
// runes, marking truncation with a trailing "…" so the on-disk record is
// unambiguously truncated.
func truncExitRecordBashCommand(s string) string {
	r := []rune(s)
	if len(r) <= exitRecordBashCommandMaxRunes {
		return s
	}
	return strings.TrimRight(string(r[:exitRecordBashCommandMaxRunes]), " ") + "…"
}

// truncExitRecordStderrTail returns the trailing exitRecordStderrTailMaxBytes
// bytes of s. Stderr is byte-sized rather than rune-sized so the 4 KB cap
// maps directly to disk.
func truncExitRecordStderrTail(s string) string {
	if len(s) <= exitRecordStderrTailMaxBytes {
		return s
	}
	return s[len(s)-exitRecordStderrTailMaxBytes:]
}
