package state

import (
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

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		issueNumber, runID, ok := ParseSnapshotFilename(name)
		if !ok {
			// Not a snapshot: the history dir's siblings, exit-records, the
			// pause-restore claim artifacts, the current-run sidecar. A claim
			// artifact deliberately does NOT protect an issue here — it is
			// another host's working state and the reconciler owns its rows.
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
		if snap.Paused {
			res.Issues[issueNumber] = true
			continue
		}

		// Arm 3: the run's own stage child. A registry-less reader's strongest
		// available evidence, and the reason a long silent stage (feature-dev
		// can run past 30 minutes without persisting) is not mistaken for a
		// crash.
		if runstate.ProcessAlive(snap.PID) {
			res.Issues[issueNumber] = true
			continue
		}

		// Arm 4: the snapshot's own timestamp lease. Every stage transition and
		// progress tick rewrites the file, so a live run's snapshot is minutes
		// old at worst.
		if now.Sub(info.ModTime()) < runstate.LivenessWindow {
			res.Issues[issueNumber] = true
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
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s: non-terminal snapshot last touched %s ago with no live stage child (pid %d) — #%d is NOT protected as in-flight",
			name, now.Sub(info.ModTime()).Round(time.Second), snap.PID, issueNumber))
	}

	return res, nil
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
// hand-join the layout the state package owns. The IPC server keeps its own
// pipelineStateDir because it starts from a repo SLUG and must resolve it to a
// root first; this is the root-in variant, and it is what internal/state's own
// offline store now uses too.
func PipelineStateDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".nightgauge", "pipeline")
}
