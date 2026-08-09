package orchestrator

// Restart must not launder a machine-raised halt (#405).
//
// The defect: haltQueueOnSlotFailure pauses the whole fleet and raises a
// blocking_fleet terminal-failure card that a human must answer. A crash then
// erases that human gate through three separate rewrites, none of which
// triaged anything:
//
//  1. loadState() downgraded paused -> stopped, so Resume() (which acts only on
//     paused|safety_tripped) no-ops and the ONLY way back is Start;
//  2. Run() unconditionally wiped PauseReason/PauseTriggeredBy/PausedAt, so the
//     halt predicate that keeps the cards standing reads "not halted";
//  3. the first cycle then retracts every open terminal-failure card AND raises
//     the misleading "Fleet idle - N promotable" card #148 exists to suppress.
//
// The operator clicked Start after a crash. That is not triage. These tests pin
// the safety_tripped precedent for the paused+machine-trigger case: a restart
// resolves nothing, so the halt is preserved and the fleet comes back
// alive-but-halted until a human explicitly Resumes.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// newRestartHaltScheduler builds a scheduler rooted at a CALLER-OWNED
// workspace root (unlike newAttentionProducerScheduler, which mints its own
// t.TempDir()). Passing the same root twice is the whole point: both
// `.nightgauge/autonomous/state.json` and `.nightgauge/attention/` live under
// it, so a second construction over the same root is a faithful process
// restart — the constructor's loadState() reads the state the dead process
// left behind and the attention store re-reads the cards it persisted.
func newRestartHaltScheduler(t *testing.T, root string) *AutonomousScheduler {
	t.Helper()
	cfg := DefaultAutonomousConfig()
	// Refinement spawns a second goroutine we neither need nor want to race
	// against; the dispatch loop is what is under test.
	cfg.RefinementEnabled = false
	// A real (if empty) inner Scheduler: Run() wires its pipeline-complete
	// callback unconditionally, so a nil one panics before the first cycle.
	as := NewAutonomousScheduler(&Scheduler{}, nil, nil, nil, cfg, root)
	if as.Attention() == nil {
		t.Fatal("attention store not wired by NewAutonomousScheduler")
	}
	return as
}

// stubGraphFn keeps the cycle off the network. An empty graph yields zero
// candidates, which is the shape that matters here: it is exactly the
// "remaining==0 && running==0" idle cycle that both producers key off.
func stubGraphFn(as *AutonomousScheduler) {
	as.buildGraphFn = func(context.Context) (*depgraph.Graph, error) {
		return buildTestGraph(nil, nil), nil
	}
}

// cyclesRun reports state.CyclesRun, incremented inside runCycle immediately
// past the `Status != "running"` early return. Zero is positive evidence,
// observed through production control flow, that no cycle ever ran — the graph
// build is NOT usable for this, because Run()'s startup Backlog->Ready
// promotion builds one before the dispatch loop regardless of status.
func cyclesRun(as *AutonomousScheduler) int {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.state.CyclesRun
}

// haltFleet puts the scheduler in the exact state haltQueueOnSlotFailure
// produces: paused by the machine, with the blocking_fleet card that names why.
func haltFleet(t *testing.T, as *AutonomousScheduler, repo string, issue int) {
	t.Helper()
	as.mu.Lock()
	as.state.Status = "running"
	as.mu.Unlock()
	as.Pause("haltQueueOnSlotFailure: issue #405 failed at feature-validate", "haltQueueOnSlotFailure")
	as.RaiseTerminalFailure(repo, issue, "feature-validate", "gate_failure", 4.2)
	if got := countProducer(t, as, producerTerminalFailure); got != 1 {
		t.Fatalf("setup: %d %q card(s), want 1", got, producerTerminalFailure)
	}
}

func countProducer(t *testing.T, as *AutonomousScheduler, producer string) int {
	t.Helper()
	n := 0
	for _, r := range openRequests(t, as) {
		if r.Producer == producer {
			n++
		}
	}
	return n
}

// runToCompletion drives Run() with an already-cancelled context. Run's
// prologue, the orphan recovery, and the initial scan all execute before the
// select loop observes ctx.Done(), so by the time Run returns the first cycle
// is definitively over — no sleeps, no polling, no flake in either direction.
// The trailing complete("cancelled") overwrites Status, so assertions after
// this call must be about durable facts (cards, pause provenance, cycle count),
// not the live status. TestRunOnPreservedHaltStaysDormant covers the status.
func runToCompletion(t *testing.T, as *AutonomousScheduler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		_ = as.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not return within timeout")
	}
}

