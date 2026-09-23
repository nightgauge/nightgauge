package orchestrator

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/intelligence/sizeGate"
	"github.com/nightgauge/nightgauge/internal/skillrender"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trace"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// DispatchContextWindow is the context window, in tokens, a dispatch of model
// on adapter runs with, resolved in ADR 023 Q10's order: the registry
// descriptor skillrender.OverlayKeys resolves (the same one a render
// records), else, for an OpenCode model on a declared local endpoint, the
// limit.context its run config is built with. 0 when neither resolves — the
// unknown window every consumer treats as "no cap".
func DispatchContextWindow(ctx context.Context, workspaceRoot, adapter, model string) int {
	if _, descriptor, ok := skillrender.OverlayKeys(model, adapter); ok && descriptor.ContextWindow > 0 {
		return descriptor.ContextWindow
	}
	if adapter == "opencode" {
		return openCodeDispatchWindow(ctx, workspaceRoot, model)
	}
	return 0
}

// dispatchIssueSize is the size the capacity check judges at dispatch: the
// run's routing decision when a real source named it (foundation, board
// field, size:* label, or a planner size already folded in), else the
// planner's complexity_assessment in planning-{N}.json. "" when neither
// names one — the default M routing assumes is a guess, not a size.
func dispatchIssueSize(workspaceRoot string, runtime *state.RuntimeState, item types.BoardItem, decision routing.Decision) string {
	if decision.SizeSource != routing.SizeSourceDefault {
		if size := sizeGate.NormalizeSize(decision.EffectiveSize); size != "" {
			return size
		}
	}
	assessment := execution.LoadPlannerAssessment(workspaceRoot, stageWorkspace(runtime, workspaceRoot), item.Repo, item.Number)
	return sizeGate.NormalizeSize(PlannerSizeFromAssessment(assessment.SizeLabel, assessment.Score))
}

// capacityGateOutcome is what enforceIssueCapacity hands back to the
// dispatch loop: either a refusal already booked, or the model and render to
// dispatch (unchanged unless a soft-route moved the stage to a fallback).
type capacityGateOutcome struct {
	refused       bool
	workRecovered bool
	model         string
	skillData     *skillrender.Result
}

// enforceIssueCapacity is the scheduler's dispatch-time capacity check
// (#1655, ADR 023 Q9). It judges the issue's size against the cap the ADR 023
// capacity table gives the window of the model this stage is about to
// dispatch on. Every stage is checked against its own resolved model, so the
// run as a whole is held to its smallest-window stage model.
//
// An over-capacity issue is refused before spawn and classified
// context_window_exceeded, with a recovery of decompose — or of human
// decomposition when the issue is already a capacity-forced child, so
// decomposition never goes a second level. context_window_exceeded parks, so
// nothing re-evaluates the refusal in a loop. Under
// pipeline.size_gate.routes.reject_action: soft-route the stage moves instead
// to the first capacity_fallback_models entry whose window admits the size
// (and, for a local OpenCode endpoint, is ready to serve it), re-rendered for
// that model. An unknown window or size applies no cap and logs one line.
func (s *Scheduler) enforceIssueCapacity(
	ctx context.Context,
	item types.BoardItem,
	runtime *state.RuntimeState,
	workspaceRoot string,
	stage state.PipelineStage,
	tracer *trace.Writer,
	adapterName string,
	model string,
	skillData *skillrender.Result,
	decision routing.Decision,
) capacityGateOutcome {
	out := capacityGateOutcome{model: model, skillData: skillData}
	cfg := sizeGate.LoadGateConfigFromYAML(filepath.Join(workspaceRoot, ".nightgauge", "config.yaml"))
	if !cfg.CapacityEnabled {
		return out
	}

	var fallbacks []sizeGate.CapacityCandidate
	if cfg.SoftRoute {
		for _, m := range cfg.CapacityFallbackModels {
			if m == model {
				continue
			}
			if adapterName == "opencode" {
				if verdict := resolveOpenCodeReadiness(workspaceRoot, m); !verdict.Ready {
					log.Printf("#%d: stage %s capacity fallback %s skipped: %s", item.Number, stage, m, verdict.Reason)
					continue
				}
			}
			fallbacks = append(fallbacks, sizeGate.CapacityCandidate{
				Model: m, Window: DispatchContextWindow(ctx, workspaceRoot, adapterName, m),
			})
		}
	}

	result := sizeGate.NewGateEvaluator(sizeGate.GateConfig{
		CapacityEnabled:        true,
		SoftRoute:              cfg.SoftRoute,
		CapacityFallbackModels: cfg.CapacityFallbackModels,
	}).WithLogger(func(format string, args ...any) {
		log.Printf("#%d: stage %s (model %s) %s", item.Number, stage, model, fmt.Sprintf(format, args...))
	}).EvaluateIssue(sizeGate.GateInput{
		Size:     dispatchIssueSize(workspaceRoot, runtime, item, decision),
		Body:     runtime.Snapshot().Body,
		Capacity: &sizeGate.CapacityInput{Window: skillData.ContextWindow, Fallbacks: fallbacks},
	})

	if result.Allowed && result.RoutedModel != "" {
		rerouted, err := skillrender.Render(skillrender.Options{
			Stage:       string(stage),
			Model:       result.RoutedModel,
			Adapter:     adapterName,
			SkillsRoots: skillrender.DefaultRoots(workspaceRoot),
			Warn:        func(msg string) { log.Printf("#%d: %s", item.Number, msg) },
		})
		if err == nil {
			for _, c := range fallbacks {
				if c.Model == result.RoutedModel && rerouted.ContextWindow <= 0 {
					rerouted.ContextWindow = c.Window
				}
			}
			out.model, out.skillData = result.RoutedModel, rerouted
			return out
		}
		result.Allowed = false
		result.Reason = fmt.Sprintf("%s; the soft-route fallback %s did not render: %v",
			result.Capacity.Reason, result.RoutedModel, err)
	}
	if result.Allowed {
		return out
	}

	reason := fmt.Sprintf("context_window_exceeded: stage %s on %s: %s", stage, model, result.Reason)
	_, out.workRecovered = s.refusePreDispatch(item, runtime, workspaceRoot, stage, tracer, "capacity", reason)
	out.refused = true
	return out
}
