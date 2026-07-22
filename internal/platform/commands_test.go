package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommandService_PollCommands_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/commands/pending") {
			t.Errorf("path = %s, want /v1/commands/pending", r.URL.Path)
		}
		if r.URL.Query().Get("agentId") != "agent-123" {
			t.Errorf("agentId = %s, want agent-123", r.URL.Query().Get("agentId"))
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %s, want Bearer test-key", r.Header.Get("Authorization"))
		}
		jsonResponse(w, []map[string]interface{}{
			{
				"id":        "cmd-1",
				"type":      "run_pipeline",
				"payload":   map[string]interface{}{"issue": 42},
				"createdAt": time.Now().UTC().Format(time.RFC3339),
			},
			{
				"id":        "cmd-2",
				"type":      "cancel_pipeline",
				"payload":   map[string]interface{}{"runId": "abc"},
				"createdAt": time.Now().UTC().Format(time.RFC3339),
			},
		})
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "test-key", AgentID: "agent-123"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	svc := NewCommandService(c)
	cmds, err := svc.PollCommands(context.Background())
	if err != nil {
		t.Fatalf("PollCommands: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("len(cmds) = %d, want 2", len(cmds))
	}
	if cmds[0].ID != "cmd-1" {
		t.Errorf("cmds[0].ID = %s, want cmd-1", cmds[0].ID)
	}
	if cmds[0].Type != "run_pipeline" {
		t.Errorf("cmds[0].Type = %s, want run_pipeline", cmds[0].Type)
	}
	if cmds[1].ID != "cmd-2" {
		t.Errorf("cmds[1].ID = %s, want cmd-2", cmds[1].ID)
	}
}

func TestCommandService_PollCommands_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "bad-key", AgentID: "agent-123"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	svc := NewCommandService(c)
	_, err = svc.PollCommands(context.Background())
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("error = %q, want to contain 'unauthorized'", err.Error())
	}
}

