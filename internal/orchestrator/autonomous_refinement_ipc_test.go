package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// refinementStageRunner is a StageRunner double standing in for
// IpcStageRunner: it records every dispatch and can be made to block, so a
// test can observe the refinement semaphore WHILE the stage is in flight.
type refinementStageRunner struct {
	mu     sync.Mutex
	calls  []StageRunParams
	block  chan struct{} // when non-nil, RunStage waits on it before returning
	result *StageRunResult
	err    error
}

func (r *refinementStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	block := r.block
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	if r.result != nil || r.err != nil {
		return r.result, r.err
	}
	return &StageRunResult{ExitCode: 0}, nil
}

func (r *refinementStageRunner) dispatches() []StageRunParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StageRunParams(nil), r.calls...)
}

// ipcRefinementScheduler builds an autonomous scheduler in the shape `serve`
// builds one for extension (IPC) mode: a Scheduler with NO CLI adapter whose
// StageRunner is the bridge, and refinement registered through it.
func ipcRefinementScheduler(t *testing.T, runner StageRunner) (*AutonomousScheduler, string) {
	t.Helper()
	ws := t.TempDir()
	withRefineRoots(t, ws, refineSkillFixture(t))

	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: ws, Adapter: nil})
	sched.WithStageRunner(runner)

	as := NewAutonomousScheduler(sched, nil, []depgraph.RepoConfig{}, nil, DefaultAutonomousConfig(), ws)
	as.markRefinedFn = func(context.Context, string, string, int) error { return nil }
	as.WithIPCRefinement()
	return as, ws
}

// TestRefinementIsViableOnceIPCRefinementIsRegistered is the guard for the
// defect itself (#1529): `serve` builds the scheduler with Adapter=nil, so
// before the registration there is no execution path at all and every
// refinement cycle returns at the top.
func TestRefinementIsViableOnceIPCRefinementIsRegistered(t *testing.T) {
	ws := t.TempDir()
	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: ws, Adapter: nil})
	sched.WithStageRunner(&refinementStageRunner{})
	as := NewAutonomousScheduler(sched, nil, []depgraph.RepoConfig{}, nil, DefaultAutonomousConfig(), ws)

	if as.refinementIsViable() {
		t.Fatal("a scheduler with no adapter and no registered runner must not claim refinement is viable")
	}
	as.WithIPCRefinement()
	if !as.refinementIsViable() {
		t.Fatal("refinement must be viable once the stage-bridge runner is registered")
	}
}

// TestRefineViaStageRunnerBuildsARefinementStageDispatch pins the envelope the
// extension receives. Every field here is one the TS SkillRunner reads: an
// empty Prompt/SkillPath spawns a process that does nothing, an empty RunID is
// hard-refused at the dispatch boundary (ADR-017 step 0b), and a WorktreePath
// that is not the target repo's root refines an issue against the wrong
// checkout in a multi-repo daemon.
func TestRefineViaStageRunnerBuildsARefinementStageDispatch(t *testing.T) {
	runner := &refinementStageRunner{}
	as, ws := ipcRefinementScheduler(t, runner)

	if err := as.refineViaStageRunner(context.Background(), "acme", "widgets", 42); err != nil {
		t.Fatalf("refineViaStageRunner: %v", err)
	}

	calls := runner.dispatches()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one stage dispatch, got %d", len(calls))
	}
	got := calls[0]
	if got.Stage != state.StageIssueRefine {
		t.Errorf("Stage = %q, want %q", got.Stage, state.StageIssueRefine)
	}
	if got.IssueNumber != 42 {
		t.Errorf("IssueNumber = %d, want 42", got.IssueNumber)
	}
	if got.Repo != "acme/widgets" || got.TargetRepo != "acme/widgets" {
		t.Errorf("Repo/TargetRepo = %q/%q, want acme/widgets", got.Repo, got.TargetRepo)
	}
	if got.WorktreePath != ws {
		t.Errorf("WorktreePath = %q, want the resolved repo root %q", got.WorktreePath, ws)
	}
	if got.Model == "" {
		t.Error("Model is empty — the extension refuses a dispatch that carries no authoritative model (#340)")
	}
	if !runstate.IsIdentity(got.RunID) {
		t.Errorf("RunID = %q, want a canonical run identity — IpcStageRunner refuses an empty one", got.RunID)
	}
	if got.Timeout <= 0 {
		t.Error("Timeout is unset — the refinement would run without the refinement deadline")
	}
	if got.SkillPath == "" || got.Prompt == "" || len(got.AllowedTools) == 0 {
		t.Errorf("dispatch is missing skill/prompt/tools: skillPath=%q promptLen=%d tools=%d",
			got.SkillPath, len(got.Prompt), len(got.AllowedTools))
	}
}

