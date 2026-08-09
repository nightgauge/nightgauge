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

// TestNotifyStageTransition_SkipsPersistForEmptyRepo verifies #307: a runtime
// whose repo is not yet known — the "initialized" snapshot a concurrent
// HeadlessOrchestrator slot emits before setRunRepo seeds the slug — is NEVER
// persisted to the shared launch root. Previously pipelineStateDir("") resolved
// s.workspaceRoot and stranded an empty repo/stage stub in a repo that never ran
// the issue (cross-contamination). Once a repo-carrying "running" transition
// arrives, the runtime persists to that repo's dir.
func TestNotifyStageTransition_SkipsPersistForEmptyRepo(t *testing.T) {
	tmpDir := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*state.RuntimeState),
		workspaceRoot:  tmpDir,
	}
	s.registerMethods()

	const issue = 304
	// The identity is minted inside the handler, so "did it persist?" is asked
	// of the DIRECTORY (ADR-017 Decision 8) rather than of a composed name.
	stateDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	snapshotCount := func() int {
		found, err := state.FindPersistedStatesForIssue(stateDir, issue)
		if err != nil {
			t.Fatalf("FindPersistedStatesForIssue: %v", err)
		}
		return len(found)
	}

	// 1. "initialized" transition with an empty repo — must NOT persist.
	initParams := json.RawMessage(
		`{"repo":"","issueNumber":304,"stage":"","status":"initialized"}`,
	)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), initParams); err != nil {
		t.Fatalf("initialized transition error: %v", err)
	}
	if n := snapshotCount(); n != 0 {
		t.Fatalf("empty-repo 'initialized' transition must NOT write a snapshot; found %d in %s", n, stateDir)
	}

	// 2. "running" transition carrying the repo — must persist to that repo's dir.
	runParams := json.RawMessage(
		`{"repo":"acme/platform","issueNumber":304,"stage":"issue-pickup","status":"running"}`,
	)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), runParams); err != nil {
		t.Fatalf("running transition error: %v", err)
	}
	if n := snapshotCount(); n != 1 {
		t.Fatalf("repo-carrying 'running' transition must write exactly one snapshot into %s; found %d", stateDir, n)
	}
}

// TestSetPaused_SkipsPersistForEmptyRepo verifies the sibling guard on the
// pause path (#307): pausing a runtime whose repo is still unknown must not
// strand a paused stub in the shared launch root.
func TestSetPaused_SkipsPersistForEmptyRepo(t *testing.T) {
	tmpDir := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*state.RuntimeState),
		workspaceRoot:  tmpDir,
	}
	s.registerMethods()

	const issue = 209
	stateDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")

	// setPaused's create-on-miss now MINTS an identity (so the entry it installs
	// under the issue key is persistable when a repo arrives — see
	// TestSetPausedThenTransitions_WritesExactlyOneDiscoverableSnapshot), but it
	// still has no repo, and the #307 gate is what keeps this write off disk.
	// Step 4 deletes the create-on-miss entirely.
	pauseParams := json.RawMessage(`{"issueNumber":209,"paused":true}`)
	if _, err := s.methods["pipeline.setPaused"](context.Background(), pauseParams); err != nil {
		t.Fatalf("setPaused error: %v", err)
	}
	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("empty-repo setPaused must NOT write a snapshot; found %d in %s", len(found), stateDir)
	}
	// And nothing at all lands in the dir — not even a nameless runtime-209-.json.
	if entries, _ := os.ReadDir(stateDir); len(entries) != 0 {
		t.Fatalf("setPaused wrote %d files into %s; a runtime with no repo has no correct home", len(entries), stateDir)
	}
}

