package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
)

func TestRequestUnmarshal(t *testing.T) {
	raw := `{"id":1,"method":"board.list","params":{"owner":"nightgauge","projectNumber":5,"status":"Ready"}}`
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ID != 1 {
		t.Errorf("ID = %d, want 1", req.ID)
	}
	if req.Method != "board.list" {
		t.Errorf("Method = %q, want %q", req.Method, "board.list")
	}

	var params BoardListParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want %q", params.Owner, "nightgauge")
	}
	if params.ProjectNumber != 5 {
		t.Errorf("ProjectNumber = %d, want 5", params.ProjectNumber)
	}
	if params.Status != "Ready" {
		t.Errorf("Status = %q, want %q", params.Status, "Ready")
	}
}

func TestResponseMarshal(t *testing.T) {
	resp := Response{
		ID:     1,
		Result: map[string]string{"status": "ok"},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON output")
	}

	var parsed Response
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.ID != 1 {
		t.Errorf("ID = %d, want 1", parsed.ID)
	}
	if parsed.Error != nil {
		t.Error("Error should be nil")
	}
}

func TestErrorResponseMarshal(t *testing.T) {
	resp := Response{
		ID:    2,
		Error: &RPCError{Code: ErrMethodNotFound, Message: "unknown method: foo"},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed Response
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Error == nil {
		t.Fatal("Error should not be nil")
	}
	if parsed.Error.Code != ErrMethodNotFound {
		t.Errorf("Code = %d, want %d", parsed.Error.Code, ErrMethodNotFound)
	}
}

func TestEventMarshal(t *testing.T) {
	evt := Event{
		Event: "stage.complete",
		Data:  map[string]interface{}{"issue": 1311, "stage": "feature-dev"},
	}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON output")
	}
}

func TestBoardListParamsUnmarshal(t *testing.T) {
	raw := `{"owner":"nightgauge","projectNumber":5}`
	var p BoardListParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Status != "" {
		t.Errorf("Status = %q, want empty", p.Status)
	}
}

func TestNotifyStageTransitionParams(t *testing.T) {
	raw := `{"repo":"nightgauge/nightgauge","issueNumber":1899,"stage":"feature-dev","status":"running","runId":"0190076b-0000-7000-8000-000000001899"}`
	var p PipelineNotifyStageTransitionParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Repo != "nightgauge/nightgauge" {
		t.Errorf("Repo = %q", p.Repo)
	}
	if p.IssueNumber != 1899 {
		t.Errorf("IssueNumber = %d", p.IssueNumber)
	}
	if p.Stage != "feature-dev" {
		t.Errorf("Stage = %q", p.Stage)
	}
	if p.Status != "running" {
		t.Errorf("Status = %q", p.Status)
	}
}

func TestNotifyPhaseTransitionParams(t *testing.T) {
	raw := `{"repo":"nightgauge/nightgauge","issueNumber":1899,"stage":"feature-dev","name":"implementation","index":3,"total":14,"eventType":"start","runId":"0190076b-0000-7000-8000-000000001899"}`
	var p PipelineNotifyPhaseTransitionParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Name != "implementation" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Index != 3 {
		t.Errorf("Index = %d", p.Index)
	}
	if p.Total != 14 {
		t.Errorf("Total = %d", p.Total)
	}
	if p.EventType != "start" {
		t.Errorf("EventType = %q", p.EventType)
	}
}

// registerStageNotifyMethod registers only pipeline.notifyStageTransition for testing.
func (s *Server) registerStageNotifyMethod() {
	s.methods["pipeline.notifyStageTransition"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyStageTransitionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// Keyed by RUN IDENTITY, mirroring the production handler after the
		// ADR-017 step-4 re-key. The stub still adopts-on-miss because that is
		// what production does for a run-progress verb — what it does NOT do is
		// mint an identity of its own.
		s.runtimesMu.Lock()
		entry, ok := s.activeRuntimes[p.RunID]
		if !ok {
			entry = newRunEntry(state.NewRuntimeState(p.Repo, p.IssueNumber, "", p.RunID), p.Repo, p.IssueNumber)
			s.activeRuntimes[p.RunID] = entry
		}
		rt := entry.rs
		s.runtimesMu.Unlock()

		stage := state.PipelineStage(p.Stage)
		switch p.Status {
		case "running":
			rt.BeginStage(stage)
		case "complete":
			rt.CompleteStage(0, tokens.TokenCounts{Input: 0, Output: 0}, "", "")
		case "failed":
			rt.SetStageError(stage, p.Error)
		case "skipped", "deferred":
			rt.SkipStage(stage)
		}

		snap := rt.Snapshot()
		s.Emit("pipeline.stateChanged", map[string]interface{}{
			"repo":        p.Repo,
			"issueNumber": p.IssueNumber,
			"state":       snap,
		})
		return map[string]string{"status": "ok"}, nil
	}
}

