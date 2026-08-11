package orchestrator

// No exit path may launder a machine-raised halt (#405).
//
// The defect: haltQueueOnSlotFailure pauses the whole fleet and raises a
// blocking_fleet terminal-failure card that a human must answer. Ending the
// process then erased that human gate through three separate rewrites, none of
// which triaged anything:
//
//  1. loadState() downgraded paused -> stopped, so Resume() (which acts only on
//     paused|safety_tripped) no-ops and the ONLY way back is Start;
//  2. Run() unconditionally wiped PauseReason/PauseTriggeredBy/PausedAt, so the
//     halt predicate that keeps the cards standing reads "not halted";
//  3. the first cycle then retracts every open terminal-failure card AND raises
//     the misleading "Fleet idle - N promotable" card #148 exists to suppress.
//
// The operator clicked Start after a crash. That is not triage.
//
// The halt is therefore a LATCH — its own persisted fact (AutonomousState.
// MachineHalt), written where the halt is raised and cleared only by Resume.
// Keying it on Status could never work: complete() writes Status
// unconditionally, so SIGTERM persisted "cancelled" over a halted fleet and
// Stop() persisted "stopped", and only a SIGKILL — the one exit that writes
// nothing — left the halt legible. loadState and Run() restore Status FROM the
// latch, so the fleet comes back alive-but-halted however the last process
// ended.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	t.Cleanup(as.drainBackground) // backstop; see newAutonomousForCascadeTest
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
	// The scheduler-level half of Start: its resume step, then Run(). This is
	// a hand-rolled sequence, NOT the handler — nothing here would notice the
	// `autonomous.start` handler reverting to a bare Resume(). The registered
	// method is pinned in internal/ipc/autonomous_halt_wire_test.go; what this
	// test owns is the scheduler's own behavior once Start declines to resume.
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
// narrow, and pins what it is keyed on. An operator's pause raises no latch,
// and nothing about it is a human gate the restart could erase — it still
// downgrades to stopped so Start behaves exactly as it always has. A latched
// halt is restored to its own status from WHATEVER the exit wrote, which is
// the property Status alone could never provide (every exit path overwrites
// it).
func TestLoadStatePreservationIsScopedToMachineRaisedHalts(t *testing.T) {
	slotHalt := &MachineHaltRecord{Tag: haltTagSlotFailure, Status: "paused"}
	cascadeHalt := &MachineHaltRecord{Tag: CascadePauseReason, Status: "safety_tripped"}

	cases := []struct {
		name           string
		prior          AutonomousState
		wantStatus     string
		wantTrigger    string
		wantRestartFlg bool
		wantFlushed    bool
	}{
		{
			name:           "operator pause still downgrades",
			prior:          AutonomousState{Status: "paused", PauseReason: "user requested via UI", PauseTriggeredBy: "user"},
			wantStatus:     "stopped",
			wantTrigger:    "user",
			wantRestartFlg: true,
			wantFlushed:    true,
		},
		{
			name:           "pause with no trigger recorded still downgrades",
			prior:          AutonomousState{Status: "paused", PauseReason: "no reason provided"},
			wantStatus:     "stopped",
			wantRestartFlg: true,
			wantFlushed:    true,
		},
		{
			name:           "running still downgrades",
			prior:          AutonomousState{Status: "running"},
			wantStatus:     "stopped",
			wantRestartFlg: true,
			wantFlushed:    true,
		},
		{
			// Provenance is not authority: a tag with no latch is a leftover.
			name:           "paused with a halt tag but no latch still downgrades",
			prior:          AutonomousState{Status: "paused", PauseTriggeredBy: haltTagSlotFailure},
			wantStatus:     "stopped",
			wantTrigger:    haltTagSlotFailure,
			wantRestartFlg: true,
			wantFlushed:    true,
		},
		{
			// The hard crash: the raw "paused" is still on disk. The
			// downgrade runs, the latch restores over it, and the #274 signal
			// that the process died mid-flight survives both.
			name:           "latched halt survives an ungraceful exit",
			prior:          AutonomousState{Status: "paused", PauseReason: "haltQueueOnSlotFailure: #405 failed", PauseTriggeredBy: haltTagSlotFailure, MachineHalt: slotHalt},
			wantStatus:     "paused",
			wantTrigger:    haltTagSlotFailure,
			wantRestartFlg: true,
			wantFlushed:    true,
		},
		{
			// The graceful shutdown the latch exists for: complete("cancelled")
			// wrote the exit status over a halted fleet on SIGTERM. Nothing
			// about that is an ungraceful exit, so RestartedFromRunning stays
			// false — and the halt still comes back.
			name:           "latched halt survives a graceful shutdown",
			prior:          AutonomousState{Status: "cancelled", PauseReason: "haltQueueOnSlotFailure: #405 failed", PauseTriggeredBy: haltTagSlotFailure, MachineHalt: slotHalt},
			wantStatus:     "paused",
			wantTrigger:    haltTagSlotFailure,
			wantRestartFlg: false,
			wantFlushed:    true,
		},
		{
			// Stop() on a halted fleet: same laundering, different verb.
			name:           "latched halt survives Stop",
			prior:          AutonomousState{Status: "stopped", PauseTriggeredBy: haltTagSlotFailure, MachineHalt: slotHalt},
			wantStatus:     "paused",
			wantTrigger:    haltTagSlotFailure,
			wantRestartFlg: false,
			wantFlushed:    true,
		},
		{
			// The safety trips latch too — a graceful shutdown used to write
			// "cancelled" over safety_tripped and lose the trip entirely.
			name:           "latched safety trip survives a graceful shutdown",
			prior:          AutonomousState{Status: "cancelled", PauseTriggeredBy: CascadePauseReason, MachineHalt: cascadeHalt},
			wantStatus:     "safety_tripped",
			wantTrigger:    CascadePauseReason,
			wantRestartFlg: false,
			wantFlushed:    true,
		},
		{
			// The sibling precedent this fix generalizes — pinned so a
			// refactor of the restore branch cannot regress it. No latch, no
			// rewrite, no flush.
			name:           "safety_tripped preserved as before",
			prior:          AutonomousState{Status: "safety_tripped", PauseTriggeredBy: CascadePauseReason},
			wantStatus:     "safety_tripped",
			wantTrigger:    CascadePauseReason,
			wantRestartFlg: false,
		},
		{
			name:       "a clean stop is not a restart recovery",
			prior:      AutonomousState{Status: "stopped"},
			wantStatus: "stopped",
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
			// The latch itself must never be consumed by the load — only
			// Resume clears it, so a second crash finds it again.
			if (as.state.MachineHalt != nil) != (tc.prior.MachineHalt != nil) {
				t.Errorf("latch presence changed across load: %v -> %v", tc.prior.MachineHalt != nil, as.state.MachineHalt != nil)
			}

			// #274: whatever loadState decided must reach disk, or a second
			// crash before the next scan replays the same stale claim.
			if tc.wantFlushed {
				onDisk := readPersistedState(t, root)
				if onDisk.Status != tc.wantStatus {
					t.Errorf("on-disk status = %q, want %q — the load-time reconcile was not flushed", onDisk.Status, tc.wantStatus)
				}
				if onDisk.RestartedFromRunning != tc.wantRestartFlg {
					t.Errorf("on-disk restartedFromRunning = %v, want %v", onDisk.RestartedFromRunning, tc.wantRestartFlg)
				}
				if (onDisk.MachineHalt != nil) != (tc.prior.MachineHalt != nil) {
					t.Error("the flushed state lost the latch — a second crash would come back un-halted")
				}
			}
		})
	}
}

