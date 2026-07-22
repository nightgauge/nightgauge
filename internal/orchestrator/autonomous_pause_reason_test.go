// Tests for the #3251 fix: pause-reason persistence + status-change
// notification emission.
//
// The bug: when Go-side autonomous transitioned to paused (haltQueueOnSlotFailure,
// safety trip, etc.) without a TS-driven IPC call updating the badge, the
// VSCode status bar stuck on the wrong state. Fix: every Status transition
// records why+who in state.json AND fires onStatusChange so the IPC server
// can push autonomous.statusChanged to the extension.
package orchestrator

import (
	"sync"
	"testing"
	"time"
)

func TestPauseRecordsReasonAndTriggeredBy(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "running",
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("user requested via UI", "user")

	if as.state.Status != "paused" {
		t.Errorf("Status: want 'paused', got %q", as.state.Status)
	}
	if as.state.PauseReason != "user requested via UI" {
		t.Errorf("PauseReason: want 'user requested via UI', got %q", as.state.PauseReason)
	}
	if as.state.PauseTriggeredBy != "user" {
		t.Errorf("PauseTriggeredBy: want 'user', got %q", as.state.PauseTriggeredBy)
	}
	if as.state.PausedAt == "" {
		t.Error("PausedAt: want non-empty ISO timestamp")
	}
	if _, err := time.Parse(time.RFC3339, as.state.PausedAt); err != nil {
		t.Errorf("PausedAt: want valid RFC3339, got %q (%v)", as.state.PausedAt, err)
	}
}

func TestResumeClearsPauseProvenance(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status:           "paused",
			PauseReason:      "haltQueueOnSlotFailure: #3239 failed at pr-merge",
			PauseTriggeredBy: "haltQueueOnSlotFailure",
			PausedAt:         "2026-05-06T18:38:20Z",
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		conflictRestartCount: map[string]int{},
		refinementCooldown:   map[string]time.Time{},
		refinementFailures:   map[string]int{},
	}

	as.Resume()

	if as.state.Status != "running" {
		t.Errorf("Status: want 'running', got %q", as.state.Status)
	}
	if as.state.PauseReason != "" {
		t.Errorf("PauseReason: want cleared, got %q", as.state.PauseReason)
	}
	if as.state.PauseTriggeredBy != "" {
		t.Errorf("PauseTriggeredBy: want cleared, got %q", as.state.PauseTriggeredBy)
	}
	if as.state.PausedAt != "" {
		t.Errorf("PausedAt: want cleared, got %q", as.state.PausedAt)
	}
}

func TestOnStatusChangeFiresOnPause(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "running",
		},
		rescanCh: make(chan struct{}, 1),
	}

	var (
		mu       sync.Mutex
		received []AutonomousStatusChange
	)
	as.OnStatusChange(func(snap AutonomousStatusChange) {
		mu.Lock()
		received = append(received, snap)
		mu.Unlock()
	})

	as.Pause("manual", "user")

	// Callback fires in a goroutine — give it time to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("want 1 status-change event, got %d", len(received))
	}
	got := received[0]
	if got.Status != "paused" {
		t.Errorf("Status: want 'paused', got %q", got.Status)
	}
	if got.PauseReason != "manual" {
		t.Errorf("PauseReason: want 'manual', got %q", got.PauseReason)
	}
	if got.PauseTriggeredBy != "user" {
		t.Errorf("PauseTriggeredBy: want 'user', got %q", got.PauseTriggeredBy)
	}
}

func TestOnStatusChangeFiresOnResume(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "paused",
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		conflictRestartCount: map[string]int{},
		refinementCooldown:   map[string]time.Time{},
		refinementFailures:   map[string]int{},
	}

	var (
		mu       sync.Mutex
		received []AutonomousStatusChange
	)
	as.OnStatusChange(func(snap AutonomousStatusChange) {
		mu.Lock()
		received = append(received, snap)
		mu.Unlock()
	})

	as.Resume()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("want 1 status-change event on resume, got %d", len(received))
	}
	if received[0].Status != "running" {
		t.Errorf("Status: want 'running', got %q", received[0].Status)
	}
	// Pause provenance must be cleared in the snapshot too.
	if received[0].PauseReason != "" {
		t.Errorf("PauseReason: want empty, got %q", received[0].PauseReason)
	}
}

func TestOnStatusChangeNotFiredWhenStatusUnchanged(t *testing.T) {
	// Pause() is a no-op when status isn't "running"; no event should fire.
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "complete",
		},
		rescanCh: make(chan struct{}, 1),
	}

	var (
		mu       sync.Mutex
		received []AutonomousStatusChange
	)
	as.OnStatusChange(func(snap AutonomousStatusChange) {
		mu.Lock()
		received = append(received, snap)
		mu.Unlock()
	})

	as.Pause("manual", "user")

	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Errorf("expected no events for no-op Pause, got %d: %+v", len(received), received)
	}
}

func TestPauseReasonDefaultsAreFriendly(t *testing.T) {
	// Empty reason/triggeredBy should still persist (the Pause method itself
	// does NOT inject defaults — that's the IPC handler's job per #3251 so
	// that direct Go callers can record their own provenance).
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "running",
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("", "")

	if as.state.PauseReason != "" {
		t.Errorf("PauseReason: want empty, got %q", as.state.PauseReason)
	}
	if as.state.PauseTriggeredBy != "" {
		t.Errorf("PauseTriggeredBy: want empty, got %q", as.state.PauseTriggeredBy)
	}
	if as.state.PausedAt == "" {
		t.Error("PausedAt: want non-empty even when reason/triggeredBy were empty")
	}
}
