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

// Tests for #307's run-record identity guard.
//
// pipeline.notifyComplete is the ONLY place that drops a runtime, and it is
// unreachable for a run whose promise never settles: HeadlessOrchestrator's
// firePipelineComplete is called only on paths that return a result. But
// ConcurrentPipelineManager's abort-deadline force-clear DOES declare such a
// run terminal — it releases the queue mark, frees the Go scheduler's slot and
// disposes the slot — which makes the issue immediately re-dispatchable while
// the runtime lives on under its bare issue number. notifyStageTransition
// reuses whatever entry it finds, so the NEXT run of that issue inherits the
// dead one's RunID, started_at, CompletedStages, StageErrors and token totals
// and stamps them onto its own authoritative V2 record — the exact
// cross-contamination shape the #313/#316 identity guards exist for.

func newAbandonTestServer(t *testing.T, root string) *Server {
	t.Helper()
	var buf bytes.Buffer
	s := &Server{
		writer:            &buf,
		methods:           make(map[string]Handler),
		activeRuntimes:    make(map[string]*state.RuntimeState),
		abandonedRuntimes: make(map[string]*state.RuntimeState),
		workspaceRoot:     root,
	}
	s.registerMethods()
	return s
}

// TestAbandonRunRuntime_NextDispatchGetsFreshRunID is the regression: after a
// force-clear, a re-dispatch of the same issue must mint a NEW run id.
func TestAbandonRunRuntime_NextDispatchGetsFreshRunID(t *testing.T) {
	tmpDir := t.TempDir()
	s := newAbandonTestServer(t, tmpDir)

	const issue = 282
	const repo = "acme/platform"
	transition := json.RawMessage(
		fmt.Sprintf(`{"repo":%q,"issueNumber":%d,"stage":"feature-dev","status":"running"}`, repo, issue),
	)

	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), transition); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	s.runtimesMu.Lock()
	firstRunID := s.activeRuntimes[fmt.Sprintf("%d", issue)].RunID
	s.runtimesMu.Unlock()
	if firstRunID == "" {
		t.Fatal("first transition did not mint a run id")
	}

	runtimePath := filepath.Join(tmpDir, ".nightgauge", "pipeline", fmt.Sprintf("runtime-%d.json", issue))
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("expected the crash-recovery snapshot at %s: %v", runtimePath, err)
	}

	// The operator pressed Stop, the run never settled, and the extension
	// force-cleared the slot.
	s.abandonRunRuntime(repo, issue)

	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Errorf("crash-recovery snapshot still at %s after the force-clear (stat err=%v) — orphan reconciliation would re-terminate a run the manager already booked", runtimePath, err)
	}

	// The issue is re-dispatched (operator re-queue).
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), transition); err != nil {
		t.Fatalf("second transition: %v", err)
	}
	s.runtimesMu.Lock()
	secondRunID := s.activeRuntimes[fmt.Sprintf("%d", issue)].RunID
	s.runtimesMu.Unlock()

	if secondRunID == "" {
		t.Fatal("second transition did not mint a run id")
	}
	if secondRunID == firstRunID {
		t.Errorf("re-dispatch reused run id %q from the force-cleared run — the next run's authoritative record would carry the dead run's identity and stage set (#307)", firstRunID)
	}
}

