package ipc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/runstate"
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
	rt.CompleteStage(0, tokens.TokenCounts{Input: 0, Output: 0}, "", "")
	rt.BeginStage(state.StageFeatureDev)
	return rt
}

// backdate moves a fixture's mtime so LADDER ARM 4 (the disk-side lease) is
// false. writeRuntimeSnapshot goes through Persist, so every fixture is written
// "now" and arm 4 would skip it — a reconcile test that forgets this asserts
// nothing and passes (ADR-017 7.2).
func backdate(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("backdate %s: %v", filepath.Base(path), err)
	}
}

// staleSnapshot writes an interrupted run's snapshot and ages it past the
// liveness window, which is the precondition for every "this run is not alive"
// assertion below.
func staleSnapshot(t *testing.T, stateDir string, issueNumber int, runID string, now time.Time) string {
	t.Helper()
	path := writeRuntimeSnapshot(t, stateDir, newInterruptedRuntime(issueNumber, runID))
	backdate(t, path, now.Add(-2*livenessWindow))
	return path
}

func TestCollectReconcileActions_BuildsTerminalEventForInterruptedRun(t *testing.T) {
	stateDir := t.TempDir()
	runID := newTestRunID()
	staleSnapshot(t, stateDir, 205, runID, reconcileNow)

	orphans := collectReconcileActions(stateDir, runEvidence{}, reconcileNow)

	if len(orphans) != 1 {
		t.Fatalf("got %d orphans, want 1", len(orphans))
	}
	if orphans[0].Disposition != dispositionEmitAndRemove {
		t.Fatalf("disposition = %s, want emit+remove", orphans[0].Disposition)
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

func TestCollectReconcileActions_SkipsPausedAndRunIDLessSnapshots(t *testing.T) {
	stateDir := t.TempDir()

	paused := newInterruptedRuntime(101, newTestRunID())
	paused.SetPaused(true)
	// Aged past the LIVENESS window but well inside the 14-day cap: a paused
	// snapshot is exempt from reconciliation while it is fresh in the cap's
	// sense, because the restore prompt reads it at the next activation
	// (ADR-017 7.4, C5).
	backdate(t, writeRuntimeSnapshot(t, stateDir, paused), reconcileNow.Add(-2*livenessWindow))

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
	backdate(t, empty, reconcileNow.Add(-2*livenessWindow))
	backdate(t, mismatched, reconcileNow.Add(-2*livenessWindow))

	orphans := collectReconcileActions(stateDir, runEvidence{}, reconcileNow)

	if len(orphans) != 0 {
		ids := make([]string, 0, len(orphans))
		for _, o := range orphans {
			ids = append(ids, o.RunID)
		}
		t.Fatalf("got %d orphans %v, want 0 — paused, content-runID-less, and name/body-mismatched files must all be skipped", len(orphans), ids)
	}
}

// The skip is PER RUN now (ADR-017 7.2): a live lease pins its own run, not
// every snapshot that happens to share an issue number. The two runs below are
// two dispatches of the same issue, which is the case the issue-keyed predicate
// could not express at all.
func TestCollectReconcileActions_SkipsLiveRunsAndIgnoresJunk(t *testing.T) {
	stateDir := t.TempDir()
	liveID := newTestRunID()
	deadID := newTestRunID()
	staleSnapshot(t, stateDir, 201, liveID, reconcileNow)
	staleSnapshot(t, stateDir, 201, deadID, reconcileNow)

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

	ev := runEvidence{leaseFresh: func(id string) bool { return id == liveID }}
	orphans := collectReconcileActions(stateDir, ev, reconcileNow)

	if len(orphans) != 1 {
		t.Fatalf("got %d orphans, want 1", len(orphans))
	}
	if orphans[0].Event.RunID != deadID {
		t.Errorf("RunID = %q, want %q — the live run's sibling dispatch must be the one collected", orphans[0].Event.RunID, deadID)
	}
}

func TestCollectReconcileActions_MissingDirIsNoop(t *testing.T) {
	orphans := collectReconcileActions(filepath.Join(t.TempDir(), "does-not-exist"), runEvidence{}, reconcileNow)
	if len(orphans) != 0 {
		t.Fatalf("got %d orphans, want 0", len(orphans))
	}
}

// TestOrphanReconcile_RunsAndRemovesWithoutAnalytics is the DELIBERATE INVERSION
// of what this file asserted before ADR-017 step 5 (F24).
//
// The old test pinned "the snapshot survives a reconcile when analyticsSvc is
// nil", which is `reconcileOrphanedRuns` returning on line 1 — and AGENTS.md
// states the product "runs fully locally against your own model keys with no
// account and no server". On that first-class configuration the reconciler, the
// retention rules and the legacy sweep were all dead code, while the scheme
// moved from one file per ISSUE (overwritten by every re-dispatch) to one file
// per RUN: monotonic growth exactly where nothing collects it.
//
// Emission and removal are split. The scan and every removal rule run
// unconditionally; only the emission is skipped.
func TestOrphanReconcile_RunsAndRemovesWithoutAnalytics(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	now := time.Now()
	file := staleSnapshot(t, stateDir, 303, newTestRunID(), now)

	s := NewServer(nil, WithWorkspaceRoot(root))
	if s.analyticsSvc != nil {
		t.Fatal("this test is about the nil-analytics path")
	}
	// The emission the pass will NOT make is still decided, and decided the same
	// way — the platform's absence changes who is told, never what is collected.
	acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)
	if len(acts) != 1 || acts[0].Disposition != dispositionEmitAndRemove {
		t.Fatalf("collector gave %+v, want one emit+remove", acts)
	}

	s.reconcilePass(now)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("a local-only workspace must still collect its snapshots, stat err = %v", err)
	}

	// No workspace root: pipelineStateScanRoots yields nothing, so a second
	// server touches no directory at all. This arm was VACUOUS before the split
	// (the nil-analytics early return fired first); it is a real assertion now.
	other := t.TempDir()
	survivor := staleSnapshot(t, filepath.Join(other, ".nightgauge", "pipeline"), 304, newTestRunID(), now)
	rootless := NewServer(nil)
	if got := rootless.pipelineStateScanRoots(); len(got) != 0 {
		t.Fatalf("a rootless server must scan nothing, got %v", got)
	}
	rootless.reconcilePass(now)
	if _, err := os.Stat(survivor); err != nil {
		t.Fatalf("a server with no workspace root must not reach another directory: %v", err)
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

	backdate(t, filepath.Join(stateDir, state.SnapshotFilename(205, rt.RunID)), reconcileNow.Add(-2*livenessWindow))

	// Session 2 activates: collector finds the orphan, server removes the file.
	orphans := collectReconcileActions(stateDir, runEvidence{}, reconcileNow)
	if len(orphans) != 1 {
		t.Fatalf("first activation: got %d orphans, want 1", len(orphans))
	}
	if err := os.Remove(orphans[0].Path); err != nil {
		t.Fatalf("remove reconciled snapshot: %v", err)
	}

	// Session 3 activates: nothing left to reconcile.
	if again := collectReconcileActions(stateDir, runEvidence{}, reconcileNow); len(again) != 0 {
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
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":205,"stage":"feature-dev","status":"complete","inputTokens":1000,"outputTokens":500,"cacheReadTokens":200,"cacheCreationTokens":3308,"cacheCreation5mTokens":0,"cacheCreation1hTokens":3308,"costUsd":5.03,"runId":"019000cd-0000-7000-8000-000000000205"}`)); err != nil {
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
	if sr.CacheCreation != 3308 {
		t.Errorf("stage CacheCreation = %d, want 3308", sr.CacheCreation)
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
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":207,"stage":"feature-dev","status":"complete","inputTokens":800,"outputTokens":300,"cacheCreationTokens":3308,"cacheCreation1hTokens":3308,"model":"claude-haiku-4-5-20251001","runId":"019000cf-0000-7000-8000-000000000207"}`)); err != nil {
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
	if sr.CacheCreation != 3308 {
		t.Errorf("stage CacheCreation = %d, want 3308", sr.CacheCreation)
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
	launchRoot := t.TempDir() // e.g. acmeapp-infra — workspaceFolders[0]
	targetRoot := t.TempDir() // e.g. acmeapp-flutter — the run's repo
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

// --- #555: a scheduler-owned run is not reaped mid-flight -------------------

// blockingStageStub writes a stub CLI that reports its own pid and then blocks
// until `release` appears — a HEALTHY stage that is SILENT, which is the shape
// #555 is about. $$ is the pid of the process Go started, because the stub is
// the command exec.Command spawns.
func blockingStageStub(t *testing.T, dir string) (stub, release string) {
	t.Helper()
	release = filepath.Join(dir, "release")
	stub = filepath.Join(dir, "stub-blocking-stage.sh")
	script := fmt.Sprintf("#!/bin/sh\nwhile [ ! -f %s ]; do sleep 0.02; done\nexit 0\n", release)
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return stub, release
}

// pollUntil spins on cond until it holds or the deadline expires.
func pollUntil(t *testing.T, why string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, why)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestOrphanReconcile_SchedulerOwnedStageSilentPastTheWindowSurvivesOnItsLiveChild
// is #555's acceptance criterion, end to end and through the real reaper.
//
// The population is a run dispatched by the Go scheduler in ANOTHER process and
// reconciled by a `serve` daemon that has neither registry. Arms 1 and 2 are
// therefore structurally false; the stage below runs quietly past
// livenessWindow, so arm 4 is false; no grace is armed, so arm 5 is false. Arm 3
// is the only arm left, and before #555 it read the ZERO the scheduler's
// stage-start persist wrote (#534) — the ladder ran out of arms and a HEALTHY
// run was emitted as pipeline_done(success=false) and had its snapshot deleted
// out from under it.
//
// BOTH DIRECTIONS, one after the other on ONE run, so neither can be satisfied
// by a predicate that always answers the same way:
//
//	phase 1 — the child is alive: reconcilePass must leave the snapshot alone.
//	phase 2 — the child has exited and the pid is retracted: the very same
//	          reconcilePass, over the very same stale file, must collect it.
//
// The stage is driven through the REAL execution.Manager, not a hand-written
// snapshot: what is under test is whether production's spawn path puts a live
// pid on disk at all, and a fixture that set snap.PID itself would pass against
// unfixed source and pin nothing.
//
// RED-FIRST: delete the publishStageChild call after SetProcess in
// internal/execution/manager.go and phase 1 fails — "mid-flight … was reaped".
func TestOrphanReconcile_SchedulerOwnedStageSilentPastTheWindowSurvivesOnItsLiveChild(t *testing.T) {
	s, root, stateDir := reconcileServer(t)
	const issue = 555
	runID := newTestRunID()

	stub, release := blockingStageStub(t, t.TempDir())
	t.Setenv("NIGHTGAUGE_GROK_CLI_COMMAND", stub)
	// ensureWorktree returns early when the directory exists, so the spawn path
	// is reachable without a git repo behind it.
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "acmeapp-issue-555"), 0755); err != nil {
		t.Fatal(err)
	}

	rt := state.NewRuntimeState("nightgauge/acmeapp", issue, "", runID)
	rt.BeginStage(state.StageFeatureDev)
	// The scheduler's own stage-start persist (#534) — the file the reconciler
	// will find, carrying pid 0.
	if err := rt.Persist(stateDir); err != nil {
		t.Fatalf("stage-start persist: %v", err)
	}
	snapshot := filepath.Join(stateDir, state.SnapshotFilename(issue, runID))

	m := execution.NewManager(root, adapters.NewGrokAdapter())
	done := make(chan error, 1)
	go func() {
		_, runErr := m.RunStage(context.Background(), execution.StageOptions{
			Repo:        "nightgauge/acmeapp",
			IssueNumber: issue,
			Stage:       "feature-dev",
			Runtime:     rt,
			Timeout:     60 * time.Second,
		})
		done <- runErr
		close(done)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0644)
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
	})

	livePID := 0
	pollUntil(t, "the running stage to publish its live child into the snapshot", 30*time.Second, func() bool {
		snap, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
		if err != nil || snap == nil {
			return false
		}
		livePID = snap.PID
		return livePID != 0
	})
	if !runstate.ProcessAlive(livePID) {
		t.Fatalf("the published pid %d is not alive while its stage still runs", livePID)
	}

	// Thirty-plus minutes of a stage that says nothing. The file has not been
	// rewritten since the spawn, which is exactly what a long silent stage looks
	// like on disk.
	now := time.Now()
	backdate(t, snapshot, now.Add(-2*livenessWindow))
	if s.withinStartupGrace() {
		t.Fatal("this test is about the post-grace pass")
	}

	// Phase 1 — the real reaper, over the real file.
	if n := countEmissions(collectReconcileActions(stateDir, s.serverEvidence(now), now)); n != 0 {
		t.Errorf("%d terminal pipeline_done(s) built for a healthy stage that is merely quiet; want 0", n)
	}
	s.reconcilePass(now)
	mustExist(t, snapshot, "a scheduler-owned stage whose child is alive was reaped mid-flight (#555)")

	// Phase 2 — the child exits, the manager retracts the pid, and the same
	// stale file is now a genuine orphan: nothing holds a lease, no scheduler
	// claims it, no process is executing it.
	if err := os.WriteFile(release, nil, 0644); err != nil {
		t.Fatal(err)
	}
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("RunStage: %v", runErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunStage did not return after the stub was released")
	}
	after, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
	if err != nil || after == nil {
		t.Fatalf("reload after the stage exited: %v", err)
	}
	if after.PID != 0 {
		t.Errorf("on-disk pid after the stage exited = %d, want 0 — arm 3 must stop vouching the moment the child does", after.PID)
	}

	later := time.Now()
	backdate(t, snapshot, later.Add(-2*livenessWindow))
	acts := collectReconcileActions(stateDir, s.serverEvidence(later), later)
	if len(acts) != 1 || acts[0].Disposition != dispositionEmitAndRemove {
		t.Fatalf("collector gave %+v, want the one emit+remove a genuinely orphaned run earns", acts)
	}
	s.reconcilePass(later)
	mustBeGone(t, snapshot, "a run with no lease, no scheduler and no live child must still be collected")
}

// TestSkipRun_LivePidIsBoundedByTheSnapshotAgeCap pins the guard that keeps
// #555's fix from trading one silent failure for another.
//
// Arm 3 is the only arm that reads its evidence out of a FILE and then asks the
// kernel about it. #555 makes real pids reach disk on the scheduler path for the
// first time, so the recycled-pid case stops being theoretical: an owner killed
// mid-stage leaves a pid behind, the kernel eventually hands that number to an
// unrelated long-lived process, and an unbounded arm 3 would then answer true
// forever — an immortal snapshot and a platform run row stuck at 'running',
// which is the phantom-in-flight symptom this whole reconciler exists to end.
//
// RED-FIRST: drop the `now.Sub(modTime) <= runstate.SnapshotRetention` conjunct from arm 3
// and the past-the-cap case below skips instead of being collected.
func TestSkipRun_LivePidIsBoundedByTheSnapshotAgeCap(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Now()
	// This process is unambiguously alive, which is what a recycled pid looks
	// like to arm 3: a real, live, entirely unrelated process.
	live := os.Getpid()

	for _, tc := range []struct {
		name     string
		age      time.Duration
		wantSkip bool
	}{
		{"stale by the liveness window — the live child carries it", 2 * livenessWindow, true},
		{"older than the 14-day cap — 7.4's last row outranks the pid", 2 * runstate.SnapshotRetention, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runID := newTestRunID()
			rt := newInterruptedRuntime(560, runID)
			rt.SetStageChild(live)
			path := writeRuntimeSnapshot(t, stateDir, rt)
			modTime := now.Add(-tc.age)
			backdate(t, path, modTime)
			t.Cleanup(func() { _ = os.Remove(path) })

			snap, err := state.LoadSnapshotByIdentity(stateDir, 560, runID)
			if err != nil || snap == nil {
				t.Fatalf("reload: %v", err)
			}
			if snap.PID != live {
				t.Fatalf("fixture pid = %d, want the live pid %d", snap.PID, live)
			}

			ev := runEvidence{processAlive: runstate.ProcessAlive}
			if got := skipRun(ev, runID, snap, modTime, now); got != tc.wantSkip {
				t.Errorf("skipRun = %v, want %v", got, tc.wantSkip)
			}
			// Through the classifier too, so the row the ladder feeds is pinned
			// and not just the predicate.
			d := classifyCandidate(reconcileCandidate{
				name: filepath.Base(path), issue: 560, runID: runID, modTime: modTime, snap: snap,
			}, ev, now)
			want := dispositionKeep
			if !tc.wantSkip {
				want = dispositionEmitAndRemove
			}
			if d != want {
				t.Errorf("disposition = %s, want %s", d, want)
			}
		})
	}
}

