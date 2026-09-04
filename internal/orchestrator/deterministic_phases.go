package orchestrator

import (
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// deterministicPhaseReporter is the scheduler's stages.PhaseReporter (#1247).
//
// It writes to BOTH sinks a phase record has to reach, because they answer
// different questions and neither substitutes for the other:
//
//   - RuntimeState.PhaseHistory — the DURABLE record. It is what
//     BuildV2Record projects into V2StageDetail.Phases, so it is what a retro,
//     a survival verdict, or anyone reading history after the run reads. A
//     deterministic stage that only lit up the live view would still have an
//     empty durable record.
//   - the live callbacks — what the tree view renders WHILE the stage runs.
//     This is the half that was showing 0/14.
//
// One reporter per run: it closes over the run's runtime, so a phase can never
// be attributed to another issue's PhaseHistory the way a field on the shared
// runner instance could.
type deterministicPhaseReporter struct {
	sched *Scheduler
	rt    *state.RuntimeState
	repo  string
	issue int
}

func (p *deterministicPhaseReporter) PhaseStart(stage, name string, index, total int) {
	p.rt.BeginPhase(state.PipelineStage(stage), name, index, total)
	if p.sched.onPhaseDetected != nil {
		p.sched.onPhaseDetected(p.repo, p.issue, stage, name, index, total)
	}
}

func (p *deterministicPhaseReporter) PhaseComplete(stage, name string) {
	p.rt.CompletePhase(state.PipelineStage(stage), name)
	// CompletePhase amends a record that already carries index/total, but the
	// live event does not — the extension's consumers key on them — so the
	// position is re-resolved from the registry rather than published as -1.
	idx, total := pmstages.PhasePosition(stage, name)
	p.settled(stage, name, idx, total, "complete")
}

func (p *deterministicPhaseReporter) PhaseFail(stage, name string, index, total int) {
	p.rt.FailPhase(state.PipelineStage(stage), name, index, total)
	p.settled(stage, name, index, total, "failed")
}

func (p *deterministicPhaseReporter) PhaseSkip(stage, name string, index, total int) {
	p.rt.SkipPhase(state.PipelineStage(stage), name, index, total)
	p.settled(stage, name, index, total, "skipped")
}

func (p *deterministicPhaseReporter) settled(stage, name string, index, total int, status string) {
	if p.sched.onPhaseSettled == nil {
		return
	}
	p.sched.onPhaseSettled(p.repo, p.issue, stage, name, index, total, status)
}

// newDeterministicPhaseReporter returns nil when there is no runtime to write
// to, so the caller can hand a typed nil straight to WithPhaseReporter — which
// treats a nil interface as "report nothing" rather than panicking.
func (s *Scheduler) newDeterministicPhaseReporter(rt *state.RuntimeState, repo string, issue int) pmstages.PhaseReporter {
	if rt == nil {
		return nil
	}
	return &deterministicPhaseReporter{sched: s, rt: rt, repo: repo, issue: issue}
}
