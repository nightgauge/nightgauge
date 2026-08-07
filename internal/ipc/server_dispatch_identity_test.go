// Dispatch-identity guard on the extension terminal funnel (#307).
//
// The runtime registry is keyed by ISSUE NUMBER, which is not a run identity.
// abortAll's deadline can force-clear a wedged run and the operator can re-queue
// the same issue inside one extension-host session, so two producers reach these
// handlers under the same key: the dead run (still alive, still emitting) and
// its live successor. DispatchToken is what tells them apart.
//
// The guarantees pinned here:
//   - a completion whose token is not the active runtime's identity writes
//     NOTHING and deletes nothing (no run record, no learning outcome, no
//     telemetry, no runtime delete, no snapshot removal);
//   - a completion carrying a token for which no runtime exists is rejected too
//     — an absent runtime means the claimed identity is gone, never an
//     invitation to adopt whatever appears next;
//   - a stale mid-run stage transition cannot rewrite the live run's stage
//     history, and the new dispatch's `initialized` mints a fresh RunID rather
//     than adopting the dead run's;
//   - the force-cleared run's runtime-{N}.json survives, so #44 orphan
//     reconciliation can close the phantom at the next server start.
package ipc

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// runtimeSnapshotPath is where notifyStageTransition persists the crash-recovery
// snapshot for a run in the given repo root.
func runtimeSnapshotPath(root string, issueNumber int) string {
	return filepath.Join(root, ".nightgauge", "pipeline", "runtime-"+strconv.Itoa(issueNumber)+".json")
}

// activeRunID returns the RunID currently registered for an issue, or "".
func activeRunID(s *Server, issueNumber int) string {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	rt, ok := s.activeRuntimes[strconv.Itoa(issueNumber)]
	if !ok {
		return ""
	}
	return rt.RunID
}

// A completion whose dispatch token is not the active runtime's identity must be
// rejected outright: the successor's record, learning outcome, runtime and crash
// snapshot all survive untouched, and the dead run contributes nothing.
func TestNotifyComplete_RejectsStaleDispatchToken(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))
	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	// Run A (the run the abort deadline force-clears) reaches feature-dev.
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"init","status":"initialized","dispatchToken":"282:1:aaa"}`)
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"feature-dev","status":"running","dispatchToken":"282:1:aaa"}`)
	runA := activeRunID(s, 282)
	if runA == "" {
		t.Fatal("run A never got a RunID")
	}

	// The operator re-queues #282: a NEW dispatch claims the issue.
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"init","status":"initialized","dispatchToken":"282:2:bbb"}`)
	runB := activeRunID(s, 282)
	if runB == "" || runB == runA {
		t.Fatalf("successor must mint a fresh RunID: runA=%q runB=%q", runA, runB)
	}

	// The wedged run A finally dies and reports its own failure.
	res := mustCall(t, complete, `{"repo":"nightgauge/acmeapp","issueNumber":282,"success":false,"totalDurationMs":900000,"dispatchToken":"282:1:aaa"}`)
	if got := res["status"]; got != "stale" {
		t.Errorf("status = %q, want %q", got, "stale")
	}

	if records := readHistoryRecords(t, dir); len(records) != 0 {
		t.Fatalf("stale completion wrote %d run record(s); want 0", len(records))
	}
	if got := activeRunID(s, 282); got != runB {
		t.Errorf("successor runtime = %q after stale completion, want %q (untouched)", got, runB)
	}
	if _, err := os.Stat(runtimeSnapshotPath(dir, 282)); err != nil {
		t.Errorf("successor crash snapshot removed by the stale completion: %v", err)
	}
}

// The ordinary case must be unaffected: a completion whose token matches the
// runtime's identity records normally.
func TestNotifyComplete_AcceptsMatchingDispatchToken(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))
	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":307,"stage":"init","status":"initialized","dispatchToken":"307:1:aaa"}`)
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":307,"stage":"feature-dev","status":"running","dispatchToken":"307:1:aaa"}`)
	runID := activeRunID(s, 307)

	res := mustCall(t, complete, `{"repo":"nightgauge/acmeapp","issueNumber":307,"success":true,"totalDurationMs":1000,"dispatchToken":"307:1:aaa"}`)
	if got := res["status"]; got != "ok" {
		t.Fatalf("status = %q, want ok", got)
	}
	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	if records[0].RunID != runID {
		t.Errorf("RunRecord.RunID = %q, want the run's own identity %q", records[0].RunID, runID)
	}
	// Terminal: the runtime and its crash snapshot are dropped as before.
	if got := activeRunID(s, 307); got != "" {
		t.Errorf("runtime still registered after its own completion: %q", got)
	}
	if _, err := os.Stat(runtimeSnapshotPath(dir, 307)); !os.IsNotExist(err) {
		t.Errorf("crash snapshot survived the run's own terminal event: %v", err)
	}
}