// TestAbandonRunRuntime_LateCompletionKeepsItsOwnIdentity: moving the runtime
// aside must not destroy it. A force-cleared process can still be alive and can
// still reach notifyComplete, and that run is entitled to write its own record
// under its OWN run id — not a fresh skeleton, and not the successor's.
func TestAbandonRunRuntime_LateCompletionKeepsItsOwnIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	s := newAbandonTestServer(t, tmpDir)

	const issue = 282
	const repo = "acme/platform"
	transition := json.RawMessage(
		fmt.Sprintf(`{"repo":%q,"issueNumber":%d,"stage":"pr-merge","status":"running"}`, repo, issue),
	)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), transition); err != nil {
		t.Fatalf("transition: %v", err)
	}
	key := fmt.Sprintf("%d", issue)
	s.runtimesMu.Lock()
	wedgedRunID := s.activeRuntimes[key].RunID
	s.runtimesMu.Unlock()

	s.abandonRunRuntime(repo, issue)

	s.runtimesMu.Lock()
	_, stillActive := s.activeRuntimes[key]
	parked, wasParked := s.abandonedRuntimes[key]
	s.runtimesMu.Unlock()

	if stillActive {
		t.Error("runtime is still in activeRuntimes after the force-clear — the next dispatch would adopt it")
	}
	if !wasParked {
		t.Fatal("runtime was deleted rather than moved aside — a force-cleared run that finishes anyway would lose its record")
	}
	if parked.RunID != wedgedRunID {
		t.Errorf("parked runtime run id = %q, want %q — the late completion must write under its own identity", parked.RunID, wedgedRunID)
	}
}

// TestAutonomousComplete_UserAbort_AbandonsRuntime pins the wiring: the
// extension signals the force-clear through autonomous.complete's terminal
// kind, and the runtime must be moved aside even when no autonomous scheduler
// is configured (the runtime map is the IPC server's own state, contaminated
// either way).
func TestAutonomousComplete_UserAbort_AbandonsRuntime(t *testing.T) {
	tmpDir := t.TempDir()
	s := newAbandonTestServer(t, tmpDir)

	const issue = 282
	key := fmt.Sprintf("%d", issue)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(
		fmt.Sprintf(`{"repo":"acme/platform","issueNumber":%d,"stage":"feature-dev","status":"running"}`, issue),
	)); err != nil {
		t.Fatalf("transition: %v", err)
	}

	// No autonomous scheduler here — the handler errors out afterwards, which
	// must NOT skip the runtime bookkeeping.
	_, _ = s.methods["autonomous.complete"](context.Background(), json.RawMessage(
		fmt.Sprintf(`{"owner":"acme","repo":"platform","issueNumber":%d,"success":false,"terminalFailureKind":"user_abort","failureDetail":"[user-abort] slot force-cleared"}`, issue),
	))

	s.runtimesMu.Lock()
	_, stillActive := s.activeRuntimes[key]
	_, parked := s.abandonedRuntimes[key]
	s.runtimesMu.Unlock()

	if stillActive || !parked {
		t.Errorf("autonomous.complete with terminalFailureKind=user_abort did not move the runtime aside (active=%v parked=%v)", stillActive, parked)
	}
}

// TestAutonomousComplete_OrdinaryFailure_LeavesRuntimeAlone is the negative
// control: only the force-clear kind abandons a runtime. Every other failure
// still reaches pipeline.notifyComplete, which owns the drop.
func TestAutonomousComplete_OrdinaryFailure_LeavesRuntimeAlone(t *testing.T) {
	tmpDir := t.TempDir()
	s := newAbandonTestServer(t, tmpDir)

	const issue = 282
	key := fmt.Sprintf("%d", issue)
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(
		fmt.Sprintf(`{"repo":"acme/platform","issueNumber":%d,"stage":"feature-dev","status":"running"}`, issue),
	)); err != nil {
		t.Fatalf("transition: %v", err)
	}

	_, _ = s.methods["autonomous.complete"](context.Background(), json.RawMessage(
		fmt.Sprintf(`{"owner":"acme","repo":"platform","issueNumber":%d,"success":false,"terminalFailureKind":"stall_kill","failureDetail":"exceeded stall idle threshold"}`, issue),
	))

	s.runtimesMu.Lock()
	_, stillActive := s.activeRuntimes[key]
	_, parked := s.abandonedRuntimes[key]
	s.runtimesMu.Unlock()

	if !stillActive || parked {
		t.Errorf("an ordinary failure moved the runtime aside (active=%v parked=%v) — notifyComplete still owns the drop for every run that settles", stillActive, parked)
	}
}
