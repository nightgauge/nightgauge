package ipc

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// ADR-017 step 5, review round 1. Each test here pins a property the reviewer
// showed the first cut did not hold, and names the scoped revert that turns it
// red so the next reader can reproduce the proof rather than trust it.

// --- MF-1: the reap's final hold must re-apply the age predicate ------------

// resurrectingScheduler drives a run's resurrection from INSIDE
// reapStaleRunEntries' unlocked phase 2. Its IsRunLive is that phase's only
// call site, so the hook fires exactly where the window is — between the
// candidate gather and the compare-and-delete — and what it does there is what
// a reconnecting run does: one real pipeline.notifyStageProgress, which
// touchLocked stamps onto the EXISTING entry IN PLACE.
//
// Once, not every call: the ladder's arm 2 consults the same registry later in
// the pass, and a second resurrection there would re-adopt the entry the reap
// had just evicted, hiding the very eviction under test.
type resurrectingScheduler struct {
	*fakeSchedulerRuns
	once   sync.Once
	revive func()
}

func (r *resurrectingScheduler) IsRunLive(string) bool {
	r.once.Do(r.revive)
	return false
}

// TestOrphanReconcile_AResurrectedRunSurvivesTheReapItRacesWith is the S1
// correction.
//
// The compare-and-delete detects entry REPLACEMENT only. touchLocked does not
// replace the entry — it refreshes `lastSeen` in place — so a run that was
// silent for >30 minutes and speaks again inside phase 2's unlocked window is
// evicted holding a lease minted milliseconds earlier. The SAME pass then finds
// no entry for it (arm 1 is the entry's lease), reads a stale mtime (arm 4) and
// a snapshot whose stage child is gone, and emits a terminal
// pipeline_done(success=false) for a run that is talking to it.
//
// RED-FIRST: against ed71fad6 (phase 3 comparing only the pointer) this test
// fails at both assertions.
func TestOrphanReconcile_AResurrectedRunSurvivesTheReapItRacesWith(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()
	runID := newTestRunID()
	const issue = 530

	snapshot := staleSnapshot(t, stateDir, issue, runID, now)
	rt, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	installRegistryEntry(t, s, rt, now.Add(-2*livenessWindow))

	s.schedulerRuns = &resurrectingScheduler{
		fakeSchedulerRuns: newFakeSchedulerRuns(),
		revive: func() {
			mustCall(t, s, "pipeline.notifyStageProgress", PipelineNotifyStageProgressParams{
				Repo: "nightgauge/acmeapp", IssueNumber: issue, Stage: "feature-dev", RunID: runID,
			})
		},
	}

	s.reconcilePass(now)

	s.runtimesMu.Lock()
	_, left := s.activeRuntimes[runID]
	s.runtimesMu.Unlock()
	if !left {
		t.Error("a run that re-asserted inside the reap's unlocked window was evicted holding a fresh lease — the pointer compare sees replacement, not refresh")
	}
	mustExist(t, snapshot, "the resurrected run's snapshot (the eviction above is what un-pins it)")
}

// --- MF-2: the interactive stage population's arm ---------------------------

// TestOrphanReconcile_InteractiveShapedRunSurvivesOnItsStagePidAlone pins the
// consumer half of C18 for the interactive stage driver
// (commands/runInteractiveStage.ts).
//
// Its traffic profile is ONE `running` transition and then nothing at all until
// the stage ends — a conversation, not a token stream — so after 30 minutes arm
// 1 (the lease) is stale, arm 2 is structurally false (it is not a scheduler
// run), arm 4 is stale (nothing has persisted since that one transition) and
// arm 5 has expired. Exactly one arm can carry it: the pid of the child the
// interactive spawn returned, which is why MF-2's producer change exists.
//
// RED-FIRST: with `rt.SetStageChild(p.StagePid)` removed from
// notifyStageTransition, or the `ProcessAlive(c.rs.StageChildPID())` arm removed
// from reapStaleRunEntries, the WITH-pid arm fails. The WITHOUT-pid arm is the
// executed proof of what the producer gap costs — it is today's interactive
// path, and it books a live 30-minute session as a failed run.
func TestOrphanReconcile_InteractiveShapedRunSurvivesOnItsStagePidAlone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stagePid int
		// wantAlive is what the ladder must decide for this producer.
		wantAlive bool
	}{
		{"the producer sends the child's pid (MF-2)", os.Getpid(), true},
		{"the producer sends nothing, as the interactive driver does today", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, stateDir := reconcileServer(t)
			now := time.Now()
			runID := newTestRunID()
			const issue = 540

			mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
				Repo: "nightgauge/acmeapp", IssueNumber: issue, Stage: "feature-dev", Status: "running",
				RunID: runID, StagePid: tc.stagePid,
			})
			snapshot := filepath.Join(stateDir, state.SnapshotFilename(issue, runID))
			mustExist(t, snapshot, "the one transition must persist")

			// 30+ minutes of conversation: no verb, no persist. Both the lease
			// and the file age out together, because the same silence produces
			// both.
			backdate(t, snapshot, now.Add(-2*livenessWindow))
			s.runtimesMu.Lock()
			e := s.activeRuntimes[runID]
			if e == nil {
				s.runtimesMu.Unlock()
				t.Fatal("the transition installed no entry")
			}
			e.lastSeen = now.Add(-2 * livenessWindow)
			e.firstSeen = e.lastSeen
			s.runtimesMu.Unlock()

			// Grace never armed on this server, and the scheduler registry is
			// cold: arms 2 and 5 are structurally out of the picture.
			if s.withinStartupGrace() {
				t.Fatal("this test is about the post-grace pass")
			}
			acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)

			s.reconcilePass(now)
			s.runtimesMu.Lock()
			_, left := s.activeRuntimes[runID]
			s.runtimesMu.Unlock()

			if tc.wantAlive {
				if n := countEmissions(acts); n != 0 {
					t.Errorf("%d terminal event(s) built for a live interactive stage; want 0", n)
				}
				if !left {
					t.Error("the reaper evicted a run whose stage child is alive — its existing StageChildPID arm is what must carry this population")
				}
				mustExist(t, snapshot, "an interactive stage whose child is alive")
				return
			}
			if countEmissions(acts) != 1 {
				t.Fatalf("got %+v, want the one terminal event a pid-less interactive run is closed with", acts)
			}
			if left {
				t.Error("without a pid nothing pins this entry — the growth fix would be neutered if it survived")
			}
			mustBeGone(t, snapshot, "a live interactive stage with no pid on the wire (the F26/C18 silent close)")
		})
	}
}

