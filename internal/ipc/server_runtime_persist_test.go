package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		activeRuntimes: make(map[string]*runEntry),
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
		`{"repo":"","issueNumber":304,"stage":"","status":"initialized","runId":"01900130-0000-7000-8000-000000000304"}`,
	)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), initParams); err != nil {
		t.Fatalf("initialized transition error: %v", err)
	}
	if n := snapshotCount(); n != 0 {
		t.Fatalf("empty-repo 'initialized' transition must NOT write a snapshot; found %d in %s", n, stateDir)
	}

	// 2. "running" transition carrying the repo — must persist to that repo's dir.
	runParams := json.RawMessage(
		`{"repo":"acme/platform","issueNumber":304,"stage":"issue-pickup","status":"running","runId":"01900130-0000-7000-8000-000000000304"}`,
	)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), runParams); err != nil {
		t.Fatalf("running transition error: %v", err)
	}
	if n := snapshotCount(); n != 1 {
		t.Fatalf("repo-carrying 'running' transition must write exactly one snapshot into %s; found %d", stateDir, n)
	}
}

// TestSetPaused_RefusesAnUnknownRunAndWritesNothing replaces the #307
// empty-repo guard test at the same seam, because ADR-017 step 4 deleted the
// create-on-miss the old test was guarding.
//
// setPaused is ADMINISTRATIVE (Decision 3): it RESOLVES, NEVER INVENTS. An id
// with no live entry and no snapshot on disk is run_not_found — nothing is
// created, nothing is written, and no runtime is pinned against #44 forever
// (F9). The #307 property the old test pinned survives as a stronger one: not
// "the write is skipped because the repo is unknown" but "there is nothing to
// write, because there is no run".
func TestSetPaused_RefusesAnUnknownRunAndWritesNothing(t *testing.T) {
	tmpDir := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
		workspaceRoot:  tmpDir,
	}
	s.registerMethods()

	const issue = 209
	stateDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")

	pauseParams := json.RawMessage(`{"issueNumber":209,"paused":true,"runId":"` + newTestRunID() + `"}`)
	_, err := s.methods["pipeline.setPaused"](context.Background(), pauseParams)
	if err == nil {
		t.Fatal("setPaused for an unknown run must be refused, not served")
	}
	if !strings.Contains(err.Error(), "run_not_found") {
		t.Errorf("error %q does not carry the machine-readable code run_not_found", err.Error())
	}

	found, ferr := state.FindPersistedStatesForIssue(stateDir, issue)
	if ferr != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", ferr)
	}
	if len(found) != 0 {
		t.Fatalf("a refused setPaused must NOT write a snapshot; found %d in %s", len(found), stateDir)
	}
	// And nothing at all lands in the dir — not even a nameless runtime-209-.json.
	if entries, _ := os.ReadDir(stateDir); len(entries) != 0 {
		t.Fatalf("setPaused wrote %d files into %s; a run that does not exist has no state", len(entries), stateDir)
	}
	// No runtime was invented for the id either.
	s.runtimesMu.Lock()
	n := len(s.activeRuntimes)
	s.runtimesMu.Unlock()
	if n != 0 {
		t.Errorf("a refused setPaused installed %d registry entr(ies); an administrative verb never adopts empty", n)
	}
}

// TestSetPaused_AdoptsAnExistingSnapshotWithoutVouchingForIt pins the other
// half of Decision 4's administrative rule, and the distinction that cost the
// second revision F33: adopting a snapshot ALREADY ON DISK is not
// "inventing a run" — the snapshot IS the evidence.
//
// The entry it installs is an ordinary entry in every respect but one: its
// lease stays at the ZERO time, so it can never make the run look alive to the
// #44 reconciliation (the F9 pin this must not re-create). Routing the pause
// through the live object is what makes rs.mu serialise it against the run's
// own Persist calls rather than racing a detached read-modify-write.
func TestSetPaused_AdoptsAnExistingSnapshotWithoutVouchingForIt(t *testing.T) {
	tmpDir := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
		workspaceRoot:  tmpDir,
	}
	s.registerMethods()

	const (
		repo  = "acme/platform"
		issue = 4242
	)
	stateDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	runID := newTestRunID()

	// A run of this issue left a snapshot behind (the modal case: the IPC
	// binary restarted under a surviving extension host).
	seeded := state.NewRuntimeState(repo, issue, "", runID)
	seeded.BeginStage(state.StageFeatureDev)
	if err := seeded.Persist(stateDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	if _, err := s.methods["pipeline.setPaused"](context.Background(),
		json.RawMessage(`{"issueNumber":4242,"repo":"acme/platform","paused":true,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("setPaused over an existing snapshot must be served: %v", err)
	}

	s.runtimesMu.Lock()
	entry, ok := s.activeRuntimes[runID]
	s.runtimesMu.Unlock()
	if !ok {
		t.Fatal("the administrative resolution installed no entry — the pause had nothing to serialise against (F33)")
	}
	if !entry.lastSeen.IsZero() {
		t.Errorf("lastSeen = %v, want the zero time: an administrative verb may install a run's state and may never make the run look alive",
			entry.lastSeen)
	}
	if entry.rs.Stage != state.StageFeatureDev {
		t.Errorf("adopted runtime lost the snapshot's history: Stage = %q, want feature-dev", entry.rs.Stage)
	}

	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil || len(found) != 1 {
		t.Fatalf("expected exactly one snapshot after the pause; got %d / %v", len(found), err)
	}
	if !found[0].Paused {
		t.Error("the pause did not reach the run's own snapshot")
	}

	// A run-progress call for the same id shares the SAME *RuntimeState, and
	// its lease stamp is what makes the run look alive again.
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4242,"stage":"feature-dev","status":"running","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("transition after an administrative adoption: %v", err)
	}
	s.runtimesMu.Lock()
	after := s.activeRuntimes[runID]
	s.runtimesMu.Unlock()
	if after != entry {
		t.Fatal("the run-progress call built a SECOND runtime for one identity")
	}
	if after.lastSeen.IsZero() {
		t.Error("a run-progress call must stamp the lease the administrative one deliberately did not")
	}
}
