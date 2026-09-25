package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
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

// capacitySizeSensitive are the stages whose context grows with the issue's
// size: planning reads the codebase the issue touches, feature-dev writes the
// change, feature-validate reviews it. issue-pickup, pr-create and pr-merge
// carry a bounded payload whatever the size, so the capacity table does not
// cap their models.
var capacitySizeSensitive = map[state.PipelineStage]bool{
	state.StageFeaturePlanning: true,
	state.StageFeatureDev:      true,
	state.StageFeatureValidate: true,
}

// capacityRefusalPhrase appears in every capacity refusal's reason; it is how
// ParkedRemediation tells a capacity refusal from a provider overflow.
const capacityRefusalPhrase = "exceeds the capacity cap"

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

// capacityDispatch is everything the dispatch-time capacity check reads.
type capacityDispatch struct {
	item          types.BoardItem
	runtime       *state.RuntimeState
	workspaceRoot string
	stage         state.PipelineStage
	// remaining are the stages still ahead of stage in this run, in order.
	remaining      []state.PipelineStage
	tracer         *trace.Writer
	adapterName    string
	model          string
	skillData      *skillrender.Result
	decision       routing.Decision
	predictedModel string
	modelFloors    map[string]string
	jobClass       string
	// pinnedModel is a remote run request's model while it is in force
	// (#1656). Every stage then runs it, so the windows ahead are the pinned
	// model's, and a soft-route to another model is refused instead.
	pinnedModel string
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
// (#1655, ADR 023 Q9). The cap binds only the size-sensitive stages
// (capacitySizeSensitive), and it is judged at every dispatch, of any stage,
// against the smallest known window among this stage (when size-sensitive)
// and the size-sensitive stages still ahead, each at the model it resolves
// to now. So a size known before planning (a label, a board field, the
// foundation override) is refused at the run's first stage, before anything
// is spent; a size known only from planning-{N}.json is judged from the next
// dispatch on, which is always a size-sensitive stage's. A stage with no
// size-sensitive stage at or after it is never checked.
//
// An over-capacity issue is refused before spawn and classified
// context_window_exceeded, with a recovery of decompose — or of human
// decomposition when the issue is already a capacity-forced child, so
// decomposition never goes a second level. context_window_exceeded parks, so
// nothing re-evaluates the refusal in a loop. Under
// pipeline.size_gate.routes.reject_action: soft-route the binding stage moves
// instead to the first capacity_fallback_models entry whose window admits
// the size (and, for a local OpenCode endpoint, is ready to serve it): now,
// when the binding stage is this one; at its own dispatch, when it is a later
// one. An unknown window or size applies no cap and logs one line.
func (s *Scheduler) enforceIssueCapacity(ctx context.Context, d capacityDispatch) capacityGateOutcome {
	out := capacityGateOutcome{model: d.model, skillData: d.skillData}
	var ahead []state.PipelineStage
	for _, st := range d.remaining {
		if capacitySizeSensitive[st] {
			ahead = append(ahead, st)
		}
	}
	current := capacitySizeSensitive[d.stage]
	if !current && len(ahead) == 0 {
		return out
	}
	cfg := sizeGate.LoadGateConfigFromYAML(filepath.Join(d.workspaceRoot, ".nightgauge", "config.yaml"))
	if !cfg.CapacityEnabled {
		return out
	}
	logf := func(format string, args ...any) {
		log.Printf("#%d: stage %s %s", d.item.Number, d.stage, fmt.Sprintf(format, args...))
	}

	size := dispatchIssueSize(d.workspaceRoot, d.runtime, d.item, d.decision)
	if size == "" {
		if current {
			logf("capacity: size unknown — no capacity cap applied")
		}
		return out
	}
	child := sizeGate.IsCapacityDecomposedChild(d.runtime.Snapshot().Body)

	// The binding window: the smallest known one among this stage (when
	// size-sensitive) and the size-sensitive stages ahead.
	bindStage, bindModel, bindWindow := state.PipelineStage(""), "", 0
	consider := func(st state.PipelineStage, model string, window int) {
		if window > 0 && (bindWindow == 0 || window < bindWindow) {
			bindStage, bindModel, bindWindow = st, model, window
		}
	}
	if current {
		consider(d.stage, d.model, d.skillData.ContextWindow)
	}
	for _, st := range ahead {
		if st == d.stage {
			continue
		}
		model := d.pinnedModel
		adapter := d.adapterName
		if model == "" {
			model = s.resolveDispatchModel(st, d.item.Number, d.workspaceRoot,
				routedStageModel(st, d.predictedModel, d.decision), d.modelFloors, d.jobClass)
			adapter = capacityStageAdapter(d.workspaceRoot, st, d.adapterName)
		}
		consider(st, model, DispatchContextWindow(ctx, d.workspaceRoot, adapter, model))
	}

	res := sizeGate.CheckCapacity(size, bindWindow, child)
	if res.Allowed {
		if res.Applied {
			logf("capacity: size %s within the cap %s of the smallest size-sensitive window (%s on %s, %d tokens)",
				size, res.MaxSize, bindStage, bindModel, bindWindow)
		} else {
			logf("%s", res.Note)
		}
		return out
	}

	pinNote := ""
	if cfg.SoftRoute && d.pinnedModel != "" {
		pinNote = fmt.Sprintf("; the run is pinned to %s by a remote run request, so it is not soft-routed to another model", d.pinnedModel)
		logf("capacity: size %s exceeds the cap %s of %s on %s (%d tokens)%s",
			size, res.MaxSize, bindStage, bindModel, bindWindow, pinNote)
	} else if cfg.SoftRoute {
		fallbacks := s.capacityFallbacks(ctx, d, cfg.CapacityFallbackModels, bindModel)
		if alt, ok := sizeGate.FirstAdmittingFallback(size, fallbacks); ok {
			if bindStage != d.stage {
				logf("capacity: size %s exceeds the cap %s of %s on %s (%d tokens) — %s will soft-route to %s (window %d) at its own dispatch",
					size, res.MaxSize, bindStage, bindModel, bindWindow, bindStage, alt.Model, alt.Window)
				return out
			}
			rerouted, err := skillrender.Render(skillrender.Options{
				Stage:       string(d.stage),
				Model:       alt.Model,
				Adapter:     d.adapterName,
				SkillsRoots: skillrender.DefaultRoots(d.workspaceRoot),
				Warn:        func(msg string) { log.Printf("#%d: %s", d.item.Number, msg) },
			})
			if err == nil {
				if rerouted.ContextWindow <= 0 {
					rerouted.ContextWindow = alt.Window
				}
				logf("capacity: size %s exceeds the cap %s for %s's %d-token window — soft-routed to %s (window %d)",
					size, res.MaxSize, bindModel, bindWindow, alt.Model, alt.Window)
				out.model, out.skillData = alt.Model, rerouted
				return out
			}
			logf("capacity: the soft-route fallback %s did not render: %v", alt.Model, err)
		} else {
			logf("capacity: no capacity_fallback_models entry admits size %s", size)
		}
	}

	reason := fmt.Sprintf("context_window_exceeded: capacity: refused at stage %s: stage %s on %s: %s%s",
		d.stage, bindStage, bindModel, res.Reason, pinNote)
	_, out.workRecovered = s.refusePreDispatch(d.item, d.runtime, d.workspaceRoot, d.stage, d.tracer, "capacity", reason)
	// The kind travels as a structured gate result as well as in the prose:
	// the CLI autonomous wrapper re-derives the kind from the failed stage
	// (ResolveTerminalKind), and the terminal-kind table deliberately never
	// classifies context_window_exceeded from pipeline prose.
	d.runtime.AppendStageGateResult(d.stage, state.StageGateResult{
		GateName: "capacity", Passed: false, Kind: "fail", Reason: reason,
		Timestamp: time.Now().UTC().Format(time.RFC3339), TerminalKind: TerminalKindContextWindowExceeded,
	})
	out.refused = true
	return out
}

// capacityFallbacks resolves the soft-route fallback models' windows, in
// order, skipping the model that just failed and any local OpenCode endpoint
// that is not ready. It runs only after the check has rejected. A fallback
// whose window does not resolve is logged: it can never be chosen, which is
// most often a typo in capacity_fallback_models.
func (s *Scheduler) capacityFallbacks(ctx context.Context, d capacityDispatch, models []string, failed string) []sizeGate.CapacityCandidate {
	var out []sizeGate.CapacityCandidate
	for _, m := range models {
		if m == failed {
			continue
		}
		if d.adapterName == "opencode" {
			if verdict := resolveOpenCodeReadiness(d.workspaceRoot, m); !verdict.Ready {
				log.Printf("#%d: stage %s capacity fallback %s skipped: %s", d.item.Number, d.stage, m, verdict.Reason)
				continue
			}
		}
		window := DispatchContextWindow(ctx, d.workspaceRoot, d.adapterName, m)
		if window <= 0 {
			log.Printf("#%d: stage %s capacity fallback %s skipped: its context window did not resolve on adapter %q",
				d.item.Number, d.stage, m, d.adapterName)
			continue
		}
		out = append(out, sizeGate.CapacityCandidate{Model: m, Window: window})
	}
	return out
}

// capacityStageAdapter is the adapter a later stage will dispatch on: its
// configured stage adapter when one resolves, else the one dispatching now.
func capacityStageAdapter(workspaceRoot string, stage state.PipelineStage, current string) string {
	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		cfg = nil
	}
	if a := strings.TrimSpace(config.ResolveStageAdapter(cfg, string(stage), os.Getenv).Adapter); a != "" {
		return a
	}
	return current
}