// --- MF-3: an administrative install must not rescue its snapshot -----------

// TestRunIdentity_AdministrativeInstallDoesNotRescueItsSnapshot pins §7.3's
// "only run traffic counts as re-assertion", which step 4 deferred here
// (run_identity_step4_regression_test.go's note at the F9 lease pin).
//
// Step 4 pinned that the entry carries the ZERO lease; what was never pinned is
// the consequence — that arm 1 REFUSES it, so the administrative resolution
// installs state without vouching for the run. The property currently rests on
// Time.Sub saturating at the zero time, which is a true fact about the stdlib
// and an invisible one in this predicate.
//
// RED-FIRST: with runLeaseIsFresh reverted to the lease-blind
// `e != nil && !e.terminal` (hasLiveRunForIssue's rule, keyed on the run) this
// test fails at "an administrative install must not answer arm 1".
func TestRunIdentity_AdministrativeInstallDoesNotRescueItsSnapshot(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()
	runID := newTestRunID()
	const issue = 550

	// A NON-paused snapshot, so the pass's decision is the ordinary-orphan row
	// rather than C5's pause exemption.
	seeded := state.NewRuntimeState("nightgauge/acmeapp", issue, "", runID)
	seeded.BeginStage(state.StageFeatureDev)
	if err := seeded.Persist(stateDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// An administrative verb on an id no registry holds: it adopts from the
	// snapshot (Decision 4) and installs an entry with a zero lease. paused
	// stays false — the verb is what installs the entry, not the state it sets.
	mustCall(t, s, "pipeline.setPaused", PipelineSetPausedParams{
		IssueNumber: issue, Repo: "nightgauge/acmeapp", Paused: false, RunID: runID,
	})

	s.runtimesMu.Lock()
	entry := s.activeRuntimes[runID]
	s.runtimesMu.Unlock()
	if entry == nil {
		t.Fatal("the administrative resolution installed no entry")
	}
	if !entry.lastSeen.IsZero() {
		t.Fatalf("lastSeen = %v, want the zero time (the step-4 F9 pin this builds on)", entry.lastSeen)
	}

	// setPaused persists, so the file is fresh: age it, or arm 4 answers and the
	// arm under test is never reached.
	snapshot := filepath.Join(stateDir, state.SnapshotFilename(issue, runID))
	staleModTime := now.Add(-2 * livenessWindow)
	backdate(t, snapshot, staleModTime)
	snap, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if snap.Paused {
		t.Fatal("this test needs a non-paused snapshot to reach the ordinary-orphan row")
	}

	if skipRun(s.serverEvidence(now), runID, snap, staleModTime, now) {
		t.Error("an administrative install must not answer arm 1 — §7.3: only run traffic counts as re-assertion, and abandonRun is the opposite claim")
	}

	s.reconcilePass(now)
	mustBeGone(t, snapshot, "a snapshot pinned only by an administratively-installed entry")
}

// --- shared: the emission recorder ------------------------------------------

// countEmissions reports how many terminal events a pass WOULD emit.
//
// The observable seam is the pure collector, not analyticsSvc: that field is a
// concrete *platform.AnalyticsService whose emission is an HTTP POST behind a
// goroutine, and every assertion here is about what the TABLE decided rather
// than about who was told. reconcileAction.Event exists for exactly this
// (pipeline_orphan_reconcile.go's collector comment).
func countEmissions(acts []reconcileAction) int {
	n := 0
	for _, a := range acts {
		if a.Disposition == dispositionEmitAndRemove && a.Event.EventType != "" {
			n++
		}
	}
	return n
}

// emissionsOf returns the events themselves, for the assertions that care which
// run would have been closed.
func emissionsOf(acts []reconcileAction) []platform.PipelineEvent {
	var out []platform.PipelineEvent
	for _, a := range acts {
		if a.Disposition == dispositionEmitAndRemove && a.Event.EventType != "" {
			out = append(out, a.Event)
		}
	}
	return out
}

// ageAllLeases back-dates every registry entry so ladder arm 1 cannot answer.
// Driving a real verb is the only way to exercise the real handler, and every
// accepted verb stamps a fresh lease — so a test about arms 2–5 has to undo the
// lease the setup itself created.
func ageAllLeases(s *Server, when time.Time) {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	for _, e := range s.activeRuntimes {
		if e == nil {
			continue
		}
		e.lastSeen = when
		e.firstSeen = when
	}
}

// --- F-H(2): the rows that collect WITHOUT telling anyone --------------------

// TestOrphanReconcile_SilentRowsCollectWithoutEmitting pins the other half of
// the F24 split. Three of 7.4's rows remove a file and emit NOTHING — the
// terminal snapshot (its claim already emitted), the stale abandonment
// (abandonRun already emitted) and the stale claim release (a claim is not a
// run outcome at all). A pass that emitted for any of them would double-book a
// run the platform has already closed, which is worse than the silence F24 was
// about.
//
// The pass runs with analyticsSvc nil — the first-class local-only
// configuration — so the assertion is that all three files went AND that the
// table built no event for any of them.
func TestOrphanReconcile_SilentRowsCollectWithoutEmitting(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()
	if s.analyticsSvc != nil {
		t.Fatal("this test is about the nil-analytics path")
	}

	terminal := newInterruptedRuntime(560, newTestRunID())
	terminal.MarkTerminal("success")
	terminalPath := writeRuntimeSnapshot(t, stateDir, terminal)
	backdate(t, terminalPath, now.Add(-2*livenessWindow))

	abandoned := newInterruptedRuntime(561, newTestRunID())
	abandoned.MarkAbandoned(now.Add(-2*livenessWindow), "force-clear")
	abandonedPath := writeRuntimeSnapshot(t, stateDir, abandoned)
	backdate(t, abandonedPath, now.Add(-2*livenessWindow))

	claimID := newTestRunID()
	claim := writeClaimArtifact(t, stateDir, 562, claimID, claimTokenAt(t, now.Add(-startupGrace-time.Minute)))

	acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)
	if len(acts) != 3 {
		t.Fatalf("got %d actions %+v, want one per silent row", len(acts), acts)
	}
	if events := emissionsOf(acts); len(events) != 0 {
		t.Fatalf("the silent rows built %d terminal event(s) %+v — each of these runs was already closed by someone else", len(events), events)
	}

	s.reconcilePass(now)

	mustBeGone(t, terminalPath, "a terminal snapshot (removed at any age, grace or no grace)")
	mustBeGone(t, abandonedPath, "an abandonment outside the liveness window")
	mustBeGone(t, claim, "a claim whose token predates the release threshold")
	mustExist(t, filepath.Join(stateDir, state.SnapshotFilename(562, claimID)),
		"the released claim's pause, back under the canonical name")
}