// TestRestartDoesNotLaunderMachineHalt is the end-to-end repro: halt the fleet
// with an open card, crash, restart, click Start, and the human gate must still
// be standing on the other side.
func TestRestartDoesNotLaunderMachineHalt(t *testing.T) {
	root := t.TempDir()

	// --- session 1: a terminal stage failure halts the whole fleet ---------
	as1 := newRestartHaltScheduler(t, root)
	haltFleet(t, as1, "octocat/acme", 405)

	// --- the crash: the process dies; state.json and attention/ persist ----

	// --- session 2: a fresh scheduler over the same workspace root ---------
	as2 := newRestartHaltScheduler(t, root)
	stubGraphFn(as2)

	as2.mu.Lock()
	loadedStatus, loadedTrigger, restarted := as2.state.Status, as2.state.PauseTriggeredBy, as2.state.RestartedFromRunning
	as2.mu.Unlock()
	if loadedStatus != "paused" {
		t.Errorf("status after restart = %q, want %q — a machine-raised halt is a human-must-triage state, and a restart resolves nothing (the safety_tripped precedent)", loadedStatus, "paused")
	}
	if loadedTrigger != "haltQueueOnSlotFailure" {
		t.Errorf("pauseTriggeredBy after restart = %q, want %q", loadedTrigger, "haltQueueOnSlotFailure")
	}
	if !restarted {
		t.Error("restartedFromRunning = false after an ungraceful exit — preservation must not cost the #274 signal that the process died mid-flight")
	}

	// --- the operator clicks Start ----------------------------------------
	// Exactly what internal/ipc/server.go's autonomous.start handler does: the
	// resume step, then Run(). Start must NOT clear a machine-raised halt —
	// clicking Start after a crash triages nothing.
	if as2.ResumeUnlessMachineHalted() {
		t.Error("Start resumed a machine-raised halt — the human gate was cleared by a click that answered no card")
	}
	runToCompletion(t, as2)

	// --- the gate must still be standing ----------------------------------
	if got := countProducer(t, as2, producerTerminalFailure); got != 1 {
		t.Errorf("%d %q card(s) after restart+Start, want 1 — the halt was laundered into 'nothing is wrong' by a state rewrite", got, producerTerminalFailure)
	}
	as2.mu.Lock()
	trigger, reason, pausedAt := as2.state.PauseTriggeredBy, as2.state.PauseReason, as2.state.PausedAt
	as2.mu.Unlock()
	if trigger != "haltQueueOnSlotFailure" {
		t.Errorf("pauseTriggeredBy after Run() = %q, want %q — Run() wiped the provenance the halt predicate reads", trigger, "haltQueueOnSlotFailure")
	}
	if reason == "" || pausedAt == "" {
		t.Errorf("pauseReason=%q pausedAt=%q — the whole provenance record must survive, not just the tag", reason, pausedAt)
	}
	if n := cyclesRun(as2); n != 0 {
		t.Errorf("CyclesRun = %d, want 0 — a cycle got past runCycle's 'Status != running' early return, so the fleet was not halted", n)
	}
}

// TestRestartDoesNotResurrectFleetIdleCard pins the #148 regression the same
// laundering causes on the OTHER producer. "Fleet idle - N promotable" and "the
// fleet halted itself" both satisfy remaining==0 && running==0, and the idle
// card actively recommends the wrong action. Erasing the pause provenance makes
// the suppression guard read false, so the misleading card comes back one cycle
// after the restart.
func TestRestartDoesNotResurrectFleetIdleCard(t *testing.T) {
	root := t.TempDir()

	as1 := newRestartHaltScheduler(t, root)
	haltFleet(t, as1, "octocat/acme", 406)

	as2 := newRestartHaltScheduler(t, root)
	stubGraphFn(as2)
	as2.mu.Lock()
	as2.state.LastPromotionEligible = 3
	as2.mu.Unlock()

	as2.ResumeUnlessMachineHalted()
	runToCompletion(t, as2)

	if got := countProducer(t, as2, producerWorkExhaustion); got != 0 {
		t.Errorf("%d %q card(s) after restart+Start, want 0 — the fleet is halted, not out of work, and 'go promote something' is the wrong action", got, producerWorkExhaustion)
	}
}

