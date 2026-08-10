package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// #410. ActiveIssuesFromSnapshots is the machine-wide in-flight source for a
// process with no registry — the CLI worktree sweep's only protection against
// `git worktree remove --force` on a live run's directory. Every snapshot here is
// written by the package's own Persist, never hand-authored JSON of the shape
// under test; only file TIMESTAMPS are manipulated, which is a filesystem fact
// and not a schema claim.

func mustRunID(t *testing.T) string {
	t.Helper()
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatalf("mint run id: %v", err)
	}
	return id
}

// persistSnapshot writes one snapshot through Persist and returns its path.
func persistSnapshot(t *testing.T, dir string, rs *RuntimeState) string {
	t.Helper()
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("persist #%d: %v", rs.IssueNumber, err)
	}
	return filepath.Join(dir, SnapshotFilename(rs.IssueNumber, rs.RunID))
}

func backdate(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("backdate %s: %v", path, err)
	}
}

func TestActiveIssuesFromSnapshots_MissingDirIsDeterminedEmpty(t *testing.T) {
	res, err := ActiveIssuesFromSnapshots(filepath.Join(t.TempDir(), "never-ran"))
	if err != nil {
		t.Fatalf("a repo that never ran the pipeline is not an error: %v", err)
	}
	if len(res.Issues) != 0 {
		t.Errorf("expected no active issues, got %v", res.Issues)
	}
	if res.Issues == nil {
		t.Error("Issues must never be nil — a caller indexes it directly")
	}
}

