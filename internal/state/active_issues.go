package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// Machine-wide in-flight discovery from the snapshot directory (#410).
//
// ADR-017 Decision 8 made `runtime-{issue}-{runId}.json` "discoverable by
// directory scan with no index, parseable by a process with no registry". This
// file is the reader that cashes that promise: it answers "which issues have a
// run in flight?" for a process that holds NO registry at all — a CLI
// invocation, minutes after the orchestrator that dispatched those runs went
// away, in a different process tree.
//
// Why that matters: `nightgauge worktree sweep` runs `git worktree remove
// --force`. Its one protection against destroying the directory a live run is
// still executing in is WorktreeSweepOptions.ActiveIssues, and the CLI passed
// nothing — SkipActiveRun was structurally unreachable from the command line.
// `internal/orchestrator`'s in-flight set (as.state.Running) is authoritative
// only inside the process that dispatched the runs, so the CLI cannot borrow it.
// The snapshot directory is the only machine-wide source there is.
//
// # What this is NOT
//
// It is not the IPC reconciler's five-arm liveness ladder (ADR-017 7.2). That
// ladder has evidence this process cannot get: an in-memory lease, the Go
// scheduler's registry, a startup grace. What survives the translation to a
// registry-less reader is arm 3 (the run's own stage child, via
// runstate.ProcessAlive) and arm 4 (the snapshot's own timestamp lease, bounded
// by runstate.LivenessWindow — the SAME constant, deliberately shared). This
// reader is therefore strictly weaker than the reconciler, and says so.
//
// # The Go-scheduler arm, and why the snapshot alone could not carry it
//
// On the Go-scheduler path arms 3 and 4 are both weak, for one measured reason:
// nothing persists the snapshot while a stage is running. `SetProcess`
// (internal/execution/manager.go) is an in-memory mutation, internal/execution
// contains no Persist call at all, and the orchestrator persists after a stage
// COMPLETES — so the pid that reaches disk always names a child that has already
// exited, and the file's mtime advances at stage boundaries only. There is no
// heartbeat in the tree. A live run in a long silent stage therefore has a dead
// pid and an old mtime: exactly the shape this reader would decline to protect.
//
// The one live signal that path does write is the crash-recovery sidecar
// `current-run.json` (internal/orchestrator's writeCurrentRunSidecar), stamped
// with `PID: os.Getpid()` at stage START and removed on clean completion. It
// lives in the very directory this scan already walks. So it is an ARM here:
// a sidecar whose process is alive protects the issue it names.

// ActiveIssues is a snapshot scan's answer about which issues have a run in
// flight, together with everything the scan could not read.
//
// Warnings is not decoration. A caller about to delete directories on the
// strength of this set needs to know which entries are guesses: an unreadable
// snapshot is counted as ACTIVE, and a snapshot that is being aged out rather
// than trusted is named, because "the sweep skipped/reclaimed it and nobody
// knows why" is the shape of the leaks this whole subsystem exists to end.
type ActiveIssues struct {
	// Issues holds the issue numbers with a run this scan believes is in
	// flight. Never nil.
	Issues map[int]bool
	// Warnings describes each file the scan could not fully account for, and
	// each non-terminal snapshot whose liveness lease has expired (i.e. an
	// issue this scan deliberately does NOT protect).
	Warnings []string
}