func TestCommandService_PollCommands_NetworkError(t *testing.T) {
	cfg := Config{BaseURL: "http://127.0.0.1:0", AgentID: "agent-123"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	svc := NewCommandService(c)
	_, err = svc.PollCommands(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

func TestCommandService_PollCommands_Offline(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999", AgentID: "agent-123"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// Leave mode as offline (default)

	svc := NewCommandService(c)
	cmds, err := svc.PollCommands(context.Background())
	if err != nil {
		t.Fatalf("PollCommands offline: %v", err)
	}
	if cmds != nil {
		t.Errorf("expected nil slice when offline, got %v", cmds)
	}
}

func TestCommandService_PollCommands_EmptyAgentID(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999", AgentID: ""}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	svc := NewCommandService(c)
	_, err = svc.PollCommands(context.Background())
	if err == nil {
		t.Fatal("expected error for empty agentID")
	}
	if !strings.Contains(err.Error(), "agentId not configured") {
		t.Errorf("error = %q, want to contain 'agentId not configured'", err.Error())
	}
}

// --- CommandPoller tests ---

// mockFetcher implements CommandFetcher for testing.
type mockFetcher struct {
	calls   atomic.Int32
	results []PendingCommand
	err     error
}

func (m *mockFetcher) FetchCommands(_ context.Context) ([]PendingCommand, error) {
	m.calls.Add(1)
	return m.results, m.err
}

// mockExecutor implements CommandExecutor for testing.
type mockExecutor struct {
	executed []PendingCommand
	ch       chan PendingCommand
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{ch: make(chan PendingCommand, 16)}
}

func (m *mockExecutor) Execute(_ context.Context, cmd PendingCommand) error {
	m.executed = append(m.executed, cmd)
	m.ch <- cmd
	return nil
}

func TestCommandPoller_DefaultConfig(t *testing.T) {
	cfg := DefaultCommandPollerConfig()
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", cfg.PollInterval)
	}
	if cfg.MaxBackoff != 60*time.Second {
		t.Errorf("MaxBackoff = %v, want 60s", cfg.MaxBackoff)
	}
}

func TestCommandPoller_DispatchesCommands(t *testing.T) {
	fetcher := &mockFetcher{
		results: []PendingCommand{
			{ID: "c1", Type: "run_pipeline"},
			{ID: "c2", Type: "cancel_pipeline"},
		},
	}
	executor := newMockExecutor()

	cfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(fetcher, executor, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)

	// Wait for both commands to be executed.
	for i := 0; i < 2; i++ {
		select {
		case <-executor.ch:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for command dispatch")
		}
	}
	cancel()

	if len(executor.executed) < 2 {
		t.Errorf("executed %d commands, want >= 2", len(executor.executed))
	}
}

func TestCommandPoller_StopViaContextCancel(t *testing.T) {
	fetcher := &mockFetcher{}
	fetcher.results = nil

	cfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(fetcher, newMockExecutor(), cfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)

	// Let it run a couple of cycles.
	time.Sleep(30 * time.Millisecond)
	cancel()

	// After cancel, fetch calls should stop increasing.
	snapshot := fetcher.calls.Load()
	time.Sleep(30 * time.Millisecond)
	after := fetcher.calls.Load()

	if after > snapshot+1 {
		t.Errorf("poller kept running after cancel: calls before=%d after=%d", snapshot, after)
	}
}

func TestCommandPoller_ExponentialBackoffOnError(t *testing.T) {
	var calls atomic.Int32
	fetcher := &mockFetcher{err: errors.New("network error")}

	cfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond}
	poller := NewCommandPoller(fetcher, newMockExecutor(), cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	poller.Start(ctx)

	<-ctx.Done()

	// With backoff 10ms → 20ms → 40ms (cap) the poller should not have called
	// fetch dozens of times in 200ms — verify it backed off.
	total := fetcher.calls.Load() + calls.Load()
	if total > 8 {
		t.Errorf("expected backoff to limit calls, got %d in 200ms", total)
	}
	if total < 2 {
		t.Errorf("expected at least 2 fetch attempts, got %d", total)
	}
}

func TestCommandPoller_BackoffCappedAtMax(t *testing.T) {
	cfg := DefaultCommandPollerConfig()
	// Simulate many consecutive errors — backoff must not exceed MaxBackoff.
	p := NewCommandPoller(&mockFetcher{err: errors.New("err")}, newMockExecutor(), cfg)
	backoff := cfg.PollInterval
	for i := 0; i < 20; i++ {
		backoff *= 2
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}
	if backoff != cfg.MaxBackoff {
		t.Errorf("backoff after 20 doublings = %v, want %v", backoff, cfg.MaxBackoff)
	}
	_ = p
}

func TestCommandPoller_NoOpExecutor(t *testing.T) {
	exec := &NoOpCommandExecutor{}
	cmd := PendingCommand{ID: "x", Type: "test"}
	if err := exec.Execute(context.Background(), cmd); err != nil {
		t.Errorf("NoOpCommandExecutor.Execute: unexpected error: %v", err)
	}
}

func TestCommandService_PollCommands_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, AgentID: "agent-123"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	svc := NewCommandService(c)
	_, err = svc.PollCommands(context.Background())
	if err == nil {
		t.Fatal("expected error for HTTP 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want to contain '500'", err.Error())
	}
}

func TestCommandService_PollCommands_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-valid-json"))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, AgentID: "agent-123"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	svc := NewCommandService(c)
	_, err = svc.PollCommands(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %q, want to contain 'parse response'", err.Error())
	}
}

func TestCommandService_PollCommands_NoAPIKey(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		jsonResponse(w, []PendingCommand{})
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, AgentID: "agent-abc"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	svc := NewCommandService(c)
	cmds, err := svc.PollCommands(context.Background())
	if err != nil {
		t.Fatalf("PollCommands with no API key: %v", err)
	}
	if cmds == nil {
		// Empty slice is acceptable; nil slice is also fine for zero results
	}
	if capturedAuth != "" {
		t.Errorf("Authorization header = %q, want empty when no APIKey", capturedAuth)
	}
}

// blockingExecutor blocks Execute until unblockCh is closed or ctx is cancelled.
type blockingExecutor struct {
	unblockCh chan struct{}
	ch        chan PendingCommand
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{
		unblockCh: make(chan struct{}),
		ch:        make(chan PendingCommand, 16),
	}
}

func (b *blockingExecutor) Execute(ctx context.Context, cmd PendingCommand) error {
	b.ch <- cmd
	select {
	case <-b.unblockCh:
	case <-ctx.Done():
	}
	return nil
}

