package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/executor"
)

// callMethod is a test helper that dispatches a method via the IPC server and
// unmarshals the result into v.
func callMethod(t *testing.T, s *Server, method string, params interface{}, v interface{}) {
	t.Helper()
	raw, _ := json.Marshal(params)
	handler, ok := s.methods[method]
	if !ok {
		t.Fatalf("method %q not registered", method)
	}
	result, err := handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s handler error: %v", method, err)
	}
	// Round-trip through JSON to fill v
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal into target: %v", err)
	}
}

// TestRemoteGetCommandHistory_NilExecutor verifies that the handler returns an
// empty commands slice when no commandExecutor is configured.
func TestRemoteGetCommandHistory_NilExecutor(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewServer(nil)
	s.writer = buf

	var result RemoteGetCommandHistoryResult
	callMethod(t, s, "remote.getCommandHistory", RemoteGetCommandHistoryParams{}, &result)

	if result.Commands == nil {
		t.Error("expected non-nil Commands slice")
	}
	if len(result.Commands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(result.Commands))
	}
}

// TestRemoteGetPollingStatus_NilExecutor verifies that the handler returns
// Active=false when no commandExecutor is configured.
func TestRemoteGetPollingStatus_NilExecutor(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewServer(nil)
	s.writer = buf

	var result RemotePollingStatus
	callMethod(t, s, "remote.getPollingStatus", RemoteGetPollingStatusParams{}, &result)

	if result.Active {
		t.Error("expected Active=false when commandExecutor is nil")
	}
}

// TestRemoteGetCommandHistory_WithExecutor verifies that history entries from
// the executor are surfaced via the IPC handler with correct field mapping.
func TestRemoteGetCommandHistory_WithExecutor(t *testing.T) {
	e := executor.New(0)
	e.Register(executor.CommandTypePipelineStatus, func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return map[string]string{"status": "idle"}, nil
	})
	_, _ = e.Execute(context.Background(), executor.Command{
		Type:    executor.CommandTypePipelineStatus,
		Payload: json.RawMessage(`{"executionId":"42"}`),
	})

	buf := &bytes.Buffer{}
	s := NewServer(nil, WithCommandExecutor(e))
	s.writer = buf

	var result RemoteGetCommandHistoryResult
	callMethod(t, s, "remote.getCommandHistory", RemoteGetCommandHistoryParams{}, &result)

	if len(result.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(result.Commands))
	}
	cmd := result.Commands[0]
	if cmd.Type != string(executor.CommandTypePipelineStatus) {
		t.Errorf("expected type %q, got %q", executor.CommandTypePipelineStatus, cmd.Type)
	}
	if cmd.Status != "success" {
		t.Errorf("expected status 'success', got %q", cmd.Status)
	}
	if cmd.ID == "" {
		t.Error("expected non-empty ID")
	}
	if cmd.ReceivedAt == "" {
		t.Error("expected non-empty ReceivedAt")
	}
	if cmd.CompletedAt == nil {
		t.Error("expected non-nil CompletedAt")
	}
}

// TestRemoteGetPollingStatus_WithExecutor verifies that polling state set on
// the executor is returned correctly via the IPC handler.
func TestRemoteGetPollingStatus_WithExecutor(t *testing.T) {
	e := executor.New(0)
	now := time.Now().UTC()
	e.SetPollingStatus(true, &now, 2, 0)

	buf := &bytes.Buffer{}
	s := NewServer(nil, WithCommandExecutor(e))
	s.writer = buf

	var result RemotePollingStatus
	callMethod(t, s, "remote.getPollingStatus", RemoteGetPollingStatusParams{}, &result)

	if !result.Active {
		t.Error("expected Active=true")
	}
	if result.PendingCount != 2 {
		t.Errorf("expected PendingCount=2, got %d", result.PendingCount)
	}
	if result.LastPolledAt == nil {
		t.Fatal("expected LastPolledAt to be set")
	}
	// Validate RFC3339 format
	parsed, err := time.Parse(time.RFC3339, *result.LastPolledAt)
	if err != nil {
		t.Fatalf("LastPolledAt not RFC3339: %v", err)
	}
	if !parsed.Equal(now.Truncate(time.Second)) {
		// Allow 1-second truncation from RFC3339 format
		diff := parsed.Sub(now)
		if diff < -time.Second || diff > time.Second {
			t.Errorf("LastPolledAt %v differs from expected %v by more than 1s", parsed, now)
		}
	}
	_ = parsed
}
