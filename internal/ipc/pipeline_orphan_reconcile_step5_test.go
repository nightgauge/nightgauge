package ipc

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// The ADR-017 step-5 regression suite — the reconciler's rows of the Testing
// Strategy table. Each test's doc comment names the ADR failure ids it covers;
// the four rows the ADR marks "verified failing" were each run red against a
// scoped revert of the exact production rule they pin, and the revert is named
// in the comment so the next reader can reproduce it.

// --- fixtures ---------------------------------------------------------------

// reconcileServer builds a server whose scan root is a real directory.
// NewServer (not the bare &Server{} helper) because pipelineStateScanRoots goes
// through s.resolver, which only the constructor builds.
func reconcileServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	return s, root, filepath.Join(root, ".nightgauge", "pipeline")
}

// claimTokenAt mints a UUIDv7 whose 48-bit timestamp prefix names `when`,
// keeping the version and variant nibbles of a REAL identity from the
// production minter. The claim token is the one value in ADR-017 that is
// decoded, so a fixture that fabricated the whole string would be testing the
// test's idea of the layout.
func claimTokenAt(t *testing.T, when time.Time) string {
	t.Helper()
	base := newTestRunID()
	prefix := fmt.Sprintf("%012x", when.UnixMilli())
	token := prefix[0:8] + "-" + prefix[8:12] + base[13:]
	if !runstate.IsIdentity(token) {
		t.Fatalf("fabricated claim token %q is not a canonical identity", token)
	}
	return token
}

// writeClaimArtifact renames a paused snapshot to its claim name, exactly as
// the pause-restore claim does (ADR-017 Decision 9, step 8's producer).
func writeClaimArtifact(t *testing.T, stateDir string, issue int, runID, token string) string {
	t.Helper()
	paused := newInterruptedRuntime(issue, runID)
	paused.SetPaused(true)
	canonical := writeRuntimeSnapshot(t, stateDir, paused)
	artifact := filepath.Join(stateDir, runstate.ResumingArtifactName(issue, runID, token))
	if err := os.Rename(canonical, artifact); err != nil {
		t.Fatalf("claim rename: %v", err)
	}
	return artifact
}

// installRegistryEntry puts a run in the IPC registry with a chosen lease age.
// Reaching into activeRuntimes directly is the same seam the step-4 suite uses
// for schedulerRuns: the alternative is driving a verb, which would also stamp
// a FRESH lease and defeat every staleness assertion here.
func installRegistryEntry(t *testing.T, s *Server, rt *state.RuntimeState, lastSeen time.Time) *runEntry {
	t.Helper()
	e := newRunEntry(rt, rt.Repo, rt.IssueNumber)
	e.lastSeen = lastSeen
	e.firstSeen = lastSeen
	s.runtimesMu.Lock()
	s.activeRuntimes[rt.RunID] = e
	s.runtimesMu.Unlock()
	return e
}

func mustExist(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s: %s is gone (%v)", why, filepath.Base(path), err)
	}
}

func mustBeGone(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s: %s survived (stat err = %v)", why, filepath.Base(path), err)
	}
}

// --- the ladder -------------------------------------------------------------

// TestOrphanReconcile_LiveSchedulerRunIsNotReconciled covers F21.
//
// The Go scheduler persists into the SAME directories the IPC server scans and
// always stamps a run identity, and scheduler runs are never in activeRuntimes.
// So before arm 2 existed, every workspace.setRoot — fired on every
// onWorkspaceChanged — emitted a terminal pipeline_done for every LIVE scheduler
// run and removed its crash snapshot. This is not hypothetical: it is the
// default multi-repo behaviour on main.
//
// RED-FIRST: with serverEvidence's schedulerLive arm forced to false (the
// pre-step-5 predicate, which consulted only the IPC registry) this test fails
// at "a live scheduler run's snapshot".
func TestOrphanReconcile_LiveSchedulerRunIsNotReconciled(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()

	liveID := newTestRunID()
	deadID := newTestRunID()
	live := staleSnapshot(t, stateDir, 401, liveID, now)
	dead := staleSnapshot(t, stateDir, 402, deadID, now)

	sched := newFakeSchedulerRuns()
	sched.register(401, state.NewRuntimeState("nightgauge/acmeapp", 401, "", liveID))
	s.schedulerRuns = sched

	setRoot := s.methods["workspace.setRoot"]
	if _, err := setRoot(t.Context(), []byte(`{"root":"`+s.workspaceRootPath()+`"}`)); err != nil {
		t.Fatalf("workspace.setRoot: %v", err)
	}

	mustExist(t, live, "a live scheduler run's snapshot")
	mustBeGone(t, dead, "a run in NEITHER registry")
}

