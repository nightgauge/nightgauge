package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

var reconcileNow = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

// writeRuntimeSnapshot persists a RuntimeState fixture the same way the
// notifyStageTransition handler does, returning the file path.
func writeRuntimeSnapshot(t *testing.T, stateDir string, rt *state.RuntimeState) string {
	t.Helper()
	if err := rt.Persist(stateDir); err != nil {
		t.Fatalf("persist fixture: %v", err)
	}
	return filepath.Join(stateDir, fmt.Sprintf("runtime-%d.json", rt.IssueNumber))
}

func newInterruptedRuntime(issueNumber int, runID string) *state.RuntimeState {
	rt := state.NewRuntimeState("nightgauge/acmeapp", issueNumber, "")
	rt.RunID = runID
	rt.BeginStage(state.StageIssuePickup)
	rt.CompleteStage(0, 0, 0, "")
	rt.BeginStage(state.StageFeatureDev)
	return rt
}

func TestCollectOrphanedRuns_BuildsTerminalEventForInterruptedRun(t *testing.T) {
	stateDir := t.TempDir()
	writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(205, "orphan-run-uuid"))

	orphans := collectOrphanedRuns(stateDir, nil, reconcileNow)

	if len(orphans) != 1 {
		t.Fatalf("got %d orphans, want 1", len(orphans))
	}
	ev := orphans[0].Event
	if ev.EventType != "pipeline_done" {
		t.Errorf("EventType = %q, want pipeline_done", ev.EventType)
	}
	if ev.RunID != "orphan-run-uuid" {
		t.Errorf("RunID = %q, want orphan-run-uuid", ev.RunID)
	}
	if ev.IssueNumber != 205 {
		t.Errorf("IssueNumber = %d, want 205", ev.IssueNumber)
	}
	if ev.Success == nil || *ev.Success {
		t.Errorf("Success = %v, want false", ev.Success)
	}
	// Only the completed canonical stage is reported — the interrupted
	// feature-dev stage never finished.
	if len(ev.StagesRun) != 1 || ev.StagesRun[0] != string(state.StageIssuePickup) {
		t.Errorf("StagesRun = %v, want [issue-pickup]", ev.StagesRun)
	}
}

func TestCollectOrphanedRuns_SkipsPausedAndRunIDLessSnapshots(t *testing.T) {
	stateDir := t.TempDir()

	paused := newInterruptedRuntime(101, "paused-run-uuid")
	paused.SetPaused(true)
	writeRuntimeSnapshot(t, stateDir, paused)

	noRunID := state.NewRuntimeState("nightgauge/acmeapp", 102, "")
	writeRuntimeSnapshot(t, stateDir, noRunID)

	orphans := collectOrphanedRuns(stateDir, nil, reconcileNow)

	if len(orphans) != 0 {
		t.Fatalf("got %d orphans, want 0 (paused + runID-less must be skipped)", len(orphans))
	}
}

func TestCollectOrphanedRuns_SkipsLiveRuntimesAndIgnoresJunk(t *testing.T) {
	stateDir := t.TempDir()
	writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(201, "live-run-uuid"))
	writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(202, "dead-run-uuid"))

	// Junk that must not trip the scanner: malformed runtime file, unrelated file.
	if err := os.WriteFile(filepath.Join(stateDir, "runtime-999.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "run-state.json"), []byte(`{"state":"running"}`), 0644); err != nil {
		t.Fatal(err)
	}

	skipIssue := func(n int) bool { return n == 201 }
	orphans := collectOrphanedRuns(stateDir, skipIssue, reconcileNow)

	if len(orphans) != 1 {
		t.Fatalf("got %d orphans, want 1", len(orphans))
	}
	if orphans[0].Event.RunID != "dead-run-uuid" {
		t.Errorf("RunID = %q, want dead-run-uuid", orphans[0].Event.RunID)
	}
}

