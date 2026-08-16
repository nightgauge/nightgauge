package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// testRunID is a canonical lowercase UUIDv7. Every RunStage test that expects
// a dispatch to succeed must carry one: RunStage refuses to emit without a run
// identity (ADR-017 step 0b), so an omitted RunID is no longer a benign default
// but the assertion firing.
const testRunID = "01890a5d-ac96-774b-bcce-b302099a8057"

// newTestStageRunner creates an IpcStageRunner backed by a minimal Server
// that writes events to the provided buffer. This mirrors the pattern used
// in server_test.go and server_protocol_test.go.
func newTestStageRunner(buf *bytes.Buffer) *IpcStageRunner {
	srv := &Server{
		writer:         buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	return NewIpcStageRunner(srv, nil) // nil engine: escalation evaluation skipped in existing tests
}

// ─── DeliverStageResult ───────────────────────────────────────────────────

// TestDeliverStageResult_DeliversToPendingChannel verifies that when a
// pending result channel exists for the given issue/stage key,
// DeliverStageResult sends the result to that channel and returns true.
func TestDeliverStageResult_DeliversToPendingChannel(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	// Manually add a pending channel (simulates what RunStage does)
	ch := make(chan StageResultParams, 1)
	runner.mu.Lock()
	runner.pendingResults["42#feature-dev"] = ch
	runner.mu.Unlock()

	result := StageResultParams{
		Stage:        "feature-dev",
		IssueNumber:  42,
		Success:      true,
		ExitCode:     0,
		InputTokens:  1000,
		OutputTokens: 500,
	}

	ok := runner.DeliverStageResult(result)
	if !ok {
		t.Fatal("DeliverStageResult returned false, want true")
	}

	// Verify the result was delivered to the channel
	select {
	case delivered := <-ch:
		if delivered.IssueNumber != 42 {
			t.Errorf("IssueNumber = %d, want 42", delivered.IssueNumber)
		}
		if delivered.Stage != "feature-dev" {
			t.Errorf("Stage = %q, want feature-dev", delivered.Stage)
		}
		if !delivered.Success {
			t.Error("Success = false, want true")
		}
		if delivered.InputTokens != 1000 {
			t.Errorf("InputTokens = %d, want 1000", delivered.InputTokens)
		}
		if delivered.OutputTokens != 500 {
			t.Errorf("OutputTokens = %d, want 500", delivered.OutputTokens)
		}
	default:
		t.Fatal("expected result on channel, got nothing")
	}
}

// TestDeliverStageResult_ReturnsFalseWhenNoPending verifies that
// DeliverStageResult returns false when there is no pending request
// for the given issue/stage combination.
func TestDeliverStageResult_ReturnsFalseWhenNoPending(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	result := StageResultParams{
		Stage:       "feature-dev",
		IssueNumber: 99,
		Success:     true,
		ExitCode:    0,
	}

	ok := runner.DeliverStageResult(result)
	if ok {
		t.Error("DeliverStageResult returned true, want false (no pending request)")
	}
}

// TestDeliverStageResult_ReturnsFalseWhenChannelFull verifies that
// DeliverStageResult returns false when the channel is already full
// (duplicate delivery attempt).
func TestDeliverStageResult_ReturnsFalseWhenChannelFull(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	ch := make(chan StageResultParams, 1)
	runner.mu.Lock()
	runner.pendingResults["10#issue-pickup"] = ch
	runner.mu.Unlock()

	result := StageResultParams{
		Stage:       "issue-pickup",
		IssueNumber: 10,
		Success:     true,
		ExitCode:    0,
	}

	// First delivery should succeed
	if ok := runner.DeliverStageResult(result); !ok {
		t.Fatal("first DeliverStageResult returned false, want true")
	}

	// Second delivery to the same (full) channel should fail
	if ok := runner.DeliverStageResult(result); ok {
		t.Error("second DeliverStageResult returned true, want false (channel full)")
	}
}

// ─── RunStage ─────────────────────────────────────────────────────────────

// TestRunStage_EmitsEventAndReturnsResult verifies the full RunStage flow:
// it emits a pipeline.runStage event to the writer and returns the result
// when DeliverStageResult is called from another goroutine.
func TestRunStage_EmitsEventAndReturnsResult(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 42, "item", testRunID)
	runtime.BeginStage(state.StageFeatureDev)

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("feature-dev"),
		IssueNumber: 42,
		Repo:        "nightgauge/nightgauge",
		Model:       "claude-sonnet-4-20250514",
		Timeout:     30 * time.Second,
		ContextFile: "/tmp/ctx.json",
		OutputFile:  "/tmp/out.json",
		TargetRepo:  "/workspace/repo",
		RunID:       testRunID,
		Runtime:     runtime,
	}

	// Run RunStage in a goroutine since it blocks until result is delivered
	resultCh := make(chan struct {
		result *orchestrator.StageRunResult
		err    error
	}, 1)

	go func() {
		r, err := runner.RunStage(context.Background(), params)
		resultCh <- struct {
			result *orchestrator.StageRunResult
			err    error
		}{r, err}
	}()

	// Wait briefly for RunStage to register the pending channel and emit the event
	time.Sleep(50 * time.Millisecond)

	// Deliver the result (simulates TypeScript sending pipeline.stageResult)
	delivered := runner.DeliverStageResult(StageResultParams{
		Stage:                 "feature-dev",
		IssueNumber:           42,
		Success:               true,
		ExitCode:              0,
		InputTokens:           2000,
		OutputTokens:          800,
		CacheReadTokens:       1200,
		CacheCreationTokens:   3308,
		CacheCreation1hTokens: 3308,
		FeedbackFile:          "/tmp/feedback.json",
	})
	if !delivered {
		t.Fatal("DeliverStageResult returned false, expected pending channel")
	}

	// Wait for RunStage to return
	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("RunStage error: %v", res.err)
		}
		if res.result.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0", res.result.ExitCode)
		}
		if res.result.InputTokens != 2000 {
			t.Errorf("InputTokens = %d, want 2000", res.result.InputTokens)
		}
		if res.result.OutputTokens != 800 {
			t.Errorf("OutputTokens = %d, want 800", res.result.OutputTokens)
		}
		if res.result.FeedbackFile != "/tmp/feedback.json" {
			t.Errorf("FeedbackFile = %q, want /tmp/feedback.json", res.result.FeedbackFile)
		}
		// Mirrors scheduler.go's existing calculated-cost call: the result's
		// unsplit total is placed in 5m while the IPC handoff carries the real
		// 1h split. Completion must prefer the split without double-counting.
		runtime.CompleteStage(
			res.result.ExitCode,
			tokens.TokenCounts{
				Input:           res.result.InputTokens,
				Output:          res.result.OutputTokens,
				CacheRead:       res.result.CacheReadTokens,
				CacheCreation5m: res.result.CacheCreationTokens,
			},
			"claude-haiku-4-5-20251001",
			"",
		)
		if got := runtime.CompletedStages[0].CacheCreation; got != 3308 {
			t.Errorf("history handoff CacheCreation = %d, want 3308", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunStage did not return within 2 seconds")
	}

	// Verify the pipeline.runStage event was emitted
	output := buf.String()
	if !strings.Contains(output, `"event":"pipeline.runStage"`) {
		t.Errorf("expected pipeline.runStage event in output, got: %s", output)
	}

	// Verify the emitted event contains the correct parameters
	var evt Event
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		var candidate Event
		if err := json.Unmarshal([]byte(line), &candidate); err == nil && candidate.Event == "pipeline.runStage" {
			evt = candidate
			break
		}
	}
	if evt.Event == "" {
		t.Fatal("could not find pipeline.runStage event in output")
	}

	// Marshal Data back to JSON for inspection
	dataBytes, err := json.Marshal(evt.Data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	var emittedParams RunStageParams
	if err := json.Unmarshal(dataBytes, &emittedParams); err != nil {
		t.Fatalf("unmarshal RunStageParams: %v", err)
	}
	if emittedParams.Stage != "feature-dev" {
		t.Errorf("emitted Stage = %q, want feature-dev", emittedParams.Stage)
	}
	if emittedParams.IssueNumber != 42 {
		t.Errorf("emitted IssueNumber = %d, want 42", emittedParams.IssueNumber)
	}
	if emittedParams.Repo != "nightgauge/nightgauge" {
		t.Errorf("emitted Repo = %q", emittedParams.Repo)
	}

	// Verify the pending channel is cleaned up
	runner.mu.Lock()
	_, exists := runner.pendingResults["42#feature-dev"]
	runner.mu.Unlock()
	if exists {
		t.Error("pending channel should be cleaned up after RunStage returns")
	}
}