// TestOrphanReconcile_LiveLeaseIsNotReconciled pins arm 1 in both directions,
// and pins it PER RUN: the sibling dispatch of the same issue is reconciled in
// the same pass that skips the live one.
func TestOrphanReconcile_LiveLeaseIsNotReconciled(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()

	liveID := newTestRunID()
	staleID := newTestRunID()
	live := staleSnapshot(t, stateDir, 410, liveID, now)
	stale := staleSnapshot(t, stateDir, 410, staleID, now)

	liveRT := state.NewRuntimeState("nightgauge/acmeapp", 410, "", liveID)
	installRegistryEntry(t, s, liveRT, now.Add(-time.Minute))
	staleRT := state.NewRuntimeState("nightgauge/acmeapp", 410, "", staleID)
	installRegistryEntry(t, s, staleRT, now.Add(-2*livenessWindow))

	s.reconcilePass(now)

	mustExist(t, live, "a run whose lease is inside the window")
	mustBeGone(t, stale, "the same issue's run whose lease expired")
}

// TestOrphanReconcile_LivePidIsNotReconciled pins arm 3 for BOTH populations —
// a scheduler runtime whose PID came from SetProcess, and an extension runtime
// whose PID arrived as stagePid on a `running` transition. An arm no run in a
// population writes is not a fallback for that population (C18).
//
// Each population writes its snapshot THE WAY IT ACTUALLY DOES: the scheduler's
// through SetProcess (internal/execution/manager.go), the extension's through
// the real notifyStageTransition handler with the wire field set. A fixture that
// called rt.SetStageChild directly would pin the state layer and leave the
// handler — the only thing that turns a wire field into arm-3 evidence —
// unexercised, which is precisely the shape of F32.
func TestOrphanReconcile_LivePidIsNotReconciled(t *testing.T) {
	for _, tc := range []struct {
		name string
		// persist writes the run's snapshot with pid recorded as its stage
		// child, by that population's own route, and returns the path.
		persist func(t *testing.T, s *Server, stateDir string, issue int, runID string, pid int) string
	}{
		{"scheduler path (SetProcess)", func(t *testing.T, _ *Server, stateDir string, issue int, runID string, pid int) string {
			rt := newInterruptedRuntime(issue, runID)
			rt.SetProcess(pid, "/tmp/worktree")
			return writeRuntimeSnapshot(t, stateDir, rt)
		}},
		{"extension path (stagePid, through the real handler)", func(t *testing.T, s *Server, stateDir string, issue int, runID string, pid int) string {
			mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
				Repo: "nightgauge/acmeapp", IssueNumber: issue, Stage: "feature-dev", Status: "running",
				RunID: runID, StagePid: pid,
			})
			return filepath.Join(stateDir, state.SnapshotFilename(issue, runID))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, stateDir := reconcileServer(t)
			now := time.Now()

			// This test process is indisputably live.
			alivePath := tc.persist(t, s, stateDir, 420, newTestRunID(), os.Getpid())
			// A pid that cannot be running: ProcessAlive refuses pid <= 0, which is
			// also what stagePid: 0 produces at stage end.
			deadPath := tc.persist(t, s, stateDir, 421, newTestRunID(), 0)
			backdate(t, alivePath, now.Add(-2*livenessWindow))
			backdate(t, deadPath, now.Add(-2*livenessWindow))
			// The handler stamps a fresh lease; arm 1 would then keep BOTH files
			// and the test would assert nothing about arm 3.
			ageAllLeases(s, now.Add(-2*livenessWindow))

			s.reconcilePass(now)

			mustExist(t, alivePath, "a snapshot whose recorded pid is a live process")
			mustBeGone(t, deadPath, "a snapshot with no live child, both registries empty")
		})
	}
}

