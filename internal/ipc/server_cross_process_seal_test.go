package ipc

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// #557 — a scheduler-owned run's snapshot must not be resurrected after a
// CROSS-PROCESS seal.
//
// ADR-017 Decision 5's terminal latch has two halves and neither one closes
// this: `sealed` is per-RUNTIME-OBJECT and so per-PROCESS, and the durable
// `terminal` marker is REMOVED by the very operation that writes it. Decision
// 3's `run_wrong_owner` does refuse a terminal verb against a scheduler-owned
// run — but only against a scheduler THIS process can see, so a `nightgauge
// run` scheduler observed by a separate `serve` daemon is refused nothing.
//
// The reachable sequence, and the one these tests walk end to end:
//
//	1. a CLI-started run persists a non-terminal snapshot from its own process;
//	2. a separate IPC server, knowing nothing of it, rehydrates that snapshot
//	   through loadRunSnapshot;
//	3. a notifyComplete there runs the whole terminal claim and reaches
//	   SealAndRemove — on a file whose owner holds no seal;
//	4. the owner's next stage-boundary persist (#534 made that twice a stage)
//	   re-creates it.
//
// The fix makes ownership an identity-scoped fact the snapshot carries, so step
// 3 declines. Step 4 then updates a file that never went away.

// deadPID returns a pid that has certainly exited: a real child, reaped.
// Invented "large number" pids are not reliably dead — the kernel recycles.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run throwaway child: %v", err)
	}
	pid := cmd.Process.Pid
	if runstate.ProcessAlive(pid) {
		t.Skipf("pid %d was recycled before the test could use it as a dead pid", pid)
	}
	return pid
}

// liveForeignPID returns a pid that is alive and is NOT this process — the test
// binary's parent, which outlives every test in the binary. It stands in for
// the `nightgauge run` scheduler process that owns the run while the server
// under test only observes it.
func liveForeignPID(t *testing.T) int {
	t.Helper()
	pid := os.Getppid()
	if pid <= 1 || pid == os.Getpid() || !runstate.ProcessAlive(pid) {
		t.Skipf("no live foreign pid available for this test (ppid %d)", pid)
	}
	return pid
}

// TestRunIdentity_AdoptedSealCannotResurrectALiveOwnersRun is the #557
// acceptance pin: adopt → seal → the owner's next persist, with the assertion
// placed BETWEEN the seal and that persist.
//
// The end state of the fixed and the unfixed tree look alike — a non-terminal
// snapshot on disk — because resurrection restores exactly what the seal
// removed. What separates them is whether the file survived step 3, which is
// why the load-bearing assertion is taken there and not at the end.
func TestRunIdentity_AdoptedSealCannotResurrectALiveOwnersRun(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	const (
		repo  = "acme/platform"
		issue = 557
	)
	runID := newTestRunID()

	// 1. THE OWNER: a CLI scheduler in another process, mid-run and unsealed.
	owner := state.NewRuntimeState(repo, issue, "", runID)
	owner.OwnerPID = liveForeignPID(t)
	owner.BeginStage(state.StageFeatureDev)
	if err := owner.Persist(stateDir); err != nil {
		t.Fatalf("owner's stage-start persist: %v", err)
	}

	// 2. + 3. THE OBSERVER: a separate IPC server whose registries have never
	// heard of this run. notifyComplete resolves it, misses both registries,
	// rehydrates the snapshot and runs the terminal claim over it.
	observer := NewServer(nil, WithWorkspaceRoot(root))
	mustCall(t, observer, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1000, RunID: runID,
	})

	live, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
	if err != nil {
		t.Fatalf("the live owner's snapshot was SEALED AWAY by an observing process (%v) — the owner's next persist would now RE-CREATE it, which is the resurrection #557 is about", err)
	}
	if live.Terminal {
		t.Error("an observing process stamped the durable terminal marker onto a run another process is still driving")
	}
	if live.OwnerPID != owner.OwnerPID {
		t.Errorf("the observed snapshot names owner pid %d, want the live owner %d — adoption must not take a live owner's run",
			live.OwnerPID, owner.OwnerPID)
	}
	if live.Stage != state.StageFeatureDev {
		t.Errorf("snapshot stage = %q, want %q — the observer overwrote the owner's content", live.Stage, state.StageFeatureDev)
	}

	// 4. THE OWNER'S NEXT STAGE BOUNDARY. It is not sealed, its run is not
	// over, and it writes exactly as it always did — but what it writes is an
	// UPDATE to a file that never went away, not a resurrection of one a
	// foreign process removed.
	owner.BeginStage(state.StagePRCreate)
	if err := owner.Persist(stateDir); err != nil {
		t.Fatalf("the owner's next stage-boundary persist was refused: %v", err)
	}
	after, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
	if err != nil {
		t.Fatalf("LoadSnapshotByIdentity after the owner's persist: %v", err)
	}
	if after.Stage != state.StagePRCreate {
		t.Errorf("snapshot stage = %q, want %q", after.Stage, state.StagePRCreate)
	}
	if after.Terminal {
		t.Error("the owner's own persist wrote a terminal snapshot")
	}
}