// TestResumeAfterRestartClearsPreservedHalt is the counterweight: preserving
// the halt must not make it unclearable. Resume() is the explicit human action
// — it clears the halt, and the next cycle retracts the cards. This is the
// designed #148 flow, now reachable only by answering the card rather than by
// crashing.
func TestResumeAfterRestartClearsPreservedHalt(t *testing.T) {
	root := t.TempDir()

	as1 := newRestartHaltScheduler(t, root)
	haltFleet(t, as1, "octocat/acme", 407)

	as2 := newRestartHaltScheduler(t, root)
	stubGraphFn(as2)

	// The human triaged and resumed. Resume() acts on "paused", which is
	// exactly what preservation left on disk — the #405 fix also restores the
	// direct Resume path that the paused->stopped downgrade had removed.
	as2.Resume()
	as2.mu.Lock()
	status, trigger := as2.state.Status, as2.state.PauseTriggeredBy
	as2.mu.Unlock()
	if status != "running" {
		t.Fatalf("status after Resume() = %q, want %q", status, "running")
	}
	if trigger != "" {
		t.Errorf("pauseTriggeredBy after Resume() = %q, want empty — Resume is what clears the halt", trigger)
	}

	runToCompletion(t, as2)

	if got := countProducer(t, as2, producerTerminalFailure); got != 0 {
		t.Errorf("%d %q card(s) after an explicit Resume, want 0 — the card must clear when the human actually answers it", got, producerTerminalFailure)
	}
	if n := cyclesRun(as2); n == 0 {
		t.Error("CyclesRun = 0 after Resume — the fleet must actually go back to work")
	}
}

// TestRunOnPreservedHaltStaysDormant asserts the live status while Run is in
// flight (runToCompletion's trailing complete("cancelled") hides it). Run()
// enters its loop with the halt still in force: alive, ticking, dispatching
// nothing — exactly the state the crash interrupted.
func TestRunOnPreservedHaltStaysDormant(t *testing.T) {
	root := t.TempDir()

	as1 := newRestartHaltScheduler(t, root)
	haltFleet(t, as1, "octocat/acme", 408)

	as2 := newRestartHaltScheduler(t, root)
	stubGraphFn(as2)
	as2.ResumeUnlessMachineHalted()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = as2.Run(ctx)
		close(done)
	}()

	// Run sets as.running=true in the same critical section that decides the
	// status, so IsRunning()==true means the prologue has committed and the
	// snapshot below is reading the post-prologue state.
	deadline := time.Now().Add(10 * time.Second)
	for !as2.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("Run() never marked itself running")
		}
		time.Sleep(time.Millisecond)
	}

	snap := as2.Status()
	if snap.Status != "paused" {
		t.Errorf("status inside Run() = %q, want %q — Run() forced a halted fleet back to running", snap.Status, "paused")
	}
	if snap.PauseTriggeredBy != "haltQueueOnSlotFailure" {
		t.Errorf("pauseTriggeredBy inside Run() = %q, want %q", snap.PauseTriggeredBy, "haltQueueOnSlotFailure")
	}
	if snap.RestartedFromRunning {
		t.Error("RestartedFromRunning survived Run() — its job ends once a Run begins (#274)")
	}
	if snap.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d — the halted fleet is a live process and must still claim the state file", snap.PID, os.Getpid())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}

	if n := cyclesRun(as2); n != 0 {
		t.Errorf("CyclesRun = %d while halted, want 0 — dormant means dormant", n)
	}
}

// writePriorState persists a state file as a dead process would have left it.
func writePriorState(t *testing.T, root string, prior AutonomousState) {
	t.Helper()
	p := filepath.Join(root, autonomousStateFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(prior)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readPersistedState(t *testing.T, root string) AutonomousState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, autonomousStateFile))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var s AutonomousState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return s
}