// registerGetStateMethod registers only pipeline.getState for testing.
func (s *Server) registerGetStateMethod() {
	s.methods["pipeline.getState"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineGetStateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.execMgr != nil {
			key := fmt.Sprintf("%s/%s#%d", p.Owner, p.Repo, p.IssueNumber)
			if st := s.execMgr.GetState(key); st != nil {
				return st, nil
			}
		}
		if current, _ := s.currentRunForIssue("", p.IssueNumber); current != nil {
			return current.rs.Snapshot(), nil
		}
		if s.workspaceRoot != "" {
			stateDir := s.workspaceRoot + "/.nightgauge/pipeline"
			persisted, err := state.PickPersistedStateForIssue(stateDir, p.IssueNumber)
			if err == nil {
				return persisted, nil
			}
		}
		return nil, nil
	}
}

func TestNotifyStageTransitionHandler(t *testing.T) {
	// Create a server with a buffer writer to capture events
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	// Register only the methods we need
	s.registerStageNotifyMethod()

	// Call the handler
	params := json.RawMessage(`{"repo":"nightgauge/nightgauge","issueNumber":1899,"stage":"feature-dev","status":"running","runId":"0190076b-0000-7000-8000-000000001899"}`)
	result, err := s.methods["pipeline.notifyStageTransition"](context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	resultMap, ok := result.(map[string]string)
	if !ok || resultMap["status"] != "ok" {
		t.Errorf("unexpected result: %v", result)
	}

	// Verify runtime was created (keyed by issueNumber)
	s.runtimesMu.Lock()
	entry, exists := s.activeRuntimes["0190076b-0000-7000-8000-000000001899"]
	s.runtimesMu.Unlock()
	if !exists {
		t.Fatal("activeRuntimes should contain the new runtime, keyed by its run identity")
	}
	rt := entry.rs
	if rt.Stage != "feature-dev" {
		t.Errorf("Stage = %q, want feature-dev", rt.Stage)
	}

	// Verify stateChanged event was emitted
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("expected stateChanged event to be emitted")
	}
	var evt Event
	if err := json.Unmarshal(lines[0], &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if evt.Event != "pipeline.stateChanged" {
		t.Errorf("Event = %q, want pipeline.stateChanged", evt.Event)
	}
}

// TestNotifyStageProgressHandler_NoRuntimeIsBestEffort verifies #233's live
// progress handler is best-effort. Since ADR-017 step 4 it is RUN-PROGRESS
// class like every other run message, so an unknown identity ADOPTS (exactly
// once, through the per-id singleflight) rather than being dropped — this is
// the caller Decision 4 names as the ordinary concurrent adopter. What it still
// does NOT do is mutate CompletedStages. analyticsSvc is nil so
// emitStageProgressTelemetry is a no-op.
func TestNotifyStageProgressHandler_NoRuntimeIsBestEffort(t *testing.T) {
	s := &Server{
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	s.registerMethods()

	handler, ok := s.methods["pipeline.notifyStageProgress"]
	if !ok {
		t.Fatal("pipeline.notifyStageProgress must be registered")
	}

	params := json.RawMessage(`{"repo":"nightgauge/nightgauge","issueNumber":4242,"stage":"feature-dev","inputTokens":1500,"outputTokens":800,"cacheReadTokens":200,"costUsd":0.42,"runId":"01901092-0000-7000-8000-000000004242"}`)
	result, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if m, ok := result.(map[string]string); !ok || m["status"] != "ok" {
		t.Errorf("unexpected result: %v", result)
	}

	s.runtimesMu.Lock()
	adopted, exists := s.activeRuntimes["01901092-0000-7000-8000-000000004242"]
	n := len(s.activeRuntimes)
	s.runtimesMu.Unlock()
	if !exists || n != 1 {
		t.Fatalf("notifyStageProgress must adopt the caller's identity exactly once; %d entr(ies), present=%v", n, exists)
	}
	if len(adopted.rs.CompletedStages) != 0 {
		t.Error("progress is in-flight only: it must never write CompletedStages")
	}
}

// TestNotifyStageProgressHandler_DoesNotMutateCompletedStages verifies the
// live-estimate handler never touches the authoritative per-stage record — the
// terminal "complete" transition owns CompletedStages.
func TestNotifyStageProgressHandler_DoesNotMutateCompletedStages(t *testing.T) {
	s := &Server{
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	s.registerMethods()

	rt := state.NewRuntimeState("nightgauge/nightgauge", 99, "item-1", "01900063-0000-7000-8000-000000000099")
	rt.BeginStage(state.StageFeatureDev)
	rt.CompleteStageWithCost(0, 100, 50, 10, 0.02)
	before := len(rt.CompletedStages)
	beforeIn := rt.InputTokens
	s.activeRuntimes["01900063-0000-7000-8000-000000000099"] = newRunEntry(rt, "nightgauge/nightgauge", 99)

	params := json.RawMessage(`{"repo":"nightgauge/nightgauge","issueNumber":99,"stage":"feature-dev","inputTokens":9000,"outputTokens":400,"cacheReadTokens":100,"costUsd":0.3,"runId":"01900063-0000-7000-8000-000000000099"}`)
	if _, err := s.methods["pipeline.notifyStageProgress"](context.Background(), params); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if len(rt.CompletedStages) != before {
		t.Errorf("CompletedStages mutated: got %d, want %d (progress is in-flight only)", len(rt.CompletedStages), before)
	}
	if rt.InputTokens != beforeIn {
		t.Errorf("InputTokens mutated: got %d, want %d", rt.InputTokens, beforeIn)
	}
}

func TestGetStateFallback(t *testing.T) {
	// Create a server with a persisted state file
	tmpDir := t.TempDir()
	stateDir := tmpDir + "/.nightgauge/pipeline"

	rs := state.NewRuntimeState("nightgauge/nightgauge", 1899, "item-1", newTestRunID())
	rs.BeginStage("feature-dev")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 500, Output: 200}, "", "")
	if err := rs.Persist(stateDir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
		workspaceRoot:  tmpDir,
	}
	s.registerGetStateMethod()

	// Call getState — should fall back to persisted file
	params := json.RawMessage(`{"owner":"nightgauge","repo":"nightgauge","issueNumber":1899}`)
	result, err := s.methods["pipeline.getState"](context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from persisted state")
	}
	loaded, ok := result.(*state.RuntimeState)
	if !ok {
		t.Fatalf("result type = %T, want *state.RuntimeState", result)
	}
	if loaded.IssueNumber != 1899 {
		t.Errorf("IssueNumber = %d", loaded.IssueNumber)
	}
	if len(loaded.CompletedStages) != 1 {
		t.Errorf("CompletedStages = %d, want 1", len(loaded.CompletedStages))
	}
}

func TestGetStateActiveRuntime(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
	}
	s.registerGetStateMethod()

	// Insert an active runtime — keyed by RUN IDENTITY, found by the derived
	// issue index (ADR-017 step 4, Decision 6).
	rt := state.NewRuntimeState("nightgauge/nightgauge", 42, "item-42", newTestRunID())
	rt.BeginStage("issue-pickup")
	s.runtimesMu.Lock()
	s.activeRuntimes[rt.RunID] = newRunEntry(rt, "nightgauge/nightgauge", 42)
	s.runtimesMu.Unlock()

	params := json.RawMessage(`{"owner":"nightgauge","repo":"nightgauge","issueNumber":42}`)
	result, err := s.methods["pipeline.getState"](context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from activeRuntimes")
	}
	snap, ok := result.(*state.RuntimeState)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if snap.Stage != "issue-pickup" {
		t.Errorf("Stage = %q", snap.Stage)
	}
}
