package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// stageStartObservation is one sample of the on-disk world, taken from INSIDE a
// dispatch — the only moment at which a run's mid-flight state exists (the
// terminal tail removes both the sidecar and the snapshot).
type stageStartObservation struct {
	dispatch int
	// stage is the stage the scheduler is dispatching right now: the ground
	// truth every other field is compared against.
	stage state.PipelineStage
	// sidecarStage is current-run.json's view. Already correct today — it is
	// written at stage start — and included so a failure report shows the two
	// files disagreeing rather than just naming one of them.
	sidecarStage string
	snapshots    int
	snapshotOK   bool
	snapshotName state.PipelineStage
	lastComplete state.PipelineStage
}

func (o stageStartObservation) String() string {
	if !o.snapshotOK {
		return fmt.Sprintf("dispatch #%d: stage=%-18s sidecar.stage=%-18s snapshots=%d (no snapshot)",
			o.dispatch, o.stage, o.sidecarStage, o.snapshots)
	}
	return fmt.Sprintf("dispatch #%d: stage=%-18s sidecar.stage=%-18s snapshots=%d snapshot.stage=%-18s lastCompleted=%s",
		o.dispatch, o.stage, o.sidecarStage, o.snapshots, o.snapshotName, o.lastComplete)
}

// TestRunPipeline_PersistsTheRuntimeSnapshotAtStageStart is the #534 regression.
//
// The VS Code Pipeline tree mirrors a scheduler-owned run from
// runtime-{issue}-{runId}.json: CliPipelineReconciliationService reads
// current-run.json for the identity, composes the snapshot filename, and
// PipelineStateService.applyRuntimeSnapshot turns that snapshot into stage
// statuses. The snapshot used to be written ONLY after a stage completed, which
// produced two distinct defects for the whole life of a run:
//
//  1. During the FIRST stage no snapshot existed at all, so the reconciler
//     ENOENTed and the run was entirely absent from the tree until issue-pickup
//     finished.
//  2. From then on the snapshot lagged the live stage by exactly one — it named
//     the stage that had just COMPLETED. applyRuntimeSnapshot skips a stage that
//     is already in completedStages (correctly: that guard is what stops a
//     terminal snapshot flipping a finished stage back to running), so the live
//     stage was never marked running and showed as pending while the adapter was
//     actively working.
//
// Arm (c) is the one that actually pins the acceptance criterion. (a) and (b)
// can both be satisfied by a snapshot that names the just-completed stage on the
// NEXT dispatch if the loop is ever restructured; only "the persisted stage is
// not the last completed stage" states the invariant the extension depends on.
//
// The observation runs inside runIDCapturingRunner.onStage, documented at
// scheduler_run_identity_test.go as the only place a test can observe on-disk
// state a run tears down when it completes.
func TestRunPipeline_PersistsTheRuntimeSnapshotAtStageStart(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	const issue = 534
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")

	var observations []stageStartObservation
	runner.onStage = func() {
		calls := runner.captured()
		obs := stageStartObservation{
			dispatch: len(calls),
			stage:    calls[len(calls)-1].Stage,
		}
		if sc, err := readCurrentRunSidecar(root); err == nil && sc != nil {
			obs.sidecarStage = sc.Stage
		}
		found, err := state.FindPersistedStatesForIssue(stateDir, issue)
		if err != nil {
			t.Errorf("FindPersistedStatesForIssue: %v", err)
			return
		}
		obs.snapshots = len(found)
		if len(found) == 1 {
			obs.snapshotOK = true
			obs.snapshotName = found[0].Stage
			if n := len(found[0].CompletedStages); n > 0 {
				obs.lastComplete = found[0].CompletedStages[n-1].Stage
			}
		}
		observations = append(observations, obs)
	}

	item := types.BoardItem{Number: issue, Repo: "nightgauge/nightgauge", ID: "item-534"}
	s.runPipeline(context.Background(), item)

	if len(observations) == 0 {
		t.Fatal("no stage was dispatched; the fixture is wrong, not the assertion")
	}
	for _, obs := range observations {
		t.Log(obs)
	}

	for _, obs := range observations {
		// (a) The run is DISCOVERABLE at every dispatch, first stage included.
		// Zero snapshots is the tree-shows-nothing defect; two would mean the
		// run split its identity across files.
		if obs.snapshots != 1 {
			t.Errorf("%s\n  want exactly 1 persisted snapshot for #%d at every dispatch — 0 means the "+
				"reconciler ENOENTs and the run is invisible in the Pipeline tree", obs, issue)
			continue
		}

		// (b) The snapshot names the LIVE stage, and agrees with the sidecar.
		if obs.snapshotName != obs.stage {
			t.Errorf("%s\n  snapshot.stage = %q but the scheduler is dispatching %q — the snapshot lags "+
				"the live stage, so the tree shows the live stage as pending",
				obs, obs.snapshotName, obs.stage)
		}
		if obs.sidecarStage != string(obs.stage) {
			t.Errorf("%s\n  current-run.json stage = %q but the scheduler is dispatching %q",
				obs, obs.sidecarStage, obs.stage)
		}

		// (c) THE ARM THAT PINS THE AC: the persisted stage is never the stage
		// that just completed. applyRuntimeSnapshot's completedStages guard
		// makes such a snapshot a no-op for stage status.
		if obs.lastComplete != "" && obs.snapshotName == obs.lastComplete {
			t.Errorf("%s\n  snapshot.stage == the last completed stage (%q); applyRuntimeSnapshot skips a "+
				"stage already in completedStages, so this snapshot marks nothing running",
				obs, obs.lastComplete)
		}
	}
}