// TestLoadStatePreservationIsScopedToMachineRaisedHalts keeps preservation
// narrow. An operator's pause carries no machine trigger, and nothing about it
// is a human gate the restart could erase — it still downgrades to stopped so
// Start behaves exactly as it always has.
func TestLoadStatePreservationIsScopedToMachineRaisedHalts(t *testing.T) {
	cases := []struct {
		name           string
		prior          AutonomousState
		wantStatus     string
		wantTrigger    string
		wantRestartFlg bool
	}{
		{
			name:           "operator pause still downgrades",
			prior:          AutonomousState{Status: "paused", PauseReason: "user requested via UI", PauseTriggeredBy: "user"},
			wantStatus:     "stopped",
			wantTrigger:    "user",
			wantRestartFlg: true,
		},
		{
			name:           "pause with no trigger recorded still downgrades",
			prior:          AutonomousState{Status: "paused", PauseReason: "no reason provided"},
			wantStatus:     "stopped",
			wantRestartFlg: true,
		},
		{
			name:           "running still downgrades",
			prior:          AutonomousState{Status: "running"},
			wantStatus:     "stopped",
			wantRestartFlg: true,
		},
		{
			// A stale trigger tag on a non-paused status is not a halt. Only
			// the conjunction is.
			name:           "running with a stale halt tag still downgrades",
			prior:          AutonomousState{Status: "running", PauseTriggeredBy: "haltQueueOnSlotFailure"},
			wantStatus:     "stopped",
			wantTrigger:    "haltQueueOnSlotFailure",
			wantRestartFlg: true,
		},
		{
			name:           "machine-raised halt is preserved",
			prior:          AutonomousState{Status: "paused", PauseReason: "haltQueueOnSlotFailure: #405 failed", PauseTriggeredBy: "haltQueueOnSlotFailure"},
			wantStatus:     "paused",
			wantTrigger:    "haltQueueOnSlotFailure",
			wantRestartFlg: true,
		},
		{
			// The sibling precedent this fix generalizes — pinned so a
			// refactor of the preservation branch cannot regress it.
			name:           "safety_tripped preserved as before",
			prior:          AutonomousState{Status: "safety_tripped", PauseTriggeredBy: CascadePauseReason},
			wantStatus:     "safety_tripped",
			wantTrigger:    CascadePauseReason,
			wantRestartFlg: false,
		},
		{
			name:           "a clean stop is not a restart recovery",
			prior:          AutonomousState{Status: "stopped"},
			wantStatus:     "stopped",
			wantRestartFlg: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writePriorState(t, root, tc.prior)

			as := &AutonomousScheduler{workspaceRoot: root, state: &AutonomousState{}}
			as.loadState()

			if as.state.Status != tc.wantStatus {
				t.Errorf("in-memory status = %q, want %q", as.state.Status, tc.wantStatus)
			}
			if as.state.PauseTriggeredBy != tc.wantTrigger {
				t.Errorf("in-memory pauseTriggeredBy = %q, want %q", as.state.PauseTriggeredBy, tc.wantTrigger)
			}
			if as.state.RestartedFromRunning != tc.wantRestartFlg {
				t.Errorf("in-memory restartedFromRunning = %v, want %v", as.state.RestartedFromRunning, tc.wantRestartFlg)
			}

			// #274: whatever loadState decided must reach disk, or a second
			// crash before the next scan replays the same stale claim.
			if tc.prior.Status == "running" || tc.prior.Status == "paused" {
				onDisk := readPersistedState(t, root)
				if onDisk.Status != tc.wantStatus {
					t.Errorf("on-disk status = %q, want %q — the load-time reconcile was not flushed", onDisk.Status, tc.wantStatus)
				}
				if onDisk.RestartedFromRunning != tc.wantRestartFlg {
					t.Errorf("on-disk restartedFromRunning = %v, want %v", onDisk.RestartedFromRunning, tc.wantRestartFlg)
				}
			}
		})
	}
}

// TestStartGateIsScopedToMachineRaisedHalts pins the other side of design D:
// Start still resumes everything that is not a machine-raised halt, so the
// operator's Start button is unchanged for every state it used to act on.
func TestStartGateIsScopedToMachineRaisedHalts(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		trigger     string
		wantResumed bool
		wantStatus  string
	}{
		{"operator pause resumes", "paused", "user", true, "running"},
		{"safety trip resumes and resets rails", "safety_tripped", CascadePauseReason, true, "running"},
		{"machine-raised halt is left in force", "paused", "haltQueueOnSlotFailure", false, "paused"},
		{"stopped is a no-op resume", "stopped", "", true, "stopped"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := newRestartHaltScheduler(t, t.TempDir())
			as.mu.Lock()
			as.state.Status = tc.status
			as.state.PauseTriggeredBy = tc.trigger
			as.mu.Unlock()

			if got := as.ResumeUnlessMachineHalted(); got != tc.wantResumed {
				t.Errorf("ResumeUnlessMachineHalted() = %v, want %v", got, tc.wantResumed)
			}
			as.mu.Lock()
			defer as.mu.Unlock()
			if as.state.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", as.state.Status, tc.wantStatus)
			}
		})
	}
}