// TestSetPausedThenTransitions_WritesExactlyOneDiscoverableSnapshot pins the
// ADOPTION regression that per-run filenames introduced.
//
// setPaused's create-on-miss installs its entry under the ISSUE key, and
// notifyStageTransition mints only in its own `!ok` branch — so an
// identity-less stub was REUSED for the life of the run, every Persist returned
// ErrNoRunIdentity, and the run wrote ZERO snapshots behind a log line the
// handler itself labels "non-fatal": no crash-recovery snapshot (so #44 could
// never close the run), no gate records, no pause-restore, no getState
// fallback. Reachable on every backend auto-restart under a surviving extension
// host (F26), and on the activation restore path, which calls resumePipeline()
// — i.e. setPaused — before runPipeline().
//
// BASELINE (the reviewer's executed probe against the pre-fix commit, quoted):
//
//	after setPaused: activeRuntimes[370].RunID="" repo=""
//	notifyStageTransition: persist runtime snapshot failed (non-fatal):
//	  persist runtime for #370: runtime state has no run identity: refusing to persist   [x3]
//	snapshots on disk after three repo-carrying transitions: 0
//
// and against main (7b7b0d8b), the same handler sequence:
//
//	MAIN BASELINE on disk: runtime-370.json
//
// This test asserts that zero-snapshot behaviour is gone: the run persists, and
// it does so under ONE discoverable, identity-keyed name.
func TestSetPausedThenTransitions_WritesExactlyOneDiscoverableSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*state.RuntimeState),
		workspaceRoot:  tmpDir,
	}
	s.registerMethods()

	const issue = 4242
	stateDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")

	// 1. A pause arrives with an EMPTY registry — the F26 shape.
	if _, err := s.methods["pipeline.setPaused"](context.Background(),
		json.RawMessage(`{"issueNumber":4242,"paused":false}`)); err != nil {
		t.Fatalf("setPaused error: %v", err)
	}
	s.runtimesMu.Lock()
	stub, ok := s.activeRuntimes["4242"]
	s.runtimesMu.Unlock()
	if !ok {
		t.Fatal("setPaused installed no entry — the rest of this test is vacuous")
	}
	// Errorf, not Fatalf: the disk assertion below is the one that reproduces
	// the operator-visible defect, and it must still run.
	if !runstate.IsIdentity(stub.RunID) {
		t.Errorf("setPaused's stub carries RunID %q, which is not a run identity — every Persist for the run that adopts it will be refused", stub.RunID)
	}

	// 2. Three repo-carrying transitions, exactly as a live run emits them.
	for _, params := range []string{
		`{"repo":"acme/platform","issueNumber":4242,"stage":"issue-pickup","status":"running"}`,
		`{"repo":"acme/platform","issueNumber":4242,"stage":"feature-planning","status":"running"}`,
		`{"repo":"acme/platform","issueNumber":4242,"stage":"feature-dev","status":"running"}`,
	} {
		if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(params)); err != nil {
			t.Fatalf("transition error: %v", err)
		}
	}

	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("a paused-then-running issue must leave exactly ONE discoverable snapshot; found %d in %s (0 = the adoption regression is back)", len(found), stateDir)
	}
	if !runstate.IsIdentity(found[0].RunID) {
		t.Errorf("snapshot RunID %q is not a canonical identity", found[0].RunID)
	}
	if found[0].Stage != state.PipelineStage("feature-dev") {
		t.Errorf("snapshot stage = %q, want the last transition's feature-dev", found[0].Stage)
	}

	// 3. And the gate seam — the cross-process reader that goes dark first —
	//    can find it.
	if err := state.AppendStageGateResultToDisk(stateDir, issue, state.StageFeatureDev,
		state.StageGateResult{GateName: "feature-dev", Passed: true}); err != nil {
		t.Errorf("gate seam could not record against the run: %v", err)
	}
}

// TestNotifyStageTransition_ReplacesAnIdentityLessEntryRatherThanAdoptingIt is
// the defence-in-depth half of the same fix: whatever installs an entry with no
// valid identity, the first transition REBUILDS it (RunID is a constructor fact,
// so repair is replacement) and carries the stub's paused flag across.
func TestNotifyStageTransition_ReplacesAnIdentityLessEntryRatherThanAdoptingIt(t *testing.T) {
	tmpDir := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*state.RuntimeState),
		workspaceRoot:  tmpDir,
	}
	s.registerMethods()

	const issue = 4243
	// Constructed directly: no production path installs this any more, which is
	// exactly why the guard needs its own fixture.
	stub := state.NewRuntimeState("", issue, "", "")
	stub.SetPaused(true)
	stub.Title = "carried title"
	s.activeRuntimes["4243"] = stub

	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4243,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("transition error: %v", err)
	}

	s.runtimesMu.Lock()
	rt := s.activeRuntimes["4243"]
	s.runtimesMu.Unlock()
	if rt == stub {
		t.Fatal("the identity-less entry was ADOPTED, not replaced — every Persist for this run is refused")
	}
	if !runstate.IsIdentity(rt.RunID) {
		t.Fatalf("replacement RunID %q is not a run identity", rt.RunID)
	}
	if !rt.Paused {
		t.Error("the replacement dropped the paused flag the stub legitimately held")
	}
	if rt.Title != "carried title" {
		t.Errorf("Title = %q, want the carried-over %q", rt.Title, "carried title")
	}

	stateDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("the repaired run must persist; found %d snapshots in %s", len(found), stateDir)
	}
}