// TestStartGateIsScopedToMachineRaisedHalts pins the other side of design D:
// Start still resumes everything that is not a latched machine halt, so the
// operator's Start button is unchanged for every state it used to act on —
// and it declines every halt shape, because it reads the latch rather than
// pattern-matching a status string.
func TestStartGateIsScopedToMachineRaisedHalts(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		trigger     string
		latch       *MachineHaltRecord
		wantResumed bool
		wantStatus  string
	}{
		{name: "operator pause resumes", status: "paused", trigger: "user", wantResumed: true, wantStatus: "running"},
		{
			// No latch: whatever put this status here, it is not a halt the
			// scheduler raised, and Start behaves as it always has.
			name: "unlatched safety_tripped resumes and resets rails", status: "safety_tripped",
			trigger: CascadePauseReason, wantResumed: true, wantStatus: "running",
		},
		{
			name: "latched slot-failure halt is left in force", status: "paused", trigger: haltTagSlotFailure,
			latch:      &MachineHaltRecord{Tag: haltTagSlotFailure, Status: "paused"},
			wantStatus: "paused",
		},
		{
			// A real cascade trip latches, so Start declines it too: the
			// cascade card asks for a decision, and Start answers nothing.
			name: "latched cascade trip is left in force", status: "safety_tripped", trigger: CascadePauseReason,
			latch:      &MachineHaltRecord{Tag: CascadePauseReason, Status: "safety_tripped"},
			wantStatus: "safety_tripped",
		},
		{
			// The laundering shape: the exit wrote "stopped" over the halt.
			// Start must still decline — and Run() restores the status.
			name: "latched halt under an exit status is left in force", status: "stopped", trigger: haltTagSlotFailure,
			latch:      &MachineHaltRecord{Tag: haltTagSlotFailure, Status: "paused"},
			wantStatus: "stopped",
		},
		{name: "stopped is a no-op resume", status: "stopped", wantResumed: true, wantStatus: "stopped"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := newRestartHaltScheduler(t, t.TempDir())
			as.mu.Lock()
			as.state.Status = tc.status
			as.state.PauseTriggeredBy = tc.trigger
			as.state.MachineHalt = tc.latch
			as.mu.Unlock()

			if got := as.ResumeUnlessMachineHalted(); got != tc.wantResumed {
				t.Errorf("ResumeUnlessMachineHalted() = %v, want %v", got, tc.wantResumed)
			}
			as.mu.Lock()
			defer as.mu.Unlock()
			if as.state.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", as.state.Status, tc.wantStatus)
			}
			if tc.latch != nil && as.state.MachineHalt == nil {
				t.Error("Start cleared the latch — only Resume may")
			}
		})
	}
}