func TestCollectOrphanedRuns_MissingDirIsNoop(t *testing.T) {
	orphans := collectOrphanedRuns(filepath.Join(t.TempDir(), "does-not-exist"), nil, reconcileNow)
	if len(orphans) != 0 {
		t.Fatalf("got %d orphans, want 0", len(orphans))
	}
}

func TestReconcileOrphanedRuns_GuardsWithoutAnalyticsOrRoot(t *testing.T) {
	stateDir := t.TempDir()
	file := writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(303, "guarded-run-uuid"))

	// No analytics service: reconcile must not delete evidence it cannot emit.
	s := NewServer(nil, WithWorkspaceRoot(filepath.Dir(filepath.Dir(stateDir))))
	s.reconcileOrphanedRuns()
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("snapshot must survive reconcile without analytics service: %v", err)
	}

	// No workspace root: same guard.
	s2 := NewServer(nil)
	s2.reconcileOrphanedRuns()
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("snapshot must survive reconcile without workspace root: %v", err)
	}
}

// Crash → reopen → reconcile: the persisted snapshot from the "crashed"
// session is turned into exactly one pipeline_done and removed, and a second
// activation finds nothing (idempotent). Uses the pure collector plus the
// same removal the server performs, since AnalyticsService requires a live
// platform client; event emission itself is covered by the guard test above
// and the builder assertions.
func TestOrphanReconcile_CrashReopenFlowIsIdempotent(t *testing.T) {
	workspaceRoot := t.TempDir()
	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")

	// Session 1 "crashes" after persisting mid-run state.
	rt := newInterruptedRuntime(205, "crashed-run-uuid")
	if err := rt.Persist(stateDir); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// Session 2 activates: collector finds the orphan, server removes the file.
	orphans := collectOrphanedRuns(stateDir, nil, reconcileNow)
	if len(orphans) != 1 {
		t.Fatalf("first activation: got %d orphans, want 1", len(orphans))
	}
	if err := os.Remove(orphans[0].FilePath); err != nil {
		t.Fatalf("remove reconciled snapshot: %v", err)
	}

	// Session 3 activates: nothing left to reconcile.
	if again := collectOrphanedRuns(stateDir, nil, reconcileNow); len(again) != 0 {
		t.Fatalf("second activation: got %d orphans, want 0 (must be idempotent)", len(again))
	}
}

// The handler-level half of the crash-recovery contract: every stage
// transition persists the runtime snapshot (so a crash leaves the RunID on
// disk), and the terminal pipeline.notifyComplete removes it.
func TestNotifyStageTransition_PersistsSnapshotAndNotifyCompleteRemovesIt(t *testing.T) {
	workspaceRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(workspaceRoot))
	snapshotPath := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "runtime-205.json")

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"stage":"issue-pickup","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("snapshot must exist after a stage transition: %v", err)
	}
	rt, err := state.LoadPersistedState(filepath.Dir(snapshotPath), 205)
	if err != nil {
		t.Fatalf("snapshot must parse: %v (raw: %s)", err, data)
	}
	if rt.RunID == "" {
		t.Fatal("persisted snapshot must carry the run's platform UUID")
	}

	complete := s.methods["pipeline.notifyComplete"]
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("snapshot must be removed after the terminal event, stat err = %v", err)
	}
}

