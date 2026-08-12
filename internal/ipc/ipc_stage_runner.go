package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// IpcStageRunner routes stage execution through the TypeScript SkillRunner
// via the IPC pipeline.runStage event + pipeline.stageResult request.
//
// Flow:
//  1. Go emits "pipeline.runStage" event with RunStageParams
//  2. TypeScript SkillRunner spawns Claude CLI, streams output
//  3. TypeScript sends "pipeline.stageResult" request with StageResultParams
//  4. Go receives result, evaluates model escalation on failure, and returns to scheduler
type IpcStageRunner struct {
	server      *Server
	retryEngine *orchestrator.RetryEngine // Evaluates model escalation on stage failures

	// AutonomousMode indicates the pipeline is driven by the autonomous scheduler.
	// When true, the TS SkillRunner uses escalation+pause on stall instead of
	// silent kill. Set by the autonomous scheduler when wiring up the IPC runner.
	// Issue #2656.
	AutonomousMode bool

	// pendingResults holds channels waiting for stage results.
	// Keyed by "issueNumber#stage".
	pendingResults map[string]chan StageResultParams
	mu             sync.Mutex
}

// NewIpcStageRunner creates an IPC-backed stage runner.
// engine may be nil (escalation evaluation is skipped when nil).
func NewIpcStageRunner(srv *Server, engine *orchestrator.RetryEngine) *IpcStageRunner {
	return &IpcStageRunner{
		server:         srv,
		retryEngine:    engine,
		pendingResults: make(map[string]chan StageResultParams),
	}
}