// --- the exit paths that used to launder the halt (#405 fixup) ---------------
//
// The three tests below are the shapes the first fix missed. It keyed the halt
// on Status, and complete() writes Status unconditionally on the way out — so
// every ordinary end of a session erased the gate:
//
//	SIGTERM  (VS Code reload, `serve` shutdown) -> complete("cancelled")
//	Stop()   (the operator's Stop button)       -> complete("stopped")
//
// Both persisted a status loadState reads as an ordinary terminal state, the
// halt predicate then read false, and the next Start retracted a standing
// blocking_fleet card with nobody having triaged anything. Only a SIGKILL — the
// one exit that writes nothing — preserved the halt. The latch inverts that: the
// halt is its own persisted fact, and the exit status is just the exit status.

// TestGracefulShutdownDoesNotLaunderMachineHalt: SIGTERM on a halted fleet.
func TestGracefulShutdownDoesNotLaunderMachineHalt(t *testing.T) {
	root := t.TempDir()

	as1 := newRestartHaltScheduler(t, root)
	stubGraphFn(as1)
	haltFleet(t, as1, "octocat/acme", 901)
	// The daemon receives SIGTERM: cmd/nightgauge/main.go's signal handler
	// cancels the SAME ctx internal/ipc/server.go handed to Run(), so Run's
	// select takes <-ctx.Done() and calls complete("cancelled").
	runToCompletion(t, as1)

	onDisk := readPersistedState(t, root)
	if onDisk.Status != "cancelled" {
		t.Fatalf("precondition: on-disk status = %q, want %q — this test is only meaningful if the shutdown really did overwrite Status", onDisk.Status, "cancelled")
	}
	if onDisk.MachineHalt == nil {
		t.Fatal("the halt latch did not survive complete() — an exit path erased the fact it must not be able to touch")
	}

	as2 := newRestartHaltScheduler(t, root)
	stubGraphFn(as2)
	as2.mu.Lock()
	loaded := as2.state.Status
	as2.mu.Unlock()
	if loaded != "paused" {
		t.Errorf("status after restart = %q, want %q — the latch restores the halt over whatever the shutdown wrote", loaded, "paused")
	}

	if as2.ResumeUnlessMachineHalted() {
		t.Error("Start resumed the halt after a graceful shutdown — a click that answered no card cleared the human gate")
	}
	runToCompletion(t, as2)

	if got := countProducer(t, as2, producerTerminalFailure); got != 1 {
		t.Errorf("%d %q card(s) after a graceful restart, want 1 — the card was retracted with zero triage", got, producerTerminalFailure)
	}
	if n := cyclesRun(as2); n != 0 {
		t.Errorf("CyclesRun = %d, want 0 — the fleet dispatched after a shutdown laundered its halt", n)
	}
}