// #227: a "complete" transition must persist the per-stage tokens/cost the
// extension threads through the notify params, not the old hardcoded zeros.
// Both the completed-stage entry and the accumulated top-level totals must
// carry the real values, with cache reads folded into InputTokens (matching
// the scheduler path via CompleteStageWithCost).
func TestNotifyStageTransition_CompletePersistsTokensAndCost(t *testing.T) {
	workspaceRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(workspaceRoot))
	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"stage":"feature-dev","status":"complete","inputTokens":1000,"outputTokens":500,"cacheReadTokens":200,"costUsd":5.03}`)); err != nil {
		t.Fatalf("notifyStageTransition(complete): %v", err)
	}

	rt, err := state.LoadPersistedState(stateDir, 205)
	if err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	if len(rt.CompletedStages) != 1 {
		t.Fatalf("CompletedStages = %d, want 1", len(rt.CompletedStages))
	}
	sr := rt.CompletedStages[0]
	// InputTokens is the combined value (actual input + cache read).
	if sr.InputTokens != 1200 {
		t.Errorf("stage InputTokens = %d, want 1200 (1000 input + 200 cache read)", sr.InputTokens)
	}
	if sr.OutputTokens != 500 {
		t.Errorf("stage OutputTokens = %d, want 500", sr.OutputTokens)
	}
	if sr.CacheRead != 200 {
		t.Errorf("stage CacheRead = %d, want 200", sr.CacheRead)
	}
	if sr.CostUSD != 5.03 {
		t.Errorf("stage CostUSD = %v, want 5.03", sr.CostUSD)
	}
	// Top-level totals accumulate from the completed stage.
	if rt.InputTokens != 1200 {
		t.Errorf("total InputTokens = %d, want 1200", rt.InputTokens)
	}
	if rt.OutputTokens != 500 {
		t.Errorf("total OutputTokens = %d, want 500", rt.OutputTokens)
	}
	if rt.TotalCostUSD != 5.03 {
		t.Errorf("total TotalCostUSD = %v, want 5.03", rt.TotalCostUSD)
	}
}

// #227 fallback: when no authoritative cost is provided (costUsd == 0), the
// handler still records the threaded token counts via CompleteStage (cost is
// then derived from the model rate rather than being lost as zeros).
func TestNotifyStageTransition_CompleteWithoutCostStillRecordsTokens(t *testing.T) {
	workspaceRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(workspaceRoot))
	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":207,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":207,"stage":"feature-dev","status":"complete","inputTokens":800,"outputTokens":300,"model":"sonnet"}`)); err != nil {
		t.Fatalf("notifyStageTransition(complete): %v", err)
	}

	rt, err := state.LoadPersistedState(stateDir, 207)
	if err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	if len(rt.CompletedStages) != 1 {
		t.Fatalf("CompletedStages = %d, want 1", len(rt.CompletedStages))
	}
	sr := rt.CompletedStages[0]
	if sr.InputTokens != 800 {
		t.Errorf("stage InputTokens = %d, want 800", sr.InputTokens)
	}
	if sr.OutputTokens != 300 {
		t.Errorf("stage OutputTokens = %d, want 300", sr.OutputTokens)
	}
	if rt.InputTokens != 800 || rt.OutputTokens != 300 {
		t.Errorf("totals = (%d,%d), want (800,300)", rt.InputTokens, rt.OutputTokens)
	}
}

// A failed stage transition is terminal for the run — its snapshot must not
// linger to be mis-reconciled as an orphan on next activation.
func TestNotifyStageTransition_FailedRemovesSnapshot(t *testing.T) {
	workspaceRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(workspaceRoot))
	snapshotPath := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "runtime-206.json")

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":206,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot must exist mid-run: %v", err)
	}

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":206,"stage":"feature-dev","status":"failed","error":"boom"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("snapshot must be removed on failed transition, stat err = %v", err)
	}
}