// TestOrphanReconcile_SilentExtensionRunInLongCiSurvivesGraceExpiry covers F32
// and C18 — the F26 trace that SURVIVED the deferred sweep, end to end.
//
// The profile is ordinary, not exotic: a run in pr-merge polling CI emits no
// assistant tokens for the whole grace window, so arm 1 never fires; it is an
// extension-path run, so arm 2 is structurally false; its last stage transition
// persisted more than livenessWindow ago, so arm 4 is stale; the grace expires,
// so arm 5 is false. Without a pid on the extension path every arm is false and
// a live run is reconciled at T+120s.
//
// RED-FIRST: with `rt.SetStageChild(p.StagePid)` removed from
// notifyStageTransition (the tree step 3 shipped the wire field into) the
// snapshot carries PID 0 and this test fails at "the long-CI run".
func TestOrphanReconcile_SilentExtensionRunInLongCiSurvivesGraceExpiry(t *testing.T) {
	s, root, stateDir := reconcileServer(t)
	now := time.Now()
	runID := newTestRunID()

	// The run's last stage transition — 40 minutes ago, carrying the pid of the
	// child that is still sitting in `gh pr checks --watch`.
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "nightgauge/acmeapp", IssueNumber: 430, Stage: "pr-merge", Status: "running",
		RunID: runID, StagePid: os.Getpid(),
	})
	snapshot := filepath.Join(stateDir, state.SnapshotFilename(430, runID))
	mustExist(t, snapshot, "the transition must persist")
	backdate(t, snapshot, now.Add(-40*time.Minute))

	// The backend restarted: both registries are empty in the new process.
	fresh := NewServer(nil, WithWorkspaceRoot(root))
	if got := len(fresh.activeRuntimes); got != 0 {
		t.Fatalf("a restarted server starts with %d entries, want 0", got)
	}
	fresh.reconcilePass(now) // grace never armed on this one: expiry, not deferral

	mustExist(t, snapshot, "the long-CI run is alive and its stage child says so")

	// The same snapshot once the child is gone: every arm is false and it is
	// reconciled normally, which is what keeps arm 3 a skip rather than a shield.
	loaded, err := state.LoadSnapshotByIdentity(stateDir, 430, runID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	loaded.SetStageChild(0)
	if err := loaded.Persist(stateDir); err != nil {
		t.Fatalf("persist: %v", err)
	}
	backdate(t, snapshot, now.Add(-40*time.Minute))
	fresh.reconcilePass(now)
	mustBeGone(t, snapshot, "the same run once its stage child is gone")
}

// TestRunIdentity_StagePidIsClearedAtStageEnd bounds the PID-reuse window to one
// stage: a terminal stage transition sends stagePid 0 and the snapshot's PID
// must follow it down, so a finished child's recycled pid cannot vouch for the
// run. The same transition still persists, so arm 4 is fresh by construction —
// between stages arm 3 is false and arm 4 is true.
func TestRunIdentity_StagePidIsClearedAtStageEnd(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "nightgauge/acmeapp", IssueNumber: 440, Stage: "feature-dev", Status: "running",
		RunID: runID, StagePid: 4242,
	})
	if got := onlySnapshotForIssue(t, stateDir, 440).PID; got != 4242 {
		t.Fatalf("PID = %d after `running`, want 4242 — the wire's stagePid is not being recorded", got)
	}

	before := time.Now().Add(-time.Second)
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "nightgauge/acmeapp", IssueNumber: 440, Stage: "feature-dev", Status: "complete",
		RunID: runID, StagePid: 0,
	})
	if got := onlySnapshotForIssue(t, stateDir, 440).PID; got != 0 {
		t.Fatalf("PID = %d after the stage's terminal transition, want 0", got)
	}
	info, err := os.Stat(filepath.Join(stateDir, state.SnapshotFilename(440, runID)))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.ModTime().Before(before) {
		t.Fatal("the terminal stage transition must still persist — arm 4 is what covers the gap between stages")
	}
}

