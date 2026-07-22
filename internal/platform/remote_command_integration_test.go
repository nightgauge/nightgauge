// Package platform provides integration tests for the remote command end-to-end
// flow: mock platform → poll → execute → acknowledge.
//
// These tests run without any external dependencies and are safe for CI.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- helpers ---

// remoteCommandIntegrationServer is a minimal mock of the platform's command
// endpoints: GET /v1/commands/pending and POST /v1/commands/{id}/ack.
type remoteCommandIntegrationServer struct {
	mu sync.Mutex

	// pending holds the commands to return on the next poll.  Replaced atomically
	// with nextBatch after a successful poll so subsequent polls return nothing.
	pending   []PendingCommand
	nextBatch []PendingCommand

	// ackReceived records (commandID → CommandResult) for every POST .../ack.
	ackReceived map[string]CommandResult

	// pollCount tracks total GET /v1/commands/pending requests.
	pollCount atomic.Int32

	// ackCount tracks total POST .../ack requests.
	ackCount atomic.Int32

	// pollErr, when set, makes the pending endpoint return an HTTP 500.
	pollErr atomic.Bool

	// ackErr, when set, makes the ack endpoint return an HTTP 500.
	ackErr atomic.Bool
}

func newRemoteCommandIntegrationServer(initialCmds []PendingCommand) *remoteCommandIntegrationServer {
	return &remoteCommandIntegrationServer{
		pending:     initialCmds,
		ackReceived: make(map[string]CommandResult),
	}
}