// TestStopThenStartDoesNotLaunderMachineHalt: the operator's own Stop button on
// a halted fleet, then Start. Stop is not triage either.
func TestStopThenStartDoesNotLaunderMachineHalt(t *testing.T) {
	root := t.TempDir()

	as := newRestartHaltScheduler(t, root)
	stubGraphFn(as)
	haltFleet(t, as, "octocat/acme", 902)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = as.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for !as.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("Run never started")
		}
		time.Sleep(time.Millisecond)
	}
	if got := as.Status().Status; got != "paused" {
		t.Fatalf("precondition: status = %q, want paused", got)
	}

	as.Stop() // IPC autonomous.stop -> Run's <-stopCh -> complete("stopped")
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after Stop")
	}
	if got := as.Status().Status; got != "stopped" {
		t.Fatalf("precondition: status after Stop = %q, want stopped", got)
	}

	// Start again — same process, no restart, so this is purely the in-memory
	// path: the latch is the only thing standing between Stop+Start and a
	// laundered halt.
	if as.ResumeUnlessMachineHalted() {
		t.Error("Start resumed the halt after Stop — Stop+Start is not triage")
	}
	runToCompletion(t, as)

	if got := countProducer(t, as, producerTerminalFailure); got != 1 {
		t.Errorf("%d %q card(s) after Stop+Start, want 1", got, producerTerminalFailure)
	}
	as.mu.Lock()
	restoredStatus := as.state.Status
	as.mu.Unlock()
	if restoredStatus != "cancelled" {
		// runToCompletion ends in complete("cancelled"); the durable facts are
		// the latch and the cards, asserted above and below.
		t.Logf("status after the second Run = %q", restoredStatus)
	}
	if !machineHalted(as.state) {
		t.Error("the latch was cleared by Stop+Start — only Resume may clear it")
	}
}

// TestSecondCrashWhileLatchedStillPreservesHalt: the halt is not a one-shot.
// A fleet that comes up halted, is left alone, and dies again must come up
// halted a third time — the restore must not consume the latch.
func TestSecondCrashWhileLatchedStillPreservesHalt(t *testing.T) {
	root := t.TempDir()

	as1 := newRestartHaltScheduler(t, root)
	stubGraphFn(as1)
	haltFleet(t, as1, "octocat/acme", 903)
	runToCompletion(t, as1) // crash #1

	as2 := newRestartHaltScheduler(t, root)
	stubGraphFn(as2)
	as2.ResumeUnlessMachineHalted()
	runToCompletion(t, as2) // came up halted, then crash #2

	as3 := newRestartHaltScheduler(t, root)
	stubGraphFn(as3)
	as3.mu.Lock()
	status, latched := as3.state.Status, as3.state.MachineHalt != nil
	as3.mu.Unlock()
	if !latched {
		t.Fatal("the latch was consumed by the first restore — a fleet nobody triaged came back clean on the second crash")
	}
	if status != "paused" {
		t.Errorf("status after the second restart = %q, want %q", status, "paused")
	}
	if got := countProducer(t, as3, producerTerminalFailure); got != 1 {
		t.Errorf("%d %q card(s) after two restarts, want 1", got, producerTerminalFailure)
	}
}