// A scheduler-owned runtime's PID belongs to SetProcess, written from the
// scheduler's own process tree (internal/execution/manager.go). The extension's
// stagePid arriving on a read-through call must not clobber it: that would
// replace the scheduler population's arm-3 evidence with a pid from another
// tree, which is a false answer in both directions.
func TestRunIdentity_StagePidNeverClobbersASchedulerOwnedRuntime(t *testing.T) {
	s, _, _ := reconcileServer(t)
	runID := newTestRunID()

	owned := state.NewRuntimeState("nightgauge/acmeapp", 441, "", runID)
	owned.SetProcess(9999, "/tmp/worktree")
	sched := newFakeSchedulerRuns()
	sched.register(441, owned)
	s.schedulerRuns = sched

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "nightgauge/acmeapp", IssueNumber: 441, Stage: "feature-dev", Status: "running",
		RunID: runID, StagePid: 1234,
	})

	if got := owned.StageChildPID(); got != 9999 {
		t.Fatalf("scheduler runtime PID = %d, want 9999 — the read-through arm must not write the caller's pid", got)
	}
}

// --- the deferred startup sweep --------------------------------------------

// TestOrphanReconcile_StartupDefersAndReEvaluates covers F26.
//
// The trigger is an ENGINEERED AUTO-BEHAVIOUR, not a rare crash: the client
// restarts the Go backend on process exit (5 attempts, 2000ms · 2^(n-1) backoff)
// while the extension host and all its in-flight runs survive. An inline sweep
// at Server.Run therefore runs with both registries empty against runs that are
// alive, emits their terminal event, removes their snapshots, and leaves the
// eventual notifyComplete writing a skeleton record with no routing signal.
//
// RED-FIRST: with startDeferredReconcileAfter's body replaced by
// `s.reconcileOrphanedRuns()` — literally what Server.Run did before step 5 —
// this test fails at "a snapshot present at Server.Run".
func TestOrphanReconcile_StartupDefersAndReEvaluates(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()

	silentID := newTestRunID()
	reconnectID := newTestRunID()
	silent := staleSnapshot(t, stateDir, 450, silentID, now)
	reconnect := staleSnapshot(t, stateDir, 451, reconnectID, now)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.startDeferredReconcile(ctx)

	if !s.withinStartupGrace() {
		t.Fatal("Server.Run must arm the startup grace")
	}
	// A workspace.setRoot landing inside the window defers exactly like the
	// startup pass — arm 5 is a server-level predicate, not a local of the
	// startup goroutine.
	s.reconcilePass(now)
	mustExist(t, silent, "a snapshot present at Server.Run, during the grace window")
	mustExist(t, reconnect, "a snapshot present at Server.Run, during the grace window")

	// Run 451 reconnects during the window; run 450 stays silent.
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "nightgauge/acmeapp", IssueNumber: 451, Stage: "feature-dev", Status: "running",
		RunID: reconnectID,
	})

	// Expiry: the set is re-evaluated FROM SCRATCH against the full ladder.
	s.startupGraceUntil.Store(time.Now().Add(-time.Second).UnixNano())
	s.reconcilePass(time.Now())

	mustExist(t, reconnect, "a run that re-asserted during the grace window")
	mustBeGone(t, silent, "a run that stayed silent through the grace window")

	// And the timer itself fires: same wiring, a grace short enough to observe.
	timed, _, timedDir := reconcileServer(t)
	doomed := staleSnapshot(t, timedDir, 452, newTestRunID(), time.Now())
	timed.startDeferredReconcileAfter(ctx, 50*time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(doomed); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deferred sweep never fired — the timer is the only thing that runs it")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The same defect at the PRODUCTION seam: Server.Run itself. The stdio loop is
// what `nightgauge serve` calls (cmd/nightgauge/main.go), and it is where the
// inline sweep stood, so the wiring is pinned here rather than only on the
// helper Run delegates to.
func TestOrphanReconcile_ServerRunArmsTheGraceInsteadOfSweepingInline(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	s.writer = io.Discard // ipc.ready would otherwise land in the test log
	live := staleSnapshot(t, stateDir, 453, newTestRunID(), time.Now())

	// Run reads os.Stdin directly; the pipe lets the loop end on demand instead
	// of inheriting whatever the test runner attached.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = original }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		_ = s.Run(ctx)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !s.withinStartupGrace() {
		if time.Now().After(deadline) {
			t.Fatal("Server.Run never armed the startup grace")
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.Close()
	<-returned

	mustExist(t, live, "a live run's snapshot at Server.Run")
}

// The timer takes Run's ctx because there is no Close/Stop/Shutdown on *Server:
// `nightgauge serve` cancels it from the SIGTERM handler that also ends the
// stdio loop. A cancelled ctx must leave the directory alone.
func TestOrphanReconcile_DeferredSweepIsCancelledWithTheServerContext(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	survivor := staleSnapshot(t, stateDir, 460, newTestRunID(), time.Now())

	ctx, cancel := context.WithCancel(t.Context())
	s.startDeferredReconcileAfter(ctx, 50*time.Millisecond)
	cancel()

	time.Sleep(300 * time.Millisecond)
	mustExist(t, survivor, "a sweep whose server was shut down before expiry")
}

// --- the disposition table (7.4) -------------------------------------------

// TestOrphanReconcile_PausedAndIdentityLessSnapshotsStillSkipped pins C1/C5
// preservation and the one row that overrides them: the 14-day cap, which does
// remove a paused snapshot nobody resumed.
func TestOrphanReconcile_PausedAndIdentityLessSnapshotsStillSkipped(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()

	freshPause := newInterruptedRuntime(470, newTestRunID())
	freshPause.SetPaused(true)
	freshPath := writeRuntimeSnapshot(t, stateDir, freshPause)
	// Days old — far outside the liveness window, far inside the age cap.
	backdate(t, freshPath, now.Add(-72*time.Hour))

	oldPause := newInterruptedRuntime(471, newTestRunID())
	oldPause.SetPaused(true)
	oldPath := writeRuntimeSnapshot(t, stateDir, oldPause)
	backdate(t, oldPath, now.Add(-snapshotAgeCap-time.Hour))

	// No identity in the name: the Go legacy sweep owns these (Migration), and
	// this reconciler must not touch them.
	legacy := filepath.Join(stateDir, "runtime-472.json")
	if err := os.WriteFile(legacy, []byte(`{"issueNumber":472,"runId":"legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(stateDir, "run-state.json")
	if err := os.WriteFile(sidecar, []byte(`{"state":"running"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	backdate(t, legacy, now.Add(-snapshotAgeCap-time.Hour))
	backdate(t, sidecar, now.Add(-snapshotAgeCap-time.Hour))

	// The capped pause is emitted for before it goes: it was never reconciled.
	acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)
	if len(acts) != 1 {
		t.Fatalf("got %d actions %+v, want exactly the capped pause", len(acts), acts)
	}
	if acts[0].Disposition != dispositionEmitAndRemove {
		t.Fatalf("capped pause disposition = %s, want emit+remove", acts[0].Disposition)
	}

	s.reconcilePass(now)

	mustExist(t, freshPath, "a paused snapshot inside the age cap (the restore prompt reads it)")
	mustExist(t, legacy, "an identity-less legacy snapshot")
	mustExist(t, sidecar, "an unrelated file")
	mustBeGone(t, oldPath, "a pause nobody resumed in two weeks")
}

// TestOrphanReconcile_FreshAbandonedSnapshotSurvives pins 7.4's two abandoned
// rows, which the F4 correction split apart: abandonment describes a DISPATCH,
// and a fresh abandoned run may still be streaming, so its crash snapshot stays
// and a stage transition arriving afterwards still finds its file.
//
// Both rows are production-inert until step 6 gives `abandoned` a writer.
func TestOrphanReconcile_FreshAbandonedSnapshotSurvives(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()

	freshID := newTestRunID()
	fresh := newInterruptedRuntime(480, freshID)
	fresh.MarkAbandoned(now.Add(-time.Minute), "force-clear")
	freshPath := writeRuntimeSnapshot(t, stateDir, fresh)
	backdate(t, freshPath, now.Add(-2*livenessWindow))

	stale := newInterruptedRuntime(481, newTestRunID())
	stale.MarkAbandoned(now.Add(-2*livenessWindow), "force-clear")
	stalePath := writeRuntimeSnapshot(t, stateDir, stale)
	backdate(t, stalePath, now.Add(-2*livenessWindow))

	// The stale one is removed WITHOUT emitting: abandonRun already emitted the
	// dispatch-terminal event, and a second one would double-book the run.
	acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)
	if len(acts) != 1 || acts[0].Disposition != dispositionRemove {
		t.Fatalf("got %+v, want exactly one remove-without-emitting", acts)
	}

	s.reconcilePass(now)
	mustExist(t, freshPath, "an abandoned dispatch whose run may still be streaming")
	mustBeGone(t, stalePath, "an abandoned dispatch outside the liveness window")

	// The file is still there for the live run's next write.
	if err := fresh.Persist(stateDir); err != nil {
		t.Fatalf("a fresh abandoned run must still own its snapshot: %v", err)
	}
}

// TestOrphanReconcile_ClosesAbandonedRunAtRootSwitch covers F11: a STALE
// registry entry no longer pins a snapshot.
//
// On main the skip was "does this ISSUE have any non-terminal entry", so an
// entry whose lease had not moved in hours — an abandoned dispatch's, an
// adopt-empty stub's — made this case dead code. The ladder asks the entry for
// its lease instead, and the pass reaps the entry itself.
func TestOrphanReconcile_ClosesAbandonedRunAtRootSwitch(t *testing.T) {
	s, root, stateDir := reconcileServer(t)
	now := time.Now()

	runID := newTestRunID()
	snapshot := staleSnapshot(t, stateDir, 490, runID, now)

	rt := state.NewRuntimeState("nightgauge/acmeapp", 490, "", runID)
	rt.MarkAbandoned(now.Add(-2*livenessWindow), "force-clear")
	installRegistryEntry(t, s, rt, now.Add(-2*livenessWindow))

	// The on-disk snapshot is the PRE-abandon crash snapshot — abandonRun marks
	// the runtime, it does not rewrite the file — so its table row is the
	// ordinary orphan's: emit, then remove.
	acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)
	if len(acts) != 1 || acts[0].Disposition != dispositionEmitAndRemove {
		t.Fatalf("got %+v, want one emit+remove", acts)
	}

	setRoot := s.methods["workspace.setRoot"]
	if _, err := setRoot(t.Context(), []byte(`{"root":"`+root+`"}`)); err != nil {
		t.Fatalf("workspace.setRoot: %v", err)
	}

	mustBeGone(t, snapshot, "a snapshot pinned only by a stale registry entry")
	s.runtimesMu.Lock()
	_, stillThere := s.activeRuntimes[runID]
	s.runtimesMu.Unlock()
	if stillThere {
		t.Error("the stale entry must be reaped: adopt-empty growth is otherwise unbounded")
	}
}

// The classifier is pure, so the table is a table.
func TestClassifyCandidate_TableIsEvaluatedTopToBottom(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-2 * livenessWindow)
	abandonedAt := now.Add(-2 * livenessWindow)
	freshAbandonedAt := now.Add(-time.Minute)

	terminal := &state.RuntimeState{RunID: "r", Terminal: true}
	ordinary := &state.RuntimeState{RunID: "r"}
	paused := &state.RuntimeState{RunID: "r", Paused: true}
	abandoned := &state.RuntimeState{RunID: "r", Abandoned: true, AbandonedAt: &abandonedAt}
	freshAbandoned := &state.RuntimeState{RunID: "r", Abandoned: true, AbandonedAt: &freshAbandonedAt}
	undatedAbandoned := &state.RuntimeState{RunID: "r", Abandoned: true}

	live := runEvidence{leaseFresh: func(string) bool { return true }}
	grace := runEvidence{withinGrace: func() bool { return true }}

	for _, tc := range []struct {
		name string
		c    reconcileCandidate
		ev   runEvidence
		want disposition
	}{
		{"terminal outranks a live entry", reconcileCandidate{snap: terminal, modTime: now}, live, dispositionRemove},
		{"terminal outranks the startup grace", reconcileCandidate{snap: terminal, modTime: stale}, grace, dispositionRemove},
		{"skipRun keeps an ordinary orphan", reconcileCandidate{snap: ordinary, modTime: stale}, live, dispositionKeep},
		{"the grace keeps an ordinary orphan", reconcileCandidate{snap: ordinary, modTime: stale}, grace, dispositionKeep},
		{"a fresh file keeps itself (arm 4)", reconcileCandidate{snap: ordinary, modTime: now}, runEvidence{}, dispositionKeep},
		{"ordinary orphan", reconcileCandidate{snap: ordinary, modTime: stale}, runEvidence{}, dispositionEmitAndRemove},
		{"fresh abandonment keeps the crash snapshot", reconcileCandidate{snap: freshAbandoned, modTime: stale}, runEvidence{}, dispositionKeep},
		{"stale abandonment removes without emitting", reconcileCandidate{snap: abandoned, modTime: stale}, runEvidence{}, dispositionRemove},
		{"an unageable abandonment is treated as fresh", reconcileCandidate{snap: undatedAbandoned, modTime: stale}, runEvidence{}, dispositionKeep},
		// "Unageable" must not mean "immortal": 7.4's last row says ANYTHING
		// past the cap goes, and an undated abandonment is no evidence that
		// abandonRun emitted, so this row emits before it removes.
		{"an unageable abandonment past the cap is still collected", reconcileCandidate{snap: undatedAbandoned, modTime: now.Add(-snapshotAgeCap - time.Hour)}, runEvidence{}, dispositionEmitAndRemove},
		{"a fresh pause is exempt", reconcileCandidate{snap: paused, modTime: now.Add(-72 * time.Hour)}, runEvidence{}, dispositionKeep},
		{"a capped pause is not", reconcileCandidate{snap: paused, modTime: now.Add(-snapshotAgeCap - time.Hour)}, runEvidence{}, dispositionEmitAndRemove},
		{"a live claim is untouched", reconcileCandidate{claim: true, claimAgeKnown: true, claimAge: time.Second}, runEvidence{}, dispositionKeep},
		{"an unageable claim is untouched", reconcileCandidate{claim: true}, runEvidence{}, dispositionKeep},
		{"a stale claim is released", reconcileCandidate{claim: true, claimAgeKnown: true, claimAge: startupGrace + time.Second}, runEvidence{}, dispositionReleaseClaim},
		// Arm 5 and only arm 5 reaches the claim rows: the startup grace says
		// this process has not been up long enough to have heard from the
		// claimant, which is the one thing THIS server's evidence can say about
		// another host's working state.
		{"the startup grace defers a stale claim", reconcileCandidate{claim: true, claimAgeKnown: true, claimAge: startupGrace + time.Second}, grace, dispositionKeep},
		{"a live lease does NOT defer a stale claim", reconcileCandidate{claim: true, claimAgeKnown: true, claimAge: startupGrace + time.Second}, live, dispositionReleaseClaim},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCandidate(tc.c, tc.ev, now); got != tc.want {
				t.Errorf("disposition = %s, want %s", got, tc.want)
			}
		})
	}
}