// TestRunStage_FailureExitCodeNonZero verifies that when TypeScript reports
// success=false with exitCode=0, RunStage normalizes the exit code to 1.
func TestRunStage_FailureExitCodeNonZero(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("feature-validate"),
		IssueNumber: 55,
		Repo:        "nightgauge/nightgauge",
		Timeout:     10 * time.Second,
		RunID:       testRunID,
	}

	resultCh := make(chan struct {
		result *orchestrator.StageRunResult
		err    error
	}, 1)

	go func() {
		r, err := runner.RunStage(context.Background(), params)
		resultCh <- struct {
			result *orchestrator.StageRunResult
			err    error
		}{r, err}
	}()

	time.Sleep(50 * time.Millisecond)

	// Deliver a failure result with exitCode=0 (should be normalized to 1)
	runner.DeliverStageResult(StageResultParams{
		Stage:       "feature-validate",
		IssueNumber: 55,
		Success:     false,
		ExitCode:    0,
	})

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("RunStage error: %v", res.err)
		}
		if res.result.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1 (normalized from success=false, exitCode=0)", res.result.ExitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunStage did not return within 2 seconds")
	}
}

// TestRunStage_ReturnsErrorOnContextCancelled verifies that RunStage
// returns ctx.Err() when the context is cancelled, and emits an abort event.
func TestRunStage_ReturnsErrorOnContextCancelled(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	ctx, cancel := context.WithCancel(context.Background())

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("pr-create"),
		IssueNumber: 77,
		Repo:        "nightgauge/nightgauge",
		Timeout:     10 * time.Second,
		RunID:       testRunID,
	}

	resultCh := make(chan struct {
		result *orchestrator.StageRunResult
		err    error
	}, 1)

	go func() {
		r, err := runner.RunStage(ctx, params)
		resultCh <- struct {
			result *orchestrator.StageRunResult
			err    error
		}{r, err}
	}()

	// Wait for RunStage to register the pending channel
	time.Sleep(50 * time.Millisecond)

	// Cancel the context
	cancel()

	select {
	case res := <-resultCh:
		if res.err == nil {
			t.Fatal("expected error from RunStage, got nil")
		}
		if res.err != context.Canceled {
			t.Errorf("error = %v, want context.Canceled", res.err)
		}
		if res.result.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1", res.result.ExitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunStage did not return within 2 seconds after cancel")
	}

	// Verify abort event was emitted
	output := buf.String()
	if !strings.Contains(output, `"event":"pipeline.abort"`) {
		t.Errorf("expected pipeline.abort event in output, got: %s", output)
	}

	// Verify abort params
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		var evt Event
		if err := json.Unmarshal([]byte(line), &evt); err == nil && evt.Event == "pipeline.abort" {
			dataBytes, _ := json.Marshal(evt.Data)
			var abortParams AbortParams
			if err := json.Unmarshal(dataBytes, &abortParams); err != nil {
				t.Fatalf("unmarshal AbortParams: %v", err)
			}
			if abortParams.IssueNumber != 77 {
				t.Errorf("abort IssueNumber = %d, want 77", abortParams.IssueNumber)
			}
			if abortParams.Reason != "context_cancelled" {
				t.Errorf("abort Reason = %q, want context_cancelled", abortParams.Reason)
			}
			break
		}
	}

	// Verify the pending channel is cleaned up
	runner.mu.Lock()
	_, exists := runner.pendingResults["77#pr-create"]
	runner.mu.Unlock()
	if exists {
		t.Error("pending channel should be cleaned up after context cancellation")
	}
}