// --- The emission seam (#472) ------------------------------------------------

// countingEmitter is the fake that makes the reconciler's terminal emission
// OBSERVABLE. Before pipelineEventEmitter existed, s.analyticsSvc could only
// hold a *platform.AnalyticsService — which needs a live platform client — so
// every test here pinned the pure collector (what WOULD be emitted) and the
// actual emit could be deleted with the whole package still green.
//
// Guarded because EmitPipelineEvent is reached from whatever goroutine runs the
// reconcile pass, and -race is in this issue's definition of done.
type countingEmitter struct {
	mu     sync.Mutex
	events []platform.PipelineEvent
}

func (c *countingEmitter) EmitPipelineEvent(_ context.Context, event platform.PipelineEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *countingEmitter) snapshot() []platform.PipelineEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]platform.PipelineEvent(nil), c.events...)
}

// A server built without a platform client must leave analyticsSvc an ACTUALLY
// nil interface. A nil *AnalyticsService stored in the interface is a non-nil
// interface value: every `analyticsSvc != nil` guard would pass and the first
// emission would dereference nothing.
func TestServerWithoutPlatformClient_AnalyticsSvcIsNilInterface(t *testing.T) {
	s := NewServer(nil)
	if s.analyticsSvc != nil {
		t.Fatalf("analyticsSvc must be a nil interface with no platform client, got %#v", s.analyticsSvc)
	}
	if s.getAnalyticsSvc() != nil {
		t.Fatal("getAnalyticsSvc must be nil with no platform client")
	}
}