// ActiveIssuesFromSnapshots scans one repo's canonical snapshot directory
// ({repoRoot}/.nightgauge/pipeline) and returns the issues whose run is in
// flight.
//
// CALLER CONTRACT: stateDir must belong to a MAIN CHECKOUT. A linked worktree
// has a `.nightgauge/pipeline` directory of its own — the `.gitkeep` is tracked,
// so every checkout has one — and it is always empty, so passing one yields a
// DETERMINED EMPTY answer with no error and no warning while the repository it
// belongs to may be running anything. Canonicalize first with
// config.MainCheckoutRoot (#410).
//
// An absent directory is a determined empty answer, not an error: a repo that
// has never run the pipeline has no snapshot dir. Any OTHER read failure IS an
// error — the caller must not treat "I could not look" as "nothing is running"
// (#296/#302), and for a destructive caller the only safe response is to skip
// that root entirely and say so.
//
// An unparseable filename is ignored (it is not a snapshot). A parseable name
// whose CONTENT cannot be read or disagrees with the name counts as ACTIVE and
// raises a warning: the file names a real run identity, and a run whose state is
// unreadable is exactly the run whose directory must not be destroyed.
func ActiveIssuesFromSnapshots(stateDir string) (ActiveIssues, error) {
	return activeIssuesFromSnapshotsAt(stateDir, time.Now())
}

