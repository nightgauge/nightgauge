package ipc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
)

var reconcileNow = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

// writeRuntimeSnapshot persists a RuntimeState fixture the same way the
// notifyStageTransition handler does, returning the file path — composed
// through the one composer, so the fixture's name is the name production
// writes (ADR-017 Decision 8).
func writeRuntimeSnapshot(t *testing.T, stateDir string, rt *state.RuntimeState) string {
	t.Helper()
	if err := rt.Persist(stateDir); err != nil {
		t.Fatalf("persist fixture: %v", err)
	}
	return filepath.Join(stateDir, state.SnapshotFilename(rt.IssueNumber, rt.RunID))
}

// newInterruptedRuntime builds a mid-run runtime under a REAL run identity —
// the identity is a constructor argument now, and a placeholder string would
// produce a filename the discovery regex rejects.
func newInterruptedRuntime(issueNumber int, runID string) *state.RuntimeState {
	rt := state.NewRuntimeState("nightgauge/acmeapp", issueNumber, "", runID)
	rt.BeginStage(state.StageIssuePickup)
	rt.CompleteStage(0, tokens.TokenCounts{Input: 0, Output: 0}, "")
	rt.BeginStage(state.StageFeatureDev)
	return rt
}

func TestCollectOrphanedRuns_BuildsTerminalEventForInterruptedRun(t *testing.T) {
	stateDir := t.TempDir()
	runID := newTestRunID()
	writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(205, runID))

	orphans := collectOrphanedRuns(stateDir, nil, reconcileNow)

	if len(orphans) != 1 {
		t.Fatalf("got %d orphans, want 1", len(orphans))
	}
	ev := orphans[0].Event
	if ev.EventType != "pipeline_done" {
		t.Errorf("EventType = %q, want pipeline_done", ev.EventType)
	}
	if ev.RunID != runID {
		t.Errorf("RunID = %q, want %q", ev.RunID, runID)
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

	paused := newInterruptedRuntime(101, newTestRunID())
	paused.SetPaused(true)
	writeRuntimeSnapshot(t, stateDir, paused)

	// The name/body-mismatch case is a CORRUPTION GUARD, not a discovery
	// filter: Persist refuses an identity-less runtime outright and composes
	// the filename from the same fields it marshals (ADR-017 Decision 1/8), so
	// the only way such a file exists is if something wrote a body that
	// disagrees with its own name. HAND-AUTHORED for exactly that reason.
	//
	// BOTH shapes are here, and the second is the one that pins the guard. An
	// EMPTY body runId is refused downstream by buildPipelineDoneEvent whether
	// the guard exists or not — a fixture using only that shape lets the guard
	// be deleted with this test green, which is what the earlier version of
	// this test did. A body carrying a DIFFERENT VALID identity is the shape
	// that escapes: it builds a perfectly well-formed pipeline_done and reports
	// the WRONG run terminal to the platform. Deleting the guard must turn this
	// test red, and it does.
	empty := filepath.Join(stateDir, state.SnapshotFilename(102, newTestRunID()))
	if err := os.WriteFile(empty, []byte(`{"issueNumber":102,"repo":"nightgauge/acmeapp","runId":"","completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	otherIdentity := newTestRunID()
	mismatched := filepath.Join(stateDir, state.SnapshotFilename(103, newTestRunID()))
	if err := os.WriteFile(mismatched, []byte(`{"issueNumber":103,"repo":"nightgauge/acmeapp","runId":"`+otherIdentity+
		`","stage":"feature-dev","completedStages":[{"stage":"issue-pickup","durationMs":1000}],"skippedStages":[],"phaseHistory":[],"stageErrors":{}}`), 0644); err != nil {
		t.Fatal(err)
	}

	orphans := collectOrphanedRuns(stateDir, nil, reconcileNow)

	if len(orphans) != 0 {
		ids := make([]string, 0, len(orphans))
		for _, o := range orphans {
			ids = append(ids, o.Event.RunID)
		}
		t.Fatalf("got %d orphans %v, want 0 — paused, content-runID-less, and name/body-mismatched files must all be skipped", len(orphans), ids)
	}
}

func TestCollectOrphanedRuns_SkipsLiveRuntimesAndIgnoresJunk(t *testing.T) {
	stateDir := t.TempDir()
	deadID := newTestRunID()
	writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(201, newTestRunID()))
	writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(202, deadID))

	// Junk that must not trip the scanner: a malformed new-scheme file, a
	// LEGACY-named file (which the new discovery regex must not match at all),
	// and an unrelated file. All hand-authored — production cannot emit them.
	if err := os.WriteFile(filepath.Join(stateDir, state.SnapshotFilename(999, newTestRunID())), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "runtime-998.json"), []byte(`{"issueNumber":998,"runId":"legacy"}`), 0644); err != nil {
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
	if orphans[0].Event.RunID != deadID {
		t.Errorf("RunID = %q, want %q", orphans[0].Event.RunID, deadID)
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
	file := writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(303, newTestRunID()))

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
	rt := newInterruptedRuntime(205, newTestRunID())
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
	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"stage":"issue-pickup","status":"running","runId":"019000cd-0000-7000-8000-000000000205"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}

	// The server-side interim mint survives until ADR-017 step 4, and it now
	// produces a canonical UUIDv7 — so the snapshot must be DISCOVERABLE by the
	// new scheme, which a v4 id would fail.
	rt := onlySnapshotForIssue(t, stateDir, 205)
	if rt.RunID == "" {
		t.Fatal("persisted snapshot must carry the run's platform UUID")
	}
	snapshotPath := filepath.Join(stateDir, state.SnapshotFilename(205, rt.RunID))

	complete := s.methods["pipeline.notifyComplete"]
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"success":true,"totalDurationMs":1000,"runId":"019000cd-0000-7000-8000-000000000205"}`)); err != nil {
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
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"stage":"feature-dev","status":"running","runId":"019000cd-0000-7000-8000-000000000205"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"stage":"feature-dev","status":"complete","inputTokens":1000,"outputTokens":500,"cacheReadTokens":200,"costUsd":5.03,"runId":"019000cd-0000-7000-8000-000000000205"}`)); err != nil {
		t.Fatalf("notifyStageTransition(complete): %v", err)
	}

	rt := onlySnapshotForIssue(t, stateDir, 205)
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
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":207,"stage":"feature-dev","status":"running","runId":"019000cf-0000-7000-8000-000000000207"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":207,"stage":"feature-dev","status":"complete","inputTokens":800,"outputTokens":300,"model":"sonnet","runId":"019000cf-0000-7000-8000-000000000207"}`)); err != nil {
		t.Fatalf("notifyStageTransition(complete): %v", err)
	}

	rt := onlySnapshotForIssue(t, stateDir, 207)
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

// TestNotifyStageTransition_FailedKeepsTheSnapshotForTheTerminalClaim pins the
// INVERSE of what this test asserted before ADR-017 step 4, deliberately.
//
// The `failed` transition used to remove the run's snapshot. That removal is
// DELETED (Decision 5), for two independent reasons:
//
//   - it was a second, redundant terminal path — notifyComplete fires
//     immediately after with Success=false — and it is what let a zombie destroy
//     a LIVE run's crash snapshot (F3);
//   - it was wrong on its own terms: if the host dies between the `failed`
//     transition and notifyComplete, the run never reached a terminal event and
//     DESERVES reconciliation, which the removal prevented.
//
// A canonical snapshot now leaves the directory through exactly three doors —
// the terminal claim's SealAndRemove, the reconciler, and the pause-restore
// claim rename — and a failed stage transition is none of them.
func TestNotifyStageTransition_FailedKeepsTheSnapshotForTheTerminalClaim(t *testing.T) {
	workspaceRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(workspaceRoot))
	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
	runID := newTestRunID()

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":206,"stage":"feature-dev","status":"running","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	snapshotPath := filepath.Join(stateDir, state.SnapshotFilename(206, runID))
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot must exist mid-run: %v", err)
	}

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":206,"stage":"feature-dev","status":"failed","error":"boom","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}
	persisted, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("a failed transition must PERSIST, not remove: %v", err)
	}
	if persisted.Terminal {
		t.Error("a failed stage transition is not the terminal claim; the durable marker must still be unset")
	}
	if persisted.StageErrors["feature-dev"] != "boom" {
		t.Errorf("the crash snapshot lost the failing stage's error: %+v", persisted.StageErrors)
	}

	// The terminal claim is the door: it seals the snapshot and removes it.
	if _, err := s.methods["pipeline.notifyComplete"](t.Context(),
		[]byte(`{"repo":"nightgauge/acmeapp","issueNumber":206,"success":false,"totalDurationMs":2000,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("the terminal claim must remove the snapshot, stat err = %v", err)
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

	targetDir := filepath.Join(targetRoot, ".nightgauge", "pipeline")
	launchDir := filepath.Join(launchRoot, ".nightgauge", "pipeline")

	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":244,"stage":"issue-pickup","status":"running","runId":"019000f4-0000-7000-8000-000000000244"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	rt := onlySnapshotForIssue(t, targetDir, 244)
	targetPath := filepath.Join(targetDir, state.SnapshotFilename(244, rt.RunID))
	if got, _ := state.FindPersistedStatesForIssue(launchDir, 244); len(got) != 0 {
		t.Fatalf("no snapshot may leak into the launch root, found %d", len(got))
	}

	complete := s.methods["pipeline.notifyComplete"]
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":244,"success":true,"totalDurationMs":1000,"runId":"019000f4-0000-7000-8000-000000000244"}`)); err != nil {
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
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":245,"stage":"issue-pickup","status":"running","runId":"019000f5-0000-7000-8000-000000000245"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	targetDir := filepath.Join(targetRoot, ".nightgauge", "pipeline")
	seeded := onlySnapshotForIssue(t, targetDir, 245)
	if err := os.Remove(filepath.Join(targetDir, state.SnapshotFilename(245, seeded.RunID))); err != nil {
		t.Fatalf("remove seeded snapshot: %v", err)
	}

	setPaused := s.methods["pipeline.setPaused"]
	if _, err := setPaused(t.Context(), []byte(`{"issueNumber":245,"paused":true,"runId":"019000f5-0000-7000-8000-000000000245"}`)); err != nil {
		t.Fatalf("setPaused: %v", err)
	}
	rt := onlySnapshotForIssue(t, targetDir, 245)
	if !rt.Paused {
		t.Fatal("persisted snapshot must record paused=true")
	}
	if got, _ := state.FindPersistedStatesForIssue(filepath.Join(launchRoot, ".nightgauge", "pipeline"), 245); len(got) != 0 {
		t.Fatalf("no paused snapshot may leak into the launch root, found %d", len(got))
	}
}