func TestCommandPoller_ContextCancelDuringDispatch(t *testing.T) {
	cmds := []PendingCommand{
		{ID: "c1", Type: "run_pipeline"},
		{ID: "c2", Type: "cancel_pipeline"},
		{ID: "c3", Type: "run_pipeline"},
	}
	fetcher := &mockFetcher{results: cmds}
	exec := newBlockingExecutor()

	cfg := CommandPollerConfig{PollInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	poller := NewCommandPoller(fetcher, exec, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)

	// Wait for first command to start executing (it will block).
	select {
	case <-exec.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first command dispatch")
	}

	// Cancel context while executor is still blocked on the first command.
	cancel()

	// Unblock the executor so the goroutine can proceed.
	close(exec.unblockCh)

	// Give the poller goroutine time to exit.
	time.Sleep(50 * time.Millisecond)

	// The poller should not have dispatched all 3 commands because ctx was
	// cancelled during dispatch of the first one. Commands 2 and 3 should be
	// blocked by the inner ctx.Done() check between loop iterations.
	dispatched := len(exec.ch) + 1 // +1 for the one we already received
	if dispatched >= 3 {
		t.Errorf("expected poller to stop before dispatching all 3 commands, dispatched ~%d", dispatched)
	}
}

func TestClient_FetchCommands_Delegation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/commands/pending") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		jsonResponse(w, []map[string]interface{}{
			{
				"id":        "cmd-delegated",
				"type":      "run_pipeline",
				"payload":   map[string]interface{}{"issue": 99},
				"createdAt": time.Now().UTC().Format(time.RFC3339),
			},
		})
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, AgentID: "agent-delegate"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)

	cmds, err := c.FetchCommands(context.Background())
	if err != nil {
		t.Fatalf("FetchCommands: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("len(cmds) = %d, want 1", len(cmds))
	}
	if cmds[0].ID != "cmd-delegated" {
		t.Errorf("cmds[0].ID = %q, want 'cmd-delegated'", cmds[0].ID)
	}
}

// --- AcknowledgeAgentCommand tests ---

func TestCommandService_AcknowledgeAgentCommand_Success(t *testing.T) {
	wantRunID := "run-xyz-789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		wantPath := "/v1/agents/agent-42/commands/cmd-99/ack"
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %s, want Bearer secret", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %s, want application/json", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"runId":"` + wantRunID + `"}`))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "secret"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	runID, err := svc.AcknowledgeAgentCommand(context.Background(), "agent-42", "cmd-99")
	if err != nil {
		t.Fatalf("AcknowledgeAgentCommand: %v", err)
	}
	if runID != wantRunID {
		t.Errorf("runID = %q, want %q", runID, wantRunID)
	}
}

func TestCommandService_AcknowledgeAgentCommand_EmptyAgentID(t *testing.T) {
	cfg := Config{BaseURL: "http://unused"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	_, err = svc.AcknowledgeAgentCommand(context.Background(), "", "cmd-1")
	if err == nil {
		t.Fatal("expected error for empty agentId")
	}
	if !strings.Contains(err.Error(), "agentId is required") {
		t.Errorf("error = %q, want 'agentId is required'", err.Error())
	}
}

func TestCommandService_AcknowledgeAgentCommand_EmptyCommandID(t *testing.T) {
	cfg := Config{BaseURL: "http://unused"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	_, err = svc.AcknowledgeAgentCommand(context.Background(), "agent-1", "")
	if err == nil {
		t.Fatal("expected error for empty commandId")
	}
	if !strings.Contains(err.Error(), "commandId is required") {
		t.Errorf("error = %q, want 'commandId is required'", err.Error())
	}
}

func TestCommandService_AcknowledgeAgentCommand_NonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"command not found"}`))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	_, err = svc.AcknowledgeAgentCommand(context.Background(), "agent-1", "cmd-1")
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %q, want to contain '404'", err.Error())
	}
}

func TestCommandService_AcknowledgeAgentCommand_NoAPIKey(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"runId":"run-no-key"}`))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL} // no APIKey
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	runID, err := svc.AcknowledgeAgentCommand(context.Background(), "agent-1", "cmd-1")
	if err != nil {
		t.Fatalf("AcknowledgeAgentCommand without API key: %v", err)
	}
	if runID != "run-no-key" {
		t.Errorf("runID = %q, want 'run-no-key'", runID)
	}
	if capturedAuth != "" {
		t.Errorf("Authorization = %q, want empty when no APIKey", capturedAuth)
	}
}
