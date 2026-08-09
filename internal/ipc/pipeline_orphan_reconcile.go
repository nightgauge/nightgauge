package ipc

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Orphaned-run reconciliation (#44).
//
// The extension/HeadlessOrchestrator path mints its platform run UUID
// in-memory (pipeline.notifyStageTransition) and emits the terminal
// pipeline_done only via pipeline.notifyComplete. When the extension host
// dies mid-run (window closed, crash, sleep), that terminal event never
// fires and the platform's pipeline_runs row stays 'running' forever — the
// "phantom in-flight run" symptom. The persisted snapshot
// carries the run's RunID across the crash; this reconciler scans those
// leftovers at server start (extension activation) and emits the missing
// pipeline_done so the platform row leaves 'running' immediately instead of
// waiting for the platform-side stale-run reaper.
//
// Paused runs are intentionally skipped: their snapshot powers the
// pause-restore prompt (#2008) and the user may still resume them. A resumed
// run gets a fresh RunID, so reconciliation never conflicts with a live run.
//
// Discovery is now `runtime-{issue}-{runId}.json` (ADR-017 Decision 8), parsed
// by state.ParseSnapshotFilename — the same expression the composer and the IPC
// wire validation are built from, so no id shape can pass validation and fail
// discovery. That mismatch is what would have stranded dashboard-triggered runs
// outside this scan while the TypeScript stub sweep deleted their live crash
// snapshots.

// orphanedRun pairs a leftover runtime snapshot's terminal event with the
// file that proves it, so the caller can emit then delete.
type orphanedRun struct {
	FilePath string
	Event    platform.PipelineEvent
}

// collectOrphanedRuns scans stateDir for persisted
// runtime-{issue}-{runId}.json snapshots left behind by interrupted runs and
// builds the terminal pipeline_done event for each. Skipped: paused snapshots
// (resumable — see package comment), snapshots whose CONTENT carries no RunID
// (corruption — the name promised one), unparseable files, and issues for which
// skipIssue reports a live runtime.
func collectOrphanedRuns(stateDir string, skipIssue func(int) bool, now time.Time) []orphanedRun {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil
	}

	var orphans []orphanedRun
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		issueNumber, runID, ok := state.ParseSnapshotFilename(entry.Name())
		if !ok {
			continue
		}
		if skipIssue != nil && skipIssue(issueNumber) {
			continue
		}
		// Load by the runId the FILENAME carried, not by issue: concurrent
		// dispatches of one issue coexist, so the issue number is an index and
		// only the identity is an address (ADR-017 Decision 8).
		rt, err := state.LoadPersistedState(stateDir, runID)
		if err != nil || rt == nil {
			continue
		}
		// The name promised an identity; this checks the CONTENT delivered THE
		// SAME ONE. It is a corruption guard, not the discovery filter it used
		// to be — a file whose body disagrees with its name is not something to
		// emit a terminal event from.
		//
		// The predicate is EQUALITY, not `!= ""`. Every in-tree writer composes
		// the filename from the same fields it marshals, so name and body always
		// agree; but `!= ""` only catches the empty body, which
		// buildPipelineDoneEvent below refuses anyway — the guard was a no-op
		// and could be deleted with every reconcile test green. Equality catches
		// the case that actually escapes: a body carrying a DIFFERENT valid
		// identity, which builds a perfectly well-formed pipeline_done and
		// reports the wrong run terminal to the platform.
		if rt.RunID != runID || rt.Paused {
			continue
		}

		snap := rt.Snapshot()
		stagesRun := make([]string, 0, len(snap.CompletedStages))
		var totalDuration time.Duration
		for _, sr := range snap.CompletedStages {
			stagesRun = append(stagesRun, string(sr.Stage))
			totalDuration += sr.Duration
		}
		event, ok := buildPipelineDoneEvent(snap.RunID, PipelineNotifyCompleteParams{
			Repo:        snap.Repo,
			IssueNumber: snap.IssueNumber,
			Success:     false,
			// Sum of completed-stage durations, NOT wall clock since start —
			// the run has been dead for an unknowable stretch of that wall
			// time (the 42h-elapsed-timer symptom this reconciler fixes).
			TotalDurationMs: int(totalDuration.Milliseconds()),
			StagesRun:       stagesRun,
		}, now)
		if !ok {
			continue
		}
		orphans = append(orphans, orphanedRun{
			FilePath: filepath.Join(stateDir, entry.Name()),
			Event:    event,
		})
	}
	return orphans
}

// pipelineStateScanRoots returns every workspace root whose
// .nightgauge/pipeline dir may hold persisted runtime snapshots: the IPC
// server's launch root plus every repo registered with the client resolver.
// Snapshots are persisted into the run's target repo (#215), so the orphan
// scan must cover all of them or crash recovery misses cross-repo runs.
func (s *Server) pipelineStateScanRoots() []string {
	seen := make(map[string]bool)
	var roots []string
	add := func(root string) {
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	add(s.workspaceRoot)
	for _, p := range s.resolver.RegisteredPaths() {
		add(p)
	}
	return roots
}

// reconcileOrphanedRuns emits the missing terminal pipeline_done for every
// orphaned runtime snapshot under the workspace's pipeline state roots (the
// launch root plus every registered repo — see pipelineStateScanRoots), then
// removes each snapshot so the reconcile is idempotent across activations.
// Best-effort: emission is fire-and-forget (AnalyticsService buffers offline)
// and a run whose event is lost anyway is caught by the platform-side reaper.
func (s *Server) reconcileOrphanedRuns() {
	if s.analyticsSvc == nil {
		return
	}

	skipIssue := func(issueNumber int) bool {
		runtimeKey := strconv.Itoa(issueNumber)
		s.runtimesMu.Lock()
		defer s.runtimesMu.Unlock()
		_, live := s.activeRuntimes[runtimeKey]
		return live
	}

	for _, root := range s.pipelineStateScanRoots() {
		stateDir := filepath.Join(root, ".nightgauge", "pipeline")
		orphans := collectOrphanedRuns(stateDir, skipIssue, time.Now())
		for _, orphan := range orphans {
			s.analyticsSvc.EmitPipelineEvent(context.Background(), orphan.Event)
			if err := os.Remove(orphan.FilePath); err != nil {
				log.Printf("orphan-reconcile: emitted pipeline_done for run %s but could not remove %s: %v",
					orphan.Event.RunID, orphan.FilePath, err)
			} else {
				log.Printf("orphan-reconcile: closed orphaned run %s (issue #%d) from %s",
					orphan.Event.RunID, orphan.Event.IssueNumber, filepath.Base(orphan.FilePath))
			}
		}
	}
}
