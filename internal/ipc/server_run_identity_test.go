package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestNotifyStageTransition_InterimMintIsAUuidV7AndPersistsUnderTheNewScheme is
// the F16 guard for the window between ADR-017 steps 1 and 4.
//
// The extension population does not acquire its own identity until step 3, so
// notifyStageTransition's create-on-miss mint SURVIVES this step — deleting it
// now would leave every extension-path runtime identity-less, Persist would
// refuse all of them (Decision 1), and every extension run in that window would
// write zero snapshots behind a healthy-looking UI. What changed is the SHAPE:
// it was uuid.NewString() (v4), which the identity pattern, the filename
// composer and the wire validation all reject.
//
// This test would have failed on the pre-ADR tree in exactly the way that
// matters: a v4 id produces a filename the discovery regex does not match.
func TestNotifyStageTransition_InterimMintIsAUuidV7AndPersistsUnderTheNewScheme(t *testing.T) {
	workspaceRoot := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*state.RuntimeState),
		workspaceRoot:  workspaceRoot,
	}
	s.registerMethods()

	const issue = 4711
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4711,"stage":"issue-pickup","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}

	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("the extension path must keep writing snapshots between steps 1 and 4; found %d in %s", len(found), stateDir)
	}
	if !runstate.IsIdentity(found[0].RunID) {
		t.Errorf("interim mint produced %q, which the shared identity pattern rejects — it must be runstate.NewRunID(), not a v4 UUID",
			found[0].RunID)
	}

	// The run's terminal event removes the snapshot by the identity-keyed name.
	// A stale bare-issue remove would silently miss it and leave every completed
	// extension run's snapshot for the reconciler to double-terminal.
	if _, err := s.methods["pipeline.notifyComplete"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4711,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	if left, err := state.FindPersistedStatesForIssue(stateDir, issue); err != nil || len(left) != 0 {
		t.Errorf("notifyComplete left %d snapshot(s) behind (err=%v) — the remove must be composed from the run's own identity",
			len(left), err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, state.SnapshotFilename(issue, found[0].RunID))); !os.IsNotExist(err) {
		t.Errorf("the run's own snapshot survived its terminal event; stat = %v", err)
	}
}
