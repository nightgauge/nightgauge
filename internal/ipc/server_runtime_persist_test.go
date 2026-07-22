package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		activeRuntimes: make(map[string]*state.RuntimeState),
		workspaceRoot:  tmpDir,
	}
	s.registerMethods()

	const issue = 304
	runtimePath := filepath.Join(
		tmpDir, ".nightgauge", "pipeline", fmt.Sprintf("runtime-%d.json", issue),
	)

	// 1. "initialized" transition with an empty repo — must NOT persist.
	initParams := json.RawMessage(
		`{"repo":"","issueNumber":304,"stage":"","status":"initialized"}`,
	)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), initParams); err != nil {
		t.Fatalf("initialized transition error: %v", err)
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("empty-repo 'initialized' transition must NOT write %s (stat err=%v)", runtimePath, err)
	}

	// 2. "running" transition carrying the repo — must persist to that repo's dir.
	runParams := json.RawMessage(
		`{"repo":"acme/platform","issueNumber":304,"stage":"issue-pickup","status":"running"}`,
	)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), runParams); err != nil {
		t.Fatalf("running transition error: %v", err)
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("repo-carrying 'running' transition must write %s: %v", runtimePath, err)
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
	runtimePath := filepath.Join(
		tmpDir, ".nightgauge", "pipeline", fmt.Sprintf("runtime-%d.json", issue),
	)

	// setPaused constructs the runtime with an empty repo (NewRuntimeState("")).
	pauseParams := json.RawMessage(`{"issueNumber":209,"paused":true}`)
	if _, err := s.methods["pipeline.setPaused"](context.Background(), pauseParams); err != nil {
		t.Fatalf("setPaused error: %v", err)
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("empty-repo setPaused must NOT write %s (stat err=%v)", runtimePath, err)
	}
}