// activeIssuesFromSnapshotsAt is ActiveIssuesFromSnapshots with an explicit
// clock, so the age matrix is testable without sleeping — the same seam
// classifyCandidate takes for the reconciler's table.
func activeIssuesFromSnapshotsAt(stateDir string, now time.Time) (ActiveIssues, error) {
	res := ActiveIssues{Issues: map[int]bool{}}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("scan runtime snapshots in %s: %w", stateDir, err)
	}

	// THE GO-SCHEDULER LIVENESS ARM, read before the snapshots because it is the
	// only arm whose evidence is CURRENT (see the block comment above): the
	// sidecar's pid is the running orchestrator's, stamped at stage start, while
	// a snapshot's pid names a stage child that has already exited by the time
	// it reaches disk.
	if issue, warning := sidecarInFlightIssue(stateDir); issue > 0 {
		res.Issues[issue] = true
	} else if warning != "" {
		res.Warnings = append(res.Warnings, warning)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		issueNumber, runID, ok := ParseSnapshotFilename(name)
		if !ok {
			// Not a snapshot: the history dir's siblings, exit-records, the
			// pause-restore claim artifacts. A claim artifact deliberately does
			// NOT protect an issue here — it is another host's working state and
			// the reconciler owns its rows. The current-run sidecar is not a
			// snapshot either, and it is read above as its own arm.
			continue
		}

		info, infoErr := entry.Info()
		snap, loadErr := LoadSnapshotByIdentity(stateDir, issueNumber, runID)
		switch {
		case infoErr != nil:
			res.Issues[issueNumber] = true
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: could not stat the snapshot (%v) — treating #%d as ACTIVE", name, infoErr, issueNumber))
			continue
		case loadErr != nil || snap == nil:
			res.Issues[issueNumber] = true
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: unreadable or corrupt snapshot (%v) — treating #%d as ACTIVE", name, loadErr, issueNumber))
			continue
		case snap.RunID != runID:
			// The name promised an identity and the body delivered a different
			// one. The reconciler refuses such a file rather than acting on it;
			// so does this reader, in the conservative direction.
			res.Issues[issueNumber] = true
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: carries run %q in its body — treating #%d as ACTIVE (name/body identity mismatch)",
				name, snap.RunID, issueNumber))
			continue
		}

		if snap.IsTerminal() {
			// THE TERMINAL TAIL (measured, see the block comment on
			// terminalTailProtects). A terminal snapshot on disk is already the
			// unusual case — the terminal claim writes it and immediately removes
			// it (RuntimeState.SealAndRemove) — so this arm covers the run whose
			// remove failed, which is the one shape where the tail window is both
			// real and observable.
			if terminalTailProtects(snap, info, now) {
				res.Issues[issueNumber] = true
			}
			continue
		}

		// A PAUSE IS A DELIBERATE "RESUME LATER", not silence. It powers the
		// restore prompt at the next activation, possibly days later (ADR-017
		// 7.4's paused row), so it must not be aged out by a 30-minute liveness
		// lease. Retention for a pause nobody resumes is the reconciler's job —
		// it removes the file past the 14-day cap, and this reader protects
		// exactly as long as the file exists.
		//
		// RESIDUAL, stated where the protection is granted: that retention
		// depends on an IPC server having started on this root. The reconcile
		// pass runs only from the server's startup timer and workspace.setRoot
		// (internal/ipc/pipeline_orphan_reconcile.go), over ITS scan roots — so
		// in a CLI-only workspace, where `serve`/the extension never runs on this
		// repo, nothing ages a paused snapshot out and this arm protects that
		// issue's worktree indefinitely. `--dry-run` shows it, and deleting the
		// snapshot is the operator's door. Bounding it here would need the 14-day
		// cap to become a shared constant rather than a second number invented
		// beside it.
		if snap.Paused {
			res.Issues[issueNumber] = true
			continue
		}

		// Arm 3: the run's recorded stage child. Strong on the extension path,
		// where the IPC server persists the pid of the child it was told about;
		// STRUCTURALLY DEAD on the Go-scheduler path, where the pid reaches disk
		// only after that child exited (see the block comment). The sidecar arm
		// above is what covers a Go-dispatched run.
		if runstate.ProcessAlive(snap.PID) {
			res.Issues[issueNumber] = true
			continue
		}

		// Arm 4: the snapshot's own timestamp lease. The file is refreshed at
		// STAGE BOUNDARIES ONLY — the orchestrator persists after a stage
		// completes and the IPC server on repo-carrying transitions; no progress
		// tick writes anything, and there is no heartbeat. So this lease means "a
		// stage boundary happened recently", not "the run breathed recently", and
		// a healthy long stage can outlive it while very much alive. That is why
		// it is the LAST arm rather than the load-bearing one.
		if now.Sub(info.ModTime()) < runstate.LivenessWindow {
			res.Issues[issueNumber] = true
			continue
		}

		if res.Issues[issueNumber] {
			// Already vouched for by the sidecar arm: this snapshot being quiet
			// is the persist cadence, not evidence about the run.
			continue
		}

		// Past the lease with no live child: a run that was killed mid-flight
		// (window reload, crash, SIGKILL). THIS IS THE POPULATION THE WORKTREE
		// SWEEP EXISTS FOR (#110), and the reason the predicate is a liveness
		// bound rather than "a non-terminal snapshot exists". Neither the Go
		// scheduler path nor a crash latches terminal, and nothing on those
		// paths removes the file — so a bare non-terminal test would protect
		// every leaked worktree forever and turn the operator's command into a
		// permanent no-op, which is #403's structural-no-op defect re-created
		// pointing the other way. Named rather than silently dropped.
		//
		// The warning says what the pid IS. On the Go-scheduler path it is the
		// last persisted stage child, which exited before the file was written —
		// so "that pid is not alive" is not evidence the run is dead, and a line
		// implying otherwise is the thing the next person reads while auditing a
		// wrongly removed directory. What actually decides here is the
		// conjunction: no live sidecar, no live recorded child, no stage boundary
		// inside the lease.
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s: non-terminal snapshot, no in-flight sidecar vouches for #%d, last persisted stage child (pid %d) is not alive, and no stage boundary in the last %s (the file is rewritten at stage boundaries only — there is no heartbeat) — #%d is NOT protected as in-flight",
			name, issueNumber, snap.PID, now.Sub(info.ModTime()).Round(time.Second), issueNumber))
	}

	return res, nil
}

// currentRunSidecarName is the in-flight sidecar's filename inside the pipeline
// state dir. The path is owned by internal/orchestrator
// (currentRunSidecarFile = ".nightgauge/pipeline/current-run.json"); this is the
// basename half, because the scan already holds the directory.
const currentRunSidecarName = "current-run.json"

