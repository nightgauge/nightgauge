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
		retryBackoff:         map[string]retryPlan{},
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
		retryBackoff:         map[string]retryPlan{},
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

// TestHaltedOnSlotFailure covers #148: the "Fleet idle — N promotable" card
// must be suppressed ONLY for a haltQueueOnSlotFailure pause — a queue halted
// on a real failure is not the same fact as an empty queue. Every other pause
// (user-requested, a safety-rail trip, etc.) still gets the honest fleet-idle
// card if the queue also happens to be empty; the guard must not swallow every
// pause, only this specific one.
//
// #405 renamed this predicate from shouldSuppressFleetIdle when the identical
// conjunct in reconcileTerminalFailureCards was folded into it: the fleet-idle
// suppression and the standing terminal-failure cards are two consequences of
// one fact, and keeping two copies is what let a single state rewrite break
// both. The fixup then moved it off Status entirely and onto the halt LATCH —
// the rows below are the same behavior expressed against the fact that
// actually survives an exit path.
func TestHaltedOnSlotFailure(t *testing.T) {
	slotHalt := &MachineHaltRecord{Tag: haltTagSlotFailure, Status: "paused"}
	cases := []struct {
		name            string
		state           *AutonomousState
		wantSlotFailure bool
		wantHalted      bool
	}{
		{
			"latched slot-failure halt",
			&AutonomousState{Status: "paused", PauseTriggeredBy: haltTagSlotFailure, MachineHalt: slotHalt},
			true, true,
		},
		{
			// The whole point of the latch: complete() overwrote Status on the
			// way out, and the halt is still in force.
			"latched slot-failure halt after a shutdown rewrote Status",
			&AutonomousState{Status: "cancelled", MachineHalt: slotHalt},
			true, true,
		},
		{
			// Provenance is not authority. A tag with no latch is a leftover,
			// not a halt — the latch is the single writer of this fact.
			"paused with the tag but no latch",
			&AutonomousState{Status: "paused", PauseTriggeredBy: haltTagSlotFailure},
			false, false,
		},
		{"operator pause", &AutonomousState{Status: "paused", PauseTriggeredBy: "user"}, false, false},
		{"paused with empty triggeredBy", &AutonomousState{Status: "paused"}, false, false},
		{
			// Both safety triggers latch (so a shutdown cannot launder them)
			// but neither is a slot-failure halt: a safety-tripped fleet with
			// an empty queue still gets the honest "nothing to do" card.
			"cascade trip is halted but not a slot-failure halt",
			&AutonomousState{Status: "safety_tripped", MachineHalt: &MachineHaltRecord{Tag: CascadePauseReason, Status: "safety_tripped"}},
			false, true,
		},
		{
			"rail-check trip is halted but not a slot-failure halt",
			&AutonomousState{Status: "safety_tripped", MachineHalt: &MachineHaltRecord{Tag: "safety:rail-check", Status: "safety_tripped"}},
			false, true,
		},
		{"nil state", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := haltedOnSlotFailure(tc.state); got != tc.wantSlotFailure {
				t.Errorf("haltedOnSlotFailure() = %v, want %v", got, tc.wantSlotFailure)
			}
			if got := machineHalted(tc.state); got != tc.wantHalted {
				t.Errorf("machineHalted() = %v, want %v", got, tc.wantHalted)
			}
		})
	}
}

// TestPauseLatchesOnlyMachineRaisedHalts pins the write side of the latch:
// Pause() latches the machine's own halt and nothing else. An operator's
// pause must stay a plain pause — Start resumes it exactly as it always has,
// and no restart resurrects it.
func TestPauseLatchesOnlyMachineRaisedHalts(t *testing.T) {
	cases := []struct {
		name      string
		trigger   string
		wantLatch bool
	}{
		{"machine halt latches", haltTagSlotFailure, true},
		{"operator pause does not latch", "user", false},
		{"untagged pause does not latch", "", false},
		// Self-clearing breakers: the extension detects their recovery and
		// auto-resumes (utils/autonomousAutoResume.ts). Latching them would
		// park a transient outage behind a human who was never needed.
		{"rate-limit breaker does not latch", "rate-limit-circuit-breaker", false},
		{"network-outage breaker does not latch", "network-outage-circuit-breaker", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := &AutonomousScheduler{
				state:    &AutonomousState{Status: "running"},
				rescanCh: make(chan struct{}, 1),
			}
			as.Pause("reason", tc.trigger)
			if as.state.Status != "paused" {
				t.Fatalf("status = %q, want paused", as.state.Status)
			}
			if got := machineHalted(as.state); got != tc.wantLatch {
				t.Errorf("machineHalted() = %v, want %v (trigger %q)", got, tc.wantLatch, tc.trigger)
			}
			if tc.wantLatch {
				if as.state.MachineHalt.Tag != tc.trigger {
					t.Errorf("latch tag = %q, want %q", as.state.MachineHalt.Tag, tc.trigger)
				}
				if as.state.MachineHalt.Status != "paused" {
					t.Errorf("latch status = %q, want paused", as.state.MachineHalt.Status)
				}
			}
		})
	}
}

// TestResumeIsTheOnlyLatchClearer: the latch and its provenance are released
// together, by Resume, from any status a shutdown might have left behind.
func TestResumeIsTheOnlyLatchClearer(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			// Stop() ran after the halt: the exit status is "stopped", the
			// halt is still in force. Resume must still act.
			Status:           "stopped",
			PauseReason:      "haltQueueOnSlotFailure: #405 failed at feature-validate",
			PauseTriggeredBy: haltTagSlotFailure,
			PausedAt:         "2026-08-09T00:00:00Z",
			MachineHalt:      &MachineHaltRecord{Tag: haltTagSlotFailure, Status: "paused"},
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Resume()

	if as.state.Status != "running" {
		t.Errorf("status = %q, want running — Resume on a latched halt must act whatever exit status is on the state", as.state.Status)
	}
	if as.state.MachineHalt != nil {
		t.Error("latch survived Resume — Resume is the one clearer")
	}
	if as.state.PauseReason != "" || as.state.PauseTriggeredBy != "" || as.state.PausedAt != "" {
		t.Errorf("provenance survived Resume: reason=%q trigger=%q at=%q — the latch and its provenance clear together",
			as.state.PauseReason, as.state.PauseTriggeredBy, as.state.PausedAt)
	}
}