// A token whose runtime is simply gone must be rejected, not adopted. This is
// the shape a force-cleared run hits after the IPC server restarted (or after a
// successor already completed and dropped the key): the identity it claims no
// longer exists, and writing under whatever occupies the key next is exactly the
// cross-contamination the guard exists to stop.
func TestNotifyComplete_RejectsTokenWithNoRuntime(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))
	complete := s.methods["pipeline.notifyComplete"]

	res := mustCall(t, complete, `{"repo":"nightgauge/acmeapp","issueNumber":404,"success":true,"totalDurationMs":1000,"dispatchToken":"404:1:aaa"}`)
	if got := res["status"]; got != "stale" {
		t.Errorf("status = %q, want %q", got, "stale")
	}
	if records := readHistoryRecords(t, dir); len(records) != 0 {
		t.Fatalf("orphan-token completion wrote %d run record(s); want 0", len(records))
	}
}

// A stale mid-run transition from the dead run must not touch the live run's
// stage history, cost totals or crash snapshot.
func TestNotifyStageTransition_RejectsStaleMidRunTransition(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))
	transition := s.methods["pipeline.notifyStageTransition"]

	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"init","status":"initialized","dispatchToken":"282:1:aaa"}`)
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"init","status":"initialized","dispatchToken":"282:2:bbb"}`)
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"issue-pickup","status":"complete","dispatchToken":"282:2:bbb","costUsd":1.5}`)
	runB := activeRunID(s, 282)

	// The dead run reports a stage completion of its own, minutes later.
	res := mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"feature-dev","status":"complete","dispatchToken":"282:1:aaa","costUsd":9.75}`)
	if got := res["status"]; got != "stale" {
		t.Errorf("status = %q, want %q", got, "stale")
	}

	s.runtimesMu.Lock()
	live := s.activeRuntimes["282"]
	s.runtimesMu.Unlock()
	if live == nil {
		t.Fatal("live runtime disappeared")
	}
	snap := live.Snapshot()
	if snap.RunID != runB {
		t.Errorf("live RunID = %q, want %q", snap.RunID, runB)
	}
	if len(snap.CompletedStages) != 1 {
		t.Errorf("live run has %d completed stages, want 1 (the stale one must not land)", len(snap.CompletedStages))
	}
	if snap.TotalCostUSD != 1.5 {
		t.Errorf("live TotalCostUSD = %v, want 1.5 (the dead run's spend must not land)", snap.TotalCostUSD)
	}
}