// Multi-repo scoping (#215): a run targeting a registered sibling repo must
// persist its runtime snapshot into THAT repo's .nightgauge/pipeline dir —
// the same root its stage context files use — not the IPC server's launch
// root, and the terminal notifyComplete must remove it from there.
func TestNotifyStageTransition_PersistsSnapshotIntoTargetRepo(t *testing.T) {
	launchRoot := t.TempDir() // e.g. bowlsheet-infra — workspaceFolders[0]
	targetRoot := t.TempDir() // e.g. bowlsheet-flutter — the run's repo
	s := NewServer(nil, WithWorkspaceRoot(launchRoot))
	s.RegisterRepo("nightgauge", "acmeapp", targetRoot)

	targetPath := filepath.Join(targetRoot, ".nightgauge", "pipeline", "runtime-244.json")
	launchPath := filepath.Join(launchRoot, ".nightgauge", "pipeline", "runtime-244.json")

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":244,"stage":"issue-pickup","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("snapshot must land in the run's target repo: %v", err)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("no snapshot may leak into the launch root, stat err = %v", err)
	}

	complete := s.methods["pipeline.notifyComplete"]
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":244,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("snapshot must be removed from the target repo after the terminal event, stat err = %v", err)
	}
}

// setPaused must persist into the run's target repo too — the snapshot
// powers the pause-restore prompt, so writing it anywhere else strands it.
func TestSetPaused_PersistsIntoTargetRepo(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(launchRoot))
	s.RegisterRepo("nightgauge", "acmeapp", targetRoot)

	// Seed the runtime's repo via a stage transition, then drop the snapshot
	// so the only writer left to observe is setPaused itself.
	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":245,"stage":"issue-pickup","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	targetPath := filepath.Join(targetRoot, ".nightgauge", "pipeline", "runtime-245.json")
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove seeded snapshot: %v", err)
	}

	setPaused := s.methods["pipeline.setPaused"]
	if _, err := setPaused(t.Context(), []byte(`{"issueNumber":245,"paused":true}`)); err != nil {
		t.Fatalf("setPaused: %v", err)
	}
	rt, err := state.LoadPersistedState(filepath.Dir(targetPath), 245)
	if err != nil {
		t.Fatalf("paused snapshot must be in the target repo: %v", err)
	}
	if !rt.Paused {
		t.Fatal("persisted snapshot must record paused=true")
	}
	if _, err := os.Stat(filepath.Join(launchRoot, ".nightgauge", "pipeline", "runtime-245.json")); !os.IsNotExist(err) {
		t.Fatalf("no paused snapshot may leak into the launch root, stat err = %v", err)
	}
}

// getState's persisted-file fallback must read from the target repo's state
// dir, where the snapshot now lives (#215).
func TestGetState_FallbackReadsFromTargetRepo(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(launchRoot))
	s.RegisterRepo("nightgauge", "acmeapp", targetRoot)

	rt := newInterruptedRuntime(246, "persisted-run-uuid")
	writeRuntimeSnapshot(t, filepath.Join(targetRoot, ".nightgauge", "pipeline"), rt)

	getState := s.methods["pipeline.getState"]
	result, err := getState(t.Context(), []byte(`{"owner":"nightgauge","repo":"acmeapp","issueNumber":246}`))
	if err != nil {
		t.Fatalf("getState: %v", err)
	}
	loaded, ok := result.(*state.RuntimeState)
	if !ok || loaded == nil {
		t.Fatalf("getState must return the persisted runtime, got %T", result)
	}
	if loaded.RunID != "persisted-run-uuid" {
		t.Errorf("RunID = %q, want persisted-run-uuid", loaded.RunID)
	}
}

// The orphan scan must cover every registered repo root, deduped against the
// launch root, or crash recovery misses cross-repo runs (#215).
func TestPipelineStateScanRoots_CoversRegisteredReposDeduped(t *testing.T) {
	launchRoot := t.TempDir()
	siblingRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(launchRoot))
	s.RegisterRepo("nightgauge", "infra", launchRoot) // same as launch root — must dedupe
	s.RegisterRepo("nightgauge", "acmeapp", siblingRoot)

	roots := s.pipelineStateScanRoots()
	if len(roots) != 2 {
		t.Fatalf("got %d roots %v, want 2 (launch + sibling, deduped)", len(roots), roots)
	}
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r] = true
	}
	if !seen[launchRoot] || !seen[siblingRoot] {
		t.Errorf("roots %v must contain launch root and sibling root", roots)
	}
}