// TestRunPipeline_StageStartSnapshotDoesNotAssertADeadStageChild covers AC-1's
// pid half (#534).
//
// The stage-start persist writes a snapshot with a fresh mtime and a correct
// stage. Left alone, it would ALSO carry runtime.PID — which at stage start
// still holds the PREVIOUS stage's exited child, because SetProcess
// (internal/execution/manager.go) is the only writer on the scheduler path and
// it runs after cmd.Start(), after which the scheduler blocks until the stage
// exits. That would republish a dead pid with more confidence than before the
// fix: the liveness ladder's arm 3 asks whether the recorded pid is alive, and a
// recycled pid reads as a live run.
//
// So the persist clears it first. Zero means "no child is executing this run
// right now", which is exactly true at stage start — the same discipline the
// extension path already applies on a stage's terminal transition.
func TestRunPipeline_StageStartSnapshotDoesNotAssertADeadStageChild(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	const issue = 535
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")

	type pidSample struct {
		dispatch int
		stage    state.PipelineStage
		pid      int
	}
	// The pid a dispatch pretends its child got. Fixed and implausible so a
	// failure names the stale value rather than some ambient process.
	const fakeStageChildPID = 424242

	var samples []pidSample
	runner.onStage = func() {
		calls := runner.captured()
		params := calls[len(calls)-1]
		if found, err := state.FindPersistedStatesForIssue(stateDir, issue); err == nil && len(found) == 1 {
			samples = append(samples, pidSample{
				dispatch: len(calls),
				stage:    params.Stage,
				pid:      found[0].PID,
			})
		}
		// Stand in for execution.Manager.RunStage's SetProcess, which stamps the
		// child's pid on the runtime after cmd.Start(). Without this the fixture
		// would leave PID at zero for the whole run and the assertion below
		// would hold vacuously.
		if params.Runtime != nil {
			params.Runtime.SetProcess(fakeStageChildPID, "")
		}
	}

	item := types.BoardItem{Number: issue, Repo: "nightgauge/nightgauge", ID: "item-535"}
	s.runPipeline(context.Background(), item)

	if len(samples) == 0 {
		t.Fatal("no snapshot was observed at any dispatch; the stage-start persist is missing")
	}
	for _, sample := range samples {
		if sample.pid != 0 {
			t.Errorf("dispatch #%d (%s): stage-start snapshot carries pid %d — at stage start the only pid "+
				"the runtime has ever been given is the PREVIOUS stage's exited child, and republishing it "+
				"hands the liveness ladder a dead pid with a fresh mtime",
				sample.dispatch, sample.stage, sample.pid)
		}
	}
}
