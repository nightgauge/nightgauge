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

// TestRunIdentity_SuccessorWithoutInitializedRecordsNormally is the test the
// refuted latch-on-`initialized` design fails, and it must never be deleted
// (ADR-017 F16).
//
// A run whose FIRST message the server never saw — the extension host survived
// while the IPC binary restarted, or the dispatch began before the socket came
// up — has EVERY subsequent transition accepted by ADOPTION, and its completion
// writes exactly one record with full telemetry. `initialized` has no special
// status: it is one transition among many, and the identity on the current
// message is the whole handshake.
//
// It also pins what replaced this file's previous test: the server-side interim
// mint is GONE. The identity is the caller's, so the snapshot filename, the
// terminal removal and the registry key are all composed from a value the run
// itself chose.
func TestRunIdentity_SuccessorWithoutInitializedRecordsNormally(t *testing.T) {
	workspaceRoot := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
		workspaceRoot:  workspaceRoot,
	}
	s.registerMethods()

	const issue = 4711
	runID := newTestRunID()
	if !runstate.IsIdentity(runID) {
		t.Fatalf("test fixture %q is not a canonical run identity", runID)
	}

	// No "initialized" — the run's first observed message is mid-pipeline.
	for _, params := range []string{
		`{"repo":"acme/platform","issueNumber":4711,"stage":"issue-pickup","status":"running","runId":"` + runID + `"}`,
		`{"repo":"acme/platform","issueNumber":4711,"stage":"issue-pickup","status":"complete","inputTokens":900,"outputTokens":120,"costUsd":0.75,"runId":"` + runID + `"}`,
	} {
		if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(params)); err != nil {
			t.Fatalf("a successor's transition must be ACCEPTED by adoption: %v", err)
		}
	}

	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("an adopted run must persist exactly one discoverable snapshot; found %d in %s", len(found), stateDir)
	}
	if found[0].RunID != runID {
		t.Errorf("snapshot RunID = %q, want the CALLER's %q — nothing server-side mints an identity any more",
			found[0].RunID, runID)
	}
	if found[0].TotalCostUSD != 0.75 {
		t.Errorf("adopted run lost its spend: TotalCostUSD = %v, want 0.75", found[0].TotalCostUSD)
	}

	// The terminal claim removes the snapshot by the identity-keyed name.
	if _, err := s.methods["pipeline.notifyComplete"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4711,"success":true,"totalDurationMs":1000,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	if left, err := state.FindPersistedStatesForIssue(stateDir, issue); err != nil || len(left) != 0 {
		t.Errorf("notifyComplete left %d snapshot(s) behind (err=%v)", len(left), err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, state.SnapshotFilename(issue, runID))); !os.IsNotExist(err) {
		t.Errorf("the run's own snapshot survived its terminal event; stat = %v", err)
	}
}