// --- F-E: the deferred pass must not lose the launch root's orphans ---------

// TestOrphanReconcile_TheLaunchRootIsScannedAfterASwitch pins F-E.
//
// The startup sweep counts its candidates at activation and re-scans two
// minutes later. A workspace.setRoot inside that window re-points
// s.workspaceRoot, and a scan built from the CURRENT root alone would then
// reconcile a directory that is not the one the deferral was granted for: the
// launch root's orphans survive the only pass this process was ever going to
// give them, because nothing re-scans a root the workspace has moved away from.
func TestOrphanReconcile_TheLaunchRootIsScannedAfterASwitch(t *testing.T) {
	s, launchRoot, launchDir := reconcileServer(t)
	now := time.Now()

	orphan := staleSnapshot(t, launchDir, 570, newTestRunID(), now)

	switched := t.TempDir()
	setRoot := s.methods["workspace.setRoot"]
	if _, err := setRoot(t.Context(), []byte(`{"root":"`+switched+`"}`)); err != nil {
		t.Fatalf("workspace.setRoot: %v", err)
	}
	if got := s.workspaceRootPath(); got != switched {
		t.Fatalf("workspace root = %q, want the switched-to root", got)
	}
	if got := s.launchRootPath(); got != launchRoot {
		t.Fatalf("launch root = %q, want the constructed root %q — it is immutable", got, launchRoot)
	}

	// The deferred pass, arriving after the switch.
	s.reconcilePass(now)
	mustBeGone(t, orphan, "an orphan in the root this server launched in")
}
