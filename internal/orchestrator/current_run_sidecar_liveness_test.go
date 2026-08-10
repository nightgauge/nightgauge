package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// #410. Two halves of the same fact — the in-flight sidecar carries a PID, and
// nothing read it.
//
//  1. The registry-less in-flight reader (state.ActiveIssuesFromSnapshots) needs
//     that pid, because on the Go-scheduler path it is the only liveness signal
//     written while a run is alive. The reader decodes the file with a local
//     struct (internal/orchestrator imports internal/state, so it cannot import
//     the writer's package back), and this test is what keeps the two shapes
//     paired: it writes with the PRODUCTION writer and reads with the production
//     reader.
//  2. Construction must not act on it. recoverOrchestratorCrashAt treated a
//     sidecar's mere presence as proof of a crash, so `nightgauge queue list` in a
//     second terminal destroyed a live run's index, invented its terminal record,
//     and paused the queue.

// TestCurrentRunSidecar_ProtectsItsIssueThroughTheStateReader pins the
// cross-package contract with the real writer on one side and the real reader on
// the other. A rename of `issue_number` or `pid` fails HERE rather than silently
// disarming the reader's Go-scheduler arm.
func TestCurrentRunSidecar_ProtectsItsIssueThroughTheStateReader(t *testing.T) {
	root := t.TempDir()
	if err := writeCurrentRunSidecar(root, CurrentRunSidecar{
		RunID:       testRunID(),
		IssueNumber: 771,
		Repo:        "acme/platform",
		StartedAt:   time.Now().UTC().Add(-2 * time.Hour),
		Stage:       "feature-dev",
		StageStart:  time.Now().UTC().Add(-90 * time.Minute),
		PID:         os.Getpid(),
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	res, err := state.ActiveIssuesFromSnapshots(state.PipelineStateDir(root))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[771] {
		t.Errorf("the sidecar the scheduler writes does not protect its issue through state.ActiveIssuesFromSnapshots; got %v (warnings %v)", res.Issues, res.Warnings)
	}
}

// TestRecoverOrchestratorCrash_LiveRunIsLeftAlone is #410's correction to the
// "constructors delete nothing" doctrine. A sidecar is evidence of a RUN, not of a
// crash: it is present for the whole life of a healthy one. With no liveness
// check, constructing a Scheduler (which every `nightgauge queue …` invocation and
// both promote gates do) unlinked the live run's current-run.json — the
// TypeScript side's index into that run — fabricated a terminal-failure
// RunRecord for it, and paused the queue.
func TestRecoverOrchestratorCrash_LiveRunIsLeftAlone(t *testing.T) {
	root := t.TempDir()
	if err := writeCurrentRunSidecar(root, CurrentRunSidecar{
		RunID:       testRunID(),
		IssueNumber: 778,
		Repo:        "acme/platform",
		Title:       "A run that is still executing",
		StartedAt:   time.Now().UTC().Add(-30 * time.Second),
		Stage:       "feature-dev",
		StageStart:  time.Now().UTC().Add(-20 * time.Second),
		PID:         os.Getpid(), // the orchestrator that owns this run is ALIVE
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	s := &Scheduler{
		workspaceRoot: root,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		queue: []QueueItem{
			{IssueNumber: 1001, Status: "pending", Repo: "acme/platform", Title: "Next up"},
		},
	}

	out := captureLog(t, func() { s.recoverOrchestratorCrash() })

	if _, err := os.Stat(filepath.Join(root, currentRunSidecarFile)); err != nil {
		t.Errorf("the live run's sidecar was removed (%v) — that file is the extension's index into the run", err)
	}
	if hasDailyJSONL(t, root) {
		t.Error("a terminal-failure RunRecord was synthesized for a run that is still executing")
	}
	if _, err := os.Stat(filepath.Join(root, queueStateFile)); !os.IsNotExist(err) {
		t.Errorf("queue-state.json was rewritten during construction (stat err=%v) — the queue must not be paused on behalf of a live run", err)
	}
	if got := s.GetState(); len(got.Items) != 1 || got.Items[0].Status != "pending" {
		t.Errorf("the queue was paused for a live run: %+v", got.Items)
	}
	if !strings.Contains(out, "LIVE pid") {
		t.Errorf("the skip does not name the live pid it deferred to; got:\n%s", out)
	}
}

// TestRecoverOrchestratorCrash_DeadPidStillSynthesizes: the liveness gate must not
// disable recovery, which is the whole point of the sidecar. A pid that is gone —
// the actual crash — still produces the terminal record, the pause and the unlink.
func TestRecoverOrchestratorCrash_DeadPidStillSynthesizes(t *testing.T) {
	root := t.TempDir()
	startedAt := time.Now().UTC().Add(-30 * time.Second)
	if err := writeCurrentRunSidecar(root, CurrentRunSidecar{
		RunID:       testRunID(),
		IssueNumber: 779,
		Repo:        "acme/platform",
		StartedAt:   startedAt,
		Stage:       "feature-dev",
		StageStart:  startedAt.Add(5 * time.Second),
		// pid 0 is never a live process (runstate.ProcessAlive rejects it), which
		// is also what a pre-ADR-017 sidecar carries.
		PID: 0,
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	s := &Scheduler{
		workspaceRoot: root,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		queue: []QueueItem{
			{IssueNumber: 1002, Status: "pending", Repo: "acme/platform", Title: "Next up"},
		},
	}

	s.recoverOrchestratorCrash()

	if _, err := os.Stat(filepath.Join(root, currentRunSidecarFile)); !os.IsNotExist(err) {
		t.Errorf("a crashed run's sidecar must be reconciled away, stat err=%v", err)
	}
	records := readDailyJSONLRecords(t, root)
	if len(records) != 1 || records[0].IssueNumber != 779 {
		t.Fatalf("expected one synthesized record for #779, got %+v", records)
	}
	if records[0].TerminalFailureKind != TerminalKindOrchestratorCrash {
		t.Errorf("TerminalFailureKind = %q, want %q", records[0].TerminalFailureKind, TerminalKindOrchestratorCrash)
	}
	if got := s.GetState(); len(got.Items) != 1 || got.Items[0].Status != "paused" {
		t.Errorf("the downstream queue item was not paused after a real crash: %+v", got.Items)
	}
}