// The successor's `initialized` supersedes the dead run's runtime: fresh RunID,
// fresh identity, and the dead run's stage history does not carry over.
func TestNotifyStageTransition_SupersedeMintsFreshIdentity(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))
	transition := s.methods["pipeline.notifyStageTransition"]

	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"init","status":"initialized","dispatchToken":"282:1:aaa"}`)
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"issue-pickup","status":"complete","dispatchToken":"282:1:aaa","costUsd":4.25}`)
	runA := activeRunID(s, 282)

	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"init","status":"initialized","dispatchToken":"282:2:bbb"}`)

	s.runtimesMu.Lock()
	live := s.activeRuntimes["282"]
	s.runtimesMu.Unlock()
	snap := live.Snapshot()
	if snap.RunID == runA {
		t.Errorf("successor adopted the dead run's RunID %q", runA)
	}
	if snap.DispatchToken != "282:2:bbb" {
		t.Errorf("DispatchToken = %q, want the successor's", snap.DispatchToken)
	}
	if len(snap.CompletedStages) != 0 {
		t.Errorf("successor inherited %d completed stage(s) from the dead run", len(snap.CompletedStages))
	}
	if snap.TotalCostUSD != 0 {
		t.Errorf("successor inherited %v of the dead run's spend", snap.TotalCostUSD)
	}
}

// The truly-hung case: the force-cleared run never calls notifyComplete at all.
// Its runtime-{N}.json is deliberately left on disk, and #44 orphan
// reconciliation builds the terminal pipeline_done from it — that is how the run
// reaches a terminal record when nothing else can produce one.
func TestForceClearedRunSnapshotStaysReconcilable(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))
	transition := s.methods["pipeline.notifyStageTransition"]

	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"init","status":"initialized","dispatchToken":"282:1:aaa"}`)
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"issue-pickup","status":"complete","dispatchToken":"282:1:aaa"}`)
	mustCall(t, transition, `{"repo":"nightgauge/acmeapp","issueNumber":282,"stage":"feature-dev","status":"running","dispatchToken":"282:1:aaa"}`)
	runID := activeRunID(s, 282)

	// The extension force-clears the slot. It makes NO IPC call: the tombstone
	// is extension-local, and the snapshot is left exactly where it is.
	stateDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if _, err := os.Stat(filepath.Join(stateDir, "runtime-282.json")); err != nil {
		t.Fatalf("force-clear must leave the crash snapshot in place: %v", err)
	}

	// At the next server start activeRuntimes is empty, so nothing is skipped.
	orphans := collectOrphanedRuns(stateDir, func(int) bool { return false }, reconcileNow)
	if len(orphans) != 1 {
		t.Fatalf("got %d orphans, want 1 — the force-cleared run has no other path to a terminal event", len(orphans))
	}
	if orphans[0].Event.RunID != runID {
		t.Errorf("orphan RunID = %q, want the force-cleared run's own %q", orphans[0].Event.RunID, runID)
	}
	if orphans[0].Event.EventType != "pipeline_done" {
		t.Errorf("EventType = %q, want pipeline_done", orphans[0].Event.EventType)
	}
}

// A run PAUSED when the deadline fired keeps its snapshot too, and orphan
// reconciliation still skips it — the snapshot powers the pause-restore prompt
// (#2008), so a force-clear must never be the thing that destroys it.
func TestForceClearedPausedRunKeepsPauseRestoreSnapshot(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	paused := newInterruptedRuntime(282, "paused-force-cleared")
	paused.SetPaused(true)
	writeRuntimeSnapshot(t, stateDir, paused)

	orphans := collectOrphanedRuns(stateDir, func(int) bool { return false }, reconcileNow)
	if len(orphans) != 0 {
		t.Fatalf("got %d orphans, want 0 — a paused snapshot is resumable, not orphaned", len(orphans))
	}
	if _, err := os.Stat(filepath.Join(stateDir, "runtime-282.json")); err != nil {
		t.Errorf("paused snapshot must survive: %v", err)
	}
}

// The supersede branch and the orphan reconciler must build the SAME terminal
// event from a snapshot — one definition, so the second producer cannot drift.
func TestUnobservedRunDoneEvent_ClosesTheRunFromItsOwnSnapshot(t *testing.T) {
	rt := newInterruptedRuntime(282, "superseded-run-uuid")
	rt.DispatchToken = "282:1:aaa"

	ev, ok := unobservedRunDoneEvent(rt.Snapshot(), reconcileNow)
	if !ok {
		t.Fatal("no event built for a snapshot carrying a RunID")
	}
	if ev.RunID != "superseded-run-uuid" {
		t.Errorf("RunID = %q, want the superseded run's own", ev.RunID)
	}
	if ev.EventType != "pipeline_done" {
		t.Errorf("EventType = %q, want pipeline_done", ev.EventType)
	}
	if ev.Success == nil || *ev.Success {
		t.Errorf("Success = %v, want false — nothing observed a result", ev.Success)
	}

	// A snapshot with no RunID is not closable: the platform has no row to
	// transition, so the builder reports that rather than emitting a stub.
	if _, ok := unobservedRunDoneEvent(state.NewRuntimeState("nightgauge/acmeapp", 283, ""), reconcileNow); ok {
		t.Error("built an event for a RunID-less snapshot")
	}
	if _, ok := unobservedRunDoneEvent(nil, reconcileNow); ok {
		t.Error("built an event for a nil snapshot")
	}
}

// mustCall invokes an IPC handler with a raw JSON params blob and returns the
// string-map result handlers on this path produce.
func mustCall(t *testing.T, fn Handler, params string) map[string]string {
	t.Helper()
	out, err := fn(t.Context(), []byte(params))
	if err != nil {
		t.Fatalf("handler returned error for %s: %v", params, err)
	}
	res, ok := out.(map[string]string)
	if !ok {
		t.Fatalf("handler result %T is not map[string]string", out)
	}
	return res
}
