package state

import (
	"fmt"
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

// TestActiveIssuesFromSnapshots_FreshNonTerminalIsActive is arm 4 at its
// strongest: the snapshot was written a moment ago, so a stage boundary happened
// inside the lease. Note what the lease does NOT mean — the file is refreshed at
// stage boundaries only (no progress tick persists anything, there is no
// heartbeat), so a healthy long stage can outlive it. The sidecar arm is what
// covers that case.
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
// 3: a RECORDED stage child that is still alive outranks a quiet file.
//
// This is the extension path's shape, where the IPC server persists the pid of
// the child it was told about. It is NOT reachable on the Go-scheduler path —
// there SetProcess is an in-memory mutation and the persist happens after the
// stage completes, so the pid on disk always names a child that has exited. The
// sequence below (SetProcess then Persist while the pid is alive) is deliberately
// the extension's, and the Go path's equivalent is
// TestActiveIssuesFromSnapshots_SidecarWithALivePidProtects.
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
// pin an issue as in-flight. The sidecar IS read (as its own arm) but only
// protects the issue it names when the process it records is alive — the `{}`
// written here names nobody and vouches for nothing.
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

// writeSidecar writes the in-flight sidecar the way internal/orchestrator's
// writeCurrentRunSidecar does. Hand-authored HERE only because internal/
// orchestrator imports internal/state, so this package cannot import the
// production writer without a cycle — the shape is pinned against that writer by
// TestCurrentRunSidecar_ProtectsItsIssueThroughTheStateReader in
// internal/orchestrator, so a field rename fails there rather than silently
// disarming this arm.
func writeSidecar(t *testing.T, dir string, issue, pid int, runID string) {
	t.Helper()
	body := fmt.Sprintf(`{"issue_number":%d,"repo":"owner/repo","run_id":%q,"started_at":%q,"stage":"feature-dev","stage_started_at":%q,"pid":%d}`,
		issue, runID, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), pid)
	if err := os.WriteFile(filepath.Join(dir, "current-run.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

// TestActiveIssuesFromSnapshots_SidecarWithALivePidProtects is the Go-scheduler
// arm, and the reason it has to exist.
//
// On that path nothing persists the snapshot WHILE a stage runs: SetProcess is
// an in-memory mutation and internal/execution holds no Persist call at all, so
// the live child's pid never reaches disk; the orchestrator persists at each
// stage's START (#534) and again when it COMPLETES, and nothing in between. The
// stage-start write clears the pid to 0 rather than republish the previous
// stage's exited child, so neither write ever puts a live pid on disk here. A
// live run in a long stage therefore has exactly the snapshot below — no usable
// pid, old mtime — and arms 3 and 4 both decline it. The sidecar is the one
// signal that path writes while the run is alive (PID: os.Getpid() at stage
// start, removed on clean completion), and it lives in the very directory this
// scan already walks.
func TestActiveIssuesFromSnapshots_SidecarWithALivePidProtects(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	runID := mustRunID(t)
	rs := NewRuntimeState("owner/repo", 510, "item", runID)
	path := persistSnapshot(t, dir, rs)
	backdate(t, path, now.Add(-24*time.Hour)) // no stage boundary in a day
	writeSidecar(t, dir, 510, os.Getpid(), runID)

	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[510] {
		t.Errorf("a run whose orchestrator process is ALIVE was not protected; got %v (warnings %v)", res.Issues, res.Warnings)
	}
	if strings.Contains(strings.Join(res.Warnings, "\n"), "NOT protected") {
		t.Errorf("the run is protected, so nothing may report it as dropped; warnings = %v", res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_SidecarWithADeadPidProtectsNothing is the other
// half. A sidecar OUTLIVES a crashed orchestrator — that is what it is for, the
// crash synthesizer reads it at the next startup — so protecting on its existence
// would pin the crashed run's worktree until an orchestrator happened to start in
// that repo again. Existence is not the gate; liveness is.
func TestActiveIssuesFromSnapshots_SidecarWithADeadPidProtectsNothing(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	runID := mustRunID(t)
	path := persistSnapshot(t, dir, NewRuntimeState("owner/repo", 511, "item", runID))
	backdate(t, path, now.Add(-24*time.Hour))
	// A pid that cannot be alive: 0 is never a live process (runstate.ProcessAlive
	// rejects it outright), and using a real-but-exited pid would be racy.
	writeSidecar(t, dir, 511, 0, runID)

	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Issues[511] {
		t.Error("a sidecar left behind by a dead orchestrator must not pin a worktree forever")
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "NOT protected") {
		t.Errorf("dropping an issue must be reported; warnings = %v", res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_UnparseableSidecarIsWarned: a sidecar that exists
// and cannot be parsed protects nothing (it cannot name an issue), which is the
// one shape worth surfacing rather than swallowing.
func TestActiveIssuesFromSnapshots_UnparseableSidecarIsWarned(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "current-run.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ActiveIssuesFromSnapshots(dir)
	if err != nil {
		t.Fatalf("one unparseable sidecar must not fail the whole scan: %v", err)
	}
	if len(res.Issues) != 0 {
		t.Errorf("an unparseable sidecar cannot name an issue, so it must pin none; got %v", res.Issues)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "unparseable") {
		t.Errorf("the unreadable sidecar must be reported; warnings = %v", res.Warnings)
	}
}

// --- #443: bounded protection, and the arm that vouched for it ---------------
//
// Two halves of one retention story. (1) The 14-day snapshot age cap lived only
// in the IPC orphan reconciler, which runs from a resident server's startup
// timer — so in a CLI-only workspace nothing ever aged a paused or corrupt
// snapshot out, and the arms below protected that issue's worktree FOREVER: the
// structural-no-op class again, pointing the other way. (2) `Issues` said THAT
// an issue was protected and never WHICH arm vouched for it, so "a paused
// snapshot from 13 days ago" and "the stage child is alive right now" printed
// identically to an operator auditing a skip.

// TestActiveIssuesFromSnapshots_PausedSnapshotAgesOut is the retention bound at
// the paused arm. Inside the cap the pause still outranks the 30-minute liveness
// lease (that is the whole point of the arm); past it, a pause nobody resumed in
// two weeks is debris, not a pending decision — the same verdict 7.4's last row
// reaches, now reachable without a resident IPC server.
func TestActiveIssuesFromSnapshots_PausedSnapshotAgesOut(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	rs := NewRuntimeState("owner/repo", 4431, "item", mustRunID(t))
	rs.SetPaused(true)
	path := persistSnapshot(t, dir, rs)

	backdate(t, path, now.Add(-13*24*time.Hour))
	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[4431] {
		t.Fatalf("a pause inside the retention cap must still protect; got %v (warnings %v)", res.Issues, res.Warnings)
	}
	if got := res.Protected[4431]; !strings.HasPrefix(got, "paused-snapshot") {
		t.Errorf("Protected[4431] = %q, want the paused-snapshot arm", got)
	}
	if got := res.Protected[4431]; !strings.Contains(got, "13d") {
		t.Errorf("Protected[4431] = %q, want the evidence age beside the arm", got)
	}

	backdate(t, path, now.Add(-15*24*time.Hour))
	res, err = activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Issues[4431] {
		t.Errorf("a pause past the %s retention cap must NOT protect the worktree forever; got %v", runstate.SnapshotRetention, res.Issues)
	}
	if _, ok := res.Protected[4431]; ok {
		t.Errorf("an unprotected issue must carry no protecting arm; Protected = %v", res.Protected)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "15d") || !strings.Contains(joined, "retention") {
		t.Errorf("the aged-out pause must be named with its age and the retention cap; warnings = %v", res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_CorruptSnapshotAgesOut is the same bound at the
// corrupt arm. An unreadable body is still treated as ACTIVE while it is fresh —
// the filename names a real run identity and that directory must not be
// destroyed — but a file nothing has rewritten in two weeks is not a run.
func TestActiveIssuesFromSnapshots_CorruptSnapshotAgesOut(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	corrupt := filepath.Join(dir, SnapshotFilename(4432, mustRunID(t)))
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	backdate(t, corrupt, now.Add(-time.Hour))
	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Issues[4432] {
		t.Fatalf("a fresh corrupt snapshot must still count as ACTIVE; got %v", res.Issues)
	}
	if got := res.Protected[4432]; !strings.HasPrefix(got, "corrupt-snapshot") {
		t.Errorf("Protected[4432] = %q, want the corrupt-snapshot arm", got)
	}

	backdate(t, corrupt, now.Add(-15*24*time.Hour))
	res, err = activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Issues[4432] {
		t.Errorf("a corrupt snapshot past the %s retention cap must NOT protect forever; got %v", runstate.SnapshotRetention, res.Issues)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "retention") {
		t.Errorf("the aged-out corrupt snapshot must be named against the retention cap; warnings = %v", res.Warnings)
	}
}

// TestActiveIssuesFromSnapshots_ProtectionReasonPerArm pins one fixture per arm
// onto a DISTINCT reason string. Without this the operator surface is
// unfalsifiable: every arm renders as the same `active-run` skip, so a
// protection granted by a fortnight-old pause is indistinguishable from one
// granted by a process that is executing right now.
func TestActiveIssuesFromSnapshots_ProtectionReasonPerArm(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	// Arm: the in-flight sidecar, the only CURRENT evidence on the Go path.
	writeSidecar(t, dir, 4440, os.Getpid(), mustRunID(t))

	// Arm: the run's recorded stage child.
	child := NewRuntimeState("owner/repo", 4441, "item", mustRunID(t))
	child.SetProcess(os.Getpid(), filepath.Join(dir, "wt"))
	backdate(t, persistSnapshot(t, dir, child), now.Add(-72*time.Hour))

	// Arm: the snapshot's own timestamp lease.
	lease := NewRuntimeState("owner/repo", 4442, "item", mustRunID(t))
	backdate(t, persistSnapshot(t, dir, lease), now.Add(-time.Minute))

	// Arm: a deliberate pause, inside the retention cap.
	paused := NewRuntimeState("owner/repo", 4443, "item", mustRunID(t))
	paused.SetPaused(true)
	backdate(t, persistSnapshot(t, dir, paused), now.Add(-72*time.Hour))

	// Arm: an unreadable body, inside the retention cap.
	corrupt := filepath.Join(dir, SnapshotFilename(4444, mustRunID(t)))
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	backdate(t, corrupt, now.Add(-time.Hour))

	// Arm: the terminal tail — a terminal snapshot whose removal failed.
	term := NewRuntimeState("owner/repo", 4445, "item", mustRunID(t))
	term.MarkTerminal("success")
	backdate(t, persistSnapshot(t, dir, term), now.Add(-time.Minute))

	res, err := activeIssuesFromSnapshotsAt(dir, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Protected == nil {
		t.Fatal("Protected must never be nil — a caller indexes it directly")
	}
	wantArm := map[int]string{
		4440: "live-sidecar",
		4441: "stage-child",
		4442: "timestamp-lease",
		4443: "paused-snapshot",
		4444: "corrupt-snapshot",
		4445: "terminal-tail",
	}
	for issue, arm := range wantArm {
		if !res.Issues[issue] {
			t.Errorf("#%d must be protected by the %s arm; issues = %v (warnings %v)", issue, arm, res.Issues, res.Warnings)
			continue
		}
		if got := res.Protected[issue]; !strings.HasPrefix(got, arm) {
			t.Errorf("Protected[%d] = %q, want the %s arm", issue, got, arm)
		}
	}
	if len(res.Protected) != len(res.Issues) {
		t.Errorf("every protected issue needs exactly one arm: Issues = %v, Protected = %v", res.Issues, res.Protected)
	}
}
