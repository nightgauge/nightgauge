package ipc

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"regexp"
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
// "phantom in-flight run" symptom. The persisted runtime-{N}.json snapshot
// carries the run's RunID across the crash; this reconciler scans those
// leftovers at server start (extension activation) and emits the missing
// pipeline_done so the platform row leaves 'running' immediately instead of
// waiting for the platform-side stale-run reaper.
//
// Paused runs are intentionally skipped: their runtime-{N}.json powers the
// pause-restore prompt (#2008) and the user may still resume them. A resumed
// run gets a fresh RunID, so reconciliation never conflicts with a live run.

var runtimeFilePattern = regexp.MustCompile(`^runtime-(\d+)\.json$`)

// orphanedRun pairs a leftover runtime snapshot's terminal event with the
// file that proves it, so the caller can emit then delete.
type orphanedRun struct {
	FilePath string
	Event    platform.PipelineEvent
}

// collectOrphanedRuns scans stateDir for persisted runtime-{N}.json snapshots
// left behind by interrupted runs and builds the terminal pipeline_done event
// for each. Skipped: paused snapshots (resumable — see package comment),
// snapshots without a RunID (predate persistence or never reached a stage),
// unparseable files, and issues for which skipIssue reports a live runtime.
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
		m := runtimeFilePattern.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		issueNumber, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if skipIssue != nil && skipIssue(issueNumber) {
			continue
		}
		rt, err := state.LoadPersistedState(stateDir, issueNumber)
		if err != nil || rt == nil {
			continue
		}
		if rt.RunID == "" || rt.Paused {
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
