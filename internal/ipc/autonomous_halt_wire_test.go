// The operator-visible wire for a machine-raised halt (#405): the registered
// IPC methods and the Action Center verbs, exercised as the extension calls
// them.
//
// Why these live here and not in internal/orchestrator: the scheduler-level
// tests call ResumeUnlessMachineHalted() and Run() by hand, so they stay green
// no matter what the handlers do. Reverting the autonomous.start handler to a
// bare Resume() left every suite passing — the behavior that ships is the
// method table, and nothing was asserting it.
package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// newHaltedServer persists `state` under a fresh workspace root, builds a
// scheduler over it (its constructor loads the state, exactly as a backend
// respawn does), and returns the server whose method table is under test.
func newHaltedServer(t *testing.T, state orchestrator.AutonomousState) (*Server, *orchestrator.AutonomousScheduler, context.Context) {
	t.Helper()
	root := t.TempDir()
	autoDir := filepath.Join(root, ".nightgauge", "autonomous")
	if err := os.MkdirAll(autoDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "state.json"), data, 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: root, Adapter: nil})
	cfg := orchestrator.DefaultAutonomousConfig()
	cfg.MaxConcurrent = 1
	cfg.RefinementEnabled = false
	as := orchestrator.NewAutonomousScheduler(sched, nil, nil, nil, cfg, root)

	server := NewServer(nil, WithAutonomousScheduler(as), WithWorkspaceRoot(root))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		// Cancelling ctx no longer stops a loop these tests start: the resume
		// verb spawns Run on detachedRunCtx (#1425), so the loop outlives every
		// context the test holds. Stop it explicitly and refuse to return while
		// it is alive — a loop that survives its test keeps writing to the
		// process-wide logger and races the next test's log capture, which is
		// how main went red at d9818067 (TestNotifyComplete_DiagnosesAnUnregisteredServedModel).
		as.Stop()
		deadline := time.Now().Add(10 * time.Second)
		for as.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if as.IsRunning() {
			t.Errorf("the autonomous dispatch loop is still running after Stop(); a leaked loop races later tests' log capture")
		}
	})
	return server, as, ctx
}

func haltedState() orchestrator.AutonomousState {
	return orchestrator.AutonomousState{
		Status:           "paused",
		PauseReason:      "haltQueueOnSlotFailure: issue #405 failed at pr-merge",
		PauseTriggeredBy: "haltQueueOnSlotFailure",
		PausedAt:         "2026-08-09T00:00:00Z",
		MachineHalt:      &orchestrator.MachineHaltRecord{Tag: "haltQueueOnSlotFailure", Status: "paused"},
	}
}

func waitRunning(t *testing.T, as *orchestrator.AutonomousScheduler) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !as.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("the dispatch goroutine never came up")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAutonomousStartHandlerLeavesMachineHaltInForce is the wire pin for
// design D: `autonomous.start` brings the backend UP and comes back halted.
// It asserts both halves, because each alone is satisfiable by a bug — a
// handler that resumes would still report IsRunning, and a handler that
// refuses to start anything would still report the halt.
func TestAutonomousStartHandlerLeavesMachineHaltInForce(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())

	handler, ok := server.methods["autonomous.start"]
	if !ok {
		t.Fatal("autonomous.start handler not registered")
	}
	res, err := handler(ctx, nil)
	if err != nil {
		t.Fatalf("autonomous.start returned error: %v", err)
	}

	status, ok := res.(orchestrator.AutonomousState)
	if !ok {
		t.Fatalf("autonomous.start returned %T, want orchestrator.AutonomousState", res)
	}
	if status.Status != "paused" {
		t.Errorf("returned status = %q, want %q — Start is 'bring the backend up', not 'I triaged that'", status.Status, "paused")
	}
	if status.MachineHalt == nil {
		t.Error("the halt latch was cleared by Start — only Resume may clear it")
	}
	if status.PauseTriggeredBy != "haltQueueOnSlotFailure" || status.PauseReason == "" {
		t.Errorf("provenance lost: triggeredBy=%q reason=%q — the returned status is how the caller learns WHY it came up halted",
			status.PauseTriggeredBy, status.PauseReason)
	}
	waitRunning(t, as)
}

// TestAutonomousStartHandlerStillResumesAnOperatorPause: the gate is scoped.
// Every state Start used to act on still resumes.
func TestAutonomousStartHandlerStillResumesAnOperatorPause(t *testing.T) {
	server, as, ctx := newHaltedServer(t, orchestrator.AutonomousState{
		Status:           "paused",
		PauseReason:      "user requested via UI",
		PauseTriggeredBy: "user",
	})

	if _, err := server.methods["autonomous.start"](ctx, nil); err != nil {
		t.Fatalf("autonomous.start returned error: %v", err)
	}
	waitRunning(t, as)
	if got := as.Status().Status; got != "running" {
		t.Errorf("status = %q, want running — Start must still resume an operator's own pause", got)
	}
}