// TestGracefulShutdownPreservesSafetyTrip covers the other latched halt: a
// cascade trip, raised by the production writer (onPipelineComplete), then a
// graceful shutdown. complete("cancelled") used to write straight over
// safety_tripped — the same laundering, on the halt the fleet takes hardest.
func TestGracefulShutdownPreservesSafetyTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "")

	as := &AutonomousScheduler{
		workspaceRoot:  root,
		config:         AutonomousConfig{MaxConcurrent: 3},
		state:          &AutonomousState{Status: "running"},
		rescanCh:       make(chan struct{}, 4),
		cascadeTracker: NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute}),
	}
	for _, num := range []int{100, 101, 102} {
		addRunning(as, "octocat/acme", num, "issue")
		as.onPipelineComplete("octocat/acme", num, false, false, "subagent_crash", "stage failed")
		as.drainBackground()
	}
	if as.state.Status != "safety_tripped" {
		t.Fatalf("precondition: status = %q, want safety_tripped", as.state.Status)
	}
	if !machineHalted(as.state) {
		t.Fatal("the cascade trip did not latch — a safety trip is a machine-raised halt too")
	}

	as.complete("cancelled") // SIGTERM during the trip

	onDisk := readPersistedState(t, root)
	if onDisk.Status != "cancelled" {
		t.Fatalf("precondition: on-disk status = %q, want cancelled", onDisk.Status)
	}

	restarted := &AutonomousScheduler{workspaceRoot: root, state: &AutonomousState{}}
	restarted.loadState()
	if restarted.state.Status != "safety_tripped" {
		t.Errorf("status after restart = %q, want %q — a graceful shutdown wrote the cascade trip away", restarted.state.Status, "safety_tripped")
	}
	if restarted.state.PauseTriggeredBy != CascadePauseReason {
		t.Errorf("pauseTriggeredBy = %q, want %q", restarted.state.PauseTriggeredBy, CascadePauseReason)
	}
	if restarted.ResumeUnlessMachineHalted() {
		t.Error("Start resumed a cascade trip — the cascade card asks for a decision and Start answers nothing")
	}
}

// TestHaltedFleetDoesNotPromoteOnStartup pins the startup gate. Run()'s
// prologue reconciles Backlog->Ready before the first cycle (#288) — a WRITE to
// every board saying "this is dispatchable now". On a halted fleet nothing
// dispatches, so the operator would return to a board reshuffled by a process
// that was supposed to be waiting for their decision.
func TestHaltedFleetDoesNotPromoteOnStartup(t *testing.T) {
	const promotionRan = "promoteUnblockedOnStartup:"

	t.Run("halted fleet skips promotion", func(t *testing.T) {
		root := t.TempDir()
		as1 := newRestartHaltScheduler(t, root)
		haltFleet(t, as1, "octocat/acme", 904)

		as2 := newRestartHaltScheduler(t, root)
		stubGraphFn(as2)
		out := captureLog(t, func() { as2.recoverOrphanedRunning(context.Background()) })

		if strings.Contains(out, promotionRan) {
			t.Errorf("the startup promotion scan ran on a halted fleet — board writes for a fleet that dispatches nothing; log: %q", out)
		}
		if !strings.Contains(out, "skipping startup Backlog->Ready promotion") {
			t.Errorf("the skip is silent — nothing names why promotion declined; log: %q", out)
		}
	})

	t.Run("control: an un-halted fleet still promotes", func(t *testing.T) {
		as := newRestartHaltScheduler(t, t.TempDir())
		stubGraphFn(as)
		out := captureLog(t, func() { as.recoverOrphanedRunning(context.Background()) })
		if !strings.Contains(out, promotionRan) {
			t.Errorf("the gate swallowed the normal startup promotion (#288); log: %q", out)
		}
	})
}