// TestRunIdentity_AGoneOwnersRunIsStillSealedByItsAdopter is the negative pin,
// and it is what stops the refusal above from becoming a leak: the refusal is
// scoped to LIVENESS, so a run whose owner has genuinely died is adopted,
// sealed and closed exactly as before. An implementation that refuses every
// adopted seal — "never seal what you did not mint" — passes the test above and
// fails this one.
func TestRunIdentity_AGoneOwnersRunIsStillSealedByItsAdopter(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	const (
		repo  = "acme/platform"
		issue = 558
	)
	runID := newTestRunID()

	orphan := state.NewRuntimeState(repo, issue, "", runID)
	orphan.OwnerPID = deadPID(t)
	orphan.BeginStage(state.StageFeatureDev)
	if err := orphan.Persist(stateDir); err != nil {
		t.Fatalf("orphan persist: %v", err)
	}

	s := NewServer(nil, WithWorkspaceRoot(root))
	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1000, RunID: runID,
	})

	if _, err := state.LoadSnapshotByIdentity(stateDir, issue, runID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("snapshot lookup after the claim = %v, want fs.ErrNotExist — a dead owner's run must still be sealed, or the ownership refusal strands it", err)
	}
	// And the run is genuinely closed, not merely file-less.
	wantRefusal(t, callRunVerb(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID,
	}), codeRunClosed)
}

// TestRunIdentity_AdoptionTakesOwnershipOnlyFromAGoneOwner pins loadRunSnapshot
// itself, through the run-progress verb that reaches it.
//
// Both arms are SERVED: the refusal is deliberately scoped to the seal, the one
// destructive operation, and never to the progress of a run this server can
// legitimately report on. What differs is who the adopted runtime names
// afterwards — which is what decides whether its terminal claim may seal.
func TestRunIdentity_AdoptionTakesOwnershipOnlyFromAGoneOwner(t *testing.T) {
	const repo = "acme/platform"

	adopt := func(t *testing.T, issue, ownerPID int) *state.RuntimeState {
		t.Helper()
		root := t.TempDir()
		stateDir := filepath.Join(root, ".nightgauge", "pipeline")
		runID := newTestRunID()

		seed := state.NewRuntimeState(repo, issue, "", runID)
		seed.OwnerPID = ownerPID
		seed.BeginStage(state.StageFeatureDev)
		if err := seed.Persist(stateDir); err != nil {
			t.Fatalf("seed persist: %v", err)
		}

		s := NewServer(nil, WithWorkspaceRoot(root))
		mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-validate", Status: "running", RunID: runID,
		})
		s.runtimesMu.Lock()
		defer s.runtimesMu.Unlock()
		e := s.activeRuntimes[runID]
		if e == nil {
			t.Fatal("the run-progress verb did not adopt the snapshot at all")
		}
		return e.rs
	}

	t.Run("gone owner", func(t *testing.T) {
		gone := deadPID(t)
		rs := adopt(t, 559, gone)
		// The transfer is asserted on the PID, not on OwnedByThisProcess: a
		// gone owner already reads as "not a live foreign owner", so the
		// permissive answer alone would stay green with no transfer at all —
		// and the second adopter of the same snapshot would then also be
		// allowed to seal, which is #557 with the scheduler swapped out.
		if rs.OwnerPID != os.Getpid() {
			t.Errorf("adopted runtime names owner pid %d, want this process (%d) — a gone owner's run must become this process's to seal",
				rs.OwnerPID, os.Getpid())
		}
		if !rs.OwnedByThisProcess() {
			t.Error("OwnedByThisProcess is false after adopting a gone owner's run")
		}
		if rs.OwnerPID == gone {
			t.Error("the adopted runtime still names the dead owner")
		}
	})

	t.Run("live owner", func(t *testing.T) {
		owner := liveForeignPID(t)
		rs := adopt(t, 560, owner)
		if rs.OwnedByThisProcess() {
			t.Error("adopting a LIVE owner's run took ownership of it — this server's terminal claim would then be allowed to seal a file the owner is still writing")
		}
		if rs.OwnerPID != owner {
			t.Errorf("adopted runtime names owner pid %d, want %d", rs.OwnerPID, owner)
		}
	})
}