// TestRunStage_ReturnsErrorOnContextTimeout verifies that RunStage returns
// the context deadline error when the context times out.
func TestRunStage_ReturnsErrorOnContextTimeout(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("feature-dev"),
		IssueNumber: 88,
		Repo:        "nightgauge/nightgauge",
		Timeout:     10 * time.Second,
		RunID:       testRunID,
	}

	result, err := runner.RunStage(ctx, params)
	if err == nil {
		t.Fatal("expected error from RunStage, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
}

// TestRunStage_AutonomousMode_ForwardsFlag verifies that when
// AutonomousMode is true on the IpcStageRunner, the emitted
// pipeline.runStage event includes "autonomousMode":true in its JSON payload.
func TestRunStage_AutonomousMode_ForwardsFlag(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)
	runner.AutonomousMode = true

	ctx, cancel := context.WithCancel(context.Background())

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("feature-dev"),
		IssueNumber: 101,
		Repo:        "nightgauge/nightgauge",
		Model:       "claude-sonnet-4-20250514",
		Timeout:     10 * time.Second,
		RunID:       testRunID,
	}

	emittedParams := runStageAndCapture(t, runner, ctx, cancel, params, &buf)
	if !emittedParams.AutonomousMode {
		t.Error("emitted AutonomousMode = false, want true")
	}
	if emittedParams.IssueNumber != 101 {
		t.Errorf("emitted IssueNumber = %d, want 101", emittedParams.IssueNumber)
	}
	if emittedParams.Stage != "feature-dev" {
		t.Errorf("emitted Stage = %q, want feature-dev", emittedParams.Stage)
	}
}

