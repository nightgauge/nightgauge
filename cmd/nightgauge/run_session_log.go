package main

import (
	"fmt"
	"io"
	"log"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/sessionlog"
	"github.com/nightgauge/nightgauge/internal/state"
)

// runProgressSource is the part of the scheduler `nightgauge run` reports
// progress from. An interface so a test can drive the callbacks.
type runProgressSource interface {
	OnStageStart(fn func(repo string, issue int, stage string, title string))
	OnStageComplete(fn func(repo string, issue int, stage string, err error, cost orchestrator.StageCost, model string))
	OnPhaseDetected(fn func(repo string, issue int, stage, name string, index, total int))
	OnPhaseSettled(fn func(repo string, issue int, stage, name string, index, total int, status string))
	OnPipelineComplete(fn func(repo string, issue int, runtime *state.RuntimeState, success bool))
	RepoRoot(repo string) (string, error)
}

// runProgress reports a CLI run's progress to the terminal and to the run's
// session log (#1903), so the run's output outlives the terminal.
type runProgress struct {
	log  *sessionlog.Writer
	src  runProgressSource
	root string
}

// wireRunProgress registers the run's progress callbacks on src and tees the
// standard logger, which carries the scheduler's narration, into the session
// log. Stage lines go to stdout and phase lines to stderr, as before. The
// returned restore puts the logger's output back and flushes the log.
func wireRunProgress(src runProgressSource, root string, stdout, stderr io.Writer) (*runProgress, func()) {
	p := &runProgress{log: sessionlog.New(root), src: src, root: root}

	src.OnStageStart(func(repo string, issue int, stage string, title string) {
		fmt.Fprintf(stdout, "[#%d] stage %s started\n", issue, stage)
		p.Track(repo, issue)
		p.log.StageStarted(issue, stage)
	})
	src.OnStageComplete(func(repo string, issue int, stage string, err error, cost orchestrator.StageCost, model string) {
		if err != nil {
			fmt.Fprintf(stdout, "[#%d] stage %s FAILED: %v\n", issue, stage, err)
			p.log.Append(issue, sessionlog.LevelError, stage, fmt.Sprintf("stage %s FAILED: %v", stage, err))
			return
		}
		line := fmt.Sprintf("stage %s complete — tokens: %s, cost: %s", stage, cost.TokenSummary(), cost.CostSummary())
		fmt.Fprintf(stdout, "[#%d] %s\n", issue, line)
		p.log.Append(issue, sessionlog.LevelInfo, stage, line)
	})
	// Phase progress (#1924) goes to STDERR deliberately: stdout carries the
	// run's own machine-readable output, a contract predating it, and phase
	// progress is operator-facing narration that must not contaminate it. See
	// orchestrator.PhaseStreamPrefix for the same split on the pr-stage route.
	src.OnPhaseDetected(func(repo string, issue int, pStage, pName string, pIndex, pTotal int) {
		line := fmt.Sprintf("%s %d/%d %s", pStage, pIndex+1, pTotal, pName)
		fmt.Fprintf(stderr, "[#%d]   %s\n", issue, line)
		p.log.Append(issue, sessionlog.LevelInfo, pStage, "phase "+line)
	})
	src.OnPhaseSettled(func(repo string, issue int, pStage, pName string, pIndex, pTotal int, status string) {
		// "passed" is the ordering-derived arm: the run moved beyond a phase
		// it never announced. Marked so an operator can tell it from a phase
		// that was actually observed to run.
		mark := status
		if status == "passed" {
			mark = "passed (not reported)"
		}
		line := fmt.Sprintf("%s %d/%d %s — %s", pStage, pIndex+1, pTotal, pName, mark)
		fmt.Fprintf(stderr, "[#%d]   %s\n", issue, line)
		p.log.Append(issue, sessionlog.LevelInfo, pStage, "phase "+line)
	})
	src.OnPipelineComplete(func(repo string, issue int, rt *state.RuntimeState, success bool) {
		runID := ""
		if rt != nil {
			runID = rt.RunID
		}
		p.log.RunFinished(issue, runID, success)
	})

	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, p.log))
	return p, func() {
		log.SetOutput(prev)
		// Flush a held partial line, redacted, now nothing else writes.
		_ = p.log.Close()
	}
}

// Track files issue's log under the root its run executes in: the target
// repository's checkout for a cross-repo run, where the extension's slot for
// the run looks for it. The launch root stands in when that cannot be
// resolved. Only the first call for an issue resolves.
func (p *runProgress) Track(repo string, issue int) {
	if p.log.Tracked(issue) {
		return
	}
	root := p.root
	if repo != "" {
		if r, err := p.src.RepoRoot(repo); err == nil && r != "" {
			root = r
		}
	}
	p.log.Track(issue, root)
}