// currentRunSidecar is a MINIMAL decode of the in-flight sidecar written by
// internal/orchestrator's writeCurrentRunSidecar (type
// orchestrator.CurrentRunSidecar).
//
// Redeclared rather than imported, deliberately: internal/orchestrator imports
// internal/state, so importing the writer's package back here would be an import
// cycle. Only the three fields this arm needs are decoded, and the pairing is
// pinned across the two packages by an orchestrator-side test that writes with
// the production writer and reads with this function — so a field rename cannot
// silently disarm the arm.
type currentRunSidecar struct {
	IssueNumber int    `json:"issue_number"`
	RunID       string `json:"run_id"`
	PID         int    `json:"pid,omitempty"`
}

// sidecarInFlightIssue reports the issue number the in-flight sidecar vouches
// for, or 0 when nothing does. The second result is a warning for the one shape
// worth surfacing: a sidecar that exists and cannot be parsed.
//
// The gate is LIVENESS, not existence. A sidecar outlives a crashed orchestrator
// (that is what it is for — the crash synthesizer reads it at the next startup),
// so protecting on existence alone would pin the crashed run's worktree until an
// orchestrator happened to start again in that repo.
func sidecarInFlightIssue(stateDir string) (int, string) {
	path := filepath.Join(stateDir, currentRunSidecarName)
	data, err := os.ReadFile(path)
	if err != nil {
		// Absent is the normal case: no run is executing here.
		return 0, ""
	}
	var sc currentRunSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return 0, fmt.Sprintf(
			"%s: in-flight sidecar is present but unparseable (%v) — it cannot name the issue it belongs to, so it protects nothing",
			currentRunSidecarName, err)
	}
	if sc.IssueNumber <= 0 || !runstate.ProcessAlive(sc.PID) {
		return 0, ""
	}
	return sc.IssueNumber, ""
}

// terminalTailProtects reports whether a TERMINAL snapshot still stands for a run
// that may be using its worktree.
//
// MEASURED ORDERING — the terminal marker lands BEFORE the worktree goes, on
// both dispatch paths, so a terminal snapshot is not proof the directory is free:
//
//   - the extension path's terminal funnel dispatches `pipeline.notifyComplete`
//     (HeadlessOrchestrator.firePipelineComplete) and then, in the same
//     synchronous function with no await between them, the worktree cleanup. The
//     Go handler latches terminal in claim step 1c and seals in step 4
//     (internal/ipc/server.go's notifyComplete → RuntimeState.SealAndRemove),
//     which normally REMOVES the file — so after a successful seal there is no
//     snapshot to protect anything, and this arm only reaches the run whose
//     remove failed and left `terminal: true` on disk;
//   - the Go scheduler path never latches terminal at all. Its terminal defer
//     (internal/orchestrator/scheduler.go's runPipeline-terminal-defer region)
//     runs ~160 lines of bookkeeping — a telemetry emit and a remote-branch
//     cleanup, both network — before `execution.Manager.CleanupWorktree`, and
//     nothing on that path marks or removes the snapshot. Such a run is covered
//     by the non-terminal arms above, not by this one.
//
// So the window is real, bounded by seconds of bookkeeping, and one shared
// LivenessWindow is a strict over-approximation of it. TerminalAt is preferred
// over the file's mtime because it is the instant the latch actually happened;
// mtime is the fallback for a snapshot whose TerminalAt is absent, which
// markTerminalLocked cannot produce and only hand-authored JSON can.
func terminalTailProtects(snap *RuntimeState, info os.FileInfo, now time.Time) bool {
	if snap.TerminalAt != nil {
		return now.Sub(*snap.TerminalAt) < runstate.LivenessWindow
	}
	return now.Sub(info.ModTime()) < runstate.LivenessWindow
}

// PipelineStateDir composes the pipeline state directory for a repo root — the
// directory SnapshotFilename's output lives in.
//
// Exported so a caller that has a repo root (the CLI worktree sweep) does not
// hand-join the layout the state package owns. The IPC server's own
// pipelineStateDir starts from a repo SLUG, resolves it to a root, and then calls
// THIS — the slug resolution is the server's business, the layout is not. Same
// for internal/state's offline store.
func PipelineStateDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".nightgauge", "pipeline")
}