// TestApplyReconcileAction_EmitAndRemoveEmitsExactlyOnce pins the EFFECT half:
// exactly one terminal event actually reaches the platform, carrying this run's
// identity, and the snapshot is gone. Deleting the emit in
// applyReconcileAction's dispositionEmitAndRemove arm must turn this red.
func TestApplyReconcileAction_EmitAndRemoveEmitsExactlyOnce(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	now := time.Now()
	runID := newTestRunID()
	file := staleSnapshot(t, stateDir, 472, runID, now)

	fake := &countingEmitter{}
	s := NewServer(nil, WithWorkspaceRoot(root))
	s.analyticsSvc = fake

	acts := collectReconcileActions(stateDir, s.serverEvidence(now), now)
	if len(acts) != 1 || acts[0].Disposition != dispositionEmitAndRemove {
		t.Fatalf("collector gave %+v, want exactly one emit+remove", acts)
	}

	s.applyReconcileAction(stateDir, acts[0])

	events := fake.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d emitted events, want exactly 1", len(events))
	}
	ev := events[0]
	if ev.RunID != runID {
		t.Errorf("emitted RunID = %q, want %q", ev.RunID, runID)
	}
	if ev.IssueNumber != 472 {
		t.Errorf("emitted IssueNumber = %d, want 472", ev.IssueNumber)
	}
	if ev.EventType != "pipeline_done" {
		t.Errorf("emitted EventType = %q, want %q", ev.EventType, "pipeline_done")
	}
	if ev.Success == nil {
		t.Fatal("emitted Success is nil; a reconciled orphan must be an explicit failure")
	}
	if *ev.Success {
		t.Error("emitted Success = true, want false — the run never finished")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("snapshot must be removed after the emission, stat err = %v", err)
	}
}