// --- the claim-artifact release (Decision 9) --------------------------------

// TestOrphanReconcile_ResumingArtifactAgesFromTheClaimToken covers F34 and C17.
//
// Constructed so the mtime and the claim token DISAGREE, which is not a
// contrived case but the only case: rename(2) updates the inode's st_ctime and
// the two directories' st_mtime, never the renamed file's own st_mtime, so a
// claim artifact inherits the mtime of the PAUSED SNAPSHOT — and a pause is by
// construction read at a later activation, minutes to days after it was written.
// An mtime-aged claim is therefore born releasable, and releasing it renames
// stale paused content back over a live run's canonical snapshot.
//
// RED-FIRST: with claimAgeOf reading `info.ModTime()` instead of decoding the
// token, this test fails at "a claim taken two seconds ago".
func TestOrphanReconcile_ResumingArtifactAgesFromTheClaimToken(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()

	freshID := newTestRunID()
	freshArtifact := writeClaimArtifact(t, stateDir, 500, freshID, claimTokenAt(t, now.Add(-2*time.Second)))
	// The paused snapshot this claim renamed was written days ago, and the
	// rename did not touch its mtime.
	backdate(t, freshArtifact, now.Add(-72*time.Hour))

	staleID := newTestRunID()
	staleArtifact := writeClaimArtifact(t, stateDir, 501, staleID, claimTokenAt(t, now.Add(-startupGrace-time.Minute)))
	// And this one's file is BRAND NEW, so an mtime rule would keep exactly the
	// claim that must be released.
	backdate(t, staleArtifact, now)

	s.reconcilePass(now)

	mustExist(t, freshArtifact, "a claim taken two seconds ago, on a days-old file")
	mustBeGone(t, staleArtifact, "a claim taken before the grace window, on a brand-new file")
	mustExist(t, filepath.Join(stateDir, state.SnapshotFilename(501, staleID)),
		"the released claim's pause must survive under the canonical name")
}