// getState's persisted-file fallback must read from the target repo's state
// dir, where the snapshot now lives (#215).
func TestGetState_FallbackReadsFromTargetRepo(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(launchRoot))
	s.RegisterRepo("nightgauge", "acmeapp", targetRoot)

	runID := newTestRunID()
	rt := newInterruptedRuntime(246, runID)
	writeRuntimeSnapshot(t, filepath.Join(targetRoot, ".nightgauge", "pipeline"), rt)

	getState := s.methods["pipeline.getState"]
	result, err := getState(t.Context(), []byte(`{"owner":"nightgauge","repo":"acmeapp","issueNumber":246}`))
	if err != nil {
		t.Fatalf("getState: %v", err)
	}
	// The response embeds the snapshot and adds the resolved identity
	// (ADR-017 Decision 6) — a superset of what it carried before the re-key.
	loaded, ok := result.(*PipelineGetStateResult)
	if !ok || loaded == nil {
		t.Fatalf("getState must return the persisted runtime, got %T", result)
	}
	if loaded.RunID != runID {
		t.Errorf("RunID = %q, want %q", loaded.RunID, runID)
	}
	if loaded.RuntimeState == nil || loaded.RuntimeState.RunID != runID {
		t.Errorf("the embedded snapshot must be the run that answered")
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

// onlySnapshotForIssue resolves the single snapshot an issue has in stateDir.
// Handler tests can no longer compose the filename: the run identity is minted
// inside the handler, so the file is DISCOVERED through the same
// FindPersistedStatesForIssue every production reader uses. It failing on zero
// or many candidates is the point — it also pins that the handler wrote exactly
// one file, under a name the new discovery regex matches.
func onlySnapshotForIssue(t *testing.T, stateDir string, issueNumber int) *state.RuntimeState {
	t.Helper()
	found, err := state.FindPersistedStatesForIssue(stateDir, issueNumber)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue(%d): %v", issueNumber, err)
	}
	if len(found) != 1 {
		t.Fatalf("#%d has %d snapshots in %s, want exactly 1", issueNumber, len(found), stateDir)
	}
	return found[0]
}