// TestRefineViaStageRunnerFailsOnNonZeroExit: a stage that came back non-zero
// is a FAILED refinement. The fire-and-forget predecessor could not express
// this at all — it returned at handoff — so a failed refinement was recorded
// as a success and the issue was labelled pipeline:refined regardless.
func TestRefineViaStageRunnerFailsOnNonZeroExit(t *testing.T) {
	runner := &refinementStageRunner{result: &StageRunResult{ExitCode: 2, ErrorText: "skill blew up"}}
	as, _ := ipcRefinementScheduler(t, runner)

	err := as.refineViaStageRunner(context.Background(), "acme", "widgets", 42)
	if err == nil {
		t.Fatal("a non-zero stage exit must surface as a refinement failure")
	}
	if !strings.Contains(err.Error(), "skill blew up") {
		t.Errorf("error does not carry the stage's reason: %v", err)
	}
}

// TestRefineIssueHoldsTheSlotUntilTheStageResultArrives settles #503 as Option
// B on the IPC path: refinement_max_concurrent bounds in-flight REFINEMENTS,
// not handoffs. While the stage is running the slot stays taken; it is
// released only after the runner returns.
func TestRefineIssueHoldsTheSlotUntilTheStageResultArrives(t *testing.T) {
	release := make(chan struct{})
	runner := &refinementStageRunner{block: release}
	as, _ := ipcRefinementScheduler(t, runner)

	as.refinementSem <- struct{}{} // the slot the dispatch loop would have taken

	done := make(chan struct{})
	go func() {
		defer close(done)
		as.refineIssue(context.Background(), "acme", "widgets",
			gh.UnrefinedIssue{Number: 42, Title: "Refine me"},
			refinementOrigin{tier: refinementTierReady, source: refinementSourceCycle})
	}()

	// Wait for the stage to actually be in flight, then assert the slot is
	// still held. A release-at-handoff implementation frees it here.
	deadline := time.After(5 * time.Second)
	for len(runner.dispatches()) == 0 {
		select {
		case <-deadline:
			t.Fatal("refinement never reached the stage runner")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := len(as.refinementSem); got != 1 {
		t.Fatalf("refinement slot count = %d while the stage is in flight, want 1 (the slot must be held to completion)", got)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("refineIssue did not return after the stage result arrived")
	}
	if got := len(as.refinementSem); got != 0 {
		t.Fatalf("refinement slot count = %d after completion, want 0", got)
	}
}

// TestRefineIssueLogsOneLinePerRefinement pins the operator-visible outcome
// line, which is the only thing that distinguishes "refinement ran" from
// "refinement was silently off" in a daemon log.
func TestRefineIssueLogsOneLinePerRefinement(t *testing.T) {
	var logs strings.Builder
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	runner := &refinementStageRunner{}
	as, _ := ipcRefinementScheduler(t, runner)
	as.refinementSem <- struct{}{}
	as.refineIssue(context.Background(), "acme", "widgets",
		gh.UnrefinedIssue{Number: 42, Title: "Refine me"},
		refinementOrigin{tier: refinementTierReady, source: refinementSourceCycle})

	want := "[refinement] refined acme/widgets#42 (tier=1, source=cycle)"
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, logs.String())
	}
}

// TestRefineIssueLogsTheFailureReason: the failure line names the stage's own
// reason, so an operator can tell a broken skill from a missing runner.
func TestRefineIssueLogsTheFailureReason(t *testing.T) {
	var logs strings.Builder
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	runner := &refinementStageRunner{err: fmt.Errorf("stall-killed")}
	as, _ := ipcRefinementScheduler(t, runner)
	as.refinementSem <- struct{}{}
	as.refineIssue(context.Background(), "acme", "widgets",
		gh.UnrefinedIssue{Number: 42, Title: "Refine me"},
		refinementOrigin{tier: refinementTierPrioritizedBacklog, source: refinementSourcePreDispatch})

	out := logs.String()
	if !strings.Contains(out, "[refinement] failed acme/widgets#42 (tier=2, source=pre-dispatch)") ||
		!strings.Contains(out, "stall-killed") {
		t.Fatalf("expected a failure line naming tier, source and reason, got:\n%s", out)
	}
}

// TestRefinementDisabledSendsNoStageTraffic is the acceptance criterion that
// the off switch is still an off switch: with autonomous.refinement_enabled
// false the pre-dispatch hook dispatches the issue unrefined and NOTHING
// crosses the stage bridge.
func TestRefinementDisabledSendsNoStageTraffic(t *testing.T) {
	var logs strings.Builder
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	runner := &refinementStageRunner{}
	as, _ := ipcRefinementScheduler(t, runner)
	as.config.RefinementEnabled = false

	if as.refineBeforeDispatch(context.Background(), hookGraph(), hookItem) {
		t.Fatal("the hook must not refine while refinement is disabled")
	}
	if n := len(runner.dispatches()); n != 0 {
		t.Fatalf("%d stage dispatch(es) crossed the bridge with refinement disabled, want 0", n)
	}
	if out := logs.String(); !strings.Contains(out, "refinement is disabled") {
		t.Fatalf("expected the disabled reason on the unrefined-dispatch line, got:\n%s", out)
	}
}
