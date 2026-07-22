package executor

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestGetCommandHistory_EmptyInitially verifies GetCommandHistory returns an
// empty slice before any commands are dispatched.
func TestGetCommandHistory_EmptyInitially(t *testing.T) {
	e := New(0)
	history := e.GetCommandHistory()
	if len(history) != 0 {
		t.Fatalf("expected empty history, got %d entries", len(history))
	}
}

// TestGetCommandHistory_RecordsSuccessfulExecution verifies that a successful
// command dispatch produces a "success" entry in command history.
func TestGetCommandHistory_RecordsSuccessfulExecution(t *testing.T) {
	e := New(0)
	e.Register(CommandTypePipelineStatus, func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return map[string]string{"status": "idle"}, nil
	})

	cmd := Command{Type: CommandTypePipelineStatus, Payload: json.RawMessage(`{"executionId":"123"}`)}
	_, err := e.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	history := e.GetCommandHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	entry := history[0]
	if entry.Status != "success" {
		t.Errorf("expected status 'success', got %q", entry.Status)
	}
	if entry.Type != string(CommandTypePipelineStatus) {
		t.Errorf("expected type %q, got %q", CommandTypePipelineStatus, entry.Type)
	}
	if entry.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
	if entry.DurationMs < 0 {
		t.Errorf("expected non-negative DurationMs, got %d", entry.DurationMs)
	}
	if entry.ID == "" {
		t.Error("expected non-empty ID")
	}
}

// TestGetCommandHistory_RecordsFailedExecution verifies that an unknown command
// type produces a "failure" entry in command history.
func TestGetCommandHistory_RecordsFailedExecution(t *testing.T) {
	e := New(0)

	cmd := Command{Type: CommandType("unknown.command"), Payload: json.RawMessage(`{}`)}
	result, err := e.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Error == "" {
		t.Error("expected non-empty Error for unknown command")
	}

	history := e.GetCommandHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	if history[0].Status != "failure" {
		t.Errorf("expected status 'failure', got %q", history[0].Status)
	}
	if history[0].Error == "" {
		t.Error("expected non-empty Error field in history entry")
	}
}

// TestGetCommandHistory_CapsAt50 verifies that the history buffer does not
// grow beyond 50 entries (oldest entry is evicted).
func TestGetCommandHistory_CapsAt50(t *testing.T) {
	e := New(0)
	e.Register(CommandTypeConfigReload, func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return map[string]bool{"reloaded": true}, nil
	})

	const overCapacity = 55
	for i := 0; i < overCapacity; i++ {
		_, _ = e.Execute(context.Background(), Command{
			Type:    CommandTypeConfigReload,
			Payload: json.RawMessage(`{}`),
		})
	}

	history := e.GetCommandHistory()
	if len(history) != historyCapacity {
		t.Fatalf("expected %d entries, got %d", historyCapacity, len(history))
	}
}

// TestGetPollingStatus_DefaultInactive verifies GetPollingStatus returns an
// inactive status before any polling state has been set.
func TestGetPollingStatus_DefaultInactive(t *testing.T) {
	e := New(0)
	ps := e.GetPollingStatus()
	if ps.Active {
		t.Error("expected Active=false initially")
	}
	if ps.LastPolledAt != nil {
		t.Error("expected LastPolledAt=nil initially")
	}
	if ps.PendingCount != 0 {
		t.Errorf("expected PendingCount=0, got %d", ps.PendingCount)
	}
	if ps.ErrorCount != 0 {
		t.Errorf("expected ErrorCount=0, got %d", ps.ErrorCount)
	}
}

// TestSetPollingStatus_ReflectsState verifies that SetPollingStatus values are
// returned by GetPollingStatus.
func TestSetPollingStatus_ReflectsState(t *testing.T) {
	e := New(0)
	now := time.Now()
	e.SetPollingStatus(true, &now, 3, 1)

	ps := e.GetPollingStatus()
	if !ps.Active {
		t.Error("expected Active=true")
	}
	if ps.LastPolledAt == nil {
		t.Fatal("expected LastPolledAt to be set")
	}
	if !ps.LastPolledAt.Equal(now) {
		t.Errorf("expected LastPolledAt=%v, got %v", now, *ps.LastPolledAt)
	}
	if ps.PendingCount != 3 {
		t.Errorf("expected PendingCount=3, got %d", ps.PendingCount)
	}
	if ps.ErrorCount != 1 {
		t.Errorf("expected ErrorCount=1, got %d", ps.ErrorCount)
	}
}