// TestRunStage_ForwardsRunID verifies that the run identity the scheduler
// populated on StageRunParams is threaded onto the emitted pipeline.runStage
// payload as "runId" (#228, ADR-017 step 0b). Without this, the TS SkillRunner
// opens the SDK TraceRecorder with no run_id and it silently disables, so
// interactive/IPC runs lose their trace.
//
// The value is read from params.RunID, not params.Runtime.RunID: the scheduler
// is the authority that mints the id and stamps it on the dispatch, and the
// runner must not re-derive it from a second source that could disagree.
func TestRunStage_ForwardsRunID(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	ctx, cancel := context.WithCancel(context.Background())

	// Runtime carries a DIFFERENT id than the dispatch on purpose. In
	// production the two always agree — the scheduler stamps params.RunID from
	// runtime.RunID — so a test where they agree cannot tell which one the
	// runner read, and `params.RunID` could be swapped for
	// `params.Runtime.RunID` with the suite green. Disagreeing here makes the
	// single-authority property observable: the emitted value must be the
	// DISPATCH's id.
	rt := state.NewRuntimeState("nightgauge/nightgauge", 101, "", "0189ffff-dead-7bee-8fff-000000000000")

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("feature-dev"),
		IssueNumber: 101,
		Repo:        "nightgauge/nightgauge",
		Model:       "claude-sonnet-4-20250514",
		Timeout:     10 * time.Second,
		Runtime:     rt,
		RunID:       testRunID,
	}

	emittedParams := runStageAndCapture(t, runner, ctx, cancel, params, &buf)
	if emittedParams.RunID != testRunID {
		t.Errorf("emitted RunID = %q, want the dispatch's RunID %q (Runtime carried %q — the runner must "+
			"not re-derive the id from a second source)", emittedParams.RunID, testRunID, rt.RunID)
	}
}