// RunStage implements orchestrator.StageRunner by sending the stage to TypeScript
// via IPC and waiting for the result.
func (r *IpcStageRunner) RunStage(ctx context.Context, params orchestrator.StageRunParams) (*orchestrator.StageRunResult, error) {
	// The run identity is asserted at the dispatch boundary — before the
	// pending channel is registered and before pipeline.runStage is emitted
	// (ADR-017 step 0b). The scheduler mints the id before it can reach a
	// stage, so this is unreachable by construction; it is here because the
	// alternative failure mode is silent, both today and after step 4.
	//
	// TODAY, an empty id dispatched down the Go-scheduler IPC path costs the
	// stage its lifecycle trace: `runId` reaches the extension's spawn call,
	// which hands it to the SDK TraceRecorder; with nothing there the recorder
	// falls back to `run-state.json` and, when that does not resolve either,
	// disables itself as a silent no-op (skillRunner.ts:3013-3019). The stage
	// runs, burns its tokens, and writes no trace.
	//
	// ONCE Decision 3's `run_id_required` lands (step 4), the same empty
	// dispatch would additionally have every notifyPhaseTransition and
	// notifyStageProgress call for that stage hard-rejected by the server and
	// swallowed by PipelineBridge's bare `.catch(warn)` — phase markers and live
	// progress gone with no error anywhere. This assertion pre-empts both.
	// Failing at t=0 costs nothing: no child process has spawned.
	if params.RunID == "" {
		return &orchestrator.StageRunResult{ExitCode: 1}, fmt.Errorf(
			"run identity missing at stage dispatch (issue #%d, stage %s) — ADR-017 step 0b: a stage may not be dispatched without the run id its progress and phase calls will be booked under",
			params.IssueNumber, params.Stage,
		)
	}

	key := fmt.Sprintf("%d#%s", params.IssueNumber, params.Stage)

	// Create pending result channel
	ch := make(chan StageResultParams, 1)
	r.mu.Lock()
	r.pendingResults[key] = ch
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.pendingResults, key)
		r.mu.Unlock()
	}()

	// Build RunStageParams for TypeScript.
	//
	// Model is the authoritative dispatch tier (#340) — resolveDispatchModel
	// has already applied escalation, sticky downgrades and every floor, and
	// the TS SkillRunner spawns on it verbatim. params.Prompt is deliberately
	// NOT forwarded: the extension composes its own prompt on this path (see
	// RunStageParams), and a field nobody reads cannot be distinguished from
	// one that broke.
	ipcParams := RunStageParams{
		Stage:             string(params.Stage),
		IssueNumber:       params.IssueNumber,
		Model:             params.Model,
		MaxTokens:         params.MaxTokens,
		TimeoutMs:         int(params.Timeout / time.Millisecond),
		SkillContent:      params.SkillContent,
		ContextFile:       params.ContextFile,
		OutputFile:        params.OutputFile,
		WorktreeDir:       params.WorktreePath,
		Repo:              params.Repo,
		AllowedTools:      params.AllowedTools,
		SkillFallbackUsed: params.SkillFallbackUsed,
		AutonomousMode:    r.AutonomousMode,
		// The run identity the scheduler minted and stamped on this dispatch
		// (#228, ADR-017). Read from params.RunID and never re-derived from
		// params.Runtime: one authority per value, so the two cannot disagree.
		RunID: params.RunID,
	}

	// Emit event to TypeScript
	r.server.Emit("pipeline.runStage", ipcParams)

	// Wait for result with context timeout
	select {
	case result := <-ch:
		exitCode := result.ExitCode
		if !result.Success && exitCode == 0 {
			exitCode = 1 // Ensure non-zero exit on failure
		}

		if params.Runtime != nil {
			cacheCreation5m, cacheCreation1h := tokens.NormalizeCacheCreation(
				result.CacheCreationTokens,
				result.CacheCreation5mTokens,
				result.CacheCreation1hTokens,
			)
			params.Runtime.RecordStageTokenCounts(params.Stage, tokens.TokenCounts{
				CacheRead:       result.CacheReadTokens,
				CacheCreation5m: cacheCreation5m,
				CacheCreation1h: cacheCreation1h,
			})
		}

		// Evaluate model escalation when stage fails.
		// This mirrors the auto-mode pattern in scheduler.go and keeps the
		// RetryEngine as the single source of truth for escalation state.
		//
		// Model rejection is checked FIRST (#42): when the API refused the
		// selected model (unknown ID / not on plan / model-specific usage
		// cap), escalating UP the ladder would be refused the same way — the
		// correct move is a sticky tier DOWNGRADE. FallbackRecorded tells the
		// scheduler to notify + retry; the substitution itself lives on the
		// shared RetryEngine so the re-dispatch resolves the weaker tier.
		escalationRecorded := false
		fallbackRecorded := false
		fallbackFrom, fallbackTo := "", ""
		if exitCode != 0 && r.retryEngine != nil {
			if orchestrator.ClassifyTerminalKind(result.ErrorText) == orchestrator.TerminalKindModelUnavailable {
				if dg := r.retryEngine.EvaluateDowngrade(params.Model); dg.ShouldDowngrade {
					log.Printf("#%d: stage %s — model %s rejected by API; falling back to %s for the rest of the run",
						params.IssueNumber, params.Stage, params.Model, dg.NewTier)
					r.retryEngine.RecordDowngrade(params.Model, dg.NewTier)
					fallbackRecorded = true
					fallbackFrom, fallbackTo = params.Model, dg.NewTier
				} else {
					log.Printf("#%d: stage %s — model %s rejected by API and no weaker tier available (%s)",
						params.IssueNumber, params.Stage, params.Model, dg.Reason)
				}
			} else {
				decision := r.retryEngine.EvaluateEscalation(string(params.Stage), params.Model)
				if decision.ShouldEscalate {
					log.Printf("#%d: stage %s failed — escalating model to %s",
						params.IssueNumber, params.Stage, decision.NewModel)
					r.retryEngine.RecordEscalation(string(params.Stage), decision.NewModel)
					escalationRecorded = true
				}
			}
		}

		// Surface the executor's error text as a Go error so the scheduler's
		// stall-recovery / classification paths fire (Issue #3207). Without
		// this, IPC-mode stall-kills arrived at the scheduler with err==nil
		// and ClassifyTerminalKind(err.Error()) never matched, so the daily
		// JSONL either dropped the record or mis-classified it.
		var stageErr error
		if !result.Success && result.ErrorText != "" {
			stageErr = fmt.Errorf("%s", result.ErrorText)
		}

		return &orchestrator.StageRunResult{
			ExitCode:           exitCode,
			InputTokens:        result.InputTokens,
			OutputTokens:       result.OutputTokens,
			CacheReadTokens:    result.CacheReadTokens,
			CostUsd:            result.CostUsd,
			FeedbackFile:       result.FeedbackFile,
			EscalationRecorded: escalationRecorded,
			FallbackRecorded:   fallbackRecorded,
			FallbackFromModel:  fallbackFrom,
			FallbackToModel:    fallbackTo,
			ErrorText:          result.ErrorText,
			LastOutputLines:    result.LastOutputLines,
			// #3605 stage-exit diagnostic record fields. Empty when TS
			// SkillRunner hasn't been updated to populate them yet — Go
			// still writes a (terser) diagnostic record using its own
			// signal-source visibility plus zero values here.
			SessionID:           result.SessionID,
			Signal:              result.Signal,
			SignalSource:        result.SignalSource,
			KillCeiling:         result.KillCeiling,
			KillCeilingValue:    result.KillCeilingValue,
			ElapsedMs:           result.ElapsedMs,
			IdleMsAtExit:        result.IdleMsAtExit,
			CacheCreationTokens: result.CacheCreationTokens,
			LastBashCommand:     result.LastBashCommand,
			LastBashExit:        result.LastBashExit,
			RecentBash:          result.RecentBash,
			StopHookErrored:     result.StopHookErrored,
			StderrTail:          result.StderrTail,
			ToolCalls:           result.ToolCalls,
			// #3666 follow-up: budget-kill + shipped-partially via IPC.
			// Replaces the budget-overrun-{N}.json disk contract, which
			// silently broke for multi-repo workspaces (Go couldn't locate
			// the per-issue worktree from workspaceRoot alone).
			BudgetExceeded:   result.BudgetExceeded,
			ShippedPartially: result.ShippedPartially,
			ShippedPRNumber:  result.ShippedPRNumber,
			// #91 served-model attribution, observed by the TS SkillRunner's
			// stream parser and forwarded verbatim. Empty when TS hasn't been
			// updated or the stream carried no model info — Go then falls
			// back to attributing the requested model as before.
			ServedModel:             result.ServedModel,
			RefusalFallbackFrom:     result.RefusalFallbackFrom,
			RefusalFallbackTo:       result.RefusalFallbackTo,
			RefusalFallbackCategory: result.RefusalFallbackCategory,
		}, stageErr
	case <-ctx.Done():
		// Send abort to TypeScript
		r.server.Emit("pipeline.abort", AbortParams{
			IssueNumber: params.IssueNumber,
			Reason:      "context_cancelled",
		})
		return &orchestrator.StageRunResult{ExitCode: 1}, ctx.Err()
	}
}

// DeliverStageResult delivers a stage result from TypeScript to the waiting RunStage call.
// Called by the pipeline.stageResult IPC handler.
func (r *IpcStageRunner) DeliverStageResult(result StageResultParams) bool {
	key := fmt.Sprintf("%d#%s", result.IssueNumber, result.Stage)
	r.mu.Lock()
	ch, ok := r.pendingResults[key]
	r.mu.Unlock()

	if !ok {
		return false // No pending request for this stage
	}

	// Non-blocking send (channel is buffered with capacity 1)
	select {
	case ch <- result:
		return true
	default:
		return false
	}
}

// Verify IpcStageRunner implements the StageRunner interface at compile time.
var _ orchestrator.StageRunner = (*IpcStageRunner)(nil)

// RegisterStageResultHandler registers the pipeline.stageResult IPC method
// on the given server, routing results to the IpcStageRunner.
func RegisterStageResultHandler(srv *Server, runner *IpcStageRunner) {
	srv.methods["pipeline.stageResult"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p StageResultParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if !runner.DeliverStageResult(p) {
			return nil, fmt.Errorf("no pending stage for #%d/%s", p.IssueNumber, p.Stage)
		}
		return map[string]string{"status": "ok"}, nil
	}
}
