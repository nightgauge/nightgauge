package orchestrator

import (
	"context"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// emitRunEvent sends one of a scheduler run's platform events with the run's
// pull request stamped on it (#3455): every event type carries prNumber/prUrl
// once pr-create has opened or reused a PR, pipeline_done included. Callers
// check s.telemetrySvc and s.telemetryEnabled first, as they always have.
func (s *Scheduler) emitRunEvent(ctx context.Context, rt *state.RuntimeState, event platform.PipelineEvent) {
	platform.StampPR(&event, rt.PRURL())
	s.telemetrySvc.EmitPipelineEvent(ctx, event)
}

// emitPhaseTelemetry sends one in-stage phase transition of a scheduler-owned
// run to the platform as a stage_progress event (#3455). index is 0-based, as
// markers and PhaseReporter number phases; total is the phase count. A transition the
// platform does not record (superseded, unreported, observed), a stage outside
// its enum, or a phase outside the contract sends nothing. Repeated sightings
// of one phase are absorbed by the analytics service's per-run coalescer.
func (s *Scheduler) emitPhaseTelemetry(rt *state.RuntimeState, repo string, issue int, stage, name string, index, total int, transition string) {
	if s.telemetrySvc == nil || !s.telemetryEnabled || rt == nil {
		return
	}
	event, ok := platform.PhaseProgressEvent(rt.RunID, repo, issue, stage, name, index, total, transition, time.Now())
	if !ok {
		return
	}
	event.Visibility = rt.RunVisibility()
	s.emitRunEvent(context.Background(), rt, event)
}