// TestRunStage_MissingRunIDRefusesDispatchAndDoesNotEmit is the ADR-017 step-0b
// assertion, and it inverts the pre-#370 TestRunStage_NilRuntimeYieldsEmptyRunID
// — which codified the exact behaviour that made F16 possible.
//
// A dispatch with no run identity used to emit pipeline.runStage with
// runId="". Today that costs the stage its lifecycle trace: the SDK
// TraceRecorder gets no id, falls back to run-state.json, and silently disables
// itself when that does not resolve either (skillRunner.ts:3013-3019). Once
// Decision 3's `run_id_required` lands (step 4), the same dispatch additionally
// has its notifyPhaseTransition and notifyStageProgress calls hard-rejected by
// the server, both rejections swallowed by PipelineBridge's bare `.catch(warn)`
// — so the Go-scheduler IPC dispatch path loses phase markers and live progress
// for that stage with no error anywhere, after a full stage's worth of tokens
// has been spent.
//
// The fix is to fail at the dispatch boundary instead: loudly, at t=0, before
// a child process spawns and before a single token is spent. The two halves
// asserted here are therefore inseparable — an error that still emitted would
// spawn the doomed stage anyway.
func TestRunStage_MissingRunIDRefusesDispatchAndDoesNotEmit(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("feature-dev"),
		IssueNumber: 101,
		Repo:        "nightgauge/nightgauge",
		Model:       "claude-sonnet-4-20250514",
		Timeout:     10 * time.Second,
		// RunID and Runtime intentionally absent — a programming error, not a
		// supported configuration.
	}

	result, err := runner.RunStage(context.Background(), params)
	if err == nil {
		t.Fatal("expected an error dispatching a stage with no run identity, got nil")
	}
	if !strings.Contains(err.Error(), "run identity") {
		t.Errorf("error = %q, should name the missing invariant (run identity)", err.Error())
	}
	if result == nil || result.ExitCode == 0 {
		t.Errorf("expected a non-zero exit result, got %+v", result)
	}

	// Nothing may reach TypeScript: no stage spawns, no tokens are spent.
	if out := buf.String(); strings.Contains(out, "pipeline.runStage") {
		t.Errorf("pipeline.runStage was emitted despite the missing run identity: %s", out)
	}

	// The refusal happens before any bookkeeping, so nothing is left behind.
	runner.mu.Lock()
	_, exists := runner.pendingResults["101#feature-dev"]
	runner.mu.Unlock()
	if exists {
		t.Error("a pending result channel was registered for a refused dispatch")
	}
}

// runStageAndCapture drives RunStage on a goroutine, lets the initial
// pipeline.runStage emit land, cancels, and — crucially — waits for RunStage to
// fully return (its ctx.Done branch emits a second pipeline.abort event to the
// same buffer) BEFORE reading. Without the wait the buffer read races the abort
// emit, which both trips -race and intermittently truncates the read.
func runStageAndCapture(
	t *testing.T,
	runner *IpcStageRunner,
	ctx context.Context,
	cancel context.CancelFunc,
	params orchestrator.StageRunParams,
	buf *bytes.Buffer,
) RunStageParams {
	t.Helper()
	done := make(chan struct{})
	go func() {
		runner.RunStage(ctx, params)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	return parseRunStageEvent(t, buf.String())
}

// parseRunStageEvent extracts the RunStageParams from the first
// pipeline.runStage event in the runner's stdout buffer.
func parseRunStageEvent(t *testing.T, output string) RunStageParams {
	t.Helper()
	var evt Event
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var candidate Event
		if err := json.Unmarshal([]byte(line), &candidate); err == nil && candidate.Event == "pipeline.runStage" {
			evt = candidate
			break
		}
	}
	if evt.Event == "" {
		t.Fatalf("could not find pipeline.runStage event in output: %s", output)
	}
	dataBytes, err := json.Marshal(evt.Data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	var emittedParams RunStageParams
	if err := json.Unmarshal(dataBytes, &emittedParams); err != nil {
		t.Fatalf("unmarshal RunStageParams: %v", err)
	}
	return emittedParams
}

// ─── RegisterStageResultHandler ───────────────────────────────────────────

// TestRegisterStageResultHandler_RoutesResultToRunner verifies that the
// pipeline.stageResult IPC method is correctly registered and routes results
// through DeliverStageResult.
func TestRegisterStageResultHandler_RoutesResultToRunner(t *testing.T) {
	var buf bytes.Buffer
	srv := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	runner := NewIpcStageRunner(srv, nil)
	RegisterStageResultHandler(srv, runner)

	// Set up a pending channel
	ch := make(chan StageResultParams, 1)
	runner.mu.Lock()
	runner.pendingResults["123#feature-dev"] = ch
	runner.mu.Unlock()

	// Call the registered handler
	params := json.RawMessage(`{"stage":"feature-dev","issueNumber":123,"success":true,"exitCode":0,"inputTokens":500,"outputTokens":200}`)
	result, err := srv.methods["pipeline.stageResult"](context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	resultMap, ok := result.(map[string]string)
	if !ok || resultMap["status"] != "ok" {
		t.Errorf("unexpected result: %v", result)
	}

	// Verify result was delivered
	select {
	case delivered := <-ch:
		if delivered.IssueNumber != 123 {
			t.Errorf("IssueNumber = %d, want 123", delivered.IssueNumber)
		}
	default:
		t.Fatal("expected result on pending channel")
	}
}

// TestRegisterStageResultHandler_ErrorsWhenNoPending verifies that the
// handler returns an error when there is no pending stage request.
func TestRegisterStageResultHandler_ErrorsWhenNoPending(t *testing.T) {
	var buf bytes.Buffer
	srv := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	runner := NewIpcStageRunner(srv, nil)
	RegisterStageResultHandler(srv, runner)

	params := json.RawMessage(`{"stage":"feature-dev","issueNumber":999,"success":true,"exitCode":0}`)
	_, err := srv.methods["pipeline.stageResult"](context.Background(), params)
	if err == nil {
		t.Fatal("expected error when no pending stage, got nil")
	}
	if !strings.Contains(err.Error(), "no pending stage") {
		t.Errorf("error = %q, should contain 'no pending stage'", err.Error())
	}
}

// TestRegisterStageResultHandler_ErrorsOnInvalidJSON verifies that the
// handler returns an error when the params JSON is malformed.
func TestRegisterStageResultHandler_ErrorsOnInvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	srv := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	runner := NewIpcStageRunner(srv, nil)
	RegisterStageResultHandler(srv, runner)

	params := json.RawMessage(`{not valid json}`)
	_, err := srv.methods["pipeline.stageResult"](context.Background(), params)
	if err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "invalid params") {
		t.Errorf("error = %q, should contain 'invalid params'", err.Error())
	}
}

