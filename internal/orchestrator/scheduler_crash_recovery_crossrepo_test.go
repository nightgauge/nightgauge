// Tests covering Issue #239 — orchestrator-crash recovery must scan every
// registered repo root, not just the launch root. Since #229 a run's
// current-run.json sidecar is written at its TARGET repo root (via runRoot), so
// a cross-repo run that crashes mid-stage leaves its sidecar outside the launch
// root; recovery has to enumerate the registered paths to reconcile it into a
// terminal RunRecord.
package orchestrator

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// hasDailyJSONL reports whether workspaceRoot has any history JSONL file — used
// to assert a synthesized crash record landed (or did not land) under a given
// repo root.
func hasDailyJSONL(t *testing.T, workspaceRoot string) bool {
	t.Helper()
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// TestRecoverOrchestratorCrash_ReconcilesCrossRepoSidecar is the core #239
// regression: a sidecar rooted at a NON-launch repo (as #229 now writes it) is
// discovered, synthesized into a terminal-failure record under that repo root,
// removed, and the downstream queue is paused — none of which happened before
// the reconciler learned to scan registered repo roots.
func TestRecoverOrchestratorCrash_ReconcilesCrossRepoSidecar(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir() // a different, non-primary repo root

	startedAt := time.Now().UTC().Add(-30 * time.Second)
	stageStart := startedAt.Add(5 * time.Second)
	// The crashed run targeted the non-launch repo, so its sidecar lives under
	// targetRoot — exactly where runRoot(item.Repo) would have written it.
	if err := writeCurrentRunSidecar(targetRoot, CurrentRunSidecar{
		IssueNumber: 777,
		Repo:        "acme/platform",
		Title:       "Cross-repo run that crashed",
		StartedAt:   startedAt,
		Stage:       "feature-dev",
		StageStart:  stageStart,
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	s := &Scheduler{
		workspaceRoot: launchRoot,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		// One downstream item in the queue so we can assert the pause.
		queue: []QueueItem{
			{IssueNumber: 1000, Status: "pending", Repo: "acme/platform", Title: "Next up"},
		},
		// The IPC server wires this from ClientResolver.RegisteredPaths; here the
		// target repo is the only registered path besides the launch root.
		repoRootsResolver: func() []string { return []string{targetRoot} },
	}

	s.recoverOrchestratorCrash()

	// The sidecar under the non-launch repo root must be gone (reconciled).
	if _, err := os.Stat(filepath.Join(targetRoot, currentRunSidecarFile)); !os.IsNotExist(err) {
		t.Errorf("cross-repo sidecar should be removed after recovery, stat err=%v", err)
	}

	// The synthesized crash record must land under the TARGET repo root (where
	// the rest of that run's on-disk state lives, #229) — not the launch root.
	if hasDailyJSONL(t, launchRoot) {
		t.Errorf("launch root should have NO synthesized record — the crashed run belongs to the target repo")
	}
	records := readDailyJSONLRecords(t, targetRoot)
	if len(records) != 1 {
		t.Fatalf("expected 1 synthesized record under target repo root, got %d", len(records))
	}
	rec := records[0]
	if rec.IssueNumber != 777 {
		t.Errorf("rec.IssueNumber = %d, want 777", rec.IssueNumber)
	}
	if rec.TerminalFailureKind != TerminalKindOrchestratorCrash {
		t.Errorf("rec.TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, TerminalKindOrchestratorCrash)
	}

	// Downstream queue item paused, linked to the crashed run's FailedRunID.
	loaded := s.GetState()
	if len(loaded.Items) != 1 {
		t.Fatalf("queue item count = %d, want 1", len(loaded.Items))
	}
	item := loaded.Items[0]
	if item.Status != "paused" {
		t.Errorf("item.Status = %q, want paused", item.Status)
	}
	if item.PausedReason == nil {
		t.Fatalf("item.PausedReason is nil")
	}
	if want := FailedRunID(777, startedAt); item.PausedReason.FailedRunID != want {
		t.Errorf("item.PausedReason.FailedRunID = %q, want %q", item.PausedReason.FailedRunID, want)
	}
}

// TestRecoverOrchestratorCrash_NoDuplicateWhenPrimaryIsRegistered guards the
// idempotency criterion: the primary repo is typically BOTH the launch root and
// a registered path, so crashScanRoots must dedup and reconcile the single
// sidecar exactly once (one synthesized record, not two).
func TestRecoverOrchestratorCrash_NoDuplicateWhenPrimaryIsRegistered(t *testing.T) {
	launchRoot := t.TempDir()

	startedAt := time.Now().UTC().Add(-30 * time.Second)
	if err := writeCurrentRunSidecar(launchRoot, CurrentRunSidecar{
		IssueNumber: 555,
		Repo:        "nightgauge/nightgauge",
		StartedAt:   startedAt,
		Stage:       "feature-dev",
		StageStart:  startedAt,
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	s := &Scheduler{
		workspaceRoot: launchRoot,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		// Resolver returns the launch root itself (primary repo registered) — the
		// scan must not process the same sidecar twice.
		repoRootsResolver: func() []string { return []string{launchRoot} },
	}

	s.recoverOrchestratorCrash()

	records := readDailyJSONLRecords(t, launchRoot)
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 synthesized record (no duplicate), got %d", len(records))
	}
	if _, err := os.Stat(filepath.Join(launchRoot, currentRunSidecarFile)); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed after recovery, stat err=%v", err)
	}
}

// TestRecoverOrchestratorCrash_LaunchRootOnlyWhenResolverNil pins the CLI/auto
// (single-repo) behavior: with no roots resolver wired, only the launch root is
// scanned. A sidecar sitting under an unregistered non-launch directory is left
// untouched — recovery never reaches beyond the launch root.
func TestRecoverOrchestratorCrash_LaunchRootOnlyWhenResolverNil(t *testing.T) {
	launchRoot := t.TempDir()
	strayRoot := t.TempDir()

	startedAt := time.Now().UTC().Add(-30 * time.Second)
	if err := writeCurrentRunSidecar(strayRoot, CurrentRunSidecar{
		IssueNumber: 42,
		Repo:        "acme/platform",
		StartedAt:   startedAt,
		Stage:       "feature-dev",
		StageStart:  startedAt,
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	s := &Scheduler{
		workspaceRoot: launchRoot,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		// repoRootsResolver deliberately nil — CLI/auto mode.
	}

	s.recoverOrchestratorCrash()

	// The stray sidecar must be untouched (not reconciled, not removed) because
	// its root was never registered.
	if _, err := os.Stat(filepath.Join(strayRoot, currentRunSidecarFile)); err != nil {
		t.Errorf("unregistered sidecar should remain untouched, stat err=%v", err)
	}
	if hasDailyJSONL(t, strayRoot) {
		t.Errorf("no record should be synthesized under an unregistered root")
	}
	if hasDailyJSONL(t, launchRoot) {
		t.Errorf("no record should be synthesized under the launch root (no sidecar there)")
	}
}

// TestCrashScanRoots_DedupsAndOrders verifies the root-enumeration helper: the
// launch root is always first, registered paths follow, and duplicates (empty
// strings, the launch root re-registered) are collapsed.
func TestCrashScanRoots_DedupsAndOrders(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: "/launch",
		repoRootsResolver: func() []string {
			return []string{"/launch", "", "/repo-a", "/repo-b", "/repo-a"}
		},
	}
	got := s.crashScanRoots()
	want := []string{"/launch", "/repo-a", "/repo-b"}
	if len(got) != len(want) {
		t.Fatalf("crashScanRoots() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("crashScanRoots()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Nil resolver → launch root only (single-repo / CLI-auto).
	s2 := &Scheduler{workspaceRoot: "/launch"}
	if got := s2.crashScanRoots(); len(got) != 1 || got[0] != "/launch" {
		t.Errorf("crashScanRoots() with nil resolver = %v, want [/launch]", got)
	}
}