func (s *remoteCommandIntegrationServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Pending-commands poll
		case strings.HasPrefix(r.URL.Path, "/v1/commands/pending") && r.Method == http.MethodGet:
			s.pollCount.Add(1)
			if s.pollErr.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			s.mu.Lock()
			cmds := s.pending
			s.pending = s.nextBatch // next poll returns nothing (or whatever was queued)
			s.nextBatch = nil
			s.mu.Unlock()

			if cmds == nil {
				cmds = []PendingCommand{}
			}
			if err := json.NewEncoder(w).Encode(cmds); err != nil {
				http.Error(w, "encode error", http.StatusInternalServerError)
			}

		// Command acknowledgement
		case strings.Contains(r.URL.Path, "/ack") && r.Method == http.MethodPost:
			s.ackCount.Add(1)
			if s.ackErr.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Extract command ID from path: /v1/commands/{id}/ack
			parts := strings.Split(r.URL.Path, "/")
			var cmdID string
			for i, p := range parts {
				if p == "ack" && i > 0 {
					cmdID = parts[i-1]
					break
				}
			}

			var result CommandResult
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.ackReceived[cmdID] = result
			s.mu.Unlock()

			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// waitForAck blocks until the given command IDs have been acknowledged or the
// deadline is reached.
func (s *remoteCommandIntegrationServer) waitForAck(t *testing.T, ids []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		found := 0
		for _, id := range ids {
			if _, ok := s.ackReceived[id]; ok {
				found++
			}
		}
		s.mu.Unlock()
		if found == len(ids) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Helper()
	s.mu.Lock()
	got := len(s.ackReceived)
	s.mu.Unlock()
	t.Fatalf("timeout waiting for acknowledgements: want %d, got %d", len(ids), got)
}

// --- acknowledgingExecutor bridges platform.CommandExecutor and records acks ---

// acknowledgingExecutor wraps a simple dispatch function and calls
// CommandService.AcknowledgeCommand after each execution.  It implements
// platform.CommandExecutor so it can be used with CommandPoller.
type acknowledgingExecutor struct {
	svc    *CommandService
	handle func(cmd PendingCommand) (string, error)
}

func (a *acknowledgingExecutor) Execute(ctx context.Context, cmd PendingCommand) error {
	start := time.Now()
	output, execErr := a.handle(cmd)

	result := CommandResult{
		Status:     "success",
		Output:     output,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if execErr != nil {
		result.Status = "failure"
		result.Error = execErr.Error()
		result.Output = ""
	}
	return a.svc.AcknowledgeCommand(ctx, cmd.ID, result)
}

// --- integration tests ---

// TestRemoteCommand_PollExecuteAcknowledge_E2E tests the full remote command
// pipeline: mock platform → poll → execute → acknowledge.
func TestRemoteCommand_PollExecuteAcknowledge_E2E(t *testing.T) {
	cmds := []PendingCommand{
		{ID: "cmd-run-1", Type: "pipeline.run", Payload: json.RawMessage(`{"repo":"acme","issueNumber":42}`)},
		{ID: "cmd-cancel-1", Type: "pipeline.cancel", Payload: json.RawMessage(`{"executionId":"exec-abc"}`)},
	}

	mock := newRemoteCommandIntegrationServer(cmds)
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "test-api-key", AgentID: "agent-e2e"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	cmdSvc := NewCommandService(client)

	// Simple handler that records which command types were executed.
	var executedTypes []string
	var mu sync.Mutex

	exec := &acknowledgingExecutor{
		svc: cmdSvc,
		handle: func(cmd PendingCommand) (string, error) {
			mu.Lock()
			executedTypes = append(executedTypes, cmd.Type)
			mu.Unlock()
			return "ok", nil
		},
	}

	pollerCfg := CommandPollerConfig{
		PollInterval: 10 * time.Millisecond,
		MaxBackoff:   50 * time.Millisecond,
	}
	poller := NewCommandPoller(client, exec, pollerCfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)

	// Wait until both commands have been acknowledged.
	mock.waitForAck(t, []string{"cmd-run-1", "cmd-cancel-1"}, 3*time.Second)
	cancel()

	// Verify both command types were dispatched to the executor.
	mu.Lock()
	defer mu.Unlock()

	if len(executedTypes) < 2 {
		t.Fatalf("expected 2 commands executed, got %d", len(executedTypes))
	}

	seen := make(map[string]bool)
	for _, ct := range executedTypes {
		seen[ct] = true
	}
	if !seen["pipeline.run"] {
		t.Error("pipeline.run was not executed")
	}
	if !seen["pipeline.cancel"] {
		t.Error("pipeline.cancel was not executed")
	}

	// Verify acknowledgements were recorded on the server.
	mock.mu.Lock()
	runAck := mock.ackReceived["cmd-run-1"]
	cancelAck := mock.ackReceived["cmd-cancel-1"]
	mock.mu.Unlock()

	if runAck.Status != "success" {
		t.Errorf("cmd-run-1 ack status = %q, want 'success'", runAck.Status)
	}
	if cancelAck.Status != "success" {
		t.Errorf("cmd-cancel-1 ack status = %q, want 'success'", cancelAck.Status)
	}
}

// TestRemoteCommand_PipelineRun_AcknowledgedOnSuccess verifies that a
// pipeline.run command is acknowledged with status=success.
func TestRemoteCommand_PipelineRun_AcknowledgedOnSuccess(t *testing.T) {
	cmd := PendingCommand{
		ID:      "cmd-run-success",
		Type:    "pipeline.run",
		Payload: json.RawMessage(`{"repo":"acme","issueNumber":7}`),
	}

	mock := newRemoteCommandIntegrationServer([]PendingCommand{cmd})
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "key", AgentID: "agent-1"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	cmdSvc := NewCommandService(client)
	exec := &acknowledgingExecutor{
		svc:    cmdSvc,
		handle: func(_ PendingCommand) (string, error) { return "queued", nil },
	}

	pollerCfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(client, exec, pollerCfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)
	defer cancel()

	mock.waitForAck(t, []string{"cmd-run-success"}, 2*time.Second)

	mock.mu.Lock()
	ack := mock.ackReceived["cmd-run-success"]
	mock.mu.Unlock()

	if ack.Status != "success" {
		t.Errorf("ack.Status = %q, want 'success'", ack.Status)
	}
	if ack.Output != "queued" {
		t.Errorf("ack.Output = %q, want 'queued'", ack.Output)
	}
	if ack.DurationMs < 0 {
		t.Errorf("ack.DurationMs = %d, want >= 0", ack.DurationMs)
	}
}

// TestRemoteCommand_PipelineCancel_AcknowledgedOnSuccess verifies that a
// pipeline.cancel command is acknowledged with status=success.
func TestRemoteCommand_PipelineCancel_AcknowledgedOnSuccess(t *testing.T) {
	cmd := PendingCommand{
		ID:      "cmd-cancel-success",
		Type:    "pipeline.cancel",
		Payload: json.RawMessage(`{"executionId":"exec-xyz"}`),
	}

	mock := newRemoteCommandIntegrationServer([]PendingCommand{cmd})
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "key", AgentID: "agent-2"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	cmdSvc := NewCommandService(client)
	exec := &acknowledgingExecutor{
		svc:    cmdSvc,
		handle: func(_ PendingCommand) (string, error) { return "cancelled", nil },
	}

	pollerCfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(client, exec, pollerCfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)
	defer cancel()

	mock.waitForAck(t, []string{"cmd-cancel-success"}, 2*time.Second)

	mock.mu.Lock()
	ack := mock.ackReceived["cmd-cancel-success"]
	mock.mu.Unlock()

	if ack.Status != "success" {
		t.Errorf("ack.Status = %q, want 'success'", ack.Status)
	}
}

// TestRemoteCommand_ExecutionFailure_AcknowledgedWithFailureStatus verifies
// that when executor returns an error, the ack is sent with status=failure.
func TestRemoteCommand_ExecutionFailure_AcknowledgedWithFailureStatus(t *testing.T) {
	cmd := PendingCommand{
		ID:      "cmd-fail-1",
		Type:    "pipeline.run",
		Payload: json.RawMessage(`{"repo":"acme","issueNumber":1}`),
	}

	mock := newRemoteCommandIntegrationServer([]PendingCommand{cmd})
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "key", AgentID: "agent-3"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	cmdSvc := NewCommandService(client)
	exec := &acknowledgingExecutor{
		svc:    cmdSvc,
		handle: func(_ PendingCommand) (string, error) { return "", errors.New("simulated failure") },
	}

	pollerCfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(client, exec, pollerCfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)
	defer cancel()

	mock.waitForAck(t, []string{"cmd-fail-1"}, 2*time.Second)

	mock.mu.Lock()
	ack := mock.ackReceived["cmd-fail-1"]
	mock.mu.Unlock()

	if ack.Status != "failure" {
		t.Errorf("ack.Status = %q, want 'failure'", ack.Status)
	}
	if !strings.Contains(ack.Error, "simulated failure") {
		t.Errorf("ack.Error = %q, want to contain 'simulated failure'", ack.Error)
	}
}

// TestRemoteCommand_PollingBackoff_OnRepeatedFailures verifies that the polling
// loop applies exponential backoff when the platform returns errors, capping at
// MaxBackoff.
func TestRemoteCommand_PollingBackoff_OnRepeatedFailures(t *testing.T) {
	mock := newRemoteCommandIntegrationServer(nil)
	mock.pollErr.Store(true) // make every poll fail
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, AgentID: "agent-backoff"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	pollerCfg := CommandPollerConfig{
		PollInterval: 10 * time.Millisecond,
		MaxBackoff:   40 * time.Millisecond,
	}
	poller := NewCommandPoller(client, &NoOpCommandExecutor{}, pollerCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	poller.Start(ctx)

	<-ctx.Done()

	total := mock.pollCount.Load()
	// With poll 10ms → backoff 20ms → 40ms (capped), in 250ms we expect roughly
	// 5–8 attempts.  The key check is that backoff prevented hammering (not >15).
	if total > 15 {
		t.Errorf("backoff not applied: got %d poll attempts in 250ms, want <= 15", total)
	}
	if total < 2 {
		t.Errorf("expected at least 2 poll attempts, got %d", total)
	}
}

// TestRemoteCommand_GracefulShutdown_DuringExecution verifies that cancelling
// the context during command execution causes the poller to stop cleanly without
// dispatching subsequent pending commands.
func TestRemoteCommand_GracefulShutdown_DuringExecution(t *testing.T) {
	cmds := []PendingCommand{
		{ID: "cmd-slow-1", Type: "pipeline.run", Payload: json.RawMessage(`{"repo":"r","issueNumber":1}`)},
		{ID: "cmd-slow-2", Type: "pipeline.run", Payload: json.RawMessage(`{"repo":"r","issueNumber":2}`)},
		{ID: "cmd-slow-3", Type: "pipeline.run", Payload: json.RawMessage(`{"repo":"r","issueNumber":3}`)},
	}

	mock := newRemoteCommandIntegrationServer(cmds)
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "key", AgentID: "agent-shutdown"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	started := make(chan struct{}, 3) // signals that an execution has started
	unblock := make(chan struct{})    // closed to unblock executions

	var dispatchCount atomic.Int32

	cmdSvc := NewCommandService(client)
	exec := &acknowledgingExecutor{
		svc: cmdSvc,
		handle: func(cmd PendingCommand) (string, error) {
			dispatchCount.Add(1)
			started <- struct{}{}
			// Block until context cancellation or explicit unblock.
			select {
			case <-unblock:
			case <-time.After(5 * time.Second):
			}
			return "done", nil
		},
	}

	pollerCfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(client, exec, pollerCfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)

	// Wait for the first execution to begin.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first command to start execution")
	}

	// Cancel context while executor is still blocked on first command.
	cancel()
	// Unblock the executor so its goroutine can return.
	close(unblock)

	// Give the poller goroutine time to wind down.
	time.Sleep(80 * time.Millisecond)

	// The poller must not have dispatched all 3 commands because the context was
	// cancelled while the first was still executing.
	final := dispatchCount.Load()
	if final >= 3 {
		t.Errorf("expected poller to stop before dispatching all 3 commands, dispatched %d", final)
	}
}

// TestRemoteCommand_GracefulShutdown_EmptyQueue verifies that when no commands
// are pending, the poller shuts down cleanly without logging errors.
func TestRemoteCommand_GracefulShutdown_EmptyQueue(t *testing.T) {
	mock := newRemoteCommandIntegrationServer(nil) // no commands
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, AgentID: "agent-empty"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	pollerCfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(client, &NoOpCommandExecutor{}, pollerCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	poller.Start(ctx)
	<-ctx.Done()

	// No panic or hang — this test just verifies clean shutdown with empty queue.
	if mock.pollCount.Load() < 1 {
		t.Error("expected at least one poll attempt with empty queue")
	}
}