// TestRetryCardResumesTheHaltedFleet walks the card chain end to end: the
// Retry option the fleet-halt card ships (clearIssueFailures + then=resume)
// must clear the halt AND bring the dispatch loop up.
//
// This is the shape that shipped broken: the arm honored only
// `autonomous.rescan`, so the resume value fell through, ExecuteVerb returned
// nil, and the store had already CAS-resolved the request — the card was
// consumed, the fleet stayed halted, and the surface that could re-raise it
// was gone.
func TestRetryCardResumesTheHaltedFleet(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())

	req := &attention.DecisionRequest{
		IdempotencyKey: "terminal-failure:octocat/acme#405",
		Context:        attention.Context{Repo: "octocat/acme", Issue: 405},
	}
	opt := attention.Option{
		ID:   "retry",
		Verb: attention.VerbAutonomousClearIssueFailures,
		Args: map[string]any{"key": "octocat/acme#405", "then": "autonomous.resume"},
	}
	if err := server.ExecuteVerb(ctx, req, opt); err != nil {
		t.Fatalf("ExecuteVerb(retry): %v", err)
	}

	if s := as.Status(); s.MachineHalt != nil || s.Status != "running" {
		t.Errorf("after Retry: status=%q latched=%v — answering the card is exactly what clears the halt",
			s.Status, s.MachineHalt != nil)
	}
	waitRunning(t, as)
}

// TestResumeVerbStartsTheDispatchLoop: the bare `autonomous.resume` verb (the
// cascade-pause card's Resume option) goes through the same primitive. Before
// the fixup it called Resume() directly — the #3303 dead state, made
// boot-reachable the moment a halt survives a restart.
func TestResumeVerbStartsTheDispatchLoop(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())

	opt := attention.Option{ID: "resume", Verb: attention.VerbAutonomousResume}
	if err := server.ExecuteVerb(ctx, &attention.DecisionRequest{}, opt); err != nil {
		t.Fatalf("ExecuteVerb(resume): %v", err)
	}
	if s := as.Status(); s.MachineHalt != nil {
		t.Error("the resume verb left the halt latched")
	}
	waitRunning(t, as)
}

// TestRetryWithEscalationResumesTheHaltedFleet: the second Retry option on the
// same card takes the redispatchAfterOverride tail, which used to hard-code a
// rescan. A rescan on a halted fleet re-enters runCycle and returns at its
// `Status != "running"` guard — the escalated retry the operator paid for
// never dispatches.
func TestRetryWithEscalationResumesTheHaltedFleet(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())

	req := &attention.DecisionRequest{Context: attention.Context{Repo: "octocat/acme", Issue: 405}}
	opt := attention.Option{
		ID:   "retry-escalate",
		Verb: attention.VerbRunRetryWithEscalation,
		Args: map[string]any{"key": "octocat/acme#405", "issueNumber": 405, "tier": "opus", "then": "autonomous.resume"},
	}
	if err := server.ExecuteVerb(ctx, req, opt); err != nil {
		t.Fatalf("ExecuteVerb(retry-escalate): %v", err)
	}
	if s := as.Status(); s.MachineHalt != nil || s.Status != "running" {
		t.Errorf("after Retry with escalation: status=%q latched=%v", s.Status, s.MachineHalt != nil)
	}
	waitRunning(t, as)
}

// TestUnknownThenActionIsLoud: an option whose follow-on the daemon does not
// implement must fail. Silence here is how a card gets consumed for an action
// that never happened.
func TestUnknownThenActionIsLoud(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())

	opt := attention.Option{
		ID:   "retry",
		Verb: attention.VerbAutonomousClearIssueFailures,
		Args: map[string]any{"key": "octocat/acme#405", "then": "autonomous.teleport"},
	}
	err := server.ExecuteVerb(ctx, &attention.DecisionRequest{Context: attention.Context{Repo: "octocat/acme", Issue: 405}}, opt)
	if err == nil {
		t.Fatal("an unrecognized \"then\" action returned success — the card is consumed and nothing ran")
	}
	if !strings.Contains(err.Error(), "autonomous.teleport") {
		t.Errorf("error %q does not name the offending value", err)
	}
	if as.Status().MachineHalt == nil {
		t.Error("the failed option still cleared the halt")
	}
}

// TestRescanThenStillWorks: the pre-existing follow-on is untouched.
func TestRescanThenStillWorks(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())

	opt := attention.Option{
		ID:   "clear",
		Verb: attention.VerbAutonomousClearIssueFailures,
		Args: map[string]any{"key": "octocat/acme#405", "then": "autonomous.rescan"},
	}
	if err := server.ExecuteVerb(ctx, &attention.DecisionRequest{Context: attention.Context{Repo: "octocat/acme", Issue: 405}}, opt); err != nil {
		t.Fatalf("ExecuteVerb(clear+rescan): %v", err)
	}
	// A rescan is not triage: the halt stays.
	if as.Status().MachineHalt == nil {
		t.Error("a rescan cleared the halt — only a resume does that")
	}
}
