package ipc

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// reapedPID returns a PID that has certainly exited: a real child, run to
// completion and reaped. An invented "large number" pid is not reliably dead —
// the kernel recycles — which is the same reason internal/runstate's sidecar
// suite builds its dead pid this way rather than hard-coding one.
//
// The recycle precondition is checked with a RAW `kill(pid, 0)` rather than with
// `runstate.ProcessAlive`, because ProcessAlive is the predicate the test exists
// to exercise: asking it whether the pid is usable would let an arm-3 regression
// (dead pids reading alive) turn this test into a silent SKIP, and the package
// would still print `ok`.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run throwaway child: %v", err)
	}
	pid := cmd.Process.Pid

	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		// pid genuinely occupied (recycled, possibly into another user's
		// process) — cannot serve as a dead pid. EPERM means a process exists
		// that we may not signal; it is NOT free.
		t.Skipf("pid %d was recycled before the test could use it as a dead pid", pid)
	}
	// Kernel says the pid is free (ESRCH). If ProcessAlive disagrees, that IS the
	// arm-3 regression this test exists to catch — fail loudly, never skip.
	if runstate.ProcessAlive(pid) {
		t.Fatalf("ProcessAlive(%d) calls a reaped pid alive — ladder arm 3 would pin such a run forever", pid)
	}
	return pid
}

// TestOrphanReconcile_ExtensionRunWithReapedStageChildReconcilesPastGrace pins
// the failure mode #427's deleted TypeScript scanner claimed: the extension
// host died mid-run and left a stage marked "running" with a recorded child pid
// that has since exited.
//
// Every existing ladder test in which arm 3 must answer FALSE reaches it with
// pid 0 — the stage-end sentinel, which `runstate.ProcessAlive` refuses on the
// `pid <= 0` guard before it ever makes a syscall. That pins "no pid recorded";
// it does not pin "the pid that WAS recorded names a process that died", which
// is the exact input the deleted scanner keyed on (`process_pid`,
// `kill(pid, 0)`).
//
// What this file pins is precisely the DEAD-recorded-pid direction: a recorded,
// once-live, now-reaped pid must still reconcile past the grace. It does not on
// its own discriminate arm 3 — with arms 1, 2 and 4 false, deleting arm 3
// outright leaves this test green. The opposite direction, arm 3 KEEPING a run
// whose recorded pid is alive, is pinned by
// TestOrphanReconcile_LivePidIsNotReconciled and
// TestOrphanReconcile_InteractiveShapedRunSurvivesOnItsStagePidAlone. Together
// the two directions discriminate the arm; this file deliberately does not
// duplicate the alive-direction pin.
func TestOrphanReconcile_ExtensionRunWithReapedStageChildReconcilesPastGrace(t *testing.T) {
	s, _, stateDir := reconcileServer(t)
	now := time.Now()
	runID := newTestRunID()
	dead := reapedPID(t)

	// The extension path's own route: the run's last stage transition, carrying
	// the pid of the child the extension host spawned.
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "nightgauge/acmeapp", IssueNumber: 427, Stage: "feature-dev", Status: "running",
		RunID: runID, StagePid: dead,
	})
	snapshot := filepath.Join(stateDir, state.SnapshotFilename(427, runID))
	mustExist(t, snapshot, "the transition must persist")

	loaded, err := state.LoadSnapshotByIdentity(stateDir, 427, runID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := loaded.StageChildPID(); got != dead {
		t.Fatalf("StageChildPID = %d, want the reaped pid %d — arm 3 has nothing to answer", got, dead)
	}

	// Arm 4 (disk-side lease) and arm 1 (this server's registry) made false.
	// Arm 2 is structurally false: schedulerRuns is nil on this server.
	backdate(t, snapshot, now.Add(-2*livenessWindow))
	ageAllLeases(s, now.Add(-2*livenessWindow))

	// Arm 5 armed: an orphan is NOT swept during the reconnect window, however
	// dead its child is.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.startDeferredReconcile(ctx)
	s.reconcilePass(now)
	mustExist(t, snapshot, "an orphan inside the startup grace")

	// Grace expires. Every arm is now false — including arm 3, which had a pid
	// to probe and got a dead one — so the run is an ordinary orphan: exactly
	// one terminal pipeline_done(success=false), then removal.
	s.startupGraceUntil.Store(time.Now().Add(-time.Second).UnixNano())
	acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)
	if len(acts) != 1 {
		t.Fatalf("got %d reconcile actions, want 1", len(acts))
	}
	if acts[0].Disposition != dispositionEmitAndRemove {
		t.Fatalf("disposition = %s, want emit+remove", acts[0].Disposition)
	}
	if ev := acts[0].Event; ev.EventType != "pipeline_done" || ev.RunID != runID ||
		ev.Success == nil || *ev.Success {
		t.Fatalf("event = %+v, want pipeline_done for %s with success=false", ev, runID)
	}

	s.reconcilePass(time.Now())
	mustBeGone(t, snapshot, "an orphaned extension-path run whose stage child is reaped, past grace")
}