// TestOrphanReconcile_ReleaseNeverOverwritesAnOccupiedCanonicalName pins
// Decision 9's release table. The "canonical present" row is not an edge case:
// it is exactly the window between the resumed run's first Persist and the
// claimant's delete, and a host killed inside that window leaves precisely this
// pair. The canonical file always wins because its content is strictly newer.
func TestOrphanReconcile_ReleaseNeverOverwritesAnOccupiedCanonicalName(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()
	runID := newTestRunID()
	staleToken := claimTokenAt(t, now.Add(-startupGrace-time.Minute))

	artifact := writeClaimArtifact(t, stateDir, 510, runID, staleToken)

	// The claimant won and resumed: the run re-persisted under the canonical
	// name, and its snapshot is NOT paused.
	resumed := newInterruptedRuntime(510, runID)
	resumed.BeginStage(state.StagePRCreate)
	canonical := writeRuntimeSnapshot(t, stateDir, resumed)
	live := readFileBytes(t, canonical)

	s.reconcilePass(now)

	mustBeGone(t, artifact, "a superseded claim artifact")
	if got := readFileBytes(t, canonical); got != live {
		t.Fatal("the live run's canonical snapshot was overwritten by the pre-pause content (F34)")
	}
	reloaded, err := state.LoadSnapshotByIdentity(stateDir, 510, runID)
	if err != nil {
		t.Fatalf("reload canonical: %v", err)
	}
	if reloaded.Paused {
		t.Fatal("the canonical snapshot re-advertises paused:true for a LIVE run id — the next host to scan would prompt and win a rename that must be impossible (F28)")
	}

	// Canonical ABSENT is the other row: a claimant that died before its first
	// persist releases its claim, and the pause survives to the next activation.
	orphanID := newTestRunID()
	crashed := writeClaimArtifact(t, stateDir, 511, orphanID, claimTokenAt(t, now.Add(-startupGrace-time.Minute)))
	s.reconcilePass(now)
	mustBeGone(t, crashed, "a crashed claim's artifact")
	restored, err := state.LoadSnapshotByIdentity(stateDir, 511, orphanID)
	if err != nil {
		t.Fatalf("the released claim must be back under the canonical name: %v", err)
	}
	if !restored.Paused {
		t.Fatal("the released pause must still read as paused, or the restore prompt has nothing to offer")
	}
}

