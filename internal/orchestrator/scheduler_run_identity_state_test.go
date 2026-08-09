package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ADR-017 step 1, scheduler side: the mint block was HOISTED above the
// constructor (RunID is a constructor argument now, immutable, no setter), and
// the current-run.json sidecar gained the identity the extension's CLI-run
// reconciler needs to find the snapshot.
//
// The hoist's own regression coverage lives in scheduler_run_identity_test.go:
// the mint-failure funnel test and the dispatch-id test both still pass
// UNCHANGED in meaning, which is what pins that the runIDMintErr →
// run-identity-preflight fall-through survived moving above the terminal defer.

// TestRunPipeline_SidecarCarriesTheRunIdentity pins D11: current-run.json now
// carries run_id, written from the same runtime the snapshot is written from.
// Without it the TypeScript CLI-run reconciler cannot compose
// runtime-{issue}-{runId}.json at all, and the whole CLI-observability feature
// dies silently behind a bare `catch { return null }` (F25).
func TestRunPipeline_SidecarCarriesTheRunIdentity(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	// Freeze the run mid-flight by writing the sidecar and reading it back
	// before the pipeline tears it down: the sidecar is removed on clean
	// completion, so it is captured from the stage the runner observes.
	item := types.BoardItem{Number: 8801, Repo: "nightgauge/nightgauge", ID: "item-8801", Title: "sidecar identity"}
	var captured *CurrentRunSidecar
	runner.onStage = func() {
		if captured != nil {
			return
		}
		sc, err := readCurrentRunSidecar(root)
		if err != nil || sc == nil {
			return
		}
		captured = sc
	}
	s.runPipeline(context.Background(), item)

	if captured == nil {
		t.Fatal("no current-run sidecar was observed during the run")
	}
	if captured.RunID == "" {
		t.Fatal("current-run.json carries no run_id — the TS CLI reconciler cannot name the snapshot without it (F25)")
	}
	if !uuidV7Pattern.MatchString(captured.RunID) {
		t.Errorf("sidecar run_id %q is not a canonical run identity", captured.RunID)
	}

	// And the id it carries must be the id the run's own dispatches carried,
	// or the sidecar points at a snapshot that does not exist.
	calls := runner.captured()
	if len(calls) == 0 {
		t.Fatal("no stage was dispatched")
	}
	if calls[0].RunID != captured.RunID {
		t.Errorf("sidecar run_id %q != dispatch run_id %q", captured.RunID, calls[0].RunID)
	}
}

// TestCurrentRunSidecar_RunIdKeyIsAlwaysPresent pins the wire-level half of
// D11: readers treat a MISSING key as "written by a binary older than
// ADR-017", so this binary must never omit it.
func TestCurrentRunSidecar_RunIdKeyIsAlwaysPresent(t *testing.T) {
	root := t.TempDir()
	if err := writeCurrentRunSidecar(root, CurrentRunSidecar{IssueNumber: 1, Repo: "o/r"}); err != nil {
		t.Fatalf("writeCurrentRunSidecar: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, currentRunSidecarFile))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["run_id"]; !ok {
		t.Error("current-run.json has no run_id key — the tag must not carry omitempty")
	}

	id := testRunID()
	if err := writeCurrentRunSidecar(root, CurrentRunSidecar{IssueNumber: 1, Repo: "o/r", RunID: id}); err != nil {
		t.Fatalf("writeCurrentRunSidecar: %v", err)
	}
	back, err := readCurrentRunSidecar(root)
	if err != nil || back == nil {
		t.Fatalf("readCurrentRunSidecar: %v", err)
	}
	if back.RunID != id {
		t.Errorf("round-tripped run_id = %q, want %q", back.RunID, id)
	}
}

// TestRunPipeline_SnapshotLandsUnderTheIdentityKeyedName pins that the
// scheduler's three Persist sites need no code change but DO get the new
// filename — the snapshot is discoverable by the same scan the reconciler runs.
func TestRunPipeline_SnapshotLandsUnderTheIdentityKeyedName(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	item := types.BoardItem{Number: 8802, Repo: "nightgauge/nightgauge", ID: "item-8802", Title: "snapshot name"}
	var stateDir string
	var midRun []*state.RuntimeState
	runner.onStage = func() {
		if len(midRun) > 0 {
			return
		}
		stateDir = filepath.Join(root, ".nightgauge", "pipeline")
		found, err := state.FindPersistedStatesForIssue(stateDir, item.Number)
		if err == nil {
			midRun = found
		}
	}
	s.runPipeline(context.Background(), item)

	if len(midRun) != 1 {
		t.Fatalf("mid-run the scheduler must have exactly one discoverable snapshot for #%d in %s, found %d",
			item.Number, stateDir, len(midRun))
	}
	if midRun[0].RunID == "" {
		t.Fatal("the discovered snapshot carries no run identity")
	}
	// The name the run wrote is the name the composer produces — this is the
	// pin that keeps the reconciler's discovery regex and Persist in agreement.
	if _, err := os.Stat(filepath.Join(stateDir, state.SnapshotFilename(item.Number, midRun[0].RunID))); err != nil {
		t.Errorf("snapshot is not at the composed path: %v", err)
	}
}
