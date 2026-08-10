package ipc

import (
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// The ADR-017 step-4 review round. Each test here pins a property the step-4
// implementation asserted but did not yet hold.

// TestRunIdentity_ProgressReadsRepoUnderTheRunsOwnMutex is a RACE-DETECTOR
// regression: run it with -race or it proves nothing.
//
// `notifyStageTransition` seeds the run's repo through SeedRunContext, which
// writes rs.Repo under rs.mu because repo is run CONTENT (ADR-017 Decision 12).
// `notifyStageProgress` read the same field off the resolved runtime with NO
// lock. The two handlers run in separate goroutines — one per inbound request —
// and both release runtimesMu before touching the runtime, so nothing orders the
// write against the read.
//
// The window is the ordinary startup shape, not an exotic one: the extension's
// FIRST transition is `initialized` and carries no repo (the dispatcher resolves
// the target repo asynchronously, #307), the >= 1/5s progress stream is already
// flowing by the time the repo-carrying transition arrives, and the value being
// torn is what routes the run's telemetry to a repo.
func TestRunIdentity_ProgressReadsRepoUnderTheRunsOwnMutex(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))

	const (
		repo  = "acme/platform"
		runs  = 48
		polls = 40
	)

	// Adopt each run with NO repo — the `initialized` transition — so the
	// repo-carrying transition below is the FIRST writer of rs.Repo.
	ids := make([]string, runs)
	for i := range ids {
		ids[i] = newTestRunID()
		mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
			IssueNumber: 5000 + i, Stage: "pipeline-start", Status: "initialized", RunID: ids[i],
		})
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range ids {
		i := i
		wg.Add(2)
		// The writer: one repo-carrying transition per run.
		go func() {
			defer wg.Done()
			<-start
			_ = callRunVerb(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
				Repo: repo, IssueNumber: 5000 + i, Stage: "feature-dev", Status: "running", RunID: ids[i],
			})
		}()
		// The reader: the live in-stage estimate stream for the same run.
		go func() {
			defer wg.Done()
			<-start
			for p := 0; p < polls; p++ {
				if err := callRunVerb(t, s, "pipeline.notifyStageProgress", PipelineNotifyStageProgressParams{
					IssueNumber: 5000 + i, Stage: "feature-dev", RunID: ids[i], InputTokens: p,
				}); err != nil {
					t.Errorf("a live run's own progress was refused: %v", err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	// The seeding still happened — the fix is a locked read, not a dropped one.
	for i, id := range ids {
		s.runtimesMu.Lock()
		e := s.activeRuntimes[id]
		s.runtimesMu.Unlock()
		if e == nil {
			t.Fatalf("run %d lost its registry entry", i)
		}
		if got := e.rs.TargetRepo(); got != repo {
			t.Fatalf("run %d has repo %q, want %q — SeedRunContext's write was lost", i, got, repo)
		}
	}
}

// TestSetScheduler_WiresTheRunRegistry goes through the PRODUCTION attach path.
//
// `nightgauge serve` builds the scheduler after the server (it needs
// IpcStageRunner) and attaches it with SetScheduler; WithScheduler has no
// production caller at all. Wiring Decision 11's scheduler registry only in the
// option left `schedulerRuns` nil in every real deployment, so every
// scheduler-run phase event fell through to ADOPTION — a phantom entry holding a
// lease that is never terminal-claimed, poisoning the derived issue index and
// swallowing the PhaseHistory the scheduler's runtime should have received.
// Behaviour tests may keep their hand-assigned fakes; this one must not.
func TestSetScheduler_WiresTheRunRegistry(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	if s.schedulerRuns != nil {
		t.Fatal("precondition: a server with no scheduler must have no scheduler registry")
	}

	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: root})
	s.SetScheduler(sched)
	if s.schedulerRuns == nil {
		t.Error("SetScheduler did not wire schedulerRuns — Decision 11's scheduler arm is dead in production")
	}

	// A nil *Scheduler stored in an interface field is a NON-nil interface, and
	// every `if s.schedulerRuns != nil` guard downstream would then call through
	// a nil receiver.
	nilServer := NewServer(nil, WithWorkspaceRoot(t.TempDir()))
	nilServer.SetScheduler(nil)
	if nilServer.schedulerRuns != nil {
		t.Error("a nil *Scheduler became a non-nil schedulerRunRegistry interface")
	}
}

// TestRunIdentity_AdoptionServePathsRecheckTheTerminalLatch covers the claim's
// unlocked STEP-2 WINDOW on the two paths that had no re-check.
//
// Between the latch (step 1c) and the compare-and-delete (step 3) the entry is
// terminal but still in activeRuntimes and not yet in closedRuns. A call that
// passed resolveRun's checks before the latch was stamped reaches the adoption
// paths, and serving it there refreshes a CLOSED run's lease — which makes it
// "current" for its repo#issue in the derived index (Decision 6) while its
// record is already written.
func TestRunIdentity_AdoptionServePathsRecheckTheTerminalLatch(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	const (
		repo  = "acme/platform"
		issue = 4370
	)
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})

	// Reproduce the window exactly: latched, still registered, not yet closed.
	s.runtimesMu.Lock()
	entry := s.activeRuntimes[runID]
	entry.terminal = true
	leaseBefore := entry.lastSeen
	s.runtimesMu.Unlock()

	// resolveOrAdopt's registry fast path. Called directly because resolveRun's
	// own terminal arm would answer first and the paths under test would never
	// be reached.
	_, fastPathErr := s.resolveOrAdopt("pipeline.notifyStageProgress", verbRunProgress, runID, repo, issue)
	wantRefusal(t, fastPathErr, codeRunClosed)

	// settleAdoption's entry-found arm: a flight that found nothing on disk
	// while a peer's entry — this one — is already latched.
	flight := &adoptFlight{done: make(chan struct{})}
	close(flight.done)
	_, settleErr := s.settleAdoption(flight, "pipeline.notifyPhaseTransition", verbRunProgress, runID, repo, issue)
	wantRefusal(t, settleErr, codeRunClosed)

	s.runtimesMu.Lock()
	leaseAfter := entry.lastSeen
	s.runtimesMu.Unlock()
	if !leaseAfter.Equal(leaseBefore) {
		t.Errorf("a refused call refreshed a terminal-latched run's lease: %v → %v", leaseBefore, leaseAfter)
	}
}

// TestRunIdentity_SchedulerRegistryIsConsultedThroughTheProductionAttach is the
// behavioural half of the wiring fix: with the scheduler attached the way
// `nightgauge serve` attaches it, a scheduler-owned run resolves through
// Decision 11's scheduler arm instead of being adopted into a second entry.
func TestRunIdentity_SchedulerRegistryIsConsultedThroughTheProductionAttach(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	s.SetScheduler(orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: root}))

	// The real scheduler owns no runs here, so the arm is consulted and finds
	// nothing — the property under test is that `schedulerRuns` is non-nil, so
	// the lookup HAPPENS. A nil registry skips the arm entirely and every
	// scheduler run silently becomes an adopted phantom.
	if s.schedulerRuns == nil {
		t.Fatal("the production attach path left the scheduler registry unwired")
	}
	if rt := s.schedulerRuns.LookupRunByID(newTestRunID()); rt != nil {
		t.Errorf("an empty scheduler registry answered a lookup with %v", rt)
	}
}