// TestFleetIdleSuppressionAtItsRealCallSite exercises the #148 guard where it
// actually fires: mid-cycle. The halt lands while a cycle is in flight (a
// pipeline fails terminally, the extension calls autonomous.pause), and the
// SAME cycle's idle tail must not then advise "N promotable, go add work".
// Reaching it through a dormant restart never runs the tail at all — runCycle
// returns at its `Status != "running"` guard — so that path cannot tell a
// working suppression from a deleted one.
func TestFleetIdleSuppressionAtItsRealCallSite(t *testing.T) {
	t.Run("halt raised mid-cycle suppresses the idle card", func(t *testing.T) {
		as := newRestartHaltScheduler(t, t.TempDir())
		as.mu.Lock()
		as.state.Status = "running"
		as.state.LastPromotionEligible = 3
		as.mu.Unlock()
		as.buildGraphFn = func(context.Context) (*depgraph.Graph, error) {
			// The terminal failure lands while the cycle is running.
			as.Pause("haltQueueOnSlotFailure: issue #905 failed at pr-merge", "haltQueueOnSlotFailure")
			as.RaiseTerminalFailure("octocat/acme", 905, "pr-merge", "gate_failure", 2.5)
			return buildTestGraph(nil, nil), nil
		}

		as.runCycle(context.Background())

		if got := countProducer(t, as, producerWorkExhaustion); got != 0 {
			t.Errorf("%d %q card(s), want 0 — the fleet halted itself, it did not run out of work, and 'go promote something' is the wrong action", got, producerWorkExhaustion)
		}
		if got := countProducer(t, as, producerTerminalFailure); got != 1 {
			t.Errorf("%d %q card(s), want 1 — the halt card must stand through the cycle that raised it", got, producerTerminalFailure)
		}
	})

	t.Run("control: an idle un-halted fleet still gets the idle card", func(t *testing.T) {
		as := newRestartHaltScheduler(t, t.TempDir())
		stubGraphFn(as)
		as.mu.Lock()
		as.state.Status = "running"
		as.state.LastPromotionEligible = 3
		as.mu.Unlock()

		as.runCycle(context.Background())

		if got := countProducer(t, as, producerWorkExhaustion); got != 1 {
			t.Errorf("%d %q card(s), want 1 — the guard swallowed the honest fleet-idle card", got, producerWorkExhaustion)
		}
	})
}

// TestClearMachineHaltOffline covers the no-daemon half of `nightgauge
// autonomous resume`: the same clearer, applied to the state file, leaving a
// status that does not claim a process is dispatching.
func TestClearMachineHaltOffline(t *testing.T) {
	root := t.TempDir()
	as := newRestartHaltScheduler(t, root)
	haltFleet(t, as, "octocat/acme", 906)

	cleared, err := ClearMachineHalt(root)
	if err != nil {
		t.Fatalf("ClearMachineHalt: %v", err)
	}
	if cleared == nil || cleared.Tag != haltTagSlotFailure {
		t.Fatalf("cleared = %+v, want the slot-failure halt", cleared)
	}

	onDisk := readPersistedState(t, root)
	if onDisk.MachineHalt != nil {
		t.Error("the latch survived the offline clear")
	}
	if onDisk.PauseTriggeredBy != "" || onDisk.PauseReason != "" || onDisk.PausedAt != "" {
		t.Errorf("provenance survived the offline clear: %+v", onDisk)
	}
	if onDisk.Status != "stopped" {
		t.Errorf("status = %q, want %q — no process is dispatching, and 'running' is the stale claim #274 exists to catch", onDisk.Status, "stopped")
	}

	// Idempotent: nothing latched, nothing to clear.
	again, err := ClearMachineHalt(root)
	if err != nil {
		t.Fatalf("second ClearMachineHalt: %v", err)
	}
	if again != nil {
		t.Errorf("second clear reported %+v, want nil", again)
	}
}