// TestActiveIssuesFromSnapshots_FreshNonTerminalIsActive is the load-bearing
// case: a live run's snapshot is rewritten on every stage transition and
// progress tick, so its mtime is minutes old at worst.
func TestActiveIssuesFromSnapshots_FreshNonTerminalIsActive(t *testing.T) {
	dir := t.TempDir()
	persistSnapshot(t, dir, NewRuntimeState("owner/repo", 501, "item", mustRunID(t)))

	res, err := ActiveIssuesFromSnapshots(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[501] {
		t.Errorf("a fresh non-terminal snapshot must protect its issue; got %v (warnings %v)", res.Issues, res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_StaleNonTerminalIsNotActive is the deliberate
// LIMIT of the protection, and the reason this is not a bare "does a
// non-terminal snapshot exist" test.
//
// Neither the Go-scheduler terminal defer nor a crash latches terminal, and
// nothing on those paths removes the file — so a killed run's non-terminal
// snapshot survives indefinitely. Protecting on its mere existence would make
// the worktree sweep a permanent no-op for exactly the leaked-worktree
// population it exists to reclaim (#110), which is #403's structural-no-op
// defect re-created pointing the other way. The skip is WARNED, never silent.
func TestActiveIssuesFromSnapshots_StaleNonTerminalIsNotActive(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	path := persistSnapshot(t, dir, NewRuntimeState("owner/repo", 502, "item", mustRunID(t)))
	backdate(t, path, now.Add(-2*runstate.LivenessWindow))

	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Issues[502] {
		t.Error("a snapshot nothing has touched in an hour, with no live stage child, must not pin a worktree forever")
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "NOT protected") {
		t.Errorf("dropping an issue from the in-flight set must be reported, not silent; warnings = %v", res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_LiveStageChildOutranksTheTimestamp is ladder arm
// 3. A long stage (feature-dev regularly exceeds 30 minutes) can be silent past
// the lease while very much alive; the pid is what covers it.
func TestActiveIssuesFromSnapshots_LiveStageChildOutranksTheTimestamp(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	rs := NewRuntimeState("owner/repo", 503, "item", mustRunID(t))
	rs.SetProcess(os.Getpid(), filepath.Join(dir, "wt"))
	path := persistSnapshot(t, dir, rs)
	backdate(t, path, now.Add(-24*time.Hour))

	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[503] {
		t.Errorf("a live stage child must protect its run however quiet the snapshot is; got %v", res.Issues)
	}
}

// TestActiveIssuesFromSnapshots_TerminalTailWindow is the measured tail. The
// terminal marker lands before the worktree goes on both dispatch paths, so a
// FRESHLY terminal snapshot is not evidence the directory is free; an old one is.
func TestActiveIssuesFromSnapshots_TerminalTailWindow(t *testing.T) {
	now := time.Now()

	t.Run("just went terminal → still protected", func(t *testing.T) {
		dir := t.TempDir()
		rs := NewRuntimeState("owner/repo", 504, "item", mustRunID(t))
		rs.MarkTerminal("merged")
		persistSnapshot(t, dir, rs)

		res, err := activeIssuesFromSnapshotsAt(dir, now)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !res.Issues[504] {
			t.Error("the terminal tail still has bookkeeping to run in that directory")
		}
	})

	t.Run("terminal long ago → reclaimable", func(t *testing.T) {
		dir := t.TempDir()
		rs := NewRuntimeState("owner/repo", 505, "item", mustRunID(t))
		rs.MarkTerminal("merged")
		persistSnapshot(t, dir, rs)

		// TerminalAt is what decides, not the file's mtime: read the snapshot's
		// own clock forward rather than touching the file.
		res, err := activeIssuesFromSnapshotsAt(dir, now.Add(2*runstate.LivenessWindow))
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if res.Issues[505] {
			t.Error("a run terminal for an hour is done with its worktree")
		}
	})
}

// TestActiveIssuesFromSnapshots_PausedSurvivesTheLease: a pause is a deliberate
// "resume later" that powers the restore prompt days later (ADR-017 7.4's paused
// row). Ageing it out on a 30-minute liveness lease would hand a paused run's
// worktree to the sweep.
func TestActiveIssuesFromSnapshots_PausedSurvivesTheLease(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	rs := NewRuntimeState("owner/repo", 506, "item", mustRunID(t))
	rs.SetPaused(true)
	path := persistSnapshot(t, dir, rs)
	backdate(t, path, now.Add(-72*time.Hour))

	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[506] {
		t.Errorf("a paused run's worktree must survive until the pause is resolved; got %v (warnings %v)", res.Issues, res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_UnreadableSnapshotIsActive: the filename names a
// real run identity, so a body nobody can parse is the run whose directory must
// least of all be destroyed. Conservative AND warned — a silent "active" is
// indistinguishable from a healthy live run and would hide corruption forever.
func TestActiveIssuesFromSnapshots_UnreadableSnapshotIsActive(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, SnapshotFilename(507, mustRunID(t)))
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ActiveIssuesFromSnapshots(dir)
	if err != nil {
		t.Fatalf("one corrupt file must not fail the whole scan: %v", err)
	}
	if !res.Issues[507] {
		t.Error("an unreadable snapshot must count as ACTIVE")
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "corrupt") {
		t.Errorf("the guess must be reported; warnings = %v", res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_NameBodyMismatchIsActive mirrors the
// reconciler's identity-mismatch refusal. A body carrying a DIFFERENT valid
// identity is the one corruption shape that builds well-formed but wrong
// conclusions, so it is never trusted in the permissive direction.
func TestActiveIssuesFromSnapshots_NameBodyMismatchIsActive(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("owner/repo", 508, "item", mustRunID(t))
	real := persistSnapshot(t, dir, rs)
	data, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes, a filename claiming a different run: the body's identity and
	// the name's now disagree.
	if err := os.WriteFile(filepath.Join(dir, SnapshotFilename(509, mustRunID(t))), data, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ActiveIssuesFromSnapshots(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[509] {
		t.Error("a name/body identity mismatch must count as ACTIVE")
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "identity mismatch") {
		t.Errorf("the mismatch must be named; warnings = %v", res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_IgnoresNonSnapshotFiles: the pipeline dir also
// holds the current-run sidecar, exit-records, pause-restore claim artifacts and
// the history subdirectory. None of them is a snapshot, and none may silently
// pin an issue as in-flight.
func TestActiveIssuesFromSnapshots_IgnoresNonSnapshotFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"current-run.json",
		"run-state.json",
		"runtime-601.json", // the legacy scheme: cannot name its run
		"resuming-602-0192f0e1-2c34-7abc-8def-0123456789ab.1700000000.json",
		"batch-state.json",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "history"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := ActiveIssuesFromSnapshots(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Issues) != 0 {
		t.Errorf("non-snapshot files pinned issues as in-flight: %v", res.Issues)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("ordinary neighbours of the snapshot scheme are not warnings: %v", res.Warnings)
	}
}