// TestRunStage_ForwardsServedModelAttribution is the #91 IPC-path regression
// test: the served-model fields TS observed in the CLI stream (the CLI's
// silent model_refusal_fallback swap) must flow through pipeline.stageResult
// into the scheduler's StageRunResult so cost/telemetry/history attribute the
// model that actually served, not the one requested.
func TestRunStage_ForwardsServedModelAttribution(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	params := orchestrator.StageRunParams{
		Stage:       state.PipelineStage("feature-dev"),
		IssueNumber: 91,
		Repo:        "nightgauge/nightgauge",
		Model:       "claude-fable-5",
		Timeout:     30 * time.Second,
		RunID:       testRunID,
	}

	resultCh := make(chan struct {
		result *orchestrator.StageRunResult
		err    error
	}, 1)
	go func() {
		r, err := runner.RunStage(context.Background(), params)
		resultCh <- struct {
			result *orchestrator.StageRunResult
			err    error
		}{r, err}
	}()
	time.Sleep(50 * time.Millisecond)

	delivered := runner.DeliverStageResult(StageResultParams{
		Stage:        "feature-dev",
		IssueNumber:  91,
		Success:      true,
		ExitCode:     0,
		InputTokens:  1000,
		OutputTokens: 200,
		// TS observed the CLI swap Fable → Opus mid-stage.
		ServedModel:             "claude-opus-4-8",
		RefusalFallbackFrom:     "claude-fable-5",
		RefusalFallbackTo:       "claude-opus-4-8",
		RefusalFallbackCategory: "reasoning_extraction",
	})
	if !delivered {
		t.Fatal("DeliverStageResult returned false")
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("RunStage error: %v", res.err)
		}
		if res.result.ServedModel != "claude-opus-4-8" {
			t.Errorf("ServedModel = %q, want claude-opus-4-8", res.result.ServedModel)
		}
		if res.result.RefusalFallbackFrom != "claude-fable-5" {
			t.Errorf("RefusalFallbackFrom = %q", res.result.RefusalFallbackFrom)
		}
		if res.result.RefusalFallbackTo != "claude-opus-4-8" {
			t.Errorf("RefusalFallbackTo = %q", res.result.RefusalFallbackTo)
		}
		if res.result.RefusalFallbackCategory != "reasoning_extraction" {
			t.Errorf("RefusalFallbackCategory = %q", res.result.RefusalFallbackCategory)
		}
		// The CLI's internal swap must NOT be conflated with the #42
		// retry-engine downgrade path — no sticky downgrade, no retry.
		if res.result.FallbackRecorded {
			t.Error("FallbackRecorded must stay false for a CLI-internal refusal swap")
		}
		if res.result.FallbackFromModel != "" || res.result.FallbackToModel != "" {
			t.Errorf("#42 fallback fields must stay empty, got %q → %q",
				res.result.FallbackFromModel, res.result.FallbackToModel)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunStage did not return within 2 seconds")
	}
}