// TestOrphanReconcile_UnparseableOrFutureClaimTokenIsTreatedAsLive pins the
// fail-safe direction (C13). A claim we cannot age is a claim we do not touch:
// the cost is a paused run needing one more activation to re-prompt; the cost of
// the other direction is two live dispatches under one identity.
func TestOrphanReconcile_UnparseableOrFutureClaimTokenIsTreatedAsLive(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()

	future := writeClaimArtifact(t, stateDir, 520, newTestRunID(), claimTokenAt(t, now.Add(claimSkewTolerance+time.Minute)))
	backdate(t, future, now.Add(-72*time.Hour))

	// A token that is not an identity does not make a claim artifact at all: the
	// name simply does not match, and the file is left alone (Decision 8).
	unparseable := filepath.Join(stateDir, "resuming-521-"+newTestRunID()+".not-a-uuid.json")
	if err := os.WriteFile(unparseable, []byte(`{"runId":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	backdate(t, unparseable, now.Add(-72*time.Hour))

	s.reconcilePass(now)

	mustExist(t, future, "a claim token ahead of this reader's clock")
	mustExist(t, unparseable, "a file whose token is not an identity")

	// Inside the tolerance a forward-skewed token is aged normally rather than
	// being treated as unageable — the tolerance is one-sided, not a second
	// threshold added to the grace.
	if _, ok := claimAgeOf(claimTokenAt(t, now.Add(claimSkewTolerance/2)), now); !ok {
		t.Error("a token inside claimSkewTolerance must still be ageable")
	}
	if _, ok := claimAgeOf("not-a-uuid", now); ok {
		t.Error("an undecodable token must never be releasable")
	}
}

func readFileBytes(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return string(b)
}