// The sibling arm: the two dispositions whose whole point is that the platform
// was ALREADY told (or has nothing terminal to be told) must emit nothing.
func TestApplyReconcileAction_RemoveDoesNotEmit(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	fake := &countingEmitter{}
	s := NewServer(nil, WithWorkspaceRoot(root))
	s.analyticsSvc = fake

	// dispositionRemove: the terminal claim or abandonRun already emitted.
	removeRunID := newTestRunID()
	removePath := filepath.Join(stateDir, state.SnapshotFilename(600, removeRunID))
	if err := os.WriteFile(removePath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.applyReconcileAction(stateDir, reconcileAction{
		Path:        removePath,
		Disposition: dispositionRemove,
		RunID:       removeRunID,
		Issue:       600,
	})
	if _, err := os.Stat(removePath); !os.IsNotExist(err) {
		t.Fatalf("dispositionRemove must remove the file, stat err = %v", err)
	}

	// dispositionReleaseClaim: a pause survives; nothing terminal happened.
	claimRunID := newTestRunID()
	claimPath := filepath.Join(stateDir, state.SnapshotFilename(601, claimRunID)+".claim")
	if err := os.WriteFile(claimPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.applyReconcileAction(stateDir, reconcileAction{
		Path:        claimPath,
		Disposition: dispositionReleaseClaim,
		RunID:       claimRunID,
		Issue:       601,
	})

	if got := fake.snapshot(); len(got) != 0 {
		t.Fatalf("got %d emitted events for non-emitting dispositions, want 0: %+v", len(got), got)
	}
}
