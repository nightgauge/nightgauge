package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// runLifecycleMu serializes every run-state.json read-modify-write this
// process makes. The record is one file per repository, and concurrent
// pipelines in one daemon share it, so the load-check-save sequences below
// must not interleave (#1964).
var runLifecycleMu sync.Mutex

// runLifecycle drives the durable internal/runstate record for one pipeline
// run (#1964): MarkRunning (or Resume) when the stage loop starts, one
// MarkStageStarted per stage entered, and a terminal transition when the run
// returns — completed on success, paused with resume_from_stage when the run's
// context was cancelled (SIGTERM/SIGINT reach it through `nightgauge run`'s
// signal context), aborted otherwise.
//
// The record lives in the run root's pipeline state directory
// (layout.PipelineStateDir), which belongs to the clone; `nightgauge run state`
// resolves the same directory from a worktree. Only one issue can own the file: a run that finds it held by a
// live run for another issue does not track at all rather than clobbering
// that run's record. Every write failure is logged and never fails the run.
//
// worktree_path is deliberately not recorded: the failure-cleanup rescue
// (loadWorktreePath) reads it to choose where uncommitted work is
// recovered from, and that choice is not this change's to move.
type runLifecycle struct {
	baseDir string
	issue   int
	active  bool
}

// provisionalBranch is the branch recorded before issue-pickup names the
// feature branch; run-state requires a non-empty branch, and the first stage
// transition after pickup replaces it.
func provisionalBranch(issue int) string { return fmt.Sprintf("issue-%d", issue) }

// beginRunLifecycle starts (or resumes) the durable record for issue. It
// returns the tracker and, when a paused or interrupted earlier attempt of the
// same issue was found, the stage that attempt recorded as its re-entry point.
func beginRunLifecycle(workspaceRoot string, issue int, branch string) (*runLifecycle, runstate.Stage) {
	lc := &runLifecycle{issue: issue}
	baseDir, err := layout.PipelineStateDir(workspaceRoot)
	if err != nil {
		log.Printf("#%d: run-state not tracked: %v", issue, err)
		return lc, ""
	}
	lc.baseDir = baseDir

	runLifecycleMu.Lock()
	defer runLifecycleMu.Unlock()

	cur, err := runstate.Load(baseDir)
	if err != nil {
		log.Printf("#%d: run-state not tracked: unreadable %s: %v", issue, runstate.Path(baseDir), err)
		return lc, ""
	}
	if cur != nil && cur.State == runstate.StateRunning && runLifecycleOwnerAlive(cur) {
		if cur.IssueNumber != issue || lastAttemptPID(cur) != os.Getpid() {
			log.Printf("#%d: run-state not tracked: %s is held by a live run of #%d",
				issue, runstate.Path(baseDir), cur.IssueNumber)
			return lc, ""
		}
	}

	if cur != nil && cur.IssueNumber == issue {
		if cur.State == runstate.StateRunning {
			// The writer is gone (hard kill, crash, machine restart) or is
			// this process's own leftover: book the interruption so the
			// resume below is a legal paused → running transition.
			if _, err := runstate.MarkPaused(baseDir, "interrupted: the run's process ended without recording a stop", nil); err != nil {
				log.Printf("#%d: run-state: cannot pause the interrupted record: %v", issue, err)
				return lc, ""
			}
			cur.State = runstate.StatePaused
		}
		if cur.State == runstate.StatePaused {
			rs, err := runstate.Resume(baseDir)
			if err != nil {
				log.Printf("#%d: run-state: cannot resume: %v", issue, err)
				return lc, ""
			}
			lc.active = true
			var from runstate.Stage
			if rs.ResumeFromStage != nil {
				from = *rs.ResumeFromStage
			}
			log.Printf("#%d: run-state: resuming run %s (attempt %d) from %s",
				issue, rs.RunID, rs.AttemptNumber, from)
			return lc, from
		}
	}

	if branch == "" {
		branch = provisionalBranch(issue)
	}
	if _, err := runstate.MarkRunning(baseDir, runstate.MarkRunningOptions{
		IssueNumber: issue,
		Branch:      branch,
	}); err != nil {
		log.Printf("#%d: run-state: cannot mark running: %v", issue, err)
		return lc, ""
	}
	lc.active = true
	return lc, ""
}

// stage records that the run entered stage. Stages outside the canonical
// run-state order (spike-materialize) leave the record on its last stage.
func (lc *runLifecycle) stage(stage state.PipelineStage, branch string) {
	if !lc.owns() || !runstate.IsStage(string(stage)) {
		return
	}
	runLifecycleMu.Lock()
	defer runLifecycleMu.Unlock()
	if !lc.stillOwns() {
		return
	}
	if _, err := runstate.MarkStageStarted(lc.baseDir, runstate.Stage(stage), branch); err != nil {
		log.Printf("#%d: run-state: cannot record stage %s: %v", lc.issue, stage, err)
	}
}

// finish books the run's terminal lifecycle transition.
func (lc *runLifecycle) finish(ctx context.Context, success bool) {
	if !lc.owns() {
		return
	}
	runLifecycleMu.Lock()
	defer runLifecycleMu.Unlock()
	if !lc.stillOwns() {
		return
	}
	var err error
	switch {
	case success:
		_, err = runstate.MarkCompleted(lc.baseDir)
	case ctx.Err() != nil:
		reason := "interrupted: " + ctx.Err().Error()
		if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, ctx.Err()) {
			reason = "interrupted: " + cause.Error()
		}
		// nil keeps resume_from_stage on the stage that was executing.
		_, err = runstate.MarkPaused(lc.baseDir, reason, nil)
	default:
		_, err = runstate.MarkAborted(lc.baseDir, "pipeline run failed", true)
	}
	if err != nil {
		log.Printf("#%d: run-state: cannot record the terminal transition: %v", lc.issue, err)
	}
}

func (lc *runLifecycle) owns() bool { return lc != nil && lc.active }

// stillOwns re-reads the record under the lock: another process (the CLI's
// `run state discard`, a concurrent run) may have replaced it.
func (lc *runLifecycle) stillOwns() bool {
	rs, err := runstate.Load(lc.baseDir)
	return err == nil && rs != nil && rs.IssueNumber == lc.issue && rs.State == runstate.StateRunning
}

// resumeStageIndex returns the index in stages of the recorded re-entry
// stage, or 0 when there is none or the current stage list does not contain it.
func resumeStageIndex(stages []state.PipelineStage, from runstate.Stage) int {
	if from == "" {
		return 0
	}
	for i, st := range stages {
		if string(st) == string(from) {
			return i
		}
	}
	return 0
}

func lastAttemptPID(rs *runstate.RunState) int {
	if n := len(rs.Attempts); n > 0 && rs.Attempts[n-1].PID != nil {
		return *rs.Attempts[n-1].PID
	}
	return 0
}

func runLifecycleOwnerAlive(rs *runstate.RunState) bool {
	pid := lastAttemptPID(rs)
	return pid > 0 && runstate.ProcessAlive(pid)
}
